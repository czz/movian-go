package libav

import (
	"runtime"

	"github.com/czz/movian-go/internal/video"
)

// ProbeHwAccel — eagerly creates the hardware decode device at
// startup so the boot log reports which GPU and backend will be
// used, instead of waiting for the first codec bind. The device
// functions are the same sync.Once singletons the decoder binds
// later — probing only moves the cost earlier, it is not duplicated.
// Order mirrors the bind chain in cgo.go: first available backend
// wins; on darwin VideoToolbox is codec-registered (no device ctx).
func ProbeHwAccel(vs *video.VideoSettings) {
	if vs == nil {
		// Unwired port reads zero settings — no hw accel.
		vs = &video.VideoSettings{}
	}
	vs.HwProbeOnce.Do(func() {
		// Same order as the bind chain: NVDEC first when the
		// renderer is NVIDIA (PRIME offload) — cross-GPU VAAPI is
		// the fallback, not the other way around.
		nvdecFirst := NvidiaRenderActive(vs)
		if nvdecFirst && vs.Nvdec != 0 && CudaDevice(vs) != nil {
			lavTS.Info("LAVC", "Hardware video decode: NVDEC")
			return
		}
		if vs.Vaapi != 0 && VaapiDevice(vs) != nil {
			lavTS.Info("LAVC", "Hardware video decode: VAAPI")
			return
		}
		if !nvdecFirst && vs.Nvdec != 0 && CudaDevice(vs) != nil {
			lavTS.Info("LAVC", "Hardware video decode: NVDEC")
			return
		}
		if vs.D3d11va != 0 && D3d11vaDevice() != nil {
			lavTS.Info("LAVC", "Hardware video decode: D3D11VA")
			return
		}
		if vs.Dxva2 != 0 && Dxva2Device() != nil {
			lavTS.Info("LAVC", "Hardware video decode: DXVA2")
			return
		}
		if runtime.GOOS == "darwin" && vs.VideoAccel != 0 {
			lavTS.Info("LAVC", "Hardware video decode: VideoToolbox")
			return
		}
		lavTS.Info("LAVC", "Hardware video decode: none (software)")
	})
}
