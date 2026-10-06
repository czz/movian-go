// fa_http.go — canonical port of src/fileaccess/fa_http.c.
//
// Implements the HTTP/HTTPS and WebDAV/WebDAVS file-access protocols and
// the http_req()/http_reqv() tagged request engine: connection pooling with
// keep-alive parking, redirect + permanent-redirect cache, RFC 2109 cookie
// jar (persisted via htsmsg_store through the cookie bridge), Basic-auth cache
// (keyring), request inspectors, chunked/gzip decoding, range reads with
// streaming-mode switchover, and WebDAV PROPFIND.

package fileaccess

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/czz/movian-go/internal/callout"
	"github.com/czz/movian-go/internal/misc"
	"github.com/czz/movian-go/internal/networking/tcpcon"
	"github.com/czz/movian-go/internal/trace"
)

// httpConnection — C: http_connection_t.
type httpConnection struct {
	fam        *FileAccessManager
	refcount   int32 // C: hc_refcount (atomic)
	hostname   string
	port       int
	id         int
	tc         *tcpcon.TCPCon
	ssl        bool
	reused     bool
	inspecting int32 // C: hc_inspecting (atomic)
	callout    callout.Callout
}

// traceRequest — C: trace_request.
func traceRequest(q *misc.HtsbufQueue, hf *httpFile) {
	fam := hf.fam
	subsys := fmt.Sprintf("HTTP-%d", hf.id)
	r := q.String()
	for line := range strings.SplitSeq(strings.ReplaceAll(r, "\r\n", "\n"), "\n") {
		if line != "" {
			fam.TraceSystem().Trace(trace.TRACE_DEBUG, subsys, "> %s", line)
		}
	}
}

// httpConnectionRelease — C: http_connection_release.
func httpConnectionRelease(hc *httpConnection) {
	if atomic.AddInt32(&hc.refcount, -1) != 0 {
		return
	}
	// C: free(hc->hc_hostname); free(hc)
}

// httpConnectionLockmgr — C: http_connection_lockmgr via callout.LockManager.
type httpConnectionLockmgr struct{}

// httpConnectionDestroy — C: http_connection_destroy.
func httpConnectionDestroy(hc *httpConnection, dbg bool, reason string) {
	hc.fam.httpTrace(dbg, "Disconnected from %s:%d (cid=%d) %s",
		hc.hostname, hc.port, hc.id, reason)
	if hc.tc != nil {
		hc.tc.TCPClose()
		hc.tc = nil
	}
	httpConnectionRelease(hc)
}

// httpConnectionGet — C: http_connection_get.
func (fam *FileAccessManager) httpConnectionGet(hostname string, port int, ssl bool,
	dbg bool, timeout int, c *misc.Cancellable,
	allowReuse bool, maxConcurrent int, verifySSL bool) (*httpConnection, error) {

	fam.httpConns.mu.Lock()

	if maxConcurrent != 0 {
		for {
			numConcurrent := 0
			for _, hc := range fam.httpConns.active {
				if hc.hostname == hostname && hc.port == port &&
					hc.ssl == ssl && atomic.LoadInt32(&hc.inspecting) == 0 {
					numConcurrent++
				}
			}
			if numConcurrent < maxConcurrent {
				break
			}
			fam.httpConns.cond.Wait()
		}
	}

	if allowReuse {
		for i, hc := range fam.httpConns.parked {
			if hc.hostname == hostname && hc.port == port && hc.ssl == ssl {
				fam.httpConns.parked = append(
					fam.httpConns.parked[:i], fam.httpConns.parked[i+1:]...)
				fam.httpConns.numParked--
				fam.httpConns.active = append(fam.httpConns.active, hc)
				if cs := fam.CalloutSystem(); cs != nil {
					cs.Disarm(&hc.callout)
				}
				fam.httpConns.mu.Unlock()
				fam.httpTrace(dbg, "Reusing connection to %s:%d (cid=%d)",
					hc.hostname, hc.port, hc.id)
				hc.reused = true
				hc.tc.TCPSetCancellable(c)
				return hc, nil
			}
		}
	}

	hc := &httpConnection{
		fam:      fam,
		refcount: 1,
		hostname: hostname,
		port:     port,
		ssl:      ssl,
	}
	fam.httpConns.active = append(fam.httpConns.active, hc)
	fam.httpConns.mu.Unlock()

	id := int(atomic.AddInt32(&fam.httpConns.connTally, 1))

	var tcpConnectFlags int
	if dbg {
		tcpConnectFlags |= tcpcon.TCPDebug
	}
	if ssl {
		tcpConnectFlags |= tcpcon.TCPSSL
	}
	if verifySSL {
		tcpConnectFlags |= tcpcon.TCPSSLVerify
	}

	fam.httpTrace(dbg, "Connecting to %s:%d", hostname, port)

	if ssl {
		fam.TraceSystem().Trace(trace.TRACE_INFO, "HTTP", "Connect to %s:%d", hostname, port)
	}

	// C: goto bad — remove hc from active list, broadcast to waiters.
	bad := func(err error) (*httpConnection, error) {
		fam.httpConns.mu.Lock()
		fam.httpConns.cond.Broadcast()
		for i, x := range fam.httpConns.active {
			if x == hc {
				fam.httpConns.active = append(
					fam.httpConns.active[:i], fam.httpConns.active[i+1:]...)
				break
			}
		}
		fam.httpConns.mu.Unlock()
		return nil, err
	}

	tc, terr := tcpcon.TCPConnect(hostname, port, timeout,
		tcpConnectFlags, c)
	if tc == nil {
		suffix := ""
		if misc.CancellableIsCancelled(c) != 0 {
			suffix = ", Cancelled by user"
		}
		fam.httpTrace(dbg, "Connection to %s:%d failed -- %s%s",
			hostname, port, terr, suffix)
		return bad(terr)
	}
	if misc.CancellableIsCancelled(c) != 0 {
		tc.TCPClose()
		return bad(errors.New("Cancelled"))
	}

	fam.httpTrace(dbg, "Connected to %s:%d (cid=%d)", hostname, port, id)

	hc.tc = tc
	hc.id = id
	return hc, nil
}

// httpConnectionKAExpired — C: http_connection_ka_expired (callout).
func (fam *FileAccessManager) httpConnectionKAExpired(c *callout.Callout, opaque any) {
	hc := opaque.(*httpConnection)
	for i, x := range fam.httpConns.parked {
		if x == hc {
			fam.httpConns.parked = append(
				fam.httpConns.parked[:i], fam.httpConns.parked[i+1:]...)
			break
		}
	}
	httpConnectionDestroy(hc, fam.Gconf().EnableHTTPDebug.Load(), "Keep alive expired")
	fam.httpConns.numParked--
}

// httpConnectionPark — C: http_connection_park.
func (fam *FileAccessManager) httpConnectionPark(hc *httpConnection, dbg bool, maxAge int, reason string) {
	hc.tc.TCPSetReadTimeout(0)
	hc.tc.TCPSetCancellable(nil)

	fam.httpTrace(dbg, "Parking connection to %s:%d (cid=%d) (expire in %ds) -- %s",
		hc.hostname, hc.port, hc.id, maxAge, reason)

	fam.httpConns.mu.Lock()

	if cs := fam.CalloutSystem(); cs != nil {
		cs.ArmManaged(&hc.callout, fam.httpConnectionKAExpired, hc,
			int64(maxAge)*1000000, httpConnectionLockmgr{}, "fa_http.go", 0)
	}

	for i, x := range fam.httpConns.active {
		if x == hc {
			fam.httpConns.active = append(
				fam.httpConns.active[:i], fam.httpConns.active[i+1:]...)
			break
		}
	}
	fam.httpConns.cond.Broadcast()
	fam.httpConns.parked = append(fam.httpConns.parked, hc)
	fam.httpConns.numParked++

	for fam.httpConns.numParked > 5 {
		hc = fam.httpConns.parked[0]
		fam.httpConns.parked = fam.httpConns.parked[1:]
		httpConnectionDestroy(hc, dbg, "Too many idle connections")
		fam.httpConns.numParked--
		if cs := fam.CalloutSystem(); cs != nil {
			cs.Disarm(&hc.callout)
		}
	}

	fam.httpConns.mu.Unlock()
}

// httpDetach — C: http_detach.
func httpDetach(hf *httpFile, reusable bool, reason string) {
	fam := hf.fam
	if hf.connection == nil {
		return
	}

	if reusable && !fam.Gconf().DisableHTTPReuse.Load() &&
		misc.CancellableIsCancelled(hf.cancellable) == 0 {
		fam.httpConnectionPark(hf.connection, hf.debug, hf.maxAge, reason)
	} else {
		fam.httpConns.mu.Lock()
		for i, x := range fam.httpConns.active {
			if x == hf.connection {
				fam.httpConns.active = append(
					fam.httpConns.active[:i], fam.httpConns.active[i+1:]...)
				break
			}
		}
		fam.httpConns.cond.Broadcast()
		fam.httpConns.mu.Unlock()
		httpConnectionDestroy(hf.connection, hf.debug, reason)
	}
	hf.connection = nil
}

// httpConnect — C: http_connect.
func httpConnect(hf *httpFile, allowReuse bool,
	maxConcurrent int) error {
	fam := hf.fam
	hf.rsize = 0

	if hf.connection != nil {
		httpDetach(hf, false, "Reconnect")
	}

	url := hf.url

	fam.httpRedirects.mu.Lock()
	for _, hr := range fam.httpRedirects.list {
		if hr.from == url {
			url = hr.to
			break
		}
	}

	if hf.retLocation != nil {
		*hf.retLocation = url
	}

	proto, auth, hostname, port, path := urlSplitStr(url)
	fam.httpRedirects.mu.Unlock()

	// C: url_split targets hf->hf_authurl[128] / hf_path[URL_MAX] —
	// urlSplitStr applies those buffer sizes.
	hf.authurl = auth
	hf.path = path

	ssl := proto == "https" || proto == "webdavs"
	if port < 0 {
		if ssl {
			port = 443
		} else {
			port = 80
		}
	}

	// empty path defaults to "/"
	if hf.path == "" {
		hf.path = "/"
	}

	timeout := hf.connectTimeout
	if timeout == 0 {
		timeout = 30000
	}

	connection, err := fam.httpConnectionGet(hostname, port, ssl,
		hf.debug, timeout, hf.cancellable, allowReuse, maxConcurrent,
		hf.sslVerify)
	hf.connection = connection

	if hf.readTimeout != 0 && hf.connection != nil {
		hf.connection.tc.TCPSetReadTimeout(hf.readTimeout)
	}

	if hf.connection != nil {
		return nil
	}
	return err
}

// httpDestroy — C: http_destroy.
func httpDestroy(hf *httpFile) {
	httpDetach(hf,
		hf.rsize == 0 && hf.connectionMode == connModePersistent,
		"Request destroyed")
	if hf.statsSpeed != nil {
		hf.statsSpeed.Release() // C: prop_ref_dec(hf->hf_stats_speed)
	}
	misc.CancellableRelease(hf.cancellable)
}
