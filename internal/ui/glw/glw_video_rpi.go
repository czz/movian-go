//go:build rpi

// glw_video_rpi.go — C: src/ui/glw/glw_video_rpi.c — two glw video
// engines: tunneled OpenMAX ('omx') and dispmanx YUV420 overlay ('YUVP').
package glw

/*
#cgo CFLAGS: -DOMX_SKIP64BIT -I${SRCDIR}/../../arch/rpi

#include <stdlib.h>
#include <string.h>
#include <OMX_Core.h>
#include <OMX_Broadcom.h>
#include "omx_shim.h"
#include <interface/vmcs_host/vc_dispmanx.h>
*/
import "C"

import (
	"fmt"
	"sync"
	"unsafe"

	"github.com/czz/movian-go/internal/arch/rpi"
	"github.com/czz/movian-go/internal/media"
	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/trace"
	decoder "github.com/czz/movian-go/internal/video/decoder"
)

// omxchkRpi — C: omxchk(fn) macro (omx.h) — local copy: C.OMX_* types
// differ per cgo package, so rpi.Omxchk can't take this file's handles.
func omxchkRpi(er C.OMX_ERRORTYPE, fn string) {
	if er == 0 {
		return
	}
	panic(fmt.Sprintf("%s: OMX Error 0x%x\n", fn, int(er)))
}

// omxHandle — bridge rpi.OmxComponent.Handle (per-package cgo type)
// into this file's OMX_HANDLETYPE.
func omxHandle(oc *rpi.OmxComponent) C.OMX_HANDLETYPE {
	return C.OMX_HANDLETYPE(unsafe.Pointer(oc.Handle))
}

// C: typedef struct omx_video_display (glw_video_rpi.c:43-70)
type omxVideoDisplay struct {
	vrender *rpi.OmxComponent // C: ovd_vrender
	vsched  *rpi.OmxComponent // C: ovd_vsched
	imgfx   *rpi.OmxComponent // C: ovd_imgfx

	tunClockVsched *rpi.OmxTunnel // C: ovd_tun_clock_vsched

	// Pipeline order is  decoder -> (imgfx) -> vsched -> render
	tunVdecoderOutput *rpi.OmxTunnel // C: ovd_tun_vdecoder_output
	tunImgfxOutput    *rpi.OmxTunnel // C: ovd_tun_imgfx_output

	tunVschedVrender *rpi.OmxTunnel // C: ovd_tun_vsched_vrender

	reconfigure       bool  // C: ovd_reconfigure
	pts               int64 // C: ovd_pts
	lastPts           int64 // C: ovd_last_pts
	estimatedDuration int64 // C: ovd_estimated_duration

	gv *GlwVideo // C: ovd_gv

	mc *mediacore.MediaCodec // C: ovd_mc — current media codec

	pos   glwRect // C: ovd_pos
	alpha float32 // C: ovd_alpha
	mutex sync.Mutex
}

// ovdGet — C: gv->gv_aux cast
func ovdGet(gv *GlwVideo) *omxVideoDisplay {
	return (*omxVideoDisplay)(gv.gvAux)
}

// ovdStart — C: ovd_init (glw_video_rpi.c:76-86)
func ovdStart(gv *GlwVideo) int {
	ovd := &omxVideoDisplay{}
	ovd.pts = mediacore.PTSUnset
	ovd.alpha = -1000
	ovd.gv = gv
	gv.gvAux = unsafe.Pointer(ovd)
	return 0
}

// ovdNewframe — C: ovd_newframe (glw_video_rpi.c:92-119)
func ovdNewframe(gv *GlwVideo, vd *decoder.VideoDecoder, flags int) int64 {
	ovd := ovdGet(gv)

	if ovd.vsched != nil && ovd.reconfigure {
		ovd.reconfigure = false

		if ovd.tunVschedVrender != nil {
			rpi.OmxTunnelDestroy(ovd.tunVschedVrender)
		}

		ovd.tunVschedVrender =
			rpi.OmxTunnelCreate(ovd.vsched, 11, ovd.vrender, 90,
				"vsched -> vrender")

		rpi.OmxSetState(ovd.vrender, C.OMX_StateExecuting)

		var dr C.OMX_CONFIG_DISPLAYREGIONTYPE
		dr.nSize = C.OMX_U32(unsafe.Sizeof(dr))
		C.omx_init_version_(&dr.nVersion)
		dr.nPortIndex = 90
		dr.set = C.OMX_DISPLAY_SET_LAYER
		dr.layer = 3
		omxchkRpi(C.omx_SetConfig_(omxHandle(ovd.vrender),
			C.OMX_IndexConfigDisplayRegion, C.OMX_PTR(unsafe.Pointer(&dr))), "OMX_SetConfig")
	}
	return ovd.pts
}

// bufferMark — C: buffer_mark (glw_video_rpi.c:125-151)
func bufferMark(oc *rpi.OmxComponent, ptr unsafe.Pointer) {
	if ptr == nil {
		return
	}
	ovd := oc.Opaque.(*omxVideoDisplay)
	gv := ovd.gv
	mp := gv.gvMp
	vd := gv.gvVd
	mbm := (*mediacore.MediaBufMeta)(ptr)

	ovd.lastPts = ovd.pts

	ovd.pts = mbm.PTS

	if mbm.Duration == 0 {
		if ovd.lastPts != mediacore.PTSUnset && ovd.pts != mediacore.PTSUnset {
			ovd.estimatedDuration = ovd.pts - ovd.lastPts
		}
		mbm.Duration = ovd.estimatedDuration
	}

	mp.Mutex.Lock()
	vd.ReorderCurrent = mbm
	mp.Video.Avail.Signal()
	mp.Mutex.Unlock()
}

// vschedPortSettingsChanged — C: vsched_port_settings_changed
// (glw_video_rpi.c:156-161)
func vschedPortSettingsChanged(oc *rpi.OmxComponent) {
	ovd := oc.Opaque.(*omxVideoDisplay)
	ovd.reconfigure = true
}

// ovdReset — C: ovd_reset (glw_video_rpi.c:167-208)
func ovdReset(gv *GlwVideo) {
	ovd := ovdGet(gv)

	rpi.OmxTunnelDestroy(ovd.tunClockVsched)

	rpi.OmxFlushPort(ovd.vsched, 10)
	rpi.OmxFlushPort(ovd.vsched, 11)

	rpi.OmxFlushPort(ovd.vrender, 90)

	if ovd.tunVschedVrender != nil {
		rpi.OmxTunnelDestroy(ovd.tunVschedVrender)
	}

	if ovd.tunImgfxOutput != nil {
		rpi.OmxTunnelDestroy(ovd.tunImgfxOutput)
	}

	if ovd.tunVdecoderOutput != nil {
		rpi.OmxTunnelDestroy(ovd.tunVdecoderOutput)
	}

	rpi.OmxSetState(ovd.vrender, C.OMX_StateIdle)
	rpi.OmxSetState(ovd.vsched, C.OMX_StateIdle)
	if ovd.imgfx != nil {
		rpi.OmxSetState(ovd.imgfx, C.OMX_StateIdle)
	}

	rpi.OmxSetState(ovd.vrender, C.OMX_StateLoaded)
	rpi.OmxSetState(ovd.vsched, C.OMX_StateLoaded)
	if ovd.imgfx != nil {
		rpi.OmxSetState(ovd.imgfx, C.OMX_StateLoaded)
	}

	rpi.OmxComponentDestroy(ovd.vrender)
	rpi.OmxComponentDestroy(ovd.vsched)
	if ovd.imgfx != nil {
		rpi.OmxComponentDestroy(ovd.imgfx)
	}

	if ovd.mc != nil {
		mediacore.MediaCodecDeref(ovd.mc)
	}

	gv.gvAux = nil
}

// ovdRender — C: ovd_render (glw_video_rpi.c:214-257)
func ovdRender(gv *GlwVideo, rc *glwRctx) {
	ovd := ovdGet(gv)
	var conf C.OMX_CONFIG_DISPLAYREGIONTYPE

	if ovd.pos != gv.gvRect {
		ovd.pos = gv.gvRect

		conf.nSize = C.OMX_U32(unsafe.Sizeof(conf))
		C.omx_init_version_(&conf.nVersion)
		conf.nPortIndex = 90

		conf.fullscreen = C.OMX_FALSE
		conf.noaspect = C.OMX_TRUE
		conf.set = C.OMX_DISPLAY_SET_DEST_RECT |
			C.OMX_DISPLAY_SET_FULLSCREEN |
			C.OMX_DISPLAY_SET_NOASPECT

		conf.dest_rect.x_offset = C.OMX_S16(ovd.pos.x1)
		conf.dest_rect.y_offset = C.OMX_S16(ovd.pos.y1)
		conf.dest_rect.width = C.OMX_S16(ovd.pos.x2 - ovd.pos.x1)
		conf.dest_rect.height = C.OMX_S16(ovd.pos.y2 - ovd.pos.y1)

		omxchkRpi(C.omx_SetConfig_(omxHandle(ovd.vrender),
			C.OMX_IndexConfigDisplayRegion, C.OMX_PTR(unsafe.Pointer(&conf))), "OMX_SetConfig")
	}

	if ovd.alpha != rc.rcAlpha {
		ovd.alpha = rc.rcAlpha

		conf.nSize = C.OMX_U32(unsafe.Sizeof(conf))
		C.omx_init_version_(&conf.nVersion)
		conf.nPortIndex = 90

		conf.alpha = C.OMX_U32(rc.rcAlpha * 255)
		if conf.alpha < 5 {
			conf.alpha = 0
		}
		conf.set = C.OMX_DISPLAY_SET_ALPHA
		omxchkRpi(C.omx_SetConfig_(omxHandle(ovd.vrender),
			C.OMX_IndexConfigDisplayRegion, C.OMX_PTR(unsafe.Pointer(&conf))), "OMX_SetConfig")
	}
}

// ovdBlackout — C: ovd_blackout (glw_video_rpi.c:263-274)
func ovdBlackout(gv *GlwVideo) {
	ovd := ovdGet(gv)
	if ovd.imgfx != nil {
		rpi.OmxFlushPort(ovd.imgfx, 190)
		rpi.OmxFlushPort(ovd.imgfx, 191)
	}
	rpi.OmxFlushPort(ovd.vsched, 10)
	rpi.OmxFlushPort(ovd.vsched, 11)
	rpi.OmxFlushPort(ovd.vrender, 90)
}

// ovdSetCodec — C: ovd_set_codec (glw_video_rpi.c:280-392)
func ovdSetCodec(mc *mediacore.MediaCodec, gv *GlwVideo,
	fi *mediacore.FrameInfo, gve *glwVideoEngine) int {
	mp := gv.gvMp

	glwVideoConfigure(gv, gve)

	gv.gvWidth = fi.Width
	gv.gvHeight = fi.Height

	ovd := ovdGet(gv)
	rvc := mc.Opaque.(*rpi.RpiVideoCodec)

	if ovd.vrender == nil {
		ovd.vrender = rpi.OmxComponentCreate("OMX.broadcom.video_render",
			&ovd.mutex, nil)
		ovd.vsched = rpi.OmxComponentCreate("OMX.broadcom.video_scheduler",
			&ovd.mutex, nil)

		ovd.vsched.Opaque = ovd
		ovd.vrender.Opaque = ovd

		gv.gvVd.RenderComponent = ovd.vrender

		rpi.OmxEnableBufferMarks(ovd.vrender)

		ovd.tunClockVsched =
			rpi.OmxTunnelCreate(rpi.OmxGetClock(mp), 81, ovd.vsched, 12,
				"clock -> vsched")

		ovd.vsched.PortSettingsChangedCb = vschedPortSettingsChanged

		ovd.vrender.EventMarkCb = bufferMark
	}
	rpi.OmxSetState(ovd.vrender, C.OMX_StateIdle)

	if ovd.tunVdecoderOutput != nil {
		rpi.OmxTunnelDestroy(ovd.tunVdecoderOutput)
	}

	if ovd.tunImgfxOutput != nil {
		rpi.OmxTunnelDestroy(ovd.tunImgfxOutput)
	}

	if ovd.mc != nil {
		mediacore.MediaCodecDeref(ovd.mc)
	}

	ovd.mc = mediacore.MediaCodecRef(mc)

	if fi.Interlaced {

		if ovd.imgfx == nil {
			ovd.imgfx = rpi.OmxComponentCreate("OMX.broadcom.image_fx",
				&ovd.mutex, nil)
			ovd.imgfx.Opaque = ovd

			// add extra buffers for Advanced Deinterlace
			var extraBuffers C.OMX_PARAM_U32TYPE
			extraBuffers.nSize = C.OMX_U32(unsafe.Sizeof(extraBuffers))
			C.omx_init_version_(&extraBuffers.nVersion)
			extraBuffers.nU32 = 6
			extraBuffers.nPortIndex = 130
			omxchkRpi(C.omx_SetParameter_(omxHandle(ovd.imgfx),
				C.OMX_IndexParamBrcmExtraBuffers, C.OMX_PTR(unsafe.Pointer(&extraBuffers))), "OMX_SetParameter")

			var imageFilter C.OMX_CONFIG_IMAGEFILTERPARAMSTYPE
			imageFilter.nSize = C.OMX_U32(unsafe.Sizeof(imageFilter))
			C.omx_init_version_(&imageFilter.nVersion)
			imageFilter.nPortIndex = 191

			imageFilter.nNumParams = 4
			imageFilter.nParams[0] = 6
			imageFilter.nParams[1] = 0 // default frame interval
			imageFilter.nParams[2] = 0 // half framerate
			imageFilter.nParams[3] = 1 // use qpus

			imageFilter.eImageFilter = C.OMX_ImageFilterDeInterlaceAdvanced

			omxchkRpi(C.omx_SetConfig_(omxHandle(ovd.imgfx),
				C.OMX_IndexConfigCommonImageFilterParameters,
				C.OMX_PTR(unsafe.Pointer(&imageFilter))), "OMX_SetConfig")
		}

		ovd.tunVdecoderOutput =
			rpi.OmxTunnelCreate(rvc.Decoder, 131, ovd.imgfx, 190,
				"vdecoder -> imgfx")

		ovd.tunImgfxOutput =
			rpi.OmxTunnelCreate(ovd.imgfx, 191, ovd.vsched, 10,
				"imgfx -> vsched")

		rpi.OmxSetState(ovd.imgfx, C.OMX_StateExecuting)

	} else {

		if ovd.imgfx != nil {
			rpi.OmxComponentDestroy(ovd.imgfx)
			ovd.imgfx = nil
		}

		ovd.tunVdecoderOutput =
			rpi.OmxTunnelCreate(rvc.Decoder, 131, ovd.vsched, 10,
				"vdecoder -> vsched")
	}

	rpi.OmxSetState(ovd.vsched, C.OMX_StateExecuting)
	return 0
}

// C: static glw_video_engine_t glw_video_ovd (glw_video_rpi.c:398-408)
// GLW_REGISTER_GVE(glw_video_ovd)
var glwVideoOvd = &glwVideoEngine{
	gveType:     mediacore.FourCCOMX, // C: 'omx'
	gveNewframe: ovdNewframe,
	gveRender:   ovdRender,
	gveReset:    ovdReset,
	gveStart:    ovdStart,
	gveSetCodec: ovdSetCodec,
	gveBlackout: ovdBlackout,
}

const dispmanxVideoSurfaces = 4 // C: DISPMANX_VIDEO_SURFACES

// C: typedef struct dispmanx_video (glw_video_rpi.c:415-419)
type dispmanxVideo struct {
	handle C.DISPMANX_ELEMENT_HANDLE_T // C: dv_handle
}

// dispmanxYuvpSurfaceReset — C: dispmanx_yuvp_surface_reset
// (glw_video_rpi.c:425-432)
func dispmanxYuvpSurfaceReset(gv *GlwVideo, gvs *glwVideoSurface) {
	if gvs.gvsID != -1 {
		C.vc_dispmanx_resource_delete(C.DISPMANX_RESOURCE_HANDLE_T(gvs.gvsID))
		gvs.gvsID = -1
	}
}

// makeSurfacesAvailable — C: make_surfaces_available
// (glw_video_rpi.c:438-446)
func makeSurfacesAvailable(gv *GlwVideo) {
	for i := 0; i < dispmanxVideoSurfaces; i++ {
		gvs := &gv.gvSurfaces[i]
		gvsTAILQInsertTail(&gv.gvAvailQueue, gvs)
		gvs.gvsID = -1
	}
}

// dispmanxYuvpStart — C: dispmanx_yuvp_init (glw_video_rpi.c:452-462)
func dispmanxYuvpStart(gv *GlwVideo) int {
	makeSurfacesAvailable(gv)

	dv := &dispmanxVideo{handle: ^C.DISPMANX_ELEMENT_HANDLE_T(0)}
	gv.gvAux = unsafe.Pointer(dv)
	return 0
}

// copyFromDisplaying — C: copy_from_displaying (glw_video_rpi.c:467-477)
func copyFromDisplaying(gv *GlwVideo) {
	for s := gv.gvDisplayingQueue.tqhFirst; s != nil; {
		C.vc_dispmanx_resource_delete(C.DISPMANX_RESOURCE_HANDLE_T(s.gvsID))
		s.gvsID = -1
		gvsTAILQRemove(&gv.gvDisplayingQueue, s)
		gvsTAILQInsertTail(&gv.gvAvailQueue, s)
		gv.gvAvailQueueCond.Signal()
		s = gv.gvDisplayingQueue.tqhFirst
	}
}

// dispmanxYuvpNewframe — C: dispmanx_yuvp_newframe
// (glw_video_rpi.c:483-579)
func dispmanxYuvpNewframe(gv *GlwVideo, vd0 *decoder.VideoDecoder, flags int) int64 {
	gr := gv.w.glwRoot
	pts := mediacore.PTSUnset
	dv := (*dispmanxVideo)(gv.gvAux)
	mp := gv.gvMp

	var srcRect, dstRect C.VC_RECT_T
	alpha := C.VC_DISPMANX_ALPHA_T{
		flags:   C.DISPMANX_FLAGS_ALPHA_FIXED_ALL_PIXELS,
		opacity: 255,
		mask:    0,
	}

	copyFromDisplaying(gv)

	mp.ClockMutex.Lock()
	aclock := mp.AudioClock + gr.grFrameStartAvtime -
		mp.AudioClockAvtime + mp.AVDelta

	aclockValid := mp.AudioClockEpoch != 0
	aepoch := mp.AudioClockEpoch
	mp.ClockMutex.Unlock()

	for s := gv.gvDecodedQueue.tqhFirst; s != nil; {
		delta := int64(gr.grFrameduration * 2)
		pts = s.gvsPts
		epoch := s.gvsEpoch

		d := s.gvsPts - aclock

		if gv.gvNextptsEpoch == epoch &&
			(pts == mediacore.PTSUnset || d < -5000000 || d > 5000000) {
			pts = gv.gvNextpts
		}

		if pts != mediacore.PTSUnset && (pts-delta) >= aclock && aclockValid {
			if media.EnableDetailedAvdiff(glwDeps.gconf) {
				glwDeps.ts.Trace(trace.TRACE_DEBUG, "AVDIFF",
					"%s: Not sending frame %d:%d %d:%d diff:%d\n",
					mp.Name, s.gvsEpoch, pts-delta, aepoch,
					aclock, (pts-delta)-aclock)
			}
			break
		} else {
			if media.EnableDetailedAvdiff(glwDeps.gconf) {
				glwDeps.ts.Trace(trace.TRACE_DEBUG, "AVDIFF",
					"%s:     Sending frame %d:%d %d:%d\n",
					mp.Name, s.gvsEpoch, pts-delta, aepoch, aclock)
			}
		}

		update := C.vc_dispmanx_update_start(10)

		C.vc_dispmanx_rect_set(&srcRect, 0, 0,
			C.uint32_t(s.gvsWidth[0])<<16,
			C.uint32_t(s.gvsHeight[0])<<16)

		C.vc_dispmanx_rect_set(&dstRect,
			C.uint32_t(int32(gv.gvRect.x1)), C.uint32_t(int32(gv.gvRect.y1)),
			C.uint32_t(gv.gvRect.x2-gv.gvRect.x1),
			C.uint32_t(gv.gvRect.y2-gv.gvRect.y1))

		if dv.handle != ^C.DISPMANX_ELEMENT_HANDLE_T(0) {
			C.vc_dispmanx_element_remove(update, dv.handle)
		}

		dv.handle = C.vc_dispmanx_element_add(update,
			C.DISPMANX_DISPLAY_HANDLE_T(rpi.DispmanDisplay),
			4,
			&dstRect,
			C.DISPMANX_RESOURCE_HANDLE_T(s.gvsID),
			&srcRect,
			C.DISPMANX_PROTECTION_NONE,
			&alpha,
			nil,
			C.VC_IMAGE_ROT0)

		C.vc_dispmanx_update_submit_sync(update)

		gvsTAILQRemove(&gv.gvDecodedQueue, s)
		gvsTAILQInsertTail(&gv.gvDisplayingQueue, s)

		s = gv.gvDecodedQueue.tqhFirst
	}

	return pts
}

// dispmanxYuvpRender — C: dispmanx_yuvp_render (glw_video_rpi.c:585-588)
func dispmanxYuvpRender(gv *GlwVideo, rc *glwRctx) {
}

// dispmanxYuvpReset — C: dispmanx_yuvp_reset (glw_video_rpi.c:595-615)
func dispmanxYuvpReset(gv *GlwVideo) {
	dv := (*dispmanxVideo)(gv.gvAux)

	for i := 0; i < dispmanxVideoSurfaces; i++ {
		gvs := &gv.gvSurfaces[i]
		dispmanxYuvpSurfaceReset(gv, gvs)
	}

	update := C.vc_dispmanx_update_start(10)

	if dv.handle != ^C.DISPMANX_ELEMENT_HANDLE_T(0) {
		C.vc_dispmanx_element_remove(update, dv.handle)
	}

	C.vc_dispmanx_update_submit_sync(update)

	gv.gvAux = nil
}

// dispmanxYuvpDeliver — C: dispmanx_yuvp_deliver
// (glw_video_rpi.c:620-673)
func dispmanxYuvpDeliver(fi *mediacore.FrameInfo, gv *GlwVideo,
	gve *glwVideoEngine) int {
	if glwVideoConfigure(gv, gve) != 0 {
		return -1
	}
	gvs := glwVideoGetSurface(gv, nil, nil)
	if gvs == nil {
		return -1
	}

	var dstRect C.VC_RECT_T

	pitch := fi.Pitch[0]

	alignedHeight := (fi.Height + 15) &^ 15

	gvs.gvsWidth[0] = fi.Width
	gvs.gvsHeight[0] = fi.Height

	C.vc_dispmanx_rect_set(&dstRect,
		0, 0, C.uint32_t(gvs.gvsWidth[0]),
		C.uint32_t((3*alignedHeight)/2))

	if gvs.gvsID != -1 {
		panic("dispmanx_yuvp_deliver: gvs_id != -1") // C: assert
	}
	lumasize := pitch * alignedHeight
	chromasize := pitch * alignedHeight / 4

	bufsize := lumasize + chromasize*2

	tmp := C.malloc(C.size_t(bufsize))
	if tmp == nil {
		return -1
	}
	p1 := tmp
	p2 := unsafe.Pointer(uintptr(tmp) + uintptr(lumasize))
	p3 := unsafe.Pointer(uintptr(p2) + uintptr(chromasize))

	C.memcpy(p1, unsafe.Pointer(&fi.Data[0][0]), C.size_t(fi.Pitch[0]*fi.Height))
	C.memcpy(p2, unsafe.Pointer(&fi.Data[1][0]), C.size_t(fi.Pitch[1]*fi.Height/2))
	C.memcpy(p3, unsafe.Pointer(&fi.Data[2][0]), C.size_t(fi.Pitch[2]*fi.Height/2))

	var imagePtr C.uint32_t // This is not used inside the API used AFAIK
	gvs.gvsID = int(C.vc_dispmanx_resource_create(C.VC_IMAGE_YUV420,
		C.uint32_t(gvs.gvsWidth[0]),
		C.uint32_t(alignedHeight),
		&imagePtr))

	C.vc_dispmanx_resource_write_data(
		C.DISPMANX_RESOURCE_HANDLE_T(gvs.gvsID),
		C.VC_IMAGE_YUV420,
		C.int32_t(fi.Pitch[0]),
		tmp,
		&dstRect)
	C.free(tmp)
	glwVideoPutSurface(gv, gvs, fi.PTS, fi.Epoch,
		int(fi.Duration), 0, 0)
	return 0
}

// C: static glw_video_engine_t glw_video_dispmanx (glw_video_rpi.c:679-688)
// GLW_REGISTER_GVE(glw_video_dispmanx)
var glwVideoDispmanx = &glwVideoEngine{
	gveType:     mediacore.FourCCYUVP, // C: 'YUVP'
	gveNewframe: dispmanxYuvpNewframe,
	gveRender:   dispmanxYuvpRender,
	gveReset:    dispmanxYuvpReset,
	gveStart:    dispmanxYuvpStart,
	gveDeliver:  dispmanxYuvpDeliver,
}

func init() {
	// C: GLW_REGISTER_GVE(glw_video_ovd) + GLW_REGISTER_GVE(glw_video_dispmanx)
	gveLISTInsertHead(glwVideoOvd)
	gveLISTInsertHead(glwVideoDispmanx)
}
