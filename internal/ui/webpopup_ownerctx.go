package ui

// webpopup_ownerctx.go — which plugin may read the shared browser jar on
// the CEF backend. The jar is one for all plugins, so a read on behalf of
// an owner is allowed only for the https origin that owner's own popup
// ended on, as on Android (androidBridge). The bookkeeping has no CEF
// dependency, so `go test ./internal/ui` covers it on the host.

import (
	"net/url"
	"strings"
	"sync"
)

// popupOwnerContexts remembers, per plugin, the https origin its last
// successful popup ended on.
type popupOwnerContexts struct {
	mu      sync.Mutex
	byOwner map[string]string
	// gen is the newest popup generation per owner. Popups of one owner may
	// overlap; only the newest one may record its end.
	gen map[string]uint64
}

// httpsOriginOf is the scheme://host[:port] of an https URL, lowercased and
// without an explicit default port, or "" for anything else.
func httpsOriginOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Opaque != "" ||
		u.User != nil || u.Hostname() == "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if p := u.Port(); p != "" && p != "443" {
		host += ":" + p
	}
	return "https://" + host
}

// begin drops owner's previous context: while a popup runs, and after one
// that failed, nothing is handed out for it. It returns the generation of the
// new popup, which record needs; 0 for an empty owner.
func (c *popupOwnerContexts) begin(owner string) uint64 {
	if owner == "" {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.byOwner, owner)
	if c.gen == nil {
		c.gen = map[string]uint64{}
	}
	c.gen[owner]++
	return c.gen[owner]
}

// record stores the origin the popup of generation gen ended on. An empty
// owner, an origin that is not https, or a popup that a newer one of the same
// owner superseded records nothing.
func (c *popupOwnerContexts) record(owner string, gen uint64, topURL string) {
	origin := httpsOriginOf(topURL)
	if owner == "" || origin == "" {
		return
	}
	c.mu.Lock()
	if c.gen[owner] != gen {
		c.mu.Unlock()
		return
	}
	if c.byOwner == nil {
		c.byOwner = map[string]string{}
	}
	c.byOwner[owner] = origin
	c.mu.Unlock()
}

// readAllowed reports whether a browser-jar read of uri may be answered for
// owner. The legacy ownerless read (webcookies()) is not restricted here.
func (c *popupOwnerContexts) readAllowed(owner, uri string) bool {
	if owner == "" {
		return true
	}
	origin := httpsOriginOf(uri)
	c.mu.Lock()
	defer c.mu.Unlock()
	return origin != "" && c.byOwner[owner] == origin
}

// mainFrameURL is the https page the main frame shows after an address
// change of uri: that page, or "" when the main frame is at an address that
// is not https (nothing is remembered for it, so no earlier page counts as
// where the popup is). A subframe address leaves prev alone.
func mainFrameURL(prev, uri string, isMain bool) string {
	if !isMain {
		return prev
	}
	if httpsOriginOf(uri) != "" {
		return uri
	}
	return ""
}

// waitOrigin is the https origin of a cookie wait's target, in the form
// httpsOriginOf gives ("" without a target).
func waitOrigin(wait *WebpopupCookieWait) string {
	if wait == nil || wait.target.host == "" {
		return ""
	}
	host := wait.target.host
	if wait.port != "" {
		host += ":" + wait.port
	}
	return httpsOriginOf(wait.target.scheme + "://" + host + "/")
}

// cookieWaitMayComplete reports whether a visitor result for the awaited
// cookie may complete the wait: only while the main frame currently shows the
// wait target's own https origin. The cookie jar is shared by all plugins, so
// a cookie that is already there says nothing about the page, and a result
// that arrives after the main frame moved on belongs to another page.
func cookieWaitMayComplete(topURL string, wait *WebpopupCookieWait) bool {
	want := waitOrigin(wait)
	return want != "" && httpsOriginOf(topURL) == want
}

// popupContextURL is the page a finished popup counts as ended on: the main
// frame's actual last https page, never the wait target. A popup that found
// its cookie counts only if the main frame still shows the wait target's
// origin. A timeout or an error ends nowhere.
func popupContextURL(code int, topURL string, wait *WebpopupCookieWait) string {
	switch code {
	case WebpopupCookieFound:
		if wait != nil && !cookieWaitMayComplete(topURL, wait) {
			return ""
		}
		return topURL
	case WebpopupTrappedURL, WebpopupClosedByUser:
		return topURL
	}
	return ""
}
