package ui

import (
	"net/url"
	"reflect"
	"strings"
	"testing"
)

// canonURL — the cookieURL of s. The inputs are canonical, as cef_parse_url
// hands them over: the table never has to canonicalize a URL in Go.
func canonURL(t *testing.T, s string) cookieURL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	host := u.Hostname()
	if strings.Contains(host, ":") {
		host = "[" + host + "]" // an IPv6 literal, bracketed as CEF gives it
	}
	return cookieURL{u.Scheme, host, u.EscapedPath()}
}

// TestCookieMatchesURL — RFC 6265 §5.4 domain and path matching, the cookie
// schemes only, Secure cookies only over https, wss or a loopback host.
// The hosts are .test names.
func TestCookieMatchesURL(t *testing.T) {
	cases := []struct {
		domain, path string
		secure       bool
		url          string
		want         bool
	}{
		// host-only cookie (no leading dot): that host only
		{"site.test", "/", false, "https://site.test/forum/index.php", true},
		{"site.test", "/", false, "https://www.site.test/", false},
		{"site.test", "/", false, "https://evilsite.test/", false},
		// domain cookie: the domain and its subdomains, on a dot boundary
		{".site.test", "/", false, "https://site.test/", true},
		{".site.test", "/", false, "https://www.site.test/", true},
		{".site.test", "/", false, "https://evilsite.test/", false},
		{".site.test", "/", false, "https://site.test.evil.example/", false},
		// path: a prefix that ends at a '/'
		{"site.test", "/forum/", false, "https://site.test/forum/", true},
		{"site.test", "/forum/", false, "https://site.test/forum/tracker.php?nm=x", true},
		{"site.test", "/forum/", false, "https://site.test/", false},
		{"site.test", "/forum/", false, "https://site.test/forumx/", false},
		{"site.test", "/forum", false, "https://site.test/forum", true},
		{"site.test", "/forum", false, "https://site.test/forum/x", true},
		{"site.test", "/forum", false, "https://site.test/forumx", false},
		{"site.test", "/", false, "https://site.test", true},
		// Secure: https, or a loopback host
		{"site.test", "/", true, "https://site.test/", true},
		{"site.test", "/", true, "http://site.test/", false},
		{"127.0.0.1", "/", true, "http://127.0.0.1:18999/", true},
		{"localhost", "/", true, "http://localhost:18999/", true},
		{"sub.localhost", "/", true, "http://sub.localhost:18999/", true},
		{"site.test", "/", false, "http://site.test/", true},
		// an IP address never domain-matches another host
		{".0.0.1", "/", false, "http://127.0.0.1/", false},
		// wss is a secure scheme, as https is
		{"site.test", "/", true, "wss://site.test/ws", true},
		{"site.test", "/", true, "ws://site.test/ws", false},
		{"site.test", "/", false, "ws://site.test/ws", true},
		// only the cookie schemes
		{"site.test", "/", false, "ftp://site.test/", false},
		{"site.test", "/", false, "file:///site.test", false},
		{".site.test", "/", false, "ftp://www.site.test/", false},
		// IPv6 literal: CEF brackets the host, and the bracketed form is what it stores
		{"[::1]", "/", true, "http://[::1]:18999/", true},
		{"[::1]", "/", true, "http://[::1]/", true},
		{"::1", "/", false, "http://[::1]/", false},
	}
	for _, c := range cases {
		if got := cookieMatchesURL(c.domain, c.path, c.secure, canonURL(t, c.url)); got != c.want {
			t.Errorf("cookie domain=%q path=%q secure=%v for %s: got %v, want %v",
				c.domain, c.path, c.secure, c.url, got, c.want)
		}
	}
}

func cv(name, value, domain, path string, secure bool) cookieVisit {
	return cookieVisit{name: name, value: value, domain: domain, path: path, secure: secure}
}

// TestScopeCookies — one cookie per name for the URL, fail closed on a tie
// the Go side cannot resolve (same name, same best path: host-only + domain
// cookie, or an unlabeled CHIPS partition).
func TestScopeCookies(t *testing.T) {
	cases := []struct {
		name          string
		visits        []cookieVisit
		url           string
		want          map[string]string
		wantAmbiguous int
	}{
		{name: "empty store: an empty map, not nil",
			url: "http://127.0.0.1:18999/", want: map[string]string{}},
		{name: "host-only does not reach a subdomain, a domain cookie does",
			visits: []cookieVisit{
				cv("dom", "1", ".localhost", "/", false),
				cv("host", "1", "localhost", "/", false)},
			url: "http://sub.localhost:18999/", want: map[string]string{"dom": "1"}},
		{name: "dot boundary: evilsite.test is not site.test",
			visits: []cookieVisit{cv("a", "1", ".site.test", "/", false)},
			url:    "https://evilsite.test/", want: map[string]string{}},
		{name: "path: /forum does not match /forumx",
			visits: []cookieVisit{cv("a", "1", "site.test", "/forum", false)},
			url:    "https://site.test/forumx", want: map[string]string{}},
		{name: "Secure cookie over http is not returned; the plain one is",
			visits: []cookieVisit{
				cv("tok", "s", "site.test", "/", true),
				cv("plain", "p", "site.test", "/", false)},
			url: "http://site.test/", want: map[string]string{"plain": "p"}},
		{name: "name collision: the longest matching path wins, whatever the visit order",
			visits: []cookieVisit{
				cv("sid", "root", "site.test", "/", false),
				cv("sid", "forum", "site.test", "/forum/", false)},
			url: "https://site.test/forum/x", want: map[string]string{"sid": "forum"}},
		{name: "a non-matching same-name visit never blocks a later match",
			visits: []cookieVisit{
				cv("sid", "other", "other.test", "/forum/", false),
				cv("sid", "ours", "site.test", "/", false)},
			url: "https://site.test/forum/", want: map[string]string{"sid": "ours"}},
		{name: "cross-partition tie: same name/domain/path twice is withheld",
			visits: []cookieVisit{
				cv("cf", "partA", ".site.test", "/", true),
				cv("cf", "partB", ".site.test", "/", true),
				cv("ok", "1", ".site.test", "/", true)},
			url: "https://www.site.test/", want: map[string]string{"ok": "1"}, wantAmbiguous: 1},
		{name: "tie across host-only and domain cookie is withheld",
			visits: []cookieVisit{
				cv("sid", "h", "site.test", "/", false),
				cv("sid", "d", ".site.test", "/", false)},
			url: "https://site.test/", want: map[string]string{}, wantAmbiguous: 1},
		{name: "a longer path resolves what a shorter-path tie would not",
			visits: []cookieVisit{
				cv("sid", "a", "site.test", "/", false),
				cv("sid", "b", ".site.test", "/", false),
				cv("sid", "deep", "site.test", "/forum", false)},
			url: "https://site.test/forum/x", want: map[string]string{"sid": "deep"}},
		{name: "a tie that is not the best path does not hide the winner",
			visits: []cookieVisit{
				cv("sid", "deep", "site.test", "/forum", false),
				cv("sid", "a", "site.test", "/", false),
				cv("sid", "b", ".site.test", "/", false)},
			url: "https://site.test/forum/x", want: map[string]string{"sid": "deep"}},
		{name: "non-cookie scheme gets none",
			visits: []cookieVisit{cv("a", "1", "site.test", "/", false)},
			url:    "ftp://site.test/", want: map[string]string{}},
		{name: "empty cookie name is dropped",
			visits: []cookieVisit{cv("", "x", "site.test", "/", false)},
			url:    "https://site.test/", want: map[string]string{}},
	}
	for _, c := range cases {
		list, amb := scopeCookies(c.visits, canonURL(t, c.url))
		if got := CookieMap(list); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
		if amb != c.wantAmbiguous {
			t.Errorf("%s: ambiguous=%d, want %d", c.name, amb, c.wantAmbiguous)
		}
		if list == nil {
			t.Errorf("%s: nil list", c.name)
		}
	}
}

// TestScopeCookiesKeepsMetadata — WebpopupCookie carries the attributes the
// visitor reported, so later consumers never re-derive them.
func TestScopeCookiesKeepsMetadata(t *testing.T) {
	v := cookieVisit{name: "n", value: "v", domain: ".site.test", path: "/a",
		secure: true, httpOnly: true, expires: 1900000000}
	list, _ := scopeCookies([]cookieVisit{v}, canonURL(t, "https://x.site.test/a/b"))
	want := []WebpopupCookie{{Name: "n", Value: "v", Domain: ".site.test", Path: "/a",
		Secure: true, HTTPOnly: true, Expires: 1900000000}}
	if !reflect.DeepEqual(list, want) {
		t.Fatalf("got %+v, want %+v", list, want)
	}
}

func TestCookieWaitSatisfied(t *testing.T) {
	u := canonURL(t, "https://site.test/")
	cases := []struct {
		name   string
		visits []cookieVisit
		want   bool
	}{
		{"present", []cookieVisit{cv("cf", "x", "site.test", "/", true)}, true},
		{"absent", nil, false},
		{"other name", []cookieVisit{cv("zz", "x", "site.test", "/", true)}, false},
		{"empty value", []cookieVisit{cv("cf", "", "site.test", "/", true)}, false},
		{"other host", []cookieVisit{cv("cf", "x", "other.test", "/", true)}, false},
		{"ambiguous never completes", []cookieVisit{
			cv("cf", "a", ".site.test", "/", true),
			cv("cf", "b", ".site.test", "/", true)}, false},
	}
	for _, c := range cases {
		if got := cookieWaitSatisfied(c.visits, u, "cf"); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCookieWaitURL(t *testing.T) {
	cases := []struct {
		open, wait, name string
		want             cookieURL
		ok               bool
	}{
		{"https://site.test/login", "https://site.test/", "cf", cookieURL{"https", "site.test", "/"}, true},
		{"https://Site.Test/login", "https://site.test/x", "cf", cookieURL{"https", "site.test", "/x"}, true},
		{"https://site.test:8443/l", "https://site.test:8443", "cf", cookieURL{"https", "site.test", "/"}, true},
		{"https://site.test/l", "https://site.test:443/", "cf", cookieURL{"https", "site.test", "/"}, true},
		{"https://site.test/l", "https://www.site.test/", "cf", cookieURL{}, false},   // not the exact origin
		{"https://site.test/l", "https://site.test:8443/", "cf", cookieURL{}, false},  // other port
		{"https://site.test/l", "http://site.test/", "cf", cookieURL{}, false},        // plain http
		{"http://site.test/l", "https://site.test/", "cf", cookieURL{}, false},        // opener not https
		{"https://site.test/l", "https://u:p@site.test/", "cf", cookieURL{}, false},   // userinfo
		{"https://site.test/l", "https://site.test:99999/", "cf", cookieURL{}, false}, // bad port
		{"https://site.test/l", "https://site.test/", "", cookieURL{}, false},         // no name
		{"https://[::1]/l", "https://[::1]/", "cf", cookieURL{"https", "[::1]", "/"}, true},
	}
	for _, c := range cases {
		got, ok := cookieWaitURL(c.open, c.wait, c.name)
		if ok != c.ok || got != c.want {
			t.Errorf("open=%q wait=%q name=%q: got %+v,%v want %+v,%v",
				c.open, c.wait, c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestCefBaseTimeUnix(t *testing.T) {
	cases := []struct {
		micros int64
		want   int64
		ok     bool
	}{
		{0, 0, false},
		{-1, 0, false},
		{cefEpochOffsetSeconds * 1000000, 0, false}, // exactly the Unix epoch: no expiry
		{(cefEpochOffsetSeconds + 1_900_000_000) * 1000000, 1_900_000_000, true},
		{(cefEpochOffsetSeconds-5)*1000000 + 7, 0, false}, // before 1970
	}
	for _, c := range cases {
		if got, ok := cefBaseTimeUnix(c.micros); got != c.want || ok != c.ok {
			t.Errorf("%d: got %d,%v want %d,%v", c.micros, got, ok, c.want, c.ok)
		}
	}
}

// fakeCanon mimics the parts of cef_parse_url that matter here: dot
// segments are resolved and a Unicode host becomes its punycode form.
func fakeCanon(uri string) (cookieURL, bool) {
	rest, ok := strings.CutPrefix(uri, "https://")
	if !ok {
		return cookieURL{}, false
	}
	host, path, _ := strings.Cut(rest, "/")
	path = "/" + path
	if host == "bücher.test" {
		host = "xn--bcher-kva.test"
	}
	for strings.Contains(path, "/a/../") {
		path = strings.Replace(path, "/a/../", "/", 1)
	}
	return cookieURL{"https", host, path}, true
}

func TestCanonWaitTargetResolvesAndKeepsOrigin(t *testing.T) {
	target, ok := cookieWaitURL("https://site.test/", "https://site.test/a/../login", "sid")
	if !ok {
		t.Fatal("wait URL rejected")
	}
	canon, ok := canonWaitTarget(fakeCanon, "https://site.test/", target)
	if !ok || canon.path != "/login" {
		t.Fatalf("canonical target = %+v, %v; want path /login", canon, ok)
	}
	visits := []cookieVisit{{name: "sid", value: "x", domain: "site.test", path: "/login"}}
	if cookieWaitSatisfied(visits, target, "sid") {
		t.Fatal("precondition: uncanonical target should not match Path=/login")
	}
	if !cookieWaitSatisfied(visits, canon, "sid") {
		t.Fatal("canonical target must match Path=/login")
	}

	idn, ok := cookieWaitURL("https://bücher.test/", "https://bücher.test/a/../login", "sid")
	if !ok {
		t.Fatal("IDN wait URL rejected")
	}
	canon, ok = canonWaitTarget(fakeCanon, "https://bücher.test/", idn)
	if !ok || canon.host != "xn--bcher-kva.test" || canon.path != "/login" {
		t.Fatalf("IDN canonical target = %+v, %v", canon, ok)
	}

	if _, ok := canonWaitTarget(fakeCanon, "https://other.test/", target); ok {
		t.Fatal("different canonical host must be rejected")
	}
	reject := func(string) (cookieURL, bool) { return cookieURL{}, false }
	if _, ok := canonWaitTarget(reject, "https://site.test/", target); ok {
		t.Fatal("URL the canonicalizer rejects must fail closed")
	}
}
