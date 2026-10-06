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

	wpLastUA string

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

// WebpopupCreate — C: webpopup_create. Opens a browser window and
// blocks until navigation reaches a URL starting with trap, or the
// user closes the window.
func WebpopupCreate(uri, title, trap string) *WebpopupResult {
	wpEnsure()
	wr := &WebpopupResult{}
	if !wpPlatformOK() {
		wr.ResultCode = WebpopupLoadError
		WebpopupFinalizeResult(wr)
		return wr
	}
	done := make(chan *WebpopupResult, 1)
	wpPlatformDispatch(func() {
		wpPlatformRunPopup(uri, title, trap, wr, done)
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

// WebpopupCookies returns the cookies the webview holds for uri's
// origin — the native jar (HttpOnly included) where the platform
// implements it, merged over the JS-visible snapshot. The platform
// hook runs on the UI thread and only STARTS the query; the async
// completion pushes the result (nil if unsupported) on done.
func WebpopupCookies(uri string) map[string]string {
	wpEnsure()
	var native map[string]string
	if !wpPlatformOK() {
		return wpGetJSCookies(uri)
	}
	done := make(chan map[string]string, 1)
	wpPlatformDispatch(func() { wpPlatformCookies(uri, done) })
	select {
	case native = <-done:
	case <-time.After(10 * time.Second):
	}
	for k, v := range wpGetJSCookies(uri) {
		if native == nil {
			native = map[string]string{}
		}
		native[k] = v
	}
	return native
}

// WebpopupUserAgent is the UA string the webview pages ran with —
// cookies like cf_clearance are bound to it.
func WebpopupUserAgent() string { return wpLastUA }

// WebpopupSetStorage enables persistent cookies for the webview's
// cookie jar (dir under the app persistent path).
func WebpopupSetStorage(dir string) {
	wpEnsure()
	if !wpPlatformOK() {
		return
	}
	wpPlatformDispatch(func() { wpPlatformStorage(dir) })
}
