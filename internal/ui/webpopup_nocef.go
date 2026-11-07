//go:build webpopup && !cef && !android

package ui

// webpopup_nocef.go — webpopup compiled in but no CEF backend: the
// embedded CEF backend is the only one (GTK window, WPE, WebView2
// and WKWebView were removed when CEF became the default).
// wpPlatformOK reports unavailable so every public API degrades to
// the no-webview result — the plugin-facing contract stays intact.
// Build with -tags "webpopup cef" (+ a cef pkg-config) for the real
// backend.

// WebpopupSupportsBrowserLogin — no browser backend is compiled in.
const WebpopupSupportsBrowserLogin = false

func wpPlatformSetup() {}

func wpPlatformOK() bool { return false }

func wpPlatformDispatch(fn func()) {}

func wpPlatformRunPopup(uri, title, trap string, wait *WebpopupCookieWait,
	wr *WebpopupResult,
	done chan<- *WebpopupResult) {
}

func wpPlatformBrowser(uri, title string) {}

func wpPlatformStorage(dir string) {}

func wpPlatformCookies(uri string, r *cookieRead) {
	r.done <- nil
}

func wpPlatformDeleteCookies(host, path string, names map[string]bool,
	done chan int) {
	done <- 0
}

func wpPlatformUserAgent() string { return "" }

func WpMainCheck() {}

func WpActive() bool { return false }
