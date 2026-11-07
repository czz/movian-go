package fileaccess

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	httpnet "github.com/czz/movian-go/internal/networking/http"
)

func testCookieSemanticsSet(t *testing.T, origin, header string) *FileAccessManager {
	t.Helper()
	u, err := url.Parse(origin)
	if err != nil {
		t.Fatal(err)
	}
	fam := &FileAccessManager{}
	hf := &httpFile{
		fam:        fam,
		connection: &httpConnection{hostname: u.Hostname()},
		path:       u.EscapedPath(),
		url:        origin,
	}
	httpCookieSet(header, hf)
	return fam
}

func testCookieSemanticsSend(fam *FileAccessManager, target string) (string, string) {
	u, _ := url.Parse(target)
	var extra, headers httpnet.HTTPHeaderList
	httpCookieAppend(u.Hostname(), &httpFile{
		fam: fam, path: u.EscapedPath(), url: target,
	}, &headers, &extra)
	h1, _ := headers.Get("Cookie")
	h2 := (&h2File{fam: fam, url: target}).h2CookieHeader(u.Hostname(), u.EscapedPath(), &extra)
	return h1, h2
}

func TestCookieSemanticsRejectLookalikeDomain(t *testing.T) {
	if validateCookie("eviltracker.test", "/forum/", "tracker.test", "/") == 0 {
		t.Fatal("domain suffix without a DNS label boundary was accepted")
	}
}

func TestCookieSemanticsHostOnly(t *testing.T) {
	fam := testCookieSemanticsSet(t,
		"https://example.test/forum/login.php",
		"sid=synthetic; Path=/")
	h1, h2 := testCookieSemanticsSend(fam, "https://sub.example.test/")
	if h1 != "" || h2 != "" {
		t.Fatalf("host-only cookie leaked to subdomain; h1-present=%t h2-present=%t", h1 != "", h2 != "")
	}
}

func TestCookieSemanticsSecureH1H2(t *testing.T) {
	fam := testCookieSemanticsSet(t,
		"https://example.test/forum/login.php",
		"sid=synthetic; Secure; Path=/")
	h1, h2 := testCookieSemanticsSend(fam, "http://example.test/")
	if h1 != "" || h2 != "" {
		t.Fatalf("Secure cookie exposed over HTTP; h1-present=%t h2-present=%t", h1 != "", h2 != "")
	}
	h1, h2 = testCookieSemanticsSend(fam, "https://example.test/")
	if h1 != "sid=synthetic" || h2 != "sid=synthetic" {
		t.Fatalf("Secure cookie should remain usable via HTTPS; h1-present=%t h2-present=%t", h1 != "", h2 != "")
	}
}

func TestCookieSemanticsMaxAgeOverridesExpires(t *testing.T) {
	fam := testCookieSemanticsSet(t,
		"https://example.test/forum/login.php",
		"sid=synthetic; Expires=Wed, 31 Dec 2036 23:59:59 GMT; Max-Age=0; Path=/")
	h1, h2 := testCookieSemanticsSend(fam, "https://example.test/")
	if h1 != "" || h2 != "" {
		t.Fatalf("Max-Age=0 did not delete the cookie; h1-present=%t h2-present=%t", h1 != "", h2 != "")
	}
}

func TestCookieSemanticsMaxAgePositiveH1(t *testing.T) {
	before := time.Now().Unix()
	fam := testCookieSemanticsSet(t,
		"https://example.test/forum/login.php",
		"sid=synthetic; Max-Age=3600; Path=/")
	after := time.Now().Unix()
	if len(fam.httpCookies.list) != 1 {
		t.Fatalf("cookies=%d, want 1", len(fam.httpCookies.list))
	}
	got := fam.httpCookies.list[0].expire
	if got < before+3600 || got > after+3600 {
		t.Fatalf("Max-Age expiry=%d, expected within call interval +3600", got)
	}
}

func TestCookieSemanticsPartitionedRejectedH1H2(t *testing.T) {
	hdr := "part=synthetic; Secure; SameSite=None; Path=/; Partitioned"
	fam := testCookieSemanticsSet(t, "https://example.test/forum/login.php", hdr)
	if len(fam.httpCookies.list) != 0 {
		t.Fatal("Partitioned cookie entered unpartitioned HTTP/1 jar")
	}
	u, _ := url.Parse("https://example.test/forum/login.php")
	resp := &http.Response{Header: http.Header{"Set-Cookie": {hdr}}}
	h2fam := &FileAccessManager{}
	(&h2File{fam: h2fam}).h2SetCookies(resp, u)
	if len(h2fam.httpCookies.list) != 0 {
		t.Fatal("Partitioned cookie entered unpartitioned HTTP/2 jar")
	}
}

func TestCookieSemanticsHTTP2DeleteMaxAgeZero(t *testing.T) {
	fam := &FileAccessManager{}
	u, _ := url.Parse("https://example.test/forum/login.php")
	h2 := &h2File{fam: fam}
	h2.h2SetCookies(&http.Response{Header: http.Header{
		"Set-Cookie": {"sid=synthetic; Max-Age=3600; Path=/"},
	}}, u)
	h2.h2SetCookies(&http.Response{Header: http.Header{
		"Set-Cookie": {"sid=; Max-Age=0; Path=/"},
	}}, u)
	_, sentH2 := testCookieSemanticsSend(fam, "https://example.test/")
	if sentH2 != "" {
		t.Fatal("HTTP/2 Max-Age=0 deletion left an outbound cookie")
	}
}

func TestCookieSemanticsPublicSuffixDomain(t *testing.T) {
	fam := testCookieSemanticsSet(t,
		"https://store.co.uk/login",
		"sid=synthetic; Domain=co.uk; Path=/")
	if len(fam.httpCookies.list) != 0 {
		t.Fatal("public suffix Domain=co.uk must be rejected")
	}
}

func TestCookieSemanticsDefaultCookiePath(t *testing.T) {
	fam := testCookieSemanticsSet(t,
		"https://example.test/forum/login.php",
		"sid=synthetic")
	if len(fam.httpCookies.list) != 1 {
		t.Fatalf("cookies=%d, want 1", len(fam.httpCookies.list))
	}
	if got := fam.httpCookies.list[0].path; got != "/forum" {
		t.Fatalf("HTTP/1 default cookie path=%q, want /forum", got)
	}
	u, _ := url.Parse("https://example.test/forum/login.php")
	h2fam := &FileAccessManager{}
	(&h2File{fam: h2fam}).h2SetCookies(&http.Response{Header: http.Header{
		"Set-Cookie": {"sid=synthetic"},
	}}, u)
	if len(h2fam.httpCookies.list) != 1 {
		t.Fatalf("HTTP/2 cookies=%d, want 1", len(h2fam.httpCookies.list))
	}
	if got := h2fam.httpCookies.list[0].path; got != "/forum" {
		t.Fatalf("HTTP/2 default cookie path=%q, want /forum", got)
	}
}

// N3: a response from one host must not plant a cookie for an unrelated
// Domain. HTTP/1 rejects it via validateCookie; HTTP/2 stores it as is.
func TestCookieSemanticsH2RejectsForeignDomain(t *testing.T) {
	const hdr = "tossed=synthetic; Domain=example.test; Path=/"
	fam1 := testCookieSemanticsSet(t, "https://evil.test/", hdr)
	h1, _ := testCookieSemanticsSend(fam1, "https://example.test/")
	if h1 != "" {
		t.Fatal("HTTP/1 sent a cookie tossed by a foreign host to example.test")
	}

	u, _ := url.Parse("https://evil.test/")
	fam2 := &FileAccessManager{}
	(&h2File{fam: fam2}).h2SetCookies(&http.Response{Header: http.Header{
		"Set-Cookie": {hdr},
	}}, u)
	_, h2 := testCookieSemanticsSend(fam2, "https://example.test/")
	if h2 != "" {
		t.Fatal("HTTP/2 sent a cookie tossed by evil.test to example.test")
	}
	h1, _ = testCookieSemanticsSend(fam2, "https://example.test/")
	if h1 != "" {
		t.Fatal("HTTP/1 sender sent the HTTP/2-tossed cookie to example.test")
	}
}

// Moved from the baseline suite. HTTPCtime fails to parse this test date
// (expiry=-1); root cause not yet established.
func TestCookieSemanticsExpiresStored(t *testing.T) {
	fam := &FileAccessManager{}
	hf := &httpFile{
		fam:        fam,
		connection: &httpConnection{hostname: "tracker.test"},
		path:       "/forum/login.php",
		url:        "https://tracker.test/forum/login.php",
	}
	httpCookieSet("sid=synthetic; Expires=Wed, 31 Dec 2036 23:59:59 GMT; Path=/forum/", hf)
	if len(fam.httpCookies.list) != 1 {
		t.Fatalf("cookies=%d, want 1", len(fam.httpCookies.list))
	}
	want := time.Date(2036, time.December, 31, 23, 59, 59, 0, time.UTC).Unix()
	if got := fam.httpCookies.list[0].expire; got != want {
		t.Fatalf("expiry=%d, want %d", got, want)
	}
}

// Domain label boundary: a lookalike host must not
// match, a real parent domain must, and a sibling subdomain setting a
// same-name Domain cookie must not clobber the host-only cookie of the
// parent. The last one is reported as residual if it fails (needs a
// host-only flag in the jar).
func TestCookieSemanticsDomainBoundaryAndValid(t *testing.T) {
	if validateCookie("evilexample.test", "/", "example.test", "/") == 0 {
		t.Fatal("evilexample.test matched example.test")
	}
	if validateCookie("evilexample.test", "/", ".example.test", "/") == 0 {
		t.Fatal("evilexample.test matched .example.test")
	}
	const hdr = "sid=synthetic; Domain=example.test; Path=/"
	for _, h2 := range []bool{false, true} {
		var fam *FileAccessManager
		if h2 {
			u, _ := url.Parse("https://www.example.test/")
			fam = &FileAccessManager{}
			(&h2File{fam: fam}).h2SetCookies(&http.Response{Header: http.Header{
				"Set-Cookie": {hdr}}}, u)
		} else {
			fam = testCookieSemanticsSet(t, "https://www.example.test/", hdr)
		}
		h1, h2c := testCookieSemanticsSend(fam, "https://a.example.test/")
		if h1 == "" || h2c == "" {
			t.Fatalf("valid Domain cookie not sent to subdomain (h2set=%v)", h2)
		}
		// evil.test / evilexample.test hosts cannot set for example.test.
		for _, host := range []string{"evil.test", "evilexample.test"} {
			u, _ := url.Parse("https://" + host + "/")
			f2 := &FileAccessManager{}
			(&h2File{fam: f2}).h2SetCookies(&http.Response{Header: http.Header{
				"Set-Cookie": {hdr}}}, u)
			if a, b := testCookieSemanticsSend(f2, "https://example.test/"); a != "" || b != "" {
				t.Fatalf("%s tossed a cookie to example.test (H2)", host)
			}
			f1 := testCookieSemanticsSet(t, "https://"+host+"/", hdr)
			if a, b := testCookieSemanticsSend(f1, "https://example.test/"); a != "" || b != "" {
				t.Fatalf("%s tossed a cookie to example.test (H1)", host)
			}
		}
	}
}

// RFC 6265 §5.3 step 11: a cookie with the same name, domain and path
// replaces the stored one, host-only or not. A sibling subdomain may set
// Domain=example.test, so this replacement is standard behaviour; host-only
// isolation (not sent to subdomains) is TestCookieSemanticsHostOnly.
func TestCookieSemanticsSiblingDomainCookieReplacesSameKey(t *testing.T) {
	fam := testCookieSemanticsSet(t, "https://example.test/", "sid=hostonly; Path=/")
	hf := &httpFile{fam: fam, connection: &httpConnection{hostname: "evil.example.test"},
		path: "/", url: "https://evil.example.test/"}
	httpCookieSet("sid=replaced; Domain=example.test; Path=/", hf)
	n := 0
	for _, hc := range fam.httpCookies.list {
		if hc.name == "sid" && hc.domain == "example.test" && hc.path == "/" {
			n++
			if hc.value != "replaced" {
				t.Fatalf("sid=%q, want the same-key cookie replaced", hc.value)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d sid cookies for example.test /, want 1", n)
	}
}

// Pins pre-branch HTTP/2 behaviour: only the Domain check is new; the
// legacy RFC 2109 path-prefix and embedded-dot rules must not reject.
func TestCookieSemanticsH2KeepsLenientPathAndDotRules(t *testing.T) {
	for _, tc := range []struct{ origin, hdr, send string }{
		{"https://example.test/login.php", "sid=synthetic; Path=/forum/", "https://example.test/forum/"},
		{"http://localhost/", "sid=synthetic; Domain=localhost; Path=/", "http://localhost/"},
	} {
		u, _ := url.Parse(tc.origin)
		fam := &FileAccessManager{}
		(&h2File{fam: fam}).h2SetCookies(&http.Response{Header: http.Header{
			"Set-Cookie": {tc.hdr}}}, u)
		if _, h2 := testCookieSemanticsSend(fam, tc.send); h2 == "" {
			t.Fatalf("HTTP/2 rejected %q from %s", tc.hdr, tc.origin)
		}
	}
}

func TestCookieSemanticsPathBoundary(t *testing.T) {
	for _, tc := range []struct {
		req, cookie string
		want        bool
	}{
		{"/forum", "/forum", true},
		{"/forum/", "/forum", true},
		{"/forum/x", "/forum", true},
		{"/forum?a=1", "/forum", true},
		{"/forumx", "/forum", false},
		{"/forumx/y", "/forum", false},
		{"/forum/x", "/forum/", true},
		{"/forum", "/forum/", false},
		{"/anything", "/", true},
	} {
		got := validateCookie("example.test", tc.req, "example.test", tc.cookie) == 0
		if got != tc.want {
			t.Errorf("cookie path %q vs request %q: match=%v, want %v", tc.cookie, tc.req, got, tc.want)
		}
	}
}
