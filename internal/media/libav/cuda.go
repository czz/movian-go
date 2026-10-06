//go:build linux && !android && !rpi && cgo

package libav

/*
#cgo linux,!android LDFLAGS: -ldl
#include <string.h>
#include <stdio.h>
#include <stdlib.h>
#include <stdint.h>
#include <dlfcn.h>
#include <libavcodec/avcodec.h>
#include <libavutil/hwcontext.h>
#include <libavutil/pixdesc.h>

// ml_cuda_get_format — same shape as ml_vaapi_get_format (vaapi.go):
// pick AV_PIX_FMT_CUDA when FFmpeg offers it, else the software
// default.
static enum AVPixelFormat
ml_cuda_get_format(struct AVCodecContext *ctx, const enum AVPixelFormat *fmt)
{
  const enum AVPixelFormat *p;

  if(getenv("MOVIANGO_NVDEC_DEBUG")) {
    fprintf(stderr, "[NVDEC] get_format offered:");
    for(p = fmt; *p != AV_PIX_FMT_NONE; p++)
      fprintf(stderr, " %d", *p);
    fprintf(stderr, " (hw_device_ctx=%p)\n", ctx->hw_device_ctx);
  }
  for(p = fmt; *p != AV_PIX_FMT_NONE; p++) {
    if(*p == AV_PIX_FMT_CUDA)
      return AV_PIX_FMT_CUDA;
  }
  return avcodec_default_get_format(ctx, fmt);
}

// ml_cuda_bind_codec — the CUDA/NVDEC counterpart of
// ml_vaapi_bind_codec. devref is an AVBufferRef holding an
// AV_HWDEVICE_TYPE_CUDA device (created by ml_cuda_device_create).
static int
ml_cuda_bind_codec(struct AVCodecContext *ctx, AVBufferRef *devref)
{
  if(ctx->hw_device_ctx == NULL) {
    ctx->hw_device_ctx = av_buffer_ref(devref);
    if(ctx->hw_device_ctx == NULL)
      return AVERROR(ENOMEM);
  }
  ctx->get_format = ml_cuda_get_format;
  return 0;
}

// ml_cuda_device_create — av_hwdevice_ctx_create(CUDA) on the default
// NVIDIA device. FFmpeg loads libcuda.so/libnvcuvid.so via dlopen;
// NULL device string = device 0. Returns an AVBufferRef* or NULL.
// name out-buffer receives the device name ("NVIDIA GeForce ...")
// via the driver API — libcuda is already loaded by hwcontext_cuda,
// so the extra dlopen just refs the same library.
static void *
ml_cuda_device_create(char *name, int namelen)
{
  AVBufferRef *ref = NULL;
  void *h;
  int (*devget)(int *, int);
  int (*getname)(char *, int, int);
  int dev = 0;

  name[0] = '\0';
  if(av_hwdevice_ctx_create(&ref, AV_HWDEVICE_TYPE_CUDA,
                           NULL, NULL, 0) != 0)
    return NULL;

  h = dlopen("libcuda.so.1", RTLD_LAZY | RTLD_GLOBAL);
  if(h != NULL) {
    devget = (int (*)(int *, int))dlsym(h, "cuDeviceGet");
    getname = (int (*)(char *, int, int))dlsym(h, "cuDeviceGetName");
    if(devget != NULL && getname != NULL && devget(&dev, 0) == 0)
      getname(name, namelen, dev);
  }
  return ref;
}
*/
import "C"

import (
	"os"
	"unsafe"

	"github.com/czz/movian-go/internal/libav"

	"github.com/czz/movian-go/internal/video"
)

// NvdecHwPresent — cheap probe for the settings UI: true when an
// NVIDIA device node exists (driver loaded). Does not touch libcuda —
// real availability is decided by CudaDevice().
func NvdecHwPresent() bool {
	if _, err := os.Stat("/dev/nvidiactl"); err == nil {
		return true
	}
	_, err := os.Stat("/dev/nvidia0")
	return err == nil
}

// CudaDevice returns the session's CUDA hw device AVBufferRef* for
// NVDEC decode (was the process-global cudaDev/cudaOnce — now cached
// on the injected VideoSettings), or nil when CUDA is unavailable
// (no NVIDIA GPU/driver, libcuda missing).
func CudaDevice(vs *video.VideoSettings) unsafe.Pointer {
	if vs == nil {
		return nil
	}
	vs.CudaDevOnce.Do(func() {
		var name [128]C.char
		vs.CudaDev = C.ml_cuda_device_create(&name[0], C.int(len(name)))
		if vs.CudaDev != nil {
			lavTS.Info("LAVC", "NVDEC device: %s", C.GoString(&name[0]))
		}
	})
	return vs.CudaDev
}

// MlCudaBindCodec — counterpart of MlVaapiBindCodec for NVDEC.
// devref is the AVBufferRef* from CudaDevice. Returns 0 on success.
func MlCudaBindCodec(ctx *libav.AVCodecContext, devref unsafe.Pointer) int {
	return int(C.ml_cuda_bind_codec((*C.AVCodecContext)(ctx.CPtr()),
		(*C.AVBufferRef)(devref)))
}
