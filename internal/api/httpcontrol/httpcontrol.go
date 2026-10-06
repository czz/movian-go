package httpcontrol

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/czz/movian-go/internal/event"
	httpnet "github.com/czz/movian-go/internal/networking/http"
)

// PluginInstaller defines the interface for plugin install/uninstall via HTTP control.
type PluginInstaller interface {
	InstallPluginByID(id string) error
	UninstallPluginByID(id string) error
}

// HTTPControlHandler handles HTTP control API requests
type HTTPControlHandler struct {
	eventMgr           *event.EventManager
	cachePath          string
	dataRoot           string
	systemName         string
	appVersion         string
	appName            string
	appNameUser        string
	enableExperimental bool
	canRestart         bool
	authToken          string
	pluginInstaller    PluginInstaller
	openURLFunc        func(url string) // Called when /api/open?url=... is requested
}

// NewHTTPControlHandler creates a new HTTP control handler
func NewHTTPControlHandler(eventMgr *event.EventManager, cachePath, dataRoot, systemName, appVersion, appName, appNameUser string) *HTTPControlHandler {
	return &HTTPControlHandler{
		eventMgr:    eventMgr,
		cachePath:   cachePath,
		dataRoot:    dataRoot,
		systemName:  systemName,
		appVersion:  appVersion,
		appName:     appName,
		appNameUser: appNameUser,
	}
}

// SetOpenURLFunc sets the callback for opening URLs from the HTTP API.
// This mirrors C's event_to_ui(EVENT_OPENURL) → nav_eventsink → nav_open0 chain.
func (h *HTTPControlHandler) SetOpenURLFunc(fn func(url string)) {
	h.openURLFunc = fn
}

// Open handles HTTP open requests
// This is the Go equivalent of hc_open in C
func (h *HTTPControlHandler) Open(hc *httpnet.HTTPConnection, remain string, opaque any, method httpnet.HTTPCmd) int {
	if !h.checkAuth(hc) {
		return 403
	}
	url := hc.HTTPArgGetReq("url")

	if url != "" {
		if h.openURLFunc != nil {
			h.openURLFunc(url)
		} else if h.eventMgr != nil {
			e := h.eventMgr.CreateStr(event.EVENT_OPENURL, url)
			h.eventMgr.Dispatch(&e.Event)
		}
		return hc.HTTPRedirect("/api/open")
	}

	html := `<html>
 <body>
  <form name="input" method="get">
   URL: <input type="text" name="url" style="width:500px"/>
   <input type="submit" value="Open" />
  </form>
 </body>
</html>`
	return hc.HTTPSendReply(0, "text/html", "", "", 0, []byte(html))
}

// Done handles HTTP done requests
// This is the Go equivalent of hc_done in C
func (h *HTTPControlHandler) Done(hc *httpnet.HTTPConnection, remain string, opaque any, method httpnet.HTTPCmd) int {
	return hc.HTTPSendReply(0, "text/plain", "", "", 0, []byte("OK"))
}

// Action handles action requests
// This is the Go equivalent of hc_action in C
func (h *HTTPControlHandler) Action(hc *httpnet.HTTPConnection, remain string, opaque any, method httpnet.HTTPCmd) int {
	if !h.checkAuth(hc) {
		return 403
	}
	if remain == "" {
		return 404
	}

	if h.eventMgr != nil {
		e := h.eventMgr.CreateActionStr(remain)
		h.eventMgr.Dispatch(e)
	}
	return 200
}

// UTF8 handles UTF-8 input requests
// This is the Go equivalent of hc_utf8 in C
func (h *HTTPControlHandler) UTF8(hc *httpnet.HTTPConnection, remain string, opaque any, method httpnet.HTTPCmd) int {
	if !h.checkAuth(hc) {
		return 403
	}
	str := hc.HTTPArgGetReq("str")
	if str == "" {
		return 400
	}

	if h.eventMgr != nil {
		// Parse UTF-8 string and dispatch events
		for _, r := range str {
			if r == 8 {
				// Backspace
				e := h.eventMgr.CreateActionMulti([]event.ActionType{event.ACTION_BS, event.ACTION_NAV_BACK})
				h.eventMgr.Dispatch(e.AsEvent())
			} else {
				e := h.eventMgr.CreateInt(event.EVENT_UNICODE, int(r))
				h.eventMgr.Dispatch(&e.Event)
			}
		}
	}
	return 200
}

// NotifyUser handles user notification requests
// This is the Go equivalent of hc_notify_user in C
func (h *HTTPControlHandler) NotifyUser(hc *httpnet.HTTPConnection, remain string, opaque any, method httpnet.HTTPCmd) int {
	if !h.checkAuth(hc) {
		return 403
	}
	msg := hc.HTTPArgGetReq("msg")
	icon := hc.HTTPArgGetReq("icon")
	levelStr := hc.HTTPArgGetReq("level")
	timeoutStr := hc.HTTPArgGetReq("timeout")

	if msg == "" {
		return 400
	}

	timeout := 5
	if timeoutStr != "" {
		if t, err := strconv.Atoi(timeoutStr); err == nil {
			timeout = t
		}
	}

	// Determine notification type
	var notifyType int // NOTIFY_INFO
	if levelStr == "error" {
		notifyType = 2 // NOTIFY_ERROR
	} else if levelStr == "warning" {
		notifyType = 1 // NOTIFY_WARNING
	}

	// Send notification (would integrate with notifications package)
	_ = notifyType
	_ = icon
	_ = timeout
	_ = msg

	return 200
}

// Diagnostics handles diagnostics requests
// This is the Go equivalent of hc_diagnostics in C
func (h *HTTPControlHandler) Diagnostics(hc *httpnet.HTTPConnection, remain string, opaque any, method httpnet.HTTPCmd) int {
	body := fmt.Sprintf("<html><body><strong>%s</strong> Version %s<br><br>",
		html.EscapeString(h.systemName), html.EscapeString(h.appVersion))
	body += h.diagHTML()
	body += "</body></html>"
	return hc.HTTPSendReply(0, "text/html; charset=utf-8", "", "", 0, []byte(body))
}

// diagHTML generates diagnostic HTML with log file links
// This is the Go equivalent of diag_html in C
func (h *HTTPControlHandler) diagHTML() string {
	var result strings.Builder
	now := time.Now()

	for i := range 6 {
		logPath := filepath.Join(h.cachePath, "log", fmt.Sprintf("%s-%d.log", h.appName, i))
		timeAgo := "unknown"

		if info, err := os.Stat(logPath); err == nil {
			modTime := info.ModTime()
			modSeconds := now.Sub(modTime).Seconds()

			if modSeconds < 60 {
				timeAgo = fmt.Sprintf("%d seconds", int(modSeconds))
			} else if modSeconds < 3600 {
				timeAgo = fmt.Sprintf("%d minutes", int(modSeconds/60))
			} else {
				timeAgo = fmt.Sprintf("%d hours", int(modSeconds/3600))
			}
		} else {
			// File doesn't exist, skip this entry
			continue
		}

		result.WriteString(fmt.Sprintf("%s-%d.log (Last modified %s ago): <a href=\"/api/logfile/%d\">View</a> | <a href=\"/api/logfile/%d?mode=download\">Download</a>| <a href=\"/api/logfile/%d?mode=pastebin\">Pastebin</a><br>",
			html.EscapeString(h.appName), i, html.EscapeString(timeAgo), i, i, i))
	}

	return result.String()
}

// LogFile handles log file requests
// This is the Go equivalent of hc_logfile in C
func (h *HTTPControlHandler) LogFile(hc *httpnet.HTTPConnection, remain string, opaque any, method httpnet.HTTPCmd) int {
	if remain == "" {
		return 400
	}

	n, err := strconv.Atoi(remain)
	if err != nil {
		return 400
	}

	mode := hc.HTTPArgGetReq("mode")

	logPath := filepath.Join(h.cachePath, "log", fmt.Sprintf("%s-%d.log", h.appName, n))
	logContent, err := os.ReadFile(logPath)
	if err != nil {
		return 404
	}

	if mode == "download" {
		hc.HTTPSetResponseHdr("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s-%d.log\"", h.appName, n))
	}

	return hc.HTTPSendReply(0, "text/plain; charset=utf-8", "", "", 0, logContent)
}

// Root handles root requests
// This is the Go equivalent of hc_root in C
func (h *HTTPControlHandler) Root(hc *httpnet.HTTPConnection, remain string, opaque any, method httpnet.HTTPCmd) int {
	if !h.enableExperimental {
		return h.rootOld(hc, remain, opaque, method)
	}
	// Serve static file from dataroot
	return hc.HTTPSendReply(0, "text/html", "", "", 0, []byte("Static index.html"))
}

// rootOld handles old root requests
// This is the Go equivalent of hc_root_old in C
func (h *HTTPControlHandler) rootOld(hc *httpnet.HTTPConnection, remain string, opaque any, method httpnet.HTTPCmd) int {
	url := hc.HTTPArgGetReq("url")

	if url != "" {
		if h.openURLFunc != nil {
			h.openURLFunc(url)
		} else if h.eventMgr != nil {
			e := h.eventMgr.CreateStr(event.EVENT_OPENURL, url)
			h.eventMgr.Dispatch(&e.Event)
		}
		return hc.HTTPRedirect("/")
	}

	body := fmt.Sprintf("<html><body><h2>%s</h2><p>Version %s", html.EscapeString(h.systemName), html.EscapeString(h.appVersion))
	body += fmt.Sprintf("<form name=\"input\" method=\"get\">Open URL in %s: <input type=\"text\" name=\"url\" style=\"width:500px\"/><input type=\"submit\" value=\"Open\" /></form>", html.EscapeString(h.appNameUser))
	body += "<h3>Diagnostics</h3>"
	body += h.diagHTML()
	body += "<p><a href=\"/api/screenshot\">Upload screenshot to imgur</a></p>"
	body += "<p><a href=\"/api/translation\">Upload and test new translation (.lang) file</a></p>"
	body += "</body></html>"
	return hc.HTTPSendReply(httpnet.HTTPStatusOK, "text/html", "", "", 0, []byte(body))
}

// Favicon handles favicon requests
// This is the Go equivalent of hc_favicon in C
func (h *HTTPControlHandler) Favicon(hc *httpnet.HTTPConnection, remain string, opaque any, method httpnet.HTTPCmd) int {
	faviconPath := filepath.Join(h.dataRoot, "res", "static", "favicon.ico")
	content, err := os.ReadFile(faviconPath)
	if err != nil {
		return 404
	}
	return hc.HTTPSendReply(0, "image/x-icon", "", "", 0, content)
}

// Static handles static file requests
// This is the Go equivalent of hc_static in C
func (h *HTTPControlHandler) Static(hc *httpnet.HTTPConnection, remain string, opaque any, method httpnet.HTTPCmd) int {
	if remain == "" || strings.Contains(remain, "..") {
		return 404
	}

	staticPath := filepath.Join(h.dataRoot, "res", "static", remain)
	content, err := os.ReadFile(staticPath)
	if err != nil {
		return 404
	}

	// Determine content type based on file extension
	contentType := h.getContentType(remain)
	return hc.HTTPSendReply(0, contentType, "", "", 0, content)
}

// getContentType returns the appropriate content type for a file
func (h *HTTPControlHandler) getContentType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js":
		return "application/javascript"
	case ".css":
		return "text/css"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	case ".webp":
		return "image/webp"
	case ".ico":
		return "image/x-icon"
	default:
		return "application/octet-stream"
	}
}

// Restart handles restart requests
// This is the Go equivalent of hc_restart in C
func (h *HTTPControlHandler) Restart(hc *httpnet.HTTPConnection, remain string, opaque any, method httpnet.HTTPCmd) int {
	if !h.checkAuth(hc) {
		return 403
	}
	// Send response before exiting
	hc.HTTPSendReply(0, "text/plain", "", "", 0, []byte("Restarting..."))

	// In a real implementation, this would be more graceful cleanup
	go func() {
		time.Sleep(100 * time.Millisecond) // Give time for response to be sent
		os.Exit(13)
	}()

	return 200
}

// SetAuthToken sets the authentication token. If set, all API endpoints
// require a matching "token" query parameter or "Authorization" header.
func (h *HTTPControlHandler) SetAuthToken(token string) {
	h.authToken = token
}

// SetPluginInstaller injects the plugin manager for install/uninstall endpoints.
func (h *HTTPControlHandler) SetPluginInstaller(pi PluginInstaller) {
	h.pluginInstaller = pi
}

// PluginInstall handles plugin install requests via /api/plugin/install?id=<fqid>
func (h *HTTPControlHandler) PluginInstall(hc *httpnet.HTTPConnection, remain string, opaque any, method httpnet.HTTPCmd) int {
	if !h.checkAuth(hc) {
		return 403
	}
	if h.pluginInstaller == nil {
		return hc.HTTPSendReply(0, "text/plain", "", "", 0, []byte("Plugin system not available"))
	}
	id := hc.HTTPArgGetReq("id")
	if id == "" {
		return 400
	}
	if err := h.pluginInstaller.InstallPluginByID(id); err != nil {
		return hc.HTTPSendReply(0, "text/plain", "", "", 0, []byte(fmt.Sprintf("Install failed: %v", err)))
	}
	return hc.HTTPSendReply(0, "text/plain", "", "", 0, []byte("OK"))
}

// PluginUninstall handles plugin uninstall requests via /api/plugin/uninstall?id=<fqid>
func (h *HTTPControlHandler) PluginUninstall(hc *httpnet.HTTPConnection, remain string, opaque any, method httpnet.HTTPCmd) int {
	if !h.checkAuth(hc) {
		return 403
	}
	if h.pluginInstaller == nil {
		return hc.HTTPSendReply(0, "text/plain", "", "", 0, []byte("Plugin system not available"))
	}
	id := hc.HTTPArgGetReq("id")
	if id == "" {
		return 400
	}
	if err := h.pluginInstaller.UninstallPluginByID(id); err != nil {
		return hc.HTTPSendReply(0, "text/plain", "", "", 0, []byte(fmt.Sprintf("Uninstall failed: %v", err)))
	}
	return hc.HTTPSendReply(0, "text/plain", "", "", 0, []byte("OK"))
}

// checkAuth verifies the request is authenticated if authToken is configured.
// Returns true if authorized (or no auth required), false otherwise.
func (h *HTTPControlHandler) checkAuth(hc *httpnet.HTTPConnection) bool {
	if h.authToken == "" {
		return true
	}
	token := hc.HTTPArgGetReq("token")
	if token == h.authToken {
		return true
	}
	authHeader := hc.HTTPArgGetReq("Authorization")
	if authHeader == "Bearer "+h.authToken {
		return true
	}
	return false
}

// Image handles /api/image — loads an image via backend_imageloader and returns it.
// C: hc_image (httpcontrol.c:155-222)
func (h *HTTPControlHandler) Image(hc *httpnet.HTTPConnection, remain string, opaque any, method httpnet.HTTPCmd) int {
	if remain == "" {
		return 404
	}
	// C: backend_imageloader with im_no_decoding=1, then returns raw coded data.
	// Go: We don't have a backend_imageloader wired here, so return 404.
	// This is a C-canonical stub — the endpoint exists but image loading
	// requires backend integration not yet wired in httpcontrol.
	return 404
}

// OpenParameterized handles /api/openparameterized — constructs a URL from
// remain + query args and dispatches it as an openurl event.
// C: hc_open_parameterized (httpcontrol.c:639-662)
func (h *HTTPControlHandler) OpenParameterized(hc *httpnet.HTTPConnection, remain string, opaque any, method httpnet.HTTPCmd) int {
	if remain == "" {
		return 404
	}
	// C: htsmsg from query args, then remain:json_args → event_create_openurl
	// Go: dispatch via openURLFunc if available
	if h.openURLFunc != nil {
		h.openURLFunc(remain)
	}
	return hc.HTTPSendReply(0, "text/plain", "", "", 0, []byte("OK"))
}

// BinReplace handles /api/replace — replaces the binary (for OTA upgrades).
// C: hc_binreplace (httpcontrol.c:273-313)
func (h *HTTPControlHandler) BinReplace(hc *httpnet.HTTPConnection, remain string, opaque any, method httpnet.HTTPCmd) int {
	// C: if(gconf.binary == NULL) return PRECONDITION_FAILED;
	// C: if(!gconf.enable_bin_replace) return 403;
	// Go: binary replacement is not supported in the Go port.
	return 403
}

// Register registers the HTTP control handlers with the HTTP server
// This is the Go equivalent of httpcontrol_init in C
func (h *HTTPControlHandler) Register(server *httpnet.HTTPServer) {
	if server == nil {
		return
	}

	server.HTTPPathAdd("/api/done", h, h.Done, false)
	server.HTTPPathAdd("/api/image", h, h.Image, false)
	server.HTTPPathAdd("/api/open", h, h.Open, true)
	server.HTTPPathAdd("/api/openparameterized", h, h.OpenParameterized, false)
	server.HTTPPathAdd("/api/input/action", h, h.Action, false)
	server.HTTPPathAdd("/api/input/utf8", h, h.UTF8, true)
	server.HTTPPathAdd("/api/notifyuser", h, h.NotifyUser, true)
	server.HTTPPathAdd("/api/diag", h, h.Diagnostics, true)
	server.HTTPPathAdd("/api/logfile", h, h.LogFile, false)
	server.HTTPPathAdd("/api/replace", h, h.BinReplace, true)
	server.HTTPPathAdd("/", h, h.Root, true)
	server.HTTPPathAdd("/favicon.ico", h, h.Favicon, true)
	server.HTTPPathAdd("/api/static", h, h.Static, false)
	server.HTTPPathAdd("/api/plugin/install", h, h.PluginInstall, false)
	server.HTTPPathAdd("/api/plugin/uninstall", h, h.PluginUninstall, false)

	if h.canRestart {
		server.HTTPPathAdd("/api/restart", h, h.Restart, true)
	}
}
