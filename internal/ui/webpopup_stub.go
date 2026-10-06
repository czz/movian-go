//go:build !webpopup

// webpopup_stub.go — builds without the webpopup tag (any platform):
// the same outcome the C configure produces when CONFIG_WEBPOPUP=0.
// Native backends live in webpopup_gtk.go (linux webkit2gtk) and
// webpopup_windows.go (WebView2), sharing webpopup_wv.go.
package ui

// WebpopupAvailable — C: ENABLE_WEBPOPUP.
const WebpopupAvailable = false

// WebpopupSetStorage is a no-op without a webview.
func WebpopupSetStorage(dir string) {}

// WebpopupCreate — C: webpopup_create (disabled build).
func WebpopupCreate(url, title, trap string) *WebpopupResult {
	return &WebpopupResult{ResultCode: WebpopupLoadError}
}

// WebbrowserOpen — C: webbrowser_open (disabled build).
func WebbrowserOpen(url, title string) {}

// WebpopupCookies returns nil without a webview.
func WebpopupCookies(uri string) map[string]string { return nil }

// WebpopupUserAgent returns "" without a webview.
func WebpopupUserAgent() string { return "" }

// WpMainCheck — C: linux_webpopup_check() slot; nothing to drain.
func WpMainCheck() {}

// WpActive — never active without a webview.
func WpActive() bool { return false }
