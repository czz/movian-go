package http

import (
	"bufio"
	"cmp"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/czz/movian-go/internal/networking/websocket"
	"github.com/czz/movian-go/internal/version"
)

// HTTPPath represents an HTTP path handler
type HTTPPath struct {
	path      string
	opaque    any
	callback  HTTPCallback
	leaf      bool
	ws        bool
	wsSetup   WebSocketCallbackConnected
	wsData    WebSocketCallbackData
	wsFini    WebSocketCallbackDisconnected
	wsRemoved WebSocketCallbackRemoved
}

// HTTPCallback is called when an HTTP request is received
type HTTPCallback func(hc *HTTPConnection, remain string, opaque any, method HTTPCmd) int

// WebSocketCallbackConnected is called when a WebSocket connection is established
type WebSocketCallbackConnected func(hc *HTTPConnection, pathOpaque any) int

// WebSocketCallbackData is called when WebSocket data is received
type WebSocketCallbackData func(hc *HTTPConnection, opcode int, data []byte, len int, connectionOpaque any) int

// WebSocketCallbackDisconnected is called when a WebSocket connection is closed
type WebSocketCallbackDisconnected func(hc *HTTPConnection, connectionOpaque any)

// WebSocketCallbackRemoved is called when a WebSocket path is removed
type WebSocketCallbackRemoved func(pathOpaque any)

// HTTPConnection represents an HTTP connection
type HTTPConnection struct {
	conn         net.Conn
	reader       *bufio.Reader
	writer       *bufio.Writer
	path         *HTTPPath
	opaque       any
	remoteAddr   string
	localAddr    string
	headers      HTTPHeaders
	args         map[string]string
	postData     []byte
	responseHdrs HTTPHeaders
	version      int  // C: hc_version — HTTP_VERSION_1_0 / HTTP_VERSION_1_1
	keepAlive    bool // C: hc_keep_alive
	mu           sync.Mutex
	closed       bool
	replySent    bool
	replyCond    *sync.Cond
}

// C: http.h HTTP_VERSION_1_0 / HTTP_VERSION_1_1
const (
	httpVersion10 = 0
	httpVersion11 = 1
)

// HTTPServer represents an HTTP server
type HTTPServer struct {
	paths       map[string]*HTTPPath
	prefixPaths []*HTTPPath // non-leaf paths sorted by length descending
	listener    net.Listener
	// auxListeners — C: extra asyncio_listen() sockets sharing the same
	// accept callback; STOS adds a second listener on port 80
	// (http_server.c:1176-1178).
	auxListeners []net.Listener
	mu           sync.RWMutex
	running      atomic.Bool
	wg           sync.WaitGroup
}

// NewHTTPServer creates a new HTTP server
func NewHTTPServer() *HTTPServer {
	return &HTTPServer{
		paths: make(map[string]*HTTPPath),
	}
}

// NewTestHTTPConnection creates an HTTPConnection for testing purposes.
// It uses net.Pipe so the caller can read responses from the returned client side.
func NewTestHTTPConnection(args map[string]string) (*HTTPConnection, net.Conn) {
	clientConn, serverConn := net.Pipe()
	hc := &HTTPConnection{
		conn:   serverConn,
		reader: bufio.NewReader(serverConn),
		writer: bufio.NewWriter(serverConn),
		args:   args,
	}
	return hc, clientConn
}

// ConnClose closes the underlying connection. Used for test cleanup.
func (hc *HTTPConnection) ConnClose() {
	if hc.conn != nil {
		hc.conn.Close()
	}
}

// SetTestPostData sets the postData field for testing purposes.
func (hc *HTTPConnection) SetTestPostData(data []byte) {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	hc.postData = data
}

// SetTestHeader sets a request header for testing purposes.
func (hc *HTTPConnection) SetTestHeader(name, value string) {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	hc.headers.HTTPHeaderAdd(name, value, false)
}

// HTTPPathAdd adds a path handler
func (s *HTTPServer) HTTPPathAdd(path string, opaque any, callback HTTPCallback, leaf bool) *HTTPPath {
	s.mu.Lock()
	defer s.mu.Unlock()

	p := &HTTPPath{
		path:     path,
		opaque:   opaque,
		callback: callback,
		leaf:     leaf,
		ws:       false,
	}

	s.paths[path] = p
	if !leaf {
		s.prefixPaths = append(s.prefixPaths, p)
		slices.SortFunc(s.prefixPaths, func(a, b *HTTPPath) int { return cmp.Compare(len(b.path), len(a.path)) })
	}
	return p
}

// HTTPPathRemove removes a path handler
func (s *HTTPServer) HTTPPathRemove(p *HTTPPath) {
	if p == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !p.leaf {
		for i, pp := range s.prefixPaths {
			if pp == p {
				s.prefixPaths = slices.Delete(s.prefixPaths, i, i+1)
				break
			}
		}
	}

	if p.ws && p.wsRemoved != nil {
		p.wsRemoved(p.opaque)
	}

	delete(s.paths, p.path)
}

// HTTPAddWebSocket adds a WebSocket path handler
func (s *HTTPServer) HTTPAddWebSocket(path string, opaque any,
	setup WebSocketCallbackConnected,
	data WebSocketCallbackData,
	fini WebSocketCallbackDisconnected,
	removed WebSocketCallbackRemoved) *HTTPPath {

	s.mu.Lock()
	defer s.mu.Unlock()

	p := &HTTPPath{
		path:      path,
		opaque:    opaque,
		ws:        true,
		wsSetup:   setup,
		wsData:    data,
		wsFini:    fini,
		wsRemoved: removed,
	}

	s.paths[path] = p
	return p
}

// HTTPSetOpaque sets the opaque data for a connection
func (hc *HTTPConnection) HTTPSetOpaque(opaque any) {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	hc.opaque = opaque
}

// HTTPSendReply sends an HTTP reply
func (hc *HTTPConnection) HTTPSendReply(rc int, content string, encoding string, location string, maxage int, output []byte) int {
	headers := make(HTTPHeaders, 0)

	if content != "" {
		headers.HTTPHeaderAdd("Content-Type", content, false)
	}
	if encoding != "" {
		headers.HTTPHeaderAdd("Content-Encoding", encoding, false)
	}
	if location != "" {
		headers.HTTPHeaderAdd("Location", location, false)
	}
	if maxage > 0 {
		headers.HTTPHeaderAdd("Cache-Control", fmt.Sprintf("max-age=%d", maxage), false)
	}

	return hc.HTTPSendRaw(rc, "", &headers, output)
}

// HTTPSendRaw sends a raw HTTP reply
func (hc *HTTPConnection) HTTPSendRaw(rc int, rctxt string, headers *HTTPHeaders, output []byte) int {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	if hc.closed {
		return -1
	}

	// Write status line
	// rc=0 means "200 OK" (C convention: http_send_reply with rc=0)
	httpRC := rc
	if httpRC == 0 {
		httpRC = 200
	}
	statusText := http.StatusText(httpRC)
	if statusText == "" {
		statusText = "Unknown"
	}
	if rctxt != "" {
		statusText = rctxt
	}

	hc.writer.WriteString(fmt.Sprintf("HTTP/1.1 %d %s\r\n", httpRC, statusText))

	// Write standard headers
	// C: http_server.c:369 — "Server: "APPNAMEUSER" %s" with appversion
	hc.writer.WriteString(fmt.Sprintf("Server: Movian Go %s\r\n", version.AppVersion()))
	hc.writer.WriteString(fmt.Sprintf("Date: %s\r\n", time.Now().UTC().Format(http.TimeFormat)))
	hc.writer.WriteString("Cache-Control: no-cache\r\n")
	// C: http_send_header — "Connection: %s" per hc->hc_keep_alive
	if hc.keepAlive {
		hc.writer.WriteString("Connection: Keep-Alive\r\n")
	} else {
		hc.writer.WriteString("Connection: Close\r\n")
	}

	// Write headers
	if headers != nil {
		for _, header := range *headers {
			hc.writer.WriteString(fmt.Sprintf("%s: %s\r\n", header.Key, header.Value))
		}
	}

	// Write response headers
	for _, header := range hc.responseHdrs {
		hc.writer.WriteString(fmt.Sprintf("%s: %s\r\n", header.Key, header.Value))
	}

	// C: htsbuf_qprintf(&hdrs, "Content-Length: %d\r\n", contentlen) —
	// always emitted, even for empty bodies; without it a keep-alive
	// client cannot tell where the reply ends and hangs.
	hc.writer.WriteString(fmt.Sprintf("Content-Length: %d\r\n", len(output)))

	hc.writer.WriteString("\r\n")

	// Write body
	if len(output) > 0 {
		hc.writer.Write(output)
	}

	err := hc.writer.Flush()
	hc.replySent = true
	if hc.replyCond != nil {
		hc.replyCond.Broadcast()
	}
	if err != nil {
		return -1
	}
	return 0
}

// HTTPError sends an HTTP error response
func (hc *HTTPConnection) HTTPError(error int, extra string, args ...any) int {
	msg := fmt.Sprintf(extra, args...)
	return hc.HTTPSendReply(error, "text/plain", "", "", 0, []byte(msg))
}

// HTTPRedirect sends an HTTP redirect
func (hc *HTTPConnection) HTTPRedirect(location string) int {
	return hc.HTTPSendReply(HTTPStatusFound, "", "", location, 0, nil)
}

// HTTPArgGetReq gets a request argument
func (hc *HTTPConnection) HTTPArgGetReq(name string) string {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	if hc.args == nil {
		return ""
	}
	return hc.args[name]
}

// HTTPArgGetHdr gets a request header
func (hc *HTTPConnection) HTTPArgGetHdr(name string) string {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	return hc.headers.HTTPHeaderGet(name)
}

// HTTPGetMyHost gets the local host address
func (hc *HTTPConnection) HTTPGetMyHost() string {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	return hc.localAddr
}

// HTTPGetMyPort gets the local port
func (hc *HTTPConnection) HTTPGetMyPort() int {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	host, portStr, err := net.SplitHostPort(hc.localAddr)
	if err != nil {
		return 0
	}
	_ = host
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0
	}
	return port
}

// HTTPGetPostData gets the POST data
func (hc *HTTPConnection) HTTPGetPostData(size *int, steal bool) []byte {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	if size != nil {
		*size = len(hc.postData)
	}

	if steal {
		data := hc.postData
		hc.postData = nil
		return data
	}
	return hc.postData
}

// HTTPSetResponseHdr sets a response header
func (hc *HTTPConnection) HTTPSetResponseHdr(name string, value string) {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	hc.responseHdrs.HTTPHeaderAdd(name, value, false)
}

// WebSocketSend sends WebSocket data over the connection
func (hc *HTTPConnection) WebSocketSend(opcode int, data []byte, len int) {
	if hc.conn == nil {
		return
	}
	frame := websocket.BuildWebSocketFrame(true, opcode, data[:len], false)
	hc.mu.Lock()
	defer hc.mu.Unlock()
	hc.conn.Write(frame)
}

// Listen starts the HTTP server on the specified port
func (s *HTTPServer) Listen(port int) error {
	addr := fmt.Sprintf(":%d", port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	s.listener = listener
	s.running.Store(true)

	s.wg.Add(1)
	go s.acceptLoop(listener)

	return nil
}

// ListenAdditional — C: a second asyncio_listen("http-server", port, ...)
// on the same handler set (http_server.c:1177, STOS: port 80).
func (s *HTTPServer) ListenAdditional(port int) error {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.auxListeners = append(s.auxListeners, listener)
	s.mu.Unlock()
	s.running.Store(true)

	s.wg.Add(1)
	go s.acceptLoop(listener)
	return nil
}

// Close stops the HTTP server and waits for connections to finish
func (s *HTTPServer) Close() {
	s.running.Store(false)
	if s.listener != nil {
		s.listener.Close()
	}
	s.mu.Lock()
	for _, l := range s.auxListeners {
		l.Close()
	}
	s.auxListeners = nil
	s.mu.Unlock()
	s.wg.Wait()
}

// acceptLoop accepts incoming connections
func (s *HTTPServer) acceptLoop(listener net.Listener) {
	defer s.wg.Done()

	for s.running.Load() {
		conn, err := listener.Accept()
		if err != nil {
			if !s.running.Load() {
				return
			}
			// Avoid CPU spin on persistent errors (e.g. too many open files)
			time.Sleep(100 * time.Millisecond)
			continue
		}

		s.wg.Add(1)
		go s.handleConnection(conn)
	}
}

// handleConnection handles an HTTP connection
func (s *HTTPServer) handleConnection(conn net.Conn) {
	defer s.wg.Done()

	var hc *HTTPConnection
	defer func() {
		if hc != nil {
			hc.mu.Lock()
			hc.closed = true
			if hc.replyCond != nil {
				hc.replyCond.Broadcast()
			}
			hc.mu.Unlock()
		}
		conn.Close()
	}()

	remoteAddr := conn.RemoteAddr().String()
	localAddr := conn.LocalAddr().String()

	hc = &HTTPConnection{
		conn:       conn,
		reader:     bufio.NewReader(conn),
		writer:     bufio.NewWriter(conn),
		remoteAddr: remoteAddr,
		localAddr:  localAddr,
	}
	hc.replyCond = sync.NewCond(&hc.mu)

	// C: http_handle_input loops per request — a keep-alive connection
	// is reused for subsequent requests until close.
	for {
		// Reset per-request state (C: hc->hc_request_headers/args/post_data
		// are re-initialized per request in HCS_COMMAND state)
		hc.headers = make(HTTPHeaders, 0, 16)
		hc.args = make(map[string]string, 8)
		hc.postData = nil
		hc.responseHdrs = make(HTTPHeaders, 0, 8)
		hc.replySent = false

		// Read request line
		requestLine, err := hc.reader.ReadString('\n')
		if err != nil {
			return
		}

		// Parse request line — C http_tokenize: method URI version
		parts := strings.Fields(requestLine)
		if len(parts) < 2 {
			hc.HTTPError(HTTPStatusBadRequest, "Invalid request line")
			return
		}

		methodStr := parts[0]
		requestURI := parts[1]

		// C: http_cmd_* parse the version token ("HTTP/1.0"/"HTTP/1.1")
		hc.version = httpVersion11
		if len(parts) >= 3 {
			switch parts[2] {
			case "HTTP/1.0":
				hc.version = httpVersion10
			case "HTTP/1.1":
				hc.version = httpVersion11
			}
		}

		// Parse method
		var method HTTPCmd
		switch strings.ToUpper(methodStr) {
		case "GET":
			method = HTTPCmdGet
		case "HEAD":
			method = HTTPCmdHead
		case "POST":
			method = HTTPCmdPost
		case "SUBSCRIBE":
			method = HTTPCmdSubscribe
		case "UNSUBSCRIBE":
			method = HTTPCmdUnsubscribe
		default:
			hc.HTTPError(HTTPStatusMethodNotAllowed, "Method not allowed")
			return
		}

		// Parse URI
		parsedURL, err := url.ParseRequestURI(requestURI)
		if err != nil {
			hc.HTTPError(HTTPStatusBadRequest, "Invalid URI")
			return
		}

		// Parse query string
		for key, values := range parsedURL.Query() {
			if len(values) > 0 {
				hc.args[key] = values[0]
			}
		}

		// Read headers (LWS continuation lines handled by HTTPHeaderAddLWS)
		for {
			line, err := hc.reader.ReadString('\n')
			if err != nil {
				return
			}

			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				break
			}

			hc.headers.HTTPHeaderAddLWS(line)
		}

		// C: http_handle_request — keep-alive is per-version:
		//   HTTP/1.0: off unless "Connection: keep-alive"
		//   HTTP/1.1: on unless "Connection: close"
		connHdr := hc.headers.HTTPHeaderGet("connection")
		if hc.version == httpVersion10 {
			hc.keepAlive = connHdr != "" && strings.EqualFold(connHdr, "keep-alive")
		} else {
			hc.keepAlive = !(connHdr != "" && strings.EqualFold(connHdr, "close"))
		}

		// Read POST data if present
		// C: http_cmd_post — no Content-Length → disconnect; >16MB → disconnect;
		//    "Expect: 100-continue" → emit HTTP/1.1 100 Continue
		if method == HTTPCmdPost {
			contentLength := hc.headers.HTTPHeaderGet("Content-Length")
			if contentLength == "" {
				// C: "No content length in POST, make us disconnect"
				return
			}
			length, err := strconv.Atoi(contentLength)
			if err != nil || length > 16*1024*1024 {
				// C: hc_post_len > 16MB → keep_alive=0 + disconnect
				return
			}
			if length > 0 {
				if expect := hc.headers.HTTPHeaderGet("Expect"); expect != "" &&
					strings.EqualFold(expect, "100-continue") {
					hc.writer.WriteString("HTTP/1.1 100 Continue\r\n\r\n")
					hc.writer.Flush()
				}
				hc.postData = make([]byte, length)
				if _, err := io.ReadFull(hc.reader, hc.postData); err != nil {
					return
				}
			}

			// C: http_cmd_post (http_server.c:745-758) — for
			// application/x-www-form-urlencoded bodies the post data is
			// parsed into hc_req_args just like the URI query args.
			ctype := hc.headers.HTTPHeaderGet("Content-Type")
			if ctype != "" {
				if semi := strings.Index(ctype, ";"); semi >= 0 {
					ctype = strings.TrimSpace(ctype[:semi])
				}
				if ctype == "application/x-www-form-urlencoded" {
					if vals, err := url.ParseQuery(string(hc.postData)); err == nil {
						for key, values := range vals {
							if len(values) > 0 {
								hc.args[key] = values[0]
							}
						}
					}
				}
			}
		}

		// Find matching path handler
		path := parsedURL.Path
		s.mu.RLock()
		var handler *HTTPPath
		var remain string

		// Try exact match first
		if h, ok := s.paths[path]; ok {
			handler = h
			remain = ""
		} else {
			// Try prefix match — prefixPaths sorted by length descending,
			// so first match is the longest prefix (early exit).
			// C: http_resolve (http_server.c:268-307) — a prefix only
			// matches when the char after it is '/' (or '?' — already
			// stripped by the URL parse); remain = v + 1 skips the '/'.
			for _, h := range s.prefixPaths {
				if strings.HasPrefix(path, h.path) {
					v := path[len(h.path):]
					if v == "" {
						handler = h
						remain = ""
					} else if v[0] == '/' {
						handler = h
						remain = v[1:]
					}
					if handler != nil {
						break
					}
				}
			}
		}
		s.mu.RUnlock()

		if handler == nil {
			hc.HTTPError(HTTPStatusNotFound, "Not found")
			if !hc.keepAlive {
				return
			}
			continue
		}

		hc.path = handler

		// Call handler
		if handler.ws {
			// WebSocket upgrade — consumes the connection permanently
			if err := s.handleWebSocketUpgrade(hc); err != nil {
				hc.HTTPError(HTTPStatusBadRequest, "WebSocket upgrade failed")
			}
			return
		}

		// C: http_exec (http_server.c:540-560) — the callback's return
		// code drives the reply: HTTP_STATUS_OK → canned "OK\n",
		// err > 0 → http_error, err == 0 → handler already replied
		// (possibly asynchronously), err < 0 → abort().
		cbret := handler.callback(hc, remain, handler.opaque, method)
		if cbret == HTTPStatusOK {
			hc.HTTPSendReply(0, "text/plain", "", "", 0, []byte("OK\n"))
		} else if cbret > 0 {
			hc.HTTPError(cbret, "")
		}
		// C: err < 0 → abort(). Go: the only negative source is a
		// failed HTTPSendReply on a dead socket (C's http_send_reply
		// returns void), so cbret == 0 and cbret < 0 both mean
		// "nothing further to send".

		// C: the http connection stays alive until a reply is sent —
		// async handlers (e.g. hc_screenshot) reply after returning.
		hc.mu.Lock()
		for !hc.replySent && !hc.closed {
			hc.replyCond.Wait()
		}
		hc.mu.Unlock()

		// C: if(output queue empty && !hc->hc_keep_alive) return 1 (close)
		if !hc.keepAlive {
			return
		}
	}
}

// handleWebSocketUpgrade handles WebSocket upgrade
func (s *HTTPServer) handleWebSocketUpgrade(hc *HTTPConnection) error {
	// Check for WebSocket upgrade headers
	upgrade := hc.headers.HTTPHeaderGet("Upgrade")
	if strings.ToLower(upgrade) != "websocket" {
		return fmt.Errorf("not a WebSocket upgrade")
	}

	// Send upgrade response
	hc.HTTPSetResponseHdr("Upgrade", "websocket")
	hc.HTTPSetResponseHdr("Connection", "Upgrade")

	// Generate WebSocket accept key per RFC 6455
	secKey := hc.headers.HTTPHeaderGet("Sec-WebSocket-Key")
	if secKey != "" {
		h := sha1.Sum([]byte(secKey + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		acceptKey := base64.StdEncoding.EncodeToString(h[:])
		hc.HTTPSetResponseHdr("Sec-WebSocket-Accept", acceptKey)
	}

	hc.HTTPSendReply(101, "", "", "", 0, nil)

	// Call WebSocket init callback
	if hc.path.wsSetup != nil {
		if hc.path.wsSetup(hc, hc.path.opaque) != 0 {
			return fmt.Errorf("WebSocket init failed")
		}
	}

	// Handle WebSocket frames
	go s.handleWebSocketFrames(hc)

	return nil
}

// handleWebSocketFrames handles WebSocket frames
func (s *HTTPServer) handleWebSocketFrames(hc *HTTPConnection) {
	defer func() {
		if hc.path.wsFini != nil {
			hc.path.wsFini(hc, hc.opaque)
		}
		hc.conn.Close()
	}()

	for {
		// Read frame header (minimum 2 bytes)
		var header [2]byte
		if _, err := io.ReadFull(hc.reader, header[:]); err != nil {
			return
		}

		_ = header[0] & 0x80 // FIN bit - full frames read, fragmentation not buffered
		opcode := int(header[0] & 0x0F)
		masked := (header[1] & 0x80) != 0
		payloadLen := int(header[1] & 0x7F)

		// Read extended payload length
		if payloadLen == 126 {
			var ext [2]byte
			if _, err := io.ReadFull(hc.reader, ext[:]); err != nil {
				return
			}
			payloadLen = int(ext[0])<<8 | int(ext[1])
		} else if payloadLen == 127 {
			var ext [8]byte
			if _, err := io.ReadFull(hc.reader, ext[:]); err != nil {
				return
			}
			payloadLen = int(ext[0])<<56 | int(ext[1])<<48 | int(ext[2])<<40 | int(ext[3])<<32 |
				int(ext[4])<<24 | int(ext[5])<<16 | int(ext[6])<<8 | int(ext[7])
			if payloadLen > 10*1024*1024 {
				return
			}
		}

		// Read masking key
		var mask [4]byte
		if masked {
			if _, err := io.ReadFull(hc.reader, mask[:]); err != nil {
				return
			}
		}

		// Read full payload
		payload := make([]byte, payloadLen)
		if payloadLen > 0 {
			if _, err := io.ReadFull(hc.reader, payload); err != nil {
				return
			}
		}

		// Unmask if needed
		if masked && len(payload) > 0 {
			for i := range payload {
				payload[i] ^= mask[i%4]
			}
		}

		// Call data callback
		if hc.path.wsData != nil {
			hc.path.wsData(hc, opcode, payload, len(payload), hc.opaque)
		}
	}
}

// Stop stops the HTTP server
func (s *HTTPServer) Stop() {
	s.mu.Lock()
	s.running.Store(false)
	if s.listener != nil {
		s.listener.Close()
	}
	for _, l := range s.auxListeners {
		l.Close()
	}
	s.auxListeners = nil
	s.mu.Unlock()

	s.wg.Wait()
}

// IsRunning returns true if the server is running
func (s *HTTPServer) IsRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running.Load()
}

// GetPort returns the port the server is listening on
func (s *HTTPServer) GetPort() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.listener == nil {
		return 0
	}
	addr := s.listener.Addr()
	if addr == nil {
		return 0
	}
	tcpAddr, ok := addr.(*net.TCPAddr)
	if !ok {
		return 0
	}
	return tcpAddr.Port
}
