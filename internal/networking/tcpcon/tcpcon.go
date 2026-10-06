// Canonical port of the TCP layer in src/networking/net_common.c and
// src/networking/net_posix.c — tcpcon_t wraps a socket with a spill
// buffer (htsbuf_queue_t), an optional cancellable (cancel → shutdown),
// a per-read timeout (SO_RCVTIMEO), optional SOCKS5 proxying via
// gconf.proxy_host, and TLS (tcp_ssl_open).
package tcpcon

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/czz/movian-go/internal/misc"
	netcore "github.com/czz/movian-go/internal/networking/core"
)

// C: TCP_* flags (networking/net.h)
const (
	TCPSSL       = 0x1
	TCPDebug     = 0x2
	TCPNoProxy   = 0x4
	TCPSSLVerify = 0x8
)

// Gconf seams — C: gconf.proxy_host / gconf.proxy_port (main.h:315-316).
var (
	GconfProxyHost string
	GconfProxyPort int
)

// tcpconDeps — link-time seams (C: nmb_resolve direct call).
var tcpconDeps struct {
	nmbResolve func(hostname string) (net.IP, error)
}

// SetNMBResolver wires nmb_resolve (C: NetBIOS fallback when DNS fails
// on a dot-less hostname — fileaccess/smb). Provider: smb.NMBManager.
func SetNMBResolver(fn func(hostname string) (net.IP, error)) {
	tcpconDeps.nmbResolve = fn
}

// netIfLocal — C: net_get_interfaces + net_is_addr_in_netif
// (net_common.c:486-494). Returns true when the numeric IPv4 address is
// on a local interface (proxy bypass).
func netIfLocal(na *netcore.NetAddr) bool {
	ifs, err := netcore.NetGetInterfaces()
	if err != nil {
		return false
	}
	for i := range ifs {
		if netcore.NetIsAddrInNetif(&ifs[i], na) {
			return true
		}
	}
	return false
}

// NetReadCB — C: net_read_cb_t — partial-progress callback.
type NetReadCB func(opaque any, bytesDone int)

// TCPCon — C: tcpcon_t.
type TCPCon struct {
	conn        net.Conn
	spill       misc.HtsbufQueue // C: htsbuf_queue_t spill
	cancellable *misc.Cancellable
	readTimeout int // C: SO_RCVTIMEO (ms); 0 = block
	debug       bool
}

// setReadDeadline applies tc->read_timeout before each blocking read
// (C: SO_RCVTIMEO makes each recv() time out independently).
func (tc *TCPCon) setReadDeadline() {
	if tc.readTimeout != 0 {
		tc.conn.SetReadDeadline(time.Now().Add(time.Duration(tc.readTimeout) * time.Millisecond))
	}
}

// tcpRead — C: tcp_read (net_posix.c:63-87). When all!=0, loops until
// len bytes are read (MSG_WAITALL semantics), firing cb with partial
// progress. Otherwise returns after a single recv.
func (tc *TCPCon) tcpRead(buf []byte, all int, cb NetReadCB, opaque any) (int, error) {
	off := 0
	for {
		tc.setReadDeadline()
		x, err := tc.conn.Read(buf[off:])
		if x <= 0 {
			if err != nil {
				return -1, err // C: return -1 (EINTR retried by Go internals)
			}
			return -1, errors.New("read returned 0")
		}
		if all != 0 {
			off += x
			if off == len(buf) {
				return len(buf), nil
			}
			if cb != nil {
				cb(opaque, off)
			}
		} else {
			return x, nil
		}
	}
}

// TCPConnect — C: tcp_connect (net_common.c:464-536) +
// tcp_connect_arch (net_posix.c:244-327). Resolves, optionally detours
// through the SOCKS5 proxy, dials with a timeout, wraps in TLS, binds
// the cancellable.
func TCPConnect(hostname string, port int, timeout int,
	flags int, c *misc.Cancellable) (*TCPCon, error) {
	dbg := flags&TCPDebug != 0

	var tc *TCPCon
	var addr *netcore.NetAddr

	if hostname == "localhost" {
		// C: addr.na_family = 4; addr = 127.0.0.1 — never proxied
		addr = netcore.NetAddrV4Addr(127, 0, 0, 1)
	} else if GconfProxyHost != "" && flags&TCPNoProxy == 0 {
		// C: numeric IPv4 addrs on a local interface bypass the proxy
		na, nerr := netcore.NetResolveNumeric(hostname)
		direct := nerr == nil && na.Family == 4 && netIfLocal(na)
		if direct {
			addr = na
		} else {
			tc, err := TCPConnect(GconfProxyHost, GconfProxyPort,
				timeout, flags&TCPDebug|TCPNoProxy, c)
			if tc == nil {
				return nil, err
			}
			if err := tc.socksSessionSetup(hostname, port); err != nil {
				tc.TCPClose()
				return nil, err
			}
			// C: goto connected
		}
	}

	if tc == nil {
		if addr == nil {
			// C: net_resolve — gethostbyname first-address semantics
			na, err := netcore.NetResolve(hostname)
			if err != nil {
				rerr := fmt.Errorf("Unable to resolve %s -- %s",
					hostname, err)
				// C: If no dots in hostname, try NetBIOS name lookup
				if strings.IndexByte(hostname, '.') != -1 {
					return nil, rerr
				}
				na = nmbResolve(hostname)
				if na == nil {
					return nil, rerr
				}
			}
			addr = na
		}
		// connect:
		addr.Port = uint16(port)
		var err error
		tc, err = tcpConnectArch(addr, timeout, c, dbg)
		if tc == nil {
			return nil, err
		}
	}

	// connected:
	if flags&TCPSSL != 0 {
		if err := tc.tcpSSLOpen(hostname, flags&TCPSSLVerify != 0); err != nil {
			tc.TCPClose()
			return nil, err
		}
	}
	return tc, nil
}

// nmbResolve — C: nmb_resolve(hostname, &addr) — NetBIOS name lookup
// for dot-less hostnames; fills addr on success.
func nmbResolve(hostname string) *netcore.NetAddr {
	if tcpconDeps.nmbResolve == nil {
		return nil
	}
	ip, err := tcpconDeps.nmbResolve(hostname)
	if err != nil {
		return nil
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return nil
	}
	return netcore.NetAddrV4Addr(ip4[0], ip4[1], ip4[2], ip4[3])
}

// tcpConnectArch — C: tcp_connect_arch (net_posix.c:242-330). Nonblocking
// connect + poll(timeout) semantics via net.Dialer; the cancellable is
// bound BEFORE connecting so cancel aborts the in-flight connect
// (C: tcp_set_cancellable → tcp_shutdown → poll abort).
func tcpConnectArch(addr *netcore.NetAddr, timeout int,
	c *misc.Cancellable, dbg bool) (*TCPCon, error) {

	ctx := context.Background()
	var cancel context.CancelFunc
	if c != nil {
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		// C: tcp_set_cancellable(tc, c) before connect — cancel aborts
		// the poll(). Bind the ctx-cancel; rebound to tcpCancel below.
		misc.CancellableBind(c, func(any) { cancel() }, nil)
	}

	d := net.Dialer{Timeout: time.Duration(timeout) * time.Millisecond}
	conn, err := d.DialContext(ctx, "tcp", addr.ToTCPAddr().String())
	if c != nil {
		misc.CancellableUnbind(c, nil)
	}
	if err != nil {
		// C: timeout → "Connection attempt timed out"; SO_ERROR →
		// strerror(err)
		if ctx.Err() == context.Canceled {
			return nil, errors.New("Cancelled")
		}
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			return nil, errors.New("Connection attempt timed out")
		}
		return nil, err
	}

	tc := &TCPCon{conn: conn, debug: dbg}
	tc.spill.HtsbufQueueSetup(0)
	tc.TCPSetCancellable(c) // C: tc->cancellable = tcp_cancel binding
	if tcpl, ok := conn.(*net.TCPConn); ok {
		tcpl.SetNoDelay(true) // C: net_change_ndelay(fd, 1)
	}
	return tc, nil
}

// TCPFromConn — C: tcp_from_fd (net_posix.c:138-146). Wraps an existing
// socket (e.g. an accepted connection) in a tcpcon.
func TCPFromConn(conn net.Conn) *TCPCon {
	tc := &TCPCon{conn: conn}
	tc.spill.HtsbufQueueSetup(0)
	return tc
}

// tcpSSLOpen — C: tcp_ssl_open (net_openssl.c:88-118). Wraps conn in
// TLS with SNI; verify controls certificate verification.
func (tc *TCPCon) tcpSSLOpen(hostname string, verify bool) error {
	tc.conn = tls.Client(tc.conn, &tls.Config{
		ServerName:         hostname,
		InsecureSkipVerify: !verify, // C: verify flag gates openssl_verify_connection
	})
	if tc.readTimeout != 0 {
		tc.conn.SetDeadline(time.Now().Add(time.Duration(tc.readTimeout) * time.Millisecond))
	}
	if err := tc.conn.(*tls.Conn).Handshake(); err != nil {
		return fmt.Errorf("SSL connect: %s", err)
	}
	tc.conn.SetDeadline(time.Time{})
	return nil
}

// socksSessionSetup — C: socks_session_setup (net_common.c:365-458).
func (tc *TCPCon) socksSessionSetup(hostname string, port int) error {
	hostnamelen := len(hostname)
	if hostnamelen >= 255 {
		return errors.New("SOCK5 too long hostname")
	}
	buf := make([]byte, 300)

	tc.TCPWriteData([]byte{5, 1, 0})

	if tc.TCPReadData(buf[:2], nil, nil) != 0 {
		return errors.New("SOCK5 Read error")
	}
	if buf[0] != 5 || buf[1] != 0 {
		return errors.New("SOCK5 Protocol error")
	}

	buf[0] = 5
	buf[1] = 1 // Connect
	buf[2] = 0
	buf[3] = 3 // Domainname
	buf[4] = byte(hostnamelen)
	copy(buf[5:], hostname)
	buf[5+hostnamelen] = byte(port >> 8)
	buf[5+hostnamelen+1] = byte(port)

	tc.TCPWriteData(buf[:5+hostnamelen+2])

	if tc.TCPReadData(buf[:5], nil, nil) != 0 {
		return errors.New("SOCK5 Read error")
	}
	if buf[0] != 5 || buf[2] != 0 {
		return errors.New("SOCK5 protocol error")
	}
	if buf[1] != 0 {
		errmsgs := [9]string{
			1: "General SOCKS server failure",
			2: "Connection not allowed by ruleset",
			3: "Network unreachable",
			4: "Host unreachable",
			5: "Connection refused",
			6: "TTL expired",
			7: "Command not supported",
			8: "Address type not supported",
		}
		if buf[1] > 8 {
			return fmt.Errorf("SOCK5 reserved error 0x%x", buf[1])
		}
		return fmt.Errorf("SOCK5 error: %s", errmsgs[buf[1]])
	}

	var alen int // remaining bytes of address - 1
	switch buf[3] {
	case 1:
		alen = 3
	case 3:
		alen = int(buf[4])
	case 4:
		alen = 15
	default:
		return fmt.Errorf("SOCK5 unrecognized address type 0x%x", buf[3])
	}

	// Throw away bound address and port
	if tc.TCPReadDataLen(nil, alen+2, nil, nil) != 0 {
		return errors.New("SOCK5 Read error")
	}
	return nil
}

// TCPSetCancellable — C: tcp_set_cancellable (net_common.c:350-361).
// Binds cancel → tcp_cancel (shutdown) so in-flight reads abort.
func (tc *TCPCon) TCPSetCancellable(c *misc.Cancellable) {
	if tc.cancellable == c {
		return
	}
	if tc.cancellable != nil {
		misc.CancellableUnbind(tc.cancellable, tc)
		tc.cancellable = nil
	}
	if c != nil {
		tc.cancellable = misc.CancellableBind(c, tcpCancel, tc)
	}
}

// tcpCancel — C: tcp_cancel (net_common.c:337-341).
func tcpCancel(aux any) {
	aux.(*TCPCon).TCPShutdown()
}

// TCPShutdown — C: tcp_shutdown (net_posix.c:345-349).
func (tc *TCPCon) TCPShutdown() {
	if t, ok := tc.conn.(*net.TCPConn); ok {
		t.CloseRead()
		t.CloseWrite()
		return
	}
	tc.conn.Close()
}

// TCPClose — C: tcp_close (net_common.c:541-553).
func (tc *TCPCon) TCPClose() {
	tc.TCPSetCancellable(nil)
	tc.spill.Flush()
	tc.conn.Close()
}

// TCPHugeBuffer — C: tcp_huge_buffer (net_posix.c:357-362).
func (tc *TCPCon) TCPHugeBuffer() {
	if t, ok := tc.conn.(*net.TCPConn); ok {
		t.SetReadBuffer(192 * 1024)
	}
}

// TCPSetReadTimeout — C: tcp_set_read_timeout (net_posix.c:368-375).
func (tc *TCPCon) TCPSetReadTimeout(ms int) {
	tc.readTimeout = ms
}

// TCPWriteQueue — C: tcp_write_queue (net_common.c:36-49). Sends each
// htsbuf_data chunk with its own send() and drains the queue.
func (tc *TCPCon) TCPWriteQueue(q *misc.HtsbufQueue) int {
	for {
		hd := q.PopHead()
		if hd == nil {
			break
		}
		tc.TCPWriteData(hd) // C: r |= tc->write(...) — errors ignored
	}
	return 0
}

// TCPWriteQueueDontfree — C: tcp_write_queue_dontfree
// (net_common.c:58-68). Sends each chunk without draining the queue.
func (tc *TCPCon) TCPWriteQueueDontfree(q *misc.HtsbufQueue) int {
	q.EachChunk(func(hd []byte) {
		tc.TCPWriteData(hd) // C: r |= tc->write(...) — errors ignored
	})
	return 0
}

// TCPWriteData — C: tcp_write_data (net_common.c:319) → tcp_write
// (net_posix.c:44-58): a single send(); r != len → ECONNRESET.
func (tc *TCPCon) TCPWriteData(buf []byte) int {
	// C: send() once — a partial send is an error (Go's Write returns
	// err != nil whenever n < len, matching C's r != len → ECONNRESET).
	n, err := tc.conn.Write(buf)
	if err != nil || n != len(buf) {
		return -1
	}
	return 0
}

// TCPPrintf — C: tcp_printf (net_common.c:75-88). Formats into a
// 2048-byte stack buffer — output is truncated at 2047 bytes.
func (tc *TCPCon) TCPPrintf(format string, args ...any) {
	s := fmt.Sprintf(format, args...)
	if len(s) > 2047 {
		s = s[:2047]
	}
	tc.TCPWriteData([]byte(s))
}

// tcpReadIntoSpill — C: tcp_read_into_spill (net_common.c:91-127).
// Fills the tail chunk's spare capacity first, else appends a fresh
// 1000-byte chunk holding the recv result.
func (tc *TCPCon) tcpReadIntoSpill() int {
	if c := tc.spill.TailSpare(); c > 0 {
		buf := make([]byte, c)
		n, err := tc.tcpRead(buf, 0, nil, nil)
		if n < 0 || err != nil {
			return -1
		}
		tc.spill.FillTail(buf[:n])
		return 0
	}
	buf := make([]byte, 1000)
	c, err := tc.tcpRead(buf, 0, nil, nil)
	if c < 0 || err != nil {
		return -1
	}
	// C: TAILQ_INSERT_TAIL of the 1000-byte chunk — keep cap for the
	// next tail-fill (AppendPrealloc preserves cap on the tail slice).
	tc.spill.AppendPrealloc(buf[:c])
	return 0
}

// TCPReadLine — C: tcp_read_line (net_common.c:134-160). Reads a
// LF-terminated line into buf; strips trailing bytes < 32 (\r\n).
func (tc *TCPCon) TCPReadLine(buf []byte) int {
	for {
		l := tc.spill.Find(0xa)
		if l == -1 {
			if tc.tcpReadIntoSpill() < 0 {
				return -1
			}
			continue
		}
		if l >= len(buf)-1 {
			return -1
		}
		tc.spill.Read(buf, l)
		length := l
		for length > 0 && buf[length-1] < 32 {
			length--
		}
		buf[length] = 0
		tc.spill.Drop(1)
		return 0
	}
}

// TCPReadLine2 — C: tcp_read_line2 (net_common.c:164-190). Returns the
// line as a string or ""+false on error.
func (tc *TCPCon) TCPReadLine2(maxlen int) (string, bool) {
	for {
		l := tc.spill.Find(0xa)
		if l == -1 {
			if tc.tcpReadIntoSpill() < 0 {
				return "", false
			}
			continue
		}
		if l >= maxlen {
			return "", false
		}
		buf := make([]byte, l+1)
		tc.spill.Read(buf, l)
		length := l
		for length > 0 && buf[length-1] < 32 {
			length--
		}
		buf[length] = 0
		tc.spill.Drop(1)
		return string(buf[:length]), true
	}
}

// TCPReadData — C: tcp_read_data (net_common.c:195-223). Reads exactly
// bufsize bytes (spill first); buf == nil discards.
func (tc *TCPCon) TCPReadData(buf []byte, cb NetReadCB, opaque any) int {
	return tc.TCPReadDataLen(buf, len(buf), cb, opaque)
}

// TCPReadDataLen is TCPReadData with an explicit size (needed for the
// buf==NULL discard form).
func (tc *TCPCon) TCPReadDataLen(buf []byte, bufsize int, cb NetReadCB, opaque any) int {
	var r int
	if buf != nil {
		r = tc.spill.Read(buf, bufsize)
	} else {
		r = tc.spill.Drop(bufsize)
	}
	if r == bufsize {
		return 0
	}
	if buf != nil {
		if _, err := tc.tcpRead(buf[r:bufsize], 1, cb, opaque); err != nil {
			return -1
		}
		return 0
	}
	remain := bufsize - r
	tmp := make([]byte, 5000)
	for remain > 0 {
		n := min(remain, 5000)
		if _, err := tc.tcpRead(tmp[:n], 1, nil, nil); err != nil {
			return -1
		}
		remain -= n
	}
	return 0
}

// TCPReadToEOF — C: tcp_read_to_eof (net_common.c:227-245). Reads up to
// bufsize bytes, returning once a single recv completes or the stream
// ends. Returns the byte count, or -1 on hard error.
func (tc *TCPCon) TCPReadToEOF(buf []byte, cb NetReadCB, opaque any) int {
	r := tc.spill.Read(buf, len(buf))
	if r == len(buf) {
		return r
	}
	x, err := tc.tcpRead(buf[r:], 0, cb, opaque)
	if err != nil || x < 0 {
		if r > 0 {
			return r
		}
		return -1
	}
	return r + x
}

// TCPReadDataNowait — C: tcp_read_data_nowait (net_common.c:248-257).
func (tc *TCPCon) TCPReadDataNowait(buf []byte) int {
	tot := tc.spill.Read(buf, len(buf))
	if tot > 0 {
		return tot
	}
	x, err := tc.tcpRead(buf[tot:], 0, nil, nil)
	if err != nil {
		return -1
	}
	return x
}

// TCPGetFD — C: tcp_get_fd — returns the underlying conn (Go has no fd
// surface; callers that need it can use SyscallConn on *net.TCPConn).
func (tc *TCPCon) TCPGetConn() net.Conn { return tc.conn }
