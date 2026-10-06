// Canonical port of src/fileaccess/smb/nmb.c — NetBIOS Name Service.
//
// C architecture: a broadcast UDP socket (port 137 target) emits
// __MSBROWSE__ queries every 15s; master-browser replies spawn a task
// that enumerates servers via DCE/RPC (smb_enum_servers) and creates
// smb:// services. Name resolution queues nmb_resolve_t requests on a
// worker, broadcasts NB name queries, and blocks on a cond until a
// reply or the 3s timeout fires.
package smb

import (
	"encoding/binary"
	"fmt"
	"net"
	"slices"
	"sync"

	"github.com/czz/movian-go/internal/asyncio"
	netcore "github.com/czz/movian-go/internal/networking/core"
	"github.com/czz/movian-go/internal/service"
	"github.com/czz/movian-go/internal/task"
)

// nmbServer — C: nmb_server_t (nmb.c:38-44)
type nmbServer struct {
	workgroup string // ns_workgroup
	name      string // ns_name
	mark      int    // ns_mark
	svc       *service.Service
}

// nmbResolve — C: nmb_resolve_t (nmb.c:48-55)
type nmbResolve struct {
	hostname string         // nr_hostname
	ip       net.IP         // nr_addr (filled on success)
	txid     uint16         // nr_txid
	timeout  *asyncio.Timer // nr_timeout
	status   int            // nr_status: 1=pending, 0=ok, -1=timeout
}

// C: nmbpkt_header_t (nmb.c:67-75) — 12 bytes
const nmbpktHeaderLen = 12

// C: nmbpkt_question_t (nmb.c:77-82) — 50 bytes
const nmbpktQuestionLen = 50

// C: nmbpkt_answer_t (nmb.c:85-94) — 62 bytes
const nmbpktAnswerLen = 62

// NMBManager — owns the C global state (nmb_servers,
// nmb_resolve_pending/sent, nmb_udp_fd, timers, txids).
type NMBManager struct {
	mu   sync.Mutex // C: nmb_mutex
	cond *sync.Cond // C: nmb_resolver_cond

	servers []*nmbServer  // C: nmb_servers
	pending []*nmbResolve // C: nmb_resolve_pending
	sent    []*nmbResolve // C: nmb_resolve_sent

	udpFd      *asyncio.AsyncIOFD // C: nmb_udp_fd
	timer      *asyncio.Timer     // C: nmb_timer (15s MSBROWSE)
	flushTimer *asyncio.Timer     // C: nmb_flush_timer (60s flush)
	msbTxid    uint16             // C: nmb_txid
	txidTally  uint16             // C: nmb_transaction_id_tally
	workerID   int                // C: nmb_resolver_signal
	aio        *asyncio.AsyncIO
	tasks      *task.TaskSystem // C: task_run globals — injected

	// enumServers — C: smb_enum_servers (fa_nativesmb.c:2862) called
	// directly. Field so tests can stub the DCE/RPC walk.
	enumServers func(hostname string) []string
	ss          *service.ServiceSystem
}

// NewNMBManager creates a new NMB manager
func NewNMBManager(sys *System) *NMBManager {
	m := &NMBManager{enumServers: sys.SMBEnumServers}
	m.cond = sync.NewCond(&m.mu)
	return m
}

// NMBResolverStart — C: nmb_resolver_init (nmb.c:358-364,
// INIT_GROUP_NET prio 0). Installs the resolver worker.
func (m *NMBManager) NMBResolverStart(aio *asyncio.AsyncIO, ts *task.TaskSystem) {
	m.aio = aio
	m.tasks = ts
	m.workerID = aio.AddWorker(m.nmbResolverProcess)
}

// NMBStart — C: nmb_init (nmb.c:372-381, INIT_GROUP_ASYNCIO prio 0).
// Binds the broadcast UDP socket, arms the MSBROWSE/flush timers, and
// fires the first master-browser query.
func (m *NMBManager) NMBStart(ss *service.ServiceSystem) error {
	if m == nil || m.aio == nil {
		return nil
	}
	m.ss = ss

	// C: asyncio_udp_bind("nmb", 0, nmb_udp_input, NULL, 0, 1)
	fd := m.aio.UDPBind("nmb", nil, m.nmbUDPInput, nil, false, true)
	if fd == nil {
		return fmt.Errorf("nmb: UDPBind failed") // C: NULL from asyncio_udp_bind
	}
	m.udpFd = fd

	// C: asyncio_timer_init(&nmb_timer, nmb_send_msb_query, NULL)
	m.timer = &asyncio.Timer{}
	m.timer.Setup(m.aio, func(any) { m.nmbSendMSBQuery() }, nil)
	m.flushTimer = &asyncio.Timer{}
	m.flushTimer.Setup(m.aio, func(any) { m.removeAllServers() }, nil)

	// C: nmb_send_msb_query(NULL)
	m.nmbSendMSBQuery()
	return nil
}

// encodeName — C: encode_name (nmb.c:99-119). NetBIOS first-level
// encoding: name padded to 15 + type byte (or "*" wildcard), each byte
// → two 'A'-relative nibbles, prefixed by length 32.
func encodeName(in string, out []byte, nameType byte) {
	var buf [20]byte
	if in == "*" {
		buf[0] = '*'
	} else {
		// C: snprintf(buf, 20, "%-15.15s%c", in, name_type)
		n := min(len(in), 15)
		copy(buf[:n], in)
		for i := n; i < 15; i++ {
			buf[i] = ' '
		}
		buf[15] = nameType
	}
	out[0] = 32
	for i := range 16 {
		c := buf[i]
		if c >= 'a' && c <= 'z' {
			c -= 32
		}
		out[1+i*2] = (c >> 4) + 'A'
		out[2+i*2] = (c & 0xf) + 'A'
	}
	out[33] = 0
}

// nsDestroy — C: ns_destroy (nmb.c:127-134)
func (m *NMBManager) nsDestroy(ns *nmbServer) {
	for i, s := range m.servers {
		if s == ns {
			m.servers = slices.Delete(m.servers, i, i+1)
			break
		}
	}
	if m.ss != nil && ns.svc != nil {
		m.ss.ServiceDestroy(ns.svc)
	}
}

// removeAllServers — C: remove_all_servers (nmb.c:141-146)
func (m *NMBManager) removeAllServers() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for len(m.servers) > 0 {
		m.nsDestroy(m.servers[0])
	}
}

// queryMasterBrowser — C: query_master_browser (nmb.c:153-206).
// Enumerates servers on the master browser and creates smb:// services.
func (m *NMBManager) queryMasterBrowser(ip [4]byte) {
	if m.enumServers == nil {
		return
	}
	na := fmt.Sprintf("%d.%d.%d.%d", ip[0], ip[1], ip[2], ip[3])
	servers := m.enumServers(na)
	if servers == nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, ns := range m.servers {
		ns.mark++
	}

	for i := 0; i+1 < len(servers); i += 2 {
		name := servers[i]
		workgroup := servers[i+1]
		var ns *nmbServer
		for _, s := range m.servers {
			if s.name == name && s.workgroup == workgroup {
				ns = s
				break
			}
		}
		if ns == nil {
			ns = &nmbServer{name: name, workgroup: workgroup}
			m.servers = slices.Insert(m.servers, 0, ns) // LIST_INSERT_HEAD

			// C: service_create_managed(id, name, url, "server", NULL,
			//   0, 0, SVC_ORIGIN_DISCOVERED)
			if m.ss != nil {
				id := fmt.Sprintf("%s/%s", workgroup, name)
				url := fmt.Sprintf("smb://%s", name)
				ns.svc = m.ss.ServiceCreateManaged(id, name, url,
					"server", "", false, false,
					service.SvcOriginDiscovered)
			}
		} else {
			ns.mark = 0
		}
	}

	// C: if(ns->ns_mark >= 2) ns_destroy(ns)
	var keep []*nmbServer
	var dead []*nmbServer
	for _, ns := range m.servers {
		if ns.mark >= 2 {
			dead = append(dead, ns)
		} else {
			keep = append(keep, ns)
		}
	}
	m.servers = keep
	for _, ns := range dead {
		if m.ss != nil && ns.svc != nil {
			m.ss.ServiceDestroy(ns.svc)
		}
	}
}

// nmbUDPInput — C: nmb_udp_input (nmb.c:212-252). Parses a NetBIOS
// name-service response.
func (m *NMBManager) nmbUDPInput(opaque any, data []byte, remoteAddr *netcore.NetAddr) {
	if len(data) < nmbpktAnswerLen {
		return
	}
	flags := binary.BigEndian.Uint16(data[2:4])
	if flags&0x8000 == 0 {
		return // not a response
	}
	if binary.BigEndian.Uint16(data[6:8]) < 1 { // answer_count
		return
	}
	if binary.BigEndian.Uint16(data[54:56]) != 6 { // length
		return
	}
	txid := binary.BigEndian.Uint16(data[0:2])
	var addr [4]byte
	copy(addr[:], data[58:62])

	if txid == m.msbTxid {
		// Master browser replied — enumerate its servers in a task
		// (C: task_run(query_master_browser, a))
		m.tasks.Run(func(opaque any) {
			m.queryMasterBrowser(opaque.([4]byte))
		}, addr)
		if m.flushTimer != nil {
			m.flushTimer.ArmDeltaSec(60)
		}
		return
	}

	m.mu.Lock()
	for i, nr := range m.sent {
		if nr.txid == txid {
			m.sent = slices.Delete(m.sent, i, i+1)
			if nr.timeout != nil {
				nr.timeout.Disarm()
			}
			nr.ip = net.IPv4(addr[0], addr[1], addr[2], addr[3])
			nr.status = 0
			m.cond.Broadcast()
			break
		}
	}
	m.mu.Unlock()
}

// nmbSendQuery — C: nmb_send_query (nmb.c:259-278). Broadcasts an NB
// name query to 255.255.255.255:137, dups+1 times. Returns the txid.
func (m *NMBManager) nmbSendQuery(name string, typ byte, dups int) uint16 {
	m.txidTally++
	txid := m.txidTally

	pkt := make([]byte, nmbpktQuestionLen)
	binary.BigEndian.PutUint16(pkt[0:2], txid)
	binary.BigEndian.PutUint16(pkt[2:4], 0x0110) // flags
	binary.BigEndian.PutUint16(pkt[4:6], 1)      // question_count
	encodeName(name, pkt[12:46], typ)
	binary.BigEndian.PutUint16(pkt[46:48], 0x20) // type = NB
	binary.BigEndian.PutUint16(pkt[48:50], 0x01) // class = IN

	// C: net_addr_t to 255.255.255.255:137
	dst := &netcore.NetAddr{Family: 4, Port: 137}
	copy(dst.Addr[:4], net.IPv4bcast.To4())
	for range dups + 1 {
		m.udpFd.UDPSend(pkt, len(pkt), dst)
	}
	return txid
}

// nmbSendMSBQuery — C: nmb_send_msb_query (nmb.c:285-290). Re-arms the
// 15s timer and broadcasts the __MSBROWSE__ query.
func (m *NMBManager) nmbSendMSBQuery() {
	if m.timer != nil {
		m.timer.ArmDeltaSec(15)
	}
	// C: nmb_send_query("\001\002__MSBROWSE__\002\001", 1, 0)
	m.msbTxid = m.nmbSendQuery("\x01\x02__MSBROWSE__\x02\x01", 1, 0)
}

// NMBResolve — C: nmb_resolve (nmb.c:297-315). Synchronous NetBIOS name
// resolution: enqueue, wake the resolver worker, block on the cond
// until a reply arrives or the 3s timeout fires.
func (m *NMBManager) NMBResolve(hostname string) (net.IP, error) {
	nr := &nmbResolve{hostname: hostname, status: 1}

	m.mu.Lock()
	m.pending = slices.Insert(m.pending, 0, nr) // LIST_INSERT_HEAD
	m.aio.WakeupWorker(m.workerID)
	for nr.status == 1 {
		m.cond.Wait()
	}
	m.mu.Unlock()

	if nr.status != 0 {
		return nil, fmt.Errorf("NetBIOS resolution failed")
	}
	return nr.ip, nil
}

// ResolveName — Go-facing wrapper returning the IP as a string.
// (C callers get the net_addr_t filled in-place.)
func (m *NMBManager) ResolveName(hostname string) (string, error) {
	ip, err := m.NMBResolve(hostname)
	if err != nil {
		return "", err
	}
	return ip.String(), nil
}

// nmbResolveTimeout — C: nmb_resolve_timeout (nmb.c:322-331)
func (m *NMBManager) nmbResolveTimeout(nr *nmbResolve) {
	m.mu.Lock()
	for i, r := range m.sent {
		if r == nr {
			m.sent = slices.Delete(m.sent, i, i+1)
			break
		}
	}
	nr.status = -1
	m.cond.Broadcast()
	m.mu.Unlock()
}

// nmbResolverProcess — C: nmb_resolver_process (nmb.c:337-352).
// Worker: drains pending requests, sends NB queries (dup=1), arms the
// 3s timeout.
func (m *NMBManager) nmbResolverProcess() {
	m.mu.Lock()
	for len(m.pending) > 0 {
		nr := m.pending[0]
		m.pending = m.pending[1:]
		m.mu.Unlock()

		m.sent = slices.Insert(m.sent, 0, nr) // LIST_INSERT_HEAD
		nr.txid = m.nmbSendQuery(nr.hostname, 0x20, 1)
		nr.timeout = &asyncio.Timer{}
		nr.timeout.Setup(m.aio, func(opaque any) {
			m.nmbResolveTimeout(opaque.(*nmbResolve))
		}, nr)
		nr.timeout.ArmDeltaSec(3)

		m.mu.Lock()
	}
	m.mu.Unlock()
}

// DiscoverServers returns the currently known servers (unmarked).
// Go-facing helper; C exposes the list only via created services.
func (m *NMBManager) DiscoverServers() []*nmbServer {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*nmbServer, len(m.servers))
	copy(out, m.servers)
	return out
}
