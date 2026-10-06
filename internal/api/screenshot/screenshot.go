//go:generate go run generate_screenshot_cgo.go

package screenshot

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"unsafe"

	"github.com/czz/movian-go/internal/event"
	"github.com/czz/movian-go/internal/libav"
	httpnet "github.com/czz/movian-go/internal/networking/http"
	"github.com/czz/movian-go/internal/trace"
)

// Codec ID constants (from FFmpeg)
const (
	AVCodecIDMjpeg = 7
	AVCodecIDPng   = 61
	AVPixFmtRgb32  = 28
	AVPixFmtRGBA   = 26
	SwsBilinear    = 2
)

// ImgurResponse represents the JSON response from Imgur
type ImgurResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Link  string `json:"link"`
		Error string `json:"error"`
	} `json:"data"`
}

// ScreenshotHandler handles screenshot API requests
type ScreenshotHandler struct {
	eventMgr  *event.EventManager
	cachePath string
	imgurID   string
	ts        *trace.TraceSystem

	mu          sync.Mutex
	active      bool
	conn        *httpnet.HTTPConnection
	requestID   int64 // unique ID per request, prevents defer race
	nextRequest int64 // monotonic counter for request IDs
}

// NewScreenshotHandler creates a new screenshot handler
func NewScreenshotHandler(eventMgr *event.EventManager, cachePath, imgurID string, ts *trace.TraceSystem) *ScreenshotHandler {
	return &ScreenshotHandler{
		eventMgr:  eventMgr,
		cachePath: cachePath,
		imgurID:   imgurID,
		ts:        ts,
	}
}

// Screenshot handles HTTP screenshot requests
// This is the Go equivalent of hc_screenshot in C
func (h *ScreenshotHandler) Screenshot(hc *httpnet.HTTPConnection, remain string, opaque any, method httpnet.HTTPCmd) int {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.active {
		return 502 // Service Unavailable
	}

	h.active = true
	h.conn = hc
	h.requestID = h.nextRequest
	h.nextRequest++

	// C: event_to_ui(event_create(EVENT_MAKE_SCREENSHOT, sizeof(event_t)))
	// (screenshot.c:56) — the GLW ui.eventSink routes it to glw_dispatch_event
	// → glw_screenshot → screenshot_deliver → Deliver().
	if h.eventMgr != nil {
		e := h.eventMgr.Create(event.EVENT_MAKE_SCREENSHOT, 0)
		h.eventMgr.EventToUI(e)
	}
	return 0
}

// Pending returns true if there's an active screenshot request.
// Called from the render loop to check if a screenshot should be captured.
func (h *ScreenshotHandler) Pending() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.active
}

// Deliver delivers a pixmap for screenshot processing
// This is the Go equivalent of screenshot_deliver in C
//
// F-RACE-1 fix: consume the request atomically under h.mu BEFORE spawning
// process(). This clears h.active so the render loop's Pending() returns
// false on subsequent frames, guaranteeing exactly one Deliver() → one
// process() per request, matching C's single-invocation semantics.
func (h *ScreenshotHandler) Deliver(img image.Image) {
	h.mu.Lock()
	h.active = false
	h.mu.Unlock()
	go h.process(img)
}

// process processes the screenshot image
// This is the Go equivalent of screenshot_process in C
func (h *ScreenshotHandler) process(img image.Image) {
	// Capture the request ID and connection atomically so we can:
	// 1. Avoid clobbering a newer request's state in the defer.
	// 2. Use the correct connection throughout process() even if a new
	//    request arrives and overwrites h.conn while we're still running.
	h.mu.Lock()
	myRequestID := h.requestID
	myConn := h.conn
	h.mu.Unlock()

	// Guarantee that active/conn are reset on ALL exit paths, including
	// panics from CGO/FFmpeg, HTTP, or file I/O. The requestID check
	// prevents a slow-exiting goroutine from clobbering a newer request.
	defer func() {
		h.mu.Lock()
		if h.requestID == myRequestID {
			h.active = false
			h.conn = nil
		}
		h.mu.Unlock()
		if r := recover(); r != nil {
			h.ts.Debug("SCREENSHOT", "panic in process: %v\n%s", r, debug.Stack())
		}
	}()

	if img == nil {
		h.response(myConn, "", "Screenshot not supported on this platform")
		return
	}

	h.ts.Trace(trace.TRACE_DEBUG, "Screenshot", "Processing image %d x %d",
		img.Bounds().Dx(), img.Bounds().Dy())

	// Determine codec ID from the captured connection (not h.conn)
	useJPEG := myConn != nil

	codecID := AVCodecIDPng
	if useJPEG {
		codecID = AVCodecIDMjpeg
	}

	// Compress using libav
	data, err := h.compressWithLibav(img, codecID)
	if err != nil {
		h.response(myConn, "", fmt.Sprintf("Unable to compress image: %v", err))
		return
	}

	// If no HTTP connection, save to file
	hasConn := myConn != nil

	if !hasConn {
		savePath := filepath.Join(h.cachePath, "screenshot.png")
		err := h.saveToFile(data, savePath)
		if err != nil {
			h.response(myConn, "", fmt.Sprintf("Unable to save screenshot: %v", err))
		} else {
			h.ts.Trace(trace.TRACE_INFO, "SCREENSHOT", "Written to %s", savePath)
		}
		return
	}

	// Upload to Imgur
	imgurURL, err := h.uploadToImgur(data)
	if err != nil {
		h.response(myConn, "", fmt.Sprintf("Imgur upload failed: %v", err))
	} else {
		h.response(myConn, imgurURL, "")
	}
}

// compressWithLibav compresses the image using libav (FFmpeg 7+)
// This is the Go equivalent of screenshot_compress in C
func (h *ScreenshotHandler) compressWithLibav(img image.Image, codecID int) ([]byte, error) {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	// Find encoder
	codec := libav.AvcodecFindEncoder(codecID)
	if codec == nil {
		return nil, fmt.Errorf("unable to find encoder for codec ID %d", codecID)
	}

	// Allocate codec context
	ctx := libav.AvcodecAllocContext3(codec)
	if ctx == nil {
		return nil, fmt.Errorf("unable to allocate codec context")
	}
	defer libav.AvcodecFreeContext(ctx)

	// Set codec parameters
	ctx.SetPixFmt(codec.GetPixFmts()[0])
	ctx.SetTimeBase(1, 1)
	ctx.SetSampleAspectRatio(1, 1)
	ctx.SetWidth(width)
	ctx.SetHeight(height)

	// Open encoder
	if err := libav.AvcodecOpen2Encoder(ctx, codec, nil); err != nil {
		return nil, fmt.Errorf("unable to open encoder: %w", err)
	}
	defer libav.AvcodecClose(ctx)

	// Allocate output frame
	oframe := libav.AvFrameAlloc()
	if oframe == nil {
		return nil, fmt.Errorf("unable to allocate frame")
	}
	defer libav.AvFrameFree(oframe)

	// Allocate image data for output format
	if err := libav.AvImageAlloc(oframe, width, height, ctx.GetPixFmt(), 1); err != nil {
		return nil, fmt.Errorf("unable to allocate image: %w", err)
	}
	// Convert Go image to RGB32 format for sws_scale
	rgba := image.NewRGBA(bounds)
	draw.Draw(rgba, bounds, img, bounds.Min, draw.Src)

	// Setup source data pointer and stride
	srcPtr := unsafe.Pointer(&rgba.Pix[0])
	srcStride := rgba.Stride

	// Handle vertical flip if needed
	if h.needsVerticalFlip(img) {
		srcPtr = unsafe.Pointer(&rgba.Pix[rgba.Stride*(height-1)])
		srcStride = -rgba.Stride
	}

	// Setup sws context for color space conversion (RGB32 → codec format)
	sws := libav.SwsGetContext(width, height, AVPixFmtRGBA,
		width, height, ctx.GetPixFmt(), SwsBilinear)
	if sws == nil {
		return nil, fmt.Errorf("unable to create sws context")
	}
	defer libav.SwsFreeContext(sws)

	// Perform color space conversion using C AVFrame data/linesize pointers
	// SwsScaleSingleSrc constructs the srcSlice[4] array in C memory,
	// avoiding the cgo violation of passing a Go array of Go pointers.
	libav.SwsScaleSingleSrc(sws, srcPtr, srcStride,
		0, height, oframe.GetCDataPtrs(), oframe.GetCLinesizePtrs())

	// Encode frame using FFmpeg 7+ API
	oframe.SetPts(libav.AVNoPTSValue)

	// Send frame to encoder
	if err := libav.AvcodecSendFrame(ctx, oframe); err != nil {
		return nil, fmt.Errorf("avcodec_send_frame failed: %w", err)
	}

	// Receive packet from encoder
	pkt := libav.AvPacketAlloc()
	if pkt == nil {
		return nil, fmt.Errorf("unable to allocate packet")
	}
	defer libav.AvPacketFree(pkt)

	if err := libav.AvcodecReceivePacket(ctx, pkt); err != nil {
		return nil, fmt.Errorf("avcodec_receive_packet failed: %w", err)
	}

	// Return encoded data
	data := pkt.GetData()
	return data, nil
}

// PixmapWithFlags is an interface for images that have pixmap flags
type PixmapWithFlags interface {
	image.Image
	PixmapFlags() int
}

// Pixmap flags
const (
	PixmapVflip = 1 << 0
)

// needsVerticalFlip checks if the image needs vertical flip
func (h *ScreenshotHandler) needsVerticalFlip(img image.Image) bool {
	// Check if image implements PixmapWithFlags interface
	if pm, ok := img.(PixmapWithFlags); ok {
		return pm.PixmapFlags()&PixmapVflip != 0
	}
	return false
}

// response sends a screenshot response
// This is the Go equivalent of screenshot_response in C
//
// F-RACE-1 fix: accepts the HTTP connection as a parameter (captured
// atomically in process()) instead of reading h.conn. This prevents
// a cross-request race where process() from request A could send a
// response on request B's connection.
// h.conn/h.active cleanup is handled by process()'s defer.
func (h *ScreenshotHandler) response(hc *httpnet.HTTPConnection, url, errmsg string) {
	if hc == nil {
		return
	}

	if url != "" {
		hc.HTTPRedirect(url)
	} else {
		msg := errmsg
		if msg == "" {
			msg = "Error not specified"
		}
		hc.HTTPSendReply(500, "text/plain", "", "", 0, []byte(msg+"\n"))
	}
}

// saveToFile saves the image data to a file
func (h *ScreenshotHandler) saveToFile(data []byte, path string) error {
	return os.WriteFile(path, data, 0644)
}

// uploadToImgur uploads the image to Imgur
func (h *ScreenshotHandler) uploadToImgur(data []byte) (string, error) {
	// Encode image as base64
	encoded := base64.StdEncoding.EncodeToString(data)

	// Build form data
	formData := url.Values{}
	formData.Set("image", encoded)

	// Create HTTP request
	clientID := h.imgurID
	if clientID == "" {
	}

	req, err := http.NewRequest("POST", "https://api.imgur.com/3/upload", bytes.NewReader([]byte(formData.Encode())))
	if err != nil {
		return "", err
	}

	req.Header.Set("Authorization", "Client-ID "+clientID)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	// Parse JSON response
	var imgurResp ImgurResponse
	if err := json.Unmarshal(body, &imgurResp); err != nil {
		return "", fmt.Errorf("unable to parse imgur response: %w", err)
	}

	if !imgurResp.Success {
		if imgurResp.Data.Error != "" {
			return "", fmt.Errorf("imgur error: %s", imgurResp.Data.Error)
		}
		return "", fmt.Errorf("unknown imgur error")
	}

	if imgurResp.Data.Link == "" {
		return "", fmt.Errorf("no link in imgur response")
	}

	return imgurResp.Data.Link, nil
}

// Register registers the screenshot handler with the HTTP server
// This is the Go equivalent of screenshot_init in C
func (h *ScreenshotHandler) Register(server *httpnet.HTTPServer) {
	if server == nil {
		return
	}
	server.HTTPPathAdd("/api/screenshot", h, h.Screenshot, false)
}
