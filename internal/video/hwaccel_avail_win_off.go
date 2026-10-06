//go:build !(windows && cgo)

package video

// No D3D11VA/DXVA2 without windows && cgo.
const (
	D3d11vaAvailable = false
	Dxva2Available   = false
)
