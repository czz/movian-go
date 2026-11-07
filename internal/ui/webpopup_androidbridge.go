// webpopup_androidbridge.go — the Android popup backend without JNI: the
// bookkeeping of the popup context and the rules for what a cookie read may
// return. The JNI calls sit behind androidPopupJNI, so `go test ./internal/ui`
// covers it on the host. webpopup_android.go supplies the real JNI side.
package ui

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf16"
)

// androidPopupJNI is the three Java calls (Core.webPopupPrototype*) the
// bridge needs. Implementations return "" when the call failed.
type androidPopupJNI interface {
	// Popup blocks until the popup finishes; waitTimeoutMs 0 means no cookie
	// wait limit beyond the Activity's session limit.
	Popup(uri, title, trap, waitURL, waitName, owner string, waitTimeoutMs int64) string
	// Cookies returns a JSON array of Set-Cookie strings, or "".
	Cookies(uri, owner string) string
	UserAgent() string
	// DeleteCookies expires the matching cookies in the clearance profiles
	// and returns {"deleted":[names],...} (see androidDeleteOutcome), or "".
	// names empty means every cookie the request would carry.
	DeleteCookies(host, path string, names []string) string
}

// androidPopupOutcome maps the Java result JSON to a webpopup result code.
// Anything unknown or malformed is a load error. url is the trapped URL;
// topOrigin is the top-level origin the WebView ended on.
func androidPopupOutcome(raw string) (code int, url, topOrigin string) {
	var r struct {
		Result    string `json:"result"`
		URL       string `json:"url"`
		TopOrigin string `json:"topOrigin"`
	}
	if json.Unmarshal([]byte(raw), &r) != nil {
		return WebpopupLoadError, "", ""
	}
	switch r.Result {
	case "cookie":
		return WebpopupCookieFound, "", r.TopOrigin
	case "trapped":
		return WebpopupTrappedURL, r.URL, r.TopOrigin
	case "userclose":
		return WebpopupClosedByUser, "", r.TopOrigin
	case "timeout":
		return WebpopupCookieTimeout, "", r.TopOrigin
	}
	return WebpopupLoadError, "", ""
}

// androidBridge remembers which plugin opened the last popup and on which
// top-level origin it ended. A cookie read gets a result only for that
// owner and origin; the policy (SelectAndroidClearance) decides the rest.
type androidBridge struct {
	jni androidPopupJNI

	mu    sync.Mutex
	gen   uint64 // bumped by every popup start; a finished popup records its context only if still current
	ctx   AndroidBridgeContext
	valid bool
	// running is set while a popup is open. Java allows one popup at a
	// time, so a request that arrives meanwhile is refused here before it
	// can touch the running popup's context.
	running bool
}

func newAndroidBridge(jni androidPopupJNI) *androidBridge { return &androidBridge{jni: jni} }

// begin starts a popup and drops the previous context: while a popup runs,
// and after one that failed, no cookie is handed out. It reports false, and
// changes nothing, when a popup is already open.
func (b *androidBridge) begin() (uint64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.running {
		return 0, false
	}
	b.running = true
	b.gen++
	b.valid = false
	b.ctx = AndroidBridgeContext{}
	return b.gen, true
}

// end marks the popup over; the next request may start.
func (b *androidBridge) end() {
	b.mu.Lock()
	b.running = false
	b.mu.Unlock()
}

func (b *androidBridge) finish(gen uint64, ctx AndroidBridgeContext) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.gen == gen {
		b.ctx, b.valid = ctx, true
	}
}

func (b *androidBridge) snapshot() (AndroidBridgeContext, uint64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ctx, b.gen, b.valid
}

func (b *androidBridge) current(gen uint64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.gen == gen
}

// runPopup is wpPlatformRunPopup without the channel: it fills wr. The
// owner comes from wr (set by WebpopupCreateFor); without one nothing is
// opened.
func (b *androidBridge) runPopup(uri, title, trap string, wait *WebpopupCookieWait, wr *WebpopupResult) {
	owner := wr.owner
	if owner == "" {
		wr.ResultCode = WebpopupLoadError
		return
	}
	gen, ok := b.begin()
	if !ok {
		// Another popup is open. Java would refuse this one anyway; refusing
		// it here keeps the open popup's context valid.
		wr.ResultCode = WebpopupLoadError
		if clearanceDebug() {
			clearanceTrace("android popup refused: another popup is open owner=%q", owner)
		}
		return
	}
	defer b.end()
	var waitURL, waitName string
	var waitMs int64
	if wait != nil {
		waitURL, waitName = wait.URL, wait.Name
		waitMs = cookieWaitTimeout(wait.Timeout).Milliseconds()
		if waitMs < 1 {
			// 0 would tell Java "no cookie-wait limit"; keep a tiny
			// positive timeout positive.
			waitMs = 1
		}
	}
	raw := b.jni.Popup(uri, title, trap, waitURL, waitName, owner, waitMs)
	code, trapped, top := androidPopupOutcome(raw)
	if clearanceDebug() {
		clearanceTrace("android popup result code=%d url=%q topOrigin=%q owner=%q raw=%q", code, trapped, top, owner, raw)
	}
	wr.ResultCode = code
	if code == WebpopupTrappedURL {
		wr.Trapped.URL = trapped
	}
	switch code {
	case WebpopupCookieFound, WebpopupTrappedURL, WebpopupClosedByUser:
		if top != "" {
			b.finish(gen, AndroidBridgeContext{Origin: top, Owner: owner})
		}
	}
}

// readCookies is wpPlatformCookies. It delivers at most the one cookie the
// policy accepts for the popup context, the read's owner and name, and
// delivers nothing once the caller has abandoned the read.
func (b *androidBridge) readCookies(uri string, r *cookieRead) {
	var abandoned atomic.Bool
	if !r.Attach(func() { abandoned.Store(true) }) {
		return
	}
	cookies := b.cookies(uri, r.owner, r.name)
	if abandoned.Load() {
		return
	}
	r.done <- cookies
}

func (b *androidBridge) cookies(uri, owner, name string) []WebpopupCookie {
	none := []WebpopupCookie{}
	ctx, gen, ok := b.snapshot()
	if !ok || owner == "" || name == "" || ctx.Owner != owner {
		if clearanceDebug() {
			clearanceTrace("android cookie read refused: no matching popup context (have=%t ctx-owner=%q owner=%q name=%q)", ok, ctx.Owner, owner, name)
		}
		return none
	}
	var lines []string
	rawCookies := b.jni.Cookies(uri, owner)
	if json.Unmarshal([]byte(rawCookies), &lines) != nil {
		if clearanceDebug() {
			clearanceTrace("android cookies unreadable owner=%q uri=%q raw=%q", owner, uri, rawCookies)
		}
		return none
	}
	ua := b.jni.UserAgent()
	clr, reason := SelectAndroidClearance(lines, uri, ctx, owner, name, ua)
	if clearanceDebug() {
		for i, l := range lines {
			clearanceTrace("android cookie-info[%d] owner=%q uri=%q line=%q", i, owner, uri, l)
		}
		traceClearanceOutcome(clr, reason)
	}
	// A popup that started meanwhile replaced the context: the answer
	// belongs to the old one.
	if clr == nil || clr.Cookie == nil || !b.current(gen) {
		return none
	}
	return []WebpopupCookie{*clr.Cookie}
}

// androidDeleteOutcome parses the Java deletecookies answer: the names of the
// cookies it expired. ok is false for an empty or malformed answer.
func androidDeleteOutcome(raw string) (deleted []string, ok bool) {
	var r struct {
		Deleted *[]string `json:"deleted"`
	}
	if json.Unmarshal([]byte(raw), &r) != nil || r.Deleted == nil {
		return nil, false
	}
	return *r.Deleted, true
}

// deleteCookies is wpPlatformDeleteCookies: it asks Java to expire the
// WebView cookies a request to host/path would carry (only the listed names
// when names is not empty) and returns how many it expired.
func (b *androidBridge) deleteCookies(host, path string, names map[string]bool) int {
	host = strings.ToLower(host)
	if host == "" {
		return 0
	}
	if path == "" {
		path = "/"
	}
	var list []string
	for n := range names {
		list = append(list, n)
	}
	sort.Strings(list)
	raw := b.jni.DeleteCookies(host, path, list)
	deleted, ok := androidDeleteOutcome(raw)
	if clearanceDebug() {
		if !ok {
			clearanceTrace("android deletecookies failed host=%q path=%q names=%q raw=%q", host, path, list, raw)
		} else {
			clearanceTrace("android deletecookies host=%q path=%q names=%q deleted=%q count=%d raw=%q", host, path, list, deleted, len(deleted), raw)
		}
	}
	if !ok {
		return 0
	}
	return len(deleted)
}

func (b *androidBridge) userAgent() string { return b.jni.UserAgent() }

// jniUTF16 and fromJNIUTF16 convert between Go strings and the UTF-16 code
// units of a Java String. JNI's NewStringUTF/GetStringUTFChars use modified
// UTF-8 (no 4-byte sequences, NUL as two bytes), which plain Go strings are
// not, so strings cross as UTF-16 instead.
func jniUTF16(s string) []uint16 { return utf16.Encode([]rune(s)) }

func fromJNIUTF16(u []uint16) string { return string(utf16.Decode(u)) }
