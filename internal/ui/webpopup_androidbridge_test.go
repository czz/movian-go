package ui

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAndroidJNI stands in for the Java Core methods.
type fakeAndroidJNI struct {
	mu        sync.Mutex
	popupRaw  string
	popupHook func()
	cookies   []string
	cookieRaw string // overrides cookies when set
	ua        string

	popupArgs  []string
	popupMs    int64
	popupCalls int
	cookieArgs []string
	cookieHook func()

	delRaw   string
	delHost  string
	delPath  string
	delNames []string
	delCalls int
}

func (f *fakeAndroidJNI) DeleteCookies(host, path string, names []string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delCalls++
	f.delHost, f.delPath, f.delNames = host, path, names
	return f.delRaw
}

func (f *fakeAndroidJNI) Popup(uri, title, trap, waitURL, waitName, owner string, ms int64) string {
	f.mu.Lock()
	f.popupCalls++
	f.popupArgs = []string{uri, title, trap, waitURL, waitName, owner}
	f.popupMs = ms
	raw, hook := f.popupRaw, f.popupHook
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return raw
}

func (f *fakeAndroidJNI) Cookies(uri, owner string) string {
	f.mu.Lock()
	f.cookieArgs = []string{uri, owner}
	raw, hook := f.cookieRaw, f.cookieHook
	if raw == "" {
		b, _ := json.Marshal(f.cookies)
		raw = string(b)
	}
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return raw
}

func (f *fakeAndroidJNI) UserAgent() string { return f.ua }

// liveLine is a policy-acceptable clearance cookie relative to the real clock
// (SelectAndroidClearance reads time.Now; goodLine is pinned to policyNow).
func liveLine() string {
	return policyName + "=tok; Path=/; Secure; Expires=" + time.Now().Add(24*time.Hour).UTC().Format(http.TimeFormat)
}

func popupJSON(result, url, top string) string {
	b, _ := json.Marshal(map[string]string{"result": result, "url": url, "topOrigin": top})
	return string(b)
}

func TestAndroidPopupOutcome(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		code int
		url  string
		top  string
	}{
		{"cookie", popupJSON("cookie", "", policyOrigin), WebpopupCookieFound, "", policyOrigin},
		{"trapped", popupJSON("trapped", "https://x.test/cb?a=1", policyOrigin), WebpopupTrappedURL, "https://x.test/cb?a=1", policyOrigin},
		{"userclose", popupJSON("userclose", "", policyOrigin), WebpopupClosedByUser, "", policyOrigin},
		{"neterror", popupJSON("neterror", "", ""), WebpopupLoadError, "", ""},
		{"timeout", popupJSON("timeout", "", policyOrigin), WebpopupCookieTimeout, "", policyOrigin},
		{"unknown", popupJSON("whatever", "", ""), WebpopupLoadError, "", ""},
		{"empty result", `{}`, WebpopupLoadError, "", ""},
		{"malformed", `{"result":`, WebpopupLoadError, "", ""},
		{"not json", `cookie`, WebpopupLoadError, "", ""},
		{"empty string", ``, WebpopupLoadError, "", ""},
		{"json array", `[]`, WebpopupLoadError, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, url, top := androidPopupOutcome(tc.raw)
			if code != tc.code || url != tc.url || top != tc.top {
				t.Fatalf("got (%d, %q, %q), want (%d, %q, %q)", code, url, top, tc.code, tc.url, tc.top)
			}
		})
	}
}

func newTestBridge(f *fakeAndroidJNI) *androidBridge { return newAndroidBridge(f) }

func runTestPopup(b *androidBridge, owner string, wait *WebpopupCookieWait) *WebpopupResult {
	wr := &WebpopupResult{owner: owner}
	b.runPopup("https://tracker.example/login", "Login", "app://done", wait, wr)
	return wr
}

func cookieWait() *WebpopupCookieWait {
	return &WebpopupCookieWait{URL: policyOrigin + "/", Name: policyName, Timeout: 90 * time.Second}
}

func readCookies(b *androidBridge, owner, uri, name string) []WebpopupCookie {
	r := newCookieRead()
	r.owner, r.name = owner, name
	b.readCookies(uri, r)
	select {
	case c := <-r.done:
		return c
	default:
		return nil
	}
}

func TestAndroidBridgePopupPassesArgsAndMapsResult(t *testing.T) {
	f := &fakeAndroidJNI{popupRaw: popupJSON("timeout", "", policyOrigin), ua: policyUA}
	b := newTestBridge(f)
	wr := runTestPopup(b, policyOwner, cookieWait())
	if wr.ResultCode != WebpopupCookieTimeout {
		t.Fatalf("code=%d, want timeout %d", wr.ResultCode, WebpopupCookieTimeout)
	}
	want := []string{"https://tracker.example/login", "Login", "app://done", policyOrigin + "/", policyName, policyOwner}
	if strings.Join(f.popupArgs, "|") != strings.Join(want, "|") {
		t.Fatalf("args=%v, want %v", f.popupArgs, want)
	}
	if f.popupMs != 90_000 {
		t.Fatalf("timeout ms=%d, want 90000", f.popupMs)
	}
}

func TestAndroidBridgePopupWithoutWait(t *testing.T) {
	f := &fakeAndroidJNI{popupRaw: popupJSON("trapped", "app://done?x=1", policyOrigin)}
	b := newTestBridge(f)
	wr := runTestPopup(b, policyOwner, nil)
	if wr.ResultCode != WebpopupTrappedURL || wr.Trapped.URL != "app://done?x=1" {
		t.Fatalf("result=%d url=%q", wr.ResultCode, wr.Trapped.URL)
	}
	if f.popupArgs[3] != "" || f.popupArgs[4] != "" || f.popupMs != 0 {
		t.Fatalf("no wait must pass empty wait args and 0 timeout: %v %d", f.popupArgs, f.popupMs)
	}
}

func TestAndroidBridgePopupWithoutOwnerNeverCallsJava(t *testing.T) {
	f := &fakeAndroidJNI{popupRaw: popupJSON("cookie", "", policyOrigin)}
	b := newTestBridge(f)
	wr := runTestPopup(b, "", cookieWait())
	if wr.ResultCode != WebpopupLoadError || f.popupCalls != 0 {
		t.Fatalf("code=%d calls=%d, want load error and no call", wr.ResultCode, f.popupCalls)
	}
}

func TestAndroidBridgeCookieLinesGoThroughPolicy(t *testing.T) {
	f := &fakeAndroidJNI{popupRaw: popupJSON("cookie", "", policyOrigin), ua: policyUA,
		cookies: []string{"a=b; Path=/", liveLine()}}
	b := newTestBridge(f)
	runTestPopup(b, policyOwner, cookieWait())

	got := readCookies(b, policyOwner, policyTarget, policyName)
	if len(got) != 1 || got[0].Name != policyName || got[0].Value != "tok" || !got[0].Secure || got[0].Expires == 0 {
		t.Fatalf("accepted cookie = %+v", got)
	}
	if f.cookieArgs[0] != policyTarget || f.cookieArgs[1] != policyOwner {
		t.Fatalf("cookie query args=%v", f.cookieArgs)
	}

	// Every rule of the policy applies: an insecure cookie yields nothing.
	f.cookies = []string{policyName + "=tok; Path=/; Expires=" + time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)}
	if got := readCookies(b, policyOwner, policyTarget, policyName); len(got) != 0 {
		t.Fatalf("insecure cookie passed: %+v", got)
	}
	// Other name, ambiguous duplicates.
	f.cookies = []string{liveLine()}
	if got := readCookies(b, policyOwner, policyTarget, "other"); len(got) != 0 {
		t.Fatalf("other name passed: %+v", got)
	}
	f.cookies = []string{liveLine(), liveLine()}
	if got := readCookies(b, policyOwner, policyTarget, policyName); len(got) != 0 {
		t.Fatalf("ambiguous cookies passed: %+v", got)
	}
}

func TestAndroidBridgeNoUANoCookie(t *testing.T) {
	f := &fakeAndroidJNI{popupRaw: popupJSON("cookie", "", policyOrigin), cookies: []string{liveLine()}}
	b := newTestBridge(f)
	runTestPopup(b, policyOwner, cookieWait())
	if got := readCookies(b, policyOwner, policyTarget, policyName); len(got) != 0 {
		t.Fatalf("cookie without UA: %+v", got)
	}
}

func TestAndroidBridgeContextBookkeeping(t *testing.T) {
	setup := func() (*androidBridge, *fakeAndroidJNI) {
		f := &fakeAndroidJNI{popupRaw: popupJSON("cookie", "", policyOrigin), ua: policyUA, cookies: []string{liveLine()}}
		b := newTestBridge(f)
		runTestPopup(b, policyOwner, cookieWait())
		return b, f
	}
	t.Run("popup by A, read by B", func(t *testing.T) {
		b, f := setup()
		f.cookieArgs = nil
		if got := readCookies(b, "plugin.other", policyTarget, policyName); len(got) != 0 {
			t.Fatalf("other owner got %+v", got)
		}
		if f.cookieArgs != nil {
			t.Fatalf("Java was queried for a foreign owner: %v", f.cookieArgs)
		}
	})
	t.Run("legacy read without owner", func(t *testing.T) {
		b, _ := setup()
		if got := readCookies(b, "", policyTarget, ""); len(got) != 0 {
			t.Fatalf("ownerless read got %+v", got)
		}
	})
	t.Run("different origin", func(t *testing.T) {
		b, _ := setup()
		if got := readCookies(b, policyOwner, "https://elsewhere.example/", policyName); len(got) != 0 {
			t.Fatalf("other origin got %+v", got)
		}
	})
	t.Run("no popup yet", func(t *testing.T) {
		f := &fakeAndroidJNI{ua: policyUA, cookies: []string{liveLine()}}
		b := newTestBridge(f)
		if got := readCookies(b, policyOwner, policyTarget, policyName); len(got) != 0 {
			t.Fatalf("no popup context, got %+v", got)
		}
		if f.cookieArgs != nil {
			t.Fatal("Java queried without a popup context")
		}
	})
	t.Run("second popup replaces the first", func(t *testing.T) {
		b, f := setup()
		f.popupRaw = popupJSON("userclose", "", "https://second.example")
		runTestPopup(b, "plugin.other", nil)
		if got := readCookies(b, policyOwner, policyTarget, policyName); len(got) != 0 {
			t.Fatalf("first context survived: %+v", got)
		}
		f.cookies = []string{liveLine()}
		if got := readCookies(b, "plugin.other", "https://second.example/", policyName); len(got) != 1 {
			// the cookie line has no host attribute, so it matches any host
			t.Fatalf("second context lost: %+v", got)
		}
	})
	t.Run("failed popup clears the context", func(t *testing.T) {
		b, f := setup()
		f.popupRaw = popupJSON("neterror", "", "")
		runTestPopup(b, policyOwner, cookieWait())
		if got := readCookies(b, policyOwner, policyTarget, policyName); len(got) != 0 {
			t.Fatalf("context kept after a failed popup: %+v", got)
		}
	})
	t.Run("timeout popup records no context", func(t *testing.T) {
		b, f := setup()
		f.popupRaw = popupJSON("timeout", "", policyOrigin)
		runTestPopup(b, policyOwner, cookieWait())
		if got := readCookies(b, policyOwner, policyTarget, policyName); len(got) != 0 {
			t.Fatalf("context recorded for a timeout: %+v", got)
		}
	})
	t.Run("read is dropped when a popup starts meanwhile", func(t *testing.T) {
		b, f := setup()
		f.cookieHook = func() {
			f.popupRaw = popupJSON("neterror", "", "")
			runTestPopup(b, policyOwner, cookieWait())
		}
		if got := readCookies(b, policyOwner, policyTarget, policyName); len(got) != 0 {
			t.Fatalf("stale read delivered: %+v", got)
		}
	})
	t.Run("a rejected second popup leaves the first usable", func(t *testing.T) {
		f := &fakeAndroidJNI{ua: policyUA, cookies: []string{liveLine()}}
		b := newTestBridge(f)
		f.popupRaw = popupJSON("cookie", "", policyOrigin)
		var second *WebpopupResult
		f.popupHook = func() {
			// While popup A is open, popup B is requested. Java rejects it
			// (neterror); that must not touch A's bookkeeping.
			f.popupHook = nil
			f.popupRaw = popupJSON("neterror", "", "")
			second = runTestPopup(b, "plugin.other", nil)
			f.popupRaw = popupJSON("cookie", "", policyOrigin)
		}
		first := runTestPopup(b, policyOwner, cookieWait())
		if first.ResultCode != WebpopupCookieFound {
			t.Fatalf("first popup result=%d, want cookie found", first.ResultCode)
		}
		if second.ResultCode != WebpopupLoadError {
			t.Fatalf("second popup result=%d, want load error", second.ResultCode)
		}
		if got := readCookies(b, policyOwner, policyTarget, policyName); len(got) != 1 {
			t.Fatalf("first popup lost its context: %+v", got)
		}
		if got := readCookies(b, "plugin.other", "https://second.example/", policyName); len(got) != 0 {
			t.Fatalf("rejected popup gained a context: %+v", got)
		}
	})
	t.Run("a popup after the first has ended runs", func(t *testing.T) {
		b, f := setup()
		f.popupRaw = popupJSON("userclose", "", "https://second.example")
		if wr := runTestPopup(b, "plugin.other", nil); wr.ResultCode != WebpopupClosedByUser {
			t.Fatalf("later popup result=%d, want closed by user", wr.ResultCode)
		}
		if f.popupCalls != 2 {
			t.Fatalf("Java popup calls=%d, want 2", f.popupCalls)
		}
	})
}

func TestAndroidBridgeMalformedCookieJSON(t *testing.T) {
	f := &fakeAndroidJNI{popupRaw: popupJSON("cookie", "", policyOrigin), ua: policyUA}
	b := newTestBridge(f)
	runTestPopup(b, policyOwner, cookieWait())
	for _, raw := range []string{"null", "{", `"x"`, `{"a":1}`} {
		f.cookieRaw = raw
		if got := readCookies(b, policyOwner, policyTarget, policyName); len(got) != 0 {
			t.Fatalf("raw %q gave %+v", raw, got)
		}
	}
}

func TestAndroidBridgeReadHonoursAbandon(t *testing.T) {
	f := &fakeAndroidJNI{popupRaw: popupJSON("cookie", "", policyOrigin), ua: policyUA, cookies: []string{liveLine()}}
	b := newTestBridge(f)
	runTestPopup(b, policyOwner, cookieWait())

	// Abandoned before the platform starts: no Java call.
	r := newCookieRead()
	r.owner, r.name = policyOwner, policyName
	r.Abandon()
	f.cookieArgs = nil
	b.readCookies(policyTarget, r)
	if f.cookieArgs != nil || len(r.done) != 0 {
		t.Fatalf("abandoned read ran: args=%v done=%d", f.cookieArgs, len(r.done))
	}

	// Abandoned while Java answers: nothing is delivered.
	r = newCookieRead()
	r.owner, r.name = policyOwner, policyName
	f.cookieHook = func() { r.Abandon() }
	b.readCookies(policyTarget, r)
	if len(r.done) != 0 {
		t.Fatal("cookies delivered to an abandoned read")
	}
}

func TestAndroidBridgeUserAgent(t *testing.T) {
	b := newTestBridge(&fakeAndroidJNI{ua: policyUA})
	if got := b.userAgent(); got != policyUA {
		t.Fatalf("ua=%q", got)
	}
}

func TestJNIUTF16RoundTrip(t *testing.T) {
	for _, s := range []string{"", "plain", "Вход 🔐 login", "a\x00b", "😀😀"} {
		if got := fromJNIUTF16(jniUTF16(s)); got != s {
			t.Fatalf("round trip %q -> %q", s, got)
		}
	}
	if n := len(jniUTF16("😀")); n != 2 {
		t.Fatalf("a non-BMP rune is %d UTF-16 units, want 2", n)
	}
	if n := len(jniUTF16("a\x00b")); n != 3 {
		t.Fatalf("embedded NUL kept as %d units, want 3", n)
	}
}

func TestAndroidBridgeSubMillisecondTimeoutStaysPositive(t *testing.T) {
	f := &fakeAndroidJNI{popupRaw: popupJSON("timeout", "", "")}
	b := newTestBridge(f)
	w := cookieWait()
	w.Timeout = 500 * time.Microsecond
	runTestPopup(b, policyOwner, w)
	if f.popupMs != 1 {
		t.Fatalf("timeout ms=%d, want 1 (0 would disable the limit)", f.popupMs)
	}
}

// A forced refresh opens the popup without a cookie wait and the user ends it
// with Back: Java answers userclose with the https top origin (and nothing
// else), and the same owner can then save the cookie.
func TestAndroidBridgeUserCloseWithOriginAllowsOwnerOnly(t *testing.T) {
	f := &fakeAndroidJNI{popupRaw: `{"result":"userclose","topOrigin":"` + policyOrigin + `"}`,
		ua: policyUA, cookies: []string{liveLine()}}
	b := newTestBridge(f)
	wr := runTestPopup(b, policyOwner, nil)
	if wr.ResultCode != WebpopupClosedByUser {
		t.Fatalf("code=%d", wr.ResultCode)
	}
	if got := readCookies(b, policyOwner, policyTarget, policyName); len(got) != 1 {
		t.Fatalf("owner after Back got %+v", got)
	}
	if got := readCookies(b, "plugin.other", policyTarget, policyName); len(got) != 0 {
		t.Fatalf("another owner got %+v", got)
	}
	// The user ended on a foreign page: the cookie target no longer matches.
	f.popupRaw = `{"result":"userclose","topOrigin":"https://foreign.example"}`
	runTestPopup(b, policyOwner, nil)
	if got := readCookies(b, policyOwner, policyTarget, policyName); len(got) != 0 {
		t.Fatalf("foreign top origin gave %+v", got)
	}
	// No https page at the end (Java reports no origin): nothing.
	f.popupRaw = `{"result":"userclose"}`
	runTestPopup(b, policyOwner, nil)
	if got := readCookies(b, policyOwner, policyTarget, policyName); len(got) != 0 {
		t.Fatalf("userclose without origin gave %+v", got)
	}
}
