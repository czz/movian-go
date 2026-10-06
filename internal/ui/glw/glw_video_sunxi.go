//go:build sunxi

// glw_video_sunxi.go — C: src/ui/glw/glw_video_sunxi.c — the three sunxi
// video engines (YUVP planar copy, CEDR zero-copy cedar frames, CED2
// refcounted frames) rendering through the disp layer instead of GL.
package glw

/*

#include <stdlib.h>
#include <string.h>
#include <stdint.h>
#include <linux/types.h>
#include "drv_display_sun4i.h"
*/
import "C"

import (
	"fmt"
	"unsafe"

	archsunxi "github.com/czz/movian-go/internal/arch/sunxi"
	"github.com/czz/movian-go/internal/media"
	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/trace"
	decoder "github.com/czz/movian-go/internal/video/decoder"
)

const (
	dispmanVideoYUV420P = 1 // C: DISPMAN_VIDEO_YUV420P
	dispmanVideoCedar   = 2 // C: DISPMAN_VIDEO_CEDAR
)

// dispmanVideo — C: dispman_video_t (glw_video_sunxi.c:36-50)
type dispmanVideo struct {
	layer   int  // C: dv_layer
	idgen   int  // C: dv_idgen
	running bool // C: dv_running

	// Current mode of scaler
	width  int // C: dv_width
	height int // C: dv_height
	format int // C: dv_format

	lastFrameStart int64 // C: last_frame_start
	lastAclock     int64 // C: last_aclock
}

// surfaceFree — C: surface_free (glw_video_sunxi.c:53-66)
func surfaceFree(gv *GlwVideo, gvs *glwVideoSurface) {
	archsunxi.GfxmemLock()
	archsunxi.GfxmemFree(unsafe.Pointer(gvs.gvsData[0]))
	archsunxi.GfxmemFree(unsafe.Pointer(gvs.gvsData[1]))
	archsunxi.GfxmemFree(unsafe.Pointer(gvs.gvsData[2]))
	archsunxi.GfxmemUnlock()
	gvs.gvsData[0] = nil
	gvs.gvsData[1] = nil
	gvs.gvsData[2] = nil
}

// videoResetCommon — C: video_reset_common (glw_video_sunxi.c:72-90)
func videoResetCommon(gv *GlwVideo) {
	dv := (*dispmanVideo)(gv.gvAux)
	var args [4]uintptr
	args[1] = uintptr(dv.layer)
	if dv.layer < 100 || dv.layer >= 105 {
		panic("sunxi video layer out of range")
	}
	archsunxi.DispIoctl(C.DISP_CMD_VIDEO_STOP, &args)
	archsunxi.DispIoctl(C.DISP_CMD_LAYER_CLOSE, &args)
	archsunxi.DispIoctl(C.DISP_CMD_LAYER_RELEASE, &args)

	glwDeps.ts.Trace(trace.TRACE_DEBUG, "GLW", "%s: Released layer %d",
		gv.gvMp.Name, dv.layer)
	gv.gvAux = nil
}

// videoResetFree — C: video_reset_free (glw_video_sunxi.c:96-103)
func videoResetFree(gv *GlwVideo) {
	for i := 0; i < glwVideoMaxSurfaces; i++ {
		surfaceFree(gv, &gv.gvSurfaces[i])
	}
	videoResetCommon(gv)
}

// videoResetCedar — C: video_reset_cedar (glw_video_sunxi.c:106-117)
func videoResetCedar(gv *GlwVideo) {
	for i := 0; i < glwVideoMaxSurfaces; i++ {
		if gv.gvSurfaces[i].gvsOpaque != nil {
			archsunxi.CedarFrameDone(gv.gvSurfaces[i].gvsOpaque)
			gv.gvSurfaces[i].gvsOpaque = nil
		}
	}
	videoResetCommon(gv)
}

// cedar2FrameRelease — C: cedar2_frame_release (glw_video_sunxi.c:120-131)
func cedar2FrameRelease(gvs *glwVideoSurface) {
	if gvs.gvsData[2] == nil {
		return
	}

	refop := (*func(unsafe.Pointer, int))(gvs.gvsOpaque)

	(*refop)(unsafe.Pointer(gvs.gvsData[2]), -1)
	gvs.gvsData[2] = nil
}

// videoResetCedar2 — C: video_reset_cedar2 (glw_video_sunxi.c:134-143)
func videoResetCedar2(gv *GlwVideo) {
	for i := 0; i < glwVideoMaxSurfaces; i++ {
		cedar2FrameRelease(&gv.gvSurfaces[i])
	}
	videoResetCommon(gv)
}

// videoSetParam — C: video_set_param (glw_video_sunxi.c:146-229)
func videoSetParam(dv *dispmanVideo, gvs *glwVideoSurface,
	mp *mediacore.MediaPipe) {
	var args [4]uintptr
	var l C.__disp_layer_info_t

	if gvs.gvsWidth[0] == dv.width &&
		gvs.gvsHeight[0] == dv.height &&
		gvs.gvsFormat == dv.format {
		return
	}

	dv.width = gvs.gvsWidth[0]
	dv.height = gvs.gvsHeight[0]
	dv.format = gvs.gvsFormat

	l.mode = C.DISP_LAYER_WORK_MODE_SCALER
	l.pipe = 1

	l.fb.size.width = C.__u32(gvs.gvsWidth[0])
	l.fb.size.height = C.__u32(gvs.gvsHeight[0])
	l.fb.br_swap = 0

	switch gvs.gvsFormat {
	case dispmanVideoYUV420P:
		l.fb.mode = C.DISP_MOD_NON_MB_PLANAR
		l.fb.format = C.DISP_FORMAT_YUV420

	case dispmanVideoCedar:
		l.fb.mode = C.DISP_MOD_MB_UV_COMBINED
		l.fb.format = C.DISP_FORMAT_YUV420
		l.fb.seq = C.DISP_SEQ_UVUV

	default:
		panic("sunxi video: bad format")
	}

	l.ck_enable = 0
	l.alpha_en = 0
	l.alpha_val = 0xff
	l.src_win.x = 0
	l.src_win.y = 0
	l.src_win.width = C.__u32(gvs.gvsWidth[0])
	l.src_win.height = C.__u32(gvs.gvsHeight[0])
	l.scn_win.x = 0
	l.scn_win.y = 0
	l.scn_win.width = 1280 // HUH
	l.scn_win.height = 720

	glwDeps.ts.Trace(trace.TRACE_DEBUG, "GLW", "%s: Video surface set to %d x %d",
		mp.Name, int(l.src_win.width), int(l.src_win.height))

	args[1] = uintptr(dv.layer)
	args[2] = uintptr(unsafe.Pointer(&l))
	args[3] = 0
	if r := archsunxi.DispIoctl(C.DISP_CMD_LAYER_SET_PARA, &args); r != 0 {
		fmt.Println("ioctl(disphd,DISP_CMD_LAYER_SET_PARA)")
	}
}

// videoStart — C: video_init (glw_video_sunxi.c:235-265)
func videoStart(gv *GlwVideo) int {
	var args [4]uintptr
	scr := 0

	args[0] = uintptr(scr)
	args[1] = C.DISP_LAYER_WORK_MODE_SCALER
	hlay := archsunxi.DispIoctl(C.DISP_CMD_LAYER_REQUEST, &args)
	if hlay == -1 {
		return -1
	}

	dv := &dispmanVideo{}

	gv.gvAux = unsafe.Pointer(dv)

	for i := 0; i < 3; i++ {
		gvs := &gv.gvSurfaces[i]
		gvs.gvsData[0] = nil
		gvs.gvsData[1] = nil
		gvs.gvsData[2] = nil
		gvsTAILQInsertTail(&gv.gvAvailQueue, gvs)
	}

	dv.layer = hlay
	glwDeps.ts.Trace(trace.TRACE_INFO, "GLW", "%s: Got layer %d for output",
		gv.gvMp.Name, hlay)

	return 0
}

// videoNewframe — C: video_newframe (glw_video_sunxi.c:271-415)
func videoNewframe(gv *GlwVideo, vd *decoder.VideoDecoder,
	flags int) int64 {
	gr := gv.w.glwRoot
	var s *glwVideoSurface
	var args [4]uintptr
	mp := gv.gvMp
	pts := mediacore.PTSUnset
	dv := (*dispmanVideo)(gv.gvAux)

	if dv.layer != 0 {

		args[1] = uintptr(dv.layer)

		curnr := archsunxi.DispIoctl(C.DISP_CMD_VIDEO_GET_FRAME_ID, &args)
		// Remove frames if they are idle and push back to the decoder
		for {
			s = gv.gvDisplayingQueue.tqhFirst
			if s == nil {
				break
			}
			if s.gvsID >= curnr {
				break
			}
			gvsTAILQRemove(&gv.gvDisplayingQueue, s)
			gvsTAILQInsertTail(&gv.gvAvailQueue, s)
			gv.gvAvailQueueCond.Signal()
		}
	}

	mp.ClockMutex.Lock()
	aclock := mp.AudioClock + gr.grFrameStartAvtime -
		mp.AudioClockAvtime + mp.AVDelta

	aclockValid := mp.AudioClockEpoch != 0

	aepoch := mp.AudioClockEpoch

	mp.ClockMutex.Unlock()

	for {
		s = gv.gvDecodedQueue.tqhFirst
		if s == nil {
			break
		}
		delta := int64(gr.grFrameduration * 2)
		var d int64
		pts = s.gvsPts
		epoch := s.gvsEpoch

		d = s.gvsPts - aclock

		if gv.gvNextptsEpoch == epoch &&
			(pts == mediacore.PTSUnset || d < -5000000 || d > 5000000) {
			pts = gv.gvNextpts
		}

		if pts != mediacore.PTSUnset && (pts-delta) >= aclock &&
			aclockValid {

			if media.EnableDetailedAvdiff(glwDeps.gconf) {
				glwDeps.ts.Trace(trace.TRACE_DEBUG, "AVDIFF",
					"%s: Not sending frame %d:%d %d:%d diff:%d\n",
					mp.Name, s.gvsEpoch, pts-delta,
					aepoch, aclock, (pts-delta)-aclock)
			}

			break
		} else {
			if media.EnableDetailedAvdiff(glwDeps.gconf) {
				glwDeps.ts.Trace(trace.TRACE_DEBUG, "AVDIFF",
					"%s:     Sending frame %d:%d %d:%d\n",
					mp.Name, s.gvsEpoch, pts-delta,
					aepoch, aclock)
			}
		}
		videoSetParam(dv, s, mp)

		if !dv.running {

			args[1] = uintptr(dv.layer)
			if archsunxi.DispIoctl(C.DISP_CMD_LAYER_OPEN, &args) != 0 {
				glwDeps.ts.Trace(trace.TRACE_ERROR, "GLW",
					"%s: Failed to open layer %d",
					mp.Name, dv.layer)
				return mediacore.PTSUnset
			}

			args[1] = uintptr(dv.layer)
			if archsunxi.DispIoctl(C.DISP_CMD_LAYER_BOTTOM, &args) != 0 {
				glwDeps.ts.Trace(trace.TRACE_ERROR, "GLW",
					"%s: Failed to set layer %d zorder",
					mp.Name, dv.layer)
				archsunxi.DispIoctl(C.DISP_CMD_LAYER_CLOSE, &args)
				return mediacore.PTSUnset
			}

			args[1] = uintptr(dv.layer)
			if archsunxi.DispIoctl(C.DISP_CMD_VIDEO_START, &args) != 0 {
				glwDeps.ts.Trace(trace.TRACE_ERROR, "GLW",
					"%s: Failed to start video on layer %d",
					mp.Name, dv.layer)
				archsunxi.DispIoctl(C.DISP_CMD_LAYER_CLOSE, &args)
				return mediacore.PTSUnset
			}
		}

		var frmbuf C.__disp_video_fb_t
		frmbuf.interlace = 0
		frmbuf.top_field_first = 0
		frmbuf.addr[0] = C.__u32(archsunxi.VaToPa(
			unsafe.Pointer(s.gvsData[0])))
		frmbuf.addr[1] = C.__u32(archsunxi.VaToPa(
			unsafe.Pointer(s.gvsData[1])))
		frmbuf.addr[2] = C.__u32(archsunxi.VaToPa(
			unsafe.Pointer(s.gvsData[2])))
		frmbuf.id = C.__s32(s.gvsID)

		args[1] = uintptr(dv.layer)

		args[2] = uintptr(unsafe.Pointer(&frmbuf))

		if archsunxi.DispIoctl(C.DISP_CMD_VIDEO_SET_FB, &args) != 0 {
			glwDeps.ts.Trace(trace.TRACE_ERROR, "GLW",
				"%s: Failed to set FB on layer %d",
				mp.Name, dv.layer)
			args[2] = 0
			archsunxi.DispIoctl(C.DISP_CMD_LAYER_CLOSE, &args)
			return mediacore.PTSUnset
		}

		dv.running = true

		if pts != mediacore.PTSUnset {
			gv.gvNextpts = pts + int64(s.gvsDuration)
			gv.gvNextptsEpoch = epoch
		}

		gv.gvWidth = s.gvsWidth[0]
		gv.gvHeight = s.gvsHeight[0]

		gvsTAILQRemove(&gv.gvDecodedQueue, s)
		gvsTAILQInsertTail(&gv.gvDisplayingQueue, s)
	}
	return pts
}

// videoRender — C: video_render (glw_video_sunxi.c:421-446)
func videoRender(gv *GlwVideo, rc *glwRctx) {
	gv.gvSurfaceMutex.Lock()

	dv := (*dispmanVideo)(gv.gvAux)

	if dv.layer != 0 &&
		gv.gvRect.x2 > gv.gvRect.x1 &&
		gv.gvRect.y2 > gv.gvRect.y1 {

		var rect C.__disp_rect_t
		rect.x = C.__s32(gv.gvRect.x1)
		rect.y = C.__s32(gv.gvRect.y1)
		rect.width = C.__u32(gv.gvRect.x2 - gv.gvRect.x1)
		rect.height = C.__u32(gv.gvRect.y2 - gv.gvRect.y1)

		var args [4]uintptr
		args[0] = 0
		args[1] = uintptr(dv.layer)
		args[2] = uintptr(unsafe.Pointer(&rect))

		archsunxi.DispIoctl(C.DISP_CMD_LAYER_SET_SCN_WINDOW, &args)
	}
	gv.gvSurfaceMutex.Unlock()
}

// ---------------------------------------------------------------------------
// C: glw_video_sunxi engine 'YUVP' (glw_video_sunxi.c:454-463)
// ---------------------------------------------------------------------------

var glwVideoSunxi = &glwVideoEngine{
	gveType:     mediacore.FourCCYUVP, // C: 'YUVP'
	gveNewframe: videoNewframe,
	gveRender:   videoRender,
	gveReset:    videoResetFree,
	gveStart:    videoStart,
	gveDeliver:  deliverYuvp,
}

// deliverYuvp — C: deliver_yuvp (glw_video_sunxi.c:468-510)
func deliverYuvp(fi *mediacore.FrameInfo, gv *GlwVideo,
	gve *glwVideoEngine) int {
	if glwVideoConfigure(gv, gve) != 0 {
		return 0
	}

	gvs := glwVideoGetSurface(gv, nil, nil)
	if gvs == nil {
		return 0
	}

	surfaceFree(gv, gvs)
	dv := (*dispmanVideo)(gv.gvAux)

	dv.idgen++
	gvs.gvsID = dv.idgen
	gvs.gvsFormat = dispmanVideoYUV420P
	gvs.gvsWidth[0] = fi.Width
	gvs.gvsHeight[0] = fi.Height

	for i := 0; i < 3; i++ {
		width := fi.Width
		height := fi.Height
		if i != 0 {
			width >>= fi.HShift
			height >>= fi.VShift
		}

		archsunxi.GfxmemLock()
		dst := archsunxi.GfxmemMemalign(1024, width*height)
		archsunxi.GfxmemUnlock()
		gvs.gvsData[i] = dst
		src := fi.Data[i]
		for y := 0; y < height; y++ {
			dstSlice := unsafe.Slice((*byte)(
				unsafe.Pointer(uintptr(dst)+
					uintptr(y*width))), width)
			copy(dstSlice, src[y*fi.Pitch[i]:y*fi.Pitch[i]+width])
		}
	}

	glwVideoPutSurface(gv, gvs, fi.PTS, fi.Epoch,
		int(fi.Duration), 0, 0)
	return 0
}

// ---------------------------------------------------------------------------
// C: glw_video_sunxi_cedar engine 'CEDR' (glw_video_sunxi.c:517-526)
// ---------------------------------------------------------------------------

var glwVideoSunxiCedar = &glwVideoEngine{
	gveType:     mediacore.FourCCCEDR, // C: 'CEDR'
	gveNewframe: videoNewframe,
	gveRender:   videoRender,
	gveReset:    videoResetCedar,
	gveStart:    videoStart,
	gveDeliver:  cedarDeliver,
}

// cedarDeliver — C: cedar_deliver (glw_video_sunxi.c:530-565)
func cedarDeliver(fi *mediacore.FrameInfo, gv *GlwVideo,
	gve *glwVideoEngine) int {
	if glwVideoConfigure(gv, gve) != 0 {
		archsunxi.CedarFrameDone(unsafe.Pointer(&fi.Data[3][0]))
		return 0
	}

	gvs := glwVideoGetSurface(gv, nil, nil)
	if gvs == nil {
		archsunxi.CedarFrameDone(unsafe.Pointer(&fi.Data[3][0]))
		return 0
	}

	if gvs.gvsOpaque != nil {
		archsunxi.CedarFrameDone(gvs.gvsOpaque)
		gvs.gvsOpaque = nil
	}

	dv := (*dispmanVideo)(gv.gvAux)

	dv.idgen++
	gvs.gvsID = dv.idgen
	gvs.gvsFormat = dispmanVideoCedar
	gvs.gvsWidth[0] = fi.Width
	gvs.gvsHeight[0] = fi.Height

	dv.idgen++
	gvs.gvsID = dv.idgen
	gvs.gvsData[0] = unsafe.Pointer(&fi.Data[0][0])
	gvs.gvsData[1] = unsafe.Pointer(&fi.Data[1][0])
	gvs.gvsData[2] = unsafe.Pointer(&fi.Data[2][0])
	gvs.gvsOpaque = unsafe.Pointer(&fi.Data[3][0])

	glwVideoPutSurface(gv, gvs, fi.PTS, fi.Epoch,
		int(fi.Duration), 0, 0)
	return 0
}

// ---------------------------------------------------------------------------
// C: glw_video_sunxi_cedar2 engine 'CED2' (glw_video_sunxi.c:575-584)
// ---------------------------------------------------------------------------

var glwVideoSunxiCedar2 = &glwVideoEngine{
	gveType:     mediacore.FourCCCED2, // C: 'CED2'
	gveNewframe: videoNewframe,
	gveRender:   videoRender,
	gveReset:    videoResetCedar2,
	gveStart:    videoStart,
	gveDeliver:  cedar2Deliver,
}

// cedar2Deliver — C: cedar2_deliver (glw_video_sunxi.c:586-617)
func cedar2Deliver(fi *mediacore.FrameInfo, gv *GlwVideo,
	gve *glwVideoEngine) int {
	if glwVideoConfigure(gv, gve) != 0 {
		return 0
	}

	gvs := glwVideoGetSurface(gv, nil, nil)
	if gvs == nil {
		return 0
	}

	cedar2FrameRelease(gvs)

	dv := (*dispmanVideo)(gv.gvAux)

	dv.idgen++
	gvs.gvsID = dv.idgen
	gvs.gvsFormat = dispmanVideoCedar
	gvs.gvsWidth[0] = fi.Width
	gvs.gvsHeight[0] = fi.Height

	dv.idgen++
	gvs.gvsID = dv.idgen
	gvs.gvsData[0] = unsafe.Pointer(&fi.Data[0][0])
	gvs.gvsData[1] = unsafe.Pointer(&fi.Data[1][0])
	gvs.gvsData[2] = unsafe.Pointer(&fi.Data[2][0])

	gvs.gvsOpaque = unsafe.Pointer(&fi.Refop)

	fi.Refop(unsafe.Pointer(gvs.gvsData[2]), 1)

	glwVideoPutSurface(gv, gvs, fi.PTS, fi.Epoch,
		int(fi.Duration), 0, 0)
	return 0
}

func init() {
	// C: GLW_REGISTER_GVE(glw_video_sunxi{,_cedar,_cedar2})
	glwRegisterVideoEngine(glwVideoSunxi)
	glwRegisterVideoEngine(glwVideoSunxiCedar)
	glwRegisterVideoEngine(glwVideoSunxiCedar2)
}
