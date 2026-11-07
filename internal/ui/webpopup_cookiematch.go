// webpopup_cookiematch.go — which cookies of the CEF store belong to a
// URL, and what the plugin-facing cookie API may return. No build tags and
// no cgo, so `go test ./internal/ui` covers it on every build.
//
// The source is the URL-scoped CEF visitor, never the whole cookie store.
package ui

import (
	"net"
	"net/url"
	"strconv"
	"strings"
)

// WebpopupCookie — the one cookie type of the webpopup API. CEF, Android
// and the ecmascript layer all describe a browser cookie with it; webcookies()
// flattens it to name→value only for plugin compatibility.
//
// CEF's cookie visitor does not report the Chromium partition key, so a
// WebpopupCookie never claims one: only unpartitioned, URL-scoped cookies
// are ever turned into this type (see ScopeCookies).
type WebpopupCookie struct {
	Name     string
	Value    string // secret: never log it
	Domain   string // as the browser stores it; a leading dot = domain cookie
	Path     string
	Secure   bool
	HTTPOnly bool
	// Expires is Unix seconds; 0 is a session cookie.
	Expires int64
}

// cookieURL — a webcookies() URL as cef_parse_url (GURL) canonicalized it:
// the scheme, the host (an IPv6 literal in brackets) and the path with dot
// segments removed. The Go side canonicalizes nothing itself.
type cookieURL struct {
	scheme, host, path string
}

// cookieVisit — one cookie of the CEF store as the visitor hands it over
// (the domain as CEF stores it).
type cookieVisit struct {
	name, value, domain, path string
	secure, httpOnly          bool
	expires                   int64 // Unix seconds; 0 = session cookie
}

const cefEpochOffsetSeconds = 11644473600

// cefBaseTimeUnix converts a CEF cef_basetime_t (microseconds since the
// Windows epoch) to Unix seconds; 0 and pre-Unix values are not an expiry.
func cefBaseTimeUnix(micros int64) (int64, bool) {
	if micros <= 0 {
		return 0, false
	}
	secs := micros / 1000000
	if secs <= cefEpochOffsetSeconds {
		return 0, false
	}
	return secs - cefEpochOffsetSeconds, true
}

// cookieMatchesURL — RFC 6265 §5.4 for one cookie of the CEF store, its
// domain as CEF stores it (a leading dot marks a domain cookie, none a
// host-only one): only the http, https, ws and wss schemes, domain-match,
// path-match, and a Secure cookie only over https, wss or to a loopback
// host, which Chromium treats as secure too. SameSite is not checked.
func cookieMatchesURL(domain, path string, secure bool, u cookieURL) bool {
	switch u.scheme {
	case "http", "https", "ws", "wss":
	default:
		return false
	}
	if d, ok := strings.CutPrefix(domain, "."); ok {
		if u.host != d && (isIP(u.host) || !strings.HasSuffix(u.host, "."+d)) {
			return false
		}
	} else if u.host != domain {
		return false
	}
	if path == "" {
		path = "/"
	}
	p := u.path
	if p == "" {
		p = "/"
	}
	if p != path && !(strings.HasPrefix(p, path) &&
		(strings.HasSuffix(path, "/") || p[len(path)] == '/')) {
		return false
	}
	return !secure || u.scheme == "https" || u.scheme == "wss" ||
		loopbackHost(u.host)
}

// scopeCookies — the cookies of visits that a request to u carries, one
// per name. It fails closed:
//
//   - a visit that does not match u is ignored (and never blocks a later one);
//   - among same-name matches the longest path wins (RFC 6265 §5.4);
//   - if the best path is shared by two or more matches of one name, the
//     name is dropped and counted in ambiguous. An unpartitioned jar holds
//     one cookie per (name, domain, path), so a tie means either a
//     host-only plus a domain cookie, or cookies of different CHIPS
//     partitions that CEF did not label; the Go side cannot tell which, and
//     a name→value map cannot hold both.
//
// The result is never nil.
func scopeCookies(visits []cookieVisit, u cookieURL) (cookies []WebpopupCookie, ambiguous int) {
	type slot struct {
		best cookieVisit
		ties int
	}
	slots := map[string]*slot{}
	var order []string
	for _, c := range visits {
		if c.name == "" || !cookieMatchesURL(c.domain, c.path, c.secure, u) {
			continue
		}
		s, ok := slots[c.name]
		switch {
		case !ok:
			slots[c.name] = &slot{best: c, ties: 1}
			order = append(order, c.name)
		case len(c.path) > len(s.best.path):
			s.best, s.ties = c, 1
		case len(c.path) == len(s.best.path):
			s.ties++
		}
	}
	cookies = []WebpopupCookie{}
	for _, name := range order {
		s := slots[name]
		if s.ties > 1 {
			ambiguous++
			continue
		}
		cookies = append(cookies, WebpopupCookie{
			Name: name, Value: s.best.value, Domain: s.best.domain,
			Path: s.best.path, Secure: s.best.secure,
			HTTPOnly: s.best.httpOnly, Expires: s.best.expires,
		})
	}
	return cookies, ambiguous
}

// CookieMap flattens cookies to the legacy webcookies() shape. Names are
// unique after scopeCookies, so nothing is overwritten. The result is never nil.
func CookieMap(cookies []WebpopupCookie) map[string]string {
	m := make(map[string]string, len(cookies))
	for _, c := range cookies {
		m[c.Name] = c.Value
	}
	return m
}

// cookieWaitSatisfied — the awaited cookie is present, nonempty and
// unambiguous for u. Ambiguity does not complete a wait.
func cookieWaitSatisfied(visits []cookieVisit, u cookieURL, name string) bool {
	cookies, _ := scopeCookies(visits, u)
	for _, c := range cookies {
		if c.Name == name && c.Value != "" {
			return true
		}
	}
	return false
}

// cookieWaitURL validates a cookie-aware popup target. Waiting is HTTPS-only
// and confined to the opener's exact origin (not merely its cookie domain).
func cookieWaitURL(openURL, waitURL, name string) (cookieURL, bool) {
	if name == "" {
		return cookieURL{}, false
	}
	open, err := url.Parse(openURL)
	if err != nil || !validHTTPSOrigin(open) {
		return cookieURL{}, false
	}
	wait, err := url.Parse(waitURL)
	if err != nil || !validHTTPSOrigin(wait) {
		return cookieURL{}, false
	}
	if !strings.EqualFold(open.Hostname(), wait.Hostname()) ||
		originPort(open) != originPort(wait) {
		return cookieURL{}, false
	}
	host := strings.ToLower(wait.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	path := wait.EscapedPath()
	if path == "" {
		path = "/"
	}
	return cookieURL{scheme: "https", host: host, path: path}, true
}

// canonWaitTarget re-derives a validated wait target in the canonical form
// canon (CEF's cef_parse_url) gives, so a Path or host the browser resolves
// differently (a/../login, an IDN host) still matches its cookies. The
// exact-origin rule is kept on the canonical form: both URLs must be https
// and have the same canonical host. A URL canon rejects fails closed.
func canonWaitTarget(canon func(string) (cookieURL, bool), openURL string,
	t cookieURL) (cookieURL, bool) {
	w, ok := canon(t.scheme + "://" + t.host + t.path)
	if !ok {
		return cookieURL{}, false
	}
	o, ok := canon(openURL)
	if !ok || w.scheme != "https" || o.scheme != "https" || w.host != o.host {
		return cookieURL{}, false
	}
	return w, true
}

func validHTTPSOrigin(u *url.URL) bool {
	return u != nil && strings.EqualFold(u.Scheme, "https") &&
		u.Opaque == "" && u.User == nil && u.Hostname() != "" &&
		(u.Port() == "" || validPort(u.Port()))
}

func validPort(port string) bool {
	n, err := strconv.Atoi(port)
	return err == nil && n > 0 && n <= 65535
}

// cookieWaitPort is the explicit non-default port of a wait URL that
// cookieWaitURL accepted, "" otherwise.
func cookieWaitPort(waitURL string) string {
	u, err := url.Parse(waitURL)
	if err != nil || !validHTTPSOrigin(u) {
		return ""
	}
	if p := originPort(u); p != "443" {
		return p
	}
	return ""
}

func originPort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	return "443"
}

// isIP — host is an IP address; an IPv6 literal comes bracketed.
func isIP(host string) bool {
	return net.ParseIP(strings.Trim(host, "[]")) != nil
}

func loopbackHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
