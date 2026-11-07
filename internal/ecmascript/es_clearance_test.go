package ecmascript

import (
	"strings"
	"sync"
	"testing"
	"time"

	facore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/gaftape"
	"github.com/czz/movian-go/internal/gconf"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/trace"
	"github.com/czz/movian-go/internal/ui"
)

const clrURL = "https://example.test/forum/index.php"

// clrCtx returns a context owned by the plugin `owner` with args pushed.
func clrCtx(owner string, args ...string) *gaftape.Context {
	ctx := gaftape.NewMainContext(gaftape.CreateHeap(nil, nil, nil, nil, nil))
	ctx.Ec = &ESContext{ecID: owner}
	for _, a := range args {
		ctx.PushString(a)
	}
	return ctx
}

func clrSetup(t *testing.T, cookies []ui.WebpopupCookie, ua string) {
	t.Helper()
	oldFam, oldList, oldUA := esEnv.fam, webpopupCookieList, webpopupUserAgent
	esEnv.fam = &facore.FileAccessManager{}
	webpopupCookieList = func(_, _, _ string) []ui.WebpopupCookie { return cookies }
	webpopupUserAgent = func() string { return ua }
	t.Cleanup(func() { esEnv.fam, webpopupCookieList, webpopupUserAgent = oldFam, oldList, oldUA })
}

func goodCookie() ui.WebpopupCookie {
	return ui.WebpopupCookie{Name: "cf_clearance", Value: "synthetic-value", Domain: "example.test",
		Path: "/", Secure: true, Expires: time.Now().Add(48 * time.Hour).Unix()}
}

// result reads the object (or null) a native function left on top.
func result(t *testing.T, ctx *gaftape.Context) map[string]any {
	t.Helper()
	if ctx.IsNull(-1) {
		return nil
	}
	m := map[string]any{}
	for _, k := range []string{"value", "ua", "path", "origin"} {
		ctx.GetPropString(-1, k)
		m[k] = ctx.ToString(-1)
		ctx.Pop()
	}
	ctx.GetPropString(-1, "expires")
	m["expires"] = ctx.ToNumber(-1)
	ctx.Pop()
	return m
}

func TestPersistWebCookieRoundTrip(t *testing.T) {
	c := goodCookie()
	clrSetup(t, []ui.WebpopupCookie{c}, "Synthetic/1.0")

	got := result(t, runNative(esPersistWebCookie, "plugin.a", clrURL, "cf_clearance"))
	if got == nil {
		t.Fatal("persist returned null")
	}
	want := map[string]any{"value": "synthetic-value", "ua": "Synthetic/1.0", "path": "/",
		"origin": "https://example.test"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s=%v, want %v", k, got[k], v)
		}
	}
	if got["expires"] != float64(c.Expires) {
		t.Errorf("expires=%v, want %d", got["expires"], c.Expires)
	}

	back := result(t, runNative(esGetPersistedWebCookie, "plugin.a", "https://example.test/other", "cf_clearance"))
	if back == nil || back["value"] != "synthetic-value" || back["origin"] != "https://example.test" {
		t.Fatalf("get after persist: %v", back)
	}
	if other := result(t, runNative(esGetPersistedWebCookie, "plugin.b", clrURL, "cf_clearance")); other != nil {
		t.Fatal("another plugin read the record")
	}
	if ctx := runNative(esDeletePersistedWebCookie, "plugin.b", clrURL, "cf_clearance"); ctx.ToBoolean(-1) {
		t.Fatal("another plugin deleted the record")
	}
	if ctx := runNative(esDeletePersistedWebCookie, "plugin.a", clrURL, "cf_clearance"); !ctx.ToBoolean(-1) {
		t.Fatal("owner delete failed")
	}
	if gone := result(t, runNative(esGetPersistedWebCookie, "plugin.a", clrURL, "cf_clearance")); gone != nil {
		t.Fatal("record survives delete")
	}
}

func runNative(fn gaftape.Function, owner string, args ...string) *gaftape.Context {
	ctx := clrCtx(owner, args...)
	if n := fn(ctx); n != 1 {
		panic("native function must return one value")
	}
	return ctx
}

func TestPersistWebCookieRefusals(t *testing.T) {
	mut := func(f func(*ui.WebpopupCookie)) []ui.WebpopupCookie {
		c := goodCookie()
		f(&c)
		return []ui.WebpopupCookie{c}
	}
	two := []ui.WebpopupCookie{goodCookie(), goodCookie()}
	two[1].Path = "/forum"
	otherName := goodCookie()
	otherName.Name = "other"
	for name, tc := range map[string]struct {
		cookies []ui.WebpopupCookie
		url     string
		ua      string
	}{
		"not secure":        {mut(func(c *ui.WebpopupCookie) { c.Secure = false }), clrURL, "UA"},
		"session cookie":    {mut(func(c *ui.WebpopupCookie) { c.Expires = 0 }), clrURL, "UA"},
		"expired":           {mut(func(c *ui.WebpopupCookie) { c.Expires = time.Now().Unix() - 5 }), clrURL, "UA"},
		"duplicate in list": {two, clrURL, "UA"},
		"no such cookie":    {[]ui.WebpopupCookie{otherName}, clrURL, "UA"},
		"no cookies":        {nil, clrURL, "UA"},
		"empty ua":          {[]ui.WebpopupCookie{goodCookie()}, clrURL, ""},
		"http url":          {[]ui.WebpopupCookie{goodCookie()}, "http://example.test/", "UA"},
		"ip literal":        {[]ui.WebpopupCookie{goodCookie()}, "https://192.0.2.5/", "UA"},
		"not a url":         {[]ui.WebpopupCookie{goodCookie()}, "nonsense", "UA"},
		"value with semi":   {mut(func(c *ui.WebpopupCookie) { c.Value = "a;b" }), clrURL, "UA"},
	} {
		t.Run(name, func(t *testing.T) {
			clrSetup(t, tc.cookies, tc.ua)
			if got := result(t, runNative(esPersistWebCookie, "plugin.a", tc.url, "cf_clearance")); got != nil {
				t.Fatalf("expected null, got a record")
			}
			if _, ok := esEnv.fam.HTTPGetImportedClearance("plugin.a", "https://example.test", "cf_clearance"); ok {
				t.Fatal("a refused persist left a record")
			}
		})
	}
	t.Run("no owner", func(t *testing.T) {
		clrSetup(t, []ui.WebpopupCookie{goodCookie()}, "UA")
		ctx := gaftape.NewMainContext(gaftape.CreateHeap(nil, nil, nil, nil, nil))
		ctx.PushString(clrURL)
		ctx.PushString("cf_clearance")
		esPersistWebCookie(ctx)
		if !ctx.IsNull(-1) {
			t.Fatal("persist without an owning context succeeded")
		}
	})
}

// Persisting again replaces the record; another plugin cannot take it over.
func TestPersistWebCookieReplace(t *testing.T) {
	c := goodCookie()
	clrSetup(t, []ui.WebpopupCookie{c}, "UA/1")
	runNative(esPersistWebCookie, "plugin.a", clrURL, "cf_clearance")
	c.Value = "second-value"
	webpopupCookieList = func(_, _, _ string) []ui.WebpopupCookie { return []ui.WebpopupCookie{c} }
	webpopupUserAgent = func() string { return "UA/2" }
	got := result(t, runNative(esPersistWebCookie, "plugin.a", clrURL, "cf_clearance"))
	if got == nil || got["value"] != "second-value" || got["ua"] != "UA/2" {
		t.Fatalf("replace: %v", got)
	}
	if other := result(t, runNative(esPersistWebCookie, "plugin.b", clrURL, "cf_clearance")); other != nil {
		t.Fatal("another plugin replaced the record")
	}
}

// http_req with persistedCookies:true hands the request engine the flag and
// the calling plugin's ID as owner; without the option it hands neither. (The
// engine's own use of both is tested in fileaccess over TLS.)
func TestHTTPReqPersistedCookies(t *testing.T) {
	oldFam, oldReqv := esEnv.fam, esHTTPReqv
	esEnv.fam = &facore.FileAccessManager{}
	var gotFlags int
	var gotOwner string
	esHTTPReqv = func(_ *facore.FileAccessManager, _ string, args []any) int {
		gotFlags, gotOwner = 0, ""
		for i := 0; i+1 < len(args); i++ {
			switch args[i] {
			case facore.HTTPTagFlags:
				gotFlags, _ = args[i+1].(int)
			case facore.HTTPTagImportedOwner:
				gotOwner, _ = args[i+1].(string)
			}
		}
		return 0
	}
	t.Cleanup(func() { esEnv.fam, esHTTPReqv = oldFam, oldReqv })

	for _, c := range []struct {
		name, owner string
		option      bool
		wantFlag    bool
		wantOwner   string
	}{
		{"option, plugin a", "plugin.a", true, true, "plugin.a"},
		{"option, plugin b", "plugin.b", true, true, "plugin.b"},
		{"no option", "plugin.a", false, false, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := clrCtx(c.owner, "https://example.test/")
			ec := EsGet(ctx)
			ec.ecMutex.Lock() // esHTTPReq suspends the context around the call
			ctx.PushObject()
			if c.option {
				ctx.PushBoolean(true)
				ctx.PutPropString(-2, "persistedCookies")
			}
			n := esHTTPReq(ctx)
			ec.ecMutex.Unlock()
			if n != 1 {
				t.Fatalf("esHTTPReq returned %d", n)
			}
			if has := gotFlags&facore.FaImportedCookies != 0; has != c.wantFlag {
				t.Fatalf("flag=%v, want %v", has, c.wantFlag)
			}
			if gotOwner != c.wantOwner {
				t.Fatalf("owner=%q, want %q", gotOwner, c.wantOwner)
			}
		})
	}
}

// The persist path tells the webview layer who asks and for which cookie, so
// the Android backend can check the owner and the origin when it saves.
func TestPersistWebCookieAsksWebviewForOwnerAndName(t *testing.T) {
	clrSetup(t, nil, "Synthetic/1.0")
	var gotOwner, gotURL, gotName string
	webpopupCookieList = func(owner, uri, name string) []ui.WebpopupCookie {
		gotOwner, gotURL, gotName = owner, uri, name
		return []ui.WebpopupCookie{goodCookie()}
	}
	if result(t, runNative(esPersistWebCookie, "plugin.a", clrURL, "cf_clearance")) == nil {
		t.Fatal("persist returned null")
	}
	if gotOwner != "plugin.a" || gotURL != clrURL || gotName != "cf_clearance" {
		t.Fatalf("webview asked as (%q, %q, %q)", gotOwner, gotURL, gotName)
	}
}

func TestSupportsBrowserLogin(t *testing.T) {
	ctx := runNative(esSupportsBrowserLogin, "plugin.a")
	if !ctx.IsBoolean(-1) || ctx.ToBoolean(-1) != ui.WebpopupSupportsBrowserLogin {
		t.Fatal("supportsBrowserLogin must return the backend constant as a boolean")
	}
	found := false
	for _, e := range fnlistPopup {
		found = found || e.Key == "supportsBrowserLogin" && e.Nargs == 0
	}
	if !found {
		t.Fatal("supportsBrowserLogin is not registered in the popup module")
	}
}

// debugLog wires esEnv.fam to a captured trace; debug is the "Debug all
// HTTP requests" setting. Call after clrSetup.
func debugLog(t *testing.T, debug bool) func() string {
	t.Helper()
	g := gconf.New()
	g.EnableHTTPDebug.Store(debug)
	esEnv.fam.SetGconf(g)
	ts := trace.NewTraceSystem(propcore.NewPropManager())
	ts.Start(t.TempDir(), "clearance-es-test")
	t.Cleanup(ts.Fini)
	var mu sync.Mutex
	var lines []string
	ts.SetPlatformLogger(func(_ int, prefix, line string) {
		mu.Lock()
		lines = append(lines, prefix+" "+line)
		mu.Unlock()
	})
	esEnv.fam.SetTraceSystem(ts)
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(lines, "\n")
	}
}

// persistWebCookie: debug off writes nothing; debug on logs the webview
// cookie and the import with the full value, on accept and on refusal.
func TestPersistWebCookieDebugLog(t *testing.T) {
	t.Run("off", func(t *testing.T) {
		clrSetup(t, []ui.WebpopupCookie{goodCookie()}, "UA")
		logs := debugLog(t, false)
		if result(t, runNative(esPersistWebCookie, "plugin.a", clrURL, "cf_clearance")) == nil {
			t.Fatal("accept path broken")
		}
		bad := goodCookie()
		bad.Secure = false
		webpopupCookieList = func(_, _, _ string) []ui.WebpopupCookie { return []ui.WebpopupCookie{bad} }
		runNative(esPersistWebCookie, "plugin.a", clrURL, "cf_clearance")
		if got := logs(); got != "" {
			t.Fatalf("debug off logged:\n%s", got)
		}
	})
	t.Run("on", func(t *testing.T) {
		clrSetup(t, []ui.WebpopupCookie{goodCookie()}, "UA")
		logs := debugLog(t, true)
		runNative(esPersistWebCookie, "plugin.a", clrURL, "cf_clearance")
		out := logs()
		for _, want := range []string{"persistWebCookie webview-cookie-taken", "synthetic-value", "import accepted", "ua=\"UA\""} {
			if !strings.Contains(out, want) {
				t.Errorf("log lacks %q:\n%s", want, out)
			}
		}
		bad := goodCookie()
		bad.Secure = false
		webpopupCookieList = func(_, _, _ string) []ui.WebpopupCookie { return []ui.WebpopupCookie{bad} }
		runNative(esPersistWebCookie, "plugin.a", clrURL, "cf_clearance")
		out = logs()
		if !strings.Contains(out, "persistWebCookie not-secure") {
			t.Errorf("refusal not logged:\n%s", out)
		}
	})
}

// deletecookies hands the calling plugin's owner to the webview backend, so
// an owner-scoped backend (Android) never deletes another plugin's cookies.
func TestDeleteCookiesPassesCallerOwner(t *testing.T) {
	clrSetup(t, nil, "")
	old := webpopupDeleteCookies
	t.Cleanup(func() { webpopupDeleteCookies = old })
	var got []string
	webpopupDeleteCookies = func(owner, host, path string, _ map[string]bool) int {
		got = append(got, owner+"|"+host+"|"+path)
		return 1
	}
	runNative(esDeleteCookies, "plugin-a", "https://example.test/forum")
	runNative(esDeleteCookies, "plugin-b", "https://example.test/")
	want := []string{"plugin-a|example.test|/forum", "plugin-b|example.test|/"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("backend saw %v, want %v", got, want)
	}
}
