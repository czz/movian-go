package http

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// cacheEntry represents a cached HTTP response
type cacheEntry struct {
	response     *HTTPResponse
	expiresAt    time.Time
	etag         string
	lastModified string
}

// isExpired returns true if the cache entry has expired
func (e *cacheEntry) isExpired() bool {
	if e.expiresAt.IsZero() {
		return false
	}
	return time.Now().After(e.expiresAt)
}

// httpCache provides HTTP response caching with ETag/Last-Modified revalidation
type httpCache struct {
	mu      sync.RWMutex
	entries map[string]*cacheEntry
}

// newHTTPCache creates a new HTTP cache
func newHTTPCache() *httpCache {
	return &httpCache{
		entries: make(map[string]*cacheEntry),
	}
}

// cacheKey generates a cache key from method and URL
func cacheKey(method string, url string) string {
	return method + " " + url
}

// get retrieves a cached entry. Returns nil if not found or expired.
func (c *httpCache) get(key string) *cacheEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[key]
	if !ok {
		return nil
	}
	return entry
}

// put stores a response in the cache, parsing Cache-Control headers
func (c *httpCache) put(key string, resp *HTTPResponse, headers http.Header) {
	entry := &cacheEntry{response: resp}

	// Parse Cache-Control
	cc := headers.Get("Cache-Control")
	if cc != "" {
		if strings.Contains(cc, "no-store") || strings.Contains(cc, "no-cache") {
			return
		}
		if strings.Contains(cc, "max-age=") {
			parts := strings.Split(cc, "max-age=")
			if len(parts) > 1 {
				maxAgeStr, _, _ := strings.Cut(parts[1], ",")
				maxAgeStr = strings.TrimSpace(maxAgeStr)
				if maxAge, err := strconv.Atoi(maxAgeStr); err == nil && maxAge > 0 {
					entry.expiresAt = time.Now().Add(time.Duration(maxAge) * time.Second)
				}
			}
		}
	}

	// Store ETag and Last-Modified for revalidation
	entry.etag = headers.Get("ETag")
	entry.lastModified = headers.Get("Last-Modified")

	// If no Cache-Control, use Expires header
	if entry.expiresAt.IsZero() {
		expires := headers.Get("Expires")
		if expires != "" && expires != "0" {
			if t, err := http.ParseTime(expires); err == nil {
				entry.expiresAt = t
			}
		}
	}

	// Only cache successful responses
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return
	}

	c.mu.Lock()
	c.entries[key] = entry
	c.mu.Unlock()
}

// revalidationHeaders returns conditional request headers for revalidation
func (c *httpCache) revalidationHeaders(key string) HTTPHeaders {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.entries[key]
	if !ok {
		return nil
	}

	var headers HTTPHeaders
	if entry.etag != "" {
		headers.HTTPHeaderAdd("If-None-Match", entry.etag, false)
	}
	if entry.lastModified != "" {
		headers.HTTPHeaderAdd("If-Modified-Since", entry.lastModified, false)
	}
	return headers
}

// cleanup removes expired entries
func (c *httpCache) cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, entry := range c.entries {
		if entry.isExpired() {
			delete(c.entries, key)
		}
	}
}

// startCleanup starts a background goroutine that periodically removes expired entries
func (c *httpCache) startCleanup(interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			c.cleanup()
		}
	}()
}
