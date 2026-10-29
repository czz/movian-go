//go:build webpopup

package ui

// wpPlatformAdapter adapts the build-tag-selected wpPlatform* funcs to
// the WebpopupPlatform interface. The funcs stay the seam's
// implementation; this is just the interface-shaped front so callers
// can hold a WebpopupPlatform value instead of calling the funcs
// directly.
type wpPlatformAdapter struct{}

// Platform returns the current backend as a WebpopupPlatform — the
// value callers should depend on going forward.
func WebpopupPlatformCurrent() WebpopupPlatform { return wpPlatformAdapter{} }

func (wpPlatformAdapter) Setup()             { wpPlatformSetup() }
func (wpPlatformAdapter) OK() bool           { return wpPlatformOK() }
func (wpPlatformAdapter) Dispatch(fn func()) { wpPlatformDispatch(fn) }
func (wpPlatformAdapter) UserAgent() string  { return wpPlatformUserAgent() }
func (wpPlatformAdapter) Storage(dir string) { wpPlatformStorage(dir) }

func (wpPlatformAdapter) RunPopup(uri, title, trap string,
	wr *WebpopupResult, done chan<- *WebpopupResult) {
	wpPlatformRunPopup(uri, title, trap, wr, done)
}

func (wpPlatformAdapter) Browser(uri, title string) { wpPlatformBrowser(uri, title) }

func (wpPlatformAdapter) Cookies(uri string, done chan map[string]string) {
	wpPlatformCookies(uri, done)
}

func (wpPlatformAdapter) DeleteCookies(host, path string,
	names map[string]bool, done chan int) {
	wpPlatformDeleteCookies(host, path, names, done)
}
