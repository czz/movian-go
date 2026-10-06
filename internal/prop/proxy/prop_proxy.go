// Package proxy implements the STPP (Showtime Transfer Protocol for
// Properties) client — a canonical port of src/prop/prop_proxy.c.
//
// A ProxyConnection holds a websocket to a remote STPP server. Remote
// properties appear locally as PROP_PROXY props: value changes,
// subscriptions, select/move/want-more operations and ext events are
// forwarded over the wire; remote notifications update local props and
// are delivered to local subscribers via propcore.Subscription.Notify.
//
// Locking: C guards all wire state with the global prop_mutex. Go splits
// this — ppc.mu guards ppc's own fields and metadata maps, while
// propcore Prop/Subscription fields keep their own locks. All calls into
// propcore that may re-enter the ppc (Destroy0 → ProxyDestroy) are made
// without holding ppc.mu.
package proxy

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/czz/movian-go/internal/asyncio"
	backendcore "github.com/czz/movian-go/internal/backend/core"
	"github.com/czz/movian-go/internal/event"
	imagepkg "github.com/czz/movian-go/internal/image"
	"github.com/czz/movian-go/internal/misc"
	netcore "github.com/czz/movian-go/internal/networking/core"
	propcore "github.com/czz/movian-go/internal/prop"
)

// STPP protocol constants (C: src/api/stpp.h)
const (
	MGPPVersion = 3

	MGPPCmdHello          = 0
	MGPPCmdSubscribe      = 1
	MGPPCmdUnsubscribe    = 2
	MGPPCmdSet            = 3
	MGPPCmdNotify         = 4
	MGPPCmdEvent          = 5
	MGPPCmdReqMove        = 6
	MGPPCmdWantMoreChilds = 7
	MGPPCmdSelect         = 8
	MGPPCmdImageLoad      = 9
	MGPPCmdImageReply     = 10
	MGPPCmdImageFail      = 11
	MGPPCmdImageCancel    = 12

	// Notify types (first byte in STPP_CMD_NOTIFY message)
	MGPPSetVoid           = 0
	MGPPSetInt            = 1
	MGPPSetFloat          = 2
	MGPPSetString         = 3 // First byte is strtype
	MGPPSetURI            = 4
	MGPPSetDir            = 5
	MGPPAddChilds         = 6
	MGPPAddChildsBefore   = 7
	MGPPDelChild          = 8
	MGPPMoveChild         = 9
	MGPPSelectChild       = 10
	MGPPAddChildSelected  = 11
	MGPPValueProp         = 12
	MGPPToggleInt         = 13
	MGPPHaveMoreChildsYes = 14
	MGPPHaveMoreChildsNo  = 15
)

// proxyDeps — dependency seams + gconf.running_instance, grouped.
// Installed at init time via the Set* functions (C: implicit globals).
var proxyDeps = struct {
	pm              *propcore.PropManager
	em              *event.EventManager
	runningInstance []byte // C: gconf.running_instance (16 bytes)
}{}

// SetPropManager installs the prop manager used to create proxy props.
func SetPropManager(pm *propcore.PropManager) { proxyDeps.pm = pm }

// SetEventManager installs the event manager used for action_code2str.
func SetEventManager(em *event.EventManager) { proxyDeps.em = em }

// SetRunningInstance installs the 16-byte instance id used to reject
// connections to ourselves (C: gconf.running_instance).
func SetRunningInstance(id []byte) { proxyDeps.runningInstance = id }

func wr32LE(b []byte, v uint32) { binary.LittleEndian.PutUint32(b, v) }
func wr16LE(b []byte, v uint16) { binary.LittleEndian.PutUint16(b, v) }
func rd32LE(b []byte) uint32    { return binary.LittleEndian.Uint32(b) }
func rd16LE(b []byte) uint16    { return binary.LittleEndian.Uint16(b) }
func rd16BE(b []byte) uint16    { return binary.BigEndian.Uint16(b) }

// proxyMeta is the Go-side bookkeeping for a PROP_PROXY prop.
// C stores these as fields on prop_t (hp_proxy_pfx, hp_owner_sub,
// hp_owned list, PROP_PROXY_OWNED_BY_PROP / FOLLOW_SYMLINK flags);
// Go keeps them in a per-connection map keyed by *Prop.
type proxyMeta struct {
	pfx      []string               // C: hp_proxy_pfx (nil-terminated strvec)
	ownerSub *propcore.Subscription // C: hp_owner_sub
	owner    *propcore.Prop         // C: PROP_PROXY_OWNED_BY_PROP → owner
	owned    []*propcore.Prop       // C: hp_owned list (on the owner)
}

// imageReq tracks an in-flight STPP_CMD_IMAGE_LOAD request.
// C: prop_proxy_imagereq_t
type imageReq struct {
	id    uint32
	ppc   *ProxyConnection // C: ppi_ppc
	image *imagepkg.Image
	done  bool
	err   string // C: ppi_errbuf[256] — written under ppc.mu
}

// wsState mirrors C's websocket_state_t — accumulates fragmented frames.
type wsState struct {
	packet []byte // C: ws.packet / packet_size
	opcode int    // C: ws.opcode (opcode of first fragment)
}

// ProxyConnection is C's prop_proxy_connection_t.
type ProxyConnection struct {
	refCount int32 // C: ppc_refcount (atomic)

	mu       sync.Mutex // C: prop_mutex scope for wire state
	conn     *asyncio.AsyncIOFD
	aio      *asyncio.AsyncIO // C: global asyncio loop
	url      string
	hostname string
	port     int

	errorProp *propcore.Prop // C: ppc_error (child "error" of caller's status prop)
	closeProp *propcore.Prop // C: ppc_closepage (child "close" of caller's status prop)
	root      *propcore.Prop // C: ppc_root (global proxy prop, id 0)

	outq      misc.HtsbufQueue // C: ppc_outq
	wsOpen    int              // C: ppc_websocket_open (0=http, 1=upgraded, 2=hello-ok)
	httpState int              // C: ppc_http_state
	ws        wsState          // C: ppc_ws

	subs     []*propcore.Subscription                             // C: ppc_subs (LIST)
	subTally int                                                  // C: ppc_subscription_tally
	subTrees map[*propcore.Subscription]map[uint32]*propcore.Prop // C: s->hps_prop_tree
	meta     map[*propcore.Prop]*proxyMeta                        // per-prop bookkeeping

	imageCond  *sync.Cond   // C: ppc_image_cond (bound to ppc.mu)
	imageReqs  []*imageReq  // C: ppc_image_requests
	imageReqID atomic.Int32 // C: ppc_image_req_id_generator (atomic)
}

// NewProxyConnection is C's prop_proxy_connect (prop_proxy.c:844-869).
// url is the remote prop tree URL; status is a caller-owned prop under
// which "error" and "close" children are created.
func NewProxyConnection(aio *asyncio.AsyncIO, url string, status *propcore.Prop) *ProxyConnection {
	ppc := &ProxyConnection{refCount: 1, aio: aio}
	ppc.imageCond = sync.NewCond(&ppc.mu)
	ppc.outq.HtsbufQueueSetup(0)
	ppc.subTrees = make(map[*propcore.Subscription]map[uint32]*propcore.Prop)
	ppc.meta = make(map[*propcore.Prop]*proxyMeta)

	hostname := make([]byte, 256)
	var port int
	misc.UrlSplit(nil, 0, nil, 0, hostname, len(hostname), &port, nil, 0, url)

	// C: if(ppc->ppc_port == -1) ppc->ppc_port = 42000;
	if port == -1 {
		port = 42000
	}
	ppc.port = port
	ppc.url = url // C: strdup
	// hostname is a C string in a fixed buffer — truncate at NUL
	ppc.hostname = misc.CStr(hostname)

	if proxyDeps.pm != nil {
		// C: ppc->ppc_error = prop_create_r(status, "error");
		ppc.errorProp = proxyDeps.pm.CreateEx(status, "error", nil, false, false)
		// C: ppc->ppc_closepage = prop_create_r(status, "close");
		ppc.closeProp = proxyDeps.pm.CreateEx(status, "close", nil, false, false)
	}

	ppc.aio.DNSLookupHost(ppc.hostname, ppcConnect, ppc.Retain())

	// C: ppc->ppc_root = prop_proxy_make(ppc, 0 /* global */, NULL, NULL, NULL);
	ppc.mu.Lock()
	ppc.root = ppc.propProxyMake(0, nil, nil, nil)
	ppc.mu.Unlock()
	return ppc
}

// Retain is C's prop_proxy_retain.
func (ppc *ProxyConnection) Retain() *ProxyConnection {
	atomic.AddInt32(&ppc.refCount, 1)
	return ppc
}

// Release is C's prop_proxy_release (prop_proxy.c:138-155).
func (ppc *ProxyConnection) Release() {
	if atomic.AddInt32(&ppc.refCount, -1) != 0 {
		return
	}
	if ppc.conn != nil {
		ppc.aio.RunTask(ppcDelFD, ppc.conn)
	}
	ppc.ws.packet = nil // C: free(ppc->ppc_ws.packet)
	// C: prop_ref_dec(ppc->ppc_error) / ppc_closepage — Go GC handles
}

// ppcDelFD is C's ppc_del_fd (asyncio task).
func ppcDelFD(aux any) {
	asyncio.DelFD(aux.(*asyncio.AsyncIOFD))
}

// Close is C's prop_proxy_close.
func (ppc *ProxyConnection) Close() {
	if proxyDeps.pm != nil {
		proxyDeps.pm.Destroy(ppc.root)
	}
	ppc.Release()
}

// GetRoot is C's prop_proxy_get_root.
func (ppc *ProxyConnection) GetRoot() *propcore.Prop {
	if ppc.root == nil {
		return nil
	}
	return ppc.root.Ref()
}

// disconnect is C's ppc_disconnect (prop_proxy.c:113-128).
// The C `reconnect` parameter is unused — once disconnected the
// connection stays dead.
func (ppc *ProxyConnection) disconnect(reconnect bool) {
	if ppc.conn != nil {
		asyncio.DelFD(ppc.conn)
		ppc.conn = nil
	}
	ppc.wsOpen = 0

	ppc.mu.Lock()
	defer ppc.mu.Unlock()
	ppc.conn = nil
	for _, ppi := range ppc.imageReqs {
		ppi.done = true
		ppi.err = "Connection lost"
	}
	ppc.imageCond.Broadcast()
}

// sendq is C's ppc_sendq — flushes ppc_outq to the socket.
// Must be called with ppc.mu held.
func (ppc *ProxyConnection) sendq() {
	if ppc.conn != nil {
		ppc.conn.Sendq(&ppc.outq, 0)
	}
}

// ppcSend is C's ppc_send (asyncio task — drains outq when open).
func ppcSend(aux any) {
	ppc := aux.(*ProxyConnection)
	ppc.mu.Lock()
	if ppc.wsOpen == 2 {
		ppc.sendq()
	}
	ppc.mu.Unlock()
	ppc.Release()
}

// sendData is C's prop_proxy_send_data — frames data as a binary ws
// message on ppc_outq and schedules a flush.
// Must be called with ppc.mu held.
func (ppc *ProxyConnection) sendData(data []byte) {
	if ppc.outq.Len() == 0 {
		ppc.aio.RunTask(ppcSend, ppc.Retain())
	}
	wsAppendHdr(&ppc.outq, 2, len(data))
	ppc.outq.Append(data)
}

// sendQueue is C's prop_proxy_send_queue — same for a pre-built queue.
// Must be called with ppc.mu held.
func (ppc *ProxyConnection) sendQueue(q *misc.HtsbufQueue) {
	if ppc.outq.Len() == 0 {
		ppc.aio.RunTask(ppcSend, ppc.Retain())
	}
	wsAppendHdr(&ppc.outq, 2, q.Len())
	ppc.outq.AppendQ(q)
}

// ppcConnect is C's ppc_connect — DNS completion → TCP connect.
func ppcConnect(aux any, status asyncio.DNSStatus, data any) {
	ppc := aux.(*ProxyConnection)
	var na *netcore.NetAddr
	switch status {
	case asyncio.DNSStatusCompleted:
		na, _ = data.(*netcore.NetAddr)
	case asyncio.DNSStatusFailed:
		if s, ok := data.(string); ok {
			ppc.setError(s)
		}
		ppc.Release()
		return
	default:
		panic("unreachable") // C: abort()
	}
	if na == nil {
		ppc.setError("DNS resolution failed")
		ppc.Release()
		return
	}
	na.Port = uint16(ppc.port)
	ppc.conn = ppc.aio.Connect("mgppclient", na,
		ppcConnected, ppcInput, ppc, 3000, nil, "")
	ppc.Release()
}

// ppcConnected is C's ppc_connected — sends the websocket upgrade GET.
func ppcConnected(aux any, err string) {
	ppc := aux.(*ProxyConnection)
	if err != "" {
		ppc.setError(err)
		ppc.disconnect(false)
		return
	}
	req := "GET /api/mgpp HTTP/1.1\r\n" +
		"Connection: Upgrade\r\n" +
		"Upgrade: websocket\r\n" +
		"Sec-WebSocket-Key: 1\r\n" +
		"\r\n"
	ppc.conn.Send([]byte(req), len(req), 0)
}

// setError is C's prop_set_string(ppc->ppc_error, ...) — nil-safe.
func (ppc *ProxyConnection) setError(s string) {
	if ppc.errorProp != nil {
		ppc.errorProp.SetString(s)
	}
}

// sendHello is C's ppc_send_hello — binary frame [HELLO, VERSION, flags].
func (ppc *ProxyConnection) sendHello() {
	msg := []byte{MGPPCmdHello, MGPPVersion, 0}
	var q misc.HtsbufQueue
	q.HtsbufQueueSetup(0)
	wsAppendHdr(&q, 2, len(msg))
	q.Append(msg)
	if ppc.conn != nil {
		ppc.conn.Sendq(&q, 0)
	}
}

// httpReadLine is a port of http_read_line (http.c:247-270).
// Returns (line, true) on success, ("", false) if incomplete, or
// ("", false) with err set on overflow — modeled as (line, ok, fatal).
func httpReadLine(q *misc.HtsbufQueue) (string, bool, bool) {
	l := q.Find(0xa)
	if l == -1 {
		return "", false, false
	}
	if l >= 10000 {
		return "", false, true // C: return (void *)-1
	}
	buf := make([]byte, l)
	q.Read(buf, l)
	for l > 0 && buf[l-1] < 32 {
		l--
	}
	q.Drop(1) // drop the \n
	return string(buf[:l]), true, false
}

// ppcInput is C's ppc_input — HTTP upgrade handshake then ws frames.
func ppcInput(opaque any, q *misc.HtsbufQueue) {
	ppc := opaque.(*ProxyConnection)
	for ppc.wsOpen == 0 {
		line, ok, fatal := httpReadLine(q)
		if fatal {
			ppc.setError("Read error")
			ppc.disconnect(false)
			return
		}
		if !ok {
			return // not a full line yet
		}
		if ppc.httpState == 0 {
			if !strings.HasPrefix(line, "HTTP/1.1 101 ") {
				ppc.setError("No websocket endpoint")
				ppc.disconnect(false)
				return
			}
		}
		ppc.httpState++
		if line == "" {
			ppc.wsOpen = 1
			ppc.sendHello()
		}
	}
	if wsParse(q, ppc.wsInput, ppc, &ppc.ws) != 0 {
		ppc.setError("Websocket protocol error")
		ppc.disconnect(false)
	}
}

// wsAppendHdr is C's websocket_append_hdr (websocket.c).
// mask is always nil here (client is unmasked in this codebase).
func wsAppendHdr(q *misc.HtsbufQueue, opcode, length int) {
	var hdr [14]byte
	hlen := 0
	hdr[0] = 0x80 | byte(opcode&0xf)
	if length <= 125 {
		hdr[1] = byte(length)
		hlen = 2
	} else if length < 65536 {
		hdr[1] = 126
		hdr[2] = byte(length >> 8)
		hdr[3] = byte(length)
		hlen = 4
	} else {
		hdr[1] = 127
		binary.BigEndian.PutUint64(hdr[2:], uint64(length))
		hlen = 10
	}
	q.Append(hdr[:hlen])
}

// wsParse is a port of websocket_parse (websocket.c) onto HtsbufQueue.
// Returns 0 on success/need-more-data, 1 on fatal error.
func wsParse(q *misc.HtsbufQueue, cb func(opaque any, opcode int, data []byte) int,
	opaque any, ws *wsState) int {
	var hdr [14]byte
	for {
		p := q.Peek(hdr[:], 14)
		if p < 2 {
			return 0
		}
		fin := hdr[0] & 0x80
		opcode := int(hdr[0] & 0xf)
		length := int64(hdr[1] & 0x7f)
		hoff := 2
		if length == 126 {
			if p < 4 {
				return 0
			}
			length = int64(hdr[2])<<8 | int64(hdr[3])
			hoff = 4
		} else if length == 127 {
			if p < 10 {
				return 0
			}
			length = int64(binary.BigEndian.Uint64(hdr[2:]))
			hoff = 10
		}

		var m []byte
		if hdr[1]&0x80 != 0 {
			if p < hoff+4 {
				return 0
			}
			m = hdr[hoff : hoff+4]
			hoff += 4
		}

		if int64(q.Len()) < int64(hoff)+length {
			return 0
		}
		q.Drop(hoff)

		if opcode&0x8 != 0 {
			// Ctrl frame — delivered standalone
			pb := make([]byte, length)
			q.Read(pb, int(length))
			if m != nil {
				for i := range pb {
					pb[i] ^= m[i&3]
				}
			}
			if err := cb(opaque, opcode, pb); err != 0 {
				return 1
			}
			continue
		}

		// Data frame — accumulate fragments into ws.packet
		d := make([]byte, length)
		q.Read(d, int(length))
		if m != nil {
			for i := range d {
				d[i] ^= m[i&3]
			}
		}
		if opcode != 0 {
			ws.opcode = opcode
		}
		ws.packet = append(ws.packet, d...)
		if fin == 0 {
			continue
		}
		err := cb(opaque, ws.opcode, ws.packet)
		ws.packet = nil // C: ws->packet_size = 0
		if err != 0 {
			return 1
		}
	}
}

// wsInput is C's ppc_ws_input — dispatches complete ws frames.
// Signature matches the wsParse callback (opaque is the ppc itself).
func (ppc *ProxyConnection) wsInput(opaque any, opcode int, data []byte) int {
	switch opcode {
	case 2: // binary — STPP command
		if len(data) < 1 {
			return 1
		}
		switch data[0] {
		case MGPPCmdNotify:
			ppc.wsInputNotify(data[1:])
			return 0
		case MGPPCmdHello:
			return ppc.wsInputHello(data[1:])
		case MGPPCmdImageReply:
			return ppc.wsInputImageReply(data[1:])
		case MGPPCmdImageFail:
			return ppc.wsInputImageFail(data[1:])
		default:
			return 1
		}
	case 9: // ping → pong
		ppc.sendPong(data)
		return 0
	case 8: // close
		ppc.wsClose(data)
		return 0
	}
	return 0
}

// wsInputHello is C's ppc_ws_input_hello.
func (ppc *ProxyConnection) wsInputHello(data []byte) int {
	if len(data) < 1 {
		return -1
	}
	if data[0] != MGPPVersion {
		ppc.setError(fmt.Sprintf("Incompatible version %d", data[0]))
		return -1
	}
	data = data[1:]
	if len(data) < 16 {
		return -1
	}
	if len(proxyDeps.runningInstance) >= 16 && string(data[:16]) == string(proxyDeps.runningInstance[:16]) {
		ppc.setError("Refusing connection to myself")
		return -1
	}
	// uint8_t flags = data[16]; (unused in C)
	ppc.mu.Lock()
	ppc.wsOpen = 2
	ppc.sendq()
	ppc.mu.Unlock()
	return 0
}

// sendPong is C's ppc_send_pong (opcode 10).
func (ppc *ProxyConnection) sendPong(data []byte) {
	var q misc.HtsbufQueue
	q.HtsbufQueueSetup(0)
	wsAppendHdr(&q, 10, len(data))
	q.Append(data)
	if ppc.conn != nil {
		ppc.conn.Sendq(&q, 0)
	}
}

// wsClose is C's ppc_close — handles the websocket close frame.
func (ppc *ProxyConnection) wsClose(data []byte) {
	msg := ""
	errcode := 1005 // No code present
	if len(data) > 2 {
		msg = string(data[2:])
	}
	if len(data) >= 2 {
		errcode = int(rd16BE(data))
	}
	if msg == "" {
		if errcode == 4000 {
			msg = "Restarting"
		} else {
			msg = "Remote exited"
		}
	}
	if errcode == 1001 {
		if ppc.closeProp != nil {
			ppc.closeProp.SetInt(1)
		}
	} else {
		ppc.setError(msg)
	}
	ppc.disconnect(errcode == 4000)
}

// wsInputImageReply is C's ppc_ws_input_image_reply.
func (ppc *ProxyConnection) wsInputImageReply(data []byte) int {
	if len(data) < 13 {
		return -1
	}
	id := rd32LE(data)
	ppc.mu.Lock()
	defer ppc.mu.Unlock()
	var ppi *imageReq
	for _, r := range ppc.imageReqs {
		if r.id == id {
			ppi = r
			break
		}
	}
	if ppi != nil && !ppi.done {
		// C: image_coded_alloc(&buf, len - 13, data[11]) + field fills
		im, buf := imagepkg.CodedAlloc(len(data)-13, imagepkg.CodedType(data[11]))
		if im != nil {
			im.Width = rd16LE(data[4:])
			im.Height = rd16LE(data[6:])
			im.Flags = rd16LE(data[8:])
			im.ColorPlanes = data[10]
			im.OriginCodedType = data[11]
			im.Orientation = data[12]
			copy(buf, data[13:])
			ppi.image = im
		}
		ppi.done = true
		ppc.imageCond.Broadcast()
	}
	return 0
}

// wsInputImageFail is C's ppc_ws_input_image_fail.
func (ppc *ProxyConnection) wsInputImageFail(data []byte) int {
	if len(data) < 4 {
		return -1
	}
	id := rd32LE(data)
	ppc.mu.Lock()
	defer ppc.mu.Unlock()
	var ppi *imageReq
	for _, r := range ppc.imageReqs {
		if r.id == id {
			ppi = r
			break
		}
	}
	if ppi != nil && !ppi.done {
		ppi.err = string(data[4:]) // C: snprintf("%s", data) — skips id
		ppi.done = true
		ppc.imageCond.Broadcast()
	}
	return 0
}

// wsInputNotify is C's ppc_ws_input_notify (prop_proxy.c:399-572) —
// the STPP_CMD_NOTIFY decoder. Runs the big setop switch and delivers
// notifications to the addressed subscription.
func (ppc *ProxyConnection) wsInputNotify(data []byte) {
	if len(data) < 5 {
		return
	}
	setop := int(data[0])
	subid := rd32LE(data[1:])

	ppc.mu.Lock()

	// C: LIST_FOREACH(s, &ppc->ppc_subs, hps_value_prop_link)
	//    if(s->hps_proxy_subid == subid) break; — "probably quite slow"
	var s *propcore.Subscription
	for _, x := range ppc.subs {
		if x.GetProxySubID() == subid {
			s = x
			break
		}
	}
	if s == nil {
		ppc.mu.Unlock()
		return
	}

	data = data[5:]

	// C performs prop_destroy0 calls and prop_courier_enqueue under
	// prop_mutex. Go must not call Destroy0 while holding ppc.mu
	// (Destroy0 → ProxyDestroy re-takes ppc.mu), so prop destruction and
	// the final notification are deferred until after unlock — preserving
	// C ordering (destroys first, notify last).
	var victims []*propcore.Prop
	var notifyFn func()
	notify := func(et propcore.EventType, prop *propcore.Prop, args ...any) {
		notifyFn = func() { s.Notify(et, prop, args...) }
	}

	switch setop {
	case MGPPSetString:
		if len(data) < 1 {
			break
		}
		victims = ppc.collectPropsOnSubLocked(s, victims)
		// C: n->hpn_rstring + hpn_rstrtype = data[0] → PROP_SET_RSTRING
		notify(propcore.EventSetRString, nil, string(data[1:]), propcore.StringType(data[0]))

	case MGPPSetVoid:
		victims = ppc.collectPropsOnSubLocked(s, victims)
		notify(propcore.EventSetVoid, nil)

	case MGPPSetDir:
		victims = ppc.collectPropsOnSubLocked(s, victims)
		notify(propcore.EventSetDir, nil)

	case MGPPSetInt:
		victims = ppc.collectPropsOnSubLocked(s, victims)
		v := 0
		if len(data) == 4 {
			v = int(int32(rd32LE(data)))
		}
		notify(propcore.EventSetInt, nil, v)

	case MGPPSetFloat:
		victims = ppc.collectPropsOnSubLocked(s, victims)
		var u uint32
		if len(data) == 4 {
			u = rd32LE(data)
		}
		notify(propcore.EventSetFloat, nil, math.Float32frombits(u))

	case MGPPAddChilds:
		if len(data)&3 != 0 {
			break
		}
		cnt := len(data) / 4
		vec := make([]*propcore.Prop, 0, cnt)
		for i := range cnt {
			p := ppc.propProxyMake(rd32LE(data[i*4:]), s, nil, nil)
			vec = append(vec, p)
		}
		notify(propcore.EventAddChildVector, nil, vec)

	case MGPPAddChildsBefore:
		if len(data)&3 != 0 || len(data) == 0 {
			break
		}
		cnt := len(data)/4 - 1
		vec := make([]*propcore.Prop, 0, cnt)
		for i := range cnt {
			p := ppc.propProxyMake(rd32LE(data[4+i*4:]), s, nil, nil)
			vec = append(vec, p)
		}
		before := ppc.findPropOnSubLocked(s, rd32LE(data))
		// C: hpn_propv + hpn_prop_extra — Go convention puts the vec in
		// args and the before prop as the last arg.
		notify(propcore.EventAddChildVectorBefore, nil, vec, before)

	case MGPPDelChild:
		if len(data) != 4 {
			break
		}
		p := ppc.findPropOnSubLocked(s, rd32LE(data))
		// C: n->hpn_prop = prop_ref_inc(p); prop_destroy0(p) — the
		// child is destroyed (deferred to after ppc.mu unlock; Go
		// convention: prop = child, args[0] = parent).
		if p != nil {
			victims = append(victims, p)
		}
		notify(propcore.EventDelChild, p, s.GetValueProp())

	case MGPPMoveChild:
		if len(data) < 4 {
			break
		}
		p := ppc.findPropOnSubLocked(s, rd32LE(data))
		var before *propcore.Prop
		if len(data) == 8 {
			before = ppc.findPropOnSubLocked(s, rd32LE(data[4:]))
		}
		// C: PROP_MOVE_CHILD with hpn_prop + hpn_prop_extra — Go
		// convention: prop = moved child, args = (parent, before).
		notify(propcore.EventMoveChild, p, s.GetValueProp(), before)

	case MGPPSelectChild:
		if len(data) != 4 {
			break
		}
		p := ppc.findPropOnSubLocked(s, rd32LE(data))
		// Track selection on the subscribed dir so local receivers
		// computing PROP_ADD_SELECTED flags see consistent state.
		if vp := s.GetValueProp(); vp != nil {
			vp.SetSelectedChild(p)
		}
		notify(propcore.EventSelectChild, p, s.GetValueProp())

	case MGPPAddChildSelected:
		if len(data) != 4 {
			break
		}
		p := ppc.propProxyMake(rd32LE(data), s, nil, nil)
		if vp := s.GetValueProp(); vp != nil {
			vp.SetSelectedChild(p)
		}
		// C: PROP_ADD_CHILD with hpn_flags = PROP_ADD_SELECTED — Go
		// receivers recompute the flag from parent.selectedChild, so the
		// selectedChild assignment above carries the flag semantics.
		notify(propcore.EventAddChild, p, s.GetValueProp())

	case MGPPValueProp:
		if len(data) != 4 {
			break
		}
		// C: if(s->hps_value_prop != NULL) prop_destroy0(s->hps_value_prop);
		if old := s.GetValueProp(); old != nil {
			victims = append(victims, old)
			s.SetValueProp(nil)
		}
		p := ppc.propProxyMake(rd32LE(data), nil, nil, nil)
		s.SetValueProp(p)
		notify(propcore.EventValueProp, p)

	case MGPPHaveMoreChildsYes:
		notify(propcore.EventHaveMoreChildsYes, nil)

	case MGPPHaveMoreChildsNo:
		notify(propcore.EventHaveMoreChildsNo, nil)

	default:
		fmt.Printf("WARNING: MGPP input can't handle op %d\n", setop)
	}
	ppc.mu.Unlock()

	// Deferred destruction + notify (C order: destroy → enqueue notify).
	if proxyDeps.pm != nil {
		for _, p := range victims {
			proxyDeps.pm.Destroy0(p)
		}
	}
	if notifyFn != nil {
		notifyFn()
	}
}

// propProxyMake is C's prop_proxy_make (prop_proxy.c:152-228).
// Creates a PROP_PROXY prop, registering it on the sub's prop tree
// (s != nil) or the owner's owned list (owner != nil, dedup by pfx).
// Must be called with ppc.mu held.
func (ppc *ProxyConnection) propProxyMake(id uint32, s *propcore.Subscription,
	owner *propcore.Prop, pfx []string) *propcore.Prop {
	if owner != nil {
		// C: LIST_FOREACH(p, &owner->hp_owned, hp_owned_prop_link) — dedup
		// on identical pfx, returns existing prop.
		if om := ppc.meta[owner]; om != nil {
			for _, p := range om.owned {
				if pm := ppc.meta[p]; pm != nil && strvecEq(pm.pfx, pfx) {
					return p
				}
			}
		}
	}

	// C: pool_get(prop_pool) + field setup — a parentless prop.
	p := proxyDeps.pm.CreateEx(nil, "", nil, false, false)
	p.SetProxyID(id)
	p.SetProxyConnection(ppc)
	p.SetPropType(propcore.PropTypeProxy)

	m := &proxyMeta{pfx: pfx, ownerSub: s, owner: owner}
	ppc.meta[p] = m

	if s != nil {
		// C: RB_INSERT_SORTED_NFL(&s->hps_prop_tree, ...) — collision aborts
		tree := ppc.subTrees[s]
		if tree == nil {
			tree = make(map[uint32]*propcore.Prop)
			ppc.subTrees[s] = tree
		}
		if _, dup := tree[id]; dup {
			fmt.Printf("HELP Unable to insert node %d on sub %d, collision detected\n",
				id, s.GetProxySubID())
		}
		tree[id] = p
	}
	if owner != nil {
		// C: LIST_INSERT_HEAD(&owner->hp_owned, ...) + flag inherit
		if om := ppc.meta[owner]; om != nil {
			om.owned = slices.Insert(om.owned, 0, p)
		}
		// C: if(owner->hp_flags & PROP_PROXY_FOLLOW_SYMLINK)
		//      p->hp_flags |= PROP_PROXY_FOLLOW_SYMLINK (prop_proxy.c:289-290)
		if proxyDeps.pm != nil &&
			proxyDeps.pm.GetFlags(owner)&propcore.FlagProxyFollowSymlink != 0 {
			proxyDeps.pm.SetFlags(p, proxyDeps.pm.GetFlags(p)|propcore.FlagProxyFollowSymlink)
		}
	}
	ppc.Retain() // C: p->hp_proxy_ppc = prop_proxy_retain(ppc)
	return p
}

// ProxyGetByName is the PROP_PROXY branch of C's prop_get_by_name
// (prop_core.c:2853-2886): vec = hp_proxy_pfx + names, then
// prop_proxy_make(ppc, hp_proxy_id, NULL, p, vec). The remote end
// resolves the path — proxy props have no local children.
func (ppc *ProxyConnection) ProxyGetByName(p *propcore.Prop, names []string,
	followSymlinks bool) *propcore.Prop {
	ppc.mu.Lock()
	defer ppc.mu.Unlock()

	// C: vec = strdup'd pfx elements followed by the name elements.
	var vec []string
	if m := ppc.meta[p]; m != nil {
		vec = append(vec, m.pfx...)
	}
	vec = append(vec, names...)

	np := ppc.propProxyMake(p.GetProxyID(), nil, p, vec)

	// C: if(follow_symlinks) p->hp_flags |= PROP_PROXY_FOLLOW_SYMLINK
	// (prop_core.c:2882)
	if followSymlinks && proxyDeps.pm != nil {
		proxyDeps.pm.SetFlags(np, proxyDeps.pm.GetFlags(np)|propcore.FlagProxyFollowSymlink)
	}
	return np
}

// findPropOnSubLocked is C's ppc_find_prop_on_sub (RB_FIND by proxy id).
// C asserts non-nil; Go returns nil (callers pass nil props to Notify).
func (ppc *ProxyConnection) findPropOnSubLocked(s *propcore.Subscription, id uint32) *propcore.Prop {
	if tree := ppc.subTrees[s]; tree != nil {
		return tree[id]
	}
	return nil
}

// collectPropsOnSubLocked detaches all props from the sub's tree
// (C: ppc_destroy_props_on_sub's RB_REMOVE loop) and appends them to
// victims for the caller to Destroy0 after releasing ppc.mu
// (Destroy0 → ProxyDestroy re-takes ppc.mu).
func (ppc *ProxyConnection) collectPropsOnSubLocked(s *propcore.Subscription,
	victims []*propcore.Prop) []*propcore.Prop {
	tree := ppc.subTrees[s]
	for id, p := range tree {
		delete(tree, id)
		if m := ppc.meta[p]; m != nil && m.ownerSub == s {
			m.ownerSub = nil
		}
		victims = append(victims, p)
	}
	return victims
}

// ProxySubscribe is C's prop_proxy_subscribe (prop_proxy.c:1227-1278).
// Called from propcore.Prop.Subscribe when the resolved value prop is a
// PROP_PROXY owned by this connection.
func (ppc *ProxyConnection) ProxySubscribe(s *propcore.Subscription, value *propcore.Prop) {
	ppc.mu.Lock()
	defer ppc.mu.Unlock()

	// C: s->hps_ppc = ppc; RB_INIT(prop tree); s->hps_value_prop = NULL;
	//    LIST_INSERT_HEAD(&ppc->ppc_subs, s, hps_value_prop_link);
	s.SetValueProp(nil)
	ppc.subs = slices.Insert(ppc.subs, 0, s)
	ppc.subTrees[s] = make(map[uint32]*propcore.Prop)

	ppc.subTally++
	s.SetProxySubID(uint32(ppc.subTally))

	// C: datalen = 11 + pfx lens + name lens (name is always NULL in the
	// Go path — named-path subs are pre-resolved before Subscribe).
	m := ppc.meta[value]
	var pfx []string
	if m != nil {
		pfx = m.pfx
	}
	datalen := 11
	for _, e := range pfx {
		datalen += 1 + len(e)
	}

	data := make([]byte, datalen)
	data[0] = MGPPCmdSubscribe
	wr32LE(data[1:], s.GetProxySubID())
	wr32LE(data[5:], value.GetProxyID())
	wr16LE(data[9:], uint16(s.GetHpsFlags()&0xffff))
	ptr := 11
	for _, e := range pfx {
		data[ptr] = byte(len(e))
		ptr++
		copy(data[ptr:], e)
		ptr += len(e)
	}
	ppc.sendData(data)
}

// ProxyUnsubscribe is C's prop_proxy_unsubscribe (prop_proxy.c:1284-1298).
func (ppc *ProxyConnection) ProxyUnsubscribe(s *propcore.Subscription) {
	vp := s.GetValueProp()
	s.SetValueProp(nil)

	ppc.mu.Lock()
	victims := ppc.collectPropsOnSubLocked(s, nil)
	delete(ppc.subTrees, s)
	for i, x := range ppc.subs {
		if x == s {
			ppc.subs = slices.Delete(ppc.subs, i, i+1)
			break
		}
	}
	ppc.mu.Unlock()

	// C: prop_destroy0(s->hps_value_prop) then ppc_destroy_props_on_sub —
	// all before STPP_CMD_UNSUBSCRIBE is sent. Go destroys outside ppc.mu
	// (Destroy0 re-enters ppc via ProxyDestroy), preserving C order.
	if proxyDeps.pm != nil {
		if vp != nil {
			proxyDeps.pm.Destroy0(vp)
		}
		for _, p := range victims {
			proxyDeps.pm.Destroy0(p)
		}
	}

	data := make([]byte, 5)
	data[0] = MGPPCmdUnsubscribe
	wr32LE(data[1:], s.GetProxySubID())
	ppc.mu.Lock()
	ppc.sendData(data)
	ppc.mu.Unlock()
}

// ProxySelect is C's prop_proxy_select (prop_proxy.c:1419-1427).
func (ppc *ProxyConnection) ProxySelect(p *propcore.Prop) {
	var q misc.HtsbufQueue
	q.HtsbufQueueSetup(0)
	q.AppendByte(MGPPCmdSelect)
	ppc.sendProp(p, &q)
	ppc.mu.Lock()
	defer ppc.mu.Unlock()
	ppc.sendQueue(&q)
}

// ProxyReqMove is C's prop_proxy_req_move (prop_proxy.c:1184-1201).
func (ppc *ProxyConnection) ProxyReqMove(p, before *propcore.Prop) {
	buf := make([]byte, 9)
	buf[0] = MGPPCmdReqMove
	wr32LE(buf[1:], p.GetProxyID())
	if before == nil {
		ppc.mu.Lock()
		ppc.sendData(buf[:5])
		ppc.mu.Unlock()
	} else {
		wr32LE(buf[5:], before.GetProxyID())
		ppc.mu.Lock()
		ppc.sendData(buf[:9])
		ppc.mu.Unlock()
	}
}

// ProxyWantMoreChilds is C's prop_proxy_want_more_childs
// (prop_proxy.c:1392-1400).
func (ppc *ProxyConnection) ProxyWantMoreChilds(s *propcore.Subscription) {
	data := make([]byte, 5)
	data[0] = MGPPCmdWantMoreChilds
	wr32LE(data[1:], s.GetProxySubID())
	ppc.mu.Lock()
	defer ppc.mu.Unlock()
	ppc.sendData(data)
}

// sendProp is C's prop_proxy_send_prop — writes the proxy id and the
// nil-terminated pfx vec.
func (ppc *ProxyConnection) sendProp(p *propcore.Prop, q *misc.HtsbufQueue) {
	var b [4]byte
	wr32LE(b[:], p.GetProxyID())
	q.Append(b[:])
	if m := ppc.meta[p]; m != nil {
		for _, e := range m.pfx {
			q.AppendByte(byte(len(e)))
			q.Append([]byte(e))
		}
	}
	q.AppendByte(0)
}

// sendStr is C's prop_proxy_send_str — len-prefixed string (0xff + le32
// when len >= 0xff).
func sendStr(s string, q *misc.HtsbufQueue) {
	if len(s) >= 0xff {
		q.AppendByte(0xff)
		var b [4]byte
		wr32LE(b[:], uint32(len(s)))
		q.Append(b[:])
	} else {
		q.AppendByte(byte(len(s)))
	}
	q.Append([]byte(s))
}

// ProxySetString is C's prop_proxy_set_string (prop_proxy.c:1304-1316).
func (ppc *ProxyConnection) ProxySetString(p *propcore.Prop, v string, strType propcore.StringType) {
	var q misc.HtsbufQueue
	q.HtsbufQueueSetup(0)
	q.AppendByte(MGPPCmdSet)
	ppc.sendProp(p, &q)
	q.AppendByte(MGPPSetString)
	q.AppendByte(byte(strType))
	q.Append([]byte(v))
	ppc.mu.Lock()
	defer ppc.mu.Unlock()
	ppc.sendQueue(&q)
}

// ProxySetFloat is C's prop_proxy_set_float (prop_proxy.c:1322-1338).
func (ppc *ProxyConnection) ProxySetFloat(p *propcore.Prop, v float32) {
	var q misc.HtsbufQueue
	q.HtsbufQueueSetup(0)
	q.AppendByte(MGPPCmdSet)
	ppc.sendProp(p, &q)
	q.AppendByte(MGPPSetFloat)
	var b [4]byte
	wr32LE(b[:], math.Float32bits(v))
	q.Append(b[:])
	ppc.mu.Lock()
	defer ppc.mu.Unlock()
	ppc.sendQueue(&q)
}

// ProxySetInt is C's prop_proxy_set_int (prop_proxy.c:1344-1355).
func (ppc *ProxyConnection) ProxySetInt(p *propcore.Prop, v int) {
	var q misc.HtsbufQueue
	q.HtsbufQueueSetup(0)
	q.AppendByte(MGPPCmdSet)
	ppc.sendProp(p, &q)
	q.AppendByte(MGPPSetInt)
	var b [4]byte
	wr32LE(b[:], uint32(int32(v)))
	q.Append(b[:])
	ppc.mu.Lock()
	defer ppc.mu.Unlock()
	ppc.sendQueue(&q)
}

// ProxySetURI — C has NO prop_proxy_set_uri: prop_set_uri on a PROP_PROXY
// prop takes the normal local path (a type-corrupting quirk). Go routes
// URIValue through the ProxySetter iface, so we send the defined
// STPP_SET_URI wire op instead (documented divergence — the least-bad
// semantic; the C quirk silently corrupts the proxy prop's type).
// C: there is no prop_proxy_set_uri — prop_set_uri_exl on a PROP_PROXY is
// refused by prop_clean (prop_core.c:1843), so no URI set ever reaches
// the wire.

// ProxySetVoid is C's prop_proxy_set_void (prop_proxy.c:1406-1414).
func (ppc *ProxyConnection) ProxySetVoid(p *propcore.Prop) {
	var q misc.HtsbufQueue
	q.HtsbufQueueSetup(0)
	q.AppendByte(MGPPCmdSet)
	ppc.sendProp(p, &q)
	q.AppendByte(MGPPSetVoid)
	ppc.mu.Lock()
	defer ppc.mu.Unlock()
	ppc.sendQueue(&q)
}

// ProxyToggleInt is C's prop_proxy_toggle_int (prop_proxy.c:1377-1387).
func (ppc *ProxyConnection) ProxyToggleInt(p *propcore.Prop) {
	var q misc.HtsbufQueue
	q.HtsbufQueueSetup(0)
	q.AppendByte(MGPPCmdSet)
	ppc.sendProp(p, &q)
	q.AppendByte(MGPPToggleInt)
	ppc.mu.Lock()
	defer ppc.mu.Unlock()
	ppc.sendQueue(&q)
}

// ProxyAddInt is C's prop_proxy_add_int — a printf("not implemeted")
// stub upstream; preserved as a no-op with a log for parity.
func (ppc *ProxyConnection) ProxyAddInt(p *propcore.Prop, v int) {
	fmt.Println("prop_proxy_add_int not implemeted")
}

// CreateChild is C's prop_proxy_create (prop_proxy.c:313-330) —
// implements propcore.ProxyCreator. Builds pfx = parent's pfx + name and
// creates an owned proxy prop under the parent's id.
func (ppc *ProxyConnection) CreateChild(parent *propcore.Prop, name string,
	opaque any, canBeAnonymous, fromSubscriptions bool) *propcore.Prop {
	ppc.mu.Lock()
	defer ppc.mu.Unlock()
	var pfx []string
	if om := ppc.meta[parent]; om != nil && om.pfx != nil {
		pfx = append(slices.Clone(om.pfx), name)
	} else {
		pfx = []string{name}
	}
	return ppc.propProxyMake(parent.GetProxyID(), nil, parent, pfx)
}

// ProxyDestroy is C's prop_proxy_destroy (prop_proxy.c:336-360) —
// implements propcore.ProxyDestroyer. Destroys owned props, releases the
// connection ref, removes owner-sub / owned links, frees pfx.
func (ppc *ProxyConnection) ProxyDestroy(p *propcore.Prop) {
	ppc.mu.Lock()
	m := ppc.meta[p]
	var owned []*propcore.Prop
	if m != nil {
		// C: while((owned = LIST_FIRST(&p->hp_owned))) — destroy each
		owned = m.owned
		for _, o := range owned {
			if om := ppc.meta[o]; om != nil {
				om.owner = nil // C: owned->hp_flags &= ~PROP_PROXY_OWNED_BY_PROP
			}
		}
		m.owned = nil
		if m.ownerSub != nil {
			// C: RB_REMOVE from owner sub's prop tree
			if tree := ppc.subTrees[m.ownerSub]; tree != nil {
				delete(tree, p.GetProxyID())
			}
			m.ownerSub = nil
		}
		if m.owner != nil {
			// C: LIST_REMOVE(p, hp_owned_prop_link)
			if om := ppc.meta[m.owner]; om != nil {
				for i, o := range om.owned {
					if o == p {
						om.owned = slices.Delete(om.owned, i, i+1)
						break
					}
				}
			}
			m.owner = nil
		}
		delete(ppc.meta, p)
	}
	ppc.mu.Unlock()

	// Destroy owned props outside ppc.mu (Destroy0 → ProxyDestroy re-takes it).
	for _, o := range owned {
		proxyDeps.pm.Destroy0(o)
	}
	ppc.Release() // C: prop_proxy_release(p->hp_proxy_ppc)
}

// ProxySendExtEvent is C's prop_proxy_send_event (prop_proxy.c:1080-1180).
// Serializes the event onto the wire; unsupported types are dropped with
// a diagnostic (C: "Can't serialize event %d").
func (ppc *ProxyConnection) ProxySendExtEvent(p *propcore.Prop, e propcore.ExtEvent) {
	var et event.EventType
	var base *event.Event
	switch v := e.(type) {
	case *event.Event:
		et, base = v.Type, v
	case *event.EventActionVector:
		et, base = v.Type, &v.Event
	case *event.EventPayload:
		et, base = v.Type, &v.Event
	case *event.EventOpenURL:
		et, base = v.Type, &v.Event
	case *event.EventPlayTrack:
		et, base = v.Type, &v.Event
	case *event.EventSelectTrack:
		et, base = v.Type, &v.Event
	default:
		return
	}

	var q misc.HtsbufQueue
	q.HtsbufQueueSetup(0)
	q.AppendByte(MGPPCmdEvent)
	ppc.sendProp(p, &q)
	q.AppendByte(byte(et))

	switch et {
	case event.EVENT_ACTION_VECTOR:
		// C: event_action_vector_t — each action as len-byte + string
		var actions []event.ActionType
		if v, ok := e.(*event.EventActionVector); ok {
			actions = v.Actions
		} else {
			actions = base.Actions
		}
		for _, a := range actions {
			s := fmt.Sprintf("unknown(%d)", a)
			if proxyDeps.em != nil {
				s = proxyDeps.em.ActionCode2Str(a)
			}
			q.AppendByte(byte(len(s)))
			q.Append([]byte(s))
		}
	case event.EVENT_DYNAMIC_ACTION:
		// C: event_payload_t — raw payload bytes. The payload lives on
		// the base Event so it survives *Event flattening.
		q.Append([]byte(base.Payload))
	case event.EVENT_OPENURL:
		// C: event_openurl_t — flags byte + present fields in order
		var url, view, how, parentURL string
		var itemModel, parentModel *propcore.Prop
		if v, ok := e.(*event.EventOpenURL); ok {
			url, view, how, parentURL = v.URL, v.View, v.How, v.ParentURL
			itemModel, _ = v.ItemModel.(*propcore.Prop)
			parentModel, _ = v.ParentModel.(*propcore.Prop)
		} else if base.OpenURL != nil {
			url, view, how, parentURL = base.OpenURL.URL, base.OpenURL.View,
				base.OpenURL.How, base.OpenURL.ParentURL
			itemModel, _ = base.OpenURL.ItemModel.(*propcore.Prop)
			parentModel, _ = base.OpenURL.ParentModel.(*propcore.Prop)
		}
		var flags byte
		if url != "" {
			flags |= 0x01
		}
		if view != "" {
			flags |= 0x02
		}
		if itemModel != nil {
			flags |= 0x04
		}
		if parentModel != nil {
			flags |= 0x08
		}
		if how != "" {
			flags |= 0x10
		}
		if parentURL != "" {
			flags |= 0x20
		}
		q.AppendByte(flags)
		if url != "" {
			sendStr(url, &q)
		}
		if view != "" {
			sendStr(view, &q)
		}
		if itemModel != nil {
			ppc.sendProp(itemModel, &q)
		}
		if parentModel != nil {
			ppc.sendProp(parentModel, &q)
		}
		if how != "" {
			sendStr(how, &q)
		}
		if parentURL != "" {
			sendStr(parentURL, &q)
		}
	case event.EVENT_PLAYTRACK:
		// C: event_playtrack_t — flags + track + source + mode
		var track, source *propcore.Prop
		var mode int
		if v, ok := e.(*event.EventPlayTrack); ok {
			track, _ = v.Track.(*propcore.Prop)
			source, _ = v.Source.(*propcore.Prop)
			mode = v.Mode
		}
		var flags byte
		if source != nil {
			flags |= 0x01
		}
		q.AppendByte(flags)
		if track != nil {
			ppc.sendProp(track, &q)
		}
		if source != nil {
			ppc.sendProp(source, &q)
		}
		q.AppendByte(byte(mode))
	case event.EVENT_SELECT_AUDIO_TRACK, event.EVENT_SELECT_SUBTITLE_TRACK:
		// C: event_select_track_t — manual byte + id string
		var manual bool
		var id string
		if v, ok := e.(*event.EventSelectTrack); ok {
			manual, id = v.Manual, v.ID
		}
		if manual {
			q.AppendByte(0x01)
		} else {
			q.AppendByte(0x00)
		}
		sendStr(id, &q)
	default:
		fmt.Printf("%s: Can't serialize event %d\n", "prop_proxy_send_event", et)
		return
	}
	ppc.mu.Lock()
	ppc.sendQueue(&q)
	ppc.mu.Unlock()
}

// ImageLoader is C's prop_proxy_image_loader (prop_proxy.c:971-1013) —
// the Backend.Imageloader implementation for proxied images. Blocks on
// ppc.imageCond until the reply/fail/cancel arrives.
func (ppc *ProxyConnection) ImageLoader(url string, imageMeta any,
	cacheControl *int, cancellable any, be *backendcore.Backend) (any, error) {
	im, _ := imageMeta.(*imagepkg.ImageMeta)
	if im != nil && im.ForceLocalLoad {
		return nil, nil
	}

	ppi := &imageReq{ppc: ppc}
	var q misc.HtsbufQueue
	q.HtsbufQueueSetup(0)
	q.AppendByte(MGPPCmdImageLoad)
	ppi.id = uint32(ppc.imageReqID.Add(1))
	var b [4]byte
	wr32LE(b[:], ppi.id)
	q.Append(b[:])
	var reqW, reqH int32
	var wantThumb int32
	if im != nil {
		reqW = int32(im.ReqWidth)
		reqH = int32(im.ReqHeight)
		if im.WantThumb {
			wantThumb = 1
		}
	}
	wr32LE(b[:], uint32(reqW))
	q.Append(b[:])
	wr32LE(b[:], uint32(reqH))
	q.Append(b[:])
	wr32LE(b[:], uint32(wantThumb))
	q.Append(b[:])
	q.Append([]byte(url))

	// C: c = cancellable_bind(c, prop_proxy_imgload_cancel, &ppi);
	var c *misc.Cancellable
	if cc, ok := cancellable.(*misc.Cancellable); ok {
		c = misc.CancellableBind(cc, imgloadCancel, ppi)
	}

	ppc.mu.Lock()
	ppc.imageReqs = slices.Insert(ppc.imageReqs, 0, ppi)
	ppc.sendQueue(&q)
	for !ppi.done {
		ppc.imageCond.Wait()
	}
	for i, r := range ppc.imageReqs {
		if r == ppi {
			ppc.imageReqs = slices.Delete(ppc.imageReqs, i, i+1)
			break
		}
	}
	ppc.mu.Unlock()

	// C: cancellable_unbind(c, &ppi);
	if c != nil {
		misc.CancellableUnbind(c, ppi)
	}
	if ppi.image != nil {
		return ppi.image, nil
	}
	if ppi.err != "" {
		return nil, errors.New(ppi.err)
	}
	return nil, nil
}

// imgloadCancel is C's prop_proxy_imgload_cancel (prop_proxy.c:955-969).
func imgloadCancel(opaque any) {
	ppi := opaque.(*imageReq)
	ppc := ppi.ppc

	ppc.mu.Lock()
	defer ppc.mu.Unlock()
	ppi.done = true
	ppi.err = "Cancelled"
	ppc.imageCond.Broadcast()

	cmd := make([]byte, 5)
	cmd[0] = MGPPCmdImageCancel
	wr32LE(cmd[1:], ppi.id)
	ppc.sendData(cmd)
}

// GetBackend is C's prop_proxy_get_backend (prop_proxy.c:1020-1031).
func (ppc *ProxyConnection) GetBackend() *backendcore.Backend {
	be := &backendcore.Backend{
		Flags:       backendcore.BackendDynamic,
		Opaque:      ppc.Retain(),
		Imageloader: ppc.ImageLoader,
	}
	// C: be->be_flags = BACKEND_DYNAMIC; be->be_destroy = prop_proxy_backend_destroy
	be.Destroy = func(b *backendcore.Backend) {
		if c, ok := b.Opaque.(*ProxyConnection); ok {
			c.Release()
		}
	}
	return be
}

// strvecEq compares two string vecs (C: pfx loop in prop_proxy_make).
func strvecEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
