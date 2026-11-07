package fileaccess

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/czz/movian-go/internal/misc"
	httpnet "github.com/czz/movian-go/internal/networking/http"
)

const (
	clrOwner  = "plugin.a"
	clrOrigin = "https://example.test"
	clrUA     = "Mozilla/5.0 Synthetic/1.0"
)

func clrCookie() ImportedCookie {
	return ImportedCookie{
		Owner: clrOwner, Origin: clrOrigin, Name: "cf_clearance",
		Value: "synthetic-value", UA: clrUA, Path: "/",
		Expire: time.Now().Add(24 * time.Hour).Unix(),
	}
}

func clrFAM(t *testing.T) *FileAccessManager {
	t.Helper()
	fam := &FileAccessManager{}
	if err := fam.HTTPImportClearance(clrCookie()); err != nil {
		t.Fatalf("import: %v", err)
	}
	return fam
}

// clrSend builds the Cookie header over HTTP/1 and HTTP/2 for target; owner
// is the context that opted in ("" = a plain request without the flag).
func clrSend(fam *FileAccessManager, target, owner string) (string, string) {
	u, _ := url.Parse(target)
	var extra, headers httpnet.HTTPHeaderList
	httpCookieAppend(u.Hostname(), urlPortNum(u), &httpFile{
		fam: fam, path: u.EscapedPath(), url: target, importedOwner: owner,
	}, &headers, &extra)
	h1, _ := headers.Get("Cookie")
	h2 := (&h2File{fam: fam, url: target, importedOwner: owner}).
		h2CookieHeader(u.Hostname(), urlPortNum(u), u.EscapedPath(), &extra)
	return h1, h2
}

func TestImportClearanceValidation(t *testing.T) {
	future := time.Now().Add(time.Hour).Unix()
	mut := func(f func(*ImportedCookie)) ImportedCookie {
		c := clrCookie()
		c.Expire = future
		f(&c)
		return c
	}
	bad := map[string]ImportedCookie{
		"http origin":          mut(func(c *ImportedCookie) { c.Origin = "http://example.test" }),
		"no scheme":            mut(func(c *ImportedCookie) { c.Origin = "example.test" }),
		"origin with path":     mut(func(c *ImportedCookie) { c.Origin = "https://example.test/x" }),
		"origin with userinfo": mut(func(c *ImportedCookie) { c.Origin = "https://u@example.test" }),
		"origin with port":     mut(func(c *ImportedCookie) { c.Origin = "https://example.test:8443" }),
		"ipv4 literal":         mut(func(c *ImportedCookie) { c.Origin = "https://192.0.2.7" }),
		"ipv6 literal":         mut(func(c *ImportedCookie) { c.Origin = "https://[2001:db8::1]" }),
		"public suffix":        mut(func(c *ImportedCookie) { c.Origin = "https://co.uk" }),
		"bare tld":             mut(func(c *ImportedCookie) { c.Origin = "https://com" }),
		"non-ascii host":       mut(func(c *ImportedCookie) { c.Origin = "https://exämple.test" }),
		"empty name":           mut(func(c *ImportedCookie) { c.Name = "" }),
		"empty value":          mut(func(c *ImportedCookie) { c.Value = "" }),
		"empty ua":             mut(func(c *ImportedCookie) { c.UA = "" }),
		"empty owner":          mut(func(c *ImportedCookie) { c.Owner = "" }),
		"newline in value":     mut(func(c *ImportedCookie) { c.Value = "a\r\nSet-Cookie: x=y" }),
		"semicolon in value":   mut(func(c *ImportedCookie) { c.Value = "a; b=c" }),
		"control in name":      mut(func(c *ImportedCookie) { c.Name = "a\x01b" }),
		"equals in name":       mut(func(c *ImportedCookie) { c.Name = "a=b" }),
		"control in ua":        mut(func(c *ImportedCookie) { c.UA = "ua\r\nX: y" }),
		"control in owner":     mut(func(c *ImportedCookie) { c.Owner = "o\x00" }),
		"control in path":      mut(func(c *ImportedCookie) { c.Path = "/a\r\n" }),
		"relative path":        mut(func(c *ImportedCookie) { c.Path = "a/b" }),
		"past expiry":          mut(func(c *ImportedCookie) { c.Expire = time.Now().Add(-time.Minute).Unix() }),
		"session expiry":       mut(func(c *ImportedCookie) { c.Expire = -1 }),
		"zero expiry":          mut(func(c *ImportedCookie) { c.Expire = 0 }),
		"huge value":           mut(func(c *ImportedCookie) { c.Value = strings.Repeat("a", 5000) }),
	}
	for name, c := range bad {
		t.Run("reject/"+name, func(t *testing.T) {
			fam := &FileAccessManager{}
			err := fam.HTTPImportClearance(c)
			if err == nil {
				t.Fatal("import accepted")
			}
			if c.Value != "" && strings.Contains(err.Error(), c.Value) {
				t.Fatalf("error leaks the value: %v", err)
			}
			if n := len(fam.httpCookies.list); n != 0 {
				t.Fatalf("rejected import left %d jar entries", n)
			}
		})
	}

	t.Run("clamp over 30 days", func(t *testing.T) {
		fam := &FileAccessManager{}
		c := clrCookie()
		c.Expire = time.Now().Add(400 * 24 * time.Hour).Unix()
		before := time.Now().Unix()
		if err := fam.HTTPImportClearance(c); err != nil {
			t.Fatalf("import: %v", err)
		}
		got, ok := fam.HTTPGetImportedClearance(clrOwner, clrOrigin, c.Name)
		if !ok {
			t.Fatal("record missing")
		}
		lo, hi := before+30*86400, time.Now().Unix()+30*86400
		if got.Expire < lo || got.Expire > hi {
			t.Fatalf("expire=%d, want clamped into [%d,%d]", got.Expire, lo, hi)
		}
	})
	t.Run("short remaining lifetime accepted", func(t *testing.T) {
		fam := &FileAccessManager{}
		c := clrCookie()
		c.Expire = time.Now().Add(5 * time.Second).Unix()
		if err := fam.HTTPImportClearance(c); err != nil {
			t.Fatalf("import: %v", err)
		}
	})
	t.Run("normalisation", func(t *testing.T) {
		fam := &FileAccessManager{}
		c := clrCookie()
		c.Origin = "HTTPS://Example.TEST:443"
		c.Path = ""
		if err := fam.HTTPImportClearance(c); err != nil {
			t.Fatalf("import: %v", err)
		}
		got, ok := fam.HTTPGetImportedClearance(clrOwner, clrOrigin, c.Name)
		if !ok || got.Origin != clrOrigin || got.Path != "/" {
			t.Fatalf("got %+v ok=%v, want origin %s path /", got, ok, clrOrigin)
		}
	})
}

func TestImportedClearanceSendRules(t *testing.T) {
	fam := clrFAM(t)
	type tc struct {
		name, target, owner string
		want                bool
	}
	for _, c := range []tc{
		{"plain request, no flag", "https://example.test/", "", false},
		{"opted in, owner", "https://example.test/", clrOwner, true},
		{"opted in, owner, deep path", "https://example.test/a/b?x=1", clrOwner, true},
		{"opted in, other owner", "https://example.test/", "plugin.b", false},
		{"opted in, over http", "http://example.test/", clrOwner, false},
		{"opted in, subdomain", "https://sub.example.test/", clrOwner, false},
		{"opted in, parent", "https://test/", clrOwner, false},
		{"opted in, lookalike host", "https://evilexample.test/", clrOwner, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			h1, h2 := clrSend(fam, c.target, c.owner)
			for proto, got := range map[string]string{"h1": h1, "h2": h2} {
				if has := hasCookie(got, "cf_clearance=synthetic-value"); has != c.want {
					t.Errorf("%s: sent=%v, want %v (%q)", proto, has, c.want, got)
				}
			}
		})
	}

	t.Run("path scope", func(t *testing.T) {
		fam := &FileAccessManager{}
		ic := clrCookie()
		ic.Path = "/forum"
		if err := fam.HTTPImportClearance(ic); err != nil {
			t.Fatal(err)
		}
		for target, want := range map[string]bool{
			"https://example.test/forum/x": true,
			"https://example.test/forum":   true,
			"https://example.test/forumx":  false,
			"https://example.test/other":   false,
		} {
			h1, h2 := clrSend(fam, target, clrOwner)
			if hasCookie(h1, "cf_clearance=synthetic-value") != want || hasCookie(h2, "cf_clearance=synthetic-value") != want {
				t.Errorf("%s: h1=%q h2=%q, want sent=%v", target, h1, h2, want)
			}
		}
	})
	t.Run("expired", func(t *testing.T) {
		fam := clrFAM(t)
		fam.httpCookies.list[0].expire = time.Now().Unix() - 1
		h1, h2 := clrSend(fam, "https://example.test/", clrOwner)
		if h1 != "" || h2 != "" {
			t.Fatalf("expired imported cookie sent: %q %q", h1, h2)
		}
	})
	t.Run("opt-in wins over a same-named jar cookie", func(t *testing.T) {
		fam := clrFAM(t)
		fam.cookieJarPut(httpCookie{name: "cf_clearance", value: "from-jar", domain: "example.test",
			path: "/x", expire: -1, hostOnly: true})
		h1, h2 := clrSend(fam, "https://example.test/x/", clrOwner)
		for _, got := range []string{h1, h2} {
			if got != "cf_clearance=synthetic-value" {
				t.Fatalf("got %q, want only the imported value", got)
			}
		}
		h1, h2 = clrSend(fam, "https://example.test/x/", "")
		if h1 != "cf_clearance=from-jar" || h2 != "cf_clearance=from-jar" {
			t.Fatalf("plain request got %q %q, want the jar cookie", h1, h2)
		}
	})
}

func TestImportedClearanceCollisionAndDelete(t *testing.T) {
	fam := clrFAM(t)
	// Set-Cookie with the same key must not replace or delete it.
	testCookieSetOn(fam, "https://example.test/", "cf_clearance=server; Secure; Path=/")
	testCookieSetOn(fam, "https://example.test/", "cf_clearance=; Path=/; Max-Age=0")
	u, _ := url.Parse("https://example.test/")
	testH2SetCookie(fam, u, "cf_clearance=server2; Path=/; Secure")
	testH2SetCookie(fam, u, "cf_clearance=; Path=/; Max-Age=0")
	got, ok := fam.HTTPGetImportedClearance(clrOwner, clrOrigin, "cf_clearance")
	if !ok || got.Value != "synthetic-value" {
		t.Fatalf("Set-Cookie touched the imported record: ok=%v", ok)
	}
	if n := len(fam.httpCookies.list); n != 1 {
		t.Fatalf("jar has %d entries, want 1", n)
	}

	// HTTPInjectCookies / HTTPDeleteCookies leave it alone.
	fam.HTTPInjectCookies("example.test", "/", map[string]string{"cf_clearance": "inject"})
	if n := fam.HTTPDeleteCookies("example.test", "/", nil); n != 0 {
		t.Fatalf("HTTPDeleteCookies removed %d imported records", n)
	}
	got, ok = fam.HTTPGetImportedClearance(clrOwner, clrOrigin, "cf_clearance")
	if !ok || got.Value != "synthetic-value" {
		t.Fatal("inject/delete touched the imported record")
	}

	// Owner isolation.
	other := clrCookie()
	other.Owner = "plugin.b"
	other.Value = "stolen"
	if err := fam.HTTPImportClearance(other); err == nil {
		t.Fatal("another owner replaced the record")
	}
	if _, ok := fam.HTTPGetImportedClearance("plugin.b", clrOrigin, "cf_clearance"); ok {
		t.Fatal("another owner can read the record")
	}
	if fam.HTTPDeleteImportedClearance("plugin.b", clrOrigin, "cf_clearance") {
		t.Fatal("another owner deleted the record")
	}

	// Replace by the owner updates value, expiry and path.
	rep := clrCookie()
	rep.Value = "replaced"
	rep.Path = "/p"
	if err := fam.HTTPImportClearance(rep); err != nil {
		t.Fatal(err)
	}
	got, _ = fam.HTTPGetImportedClearance(clrOwner, clrOrigin, "cf_clearance")
	if got.Value != "replaced" || got.Path != "/p" || len(fam.httpCookies.list) != 1 {
		t.Fatalf("replace: %+v, jar=%d", got.Path, len(fam.httpCookies.list))
	}
	if !fam.HTTPDeleteImportedClearance(clrOwner, clrOrigin, "cf_clearance") {
		t.Fatal("owner delete failed")
	}
	if _, ok := fam.HTTPGetImportedClearance(clrOwner, clrOrigin, "cf_clearance"); ok {
		t.Fatal("record survives delete")
	}
}

func TestImportedClearanceUAPin(t *testing.T) {
	fam := &FileAccessManager{}
	c := clrCookie()
	if err := fam.HTTPImportClearance(c); err != nil {
		t.Fatal(err)
	}
	if got := fam.httpDomainUAGet("example.test"); got != clrUA {
		t.Fatalf("pin=%q after import, want %q", got, clrUA)
	}
	c.UA = "Mozilla/5.0 Synthetic/2.0"
	if err := fam.HTTPImportClearance(c); err != nil {
		t.Fatal(err)
	}
	if got := fam.httpDomainUAGet("example.test"); got != c.UA {
		t.Fatalf("pin=%q after replace, want %q", got, c.UA)
	}
	if !fam.HTTPDeleteImportedClearance(clrOwner, clrOrigin, c.Name) {
		t.Fatal("delete failed")
	}
	if got := fam.httpDomainUAGet("example.test"); got != "" {
		t.Fatalf("pin=%q after delete, want cleared", got)
	}

	// A pin somebody else set after the import survives the delete.
	if err := fam.HTTPImportClearance(c); err != nil {
		t.Fatal(err)
	}
	fam.HTTPDomainSetUserAgent("example.test", "UserSet/1")
	fam.HTTPDeleteImportedClearance(clrOwner, clrOrigin, c.Name)
	if got := fam.httpDomainUAGet("example.test"); got != "UserSet/1" {
		t.Fatalf("delete wiped a pin the record did not set: %q", got)
	}

	// A pin that already equalled the record's UA is not the record's.
	fam.HTTPDomainSetUserAgent("example.test", c.UA)
	if err := fam.HTTPImportClearance(c); err != nil {
		t.Fatal(err)
	}
	fam.HTTPDeleteImportedClearance(clrOwner, clrOrigin, c.Name)
	if got := fam.httpDomainUAGet("example.test"); got != c.UA {
		t.Fatalf("delete wiped a pre-existing pin: %q", got)
	}

	// Two records on one host must share one UA; deleting one keeps the
	// pin for the other.
	fam = &FileAccessManager{}
	a, b := clrCookie(), clrCookie()
	b.Name = "second"
	if err := fam.HTTPImportClearance(a); err != nil {
		t.Fatal(err)
	}
	b.UA = "Other/1"
	if err := fam.HTTPImportClearance(b); err == nil {
		t.Fatal("second record with a different UA on the same host accepted")
	}
	b.UA = a.UA
	if err := fam.HTTPImportClearance(b); err != nil {
		t.Fatal(err)
	}
	fam.HTTPDeleteImportedClearance(clrOwner, clrOrigin, a.Name)
	if got := fam.httpDomainUAGet("example.test"); got != a.UA {
		t.Fatalf("pin=%q, want kept for the remaining record", got)
	}
	fam.HTTPDeleteImportedClearance(clrOwner, clrOrigin, b.Name)
	if got := fam.httpDomainUAGet("example.test"); got != "" {
		t.Fatalf("pin=%q, want cleared with the last record", got)
	}
}

func TestImportedClearancePersist(t *testing.T) {
	src := clrFAM(t)
	testCookieSetOn(src, "https://example.test/", "plain=1; Path=/; Max-Age=3600")
	var saved []HTTPCookieRecord
	src.httpCookies.cookieSave = func(_ any, r []HTTPCookieRecord) { saved = r }
	src.cookiePersist(nil, nil)
	if len(saved) != 2 {
		t.Fatalf("saved %d records, want 2", len(saved))
	}

	load := func(recs []HTTPCookieRecord) *FileAccessManager {
		dst := &FileAccessManager{}
		dst.httpCookies.cookieLoad = func(any) []HTTPCookieRecord { return recs }
		dst.loadCookies()
		return dst
	}

	dst := load(saved)
	got, ok := dst.HTTPGetImportedClearance(clrOwner, clrOrigin, "cf_clearance")
	if !ok || got.Value != "synthetic-value" || got.UA != clrUA {
		t.Fatalf("round trip lost the record: ok=%v", ok)
	}
	if ua := dst.httpDomainUAGet("example.test"); ua != clrUA {
		t.Fatalf("pin after load = %q", ua)
	}
	if h1, _ := clrSend(dst, "https://example.test/", ""); hasCookie(h1, "cf_clearance=synthetic-value") {
		t.Fatal("loaded imported record sent without the opt-in")
	}
	if h1, h2 := clrSend(dst, "https://example.test/", clrOwner); !hasCookie(h1, "cf_clearance=synthetic-value") || !hasCookie(h2, "cf_clearance=synthetic-value") {
		t.Fatalf("loaded record not sent to its owner: %q %q", h1, h2)
	}

	imp := func() HTTPCookieRecord {
		for _, r := range saved {
			if r.Imported {
				return r
			}
		}
		t.Fatal("no imported record saved")
		return HTTPCookieRecord{}
	}()
	tamper := map[string]func(*HTTPCookieRecord){
		"not secure":      func(r *HTTPCookieRecord) { r.Secure = false },
		"not host-only":   func(r *HTTPCookieRecord) { r.HostOnly = false },
		"expired":         func(r *HTTPCookieRecord) { r.Expire = time.Now().Unix() - 10 },
		"beyond cap":      func(r *HTTPCookieRecord) { r.Expire = time.Now().Add(90 * 24 * time.Hour).Unix() },
		"bad origin":      func(r *HTTPCookieRecord) { r.ScopeOrigin = "http://example.test" },
		"ip origin":       func(r *HTTPCookieRecord) { r.ScopeOrigin = "https://192.0.2.1"; r.Domain = "192.0.2.1" },
		"domain mismatch": func(r *HTTPCookieRecord) { r.Domain = "evil.test" },
		"no owner":        func(r *HTTPCookieRecord) { r.ScopeOwner = "" },
		"no ua":           func(r *HTTPCookieRecord) { r.UA = "" },
		"control in ua":   func(r *HTTPCookieRecord) { r.UA = "a\nb" },
		"semicolon value": func(r *HTTPCookieRecord) { r.Value = "a; b=c" },
		"relative path":   func(r *HTTPCookieRecord) { r.Path = "x" },
	}
	for name, f := range tamper {
		t.Run("tampered/"+name, func(t *testing.T) {
			r := imp
			f(&r)
			dst := load([]HTTPCookieRecord{r})
			if n := len(dst.httpCookies.list); n != 0 {
				t.Fatalf("tampered record kept (%d)", n)
			}
			if ua := dst.httpDomainUAGet("example.test"); ua != "" {
				t.Fatalf("dropped record left a UA pin: %q", ua)
			}
		})
	}

	t.Run("imported fields on a non-imported record are ignored", func(t *testing.T) {
		r := imp
		r.Imported = false
		dst := load([]HTTPCookieRecord{r})
		if n := len(dst.httpCookies.list); n != 1 || dst.httpCookies.list[0].imported {
			t.Fatal("non-imported record gained imported state")
		}
		if ua := dst.httpDomainUAGet("example.test"); ua != "" {
			t.Fatalf("non-imported record pinned a UA: %q", ua)
		}
	})
	t.Run("duplicate keys keep one", func(t *testing.T) {
		dst := load([]HTTPCookieRecord{imp, imp})
		if n := len(dst.httpCookies.list); n != 1 {
			t.Fatalf("%d entries for one key", n)
		}
	})
	t.Run("old-format jar", func(t *testing.T) {
		old := HTTPCookieRecord{Name: "old", Path: "/", Domain: "example.test",
			Value: "4", Expire: time.Now().Add(time.Hour).Unix()}
		dst := load([]HTTPCookieRecord{old})
		if len(dst.httpCookies.list) != 1 || dst.httpCookies.list[0].imported {
			t.Fatal("old record did not load as a plain cookie")
		}
	})
	t.Run("expired imported record is purged on persist", func(t *testing.T) {
		fam := clrFAM(t)
		fam.httpCookies.cookieSave = func(any, []HTTPCookieRecord) {}
		fam.httpCookies.list[0].expire = time.Now().Unix() - 1
		fam.cookiePersist(nil, nil)
		if len(fam.httpCookies.list) != 0 {
			t.Fatal("expired imported record kept in the jar")
		}
		if ua := fam.httpDomainUAGet("example.test"); ua != "" {
			t.Fatalf("expired record left its UA pin: %q", ua)
		}
	})
}

func testH2SetCookie(fam *FileAccessManager, u *url.URL, header string) {
	(&h2File{fam: fam}).h2SetCookies(&http.Response{Header: http.Header{
		"Set-Cookie": {header}}}, u)
}

// The flag and the owner reach the wire on the HTTP/2 open path; a request
// without either does not carry the imported cookie.
func TestImportedClearanceOverTLSOpen(t *testing.T) {
	p := &redirectProbe{cookies: map[string]string{}}
	srv := httptest.NewUnstartedServer(p.handler("a", ""))
	srv.TLS = &tls.Config{}
	srv.StartTLS()
	defer srv.Close()

	fam := &FileAccessManager{}
	clrPlant(fam, srv.URL)
	for _, c := range []struct {
		name  string
		flags int
		owner string
		want  bool
	}{
		{"no flag", 0, clrOwner, false},
		{"flag, no owner", FaImportedCookies, "", false},
		{"flag, other owner", FaImportedCookies, "plugin.b", false},
		{"flag, owner", FaImportedCookies, clrOwner, true},
		{"flag, owner, no-cookies", FaImportedCookies | FaNoCookies, clrOwner, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, err := h2Open(&FAProtocol{fam: fam}, srv.URL, c.flags,
				&OpenExtra{ImportedOwner: c.owner})
			if err != nil {
				t.Fatalf("h2Open: %v", err)
			}
			defer h.Close()
			if got := hasCookie(p.seen("a"), "cf_clearance=synthetic-value"); got != c.want {
				t.Fatalf("sent=%v, want %v (%q)", got, c.want, p.seen("a"))
			}
		})
	}
}

// Expired records must not keep blocking an import with a new User-Agent.
func TestImportedClearanceExpiredDoesNotBlockImport(t *testing.T) {
	fam := &FileAccessManager{}
	a, b := clrCookie(), clrCookie()
	b.Name = "second"
	for _, c := range []ImportedCookie{a, b} {
		if err := fam.HTTPImportClearance(c); err != nil {
			t.Fatal(err)
		}
	}
	for _, hc := range fam.httpCookies.list {
		hc.expire = time.Now().Unix() - 1
	}
	a.UA = "Mozilla/5.0 Synthetic/9.0"
	if err := fam.HTTPImportClearance(a); err != nil {
		t.Fatalf("import blocked by expired records: %v", err)
	}
	if got := fam.httpDomainUAGet("example.test"); got != a.UA {
		t.Fatalf("pin=%q, want %q", got, a.UA)
	}
	if n := len(fam.httpCookies.list); n != 1 {
		t.Fatalf("jar has %d entries, want 1", n)
	}
}

// A mixed-case request host still gets the pinned User-Agent, so the
// cookie and the UA it is bound to travel together.
func TestImportedClearanceUAPinHostCase(t *testing.T) {
	fam := clrFAM(t)
	if got := fam.httpDomainUAGet("Example.TEST"); got != clrUA {
		t.Fatalf("pin for a mixed-case host = %q, want %q", got, clrUA)
	}
	h1, _ := clrSend(fam, "https://Example.TEST/", clrOwner)
	if !hasCookie(h1, "cf_clearance=synthetic-value") {
		t.Fatalf("mixed-case host lost the cookie: %q", h1)
	}
	fam.HTTPDomainSetUserAgent("EXAMPLE.test", "")
	if got := fam.httpDomainUAGet("example.test"); got != "" {
		t.Fatalf("clearing through another case left %q", got)
	}
}

// clrPlant puts an imported record for the loopback host of srvURL into the
// jar. The import API rejects IP literals and non-default ports, so the
// record is planted directly, bound to the test server's own origin.
func clrPlant(fam *FileAccessManager, srvURL string) {
	fam.cookieJarPut(httpCookie{name: "cf_clearance", value: "synthetic-value",
		domain: "127.0.0.1", path: "/", expire: time.Now().Add(time.Hour).Unix(),
		secure: true, hostOnly: true, imported: true, ua: clrUA,
		scopeOrigin: "https://127.0.0.1:" + srvPort(srvURL), scopeOwner: clrOwner})
}

func srvPort(srvURL string) string {
	u, _ := url.Parse(srvURL)
	return u.Port()
}

// The HTTP/1 request engine parses the flag and the owner tag.
func TestImportedClearanceHTTPReqvTags(t *testing.T) {
	p := &redirectProbe{cookies: map[string]string{}}
	srv := httptest.NewUnstartedServer(p.handler("a", ""))
	srv.TLS = &tls.Config{NextProtos: []string{"http/1.1"}}
	srv.StartTLS()
	defer srv.Close()

	fam := NewFileAccessManager(nil, nil)
	clrPlant(fam, srv.URL)
	for _, c := range []struct {
		name string
		args []any
		want bool
	}{
		{"no flag", []any{HTTPTagFlags, 0, HTTPTagImportedOwner, clrOwner}, false},
		{"flag, no owner tag", []any{HTTPTagFlags, FaImportedCookies}, false},
		{"flag, other owner", []any{HTTPTagFlags, FaImportedCookies, HTTPTagImportedOwner, "plugin.b"}, false},
		{"flag, owner", []any{HTTPTagFlags, FaImportedCookies, HTTPTagImportedOwner, clrOwner}, true},
		{"flag and no-cookies", []any{HTTPTagFlags, FaImportedCookies | FaNoCookies, HTTPTagImportedOwner, clrOwner}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			p.mu.Lock()
			p.cookies["a"] = ""
			p.mu.Unlock()
			var errbuf [256]byte
			args := append([]any{HTTPTagErrbuf, errbuf[:], HTTPTagMethod, "GET"}, c.args...)
			if rc := fam.HTTPReqv(srv.URL+"/", args, nil, nil); rc != 0 {
				t.Fatalf("HTTPReqv rc=%d: %s", rc, misc.CStr(errbuf[:]))
			}
			if got := hasCookie(p.seen("a"), "cf_clearance=synthetic-value"); got != c.want {
				t.Fatalf("sent=%v, want %v (%q)", got, c.want, p.seen("a"))
			}
		})
	}
}

// A pin does not outlive its expired cookie, even with no later jar change.
func TestImportedClearanceExpiryReleasesPinOnRequest(t *testing.T) {
	fam := clrFAM(t)
	if got := fam.requestUA("example.test"); got != clrUA {
		t.Fatalf("live record: pin=%q", got)
	}
	fam.httpCookies.list[0].expire = time.Now().Unix() - 1
	if got := fam.requestUA("example.test"); got != "" {
		t.Fatalf("expired record left pin %q", got)
	}
	if n := len(fam.httpCookies.list); n != 0 {
		t.Fatalf("expired record kept (%d)", n)
	}
}

// An imported record is bound to the exact origin it was solved for: the
// same host on another port is a different origin and gets nothing.
func TestImportedClearanceNotSentToOtherPort(t *testing.T) {
	fam := clrFAM(t)
	for _, c := range []struct {
		target string
		want   bool
	}{
		{"https://example.test/", true},
		{"https://example.test:443/", true},
		{"https://example.test:8443/", false},
	} {
		h1, h2 := clrSend(fam, c.target, clrOwner)
		for proto, got := range map[string]string{"h1": h1, "h2": h2} {
			if sent := hasCookie(got, "cf_clearance=synthetic-value"); sent != c.want {
				t.Errorf("%s send to %s: sent=%v, want %v", proto, c.target, sent, c.want)
			}
		}
	}
}

// An import pins the host's User-Agent. When the last pin leaves, the UA
// the host had before the import comes back, unless something else changed
// it meanwhile.
func TestImportedClearanceRestoresPriorUserAgent(t *testing.T) {
	const host = "example.test"
	imp := func(t *testing.T, fam *FileAccessManager, name string) {
		t.Helper()
		c := clrCookie()
		c.Name = name
		if err := fam.HTTPImportClearance(c); err != nil {
			t.Fatal(err)
		}
	}
	want := func(t *testing.T, fam *FileAccessManager, ua string) {
		t.Helper()
		if got := fam.httpDomainUAGet(host); got != ua {
			t.Fatalf("host UA=%q, want %q", got, ua)
		}
	}

	t.Run("delete restores", func(t *testing.T) {
		fam := &FileAccessManager{}
		fam.HTTPDomainSetUserAgent(host, "Old/1.0")
		imp(t, fam, "cf_clearance")
		want(t, fam, clrUA)
		if !fam.HTTPDeleteImportedClearance(clrOwner, clrOrigin, "cf_clearance") {
			t.Fatal("record not deleted")
		}
		want(t, fam, "Old/1.0")
	})
	t.Run("expiry restores", func(t *testing.T) {
		fam := &FileAccessManager{}
		fam.HTTPDomainSetUserAgent(host, "Old/1.0")
		imp(t, fam, "cf_clearance")
		fam.httpCookies.list[0].expire = time.Now().Unix() - 1
		if got := fam.requestUA(host); got != "Old/1.0" {
			t.Fatalf("request UA=%q, want the prior one", got)
		}
	})
	t.Run("the last of two pins restores", func(t *testing.T) {
		fam := &FileAccessManager{}
		fam.HTTPDomainSetUserAgent(host, "Old/1.0")
		imp(t, fam, "first")
		imp(t, fam, "second")
		fam.HTTPDeleteImportedClearance(clrOwner, clrOrigin, "first")
		want(t, fam, clrUA)
		fam.HTTPDeleteImportedClearance(clrOwner, clrOrigin, "second")
		want(t, fam, "Old/1.0")
	})
	t.Run("deleting the pin setter first still restores at the end", func(t *testing.T) {
		fam := &FileAccessManager{}
		fam.HTTPDomainSetUserAgent(host, "Old/1.0")
		imp(t, fam, "first")
		imp(t, fam, "second")
		fam.HTTPDeleteImportedClearance(clrOwner, clrOrigin, "second")
		fam.HTTPDeleteImportedClearance(clrOwner, clrOrigin, "first")
		want(t, fam, "Old/1.0")
	})
	t.Run("re-import keeps the prior UA", func(t *testing.T) {
		fam := &FileAccessManager{}
		fam.HTTPDomainSetUserAgent(host, "Old/1.0")
		imp(t, fam, "cf_clearance")
		imp(t, fam, "cf_clearance")
		fam.HTTPDeleteImportedClearance(clrOwner, clrOrigin, "cf_clearance")
		want(t, fam, "Old/1.0")
	})
	t.Run("a later external change is kept", func(t *testing.T) {
		fam := &FileAccessManager{}
		fam.HTTPDomainSetUserAgent(host, "Old/1.0")
		imp(t, fam, "cf_clearance")
		fam.HTTPDomainSetUserAgent(host, "Newer/2.0")
		fam.HTTPDeleteImportedClearance(clrOwner, clrOrigin, "cf_clearance")
		want(t, fam, "Newer/2.0")
	})
	t.Run("no prior UA clears the pin", func(t *testing.T) {
		fam := &FileAccessManager{}
		imp(t, fam, "cf_clearance")
		fam.HTTPDeleteImportedClearance(clrOwner, clrOrigin, "cf_clearance")
		want(t, fam, "")
	})
}
