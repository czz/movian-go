//go:build !(linux && !android && !rpi && cgo) && !windows

package libav

import (
	"unsafe"

	"github.com/czz/movian-go/internal/libav"
	"github.com/czz/movian-go/internal/video"
)

// VAAPI/CUDA are desktop-Linux only — Android's hardware decode path
// is MediaCodec (android_video_codec.c). These stubs keep the codec
// bind/transfer call sites compiling; they are unreachable on
// platforms where no hw device context can be created.

func VaapiDevice(vs *video.VideoSettings) unsafe.Pointer { return nil }

func MlVaapiBindCodec(ctx *libav.AVCodecContext, devref unsafe.Pointer) int { return -1 }

func CudaDevice(vs *video.VideoSettings) unsafe.Pointer { return nil }

func MlCudaBindCodec(ctx *libav.AVCodecContext, devref unsafe.Pointer) int { return -1 }

func HwframeTransfer(dst, src *libav.AVFrame) int { return -1 }

func VaapiHwPresent() bool { return false }

func NvdecHwPresent() bool { return false }

func NvidiaRenderActive(vs *video.VideoSettings) bool { return false }
