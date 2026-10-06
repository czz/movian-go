package glw

// C: src/ui/glw/glw.c — canonical 1:1 port.
//
// Global singletons used by C (event_mgr, the prop context, gconf.*,
// runcontrol) map to package-level seams configured at init time, matching
// the established convention (GconfClipboardSet, EsSetPropDeps, ...).

import (
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"unsafe"

	backendcore "github.com/czz/movian-go/internal/backend/core"

	"github.com/czz/movian-go/internal/db/kvstore"
	"github.com/czz/movian-go/internal/gconf"

	archpkg "github.com/czz/movian-go/internal/arch"
	"github.com/czz/movian-go/internal/asyncio"
	eventpkg "github.com/czz/movian-go/internal/event"
	facore "github.com/czz/movian-go/internal/fileaccess"
	imagepkg "github.com/czz/movian-go/internal/image"
	miscpkg "github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
	settingscore "github.com/czz/movian-go/internal/settings"
	"github.com/czz/movian-go/internal/text"
	tracepkg "github.com/czz/movian-go/internal/trace"
	uipkg "github.com/czz/movian-go/internal/ui"
)

// SetGconf injects the process gconf (C: gconf_t — owned by main).
func SetGconf(g *gconf.T) { glwDeps.gconf = g }

// C: const float glw_identitymtx[16]
var glwIdentitymtx = [16]float32{
	1, 0, 0, 0,
	0, 1, 0, 0,
	0, 0, 1, 0,
	0, 0, 0, 1,
}

// glwDeps — C: gconf / event_mgr / prop global seams, grouped.
var glwDeps = struct {
	pm  *propcore.PropManager     // C: implicit global prop context
	em  *eventpkg.EventManager    // C: event_mgr
	fam *facore.FileAccessManager // C: implicit global fa context

	// runcontrolActivity — C: runcontrol_activity() global
	runcontrolActivity func()
	screenshotDeliver  func(pm *imagepkg.Pixmap) // C: screenshot_deliver
	// appDataroot — C: app_dataroot() global
	appDataroot func() string
	// navCourierPoll — drains the navigator's event courier once per
	// frame, where C's glw_prepare_frame polls gr_courier (C routes
	// nav_eventsink through GLOBAL dispatch).
	navCourierPoll func()

	// kvstore — C: global kvstore_* funcs (glw_settings kv_prop_bind)
	kvstore *kvstore.KVStore

	// ts — C: trace() global reached by glw file statics
	ts *tracepkg.TraceSystem

	// textSys — C: freetype/fontstash statics (text_render,
	// text_parse, freetype_get_context)
	textSys *text.System

	// appShutdown — C: app_shutdown (main.c) called directly from the
	// window-close paths. Provider lives in cmd/movian-go.
	appShutdown func(retcode int)

	// bs — C: gconf.backend — backend system for backend_imageloader
	bs *backendcore.BackendSystem

	// clip — C: clipboard.c statics reached from the text widget's
	// ACTION_PASTE handler. Owned by the app context, injected.
	clip *uipkg.Clipboard

	// gconf — C: gconf_t gconf fields (debug_glw, skin, fullscreen,
	// convert_pointer_to_touch, enable_touch_debug...). Injected.
	gconf *gconf.T

	// aio — C: global asyncio loop, reached by the propProxyConnect
	// adapter. Owned by the app context, injected.
	aio *asyncio.AsyncIO

	// rec — C: glw_rec.c file-statics (glw_rec_mutex + glw_recs list)
	rec glwRecStateT

	// settings — C: glw_settings_t + glw_settings.c statics
	// (screensaver items/userImages + mutex)
	settings   glwSettingsT
	settingsSM *settingscore.SettingsManager

	// activeRoot — the running glw_root (set by glwStart4). Lets the
	// settings callbacks reach the live root — e.g. skin switching
	// posts ACTION_RELOAD_UI through glw_inject_event. Extension, no
	// C counterpart (C had no runtime skin switch).
	activeRoot *glwRoot
}{gconf: gconf.New(), textSys: text.NewSystem()}

// SetPropManager wires the global prop manager seam (C: implicit global
// prop context resolved via gconf/prop_get_global at link time).
func SetPropManager(pm *propcore.PropManager) { glwDeps.pm = pm }

// SetEventManager wires the global event manager seam (C: event_mgr).
func SetEventManager(em *eventpkg.EventManager) { glwDeps.em = em }

// SetKVStore wires the kvstore seam (C: global kvstore_* funcs).
func SetKVStore(kvs *kvstore.KVStore) { glwDeps.kvstore = kvs }

// SetAsyncIO wires the asyncio loop (C: global asyncio state) for the
// prop-proxy adapter.
func SetAsyncIO(aio *asyncio.AsyncIO) { glwDeps.aio = aio }

// SetTraceSystem wires the trace system (C: trace() global).
func SetTraceSystem(ts *tracepkg.TraceSystem) { glwDeps.ts = ts }

// SetTextSystem injects the text subsystem (C: freetype/fontstash
// statics reached by text_render / text_parse / freetype_get_context).
func SetTextSystem(ts *text.System) { glwDeps.textSys = ts }

// SetAppShutdown wires app_shutdown (C: main.c direct call from the
// window-close callbacks).
func SetAppShutdown(fn func(retcode int)) { glwDeps.appShutdown = fn }

// SetDeps wires the remaining global seams (C: runcontrol_activity,
// app_dataroot, and the nav-courier drain run inside glw_prepare_frame).
func SetDeps(appDataroot func() string, runcontrolActivity func(),
	navCourierPoll func()) {
	glwDeps.appDataroot = appDataroot
	glwDeps.runcontrolActivity = runcontrolActivity
	glwDeps.navCourierPoll = navCourierPoll
}

// C: #define GLW_TRACE(x, ...) (glw.h:41-46, DEBUG variant)
func glwTrace(format string, args ...any) {
	if glwDeps.gconf.DebugGLW {
		glwDeps.ts.Trace(tracepkg.TRACE_DEBUG, "GLW", format, args...)
	}
}

// ---------------------------------------------------------------------------
// C: void glw_cond_wait (glw.c:70-73)
func glwCondWait(gr *glwRoot, c *sync.Cond) {
	c.Wait() // C: hts_cond_wait(c, &gr->gr_mutex) — cond created with gr_mutex
}

// C: static void glw_update_underscan (glw.c:80-125)
func glwUpdateUnderscan(gr *glwRoot) {
	var val int

	if gr.grStartFlags&glwFlagOverscan != 0 {
		if gr.grHeight >= 1080 {
			gr.grBaseUnderscanH = 66
			gr.grBaseUnderscanV = 34
		} else if gr.grHeight >= 720 {
			gr.grBaseUnderscanH = 43
			gr.grBaseUnderscanV = 22
		} else {
			gr.grBaseUnderscanH = 36
			gr.grBaseUnderscanV = 20
		}
	}

	if glwDeps.settings.gsUnderscanH != gr.grUserUnderscanH ||
		glwDeps.settings.gsUnderscanV != gr.grUserUnderscanV {
		gr.grUserUnderscanH = glwDeps.settings.gsUnderscanH
		gr.grUserUnderscanV = glwDeps.settings.gsUnderscanV

		if gr.grUserUnderscanChanged > 0 {
			// Don't send change first time
			glwDeps.pm.SetVEx(nil, gr.grPropUi, "underscan_changes",
				gr.grUserUnderscanChanged)
		}
		gr.grUserUnderscanChanged++
	}

	val = int(glwClamp(float32(gr.grBaseUnderscanH+glwDeps.settings.gsUnderscanH),
		0, 100))

	if gr.grUnderscanH != val {
		glwDeps.pm.SetVEx(nil, gr.grPropUi, "underscan_h", val)
		gr.grUnderscanH = val
		gr.grNeedRefresh = glwRefreshFlagLayout | glwRefreshFlagRender
	}

	val = int(glwClamp(float32(gr.grBaseUnderscanV+glwDeps.settings.gsUnderscanV),
		0, 100))

	if gr.grUnderscanV != val {
		glwDeps.pm.SetVEx(nil, gr.grPropUi, "underscan_v", val)
		gr.grUnderscanV = val
		gr.grNeedRefresh = glwRefreshFlagLayout | glwRefreshFlagRender
	}
}

// C: static void glw_update_size (glw.c:135-161)
func glwUpdateSize(gr *glwRoot) {
	var val int
	bs1 := gr.grHeight / 35 // 35 is just something

	baseSize := bs1 // MIN(bs1, bs2);

	val = int(glwClamp(float32(baseSize+glwDeps.settings.gsSize+
		gr.grSkinScaleAdjustment), 8, 80))

	if gr.grCurrentSize != val {
		gr.grCurrentSize = val
		glwDeps.pm.SetVEx(nil, gr.grPropUi, "size", val)
		glwTextFlush(gr)
		glwIconFlush(gr)
		glwUpdateEm(gr)
		glwDeps.ts.Trace(tracepkg.TRACE_DEBUG, "GLW",
			"UI size scale changed to %d (user adj: %d  skin adj: %d) ",
			val, glwDeps.settings.gsSize, gr.grSkinScaleAdjustment)
	}
	glwUpdateUnderscan(gr)
}

// C: static void glw_sizeoffset_callback (glw.c:164-169)
func glwSizeoffsetCallback(opaque any, value int) {
	gr := opaque.(*glwRoot)
	gr.grSkinScaleAdjustment = value
}

// C: int glw_init (glw.c:186-190)
func glwStart(gr *glwRoot) int {
	return glwStart2(gr, 0)
}

// C: int glw_init2 (glw.c:194-198)
func glwStart2(gr *glwRoot, flags int) int {
	return glwStart4(gr, func(pc *propcore.Courier, timeout int) {
		pc.PollTimed(timeout)
	}, propcore.NewCourier("glw"), flags)
}

// C: int glw_init4 (glw.c:205-311)
func glwStart4(gr *glwRoot,
	dispatcher func(pc *propcore.Courier, timeout int),
	courier *propcore.Courier,
	flags int) int {
	var skinbuf [4096]byte // C: PATH_MAX
	skin := glwDeps.gconf.Skin
	var p *propcore.Prop

	atomic.StoreInt32(&gr.grRefcount, 1)

	if gr.grPropCore == nil {
		gr.grPropCore = glwDeps.pm.GetGlobal()
	}

	gr.grPropDispatcher = dispatcher
	gr.grCourier = courier
	gr.grStartFlags = flags
	gr.grPropMaxtime = -1

	if glwDeps.settings.gsSettings == nil {
		panic("glwDeps.settings.gsSettings == nil") // C: assert
	}

	p = glwDeps.pm.CreateEx(glwDeps.pm.GetGlobal(), "userinterfaces", nil, false, false)

	if glwDeps.pm.SetParentEx(gr.grPropUi, p, nil, "") != 0 {
		panic("prop_set_parent failed") // C: abort()
	}

	if skin == "" {
		skin = fmt.Sprintf("%s/glwskins/%s",
			glwDeps.appDataroot(), "flat") // C: SHOWTIME_GLW_DEFAULT_SKIN
		_ = skinbuf
	}
	gr.grMutex = sync.Mutex{}
	gr.grTokenPool = miscpkg.PoolCreate("glwtokens",
		int(unsafe.Sizeof(Token{})), miscpkg.PoolZeroMem)
	gr.grClonePool = miscpkg.PoolCreate("glwclone",
		int(unsafe.Sizeof(glwClone{})), miscpkg.PoolZeroMem)
	gr.grStyleBindingPool = miscpkg.PoolCreate("glwstylebindings",
		int(unsafe.Sizeof(glwStyleBinding{})), 0)

	gr.grUserUnderscanH = math.MinInt32
	gr.grUserUnderscanV = math.MinInt32

	gr.grSkin = skin // C: strdup

	// Extension: expose the live root to settings callbacks (skin
	// switch via ACTION_RELOAD_UI).
	glwDeps.activeRoot = gr

	gr.grFontDomain = glwDeps.textSys.FreetypeGetContext()

	glwTextBitmapStart(gr)

	glwDeps.pm.SetVEx(nil, gr.grPropUi, "skin.path", skin) // C: prop_setv skin/path

	gr.grPointerVisible = glwDeps.pm.CreateEx(gr.grPropUi, "pointerVisible", nil, false, false)
	gr.grScreensaverActive = glwDeps.pm.CreateEx(gr.grPropUi, "screensaverActive", nil, false, false)
	gr.grPropWidth = glwDeps.pm.CreateEx(gr.grPropUi, "width", nil, false, false)
	gr.grPropHeight = glwDeps.pm.CreateEx(gr.grPropUi, "height", nil, false, false)
	gr.grPropAspect = glwDeps.pm.CreateEx(gr.grPropUi, "aspect", nil, false, false)

	glwDeps.pm.SetIntEx(gr.grScreensaverActive, nil, 0)

	if flags&glwFlagKeyboardMode != 0 {
		glwSetKeyboardMode(gr, 1)
	}

	if flags&glwFlagInFullscreen != 0 {
		gr.grIsFullscreen = 1
	}

	gr.grEvsub = glwPropSubscribeTags(0,
		propTagCallback, glwEventsink, gr,
		propTagName, []string{"ui", "eventSink"},
		propTagRoot, gr.grPropUi,
		propTagCourier, gr.grCourier,
	)

	gr.grScalesub = glwPropSubscribeTags(0,
		propTagCallbackInt, glwSizeoffsetCallback, gr,
		propTagName, []string{"ui", "sizeOffset"},
		propTagRoot, gr.grPropUi,
		propTagCourier, gr.grCourier,
	)

	gr.grDisableScreensaverSub = glwPropSubscribeTags(0,
		propTagCallbackInt, glwDisScreensaverCallback, gr,
		propTagName, []string{"ui", "disableScreensaver"},
		propTagRoot, gr.grPropUi,
		propTagCourier, gr.grCourier,
	)

	// C: TAILQ_INIT x3 + hts_cond_init
	gr.grDestroyerQueue.tqhFirst = nil
	gr.grDestroyerQueue.tqhLast = &gr.grDestroyerQueue.tqhFirst
	gr.grViewLoadRequests.tqhFirst = nil
	gr.grViewLoadRequests.tqhLast = &gr.grViewLoadRequests.tqhFirst
	gr.grViewEvalRequests.tqhFirst = nil
	gr.grViewEvalRequests.tqhLast = &gr.grViewEvalRequests.tqhFirst
	gr.grViewLoaderCond = sync.Cond{L: &gr.grMutex}

	glwTexStart(gr)

	gr.grFrontface = glwCcw

	gr.grFramerate = 60
	// C: 1000000 / gr->gr_framerate — float division (glw.c:308)
	gr.grFrameduration = int(1000000 / gr.grFramerate)
	gr.grUiStart = archpkg.GetTS()
	gr.grFrameStart = gr.grUiStart
	glwRegisterActivity(gr)
	gr.grOpenOsk = glwOskOpenDefault
	websurfaceInit(gr)
	return 0
}

// C: void glw_fini (glw.c:321-367)
func glwFini(gr *glwRoot) {
	websurfaceFini(gr)
	if gr.grOskWidget != nil {
		glwUnref(gr.grOskWidget)
		glwDeps.pm.Unsubscribe(gr.grOskTextSub)
		glwDeps.pm.Unsubscribe(gr.grOskEvSub)
	}

	gr.grViewLoaderRun = 0
	gr.grViewLoaderCond.Signal() // C: hts_cond_signal

	glwTextBitmapFini(gr)
	miscpkg.RstrRelease(gr.grDefaultFont)
	glwTexFini(gr)
	glwDeps.pm.Unsubscribe(gr.grEvsub)
	glwDeps.pm.Unsubscribe(gr.grScalesub)
	glwDeps.pm.Unsubscribe(gr.grDisableScreensaverSub)
	gr.grCourier.Release() // C: prop_courier_destroy(gr->gr_courier)

	// The view loader thread sometimes run with gr_mutex unlocked
	// and when doing so it expects certain variables in glw_root to
	// be available. gr_vpaths (indirectly gr_skin) must be intact.
	// It may also allocate items from gr_token_pool (although not while
	// locked but after we've asked it to exit), thus we must not
	// destroy the pool until after the thread has joined.

	if gr.grViewLoaderThread != nil {
		gr.grViewLoaderThread.Join() // C: hts_thread_join
	}

	glwViewLoaderFlush(gr)

	glwStyleCleanup(gr)

	gr.grTokenPool.PoolDestroy()
	gr.grClonePool.PoolDestroy()
	gr.grStyleBindingPool.PoolDestroy()

	// C: free(gr->gr_vtmp_buffer) etc. — GC
	gr.grVtmpBuffer = nil
	gr.grRenderJobs = nil
	gr.grRenderOrder = nil
	gr.grVertexBuffer = nil
	gr.grIndexBuffer = nil
	miscpkg.RstrRelease(gr.grPendingFocus)
}

// C: void glw_release_root (glw.c:372-381)
func glwReleaseRoot(gr *glwRoot) {
	if atomic.AddInt32(&gr.grRefcount, -1) != 0 {
		return
	}
	// C: hts_mutex_destroy, free(gr->gr_skin), free(gr) — GC
}

// C: void glw_unload_universe (glw.c:386-396)
func glwUnloadUniverse(gr *glwRoot) {
	glwViewCacheFlush(gr)

	if gr.grUniverse != nil {
		glwDestroy(gr.grUniverse)
	}

	glwFlush(gr)
}

// C: void glw_load_universe (glw.c:401-424)
func glwLoadUniverse(gr *glwRoot) {
	glwUnloadUniverse(gr)

	universe := miscpkg.RstrAllocStr(facore.FAPathjoin(gr.grSkin, "universe.view"))

	scope := glwScopeCreate()

	scope.gsRoots[glwRootCore].P = glwDeps.pm.RefInc(gr.grPropCore)

	gr.grUniverse = glwViewCreate(gr, universe, nil, nil, scope, nil, 0)

	miscpkg.RstrRelease(universe)
	glwScopeRelease(scope)
}

// C: void glw_reap (glw.c:636-657)
func glwReap(gr *glwRoot) {
	// C: LIST_MOVE(&gr->gr_active_flush_list, &gr->gr_active_list, glw_active_link)
	if gr.grActiveList.lhFirst != nil {
		gr.grActiveList.lhFirst.glwActiveLinkPrev =
			&gr.grActiveFlushList.lhFirst
	}
	gr.grActiveFlushList.lhFirst = gr.grActiveList.lhFirst
	// C: LIST_INIT(&gr->gr_active_list)
	gr.grActiveList.lhFirst = nil

	glwTexPurge(gr)

	glwTexAutoflush(gr)

	for {
		w := gr.grDestroyerQueue.tqhFirst
		if w == nil {
			break
		}
		// C: TAILQ_REMOVE(&gr->gr_destroyer_queue, w, glw_parent_link)
		if w.glwParentLinkNext != nil {
			w.glwParentLinkNext.glwParentLinkPrev = w.glwParentLinkPrev
		} else {
			gr.grDestroyerQueue.tqhLast = w.glwParentLinkPrev
		}
		*w.glwParentLinkPrev = w.glwParentLinkNext

		if w.glwClass.gcDtor != nil {
			w.glwClass.gcDtor(w)
		}

		glwSignalHandlerClean(w)
		glwUnref(w)
	}
}

// C: void glw_idle (glw.c:662-668)
func glwIdle(gr *glwRoot) {
	if gr.grPropDispatcher != nil {
		gr.grPropDispatcher(gr.grCourier, gr.grPropMaxtime)
	}
	glwReap(gr)
}

// C: void glw_prepare_frame (glw.c:673-757)
func glwPrepareFrame(gr *glwRoot, flags int) {
	glwUpdateSize(gr)

	gr.grFrameStart = archpkg.GetTS()
	gr.grFrameStartAvtime = archpkg.GetAvtime()
	gr.grTimeUsec = uint64(gr.grFrameStart - gr.grUiStart)
	gr.grTimeSec = float64(gr.grTimeUsec) / 1000000.0

	if flags&glwNoFramerateUpdate == 0 {

		if gr.grFrames > 16 { // C: likely(gr->gr_frames > 16)
			d := gr.grFrameStart - gr.grFramerateAvg[gr.grFrames&0xf]
			hz := 16000000.0 / float64(d)
			// C: prop_set(gr->gr_prop_ui, "framerate", PROP_SET_FLOAT, hz)
			gr.grPropUi.CreateFloat("framerate", hz)
			gr.grFramerate = float32(hz)
		}

		gr.grFramerateAvg[gr.grFrames&0xf] = gr.grFrameStart
	}
	gr.grFrames++

	gr.grNumRenderJobs = 0
	gr.grVertexOffset = 0
	gr.grIndexOffset = 0

	gr.grScreensaverActive.SetInt(glwScreensaverIsActive(gr))
	gr.grPropWidth.SetInt(gr.grWidth)
	gr.grPropHeight.SetInt(gr.grHeight)
	gr.grPropAspect.SetFloat(float32(gr.grWidth) / float32(gr.grHeight))

	if gr.grPropDispatcher != nil {
		gr.grPropDispatcher(gr.grCourier, gr.grPropMaxtime)
	}
	if glwDeps.navCourierPoll != nil {
		glwDeps.navCourierPoll()
	}

	for w := gr.grEveryFrameList.lhFirst; w != nil; w = w.glwEveryFrameLinkNext {
		w.glwClass.gcNewframe(w, flags)
	}

	if gr.grNeedRefresh != 0 {

		for {
			w := gr.grActiveFlushList.lhFirst
			if w == nil {
				break
			}
			// C: LIST_REMOVE(w, glw_active_link)
			if w.glwActiveLinkNext != nil {
				w.glwActiveLinkNext.glwActiveLinkPrev = w.glwActiveLinkPrev
			}
			*w.glwActiveLinkPrev = w.glwActiveLinkNext
			// C: LIST_INSERT_HEAD(&gr->gr_active_dummy_list, w, glw_active_link)
			w.glwActiveLinkNext = gr.grActiveDummyList.lhFirst
			if w.glwActiveLinkNext != nil {
				w.glwActiveLinkNext.glwActiveLinkPrev = &w.glwActiveLinkNext
			}
			gr.grActiveDummyList.lhFirst = w
			w.glwActiveLinkPrev = &gr.grActiveDummyList.lhFirst
			w.glwFlags &^= glwActive
			glwSignal0(w, glwSignalInactive, nil)
		}

		glwReap(gr)
	}

	if gr.grMouseValid != 0 {
		var gpe glwPointerEventT
		gpe.ts = 0
		gpe.screenX = gr.grMouseX
		gpe.screenY = gr.grMouseY
		gpe.typ = glwPointerMotionRefresh
		glwPointerEvent(gr, &gpe)
	}

	if gr.grPointerPress != nil && // C: unlikely
		gr.grPointerPressTime != 0 &&
		gr.grFrameStart > gr.grPointerPressTime+500000 {
		// touch longpress
		glwTouchLongpress(gr)
	}

	if gr.grDelayedFocusLeave != 0 {
		gr.grDelayedFocusLeave--
		if gr.grDelayedFocusLeave == 0 && gr.grCurrentFocus != nil {
			glwFocusLeave(gr.grCurrentFocus)
		}
	}

	if gr.grScheduledRefresh <= gr.grFrameStart {
		gr.grNeedRefresh = glwRefreshFlagLayout | glwRefreshFlagRender
		gr.grScheduledRefresh = math.MaxInt64
	}

	if gr.grRec != nil {
		gr.grNeedRefresh = glwRefreshFlagLayout | glwRefreshFlagRender
	}

	glwViewLoaderEval(gr)
}

// C: void glw_post_scene (glw.c:763-774)
func glwPostScene(gr *glwRoot) {
	glwRendererRender(gr)
	// C: #if CONFIG_GLW_REC (glw.c:767-772) — glwRecPostScene is a no-op
	//    without the "glwrec" build tag.
	glwRecPostScene(gr)
}

// C: void glw_flush (glw.c:1071-1077)
func glwFlush(gr *glwRoot) {
	glwGfDo()
	glwTexFlushAll(gr)
	glwTextFlush(gr)
}

// C: void glw_need_refresh0 (glw.c:3410-3425, GLW_TRACK_REFRESH variant)
func glwNeedRefresh0(gr *glwRoot, how int, file string, line int) {
	flags := glwRefreshFlagLayout

	if how != glwRefreshLayoutOnly {
		flags |= glwRefreshFlagRender
	}

	if gr.grNeedRefresh&flags == flags {
		return
	}

	gr.grNeedRefresh |= flags
	if glwDeps.gconf.DebugGLW {
		glwDeps.ts.Trace(tracepkg.TRACE_DEBUG,
			"GLW", "%s%srefresh requested by %s:%d",
			map[bool]string{true: "layout ", false: ""}[flags&glwRefreshFlagLayout != 0],
			map[bool]string{true: "render ", false: ""}[flags&glwRefreshFlagRender != 0],
			file, line)
	}
}

// C: static void glw_update_dynamics_r (glw.c:3428-3437)
func glwUpdateDynamicsR(w *Glw, flags int) {
	for c := w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		glwUpdateDynamicsR(c, flags)
	}

	if int(w.glwDynamicEval)&flags != 0 {
		glwViewEvalDynamics(w, flags)
	}
}

// C: static void glw_update_em (glw.c:3440-3447)
func glwUpdateEm(gr *glwRoot) {
	if gr.grUniverse != nil {
		glwUpdateDynamicsR(gr.grUniverse, GLW_VIEW_EVAL_EM)
	}

	glwStyleUpdateEm(gr)
}

// C: static int glw_set_keyboard_mode (glw.c:3450-3464)
func glwSetKeyboardMode(gr *glwRoot, on int) int {
	if gr.grKeyboardMode == on {
		return 0
	}

	gr.grKeyboardMode = on
	gr.grPropUi.CreateInt("keyboard", on) // C: prop_set keyboard PROP_SET_INT

	if gr.grUniverse != nil {
		glwUpdateDynamicsR(gr.grUniverse, GLW_VIEW_EVAL_FHP_CHANGE)
	}

	glwNeedRefresh(gr, 0)
	return 1
}
