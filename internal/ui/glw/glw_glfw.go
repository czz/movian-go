//go:build (linux || darwin || windows) && glfw

package glw

// GLFW frontend — NOT a C port: upstream Movian has no GLFW/Wayland
// backend (documented extension, build tag "glfw"). Mirrors the role of
// glw_x11.c: window+GL context lifecycle, key/pointer translation into
// glwPointerEventT / event actions, and the render mainloop driving
// glwPrepareFrame/glwRender0/glwPostScene on a dedicated "glw" thread.
//
// GLFW 3.4 selects Wayland or X11 at runtime (GLFW_PLATFORM /
// XDG_SESSION_TYPE), so this frontend can present a window on
// Wayland-only sessions where the X11 window manager is unavailable.
//
// X11-specific features with no GLFW equivalent are omitted (documented):
// WM probing (glfwSetWindowMonitor handles fullscreen on both
// platforms).
//
// Reimplemented via native access (glfw3native.h) + Wayland protocols:
// - fullscreen pointer/keyboard grabs (C: fullscreen_grab): on X11 the
//   same XGrabPointer/XGrabKeyboard calls run on GLFW's window; on
//   Wayland (no global grabs) pointer-constraints-unstable-v1 confines
//   the pointer and keyboard-shortcuts-inhibit-unstable-v1 routes all
//   keys to the surface.
// - XF86 media keys: GLFW maps none of them, so they surface as
//   GLFW_KEY_UNKNOWN with a platform scancode (evdev code on Wayland,
//   XKB keycode = evdev+8 on X11); glfwMediaKey2action maps them to
//   the same actions as the C key2action XF86 entries.
// - screensaver suspend (C: x11_screensaver_suspend/resume): on X11
//   the same XResetScreenSaver callout chain (Go timer, 1s then 30s);
//   on Wayland an idle-inhibit-unstable-v1 inhibitor exists while
//   ui.fullwindow is set.
// - input method (C: XCreateIC XIMPreeditNothing|XIMStatusNothing):
//   on Wayland a text-input-unstable-v3 object replaces the X input
//   context. C disables preedit rendering, so only commit_string is
//   consumed — buffered until done, then delivered as EVENT_UNICODE
//   like the GLFW char callback.

/*
#cgo linux pkg-config: glfw3
#cgo darwin pkg-config: glfw3
// Windows: vendored glfw3 (scripts/build_windows_deps.sh).
#cgo windows,amd64 CFLAGS: -I${SRCDIR}/../../../third_party/glfw/windows/amd64/include
#cgo windows,386 CFLAGS: -I${SRCDIR}/../../../third_party/glfw/windows/386/include
#cgo windows,amd64 LDFLAGS: -L${SRCDIR}/../../../third_party/glfw/windows/amd64/lib -lglfw3 -lgdi32 -luser32 -lshell32 -ldwmapi -lopengl32
#cgo windows,386 LDFLAGS: -L${SRCDIR}/../../../third_party/glfw/windows/386/lib -lglfw3 -lgdi32 -luser32 -lshell32 -ldwmapi -lopengl32
#define GL_GLEXT_PROTOTYPES
#include <GLFW/glfw3.h>
#if defined(__APPLE__)
#include <OpenGL/gl.h>
#elif defined(_WIN32)
#include <GL/gl.h>
#include <GL/glext.h> // windows gl.h is 1.1-only — GL_BGRA & friends live here
#else
#include <GL/gl.h>
#endif
#include <stdlib.h>
#include <string.h>
#ifndef _WIN32
#include <unistd.h>
#endif

extern void glfwGoErrorCB(int, char*);
extern void glfwGoKeyCB(GLFWwindow*, int, int, int, int);
extern void glfwGoCharCB(GLFWwindow*, unsigned int);
extern void glfwGoCursorPosCB(GLFWwindow*, double, double);
extern void glfwGoCursorEnterCB(GLFWwindow*, int);
extern void glfwGoMouseButtonCB(GLFWwindow*, int, int, int);
extern void glfwGoScrollCB(GLFWwindow*, double, double);
extern void glfwGoFbSizeCB(GLFWwindow*, int, int);
extern void glfwGoWinSizeCB(GLFWwindow*, int, int);
extern void glfwGoRefreshCB(GLFWwindow*);
extern void glfwGoCloseCB(GLFWwindow*);

static void glfw_install_error_cb(void) {
  glfwSetErrorCallback((GLFWerrorfun)glfwGoErrorCB);
}

static void glfw_install_cbs(GLFWwindow *w) {
  glfwSetKeyCallback(w, glfwGoKeyCB);
  glfwSetCharCallback(w, glfwGoCharCB);
  glfwSetCursorPosCallback(w, glfwGoCursorPosCB);
  glfwSetCursorEnterCallback(w, glfwGoCursorEnterCB);
  glfwSetMouseButtonCallback(w, glfwGoMouseButtonCB);
  glfwSetScrollCallback(w, glfwGoScrollCB);
  glfwSetFramebufferSizeCallback(w, glfwGoFbSizeCB);
  glfwSetWindowSizeCallback(w, glfwGoWinSizeCB);
  glfwSetWindowRefreshCallback(w, glfwGoRefreshCB);
  glfwSetWindowCloseCallback(w, glfwGoCloseCB);
}

static void glfw_get_window_pos(GLFWwindow *w, int *x, int *y) {
  glfwGetWindowPos(w, x, y);
}
static void glfw_get_window_size(GLFWwindow *w, int *x, int *y) {
  glfwGetWindowSize(w, x, y);
}
static void glfw_get_fb_size(GLFWwindow *w, int *x, int *y) {
  glfwGetFramebufferSize(w, x, y);
}

// Application identity hints: on Wayland the compositor matches
// xdg_toplevel.app_id to a <app_id>.desktop file for the taskbar/dock
// icon; on X11 WM_CLASS serves the same role. GLFW 3.4 only.
static void glfw_set_app_id(const char *appid) {
#ifdef GLFW_WAYLAND_APP_ID
  glfwWindowHintString(GLFW_WAYLAND_APP_ID, appid);
#endif
#ifdef GLFW_X11_CLASS_NAME
  glfwWindowHintString(GLFW_X11_CLASS_NAME, appid);
  glfwWindowHintString(GLFW_X11_INSTANCE_NAME, appid);
#endif
}

// Window icon (titlebar/taskbar on X11+Windows; ignored on Wayland,
// which resolves the icon from the .desktop file via app_id).
// glfwSetWindowIcon copies the pixel data during the call.
static void glfw_set_window_icon(GLFWwindow *w, int width, int height,
                                 unsigned char *pixels) {
  GLFWimage img;
  img.width = width;
  img.height = height;
  img.pixels = pixels;
  glfwSetWindowIcon(w, 1, &img);
}


*/
import "C"

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	apppkg "github.com/czz/movian-go/internal/app"
	archpkg "github.com/czz/movian-go/internal/arch"
	eventpkg "github.com/czz/movian-go/internal/event"
	propcore "github.com/czz/movian-go/internal/prop"
	tracepkg "github.com/czz/movian-go/internal/trace"
)

// C: glw_x11_t (glw_x11.c:57-114) — same role, GLFW handles.
type glwGlfw struct {
	gr glwRoot

	running atomic.Bool // C: int running — written by stop thread, atomic
	thread  *archpkg.Thread

	window *C.GLFWwindow // C: Window win / GLX context

	cursorHidden bool  // C: cursor_hidden
	hideCursorAt int64 // C: hide_cursor_at

	isFullscreen   int // C: is_fullscreen
	wantFullscreen int // C: want_fullscreen

	fullwindow      int // C: fullwindow
	reqWidth        int // C: req_width
	reqHeight       int // C: req_height
	fixedWindowSize int // C: fixed_window_size

	workingVsync int // C: working_vsync

	// Wayland frame-callback pacing (wl_surface_frame + bounded wait).
	// No C counterpart — PRIME/render-offload cannot vsync via swap
	// throttling, so pacing comes from the compositor's frame callback.
	wlPacing bool

	// wlOccluded — set when a presented frame's callback times out
	// (fully covered/minimized surfaces stop receiving callbacks).
	// While set, swaps are skipped entirely: on Wayland an occluded
	// surface also stops releasing EGL buffers, so continuing to swap
	// would fill the queue and stall the thread inside eglSwapBuffers.
	wlOccluded bool

	// Window-coordinate size (pointer normalization); grWidth/grHeight
	// hold framebuffer pixels (ConfigureNotify equivalent).
	winW int
	winH int

	// Windowed rect saved before fullscreen (glfwSetWindowMonitor
	// restore, replaces the C WM-driven path).
	saveX int
	saveY int
	saveW int
	saveH int

	pendingScreensaverKill bool // C: loop-local; on struct for callbacks

	// Per-OS platform layer (glw_glfw_linux.go / glw_glfw_darwin.go):
	// Wayland grab + text-input state on linux, UpdateSystemActivity
	// timer on darwin.
	plat glwGlfwPlat

	sss *glfwScreensaverState // C: x11_screensaver_state *sss
}

// glfwCurrent — sole frontend instance, reached by C trampolines.
// Mirrors the C file's single static gx11 consumer per process.
var glfwCurrent *glwGlfw

// C: build_blank_cursor/hide_cursor — GLFW_CURSOR_HIDDEN replaces the
// blank pixmap cursor.
func glfwHideCursor(g *glwGlfw) {
	var gpe glwPointerEventT

	if g.cursorHidden {
		return
	}

	g.cursorHidden = true
	C.glfwSetInputMode(g.window, C.GLFW_CURSOR, C.GLFW_CURSOR_HIDDEN)

	gpe.typ = glwPointerGone
	glwLock(&g.gr)
	glwPointerEvent(&g.gr, &gpe)
	glwUnlock(&g.gr)
}

// C: autohide_cursor (glw_x11.c:166-177)
func glfwAutohideCursor(g *glwGlfw) {
	if g.fullwindow == 0 {
		return
	}

	if g.cursorHidden {
		return
	}

	if g.gr.grTimeUsec > uint64(g.hideCursorAt) {
		glfwHideCursor(g)
	}
}

// C: show_cursor (glw_x11.c:184-192)
func glfwShowCursor(g *glwGlfw) {
	g.hideCursorAt = int64(g.gr.grTimeUsec) + glwCursorAutohideTime
	if !g.cursorHidden {
		return
	}

	g.cursorHidden = false
	C.glfwSetInputMode(g.window, C.GLFW_CURSOR, C.GLFW_CURSOR_NORMAL)
}

// C: check_vsync (glw_x11.c:224-240)
func glfwCheckVsync(g *glwGlfw) int {
	C.glClear(C.GL_DEPTH_BUFFER_BIT | C.GL_COLOR_BUFFER_BIT)
	C.glfwSwapBuffers(g.window)
	c := archpkg.GetTS()
	for range 5 {
		C.glClear(C.GL_DEPTH_BUFFER_BIT | C.GL_COLOR_BUFFER_BIT)
		C.glfwSwapBuffers(g.window)
	}
	c = archpkg.GetTS() - c

	if c > 25000 {
		return 1
	}
	return 0
}

// C: window_open (glw_x11.c:247-375) — GLFW creates window+context in
// one call; visual/colormap/override-redirect have no counterpart.
func glfwWindowOpen(g *glwGlfw) int {
	monitor := (*C.GLFWmonitor)(nil)
	var w, h int

	g.isFullscreen = g.wantFullscreen

	if g.wantFullscreen != 0 {
		monitor = C.glfwGetPrimaryMonitor()
		vm := C.glfwGetVideoMode(monitor)
		w = int(vm.width)
		h = int(vm.height)
		// Restore rect for leaving fullscreen (C: WM restores the
		// windowed geometry automatically).
		g.saveX = w / 4
		g.saveY = h / 4
		g.saveW = 1280
		g.saveH = 720
	} else {
		w = g.reqWidth
		if w == 0 {
			w = 1280
		}
		h = g.reqHeight
		if h == 0 {
			h = 720
		}
	}

	/* Set window title + desktop identity (dock/taskbar matching) */
	appid := C.CString(apppkg.AppName)
	C.glfw_set_app_id(appid)
	cname := C.CString(apppkg.AppNameUser)
	g.window = C.glfwCreateWindow(C.int(w), C.int(h), cname, monitor, nil)
	C.free(unsafe.Pointer(cname))
	C.free(unsafe.Pointer(appid))

	if g.window == nil {
		glwDeps.ts.Trace(tracepkg.TRACE_ERROR, "GLW",
			"Unable to create GLFW window/context")
		return 1
	}

	// Wayland ignores per-window icons (icon comes from the .desktop
	// file matched via GLFW_WAYLAND_APP_ID) and logs an error — skip.
	if C.glfwGetPlatform() != C.GLFW_PLATFORM_WAYLAND {
		glfwSetWindowIcon(g.window)
	}

	C.glfwMakeContextCurrent(g.window)
	C.glfw_install_cbs(g.window)

	// C: XCreateIC in window_open (glw_x11.c:363-368) + the osx
	// UpdateSystemActivity timer (GLWUI.m:181) — per-OS platform
	// extras: text-input-v3 on linux/Wayland, sys-activity on darwin.
	glfwPlatformWindowOpen(g)

	var fw, fh, ww, wh C.int
	C.glfw_get_fb_size(g.window, &fw, &fh)
	C.glfw_get_window_size(g.window, &ww, &wh)
	g.gr.grWidth = int(fw)
	g.gr.grHeight = int(fh)
	g.winW = int(ww)
	g.winH = int(wh)

	glwOpenglSetupContext(&g.gr)

	// VAAPI-EGL zero-copy probe — extension, no C counterpart. Checks
	// eglGetCurrentDisplay + EGL_EXT_image_dma_buf_import while the
	// context is current; publishes to video.VaapiEGLZeroCopy.
	glwVaapiEGLProbe()

	// Wayland note: eglSwapBuffers with interval>=1 blocks in
	// wl_display_dispatch_queue until the compositor sends a frame
	// callback. Compositors stop frame callbacks for fully occluded
	// surfaces, which would stall this thread (it also dispatches
	// input). Use interval 0 + soft-timer pacing (the same path the C
	// code falls back to when vsync is unavailable).
	//
	// On X11 the C counterpart is glXSwapIntervalSGI(1) + check_vsync:
	// attempt interval 1 and measure whether swaps actually block.
	// Exception — NVIDIA under PRIME render offload: check_vsync may
	// pass while the window is visible (DRI3 back-pressure), but an
	// occluded window stops present consumption and a blocking swap
	// then stalls the render thread forever (observed: >8min stuck in
	// glfwSwapBuffers). NVIDIA always gets soft timers instead.
	vendor := C.GoString((*C.char)(unsafe.Pointer(
		C.glGetString(C.GL_VENDOR))))
	isNvidia := strings.Contains(vendor, "NVIDIA")

	if C.glfwGetPlatform() == C.GLFW_PLATFORM_X11 && !isNvidia {
		C.glfwSwapInterval(1)
		g.workingVsync = glfwCheckVsync(g)
	} else {
		C.glfwSwapInterval(0)
		g.workingVsync = 0
		if C.glfwGetPlatform() == C.GLFW_PLATFORM_WAYLAND &&
			glfwWlPacingStart(g) == 0 {
			// Real vsync via compositor frame callbacks — works under
			// PRIME offload where swap-interval throttling can't, and
			// the bounded wait is stall-safe even on NVIDIA.
			g.wlPacing = true
		}
	}

	if g.workingVsync == 0 && !g.wlPacing {
		if isNvidia {
			// C aborts here (glw_x11.c:338-345). Under PRIME render
			// offload swap-blocking vsync is never detectable — the
			// frame is composited by the display GPU — so fall back
			// to soft-timer pacing like every other driver instead
			// of making the UI unusable.
			glwDeps.ts.Trace(tracepkg.TRACE_ERROR, "GLW",
				"OpenGL does not sync to vertical blank.\n"+
					"Falling back to soft timers (PRIME offload)")
		} else {
			glwDeps.ts.Trace(tracepkg.TRACE_INFO, "GLW",
				"OpenGL driver does not provide adequate vertical sync "+
					"capabilities. Using soft timers")
		}
	}

	glfwHideCursor(g)
	return 0
}

// glfwSetWindowIcon loads the Movian Go mascot PNG from the active
// skin and hands it to glfwSetWindowIcon (titlebar/taskbar on X11 and
// Windows; no-op on Wayland where the icon comes from the .desktop
// file matched by GLFW_WAYLAND_APP_ID). GLFW wants non-premultiplied
// RGBA — image.NRGBA is exactly that layout.
func glfwSetWindowIcon(w *C.GLFWwindow) {
	if glwDeps.appDataroot == nil {
		return
	}
	path := filepath.Join(glwDeps.appDataroot(), "glwskins",
		"flat", "icons", "movian-go-icon.png")
	f, err := os.Open(path)
	if err != nil {
		glwDeps.ts.Trace(tracepkg.TRACE_INFO, "GLW",
			"Window icon %s: %v", path, err)
		return
	}
	defer f.Close()

	src, err := png.Decode(f)
	if err != nil {
		glwDeps.ts.Trace(tracepkg.TRACE_ERROR, "GLW",
			"Cannot decode window icon %s: %v", path, err)
		return
	}

	b := src.Bounds()
	rgba, ok := src.(*image.NRGBA)
	if !ok || b.Min.X != 0 || b.Min.Y != 0 {
		// Convert to a packed NRGBA starting at (0,0)
		nrgba := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
		for y := 0; y < b.Dy(); y++ {
			for x := 0; x < b.Dx(); x++ {
				nrgba.Set(x, y, src.At(b.Min.X+x, b.Min.Y+y))
			}
		}
		rgba = nrgba
	}

	n := len(rgba.Pix)
	pix := (*C.uchar)(C.malloc(C.size_t(n)))
	if pix == nil {
		return
	}
	defer C.free(unsafe.Pointer(pix))
	C.memmove(unsafe.Pointer(pix), unsafe.Pointer(&rgba.Pix[0]),
		C.size_t(n))
	C.glfw_set_window_icon(w, C.int(rgba.Bounds().Dx()),
		C.int(rgba.Bounds().Dy()), pix)
	glwDeps.ts.Trace(tracepkg.TRACE_INFO, "GLW",
		"Set window icon from %s (%d x %d)", path,
		rgba.Bounds().Dx(), rgba.Bounds().Dy())
}

// C: window_close (glw_x11.c:381-394)
func glfwWindowClose(g *glwGlfw) {
	glwOpenglFiniContext(&g.gr)

	// C: XDestroyIC on window close (+ osx sys-activity timer stop)
	glfwPlatformWindowClose(g)

	glfwShowCursor(g)
	g.wlPacing = false
	glfwWlPacingFini(g)
	if g.window != nil {
		C.glfwDestroyWindow(g.window)
		g.window = nil
	}
}

// C: window_shutdown (glw_x11.c:401-416)
func glfwWindowShutdown(g *glwGlfw) {
	glwVideoReset(&g.gr)

	C.glFlush()

	if g.isFullscreen != 0 {
		glfwFullscreenUngrab(g)
	}

	glwLock(&g.gr)
	glwFlush(&g.gr)
	glwUnlock(&g.gr)
	glfwWindowClose(g)
}

// C: glw_x11_init (glw_x11.c:574-685) — locale/IM, GLX visual probing,
// WM probing and the screensaver plumbing are X11-specific; GLFW's
// default hints provide the GLX_RGBA|RGB1|DOUBLEBUFFER equivalent.
func glwGlfwStart(g *glwGlfw) int {
	C.glfw_install_error_cb()

	// Platform selection: GLFW_ANY picks Wayland/X11 from
	// XDG_SESSION_TYPE at runtime. Some distro builds ignore the
	// GLFW_PLATFORM env — honor it ourselves via glfwInitHint.
	// --platform <x11|wayland> (gconf) overrides the env var.
	platform := os.Getenv("GLFW_PLATFORM")
	if glwDeps.gconf != nil && glwDeps.gconf.Platform != "" {
		platform = glwDeps.gconf.Platform
	}
	hintSet := false
	switch platform {
	case "x11", "X11":
		C.glfwInitHint(C.GLFW_PLATFORM, C.GLFW_PLATFORM_X11)
		hintSet = true
	case "wayland", "Wayland", "WAYLAND":
		C.glfwInitHint(C.GLFW_PLATFORM, C.GLFW_PLATFORM_WAYLAND)
		hintSet = true
	}
	ok := C.glfwInit() == C.GLFW_TRUE
	if !ok && !hintSet && os.Getenv("DISPLAY") != "" {
		// Wayland-first build on an X-only session (Xvfb, ssh -X,
		// XDG_SESSION_TYPE unset/misleading): GLFW_ANY tried
		// wayland and gave up — retry pinned to X11.
		C.glfwInitHint(C.GLFW_PLATFORM, C.GLFW_PLATFORM_X11)
		ok = C.glfwInit() == C.GLFW_TRUE
	}
	if !ok {
		glwDeps.ts.Trace(tracepkg.TRACE_ERROR, "GLW",
			"Unable to initialize GLFW")
		return 1
	}

	return glfwWindowOpen(g)
}

// C: window_change_fullscreen (glw_x11.c:693-707) — the C WM path is
// replaced by glfwSetWindowMonitor (always "can fullscreen").
func glfwWindowChangeFullscreen(g *glwGlfw) {
	if g.wantFullscreen != 0 {
		var x, y, w, h C.int
		C.glfw_get_window_pos(g.window, &x, &y)
		C.glfw_get_window_size(g.window, &w, &h)
		g.saveX, g.saveY = int(x), int(y)
		g.saveW, g.saveH = int(w), int(h)

		monitor := C.glfwGetPrimaryMonitor()
		vm := C.glfwGetVideoMode(monitor)
		C.glfwSetWindowMonitor(g.window, monitor, 0, 0,
			vm.width, vm.height, vm.refreshRate)
		glfwFullscreenGrab(g)
	} else {
		glfwFullscreenUngrab(g)
		C.glfwSetWindowMonitor(g.window, nil,
			C.int(g.saveX), C.int(g.saveY),
			C.int(g.saveW), C.int(g.saveH),
			C.GLFW_DONT_CARE)
	}
	g.isFullscreen = g.wantFullscreen
	glwSetFullscreen(&g.gr, g.isFullscreen)
}

// C: keysym2action[] (glw_x11.c:713-774) — GLFW key codes + modifier
// bits replace X keysyms/X masks. XK_plus/XK_minus/XK_0 are bound to
// the GLFW keys producing those characters (EQUAL/MINUS/0 and keypad
// equivalents). The XF86 media-keysym entries are covered by
// glfwMediaKey2action (scancode-based — GLFW maps no media keys).
type glfwKeyActionT struct {
	key      int
	modifier int
	action1  eventpkg.ActionType
	action2  eventpkg.ActionType
	action3  eventpkg.ActionType
}

var glfwkey2action = []glfwKeyActionT{
	{C.GLFW_KEY_LEFT, 0, eventpkg.ACTION_LEFT, 0, 0},
	{C.GLFW_KEY_RIGHT, 0, eventpkg.ACTION_RIGHT, 0, 0},
	{C.GLFW_KEY_UP, 0, eventpkg.ACTION_UP, 0, 0},
	{C.GLFW_KEY_DOWN, 0, eventpkg.ACTION_DOWN, 0, 0},

	{C.GLFW_KEY_LEFT, C.GLFW_MOD_SHIFT, eventpkg.ACTION_MOVE_LEFT, 0, 0},
	{C.GLFW_KEY_RIGHT, C.GLFW_MOD_SHIFT, eventpkg.ACTION_MOVE_RIGHT, 0, 0},
	{C.GLFW_KEY_UP, C.GLFW_MOD_SHIFT, eventpkg.ACTION_MOVE_UP, 0, 0},
	{C.GLFW_KEY_DOWN, C.GLFW_MOD_SHIFT, eventpkg.ACTION_MOVE_DOWN, 0, 0},

	{C.GLFW_KEY_TAB, C.GLFW_MOD_SHIFT, eventpkg.ACTION_FOCUS_PREV, 0, 0},

	{C.GLFW_KEY_LEFT, C.GLFW_MOD_ALT, eventpkg.ACTION_NAV_BACK, 0, 0},
	{C.GLFW_KEY_RIGHT, C.GLFW_MOD_ALT, eventpkg.ACTION_NAV_FWD, 0, 0},

	{C.GLFW_KEY_LEFT, C.GLFW_MOD_CONTROL, eventpkg.ACTION_SKIP_BACKWARD, 0, 0},
	{C.GLFW_KEY_RIGHT, C.GLFW_MOD_CONTROL, eventpkg.ACTION_SKIP_FORWARD, 0, 0},
	{C.GLFW_KEY_UP, C.GLFW_MOD_CONTROL, eventpkg.ACTION_VOLUME_UP, 0, 0},
	{C.GLFW_KEY_DOWN, C.GLFW_MOD_CONTROL, eventpkg.ACTION_VOLUME_DOWN, 0, 0},

	{C.GLFW_KEY_DOWN, C.GLFW_MOD_SHIFT | C.GLFW_MOD_CONTROL, eventpkg.ACTION_VOLUME_MUTE_TOGGLE, 0, 0},
	{C.GLFW_KEY_LEFT, C.GLFW_MOD_SHIFT | C.GLFW_MOD_CONTROL, eventpkg.ACTION_SEEK_BACKWARD, 0, 0},
	{C.GLFW_KEY_RIGHT, C.GLFW_MOD_SHIFT | C.GLFW_MOD_CONTROL, eventpkg.ACTION_SEEK_FORWARD, 0, 0},

	{C.GLFW_KEY_PAGE_UP, 0, eventpkg.ACTION_PAGE_UP, eventpkg.ACTION_PREV_CHANNEL, eventpkg.ACTION_SKIP_BACKWARD},
	{C.GLFW_KEY_PAGE_DOWN, 0, eventpkg.ACTION_PAGE_DOWN, eventpkg.ACTION_NEXT_CHANNEL, eventpkg.ACTION_SKIP_FORWARD},

	{C.GLFW_KEY_HOME, 0, eventpkg.ACTION_TOP, 0, 0},
	{C.GLFW_KEY_END, 0, eventpkg.ACTION_BOTTOM, 0, 0},

	{C.GLFW_KEY_EQUAL, C.GLFW_MOD_CONTROL, eventpkg.ACTION_ZOOM_UI_INCR, 0, 0},
	{C.GLFW_KEY_KP_ADD, C.GLFW_MOD_CONTROL, eventpkg.ACTION_ZOOM_UI_INCR, 0, 0},
	{C.GLFW_KEY_MINUS, C.GLFW_MOD_CONTROL, eventpkg.ACTION_ZOOM_UI_DECR, 0, 0},
	{C.GLFW_KEY_KP_SUBTRACT, C.GLFW_MOD_CONTROL, eventpkg.ACTION_ZOOM_UI_DECR, 0, 0},
	{C.GLFW_KEY_0, C.GLFW_MOD_CONTROL, eventpkg.ACTION_ZOOM_UI_RESET, 0, 0},
	{C.GLFW_KEY_KP_0, C.GLFW_MOD_CONTROL, eventpkg.ACTION_ZOOM_UI_RESET, 0, 0},

	{C.GLFW_KEY_F4, C.GLFW_MOD_ALT, eventpkg.ACTION_QUIT, 0, 0},
	{C.GLFW_KEY_F12, C.GLFW_MOD_ALT, eventpkg.ACTION_RECORD_UI, 0, 0},
}

// C: gl_keypress (glw_x11.c:782-930) — GLFW splits C's XLookupString
// path in two: the key callback handles control/action keys, the char
// callback delivers text (EVENT_UNICODE). The C single-ASCII switch is
// gated to (state & ~ShiftMask) == 0 — mirrored on GLFW mods.
func glfwKeypress(g *glwGlfw, key, scancode, mods int) int {
	var e *eventpkg.Event

	state := mods & (C.GLFW_MOD_SHIFT | C.GLFW_MOD_CONTROL |
		C.GLFW_MOD_ALT)

	if state&^C.GLFW_MOD_SHIFT == 0 {
		switch key {
		/* Static key mappings, these cannot be changed */
		case C.GLFW_KEY_BACKSPACE:
			eav := glwDeps.em.CreateActionMulti([]eventpkg.ActionType{
				eventpkg.ACTION_BS, eventpkg.ACTION_NAV_BACK})
			e = &eav.Event
		case C.GLFW_KEY_ENTER, C.GLFW_KEY_KP_ENTER:
			eav := glwDeps.em.CreateActionMulti([]eventpkg.ActionType{
				eventpkg.ACTION_ACTIVATE, eventpkg.ACTION_ENTER})
			e = &eav.Event
		case C.GLFW_KEY_TAB:
			eav := glwDeps.em.CreateAction(eventpkg.ACTION_FOCUS_NEXT)
			e = &eav.Event
		case C.GLFW_KEY_ESCAPE:
			eav := glwDeps.em.CreateActionMulti([]eventpkg.ActionType{
				eventpkg.ACTION_CANCEL, eventpkg.ACTION_NAV_BACK})
			e = &eav.Event
		case C.GLFW_KEY_DELETE:
			eav := glwDeps.em.CreateAction(eventpkg.ACTION_DELETE)
			e = &eav.Event
		}
	}

	if e == nil && key == C.GLFW_KEY_F12 &&
		state&(C.GLFW_MOD_CONTROL|C.GLFW_MOD_SHIFT) != 0 {
		glwDeps.ts.Trace(tracepkg.TRACE_DEBUG, "GLW", "F12 screenshot event created") // TEMP DEBUG
		e = glwDeps.em.Create(eventpkg.EVENT_MAKE_SCREENSHOT, 0)
	}

	if e == nil {

		for i := range len(glfwkey2action) {

			if glfwkey2action[i].key == key &&
				glfwkey2action[i].modifier == state {

				av := []eventpkg.ActionType{
					glfwkey2action[i].action1,
					glfwkey2action[i].action2,
					glfwkey2action[i].action3}

				var eav *eventpkg.EventActionVector
				if glfwkey2action[i].action3 != eventpkg.ActionNone {
					eav = glwDeps.em.CreateActionMulti(av[:3])
				} else if glfwkey2action[i].action2 != eventpkg.ActionNone {
					eav = glwDeps.em.CreateActionMulti(av[:2])
				} else {
					eav = glwDeps.em.CreateActionMulti(av[:1])
				}
				e = &eav.Event
				break
			}
		}
	}

	if e == nil && key == C.GLFW_KEY_UNKNOWN && state == 0 {
		// C: XF86 keysym entries of key2action (modifier == 0) —
		// linux-only: upstream osx maps no media keys, darwin seam
		// returns nil.
		e = glfwMediaKeyAction(int(scancode))
	}

	if e == nil && key >= C.GLFW_KEY_F1 && key <= C.GLFW_KEY_F12 {
		shift := uint(0)
		if mods&C.GLFW_MOD_SHIFT != 0 {
			shift = 1
		}
		e = glwDeps.em.FromFkey(uint(key-C.GLFW_KEY_F1)+1, shift)
	}

	if e == nil &&
		key != C.GLFW_KEY_LEFT_SHIFT && key != C.GLFW_KEY_RIGHT_SHIFT &&
		key != C.GLFW_KEY_LEFT_CONTROL && key != C.GLFW_KEY_RIGHT_CONTROL &&
		key != C.GLFW_KEY_CAPS_LOCK &&
		key != C.GLFW_KEY_LEFT_ALT && key != C.GLFW_KEY_RIGHT_ALT &&
		key != C.GLFW_KEY_LEFT_SUPER && key != C.GLFW_KEY_RIGHT_SUPER {

		/* Construct a string representing the key */
		var buf string
		name := C.glfwGetKeyName(C.int(key), C.int(scancode))
		if state&(C.GLFW_MOD_CONTROL|C.GLFW_MOD_ALT) != 0 {
			prefix := ""
			if mods&C.GLFW_MOD_SHIFT != 0 {
				prefix += "Shift+"
			}
			if mods&C.GLFW_MOD_ALT != 0 {
				prefix += "Alt+"
			}
			if mods&C.GLFW_MOD_CONTROL != 0 {
				prefix += "Ctrl+"
			}
			if name != nil {
				buf = prefix + C.GoString(name)
			} else {
				buf = prefix + "GLFW+0x" + hexInt(uint(key))
			}
			ep := glwDeps.em.CreateStr(eventpkg.EVENT_KEYDESC, buf)
			e = &ep.Event
		} else if name == nil {
			// Non-printable key outside the action table
			// (C: XKeysymToString fallback / "X11+0x%x").
			buf = "GLFW+0x" + hexInt(uint(key))
			ep := glwDeps.em.CreateStr(eventpkg.EVENT_KEYDESC, buf)
			e = &ep.Event
		}
		// else: text-producing key — the char callback delivers
		// EVENT_UNICODE (C: XLookupString length >= 1 path).
	}
	if e != nil {
		e.Flags |= eventpkg.EventKeypress
		glwInjectEvent(&g.gr, e)
		return 1
	}
	// Text keys produce their event in the char callback.
	if C.glfwGetKeyName(C.int(key), C.int(scancode)) != nil {
		return 1
	}
	return 0
}

// C: glw_x11_in_fullwindow (glw_x11.c:953-957)
func glwGlfwInFullwindow(opaque any, v int) {
	g := opaque.(*glwGlfw)
	g.fullwindow = v
}

// ---- GLFW callbacks (C: the XNextEvent switch in glw_x11_mainloop) ----

//export glfwGoErrorCB
func glfwGoErrorCB(code C.int, desc *C.char) { // C: const char*
	glwDeps.ts.Trace(tracepkg.TRACE_ERROR, "GLW",
		"GLFW error %d: %s", int(code), C.GoString(desc))
}

//export glfwGoKeyCB
func glfwGoKeyCB(w *C.GLFWwindow, key, scancode, action, mods C.int) {
	g := glfwCurrent
	if g == nil {
		return
	}
	if action == C.GLFW_RELEASE {
		if g.pendingScreensaverKill {
			glwKillScreensaver(&g.gr)
		}
		return
	}
	// C: KeyPress — GLFW_REPEAT mirrors X auto-repeat press events.
	glfwHideCursor(g)
	if glfwKeypress(g, int(key), int(scancode), int(mods)) != 0 {
		g.pendingScreensaverKill = false
	} else {
		g.pendingScreensaverKill = true
	}
}

//export glfwGoCharCB
func glfwGoCharCB(w *C.GLFWwindow, cp C.uint) {
	g := glfwCurrent
	if g == nil {
		return
	}
	// C: mbrtowc loop → event_create(EVENT_UNICODE) per char.
	ei := glwDeps.em.CreateInt(eventpkg.EVENT_UNICODE, int(cp))
	e := &ei.Event
	e.Flags |= eventpkg.EventKeypress
	glwInjectEvent(&g.gr, e)
}

//export glfwGoCursorPosCB
func glfwGoCursorPosCB(w *C.GLFWwindow, x, y C.double) {
	g := glfwCurrent
	if g == nil || g.winW == 0 || g.winH == 0 {
		return
	}
	var gpe glwPointerEventT

	// C: MotionNotify
	glfwShowCursor(g)

	gpe.screenX = 2.0*float32(x)/float32(g.winW) - 1
	gpe.screenY = -(2.0 * float32(y) / float32(g.winH)) + 1
	gpe.ts = archpkg.GetTS()
	gpe.typ = glwPointerMotionUpdate
	glwLock(&g.gr)
	glwPointerEvent(&g.gr, &gpe)
	glwUnlock(&g.gr)
}

//export glfwGoCursorEnterCB
func glfwGoCursorEnterCB(w *C.GLFWwindow, entered C.int) {
	g := glfwCurrent
	if g == nil {
		return
	}
	if entered == 0 {
		// C: LeaveNotify
		var gpe glwPointerEventT
		gpe.typ = glwPointerGone
		glwLock(&g.gr)
		glwPointerEvent(&g.gr, &gpe)
		glwUnlock(&g.gr)
	}
}

//export glfwGoMouseButtonCB
func glfwGoMouseButtonCB(w *C.GLFWwindow, button, action, mods C.int) {
	g := glfwCurrent
	if g == nil || g.winW == 0 || g.winH == 0 {
		return
	}
	var gpe glwPointerEventT
	var x, y C.double
	C.glfwGetCursorPos(w, &x, &y)

	gpe.screenX = 2.0*float32(x)/float32(g.winW) - 1
	gpe.screenY = -(2.0 * float32(y) / float32(g.winH)) + 1
	gpe.ts = archpkg.GetTS()

	// GLFW button numbering: LEFT=0 RIGHT=1 MIDDLE=2, 3/4 = X1/X2.
	switch int(button) {
	case C.GLFW_MOUSE_BUTTON_LEFT:
		if action == C.GLFW_PRESS {
			gpe.typ = glwPointerLeftPress
		} else {
			gpe.typ = glwPointerLeftRelease
		}
	case C.GLFW_MOUSE_BUTTON_RIGHT:
		if action == C.GLFW_PRESS {
			gpe.typ = glwPointerRightPress
		} else {
			gpe.typ = glwPointerRightRelease
		}
	case C.GLFW_MOUSE_BUTTON_MIDDLE:
		// C: X button 2 (press only)
		if action == C.GLFW_PRESS {
			eav := glwDeps.em.CreateAction(eventpkg.ACTION_MENU)
			glwInjectEvent(&g.gr, &eav.Event)
		}
		return
	case C.GLFW_MOUSE_BUTTON_4:
		// C: X button 8 (back)
		if action == C.GLFW_PRESS {
			eav := glwDeps.em.CreateAction(eventpkg.ACTION_NAV_BACK)
			glwInjectEvent(&g.gr, &eav.Event)
		}
		return
	case C.GLFW_MOUSE_BUTTON_5:
		// C: X button 9 (forward)
		if action == C.GLFW_PRESS {
			eav := glwDeps.em.CreateAction(eventpkg.ACTION_NAV_FWD)
			glwInjectEvent(&g.gr, &eav.Event)
		}
		return
	default:
		return
	}

	glwLock(&g.gr)
	glwPointerEvent(&g.gr, &gpe)
	glwUnlock(&g.gr)
}

//export glfwGoScrollCB
func glfwGoScrollCB(w *C.GLFWwindow, xoff, yoff C.double) {
	g := glfwCurrent
	if g == nil {
		return
	}
	// C: X buttons 4/5 — scroll up/down. GLFW reports offsets instead
	// of button presses; one unit maps to one C button click.
	var gpe glwPointerEventT
	gpe.ts = archpkg.GetTS()

	if glwDeps.settings.gsMapMouseWheelToKeys != 0 {
		for range int(yoff) {
			eav := glwDeps.em.CreateAction(eventpkg.ACTION_UP)
			glwInjectEvent(&g.gr, &eav.Event)
		}
		for range int(-yoff) {
			eav := glwDeps.em.CreateAction(eventpkg.ACTION_DOWN)
			glwInjectEvent(&g.gr, &eav.Event)
		}
		return
	}

	var x, y C.double
	C.glfwGetCursorPos(w, &x, &y)
	if g.winW != 0 && g.winH != 0 {
		gpe.screenX = 2.0*float32(x)/float32(g.winW) - 1
		gpe.screenY = -(2.0 * float32(y) / float32(g.winH)) + 1
	}
	gpe.typ = glwPointerScroll
	gpe.deltaY = -0.2 * float32(yoff)
	glwLock(&g.gr)
	glwPointerEvent(&g.gr, &gpe)
	glwUnlock(&g.gr)
}

//export glfwGoFbSizeCB
func glfwGoFbSizeCB(w *C.GLFWwindow, fw, fh C.int) {
	g := glfwCurrent
	if g == nil {
		return
	}
	// C: ConfigureNotify
	if g.fixedWindowSize != 0 {
		return
	}

	C.glViewport(0, 0, C.GLsizei(fw), C.GLsizei(fh))
	glwLock(&g.gr)
	g.gr.grWidth = int(fw)
	g.gr.grHeight = int(fh)
	glwUnlock(&g.gr)
}

//export glfwGoWinSizeCB
func glfwGoWinSizeCB(w *C.GLFWwindow, ww, wh C.int) {
	g := glfwCurrent
	if g == nil {
		return
	}
	g.winW = int(ww)
	g.winH = int(wh)
}

//export glfwGoRefreshCB
func glfwGoRefreshCB(w *C.GLFWwindow) {
	g := glfwCurrent
	if g == nil {
		return
	}
	// C: Expose
	glwLock(&g.gr)
	glwNeedRefresh(&g.gr, 0)
	glwUnlock(&g.gr)
}

//export glfwGoCloseCB
func glfwGoCloseCB(w *C.GLFWwindow) {
	// C: ClientMessage WM_DELETE_WINDOW — the window itself stays
	// alive; the app shutdown path tears it down.
	if glwDeps.appShutdown != nil {
		glwDeps.appShutdown(0)
	}
}

// C: update_gpu_info (glw_x11.c:936-947)
func glfwUpdateGpuInfo(g *glwGlfw) {
	gpu := glwDeps.pm.CreateEx(g.gr.grPropUi, "gpu", nil, false, false)
	gpu.CreateString("vendor",
		C.GoString((*C.char)(unsafe.Pointer(C.glGetString(C.GL_VENDOR)))))
	gpu.CreateString("name",
		C.GoString((*C.char)(unsafe.Pointer(C.glGetString(C.GL_RENDERER)))))
	gpu.CreateString("driver",
		C.GoString((*C.char)(unsafe.Pointer(C.glGetString(C.GL_VERSION)))))
}

// C: glw_x11_mainloop (glw_x11.c:964-1234) — the XNextEvent drain is
// replaced by glfwPollEvents dispatching the callbacks above.
// Screensaver suspend/resume is the XResetScreenSaver callout on X11,
// an idle-inhibit inhibitor on Wayland (fullwindow-tracked, as C).
func glwGlfwMainloop(g *glwGlfw) {
	frame := 0

	start := time.Now() // C: clock_gettime(CLOCK_MONOTONIC)

	fwsub := glwPropSubscribeTags(0,
		propTagName, []string{"ui", "fullwindow"},
		propTagCourier, g.gr.grCourier,
		propTagCallbackInt, glwGlfwInFullwindow, g,
		propTagRoot, g.gr.grPropUi,
	)

	glwSetFullscreen(&g.gr, g.isFullscreen)

	for g.running.Load() {

		glfwAutohideCursor(g)

		// C: glw_x11.c:994-1004 — GLFW always has a WM and
		// no_screensaver is never set, so suspend tracks
		// fullwindow only.
		if g.fullwindow != 0 && g.sss == nil {
			g.sss = glfwScreensaverSuspend(g)
		}
		if g.fullwindow == 0 && g.sss != nil {
			glfwScreensaverResume(g.sss)
			g.sss = nil
		}

		if g.isFullscreen != g.wantFullscreen {
			glfwWindowChangeFullscreen(g)
		}

		C.glfwPollEvents()

		gr := &g.gr

		glwLock(gr)

		flags := 0

		if g.isFullscreen == 0 {
			gr.grScreensaverResetAt = gr.grFrameStart
		}

		glwPrepareFrame(gr, flags)
		refresh := gr.grNeedRefresh
		gr.grNeedRefresh = 0

		if refresh != 0 {
			var rc glwRctx
			zmax := 0
			glwRctxSetup(&rc, gr.grWidth, gr.grHeight, 1, &zmax)

			glwLayout0(gr.grUniverse, &rc)

			if refresh&glwRefreshFlagRender != 0 {
				C.glClear(C.GL_DEPTH_BUFFER_BIT | C.GL_COLOR_BUFFER_BIT)
				glwRender0(gr.grUniverse, &rc)
			}
		}

		glwUnlock(gr)

		if refresh&glwRefreshFlagRender != 0 {
			glwPostScene(gr)

			if g.wlPacing {
				if g.wlOccluded {
					// Occluded: never swap (an occluded surface
					// stops releasing buffers → eglSwapBuffers
					// would stall the thread). Probe visibility
					// with an empty commit; the frame callback
					// fires when the surface is repainted.
					glfwWlFrameProbe(g)
					if glfwWlFrameWait(g, 50) != 0 {
						g.wlOccluded = false
					}
				} else {
					// Request the frame callback before the
					// commit so it binds to the frame being
					// presented, then wait bounded.
					glfwWlFrameRequest(g)
					C.glfwSwapBuffers(g.window)
					if glfwWlFrameWait(g, 250) == 0 {
						g.wlOccluded = true
					}
				}
			} else {
				if g.workingVsync == 0 {
					// C: clock_nanosleep(TIMER_ABSTIME) to the 60Hz deadline
					deadline := start.Add(
						time.Duration(frame) * time.Second / 60)
					time.Sleep(time.Until(deadline))
				}
				C.glfwSwapBuffers(g.window)
			}
		} else {
			time.Sleep(16666 * time.Microsecond)
		}

		frame++
	}

	// C: x11_screensaver_resume (glw_x11.c:1227)
	if g.sss != nil {
		glfwScreensaverResume(g.sss)
		g.sss = nil
	}

	glwDeps.pm.Unsubscribe(fwsub)

	glfwWindowShutdown(g)
}

// C: eventsink (glw_x11.c:1242-1260) — frontend eventSink handling
// for ACTION_FULLSCREEN_TOGGLE.
func glfwEventsink(opaque any, event propcore.EventType,
	args ...any) {
	g := opaque.(*glwGlfw)

	switch event {
	case propcore.EventExtEvent:
		if len(args) > 0 {
			if e, ok := args[0].(*eventpkg.Event); ok &&
				e.IsAction(eventpkg.ACTION_FULLSCREEN_TOGGLE) {
				if g.wantFullscreen != 0 {
					g.wantFullscreen = 0
				} else {
					g.wantFullscreen = 1
				}
			}
		}
	}
}

// C: glw_x11_thread (glw_x11.c:1267-1319)
func glwGlfwThread(aux any) any {
	// The GL context is bound to the OS thread via
	// glfwMakeContextCurrent — pin this goroutine so it cannot
	// migrate (C: dedicated pthread).
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	g := aux.(*glwGlfw)
	glfwCurrent = g
	gr := &g.gr

	// This may aid some vsync problems with nVidia drivers
	os.Setenv("__GL_SYNC_TO_VBLANK", "1")

	if glwGlfwStart(g) != 0 {
		return nil
	}

	if glwStart(gr) != 0 {
		return nil
	}

	evsub := glwPropSubscribeTags(0,
		propTagCallback, glfwEventsink, g,
		propTagName, []string{"ui", "eventSink"},
		propTagRoot, gr.grPropUi,
		propTagCourier, gr.grCourier,
	)

	if glwDeps.gconf.Fullscreen {
		g.wantFullscreen = 1
	}

	glfwUpdateGpuInfo(g)
	glwLock(gr)
	glwLoadUniverse(gr)
	glwUnlock(gr)

	glwGlfwMainloop(g)

	glwLock(gr)
	glwUnloadUniverse(gr)
	glwUnlock(gr)
	glwReap(gr)
	glwReap(gr)

	glwDeps.pm.Unsubscribe(evsub)

	glwFini(gr)
	return nil
}

// C: glw_x11_start (glw_x11.c:1327-1339)
func GlwGlfwStart(nav *propcore.Prop) any {
	g := &glwGlfw{}

	g.gr.grPropUi = glwDeps.pm.CreateRoot("ui")
	if nav != nil {
		g.gr.grPropNav = nav
	} else if GlwX11NavSpawnFn != nil {
		g.gr.grPropNav = GlwX11NavSpawnFn()
	}
	g.running.Store(true)

	g.thread = archpkg.ThreadCreateJoinable("glw",
		glwGlfwThread, g, 0)

	return g
}

// C: glw_x11_stop (glw_x11.c:1345-1355)
func GlwGlfwStop(aux any) *propcore.Prop {
	g := aux.(*glwGlfw)
	gr := &g.gr
	nav := gr.grPropNav
	g.running.Store(false)
	g.thread.Join()
	glfwCurrent = nil
	C.glfwTerminate()
	glwDeps.pm.Destroy(gr.grPropUi)
	glwReleaseRoot(gr)
	return nav
}
