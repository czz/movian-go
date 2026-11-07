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
// that failed, nothing is handed out for it.
func (c *popupOwnerContexts) begin(owner string) {
	if owner == "" {
		return
	}
	c.mu.Lock()
	delete(c.byOwner, owner)
	c.mu.Unlock()
}

// record stores the origin owner's popup ended on. An empty owner or an
// origin that is not https records nothing.
func (c *popupOwnerContexts) record(owner, topURL string) {
	origin := httpsOriginOf(topURL)
	if owner == "" || origin == "" {
		return
	}
	c.mu.Lock()
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
