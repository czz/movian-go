//go:build webpopup

package ui

// WebpopupPlatform is the seam between the webpopup feature and its
// browser-engine backend. Today the same operations exist as
// wpPlatform* package functions selected by build tags
// (webpopup_cef.go = CEF, webpopup_nocef/webpopup_stub.go = stubs,
// webpopup_wv.go = other webview); this interface names the contract
// so a backend conforms to one shape and can be swapped at the seam.
//
// contract: browser work must run on the platform UI thread —
// Dispatch posts fn there; RunPopup/Browser return only after the
// engine accepted the request; Storage paths are directories the
// engine may write; Cookies/DeleteCookies deliver results on the
// provided channel exactly once.
type WebpopupPlatform interface {
	// Setup — one-time engine init hooks (may be a no-op).
	Setup()

	// OK — reports whether a real browser engine is available.
	OK() bool

	// Dispatch — schedule fn on the engine/UI thread.
	Dispatch(fn func())

	// UserAgent — the UA string the engine actually sends.
	UserAgent() string

	// RunPopup — open a popup at uri; sends wr on done (once).
	RunPopup(uri, title, trap string, wr *WebpopupResult,
		done chan<- *WebpopupResult)

	// Browser — open a full browser window at uri.
	Browser(uri, title string)

	// Storage — set the engine's persistent storage dir.
	Storage(dir string)

	// Cookies — deliver uri's cookies on done (once).
	Cookies(uri string, done chan map[string]string)

	// DeleteCookies — remove matching cookies; sends the removed
	// count on done (once).
	DeleteCookies(host, path string, names map[string]bool, done chan int)
}
