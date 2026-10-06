package asyncio

// C: src/networking/asyncio_posix.c — TCP section
// (asyncio_posix.c:746-1135): accept, bind, listen, resume, do_read,
// do_write, connect completion, send/sendq, attach, get_port.

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"syscall"
	"time"

	archpkg "github.com/czz/movian-go/internal/arch"
	miscpkg "github.com/czz/movian-go/internal/misc"
	netcore "github.com/czz/movian-go/internal/networking/core"
	tracepkg "github.com/czz/movian-go/internal/trace"
)

// C: static int asyncio_tcp_accept (asyncio_posix.c:747-796).
// Go-native: accept goroutine on a net.Listener, each conn dispatched
// to the asyncio thread via RunTask — acceptCb still runs serialized.
func (af *AsyncIOFD) tcpAcceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return // listener closed (DelFD/suspend)
			}
			af.aio.ts.Trace(tracepkg.TRACE_ERROR, "TCP",
				"%s: Accept error: %v", af.name, err)
			time.Sleep(time.Second) // C: sleep(1)
			continue
		}

		if tc, ok := conn.(*net.TCPConn); ok {
			// C: SO_KEEPALIVE, KEEPIDLE 30 / KEEPINTVL 15 / KEEPCNT 5,
			// TCP_NODELAY
			tc.SetKeepAlive(true)
			tc.SetKeepAlivePeriod(30 * time.Second)
			tc.SetNoDelay(true)
		}

		var local, remote netcore.NetAddr
		naFromNetAddr(&local, conn.LocalAddr())
		naFromNetAddr(&remote, conn.RemoteAddr())

		af.aio.RunTask(func(aux any) {
			if af.listener != ln {
				conn.Close() // af deleted/suspended before dispatch
				return
			}
			af.acceptCb(af.opaque, conn, &local, &remote)
		}, nil)
	}
}

// naFromNetAddr fills na from a net.Addr — C: net_addr_from_sockaddr_in.
func naFromNetAddr(na *netcore.NetAddr, a net.Addr) {
	var ip net.IP
	var port int
	switch t := a.(type) {
	case *net.TCPAddr:
		ip, port = t.IP, t.Port
	case *net.UDPAddr:
		ip, port = t.IP, t.Port
	default:
		return
	}
	na.Port = uint16(port)
	if ip4 := ip.To4(); ip4 != nil {
		na.Family = 4
		copy(na.Addr[:4], ip4)
	} else {
		na.Family = 6
		copy(na.Addr[:], ip)
	}
}

// C: static int asyncio_tcp_bind_socket (asyncio_posix.c:801-841)
// — Go-native listen socket creation preserving bindAny fallback.
func (aio *AsyncIO) asyncioTCPListen(bindAny bool, port int, name string) net.Listener {
	ln, err := net.Listen("tcp4", fmt.Sprintf(":%d", port))
	if err != nil {
		if !bindAny {
			aio.ts.Trace(tracepkg.TRACE_ERROR, "TCP",
				"%s: Bind failed -- %v", name, err)
			return nil
		}
		ln, err = net.Listen("tcp4", ":0")
		if err != nil {
			aio.ts.Trace(tracepkg.TRACE_ERROR, "TCP",
				"%s: Unable to bind -- %v", name, err)
			return nil
		}
	}
	return ln
}

// C: static void asyncio_tcp_resume (asyncio_posix.c:846-859)
func asyncioTCPResume(af *AsyncIOFD) {
	ln := af.aio.asyncioTCPListen(af.bindAny, int(af.bindAddr.Port), af.name)
	if ln == nil {
		return
	}
	af.listener = ln
	af.suspended = false
	go af.tcpAcceptLoop(ln)
	af.aio.ts.Trace(tracepkg.TRACE_INFO, "TCP",
		"%s: Resumed listening on port %d", af.name, GetPort(af))
}

// C: asyncio_fd_t *asyncio_listen (asyncio_posix.c:865-883)
func (aio *AsyncIO) Listen(name string, port int, cb AcceptCallback,
	opaque any, bindAny bool) *AsyncIOFD {
	ln := aio.asyncioTCPListen(bindAny, port, name)
	if ln == nil {
		return nil
	}

	// C: asyncio_add_fd — registration minus the raw fd
	af := &AsyncIOFD{aio: aio}
	af.recvq.HtsbufQueueSetup(0x7fffffff)
	af.sendq.HtsbufQueueSetup(0x7fffffff)
	af.refcount = 1
	af.name = name
	af.acceptCb = cb
	af.opaque = opaque
	af.bindAny = bindAny
	af.bindAddr.Port = uint16(port)
	af.listener = ln
	af.resume = asyncioTCPResume

	af.linkNext = af.aio.fdsFirst
	if af.linkNext != nil {
		af.linkNext.linkPrev = &af.linkNext
	}
	af.aio.fdsFirst = af
	af.linkPrev = &af.aio.fdsFirst
	af.aio.wake() // Go: callers may be off-loop (C: asyncio_verify_thread)

	go af.tcpAcceptLoop(ln)
	aio.ts.Trace(tracepkg.TRACE_INFO, "TCP",
		"%s: Listening on port %d", name, GetPort(af))
	return af
}

// C: static void do_write (asyncio_posix.c:889-935) — superseded by
// the tcpWriter goroutine (native conns). The raw-fd send() loop only
// ever ran for asyncio_attach'd fds, which no Go consumer uses.
// C: static void do_read (asyncio_posix.c:941-972) — superseded by
// tcpReader/tlsReader goroutines for the same reason.

// ---------------------------------------------------------------------------
// TLS adaptation (C: ENABLE_OPENSSL asyncio_ssl_* section).
// Go drives crypto/tls on a runtime-polled conn; callbacks are still
// delivered on the asyncio thread via the task queue.

// tlsStartHandshake — C: SSL_set_fd + SSL_set_*_state +
// asyncio_ssl_handshake with af_connected = 2
func tlsStartHandshake(af *AsyncIOFD, conn net.Conn, server bool) {
	var tc *tls.Conn
	if server {
		tc = tls.Server(conn, af.tlsCtx)
	} else {
		tc = tls.Client(conn, af.tlsCtx)
	}
	af.tlsConn = tc
	af.connected = 2
	af.refcount++ // held by handshake+reader goroutines
	go func() {
		err := tc.Handshake()
		af.aio.RunTask(func(aux any) {
			af := aux.(*AsyncIOFD)
			defer afRelease(af)
			if af.callback == nil {
				return
			}
			if err != nil {
				af.errCb(af.opaque, err.Error())
				return
			}
			af.connected = 1
			af.errCb(af.opaque, "")
			tlsReader(af)
			// writer goroutine drains sendq (writeCh always set on the
			// native conn path — Attach's raw-fd branch is gone)
			af.tcpConn = tc
			go af.tcpWriter(tc, af.writeCh, af.delCh)
			af.signalWrite()
		}, af)
	}()
}

// tlsReader — spawn the read pump feeding af_recvq through tasks
func tlsReader(af *AsyncIOFD) {
	tc := af.tlsConn
	af.refcount++
	go func() {
		var buf [4096]byte
		for {
			n, err := tc.Read(buf[:])
			data := slices.Clone(buf[:n])
			af.aio.RunTask(func(aux any) {
				af := aux.(*AsyncIOFD)
				defer afRelease(af)
				if af.callback == nil {
					return
				}
				if n > 0 {
					af.recvq.Append(data)
					af.readCb(af.opaque, &af.recvq)
				}
				if err != nil {
					if n == 0 {
						af.errCb(af.opaque, "Connection reset")
					}
					return
				}
			}, af)
			if err != nil {
				return
			}
		}
	}()
}

// C: static int asyncio_tcp_connected (asyncio_posix.c:978-1042).
// Native conns have fd == -1, so only the event-delivery branches can
// fire: AsyncIOTimeout (af.timeout) and AsyncIOError (af_pending_errno).
// The C read/write/connect-completion branches were raw-fd machinery —
// read/write live in tcpReader/tcpWriter, connect completion in the
// Connect dial task.
func asyncioTCPConnected(af *AsyncIOFD, opaque any, events,
	errno int) int {
	if events&AsyncIOTimeout != 0 {
		af.errCb(af.opaque, "Connection timed out")
		return 0
	}

	if events&AsyncIOError != 0 {
		af.timeout = 0
		af.errCb(af.opaque, syscall.Errno(errno).Error())
		return 0
	}
	return 0
}

// signalWrite wakes the writer goroutine — C: POLLOUT re-arm.
func (af *AsyncIOFD) signalWrite() {
	if af.writeCh == nil {
		return
	}
	select {
	case af.writeCh <- struct{}{}:
	default:
	}
}

// tcpWriter — C: do_write (asyncio_posix.c:889-935) moved to a
// dedicated goroutine so a backpressured conn.Write never stalls the
// asyncio thread. sendq is mutex-guarded; the queue itself stays
// allocated/freed on the asyncio thread.
func (af *AsyncIOFD) tcpWriter(conn net.Conn, writeCh,
	delCh chan struct{}) {
	var tmp [1024]byte
	for {
		af.sendMu.Lock()
		avail := af.sendq.Peek(tmp[:], len(tmp))
		af.sendMu.Unlock()
		if avail == 0 {
			select {
			case <-writeCh:
				continue
			case <-delCh:
				return
			}
		}
		n, err := conn.Write(tmp[:avail])
		if n > 0 {
			af.sendMu.Lock()
			af.sendq.Drop(n)
			af.sendMu.Unlock()
		}
		if err != nil {
			// C: sets af_pending_errno → AsyncIOError on next loop pass
			af.aio.RunTask(func(aux any) {
				if af.callback == nil {
					return
				}
				af.errCb(af.opaque, err.Error())
			}, nil)
			return
		}
	}
}

// tcpReader — C: do_read (asyncio_posix.c:941-972) as a goroutine;
// each datagram is appended to af.recvq and readCb invoked on the
// asyncio thread via RunTask, preserving C dispatch semantics.
func (af *AsyncIOFD) tcpReader(conn net.Conn) {
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			data := slices.Clone(buf[:n])
			af.aio.RunTask(func(aux any) {
				if af.callback == nil {
					return
				}
				if n > 0 {
					af.recvq.Append(data)
					af.readCb(af.opaque, &af.recvq)
				}
				if err != nil {
					// C: r==0 → "Connection reset",
					// error → strerror, timeout → AsyncIOTimeout msg
					msg := "Connection reset"
					if n > 0 || !errors.Is(err, io.EOF) {
						msg = err.Error()
						if nerr, ok := err.(net.Error); ok &&
							nerr.Timeout() {
							msg = "Connection timed out"
						}
					}
					af.errCb(af.opaque, msg)
				}
			}, nil)
			if err != nil {
				return
			}
		}
	}()
}

// C: void asyncio_send (asyncio_posix.c:1048-1055)
func Send(af *AsyncIOFD, buf []byte, cork int) {
	// C: asyncio_verify_thread()
	af.sendMu.Lock()
	af.sendq.Append(buf)
	af.sendMu.Unlock()
	if cork != 0 {
		return
	}
	if af.tcpConn != nil {
		af.signalWrite()
	}
}

// C: void asyncio_sendq (asyncio_posix.c:1061-1067)
func Sendq(af *AsyncIOFD, hq *miscpkg.HtsbufQueue, cork int) {
	// C: asyncio_verify_thread()
	af.sendMu.Lock()
	af.sendq.AppendQ(hq)
	af.sendMu.Unlock()
	if cork != 0 {
		return
	}
	if af.tcpConn != nil {
		af.signalWrite()
	}
}

// C: asyncio_fd_t *asyncio_connect (asyncio_posix.c:1073-1129).
// Go-native: net.Dialer on a goroutine (C: nonblocking connect +
// POLLOUT + SO_ERROR probe); the completion is dispatched back onto
// the asyncio thread via RunTask so errCb/readCb stay serialized.
// af.timeout is still delivered by the loop (af is in asyncioFds even
// with fd == -1) → AsyncIOTimeout works exactly like C.
func (aio *AsyncIO) Connect(name string, addr *netcore.NetAddr,
	errcb ErrorCallback, readCb ReadCallback, opaque any,
	timeout int, tlsctx *tls.Config, hostname string) *AsyncIOFD {

	af := &AsyncIOFD{aio: aio}
	af.recvq.HtsbufQueueSetup(0x7fffffff)
	af.sendq.HtsbufQueueSetup(0x7fffffff)
	af.refcount = 1
	af.name = name
	af.callback = asyncioTCPConnected
	af.opaque = opaque
	af.errCb = errcb
	af.readCb = readCb
	af.timeout = archpkg.GetTS() + int64(timeout)*1000 // C: timeout is ms
	af.hostname = hostname                             // C: strdup
	af.tlsCtx = tlsctx                                 // C: af_ssl = SSL_new(tlsctx)
	af.writeCh = make(chan struct{}, 1)
	af.delCh = make(chan struct{})

	af.linkNext = af.aio.fdsFirst
	if af.linkNext != nil {
		af.linkNext.linkPrev = &af.linkNext
	}
	af.aio.fdsFirst = af
	af.linkPrev = &af.aio.fdsFirst
	af.aio.wake() // Go: callers may be off-loop (C: asyncio_verify_thread)

	go func() {
		// C: connect() → EINPROGRESS → POLLOUT → SO_ERROR
		var d net.Dialer
		if timeout > 0 {
			d.Timeout = time.Duration(timeout) * time.Millisecond
		}
		conn, err := d.Dial("tcp4", addr.ToTCPAddr().String())
		af.aio.RunTask(func(aux any) {
			if af.callback == nil {
				if conn != nil {
					conn.Close()
				}
				return // af deleted while dialing
			}
			af.timeout = 0 // C: cleared when connect completes
			if err != nil {
				msg := err.Error()
				if nerr, ok := err.(net.Error); ok && nerr.Timeout() {
					msg = "Connection timed out" // C: AsyncIOTimeout
				}
				af.errCb(af.opaque, msg)
				return
			}
			if tc, ok := conn.(*net.TCPConn); ok {
				tc.SetNoDelay(true) // C: net_change_ndelay(fd, 1)
			}
			if af.tlsCtx != nil {
				tlsStartHandshake(af, conn, false)
				return
			}
			af.tcpConn = conn
			af.connected = 1
			af.errCb(af.opaque, "") // C: successful connect → NULL
			af.tcpReader(conn)
			go af.tcpWriter(conn, af.writeCh, af.delCh)
			af.signalWrite() // flush anything queued pre-connect
		}, nil)
	}()
	return af
}

// C: asyncio_fd_t *asyncio_attach (asyncio_posix.c:1183-1215) —
// removed: in C it serves http_server.c:1151 (adopting accepted fds
// onto the poll loop); the Go http_server runs its own accept loop and
// no consumer adopts raw fds onto asyncio, so the whole raw-fd
// machinery (do_read/do_write/sysSendmsgN/tlsNetConn) is dead.

// C: int asyncio_get_port (asyncio_posix.c:1221-1231)
func GetPort(af *AsyncIOFD) int {
	if af.listener != nil {
		if ta, ok := af.listener.Addr().(*net.TCPAddr); ok {
			return ta.Port
		}
		return -1
	}
	if conn := af.udpConn.Load(); conn != nil {
		// Go-native UDP socket — C: getsockname(af_fd)
		if ua, ok := conn.LocalAddr().(*net.UDPAddr); ok {
			return ua.Port
		}
		return -1
	}
	// C: getsockname(af_fd) — all remaining afs are fd-less
	// (native sockets); a raw fd only existed via asyncio_attach.
	return -1
}
