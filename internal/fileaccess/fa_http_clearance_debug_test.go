package fileaccess

import (
	"crypto/tls"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/czz/movian-go/internal/gconf"
	"github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/trace"
)

const dbgValue = "synthetic-value"

// dbgFAM is a manager whose trace output is captured; debug is the
// "Debug all HTTP requests" setting.
func dbgFAM(t *testing.T, debug bool) (*FileAccessManager, func() string) {
	t.Helper()
	fam := NewFileAccessManager(nil, nil)
	g := gconf.New()
	g.EnableHTTPDebug.Store(debug)
	fam.SetGconf(g)
	ts := trace.NewTraceSystem(propcore.NewPropManager())
	ts.Start(t.TempDir(), "clearance-debug-test")
	t.Cleanup(ts.Fini)
	var mu sync.Mutex
	var lines []string
	ts.SetPlatformLogger(func(_ int, prefix string, line string) {
		mu.Lock()
		lines = append(lines, prefix+" "+line)
		mu.Unlock()
	})
	fam.SetTraceSystem(ts)
	return fam, func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(lines, "\n")
	}
}

func mustContain(t *testing.T, log string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(log, s) {
			t.Errorf("log lacks %q:\n%s", s, log)
		}
	}
}

// Debug off: import, send and the request trace write nothing, and no
// cookie value reaches the log.
func TestClearanceDebugOffLogsNothing(t *testing.T) {
	fam, logs := dbgFAM(t, false)
	if err := fam.HTTPImportClearance(clrCookie()); err != nil {
		t.Fatal(err)
	}
	bad := clrCookie()
	bad.Origin = "http://example.test"
	if fam.HTTPImportClearance(bad) == nil {
		t.Fatal("bad import accepted")
	}
	other := clrCookie()
	other.Owner = "plugin.b"
	if fam.HTTPImportClearance(other) == nil {
		t.Fatal("other owner accepted")
	}
	clrSend(fam, "https://example.test/", clrOwner)
	clrSend(fam, "https://example.test/", "plugin.b")

	if got := logs(); got != "" {
		t.Fatalf("debug off logged:\n%s", got)
	}
}

// Debug off, a real HTTPS request that carries the imported cookie.
func TestClearanceDebugOffRequestLogsNothing(t *testing.T) {
	fam, logs := dbgFAM(t, false)
	dbgRequest(t, fam)
	if got := logs(); strings.Contains(got, dbgValue) || strings.Contains(got, "clearance") {
		t.Fatalf("debug off logged:\n%s", got)
	}
}

func dbgRequest(t *testing.T, fam *FileAccessManager, extra ...any) {
	t.Helper()
	p := &redirectProbe{cookies: map[string]string{}}
	srv := httptest.NewUnstartedServer(p.handler("a", ""))
	srv.TLS = &tls.Config{NextProtos: []string{"http/1.1"}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	clrPlant(fam, srv.URL)
	var errbuf [256]byte
	args := append([]any{HTTPTagErrbuf, errbuf[:], HTTPTagMethod, "GET",
		HTTPTagFlags, FaImportedCookies, HTTPTagImportedOwner, clrOwner}, extra...)
	if rc := fam.HTTPReqv(srv.URL+"/", args, nil, nil); rc != 0 {
		t.Fatalf("HTTPReqv rc=%d: %s", rc, misc.CStr(errbuf[:]))
	}
}

// Debug on: the HTTP/1 request trace carries the imported cookie in full,
// and the send trace says the record was attached.
func TestClearanceDebugOnRequestTrace(t *testing.T) {
	fam, logs := dbgFAM(t, true)
	dbgRequest(t, fam)
	mustContain(t, logs(), "Cookie: cf_clearance="+dbgValue, "send attached", dbgValue)
}

// A single request can turn debug on with FaDebug while the setting is off.
func TestClearanceDebugOnRequestFlag(t *testing.T) {
	fam, logs := dbgFAM(t, false)
	p := &redirectProbe{cookies: map[string]string{}}
	srv := httptest.NewUnstartedServer(p.handler("a", ""))
	srv.TLS = &tls.Config{NextProtos: []string{"http/1.1"}}
	srv.StartTLS()
	defer srv.Close()
	clrPlant(fam, srv.URL)
	var errbuf [256]byte
	args := []any{HTTPTagErrbuf, errbuf[:], HTTPTagMethod, "GET",
		HTTPTagFlags, FaImportedCookies | FaDebug, HTTPTagImportedOwner, clrOwner}
	if rc := fam.HTTPReqv(srv.URL+"/", args, nil, nil); rc != 0 {
		t.Fatalf("HTTPReqv rc=%d: %s", rc, misc.CStr(errbuf[:]))
	}
	mustContain(t, logs(), "Cookie: cf_clearance="+dbgValue, "send attached")
}

func TestClearanceDebugOnImport(t *testing.T) {
	fam, logs := dbgFAM(t, true)
	c := clrCookie()
	c.Expire = time.Now().Add(90 * 24 * time.Hour).Unix()
	before := c.Expire
	if err := fam.HTTPImportClearance(c); err != nil {
		t.Fatal(err)
	}
	out := logs()
	mustContain(t, out, "clearance", "import accepted", dbgValue, clrOwner, clrOrigin, clrUA, "cf_clearance", "clamped=true")
	if strings.Contains(out, "expire="+itoa(before)+"->"+itoa(before)) {
		t.Errorf("expiry not clamped in log:\n%s", out)
	}
	mustContain(t, out, "expire="+itoa(before)+"->")

	bad := clrCookie()
	bad.UA = "Mozilla/5.0 Synthetic/9.0"
	bad.Name = "second"
	if fam.HTTPImportClearance(bad) == nil {
		t.Fatal("UA conflict accepted")
	}
	inv := clrCookie()
	inv.Origin = "http://example.test"
	if fam.HTTPImportClearance(inv) == nil {
		t.Fatal("http origin accepted")
	}
	out = logs()
	mustContain(t, out, "import rejected", "another user agent", "origin must use https", dbgValue)
}

func TestClearanceDebugOnSendSkipped(t *testing.T) {
	fam, logs := dbgFAM(t, true)
	if err := fam.HTTPImportClearance(clrCookie()); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ target, owner, reason string }{
		{"https://example.test/", "plugin.b", "owner-mismatch"},
		{"https://other.test/", clrOwner, "host-mismatch"},
		{"http://example.test/", clrOwner, "insecure-request"},
	} {
		fam.selectCookies(strings.TrimPrefix(strings.TrimPrefix(c.target, "https://"), "http://"),
			0, "/", strings.HasPrefix(c.target, "https:"), c.owner, true, func(string) bool { return false })
		mustContain(t, logs(), "send skipped ("+c.reason+")")
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// An explicit cookie of the same name wins over the imported record; the log
// must say the record was skipped, not attached.
func TestClearanceDebugOnSendOverridden(t *testing.T) {
	fam, logs := dbgFAM(t, true)
	if err := fam.HTTPImportClearance(clrCookie()); err != nil {
		t.Fatal(err)
	}
	got := fam.selectCookies("example.test", 0, "/", true, clrOwner, true,
		func(name string) bool { return name == "cf_clearance" })
	if len(got) != 0 {
		t.Fatalf("override still sent the record: %v", got)
	}
	out := logs()
	mustContain(t, out, "send skipped (explicit-cookie-override)")
	if strings.Contains(out, "send attached") {
		t.Errorf("overridden record logged as attached:\n%s", out)
	}
}
