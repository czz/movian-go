package fileaccess

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	httpnet "github.com/czz/movian-go/internal/networking/http"
)

// Characterization cases that should pass on the pre-fix master.
// Values and domains are synthetic. No external connections are used.

func TestHTTPCookieBaselineInspectorOverride(t *testing.T) {
	fam := &FileAccessManager{}
	fam.httpCookies.list = []*httpCookie{
		{name: "session", value: "jar-value", domain: "example.test", path: "/", expire: -1},
	}
	var explicit, out httpnet.HTTPHeaderList
	explicit.Add("session", "explicit-value", false)
	hf := &httpFile{fam: fam, url: "https://example.test/path", path: "/path"}
	httpCookieAppend("example.test", hf, &out, &explicit)
	got, ok := out.Get("Cookie")
	if !ok || got != "session=explicit-value" {
		t.Fatalf("inspector override broken (present=%t); expected one explicit cookie", ok)
	}
}

func TestHTTPCookieBaselineSharedH1H2Jar(t *testing.T) {
	fam := &FileAccessManager{}
	fam.httpCookies.list = []*httpCookie{
		{name: "shared", value: "synthetic", domain: "example.test", path: "/", expire: -1},
	}
	var extra, headers httpnet.HTTPHeaderList
	hf := &httpFile{fam: fam, path: "/", url: "https://example.test/"}
	httpCookieAppend("example.test", hf, &headers, &extra)
	gotH1, _ := headers.Get("Cookie")
	gotH2 := (&h2File{fam: fam, url: "https://example.test/"}).h2CookieHeader("example.test", "/", &extra)
	if gotH1 != "shared=synthetic" || gotH2 != gotH1 {
		t.Fatalf("shared-jar mismatch: http1=%q http2=%q", gotH1, gotH2)
	}
}

// Inspector cookies across redirects (HTTP/2 path, h2Issue). h2NewRequest
// re-runs the inspector chain for every hop and builds fresh header and
// cookie lists, so an inspector cookie is never carried to the next hop.
// Loopback httptest servers only; "localhost" vs "127.0.0.1" stand in for
// two different hosts.

type redirectProbe struct {
	mu      sync.Mutex
	cookies map[string]string // server label -> Cookie header seen
}

func (p *redirectProbe) handler(label, redirectTo string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.cookies[label] = r.Header.Get("Cookie")
		p.mu.Unlock()
		if redirectTo != "" {
			http.Redirect(w, r, redirectTo, http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

func (p *redirectProbe) seen(label string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cookies[label]
}

// h2Fetch opens target through h2Issue with an inspector that sets
// cf=synthetic only for requests accepted by want(url).
func h2Fetch(t *testing.T, target string, want func(u *url.URL) bool) {
	t.Helper()
	fam := &FileAccessManager{}
	fam.HTTPRequestInspectorRegister(&HTTPRequestInspector{
		Check: func(raw string, hri *HTTPRequestInspection) bool {
			if u, err := url.Parse(raw); err == nil && want(u) {
				HTTPClientSetCookie(hri, "cf", "synthetic")
			}
			return true
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hf := &h2File{fam: fam, url: target, client: fam.http2Client(false),
		ctx: ctx, cancel: cancel, size: -1}
	if err := hf.h2Issue(0); err != nil {
		t.Fatalf("h2Issue: %v", err)
	}
	hf.body.Close()
}

func TestHTTPCookieBaselineInspectorCookieCrossHostRedirect(t *testing.T) {
	p := &redirectProbe{cookies: map[string]string{}}
	b := httptest.NewTLSServer(p.handler("b", ""))
	defer b.Close()
	bu, _ := url.Parse(b.URL)
	a := httptest.NewTLSServer(p.handler("a",
		"https://localhost:"+bu.Port()+"/"))
	defer a.Close()

	// Inspector sets the cookie for 127.0.0.1 only.
	h2Fetch(t, a.URL, func(u *url.URL) bool { return u.Hostname() == "127.0.0.1" })
	if got := p.seen("a"); got != "cf=synthetic" {
		t.Fatalf("control: first hop should carry the inspector cookie, got present=%t", got != "")
	}
	if got := p.seen("b"); got != "" {
		t.Fatalf("inspector cookie for the first host reached the redirect target (present=%t)", got != "")
	}

	// Control: when the inspector sets it again for the new hop, it is sent.
	h2Fetch(t, a.URL, func(u *url.URL) bool { return true })
	if got := p.seen("b"); got != "cf=synthetic" {
		t.Fatalf("control: re-set inspector cookie missing on second hop, present=%t", got != "")
	}
}

func TestHTTPCookieBaselineInspectorCookieHTTPSDowngradeRedirect(t *testing.T) {
	p := &redirectProbe{cookies: map[string]string{}}
	b := httptest.NewServer(p.handler("b", ""))
	defer b.Close()
	a := httptest.NewUnstartedServer(p.handler("a", b.URL+"/"))
	a.TLS = &tls.Config{}
	a.StartTLS()
	defer a.Close()

	// Inspector sets the cookie for the HTTPS request only.
	h2Fetch(t, a.URL, func(u *url.URL) bool { return strings.EqualFold(u.Scheme, "https") })
	if got := p.seen("a"); got != "cf=synthetic" {
		t.Fatalf("control: HTTPS hop should carry the inspector cookie, present=%t", got != "")
	}
	if got := p.seen("b"); got != "" {
		t.Fatalf("inspector cookie for the HTTPS request was sent over HTTP (present=%t)", got != "")
	}
}
