package ui

import "testing"

func TestPopupOwnerContexts(t *testing.T) {
	const origin = "https://example.test/"
	var c popupOwnerContexts
	c.record("plugin.a", "https://example.test/challenge?x=1")

	for _, tc := range []struct {
		name, owner, uri string
		want             bool
	}{
		{"the owner on its origin", "plugin.a", origin, true},
		{"origin match ignores path and case", "plugin.a", "https://EXAMPLE.test/other", true},
		{"another plugin on the same origin", "plugin.b", origin, false},
		{"a plugin that never opened a popup", "plugin.c", origin, false},
		{"another host", "plugin.a", "https://other.test/", false},
		{"a subdomain", "plugin.a", "https://www.example.test/", false},
		{"the explicit default port is the same origin", "plugin.a", "https://example.test:443/", true},
		{"another port", "plugin.a", "https://example.test:8443/", false},
		{"plain http", "plugin.a", "http://example.test/", false},
		{"an unparsable URL", "plugin.a", "https://%zz", false},
		{"the legacy ownerless read", "", origin, true},
	} {
		if got := c.readAllowed(tc.owner, tc.uri); got != tc.want {
			t.Errorf("%s: readAllowed=%v, want %v", tc.name, got, tc.want)
		}
	}

	c.begin("plugin.a")
	if c.readAllowed("plugin.a", origin) {
		t.Error("a new popup must drop the previous context")
	}
	c.record("plugin.a", "app://done")
	c.record("", origin)
	if c.readAllowed("plugin.a", origin) || c.readAllowed("plugin.b", origin) {
		t.Error("a non-https end or an empty owner must record nothing")
	}
}

// The origin an owner's popup ended on: a cookie wait that completes before
// any page callback (the cookie was already in the jar) still names the
// wait target's origin, and a late iframe navigation never replaces it.
func TestPopupContextURL(t *testing.T) {
	wait := &WebpopupCookieWait{target: cookieURL{scheme: "https", host: "example.test", path: "/"}}
	for _, tc := range []struct {
		name string
		code int
		top  string
		wait *WebpopupCookieWait
		want string
	}{
		{"cookie found before any page", WebpopupCookieFound, "", wait, "https://example.test/"},
		{"cookie found after a page", WebpopupCookieFound, "https://example.test/ch", wait, "https://example.test/"},
		{"trapped uses the last page", WebpopupTrappedURL, "https://example.test/ch", wait, "https://example.test/ch"},
		{"user close uses the last page", WebpopupClosedByUser, "https://example.test/ch", nil, "https://example.test/ch"},
		{"user close before any page", WebpopupClosedByUser, "", wait, ""},
		{"cookie found without a wait", WebpopupCookieFound, "https://example.test/ch", nil, "https://example.test/ch"},
		{"timeout records nothing", WebpopupCookieTimeout, "https://example.test/ch", wait, ""},
		{"load error records nothing", WebpopupLoadError, "https://example.test/ch", wait, ""},
	} {
		if got := popupContextURL(tc.code, tc.top, tc.wait); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}
