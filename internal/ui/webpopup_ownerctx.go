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

// mainFrameURL is the page a popup counts as showing after an address change
// of uri: the main frame's latest https page. A subframe or a non-https
// address leaves prev alone.
func mainFrameURL(prev, uri string, isMain bool) string {
	if isMain && httpsOriginOf(uri) != "" {
		return uri
	}
	return prev
}

// popupContextURL is the page a finished popup counts as ended on. A popup
// that found its cookie ended on the wait target: that is where the cookie
// was seen, and it can happen before any page callback ran (the cookie was
// already in the jar). Otherwise it is the last https page of the main
// frame. A timeout or an error ends nowhere.
func popupContextURL(code int, topURL string, wait *WebpopupCookieWait) string {
	switch code {
	case WebpopupCookieFound:
		if wait != nil && wait.target.host != "" {
			host := wait.target.host
			if wait.port != "" {
				host += ":" + wait.port
			}
			return wait.target.scheme + "://" + host + wait.target.path
		}
		return topURL
	case WebpopupTrappedURL, WebpopupClosedByUser:
		return topURL
	}
	return ""
}
