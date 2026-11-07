//go:build webpopup && !cef

package ui

import "testing"

// Adapted from 931add6: without CEF the popup is unavailable, and both the
// three-argument call and a cookie wait degrade to a load error.
func TestWebpopupWithoutCEFReportsLoadError(t *testing.T) {
	if got := WebpopupCreate("https://site.test/login", "t", ""); got.ResultCode != WebpopupLoadError {
		t.Fatalf("legacy call result=%d, want load error", got.ResultCode)
	}
	w := WebpopupCookieWait{URL: "https://site.test/", Name: "cf"}
	if got := WebpopupCreate("https://site.test/login", "t", "", w); got.ResultCode != WebpopupLoadError {
		t.Fatalf("wait call result=%d, want load error", got.ResultCode)
	}
	if got := WebpopupCookieList("https://site.test/"); got != nil {
		t.Fatalf("cookie list without CEF = %v, want nil", got)
	}
}
