//go:build rpi

// glw_rpi.go — C: the UI portions of src/arch/rpi/rpi_main.c —
// ui_create / ui_run / rpi_mainloop driving GLW over dispmanx+EGL,
// plus the dispmanx background-element backdrop machinery
// (set_bg_image / pick_backdrop / backdrop_loader).
package glw

/*
#cgo CFLAGS: -I${SRCDIR}/../../arch/rpi

#include <stdlib.h>
#include <string.h>
#include <bcm_host.h>
#include <EGL/egl.h>
#include <GLES2/gl2.h>
#include <interface/vmcs_host/vc_dispmanx.h>

// VC_IMAGE_TYPE_T / resource_write_data are plain functions but take
// VC_IMAGE_TYPE_T which cgo can't express — small wrappers.
static int resource_write_bgr(DISPMANX_RESOURCE_HANDLE_T res, int pitch,
			      const void *data, const VC_RECT_T *rect) {
	return vc_dispmanx_resource_write_data(res, VC_IMAGE_RGBX32, pitch,
					       data, rect);
}
static EGLDisplay egl_get_default_display(void) {
	return eglGetDisplay(EGL_DEFAULT_DISPLAY);
}
static int egl_no_display(EGLDisplay d) { return d == EGL_NO_DISPLAY; }
static EGLContext egl_make_ctx(EGLDisplay d, EGLConfig c, const EGLint *attr) {
	return eglCreateContext(d, c, EGL_NO_CONTEXT, attr);
}
static int egl_no_context(EGLContext c) { return c == EGL_NO_CONTEXT; }
static EGLSurface egl_make_win(EGLDisplay d, EGLConfig c,
			       EGL_DISPMANX_WINDOW_T *nw) {
	return eglCreateWindowSurface(d, c, nw, NULL);
}
static int egl_no_surface(EGLSurface s) { return s == EGL_NO_SURFACE; }
static void egl_unbind(EGLDisplay d) {
	eglMakeCurrent(d, EGL_NO_SURFACE, EGL_NO_SURFACE, EGL_NO_CONTEXT);
}
static int resource_write_rgb24(DISPMANX_RESOURCE_HANDLE_T res, int pitch,
				const void *data, const VC_RECT_T *rect) {
	return vc_dispmanx_resource_write_data(res, VC_IMAGE_RGB888, pitch,
					       data, rect);
}
*/
import "C"

import (
	"bytes"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/czz/movian-go/internal/arch"
	archrpi "github.com/czz/movian-go/internal/arch/rpi"
	backendcore "github.com/czz/movian-go/internal/backend/core"
	imagepkg "github.com/czz/movian-go/internal/image"
	miscpkg "github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/trace"
)

// ---------------------------------------------------------------------------
// C: static DISPMANX_ELEMENT_HANDLE_T bg_element; static int bg_resource;
//     static float bg_current_alpha; static VC_RECT_T bg_src_rect, bg_dst_rect
// (rpi_main.c:142-146)
// ---------------------------------------------------------------------------

var (
	bgElement      C.DISPMANX_ELEMENT_HANDLE_T
	bgResource     C.DISPMANX_RESOURCE_HANDLE_T
	bgCurrentAlpha float32
	bgSrcRect      C.VC_RECT_T
	bgDstRect      C.VC_RECT_T
)

// bgRefreshElement — C: bg_refresh_element (rpi_main.c:153-189).
// Must be called under gr_lock().
func bgRefreshElement(forceFlush int) {
	u := C.vc_dispmanx_update_start(0)

	var alpha C.VC_DISPMANX_ALPHA_T
	alpha.flags = C.DISPMANX_FLAGS_ALPHA_FIXED_ALL_PIXELS
	alpha.opacity = C.uint32_t(bgCurrentAlpha * 255)
	alpha.mask = 0

	if forceFlush != 0 {
		C.vc_dispmanx_element_remove(u, bgElement)
		bgElement = 0
	}

	if alpha.opacity == 0 {
		if bgElement != 0 {
			C.vc_dispmanx_element_remove(u, bgElement)
			bgElement = 0
		}
	} else if bgElement == 0 {
		if bgResource != 0 {
			bgElement = C.vc_dispmanx_element_add(u,
				C.DISPMANX_DISPLAY_HANDLE_T(archrpi.DispmanDisplay),
				-10, &bgDstRect, bgResource, &bgSrcRect,
				C.DISPMANX_PROTECTION_NONE, &alpha, nil, 0)
		}
	} else {
		C.vc_dispmanx_element_change_attributes(u, bgElement,
			1<<1, 0, C.uint8_t(alpha.opacity), nil, nil, 0, 0)
	}
	C.vc_dispmanx_update_submit_sync(u)
}

// setBgImage — C: set_bg_image (rpi_main.c:195-261)
func setBgImage(url *miscpkg.Rstr, gr *glwRoot) {
	var w, h C.uint32_t

	gr.grMutex.Unlock() // C: glw_unlock(gr)

	C.graphics_get_display_size(0, &w, &h)

	im := &backendcore.ImageMeta{}
	im.ReqWidth = int(w)
	im.ReqHeight = int(h)

	var img *imagepkg.Image
	var ierr error
	if glwDeps.bs != nil {
		var img0 any
		img0, ierr = glwDeps.bs.Imageloader(miscpkg.RstrGet(url), im,
			nil, nil, nil)
		img, _ = img0.(*imagepkg.Image)
	}
	gr.grMutex.Lock() // C: glw_lock(gr)

	if img == nil {
		errStr := ""
		if ierr != nil {
			errStr = ierr.Error()
		}
		glwDeps.ts.Trace(trace.TRACE_ERROR, "BG", "Unable to load %s -- %s",
			miscpkg.RstrGet(url), errStr)
		return
	}

	ic := img.FindComponent(imagepkg.ComponentPixmap)
	if ic == nil {
		img.Release()
		return
	}

	pm := ic.Pixmap

	switch pm.Type {
	case imagepkg.PixmapBGR32:
	case imagepkg.PixmapRGB24:
	default:
		glwDeps.ts.Trace(trace.TRACE_ERROR, "BG", "Can't handle format %d",
			pm.Type)
		img.Release()
		return
	}

	if bgResource != 0 {
		C.vc_dispmanx_resource_delete(bgResource)
	}

	var ip C.uint32_t
	var it C.VC_IMAGE_TYPE_T
	if pm.Type == imagepkg.PixmapBGR32 {
		it = C.VC_IMAGE_RGBX32
	} else {
		it = C.VC_IMAGE_RGB888
	}

	bgResource = C.vc_dispmanx_resource_create(it,
		C.uint32_t(pm.Width), C.uint32_t(pm.Height), &ip)

	C.vc_dispmanx_rect_set(&bgSrcRect, 0, 0,
		C.uint32_t(pm.Width)<<16, C.uint32_t(pm.Height)<<16)
	C.vc_dispmanx_rect_set(&bgDstRect, 0, 0,
		C.uint32_t(pm.Width), C.uint32_t(pm.Height))

	data := unsafe.Pointer(&pm.Data[0])
	if pm.Type == imagepkg.PixmapBGR32 {
		C.resource_write_bgr(bgResource, C.int32_t(pm.Stride),
			data, &bgDstRect)
	} else {
		C.resource_write_rgb24(bgResource, C.int32_t(pm.Stride),
			data, &bgDstRect)
	}

	img.Release()

	bgRefreshElement(1)
}

// setBgAlpha — C: set_bg_alpha (rpi_main.c:267-275)
func setBgAlpha(alpha float32) {
	if bgCurrentAlpha == alpha {
		return
	}
	bgCurrentAlpha = alpha
	bgRefreshElement(0)
}

// ---------------------------------------------------------------------------
// C: backdrop machinery (rpi_main.c:277-410)
// ---------------------------------------------------------------------------

// backdropRelease — C: backdrop_release (rpi_main.c:297-305)
func backdropRelease(b *backdrop) {
	b.refcount--
	if b.refcount > 0 {
		return
	}
	miscpkg.RstrRelease(b.url)
}

// backdropLoader — C: backdrop_loader (rpi_main.c:311-342)
func backdropLoader(aux any) any {
	gr := aux.(*glwRoot)

	gr.grMutex.Lock() // C: glw_lock(gr)

	for gr.bdRun {
		if gr.bdPending == nil {
			gr.bdCond.Wait() // C: hts_cond_wait(..., &gr->gr_mutex)
			continue
		}

		if gr.bdCurrent != nil {
			backdropRelease(gr.bdCurrent)
		}

		tgt := gr.bdPending.url
		gr.bdCurrent = gr.bdPending
		gr.bdPending = nil

		glwDeps.ts.Trace(trace.TRACE_DEBUG, "RPI", "Backdrop loading %s",
			miscpkg.RstrGet(tgt))
		setBgImage(tgt, gr)
	}
	if gr.bdCurrent != nil {
		backdropRelease(gr.bdCurrent)
	}
	gr.bdCurrent = nil
	gr.grMutex.Unlock() // C: glw_unlock(gr)
	return nil
}

// pickBackdrop — C: pick_backdrop (rpi_main.c:349-410)
func pickBackdrop(gr *glwRoot) {
	var path [512]byte
	var alpha float32

	for i := range gr.grExternalizeCnt {
		if glwImageGetDetails(gr.grExternalized[i], path[:], &alpha) != 0 {
			continue
		}
		p := string(bytes.TrimRight(path[:], "\x00"))

		var b *backdrop
		for _, e := range gr.bdList {
			if miscpkg.RstrGet(e.url) == p {
				b = e
				break
			}
		}

		if b == nil {
			b = &backdrop{}
			b.url = miscpkg.RstrAllocStr(p)
			gr.bdList = slices.Insert(gr.bdList, 0, b)
			b.refcount = 1
		}

		b.mark = true
		b.alpha = alpha
	}

	var best *backdrop

	kept := gr.bdList[:0]
	for _, b := range gr.bdList {
		if b.mark {
			if best == nil || b.alpha > best.alpha {
				best = b
			}
			b.mark = false
			kept = append(kept, b)
		} else {
			backdropRelease(b)
		}
	}
	gr.bdList = kept

	if best == nil {
		setBgAlpha(0)
		return
	}

	if gr.bdCurrent != nil {
		setBgAlpha(gr.bdCurrent.alpha)
	}

	if best == gr.bdPending || best == gr.bdCurrent {
		return // have correct or at least on our way
	}

	if gr.bdPending != nil {
		backdropRelease(gr.bdPending)
	}

	gr.bdPending = best
	best.refcount++
	gr.bdCond.Signal() // C: hts_cond_signal
}

// ---------------------------------------------------------------------------
// C: ui_should_run / ui_run / rpi_mainloop (rpi_main.c:484-742)
// ---------------------------------------------------------------------------

// glwRpi — the GLW/RPi frontend instance (cf. glwX11 / glwGlfw)
type glwRpi struct {
	gr      glwRoot
	thread  *arch.Thread
	running atomic.Bool
	dpy     C.EGLDisplay
}

// uiShouldRun — C: ui_should_run (rpi_main.c:484-500)
func uiShouldRun() int {
	if archrpi.Runmode != archrpi.RunmodeRunning {
		return 0
	}
	if archrpi.Ctrlc != 0 {
		return 0
	}
	if archrpi.CecWeAreNotActive != 0 {
		return 0
	}
	if archrpi.DisplayStatus != archrpi.DisplayStatusOn {
		return 0
	}
	return 1
}

// uiRun — C: ui_run (rpi_main.c:505-695)
func (g *glwRpi) uiRun(dpy C.EGLDisplay) {
	var numConfig C.EGLint
	var screenWidth, screenHeight C.uint32_t
	var nw C.EGL_DISPMANX_WINDOW_T

	attributeList := []C.EGLint{
		C.EGL_RED_SIZE, 8,
		C.EGL_GREEN_SIZE, 8,
		C.EGL_BLUE_SIZE, 8,
		C.EGL_ALPHA_SIZE, 8,
		C.EGL_SURFACE_TYPE, C.EGL_WINDOW_BIT,
		C.EGL_NONE,
	}

	contextAttributes := []C.EGLint{
		C.EGL_CONTEXT_CLIENT_VERSION, 2,
		C.EGL_NONE,
	}

	C.glGetError()

	var config C.EGLConfig

	for {
		// get an appropriate EGL frame buffer configuration
		C.eglChooseConfig(dpy, &attributeList[0],
			&config, 1, &numConfig)

		if C.glGetError() != 0 {
			C.sleep(1)
			continue
		}
		break
	}

	// get an appropriate EGL frame buffer configuration
	result := C.eglBindAPI(C.EGL_OPENGL_ES_API)
	if result == C.EGL_FALSE {
		glwDeps.ts.Trace(trace.TRACE_ERROR, "RPI", "Unable to bind EGL API")
		os.Exit(2)
	}

	// create an EGL rendering context
	context := C.egl_make_ctx(dpy, config,
		&contextAttributes[0])

	if C.egl_no_context(context) != 0 {
		glwDeps.ts.Trace(trace.TRACE_ERROR, "RPI", "Unable to create context")
		os.Exit(2)
	}

	// create an EGL window surface
	success := C.graphics_get_display_size(0, &screenWidth, &screenHeight)
	if success < 0 {
		glwDeps.ts.Trace(trace.TRACE_ERROR, "RPI", "Unable to get display size")
		os.Exit(2)
	}

	var alpha C.VC_DISPMANX_ALPHA_T
	alpha.flags = C.DISPMANX_FLAGS_ALPHA_FROM_SOURCE |
		C.DISPMANX_FLAGS_ALPHA_PREMULT
	alpha.opacity = 255
	alpha.mask = 0

	u := C.vc_dispmanx_update_start(0)

	var dstRect, srcRect C.VC_RECT_T
	dstRect.x = 0
	dstRect.y = 0
	dstRect.width = C.int32_t(screenWidth)
	dstRect.height = C.int32_t(screenHeight)

	srcRect.x = 0
	srcRect.y = 0
	srcRect.width = C.int32_t(screenWidth << 16)
	srcRect.height = C.int32_t(screenHeight << 16)

	de := C.vc_dispmanx_element_add(u,
		C.DISPMANX_DISPLAY_HANDLE_T(archrpi.DispmanDisplay),
		10, &dstRect, 0, &srcRect, C.DISPMANX_PROTECTION_NONE,
		&alpha, nil, 0)

	nw.element = de
	nw.width = C.int(screenWidth)
	nw.height = C.int(screenHeight)
	C.vc_dispmanx_update_submit_sync(u)

	surface := C.egl_make_win(dpy, config, &nw)
	if C.egl_no_surface(surface) != 0 {
		glwDeps.ts.Trace(trace.TRACE_ERROR, "EGL",
			"Unable to create %d x %d window surface -- 0x%x",
			uint32(screenWidth), uint32(screenHeight), int(C.eglGetError()))
		os.Exit(2)
	}

	// connect the context to the surface
	result = C.eglMakeCurrent(dpy, surface, surface, context)
	if result == C.EGL_FALSE {
		glwDeps.ts.Trace(trace.TRACE_ERROR, "RPI",
			"Unable to make surface current context")
		os.Exit(2)
	}

	gr := &g.gr
	gr.grWidth = int(screenWidth)
	gr.grHeight = int(screenHeight)

	glwOpenglSetupContext(gr)

	C.glClearColor(0, 0, 0, 0)

	glwDeps.ts.Trace(trace.TRACE_DEBUG, "RPI", "UI starting")

	gr.bdCond = sync.NewCond(&gr.grMutex)

	gr.bdRun = true
	loaderTid := arch.ThreadCreateJoinable("bgloader",
		backdropLoader, gr, arch.ThreadPrioUIWorkerLow)

	for uiShouldRun() != 0 {
		if archrpi.RestartUI != 0 {
			archrpi.RestartUI = 0
			break
		}

		gr.grMutex.Lock() // C: glw_lock(gr)

		glwPrepareFrame(gr, 0)

		refresh := gr.grNeedRefresh
		gr.grNeedRefresh = 0
		if refresh != 0 {
			var zmax int
			var rc glwRctx

			gr.grCanExternalize = 1
			gr.grExternalizeCnt = 0
			glwRctxSetup(&rc, gr.grWidth, gr.grHeight, 1, &zmax)
			glwLayout0(gr.grUniverse, &rc)

			if refresh&glwRefreshFlagRender != 0 {
				C.glViewport(0, 0, C.GLsizei(gr.grWidth),
					C.GLsizei(gr.grHeight))
				C.glClear(C.GL_DEPTH_BUFFER_BIT | C.GL_COLOR_BUFFER_BIT)
				glwRender0(gr.grUniverse, &rc)
			}

			pickBackdrop(gr)
		}

		gr.grMutex.Unlock() // C: glw_unlock(gr)
		if refresh&glwRefreshFlagRender != 0 {
			glwPostScene(gr)
			C.eglSwapBuffers(dpy, surface)
		} else {
			arch.USleep(16666) // C: usleep(16666)
		}
	}
	gr.grMutex.Lock() // C: glw_lock(gr)

	glwReap(gr)
	glwReap(gr)
	glwFlush(gr)
	glwOpenglFiniContext(gr)

	gr.bdRun = false
	gr.bdCond.Signal()
	gr.grMutex.Unlock() // C: glw_unlock(gr)

	loaderTid.Join()

	gr.bdCond = nil

	C.egl_unbind(dpy)
	C.eglDestroySurface(dpy, surface)
	C.eglDestroyContext(dpy, context)
	glwDeps.ts.Trace(trace.TRACE_DEBUG, "RPI", "UI terminated")
}

// rpiMainloop — C: rpi_mainloop (rpi_main.c:701-742)
func (g *glwRpi) rpiMainloop() {
	dpy := C.egl_get_default_display()
	if C.egl_no_display(dpy) != 0 {
		glwDeps.ts.Trace(trace.TRACE_ERROR, "RPI", "Unable to get display")
		os.Exit(2)
	}
	// initialize the EGL display connection
	result := C.eglInitialize(dpy, nil, nil)
	if result == C.EGL_FALSE {
		glwDeps.ts.Trace(trace.TRACE_ERROR, "RPI", "Unable initialize EGL")
		os.Exit(2)
	}

	archrpi.DispmanDisplay = uint32(C.vc_dispmanx_display_open(0))

	archrpi.Runmode = archrpi.RunmodeRunning

	gr := &g.gr
	for archrpi.Runmode != archrpi.RunmodeExit && archrpi.Ctrlc == 0 &&
		g.running.Load() {
		if uiShouldRun() != 0 {
			if swrefreshFn != nil {
				swrefreshFn() // C: swrefresh()
			}
			g.uiRun(dpy)
			C.vc_dispmanx_display_close(C.DISPMANX_DISPLAY_HANDLE_T(archrpi.DispmanDisplay))

			C.eglTerminate(dpy)
			result := C.eglInitialize(dpy, nil, nil)
			if result == C.EGL_FALSE {
				glwDeps.ts.Trace(trace.TRACE_ERROR, "RPI",
					"Unable initialize EGL")
				os.Exit(2)
			}
			archrpi.DispmanDisplay = uint32(C.vc_dispmanx_display_open(0))
		} else {
			gr.grMutex.Lock()   // C: glw_lock(gr)
			glwIdle(gr)         // C: glw_idle(gr)
			gr.grMutex.Unlock() // C: glw_unlock(gr)
			arch.USleep(100000) // C: usleep(100000)
		}
	}
}

// glwRpiThread — the frontend thread
func glwRpiThread(aux any) any {
	aux.(*glwRpi).rpiMainloop()
	return nil
}

// SwrefreshFn — C: swrefresh() (main.c:321-329) — the cmd-side upgrade
// check wakeup; wired by the platform init.
var swrefreshFn func()

// SetSwrefresh wires the software-refresh trigger (C: refresh direct
// call — provider lives in cmd/movian-go, linux_rpi build).
func SetSwrefresh(fn func()) { swrefreshFn = fn }

// GlwRpiStart — C: the ui_create + rpi_mainloop invocation from main().
// Same seam as GlwX11Start/GlwGlfwStart.
func GlwRpiStart(nav *propcore.Prop) any {
	g := &glwRpi{}

	// C: ui_create (rpi_main.c:439-478)
	g.gr.grReduceCpu = 1
	g.gr.grPropUi = glwDeps.pm.CreateRoot("ui")
	if nav != nil {
		g.gr.grPropNav = nav
	} else if GlwX11NavSpawnFn != nil {
		g.gr.grPropNav = GlwX11NavSpawnFn()
	}

	// C: prop_courier_poll_with_alarm — bounded dispatch; the C alarm
	// interval (gr_prop_maxtime) is in microseconds, PollTimed is ms.
	dispatcher := func(pc *propcore.Courier, timeout int) {
		if timeout < 0 {
			pc.Poll()
		} else {
			pc.PollTimed(timeout / 1000)
		}
	}

	if glwStart4(&g.gr, dispatcher, propcore.NewCourier("glw"),
		glwFlagKeyboardMode|glwFlagOverscan|glwFlagInFullscreen) != 0 {
		glwDeps.ts.Trace(trace.TRACE_ERROR, "GLW", "Unable to init GLW")
		os.Exit(1)
	}

	glwLoadUniverse(&g.gr)

	// C: sigaction(SIGALRM, the_alarm) + signal(SIGTERM/SIGINT, doexit)
	// + pthread_sigmask unblock — the alarm mechanism bounded courier
	// dispatch in C; the Go courier handles the timeout internally.
	// SIGINT/SIGTERM handling lives in cmd/movian-go signal code.

	g.gr.grPropMaxtime = 5000

	g.running.Store(true)
	g.thread = arch.ThreadCreateJoinable("glw", glwRpiThread, g, 0)

	return g
}

// GlwRpiStop — C: runmode=RUNMODE_EXIT + thread join (cf. glw_x11_stop)
func GlwRpiStop(aux any) *propcore.Prop {
	g := aux.(*glwRpi)
	gr := &g.gr
	nav := gr.grPropNav
	g.running.Store(false)
	archrpi.Runmode = archrpi.RunmodeExit
	g.thread.Join()
	glwDeps.pm.Destroy(gr.grPropUi)
	glwReleaseRoot(gr)
	return nav
}
