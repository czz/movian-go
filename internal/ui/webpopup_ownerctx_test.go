package ui

import "testing"

func TestPopupOwnerContexts(t *testing.T) {
	const origin = "https://example.test/"
	var c popupOwnerContexts
	gen := c.begin("plugin.a")
	c.record("plugin.a", gen, "https://example.test/challenge?x=1")

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

	gen = c.begin("plugin.a")
	if c.readAllowed("plugin.a", origin) {
		t.Error("a new popup must drop the previous context")
	}
	c.record("plugin.a", gen, "app://done")
	c.record("", 0, origin)
	if c.readAllowed("plugin.a", origin) || c.readAllowed("plugin.b", origin) {
		t.Error("a non-https end or an empty owner must record nothing")
	}
}

// The origin an owner's popup ended on: a cookie wait that completes before
// any page callback (the cookie was already in the jar) still names the
// wait target's origin, and a late iframe navigation never replaces it.
func TestPopupContextURL(t *testing.T) {
	wait := &WebpopupCookieWait{target: cookieURL{scheme: "https", host: "example.test", path: "/"}}
	portWait := &WebpopupCookieWait{port: "8443", target: cookieURL{scheme: "https", host: "example.test", path: "/"}}
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
		{"cookie found on a non-default port", WebpopupCookieFound, "", portWait, "https://example.test:8443/"},
		{"cookie found on a non-default port after its page", WebpopupCookieFound, "https://example.test:8443/ch", portWait, "https://example.test:8443/"},
		{"cookie found without a wait", WebpopupCookieFound, "https://example.test/ch", nil, "https://example.test/ch"},
		{"timeout records nothing", WebpopupCookieTimeout, "https://example.test/ch", wait, ""},
		{"load error records nothing", WebpopupLoadError, "https://example.test/ch", wait, ""},
	} {
		if got := popupContextURL(tc.code, tc.top, tc.wait); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The wait target keeps its host in canonical form, without the port; the
// non-default port of the validated wait URL is carried on its own, so the
// owner context names the origin the popup really ended on.
func TestCookieWaitPort(t *testing.T) {
	for _, tc := range []struct{ url, want string }{
		{"https://example.test/", ""},
		{"https://example.test:443/x", ""},
		{"https://example.test:8443/x", "8443"},
		{"https://[2001:db8::1]:8443/", "8443"},
		{"https://example.test:0/", ""},
		{"::bad", ""},
	} {
		if got := cookieWaitPort(tc.url); got != tc.want {
			t.Errorf("%q: %q, want %q", tc.url, got, tc.want)
		}
	}
}

// Two popups of one owner may overlap on CEF. Only the newest one decides the
// context, whichever finishes last: an older popup that ends late must not
// replace it, nor restore its own origin after a newer popup failed.
func TestPopupOwnerContextsOverlappingPopups(t *testing.T) {
	const a, b = "https://a.test/", "https://b.test/"
	for _, tc := range []struct {
		name   string
		finish func(c *popupOwnerContexts, genA, genB uint64)
		wantA  bool
		wantB  bool
	}{
		{"newest finishes first, oldest last", func(c *popupOwnerContexts, genA, genB uint64) {
			c.record("plugin.a", genB, b)
			c.record("plugin.a", genA, a)
		}, false, true},
		{"oldest finishes first", func(c *popupOwnerContexts, genA, genB uint64) {
			c.record("plugin.a", genA, a)
			c.record("plugin.a", genB, b)
		}, false, true},
		{"newest ends nowhere, oldest finishes after", func(c *popupOwnerContexts, genA, genB uint64) {
			c.record("plugin.a", genB, "app://done")
			c.record("plugin.a", genA, a)
		}, false, false},
	} {
		var c popupOwnerContexts
		genA := c.begin("plugin.a")
		genB := c.begin("plugin.a")
		if genA == genB {
			t.Fatalf("%s: generations must differ", tc.name)
		}
		tc.finish(&c, genA, genB)
		if got := c.readAllowed("plugin.a", a); got != tc.wantA {
			t.Errorf("%s: read a = %v, want %v", tc.name, got, tc.wantA)
		}
		if got := c.readAllowed("plugin.a", b); got != tc.wantB {
			t.Errorf("%s: read b = %v, want %v", tc.name, got, tc.wantB)
		}
	}
}
