package ecmascript

import (
	"math"
	"testing"
	"time"

	"github.com/czz/movian-go/internal/ui"
)

// The plugin-visible strings of the popup result; the first three predate
// the cookie wait and must not change.
func TestPopupResultName(t *testing.T) {
	cases := map[int]string{
		ui.WebpopupTrappedURL:    "trapped",
		ui.WebpopupClosedByUser:  "userclose",
		ui.WebpopupLoadError:     "neterror",
		ui.WebpopupCookieFound:   "cookie",
		ui.WebpopupCookieTimeout: "timeout",
		99:                       "error",
	}
	for code, want := range cases {
		if got := popupResultName(code); got != want {
			t.Errorf("code %d: got %q, want %q", code, got, want)
		}
	}
}

func TestCookieWaitSeconds(t *testing.T) {
	cases := []struct {
		in   float64
		want time.Duration
	}{
		{0, 0}, {-3, 0}, {math.NaN(), 0},
		{1.5, 1500 * time.Millisecond},
		{1e9, ui.MaxCookieWaitTimeout},
		{math.Inf(1), ui.MaxCookieWaitTimeout},
	}
	for _, c := range cases {
		if got := cookieWaitSeconds(c.in); got != c.want {
			t.Errorf("%v: got %v, want %v", c.in, got, c.want)
		}
	}
}
