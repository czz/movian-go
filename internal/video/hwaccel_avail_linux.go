//go:build linux && !android && !rpi && cgo

package video

// VaapiAvailable — VAAPI extension (no C counterpart): true on
// linux && cgo where the bundled FFmpeg is built --enable-vaapi and
// the code can reach libva through cgo. Independent of the x11 tag —
// VAAPI uses DRM render nodes, not X11.
const VaapiAvailable = true

// NvdecAvailable — NVDEC extension (no C counterpart): true on
// linux && cgo where the bundled FFmpeg is built --enable-nvdec and
// libcuda/libnvcuvid are dlopen'ed by FFmpeg at runtime.
const NvdecAvailable = true
