package http

// HTTP client and WebSocket client implementation
// This package provides HTTP client functionality and WebSocket client support.
// The WebSocket client is used by pkg/proxy for STPP-based property proxying.
//
// Architecture:
// - pkg/networking: HTTP/WebSocket transport layer (no dependencies on prop)
// - pkg/proxy: Uses WebSocket client for STPP connections
// - pkg/api: Uses WebSocket server for STPP connections
// - pkg/prop: Property system (no networking dependency to avoid cycles)

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// HTTPCmd represents HTTP command types
type HTTPCmd int

const (
	HTTPCmdGet HTTPCmd = iota
	HTTPCmdHead
	HTTPCmdPost
	HTTPCmdSubscribe
	HTTPCmdUnsubscribe
)

// HTTPStatus codes
const (
	HTTPStatusOK                   = 200
	HTTPStatusFound                = 302
	HTTPStatusBadRequest           = 400
	HTTPStatusUnauthorized         = 401
	HTTPStatusNotFound             = 404
	HTTPStatusMethodNotAllowed     = 405
	HTTPStatusPreconditionFailed   = 412
	HTTPStatusUnsupportedMediaType = 415
	HTTPStatusNotImplemented       = 501
)

// HTTPHeader represents an HTTP header
type HTTPHeader struct {
	Key   string
	Value string
}

// HTTPHeaders represents a list of HTTP headers
type HTTPHeaders []HTTPHeader

// HTTPHeadersFree frees the headers
func (h *HTTPHeaders) HTTPHeadersFree() {
	*h = nil
}

// HTTPHeaderGet gets a header value by key
func (h *HTTPHeaders) HTTPHeaderGet(key string) string {
	keyLower := strings.ToLower(key)
	for _, header := range *h {
		if strings.ToLower(header.Key) == keyLower {
			return header.Value
		}
	}
	return ""
}

// HTTPHeaderAdd adds a header
func (h *HTTPHeaders) HTTPHeaderAdd(key, value string, shouldAppend bool) {
	if shouldAppend {
		for i, header := range *h {
			if strings.EqualFold(header.Key, key) {
				(*h)[i].Value = header.Value + ", " + value
				return
			}
		}
	}
	*h = append(*h, HTTPHeader{Key: key, Value: value})
}

// HTTPHeaderAddLWS adds headers from a line
func (h *HTTPHeaders) HTTPHeaderAddLWS(data string) {
	// Parse header line
	key, value, found := strings.Cut(data, ":")
	if found {
		h.HTTPHeaderAdd(strings.TrimSpace(key), strings.TrimSpace(value), false)
	}
}

// HTTPHeaderAddInt adds an integer header
func (h *HTTPHeaders) HTTPHeaderAddInt(key string, value int) {
	h.HTTPHeaderAdd(key, strconv.Itoa(value), false)
}

// HTTPHeaderMerge merges headers from another list
func (h *HTTPHeaders) HTTPHeaderMerge(src *HTTPHeaders) {
	if src == nil {
		return
	}
	for _, header := range *src {
		h.HTTPHeaderAdd(header.Key, header.Value, false)
	}
}

// HTTPClient represents an HTTP client
type HTTPClient struct {
	timeout   time.Duration
	userAgent string
	headers   HTTPHeaders
	proxyURL  *url.URL
	sslVerify bool
	cache     *httpCache
}

// NewHTTPClient creates a new HTTP client
func NewHTTPClient() *HTTPClient {
	client := &HTTPClient{
		timeout:   30 * time.Second,
		userAgent: "Movian Go/1.0",
		headers:   make(HTTPHeaders, 0),
		sslVerify: true,
		cache:     newHTTPCache(),
	}
	client.cache.startCleanup(5 * time.Minute)

	return client
}

// HTTPRequest represents an HTTP request
type HTTPRequest struct {
	Method  HTTPCmd
	URL     string
	Headers HTTPHeaders
	Body    []byte
}

// HTTPResponse represents an HTTP response
type HTTPResponse struct {
	StatusCode int
	Headers    HTTPHeaders
	Body       []byte
}

// Do performs an HTTP request
func (c *HTTPClient) Do(req *HTTPRequest) (*HTTPResponse, error) {
	method := http.MethodGet
	switch req.Method {
	case HTTPCmdHead:
		method = http.MethodHead
	case HTTPCmdPost:
		method = http.MethodPost
	}

	// Check cache for cacheable methods (GET only)
	ck := cacheKey(method, req.URL)
	if c.cache != nil && method == http.MethodGet {
		if entry := c.cache.get(ck); entry != nil && !entry.isExpired() {
			return entry.response, nil
		}
	}

	httpReq, err := http.NewRequest(method, req.URL, bytes.NewReader(req.Body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	for _, header := range req.Headers {
		httpReq.Header.Add(header.Key, header.Value)
	}

	// Add revalidation headers for stale cache entries
	if c.cache != nil && method == http.MethodGet {
		revalHeaders := c.cache.revalidationHeaders(ck)
		for _, h := range revalHeaders {
			httpReq.Header.Add(h.Key, h.Value)
		}
	}

	// Set default headers
	if c.userAgent != "" && httpReq.Header.Get("User-Agent") == "" {
		httpReq.Header.Set("User-Agent", c.userAgent)
	}

	// Create HTTP client with timeout
	httpClient := &http.Client{
		Timeout: c.timeout,
	}

	transport := &http.Transport{}

	if c.proxyURL != nil {
		transport.Proxy = http.ProxyURL(c.proxyURL)
	}

	if !c.sslVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}

	httpClient.Transport = transport

	// Perform request
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// Handle 304 Not Modified — serve cached response
	if resp.StatusCode == http.StatusNotModified && c.cache != nil {
		if entry := c.cache.get(ck); entry != nil {
			return entry.response, nil
		}
	}

	// Read response body (limited to 100MB to prevent OOM while allowing large playlists)
	limitedReader := io.LimitReader(resp.Body, 100*1024*1024)
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// Convert headers
	headers := make(HTTPHeaders, 0)
	for key, values := range resp.Header {
		for _, value := range values {
			headers.HTTPHeaderAdd(key, value, false)
		}
	}

	result := &HTTPResponse{
		StatusCode: resp.StatusCode,
		Headers:    headers,
		Body:       body,
	}

	// Cache successful GET responses
	if c.cache != nil && method == http.MethodGet && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		c.cache.put(ck, result, resp.Header)
	}

	return result, nil
}

// Get performs a GET request
func (c *HTTPClient) Get(url string) (*HTTPResponse, error) {
	return c.Do(&HTTPRequest{
		Method: HTTPCmdGet,
		URL:    url,
	})
}

// HTTPCTime parses a C-time formatted date
func HTTPCTime(tp *time.Time, d string) error {
	// Parse HTTP date format
	t, err := http.ParseTime(d)
	if err != nil {
		return err
	}
	*tp = t
	return nil
}

// HTTPAsctime formats a time as an HTTP date
func HTTPAsctime(tp time.Time, out []byte, outlen int) string {
	return tp.UTC().Format(http.TimeFormat)
}

// HTTPParseURIArgs parses URI arguments into headers
func HTTPParseURIArgs(headers *HTTPHeaders, args string, append bool) {
	if args == "" {
		return
	}

	pairs := strings.SplitSeq(args, "&")
	for pair := range pairs {
		key, value, found := strings.Cut(pair, "=")
		if found {
			headers.HTTPHeaderAdd(key, value, append)
		}
	}
}

// HTTPReadLine reads a line from a buffered reader
func HTTPReadLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	// Trim trailing whitespace
	line = strings.TrimRight(line, "\r\n")
	return line, nil
}
