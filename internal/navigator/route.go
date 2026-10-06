package navigator

import (
	"cmp"
	"github.com/czz/movian-go/internal/trace"
	"regexp"
	"slices"
	"sync"
)

// RouteCallback is invoked when a registered route matches a URL.
// The callback receives the page prop (for building the page content),
// the URL, and any regex capture groups.
type RouteCallback func(page any, url string, matches []string)

// Route represents a registered URL route.
type Route struct {
	pattern  string
	regex    *regexp.Regexp
	prio     int
	callback func(page any, url string, matches []string)
}

// RouteSystem manages ECMAScript-style URL routes for the navigator.
type RouteSystem struct {
	mu     sync.Mutex
	routes []*Route

	ts *trace.TraceSystem // C: trace() global — injected via NavigatorSystem
}

// NewRouteSystem creates a new route system.
func NewRouteSystem() *RouteSystem {
	return &RouteSystem{
		routes: make([]*Route, 0),
	}
}

// CreateRoute registers a route pattern with a callback.
// The pattern is anchored at the start (prefixed with ^ if not already).
// Routes are sorted by priority (longer literal prefix = higher priority).
func (rs *RouteSystem) CreateRoute(pattern string, callback func(page any, url string, matches []string)) error {
	if pattern == "" {
		return nil
	}

	// Anchor at start like C Movian
	if pattern[0] != '^' {
		pattern = "^" + pattern
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return err
	}

	// Check for duplicate
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for _, r := range rs.routes {
		if r.pattern == pattern {
			rs.ts.Info("route", "Route %s already exists", pattern)
			return nil
		}
	}

	// Calculate priority: position of first regex special char
	// (same heuristic as C Movian's strcspn)
	specialChars := "()[]*?+$"
	prio := len(pattern)
	for i, c := range pattern {
		for _, sc := range specialChars {
			if c == sc {
				prio = i
				goto found
			}
		}
	}
found:
	if prio == 0 {
		prio = 1 << 30 // INT32_MAX equivalent
	}

	r := &Route{
		pattern:  pattern,
		regex:    re,
		prio:     prio,
		callback: callback,
	}

	rs.routes = append(rs.routes, r)

	// Sort by priority descending (higher priority = matched first)
	slices.SortFunc(rs.routes, func(a, b *Route) int { return cmp.Compare(b.prio, a.prio) })

	rs.ts.Debug("route", "Route %s added (prio: %d)", pattern, prio)
	return nil
}

// TestURL checks if any registered route matches the given URL.
func (rs *RouteSystem) TestURL(url string) bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for _, r := range rs.routes {
		if r.regex.MatchString(url) {
			return true
		}
	}
	return false
}

// OpenURL attempts to open a URL by matching against registered routes.
// Returns true if a route matched and the callback was invoked.
// The page prop is passed to the callback for building page content.
func (rs *RouteSystem) OpenURL(page any, url string) bool {
	rs.mu.Lock()
	var matched *Route
	for _, r := range rs.routes {
		if r.regex.MatchString(url) {
			matched = r
			break
		}
	}
	rs.mu.Unlock()

	if matched == nil {
		return false
	}

	matches := matched.regex.FindStringSubmatch(url)
	rs.ts.Debug("NAV", "route matched %s for URL %s, matches: %v", matched.pattern, url, matches[1:])
	if matched.callback != nil {
		matched.callback(page, url, matches[1:])
	}
	return true
}

// RemoveRoute removes a route by pattern.
func (rs *RouteSystem) RemoveRoute(pattern string) {
	if pattern[0] != '^' {
		pattern = "^" + pattern
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for i, r := range rs.routes {
		if r.pattern == pattern {
			rs.routes = slices.Delete(rs.routes, i, i+1)
			rs.ts.Debug("route", "Route %s removed", pattern)
			return
		}
	}
}
