package glw

// C: src/ui/glw/glw_video_common.c — canonical 1:1 port.
// Video widget class + engine registry + surface queues + AV-diff
// (Kalman) synchronization.

import (
	"math"
	"sync"
	"unsafe"

	archpkg "github.com/czz/movian-go/internal/arch"
	backendcore "github.com/czz/movian-go/internal/backend/core"
	eventpkg "github.com/czz/movian-go/internal/event"
	"github.com/czz/movian-go/internal/media"
	mediacore "github.com/czz/movian-go/internal/media/core"
	medialibav "github.com/czz/movian-go/internal/media/libav"
	miscpkg "github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
	tracepkg "github.com/czz/movian-go/internal/trace"
	"github.com/czz/movian-go/internal/video/decoder"
)

// ---------------------------------------------------------------------------
// TAILQ ops for glw_video_surface_queue (C: queue.h macros)

func gvsTAILQSetup(q *glwVideoSurfaceQueue) {
	q.tqhFirst = nil
	q.tqhLast = &q.tqhFirst
}

func gvsTAILQInsertTail(q *glwVideoSurfaceQueue, s *glwVideoSurface) {
	s.gvsLinkNext = nil
	s.gvsLinkPrev = q.tqhLast
	*q.tqhLast = s
	q.tqhLast = &s.gvsLinkNext
}

func gvsTAILQRemove(q *glwVideoSurfaceQueue, s *glwVideoSurface) {
	if s.gvsLinkNext != nil {
		s.gvsLinkNext.gvsLinkPrev = s.gvsLinkPrev
	} else {
		q.tqhLast = s.gvsLinkPrev
	}
	*s.gvsLinkPrev = s.gvsLinkNext
}

// ---------------------------------------------------------------------------
// LIST ops for glw_video_reap_task_list and the engine registry

func gvrLISTInsertHead(l *glwVideoReapTaskList, t *glwVideoReapTask) {
	t.linkNext = l.lhFirst
	if t.linkNext != nil {
		t.linkNext.linkPrev = &t.linkNext
	}
	l.lhFirst = t
	t.linkPrev = &l.lhFirst
}

func gvrLISTRemove(l *glwVideoReapTaskList, t *glwVideoReapTask) {
	if t.linkNext != nil {
		t.linkNext.linkPrev = t.linkPrev
	}
	*t.linkPrev = t.linkNext
}

func gvrLISTRemoveAll(l *glwVideoReapTaskList, t *glwVideoReapTask) {
	gvrLISTRemove(l, t)
}

var glwVideoEngines struct {
	lhFirst *glwVideoEngine
}

func gveLISTInsertHead(gve *glwVideoEngine) {
	gve.linkNext = glwVideoEngines.lhFirst
	if gve.linkNext != nil {
		gve.linkNext.linkPrev = &gve.linkNext
	}
	glwVideoEngines.lhFirst = gve
	gve.linkPrev = &glwVideoEngines.lhFirst
}

// ---------------------------------------------------------------------------

// C: static void glw_video_reap (glw_video_common.c:43-52)
func glwVideoReap(gv *GlwVideo) {
	for t := gv.gvReaps.lhFirst; t != nil; {
		gvrLISTRemoveAll(&gv.gvReaps, t)
		t.fn(gv, t)
		t = gv.gvReaps.lhFirst
	}
}

// C: static void glw_video_rctx_adjust (glw_video_common.c:58-107)
func glwVideoRctxAdjust(rc *glwRctx, gv *GlwVideo) {
	gr := gv.w.glwRoot

	if gr.grUnderscanH != 0 || gr.grUnderscanV != 0 {
		glwReposition(rc,
			-gr.grUnderscanH,
			int(rc.rcHeight)+gr.grUnderscanV,
			int(rc.rcWidth)+gr.grUnderscanH,
			-gr.grUnderscanV)
	}

	tAspect := float32(gv.gvDarNum) / float32(gv.gvDarDen)

	if gv.gvFstretch != 0 {
		return
	}

	if tAspect*float32(rc.rcHeight) < float32(rc.rcWidth) {

		if gv.gvHstretch != 0 {
			return
		}

		// Shrink X
		border := int(float32(rc.rcWidth) - tAspect*float32(rc.rcHeight))
		left := int(float32(border+1) * 0.5)
		// C: right = rc_width - border * 0.5f — float subtraction,
		// truncated once on assignment (odd border → x.5 rounds down
		// after the subtraction, not before).
		right := int(float32(rc.rcWidth) - float32(border)*0.5)

		s := float32(right-left) / float32(rc.rcWidth)
		t := -1.0 + float32(right+left)/float32(rc.rcWidth)

		glwTranslatef(rc, t, 0, 0)
		glwScalef(rc, s, 1, 1)

		rc.rcWidth = int16(right - left)

	} else {
		// Shrink Y
		border := int(float32(rc.rcHeight) - float32(rc.rcWidth)/tAspect)
		bottom := int(float32(border+1) * 0.5)
		// C: top = rc_height - border * 0.5f — same truncation order.
		top := int(float32(rc.rcHeight) - float32(border)*0.5)

		s := float32(top-bottom) / float32(rc.rcHeight)
		t := -1.0 + float32(top+bottom)/float32(rc.rcHeight)

		glwTranslatef(rc, 0, t, 0)
		glwScalef(rc, 1, s, 1)
		rc.rcHeight = int16(top - bottom)
	}
}

// C: static int glw_video_widget_event (glw_video_common.c:113-146)
func glwVideoWidgetEvent(w *Glw, e *eventpkg.Event) int {
	gv := (*GlwVideo)(unsafe.Pointer(w))
	mp := gv.gvMp

	// Intercept media events
	if e.IsAction(eventpkg.ACTION_PLAYPAUSE) ||
		e.IsAction(eventpkg.ACTION_PLAY) ||
		e.IsAction(eventpkg.ACTION_PAUSE) ||
		e.IsAction(eventpkg.ACTION_STOP) ||
		e.IsAction(eventpkg.ACTION_SKIP_FORWARD) ||
		e.IsAction(eventpkg.ACTION_SKIP_BACKWARD) ||
		e.Type == eventpkg.EVENT_SELECT_AUDIO_TRACK ||
		e.Type == eventpkg.EVENT_SELECT_SUBTITLE_TRACK {
		mediacore.MpEnqueueEvent(mp, &mediacore.MediaEvent{Type: int(e.Type), Data: e.Concrete()})
		return 1
	}

	// If we are in DVD menu, intercept those events as well
	if gv.gvSpuInMenu != 0 {
		if e.IsAction(eventpkg.ACTION_UP) ||
			e.IsAction(eventpkg.ACTION_DOWN) ||
			e.IsAction(eventpkg.ACTION_LEFT) ||
			e.IsAction(eventpkg.ACTION_RIGHT) ||
			e.IsAction(eventpkg.ACTION_ACTIVATE) {
			mediacore.MpEnqueueEvent(mp, &mediacore.MediaEvent{Type: int(e.Type), Data: e.Concrete()})
			return 1
		}
	}
	return 0
}

// C: static int glw_video_compute_output_duration (glw_video_common.c:152-171)
func glwVideoComputeOutputDuration(gv *GlwVideo, frameDuration int) int {
	var delta int
	const maxdiff = 5000

	if gv.gvAvdiffX > 0 {
		delta = min(int(math.Pow(float64(gv.gvAvdiffX)*1000.0, 2)), maxdiff)
	} else if gv.gvAvdiffX < 0 {
		delta = max(-int(math.Pow(float64(-gv.gvAvdiffX)*1000.0, 2)), -maxdiff)
	} else {
		delta = 0
	}
	return frameDuration + delta
}

// C: const char *statustab[] (glw_video_common.c:182-188)
var glwVideoAvdiffStatustab = []string{
	media.AVDiffLocked:         "locked",
	media.AVDiffNoLock:         "no lock",
	media.AVDiffIncorrectEpoch: "incorrect epoch",
	media.AVDiffHold:           "holding",
	media.AVDiffCatchUp:        "catching up",
}

// C: static int glw_video_compute_avdiff (glw_video_common.c:193-265)
func glwVideoComputeAvdiff(gr *glwRoot, mp *mediacore.MediaPipe,
	pts int64, epoch int, gv *GlwVideo) int {
	var code int

	mp.ClockMutex.Lock()

	aclock := mp.AudioClock + gr.grFrameStartAvtime -
		mp.AudioClockAvtime + mp.AVDelta

	if int(mp.AudioClockEpoch) != epoch {
		// Not the same clock epoch, can not sync
		gv.gvAvdiffX = 0
		gv.gvAvfilter.Reset()
		code = media.AVDiffIncorrectEpoch
		mp.PropAVDiffError.SetInt(code)
	} else {

		gv.gvAvdiff = int32(aclock - pts)

		var d int32
		if gv.gvAvdiff < 0 {
			d = -gv.gvAvdiff
		} else {
			d = gv.gvAvdiff
		}

		if d < 30000000 {
			gv.gvAvdiffX = float32(gv.gvAvfilter.Update(
				float64(gv.gvAvdiff) / 1000000))
			gv.gvAvdiffX = glwMax(glwMin(gv.gvAvdiffX, 30.0), -30.0)
			code = media.AVDiffLocked

			if gv.gvAvdiffX > 0.1 {
				code = media.AVDiffCatchUp
			}
			if gv.gvAvdiffX < -0.1 {
				code = media.AVDiffHold
			}
		} else {
			code = media.AVDiffNoLock
		}

		if gv.gvAvdiffUpdateThres == 0 {
			mp.PropAVDiff.SetFloat(gv.gvAvdiffX)
			mp.PropAVDiffError.SetInt(code)
			gv.gvAvdiffUpdateThres = 5
		} else {
			gv.gvAvdiffUpdateThres--
		}
	}

	mp.ClockMutex.Unlock()

	if media.EnableDetailedAvdiff(glwDeps.gconf) {
		glwDeps.ts.Trace(tracepkg.TRACE_DEBUG, "AVDIFF",
			"VE:%d AE:%d %10f %10d %15d:a:%-8d %15d:v:%-8d %15d %15d %s %d",
			epoch,
			mp.AudioClockEpoch,
			gv.gvAvdiffX,
			gv.gvAvdiff,
			aclock,
			aclock-glwVideoLastAclock,
			pts,
			pts-glwVideoLastpts,
			mp.AudioClock,
			gr.grFrameStartAvtime-glwVideoLastclock,
			glwVideoAvdiffStatustab[code],
			archpkg.GetTS()-aclock)
		glwVideoLastpts = pts
		glwVideoLastAclock = aclock
		glwVideoLastclock = gr.grFrameStartAvtime
	}
	return code
}

// C: static int64_t lastpts, lastaclock, lastclock (glw_video_common.c:245)
var (
	glwVideoLastpts    int64
	glwVideoLastAclock int64
	glwVideoLastclock  int64
)

// C: static int64_t glw_video_compute_blend (glw_video_common.c:271-341)
func glwVideoComputeBlend(gv *GlwVideo, sa *glwVideoSurface,
	sb *glwVideoSurface, outputDuration int, interpolation int) int64 {
	var pts int64

	if interpolation == 0 {

		sa.gvsDuration -= outputDuration
		if sa.gvsPts != mediacore.PTSUnset {
			sa.gvsPts += int64(outputDuration)
		}

		if sa.gvsDuration < 0 && sb != nil {

			spill := -sa.gvsDuration
			sa.gvsDuration = 0

			sb.gvsDuration -= spill
			if sb.gvsPts != mediacore.PTSUnset {
				sb.gvsPts += int64(spill)
			}

			sa = sb
		}

		gv.gvSa = sa
		gv.gvSb = nil

		pts = sa.gvsPts

	} else if sa.gvsDuration >= outputDuration {

		gv.gvSa = sa
		gv.gvSb = nil

		sa.gvsDuration -= outputDuration

		pts = sa.gvsPts
		if sa.gvsPts != mediacore.PTSUnset {
			sa.gvsPts += int64(outputDuration)
		}

	} else if sb != nil {

		gv.gvSa = sa
		gv.gvSb = sb

		gv.gvBlend = float32(sa.gvsDuration) / float32(outputDuration)

		if sa.gvsDuration+sb.gvsDuration < outputDuration {

			pts = sb.gvsPts

		} else {
			pts = sa.gvsPts
			x := outputDuration - sa.gvsDuration
			sb.gvsDuration -= x
			if sb.gvsPts != mediacore.PTSUnset {
				sb.gvsPts += int64(x)
			}
		}
		sa.gvsDuration = 0

	} else {
		gv.gvSa = sa
		gv.gvSb = nil
		if sa.gvsPts != mediacore.PTSUnset {
			sa.gvsPts += int64(outputDuration)
		}

		pts = sa.gvsPts
	}

	return pts
}

// C: int64_t glw_video_newframe_blend (glw_video_common.c:347-423)
func glwVideoNewframeBlend(gv *GlwVideo, vd *decoder.VideoDecoder, flags int,
	release gvSurfacePixmapReleaseFunc, interpolation int) int64 {
	gr := gv.w.glwRoot
	mp := gv.gvMp
	pts := mediacore.PTSUnset
	frameDuration := gv.w.glwRoot.grFrameduration

	interpolation &= gv.gvVinterpolate

	// Find new surface to display
	// C: hts_mutex_assert(&gv->gv_surface_mutex)
again:
	sa := gv.gvDecodedQueue.tqhFirst
	if sa == nil {
		// No frame available
		sa = gv.gvDisplayingQueue.tqhFirst
		if sa != nil {
			// Continue to display last frame
			// C: gv->gv_sa = sa; gv->gv_sa = NULL; — canonical bug:
			// gv_sb is never cleared (the second store targets gv_sa
			// again). Preserved verbatim.
			gv.gvSa = sa
			gv.gvSa = nil
		} else {
			gv.gvSa = nil
			gv.gvSa = nil
		}
	} else {

		sb := sa.gvsLinkNext

		if !vd.Hold {
			var outputDuration int
			outputDuration = glwVideoComputeOutputDuration(gv, frameDuration)

			pts = glwVideoComputeBlend(gv, sa, sb, outputDuration, interpolation)
			if pts != mediacore.PTSUnset {
				pts -= int64(frameDuration * 2)
				code := glwVideoComputeAvdiff(gr, mp, pts, sa.gvsEpoch, gv)

				if code == media.AVDiffHold {
					gv.gvAvfilter.Reset()
					return pts
				}

				if code == media.AVDiffCatchUp && sb != nil {
					gv.gvSa = nil
					release(gv, sa, &gv.gvDecodedQueue)
					gv.gvAvfilter.Reset()
					goto again
				}
			}
		}

		if !vd.Hold || sb != nil {
			// C: sa != NULL check is redundant here (sa is non-nil in
			// this branch) — preserved verbatim (glw_video_common.c:404).
			if sa != nil && sa.gvsDuration == 0 {
				glwVideoEnqueueForDisplay(gv, sa, &gv.gvDecodedQueue)
			}
		}
		if sb != nil && sb.gvsDuration == 0 {
			glwVideoEnqueueForDisplay(gv, sb, &gv.gvDecodedQueue)
		}

		// There are frames available that we are going to display,
		// push back old frames to decoder
		for s := gv.gvDisplayingQueue.tqhFirst; s != nil; {
			if s != gv.gvSa && s != gv.gvSb {
				release(gv, s, &gv.gvDisplayingQueue)
			} else {
				break
			}
			s = gv.gvDisplayingQueue.tqhFirst
		}
	}
	return pts
}

// C: static __inline void glw_video_enqueue_for_display
// (glw_video_common.h:305-311)
func glwVideoEnqueueForDisplay(gv *GlwVideo, gvs *glwVideoSurface,
	fromqueue *glwVideoSurfaceQueue) {
	gvsTAILQRemove(fromqueue, gvs)
	gvsTAILQInsertTail(&gv.gvDisplayingQueue, gvs)
}

// C: static void glw_video_play (glw_video_common.c:429-455)
func glwVideoPlay(gv *GlwVideo) {
	if gv.gvFreezed != 0 {
		return
	}

	if gv.gvCurrentURL == gv.gvPendingURL {
		return
	}

	gv.gvCurrentURL = gv.gvPendingURL

	e := glwDeps.em.CreatePlayURLArgs(&eventpkg.EventPlayURLArgs{
		URL:         gv.gvCurrentURL,
		Primary:     gv.gvFlags&glwVideoPrimary != 0,
		Priority:    gv.gvPriority,
		NoAudio:     gv.gvFlags&glwVideoNoAudio != 0,
		ItemModel:   gv.gvItemModel,
		How:         gv.gvHow,
		ParentModel: gv.gvParentModel,
		ParentURL:   miscpkg.RstrGet(gv.gvParentURLX),
	})

	mediacore.MpEnqueueEvent(gv.gvMp, &mediacore.MediaEvent{Type: int(e.Type), Data: e})
	e.Release()
}

// C: static void glw_video_dtor (glw_video_common.c:462-511)
func glwVideoDtor(w *Glw) {
	gv := (*GlwVideo)(unsafe.Pointer(w))
	vd := gv.gvVd

	glwRendererFree(&gv.gvQuad)

	glwDeps.pm.RefDec(gv.gvItemModel)
	glwDeps.pm.RefDec(gv.gvParentModel)

	// ENABLE_MEDIA_SETTINGS
	glwDeps.pm.Unsubscribe(gv.gvVoScalingSub)
	glwDeps.pm.Unsubscribe(gv.gvVoDisplaceXSub)
	glwDeps.pm.Unsubscribe(gv.gvVoDisplaceYSub)
	glwDeps.pm.Unsubscribe(gv.gvVzoomSub)
	glwDeps.pm.Unsubscribe(gv.gvPanHorizontalSub)
	glwDeps.pm.Unsubscribe(gv.gvPanVerticalSub)
	glwDeps.pm.Unsubscribe(gv.gvScaleHorizontalSub)
	glwDeps.pm.Unsubscribe(gv.gvScaleVerticalSub)
	glwDeps.pm.Unsubscribe(gv.gvHstretchSub)
	glwDeps.pm.Unsubscribe(gv.gvFstretchSub)
	glwDeps.pm.Unsubscribe(gv.gvVoOnVideoSub)
	glwDeps.pm.Unsubscribe(gv.gvVinterpolateSub)

	miscpkg.RstrRelease(gv.gvParentURLX)

	glwVideoOverlayTeardown(gv)

	// C: LIST_REMOVE(gv, gv_global_link)
	if gv.gvGlobalLinkNext != nil {
		gv.gvGlobalLinkNext.gvGlobalLinkPrev = gv.gvGlobalLinkPrev
	}
	*gv.gvGlobalLinkPrev = gv.gvGlobalLinkNext

	gv.gvSurfaceMutex.Lock()
	glwVideoSurfacesCleanup(gv)
	gv.gvSurfaceMutex.Unlock()

	vd.Destroy()

	mediacore.MpDestroy(gv.gvMp)
	gv.gvMp = nil
}

// C: static void glw_video_newframe (glw_video_common.c:517-543)
func glwVideoNewframe(w *Glw, flags int) {
	gv := (*GlwVideo)(unsafe.Pointer(w))
	vd := gv.gvVd
	var pts int64

	gv.gvSurfaceMutex.Lock()

	glwVideoReap(gv)

	if gv.gvNeedStart != 0 {
		gv.gvEngine.gveStart(gv)
		gv.gvNeedStart = 0
		gv.gvStartCond.Signal()
	}

	if gv.gvEngine != nil {
		pts = gv.gvEngine.gveNewframe(gv, vd, flags)
	} else {
		pts = mediacore.PTSUnset
	}

	gv.gvSurfaceMutex.Unlock()

	if pts != mediacore.PTSUnset {
		glwVideoOverlaySetPts(gv, pts)
	}
}

// C: static void glw_video_layout (glw_video_common.c:549-560)
func glwVideoLayout(w *Glw, rc *glwRctx) {
	gv := (*GlwVideo)(unsafe.Pointer(w))

	w.glwRoot.grCanExternalize = 0

	rc0 := *rc
	glwVideoRctxAdjust(&rc0, gv)
	glwVideoOverlayLayout(gv, rc, &rc0)
}

// C: static int glw_video_pointer_event (glw_video_common.c:566-574)
func glwVideoPointerEvent(w *Glw, gpe *glwPointerEventT) int {
	gv := (*GlwVideo)(unsafe.Pointer(w))
	vd := gv.gvVd
	return glwVideoOverlayPointerEvent(vd, gv.gvWidth, gv.gvHeight,
		gpe, gv.gvMp)
}

// C: static int glw_video_widget_callback (glw_video_common.c:579-600)
func glwVideoWidgetCallback(w *Glw, opaque any, signal glwSignal,
	extra any) int {
	gv := (*GlwVideo)(unsafe.Pointer(w))
	vd := gv.gvVd

	switch signal {
	case glwSignalDestroy:
		gv.gvSurfaceMutex.Lock()
		gv.gvAvailQueueCond.Signal()
		gv.gvStartCond.Signal()
		gv.gvSurfaceMutex.Unlock()
		backendcore.VideoPlaybackDestroy(gv.gvMp)
		vd.Stop()
		return 0
	default:
		return 0
	}
}

// C: static void glw_video_ctor (glw_video_common.c:606-743)
func glwVideoCtor(w *Glw) {
	gv := (*GlwVideo)(unsafe.Pointer(w))
	gr := w.glwRoot

	gv.gvAvfilter.Reset()

	gvsTAILQSetup(&gv.gvAvailQueue)
	gvsTAILQSetup(&gv.gvParkedQueue)
	gvsTAILQSetup(&gv.gvDisplayingQueue)
	gvsTAILQSetup(&gv.gvDecodedQueue)

	gv.gvAvailQueueCond = sync.NewCond(&gv.gvSurfaceMutex)
	gv.gvStartCond = sync.NewCond(&gv.gvSurfaceMutex)

	gv.gvMp = mediacore.MpCreate(glwDeps.bs.MediaSys(), glwDeps.pm, "Video decoder",
		mediacore.MPVideo|mediacore.MPPrimaable)
	gv.gvMp.Mutex.Lock()
	gv.gvMp.KVStore = glwDeps.kvstore
	gv.gvMp.Mutex.Unlock()
	gv.gvMp.FAM = glwDeps.fam
	// C: #if CONFIG_GLW_BACKEND_OPENGL — mp_vdpau_dev hand-off
	// (glw_video_common.c:624-627) removed with the VDPAU engine.

	// C: LIST_INSERT_HEAD(&gr->gr_video_decoders, gv, gv_global_link)
	gv.gvGlobalLinkNext = gr.grVideoDecoders.lhFirst
	if gv.gvGlobalLinkNext != nil {
		gv.gvGlobalLinkNext.gvGlobalLinkPrev = &gv.gvGlobalLinkNext
	}
	gr.grVideoDecoders.lhFirst = gv
	gv.gvGlobalLinkPrev = &gr.grVideoDecoders.lhFirst

	gv.gvMp.VideoFrameDeliver = glwVideoInput
	gv.gvMp.SetVideoCodec = glwSetVideoCodec
	gv.gvMp.VideoFrameOpaque = gv

	gv.gvVd = decoder.NewVideoDecoder(gv.gvMp)
	backendcore.VideoPlaybackCreate(gv.gvMp)

	gv.gvVzoom = 100

	self := w.glwScope.gsRoots[glwRootSelf].P
	glwDeps.pm.Link(gv.gvMp.PropRoot, glwDeps.pm.CreateEx(self, "media", nil, false, false), nil, false, false)

	// ENABLE_MEDIA_SETTINGS — C uses PROP_TAG_SET_INT / PROP_TAG_SET_FLOAT
	// binding the variable directly; Go subscribes a trampoline that
	// writes the value — same semantics.
	c := gv.gvMp.PropCtrl

	gv.gvVoScalingSub = glwPropSubscribeTags(0,
		propTagCallbackFloat, func(_ any, v float64) { gv.gvVoScaling = float32(v) }, nil,
		propTagCourier, gr.grCourier,
		propTagRoot, c,
		propTagName, []string{"ctrl", "subscale"})

	gv.gvVoDisplaceYSub = glwPropSubscribeTags(0,
		propTagCallbackInt, func(_ any, v int) { gv.gvVoDisplaceY = v }, nil,
		propTagCourier, gr.grCourier,
		propTagRoot, c,
		propTagName, []string{"ctrl", "subvdisplace"})

	gv.gvVoDisplaceXSub = glwPropSubscribeTags(0,
		propTagCallbackInt, func(_ any, v int) { gv.gvVoDisplaceX = v }, nil,
		propTagCourier, gr.grCourier,
		propTagRoot, c,
		propTagName, []string{"ctrl", "subhdisplace"})

	gv.gvVzoomSub = glwPropSubscribeTags(0,
		propTagCallbackInt, func(_ any, v int) { gv.gvVzoom = v }, nil,
		propTagCourier, gr.grCourier,
		propTagRoot, c,
		propTagName, []string{"ctrl", "vzoom"})

	gv.gvPanHorizontalSub = glwPropSubscribeTags(0,
		propTagCallbackInt, func(_ any, v int) { gv.gvPanHorizontal = v }, nil,
		propTagCourier, gr.grCourier,
		propTagRoot, c,
		propTagName, []string{"ctrl", "panhorizontal"})

	gv.gvPanVerticalSub = glwPropSubscribeTags(0,
		propTagCallbackInt, func(_ any, v int) { gv.gvPanVertical = v }, nil,
		propTagCourier, gr.grCourier,
		propTagRoot, c,
		propTagName, []string{"ctrl", "panvertical"})

	gv.gvScaleHorizontalSub = glwPropSubscribeTags(0,
		propTagCallbackInt, func(_ any, v int) { gv.gvScaleHorizontal = v }, nil,
		propTagCourier, gr.grCourier,
		propTagRoot, c,
		propTagName, []string{"ctrl", "scalehorizontal"})

	gv.gvScaleVerticalSub = glwPropSubscribeTags(0,
		propTagCallbackInt, func(_ any, v int) { gv.gvScaleVertical = v }, nil,
		propTagCourier, gr.grCourier,
		propTagRoot, c,
		propTagName, []string{"ctrl", "scalevertical"})

	gv.gvHstretchSub = glwPropSubscribeTags(0,
		propTagCallbackInt, func(_ any, v int) { gv.gvHstretch = v }, nil,
		propTagCourier, gr.grCourier,
		propTagRoot, c,
		propTagName, []string{"ctrl", "hstretch"})

	gv.gvFstretchSub = glwPropSubscribeTags(0,
		propTagCallbackInt, func(_ any, v int) { gv.gvFstretch = v }, nil,
		propTagCourier, gr.grCourier,
		propTagRoot, c,
		propTagName, []string{"ctrl", "fstretch"})

	gv.gvVoOnVideoSub = glwPropSubscribeTags(0,
		propTagCallbackInt, func(_ any, v int) { gv.gvVoOnVideo = v }, nil,
		propTagCourier, gr.grCourier,
		propTagRoot, c,
		propTagName, []string{"ctrl", "subalign"})

	gv.gvVinterpolateSub = glwPropSubscribeTags(0,
		propTagCallbackInt, func(_ any, v int) { gv.gvVinterpolate = v }, nil,
		propTagCourier, gr.grCourier,
		propTagRoot, c,
		propTagName, []string{"ctrl", "vinterpolate"})
}

// C: static void mod_video_flags (glw_video_common.c:749-754)
func glwVideoModVideoFlags(w *Glw, set, clr int) {
	gv := (*GlwVideo)(unsafe.Pointer(w))
	gv.gvFlags = (gv.gvFlags | set) &^ clr
}

// C: static void set_how (glw_video_common.c:760-770)
func glwVideoSetHow(w *Glw, how string) {
	gv := (*GlwVideo)(unsafe.Pointer(w))

	// C: if(how == NULL) return — the view may evaluate $self.how to a
	// void rstr; "" is the Go-side NULL here (a literal "" how is
	// unreachable: no strcmp in video_player_idle matches it anyway).
	if how == "" {
		return
	}

	gv.gvHow = how
	glwVideoPlay(gv)
}

// C: static void set_source (glw_video_common.c:777-787)
func glwVideoSetSource(w *Glw, url *miscpkg.Rstr, flags int, origin *GlwStyle) {
	gv := (*GlwVideo)(unsafe.Pointer(w))

	if url == nil {
		return
	}

	gv.gvPendingURL = miscpkg.RstrGet(url)
	glwVideoPlay(gv)
}

// C: static void freeze (glw_video_common.c:793-798)
func glwVideoFreeze(w *Glw) {
	gv := (*GlwVideo)(unsafe.Pointer(w))
	gv.gvFreezed = 1
}

// C: static void thaw (glw_video_common.c:804-810)
func glwVideoThaw(w *Glw) {
	gv := (*GlwVideo)(unsafe.Pointer(w))
	gv.gvFreezed = 0
	glwVideoPlay(gv)
}

// C: static void set_audio_volume (glw_video_common.c:816-828)
func glwVideoSetAudioVolume(gv *GlwVideo, v float32) {
	mp := gv.gvMp

	if mp.VolUI == float64(v) {
		return
	}

	mp.Mutex.Lock()
	defer mp.Mutex.Unlock()
	mp.VolUI = float64(v)
	mediacore.MpSendVolumeUpdateLocked(mp)
}

// C: static int glw_video_set_float (glw_video_common.c:834-848)
func glwVideoSetFloat(w *Glw, attrib glwAttribute, value float32,
	origin *GlwStyle) int {
	gv := (*GlwVideo)(unsafe.Pointer(w))

	switch attrib {
	case glwAttribAudioVolume:
		glwVideoSetAudioVolume(gv, value)
		return 0 // Setting audio volume does not need to repaint UI
	default:
		return -1
	}
}

// C: static int glw_video_set_int (glw_video_common.c:854-874)
func glwVideoSetInt(w *Glw, attrib glwAttribute, value int,
	origin *GlwStyle) int {
	gv := (*GlwVideo)(unsafe.Pointer(w))

	switch attrib {
	case glwAttribPriority:
		gv.gvPriority = value

		e := glwDeps.em.CreateInt(eventpkg.EVENT_PLAYBACK_PRIORITY, value)
		mediacore.MpEnqueueEvent(gv.gvMp, &mediacore.MediaEvent{Type: int(e.Type), Data: e})
		e.Release()
		return 0
	default:
		return -1
	}
}

// C: static int glw_video_set_prop (glw_video_common.c:880-900)
func glwVideoSetProp(w *Glw, attrib glwAttribute, p *propcore.Prop) int {
	gv := (*GlwVideo)(unsafe.Pointer(w))

	switch attrib {
	case glwAttribPropItemModel:
		glwDeps.pm.RefDec(gv.gvItemModel)
		gv.gvItemModel = glwDeps.pm.RefInc(p)
		return 0

	case glwAttribPropParentModel:
		glwDeps.pm.RefDec(gv.gvParentModel)
		gv.gvParentModel = glwDeps.pm.RefInc(p)
		return 0

	default:
		return -1
	}
}

// C: static int glw_video_set_rstr (glw_video_common.c:906-921)
func glwVideoSetRstr(w *Glw, attrib glwAttribute, rstr *miscpkg.Rstr,
	origin *GlwStyle) int {
	gv := (*GlwVideo)(unsafe.Pointer(w))

	switch attrib {
	case glwAttribParentURL:
		miscpkg.RstrSet(&gv.gvParentURLX, rstr)
		return 0
	default:
		return -1
	}
}

// C: void glw_video_render (glw_video_common.c:929-980)
func glwVideoRender(w *Glw, rc *glwRctx) {
	gr := w.glwRoot
	gv := (*GlwVideo)(unsafe.Pointer(w))
	rc0 := *rc

	glwVideoRctxAdjust(&rc0, gv)

	gv.gvRwidth = int(rc0.rcWidth)
	gv.gvRheight = int(rc0.rcHeight)

	if glwIsFocusableOrClickable(w) {
		glwStoreMatrix(w, &rc0)
	}

	rc1 := rc0

	if gv.gvVzoom != 100 {
		zoom := float32(gv.gvVzoom) / 100.0
		glwScalef(&rc1, zoom, zoom, 1.0)
	}

	if gv.gvScaleHorizontal != 100 || gv.gvScaleVertical != 100 {
		glwScalef(&rc1, float32(gv.gvScaleHorizontal)/100.0,
			float32(gv.gvScaleVertical)/100.0, 1.0)
	}

	glwTranslatef(&rc1,
		float32(gv.gvPanHorizontal)/50.0,
		float32(gv.gvPanVertical)/50.0,
		0)

	glwProject(&gv.gvRect, &rc1, gr)

	if !glwRendererStarted(&gv.gvQuad) {
		glwRendererSetupQuad(&gv.gvQuad)

		glwRendererVtxPos(&gv.gvQuad, 0, -1, -1, 0)
		glwRendererVtxPos(&gv.gvQuad, 1, 1, -1, 0)
		glwRendererVtxPos(&gv.gvQuad, 2, 1, 1, 0)
		glwRendererVtxPos(&gv.gvQuad, 3, -1, 1, 0)
	}

	gv.gvSurfaceMutex.Lock()

	if gv.gvEngine != nil {
		gv.gvEngine.gveRender(gv, &rc1)
	}

	gv.gvSurfaceMutex.Unlock()

	glwVideoOverlayRender(gv, rc, &rc0)
}

// C: static int glw_video_set_int_unresolved (glw_video_common.c:986-1000)
func glwVideoSetIntUnresolved(w *Glw, a string, value int, gs *GlwStyle) int {
	gv := (*GlwVideo)(unsafe.Pointer(w))

	if a == "bottomOverlayDisplacement" {
		if gv.gvBottomOverlayDisplacement == value {
			return glwSetNoChange
		}
		gv.gvBottomOverlayDisplacement = value
		return glwSetRerenderRequired
	}

	return glwSetNotResponding
}

// C: static glw_class_t glw_video (glw_video_common.c:1006-1027)
var glwVideoClass = glwClass{
	gcName:         "video",
	gcInstanceSize: int(unsafe.Sizeof(GlwVideo{})),
	gcNew: func(parent *Glw) *Glw {
		gv := &GlwVideo{}
		return &gv.w
	},
	gcSetInt:           glwVideoSetInt,
	gcSetFloat:         glwVideoSetFloat,
	gcSetProp:          glwVideoSetProp,
	gcSetRstr:          glwVideoSetRstr,
	gcCtor:             glwVideoCtor,
	gcDtor:             glwVideoDtor,
	gcLayout:           glwVideoLayout,
	gcRender:           glwVideoRender,
	gcNewframe:         glwVideoNewframe,
	gcSignalHandler:    glwVideoWidgetCallback,
	gcModVideoFlags:    glwVideoModVideoFlags,
	gcSetSource:        glwVideoSetSource,
	gcSetHow:           glwVideoSetHow,
	gcFreeze:           glwVideoFreeze,
	gcThaw:             glwVideoThaw,
	gcSendEvent:        glwVideoWidgetEvent,
	gcPointerEvent:     glwVideoPointerEvent,
	gcSetIntUnresolved: glwVideoSetIntUnresolved,
}

// C: GLW_REGISTER_CLASS(glw_video) (glw_video_common.c:1029)
func registerVideoCommona() {
	glwVideoClass.gcInstanceSize = int(unsafe.Sizeof(GlwVideo{}))
	glwRegisterClass(&glwVideoClass)
}

// C: int glw_video_configure (glw_video_common.c:1035-1069)
func glwVideoConfigure(gv *GlwVideo, engine *glwVideoEngine) int {
	// C: hts_mutex_assert(&gv->gv_surface_mutex)

	if gv.gvEngine != engine {

		if gv.gvEngine != nil {
			gv.gvEngine.gveReset(gv)
		}

		gvsTAILQSetup(&gv.gvAvailQueue)
		gvsTAILQSetup(&gv.gvParkedQueue)
		gvsTAILQSetup(&gv.gvDisplayingQueue)
		gvsTAILQSetup(&gv.gvDecodedQueue)

		gv.gvEngine = engine

		if engine == nil {
			return 0
		}

		if engine.gveStartOnUIThread != 0 {
			gv.gvNeedStart = 1
			for {
				if gv.w.glwFlags&glwDestroying != 0 {
					return 1
				}
				if gv.gvNeedStart == 0 {
					break
				}
				gv.gvStartCond.Wait()
			}
		} else {
			engine.gveStart(gv)
		}
	}
	return 0
}

// C: void *glw_video_add_reap_task (glw_video_common.c:1075-1083)
func glwVideoAddReapTask(gv *GlwVideo, s int,
	fn func(gv *GlwVideo, ptr *glwVideoReapTask)) *glwVideoReapTask {
	t := &glwVideoReapTask{fn: fn}
	gvrLISTInsertHead(&gv.gvReaps, t)
	// C: hts_mutex_assert(&gv->gv_surface_mutex)
	return t
}

// C: glw_video_surface_t *glw_video_get_surface (glw_video_common.c:1089-1130)
func glwVideoGetSurface(gv *GlwVideo, w, h *[3]int) *glwVideoSurface {
	var gvs *glwVideoSurface
	// C: hts_mutex_assert(&gv->gv_surface_mutex)

	for {
		if gv.w.glwFlags&glwDestroying != 0 {
			return nil
		}

		if gvs = gv.gvAvailQueue.tqhFirst; gvs != nil {
			gvsTAILQRemove(&gv.gvAvailQueue, gvs)

			if w == nil {
				return gvs
			}

			if w[0] == gvs.gvsWidth[0] &&
				w[1] == gvs.gvsWidth[1] &&
				w[2] == gvs.gvsWidth[2] &&
				h[0] == gvs.gvsHeight[0] &&
				h[1] == gvs.gvsHeight[1] &&
				h[2] == gvs.gvsHeight[2] {
				return gvs
			}

			gvs.gvsWidth[0] = w[0]
			gvs.gvsWidth[1] = w[1]
			gvs.gvsWidth[2] = w[2]
			gvs.gvsHeight[0] = h[0]
			gvs.gvsHeight[1] = h[1]
			gvs.gvsHeight[2] = h[2]

			if gv.gvEngine != nil && gv.gvEngine.gveSurfaceSetup != nil {
				// C: if(gv->gv_engine->gve_surface_init &&
				//      gv->gv_engine->gve_surface_init(gv, gvs))
				//    return NULL;  (glw_video_common.c:1116-1119)
				if gv.gvEngine.gveSurfaceSetup(gv, gvs) != 0 {
					return nil
				}
				return gvs
			}

			gvsTAILQInsertTail(&gv.gvParkedQueue, gvs)
			continue
		}
		gv.gvAvailQueueCond.Wait()
	}
}

// C: void glw_video_put_surface (glw_video_common.c:1136-1148)
func glwVideoPutSurface(gv *GlwVideo, s *glwVideoSurface,
	pts int64, epoch int, duration int,
	interlaced int, yshift int) {
	s.gvsPts = pts
	s.gvsEpoch = epoch
	s.gvsDuration = duration
	s.gvsInterlaced = interlaced
	s.gvsYshift = yshift
	gvsTAILQInsertTail(&gv.gvDecodedQueue, s)
	// C: hts_mutex_assert(&gv->gv_surface_mutex)
}

// C: static LIST_HEAD(, glw_video_engine) engines (glw_video_common.c:1151)
// → glwVideoEngines package var above.

// C: void glw_register_video_engine (glw_video_common.c:1153-1157)
func glwRegisterVideoEngine(gve *glwVideoEngine) {
	gveLISTInsertHead(gve)
}

// C: static int glw_video_input (glw_video_common.c:1163-1197)
func glwVideoInput(fi *mediacore.FrameInfo, opaque any) int {

	gv := opaque.(*GlwVideo)
	var gve *glwVideoEngine
	var rval int

	gv.gvSurfaceMutex.Lock()

	if fi == nil {

		rval = 0

		if gv.gvEngine != nil && gv.gvEngine.gveBlackout != nil {
			gv.gvEngine.gveBlackout(gv)
		}

	} else {

		rval = 1

		gv.gvDarNum = fi.DARNum
		gv.gvDarDen = fi.DARDen
		gv.gvVheight = fi.Height

		for gve = glwVideoEngines.lhFirst; gve != nil; gve = gve.linkNext {
			if gve.gveType == fi.Type {
				rval = gve.gveDeliver(fi, gv, gve)
				break
			}
		}
	}

	gv.gvSurfaceMutex.Unlock()
	return rval
}

// C: static int glw_set_video_codec (glw_video_common.c:1203-1233)
func glwSetVideoCodec(codecType mediacore.FourCC, mc *mediacore.MediaCodec,
	opaque any, fi *mediacore.FrameInfo) int {
	gv := opaque.(*GlwVideo)
	var gve *glwVideoEngine
	r := -1
	gv.gvSurfaceMutex.Lock()

	if codecType == mediacore.FourCCNone { // C: 'none'
		glwVideoConfigure(gv, nil)
		gv.gvSurfaceMutex.Unlock()
		return 0
	}

	if fi != nil {
		gv.gvDarNum = fi.DARNum
		gv.gvDarDen = fi.DARDen
		gv.gvVheight = fi.Height
	}

	for gve = glwVideoEngines.lhFirst; gve != nil; gve = gve.linkNext {
		if gve.gveType == codecType {
			r = gve.gveSetCodec(mc, gv, fi, gve)
			break
		}
	}

	gv.gvSurfaceMutex.Unlock()
	return r
}

// C: void glw_video_surfaces_cleanup (glw_video_common.c:1240-1251)
func glwVideoSurfacesCleanup(gv *GlwVideo) {
	if gv.gvEngine != nil {
		gv.gvEngine.gveReset(gv)
	}

	glwVideoReap(gv)
	gvsTAILQSetup(&gv.gvAvailQueue)
	gvsTAILQSetup(&gv.gvParkedQueue)
	gvsTAILQSetup(&gv.gvDisplayingQueue)
	gvsTAILQSetup(&gv.gvDecodedQueue)
}

// C: void glw_video_reset (glw_video_common.c:1260-1274)
func glwVideoReset(gr *glwRoot) {
	for gv := gr.grVideoDecoders.lhFirst; gv != nil; gv = gv.gvGlobalLinkNext {
		gv.gvSurfaceMutex.Lock()
		glwVideoSurfacesCleanup(gv)
		gv.gvEngine = nil
		gv.gvSurfaceMutex.Unlock()
	}
}

// ---------------------------------------------------------------------------
// ENABLE_LIBAV

// C: static int video_deliver_lavc (glw_video_common.c:1281-1330)
func videoDeliverLavc(fi *mediacore.FrameInfo, gv *GlwVideo,
	e *glwVideoEngine) int {
	nfi := *fi

	switch fi.PixFmt {
	case medialibav.LavcPixFmtYUV420P,
		medialibav.LavcPixFmtYUV422P,
		medialibav.LavcPixFmtYUV444P,
		medialibav.LavcPixFmtYUV410P,
		medialibav.LavcPixFmtYUV411P,
		medialibav.LavcPixFmtYUV440P,
		medialibav.LavcPixFmtYUVJ420P,
		medialibav.LavcPixFmtYUVJ422P,
		medialibav.LavcPixFmtYUVJ444P,
		medialibav.LavcPixFmtYUVJ440P:
		nfi.HShift, nfi.VShift = medialibav.PixFmtChromaSubSample(nfi.PixFmt)
		nfi.Type = mediacore.FourCCYUVP // C: 'YUVP'

	case medialibav.LavcPixFmtVAAPI:
		nfi.Type = mediacore.FourCCVAAE // 'VAAE' — extension, no C counterpart

	case medialibav.LavcPixFmtBGR24:
		nfi.Type = mediacore.FourCCBGR // C: 'BGR' (3-char literal)

	case medialibav.LavcPixFmtYUV420P10LE:
		nfi.Type = mediacore.FourCCYUVp // C: 'YUVp'

	case medialibav.LavcPixFmtXYZ12LE:
		nfi.Type = mediacore.FourCCXYZ6 // C: 'XYZ6'

	default:
		return 1
	}

	var gve *glwVideoEngine
	for gve = glwVideoEngines.lhFirst; gve != nil; gve = gve.linkNext {
		if gve.gveType == nfi.Type {
			return gve.gveDeliver(&nfi, gv, gve)
		}
	}
	return 1
}

// C: static glw_video_engine_t glw_video_lavc (glw_video_common.c:1336-1339)
var glwVideoLavc = glwVideoEngine{
	gveType:    mediacore.FourCCLAVC, // C: 'LAVC'
	gveDeliver: videoDeliverLavc,
}

// C: GLW_REGISTER_GVE(glw_video_lavc) (glw_video_common.c:1341)
func registerVideoCommonb() { glwRegisterVideoEngine(&glwVideoLavc) }
