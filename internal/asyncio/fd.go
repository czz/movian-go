package asyncio

// C: src/networking/asyncio_posix.c — canonical 1:1 port.
// asyncio_fd_t struct, fd registry, event masks, refcounted release
// (asyncio_posix.c:95-150, 334-569).

import (
	"crypto/tls"
	"net"
	"sync"
	"sync/atomic"

	miscpkg "github.com/czz/movian-go/internal/misc"
	netcore "github.com/czz/movian-go/internal/networking/core"
)

// C: asyncio event masks (asyncio.h:59-62)
const (
	AsyncIORead    = 0x1 // C: ASYNCIO_READ
	AsyncIOWrite   = 0x2 // C: ASYNCIO_WRITE
	AsyncIOError   = 0x4 // C: ASYNCIO_ERROR
	AsyncIOTimeout = 0x8 // C: ASYNCIO_TIMEOUT
)

// Compat aliases (EventRead/EventWrite/EventError/EventTimeout)
// removed — no callers remained.

// C: typedef asyncio_fd_callback_t (asyncio.h:37-38)
type FDCallback func(af *AsyncIOFD, opaque any, events,
	errno int) int

// C: typedef asyncio_udp_callback_t (asyncio.h:40-42)
type UDPCallback func(opaque any, data []byte,
	remote *netcore.NetAddr)

// C: typedef asyncio_read_callback_t (asyncio.h:44)
type ReadCallback func(opaque any, q *miscpkg.HtsbufQueue)

// C: typedef asyncio_accept_callback_t (asyncio.h:102-105).
// C delivers the accepted fd; the Go-native accept path hands the
// consumer a ready net.Conn (every consumer immediately adopts it).
type AcceptCallback func(opaque any, conn net.Conn,
	local, remote *netcore.NetAddr)

// C: typedef asyncio_error_callback_t (asyncio.h:107).
// C passes NULL for a successful connect; Go uses "".
type ErrorCallback func(opaque any, err string)

// C: struct asyncio_fd (asyncio_posix.c:97-149).
// The af union (accept/udp/error) is kept as three fields — Go has no
// unions and each is set exactly once per fd kind.
type AsyncIOFD struct {
	linkNext *AsyncIOFD  // C: LIST_ENTRY af_link
	linkPrev **AsyncIOFD //

	callback FDCallback // C: af_callback
	opaque   any
	name     string // C: af_name (strdup)

	acceptCb AcceptCallback // C: af_accept_callback (union)
	udpCb    UDPCallback    // C: af_udp_callback (union)
	errCb    ErrorCallback  // C: af_error_callback (union)
	readCb   ReadCallback   // C: af_read_callback

	sendq miscpkg.HtsbufQueue // C: af_sendq
	recvq miscpkg.HtsbufQueue // C: af_recvq

	timeout int64 // C: af_timeout (arch_get_ts domain)

	refcount int // C: af_refcount
	// C: af_fd, af_poll_events, af_pending_errno, af_ext_events —
	// removed with the poll loop: no af carries a raw fd anymore
	// (every socket is Go-native; pendingErrno was only set by the
	// deleted raw-fd do_write).
	connected uint8 // C: af_connected

	hostname string // C: af_hostname

	bindAddr netcore.NetAddr // C: af_bind_addr

	resume func(af *AsyncIOFD) // C: af_resume

	suspended bool // C: af_suspended : 1
	bindAny   bool // C: af_bind_any : 1
	broadcast bool // C: af_broadcast : 1

	// TLS — divergent adaptation: C embeds OpenSSL state in the poll
	// loop (af_ssl*, af_connected==2). Go drives crypto/tls on a
	// runtime-polled conn once the TCP handshake completes; callbacks
	// still fire on the asyncio thread. Documented divergence.
	tlsCtx  *tls.Config
	tlsConn *tls.Conn
	tlsMu   sync.Mutex

	// UDP sockets are Go-native (net.UDPConn + recv goroutine);
	// inbound datagrams are dispatched back to the asyncio thread via
	// RunTask so udpCb still runs serialized like C's poll dispatch.
	// Atomic: UDPSend may run on any goroutine (C: plain sendto).
	udpConn atomic.Pointer[net.UDPConn]

	// TCP listeners are Go-native too: net.Listener + accept goroutine
	// dispatching each conn to the asyncio thread via RunTask.
	// Loop-thread only — guarded before every Accept dispatch.
	listener net.Listener

	// Connected TCP sockets are Go-native (net.Conn — plain *TCPConn or
	// *tls.Conn). A reader goroutine posts datagrams to recvq via
	// RunTask (readCb still on the asyncio thread); a writer goroutine
	// drains sendq (C: do_write on POLLOUT). sendMu guards sendq
	// cross-thread; writeCh is the cap-1 wake signal; delCh is closed
	// by DelFD to exit the goroutines.
	tcpConn net.Conn
	sendMu  sync.Mutex
	writeCh chan struct{}
	delCh   chan struct{}

	aio *AsyncIO // owning loop (C: global asyncio state)
}

// C: static struct asyncio_fd_list asyncio_fds — lives on AsyncIO
// (aio.fdsFirst / aio.fdsMu).

// C: static void af_release (asyncio_posix.c:334-352)
func afRelease(af *AsyncIOFD) {
	// C: asyncio_verify_thread()
	af.refcount--
	if af.refcount > 0 {
		return
	}
	af.recvq.Flush()
	af.sendMu.Lock()
	af.sendq.Flush()
	af.sendMu.Unlock()
	// C: free(af_name), free(af_hostname), SSL_shutdown/SSL_free, free(af)
}

// C: events_to_poll / asyncio_set_events / asyncio_add_events /
// asyncio_rem_events / asyncio_add_fd (asyncio_posix.c:358-539) —
// removed: they existed to register raw fds in the poll set. All afs
// are now fd-less; Listen/Connect/UDPBind construct+link the af
// directly instead.

// C: void asyncio_del_fd (asyncio_posix.c:545-565)
func DelFD(af *AsyncIOFD) {
	// C: asyncio_verify_thread()
	// C: SSL_shutdown/SSL_free — TLS adaptation closes the conn
	if af.tlsConn != nil {
		af.tlsMu.Lock()
		af.tlsConn.Close()
		af.tlsConn = nil
		af.tlsMu.Unlock()
	}
	if c := af.udpConn.Swap(nil); c != nil {
		c.Close()
	}
	if af.listener != nil {
		af.listener.Close()
		af.listener = nil
	}
	if af.tcpConn != nil {
		af.tcpConn.Close()
		af.tcpConn = nil
	}
	if af.delCh != nil {
		close(af.delCh)
		af.delCh = nil
	}

	// C: LIST_REMOVE(af, af_link) — leaves af's own le_next pointing at
	// the old successor, which is what LIST_FOREACH in the asyncio_dopoll
	// build loop relies on after a callback deletes an fd. Preserve
	// that: do not clear linkNext/linkPrev here.
	if af.linkNext != nil {
		af.linkNext.linkPrev = af.linkPrev
	}
	*af.linkPrev = af.linkNext
	af.callback = nil
	afRelease(af)
}

// C: void asyncio_set_timeout_delta_sec (asyncio_posix.c:571-575)
func SetTimeoutDeltaSec(af *AsyncIOFD, delta int) {
	af.timeout = int64(delta)*1000000 + af.aio.now
}

// --- Thin method shims used by canonical-style call sites ---

// Send — C: asyncio_send (asyncio_posix.c:1049-1055)
func (af *AsyncIOFD) Send(buf []byte, length, cork int) error {
	Send(af, buf[:length], cork)
	return nil
}

// Sendq — C: asyncio_sendq
func (af *AsyncIOFD) Sendq(hq *miscpkg.HtsbufQueue, cork int) {
	Sendq(af, hq, cork)
}

// Close — C: asyncio_del_fd
func (af *AsyncIOFD) Close() { DelFD(af) }

// GetPort — C: asyncio_get_port
func (af *AsyncIOFD) GetPort() int { return GetPort(af) }

// GetHost — C: asyncio_get_host
func (af *AsyncIOFD) GetHost() string { return af.hostname }

// SetTimeoutDeltaSec — C: asyncio_set_timeout_delta_sec
func (af *AsyncIOFD) SetTimeoutDeltaSec(s int) {
	SetTimeoutDeltaSec(af, s)
}
