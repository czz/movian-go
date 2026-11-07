//go:build webpopup

// webpopup_wv.go — cross-platform webpopup core. The platform
// backend (webpopup_cef.go — CEF is the only one) provides the
// UI-thread machinery and the native cookie/browser hooks
// (wpPlatform*). Trap-URL capture, the last-seen user agent and the
// JS-visible cookie cache (document.cookie — non-HttpOnly only) are
// platform-agnostic and live here.
package ui

import (
	"net/url"
	"strings"
	"sync"
	"time"
)

// WebpopupAvailable — C: ENABLE_WEBPOPUP.
const WebpopupAvailable = true

var (
	wpOnce sync.Once

	// wpJSCookies — per-origin document.cookie cache filled by the
	// injected page script (HttpOnly cookies never reach JS; the
	// platform hook covers those where available).
	wpCookiesMu sync.Mutex
	wpJSCookies = map[string]map[string]string{}
)

// wpEnsure performs the one-time platform init (C: gdk threads setup).
func wpEnsure() {
	wpOnce.Do(func() { wpPlatformSetup() })
}

// wpStoreJSCookies records a document.cookie snapshot for origin.
func wpStoreJSCookies(origin, cookieStr string) {
	m := map[string]string{}
	for _, pair := range strings.Split(cookieStr, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if ok && k != "" {
			m[k] = v
		}
	}
	wpCookiesMu.Lock()
	wpJSCookies[origin] = m
	wpCookiesMu.Unlock()
}

// wpGetJSCookies returns the JS-visible cookie cache for a URL's
// origin ("" if the page never ran — i.e. no popup visited it yet).
func wpGetJSCookies(uri string) map[string]string {
	u, err := url.Parse(uri)
	if err != nil {
		return nil
	}
	wpCookiesMu.Lock()
	defer wpCookiesMu.Unlock()
	if m, ok := wpJSCookies[u.Scheme+"://"+u.Host]; ok {
		out := map[string]string{}
		for k, v := range m {
			out[k] = v
		}
		return out
	}
	return nil
}

// WebpopupCreate — C: webpopup_create. Opens a browser window and blocks
// until navigation reaches a URL starting with trap, the user closes the
// window, or — when a WebpopupCookieWait is given — the awaited cookie
// appears (WebpopupCookieFound) or the wait times out
// (WebpopupCookieTimeout). The optional wait keeps every three-argument
// caller source-compatible; an invalid wait does not start the popup.
func WebpopupCreate(uri, title, trap string, waits ...WebpopupCookieWait) *WebpopupResult {
	return WebpopupCreateFor("", uri, title, trap, waits...)
}

// WebpopupCreateFor is WebpopupCreate on behalf of the plugin owner. The
// Android backend binds its WebView profile and the cookie it later hands
// out to that owner and refuses an empty one; the CEF backend ignores it.
func WebpopupCreateFor(owner, uri, title, trap string, waits ...WebpopupCookieWait) *WebpopupResult {
	wpEnsure()
	wr := &WebpopupResult{owner: owner}
	if !wpPlatformOK() {
		wr.ResultCode = WebpopupLoadError
		WebpopupFinalizeResult(wr)
		return wr
	}
	var wait *WebpopupCookieWait
	if len(waits) > 0 {
		w := waits[0]
		target, ok := cookieWaitURL(uri, w.URL, w.Name)
		if !ok {
			wr.ResultCode = WebpopupLoadError
			WebpopupFinalizeResult(wr)
			return wr
		}
		w.target = target
		wait = &w
	}
	done := make(chan *WebpopupResult, 1)
	wpPlatformDispatch(func() {
		wpPlatformRunPopup(uri, title, trap, wait, wr, done)
	})
	wr = <-done
	WebpopupFinalizeResult(wr)
	return wr
}

// WebbrowserOpen — C: webbrowser_open. Non-blocking browser window
// (native webview where implemented, else the system browser).
func WebbrowserOpen(uri, title string) {
	wpEnsure()
	if !wpPlatformOK() {
		return
	}
	wpPlatformDispatch(func() { wpPlatformBrowser(uri, title) })
}

// WebpopupCookieList returns the unpartitioned cookies the webview sends
// to uri — the URL-scoped native jar, HttpOnly included, one per name
// (see scopeCookies: ambiguous names are dropped). The platform hook runs
// on the UI thread and only STARTS the query; the async completion pushes
// the result (nil if unsupported) on done. Cookies of other CHIPS
// partitions are never returned: CEF does not label them.
func WebpopupCookieList(uri string) []WebpopupCookie {
	return WebpopupCookieListFor("", uri, "")
}

// WebpopupCookieListFor is WebpopupCookieList for one cookie name on behalf
// of a plugin owner. The CEF backend ignores both; the Android backend
// returns only the clearance cookie its policy accepts for that owner.
func WebpopupCookieListFor(owner, uri, name string) []WebpopupCookie {
	wpEnsure()
	if !wpPlatformOK() {
		return nil
	}
	r := newCookieRead()
	r.owner, r.name = owner, name
	wpPlatformDispatch(func() { wpPlatformCookies(uri, r) })
	select {
	case cookies := <-r.done:
		return cookies
	case <-time.After(10 * time.Second):
		r.Abandon() // detach: late CEF callbacks become no-ops
		return nil
	}
}

// WebpopupCookies — WebpopupCookieList flattened to the legacy
// name→value map of webcookies(), merged under the JS-visible snapshot
// (document.cookie: first-party, so never another partition's).
func WebpopupCookies(uri string) map[string]string {
	cookies := WebpopupCookieList(uri)
	js := wpGetJSCookies(uri)
	if cookies == nil && js == nil {
		return nil
	}
	m := CookieMap(cookies)
	for k, v := range js {
		m[k] = v
	}
	return m
}

// WebpopupUserAgent is the UA string the webview sends — cookies like
// cf_clearance are bound to it. The CEF backend reports the exact UA
// Chromium emits (Chrome/<major>.0.0.0), even before the first popup.
func WebpopupUserAgent() string { return wpPlatformUserAgent() }

var wpDelCookiesMu sync.Mutex

// WebpopupDeleteCookies removes the webview-store cookies a request to
// host/path would carry — optionally restricted to the listed names —
// and returns the removed count. Partitioned cookies are covered too:
// the native visitor walks the whole store. An empty jar reports 0 via
// the wait timeout, like the read path.
func WebpopupDeleteCookies(host, path string, names map[string]bool) int {
	wpEnsure()
	if !wpPlatformOK() {
		return 0
	}
	done := make(chan int, 1)
	wpDelCookiesMu.Lock()
	defer wpDelCookiesMu.Unlock()
	wpPlatformDispatch(func() { wpPlatformDeleteCookies(host, path, names, done) })
	select {
	case n := <-done:
		return n
	case <-time.After(10 * time.Second):
		return 0
	}
}

// WebpopupSetStorage enables persistent cookies for the webview's
// cookie jar (dir under the app persistent path).
func WebpopupSetStorage(dir string) {
	wpEnsure()
	if !wpPlatformOK() {
		return
	}
	wpPlatformDispatch(func() { wpPlatformStorage(dir) })
}
