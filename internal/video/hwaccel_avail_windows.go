//go:build windows && cgo

package video

// D3D11VA / DXVA2 — windows hwaccel availability (no C counterpart:
// upstream never shipped a win32 build). FFmpeg's hwcontext_d3d11va /
// hwcontext_dxva2 create the device internally. Requires cgo like the
// linux side — the FFmpeg bindings are cgo.
const (
	D3d11vaAvailable = true
	Dxva2Available   = true
)
