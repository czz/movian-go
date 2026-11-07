//go:build !webpopup || (webpopup && !cef && !android)

package ui

import "testing"

// Without a browser backend (no webview, or webpopup without CEF) a plugin
// must not be told it can run a browser login. The CEF and Android values
// are fixed by their tag builds (make check-all-tags).
func TestWebpopupSupportsBrowserLoginOffWithoutBrowser(t *testing.T) {
	if WebpopupSupportsBrowserLogin {
		t.Fatal("a build without a browser backend reports browser login")
	}
}
