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

// The origin an owner's popup ended on is the main frame's actual origin, never
// the wait target: a cookie wait records only where the popup really is, and
// a late iframe navigation never replaces it.
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
		{"cookie found before any page records nothing", WebpopupCookieFound, "", wait, ""},
		{"cookie found on the wait origin names the actual page", WebpopupCookieFound, "https://example.test/ch", wait, "https://example.test/ch"},
		{"cookie found after the main frame moved to another origin", WebpopupCookieFound, "https://other.test/", wait, ""},
		{"cookie found on the same host, other port", WebpopupCookieFound, "https://example.test:8443/", wait, ""},
		{"trapped uses the last page", WebpopupTrappedURL, "https://example.test/ch", wait, "https://example.test/ch"},
		{"user close uses the last page", WebpopupClosedByUser, "https://example.test/ch", nil, "https://example.test/ch"},
		{"user close before any page", WebpopupClosedByUser, "", wait, ""},
		{"cookie found on a non-default port before any page", WebpopupCookieFound, "", portWait, ""},
		{"cookie found on a non-default port after its page", WebpopupCookieFound, "https://example.test:8443/ch", portWait, "https://example.test:8443/ch"},
		{"cookie found on the default port, wait on another", WebpopupCookieFound, "https://example.test/", portWait, ""},
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

// A late address change in an iframe must not move the origin the popup ended
// on: the main frame's last https page stays the authorized one.
func TestMainFrameURLIgnoresSubframes(t *testing.T) {
	top := ""
	for _, ev := range []struct {
		url    string
		isMain bool
	}{
		{"https://site.test/login", true},
		{"https://ads.example/frame", false},
		{"https://cdn.example/widget", false},
	} {
		top = mainFrameURL(top, ev.url, ev.isMain)
	}
	if top != "https://site.test/login" {
		t.Fatalf("top = %q, want the main frame's https page", top)
	}

	var c popupOwnerContexts
	gen := c.begin("plugin.a")
	c.record("plugin.a", gen, popupContextURL(WebpopupClosedByUser, top, nil))
	if !c.readAllowed("plugin.a", "https://site.test/") {
		t.Error("the main-frame origin must be authorized")
	}
	for _, other := range []string{"https://ads.example/", "https://cdn.example/"} {
		if c.readAllowed("plugin.a", other) {
			t.Errorf("subframe origin %s must not be authorized", other)
		}
	}
}

// A main frame that moves to a page that is not https shows no https origin
// any more: nothing is remembered for it, whatever page came before.
func TestMainFrameURLClearsOnNonHTTPS(t *testing.T) {
	top := mainFrameURL("", "https://site.test/login", true)
	for _, uri := range []string{"http://site.test/plain", "about:blank", "app://done", "https://%zz", ""} {
		if got := mainFrameURL(top, uri, true); got != "" {
			t.Errorf("main frame at %q left top = %q, want it cleared", uri, got)
		}
	}
	// A subframe at such an address changes nothing.
	if got := mainFrameURL(top, "http://x.test/", false); got != top {
		t.Errorf("subframe cleared top: %q", got)
	}
}

// A cookie wait may complete only while the main frame shows the wait target's
// own https origin. The cookie jar is shared, so an existing cookie proves
// nothing about the page.
func TestCookieWaitMayComplete(t *testing.T) {
	wait := &WebpopupCookieWait{target: cookieURL{scheme: "https", host: "example.test", path: "/login"}}
	portWait := &WebpopupCookieWait{port: "8443", target: cookieURL{scheme: "https", host: "example.test", path: "/"}}
	for _, tc := range []struct {
		name string
		top  string
		wait *WebpopupCookieWait
		want bool
	}{
		{"before the first navigation", "", wait, false},
		{"the wait origin, any path", "https://example.test/other", wait, true},
		{"the host in another case", "https://EXAMPLE.test/", wait, true},
		{"the explicit default port", "https://example.test:443/", wait, true},
		{"redirected to another host", "https://login.other.test/", wait, false},
		{"a subdomain", "https://www.example.test/", wait, false},
		{"another port", "https://example.test:8443/", wait, false},
		{"moved to non-https (top cleared)", "", wait, false},
		{"non-default port reached", "https://example.test:8443/x", portWait, true},
		{"non-default wait, default port shown", "https://example.test/", portWait, false},
		{"no wait", "https://example.test/", nil, false},
	} {
		if got := cookieWaitMayComplete(tc.top, tc.wait); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The scenario of the review: another plugin's cookie is already in the
// shared jar, so the wait's first visitor result is positive before any page
// callback, or after the main frame was redirected away while the query ran.
// Neither may complete the wait or authorize the owner's read.
func TestCookieWaitDoesNotFabricateOwnerContext(t *testing.T) {
	const origin = "https://example.test/"
	wait := &WebpopupCookieWait{target: cookieURL{scheme: "https", host: "example.test", path: "/"}}
	finish := func(c *popupOwnerContexts, gen uint64, top string) bool {
		if !cookieWaitMayComplete(top, wait) {
			return false // the result is ignored; the wait goes on
		}
		c.record("plugin.b", gen, popupContextURL(WebpopupCookieFound, top, wait))
		return true
	}
	t.Run("existing cookie before the first navigation", func(t *testing.T) {
		var c popupOwnerContexts
		gen := c.begin("plugin.b")
		if finish(&c, gen, "") || c.readAllowed("plugin.b", origin) {
			t.Fatal("completed or authorized without a page")
		}
	})
	t.Run("redirect away while the query is outstanding", func(t *testing.T) {
		var c popupOwnerContexts
		gen := c.begin("plugin.b")
		top := mainFrameURL("", origin, true)
		top = mainFrameURL(top, "https://evil.test/", true) // query result arrives now
		if finish(&c, gen, top) || c.readAllowed("plugin.b", origin) || c.readAllowed("plugin.b", "https://evil.test/") {
			t.Fatal("completed or authorized after leaving the wait origin")
		}
		// The same result forced through by a racing finish records nothing.
		c.record("plugin.b", gen, popupContextURL(WebpopupCookieFound, top, wait))
		if c.readAllowed("plugin.b", origin) || c.readAllowed("plugin.b", "https://evil.test/") {
			t.Fatal("a stale cookie-found result recorded an origin")
		}
	})
	t.Run("redirect to a non-https page", func(t *testing.T) {
		var c popupOwnerContexts
		gen := c.begin("plugin.b")
		top := mainFrameURL(mainFrameURL("", origin, true), "http://example.test/", true)
		if finish(&c, gen, top) || c.readAllowed("plugin.b", origin) {
			t.Fatal("completed or authorized on a non-https page")
		}
	})
	t.Run("normal flow", func(t *testing.T) {
		var c popupOwnerContexts
		gen := c.begin("plugin.b")
		top := mainFrameURL("", "https://example.test/challenge", true)
		if !finish(&c, gen, top) {
			t.Fatal("wait did not complete on its own origin")
		}
		if !c.readAllowed("plugin.b", origin) || c.readAllowed("plugin.a", origin) {
			t.Fatal("the owner must read its origin and another owner must not")
		}
	})
}
