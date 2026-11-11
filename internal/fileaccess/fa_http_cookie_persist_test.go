package fileaccess

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	httpnet "github.com/czz/movian-go/internal/networking/http"
)

func testCookieSetOn(fam *FileAccessManager, origin, header string) {
	u, _ := url.Parse(origin)
	httpCookieSet(header, &httpFile{fam: fam, url: origin, path: u.EscapedPath(),
		connection: &httpConnection{hostname: u.Hostname()}})
}

func hasCookie(header, kv string) bool {
	for _, p := range strings.Split(header, "; ") {
		if p == kv {
			return true
		}
	}
	return false
}

// Max-Age parity: the same Set-Cookie must leave the same jar state on
// HTTP/1 and on HTTP/2/3 (which share h2SetCookies).
func TestCookieMaxAgeParityH1H2(t *testing.T) {
	const origin = "https://example.test/forum/login.php"
	u, _ := url.Parse(origin)
	setH2 := func(fam *FileAccessManager, hdr string) {
		(&h2File{fam: fam}).h2SetCookies(&http.Response{Header: http.Header{
			"Set-Cookie": {hdr}}}, u)
	}
	setH1 := func(fam *FileAccessManager, hdr string) { testCookieSetOn(fam, origin, hdr) }
	for name, set := range map[string]func(*FileAccessManager, string){"h1": setH1, "h2": setH2} {
		t.Run(name, func(t *testing.T) {
			fam := &FileAccessManager{}
			before := time.Now().Unix()
			set(fam, "sid=v; Path=/; Max-Age=3600; Expires=Wed, 31 Dec 2036 23:59:59 GMT")
			if len(fam.httpCookies.list) != 1 {
				t.Fatalf("cookies=%d, want 1", len(fam.httpCookies.list))
			}
			got := fam.httpCookies.list[0].expire
			if got < before+3600 || got > time.Now().Unix()+3600 {
				t.Fatalf("expire=%d, Max-Age must override Expires", got)
			}
			set(fam, "sid=; Path=/; Max-Age=0")
			if n := len(fam.httpCookies.list); n != 0 {
				t.Fatalf("Max-Age=0 left %d cookies", n)
			}
		})
	}
}

// Persist round trip: Secure, host-only and expiry survive save + load,
// and a record without the newer fields loads with the earlier behaviour
// (domain-wide, not Secure).
func TestCookiePersistRoundTrip(t *testing.T) {
	src := &FileAccessManager{}
	var saved []HTTPCookieRecord
	src.httpCookies.cookieSave = func(_ any, r []HTTPCookieRecord) { saved = r }
	exp := time.Now().Add(24 * time.Hour).UTC().Format(http.TimeFormat)
	testCookieSetOn(src, "https://example.test/", "a=1; Secure; Path=/; Expires="+exp)
	testCookieSetOn(src, "https://example.test/", "b=2; Domain=example.test; Path=/; Expires="+exp)
	testCookieSetOn(src, "https://example.test/", "sess=3; Path=/")
	src.cookiePersist(nil, nil)
	if len(saved) != 2 {
		t.Fatalf("saved %d records, want 2 (session cookie excluded)", len(saved))
	}

	old := HTTPCookieRecord{Name: "old", Path: "/", Domain: "example.test",
		Value: "4", Expire: time.Now().Add(time.Hour).Unix()}
	dst := &FileAccessManager{}
	dst.httpCookies.cookieLoad = func(any) []HTTPCookieRecord { return append(saved, old) }
	dst.loadCookies()

	_, httpsHost := testCookieSemanticsSend(dst, "https://example.test/")
	for _, want := range []string{"a=1", "b=2", "old=4"} {
		if !hasCookie(httpsHost, want) {
			t.Fatalf("after reload https example.test lost %s: %q", want, httpsHost)
		}
	}
	_, plainSub := testCookieSemanticsSend(dst, "http://sub.example.test/")
	if hasCookie(plainSub, "a=1") {
		t.Fatalf("Secure/host-only a=1 sent over http to a subdomain: %q", plainSub)
	}
	if !hasCookie(plainSub, "b=2") || !hasCookie(plainSub, "old=4") {
		t.Fatalf("domain-wide cookies must reach the subdomain: %q", plainSub)
	}
	_, plainHost := testCookieSemanticsSend(dst, "http://example.test/")
	if hasCookie(plainHost, "a=1") {
		t.Fatalf("Secure cookie sent over http after reload: %q", plainHost)
	}
	for _, hc := range dst.httpCookies.list {
		if hc.name == "a" && (!hc.secure || !hc.hostOnly || hc.expire <= time.Now().Unix()) {
			t.Fatalf("cookie a lost attributes on reload: %+v", *hc)
		}
	}
}

// A jar-held Secure cookie must not follow an https -> http redirect:
// Secure is judged on the URL being requested, not the original one.
func TestCookieSecureNotSentAfterHTTPSDowngradeRedirect(t *testing.T) {
	p := &redirectProbe{cookies: map[string]string{}}
	b := httptest.NewServer(p.handler("b", ""))
	defer b.Close()
	a := httptest.NewUnstartedServer(p.handler("a", b.URL+"/"))
	a.TLS = &tls.Config{}
	a.StartTLS()
	defer a.Close()

	fam := &FileAccessManager{}
	fam.cookieJarPut(httpCookie{name: "sec", value: "v", domain: "127.0.0.1",
		path: "/", expire: -1, secure: true, hostOnly: true})
	fam.cookieJarPut(httpCookie{name: "plain", value: "v", domain: "127.0.0.1",
		path: "/", expire: -1, hostOnly: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hf := &h2File{fam: fam, url: a.URL, client: fam.http2Client(false),
		ctx: ctx, cancel: cancel, size: -1}
	if err := hf.h2Issue(0); err != nil {
		t.Fatalf("h2Issue: %v", err)
	}
	hf.body.Close()
	if got := p.seen("a"); !hasCookie(got, "sec=v") {
		t.Fatalf("control: https hop must carry the Secure cookie, got %q", got)
	}
	got := p.seen("b")
	if hasCookie(got, "sec=v") {
		t.Fatalf("Secure cookie sent over the http redirect target: %q", got)
	}
	if !hasCookie(got, "plain=v") {
		t.Fatalf("control: non-Secure cookie missing on the http hop: %q", got)
	}
}

// Flag attributes with an attached value are still flags, on both paths.
func TestCookieValuedFlagAttributesParity(t *testing.T) {
	const origin = "https://example.test/"
	u, _ := url.Parse(origin)
	setH2 := func(fam *FileAccessManager, hdr string) {
		(&h2File{fam: fam}).h2SetCookies(&http.Response{Header: http.Header{
			"Set-Cookie": {hdr}}}, u)
	}
	setH1 := func(fam *FileAccessManager, hdr string) { testCookieSetOn(fam, origin, hdr) }
	for name, set := range map[string]func(*FileAccessManager, string){"h1": setH1, "h2": setH2} {
		t.Run(name, func(t *testing.T) {
			fam := &FileAccessManager{}
			set(fam, "part=v; Secure; Partitioned=1; Path=/")
			if n := len(fam.httpCookies.list); n != 0 {
				t.Fatalf("Partitioned=1 cookie stored (%d)", n)
			}
			set(fam, "sid=v; Secure=1; Path=/")
			_, h2 := testCookieSemanticsSend(fam, "http://example.test/")
			h1, _ := testCookieSemanticsSend(fam, "http://example.test/")
			if h1 != "" || h2 != "" {
				t.Fatalf("Secure=1 cookie sent over http: h1=%q h2=%q", h1, h2)
			}
		})
	}
}

// HTTP/1: the connection decides Secure, not the original URL. A cached
// permanent redirect can leave hf.url at https over a plaintext socket.
func TestCookieSecureFollowsHTTP1Connection(t *testing.T) {
	fam := &FileAccessManager{}
	fam.cookieJarPut(httpCookie{name: "sec", value: "v", domain: "example.test",
		path: "/", expire: -1, secure: true, hostOnly: true})
	send := func(ssl bool) string {
		var extra, headers httpnet.HTTPHeaderList
		httpCookieAppend("example.test", 0, &httpFile{fam: fam, path: "/",
			url:        "https://example.test/",
			connection: &httpConnection{hostname: "example.test", ssl: ssl}},
			&headers, &extra)
		h, _ := headers.Get("Cookie")
		return h
	}
	if got := send(false); got != "" {
		t.Fatalf("Secure cookie sent over a plaintext connection: %q", got)
	}
	if got := send(true); got != "sec=v" {
		t.Fatalf("control: TLS connection must carry it, got %q", got)
	}
}
