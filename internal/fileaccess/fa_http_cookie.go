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
	"slices"
	"strings"
	"time"

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
}

// HTTPCookieRecord — persisted cookie fields (C: htsmsg map entries in
// cookie_persist / load_cookies).
type HTTPCookieRecord struct {
	Name, Path, Domain, Value string
	Expire                    int64
}

// validateCookie — C: validate_cookie (RFC 2109 §4.3.2).
func validateCookie(reqHost, reqPath, domain, path string) int {
	// The value for the Path attribute is not a prefix of the request-URI.
	if !strings.HasPrefix(reqPath, path) {
		return 1
	}

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

	// The request-host does not domain-match the Domain attribute.
	s := strings.Index(reqHost, domain)
	if s == -1 && strings.HasPrefix(domain, ".") && reqHost == domain[1:] {
		s = 0
	} else if s == -1 || s+len(domain) != len(reqHost) {
		return 3
	}
	return 0
}

// cookiePersist — C: cookie_persist (callout).
func (fam *FileAccessManager) cookiePersist(c *callout.Callout, opaque any) {
	if fam.httpCookies.cookieSave == nil {
		return
	}
	now := time.Now().Unix()
	var records []HTTPCookieRecord
	fam.httpCookies.mu.Lock()
	for _, hc := range fam.httpCookies.list {
		if hc.expire == -1 || now > hc.expire {
			continue
		}
		records = append(records, HTTPCookieRecord{
			Name: hc.name, Path: hc.path, Domain: hc.domain,
			Value: hc.value, Expire: hc.expire,
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
		fam.httpCookies.list = append([]*httpCookie{{
			name: r.Name, path: r.Path, domain: r.Domain,
			value: r.Value, expire: r.Expire,
		}}, fam.httpCookies.list...)
	}
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
	path := reqPath
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

	for i := 1; i < len(argv); i++ {
		a := string(argv[i])
		if strings.HasPrefix(strings.ToLower(a), "domain=") {
			domain = a[len("domain="):]
		}
		if strings.HasPrefix(strings.ToLower(a), "path=") {
			path = a[len("path="):]
		}
		if strings.HasPrefix(strings.ToLower(a), "expires=") {
			if t, r := httpnet.HTTPCtime(a[len("expires="):]); r == 0 {
				expire = t.Unix()
			}
		}
	}

	if r := validateCookie(reqHost, reqPath, domain, path); r != 0 {
		hf.hfTrace("Rejected cookie name=%s path=%s domain=%s value=%s error=%d",
			name, path, domain, value, r)
		return
	}

	expiresIn := int64(-1)
	if expire > 0 {
		expiresIn = expire - time.Now().Unix()
	}
	hf.hfTrace("Updating cookie name=%s path=%s domain=%s value=%s expires in %d seconds",
		name, path, domain, value, expiresIn)

	fam.httpCookies.mu.Lock()

	var hc *httpCookie
	for _, x := range fam.httpCookies.list {
		if x.name == name && x.path == path && x.domain == domain {
			hc = x
			break
		}
	}
	if hc == nil {
		hc = &httpCookie{name: name, path: path, domain: domain}
		fam.httpCookies.list = slices.Insert(fam.httpCookies.list, 0, hc)
	}

	if expire != -1 {
		if cs := fam.CalloutSystem(); cs != nil {
			cs.Arm(&fam.httpCookies.persistTimer, fam.cookiePersist, nil, 5)
		}
	}
	hc.value = value
	hc.expire = expire

	fam.httpCookies.mu.Unlock()
}

// HTTPInjectCookies inserts cookies into the shared jar for host/path
// (no C counterpart): used to plant a webview-solved session so the
// fileaccess layer sends it on every request to the domain.
func (fam *FileAccessManager) HTTPInjectCookies(host, path string, cookies map[string]string) {
	if path == "" {
		path = "/"
	}
	expire := time.Now().Unix() + 86400*30
	fam.httpCookies.mu.Lock()
	for name, value := range cookies {
		var hc *httpCookie
		for _, x := range fam.httpCookies.list {
			if x.name == name && x.path == path && x.domain == host {
				hc = x
				break
			}
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

// httpCookieAppend — C: http_cookie_append.
func httpCookieAppend(reqHost string, hf *httpFile,
	headers, extraCookies *httpnet.HTTPHeaderList) {
	fam := hf.fam
	if hf.noCookies {
		return
	}
	reqPath := hf.path
	s := ""
	now := time.Now().Unix()

	var hq misc.HtsbufQueue
	hq.HtsbufQueueSetup(0)

	fam.httpCookies.mu.Lock()
	for _, hc := range fam.httpCookies.list {
		if hc.expire != -1 && now > hc.expire {
			continue
		}
		if validateCookie(reqHost, reqPath, hc.domain, hc.path) != 0 {
			continue
		}
		// C: http_header_get(extra_cookies, hc->hc_name) — presence
		// check only (even an empty value suppresses the cookie).
		if _, ok := extraCookies.Get(hc.name); ok {
			continue
		}
		// Skip overridden headers — C: LIST_FOREACH + strcmp
		// (case-sensitive)
		skip := false
		for _, hh := range extraCookies.List {
			if hh.Key == hc.name {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		hq.Append([]byte(s))
		hq.Append([]byte(hc.name))
		hq.Append([]byte("="))
		hq.Append([]byte(hc.value))
		s = "; "
	}
	fam.httpCookies.mu.Unlock()

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
