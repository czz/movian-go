package glw

import (
	decoderpkg "github.com/czz/movian-go/internal/video/decoder"
)

// VideoRenderer is the seam between glw's video engine and the
// platform GPU/display backend. Today the per-platform files
// (glw_video_opengl.go, _android, _rpi, _sunxi, _vaapi) implement the
// same operations under build tags with per-platform func names
// (androidStart/ovdStart/...); this interface names the contract so a
// backend — including a future pure-Go one — conforms to one shape.
//
// contract: the engine calls Start once per GlwVideo instance on its
// own thread; Newframe per decoded frame (may return a PTS/duration);
// Render inside the frame draw; Reset/Reap on teardown. All calls
// happen on the GLW render thread — implementations must not block it.
type VideoRenderer interface {
	// Start — bind the renderer to a video instance. Returns 0 on ok.
	Start(gv *GlwVideo) int

	// Newframe — deliver/consume a decoded frame. Returns the frame's
	// presentation duration in microseconds (0 when unknown).
	Newframe(gv *GlwVideo, vd *decoderpkg.VideoDecoder, flags int) int64

	// Render — draw the current frame for this render pass.
	Render(gv *GlwVideo, rc *glwRctx)

	// Reset — drop all renderer surfaces/pending frames.
	Reset(gv *GlwVideo)
}

// *glwVideoEngine adapts to VideoRenderer by delegating to its own
// C-vtable func fields — the engines (glw_video_opengl/android/rpi/
// sunxi/vaapi) already register as gve* tables; these methods make the
// vtable conform to the Go-shaped seam without touching call sites.
func (gve *glwVideoEngine) Start(gv *GlwVideo) int {
	if gve.gveStart == nil {
		return -1
	}
	return gve.gveStart(gv)
}

func (gve *glwVideoEngine) Newframe(gv *GlwVideo,
	vd *decoderpkg.VideoDecoder, flags int) int64 {
	if gve.gveNewframe == nil {
		return 0
	}
	return gve.gveNewframe(gv, vd, flags)
}

func (gve *glwVideoEngine) Render(gv *GlwVideo, rc *glwRctx) {
	if gve.gveRender != nil {
		gve.gveRender(gv, rc)
	}
}

func (gve *glwVideoEngine) Reset(gv *GlwVideo) {
	if gve.gveReset != nil {
		gve.gveReset(gv)
	}
}

// compile-time seam proof: the engine vtable is a VideoRenderer.
var _ VideoRenderer = (*glwVideoEngine)(nil)
