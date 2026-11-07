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
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"

	"github.com/czz/movian-go/internal/callout"
	"github.com/czz/movian-go/internal/misc"
	httpnet "github.com/czz/movian-go/internal/networking/http"
)

// HTTPClientSetCookie — C: http_client_set_cookie.
func HTTPClientSetCookie(hri *HTTPRequestInspection, key, value string) {
	hri.Cookies.Add(key, value, false)
}

// httpCookie — C: http_cookie_t.
type httpCookie struct {
	name   string
	path   string
	domain string
	value  string
	expire int64 // time_t; -1 = never
	secure bool  // Secure attribute: only sent over https
	// hostOnly: no Domain attribute, so only the exact host gets it.
	hostOnly bool

	// Imported records (HTTPImportClearance) are never sent by the generic
	// jar; see cookieSendable.
	imported    bool
	ua          string // User-Agent the cookie is bound to
	scopeOrigin string // https://host the record was solved for
	scopeOwner  string // context that imported it
	uaPinned    bool   // this record set the host's User-Agent pin
}

// HTTPCookieRecord — persisted cookie fields (C: htsmsg map entries in
// cookie_persist / load_cookies).
type HTTPCookieRecord struct {
	Name, Path, Domain, Value string
	Expire                    int64
	Secure                    bool // absent in records written before this field: false
	// HostOnly is false for records written before this field existed,
	// which keeps their earlier domain-wide behaviour.
	HostOnly bool
	// Imported clearance record fields (optional; absent in older records).
	Imported    bool
	UA          string
	ScopeOrigin string
	ScopeOwner  string
}

// validateCookie — C: validate_cookie (RFC 2109 §4.3.2), for the host and
// path of a request that would carry the cookie: the cookie's path must
// match the request path and its domain the request host. Accepting a
// Set-Cookie checks the domain only (validateCookieDomain): the Path
// attribute limits where a cookie is sent, not whether it is stored.
func validateCookie(reqHost, reqPath, domain, path string) int {
	// The value for the Path attribute is not a prefix of the request-URI.
	if !cookiePathMatch(reqPath, path) {
		return 1
	}
	return validateCookieDomain(reqHost, domain)
}

// validateCookieDomain is the Domain part of validateCookie: 0 when a
// response from reqHost may set a cookie for domain, else the RFC 2109
// error code.
func validateCookieDomain(reqHost, domain string) int {
	// Host names compare without regard to case.
	reqHost, domain = strings.ToLower(reqHost), strings.ToLower(domain)
	// The value for the Domain attribute contains no embedded dots or
	// does not start with a dot — unless it matches req_host perfectly.
	// C: if(*domain == '.') domain++; then strchr(domain + 1, '.')
	if domain != reqHost {
		if strings.HasPrefix(domain, ".") {
			domain = domain[1:]
		}
		if len(domain) < 2 || !strings.Contains(domain[1:], ".") {
			return 2
		}
	}

	// The request-host does not domain-match the Domain attribute:
	// equal, or a subdomain separated by a dot (no bare suffix match, so
	// evilexample.test does not match example.test).
	if !cookieDomainMatch(reqHost, domain) {
		return 3
	}
	return 0
}

// cookiePathMatch is the RFC 6265 §5.1.4 path-match: the cookie path equals
// the request path, or is a prefix that ends at a "/" boundary, so /forum
// matches /forum and /forum/x but not /forumx. A query or fragment on the
// request path is not part of the path.
func cookiePathMatch(reqPath, cookiePath string) bool {
	if i := strings.IndexAny(reqPath, "?#"); i >= 0 {
		reqPath = reqPath[:i]
	}
	if !strings.HasPrefix(reqPath, cookiePath) {
		return false
	}
	return len(reqPath) == len(cookiePath) ||
		strings.HasSuffix(cookiePath, "/") ||
		reqPath[len(cookiePath)] == '/'
}

// cookieDomainMatch reports whether reqHost equals domain or is a
// subdomain of it (leading dot of domain ignored), on a label boundary.
// It is the Set-Cookie Domain check shared by HTTP/1 (inside
// validateCookieDomain, together with the legacy RFC 2109 dot rules) and
// HTTP/2 (h2SetCookies, domain check only; HTTP/3 shares that path).
func cookieDomainMatch(reqHost, domain string) bool {
	reqHost = strings.ToLower(reqHost)
	domain = strings.TrimPrefix(strings.ToLower(domain), ".")
	return reqHost == domain || strings.HasSuffix(reqHost, "."+domain)
}

// cookiePersist — C: cookie_persist (callout).
func (fam *FileAccessManager) cookiePersist(c *callout.Callout, opaque any) {
	if fam.httpCookies.cookieSave == nil {
		return
	}
	now := time.Now().Unix()
	var records []HTTPCookieRecord
	fam.httpCookies.mu.Lock()
	fam.purgeExpiredImportedLocked(now)
	for _, hc := range fam.httpCookies.list {
		if hc.expire == -1 || now > hc.expire {
			continue
		}
		records = append(records, HTTPCookieRecord{
			Name: hc.name, Path: hc.path, Domain: hc.domain,
			Value: hc.value, Expire: hc.expire, Secure: hc.secure,
			HostOnly: hc.hostOnly, Imported: hc.imported, UA: hc.ua,
			ScopeOrigin: hc.scopeOrigin, ScopeOwner: hc.scopeOwner,
		})
	}
	fam.httpCookies.mu.Unlock()
	fam.httpCookies.cookieSave(fam.httpCookies.cookieStore, records)
}

// loadCookies — C: load_cookies.
func (fam *FileAccessManager) loadCookies() {
	if fam.httpCookies.cookieLoad == nil {
		return
	}
	now := time.Now().Unix()
	for _, r := range fam.httpCookies.cookieLoad(fam.httpCookies.cookieStore) {
		if r.Name == "" || r.Path == "" || r.Domain == "" ||
			r.Value == "" || r.Expire < now {
			continue
		}
		if r.Imported {
			hc, ok := validLoadedImported(r, now)
			if !ok || fam.findImportedLocked(hc.scopeOrigin, hc.name) >= 0 {
				continue
			}
			if ua := fam.importedHostUALocked(hc.domain, -1); ua != "" && ua != hc.ua {
				continue // a host has one pinned UA
			}
			prior := fam.httpDomainUAGet(hc.domain)
			hc.uaPinned = prior != hc.ua
			fam.HTTPDomainSetUserAgent(hc.domain, hc.ua)
			fam.httpCookies.list = append([]*httpCookie{&hc}, fam.httpCookies.list...)
			continue
		}
		// New cookies store their domain without the leading dot; records
		// written before that keep it. Normalize so a replacement or a
		// deletion finds them, and drop a later duplicate of the same cookie
		// (records are saved newest first).
		domain := cookieCanonDomain(r.Domain)
		if domain == "" || fam.findJarCookieLocked(r.Name, r.Path, domain) >= 0 {
			continue
		}
		fam.httpCookies.list = append([]*httpCookie{{
			name: r.Name, path: r.Path, domain: domain,
			value: r.Value, expire: r.Expire, secure: r.Secure,
			hostOnly: r.HostOnly,
		}}, fam.httpCookies.list...)
	}
}

// cookieCanonDomain is the form a jar domain is stored and compared in:
// lowercase, without a leading dot.
func cookieCanonDomain(d string) string {
	return strings.ToLower(strings.TrimPrefix(d, "."))
}

// findJarCookieLocked returns the index of the jar cookie with this name,
// path and domain, or -1. The caller holds the jar lock or owns the jar.
func (fam *FileAccessManager) findJarCookieLocked(name, path, domain string) int {
	for i, x := range fam.httpCookies.list {
		if x.name == name && x.path == path && x.domain == domain {
			return i
		}
	}
	return -1
}

// httpCookieSet — C: http_cookie_set.
func httpCookieSet(cookie string, hf *httpFile) {
	fam := hf.fam
	if hf.noCookies {
		return
	}
	reqHost := hf.connection.hostname
	reqPath := hf.path
	domain := reqHost
	path := cookieDefaultPath(reqPath)
	var expire int64 = -1

	cbuf := []byte(cookie)
	argv := httpTokenize(cbuf, 20, ';')
	if len(argv) == 0 {
		hf.hfTrace("Ignoring malformed cookie")
		return
	}

	name := string(argv[0])
	eq := strings.IndexByte(name, '=')
	if eq == -1 {
		hf.hfTrace("Ignoring malformed cookie, no value")
		return
	}
	value := name[eq+1:]
	name = name[:eq]

	var expires time.Time
	var maxAge int64
	hasMaxAge := false
	secure := false
	hostOnly := true
	for i := 1; i < len(argv); i++ {
		a := string(argv[i])
		lower := strings.ToLower(a)
		// Flag attributes are matched by name, ignoring any "=value" a
		// server attaches (net/http does the same on the HTTP/2 path).
		attr, _, _ := strings.Cut(strings.TrimSpace(a), "=")
		if strings.EqualFold(attr, "secure") {
			secure = true
		}
		if strings.EqualFold(attr, "partitioned") {
			// A Go request has no browser partition context: never widen a
			// partitioned cookie into the shared jar.
			hf.hfTrace("Ignoring Partitioned cookie name=%s", name)
			return
		}
		if strings.HasPrefix(lower, "domain=") {
			if d := strings.TrimPrefix(strings.TrimSpace(a[len("domain="):]), "."); d != "" {
				domain = strings.ToLower(d)
				hostOnly = false
			}
		}
		if strings.HasPrefix(lower, "path=") {
			if p := a[len("path="):]; strings.HasPrefix(p, "/") {
				path = p
			} else {
				path = cookieDefaultPath(reqPath)
			}
		}
		if strings.HasPrefix(lower, "expires=") {
			if t, ok := parseCookieExpires(a[len("expires="):]); ok {
				expires = t
			}
		}
		if strings.HasPrefix(lower, "max-age=") {
			if n, err := strconv.ParseInt(strings.TrimSpace(a[len("max-age="):]), 10, 64); err == nil {
				maxAge, hasMaxAge = n, true
			}
		}
	}
	now := time.Now().Unix()
	expire = cookieExpiry(now, expires, maxAge, hasMaxAge)

	if !hostOnly {
		reject, forceHostOnly := cookiePublicSuffixScope(reqHost, domain)
		if reject {
			hf.hfTrace("Rejected cookie name=%s domain=%s: public suffix", name, domain)
			return
		}
		hostOnly = hostOnly || forceHostOnly
	}
	if r := validateCookieDomain(reqHost, domain); r != 0 {
		hf.hfTrace("Rejected cookie name=%s path=%s domain=%s value=%s error=%d",
			name, path, domain, value, r)
		return
	}

	expiresIn := int64(-1)
	if expire != -1 {
		expiresIn = expire - now
	}
	hf.hfTrace("Updating cookie name=%s path=%s domain=%s value=%s expires in %d seconds",
		name, path, domain, value, expiresIn)

	fam.cookieJarPut(httpCookie{
		name: name, path: path, domain: domain, value: value, expire: expire, secure: secure,
		hostOnly: hostOnly,
	})
}

// cookiePublicSuffixScope applies RFC 6265 §5.3 step 5 to a Domain
// attribute. A domain that names a public suffix (co.uk) must not scope a
// cookie to every site under it: when the suffix is not the request host
// the cookie is rejected; when it is the request host the cookie is kept
// but becomes host-only. A domain that is not a public suffix is left alone.
func cookiePublicSuffixScope(reqHost, domain string) (reject, hostOnly bool) {
	domain = cookieCanonDomain(domain)
	suffix, _ := publicsuffix.PublicSuffix(domain)
	if suffix != domain {
		return false, false
	}
	if domain == strings.ToLower(reqHost) {
		return false, true
	}
	return true, false
}

// cookieDefaultPath is the RFC 6265 §5.1.4 default-path: the request path
// up to, not including, its last "/" ("/" when that leaves nothing).
func cookieDefaultPath(reqPath string) string {
	if i := strings.IndexAny(reqPath, "?#"); i >= 0 {
		reqPath = reqPath[:i]
	}
	if !strings.HasPrefix(reqPath, "/") {
		return "/"
	}
	if i := strings.LastIndex(reqPath, "/"); i > 0 {
		return reqPath[:i]
	}
	return "/"
}

// parseCookieExpires parses a Set-Cookie Expires value: the three HTTP
// date layouts plus the dash-separated RFC 850 variant servers still send.
func parseCookieExpires(v string) (time.Time, bool) {
	v = strings.TrimSpace(v)
	if t, err := http.ParseTime(v); err == nil {
		return t, true
	}
	if t, err := time.Parse("Mon, 02-Jan-2006 15:04:05 MST", v); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// cookieExpiry resolves a Set-Cookie lifetime to a Unix expiry shared by
// HTTP/1 and HTTP/2/3: Max-Age wins over Expires (RFC 6265 §5.3 step 3);
// Max-Age <= 0 yields a past time, which deletes; neither attribute is a
// session cookie (-1). expires is the zero time when absent or invalid.
func cookieExpiry(now int64, expires time.Time, maxAge int64, hasMaxAge bool) int64 {
	if hasMaxAge {
		switch {
		case maxAge <= 0:
			return 0
		case maxAge > math.MaxInt64-now:
			return math.MaxInt64
		default:
			return now + maxAge
		}
	}
	if !expires.IsZero() {
		return expires.Unix()
	}
	return -1
}

// cookieSendable reports whether hc may be attached to a request for
// host/path now. HTTP/1 and HTTP/2/3 senders share it so the rules cannot
// drift. secureReq is true when the request goes over TLS. importedOwner
// is the owner of a request that opted in with FaImportedCookies; an
// imported record is sent to that owner only.
func cookieSendable(hc *httpCookie, host, path string, secureReq bool,
	importedOwner string, now int64) bool {
	return cookieSkipReason(hc, host, path, secureReq, importedOwner, now) == ""
}

// cookieSkipReason is "" when hc may be sent, else why not. It holds the
// rules; cookieSendable and the clearance debug trace both use it.
func cookieSkipReason(hc *httpCookie, host, path string, secureReq bool,
	importedOwner string, now int64) string {
	if hc.expire != -1 && now > hc.expire {
		return "expired"
	}
	if hc.imported {
		switch {
		case importedOwner == "":
			return "request-not-opted-in"
		case hc.scopeOwner != importedOwner:
			return "owner-mismatch"
		case !secureReq:
			return "insecure-request"
		case !strings.EqualFold(host, hc.domain):
			return "host-mismatch"
		}
	}
	if hc.secure && !secureReq {
		return "secure-required"
	}
	if hc.hostOnly && !strings.EqualFold(host, hc.domain) {
		return "host-mismatch"
	}
	if validateCookie(host, path, hc.domain, hc.path) != 0 {
		return "domain-or-path-mismatch"
	}
	return ""
}

// cookieURLSecure reports whether a request URL is TLS-protected.
func cookieURLSecure(rawURL string) bool {
	i := strings.Index(rawURL, ":")
	if i < 0 {
		return false
	}
	switch strings.ToLower(rawURL[:i]) {
	case "https", "webdavs":
		return true
	}
	return false
}

// cookieJarPut stores nc in the shared jar, replacing a cookie with the
// same name, domain and path (RFC 6265 §5.3 step 11), or deletes that
// cookie when nc is already expired. HTTP/1 and HTTP/2/3 both end here.
func (fam *FileAccessManager) cookieJarPut(nc httpCookie) {
	nc.domain = cookieCanonDomain(nc.domain)
	now := time.Now().Unix()
	fam.httpCookies.mu.Lock()
	idx := -1
	for i, x := range fam.httpCookies.list {
		if x.name == nc.name && x.path == nc.path && x.domain == nc.domain {
			if x.imported {
				// An imported record is only changed through its own API.
				fam.httpCookies.mu.Unlock()
				return
			}
			idx = i
			break
		}
	}
	switch {
	case nc.expire != -1 && nc.expire <= now:
		if idx >= 0 {
			fam.httpCookies.list = slices.Delete(fam.httpCookies.list, idx, idx+1)
		}
	case idx >= 0:
		*fam.httpCookies.list[idx] = nc
	default:
		fam.httpCookies.list = slices.Insert(fam.httpCookies.list, 0, &nc)
	}
	fam.httpCookies.mu.Unlock()
	if cs := fam.CalloutSystem(); cs != nil {
		cs.Arm(&fam.httpCookies.persistTimer, fam.cookiePersist, nil, 5)
	}
}

// HTTPInjectCookies inserts cookies into the shared jar for host/path
// (no C counterpart): used to plant a webview-solved session so the
// fileaccess layer sends it on every request to the domain.
func (fam *FileAccessManager) HTTPInjectCookies(host, path string, cookies map[string]string) {
	if path == "" {
		path = "/"
	}
	host = cookieCanonDomain(host)
	expire := time.Now().Unix() + 86400*30
	fam.httpCookies.mu.Lock()
	for name, value := range cookies {
		var hc *httpCookie
		protected := false
		for _, x := range fam.httpCookies.list {
			if x.name == name && x.path == path && x.domain == host {
				hc = x
				protected = x.imported
				break
			}
		}
		if protected {
			continue
		}
		if hc == nil {
			hc = &httpCookie{name: name, path: path, domain: host}
			fam.httpCookies.list = slices.Insert(fam.httpCookies.list, 0, hc)
		}
		hc.value = value
		hc.expire = expire
	}
	fam.httpCookies.mu.Unlock()
	if cs := fam.CalloutSystem(); cs != nil {
		cs.Arm(&fam.httpCookies.persistTimer, fam.cookiePersist, nil, 5)
	}
}

// HTTPDeleteCookies drops the jar entries a request to host/path would
// carry — i.e. domain/path pairs passing validateCookie — optionally
// restricted to the listed names (nil map = all names). No C
// counterpart: the inject/delete pair exists for plugin-driven session
// logout. Returns the removed count and schedules a jar persist.
func (fam *FileAccessManager) HTTPDeleteCookies(host, path string, names map[string]bool) int {
	if path == "" {
		path = "/"
	}
	fam.httpCookies.mu.Lock()
	n := 0
	kept := fam.httpCookies.list[:0]
	for _, x := range fam.httpCookies.list {
		if !x.imported && (len(names) == 0 || names[x.name]) &&
			(!x.hostOnly || strings.EqualFold(host, x.domain)) &&
			validateCookie(host, path, x.domain, x.path) == 0 {
			n++
			continue
		}
		kept = append(kept, x)
	}
	for i := len(kept); i < len(fam.httpCookies.list); i++ {
		fam.httpCookies.list[i] = nil
	}
	fam.httpCookies.list = kept
	fam.httpCookies.mu.Unlock()
	if n > 0 {
		if cs := fam.CalloutSystem(); cs != nil {
			cs.Arm(&fam.httpCookies.persistTimer, fam.cookiePersist, nil, 5)
		}
	}
	return n
}

// httpCookieAppend — C: http_cookie_append.
func httpCookieAppend(reqHost string, hf *httpFile,
	headers, extraCookies *httpnet.HTTPHeaderList) {
	fam := hf.fam
	if hf.noCookies {
		return
	}
	reqPath := hf.path
	// The live connection is authoritative: a cached permanent redirect can
	// leave hf.url at https while the socket is plaintext.
	secureReq := cookieURLSecure(hf.url)
	if hf.connection != nil {
		secureReq = hf.connection.ssl
	}
	s := ""

	var hq misc.HtsbufQueue
	hq.HtsbufQueueSetup(0)

	for _, c := range fam.selectCookies(reqHost, reqPath, secureReq, hf.importedOwner, hf.debug,
		func(name string) bool { return extraCookieOverrides(extraCookies, name) }) {
		hq.Append([]byte(s))
		hq.Append([]byte(c.name))
		hq.Append([]byte("="))
		hq.Append([]byte(c.value))
		s = "; "
	}

	for _, hh := range extraCookies.List {
		// C: if(hh->hh_value == NULL) continue; — Go values are never
		// NULL, so every entry is emitted (empty → "key=").
		hq.Append([]byte(s))
		hq.Append([]byte(hh.Key))
		hq.Append([]byte("="))
		hq.Append([]byte(hh.Value))
		s = "; "
	}

	if hq.Len() == 0 {
		return
	}
	headers.AddAlloced("Cookie", hq.String(), false)
}

// extraCookieOverrides reports whether the request's own cookie list names
// name. C: http_header_get(extra_cookies, hc_name) is a presence check
// (even an empty value suppresses the jar cookie), plus a case-sensitive
// scan of the list.
func extraCookieOverrides(extra *httpnet.HTTPHeaderList, name string) bool {
	if _, ok := extra.Get(name); ok {
		return true
	}
	for _, hh := range extra.List {
		if hh.Key == name {
			return true
		}
	}
	return false
}
