package airplay

import (
	"fmt"
	"strings"

	"github.com/czz/movian-go/internal/event"
	httpnet "github.com/czz/movian-go/internal/networking/http"
)

// AirplayHandler handles AirPlay API requests
type AirplayHandler struct {
	eventMgr *event.EventManager
}

// NewAirplayHandler creates a new AirPlay handler
func NewAirplayHandler(eventMgr *event.EventManager) *AirplayHandler {
	return &AirplayHandler{
		eventMgr: eventMgr,
	}
}

// Reverse handles AirPlay reverse connection requests
// This is the Go equivalent of airplay_reverse in C
func (h *AirplayHandler) Reverse(hc *httpnet.HTTPConnection, remain string, opaque any, method httpnet.HTTPCmd) int {
	headers := make(httpnet.HTTPHeaders, 0)
	headers.HTTPHeaderAdd("Connection", "Upgrade", false)
	headers.HTTPHeaderAdd("Upgrade", "PTTH/1.0", false)

	return hc.HTTPSendRaw(101, "Switching Protocols", &headers, nil)
}

// Scrub handles AirPlay scrub requests (playback position)
// This is the Go equivalent of airplay_scrub in C
func (h *AirplayHandler) Scrub(hc *httpnet.HTTPConnection, remain string, opaque any, method httpnet.HTTPCmd) int {
	output := fmt.Sprintf("position: 0.123456\r\nduration: 50.123456")
	return hc.HTTPSendReply(0, "", "", "", 0, []byte(output))
}

// Play handles AirPlay play requests
// This is the Go equivalent of airplay_play in C
func (h *AirplayHandler) Play(hc *httpnet.HTTPConnection, remain string, opaque any, method httpnet.HTTPCmd) int {
	data := hc.HTTPGetPostData(nil, false)
	if data == nil {
		return 400
	}

	dataStr := string(data)
	url, err := extractContentLocation(dataStr)
	if err != nil {
		return 400
	}

	// Dispatch open URL event
	if h.eventMgr != nil {
		args := &event.EventOpenURLArgs{
			URL: url,
		}
		e := h.eventMgr.CreateOpenURLArgs(args)
		h.eventMgr.Dispatch(&e.Event)
	}

	return 200
}

// Rate handles AirPlay rate requests (playback speed)
// This is the Go equivalent of airplay_rate in C — a stub that
// acknowledges the request and does nothing else.
func (h *AirplayHandler) Rate(hc *httpnet.HTTPConnection, remain string, opaque any, method httpnet.HTTPCmd) int {
	return 200
}

// Register registers the AirPlay handlers with the HTTP server
// This is the Go equivalent of airplay_init in C
func (h *AirplayHandler) Register(server *httpnet.HTTPServer) {
	if server == nil {
		return
	}

	server.HTTPPathAdd("/reverse", h, h.Reverse, true)
	server.HTTPPathAdd("/scrub", h, h.Scrub, true)
	server.HTTPPathAdd("/play", h, h.Play, true)
	server.HTTPPathAdd("/rate", h, h.Rate, true)
}

// extractContentLocation extracts the Content-Location from POST data.
// C: strstr(data, "Content-Location: ") then url[strcspn(url, "\r\n")] = 0
// — the value ends at the first CR or LF, or at end of data.
func extractContentLocation(data string) (string, error) {
	const prefix = "Content-Location: "
	idx := strings.Index(data, prefix)
	if idx == -1 {
		return "", fmt.Errorf("Content-Location not found")
	}

	start := idx + len(prefix)
	if end := strings.IndexAny(data[start:], "\r\n"); end != -1 {
		return data[start : start+end], nil
	}
	return data[start:], nil
}
