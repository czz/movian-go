package asyncio

// C: src/networking/asyncio_posix.c — UDP section
// (asyncio_posix.c:1313-1450)
//
// Go-native adaptation: UDP sockets are net.UDPConn instead of raw fds
// registered in the poll loop. A recv goroutine reads datagrams and posts
// each one to the asyncio thread via RunTask, so af.udpCb still runs
// serialized on the event loop exactly like C's asyncio_udp_event.
// Lifecycle (suspend/resume/del) stays on the asyncio thread.

import (
	"context"
	"net"
	"slices"

	netcore "github.com/czz/movian-go/internal/networking/core"
	tracepkg "github.com/czz/movian-go/internal/trace"
)

// C: static int asyncio_udp_bind_socket (asyncio_posix.c:1319-1364)
func (aio *AsyncIO) asyncioUDPListen(bindAny, broadcast bool,
	na *netcore.NetAddr, name string) *net.UDPConn {
	lc := net.ListenConfig{Control: udpSockopts(broadcast)}
	var conn net.PacketConn
	var err error
	if na != nil {
		conn, err = lc.ListenPacket(context.Background(), "udp4",
			na.ToUDPAddr().String())
		if err != nil {
			if !bindAny {
				aio.ts.Trace(tracepkg.TRACE_ERROR, "UDP",
					"%s: Bind failed -- %v", name, err)
				return nil
			}
			na = nil
		}
	}
	if na == nil {
		conn, err = lc.ListenPacket(context.Background(), "udp4", ":0")
		if err != nil {
			aio.ts.Trace(tracepkg.TRACE_ERROR, "UDP",
				"%s: Unable to bind -- %v", name, err)
			return nil
		}
	}
	return conn.(*net.UDPConn)
}

// asyncioNopFD is the liveness-marker callback for af entries that are
// never polled (Go-native sockets); poll events can't reach them.
func asyncioNopFD(af *AsyncIOFD, opaque any,
	events, errno int) int {
	return 0
}

// C: static void asyncio_udp_resume (asyncio_posix.c:1370-1383)
func asyncioUDPResume(af *AsyncIOFD) {
	var na *netcore.NetAddr
	if af.bindAddr.Family != 0 {
		na = &af.bindAddr
	}
	conn := af.aio.asyncioUDPListen(af.bindAny, af.broadcast, na, af.name)
	if conn == nil {
		return
	}
	af.udpConn.Store(conn)
	af.suspended = false
	go af.udpRecv(conn)
	af.aio.ts.Trace(tracepkg.TRACE_INFO, "UDP",
		"%s: Resumed listening on port %d", af.name, GetPort(af))
}

// C: static uint8_t udp_recv_buf[8192] — shared receive buffer.
// Go copies per datagram since dispatch is queued, not inline.
func (af *AsyncIOFD) udpRecv(conn *net.UDPConn) {
	buf := make([]byte, 8192)
	for {
		n, raddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			// C: AsyncIOError path of asyncio_udp_event —
			// close fd and mark suspended (asyncio_posix.c:1393-1397)
			af.aio.RunTask(func(aux any) {
				if af.udpConn.CompareAndSwap(conn, nil) {
					af.suspended = true
				}
				conn.Close()
			}, nil)
			return
		}
		data := slices.Clone(buf[:n])
		var na netcore.NetAddr
		na.Family = 4
		na.Port = uint16(raddr.Port)
		copy(na.Addr[:4], raddr.IP.To4())
		cb, op := af.udpCb, af.opaque
		af.aio.RunTask(func(aux any) {
			if af.callback == nil {
				return // af deleted between read and dispatch
			}
			cb(op, data, &na)
		}, nil)
	}
}

// C: asyncio_fd_t *asyncio_udp_bind (asyncio_posix.c:1425-1445)
func (aio *AsyncIO) UDPBind(name string, na *netcore.NetAddr, cb UDPCallback,
	opaque any, bindAny, broadcast bool) *AsyncIOFD {
	conn := aio.asyncioUDPListen(bindAny, broadcast, na, name)
	if conn == nil {
		return nil
	}

	// C: asyncio_add_fd — same registration minus the raw fd
	af := &AsyncIOFD{aio: aio}
	af.recvq.HtsbufQueueSetup(0x7fffffff)
	af.sendq.HtsbufQueueSetup(0x7fffffff)
	af.refcount = 1
	af.name = name
	// callback==nil is the package-wide "deleted" marker (DelFD nils
	// it); the stub keeps the liveness convention working for UDP fds
	// that are never polled.
	af.callback = asyncioNopFD
	af.udpCb = cb
	af.opaque = opaque
	af.bindAny = bindAny
	af.broadcast = broadcast
	af.udpConn.Store(conn)
	if na != nil {
		af.bindAddr = *na
	}
	af.resume = asyncioUDPResume

	af.linkNext = af.aio.fdsFirst
	if af.linkNext != nil {
		af.linkNext.linkPrev = &af.linkNext
	}
	af.aio.fdsFirst = af
	af.linkPrev = &af.aio.fdsFirst
	af.aio.wake() // Go: callers may be off-loop (C: asyncio_verify_thread)

	go af.udpRecv(conn)
	aio.ts.Trace(tracepkg.TRACE_INFO, "UDP",
		"%s: Listening on port %d", name, GetPort(af))
	return af
}

// C: void asyncio_udp_send (asyncio_posix.c:1451-1462)
func UDPSend(af *AsyncIOFD, data []byte, size int,
	remoteAddr *netcore.NetAddr) {
	conn := af.udpConn.Load()
	if conn == nil {
		return
	}
	conn.WriteToUDP(data[:size], remoteAddr.ToUDPAddr())
}

// UDPSend — method form
func (af *AsyncIOFD) UDPSend(data []byte, size int,
	remote *netcore.NetAddr) {
	UDPSend(af, data, size, remote)
}
