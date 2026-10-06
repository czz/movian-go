//go:build linux && !android && !rpi && !sunxi && cgo

package glw

// VAAPI-EGL zero-copy video engine — extension, no C counterpart.
// Upstream Movian has no VAAPI path at all (VDPAU only, X11-only).
//
// Pipeline: 'VAAE' frame (fi_avframe = AV_PIX_FMT_VAAPI AVFrame)
//   → vaExportSurfaceHandle(DRM_PRIME_2, COMPOSED_LAYERS)   [deliver]
//   → 2x eglCreateImageKHR(EGL_LINUX_DMA_BUF_EXT)           [render]
//        plane0 → DRM_FORMAT_R8,  plane1 → DRM_FORMAT_GR88
//   → 2x glEGLImageTargetTexture2DOES → GL_TEXTURE_2D       [render]
//   → nv12_*_norm shaders (sampler2D + u_colormtx)          [render]
//
// The composed NV12 fourcc is external_only on Mesa and
// GL_TEXTURE_EXTERNAL_OES does not exist on desktop GL — so each
// plane is imported as a separate single-plane EGLImage bound to a
// regular texture (R8/GR88 are importable as TEXTURE_2D even with
// the iHD X_TILED modifier). The shader does the YUV→RGB conversion.
// Resource teardown goes through gv_reaps so it always runs on the UI
// thread (same pattern as glw_video_opengl.c's reap).

/*
#cgo linux LDFLAGS: -lEGL
#include <string.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <unistd.h>
#include <EGL/egl.h>
#include <EGL/eglext.h>
#include <GL/gl.h>

#ifndef EGL_LINUX_DMA_BUF_EXT
#define EGL_LINUX_DMA_BUF_EXT             0x3270
#endif
#ifndef EGL_LINUX_DRM_FOURCC_EXT
#define EGL_LINUX_DRM_FOURCC_EXT          0x3271
#define EGL_DMA_BUF_PLANE0_FD_EXT         0x3272
#define EGL_DMA_BUF_PLANE0_OFFSET_EXT     0x3273
#define EGL_DMA_BUF_PLANE0_PITCH_EXT      0x3274
#endif
#ifndef EGL_DMA_BUF_PLANE0_MODIFIER_LO_EXT
#define EGL_DMA_BUF_PLANE0_MODIFIER_LO_EXT 0x3443
#define EGL_DMA_BUF_PLANE0_MODIFIER_HI_EXT 0x3444
#define EGL_DMA_BUF_PLANE1_MODIFIER_LO_EXT 0x3445
#define EGL_DMA_BUF_PLANE1_MODIFIER_HI_EXT 0x3446
#define EGL_DMA_BUF_PLANE2_MODIFIER_LO_EXT 0x3447
#define EGL_DMA_BUF_PLANE2_MODIFIER_HI_EXT 0x3448
#endif
#ifndef GL_TEXTURE_EXTERNAL_OES
#define GL_TEXTURE_EXTERNAL_OES 0x8D65
#endif
#ifndef EGL_DEVICE_EXT
#define EGL_DEVICE_EXT 0x322C
#endif
#ifndef EGL_DRM_RENDER_NODE_FILE_EXT
#define EGL_DRM_RENDER_NODE_FILE_EXT 0x3377
#endif

#define ML_DRM_FORMAT_MOD_INVALID 0x00ffffffffffffffULL

static PFNEGLCREATEIMAGEKHRPROC          p_create_image;
static PFNEGLDESTROYIMAGEKHRPROC         p_destroy_image;
static PFNGLEGLIMAGETARGETTEXTURE2DOESPROC p_image_target_tex;
static EGLDisplay egl_dpy;
static int egl_probed;
static int egl_modifiers;
static char egl_render_node[64]; // "/dev/dri/renderDxxx" or ""

static void glw_egl_dump_dmabuf_fmts(void);
static void glw_egl_render_node_query(void);

// glw_egl_probe — called once on the UI thread with the GL context
// current. Returns 1 when eglGetCurrentDisplay yields a real
// EGLDisplay exposing EGL_EXT_image_dma_buf_import and all required
// entry points resolve; 0 otherwise (GLX context, headless, no ext).
static int
glw_egl_probe(void)
{
  const char *exts;

  if(egl_probed)
    return egl_dpy != EGL_NO_DISPLAY;
  egl_probed = 1;

  egl_dpy = eglGetCurrentDisplay();
  if(egl_dpy == EGL_NO_DISPLAY)
    return 0;

  exts = eglQueryString(egl_dpy, EGL_EXTENSIONS);
  if(exts == NULL ||
     strstr(exts, "EGL_EXT_image_dma_buf_import") == NULL) {
    egl_dpy = EGL_NO_DISPLAY;
    return 0;
  }
  egl_modifiers = strstr(exts,
      "EGL_EXT_image_dma_buf_import_modifiers") != NULL;

  p_create_image = (PFNEGLCREATEIMAGEKHRPROC)
      eglGetProcAddress("eglCreateImageKHR");
  p_destroy_image = (PFNEGLDESTROYIMAGEKHRPROC)
      eglGetProcAddress("eglDestroyImageKHR");
  p_image_target_tex = (PFNGLEGLIMAGETARGETTEXTURE2DOESPROC)
      eglGetProcAddress("glEGLImageTargetTexture2DOES");

  if(p_create_image == NULL || p_destroy_image == NULL ||
     p_image_target_tex == NULL) {
    egl_dpy = EGL_NO_DISPLAY;
    return 0;
  }
  if(getenv("MOVIANGO_VAAPI_DEBUG")) {
    fprintf(stderr, "[VAAE] EGL probe ok, modifiers=%d\n",
            egl_modifiers);
    glw_egl_dump_dmabuf_fmts();
  }
  glw_egl_render_node_query();
  if(getenv("MOVIANGO_VAAPI_DEBUG"))
    fprintf(stderr, "[VAAE] EGL render node: %s\n",
            egl_render_node[0] ? egl_render_node : "(unknown)");
  return 1;
}

// glw_egl_render_node_query — resolve the DRM render node backing
// egl_dpy via EGL_EXT_device_query (client ext) +
// EGL_EXT_device_drm_render_node. The decoder uses this node first so
// VAAPI decodes on the same GPU EGL renders on — required for the
// dmabuf import to be reliable. "" when unavailable (GLX, old Mesa).
static void
glw_egl_render_node_query(void)
{
  const char *client_exts, *node;
  PFNEGLQUERYDISPLAYATTRIBEXTPROC qda;
  PFNEGLQUERYDEVICESTRINGEXTPROC qds;
  EGLAttrib dev;

  egl_render_node[0] = '\0';
  client_exts = eglQueryString(EGL_NO_DISPLAY, EGL_EXTENSIONS);
  if(client_exts == NULL ||
     strstr(client_exts, "EGL_EXT_device_query") == NULL)
    return;
  qda = (PFNEGLQUERYDISPLAYATTRIBEXTPROC)
      eglGetProcAddress("eglQueryDisplayAttribEXT");
  qds = (PFNEGLQUERYDEVICESTRINGEXTPROC)
      eglGetProcAddress("eglQueryDeviceStringEXT");
  if(qda == NULL || qds == NULL)
    return;
  if(!qda(egl_dpy, EGL_DEVICE_EXT, &dev))
    return;
  node = qds((EGLDeviceEXT)dev, EGL_DRM_RENDER_NODE_FILE_EXT);
  if(node != NULL)
    snprintf(egl_render_node, sizeof(egl_render_node), "%s", node);
}

// glw_egl_get_render_node — Go-visible accessor; "" when unknown.
static const char *
glw_egl_get_render_node(void)
{
  return egl_render_node;
}

// glw_egl_import_plane — eglCreateImageKHR for a SINGLE plane of a
// dmabuf surface + glEGLImageTargetTexture2DOES into a fresh
// GL_TEXTURE_2D. A multi-plane format (NV12) is imported as one
// EGLImage per plane, each described with a single-plane DRM format
// (R8 for Y, GR88 for UV): Mesa marks the composed NV12 fourcc
// external_only — GL_TEXTURE_EXTERNAL_OES does not exist on desktop
// GL — while R8/GR88 import fine as regular textures even with the
// iHD X_TILED modifier (explicit attribs; implicit resolution is
// unreliable on iris). The nv12_* shaders do the YUV→RGB.
static void *
glw_egl_import_plane(int w, int h, uint32_t fourcc, int fd,
                     uint32_t off, uint32_t pitch, uint64_t mod,
                     GLuint *tex_out)
{
  EGLint attrs[24];
  int n = 0;
  EGLImageKHR img;
  GLuint tex;

  if(egl_dpy == EGL_NO_DISPLAY || p_create_image == NULL ||
     p_image_target_tex == NULL)
    return NULL;

  attrs[n++] = EGL_WIDTH;
  attrs[n++] = w;
  attrs[n++] = EGL_HEIGHT;
  attrs[n++] = h;
  attrs[n++] = EGL_LINUX_DRM_FOURCC_EXT;
  attrs[n++] = (EGLint)fourcc;
  attrs[n++] = EGL_DMA_BUF_PLANE0_FD_EXT;
  attrs[n++] = fd;
  attrs[n++] = EGL_DMA_BUF_PLANE0_OFFSET_EXT;
  attrs[n++] = (EGLint)off;
  attrs[n++] = EGL_DMA_BUF_PLANE0_PITCH_EXT;
  attrs[n++] = (EGLint)pitch;
  if(egl_modifiers && mod != ML_DRM_FORMAT_MOD_INVALID) {
    attrs[n++] = EGL_DMA_BUF_PLANE0_MODIFIER_LO_EXT;
    attrs[n++] = (EGLint)(mod & 0xffffffff);
    attrs[n++] = EGL_DMA_BUF_PLANE0_MODIFIER_HI_EXT;
    attrs[n++] = (EGLint)(mod >> 32);
  }
  attrs[n++] = EGL_NONE;

  img = p_create_image(egl_dpy, EGL_NO_CONTEXT, EGL_LINUX_DMA_BUF_EXT,
                       (EGLClientBuffer)NULL, attrs);
  if(img == EGL_NO_IMAGE_KHR) {
    if(getenv("MOVIANGO_VAAPI_DEBUG"))
      fprintf(stderr, "[VAAE] eglCreateImageKHR failed: err=%x "
              "fourcc=%08x %dx%d fd=%d off=%u pitch=%u mod=%llx\n",
              eglGetError(), fourcc, w, h, fd, off, pitch,
              (unsigned long long)mod);
    return NULL;
  }

  glGenTextures(1, &tex);
  glBindTexture(GL_TEXTURE_2D, tex);
  p_image_target_tex(GL_TEXTURE_2D, (GLeglImageOES)img);
  glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MIN_FILTER,
                  GL_LINEAR);
  glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MAG_FILTER,
                  GL_LINEAR);
  glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_WRAP_S,
                  GL_CLAMP_TO_EDGE);
  glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_WRAP_T,
                  GL_CLAMP_TO_EDGE);
  *tex_out = tex;
  return (void *)img;
}

static void
glw_egl_destroy_image(void *img)
{
  if(egl_dpy != EGL_NO_DISPLAY && img != NULL &&
     p_destroy_image != NULL)
    p_destroy_image(egl_dpy, (EGLImageKHR)img);
}

// glw_egl_tex_sample — test support: read back a bound texture via
// glGetTexImage (GL 1.1) into caller's RGBA buffer. Returns
// glGetError() (nonzero on drivers that can't download EGLImage
// textures).
static int
glw_egl_tex_sample(GLuint tex, int w, int h, uint8_t *buf)
{
  glBindTexture(GL_TEXTURE_2D, tex);
  glGetTexImage(GL_TEXTURE_2D, 0, GL_RGBA, GL_UNSIGNED_BYTE, buf);
  return (int)glGetError();
}

// glw_egl_dump_dmabuf_fmts — debug: list the dma-buf formats and
// modifiers the EGL display accepts (MOVIANGO_VAAPI_DEBUG).
static void
glw_egl_dump_dmabuf_fmts(void)
{
  PFNEGLQUERYDMABUFFORMATSEXTPROC qfmt;
  PFNEGLQUERYDMABUFMODIFIERSEXTPROC qmod;
  EGLint *fmts, n = 0, i, j;

  qfmt = (PFNEGLQUERYDMABUFFORMATSEXTPROC)
      eglGetProcAddress("eglQueryDmaBufFormatsEXT");
  qmod = (PFNEGLQUERYDMABUFMODIFIERSEXTPROC)
      eglGetProcAddress("eglQueryDmaBufModifiersEXT");
  if(qfmt == NULL || egl_dpy == EGL_NO_DISPLAY)
    return;
  if(!qfmt(egl_dpy, 0, NULL, &n) || n <= 0)
    return;
  fmts = malloc(n * sizeof(*fmts));
  qfmt(egl_dpy, n, fmts, &n);
  fprintf(stderr, "[VAAE] supported dmabuf formats (%d):", n);
  for(i = 0; i < n; i++)
    fprintf(stderr, " %08x", (unsigned)fmts[i]);
  fprintf(stderr, "\n");
  if(qmod != NULL) {
    for(i = 0; i < n; i++) {
      EGLuint64KHR mods[32];
      EGLBoolean ext[32];
      EGLint nm = 0;
      if(qmod(egl_dpy, fmts[i], 32, mods, ext, &nm) && nm > 0) {
        fprintf(stderr, "[VAAE]   %08x modifiers:", (unsigned)fmts[i]);
        for(j = 0; j < nm; j++)
          fprintf(stderr, " %llx%s", (unsigned long long)mods[j],
                  ext[j] ? "(ext)" : "");
        fprintf(stderr, "\n");
      }
    }
  }
  free(fmts);
}

#ifndef EGL_PLATFORM_SURFACELESS_MESA
#define EGL_PLATFORM_SURFACELESS_MESA 0x31DD
#endif

// glw_egl_node_driver — basename of the kernel driver bound to a
// /dev/dri/renderD* node (e.g. "i915", "nvidia"). "" when unknown.
static void
glw_egl_node_driver(const char *node, char *out, size_t outsz)
{
  char linkpath[160], buf[160];
  ssize_t n;
  const char *base;

  out[0] = '\0';
  base = strrchr(node, '/');
  base = base ? base + 1 : node;
  snprintf(linkpath, sizeof(linkpath),
           "/sys/class/drm/%s/device/driver", base);
  n = readlink(linkpath, buf, sizeof(buf) - 1);
  if(n <= 0)
    return;
  buf[n] = '\0';
  base = strrchr(buf, '/');
  snprintf(out, outsz, "%s", base ? base + 1 : buf);
}

// glw_egl_surfaceless_ctx — test support: create + make current a
// surfaceless Mesa EGL context so the import path can be exercised
// headless (CI, no display server). Picks the EGLDevice whose render
// node is not nvidia-driven (the VAAPI dmabuf comes from i915 — a
// cross-device import would legitimately fail). Returns 1 on success.
static int
glw_egl_surfaceless_ctx(void)
{
  EGLDisplay d;
  EGLConfig cfg;
  EGLContext c;
  EGLint n;
  EGLDeviceEXT devs[16], dev = NULL;
  EGLint ndev = 0;
  int i;
  static const EGLint cfgatts[] = {
    EGL_SURFACE_TYPE, EGL_PBUFFER_BIT,
    EGL_RENDERABLE_TYPE, EGL_OPENGL_BIT, EGL_NONE };
  static const EGLint ctxatts[] = {
    EGL_CONTEXT_MAJOR_VERSION, 3,
    EGL_CONTEXT_MINOR_VERSION, 3, EGL_NONE };
  PFNEGLQUERYDEVICESEXTPROC qdev;
  PFNEGLQUERYDEVICESTRINGEXTPROC qds;

  // Prefer an explicit device on the same GPU as the VAAPI node:
  // EGL_PLATFORM_SURFACELESS_MESA would otherwise grab the first
  // device — the NVIDIA one on hybrid systems — and the i915 dmabuf
  // can never import there.
  qdev = (PFNEGLQUERYDEVICESEXTPROC)
      eglGetProcAddress("eglQueryDevicesEXT");
  qds = (PFNEGLQUERYDEVICESTRINGEXTPROC)
      eglGetProcAddress("eglQueryDeviceStringEXT");
  if(qdev != NULL && qds != NULL &&
     qdev(16, devs, &ndev) && ndev > 0) {
    for(i = 0; i < ndev; i++) {
      const char *node =
          qds(devs[i], EGL_DRM_RENDER_NODE_FILE_EXT);
      char drv[32];
      if(node == NULL)
        continue;
      glw_egl_node_driver(node, drv, sizeof(drv));
      if(strcmp(drv, "nvidia") == 0)
        continue;
      dev = devs[i];
      break;
    }
  }

  if(dev != NULL)
    d = eglGetPlatformDisplay(EGL_PLATFORM_DEVICE_EXT, dev, NULL);
  else
    d = eglGetPlatformDisplay(EGL_PLATFORM_SURFACELESS_MESA,
                              EGL_DEFAULT_DISPLAY, NULL);
  if(d == EGL_NO_DISPLAY)
    return -1;
  if(!eglInitialize(d, NULL, NULL))
    return -2;
  eglBindAPI(EGL_OPENGL_API);
  if(!eglChooseConfig(d, cfgatts, &cfg, 1, &n) || n == 0)
    return -3;
  c = eglCreateContext(d, cfg, EGL_NO_CONTEXT, ctxatts);
  if(c == EGL_NO_CONTEXT)
    return -4;
  if(!eglMakeCurrent(d, EGL_NO_SURFACE, EGL_NO_SURFACE, c))
    return -5;
  return 1;
}
*/
import "C"

import (
	"unsafe"

	"github.com/czz/movian-go/internal/libav"
	mediacore "github.com/czz/movian-go/internal/media/core"
	medialibav "github.com/czz/movian-go/internal/media/libav"
	"github.com/czz/movian-go/internal/video"
	"github.com/czz/movian-go/internal/video/decoder"
)

// glwVaapiEGLProbe — UI-thread EGL capability probe; publishes the
// result to video.SetVaapiEGLZeroCopy so the libav deliver path can
// pick dmabuf export vs hwframe transfer.
func glwVaapiEGLProbe() {
	ok := C.glw_egl_probe() == 1
	var vs *video.VideoSettings
	if glwDeps.bs != nil && glwDeps.bs.MediaSys() != nil {
		vs = glwDeps.bs.MediaSys().VS
	}
	if vs != nil {
		vs.VaapiEGL.SetZeroCopy(ok)
		if ok {
			vs.VaapiEGL.SetRenderNode(
				C.GoString(C.glw_egl_get_render_node()))
		}
	}
	// Eager hw probe: runs after the EGL node is published so the
	// VAAPI device is created on the GPU that renders; logs which
	// backend/GPU will decode video at startup.
	medialibav.ProbeHwAccel(vs)
}

// glwVaapiEGLSurfacelessCtx — test support: create + make current a
// surfaceless Mesa EGL context (EGL_PLATFORM_SURFACELESS_MESA) so the
// dmabuf import path can run headless.
func glwVaapiEGLSurfacelessCtx() int {
	return int(C.glw_egl_surfaceless_ctx())
}

// glwVaapiEGLTexSample — test support: render a texture into an FBO
// and read back w*h RGBA pixels into buf.
func glwVaapiEGLTexSample(tex uint32, w, h int, buf []byte) int {
	return int(C.glw_egl_tex_sample(C.GLuint(tex), C.int(w), C.int(h),
		(*C.uint8_t)(unsafe.Pointer(&buf[0]))))
}

// ---------------------------------------------------------------------------
// Resource teardown — mirrors videoOglDoReap: a reap task carries the
// GL handles so destruction always happens on the UI thread.

type vaapiReapTask struct {
	images [4]unsafe.Pointer
	tex    [4]uint32
	fds    [4]int32
}

func vaapiDoReap(gv *GlwVideo, t *glwVideoReapTask) {
	rt := t.aux.(*vaapiReapTask)
	for i := range 4 {
		if rt.images[i] != nil {
			C.glw_egl_destroy_image(rt.images[i])
		}
		if rt.fds[i] >= 0 {
			medialibav.FdClose(rt.fds[i])
		}
	}
	if rt.tex[0] != 0 {
		C.glDeleteTextures(4, (*C.GLuint)(&rt.tex[0]))
	}
}

// vaapiSurfaceReleaseRes — enqueue a reap task for the surface's EGL
// images, textures and not-yet-imported fds, then clear the state.
// Safe on any thread: the reap runs on the UI thread.
func vaapiSurfaceReleaseRes(gv *GlwVideo, gvs *glwVideoSurface) {
	if gvs.gvsVaapiState == 0 {
		return
	}
	t := glwVideoAddReapTask(gv, 0, vaapiDoReap)
	rt := &vaapiReapTask{}
	t.aux = rt

	for i := range 4 {
		rt.images[i] = gvs.gvsVaapiImages[i]
		rt.fds[i] = gvs.gvsVaapiFdOwned[i]
	}
	for i := range 3 {
		rt.tex[i] = gvs.gvsTexture.Textures[i]
	}
	gvs.gvsVaapiState = 0
	for i := range 4 {
		gvs.gvsVaapiImages[i] = nil
		gvs.gvsVaapiFdOwned[i] = -1
	}
	gvs.gvsTexture = GlwBackendTexture{}
}

// C shape: surface_reset (glw_video_tex.c:43-52) + reap
func vaapiSurfaceReset(gv *GlwVideo, gvs *glwVideoSurface) {
	vaapiSurfaceReleaseRes(gv, gvs)

	if gvs.gvsRefAux != nil {
		gvs.gvsRefRelease(gvs.gvsRefAux)
		gvs.gvsRefAux = nil
	}

	*gvs = glwVideoSurface{}
	for i := range gvs.gvsVaapiFdOwned {
		gvs.gvsVaapiFdOwned[i] = -1
	}
}

func vaapiReset(gv *GlwVideo) {
	for i := range glwVideoMaxSurfaces {
		vaapiSurfaceReset(gv, &gv.gvSurfaces[i])
	}
}

// C shape: surface_init (glw_video_tex.c:69-75)
func vaapiSurfaceSetup(gv *GlwVideo, gvs *glwVideoSurface) {
	vaapiSurfaceReleaseRes(gv, gvs)
	gvs.gvsUploaded = 0
	gvsTAILQInsertTail(&gv.gvAvailQueue, gvs)
	gv.gvAvailQueueCond.Signal()
}

const glwVideoVaapiNumSurfaces = 4 // C shape: NUM_SURFACES (glw_video_tex.c:34)

// C shape: make_surfaces_available (glw_video_tex.c:80-87)
func vaapiMakeSurfacesAvailable(gv *GlwVideo) {
	for i := range glwVideoVaapiNumSurfaces {
		gvs := &gv.gvSurfaces[i]
		gvsTAILQInsertTail(&gv.gvAvailQueue, gvs)
	}
}

// C shape: tex_init (glw_video_tex.c:93-105)
func vaapiStart(gv *GlwVideo) int {
	gv.gvGpa.GpaAux = gv
	gv.gvGpa.GpaLoadUniforms = glwVideoOpenglLoadUniforms
	gv.gvGpa.GpaLoadTexture = vaapiLoadTextureExt

	// fd ownership sentinel: zero-value 0 is stdin — vaapiSurfaceReleaseRes
	// closes every entry >= 0, so surfaces must start life with -1 or
	// init-time reaps would close fd 0 (see vaapiDeliver).
	for i := range gv.gvSurfaces {
		for j := range gv.gvSurfaces[i].gvsVaapiFdOwned {
			gv.gvSurfaces[i].gvsVaapiFdOwned[j] = -1
		}
	}

	gv.gvPlanes = 1

	for i := range gv.gvCmatrixCur {
		gv.gvCmatrixCur[i] = 0
	}
	vaapiMakeSurfacesAvailable(gv)
	return 0
}

// vaapiLoadTextureExt — 2 GL_TEXTURE_2D per surface (Y + UV):
// sa → units 0,1 (u_t0, u_t1); sb → units 2,3 (u_t2, u_t3).
func vaapiLoadTextureExt(gr *glwRoot, gp *glwProgram, args any,
	t *GlwBackendTexture, num int) {
	if num == 1 {
		C.glActiveTexture(C.GL_TEXTURE3)
		C.glBindTexture(C.GL_TEXTURE_2D, C.GLuint(t.Textures[1]))
		C.glActiveTexture(C.GL_TEXTURE2)
		C.glBindTexture(C.GL_TEXTURE_2D, C.GLuint(t.Textures[0]))
		C.glActiveTexture(C.GL_TEXTURE0)
	} else {
		C.glActiveTexture(C.GL_TEXTURE1)
		C.glBindTexture(C.GL_TEXTURE_2D, C.GLuint(t.Textures[1]))
		C.glActiveTexture(C.GL_TEXTURE0)
		C.glBindTexture(C.GL_TEXTURE_2D, C.GLuint(t.Textures[0]))
	}
}

// C shape: gv_surface_pixmap_release (glw_video_tex.c:111-127)
func vaapiSurfacePixmapRelease(gv *GlwVideo, gvs *glwVideoSurface,
	fromqueue *glwVideoSurfaceQueue) {
	// C: assert(gvs != gv->gv_sa); assert(gvs != gv->gv_sb)

	gvsTAILQRemove(fromqueue, gvs)

	vaapiSurfaceReleaseRes(gv, gvs)

	if gvs.gvsRefAux != nil {
		gvs.gvsRefRelease(gvs.gvsRefAux)
		gvs.gvsRefAux = nil
	}

	gvsTAILQInsertTail(&gv.gvAvailQueue, gvs)
	gv.gvAvailQueueCond.Signal()
}

// C shape: tex_newframe (glw_video_tex.c:133-148)
func vaapiNewframe(gv *GlwVideo, vd *decoder.VideoDecoder, flags int) int64 {
	// C: hts_mutex_assert(&gv->gv_surface_mutex)

	for gvs := gv.gvParkedQueue.tqhFirst; gvs != nil; {
		gvsTAILQRemove(&gv.gvParkedQueue, gvs)
		vaapiSurfaceSetup(gv, gvs)
		gvs = gv.gvParkedQueue.tqhFirst
	}

	glwNeedRefresh(gv.w.glwRoot, 0)

	gvColorMatrixUpdate(gv)

	return glwVideoNewframeBlend(gv, vd, flags, vaapiSurfacePixmapRelease, 1)
}

// vaapiImportSurface — UI thread: turn the pending dmabuf descriptor
// into one EGLImage + GL_TEXTURE_2D per NV12 plane (Y→R8, UV→GR88).
// On failure the surface keeps no texture and is not drawn (the
// frame is lost but playback continues; subsequent frames retry).
func vaapiImportSurface(gv *GlwVideo, gvs *glwVideoSurface) {
	desc := &gvs.gvsVaapiDesc
	l := &desc.Layer[0]

	// Single-plane DRM formats used to import each NV12 plane:
	// plane0 → DRM_FORMAT_R8 (Y, WxH), plane1 → DRM_FORMAT_GR88
	// (interleaved CbCr, W/2 x H/2). Both are non-external in Mesa's
	// dmabuf format list — the composed NV12 fourcc is not.
	fourccs := [2]C.uint32_t{0x20203852, 0x38385247} // 'R8  ', 'GR88'
	ws := [2]C.int{C.int(desc.Width), C.int((desc.Width + 1) / 2)}
	hs := [2]C.int{C.int(desc.Height), C.int((desc.Height + 1) / 2)}

	for j := range 2 {
		var tex C.GLuint
		img := C.glw_egl_import_plane(
			ws[j], hs[j], fourccs[j],
			C.int(desc.Fd[l.ObjectIndex[j]]),
			C.uint32_t(l.Offset[j]),
			C.uint32_t(l.Pitch[j]),
			C.uint64_t(desc.Modifier[l.ObjectIndex[j]]),
			&tex)
		if img == nil {
			// Import is driver/EGL dependent — after a few
			// consecutive failures the zero-copy path is dead
			// weight: flip the flag off so LibAVDeliverFrame
			// falls back to hwframe transfer (video visible
			// instead of a black picture).
			gvs.vaapiImportFails++
			if gvs.vaapiImportFails == 3 {
				glwDeps.ts.Error("VAAE", "dmabuf import keeps failing, "+
					"falling back to software transfer")
				if glwDeps.bs != nil && glwDeps.bs.MediaSys() != nil {
					if vs := glwDeps.bs.MediaSys().VS; vs != nil {
						vs.VaapiEGL.SetZeroCopy(false)
					}
				}
			}
			return
		}
		gvs.gvsVaapiImages[j] = unsafe.Pointer(img)
		gvs.gvsTexture.Textures[j] = uint32(tex)
	}
	gvs.vaapiImportFails = 0

	// The EGLImages hold references; the dmabuf fds can be closed.
	for i := 0; i < desc.NumObjects && i < 4; i++ {
		if gvs.gvsVaapiFdOwned[i] >= 0 {
			medialibav.FdClose(gvs.gvsVaapiFdOwned[i])
			gvs.gvsVaapiFdOwned[i] = -1
		}
	}
	gvs.gvsTexture.Gltype = C.GL_TEXTURE_2D
	gvs.gvsVaapiState = 2
}

// C shape: tex_render (glw_video_tex.c:154-197)
func vaapiRender(gv *GlwVideo, rc *glwRctx) {
	gr := gv.w.glwRoot
	sa := gv.gvSa
	sb := gv.gvSb
	var gp *glwProgram
	gbr := &gr.grBe

	if sa == nil {
		return
	}

	if sa.gvsVaapiState == 1 {
		vaapiImportSurface(gv, sa)
	}
	if sa.gvsVaapiState != 2 {
		return
	}
	if sb != nil && sb.gvsVaapiState == 1 {
		vaapiImportSurface(gv, sb)
	}
	if sb != nil && sb.gvsVaapiState != 2 {
		sb = nil
	}

	gv.gvWidth = sa.gvsWidth[0]
	gv.gvHeight = sa.gvsHeight[0]

	glwRendererVtxSt(&gv.gvQuad, 0, 0, 1)
	glwRendererVtxSt(&gv.gvQuad, 1, 1, 1)
	glwRendererVtxSt(&gv.gvQuad, 2, 1, 0)
	glwRendererVtxSt(&gv.gvQuad, 3, 0, 0)

	if sb != nil {
		// Two pictures that should be mixed
		gp = gbr.gbrNv122f

		glwRendererVtxSt2(&gv.gvQuad, 0, 0, 1)
		glwRendererVtxSt2(&gv.gvQuad, 1, 1, 1)
		glwRendererVtxSt2(&gv.gvQuad, 2, 1, 0)
		glwRendererVtxSt2(&gv.gvQuad, 3, 0, 0)

	} else {

		// One picture
		gp = gbr.gbrNv121f
	}

	gv.gvGpa.GpaProg = gp

	glwRendererDraw(&gv.gvQuad, gr, rc,
		&sa.gvsTexture,
		sbTexture(sb),
		nil, nil,
		rc.rcAlpha*gv.w.glwAlpha, 0, &gv.gvGpa)
}

// C shape: tex_blackout (glw_video_tex.c:203-207)
func vaapiBlackout(gv *GlwVideo) {
	for i := range gv.gvCmatrixTgt {
		gv.gvCmatrixTgt[i] = 0
	}
}

// C shape: tex_deliver (glw_video_tex.c:213-234)
func vaapiDeliver(fi *mediacore.FrameInfo, gv *GlwVideo,
	gve *glwVideoEngine) int {
	var s *glwVideoSurface

	glwVideoConfigure(gv, gve)

	// Same as the other YUV engines: the nv12 shaders convert via
	// u_colormtx — without this gvCmatrix* stay zero and the video
	// renders black.
	gvColorMatrixSet(gv, fi)

	if s = glwVideoGetSurface(gv, nil, nil); s == nil {
		return -1
	}

	desc, err := medialibav.VaapiExportSurface(
		fi.AVFrame)
	if err != nil {
		gvsTAILQInsertTail(&gv.gvAvailQueue, s)
		return -1
	}

	// Only a single-layer NV12 image (2 planes) is consumable — it
	// becomes one EGLImage bound to GL_TEXTURE_EXTERNAL_OES.
	if desc.NumLayers != 1 || desc.Layer[0].NumPlanes != 2 {
		for i := 0; i < desc.NumObjects && i < 4; i++ {
			medialibav.FdClose(desc.Fd[i])
		}
		gvsTAILQInsertTail(&gv.gvAvailQueue, s)
		return -1
	}

	s.gvsVaapiDesc = *desc
	s.gvsVaapiState = 1

	// Hold the decoded frame until the surface is released: the
	// EGLImage references the VASurface's dmabuf — if the AVFrame
	// unrefs, the surface goes back to the decoder pool and its
	// content is recycled/cleared while we still display it
	// (white/garbage picture). gvsRefAux is released by
	// vaapiSurfaceReleaseRes on the UI thread.
	s.gvsRefAux = medialibav.AvFrameCloneRaw(fi.AVFrame).CPtr()
	s.gvsRefRelease = func(aux unsafe.Pointer) {
		medialibav.AvFrameFreeRaw(libav.WrapAVFrame(aux))
	}

	for i := range 4 {
		s.gvsVaapiImages[i] = nil
		s.gvsVaapiFdOwned[i] = -1
	}
	// Only desc.NumObjects fds are real — the rest of desc.Fd is
	// zero padding and must never be marked owned: FdClose(0) would
	// eat stdin, after which VAAPI's next dup() hands out fd 0 as a
	// real dmabuf and a later reap closes it again, making every
	// eglCreateImageKHR fail with EGL_BAD_ALLOC.
	for i := 0; i < desc.NumObjects && i < 4; i++ {
		s.gvsVaapiFdOwned[i] = desc.Fd[i]
	}

	s.gvsTexture.Width = int32(fi.Width)
	s.gvsTexture.Height = int32(fi.Height)
	s.gvsWidth[0] = fi.Width
	s.gvsHeight[0] = fi.Height

	glwVideoPutSurface(gv, s, fi.PTS, fi.Epoch, int(fi.Duration), 0, 0)
	return 0
}

// Engine registration — mirrors glw_video_tex (glw_video_tex.c:240-248).
var glwVideoVaapiEngine = glwVideoEngine{
	gveType:     mediacore.FourCCVAAE, // 'VAAE' — extension
	gveNewframe: vaapiNewframe,
	gveRender:   vaapiRender,
	gveReset:    vaapiReset,
	gveStart:    vaapiStart,
	gveDeliver:  vaapiDeliver,
	gveBlackout: vaapiBlackout,
}

func init() { glwRegisterVideoEngine(&glwVideoVaapiEngine) }
