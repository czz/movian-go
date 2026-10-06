//go:build linux && !android && !rpi && cgo

package libav

/*
#cgo linux,!android CFLAGS: -I${SRCDIR}/../../../third_party/libva/usr/include
#include <string.h>
#include <stdio.h>
#include <stdlib.h>
#include <stdint.h>
#include <unistd.h>
#include <libavcodec/avcodec.h>
#include <libavutil/hwcontext.h>
#include <libavutil/hwcontext_vaapi.h>
#include <libavutil/pixdesc.h>
#include <va/va.h>
#include <va/va_drmcommon.h>

// ml_vaapi_get_format — same shape as ml_vdpau_get_format
// (vdpau.go): FFmpeg offers the codec's supported pix_fmts; pick
// AV_PIX_FMT_VAAPI when present, else the software default.
static enum AVPixelFormat
ml_vaapi_get_format(struct AVCodecContext *ctx, const enum AVPixelFormat *fmt)
{
  const enum AVPixelFormat *p;

  if(getenv("MOVIANGO_VAAPI_DEBUG")) {
    fprintf(stderr, "[VAAPI] get_format offered:");
    for(p = fmt; *p != AV_PIX_FMT_NONE; p++)
      fprintf(stderr, " %d", *p);
    fprintf(stderr, " (hw_device_ctx=%p)\n", ctx->hw_device_ctx);
  }
  for(p = fmt; *p != AV_PIX_FMT_NONE; p++) {
    if(*p == AV_PIX_FMT_VAAPI)
      return AV_PIX_FMT_VAAPI;
  }
  return avcodec_default_get_format(ctx, fmt);
}

// ml_vaapi_bind_codec — the VAAPI counterpart of ml_vdpau_bind_codec.
// devref is an AVBufferRef holding an AV_HWDEVICE_TYPE_VAAPI device
// (created by ml_vaapi_device_create). FFmpeg derives hw_frames_ctx
// and the surface pool itself (ff_decode_get_hw_frames_ctx), same as
// the VDPAU path.
static int
ml_vaapi_bind_codec(struct AVCodecContext *ctx, AVBufferRef *devref)
{
  if(ctx->hw_device_ctx == NULL) {
    ctx->hw_device_ctx = av_buffer_ref(devref);
    if(ctx->hw_device_ctx == NULL)
      return AVERROR(ENOMEM);
  }
  ctx->get_format = ml_vaapi_get_format;
  return 0;
}

// ml_vaapi_node_usable — the proprietary NVIDIA VA-API driver
// (nvidia_drv_video.so) is a VDPAU bridge that requires an X11
// Display; opening an nvidia-driven DRM node headless (render node,
// Wayland, PRIME offload) segfaults inside vaInitialize →
// XDisplayString(NULL). NVIDIA decode uses NVDEC instead. Returns 0
// for nodes whose kernel driver is "nvidia", 1 otherwise (unknown
// driver → let vaInitialize decide).
static int
ml_vaapi_node_usable(const char *path)
{
  const char *base = strrchr(path, '/');
  const char *drv;
  char sys[256], link[256];
  ssize_t n;

  base = base != NULL ? base + 1 : path;
  snprintf(sys, sizeof(sys), "/sys/class/drm/%s/device/driver", base);
  n = readlink(sys, link, sizeof(link) - 1);
  if(n <= 0)
    return 1;
  link[n] = '\0';
  drv = strrchr(link, '/');
  drv = drv != NULL ? drv + 1 : link;
  return strcmp(drv, "nvidia") != 0;
}

// ml_vaapi_device_create — av_hwdevice_ctx_create(VAAPI). The
// preferred render node (the one backing the EGL display, published
// by the GL backend) is tried first so decode happens on the same GPU
// that renders — required for the dmabuf zero-copy path. Without a
// hint (GLX, old Mesa, headless) fall back to probing all DRM render
// nodes (display-server independent: works on Wayland, X11 and
// headless). Returns an AVBufferRef* or NULL.
static void *
ml_vaapi_device_create(const char *preferred, char *usedpath,
                       int usedpathlen, char *vendor, int vendorlen)
{
  AVBufferRef *ref = NULL;
  AVHWDeviceContext *dc;
  AVVAAPIDeviceContext *vdc;
  const char *v;
  char path[64];
  int i;

  usedpath[0] = vendor[0] = '\0';

  if(preferred != NULL && preferred[0] != '\0' &&
     ml_vaapi_node_usable(preferred) &&
     av_hwdevice_ctx_create(&ref, AV_HWDEVICE_TYPE_VAAPI,
                            preferred, NULL, 0) == 0) {
    snprintf(usedpath, usedpathlen, "%s", preferred);
    goto ok;
  }

  for(i = 128; i < 144; i++) {
    snprintf(path, sizeof(path), "/dev/dri/renderD%d", i);
    if(ml_vaapi_node_usable(path) &&
       av_hwdevice_ctx_create(&ref, AV_HWDEVICE_TYPE_VAAPI,
                              path, NULL, 0) == 0) {
      snprintf(usedpath, usedpathlen, "%s", path);
      goto ok;
    }
  }
  for(i = 0; i < 16; i++) {
    snprintf(path, sizeof(path), "/dev/dri/card%d", i);
    if(ml_vaapi_node_usable(path) &&
       av_hwdevice_ctx_create(&ref, AV_HWDEVICE_TYPE_VAAPI,
                              path, NULL, 0) == 0) {
      snprintf(usedpath, usedpathlen, "%s", path);
      goto ok;
    }
  }
  return NULL;

ok:
  dc = (AVHWDeviceContext *)ref->data;
  vdc = (AVVAAPIDeviceContext *)dc->hwctx;
  v = vaQueryVendorString(vdc->display);
  if(v != NULL)
    snprintf(vendor, vendorlen, "%s", v);
  return ref;
}

// ml_hwframe_transfer — av_hwframe_transfer_data(dst, src): pulls a
// hardware frame into a software AVFrame (NV12 for VAAPI).
static int
ml_hwframe_transfer(void *dst, void *src)
{
  return av_hwframe_transfer_data((AVFrame *)dst, (AVFrame *)src, 0);
}

// ml_avlog_verbose — raises the av_log level so hwaccel errors are
// visible while debugging (MOVIANGO_VAAPI_DEBUG).
static void
ml_avlog_verbose(void)
{
  av_log_set_level(AV_LOG_VERBOSE);
}

// ml_dmabuf_layer / ml_dmabuf_desc — flat, Go-readable copy of
// VADRMPRIMESurfaceDescriptor (vaExportSurfaceHandle with
// VA_SURFACE_ATTRIB_MEM_TYPE_DRM_PRIME_2). Max 4 objects/layers —
// NV12 uses 1 object, 2 layers, 1 plane each.
typedef struct {
  uint32_t fourcc;
  int      num_planes;
  uint32_t object_index[4];
  uint32_t offset[4];
  uint32_t pitch[4];
} ml_dmabuf_layer;

typedef struct {
  uint32_t fourcc;
  int      width, height;
  int      num_objects;
  int      num_layers;
  int      fd[4];
  uint64_t modifier[4];
  ml_dmabuf_layer layer[4];
} ml_dmabuf_desc;

// ml_vaapi_export — export the VASurfaceID in frame->data[3] as
// separate-layer DMA-BUFs for EGL_EXT_image_dma_buf_import. Returns 0
// on success; the caller owns the fds in out->fd.
static int
ml_vaapi_export(void *avframe, ml_dmabuf_desc *out)
{
  AVFrame *f = (AVFrame *)avframe;
  AVHWFramesContext *fc;
  AVVAAPIDeviceContext *vdc;
  VASurfaceID surf;
  VADRMPRIMESurfaceDescriptor d;
  VAStatus st;
  int i, j;

  if(f == NULL || f->hw_frames_ctx == NULL)
    return -1;
  fc = (AVHWFramesContext *)f->hw_frames_ctx->data;
  if(fc == NULL || fc->device_ctx == NULL)
    return -1;
  vdc = (AVVAAPIDeviceContext *)fc->device_ctx->hwctx;
  surf = (VASurfaceID)(uintptr_t)f->data[3];

  memset(&d, 0, sizeof(d));
  st = vaExportSurfaceHandle(vdc->display, surf,
      VA_SURFACE_ATTRIB_MEM_TYPE_DRM_PRIME_2,
      VA_EXPORT_SURFACE_READ_ONLY | VA_EXPORT_SURFACE_COMPOSED_LAYERS,
      &d);
  if(st != VA_STATUS_SUCCESS)
    return (int)st;

  memset(out, 0, sizeof(*out));
  out->fourcc = d.fourcc;
  out->width = d.width;
  out->height = d.height;
  out->num_objects = d.num_objects;
  out->num_layers = d.num_layers;

  if(getenv("MOVIANGO_VAAPI_DEBUG")) {
    fprintf(stderr, "[VAAE] export: objects=%d layers=%d %dx%d "
            "fourcc=%08x\n",
            d.num_objects, d.num_layers, d.width, d.height, d.fourcc);
    for(i = 0; i < d.num_objects && i < 4; i++)
      fprintf(stderr, "[VAAE]   obj%d fd=%d size=%u mod=%llx\n",
              i, d.objects[i].fd, d.objects[i].size,
              (unsigned long long)d.objects[i].drm_format_modifier);
  }

  for(i = 0; i < d.num_objects && i < 4; i++) {
    out->fd[i] = d.objects[i].fd;
    out->modifier[i] = d.objects[i].drm_format_modifier;
  }
  for(i = 0; i < d.num_layers && i < 4; i++) {
    out->layer[i].fourcc = d.layers[i].drm_format;
    out->layer[i].num_planes = d.layers[i].num_planes;
    for(j = 0; j < d.layers[i].num_planes && j < 4; j++) {
      out->layer[i].object_index[j] = d.layers[i].object_index[j];
      out->layer[i].offset[j] = d.layers[i].offset[j];
      out->layer[i].pitch[j] = d.layers[i].pitch[j];
    }
  }
  return 0;
}

// ml_vaapi_alloc_frame — allocate an AV_PIX_FMT_VAAPI frame on the
// hw device (AVBufferRef from ml_vaapi_device_create): hw_frames_ctx
// + av_hwframe_get_buffer. The returned AVFrame carries a real
// VASurfaceID in data[3] — test/diagnostic use.
static void *
ml_vaapi_alloc_frame(void *devref, int w, int h)
{
  AVBufferRef *frames;
  AVHWFramesContext *fc;
  AVFrame *f;

  if(devref == NULL)
    return NULL;
  frames = av_hwframe_ctx_alloc((AVBufferRef *)devref);
  if(frames == NULL)
    return NULL;
  fc = (AVHWFramesContext *)frames->data;
  fc->format = AV_PIX_FMT_VAAPI;
  fc->sw_format = AV_PIX_FMT_NV12;
  fc->width = w;
  fc->height = h;
  fc->initial_pool_size = 4;
  if(av_hwframe_ctx_init(frames) < 0) {
    av_buffer_unref(&frames);
    return NULL;
  }
  f = av_frame_alloc();
  if(f == NULL || av_hwframe_get_buffer(frames, f, 0) < 0) {
    av_buffer_unref(&frames);
    return NULL;
  }
  av_buffer_unref(&frames);
  return f;
}

// ml_vaapi_fill — fill the VASurface in frame->data[3] with a flat
// test pattern via vaDeriveImage + vaMapBuffer: plane0 = y, plane1
// = u,v alternating. Test/diagnostic use.
static int
ml_vaapi_fill(void *avframe, int y, int u, int v)
{
  AVFrame *f = (AVFrame *)avframe;
  AVHWFramesContext *fc;
  AVVAAPIDeviceContext *vdc;
  VASurfaceID surf;
  VAImage img;
  VAStatus st;
  void *ptr;
  int i;

  if(f == NULL || f->hw_frames_ctx == NULL)
    return -1;
  fc = (AVHWFramesContext *)f->hw_frames_ctx->data;
  if(fc == NULL || fc->device_ctx == NULL)
    return -1;
  vdc = (AVVAAPIDeviceContext *)fc->device_ctx->hwctx;
  surf = (VASurfaceID)(uintptr_t)f->data[3];

  st = vaDeriveImage(vdc->display, surf, &img);
  if(st != VA_STATUS_SUCCESS)
    return (int)st;
  st = vaMapBuffer(vdc->display, img.buf, &ptr);
  if(st != VA_STATUS_SUCCESS) {
    vaDestroyImage(vdc->display, img.image_id);
    return (int)st;
  }
  for(i = 0; i < img.height; i++)
    memset((uint8_t *)ptr + img.offsets[0] + i * img.pitches[0],
           y, img.pitches[0]);
  if(img.num_planes > 1)
    for(i = 0; i < img.height / 2; i++) {
      uint8_t *row = (uint8_t *)ptr + img.offsets[1] +
                     i * img.pitches[1];
      int j;
      for(j = 0; j + 1 < img.pitches[1]; j += 2) {
        row[j] = (uint8_t)u;
        row[j + 1] = (uint8_t)v;
      }
    }
  vaUnmapBuffer(vdc->display, img.buf);
  vaDestroyImage(vdc->display, img.image_id);
  return 0;
}

// ml_fd_close — close() a dmabuf fd from Go.
static void
ml_fd_close(int fd)
{
  if(fd >= 0)
    close(fd);
}
*/
import "C"

import (
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"github.com/czz/movian-go/internal/libav"
	"github.com/czz/movian-go/internal/video"
)

// vaapiNodeUsable — Go twin of ml_vaapi_node_usable: a DRM node is
// usable for VAAPI unless its kernel driver is "nvidia" (that VA
// driver is an X11-only VDPAU bridge). Existence of the node is
// enough — driver presence is decided by vaInitialize at create.
func vaapiNodeUsable(path string) bool {
	if _, err := os.Stat(path); err != nil {
		return false
	}
	drv, err := os.Readlink("/sys/class/drm/" + filepath.Base(path) +
		"/device/driver")
	if err != nil {
		return true
	}
	return filepath.Base(drv) != "nvidia"
}

// NvidiaRenderActive — true when the GL renderer runs on the NVIDIA
// GPU: PRIME render-offload env vars, or the EGL render node (when
// the backend resolved one) is nvidia-driven. The decoder prefers
// NVDEC then — binding VAAPI on a different GPU (Intel) would make
// the dmabuf zero-copy path impossible anyway.
func NvidiaRenderActive(vs *video.VideoSettings) bool {
	if os.Getenv("__NV_PRIME_RENDER_OFFLOAD") == "1" ||
		os.Getenv("__GLX_VENDOR_LIBRARY_NAME") == "nvidia" {
		return true
	}
	if vs != nil {
		if n := vs.VaapiEGL.RenderNode(); n != "" && !vaapiNodeUsable(n) {
			return true
		}
	}
	return false
}

// VaapiHwPresent — cheap probe for the settings UI: true when at
// least one usable DRM node exists. Does not load libva (the NVIDIA
// bridge driver would crash headless) — real availability is still
// decided by VaapiDevice().
func VaapiHwPresent() bool {
	for i := 128; i < 144; i++ {
		if vaapiNodeUsable(fmt.Sprintf("/dev/dri/renderD%d", i)) {
			return true
		}
	}
	for i := range 16 {
		if vaapiNodeUsable(fmt.Sprintf("/dev/dri/card%d", i)) {
			return true
		}
	}
	return false
}

// VaapiDevice returns the session's VAAPI device AVBufferRef* (was
// the process-global vaapiDev/vaapiOnce — now cached on the injected
// VideoSettings), or nil when VAAPI is not usable on this system.
// Display-server independent (DRM render node), so unlike VDPAU it
// needs no UI wiring. The EGL render node (if the GL backend resolved
// one) is preferred so decode runs on the GPU that renders — the
// dmabuf zero-copy import is only reliable intra-device.
func VaapiDevice(vs *video.VideoSettings) unsafe.Pointer {
	if vs == nil {
		return nil
	}
	vs.VaapiDevOnce.Do(func() {
		node := vs.VaapiEGL.RenderNode()
		var cnode *C.char
		if node != "" {
			cnode = C.CString(node)
			defer C.free(unsafe.Pointer(cnode))
		}
		var usedpath, vendor [128]C.char
		vs.VaapiDev = C.ml_vaapi_device_create(cnode,
			&usedpath[0], C.int(len(usedpath)),
			&vendor[0], C.int(len(vendor)))
		if vs.VaapiDev != nil {
			lavTS.Info("LAVC", "VAAPI device: %s (%s)",
				C.GoString(&vendor[0]), C.GoString(&usedpath[0]))
		}
	})
	return vs.VaapiDev
}

// MlVaapiBindCodec — counterpart of MlVdpauBindCodec for VAAPI.
// devref is the AVBufferRef* from VaapiDevice. Returns 0 on success.
func MlVaapiBindCodec(ctx *libav.AVCodecContext, devref unsafe.Pointer) int {
	return int(C.ml_vaapi_bind_codec((*C.AVCodecContext)(ctx.CPtr()),
		(*C.AVBufferRef)(devref)))
}

// HwframeTransfer wraps av_hwframe_transfer_data — pulls a hw frame
// (e.g. AV_PIX_FMT_VAAPI) into the software frame dst.
func HwframeTransfer(dst, src *libav.AVFrame) int {
	return int(C.ml_hwframe_transfer(dst.CPtr(), src.CPtr()))
}

// AvLogVerbose raises av_log to VERBOSE — test/debug use.
func AvLogVerbose() { C.ml_avlog_verbose() }

// VaapiExportSurface — vaExportSurfaceHandle(DRM_PRIME_2, READ_ONLY |
// SEPARATE_LAYERS) on the VASurfaceID carried by an AV_PIX_FMT_VAAPI
// AVFrame. Returns the descriptor on success.
func VaapiExportSurface(avframe *libav.AVFrame) (*VaapiDmabufDesc, error) {
	var d C.ml_dmabuf_desc
	if ret := C.ml_vaapi_export(avframe.CPtr(), &d); ret != 0 {
		return nil, fmt.Errorf("vaExportSurfaceHandle ret=%d", int(ret))
	}
	out := &VaapiDmabufDesc{
		FourCC:     uint32(d.fourcc),
		Width:      int(d.width),
		Height:     int(d.height),
		NumObjects: int(d.num_objects),
		NumLayers:  int(d.num_layers),
	}
	for i := range 4 {
		out.Fd[i] = int32(d.fd[i])
		out.Modifier[i] = uint64(d.modifier[i])
		out.Layer[i].FourCC = uint32(d.layer[i].fourcc)
		out.Layer[i].NumPlanes = int(d.layer[i].num_planes)
		for j := range 4 {
			out.Layer[i].ObjectIndex[j] = uint32(d.layer[i].object_index[j])
			out.Layer[i].Offset[j] = uint32(d.layer[i].offset[j])
			out.Layer[i].Pitch[j] = uint32(d.layer[i].pitch[j])
		}
	}
	return out, nil
}

// FdClose — close() a dmabuf fd.
func FdClose(fd int32) { C.ml_fd_close(C.int(fd)) }

// VaapiAllocFrame — allocate an AV_PIX_FMT_VAAPI frame (real
// VASurfaceID in data[3]) on the hw device. Test/diagnostic use.
func VaapiAllocFrame(devref unsafe.Pointer, w, h int) *libav.AVFrame {
	return libav.WrapAVFrame(C.ml_vaapi_alloc_frame(devref, C.int(w), C.int(h)))
}

// VaapiFillFrame — fill the frame's VASurface with a flat test
// pattern (vaDeriveImage + vaMapBuffer). Test/diagnostic use.
func VaapiFillFrame(avframe *libav.AVFrame, y, u, v int) int {
	return int(C.ml_vaapi_fill(avframe.CPtr(), C.int(y), C.int(u), C.int(v)))
}
