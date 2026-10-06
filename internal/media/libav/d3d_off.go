//go:build !(windows && cgo)

package libav

import (
	"unsafe"

	"github.com/czz/movian-go/internal/libav"
)

// D3D11VA/DXVA2 stubs — windows-only hwaccels; keep the shared codec
// bind call sites compiling elsewhere. Unreachable: the settings stay
// 0 off windows (not registered).
func D3d11vaDevice() unsafe.Pointer { return nil }

func MlD3d11vaBindCodec(ctx *libav.AVCodecContext, devref unsafe.Pointer) int { return -1 }

func Dxva2Device() unsafe.Pointer { return nil }

func MlDxva2BindCodec(ctx *libav.AVCodecContext, devref unsafe.Pointer) int { return -1 }
