// fa_http2.go — Go extension (no upstream C counterpart).
//
// Routes https:// open/stat through net/http so crypto/tls negotiates
// HTTP/2 via ALPN (ForceAttemptHTTP2 on the shared Transport; servers
// without h2 silently keep HTTP/1.1 — net/http handles both). Gated by
// gconf.FAHTTP2 ("Use HTTP/2 for HTTPS" setting). The canonical
// hand-written HTTP/1.x path in fa_http.go (C: fa_http.c) is untouched
// and still serves http:// plus any https:// open when the toggle is off.
//
// HTTP/3 (QUIC) lives in fa_http3.go — same modern path, gated by
// gconf.FAHTTP3 and attempted per host before TCP.
//
// Deliberate divergences from the canonical path (documented, reversible):
//   - 401/407 → the open is delegated to the canonical httpOpen, whose
//     digest/basic negotiation + keyring machinery populates the shared
//     httpAuthCaches; subsequent h2 opens pre-seed Authorization from
//     that cache (same as the h1 path)
//   - hri.HF is a synthetic *httpFile carrying fam+url only — inspector
//     hooks that dereference connection state see nil fields
//   - connection keep-alive/parking is delegated to http.Transport

package fileaccess

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/czz/movian-go/internal/arch"
	"github.com/czz/movian-go/internal/misc"
	httpnet "github.com/czz/movian-go/internal/networking/http"
	"github.com/czz/movian-go/internal/trace"
	"github.com/czz/movian-go/internal/version"
)

var h2FileID atomic.Int32

// h2File — net/http-backed https stream (io.ReadCloser + io.Seeker for
// Handle). Seek re-issues the request with a Range header, mirroring
// the C path's reconnect-on-seek semantics.
type h2File struct {
	fam    *FileAccessManager
	url    string
	client *http.Client
	ctx    context.Context
	cancel context.CancelFunc
	body   io.ReadCloser
	pos    int64
	size   int64 // -1 = unknown
	c      *misc.Cancellable
	foe    *OpenExtra
	verify bool // FaSSLVerify — selects the shared client/transport
	ranged bool // last response honored our Range (206)
	debug  bool
	id     int32
}

// h2Trace — same pattern as hfTrace (fa_http.go).
func (hf *h2File) h2Trace(format string, args ...any) {
	if hf.debug {
		hf.fam.TraceSystem().Trace(trace.TRACE_DEBUG,
			fmt.Sprintf("HTTP2-%d", hf.id), format, args...)
	}
}

// http2Client returns the fam-shared *http.Client for the given TLS
// verify mode (verify mirrors FaSSLVerify → !InsecureSkipVerify).
// Two shared clients at most (verify / insecure).
func (fam *FileAccessManager) http2Client(verify bool) *http.Client {
	fam.http2Mu.Lock()
	defer fam.http2Mu.Unlock()
	if fam.http2Clients == nil {
		fam.http2Clients = make(map[bool]*http.Client)
	}
	if c := fam.http2Clients[verify]; c != nil {
		return c
	}
	tr := &http.Transport{
		// Without a custom DialContext, h2 would be on anyway —
		// ForceAttemptHTTP2 makes the intent explicit and survives
		// future transport knobs.
		ForceAttemptHTTP2:   true,
		Proxy:               http.ProxyFromEnvironment,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: !verify},
	}
	c := &http.Client{
		Transport: tr,
		// Redirects are followed manually in h2Issue so 301/308 feed
		// the shared permanent-redirect cache — net/http's CheckRedirect
		// cannot see the per-hop status codes.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	fam.http2Clients[verify] = c
	return c
}

// h2CookieHeader — same matching loop as httpCookieAppend
// (fa_http_cookie.go), flattened to a Cookie header value.
func (hf *h2File) h2CookieHeader(host, path string,
	extra *httpnet.HTTPHeaderList) string {
	fam := hf.fam
	now := time.Now().Unix()
	var b strings.Builder
	s := ""
	fam.httpCookies.mu.Lock()
	for _, hc := range fam.httpCookies.list {
		if hc.expire != -1 && now > hc.expire {
			continue
		}
		if validateCookie(host, path, hc.domain, hc.path) != 0 {
			continue
		}
		if _, ok := extra.Get(hc.name); ok {
			continue
		}
		skip := false
		for _, hh := range extra.List {
			if hh.Key == hc.name {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		b.WriteString(s)
		b.WriteString(hc.name)
		b.WriteString("=")
		b.WriteString(hc.value)
		s = "; "
	}
	fam.httpCookies.mu.Unlock()
	for _, hh := range extra.List {
		b.WriteString(s)
		b.WriteString(hh.Key)
		b.WriteString("=")
		b.WriteString(hh.Value)
		s = "; "
	}
	return b.String()
}

// h2SetCookies harvests Set-Cookie response headers into the shared jar
// (same semantics as http_cookie_set, minus the httpFile coupling).
func (hf *h2File) h2SetCookies(resp *http.Response, u *neturl.URL) {
	fam := hf.fam
	now := time.Now().Unix()
	for _, c := range resp.Cookies() {
		if c.Name == "" {
			continue
		}
		domain := c.Domain
		if domain == "" {
			domain = u.Hostname()
		} else {
			domain = strings.TrimPrefix(domain, ".")
		}
		path := c.Path
		if path == "" {
			path = "/"
		}
		expire := int64(-1)
		if !c.Expires.IsZero() {
			expire = c.Expires.Unix()
		} else if c.MaxAge > 0 {
			expire = now + int64(c.MaxAge)
		}
		fam.httpCookies.mu.Lock()
		var hc *httpCookie
		for _, x := range fam.httpCookies.list {
			if x.name == c.Name && x.path == path && x.domain == domain {
				hc = x
				break
			}
		}
		if hc == nil {
			hc = &httpCookie{name: c.Name, path: path, domain: domain}
			fam.httpCookies.list = append([]*httpCookie{hc},
				fam.httpCookies.list...)
		}
		hc.value = c.Value
		hc.expire = expire
		fam.httpCookies.mu.Unlock()
	}
	if cs := fam.CalloutSystem(); cs != nil && len(resp.Cookies()) > 0 {
		cs.Arm(&fam.httpCookies.persistTimer, fam.cookiePersist, nil, 5)
	}
}

// h2NewRequest builds the request for (url, method, start). Runs the
// same request-inspector chain as httpRequestInspect so plugins keep
// seeing and mutating https traffic; hri.HF is a synthetic *httpFile
// (fam+url only — no httpConnection exists on this path).
func (hf *h2File) h2NewRequest(url, method string, start int64) (*http.Request, error) {
	u, err := neturl.Parse(url)
	if err != nil {
		return nil, err
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}

	var headers, cookies httpnet.HTTPHeaderList
	hri := &HTTPRequestInspection{
		Method:  method,
		HF:      &httpFile{fam: hf.fam, url: url},
		Headers: &headers,
		Cookies: &cookies,
		Errbuf:  make([]byte, 256),
	}
	for _, x := range hf.fam.httpInspectors {
		if !x.Check(url, hri) {
			if hri.ForceFail {
				return nil, errors.New(misc.CStr(hri.Errbuf))
			}
		}
	}

	req, err := http.NewRequestWithContext(hf.ctx, method, url, nil)
	if err != nil {
		return nil, err
	}

	if ua := hf.fam.httpDomainUAGet(u.Hostname()); ua != "" {
		req.Header.Set("User-Agent", ua)
	} else {
		req.Header.Set("User-Agent", fmt.Sprintf("Movian Go %s %s",
			arch.GetSystemType(), version.AppVersion()))
	}
	req.Header.Set("Accept", "*/*")

	if ck := hf.h2CookieHeader(u.Hostname(), path, &cookies); ck != "" {
		req.Header.Set("Cookie", ck)
	}
	for _, hh := range headers.List {
		req.Header.Set(hh.Key, hh.Value)
	}
	if foe := hf.foe; foe != nil {
		for k, v := range foe.RequestHeaders {
			req.Header.Set(k, v)
		}
		if l := foe.RequestHeadersList; l != nil {
			for _, hh := range l.List {
				req.Header.Set(hh.Key, hh.Value)
			}
		}
	}
	if start > 0 {
		req.Header.Set("Range",
			"bytes="+strconv.FormatInt(start, 10)+"-")
	}

	// C: http_request_inspect tail — Authorization from the shared
	// auth cache (populated by the canonical path's digest/basic dance).
	fam := hf.fam
	port := 443
	if p, err := strconv.Atoi(u.Port()); err == nil && p > 0 {
		port = p
	}
	fam.httpAuthMu.Lock()
	for _, hac := range fam.httpAuthCaches {
		if hac.hostname == u.Hostname() && hac.port == port {
			req.Header.Set("Authorization", hac.credentials)
			break
		}
	}
	fam.httpAuthMu.Unlock()
	return req, nil
}

// h2RedirectedURL applies the shared permanent-redirect cache —
// C: the http_redirects lookup in http_request (fa_http_conn.c).
func (hf *h2File) h2RedirectedURL() string {
	fam := hf.fam
	url := hf.url
	fam.httpRedirects.mu.Lock()
	for _, hr := range fam.httpRedirects.list {
		if hr.from == url {
			url = hr.to
			break
		}
	}
	fam.httpRedirects.mu.Unlock()
	return url
}

// h2Issue performs the request and validates the response, replacing
// hf.body on success. Redirects are followed manually (max 10 hops,
// C: redircount) so 301/308 feed addPermanentRedirect. start>0 expects
// a 206; a 200 answer means the server ignored Range and the caller
// drains the leading bytes.
func (hf *h2File) h2Issue(start int64) error {
	if hf.body != nil {
		hf.body.Close()
		hf.body = nil
	}
	url := hf.h2RedirectedURL()
	var resp *http.Response
	for redirects := 0; ; redirects++ {
		req, err := hf.h2NewRequest(url, "GET", start)
		if err != nil {
			return err
		}
		hf.h2Trace("GET %s (from %d)", url, start)
		resp, err = hf.h2RoundTrip(req)
		if err != nil {
			return err
		}
		hf.h2Trace("← %d %s proto=%s", resp.StatusCode, resp.Status,
			resp.Proto)

		switch resp.StatusCode {
		case 301, 302, 303, 307, 308:
			loc := resp.Header.Get("Location")
			code := resp.StatusCode
			resp.Body.Close()
			if loc == "" {
				return fapError{code: code,
					msg: fmt.Sprintf("HTTP error %d: redirect without Location", code)}
			}
			base, err := neturl.Parse(url)
			if err != nil {
				return err
			}
			next, err := base.Parse(loc)
			if err != nil {
				return err
			}
			// C: add_premanent_redirect on 301 — keep 308 (permanent,
			// method-preserving) in the same cache.
			if code == 301 || code == 308 {
				hf.fam.addPermanentRedirect(url, next.String())
			}
			if redirects >= 10 {
				return errors.New("too many redirects")
			}
			url = next.String()
			resp = nil
			continue
		}
		break
	}

	if resp.StatusCode >= 400 {
		code := resp.StatusCode
		resp.Body.Close()
		return fapError{code: code,
			msg: fmt.Sprintf("HTTP error %d", code)}
	}
	// Cookies bind to the final (post-redirect) origin.
	u, _ := neturl.Parse(url)
	if u != nil {
		hf.h2SetCookies(resp, u)
	}
	if foe := hf.foe; foe != nil {
		foe.ProtocolError = resp.StatusCode
		if foe.ResponseHeaders == nil {
			foe.ResponseHeaders = make(map[string]string)
		}
		for k, v := range resp.Header {
			foe.ResponseHeaders[k] = strings.Join(v, ", ")
		}
		if foe.ResponseHeadersList != nil {
			for k, v := range resp.Header {
				foe.ResponseHeadersList.Add(k, strings.Join(v, ", "), false)
			}
		}
	}

	hf.ranged = false
	switch {
	case start > 0 && resp.StatusCode == http.StatusPartialContent:
		hf.ranged = true
		// content-range total takes precedence over content-length
		if cr := resp.Header.Get("Content-Range"); cr != "" {
			if i := strings.LastIndex(cr, "/"); i >= 0 {
				if total, err := strconv.ParseInt(
					strings.TrimSpace(cr[i+1:]), 10, 64); err == nil {
					hf.size = total
				}
			}
		}
	case resp.StatusCode == http.StatusOK:
		hf.size = resp.ContentLength // -1 if unknown
	default:
		// e.g. 416 on a range beyond EOF
		resp.Body.Close()
		return fapError{code: resp.StatusCode,
			msg: fmt.Sprintf("HTTP error %d", resp.StatusCode)}
	}
	hf.body = resp.Body
	return nil
}

// h2Open — counterpart of httpOpen for the h2 path.
func h2Open(fap *FAProtocol, url string, flags int,
	foe *OpenExtra) (*Handle, error) {
	fam := fap.fam

	ctx, cancel := context.WithCancel(context.Background())
	verify := flags&FaSSLVerify != 0
	hf := &h2File{
		fam:    fam,
		url:    url,
		client: fam.http2Client(verify),
		verify: verify,
		ctx:    ctx,
		cancel: cancel,
		size:   -1,
		foe:    foe,
		id:     h2FileID.Add(1),
		debug:  flags&FaDebug != 0 || fam.Gconf().EnableHTTPDebug.Load(),
	}
	if c := foeCancellable(foe); c != nil {
		hf.c = misc.CancellableRetain(c)
		misc.CancellableBind(c, func(opaque any) {
			opaque.(*h2File).cancel()
		}, hf)
	}

	if err := hf.h2Issue(0); err != nil {
		hf.Close()
		if fe, ok := err.(fapError); ok && (fe.code == 401 || fe.code == 407) {
			// Auth negotiation (digest/basic + keyring) lives in the
			// canonical path — delegate the open; the resulting
			// credentials land in httpAuthCaches for later h2 opens.
			return httpOpen(fap, url, flags, foe)
		}
		if flags&FaNonInteractive != 0 {
			if fe, ok := err.(fapError); ok && foe != nil {
				foe.ProtocolError = fe.code
			}
		}
		return nil, err
	}

	h := &Handle{
		fam:    fam,
		reader: hf,
		seeker: hf,
		url:    url,
		size:   hf.size,
	}
	h.fap = fap
	h.sizer = func() int64 { return hf.size }
	return h, nil
}

// h2Stat — counterpart of httpStat: open, take the size, close
// (the C path does a full GET rather than HEAD — same here).
func h2Stat(fap *FAProtocol, url string, flags int) (*FileStat, error) {
	h, err := h2Open(fap, url, flags, nil)
	if err != nil {
		return nil, err
	}
	st := &FileStat{Type: ContentFile, Size: h.Size()}
	h.Close()
	return st, nil
}

// Read — io.Reader (Handle.reader).
func (hf *h2File) Read(buf []byte) (int, error) {
	if hf.body == nil {
		return 0, io.EOF
	}
	n, err := hf.body.Read(buf)
	hf.pos += int64(n)
	return n, err
}

// Close — io.Closer. Releases the cancellable binding like httpDestroy.
// ctx is cancelled BEFORE closing the body so an in-flight Read on the
// h2 stream aborts instead of waiting for the server.
func (hf *h2File) Close() error {
	if hf.cancel != nil {
		hf.cancel()
	}
	if hf.body != nil {
		hf.body.Close()
		hf.body = nil
	}
	if hf.c != nil {
		misc.CancellableUnbind(hf.c, hf)
		misc.CancellableRelease(hf.c)
		hf.c = nil
	}
	return nil
}

// Seek — io.Seeker; re-issues the request with Range like the C path
// reopens on seek (lazy=0 semantics).
func (hf *h2File) Seek(pos int64, whence int) (int64, error) {
	var target int64
	switch whence {
	case io.SeekStart:
		target = pos
	case io.SeekCurrent:
		target = hf.pos + pos
	case io.SeekEnd:
		if hf.size < 0 {
			return 0, errors.New("seek end on unknown size")
		}
		target = hf.size + pos
	default:
		return 0, errors.New("invalid whence")
	}
	if target < 0 {
		return 0, errors.New("seek before start")
	}
	if target == hf.pos && hf.body != nil {
		return hf.pos, nil
	}

	if err := hf.h2Issue(target); err != nil {
		return 0, err
	}
	// Server ignored Range (answered 200): the body starts at 0 —
	// drain the leading bytes, matching the C fallback.
	if target > 0 && !hf.ranged {
		if _, err := io.CopyN(io.Discard, hf.body, target); err != nil {
			return 0, err
		}
	}
	hf.pos = target
	return hf.pos, nil
}
