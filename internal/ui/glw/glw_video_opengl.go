//go:build !android && !rpi

package glw

// C: src/ui/glw/glw_video_opengl.c — canonical 1:1 port.
// OpenGL video engines: 'YUVP' (PBO-uploaded planar YUV), 'BGR' and
// 'XYZ6' — all sharing the same newframe/render/reset machinery.

/*
#define GL_GLEXT_PROTOTYPES
#ifdef __ANDROID__
#include <GLES2/gl2.h>
#include <GLES2/gl2ext.h>
#elif defined(__APPLE__)
#include <OpenGL/gl.h>
#else
#include <GL/gl.h>
#endif
#ifdef __APPLE__
#include <OpenGL/glext.h>
#else
#include <GL/glext.h>
#endif
#include <stdlib.h>

#if defined(_WIN32)
#include "glw_win32_gl.h"
#endif
#include <string.h>
*/
import "C"

import (
	"unsafe"

	mediacore "github.com/czz/movian-go/internal/media/core"
	medialibav "github.com/czz/movian-go/internal/media/libav"
	"github.com/czz/movian-go/internal/video/decoder"
)

// C: #define GVF_TEX_L/Cr/Cb (glw_video_opengl.c:34-36) — unused, doc only.
// C: LINESIZE(w, c) (glw_video_opengl.c:39-40) — PIXMAP_ROW_ALIGN is 8 on
// non-PPC (pixmap.h:22-26).
const glwVideoPixmapRowAlign = 8

func glwVideoLinesize(w, c int) int {
	return (w*c + glwVideoPixmapRowAlign - 1) &^ (glwVideoPixmapRowAlign - 1)
}

// C: #define PBO_RELEASE_BEFORE_MAP (glw_video_opengl.c:42)
const glwVideoPboReleaseBeforeMap = true

const glwVideoNumSurfaces = 4 // C: NUM_SURFACES (glw_video_opengl.c:44)

// C: typedef struct reap_task (glw_video_opengl.c:50-58). C allocates
// sizeof(reap_task_t) via glw_video_add_reap_task and casts the task
// pointer; Go keeps the GL handles on the task's aux field.
type videoOglReapTask struct {
	pbo    [3]uint32
	tex    [3]uint32
	planes int
}

// C: static void do_reap (glw_video_opengl.c:63-78)
func videoOglDoReap(gv *GlwVideo, t *glwVideoReapTask) {
	rt := t.aux.(*videoOglReapTask)
	for i := range rt.planes {
		if rt.pbo[i] != 0 {
			C.glBindBuffer(C.GL_PIXEL_UNPACK_BUFFER, C.GLuint(rt.pbo[i]))
			C.glUnmapBuffer(C.GL_PIXEL_UNPACK_BUFFER)
		}
		C.glBindBuffer(C.GL_PIXEL_UNPACK_BUFFER, 0)
	}
	if rt.pbo[0] != 0 {
		C.glDeleteBuffers(C.GLsizei(rt.planes), (*C.GLuint)(&rt.pbo[0]))
	}
	if rt.tex[0] != 0 {
		C.glDeleteTextures(C.GLsizei(rt.planes), (*C.GLuint)(&rt.tex[0]))
	}
}

// C: static void surface_reset (glw_video_opengl.c:84-95)
func videoOglSurfaceReset(gv *GlwVideo, gvs *glwVideoSurface) {
	t := glwVideoAddReapTask(gv, 0, videoOglDoReap)
	rt := &videoOglReapTask{planes: gv.gvPlanes}
	t.aux = rt

	for i := range gv.gvPlanes {
		rt.pbo[i] = gvs.gvsPbo[i]
		rt.tex[i] = gvs.gvsTexture.Textures[i]
	}
	*gvs = glwVideoSurface{}
}

// C: static void video_opengl_reset (glw_video_opengl.c:101-106)
func videoOglReset(gv *GlwVideo) {
	for i := range glwVideoMaxSurfaces {
		videoOglSurfaceReset(gv, &gv.gvSurfaces[i])
	}
}

// C: static void surface_init (glw_video_opengl.c:112-141)
func videoOglSurfaceSetup(gv *GlwVideo, gvs *glwVideoSurface) {
	if gvs.gvsPbo[0] != 0 {
		C.glDeleteBuffers(C.GLsizei(gv.gvPlanes), (*C.GLuint)(&gvs.gvsPbo[0]))
	}

	C.glGenBuffers(C.GLsizei(gv.gvPlanes), (*C.GLuint)(&gvs.gvsPbo[0]))

	if gvs.gvsTexture.Textures[0] == 0 {
		C.glGenTextures(C.GLsizei(gv.gvPlanes),
			(*C.GLuint)(&gvs.gvsTexture.Textures[0]))
	}

	gvs.gvsUploaded = 0
	for i := range gv.gvPlanes {

		linesize := glwVideoLinesize(gvs.gvsWidth[i], gv.gvTexBytesPerPixel)

		gvs.gvsSize[i] = linesize * gvs.gvsHeight[i]
		// C: assert(gvs->gvs_size[i] > 0)

		C.glBindBuffer(C.GL_PIXEL_UNPACK_BUFFER, C.GLuint(gvs.gvsPbo[i]))
		C.glBufferData(C.GL_PIXEL_UNPACK_BUFFER,
			C.GLsizeiptr(gvs.gvsSize[i]), nil, C.GL_STREAM_DRAW)
		gvs.gvsData[i] = unsafe.Pointer(C.glMapBuffer(C.GL_PIXEL_UNPACK_BUFFER, C.GL_WRITE_ONLY))
		// C: assert(gvs->gvs_data[i] != NULL)
	}
	C.glBindBuffer(C.GL_PIXEL_UNPACK_BUFFER, 0)
	gvsTAILQInsertTail(&gv.gvAvailQueue, gvs)
	gv.gvAvailQueueCond.Signal()
}

// C: static void make_surfaces_available (glw_video_opengl.c:146-153)
func videoOglMakeSurfacesAvailable(gv *GlwVideo) {
	for i := range glwVideoNumSurfaces {
		gvs := &gv.gvSurfaces[i]
		gvsTAILQInsertTail(&gv.gvAvailQueue, gvs)
	}
}

// C: void glw_video_opengl_load_uniforms (glw_video_opengl.c:159-171)
func glwVideoOpenglLoadUniforms(gr *glwRoot, gp *glwProgram, args any,
	rj *GlwRenderJob) {
	gv := args.(*GlwVideo)

	if gp.gpUniformBlend != -1 {
		C.glUniform1f(C.GLint(gp.gpUniformBlend), C.GLfloat(gv.gvBlend))
	}

	if gp.gpUniformColormtx != -1 {
		C.glUniformMatrix4fv(C.GLint(gp.gpUniformColormtx), 1, 0,
			(*C.GLfloat)(&gv.gvCmatrixCur[0]))
	}
}

// C: static void load_texture_yuv (glw_video_opengl.c:177-199)
func videoOglLoadTextureYuv(gr *glwRoot, gp *glwProgram, args any,
	t *GlwBackendTexture, num int) {
	if num == 1 {
		C.glActiveTexture(C.GL_TEXTURE5)
		C.glBindTexture(C.GL_TEXTURE_2D, C.GLuint(t.Textures[2]))
		C.glActiveTexture(C.GL_TEXTURE4)
		C.glBindTexture(C.GL_TEXTURE_2D, C.GLuint(t.Textures[1]))
		C.glActiveTexture(C.GL_TEXTURE3)
		C.glBindTexture(C.GL_TEXTURE_2D, C.GLuint(t.Textures[0]))
		C.glActiveTexture(C.GL_TEXTURE0) // We must exit with unit 0 active
	} else {
		C.glActiveTexture(C.GL_TEXTURE2)
		C.glBindTexture(C.GL_TEXTURE_2D, C.GLuint(t.Textures[2]))
		C.glActiveTexture(C.GL_TEXTURE1)
		C.glBindTexture(C.GL_TEXTURE_2D, C.GLuint(t.Textures[1]))
		C.glActiveTexture(C.GL_TEXTURE0)
		C.glBindTexture(C.GL_TEXTURE_2D, C.GLuint(t.Textures[0]))
	}
}

// C: static int yuvp_init (glw_video_opengl.c:205-222)
func videoOglYuvpStart(gv *GlwVideo) int {
	gv.gvGpa.GpaAux = gv
	gv.gvGpa.GpaLoadUniforms = glwVideoOpenglLoadUniforms
	gv.gvGpa.GpaLoadTexture = videoOglLoadTextureYuv

	gv.gvPlanes = 3

	gv.gvTexInternalFormat = 1 // C: 1 (legacy GL_LUMINANCE internal)
	gv.gvTexFormat = C.GL_LUMINANCE
	gv.gvTexType = C.GL_UNSIGNED_BYTE
	gv.gvTexBytesPerPixel = 1

	for i := range gv.gvCmatrixCur {
		gv.gvCmatrixCur[i] = 0
	}
	videoOglMakeSurfacesAvailable(gv)
	return 0
}

// C: static void gv_set_tex_meta (glw_video_opengl.c:228-235)
func gvSetTexMeta() {
	C.glTexParameteri(C.GL_TEXTURE_2D, C.GL_TEXTURE_MAG_FILTER, C.GL_LINEAR)
	C.glTexParameteri(C.GL_TEXTURE_2D, C.GL_TEXTURE_MIN_FILTER, C.GL_LINEAR)
	C.glTexParameteri(C.GL_TEXTURE_2D, C.GL_TEXTURE_WRAP_S, C.GL_CLAMP_TO_EDGE)
	C.glTexParameteri(C.GL_TEXTURE_2D, C.GL_TEXTURE_WRAP_T, C.GL_CLAMP_TO_EDGE)
}

// C: static GLuint gv_tex_get (glw_video_opengl.c:241-245)
func gvTexGet(gvs *glwVideoSurface, plane int) uint32 {
	return gvs.gvsTexture.Textures[plane]
}

// C: static void gv_surface_pixmap_upload (glw_video_opengl.c:251-272)
func gvSurfacePixmapUpload(gvs *glwVideoSurface, gv *GlwVideo) {
	if gvs.gvsUploaded != 0 || gvs.gvsPbo[0] == 0 {
		return
	}

	gvs.gvsUploaded = 1

	for i := range gv.gvPlanes {
		C.glBindBuffer(C.GL_PIXEL_UNPACK_BUFFER, C.GLuint(gvs.gvsPbo[i]))
		C.glUnmapBuffer(C.GL_PIXEL_UNPACK_BUFFER)
		C.glBindTexture(C.GL_TEXTURE_2D, C.GLuint(gvTexGet(gvs, i)))
		gvSetTexMeta()
		C.glTexImage2D(C.GL_TEXTURE_2D, 0, C.GLint(gv.gvTexInternalFormat),
			C.GLsizei(gvs.gvsWidth[i]), C.GLsizei(gvs.gvsHeight[i]),
			0, C.GLenum(gv.gvTexFormat), C.GLenum(gv.gvTexType), nil)
		gvs.gvsData[i] = nil
	}

	C.glBindBuffer(C.GL_PIXEL_UNPACK_BUFFER, 0)
}

// C: static void gv_surface_pixmap_release (glw_video_opengl.c:278-310)
func gvSurfacePixmapRelease(gv *GlwVideo, gvs *glwVideoSurface,
	fromqueue *glwVideoSurfaceQueue) {
	// C: assert(gvs != gv->gv_sa); assert(gvs != gv->gv_sb)

	gvsTAILQRemove(fromqueue, gvs)

	if gvs.gvsUploaded != 0 {
		gvs.gvsUploaded = 0

		for i := range gv.gvPlanes {
			C.glBindBuffer(C.GL_PIXEL_UNPACK_BUFFER, C.GLuint(gvs.gvsPbo[i]))

			// Setting the buffer to NULL tells the GPU it can assign
			// us another piece of memory as backing store.
			// PBO_RELEASE_BEFORE_MAP
			C.glBufferData(C.GL_PIXEL_UNPACK_BUFFER,
				C.GLsizeiptr(gvs.gvsSize[i]), nil, C.GL_STREAM_DRAW)

			gvs.gvsData[i] = unsafe.Pointer(C.glMapBuffer(C.GL_PIXEL_UNPACK_BUFFER,
				C.GL_WRITE_ONLY))
			// C: assert(gvs->gvs_data[i] != NULL)
		}
		C.glBindBuffer(C.GL_PIXEL_UNPACK_BUFFER, 0)
	}

	gvsTAILQInsertTail(&gv.gvAvailQueue, gvs)
	gv.gvAvailQueueCond.Signal()
}

// C: static const float cmatrix_ITUR_BT_601[16] (glw_video_opengl.c:313-318)
var cmatrixITURBT601 = [16]float32{
	1.164400, 1.164400, 1.164400, 0,
	0.000000, -0.391800, 2.017200, 0,
	1.596000, -0.813000, 0.000000, 0,
	-0.874190, 0.531702, -1.085616, 1,
}

// C: static const float cmatrix_ITUR_BT_709[16] (glw_video_opengl.c:320-325)
var cmatrixITURBT709 = [16]float32{
	1.164400, 1.164400, 1.164400, 0,
	0.000000, -0.213200, 2.112400, 0,
	1.792700, -0.532900, 0.000000, 0,
	-0.972926, 0.301453, -1.133402, 1,
}

// C: static const float cmatrix_SMPTE_240M[16] (glw_video_opengl.c:327-332)
var cmatrixSMPTE240M = [16]float32{
	1.164400, 1.164400, 1.164400, 0,
	0.000000, -0.257800, 2.078700, 0,
	1.793900, -0.542500, 0.000000, 0,
	-0.973528, 0.328659, -1.116486, 1,
}

// C: static void gv_color_matrix_set (glw_video_opengl.c:339-363)
func gvColorMatrixSet(gv *GlwVideo, fi *mediacore.FrameInfo) {
	var f *[16]float32

	switch fi.ColorSpace {
	case medialibav.ColorSpaceBT709:
		f = &cmatrixITURBT709

	case medialibav.ColorSpaceBT601:
		f = &cmatrixITURBT601

	case medialibav.ColorSpaceSMPTE240:
		f = &cmatrixSMPTE240M

	default:
		if fi.Height < 720 {
			f = &cmatrixITURBT601
		} else {
			f = &cmatrixITURBT709
		}
	}

	gv.gvCmatrixTgt = *f
}

// C: static void gv_color_matrix_update (glw_video_opengl.c:366-373)
func gvColorMatrixUpdate(gv *GlwVideo) {
	for i := range 16 {
		gv.gvCmatrixCur[i] = (gv.gvCmatrixCur[i]*3.0 +
			gv.gvCmatrixTgt[i]) / 4.0
	}
}

// C: static int64_t video_opengl_newframe (glw_video_opengl.c:380-396)
func videoOglNewframe(gv *GlwVideo, vd *decoder.VideoDecoder, flags int) int64 {
	// C: hts_mutex_assert(&gv->gv_surface_mutex)

	for gvs := gv.gvParkedQueue.tqhFirst; gvs != nil; {
		gvsTAILQRemove(&gv.gvParkedQueue, gvs)
		videoOglSurfaceSetup(gv, gvs)
		gvs = gv.gvParkedQueue.tqhFirst
	}

	glwNeedRefresh(gv.w.glwRoot, 0)

	gvColorMatrixUpdate(gv)
	return glwVideoNewframeBlend(gv, vd, flags, gvSurfacePixmapRelease, 1)
}

// C: static void video_opengl_render (glw_video_opengl.c:402-460)
func videoOglRender(gv *GlwVideo, rc *glwRctx) {
	gr := gv.w.glwRoot
	sa := gv.gvSa
	sb := gv.gvSb
	var gp *glwProgram
	gbr := &gr.grBe

	if sa == nil {
		return
	}

	gv.gvWidth = sa.gvsWidth[0]
	gv.gvHeight = sa.gvsHeight[0]

	// Upload textures
	gvSurfacePixmapUpload(sa, gv)

	// C: (-0.5 * sa->gvs_yshift) / (float)sa->gvs_height[0] — the
	// division happens in double, the result is narrowed to float.
	yshiftA := float32(-0.5 * float64(sa.gvsYshift) / float64(sa.gvsHeight[0]))

	glwRendererVtxSt(&gv.gvQuad, 0, 0, 1+yshiftA)
	glwRendererVtxSt(&gv.gvQuad, 1, 1, 1+yshiftA)
	glwRendererVtxSt(&gv.gvQuad, 2, 1, 0+yshiftA)
	glwRendererVtxSt(&gv.gvQuad, 3, 0, 0+yshiftA)

	if sb != nil {
		// Two pictures that should be mixed
		gvSurfacePixmapUpload(sb, gv)

		if gv.gvPlanes == 3 {
			gp = gbr.gbrYuv2rgb2f
		} else {
			gp = gbr.gbrRgb2rgb2f
		}

		yshiftB := float32(-0.5 * float64(sb.gvsYshift) / float64(sb.gvsHeight[0]))

		glwRendererVtxSt2(&gv.gvQuad, 0, 0, 1+yshiftB)
		glwRendererVtxSt2(&gv.gvQuad, 1, 1, 1+yshiftB)
		glwRendererVtxSt2(&gv.gvQuad, 2, 1, 0+yshiftB)
		glwRendererVtxSt2(&gv.gvQuad, 3, 0, 0+yshiftB)

	} else {

		// One picture
		if gv.gvPlanes == 3 {
			gp = gbr.gbrYuv2rgb1f
		} else {
			gp = gbr.gbrRgb2rgb1f
		}
	}

	gv.gvGpa.GpaProg = gp

	glwRendererDraw(&gv.gvQuad, gr, rc,
		&sa.gvsTexture,
		sbTexture(sb),
		nil, nil,
		rc.rcAlpha*gv.w.glwAlpha, 0, &gv.gvGpa)
}

func sbTexture(sb *glwVideoSurface) *GlwBackendTexture {
	if sb != nil {
		return &sb.gvsTexture
	}
	return nil
}

// C: static void yuvp_blackout (glw_video_opengl.c:466-470)
func videoOglYuvpBlackout(gv *GlwVideo) {
	for i := range gv.gvCmatrixTgt {
		gv.gvCmatrixTgt[i] = 0
	}
}

// C: static int yuvp_deliver (glw_video_opengl.c:476-567)
func videoOglYuvpDeliver(fi *mediacore.FrameInfo, gv *GlwVideo,
	gve *glwVideoEngine) int {
	var hvec, wvec [3]int
	var i, h, w int
	var src []byte
	var dst unsafe.Pointer
	var tff int
	hshift := fi.HShift
	vshift := fi.VShift
	var s *glwVideoSurface
	const parity = 0
	pts := fi.PTS

	var interlaced int
	if fi.Interlaced {
		interlaced = 1
	}

	wvec[0] = fi.Width
	wvec[1] = fi.Width >> hshift
	wvec[2] = fi.Width >> hshift
	hvec[0] = fi.Height >> interlaced
	hvec[1] = fi.Height >> (vshift + interlaced)
	hvec[2] = fi.Height >> (vshift + interlaced)

	glwVideoConfigure(gv, gve)

	gvColorMatrixSet(gv, fi)

	if s = glwVideoGetSurface(gv, &wvec, &hvec); s == nil {
		return -1
	}

	if !fi.Interlaced {

		for i = range 3 {
			w = wvec[i]
			h = hvec[i]
			src = fi.Data[i]
			dst = s.gvsData[i]
			// C: assert(dst != NULL)

			linesize := glwVideoLinesize(w, 1)

			doff := 0
			soff := 0
			for h > 0 {
				h--
				// C: memcpy(dst + doff, src + soff, w)
				copy(unsafe.Slice((*byte)(unsafe.Add(dst, doff)), w),
					src[soff:soff+w])
				doff += linesize
				soff += fi.Pitch[i]
			}
		}

		glwVideoPutSurface(gv, s, pts, fi.Epoch, int(fi.Duration), 0, 0)

	} else {

		duration := int(fi.Duration) >> 1

		var fiTff int
		if fi.TFF {
			fiTff = 1
		}
		tff = fiTff ^ parity

		for i = range 3 {
			w = wvec[i]
			h = hvec[i]

			src = fi.Data[i]
			dst = s.gvsData[i]
			linesize := glwVideoLinesize(w, 1)
			doff := 0
			soff := 0
			for h > 0 {
				h--
				copy(unsafe.Slice((*byte)(unsafe.Add(dst, doff)), w),
					src[soff:soff+w])
				doff += linesize
				soff += fi.Pitch[i] * 2
			}
		}
		glwVideoPutSurface(gv, s, pts, fi.Epoch, duration, 1, 1-tff)

		if s = glwVideoGetSurface(gv, &wvec, &hvec); s == nil {
			return -1
		}

		for i = range 3 {
			w = wvec[i]
			h = hvec[i]

			src = fi.Data[i]
			dst = s.gvsData[i]
			linesize := glwVideoLinesize(w, 1)
			doff := 0
			soff := fi.Pitch[i]
			for h > 0 {
				h--
				copy(unsafe.Slice((*byte)(unsafe.Add(dst, doff)), w),
					src[soff:soff+w])
				doff += linesize
				soff += fi.Pitch[i] * 2
			}
		}

		if pts != mediacore.PTSUnset {
			pts += int64(duration)
		}

		glwVideoPutSurface(gv, s, pts, fi.Epoch, duration, 1, tff)
	}
	return 0
}

// C: static glw_video_engine_t glw_video_opengl (glw_video_opengl.c:573-581)
var glwVideoOglEngine = glwVideoEngine{
	gveType:     mediacore.FourCCYUVP, // C: 'YUVP'
	gveNewframe: videoOglNewframe,
	gveRender:   videoOglRender,
	gveReset:    videoOglReset,
	gveStart:    videoOglYuvpStart,
	gveDeliver:  videoOglYuvpDeliver,
	gveBlackout: videoOglYuvpBlackout,
}

// C: GLW_REGISTER_GVE(glw_video_opengl) (glw_video_opengl.c:583)
func init() { glwRegisterVideoEngine(&glwVideoOglEngine) }

// C: static int bgr_init (glw_video_opengl.c:589-604)
func videoOglBgrStart(gv *GlwVideo) int {
	gv.gvGpa.GpaAux = gv
	gv.gvGpa.GpaLoadUniforms = glwVideoOpenglLoadUniforms

	gv.gvPlanes = 1

	gv.gvTexInternalFormat = C.GL_RGBA8
	gv.gvTexFormat = C.GL_BGR
	gv.gvTexType = C.GL_UNSIGNED_BYTE
	gv.gvTexBytesPerPixel = 3

	videoOglMakeSurfacesAvailable(gv)
	return 0
}

// C: static int bgr_deliver (glw_video_opengl.c:610-638)
func videoOglBgrDeliver(fi *mediacore.FrameInfo, gv *GlwVideo,
	gve *glwVideoEngine) int {
	var s *glwVideoSurface
	pts := fi.PTS
	var wvec, hvec [3]int

	wvec[0] = fi.Width
	hvec[0] = fi.Height

	glwVideoConfigure(gv, gve)

	if s = glwVideoGetSurface(gv, &wvec, &hvec); s == nil {
		return -1
	}

	linesize := glwVideoLinesize(fi.Width, 3)

	src := fi.Data[0]
	dst := unsafe.Slice((*byte)(s.gvsData[0]), linesize*fi.Height)
	soff := 0
	for y := range fi.Height {
		copy(dst[y*linesize:y*linesize+linesize],
			src[soff:soff+linesize])
		soff += fi.Pitch[0]
	}

	glwVideoPutSurface(gv, s, pts, fi.Epoch, int(fi.Duration), 0, 0)
	return 0
}

// C: static glw_video_engine_t glw_video_BGR (glw_video_opengl.c:644-651)
var glwVideoBgrEngine = glwVideoEngine{
	gveType:     mediacore.FourCCBGR, // C: 'BGR' (3-char literal)
	gveNewframe: videoOglNewframe,
	gveRender:   videoOglRender,
	gveReset:    videoOglReset,
	gveStart:    videoOglBgrStart,
	gveDeliver:  videoOglBgrDeliver,
}

// C: GLW_REGISTER_GVE(glw_video_BGR) (glw_video_opengl.c:653)
func init() { glwRegisterVideoEngine(&glwVideoBgrEngine) }

// C: static int xyz_init (glw_video_opengl.c:658-673)
func videoOglXyzStart(gv *GlwVideo) int {
	gv.gvGpa.GpaAux = gv
	gv.gvGpa.GpaLoadUniforms = glwVideoOpenglLoadUniforms

	gv.gvPlanes = 1

	gv.gvTexInternalFormat = C.GL_SRGB
	gv.gvTexFormat = C.GL_RGB
	gv.gvTexType = C.GL_UNSIGNED_SHORT
	gv.gvTexBytesPerPixel = 6

	videoOglMakeSurfacesAvailable(gv)
	return 0
}

// C: static int xyz_deliver (glw_video_opengl.c:679-707)
func videoOglXyzDeliver(fi *mediacore.FrameInfo, gv *GlwVideo,
	gve *glwVideoEngine) int {
	var s *glwVideoSurface
	pts := fi.PTS
	var wvec, hvec [3]int

	wvec[0] = fi.Width
	hvec[0] = fi.Height

	glwVideoConfigure(gv, gve)

	if s = glwVideoGetSurface(gv, &wvec, &hvec); s == nil {
		return -1
	}

	linesize := glwVideoLinesize(fi.Width, 6)

	src := fi.Data[0]
	dst := unsafe.Slice((*byte)(s.gvsData[0]), linesize*fi.Height)
	copybytes := fi.Width * 6
	soff := 0
	for y := range fi.Height {
		copy(dst[y*linesize:y*linesize+copybytes],
			src[soff:soff+copybytes])
		soff += fi.Pitch[0]
	}
	glwVideoPutSurface(gv, s, pts, fi.Epoch, int(fi.Duration), 0, 0)
	return 0
}

// C: static glw_video_engine_t glw_video_XYZ (glw_video_opengl.c:713-720)
var glwVideoXyzEngine = glwVideoEngine{
	gveType:     mediacore.FourCCXYZ6, // C: 'XYZ6'
	gveNewframe: videoOglNewframe,
	gveRender:   videoOglRender,
	gveReset:    videoOglReset,
	gveStart:    videoOglXyzStart,
	gveDeliver:  videoOglXyzDeliver,
}

// C: GLW_REGISTER_GVE(glw_video_XYZ) (glw_video_opengl.c:722)
func init() { glwRegisterVideoEngine(&glwVideoXyzEngine) }
