//go:build windows && cgo

package libav

// D3D11VA / DXVA2 hardware decode — windows counterpart of vaapi.go.
// NO upstream C counterpart (upstream never shipped win32; FFmpeg
// supplies hwcontext_d3d11va / hwcontext_dxva2 which create the D3D
// device internally — av_hwdevice_ctx_create with a NULL device).
// Decoded frames are always pulled to software frames via
// av_hwframe_transfer_data (NV12) — the GL renderer has no
// D3D-surface interop path, same as the NVDEC path on linux.

/*
#define COBJMACROS // before any header pulls in d3d11.h/dxgi.h
#include <string.h>
#include <stdio.h>
#include <stdlib.h>
#include <stdint.h>
#include <libavcodec/avcodec.h>
#include <libavutil/hwcontext.h>
#include <libavutil/hwcontext_d3d11va.h>
#include <d3d11.h>
#include <dxgi.h>

// IID_IDXGIDevice defined locally — mingw libuuid lacks it (same
// pattern as the WASAPI GUIDs in audio/core/wasapi_windows.go).
static const GUID ml_IID_IDXGIDevice =
  {0x54ec77fa,0x1377,0x44e6,{0x8c,0x32,0x88,0xfd,0x5f,0x44,0xc8,0x4c}};

// ml_d3d11va_get_format — same shape as ml_vaapi_get_format
// (vaapi.go): pick AV_PIX_FMT_D3D11 when offered, else software.
static enum AVPixelFormat
ml_d3d11va_get_format(struct AVCodecContext *ctx,
                      const enum AVPixelFormat *fmt)
{
  const enum AVPixelFormat *p;

  if(getenv("MOVIANGO_D3D_DEBUG")) {
    fprintf(stderr, "[D3D11VA] get_format offered:");
    for(p = fmt; *p != AV_PIX_FMT_NONE; p++)
      fprintf(stderr, " %d", *p);
    fprintf(stderr, " (hw_device_ctx=%p)\n", ctx->hw_device_ctx);
  }
  for(p = fmt; *p != AV_PIX_FMT_NONE; p++) {
    if(*p == AV_PIX_FMT_D3D11)
      return AV_PIX_FMT_D3D11;
  }
  return avcodec_default_get_format(ctx, fmt);
}

// ml_dxva2_get_format — DXVA2_VLD variant.
static enum AVPixelFormat
ml_dxva2_get_format(struct AVCodecContext *ctx,
                    const enum AVPixelFormat *fmt)
{
  const enum AVPixelFormat *p;

  if(getenv("MOVIANGO_D3D_DEBUG")) {
    fprintf(stderr, "[DXVA2] get_format offered:");
    for(p = fmt; *p != AV_PIX_FMT_NONE; p++)
      fprintf(stderr, " %d", *p);
    fprintf(stderr, " (hw_device_ctx=%p)\n", ctx->hw_device_ctx);
  }
  for(p = fmt; *p != AV_PIX_FMT_NONE; p++) {
    if(*p == AV_PIX_FMT_DXVA2_VLD)
      return AV_PIX_FMT_DXVA2_VLD;
  }
  return avcodec_default_get_format(ctx, fmt);
}

// ml_d3d11va_bind_codec / ml_dxva2_bind_codec — counterparts of
// ml_vaapi_bind_codec: ref the hw device and install get_format.
// FFmpeg derives hw_frames_ctx and the surface pool itself.
static int
ml_d3d11va_bind_codec(struct AVCodecContext *ctx, AVBufferRef *devref)
{
  if(ctx->hw_device_ctx == NULL) {
    ctx->hw_device_ctx = av_buffer_ref(devref);
    if(ctx->hw_device_ctx == NULL)
      return AVERROR(ENOMEM);
  }
  ctx->get_format = ml_d3d11va_get_format;
  return 0;
}

static int
ml_dxva2_bind_codec(struct AVCodecContext *ctx, AVBufferRef *devref)
{
  if(ctx->hw_device_ctx == NULL) {
    ctx->hw_device_ctx = av_buffer_ref(devref);
    if(ctx->hw_device_ctx == NULL)
      return AVERROR(ENOMEM);
  }
  ctx->get_format = ml_dxva2_get_format;
  return 0;
}

// ml_d3d11va_device_create / ml_dxva2_device_create —
// av_hwdevice_ctx_create with a NULL device: hwcontext_d3d11va
// creates a D3D11 device on the default adapter; hwcontext_dxva2
// creates a D3D9 device + Direct3D9Ex device manager. Returns an
// AVBufferRef* or NULL.
static void *
ml_d3d11va_device_create(char *name, int namelen)
{
  AVBufferRef *ref = NULL;
  AVHWDeviceContext *dc;
  AVD3D11VADeviceContext *d3d;
  IDXGIDevice *dxgi = NULL;
  IDXGIAdapter *ad = NULL;
  DXGI_ADAPTER_DESC desc;
  int i, n = 0;

  name[0] = '\0';
  if(av_hwdevice_ctx_create(&ref, AV_HWDEVICE_TYPE_D3D11VA,
                           NULL, NULL, 0) != 0)
    return NULL;

  // DXGI adapter description → GPU name for logging.
  dc = (AVHWDeviceContext *)ref->data;
  d3d = (AVD3D11VADeviceContext *)dc->hwctx;
  if(d3d->device != NULL &&
     ID3D11Device_QueryInterface(d3d->device, &ml_IID_IDXGIDevice,
                                 (void **)&dxgi) == S_OK) {
    if(IDXGIDevice_GetAdapter(dxgi, &ad) == S_OK) {
      if(IDXGIAdapter_GetDesc(ad, &desc) == S_OK) {
        char tmp[128];
        for(i = 0; i < 127 && desc.Description[i] != 0; i++)
          tmp[n++] = (char)(desc.Description[i] & 0x7f);
        tmp[n] = '\0';
        snprintf(name, namelen, "%s", tmp);
      }
      IDXGIAdapter_Release(ad);
    }
    IDXGIDevice_Release(dxgi);
  }
  return ref;
}

static void *
ml_dxva2_device_create(void)
{
  AVBufferRef *ref = NULL;
  if(av_hwdevice_ctx_create(&ref, AV_HWDEVICE_TYPE_DXVA2,
                           NULL, NULL, 0) == 0)
    return ref;
  return NULL;
}

// ml_hwframe_transfer — av_hwframe_transfer_data(dst, src): pulls a
// hardware frame into a software AVFrame (NV12 for D3D11VA/DXVA2).
static int
ml_hwframe_transfer(void *dst, void *src)
{
  return av_hwframe_transfer_data((AVFrame *)dst, (AVFrame *)src, 0);
}
*/
import "C"

import (
	"sync"
	"unsafe"

	"github.com/czz/movian-go/internal/libav"

	"github.com/czz/movian-go/internal/video"
)

var (
	d3d11vaOnce sync.Once
	d3d11vaDev  unsafe.Pointer // AVBufferRef*
	dxva2Once   sync.Once
	dxva2Dev    unsafe.Pointer // AVBufferRef*
)

// D3d11vaDevice returns the shared AVBufferRef* for the D3D11VA
// device, or nil when unusable (no D3D11 driver, WARP-only, etc.).
func D3d11vaDevice() unsafe.Pointer {
	d3d11vaOnce.Do(func() {
		var name [128]C.char
		d3d11vaDev = C.ml_d3d11va_device_create(&name[0], C.int(len(name)))
		if d3d11vaDev != nil {
			lavTS.Info("LAVC", "D3D11VA device: %s", C.GoString(&name[0]))
		}
	})
	return d3d11vaDev
}

// Dxva2Device returns the shared AVBufferRef* for the DXVA2 device,
// or nil when unusable.
func Dxva2Device() unsafe.Pointer {
	dxva2Once.Do(func() {
		dxva2Dev = C.ml_dxva2_device_create()
		if dxva2Dev != nil {
			lavTS.Info("LAVC", "DXVA2 device created (D3D9 default adapter)")
		}
	})
	return dxva2Dev
}

// MlD3d11vaBindCodec — counterpart of MlVaapiBindCodec for D3D11VA.
// Returns 0 on success.
func MlD3d11vaBindCodec(ctx *libav.AVCodecContext, devref unsafe.Pointer) int {
	return int(C.ml_d3d11va_bind_codec((*C.AVCodecContext)(ctx.CPtr()),
		(*C.AVBufferRef)(devref)))
}

// MlDxva2BindCodec — DXVA2 variant.
func MlDxva2BindCodec(ctx *libav.AVCodecContext, devref unsafe.Pointer) int {
	return int(C.ml_dxva2_bind_codec((*C.AVCodecContext)(ctx.CPtr()),
		(*C.AVBufferRef)(devref)))
}

// HwframeTransfer wraps av_hwframe_transfer_data — pulls a hw frame
// (AV_PIX_FMT_D3D11 / AV_PIX_FMT_DXVA2_VLD) into the software frame
// dst. Real implementation on windows (the off-stub returns -1).
func HwframeTransfer(dst, src *libav.AVFrame) int {
	return int(C.ml_hwframe_transfer(dst.CPtr(), src.CPtr()))
}

// VAAPI/CUDA stubs — linux-only APIs; keep the shared call sites in
// cgo.go compiling. Unreachable: mediacore.VideoSettings().Vaapi/Nvdec() stay 0
// on windows (no settings registered).
func VaapiDevice(vs *video.VideoSettings) unsafe.Pointer { return nil }

func MlVaapiBindCodec(ctx *libav.AVCodecContext, devref unsafe.Pointer) int { return -1 }

func CudaDevice(vs *video.VideoSettings) unsafe.Pointer { return nil }

func MlCudaBindCodec(ctx *libav.AVCodecContext, devref unsafe.Pointer) int { return -1 }

func VaapiHwPresent() bool { return false }

func NvdecHwPresent() bool { return false }

func NvidiaRenderActive(vs *video.VideoSettings) bool { return false }
