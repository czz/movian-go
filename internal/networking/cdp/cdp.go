// Package cdp — a minimal Chrome DevTools Protocol client over the
// canonical websocket primitives (internal/networking/websocket).
// No upstream C counterpart: used by the windows webpopup branch to
// reach WebView2's cookie jar (HttpOnly included) through the remote
// debugging endpoint, since webview_go does not expose the COM
// CookieManager. Usable against any Chromium-family endpoint.
package cdp

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	wspkg "github.com/czz/movian-go/internal/networking/websocket"
)

// Fetcher reads a whole file or URL through the caller's canonical
// stack — in movian this is fileaccess (fs paths and http:// alike),
// so no net/http or os.ReadFile leaks into this package.
type Fetcher func(url string) ([]byte, error)

// DevToolsActivePort reads <userDataDir>/DevToolsActivePort — the file
// Chromium writes when --remote-debugging-port[=0] is in effect. Line 1
// is the HTTP/ws port, line 2 the browser-target ws path.
func DevToolsActivePort(fetch Fetcher, userDataDir string) (int, string, error) {
	data, err := fetch(filepath.Join(userDataDir, "DevToolsActivePort"))
	if err != nil {
		return 0, "", err
	}
	lines := strings.SplitN(strings.TrimRight(string(data), "\r\n"), "\n", 2)
	port, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil || port <= 0 {
		return 0, "", fmt.Errorf("bad DevToolsActivePort: %q", lines[0])
	}
	browserPath := ""
	if len(lines) > 1 {
		browserPath = strings.TrimSpace(lines[1])
	}
	return port, browserPath, nil
}

// cdpTarget — one entry of /json/list.
type cdpTarget struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

// PageWSURL returns the webSocketDebuggerUrl of the first "page"
// target exposed by the debugging HTTP endpoint (fallback: any target
// that carries one). fetch pulls the JSON document via the caller's
// HTTP stack.
func PageWSURL(fetch Fetcher, port int) (string, error) {
	body, err := fetch(fmt.Sprintf("http://127.0.0.1:%d/json/list", port))
	if err != nil {
		return "", err
	}
	var targets []cdpTarget
	if err := json.Unmarshal(body, &targets); err != nil {
		return "", err
	}
	fallback := ""
	for _, t := range targets {
		if t.WebSocketDebuggerURL == "" {
			continue
		}
		if t.Type == "page" {
			return t.WebSocketDebuggerURL, nil
		}
		if fallback == "" {
			fallback = t.WebSocketDebuggerURL
		}
	}
	if fallback != "" {
		return fallback, nil
	}
	return "", errors.New("no debuggable targets")
}

// Conn — a CDP websocket session to a single target.
type Conn struct {
	conn net.Conn
	r    *wspkg.WebSocketReader
	w    *wspkg.WebSocketWriter
	next int
}

// Dial performs the HTTP upgrade handshake to a ws:// URL
// (ws://host:port/devtools/<kind>/<id>).
func Dial(wsURL string, timeout time.Duration) (*Conn, error) {
	u, err := url.Parse(wsURL)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("tcp", u.Host, timeout)
	if err != nil {
		return nil, err
	}
	conn.SetDeadline(time.Now().Add(timeout))
	var kb [16]byte
	rand.Read(kb[:])
	path := u.RequestURI()
	if path == "" {
		path = "/"
	}
	fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\n"+
		"Connection: Upgrade\r\nSec-WebSocket-Key: %s\r\n"+
		"Sec-WebSocket-Version: 13\r\n\r\n",
		path, u.Host, base64.StdEncoding.EncodeToString(kb[:]))
	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil {
		conn.Close()
		return nil, err
	}
	if !strings.Contains(status, " 101") {
		conn.Close()
		return nil, fmt.Errorf("ws upgrade refused: %s", strings.TrimSpace(status))
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil || line == "\r\n" || line == "\n" {
			break
		}
	}
	c := &Conn{
		conn: conn,
		r:    wspkg.NewWebSocketReader(br),
		w:    wspkg.NewWebSocketWriter(conn, true), // clients must mask
		next: 1,
	}
	return c, nil
}

// Close terminates the ws session and the TCP connection.
func (c *Conn) Close() error {
	c.w.WriteClose(wspkg.WSStatusNormalClose, "")
	return c.conn.Close()
}

// Call sends {"id":n,"method":m,"params":p} and returns the result
// payload. Events (no id) are skipped; pings are answered.
func (c *Conn) Call(method string, params any, timeout time.Duration) (json.RawMessage, error) {
	id := c.next
	c.next++
	req := map[string]any{"id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if err := c.w.WriteText(string(payload)); err != nil {
		return nil, err
	}
	c.conn.SetDeadline(time.Now().Add(timeout))
	for {
		frame, err := c.r.ReadFrame()
		if err != nil {
			return nil, err
		}
		switch frame.Opcode {
		case wspkg.WSOpcodePing:
			c.w.WritePong(frame.Payload)
			continue
		case wspkg.WSOpcodeClose:
			return nil, errors.New("ws closed by peer")
		case wspkg.WSOpcodeText:
		default:
			continue
		}
		var resp struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Err    *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(frame.Payload, &resp) != nil || resp.ID != id {
			continue
		}
		if resp.Err != nil {
			return nil, fmt.Errorf("cdp %s: %s", method, resp.Err.Message)
		}
		return resp.Result, nil
	}
}

// Cookies runs Network.getCookies for urls and returns name → value —
// HttpOnly cookies included, unlike document.cookie.
func (c *Conn) Cookies(urls ...string) (map[string]string, error) {
	res, err := c.Call("Network.getCookies",
		map[string]any{"urls": urls}, 5*time.Second)
	if err != nil {
		return nil, err
	}
	var out struct {
		Cookies []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"cookies"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, err
	}
	m := make(map[string]string, len(out.Cookies))
	for _, ck := range out.Cookies {
		m[ck.Name] = ck.Value
	}
	return m, nil
}
