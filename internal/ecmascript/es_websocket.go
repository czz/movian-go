package ecmascript

import (
	"bytes"
	cryptorand "crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/czz/movian-go/internal/asyncio"
	"github.com/czz/movian-go/internal/gaftape"
	"github.com/czz/movian-go/internal/misc"
	netcore "github.com/czz/movian-go/internal/networking/core"
	httpnet "github.com/czz/movian-go/internal/networking/http"
	wspkg "github.com/czz/movian-go/internal/networking/websocket"
	"github.com/czz/movian-go/internal/task"
)

// es_websocket.go — canonical port of src/ecmascript/es_websocket.c
//
// JS-facing websocket client + server. The client runs on the asyncio
// thread; all JS callbacks are marshalled into the ecmascript context via
// task_run_in_group. The server registers a websocket HTTP path and
// creates per-connection es resources.

// ---------------------------------------------------------------------------
// C: typedef struct es_websocket_client (es_websocket.c:40-74)
// ---------------------------------------------------------------------------

type esWebsocketClient struct {
	super      ESResource
	connection *asyncio.AsyncIOFD // C: asyncio_fd_t *ewc_connection
	taskGroup  *task.TaskGroup    // C: task_group_t *ewc_task_group

	hostname string // C: char *ewc_hostname
	path     string // C: char *ewc_path
	port     int    // C: int ewc_port

	tlsctx *tls.Config // C: void *ewc_tlsctx

	protocol string // C: char *ewc_protocol

	outq bytes.Buffer // C: htsbuf_queue_t ewc_outq
	inq  bytes.Buffer // C: input queue — asyncio read side feeds it

	ws wspkg.WebSocketState // C: websocket_state_t ewc_ws

	dnsLookup *asyncio.DNSReq // C: asyncio_dns_req_t *ewc_dns_lookup

	state int // C: int ewc_state (EWC_*)

	httpLineCnt int    // C: int ewc_http_linecnt
	httpStatus  int    // C: int ewc_http_status
	statusStr   string // C: char *ewc_status_str

	timer asyncio.Timer // C: asyncio_timer_t ewc_timer

	alive bool // C: int ewc_alive

	maskGen *misc.Prng // Go-side prng for ws.MaskGen
}

// C: EWC_* states
const (
	ewcConnecting = 0
	ewcConnected  = 1
	ewcClosing    = 2
	ewcClosed     = 3
)

// C: typedef struct es_websocket_client_xfer_task (es_websocket.c:80-85)
type esWebsocketClientXferTask struct {
	ewc     *esWebsocketClient
	opcode  int
	buf     []byte
	bufsize int
}

// C: typedef struct es_websocket_client_close_task (es_websocket.c:91-95)
type esWebsocketClientCloseTask struct {
	ewc    *esWebsocketClient
	status int
	msg    string
}

// ---------------------------------------------------------------------------
// C: es_websocket_client_close_task_fn (es_websocket.c:102-126)
// ---------------------------------------------------------------------------

func esWebsocketClientCloseTaskFn(aux any) {
	t := aux.(*esWebsocketClientCloseTask)
	ewc := t.ewc

	ec := ewc.super.erCtx
	ctx := EsContextBegin(ec)

	if !ewc.super.erZombie {
		EsPushRoot(ctx, ewc)

		ctx.GetPropString(-1, "onClose")
		ctx.PushInt(t.status)
		ctx.PushString(t.msg)
		rc := ctx.PCall(2)
		if rc != 0 {
			EsDumpErr(ctx)
		}
		ctx.Pop()
	}

	EsContextEnd(ec, 1, ctx)
	EsResourceRelease(&ewc.super)
}

// ---------------------------------------------------------------------------
// C: es_websocket_client_close (es_websocket.c:133-145)
// ---------------------------------------------------------------------------

func esWebsocketClientClose(ewc *esWebsocketClient, statuscode int, statusmsg string) {
	t := &esWebsocketClientCloseTask{}

	EsResourceRetain(&ewc.super)

	t.ewc = ewc
	t.msg = statusmsg
	t.status = statuscode
	esEnv.tasks.RunInGroup(esWebsocketClientCloseTaskFn, t, ewc.taskGroup)
}

// ---------------------------------------------------------------------------
// C: es_websocket_client_connect_task_fn (es_websocket.c:153-172)
// ---------------------------------------------------------------------------

func esWebsocketClientConnectTaskFn(aux any) {
	ewc := aux.(*esWebsocketClient)

	ec := ewc.super.erCtx
	ctx := EsContextBegin(ec)

	if !ewc.super.erZombie {
		EsPushRoot(ctx, ewc)

		ctx.GetPropString(-1, "onConnect")
		rc := ctx.PCall(0)
		if rc != 0 {
			EsDumpErr(ctx)
		}
		ctx.Pop()
	}

	EsContextEnd(ec, 1, ctx)
	EsResourceRelease(&ewc.super)
}

// ---------------------------------------------------------------------------
// C: es_websocket_client_input_task_fn (es_websocket.c:180-216)
// ---------------------------------------------------------------------------

func esWebsocketClientInputTaskFn(aux any) {
	t := aux.(*esWebsocketClientXferTask)
	ewc := t.ewc

	ec := ewc.super.erCtx
	ctx := EsContextBegin(ec)

	if !ewc.super.erZombie {
		EsPushRoot(ctx, ewc)

		ctx.GetPropString(-1, "onInput")

		if t.opcode == 2 {
			ptr := ctx.PushFixedBuffer(t.bufsize)
			copy(ptr, t.buf[:t.bufsize])
			ctx.PushBufferObject(-1, 0, t.bufsize, gaftape.GAF_BUFOBJ_ARRAYBUFFER)
			ctx.SwapTop(-1)
			ctx.Pop()
		} else {
			ctx.PushLstring(string(t.buf), t.bufsize)
		}

		rc := ctx.PCall(1)
		if rc != 0 {
			EsDumpErr(ctx)
		}
		ctx.Pop()
	}

	EsContextEnd(ec, 0, ctx)
	EsResourceRelease(&ewc.super)
}

// ---------------------------------------------------------------------------
// C: es_websocket_client_net_destroy (es_websocket.c:223-250)
// ---------------------------------------------------------------------------

func esWebsocketClientNetDestroy(aux any) {
	ewc := aux.(*esWebsocketClient)
	if ewc.connection != nil {
		ewc.connection.Close()
		ewc.connection = nil
	}

	if ewc.dnsLookup != nil {
		ewc.dnsLookup.Cancel()
		EsResourceRelease(&ewc.super) // DNS lookup held a refcount
		ewc.dnsLookup = nil
	}

	ewc.timer.Disarm()

	ewc.ws.Packet = nil

	ewc.outq.Reset()

	if ewc.tlsctx != nil {
		// C: asyncio_ssl_free(ewc->ewc_tlsctx) — Go GC's the tls.Config
		ewc.tlsctx = nil
	}

	EsResourceRelease(&ewc.super)
}

// ---------------------------------------------------------------------------
// C: es_websocket_client_send_task_fn (es_websocket.c:262-277)
// ---------------------------------------------------------------------------

func esWebsocketClientSendTaskFn(aux any) {
	t := aux.(*esWebsocketClientXferTask)
	ewc := t.ewc

	if ewc.connection != nil {
		var q bytes.Buffer
		wspkg.WebSocketAppend(&q, t.opcode, t.buf, t.bufsize, &ewc.ws)
		ewcSendq(ewc.connection, &q, 0)
	}

	EsResourceRelease(&ewc.super)
}

// ewcSendq — C: asyncio_sendq — queue bytes to the fd.
func ewcSendq(af *asyncio.AsyncIOFD, q *bytes.Buffer, cork int) {
	b := q.Bytes()
	if len(b) > 0 {
		af.Send(b, len(b), cork)
	}
	q.Reset()
}

// ---------------------------------------------------------------------------
// C: es_websocket_client_destroy / info / finalizer (es_websocket.c:286-330)
// ---------------------------------------------------------------------------

func esWebsocketClientDestroy(eres *ESResource) {
	ewc := eres.Data.(*esWebsocketClient)
	EsResourceRetain(eres)
	esEnv.asyncIO.RunTask(esWebsocketClientNetDestroy, eres.Data)

	EsRootUnregister(eres.erCtx.ecGaf, ewc)
	EsResourceUnlink(eres)
}

func esWebsocketClientInfo(eres *ESResource) string {
	ewc := eres.Data.(*esWebsocketClient)
	return fmt.Sprintf("%s:%d%s", ewc.hostname, ewc.port, ewc.path)
}

func esWebsocketClientFinalizer(eres *ESResource) {
	ewc := eres.Data.(*esWebsocketClient)
	ewc.hostname = ""
	ewc.path = ""
	ewc.statusStr = ""
	esEnv.tasks.GroupDestroy(ewc.taskGroup)
}

// C: es_resource_websocket_client (es_websocket.c:324-330)
var esResourceWebsocketClient = &ESResourceClass{
	ErcName:      "websocket_client",
	ErcDestroy:   esWebsocketClientDestroy,
	ErcInfo:      esWebsocketClientInfo,
	ErcFinalizer: esWebsocketClientFinalizer,
}

// ---------------------------------------------------------------------------
// C: es_websocket_client_input_ws (es_websocket.c:340-376)
// ---------------------------------------------------------------------------

func esWebsocketClientInputWs(opaque any, opcode int, data []byte, l int) {
	ewc := opaque.(*esWebsocketClient)

	switch opcode {
	case 1, 2: // Text / Binary
		t := &esWebsocketClientXferTask{}
		EsResourceRetain(&ewc.super)
		t.ewc = ewc
		t.buf = make([]byte, l)
		t.bufsize = l
		t.opcode = opcode
		copy(t.buf, data[:l])

		esEnv.tasks.RunInGroup(esWebsocketClientInputTaskFn, t, ewc.taskGroup)

	case 9: // PING -> Send PONG
		var q bytes.Buffer
		wspkg.WebSocketAppendHdr(&q, 10, l, nil)
		q.Write(data[:l])
		ewcSendq(ewc.connection, &q, 0)

	case 10:
		ewc.alive = true
	}
}

// ---------------------------------------------------------------------------
// C: es_websocket_client_input_http (es_websocket.c:383-427)
// ---------------------------------------------------------------------------

func esWebsocketClientInputHttp(ewc *esWebsocketClient, q *bytes.Buffer) int {
	for {
		line := httpReadLineQ(q)
		if line == nil {
			return 0
		}
		l := *line
		if ewc.httpLineCnt == 0 {
			if !strings.HasPrefix(l, "HTTP/1.1 ") {
				return -1
			}
			ewc.statusStr = l[9:]
			ewc.httpStatus, _ = strconv.Atoi(strings.Fields(l[9:])[0])
		} else if l == "" {
			// Last line
			if ewc.httpStatus == 101 {
				ewc.alive = true
				ewc.state = ewcConnected

				EsResourceRetain(&ewc.super)
				esEnv.tasks.RunInGroup(esWebsocketClientConnectTaskFn, ewc,
					ewc.taskGroup)

				return 0
			}
			return -1
		}

		ewc.httpLineCnt++
	}
}

// httpReadLineQ — C: http_read_line(q) over a byte queue. Returns nil when
// the buffer holds no complete line (C: NULL), or a line without CRLF.
func httpReadLineQ(q *bytes.Buffer) *string {
	b := q.Bytes()
	i := bytes.Index(b, []byte("\r\n"))
	if i < 0 {
		return nil
	}
	s := string(b[:i])
	q.Next(i + 2)
	return &s
}

// ---------------------------------------------------------------------------
// C: es_websocket_client_input (es_websocket.c:434-461)
// ---------------------------------------------------------------------------

func esWebsocketClientInput(opaque any, data []byte) {
	ewc := opaque.(*esWebsocketClient)
	ewc.inq.Write(data)
	q := &ewc.inq
	r := 0
	switch ewc.state {
	case ewcConnecting:
		r = esWebsocketClientInputHttp(ewc, q)
		if r != 0 || ewc.state == ewcConnecting {
			break
		}
		fallthrough
	case ewcConnected:
		if wspkg.WebSocketParse(q, esWebsocketClientInputWs, ewc, &ewc.ws) != 0 {
			ewc.statusStr = "Websocket error"
			r = 1
		} else {
			r = 0
		}
	default:
		return
	}
	if r != 0 {
		esWebsocketClientClose(ewc, wspkg.WSStatusAbnormalClose,
			ewc.statusStr)
		esWebsocketClientNetDestroy(ewc) // Will release the ref we have
	}
}

// ---------------------------------------------------------------------------
// C: es_websocket_timeout (es_websocket.c:468-485)
// ---------------------------------------------------------------------------

func esWebsocketTimeout(aux any) {
	ewc := aux.(*esWebsocketClient)
	if !ewc.alive {
		esWebsocketClientClose(ewc, wspkg.WSStatusAbnormalClose, "Timeout")
		esWebsocketClientNetDestroy(ewc) // Will release the ref we have
		return
	}

	ewc.alive = false
	var q bytes.Buffer
	wspkg.WebSocketAppendHdr(&q, 9, 4, nil)
	var payload uint32
	q.Write([]byte{byte(payload), byte(payload >> 8),
		byte(payload >> 16), byte(payload >> 24)})
	ewcSendq(ewc.connection, &q, 0)
	ewc.timer.ArmDeltaSec(20)
}

// ---------------------------------------------------------------------------
// C: es_websocket_client_connected (es_websocket.c:492-523)
// ---------------------------------------------------------------------------

func esWebsocketClientConnected(ewc *esWebsocketClient, err string) {
	if err != "" {
		esWebsocketClientClose(ewc, wspkg.WSStatusAbnormalClose, err)
		esWebsocketClientNetDestroy(ewc) // Will release the ref we have
		return
	}

	var nonce [16]byte
	// C: arch_get_random_bytes(nonce, sizeof(nonce))
	cryptorand.Read(nonce[:])
	// C: av_base64_encode(key, sizeof(key), nonce, sizeof(nonce))
	key := base64.StdEncoding.EncodeToString(nonce[:])

	buf := fmt.Sprintf(
		"GET %s HTTP/1.1\r\n"+
			"Host: %s\r\n"+
			"Connection: Upgrade\r\n"+
			"Upgrade: websocket\r\n"+
			"Sec-WebSocket-Version: 13\r\n"+
			"Sec-WebSocket-Key: %s\r\n",
		ewc.path, ewc.hostname, key)
	ewc.connection.Send([]byte(buf), len(buf), 1)
	ewc.connection.Send([]byte("\r\n"), 2, 0)

	ewc.timer.Setup(esEnv.asyncIO, esWebsocketTimeout, ewc)
	ewc.timer.ArmDeltaSec(20)
}

// ---------------------------------------------------------------------------
// C: es_websocket_client_connect (es_websocket.c:529-552)
// ---------------------------------------------------------------------------

func esWebsocketClientConnect(opaque any, dnsLookupStatus asyncio.DNSStatus, data any) {
	ewc := opaque.(*esWebsocketClient)
	ewc.dnsLookup = nil
	if dnsLookupStatus == asyncio.DNSStatusCompleted {
		// C: data is const net_addr_t *
		na, _ := data.(*netcore.NetAddr)
		if na == nil {
			esWebsocketClientClose(ewc, wspkg.WSStatusAbnormalClose, "DNS failure")
			EsResourceRelease(&ewc.super)
			return
		}
		na.Port = uint16(ewc.port)
		// C: asyncio_connect — error_cb doubles as connected callback
		// (err == NULL / "" on success); read errors and connect
		// failures also land here, exactly like C.
		ewc.connection = esEnv.asyncIO.Connect("ecmascript/websocket", na,
			func(opaque any, err string) {
				e := opaque.(*esWebsocketClient)
				esWebsocketClientConnected(e, err)
			},
			ewcReadCB,
			ewc, 60000,
			ewc.tlsctx,
			ewc.hostname)
	} else {
		errStr, _ := data.(string)
		esWebsocketClientClose(ewc, wspkg.WSStatusAbnormalClose, errStr)
		EsResourceRelease(&ewc.super)
	}
}

// ewcReadCB — canonical asyncio_read_callback_t adapter: drains the
// recv queue into esWebsocketClientInput (C: es_websocket_client_input
// receives htsbuf_queue_t*).
func ewcReadCB(opaque any, q *misc.HtsbufQueue) {
	n := q.Len()
	if n == 0 {
		return
	}
	buf := make([]byte, n)
	q.Read(buf, n)
	esWebsocketClientInput(opaque, buf)
}

// ---------------------------------------------------------------------------
// C: es_websocket_client_create (es_websocket.c:557-607)
// ---------------------------------------------------------------------------

func esWebsocketClientCreate(ctx *gaftape.Context) int {
	ec := EsGet(ctx)
	url := ctx.SafeToString(0)
	proto := ctx.GetString(1)

	ewc := &esWebsocketClient{}
	ewc.super.erClass = esResourceWebsocketClient
	ewc.super.Data = ewc
	ewc.taskGroup = esEnv.tasks.GroupCreate()
	misc.PrngSeed2(ewc.wsMaskGen())

	EsResourceLink(&ewc.super, ec, 1)

	ewc.protocol = proto
	EsRootRegister(ctx, 2, ewc)

	var protostr, hostname, path [1024]byte
	port := -1

	misc.UrlSplit(protostr[:64], 64, nil, 0,
		hostname[:256], 256,
		&port, path[:1024], 1024, url)

	if port == -1 {
		if misc.CStr(protostr[:]) == "wss" {
			port = 443
		} else {
			port = 80
		}
	}

	if misc.CStr(protostr[:]) == "wss" {
		ewc.tlsctx = esEnv.asyncIO.SSLCreateClient()
	}

	ewc.hostname = misc.CStr(hostname[:])
	ewc.path = misc.CStr(path[:])
	ewc.port = port

	EsResourceRetain(&ewc.super) // for DNS lookup
	ewc.dnsLookup = esEnv.asyncIO.DNSLookupHost(ewc.hostname,
		esWebsocketClientConnect, ewc)

	EsResourcePush(ctx, &ewc.super)
	return 1
}

// wsMaskGen — C: ewc_ws.maskgen (prng_t); the Go WebSocketState carries
// MaskGen []byte — keep a Prng alongside and expose the slice.
func (ewc *esWebsocketClient) wsMaskGen() *misc.Prng {
	if ewc.maskGen == nil {
		ewc.maskGen = &misc.Prng{}
	}
	return ewc.maskGen
}

// ---------------------------------------------------------------------------
// C: es_websocket_client_send (es_websocket.c:612-643)
// ---------------------------------------------------------------------------

func esWebsocketClientSend(ctx *gaftape.Context) int {
	// C: es_resource_get(ctx, 0, &es_resource_websocket_client)
	ewc := EsResourceGet(ctx, 0, esResourceWebsocketClient).(*ESResource)
	cw := ewc.Data.(*esWebsocketClient)

	var buf []byte
	var opcode int
	if b := ctx.GetBufferData(1); b != nil {
		buf = b
		opcode = 2 // binary
	} else {
		s := ctx.ToString(1)
		buf = []byte(s)
		opcode = 1 // text
	}

	t := &esWebsocketClientXferTask{}
	t.opcode = opcode
	t.buf = bytes.Clone(buf)
	t.bufsize = len(buf)

	EsResourceRetain(&cw.super)
	t.ewc = cw

	esEnv.asyncIO.RunTask(esWebsocketClientSendTaskFn, t)
	return 0
}

// ---------------------------------------------------------------------------
// C: typedef struct es_websocket_server (es_websocket.c:651-658)
// ---------------------------------------------------------------------------

type esWebsocketServer struct {
	super ESResource

	path *httpnet.HTTPPath // C: http_path_t *ews_path

	pathstr string // C: char *ews_pathstr
}

// ---------------------------------------------------------------------------
// C: es_websocket_server_destroy / info / finalizer (es_websocket.c:666-707)
// ---------------------------------------------------------------------------

func esWebsocketServerDestroy(eres *ESResource) {
	ews := eres.Data.(*esWebsocketServer)

	if esEnv.httpServer != nil {
		esEnv.httpServer.HTTPPathRemove(ews.path)
	}
	ews.path = nil

	EsRootUnregister(eres.erCtx.ecGaf, ews)
	EsResourceUnlink(eres)
}

func esWebsocketServerInfo(eres *ESResource) string {
	ews := eres.Data.(*esWebsocketServer)
	return ews.pathstr
}

func esWebsocketServerFinalizer(eres *ESResource) {
	ews := eres.Data.(*esWebsocketServer)
	ews.pathstr = ""
}

// C: es_resource_websocket_server (es_websocket.c:701-707)
var esResourceWebsocketServer = &ESResourceClass{
	ErcName:      "websocket_server",
	ErcDestroy:   esWebsocketServerDestroy,
	ErcInfo:      esWebsocketServerInfo,
	ErcFinalizer: esWebsocketServerFinalizer,
}

// ---------------------------------------------------------------------------
// C: typedef struct es_websocket_server_connection (es_websocket.c:713-722)
// ---------------------------------------------------------------------------

type esWebsocketServerConnection struct {
	super ESResource

	server *esWebsocketServer // C: es_websocket_server_t *ewsc_server

	hc *httpnet.HTTPConnection // C: http_connection_t *ewsc_hc (asyncio thread only)

	taskGroup *task.TaskGroup // C: task_group_t *ewsc_task_group
}

// ---------------------------------------------------------------------------
// C: es_websocket_server_connection_destroy / info / finalizer
// (es_websocket.c:731-782)
// ---------------------------------------------------------------------------

func esWebsocketServerConnectionDestroy(eres *ESResource) {
	ewsc := eres.Data.(*esWebsocketServerConnection)

	EsRootUnregister(eres.erCtx.ecGaf, ewsc)

	EsResourceRelease(&ewsc.server.super)
	ewsc.server = nil

	EsResourceUnlink(eres)
}

func esWebsocketServerConnectionInfo(eres *ESResource) string {
	ewsc := eres.Data.(*esWebsocketServerConnection)
	return ewsc.server.pathstr
}

func esWebsocketServerConnectionFinalizer(eres *ESResource) {
	ewsc := eres.Data.(*esWebsocketServerConnection)
	esEnv.tasks.GroupDestroy(ewsc.taskGroup)
}

// C: es_resource_websocket_server_connection (es_websocket.c:776-782)
var esResourceWebsocketServerConnection = &ESResourceClass{
	ErcName:      "websocket_server_connection",
	ErcDestroy:   esWebsocketServerConnectionDestroy,
	ErcInfo:      esWebsocketServerConnectionInfo,
	ErcFinalizer: esWebsocketServerConnectionFinalizer,
}

// ---------------------------------------------------------------------------
// C: typedef struct es_websocket_server_connection_task (es_websocket.c:788-793)
// ---------------------------------------------------------------------------

type esWebsocketServerConnectionTask struct {
	ewsc    *esWebsocketServerConnection
	opcode  int
	buf     []byte
	bufsize int
}

// ---------------------------------------------------------------------------
// C: ewsc_maketask / ewsc_freetask (es_websocket.c:800-821)
// ---------------------------------------------------------------------------

func ewscMaketask(ewsc *esWebsocketServerConnection, opcode int) *esWebsocketServerConnectionTask {
	t := &esWebsocketServerConnectionTask{}
	EsResourceRetain(&ewsc.super)
	t.ewsc = ewsc
	t.opcode = opcode
	return t
}

func ewscFreetask(t *esWebsocketServerConnectionTask) {
	EsResourceRelease(&t.ewsc.super)
}

// ---------------------------------------------------------------------------
// C: ews_removed (es_websocket.c:828-831)
// ---------------------------------------------------------------------------

func ewsRemoved(opaque any) {
	EsResourceRelease(opaque.(*ESResource))
}

// ---------------------------------------------------------------------------
// C: ewsc_send_task_fn / ewsc_send (es_websocket.c:837-877)
// ---------------------------------------------------------------------------

func ewscSendTaskFn(aux any) {
	t := aux.(*esWebsocketServerConnectionTask)
	ewsc := t.ewsc

	if ewsc.hc != nil {
		ewsc.hc.WebSocketSend(t.opcode, t.buf, t.bufsize)
	}

	ewscFreetask(t)
}

func ewscSend(ctx *gaftape.Context) int {
	ctx.PushThis()

	ewsc := EsResourceGet(ctx, -1, esResourceWebsocketServerConnection).(*ESResource)
	conn := ewsc.Data.(*esWebsocketServerConnection)

	var buf []byte
	var opcode int
	if b := ctx.GetBufferData(0); b != nil {
		buf = b
		opcode = 2 // binary
	} else {
		s := ctx.ToString(0)
		buf = []byte(s)
		opcode = 1 // text
	}

	t := ewscMaketask(conn, opcode)
	t.buf = bytes.Clone(buf)
	t.bufsize = len(buf)

	esEnv.asyncIO.RunTask(ewscSendTaskFn, t)
	return 0
}

// ---------------------------------------------------------------------------
// C: ewsc_close (es_websocket.c:884-906)
// ---------------------------------------------------------------------------

func ewscClose(ctx *gaftape.Context) int {
	ctx.PushThis()
	ewsc := EsResourceGet(ctx, -1, esResourceWebsocketServerConnection).(*ESResource)
	conn := ewsc.Data.(*esWebsocketServerConnection)
	t := ewscMaketask(conn, 8)

	code := 1001
	if ctx.IsNumber(0) {
		code = ctx.ToInt(0)
	}
	var msg []byte
	if ctx.IsString(1) {
		msg = []byte(ctx.ToString(1))
	}

	t.bufsize = 2 + len(msg)
	t.buf = make([]byte, t.bufsize)
	// C: wr16_be(t->buf, code)
	t.buf[0] = byte(code >> 8)
	t.buf[1] = byte(code)
	copy(t.buf[2:], msg)

	esEnv.asyncIO.RunTask(ewscSendTaskFn, t)
	return 0
}

// ---------------------------------------------------------------------------
// C: es_websocket_server_connection_open_fn (es_websocket.c:913-942)
// ---------------------------------------------------------------------------

func esWebsocketServerConnectionOpenFn(aux any) {
	t := aux.(*esWebsocketServerConnectionTask)
	ewsc := t.ewsc
	ec := ewsc.super.erCtx
	ctx := EsContextBegin(ec)

	ews := ewsc.server
	if ews != nil && !ews.super.erZombie {
		EsPushRoot(ctx, ews)
		ctx.GetPropString(-1, "onOpen")

		objidx := EsResourcePush(ctx, &ewsc.super)

		EsRootRegister(ctx, objidx, ewsc)

		ctx.PushCLightFunc(ewscSend, 1, 0, 0)
		ctx.PutPropString(objidx, "send")

		ctx.PushCLightFunc(ewscClose, 2, 0, 0)
		ctx.PutPropString(objidx, "close")

		rc := ctx.PCall(1)
		if rc != 0 {
			EsDumpErr(ctx)
		}
		ctx.Pop()
	}
	ewscFreetask(t)
	EsContextEnd(ec, 1, ctx)
}

// ---------------------------------------------------------------------------
// C: ews_connected (es_websocket.c:949-977)
// ---------------------------------------------------------------------------

func ewsConnected(hc *httpnet.HTTPConnection, pathOpaque any) int {
	ews := pathOpaque.(*esWebsocketServer)
	ec := ews.super.erCtx

	ctx := EsContextBegin(ec)

	ewsc := &esWebsocketServerConnection{}
	ewsc.super.erClass = esResourceWebsocketServerConnection
	ewsc.super.Data = ewsc

	ewsc.taskGroup = esEnv.tasks.GroupCreate()

	EsResourceLink(&ewsc.super, ec, 0)

	// Link to server definition
	EsResourceRetain(&ews.super)
	ewsc.server = ews

	// Take on reference owned by http connection (released by ews_disconnected)
	EsResourceRetain(&ewsc.super)
	ewsc.hc = hc
	hc.HTTPSetOpaque(ewsc)

	esEnv.tasks.RunInGroup(esWebsocketServerConnectionOpenFn,
		ewscMaketask(ewsc, 0), ewsc.taskGroup)

	EsContextEnd(ec, 1, ctx)
	return 0
}

// ---------------------------------------------------------------------------
// C: es_websocket_server_connection_close_fn / ews_disconnected
// (es_websocket.c:984-1018)
// ---------------------------------------------------------------------------

func esWebsocketServerConnectionCloseFn(aux any) {
	t := aux.(*esWebsocketServerConnectionTask)
	ewsc := t.ewsc
	ec := ewsc.super.erCtx
	ctx := EsContextBegin(ec)
	if !ewsc.super.erZombie {
		EsPushRoot(ctx, ewsc)
		ctx.GetPropString(-1, "onClose")
		rc := ctx.PCall(0)
		if rc != 0 {
			EsDumpErr(ctx)
		}
		ctx.Pop()
	}
	ewscFreetask(t)
	EsResourceDestroy(&ewsc.super)
	EsContextEnd(ec, 1, ctx)
}

func ewsDisconnected(hc *httpnet.HTTPConnection, connectionOpaque any) {
	ewsc := connectionOpaque.(*esWebsocketServerConnection)

	esEnv.tasks.RunInGroup(esWebsocketServerConnectionCloseFn,
		ewscMaketask(ewsc, 0), ewsc.taskGroup)

	EsResourceRelease(&ewsc.super)
	ewsc.hc = nil
}

// ---------------------------------------------------------------------------
// C: es_websocket_server_connection_input_fn / ews_input
// (es_websocket.c:1025-1077)
// ---------------------------------------------------------------------------

func esWebsocketServerConnectionInputFn(aux any) {
	t := aux.(*esWebsocketServerConnectionTask)
	ewsc := t.ewsc
	ec := ewsc.super.erCtx
	ctx := EsContextBegin(ec)

	if !ewsc.super.erZombie {
		EsPushRoot(ctx, ewsc)
		ctx.GetPropString(-1, "onInput")

		if t.opcode == 2 {
			ptr := ctx.PushFixedBuffer(t.bufsize)
			copy(ptr, t.buf[:t.bufsize])
			ctx.PushBufferObject(-1, 0, t.bufsize, gaftape.GAF_BUFOBJ_ARRAYBUFFER)
			ctx.SwapTop(-1)
			ctx.Pop()
		} else {
			ctx.PushLstring(string(t.buf), t.bufsize)
		}

		rc := ctx.PCall(1)
		if rc != 0 {
			EsDumpErr(ctx)
		}
		ctx.Pop()
	}
	ewscFreetask(t)
	EsContextEnd(ec, 0, ctx)
}

func ewsInput(hc *httpnet.HTTPConnection, opcode int, data []byte,
	length int, connectionOpaque any) int {
	ewsc := connectionOpaque.(*esWebsocketServerConnection)
	t := ewscMaketask(ewsc, opcode)
	// data is always nul terminated with an extra allocated byte
	// not visible in 'len', pass that on
	t.buf = make([]byte, length+1)
	copy(t.buf, data[:length+1])
	t.bufsize = length
	esEnv.tasks.RunInGroup(esWebsocketServerConnectionInputFn,
		t, ewsc.taskGroup)
	return 0
}

// ---------------------------------------------------------------------------
// C: es_websocket_server_create (es_websocket.c:1085-1106)
// ---------------------------------------------------------------------------

func esWebsocketServerCreate(ctx *gaftape.Context) int {
	ec := EsGet(ctx)
	path := ctx.SafeToString(0)

	ews := &esWebsocketServer{pathstr: path}
	ews.super.erClass = esResourceWebsocketServer
	ews.super.Data = ews

	EsResourceLink(&ews.super, ec, 1)

	// Take on reference owned by http server (released by ews_removed)
	EsResourceRetain(&ews.super)
	if esEnv.httpServer != nil {
		ews.path = esEnv.httpServer.HTTPAddWebSocket(path, ews,
			ewsConnected,
			ewsInput,
			ewsDisconnected,
			ewsRemoved)
	}
	EsRootRegister(ctx, 1, ews)
	EsResourcePush(ctx, &ews.super)
	return 1
}

// ---------------------------------------------------------------------------
// C: fnlist_websocket + ES_MODULE("websocket") (es_websocket.c:1111-1120)
// ---------------------------------------------------------------------------

var esFnlistWebsocket = []gaftape.FunctionListEntry{
	{Key: "clientCreate", Value: esWebsocketClientCreate, Nargs: 3},
	{Key: "clientSend", Value: esWebsocketClientSend, Nargs: 2},
	{Key: "serverCreate", Value: esWebsocketServerCreate, Nargs: 2},
}

func registerEsWebsocket() {
	EcmascriptRegisterModule(&EcmascriptModule{
		Name:      "websocket",
		Functions: esFnlistWebsocket,
	})
}
