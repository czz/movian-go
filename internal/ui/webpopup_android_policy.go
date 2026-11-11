// webpopup_android_policy.go — which cookie the Android WebView bridge may
// hand to the Core as clearance. Pure Go: no JNI, no Android runtime, no
// build tags except the variant constant, so `go test ./internal/ui` covers
// it on the host. The shared WebpopupCookie carries no origin or partition field. The origin,
// the owner and the first-party limit travel in AndroidBridgeContext.
package ui

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// AndroidBridgeContext is what the Android bridge knows about one popup,
// apart from the cookie: the exact top-level origin the WebView showed and
// the plugin that owns the request. It is not part of the shared types.
type AndroidBridgeContext struct {
	// Origin is an exact HTTPS origin: scheme and host, optional port, no path.
	Origin string
	// Owner is the plugin that opened the popup.
	Owner string
	// FirstPartyLimited is set on a result: the cookie was Partitioned and
	// was accepted only for Origin. It must not be treated as an ordinary
	// jar cookie and is not a CHIPS partition key.
	FirstPartyLimited bool
}

// AndroidClearance is an accepted cookie plus the context it is valid in.
type AndroidClearance struct {
	Cookie  *WebpopupCookie
	Context AndroidBridgeContext
}

// SelectAndroidClearance picks the one clearance cookie `name` from the
// Set-Cookie strings of the WebView (AndroidX getCookieInfo) for target. It
// returns a secret-free reason on rejection. The variant (strict or
// first-party) is fixed at build time by the android_cf_firstparty tag.
func SelectAndroidClearance(lines []string, target string, ctx AndroidBridgeContext, owner, name, ua string) (*AndroidClearance, string) {
	return selectAndroidClearanceAt(time.Now(), lines, target, ctx, owner, name, ua, androidFirstPartyVariant)
}

func selectAndroidClearanceAt(now time.Time, lines []string, target string, ctx AndroidBridgeContext, owner, name, ua string, allowPartitioned bool) (*AndroidClearance, string) {
	if name == "" {
		return nil, "cookie-name-empty"
	}
	if ua == "" {
		return nil, "ua-empty"
	}
	targetURL, ok := parseAndroidHTTPSURL(target)
	if !ok {
		return nil, "target-invalid-https-origin"
	}
	topURL, ok := parseAndroidHTTPSURL(ctx.Origin)
	if !ok || topURL.Path != "/" ||
		topURL.RawQuery != "" || topURL.Fragment != "" || !sameAndroidHTTPSOrigin(topURL, targetURL) {
		return nil, "needs-exact-top-level-origin"
	}
	if ctx.Owner == "" || owner == "" || ctx.Owner != owner {
		return nil, "owner-mismatch"
	}

	var candidates []androidSetCookie
	for _, line := range lines {
		if !strings.Contains(strings.SplitN(line, ";", 2)[0], "=") {
			// A nameless cookie (serialized without "=") cannot be the
			// requested one and must not block the selection.
			continue
		}
		c, reason := parseAndroidSetCookieAt(now, line)
		if reason != "" {
			return nil, reason
		}
		if c.name != name {
			continue
		}
		if c.domain != "" && !androidCookieDomainMatch(c.domain, targetURL.Hostname()) {
			return nil, "host-mismatch"
		}
		if !androidCookiePathMatch(c.path, targetURL.EscapedPath()) {
			continue
		}
		if c.value == "" {
			return nil, "empty-value"
		}
		if c.partitioned && !allowPartitioned {
			return nil, "partitioned-not-allowed"
		}
		if !c.secure {
			return nil, "secure-required"
		}
		if !c.hasExpiry || !c.expires.After(now) {
			return nil, "expires-not-in-future"
		}
		candidates = append(candidates, c)
	}
	switch len(candidates) {
	case 0:
		return nil, "no-matching-cookie"
	case 1:
	default:
		return nil, "ambiguous-cookie"
	}
	c := candidates[0]
	domain := targetURL.Hostname()
	if c.domain != "" {
		domain = c.domain // keep the leading dot: it marks a domain cookie
	}
	out := ctx
	out.Origin = topURL.Scheme + "://" + topURL.Host
	out.FirstPartyLimited = c.partitioned
	return &AndroidClearance{
		Cookie: &WebpopupCookie{
			Name: c.name, Value: c.value, Domain: domain, Path: c.path,
			Secure: true, HTTPOnly: c.httpOnly, Expires: c.expires.Unix(),
		},
		Context: out,
	}, ""
}

type androidSetCookie struct {
	name, value, domain, path string
	expires                   time.Time
	hasExpiry                 bool
	secure, httpOnly          bool
	partitioned               bool
}

// parseAndroidSetCookieAt parses one Set-Cookie string. Max-Age overrides
// Expires, as in RFC 6265; its expiry is relative to now.
func parseAndroidSetCookieAt(now time.Time, raw string) (androidSetCookie, string) {
	var c androidSetCookie
	parts := strings.Split(raw, ";")
	name, value, ok := strings.Cut(strings.TrimSpace(parts[0]), "=")
	if !ok || strings.TrimSpace(name) == "" {
		return c, "malformed-cookie"
	}
	c.name, c.value = strings.TrimSpace(name), strings.TrimSpace(value)
	var maxAge *time.Time
	for _, rawAttr := range parts[1:] {
		key, value, hasValue := strings.Cut(strings.TrimSpace(rawAttr), "=")
		value = strings.TrimSpace(value)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "domain":
			if !hasValue || value == "" {
				return c, "malformed-domain"
			}
			c.domain = strings.ToLower(value)
		case "path":
			if !hasValue || value == "" {
				return c, "malformed-path"
			}
			c.path = value
		case "expires":
			expires, err := http.ParseTime(value)
			if !hasValue || err != nil {
				return c, "malformed-expires"
			}
			c.expires, c.hasExpiry = expires, true
		case "max-age":
			n, err := strconv.ParseInt(value, 10, 64)
			if !hasValue || err != nil {
				return c, "malformed-max-age"
			}
			t := time.Unix(0, 0) // Max-Age <= 0: already expired
			if n > 0 {
				t = now.Add(time.Duration(min(n, 1<<31)) * time.Second)
			}
			maxAge = &t
		case "secure":
			c.secure = true
		case "httponly":
			c.httpOnly = true
		case "partitioned":
			c.partitioned = true
		}
	}
	if maxAge != nil {
		c.expires, c.hasExpiry = *maxAge, true
	}
	if c.path == "" || !strings.HasPrefix(c.path, "/") {
		return c, "path-missing-or-invalid"
	}
	return c, ""
}

func parseAndroidHTTPSURL(raw string) (*url.URL, bool) {
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.User != nil || u.Opaque != "" || u.Hostname() == "" {
		return nil, false
	}
	u.Scheme = "https"
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, false
		}
	}
	host := strings.ToLower(u.Hostname())
	for i := range host {
		if host[i] > 0x7f {
			return nil, false
		}
	}
	port := u.Port()
	if port == "443" {
		port = ""
	}
	u.Host = host
	if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	}
	if port != "" {
		u.Host += ":" + port
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u, true
}

func sameAndroidHTTPSOrigin(a, b *url.URL) bool {
	return a.Scheme == b.Scheme && a.Host == b.Host
}

func androidCookiePathMatch(cookiePath, requestPath string) bool {
	if cookiePath == "" {
		cookiePath = "/"
	}
	if requestPath == "" {
		requestPath = "/"
	}
	return requestPath == cookiePath || (strings.HasPrefix(requestPath, cookiePath) &&
		(strings.HasSuffix(cookiePath, "/") || len(requestPath) > len(cookiePath) && requestPath[len(cookiePath)] == '/'))
}

// androidCookieDomainMatch: a leading-dot domain covers the host and its
// subdomains; a host-only domain matches the host exactly.
func androidCookieDomainMatch(domain, host string) bool {
	if rest, ok := strings.CutPrefix(domain, "."); ok {
		return host == rest || strings.HasSuffix(host, domain)
	}
	return host == domain
}
