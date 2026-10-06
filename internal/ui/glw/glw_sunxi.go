//go:build sunxi

// glw_sunxi.go — C: src/arch/sunxi/sunxi_main.c — the sunxi EGL/GLES2
// frontend: ui_init, display_mode/display_onoff (HDMI disp ioctls) and
// the ui_run render loop. In C main() runs ui_run on the main thread;
// the Go port runs it on the frontend thread through the shared
// LinuxUI Start/Stop seam (same as glw_rpi/glw_x11).
package glw

/*
#cgo LDFLAGS: -lEGL -lGLESv2

#include <stdlib.h>
#include <stdint.h>
#include <EGL/fbdev_window.h>
#include <EGL/egl.h>
#include <GLES2/gl2.h>
#include <linux/types.h>
#include "drv_display_sun4i.h"

// EGL_DEFAULT_DISPLAY is a cast macro — cgo can't use it directly.
static EGLDisplay sx_egl_get_default_display(void) {
	return eglGetDisplay(EGL_DEFAULT_DISPLAY);
}
static int sx_egl_no_surface(EGLSurface s) { return s == EGL_NO_SURFACE; }
static int sx_egl_no_context(EGLContext c) { return c == EGL_NO_CONTEXT; }
// EGLNativeWindowType varies by platform branch in eglplatform.h —
// canonical target is ARM fbdev (fbdev_window*); cast via shim.
static EGLNativeWindowType sx_native_win(fbdev_window *w) {
	return (EGLNativeWindowType)w;
}
*/
import "C"

import (
	"fmt"
	"os"
	"sync/atomic"

	"github.com/czz/movian-go/internal/arch"
	archsunxi "github.com/czz/movian-go/internal/arch/sunxi"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/trace"
)

// C: ui_config_attribs / ui_ctx_attribs / ui_win_attribs
// (sunxi_main.c:43-65)
var uiConfigAttribs = [...]C.EGLint{
	C.EGL_SAMPLES, C.EGL_DONT_CARE,
	C.EGL_RED_SIZE, 8,
	C.EGL_GREEN_SIZE, 8,
	C.EGL_BLUE_SIZE, 8,
	C.EGL_ALPHA_SIZE, 8,
	C.EGL_BUFFER_SIZE, 32,
	C.EGL_STENCIL_SIZE, 0,
	C.EGL_RENDERABLE_TYPE, C.EGL_OPENGL_ES2_BIT,
	C.EGL_SURFACE_TYPE, C.EGL_WINDOW_BIT | C.EGL_PIXMAP_BIT,
	C.EGL_DEPTH_SIZE, 16,
	C.EGL_NONE,
}

var uiCtxAttribs = [...]C.EGLint{
	C.EGL_CONTEXT_CLIENT_VERSION, 2,
	C.EGL_NONE,
}

var uiWinAttribs = [...]C.EGLint{
	C.EGL_RENDER_BUFFER, C.EGL_BACK_BUFFER,
	C.EGL_NONE,
}

// glwSunxi — C: sunxi_ui_t (sunxi_main.c:67-76) + static su
type glwSunxi struct {
	gr glwRoot // C: su_gr

	dpy    C.EGLDisplay // C: su_dpy
	config C.EGLConfig  // C: su_config

	fullwindow  int // C: su_fullwindow
	screensaver int // C: su_screensaver
	thread      *arch.Thread
	running     atomic.Bool
}

// setInFullwindow — C: set_in_fullwindow (sunxi_main.c:84-89)
func setInFullwindow(opaque any, v int) {
	su := opaque.(*glwSunxi)
	su.fullwindow = v
}

// setInScreensaver — C: set_in_screensaver (sunxi_main.c:95-100)
func setInScreensaver(opaque any, v int) {
	su := opaque.(*glwSunxi)
	su.screensaver = v
}

// uiStart — C: ui_init (sunxi_main.c:106-151)
func (su *glwSunxi) uiStart() int {
	var configs [32]C.EGLConfig
	var noc C.EGLint
	su.dpy = C.sx_egl_get_default_display()

	if C.eglInitialize(su.dpy, nil, nil) != C.EGL_TRUE {
		glwDeps.ts.Trace(trace.TRACE_ERROR, "EGL", "eglInitialize failed")
		return 1
	}

	if C.eglChooseConfig(su.dpy, &uiConfigAttribs[0], &configs[0],
		32, &noc) != C.EGL_TRUE {
		glwDeps.ts.Trace(trace.TRACE_ERROR, "EGL", "Unable to choose config")
		return 1
	}
	su.config = configs[0]

	gr := &su.gr

	gr.grPropUi = glwDeps.pm.CreateRoot("ui")
	if gr.grPropNav == nil && GlwX11NavSpawnFn != nil {
		gr.grPropNav = GlwX11NavSpawnFn() // C: nav_spawn()
	}
	gr.grPropUi.CreateInt("nobackground", 1)
	if glwStart(gr) != 0 {
		glwDeps.ts.Trace(trace.TRACE_ERROR, "GLW", "Unable to init GLW")
		return 1
	}

	glwLoadUniverse(gr)

	glwPropSubscribeTags(0,
		propTagName, []string{"ui", "fullwindow"},
		propTagCallbackInt, setInFullwindow, su,
		propTagRoot, gr.grPropUi,
		propTagCourier, gr.grCourier,
	)

	glwPropSubscribeTags(0,
		propTagName, []string{"ui", "screensaverActive"},
		propTagCallbackInt, setInScreensaver, su,
		propTagRoot, gr.grPropUi,
		propTagCourier, gr.grCourier,
	)
	return 0
}

// displayOnoff — C: display_onoff (sunxi_main.c:156-160)
func displayOnoff(on int) {
	cmd := uint(C.DISP_CMD_HDMI_OFF)
	if on != 0 {
		cmd = C.DISP_CMD_HDMI_ON
	}
	archsunxi.DispIoctl(cmd, nil)
}

// displayMode — C: display_mode (sunxi_main.c:166-204)
func (su *glwSunxi) displayMode(mode int) {
	gr := &su.gr
	var args [4]uintptr

	switch mode {
	case C.DISP_TV_MOD_720P_50HZ, C.DISP_TV_MOD_720P_60HZ:
		gr.grWidth = 1280
		gr.grHeight = 720

	case C.DISP_TV_MOD_1080I_50HZ, C.DISP_TV_MOD_1080I_60HZ,
		C.DISP_TV_MOD_1080P_24HZ, C.DISP_TV_MOD_1080P_50HZ,
		C.DISP_TV_MOD_1080P_60HZ:
		gr.grWidth = 1920
		gr.grHeight = 1080

	default:
		panic("sunxi: unsupported display mode")
	}

	glwDeps.ts.Trace(trace.TRACE_INFO, "UI", "Display size %d x %d",
		gr.grWidth, gr.grHeight)

	if archsunxi.DispIoctl(C.DISP_CMD_HDMI_GET_MODE, &args) == mode {
		return
	}

	displayOnoff(0)

	args[1] = uintptr(mode)
	archsunxi.DispIoctl(C.DISP_CMD_HDMI_SET_MODE, &args)
	args[1] = 0
}

// uiRun — C: ui_run (sunxi_main.c:210-323)
func (su *glwSunxi) uiRun() int {
	gr := &su.gr
	var fbwin C.fbdev_window

	su.displayMode(C.DISP_TV_MOD_1080P_60HZ)
	displayOnoff(1)

	fbwin.width = C.ushort(gr.grWidth)
	fbwin.height = C.ushort(gr.grHeight)

	surface := C.eglCreateWindowSurface(su.dpy, su.config,
		C.sx_native_win(&fbwin), &uiWinAttribs[0])
	if C.sx_egl_no_surface(surface) != 0 {
		glwDeps.ts.Trace(trace.TRACE_ERROR, "EGL",
			"Failed to create EGL surface")
		return -1
	}

	C.eglBindAPI(C.EGL_OPENGL_ES_API)

	ctx := C.eglCreateContext(su.dpy, su.config, nil,
		&uiCtxAttribs[0])

	if C.sx_egl_no_context(ctx) != 0 {
		glwDeps.ts.Trace(trace.TRACE_ERROR, "EGL", "Failed to create context")
		return -1
	}

	C.eglMakeCurrent(su.dpy, surface, surface, ctx)
	glwOpenglSetupContext(gr)

	C.glClearColor(0, 0, 0, 0)

	// C: int64_t ts0 — the if(0)'d frame-delta timing; kept out.
	framenum := archsunxi.Fb0Ioctl(0x4740, 0)
	fmt.Printf("Starting at frame %d\n", framenum)
	for atomic.LoadInt32(&archsunxi.Running) != 0 {

		gr.grMutex.Lock() // C: glw_lock(gr)

		C.glViewport(0, 0, C.GLsizei(gr.grWidth),
			C.GLsizei(gr.grHeight))
		C.glClear(C.GL_DEPTH_BUFFER_BIT | C.GL_COLOR_BUFFER_BIT)

		gr.grCanExternalize = 1
		gr.grExternalizeCnt = 0

		glwPrepareFrame(gr, 0)

		var rc glwRctx
		var zmax int
		glwRctxSetup(&rc, gr.grWidth, gr.grHeight, 1, &zmax)
		glwLayout0(gr.grUniverse, &rc)
		glwRender0(gr.grUniverse, &rc)

		gr.grMutex.Unlock() // C: glw_unlock(gr)

		glwPostScene(gr)

		// C: int64_t ts2 = arch_get_ts() (debug timing, if(0)'d)
		C.eglSwapBuffers(su.dpy, surface)

		// C: ts = arch_get_ts() + if(0) printf — dropped with the
		// if(0) block (sunxi_main.c:289-295).

		archsunxi.SunxiBgEveryFrame(su.fullwindow | su.screensaver)

		if atomic.LoadInt32(&archsunxi.Ctrlc) == 1 {
			atomic.StoreInt32(&archsunxi.Ctrlc, 2)
			if sunxiAppShutdownFn != nil {
				sunxiAppShutdownFn(0) // C: app_shutdown(0)
			}
		}
	}

	// Reap twice makes all resources flushed out of memory
	gr.grMutex.Lock()
	glwUnloadUniverse(gr)
	glwReap(gr)
	glwReap(gr)
	gr.grMutex.Unlock()

	C.glClear(C.GL_DEPTH_BUFFER_BIT | C.GL_COLOR_BUFFER_BIT)
	C.eglSwapBuffers(su.dpy, surface)
	C.glClear(C.GL_DEPTH_BUFFER_BIT | C.GL_COLOR_BUFFER_BIT)
	C.eglSwapBuffers(su.dpy, surface)

	C.eglDestroyContext(su.dpy, ctx)
	C.eglDestroySurface(su.dpy, surface)

	return 0
}

// SunxiAppShutdownFn — C: app_shutdown(0) invoked from ui_run on ctrlc.
// Wired by cmd/movian-go.
var sunxiAppShutdownFn func(int)

// SetSunxiAppShutdown wires the app_shutdown call for the sunxi UI
// (C: app_shutdown direct call — provider lives in cmd/movian-go).
func SetSunxiAppShutdown(fn func(code int)) { sunxiAppShutdownFn = fn }

// glwSunxiThread — the frontend thread body.
func glwSunxiThread(aux any) any {
	aux.(*glwSunxi).uiRun()
	return nil
}

// GlwSunxiStart — C: sunxi_main.c main() tail — ui_init + ui_run.
// Same seam as GlwX11Start/GlwRpiStart.
func GlwSunxiStart(nav *propcore.Prop) any {
	su := &glwSunxi{}
	if nav != nil {
		su.gr.grPropNav = nav
	}

	if su.uiStart() != 0 {
		os.Exit(1)
	}

	su.running.Store(true)
	su.thread = arch.ThreadCreateJoinable("glw", glwSunxiThread, su, 0)
	return su
}

// GlwSunxiStop — C: the ui_run exit path (running=0 via arch_stop_req)
// + thread join.
func GlwSunxiStop(aux any) *propcore.Prop {
	su := aux.(*glwSunxi)
	nav := su.gr.grPropNav
	atomic.StoreInt32(&archsunxi.Running, 0)
	su.running.Store(false)
	if su.thread != nil {
		su.thread.Join()
	}
	glwDeps.pm.Destroy(su.gr.grPropUi)
	glwReleaseRoot(&su.gr)
	return nav
}
