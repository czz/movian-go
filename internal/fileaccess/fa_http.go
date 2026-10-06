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
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/czz/movian-go/internal/arch"
	"github.com/czz/movian-go/internal/misc"
	httpnet "github.com/czz/movian-go/internal/networking/http"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/trace"
	"github.com/czz/movian-go/internal/version"
)

// HTTPRequestInspectorRegister — C: http_request_inspector_register.
func (fam *FileAccessManager) HTTPRequestInspectorRegister(hri *HTTPRequestInspector) {
	fam.httpInspectors = slices.Insert(fam.httpInspectors, 0, hri)
}

// HTTPClientSetHeader — C: http_client_set_header.
func HTTPClientSetHeader(hri *HTTPRequestInspection, key, value string) {
	hri.Headers.Add(key, value, false)
}

// HTTPClientFailReq — C: http_client_fail_req.
func HTTPClientFailReq(hri *HTTPRequestInspection, reason string) {
	setErrbuf(hri.Errbuf, reason)
	hri.ForceFail = true
}

// httpTrace — C: HTTP_TRACE.
func (fam *FileAccessManager) httpTrace(dbg bool, format string, args ...any) {
	if dbg {
		fam.TraceSystem().Trace(trace.TRACE_DEBUG, "HTTP", format, args...)
	}
}

// hfTrace — C: hf_trace0 / HF_TRACE.
func (hf *httpFile) hfTrace(format string, args ...any) {
	fam := hf.fam
	if hf != nil && hf.debug {
		fam.TraceSystem().Trace(trace.TRACE_DEBUG, fmt.Sprintf("HTTP-%d", hf.id), format, args...)
	}
}

func (httpConnectionLockmgr) Lock(opaque any) {
	opaque.(*httpConnection).fam.httpConns.mu.Lock()
}

func (httpConnectionLockmgr) Unlock(opaque any) {
	opaque.(*httpConnection).fam.httpConns.mu.Unlock()
}

func (httpConnectionLockmgr) Retain(opaque any) {
	atomic.AddInt32(&opaque.(*httpConnection).refcount, 1)
}

func (httpConnectionLockmgr) Release(opaque any) {
	httpConnectionRelease(opaque.(*httpConnection))
}

// addPermanentRedirect — C: add_premanent_redirect (sic).
func (fam *FileAccessManager) addPermanentRedirect(from, to string) {
	fam.httpRedirects.mu.Lock()
	defer fam.httpRedirects.mu.Unlock()
	for i := range fam.httpRedirects.list {
		if fam.httpRedirects.list[i].from == from {
			fam.httpRedirects.list[i].to = to
			return
		}
	}
	fam.httpRedirects.list = append([]httpRedirect{{from: from, to: to}}, fam.httpRedirects.list...)
}

// HTTPDomainSetUserAgent pins a User-Agent for a host ("" clears).
func (fam *FileAccessManager) HTTPDomainSetUserAgent(host, ua string) {
	fam.httpDUA.Lock()
	defer fam.httpDUA.Unlock()
	if ua == "" {
		delete(fam.httpDUA.m, host)
	} else {
		fam.httpDUA.m[host] = ua
	}
}

// httpDomainUAGet returns the pinned UA for host, "" if none.
func (fam *FileAccessManager) httpDomainUAGet(host string) string {
	fam.httpDUA.Lock()
	defer fam.httpDUA.Unlock()
	return fam.httpDUA.m[host]
}

// httpSendVerb — C: http_send_verb.
func httpSendVerb(q *misc.HtsbufQueue, hf *httpFile, method string) {
	path := hf.path
	if strings.Contains(path, " ") {
		path = strings.ReplaceAll(path, " ", "+")
	}
	q.QPrintf("%s %s HTTP/1.%d\r\n", method, path, hf.version)
}

// httpHeadersSetup — C: http_headers_init.
func httpHeadersSetup(l *httpnet.HTTPHeaderList, hf *httpFile) {
	fam := hf.fam
	hc := hf.connection

	if hc.port != 80 && hc.port != 443 {
		// C: snprintf(str[200], "%s:%d", ...) — truncate at 199
		l.Add("Host", snprintfN(200, "%s:%d", hc.hostname, hc.port), false)
	} else {
		l.Add("Host", hc.hostname, false)
	}
	if hf.reqCompression {
		l.Add("Accept-Encoding", "gzip", false)
	} else {
		l.Add("Accept-Encoding", "identity", false)
	}

	l.Add("Accept", "*/*", false)

	conn := "keep-alive"
	if hf.wantClose {
		conn = "close"
	}
	l.Add("Connection", conn, false)
	// C: snprintf(str[200], APPNAMEUSER" %s %s", systype, appversion)
	if ua := fam.httpDomainUAGet(hc.hostname); ua != "" {
		l.Add("User-Agent", ua, false)
	} else {
		l.Add("User-Agent", snprintfN(200, "Movian Go %s %s",
			arch.GetSystemType(), version.AppVersion()), false)
	}
}

// httpHeadersSend — C: http_headers_send.
func httpHeadersSend(q *misc.HtsbufQueue, def *httpnet.HTTPHeaderList,
	user1 *httpnet.HTTPHeaderList) {
	if user1 != nil {
		def.Merge(user1)
	}
	for _, hh := range def.List {
		q.QPrintf("%s: %s\r\n", hh.Key, hh.Value)
	}
	def.Free()
	q.QPrintf("\r\n")
}

// httpRequestInspect — C: http_request_inspect.
// Returns non-nil error when the request must fail (hri_force_fail).
func httpRequestInspect(headers, cookies *httpnet.HTTPHeaderList,
	hf *httpFile, method string, parameters []string) error {

	fam := hf.fam
	hostname := hf.connection.hostname
	port := hf.connection.port

	var errbuf [256]byte
	hri := &HTTPRequestInspection{
		Method:        method,
		Parameters:    parameters,
		HF:            hf,
		Headers:       headers,
		Cookies:       cookies,
		Errbuf:        errbuf[:],
		AuthHasFailed: hf.authFailed,
	}

	atomic.AddInt32(&hf.connection.inspecting, 1)

	for _, x := range fam.httpInspectors {
		if !x.Check(hf.url, hri) {
			hf.extAuth = true
			if hri.ForceFail {
				cookies.Free()
			}
			atomic.AddInt32(&hf.connection.inspecting, -1)
			if hri.ForceFail {
				return errors.New(misc.CStr(errbuf[:]))
			}
			return nil
		}
	}

	atomic.AddInt32(&hf.connection.inspecting, -1)

	if hf.auth != "" {
		headers.Add("Authorization", hf.auth, false)
		return nil
	}

	fam.httpAuthMu.Lock()
	for _, hac := range fam.httpAuthCaches {
		if hac.hostname == hostname && hac.port == port {
			hf.auth = hac.credentials
			headers.Add("Authorization", hac.credentials, false)
			break
		}
	}
	fam.httpAuthMu.Unlock()
	return nil
}

// httpReadContent — C: http_read_content.
func httpReadContent(hf *httpFile) *misc.Buf {
	hc := hf.connection

	if hf.chunkedTransfer {
		var buf []byte
		var chunkheader [100]byte

		for {
			if hc.tc.TCPReadLine(chunkheader[:]) < 0 {
				break
			}
			csize := strtolHex(misc.CStr(chunkheader[:]))

			if csize > 0 {
				off := len(buf)
				buf = append(buf, make([]byte, csize+1)...)
				if hc.tc.TCPReadData(buf[off:off+int(csize)], nil, nil) != 0 {
					break
				}
			}
			if hc.tc.TCPReadData(chunkheader[:2], nil, nil) != 0 {
				break
			}
			if csize == 0 {
				hf.rsize = 0
				return misc.BufCreateAndCopy(len(buf), buf)
			}
		}
		hf.chunkedTransfer = false
		return nil
	}

	s := max(hf.rsize, 0)
	buf := make([]byte, s)
	if hc.tc.TCPReadData(buf, nil, nil) != 0 {
		return nil
	}
	hf.rsize = 0
	return misc.BufCreateAndCopy(int(s), buf)
}

// httpDrainContent — C: http_drain_content.
func httpDrainContent(hf *httpFile) int {
	fam := hf.fam
	if !hf.chunkedTransfer && hf.rsize < 0 {
		hf.rsize = 0
		return 0
	}

	buf := httpReadContent(hf)
	if buf == nil {
		return -1
	}

	if hf.debug {
		fam.TraceSystem().Trace(trace.TRACE_DEBUG, fmt.Sprintf("HTTP-%d", hf.id),
			"%s", hexDump(buf.C8()[:buf.Len()]))
	}
	buf.Release()

	if hf.connectionMode == connModeClose {
		httpDetach(hf, false, "Connection-mode = close")
	}
	return 0
}

// hfDrainBytes — C: hf_drain_bytes.
func hfDrainBytes(hf *httpFile, bytes int64) int {
	var chunkheader [100]byte
	hc := hf.connection

	if !hf.chunkedTransfer {
		hf.rsize -= bytes
		return hc.tc.TCPReadDataLen(nil, int(bytes), nil, nil)
	}

	for bytes > 0 {
		if hf.chunkSize == 0 {
			if hc.tc.TCPReadLine(chunkheader[:]) < 0 {
				return -1
			}
			hf.chunkSize = int(strtolHex(misc.CStr(chunkheader[:])))
		}

		readSize := min(bytes, int64(hf.chunkSize))
		if readSize > 0 {
			if hc.tc.TCPReadDataLen(nil, int(readSize), nil, nil) != 0 {
				return -1
			}
		}
		bytes -= readSize
		hf.chunkSize -= int(readSize)
		hf.rsize -= readSize

		if hf.chunkSize == 0 {
			if hc.tc.TCPReadData(chunkheader[:2], nil, nil) != 0 {
				return -1
			}
		}
	}
	return 0
}

// isDelimited — C: isdelimited.
func isDelimited(c byte, delimiter int) bool {
	if delimiter == -1 {
		return c < 33
	}
	return c == byte(delimiter)
}

// httpTokenize — C: http_tokenize. Splits buf in place; returns vec of
// subslices into buf.
func httpTokenize(buf []byte, vecsize int, delimiter int) [][]byte {
	var vec [][]byte
	i := 0
	for {
		for i < len(buf) && buf[i] != 0 &&
			(isDelimited(buf[i], delimiter) || buf[i] == 32) {
			i++
		}
		if i >= len(buf) || buf[i] == 0 {
			break
		}
		start := i
		// C: vec[n++] = buf; if(n == vecsize) break; — the last token
		// keeps the remainder verbatim (delimiters included).
		if len(vec)+1 == vecsize {
			end := len(buf)
			for j := i; j < len(buf); j++ {
				if buf[j] == 0 {
					end = j
					break
				}
			}
			vec = append(vec, buf[start:end])
			break
		}
		for i < len(buf) && buf[i] != 0 && !isDelimited(buf[i], delimiter) {
			i++
		}
		vec = append(vec, buf[start:i])
		if i >= len(buf) || buf[i] == 0 {
			break
		}
		i++ // skip the delimiter
	}
	return vec
}

// hfSetLocation — C: hf_set_location (escapes spaces in Location:).
func hfSetLocation(hf *httpFile, str string) {
	dst := make([]byte, len(str)*3+1)
	n := misc.UrlEscape(dst, len(dst), []byte(str), misc.URLEscapeSpaceOnly)
	if n > 0 && dst[n-1] == 0 {
		n--
	}
	hf.location = string(dst[:n])
}

// httpReadResponse — C: http_read_response.
func httpReadResponse(hf *httpFile, headers *httpnet.HTTPHeaderList) int {
	code := -1
	hc := hf.connection

	if headers != nil {
		headers.Free()
	}

	hf.contentEncoding = httpCEIdentity
	hf.connectionMode = connModePersistent
	hf.rsize = -1
	hf.chunkedTransfer = false
	hf.contentType = ""
	hf.maxAge = 5

	firstLine := true

	for li := 0; ; li++ {
		line, ok := hc.tc.TCPReadLine2(65536)
		if !ok {
			return -1
		}

		if firstLine {
			hf.hfTrace("%s: Response:", hf.url)
			firstLine = false
		}
		hf.hfTrace("< %s", line)

		if line == "" {
			break
		}

		if li == 0 {
			q := strings.Index(line, " ")
			for q >= 0 && q < len(line) && line[q] == ' ' {
				q++
			}
			if q < 0 {
				code = 0
			} else {
				code = misc.Atoi(line[q:])
			}
			continue
		}

		found := strings.Contains(line, ":")
		if !found {
			continue
		}
		lb := []byte(line)
		argv := httpTokenize(lb, 2, ':')
		if len(argv) != 2 {
			continue
		}
		hkey := string(argv[0])
		hval := string(argv[1])

		if headers != nil {
			headers.Add(hkey, hval, true)
		}

		lkey := strings.ToLower(hkey)
		lval := strings.ToLower(hval)

		if lkey == "transfer-encoding" {
			if lval == "chunked" {
				hf.chunkedTransfer = true
				hf.chunkSize = 0
			}
			continue
		}

		if lkey == "www-authenticate" {
			b := []byte(hval)
			av := httpTokenize(b, 2, -1)
			if len(av) != 2 {
				continue
			}
			if !strings.EqualFold(string(av[0]), "Basic") {
				continue
			}
			realm := string(av[1])
			if !strings.HasPrefix(strings.ToLower(realm), "realm=\"") {
				continue
			}
			realm = realm[len("realm=\""):]
			if q := strings.LastIndexByte(realm, '"'); q >= 0 {
				realm = realm[:q]
			} else {
				continue
			}
			hf.authRealm = realm
			continue
		}

		if lkey == "location" {
			hfSetLocation(hf, hval)
			continue
		}

		if lkey == "server" {
			// CDN network typically never change the size of a file
			if lval == "akamaighost" {
				hf.filesizeIsFinal = true
			}
			continue
		}

		if lkey == "keep-alive" {
			// C: strstr(argv[1], "timeout=") — case-sensitive
			if _, after, ok := strings.Cut(hval, "timeout="); ok {
				hf.maxAge = misc.Atoi(after)
			}
			continue
		}

		if lkey == "content-encoding" {
			if lval == "gzip" {
				hf.contentEncoding = httpCEGzip
			} else {
				hf.contentEncoding = httpCEIdentity
			}
		}
		if lkey == "content-length" {
			i64 := strtoll0(hval) // C: strtoll(argv[1], NULL, 0)
			hf.rsize = i64
			if code == 200 {
				hf.filesize = i64
			}
		}

		if lkey == "content-type" {
			hf.contentType = hval
		}

		if code == 206 && lkey == "content-range" && hf.filesize == -1 {
			if len(hval) >= 5 && strings.EqualFold(hval[:5], "bytes") {
				if _, after, ok := strings.Cut(hval, "/"); ok {
					hf.filesize = strtoll0(after) // C: strtoll base 0
				}
			}
		}

		if lkey == "connection" {
			if lval == "close" {
				hf.connectionMode = connModeClose
			}
		}

		if lkey == "set-cookie" {
			httpCookieSet(hval, hf)
		}
	}

	if code >= 200 && code < 400 {
		hf.authFailed = 0
		httpAuthCacheSet(hf)
	}
	return code
}

// redirect — C: redirect.
func httpRedirectF(hf *httpFile, redircount *int,
	code int, expectContent bool) error {
	*redircount++
	if *redircount == 10 {
		return errors.New("Too many redirects")
	}

	if hf.location == "" {
		return errors.New("Redirect response without location")
	}

	suffix := ""
	if code == 301 {
		suffix = ", (premanent)" // C: "premanent" (sic)
	}
	hf.hfTrace("%s: Following redirect to %s%s", hf.url, hf.location, suffix)

	hc := hf.connection
	proto := "http"
	if hc.ssl {
		proto = "https"
	}
	newurl := misc.UrlResolveRelative(proto, hc.hostname, hc.port,
		hf.path, hf.location)

	if code == 301 {
		hf.fam.addPermanentRedirect(hf.url, newurl)
	} else {
		if hf.originalURL == "" {
			hf.originalURL = hf.url
		}
	}
	hf.url = newurl
	hf.location = ""

	if expectContent && httpDrainContent(hf) != 0 {
		hf.connectionMode = connModeClose
	}

	// Location changed, must detach from connection — it might still be
	// reusable if hostname+port is the same (decided on reconnect).
	httpDetach(hf, hf.connectionMode == connModePersistent,
		"Location changed")
	return nil
}

// httpSetReadTimeout — C: http_set_read_timeout (fap_set_read_timeout).
func httpSetReadTimeout(fh *Handle, ms int) {
	hf, ok := fh.reader.(*httpFile)
	if !ok || hf == nil {
		if w, ok2 := fh.reader.(interface{ SetReadTimeout(int) }); ok2 {
			w.SetReadTimeout(ms)
		}
		return
	}
	hf.readTimeout = ms
	hf.connectTimeout = ms
	if hf.connection != nil {
		hf.connection.tc.TCPSetReadTimeout(ms)
	}
}

// httpOpen0 — C: http_open0.
func httpOpen0(hf *httpFile, probe bool,
	nonInteractive *int) error {
	var code int
	redircount := 0

reconnect:
	hf.filesize = -1

	if err := httpConnect(hf, true, 0); err != nil {
		return err
	}
	if !probe && hf.filesize != -1 {
		return nil
	}

	var q misc.HtsbufQueue
	q.HtsbufQueueSetup(0)

again:
	var headers, cookies httpnet.HTTPHeaderList

	httpHeadersSetup(&headers, hf)

	if hf.streaming {
		httpSendVerb(&q, hf, "GET")
		hf.connection.tc.TCPHugeBuffer()
	} else {
		httpSendVerb(&q, hf, "GET")
		q.QPrintf("Range: bytes=0-4095\r\n")
	}

	if err := httpRequestInspect(&headers, &cookies, hf, "GET", nil); err != nil {
		return err
	}

	httpCookieAppend(hf.connection.hostname, hf, &headers, &cookies)
	cookies.Free()

	hf.hfTrace("Sending open request for %s (cid=%d)",
		hf.url, hf.connection.id)

	httpHeadersSend(&q, &headers, hf.userReqHeaders)

	if hf.debug {
		traceRequest(&q, hf)
	}

	hf.connection.tc.TCPWriteQueue(&q)

	code = httpReadResponse(hf, hf.userRespHeaders)
	if code == -1 && hf.connection.reused {
		httpDetach(hf, false, "Read error on reused connection, retrying")
		goto reconnect
	}

	hf.statusCode = code

	switch code {
	case 200:
		if hf.filesize == -1 {
			hf.rsize = int64(^uint64(0) >> 1) // INT64_MAX
		}
		hf.noRanges = true
		hf.hfTrace("Opened in streaming mode")
		return nil

	case 206:
		if hf.filesize == -1 {
			hf.hfTrace("%s: No known filesize, seeking may be slower", hf.url)
		}
		return nil

	case 301, 302, 303, 307:
		if err := httpRedirectF(hf, &redircount, code, true); err != nil {
			return err
		}
		if hf.connectionMode == connModeClose {
			httpDetach(hf, false, "Redirect")
		}
		goto reconnect

	case 401:
		if err := httpAuthenticate(hf, nonInteractive, true); err != nil {
			return err
		}
		if hf.connectionMode == connModeClose {
			httpDetach(hf, false, "Connection-mode = close")
			goto reconnect
		}
		goto again

	case 405:
		return errors.New("Unsupported method")

	case -1:
		return errors.New("Server reset connection")

	default:
		return fmt.Errorf("Unhandled HTTP response %d", code)
	}
}

// httpOpenEx — C: http_open_ex.
func httpOpenEx(fam *FileAccessManager, url string, nonInteractive *int,
	flags int, foe *OpenExtra) (*httpFile, error) {
	hf := &httpFile{fam: fam}
	hf.id = int(atomic.AddInt32(&fam.httpConns.fileTally, 1))
	hf.version = 1
	hf.url = url
	if flags&FaNoDebug == 0 {
		if flags&FaDebug != 0 || fam.Gconf().EnableHTTPDebug.Load() {
			hf.debug = true
		}
	}
	hf.streaming = flags&FaStreaming != 0
	hf.noRetries = flags&FaNoRetries != 0
	hf.noCookies = flags&FaNoCookies != 0
	hf.sslVerify = flags&FaSSLVerify != 0

	if foe != nil {
		if foe.Stats != nil {
			if statsProp, ok := foe.Stats.(*propcore.Prop); ok && statsProp != nil {
				// C: hf_stats_speed = prop_create_r(foe_stats, "bitrate")
				// prop_create_r → prop_create_ex(..., incref=1): the
				// caller's extra ref is balanced by prop_ref_dec in
				// http_destroy (hf.statsSpeed.Release()).
				pm := statsProp.Manager()
				if pm != nil {
					hf.statsSpeed = pm.RefInc(pm.CreateEx(statsProp,
						"bitrate", nil, false, true))
				}
				// C: prop_set(foe_stats, "bitrateValid", PROP_SET_INT, 1)
				statsProp.CreateInt("bitrateValid", 1)
			}
		}
		hf.userReqHeaders = foeHeaderList(foe.RequestHeaders)
		if hf.userReqHeaders == nil {
			hf.userReqHeaders = foe.RequestHeadersList
		}
		// C: hf_user_response_headers = foe->foe_response_headers (caller's
		// list, updated live by read_response). Prefer the canonical list;
		// fall back to an internal list mirrored into the map post-open.
		if foe.ResponseHeadersList != nil {
			hf.userRespHeaders = foe.ResponseHeadersList
		} else if foe.ResponseHeaders != nil {
			hf.userRespHeaders = &httpnet.HTTPHeaderList{}
		}
		hf.cancellable = misc.CancellableRetain(foeCancellable(foe))
		hf.connectTimeout = foe.OpenTimeout
	}

	err := httpOpen0(hf, true, nonInteractive)
	if err == nil {
		if foe != nil && foe.ResponseHeaders != nil && hf.userRespHeaders != nil {
			for _, hh := range hf.userRespHeaders.List {
				foe.ResponseHeaders[hh.Key] = hh.Value
			}
		}
		return hf, nil
	}
	if foe != nil {
		foe.ProtocolError = hf.statusCode
	}
	httpDestroy(hf)
	return nil, err
}

// foeHeaderList converts an OpenExtra header map into the canonical
// header list (C: foe_request_headers is a struct http_header_list *).
func foeHeaderList(m map[string]string) *httpnet.HTTPHeaderList {
	if m == nil {
		return nil
	}
	l := &httpnet.HTTPHeaderList{}
	for k, v := range m {
		l.Add(k, v, false)
	}
	return l
}

// foeCancellable extracts *misc.Cancellable from OpenExtra.Cancellable.
func foeCancellable(foe *OpenExtra) *misc.Cancellable {
	if foe == nil {
		return nil
	}
	if c, ok := foe.Cancellable.(*misc.Cancellable); ok {
		return c
	}
	return nil
}

// httpOpen — C: http_open.
// httpOpen — C: http_open (fap_open slot). The int statcode callers
// used for "HTTP error %d" rides inside the error (fapError).
func httpOpen(fap *FAProtocol, url string, flags int,
	foe *OpenExtra) (*Handle, error) {
	fam := fap.fam
	statcode := -1
	var ni *int
	if flags&FaNonInteractive != 0 {
		ni = &statcode
	}
	hf, err := httpOpenEx(fam, url, ni, flags, foe)
	if hf == nil {
		msg := err.Error()
		if msg == "" {
			msg = fmt.Sprintf("HTTP error %d", statcode)
		}
		return nil, fapError{code: statcode, msg: msg}
	}
	h := &Handle{
		fam:    fam,
		reader: hf,
		seeker: hf,
		url:    url,
		size:   hf.filesize,
	}
	h.fap = fap
	h.sizer = func() int64 { return hf.filesize }
	return h, nil
}

// Read — io.Reader on the Handle (C: http_read via fap_read).
func (hf *httpFile) Read(buf []byte) (int, error) {
	r := httpRead(hf, buf)
	if r < 0 {
		if hf.err != nil {
			return 0, hf.err
		}
		return 0, errHTTPRead
	}
	if r == 0 {
		return 0, io.EOF
	}
	return r, nil
}

// Close — io.Closer (C: http_close).
func (hf *httpFile) Close() error {
	httpDestroy(hf)
	return nil
}

// Seek — io.Seeker (C: http_seek with lazy=0).
func (hf *httpFile) Seek(pos int64, whence int) (int64, error) {
	return hf.Seek4(pos, whence, false)
}

// httpResetURL — C: http_reset_url (revert to original URL after temp
// redirects — issue #2591).
func httpResetURL(hf *httpFile) {
	if hf.originalURL != "" {
		hf.url = hf.originalURL
		hf.hfTrace("read() reverting to original URL %s", hf.url)
	}
}

// httpReadI — C: http_read_i.
func httpReadI(hf *httpFile, buf []byte) int {
	size := len(buf)
	var q misc.HtsbufQueue
	var code int
	var hc *httpConnection
	var chunkheader [100]byte
	totsize := 0
	var readSize int64

	redircount := 0

	if size == 0 {
		return 0
	}

	// Max 5 retries
	for i := 0; i < 5; i++ {
	retry:
		// If not connected, try to (re-)connect
		if hf.filesize != -1 && hf.pos >= hf.filesize {
			return totsize // Reading outside known filesize
		}

		if hf.connection == nil {
			if hf.noRetries {
				return -1
			}
			if httpConnect(hf, true, 0) != nil {
				return -1
			}
		}
		hc = hf.connection

		if hf.rsize > 0 {
			// Pending data on the socket — can't read more than
			// is available.
			readSize = min(int64(size-totsize), hf.rsize)
		} else {
			hf.hfTrace("read() needs to send a new GET request (cid=%d)",
				hc.id)
			readSize = int64(size - totsize)

			// Must send a new request
			q.HtsbufQueueSetup(0)

			httpSendVerb(&q, hf, "GET")

			var headers, cookies httpnet.HTTPHeaderList
			httpHeadersSetup(&headers, hf)
			if err := httpRequestInspect(&headers, &cookies, hf, "GET",
				nil); err != nil {
				httpDetach(hf, false, err.Error())
				return -1
			}

			var rangestr string
			if hf.filesize == -1 || hf.noRanges {
				rangestr = ""
			} else if hf.streaming || hf.consecutiveRead > streamingLimit {
				if !hf.streaming {
					hf.hfTrace("%s: switching to streaming mode", hf.url)
				}
				rangestr = fmt.Sprintf("bytes=%d-", hf.pos)
				hc.tc.TCPHugeBuffer()
			} else {
				end := min(hf.pos+readSize, hf.filesize)
				rangestr = fmt.Sprintf("bytes=%d-%d", hf.pos, end-1)
			}
			if rangestr != "" {
				q.QPrintf("Range: %s\r\n", rangestr)
			}

			httpCookieAppend(hc.hostname, hf, &headers, &cookies)
			cookies.Free()
			hf.hfTrace("Read issuing new request for %s (cid=%d)",
				hf.url, hc.id)
			httpHeadersSend(&q, &headers, hf.userReqHeaders)
			if hf.debug {
				traceRequest(&q, hf)
			}

			hc.tc.TCPWriteQueue(&q)

			code = httpReadResponse(hf, hf.userRespHeaders)
			if code == -1 && hf.connection.reused {
				httpDetach(hf, false,
					"Read error on reused connection, retrying")
				httpResetURL(hf)
				goto retry
			}

			switch code {
			case 206:
				// Range transfer OK

			case 301, 302, 303, 307:
				if httpRedirectF(hf, &redircount, code, true) != nil {
					return -1
				}
				if hf.connectionMode == connModeClose {
					httpDetach(hf, false, "Redirect")
				}
				continue

			case 200:
				if rangestr != "" {
					hf.noRanges = true
				}
				if hf.rsize == -1 {
					hf.rsize = int64(^uint64(0) >> 1)
				}
				if hf.pos != 0 {
					hf.hfTrace("Skipping by reading %d bytes, rsize=%d",
						hf.pos, hf.rsize)
					if hfDrainBytes(hf, hf.pos) != 0 {
						httpDetach(hf, false, "Read error during drain")
						continue
					}
					hf.hfTrace("rsize is now = %d", hf.rsize)
				}

			case 416:
				hf.noRanges = true
				httpDetach(hf, false, "Requested Range Not Satisfiable")
				httpResetURL(hf)
				continue

			default:
				hf.hfTrace("Read error (%d) [%s] filesize %d -- retrying",
					code, rangestr, hf.filesize)
				httpDetach(hf, false, "Read error")
				httpResetURL(hf)
				continue
			}

			if hf.rsize < readSize {
				readSize = hf.rsize
			}
		}

		if hf.filesize == -1 && hf.streaming && !hf.chunkedTransfer {
			// Read until EOF
			for readSize > 0 {
				r := hc.tc.TCPReadToEOF(buf[totsize:totsize+int(readSize)],
					nil, nil)
				if r < 0 {
					return totsize
				}
				readSize -= int64(r)
				hf.pos += int64(r)
				totsize += r
				hf.consecutiveRead += int64(r)
			}
			return totsize
		}

		if hf.chunkedTransfer {
			if hf.chunkSize == 0 {
				if hc.tc.TCPReadLine(chunkheader[:]) < 0 {
					goto bad
				}
				hf.chunkSize = int(strtolHex(misc.CStr(chunkheader[:])))
			}
			// C: read_size = MIN(size - totsize, hf->hf_chunk_size)
			readSize = min(int64(hf.chunkSize), int64(size-totsize))
		}

		if readSize > 0 {
			if hc.tc.TCPReadData(buf[totsize:totsize+int(readSize)],
				nil, nil) != 0 {
				// Fail — disconnect, but retry a couple of times
				httpDetach(hf, false, "Read error during fa_read()")
				httpResetURL(hf)
				continue
			}
			hf.pos += readSize
			hf.rsize -= readSize
			totsize += int(readSize)
			hf.consecutiveRead += readSize
		} else {
			hf.rsize = 0
		}

		if hf.chunkedTransfer {
			hf.chunkSize -= int(readSize)
			if hf.chunkSize == 0 {
				if hc.tc.TCPReadData(chunkheader[:2], nil, nil) != 0 {
					goto bad
				}
			}
		}

		if readSize == 0 {
			return totsize
		}

		if hf.rsize == 0 && hf.connectionMode == connModeClose {
			httpDetach(hf, false, "Connection-mode = close")
		}

		if totsize != size {
			i--
			continue
		}
		return totsize
	}
bad:
	httpDetach(hf, false, "Error during fa_read()")
	return -1
}

// httpRead — C: http_read (with download-rate stats).
func httpRead(hf *httpFile, buf []byte) int {
	if hf.statsSpeed == nil {
		return httpReadI(hf, buf)
	}
	r := httpReadI(hf, buf)
	// C: hf->hf_bytes_downloaded += r — unconditional (a negative r
	// underflows the u64, same as C's uint64_t wraparound).
	hf.bytesDownloaded += uint64(int64(r))
	now := int(time.Now().Unix())
	hf.downloadRate.AverageFill(now, int64(hf.bytesDownloaded))
	rate := hf.downloadRate.AverageRead(now) / 125
	hf.statsSpeed.SetInt(rate)
	return r
}

// Seek4 — C: http_seek (fap_seek with lazy).
func (hf *httpFile) Seek4(pos int64, whence int, lazy bool) (int64, error) {
	hc := hf.connection
	var np int64

	switch whence {
	case io.SeekStart:
		np = pos
	case io.SeekCurrent:
		np = hf.pos + pos
	case io.SeekEnd:
		if hf.filesize == -1 {
			hf.hfTrace("%s: Refusing to seek to END on non-seekable file",
				hf.url)
			return -1, errors.New("seek to end on non-seekable file")
		}
		np = hf.filesize + pos
	default:
		return -1, errors.New("invalid whence")
	}

	if np < 0 {
		return -1, errors.New("negative seek position")
	}

	if hf.filesizeIsFinal && hf.filesize != -1 && np > hf.filesize {
		return -1, errors.New("seek beyond final filesize")
	}

	if hf.pos != np && hc != nil {
		hf.consecutiveRead = 0

		if hf.rsize != 0 {
			// Data pending on socket — small forward deltas are
			// satisfied by reading.
			d := np - hf.pos
			if d > 0 && (d < seekByReadThres || (hf.noRanges && !lazy)) &&
				d < hf.rsize {
				if hfDrainBytes(hf, d) == 0 {
					hf.pos = np
					return np, nil
				}
				httpDetach(hf, false, "Disconnected while draining")
				hf.pos = np
				return np, nil
			}
			if lazy && hf.noRanges {
				return -1, errors.New("lazy seek on non-range server")
			}
			// Still stale data on the socket — disconnect
			httpDetach(hf, false, "Seeking during streaming")
		}
		if lazy && hf.noRanges {
			return -1, errors.New("lazy seek on non-range server")
		}
	}
	hf.pos = np
	return np, nil
}

// httpFsize — C: http_fsize.
func (hf *httpFile) fsize() int64 { return hf.filesize }

// httpStat — C: http_stat.
func httpStat(fap *FAProtocol, url string, flags int) (*FileStat, error) {
	fam := fap.fam
	statcode := -1
	var ni *int
	if flags&FaNonInteractive != 0 {
		ni = &statcode
	}
	hf, err := httpOpenEx(fam, url, ni, flags, nil)
	if hf == nil {
		return nil, fapError{code: statcode, msg: err.Error()}
	}
	st := &FileStat{} // C: memset(fs, 0, sizeof(struct fa_stat))
	st.Type = ContentFile
	st.Size = hf.filesize
	httpDestroy(hf)
	return st, nil
}

// httpLoad — C: http_load.
func httpLoad(fap *FAProtocol, url string,
	etag *string, mtime *time.Time, maxAge *int, flags int,
	cb FALoadCB, opaque, c any,
	reqHeadersI, respHeadersI any, location *string,
	protocolCode *int) (*Buffer, error) {

	var errbuf [256]byte
	var headersIn, headersOut httpnet.HTTPHeaderList

	requestHeaders, _ := reqHeadersI.(*httpnet.HTTPHeaderList)
	if requestHeaders == nil {
		requestHeaders = &headersIn
	}
	responseHeaders, _ := respHeadersI.(*httpnet.HTTPHeaderList)
	if responseHeaders == nil {
		responseHeaders = &headersOut
	}

	if mtime != nil && !mtime.IsZero() {
		requestHeaders.Add("If-Modified-Since",
			httpnet.HTTPAsctime(*mtime, nil, 40), false)
	}

	if etag != nil && *etag != "" {
		requestHeaders.Add("If-None-Match", *etag, false)
	}

	var b *misc.Buf
	code := fap.fam.HTTPReq(url,
		HTTPTagResultPtr, &b,
		HTTPTagErrbuf, errbuf[:],
		HTTPTagFlags, flags,
		HTTPTagResponseHeaders, responseHeaders,
		HTTPTagRequestHeaders, requestHeaders,
		HTTPTagProgressCallback, cb, opaque,
		HTTPTagCancellable, c,
		HTTPTagLocation, location,
		HTTPTagResponseCode, protocolCode)

	switch {
	case code == -1:
		b = nil
	case code == 304:
		b = nil
	default:
		if b != nil {
			if s, ok := responseHeaders.Get("content-type"); ok {
				b.SetContentType(s)
			}
		}

		if mtime != nil {
			*mtime = time.Time{}
			if s, ok := responseHeaders.Get("last-modified"); ok {
				if t, r := httpnet.HTTPCtime(s); r == 0 {
					*mtime = t
				}
			}
		}

		if etag != nil {
			if s, ok := responseHeaders.Get("etag"); ok {
				*etag = s
			} else {
				*etag = ""
			}
		}

		if maxAge != nil {
			s, ok1 := responseHeaders.Get("date")
			s2, ok2 := responseHeaders.Get("expires")
			if ok1 && ok2 {
				var sdate, expires time.Time
				var r1, r2 int
				sdate, r1 = httpnet.HTTPCtime(s)
				expires, r2 = httpnet.HTTPCtime(s2)
				if r1 == 0 && r2 == 0 {
					*maxAge = int(expires.Unix() - sdate.Unix())
				}
			}
			// C: strstr is case-sensitive on the raw header value
			if s, ok := responseHeaders.Get("cache-control"); ok {
				if _, after, ok := strings.Cut(s, "max-age="); ok {
					*maxAge = misc.Atoi(after)
				}
				if strings.Contains(s, "no-cache") ||
					strings.Contains(s, "no-store") {
					*maxAge = 0
				}
			}
		}
	}

	// C: free the local lists only when they were used as defaults
	if requestHeaders == &headersIn {
		headersIn.Free()
	}
	if responseHeaders == &headersOut {
		headersOut.Free()
	}

	if b == nil {
		if code == 304 {
			return nil, ErrLoadNotModified
		}
		if code != 0 && code != -1 {
			return nil, fmt.Errorf("HTTP error: %d", code)
		}
		msg := misc.CStr(errbuf[:])
		if msg == "" {
			msg = "HTTP request failed"
		}
		return nil, errors.New(msg)
	}
	return bufToBuffer2(b), nil
}

// bufToBuffer2 converts a misc.Buf into a fileaccess Buffer.
func bufToBuffer2(b *misc.Buf) *Buffer {
	if b == nil {
		return nil
	}
	out := &Buffer{Data: slices.Clone(b.C8()[:b.Len()]),
		Size: b.Len(), ContentType: b.GetContentType()}
	b.Release()
	return out
}

// httpGetLastComponent — C: http_get_last_component.
func httpGetLastComponent(fap *FAProtocol, url string, dst []byte) {
	e := 0
	for e < len(url) && url[e] != 0 && url[e] != '?' {
		e++
	}
	if e > 0 && url[e-1] == '/' {
		e--
	}
	if e > 0 && url[e-1] == '|' {
		e--
	}
	if e == 0 {
		if len(dst) > 0 {
			dst[0] = 0
		}
		return
	}
	b := e
	for b > 0 {
		b--
		if url[b] == '/' {
			b++
			break
		}
	}
	l := min(len(dst), e-b+1)
	if l > 0 {
		copy(dst, url[b:b+l-1])
		dst[l-1] = 0
		misc.UrlDeescape(dst[:l])
	}
}

// httpNoParking — C: http_no_parking.
func httpNoParking(fh *Handle) bool {
	hf, ok := fh.reader.(*httpFile)
	if !ok || hf == nil {
		return true
	}
	return hf.connectionMode == connModeClose
}

// httpStart — C: http_init once-guard (fa_http.c).
func (fam *FileAccessManager) httpStart() {
	fam.httpStartOnce.Do(fam.loadCookies)
}

// fieldFind — C: htsmsg_field_find.
func (m *DAVXMLMap) fieldFind(name string) *DAVXMLField {
	if m == nil {
		return nil
	}
	for _, f := range m.Fields {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// getMap — C: htsmsg_get_map. A field is a map only when it has no
// text value (C: hmf_type == HMF_MAP; text-bearing elements are
// HMF_STR even when children are retained).
func (m *DAVXMLMap) getMap(name string) *DAVXMLMap {
	f := m.fieldFind(name)
	if f == nil || f.HasStr {
		return nil
	}
	return f.Childs
}

// getMapMulti — C: htsmsg_get_map_multi (traverse successive names).
func (m *DAVXMLMap) getMapMulti(names ...string) *DAVXMLMap {
	for _, n := range names {
		m = m.getMap(n)
		if m == nil {
			return nil
		}
	}
	return m
}

// getStr — C: htsmsg_get_str (element text or attribute value).
func (m *DAVXMLMap) getStr(name string) (string, bool) {
	f := m.fieldFind(name)
	if f == nil || !f.HasStr {
		return "", false
	}
	return f.Str, true
}

// httpRequestPartial — C: http_request_partial (net_read_cb_t).
func httpRequestPartial(opaque any, amount int) {
	hra := opaque.(*HTTPReqAux)
	amount += int(hra.bytesCompleted)
	if hra.cb != nil {
		hra.cb(hra.opaque, amount, int(hra.total))
	}
}

// HTTPReqv — C: http_reqv. args is a flat tagged list: each tag is
// followed by its payload (see HTTPTag* constants).
func (fam *FileAccessManager) HTTPReqv(url string, args []any,
	asyncCallback func(hra *HTTPReqAux, opaque any, error int),
	asyncOpaque any) int {
	hf := &httpFile{fam: fam}
	hra := &HTTPReqAux{}
	hf.id = int(atomic.AddInt32(&fam.httpConns.fileTally, 1))
	atomic.StoreInt32(&hra.refcount, 1)

	hra.decodedData = appendWaste
	hra.postdata.HtsbufQueueSetup(0)

	for i := 0; i < len(args); {
		tag, _ := args[i].(int)
		i++
		switch tag {
		case HTTPTagArg: // key, val
			key, _ := args[i].(string)
			val, _ := args[i+1].(string)
			i += 2
			if key != "" && val != "" {
				hra.queryArgs = append(hra.queryArgs,
					httpQueryArg{key: key, val: val, valLen: len(val)})
			}

		case HTTPTagArgInt: // key, int
			key, _ := args[i].(string)
			v, _ := args[i+1].(int)
			i += 2
			if key != "" {
				s := strconv.Itoa(v)
				hra.queryArgs = append(hra.queryArgs,
					httpQueryArg{key: key, val: s, valLen: len(s)})
			}

		case HTTPTagArgInt64: // key, int64
			key, _ := args[i].(string)
			v, _ := args[i+1].(int64)
			i += 2
			if key != "" {
				s := strconv.FormatInt(v, 10)
				hra.queryArgs = append(hra.queryArgs,
					httpQueryArg{key: key, val: s, valLen: len(s)})
			}

		case HTTPTagArgBin: // key, []byte
			key, _ := args[i].(string)
			val, _ := args[i+1].([]byte)
			i += 2
			if key != "" {
				hra.queryArgs = append(hra.queryArgs, httpQueryArg{
					key: key, val: string(val), valLen: len(val)})
			}

		case HTTPTagArgList: // []string (k,v pairs)
			arguments, _ := args[i].([]string)
			i++
			if arguments != nil {
				hra.arguments = slices.Clone(arguments)
			}

		case HTTPTagResultPtr: // **misc.Buf or HTTPBufferInternally
			ptr := args[i]
			i++
			if ptr == nil {
				break // C: if(ptr == NULL) break;
			}
			if p, ok := ptr.(**misc.Buf); ok && p == nil {
				break // typed nil — same as NULL
			}
			// C: decoded_opaque = calloc(1, sizeof(buf_t)); refcount=1
			b := misc.BufCreate(0)
			hra.decodedOpaque = b
			hra.decodedData = appendBuf
			hra.decodedCleanup = cleanupBuf
			hra.wantResult = true
			if p, ok := ptr.(**misc.Buf); ok {
				*p = b
				hra.resultPtr = p
			} else {
				// C: ptr == HTTP_BUFFER_INTERNALLY → ptr = &hra->result
				hra.result = b
			}

		case HTTPTagErrbuf: // []byte
			errbuf, _ := args[i].([]byte)
			i++
			hra.errbuf = errbuf

		case HTTPTagPostData: // *misc.HtsbufQueue, contentType string
			hq, _ := args[i].(*misc.HtsbufQueue)
			key, _ := args[i+1].(string)
			i += 2
			if hq != nil {
				hra.postdata.AppendQ(hq)
				hra.postContentType = key
				hra.post = true
			}

		case HTTPTagFlags: // int
			flags, _ := args[i].(int)
			i++
			hra.flags = flags

		case HTTPTagRequestHeader: // key, val
			key, _ := args[i].(string)
			val, _ := args[i+1].(string)
			i += 2
			if key != "" && val != "" {
				hra.headersIn.Add(key, val, true)
			}

		case HTTPTagRequestHeaders: // *HTTPHeaderList
			l, _ := args[i].(*httpnet.HTTPHeaderList)
			i++
			hra.headersIn.Merge(l)

		case HTTPTagResponseHeaders: // *HTTPHeaderList
			l, _ := args[i].(*httpnet.HTTPHeaderList)
			i++
			hra.headersOut = l

		case HTTPTagMethod: // string
			m, _ := args[i].(string)
			i++
			hra.method = m

		case HTTPTagProgressCallback: // FALoadCB, opaque
			cb, _ := args[i].(FALoadCB)
			op := args[i+1]
			i += 2
			hra.cb = cb
			hra.opaque = op

		case HTTPTagCancellable: // *misc.Cancellable (or nil)
			c, _ := args[i].(*misc.Cancellable)
			i++
			hf.cancellable = misc.CancellableRetain(c)

		case HTTPTagConnectTimeout: // int (ms)
			v, _ := args[i].(int)
			i++
			hf.connectTimeout = v

		case HTTPTagReadTimeout: // int (ms)
			v, _ := args[i].(int)
			i++
			hf.readTimeout = v

		case HTTPTagLocation: // *string
			p, _ := args[i].(*string)
			i++
			hf.retLocation = p
			if p != nil {
				*p = ""
			}

		case HTTPTagResponseCode: // *int
			p, _ := args[i].(*int)
			i++
			hra.httpCodePtr = p
			if p != nil {
				*p = 0
			}

		default:
			// C: abort() on unknown tag
			panic(fmt.Sprintf("http_reqv: unknown tag %d", tag))
		}
	}

	hra.asyncCallback = asyncCallback
	hra.asyncOpaque = asyncOpaque

	if hra.headersOut != nil {
		hra.headersOut.Free()
	}
	hf.version = 1
	if hra.flags&FaNoDebug == 0 {
		if hra.flags&FaDebug != 0 || fam.Gconf().EnableHTTPDebug.Load() {
			hf.debug = true
		}
	}
	hf.reqCompression = hra.flags&FaCompression != 0
	hf.noCookies = hra.flags&FaNoCookies != 0
	hf.sslVerify = hra.flags&FaSSLVerify != 0
	hf.url = url

	hra.hf = hf

	if hra.asyncCallback != nil {
		fam.tasks.Run(httpReqAsync, hra)
		return 0
	}

	r := httpReqDo(hra)
	HTTPReqRelease(hra)
	return r
}

// HTTPReq — C: http_req (synchronous convenience).
func (fam *FileAccessManager) HTTPReq(url string, args ...any) int {
	return fam.HTTPReqv(url, args, nil, nil)
}

// populateHTTPFAProtocol fills a bare "http"/"https"/"webdav"/"webdavs"
// gate with the canonical ops (C: fa_protocol_http et al).
func populateHTTPFAProtocol(fp *FAProtocol, dav bool) {
	fp.Flags = FAPIncludeProtoInURL | FAPAllowCache
	fp.IncludeProto = true
	if dav {
		fp.Stat = davStat
	} else {
		fp.Stat = httpStat
	}
	fp.Load = httpLoad
	fp.GetLastComponent = httpGetLastComponent
	fp.Deadline = httpSetReadTimeout
	fp.NoParking = httpNoParking
}

// Name — C: fap_name.
func (p *WebDAVProtocol) Name() string {
	if p.ssl {
		return "webdavs"
	}
	return "webdav"
}

// CanHandle — scheme gate (C: fap registration on name match).
func (p *WebDAVProtocol) CanHandle(url string) bool {
	if p.ssl {
		return strings.HasPrefix(url, "webdavs://")
	}
	return strings.HasPrefix(url, "webdav://")
}

// Open — C: fap_open = http_open.
func (p *WebDAVProtocol) Open(url string, extra *OpenExtra) (*Handle, error) {
	flags := 0
	if extra != nil {
		flags = extra.Flags
	}
	h, herr := httpOpen(p.fam.lookupFAProtocol(p.Name()), url, flags, extra)
	if h == nil {
		return nil, herr
	}
	h.proto = p
	return h, nil
}

// Stat — C: fap_stat = dav_stat.
func (p *WebDAVProtocol) Stat(url string) (*FileStat, error) {
	return davStat(p.fam.lookupFAProtocol(p.Name()), url, 0)
}

// ScanDir — C: fap_scan = dav_scandir.
func (p *WebDAVProtocol) ScanDir(ctx context.Context, url string) (*Dir, error) {
	fd := DirAlloc()
	if err := davScandir(p.fam.lookupFAProtocol(p.Name()), fd, url, 0); err != nil {
		fd.Free()
		return nil, err
	}
	return fd, nil
}
