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
	"time"
)

// popupOwnerContexts remembers, per plugin, the https origin its last
// successful popup ended on.
type popupOwnerContexts struct {
	mu      sync.Mutex
	byOwner map[string]string
	// gen is the newest popup generation per owner. Popups of one owner may
	// overlap; only the newest one may record its end.
	gen map[string]uint64
	// fp holds, per plugin, the first-party Partitioned cookie its last
	// finished wait read through DevTools (see webpopup_devtools.go). The
	// popup's browser is gone by the time persistWebCookie asks, and the
	// visitor cannot see such a cookie.
	fp map[string]firstPartyHold
}

type firstPartyHold struct {
	origin string
	set    firstPartySet
	at     time.Time
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
	delete(c.fp, owner)
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
	c.recordFirstParty(owner, gen, topURL, time.Time{})
}

// firstPartySet is what a popup's end read leaves for its owner: the
// eligible first-party Partitioned cookies of the origin, on any path, and
// the other cookies of the origin (no values) that would be a twin of one.
type firstPartySet struct {
	cookies  []WebpopupCookie
	blockers []fpBlocker
}

// fpBlocker is a twin of a held cookie: a cookie of the origin that is not
// eligible. foreign marks one a deletion request does not remove (a
// partition that is not the site's own); the others go with their name.
type fpBlocker struct {
	WebpopupCookie
	foreign bool
}

// recordFirstParty is recordFirstPartySet with no twins.
func (c *popupOwnerContexts) recordFirstParty(owner string, gen uint64, topURL string, now time.Time, fps ...WebpopupCookie) {
	c.recordFirstPartySet(owner, gen, topURL, now, firstPartySet{cookies: fps})
}

// recordFirstPartySet records where the owner's popup ended and holds the
// set for that origin, only if gen is still the owner's newest popup.
func (c *popupOwnerContexts) recordFirstPartySet(owner string, gen uint64, topURL string, now time.Time, set firstPartySet) {
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
	var kept firstPartySet
	same := func(a, b WebpopupCookie) bool {
		return a.Name == b.Name && a.Domain == b.Domain && a.Path == b.Path
	}
	for _, fp := range set.cookies {
		dup := false
		for _, k := range kept.cookies {
			dup = dup || same(k, fp)
		}
		if !dup && cookieBelongsToOrigin(&fp, origin) {
			kept.cookies = append(kept.cookies, fp)
		}
	}
	for _, b := range set.blockers {
		// Not deduplicated against anything: a twin of an eligible cookie
		// (same name, domain and path, another partition) is a cookie of
		// its own that the browser would send as well.
		if cookieBelongsToOrigin(&b.WebpopupCookie, origin) {
			b.Value = ""
			kept.blockers = append(kept.blockers, b)
		}
	}
	// A blocker-only set is held too: its twins make the name ambiguous.
	if len(kept.cookies) > 0 || len(kept.blockers) > 0 {
		if c.fp == nil {
			c.fp = map[string]firstPartyHold{}
		}
		c.fp[owner] = firstPartyHold{origin: origin, set: kept, at: now}
	}
	c.mu.Unlock()
}

// firstPartyFor returns a copy of owner's held first-party cookie `name` for
// uri, only for the origin the owner's popup ended on, within
// firstPartyHoldTTL of the read and while the cookie is unexpired.
func (c *popupOwnerContexts) firstPartyFor(owner, uri, name string, now time.Time) *WebpopupCookie {
	u, err := url.Parse(uri)
	if err != nil {
		return nil
	}
	return c.firstPartyForURL(owner, uri,
		cookieURL{scheme: "https", host: bracketHost(u.Hostname()), path: pathOrRoot(u.EscapedPath())}, name, now)
}

// firstPartyForURL is firstPartyFor with the request URL as the browser
// canonicalized it (dot segments removed), which the path match must use.
func (c *popupOwnerContexts) firstPartyForURL(owner, uri string, req cookieURL, name string, now time.Time) *WebpopupCookie {
	hit, _ := c.firstPartyLookup(owner, uri, req, name, now)
	return hit
}

// firstPartyLookup is firstPartyForURL that also reports an ambiguous name:
// more than one held cookie or twin of it matches the request. The caller
// must then withhold the name from the visitor's cookies too.
func (c *popupOwnerContexts) firstPartyLookup(owner, uri string, req cookieURL, name string, now time.Time) (hit *WebpopupCookie, ambiguous bool) {
	origin := httpsOriginOf(uri)
	if owner == "" || origin == "" {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	h, ok := c.fp[owner]
	if !ok || h.origin != origin || c.byOwner[owner] != origin || now.Sub(h.at) >= firstPartyHoldTTL {
		return nil, false
	}
	// Every cookie of the name the request matches counts: more than one,
	// an eligible one next to a twin the browser would also send, is
	// ambiguous and served as nothing.
	n := 0
	for _, hc := range h.set.cookies {
		if hc.Name == name && hc.Expires > now.Unix() && cookieMatchesURL(hc.Domain, hc.Path, hc.Secure, req) {
			hc := hc
			hit = &hc
			n++
		}
	}
	for _, b := range h.set.blockers {
		if b.Name == name && (b.Expires == 0 || b.Expires > now.Unix()) && cookieMatchesURL(b.Domain, b.Path, b.Secure, req) {
			n++
		}
	}
	if n != 1 {
		return nil, n > 1
	}
	return hit, false
}

// dropFirstParty forgets every held cookie that a deletion request for
// host and path (names nil = any name) covers, for all owners: CEF has one
// cookie store, so a deletion reaches every owner's cookie.
func (c *popupOwnerContexts) dropFirstParty(host, path string, names map[string]bool) {
	if path == "" {
		path = "/"
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for owner, h := range c.fp {
		kept := h.set.cookies[:0:0]
		for _, hc := range h.set.cookies {
			if !cookieDeleteMatches(hc.Domain, hc.Path, hc.Name, host, path, names) {
				kept = append(kept, hc)
			}
		}
		h.set.cookies = kept
		bl := h.set.blockers[:0:0]
		for _, b := range h.set.blockers {
			if b.foreign || !cookieDeleteMatches(b.Domain, b.Path, b.Name, host, path, names) {
				bl = append(bl, b)
			}
		}
		h.set.blockers = bl
		c.fp[owner] = h
		if len(h.set.cookies) == 0 && len(h.set.blockers) == 0 {
			delete(c.fp, owner)
		}
	}
}

func bracketHost(host string) string {
	host = strings.ToLower(host)
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}

func pathOrRoot(p string) string {
	if p == "" {
		return "/"
	}
	return p
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

// readAllowedGen is readAllowed that also returns the owner's popup
// generation the answer was judged under (0 for the ownerless read), for
// stillCurrent to check when an asynchronous read completes.
func (c *popupOwnerContexts) readAllowedGen(owner, uri string) (uint64, bool) {
	if owner == "" {
		return 0, true
	}
	origin := httpsOriginOf(uri)
	c.mu.Lock()
	defer c.mu.Unlock()
	if origin == "" || c.byOwner[owner] != origin {
		return 0, false
	}
	return c.gen[owner], true
}

// stillCurrent reports whether no popup of owner began after gen was read.
func (c *popupOwnerContexts) stillCurrent(owner string, gen uint64) bool {
	if owner == "" {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen[owner] == gen
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

// cookieBelongsToOrigin reports whether c would be sent to the https origin
// origin (domain-match, path-match, Secure).
func cookieBelongsToOrigin(c *WebpopupCookie, origin string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	p := c.Path
	if p == "" {
		p = "/"
	}
	return cookieMatchesURL(c.Domain, c.Path, c.Secure,
		cookieURL{scheme: "https", host: bracketHost(u.Hostname()), path: p})
}
