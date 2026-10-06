package video

import "sync/atomic"

// VAAPI-EGL zero-copy capability — extension, no C counterpart.
// The GL backend (UI thread, where the EGL context is current) probes
// eglGetCurrentDisplay + EGL_EXT_image_dma_buf_import once at window
// open and publishes the result here; the libav deliver path (media
// thread) reads it to decide between exporting the VAAPI surface as a
// dmabuf (zero-copy) and av_hwframe_transfer_data (GPU→CPU copy).
//
// VAAPI-EGL render node — extension, no C counterpart. The GL backend
// also publishes the DRM render node backing its EGLDisplay
// (EGL_EXT_device_query → EGL_DRM_RENDER_NODE_FILE_EXT) so the libav
// hw device is created on the SAME GPU the renderer uses: dmabuf
// import across different GPUs is unreliable, so picking the EGL node
// first is what makes the zero-copy path deterministic on multi-GPU
// systems. Empty string when unknown (GLX context, old Mesa, non-DRM
// EGL) — the decoder then falls back to probing all nodes.

// VaapiEGLCaps holds the session-scoped VAAPI/EGL capability state.
// Extension, no C counterpart — the GL backend (UI thread, where the
// EGL context is current) probes eglGetCurrentDisplay +
// EGL_EXT_image_dma_buf_import once at window open and publishes the
// result here; the libav deliver path (media thread) reads it to
// decide between dmabuf export and av_hwframe_transfer_data.
// Lives as a value field on VideoSettings — writer (glw) and reader
// (libav) meet at the injected session object.
type VaapiEGLCaps struct {
	zeroCopy   atomic.Bool
	renderNode atomic.Pointer[string]
}

// ZeroCopy reports whether the current GL context can import dma-bufs
// as EGLImages (VAAPI zero-copy render path).
func (c *VaapiEGLCaps) ZeroCopy() bool { return c.zeroCopy.Load() }

// SetZeroCopy is called by the GL backend after its EGL probe.
func (c *VaapiEGLCaps) SetZeroCopy(ok bool) { c.zeroCopy.Store(ok) }

// RenderNode returns the DRM render node path (e.g.
// "/dev/dri/renderD129") backing the current EGL display, or "".
func (c *VaapiEGLCaps) RenderNode() string {
	if v := c.renderNode.Load(); v != nil {
		return *v
	}
	return ""
}

// SetRenderNode is called by the GL backend after its EGL device
// query; "" means "no information — probe all nodes".
func (c *VaapiEGLCaps) SetRenderNode(node string) {
	c.renderNode.Store(&node)
}
