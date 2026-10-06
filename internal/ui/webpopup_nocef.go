//go:build webpopup && !cef

package ui

// webpopup_nocef.go — webpopup compiled in but no CEF backend: the
// embedded CEF backend is the only one (GTK window, WPE, WebView2
// and WKWebView were removed when CEF became the default).
// wpPlatformOK reports unavailable so every public API degrades to
// the no-webview result — the plugin-facing contract stays intact.
// Build with -tags "webpopup cef" (+ a cef pkg-config) for the real
// backend.

func wpPlatformSetup() {}

func wpPlatformOK() bool { return false }

func wpPlatformDispatch(fn func()) {}

func wpPlatformRunPopup(uri, title, trap string, wr *WebpopupResult,
	done chan<- *WebpopupResult) {
}

func wpPlatformBrowser(uri, title string) {}

func wpPlatformStorage(dir string) {}

func wpPlatformCookies(uri string, done chan map[string]string) {}

func WpMainCheck() {}

func WpActive() bool { return false }
