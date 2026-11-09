// webpopup_devtools.go — the CEF DevTools cookie read and its first-party
// rule. CEF's cookie visitor does not return Partitioned (CHIPS) cookies and
// cef_cookie_t has no partition field, so a cookie such as Cloudflare's
// cf_clearance, which Chromium 152 stores with partition key
// {topLevelSite: the site itself}, is invisible to it. The DevTools
// Network.getCookies method reports the key. Pure Go, no build tags and no
// cgo, so `go test ./internal/ui` covers it on every build.
//
// The rule mirrors Android's android_cf_firstparty policy
// (webpopup_android_policy.go): a Partitioned cookie is taken only when its
// key is first-party for the target, the popup of the requesting owner
// ended on the target's exact https origin, and the cookie is Secure with a
// future expiry; any other Partitioned cookie is refused.
package ui

import (
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"
)

// devtoolsReadLimit bounds one DevTools read: an unanswered one (the agent
// detached, the browser went away) stops blocking the next after this long.
const devtoolsReadLimit = 5 * time.Second

// firstPartyHoldTTL bounds how long the cookie a finished wait read stays
// available to persistWebCookie of the same owner and origin.
const firstPartyHoldTTL = 5 * time.Minute

// devtoolsPartition is a cookie's partition key. Legacy marks the pre-object
// form (a bare string), which cannot prove that no ancestor is cross-site.
type devtoolsPartition struct {
	TopLevelSite         string
	HasCrossSiteAncestor bool
	Legacy               bool
}

// devtoolsCookie — one Network.Cookie. PartitionKey is nil for an
// unpartitioned cookie.
type devtoolsCookie struct {
	Name, Value, Domain, Path string
	Expires                   float64 // Unix seconds; -1 for a session cookie
	Secure, HTTPOnly, Session bool
	PartitionKey              *devtoolsPartition
	PartitionKeyOpaque        bool
}

type devtoolsCookieJSON struct {
	Name               string          `json:"name"`
	Value              string          `json:"value"`
	Domain             string          `json:"domain"`
	Path               string          `json:"path"`
	Expires            float64         `json:"expires"`
	Secure             bool            `json:"secure"`
	HTTPOnly           bool            `json:"httpOnly"`
	Session            bool            `json:"session"`
	PartitionKey       json.RawMessage `json:"partitionKey"`
	PartitionKeyOpaque bool            `json:"partitionKeyOpaque"`
}

// parseDevToolsCookies decodes the "result" of Network.getCookies. A missing
// "cookies" key is an empty list; a malformed result is an error.
func parseDevToolsCookies(raw []byte) ([]devtoolsCookie, error) {
	var res struct {
		Cookies []devtoolsCookieJSON `json:"cookies"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	out := make([]devtoolsCookie, 0, len(res.Cookies))
	for _, j := range res.Cookies {
		c := devtoolsCookie{
			Name: j.Name, Value: j.Value, Domain: j.Domain, Path: j.Path,
			Expires: j.Expires, Secure: j.Secure, HTTPOnly: j.HTTPOnly,
			Session: j.Session, PartitionKeyOpaque: j.PartitionKeyOpaque,
		}
		if k := strings.TrimSpace(string(j.PartitionKey)); k != "" && k != "null" {
			var obj struct {
				TopLevelSite         string `json:"topLevelSite"`
				HasCrossSiteAncestor bool   `json:"hasCrossSiteAncestor"`
			}
			var str string
			switch {
			case json.Unmarshal(j.PartitionKey, &obj) == nil:
				c.PartitionKey = &devtoolsPartition{TopLevelSite: obj.TopLevelSite,
					HasCrossSiteAncestor: obj.HasCrossSiteAncestor}
			case json.Unmarshal(j.PartitionKey, &str) == nil:
				c.PartitionKey = &devtoolsPartition{TopLevelSite: str, Legacy: true}
			default:
				return nil, errors.New("devtools: unreadable partition key")
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// devtoolsCookiesRequest — the DevTools message that asks for the cookies
// the browser would send to uri, partitioned ones of the page's own
// partition included.
func devtoolsCookiesRequest(id int, uri string) string {
	u, _ := json.Marshal(uri)
	return `{"id":` + strconv.Itoa(id) + `,"method":"Network.getCookies","params":{"urls":[` + string(u) + `]}}`
}

// firstPartySiteOf is the site (scheme and registrable domain, no port) that
// Chromium puts in a partition key for host: https://eTLD+1, or the host
// itself for an IP literal or a name that is a public suffix.
func firstPartySiteOf(host string) string {
	if !isIP(host) {
		if d, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
			host = d
		}
	}
	return "https://" + host
}

// selectFirstPartyCookie picks the one Partitioned cookie `name` that may
// complete a wait for target. targetOrigin is the wait's exact https origin
// and topOrigin the origin the popup's main frame shows; they must be equal.
// It fails closed with a secret-free reason: the candidates are every cookie
// of that name the browser would send to target, so an unpartitioned twin or
// a second partition is ambiguous. An unpartitioned lone cookie is not this
// function's business ("not-partitioned"): the visitor path handles it.
func selectFirstPartyCookie(list []devtoolsCookie, target cookieURL, targetOrigin, topOrigin, name string, now time.Time) (*WebpopupCookie, string) {
	if targetOrigin == "" || topOrigin != targetOrigin {
		return nil, "origin-mismatch"
	}
	var cands []devtoolsCookie
	for _, c := range list {
		if c.Name == name && cookieMatchesURL(c.Domain, c.Path, c.Secure, target) {
			cands = append(cands, c)
		}
	}
	switch len(cands) {
	case 0:
		return nil, "no-matching-cookie"
	case 1:
	default:
		return nil, "ambiguous-cookie"
	}
	c := cands[0]
	if c.PartitionKey == nil && !c.PartitionKeyOpaque {
		return nil, "not-partitioned"
	}
	if c.PartitionKeyOpaque || c.PartitionKey.Legacy || c.PartitionKey.HasCrossSiteAncestor ||
		c.PartitionKey.TopLevelSite != firstPartySiteOf(target.host) {
		return nil, "cross-site-partition"
	}
	if c.Value == "" {
		return nil, "empty-value"
	}
	if !c.Secure {
		return nil, "secure-required"
	}
	if c.Session || c.Expires <= 0 || int64(c.Expires) <= now.Unix() {
		return nil, "expires-not-in-future"
	}
	return &WebpopupCookie{
		Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path,
		Secure: true, HTTPOnly: c.HTTPOnly, Expires: int64(c.Expires),
	}, ""
}

// mergeFirstParty is scopeCookies plus the held first-party cookie. Any
// visited cookie of the same name that u matches is its unpartitioned twin
// (the browser would send both): neither is kept, whether or not scopeCookies
// already withheld the name as ambiguous. The result is never nil.
func mergeFirstParty(visits []cookieVisit, u cookieURL, hold *WebpopupCookie, heldAmbiguous string) (out []WebpopupCookie, ambiguous int) {
	out, ambiguous = scopeCookies(visits, u)
	if heldAmbiguous != "" {
		// The hold found several cookies of this name for u: the visitor's
		// one is one of them and must not stand for the name.
		kept := out[:0]
		for _, c := range out {
			if c.Name == heldAmbiguous {
				ambiguous++
				continue
			}
			kept = append(kept, c)
		}
		out = kept
	}
	if hold == nil {
		return out, ambiguous
	}
	for _, v := range visits {
		if v.name == hold.Name && cookieMatchesURL(v.domain, v.path, v.secure, u) {
			kept := out[:0]
			for _, c := range out {
				if c.Name != hold.Name {
					kept = append(kept, c)
				}
			}
			return kept, ambiguous + 1
		}
	}
	return append(out, *hold), ambiguous
}

// devtoolsRead tracks the one DevTools read a session has in flight, with a
// time limit so a read that is never answered cannot block the wait.
type devtoolsRead struct {
	id      int
	started time.Time
}

func (r *devtoolsRead) busy(now time.Time) bool {
	return r.id != 0 && now.Sub(r.started) < devtoolsReadLimit
}

func (r *devtoolsRead) begin(id int, now time.Time) { r.id, r.started = id, now }

// take accepts the result of read id exactly once; a stale id (an abandoned
// read answering late) or no read in flight is refused.
func (r *devtoolsRead) take(id int) bool {
	if id == 0 || r.id != id {
		return false
	}
	r.id = 0
	return true
}

// selectFirstPartyInventory sorts, from a Storage.getCookies list, the
// cookies the browser would send to some path of origin (an exact https
// origin) into the first-party Partitioned ones the rule accepts (key = the
// origin's own site, no cross-site ancestor, Secure, non-empty, future
// expiry) and the others, which only matter as twins: they come back as
// blockers without their values. The page path is deliberately not
// consulted; the request's path and the ambiguity of a name are judged when
// the hold is read.
func selectFirstPartyInventory(list []devtoolsCookie, origin string, now time.Time) (eligible []WebpopupCookie, blockers []fpBlocker) {
	u, err := url.Parse(origin)
	if origin == "" || err != nil || httpsOriginOf(origin) != origin {
		return nil, nil
	}
	host := bracketHost(u.Hostname())
	site := firstPartySiteOf(host)
	for _, c := range list {
		p := c.Path
		if p == "" {
			p = "/"
		}
		if !cookieMatchesURL(c.Domain, p, c.Secure, cookieURL{scheme: "https", host: host, path: p}) {
			continue
		}
		k := c.PartitionKey
		if k != nil && !c.PartitionKeyOpaque && !k.Legacy && !k.HasCrossSiteAncestor && k.TopLevelSite == site &&
			c.Value != "" && c.Secure && !c.Session && c.Expires > 0 && int64(c.Expires) > now.Unix() {
			eligible = append(eligible, WebpopupCookie{
				Name: c.Name, Value: c.Value, Domain: c.Domain, Path: p,
				Secure: true, HTTPOnly: c.HTTPOnly, Expires: int64(c.Expires),
			})
			continue
		}
		b := fpBlocker{WebpopupCookie: WebpopupCookie{Name: c.Name, Domain: c.Domain, Path: p, Secure: c.Secure}}
		if !c.Session && c.Expires > 0 {
			b.Expires = int64(c.Expires)
		}
		// What deletableFirstParty would not remove: another partition.
		b.foreign = c.PartitionKeyOpaque || (k != nil && (k.Legacy || k.HasCrossSiteAncestor || k.TopLevelSite != site))
		blockers = append(blockers, b)
	}
	return eligible, blockers
}

// deletableFirstParty picks, from a Storage.getCookies list, the
// Partitioned cookies a deletion request for host and path (names nil = any)
// may remove: first-party ones only (key = the host's own site, no
// cross-site ancestor), matched like the visitor's deletion.
func deletableFirstParty(list []devtoolsCookie, host, path string, names map[string]bool) []devtoolsCookie {
	site := firstPartySiteOf(bracketHost(host))
	var out []devtoolsCookie
	for _, c := range list {
		k := c.PartitionKey
		if k == nil || c.PartitionKeyOpaque || k.Legacy || k.HasCrossSiteAncestor || k.TopLevelSite != site {
			continue
		}
		if cookieDeleteMatches(c.Domain, c.Path, c.Name, host, path, names) {
			out = append(out, c)
		}
	}
	return out
}

// devtoolsStorageRequest asks for every cookie of the browser context, with
// partition keys.
func devtoolsStorageRequest(id int) string {
	return `{"id":` + strconv.Itoa(id) + `,"method":"Storage.getCookies"}`
}

// devtoolsDeleteRequest deletes exactly the first-party Partitioned cookie c.
func devtoolsDeleteRequest(id int, c devtoolsCookie) string {
	p := struct {
		Name         string `json:"name"`
		Domain       string `json:"domain"`
		Path         string `json:"path"`
		PartitionKey struct {
			TopLevelSite         string `json:"topLevelSite"`
			HasCrossSiteAncestor bool   `json:"hasCrossSiteAncestor"`
		} `json:"partitionKey"`
	}{Name: c.Name, Domain: c.Domain, Path: c.Path}
	p.PartitionKey.TopLevelSite = c.PartitionKey.TopLevelSite
	b, _ := json.Marshal(p)
	return `{"id":` + strconv.Itoa(id) + `,"method":"Network.deleteCookies","params":` + string(b) + `}`
}
