//go:build linux && glfw

package glw

// GLFW platform layer — linux (X11 + Wayland). Split from glw_glfw.go:
// the C preamble carries the Wayland protocol objects
// (keyboard-shortcuts-inhibit, pointer-constraints, idle-inhibit,
// text-input-v3) that have no counterpart on darwin.

/*
#cgo linux pkg-config: wayland-client
#define GL_GLEXT_PROTOTYPES
#include <GLFW/glfw3.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#define GLFW_EXPOSE_NATIVE_WAYLAND
#include <GLFW/glfw3native.h>
#include <poll.h>
#include <time.h>
#include <errno.h>
#include "glfw_wayland_ksi.h"
#include "glfw_wayland_pc.h"
#include "glfw_wayland_idle.h"
#include "glfw_wayland_ti.h"
#include "glfw_wayland_ksi.inc"
#include "glfw_wayland_pc.inc"
#include "glfw_wayland_idle.inc"
#include "glfw_wayland_ti.inc"

extern void glfwGoTextCommitCB(char*);

// --- fullscreen_grab (glw_x11.c:198-218) equivalents ---

// Wayland: no global grabs exist. keyboard-shortcuts-inhibit delivers
// all keys (incl. compositor shortcuts) to the surface — the
// XGrabKeyboard equivalent — and pointer-constraints confines the
// pointer to the surface, the XGrabPointer(confine_to=win) equivalent.
struct glfw_wl_grab {
  struct zwp_keyboard_shortcuts_inhibit_manager_v1 *ksi_mgr;
  struct zwp_pointer_constraints_v1 *constraints;
  struct wl_seat *seat;
  struct wl_pointer *pointer;
  struct zwp_keyboard_shortcuts_inhibitor_v1 *inhibitor;
  struct zwp_confined_pointer_v1 *confined;
};

static void glfw_wl_reg_global(void *data, struct wl_registry *reg,
                             uint32_t name, const char *iface,
                             uint32_t version) {
  struct glfw_wl_grab *g = data;
  if (!strcmp(iface, zwp_keyboard_shortcuts_inhibit_manager_v1_interface.name))
    g->ksi_mgr = wl_registry_bind(reg, name,
        &zwp_keyboard_shortcuts_inhibit_manager_v1_interface, 1);
  else if (!strcmp(iface, zwp_pointer_constraints_v1_interface.name))
    g->constraints = wl_registry_bind(reg, name,
        &zwp_pointer_constraints_v1_interface, 1);
  else if (!strcmp(iface, wl_seat_interface.name) && g->seat == NULL)
    g->seat = wl_registry_bind(reg, name, &wl_seat_interface, 1);
}
static void glfw_wl_reg_remove(void *d, struct wl_registry *r, uint32_t n) {}
static const struct wl_registry_listener glfw_wl_reg_listener = {
  glfw_wl_reg_global, glfw_wl_reg_remove,
};

// The registry roundtrip runs on a private event queue so it cannot
// steal events queued for GLFW's own proxies on the default queue.
// Bound proxies inherit that queue, so they are moved back to the
// default queue before it is destroyed — a proxy must not outlive
// its queue.
static void glfw_wl_default_queue(struct wl_proxy *p) {
  if (p != NULL)
    wl_proxy_set_queue(p, NULL);
}

static struct glfw_wl_grab *glfw_wl_grab_new(GLFWwindow *win) {
  struct wl_display *dpy = glfwGetWaylandDisplay();
  struct wl_surface *surf = glfwGetWaylandWindow(win);
  struct glfw_wl_grab *g;
  struct wl_event_queue *q;
  struct wl_registry *reg;
  if (!dpy || !surf)
    return NULL;
  g = calloc(1, sizeof(*g));
  q = wl_display_create_queue(dpy);
  reg = wl_display_get_registry(dpy);
  wl_proxy_set_queue((struct wl_proxy *)reg, q);
  wl_registry_add_listener(reg, &glfw_wl_reg_listener, g);
  wl_display_roundtrip_queue(dpy, q);
  glfw_wl_default_queue((struct wl_proxy *)g->ksi_mgr);
  glfw_wl_default_queue((struct wl_proxy *)g->constraints);
  glfw_wl_default_queue((struct wl_proxy *)g->seat);
  wl_registry_destroy(reg);
  wl_event_queue_destroy(q);
  if (g->seat != NULL)
    g->pointer = wl_seat_get_pointer(g->seat);
  if (g->ksi_mgr != NULL && g->seat != NULL)
    g->inhibitor =
      zwp_keyboard_shortcuts_inhibit_manager_v1_inhibit_shortcuts(
          g->ksi_mgr, surf, g->seat);
  if (g->constraints != NULL && g->pointer != NULL)
    g->confined = zwp_pointer_constraints_v1_confine_pointer(
        g->constraints, surf, g->pointer, NULL,
        ZWP_POINTER_CONSTRAINTS_V1_LIFETIME_PERSISTENT);
  return g;
}

static void glfw_wl_grab_free(struct glfw_wl_grab *g) {
  if (g == NULL)
    return;
  if (g->confined != NULL)
    zwp_confined_pointer_v1_destroy(g->confined);
  if (g->inhibitor != NULL)
    zwp_keyboard_shortcuts_inhibitor_v1_destroy(g->inhibitor);
  if (g->pointer != NULL)
    wl_pointer_destroy(g->pointer);
  if (g->seat != NULL)
    wl_seat_destroy(g->seat);
  if (g->constraints != NULL)
    zwp_pointer_constraints_v1_destroy(g->constraints);
  if (g->ksi_mgr != NULL)
    zwp_keyboard_shortcuts_inhibit_manager_v1_destroy(g->ksi_mgr);
  free(g);
}

// X11: the grab/screensaver X calls live in glw_glfw_x11.go (build
// tag x11) — Go wrappers glfwX11Grab/glfwX11Ungrab/glfwX11SsReset.
// Without the x11 tag they are no-ops (glw_glfw_x11_off.go).

// --- screensaver suspend/resume (x11_common.c) equivalents ---

// Wayland: idle-inhibit-unstable-v1 — an inhibitor object exists while
// inhibition is wanted; destroying it resumes (replaces X11's
// XResetScreenSaver callout, which Wayland has no equivalent of).
struct glfw_idle_scan {
  struct zwp_idle_inhibit_manager_v1 *mgr;
};
static void glfw_idle_reg_global(void *data, struct wl_registry *reg,
                                 uint32_t name, const char *iface,
                                 uint32_t version) {
  struct glfw_idle_scan *s = data;
  if (!strcmp(iface, zwp_idle_inhibit_manager_v1_interface.name))
    s->mgr = wl_registry_bind(reg, name,
        &zwp_idle_inhibit_manager_v1_interface, 1);
}
static void glfw_idle_reg_remove(void *d, struct wl_registry *r, uint32_t n) {}
static const struct wl_registry_listener glfw_idle_reg_listener = {
  glfw_idle_reg_global, glfw_idle_reg_remove,
};

static struct zwp_idle_inhibitor_v1 *glfw_wl_idle_inhibit(GLFWwindow *win) {
  struct wl_display *dpy = glfwGetWaylandDisplay();
  struct wl_surface *surf = glfwGetWaylandWindow(win);
  struct zwp_idle_inhibitor_v1 *inhib;
  struct glfw_idle_scan s = {NULL};
  struct wl_event_queue *q;
  struct wl_registry *reg;
  if (!dpy || !surf)
    return NULL;
  q = wl_display_create_queue(dpy);
  reg = wl_display_get_registry(dpy);
  wl_proxy_set_queue((struct wl_proxy *)reg, q);
  wl_registry_add_listener(reg, &glfw_idle_reg_listener, &s);
  wl_display_roundtrip_queue(dpy, q);
  glfw_wl_default_queue((struct wl_proxy *)s.mgr);
  wl_registry_destroy(reg);
  wl_event_queue_destroy(q);
  if (s.mgr == NULL)
    return NULL;
  inhib = zwp_idle_inhibit_manager_v1_create_inhibitor(s.mgr, surf);
  zwp_idle_inhibit_manager_v1_destroy(s.mgr);
  return inhib;
}

static void glfw_wl_idle_uninhibit(struct zwp_idle_inhibitor_v1 *inhib) {
  if (inhib != NULL)
    zwp_idle_inhibitor_v1_destroy(inhib);
}

// --- input method (C: XCreateIC XIMPreeditNothing|XIMStatusNothing) ---
// Wayland equivalent: text-input-unstable-v3. The C input context
// disables preedit rendering and consumes only the committed string —
// mirrored here: preedit/delete events are ignored and commit_string
// is buffered until the done event (the protocol's apply point).

struct glfw_wl_ti {
  struct zwp_text_input_manager_v3 *mgr;
  struct wl_seat *seat;
  struct zwp_text_input_v3 *ti;
  char *pending; // commit_string buffered until done
};

static void glfw_ti_enter(void *d, struct zwp_text_input_v3 *t,
                        struct wl_surface *s) {}
static void glfw_ti_leave(void *d, struct zwp_text_input_v3 *t,
                        struct wl_surface *s) {}
static void glfw_ti_preedit(void *d, struct zwp_text_input_v3 *t,
                            const char *text, int32_t b, int32_t e) {}
static void glfw_ti_delete(void *d, struct zwp_text_input_v3 *t,
                           uint32_t before, uint32_t after) {}
static void glfw_ti_commit_string(void *data, struct zwp_text_input_v3 *t,
                                  const char *text) {
  struct glfw_wl_ti *g = data;
  free(g->pending);
  g->pending = text ? strdup(text) : NULL;
}
static void glfw_ti_done(void *data, struct zwp_text_input_v3 *t,
                         uint32_t serial) {
  struct glfw_wl_ti *g = data;
  if (g->pending != NULL) {
    glfwGoTextCommitCB(g->pending);
    free(g->pending);
    g->pending = NULL;
  }
}
static const struct zwp_text_input_v3_listener glfw_ti_listener = {
  glfw_ti_enter, glfw_ti_leave, glfw_ti_preedit,
  glfw_ti_commit_string, glfw_ti_delete, glfw_ti_done,
};

struct glfw_ti_scan {
  struct glfw_wl_ti *g;
};
static void glfw_ti_reg_global(void *data, struct wl_registry *reg,
                               uint32_t name, const char *iface,
                               uint32_t version) {
  struct glfw_wl_ti *g = data;
  if (!strcmp(iface, zwp_text_input_manager_v3_interface.name))
    g->mgr = wl_registry_bind(reg, name,
        &zwp_text_input_manager_v3_interface, 1);
  else if (!strcmp(iface, wl_seat_interface.name) && g->seat == NULL)
    g->seat = wl_registry_bind(reg, name, &wl_seat_interface, 1);
}
static void glfw_ti_reg_remove(void *d, struct wl_registry *r, uint32_t n) {}
static const struct wl_registry_listener glfw_ti_reg_listener = {
  glfw_ti_reg_global, glfw_ti_reg_remove,
};

// C: input context created once for the window and always active —
// enable+commit is issued once; the compositor only delivers events
// while the surface has keyboard focus.
static struct glfw_wl_ti *glfw_wl_ti_new(GLFWwindow *win) {
  struct wl_display *dpy = glfwGetWaylandDisplay();
  struct glfw_wl_ti *g;
  struct wl_event_queue *q;
  struct wl_registry *reg;
  if (!dpy)
    return NULL;
  g = calloc(1, sizeof(*g));
  q = wl_display_create_queue(dpy);
  reg = wl_display_get_registry(dpy);
  wl_proxy_set_queue((struct wl_proxy *)reg, q);
  wl_registry_add_listener(reg, &glfw_ti_reg_listener, g);
  wl_display_roundtrip_queue(dpy, q);
  glfw_wl_default_queue((struct wl_proxy *)g->mgr);
  glfw_wl_default_queue((struct wl_proxy *)g->seat);
  wl_registry_destroy(reg);
  wl_event_queue_destroy(q);
  if (g->mgr != NULL && g->seat != NULL) {
    g->ti = zwp_text_input_manager_v3_get_text_input(g->mgr, g->seat);
    if (g->ti != NULL) {
      zwp_text_input_v3_add_listener(g->ti, &glfw_ti_listener, g);
      zwp_text_input_v3_enable(g->ti);
      zwp_text_input_v3_commit(g->ti);
    }
  }
  return g;
}

static void glfw_wl_ti_free(struct glfw_wl_ti *g) {
  if (g == NULL)
    return;
  if (g->ti != NULL) {
    zwp_text_input_v3_disable(g->ti);
    zwp_text_input_v3_commit(g->ti);
    zwp_text_input_v3_destroy(g->ti);
  }
  if (g->seat != NULL)
    wl_seat_destroy(g->seat);
  if (g->mgr != NULL)
    zwp_text_input_manager_v3_destroy(g->mgr);
  free(g->pending);
  free(g);
}

// --- Wayland frame-callback pacing ---
// wl_surface_frame gives true vblank pacing under PRIME/render-offload
// (the callback comes from the compositor driving the display, not the
// rendering GPU — unlike glXSwapInterval which never blocks there).
// The wait is bounded: an occluded surface stops receiving callbacks,
// so a timeout falls back to timer pacing instead of stalling the UI
// thread, which also dispatches input.

static struct wl_event_queue *glw_wl_pacing_q;
static struct wl_callback *glw_wl_pending_cb;
static volatile int glw_wl_frame_fired;

static void glw_wl_frame_done(void *data, struct wl_callback *cb,
                              uint32_t time) {
  glw_wl_frame_fired = 1;
  wl_callback_destroy(cb);
  glw_wl_pending_cb = NULL;
}
static const struct wl_callback_listener glw_wl_frame_listener = {
  glw_wl_frame_done,
};

static long long glw_wl_now_ms(void) {
  struct timespec ts;
  clock_gettime(CLOCK_MONOTONIC, &ts);
  return (long long)ts.tv_sec * 1000 + ts.tv_nsec / 1000000;
}

static int glw_wl_pacing_init(GLFWwindow *win) {
  struct wl_display *dpy = glfwGetWaylandDisplay();
  if (dpy == NULL || glfwGetWaylandWindow(win) == NULL)
    return 1;
  if (glw_wl_pacing_q == NULL)
    glw_wl_pacing_q = wl_display_create_queue(dpy);
  return glw_wl_pacing_q == NULL;
}

// Request a frame callback bound to the next commit — must run before
// eglSwapBuffers so the callback attaches to the frame being presented.
static void glw_wl_frame_request(GLFWwindow *win) {
  struct wl_surface *surf = glfwGetWaylandWindow(win);
  struct wl_callback *cb;
  if (surf == NULL || glw_wl_pending_cb != NULL)
    return;
  glw_wl_frame_fired = 0;
  cb = wl_surface_frame(surf);
  if (cb == NULL)
    return;
  wl_proxy_set_queue((struct wl_proxy *)cb, glw_wl_pacing_q);
  wl_callback_add_listener(cb, &glw_wl_frame_listener, NULL);
  glw_wl_pending_cb = cb;
}

// While occluded: probe visibility with an empty commit — the pending
// frame request fires as soon as the compositor repaints the surface
// (i.e. it becomes visible again). No buffer attach → no EGL buffer
// queue growth → no swap stall.
static void glw_wl_frame_probe(GLFWwindow *win) {
  struct wl_surface *surf = glfwGetWaylandWindow(win);
  glw_wl_frame_request(win);
  if (surf != NULL)
    wl_surface_commit(surf);
}

// Wait for the pending frame callback, at most timeout_ms.
// Returns 1 if it fired, 0 on timeout/error (occluded or broken
// compositor). Events for GLFW's proxies are routed to the default
// queue and dispatched by glfwPollEvents later — none are lost.
static int glw_wl_frame_wait(GLFWwindow *win, int timeout_ms) {
  struct wl_display *dpy = glfwGetWaylandDisplay();
  struct pollfd pfd;
  long long deadline, rem;
  int fd, r;
  if (dpy == NULL)
    return 0;
  fd = wl_display_get_fd(dpy);
  deadline = glw_wl_now_ms() + timeout_ms;
  for (;;) {
    // Events already read into our queue (glfwPollEvents reads the fd
    // for all queues) must be dispatched before checking the deadline.
    wl_display_dispatch_queue_pending(dpy, glw_wl_pacing_q);
    if (glw_wl_frame_fired)
      return 1;
    rem = deadline - glw_wl_now_ms();
    if (rem <= 0)
      return 0;
    while (wl_display_prepare_read_queue(dpy, glw_wl_pacing_q) != 0)
      wl_display_dispatch_queue_pending(dpy, glw_wl_pacing_q);
    if (wl_display_flush(dpy) < 0) {
      wl_display_cancel_read(dpy);
      return 0;
    }
    pfd.fd = fd;
    pfd.events = POLLIN;
    pfd.revents = 0;
    r = poll(&pfd, 1, (int)rem);
    if (r > 0 && (pfd.revents & POLLIN)) {
      wl_display_read_events(dpy);
    } else {
      wl_display_cancel_read(dpy);
      if (r == 0)
        return 0;
      if (r < 0 && errno != EINTR)
        return 0;
      continue;
    }
  }
}

static void glw_wl_pacing_fini(void) {
  if (glw_wl_pending_cb != NULL) {
    wl_callback_destroy(glw_wl_pending_cb);
    glw_wl_pending_cb = NULL;
  }
  if (glw_wl_pacing_q != NULL) {
    wl_event_queue_destroy(glw_wl_pacing_q);
    glw_wl_pacing_q = NULL;
  }
}
*/
import "C"

import (
	"time"
	"unsafe"

	eventpkg "github.com/czz/movian-go/internal/event"
	tracepkg "github.com/czz/movian-go/internal/trace"
)

// glwGlfwPlat — linux platform state: the Wayland grab
// (struct glfw_wl_grab*; nil on X11, where the grab lives entirely
// X-server-side) and the Wayland text-input object (XIC equivalent).
type glwGlfwPlat struct {
	wlGrab      *C.struct_glfw_wl_grab
	wlTextInput *C.struct_glfw_wl_ti
}

// glfwScreensaverState — C: struct x11_screensaver_state
// (x11_common.c:55-60). X11: Go timer re-arming XResetScreenSaver
// (the C callout chain); Wayland: idle-inhibit object.
type glfwScreensaverState struct {
	timer     *time.Timer                     // X11 reset callout
	inhibitor *C.struct_zwp_idle_inhibitor_v1 // Wayland inhibitor
}

// x11Reset — C: reset_screensaver (x11_common.c:65-73). First arm is
// 1s (x11_screensaver_suspend), re-arms at 30s.
func (s *glfwScreensaverState) x11Reset(win *C.GLFWwindow, after time.Duration) {
	s.timer = time.AfterFunc(after, func() {
		glfwX11SsReset(unsafe.Pointer(win))
		s.x11Reset(win, 30*time.Second)
	})
}

// C: x11_screensaver_suspend (x11_common.c:78-92)
func glfwScreensaverSuspend(g *glwGlfw) *glfwScreensaverState {
	s := &glfwScreensaverState{}
	if C.glfwGetPlatform() == C.GLFW_PLATFORM_X11 {
		s.x11Reset(g.window, time.Second)
	} else if C.glfwGetPlatform() == C.GLFW_PLATFORM_WAYLAND {
		s.inhibitor = C.glfw_wl_idle_inhibit(g.window)
		if s.inhibitor == nil {
			glwDeps.ts.Trace(tracepkg.TRACE_INFO, "GLW",
				"Wayland idle-inhibit not available")
		}
	}
	glwDeps.ts.Trace(tracepkg.TRACE_DEBUG, "GLW", "Suspending screensaver")
	return s
}

// C: x11_screensaver_resume (x11_common.c:98-103)
func glfwScreensaverResume(s *glfwScreensaverState) {
	glwDeps.ts.Trace(tracepkg.TRACE_DEBUG, "GLW", "Resuming screensaver")
	if s.timer != nil {
		s.timer.Stop()
	}
	C.glfw_wl_idle_uninhibit(s.inhibitor)
}

// C: fullscreen_grab (glw_x11.c:198-218) + the ungrab in
// window_shutdown (glw_x11.c:408-410). In C the grab exists because
// the fullscreen window is override-redirect (bypasses the WM) —
// the WM-fullscreen path (wm_set_fullscreen) does not grab. GLFW's
// glfwSetWindowMonitor is always "WM fullscreen", so the grab is
// tied to the fullscreen STATE instead: it preserves C's intent —
// a fullscreen Movian owns all input. On X11 the same X calls run
// on GLFW's window via native access; on Wayland the equivalents
// are keyboard-shortcuts-inhibit (keyboard grab) and
// pointer-constraints (pointer grab/confine).
func glfwFullscreenGrab(g *glwGlfw) {
	if C.glfwGetPlatform() == C.GLFW_PLATFORM_X11 {
		if glfwX11Grab(unsafe.Pointer(g.window)) == 0 {
			glwDeps.ts.Trace(tracepkg.TRACE_INFO, "GLW",
				"X11 fullscreen grab failed")
		}
	} else if C.glfwGetPlatform() == C.GLFW_PLATFORM_WAYLAND {
		g.plat.wlGrab = C.glfw_wl_grab_new(g.window)
		if g.plat.wlGrab == nil {
			glwDeps.ts.Trace(tracepkg.TRACE_INFO, "GLW",
				"Wayland fullscreen grab failed")
		}
	}
	// C: XWarpPointer → screen center (ignored by GLFW on Wayland).
	C.glfwSetCursorPos(g.window,
		C.double(g.gr.grWidth)/2, C.double(g.gr.grHeight)/2)
}

func glfwFullscreenUngrab(g *glwGlfw) {
	if C.glfwGetPlatform() == C.GLFW_PLATFORM_X11 {
		glfwX11Ungrab(unsafe.Pointer(g.window))
	} else if g.plat.wlGrab != nil {
		C.glfw_wl_grab_free(g.plat.wlGrab)
		g.plat.wlGrab = nil
	}
}

// glfwPlatformWindowOpen — C: XCreateIC in window_open
// (glw_x11.c:363-368); the Wayland equivalent is a text-input-v3
// object on the window's seat.
func glfwPlatformWindowOpen(g *glwGlfw) {
	if C.glfwGetPlatform() == C.GLFW_PLATFORM_WAYLAND {
		g.plat.wlTextInput = C.glfw_wl_ti_new(g.window)
	}
}

// glfwPlatformWindowClose — C: XDestroyIC on window close.
func glfwPlatformWindowClose(g *glwGlfw) {
	if g.plat.wlTextInput != nil {
		C.glfw_wl_ti_free(g.plat.wlTextInput)
		g.plat.wlTextInput = nil
	}
}

// C: the XF86 keysym entries of key2action (glw_x11.c keysymmap
// tail). GLFW maps no media keys — they surface as GLFW_KEY_UNKNOWN
// and must be resolved by scancode: the evdev keycode on Wayland, the
// XKB keycode (evdev+8) on X11. Keys below are evdev codes
// (linux/input-event-codes.h) for the same keysyms as C.
var glfwMediaKey2action = []struct {
	scancode int
	action   eventpkg.ActionType
}{
	{201, eventpkg.ACTION_PLAYPAUSE},          // KEY_PAUSECD — XF86XK_AudioPause
	{200, eventpkg.ACTION_PLAYPAUSE},          // KEY_PLAYCD — XF86XK_AudioPlay
	{164, eventpkg.ACTION_PLAYPAUSE},          // KEY_PLAYPAUSE — XF86AudioPlayPause
	{166, eventpkg.ACTION_STOP},               // KEY_STOPCD — XF86XK_AudioStop
	{161, eventpkg.ACTION_EJECT},              // KEY_EJECTCD — XF86XK_Eject
	{162, eventpkg.ACTION_EJECT},              // KEY_EJECTCLOSECD — XF86XK_Eject
	{167, eventpkg.ACTION_RECORD},             // KEY_RECORD — XF86XK_AudioRecord
	{163, eventpkg.ACTION_SKIP_FORWARD},       // KEY_NEXTSONG — XF86XK_AudioNext
	{165, eventpkg.ACTION_SKIP_BACKWARD},      // KEY_PREVIOUSSONG — XF86XK_AudioPrev
	{114, eventpkg.ACTION_VOLUME_DOWN},        // KEY_VOLUMEDOWN — XF86XK_AudioLowerVolume
	{115, eventpkg.ACTION_VOLUME_UP},          // KEY_VOLUMEUP — XF86XK_AudioRaiseVolume
	{113, eventpkg.ACTION_VOLUME_MUTE_TOGGLE}, // KEY_MUTE — XF86XK_AudioMute
	{205, eventpkg.ACTION_STANDBY},            // KEY_SUSPEND — XF86XK_Standby
	{142, eventpkg.ACTION_STANDBY},            // KEY_SLEEP — XF86Sleep
}

// glfwMediaKeyAction — the modifier==0 XF86 keysym path of key2action.
// Scancode is the evdev keycode on Wayland, the XKB keycode (evdev+8)
// on X11. Returns the injected-ready event or nil.
func glfwMediaKeyAction(scancode int) *eventpkg.Event {
	evdev := scancode
	if C.glfwGetPlatform() == C.GLFW_PLATFORM_X11 {
		evdev -= 8
	}
	for i := range len(glfwMediaKey2action) {
		if glfwMediaKey2action[i].scancode == evdev {
			eav := glwDeps.em.CreateActionMulti([]eventpkg.ActionType{
				glfwMediaKey2action[i].action})
			return &eav.Event
		}
	}
	return nil
}

//export glfwGoTextCommitCB
func glfwGoTextCommitCB(text *C.char) {
	g := glfwCurrent
	if g == nil {
		return
	}
	// C: Xutf8LookupString result → EVENT_UNICODE per char (the IM
	// commit carries the same text XLookupString would produce).
	for _, r := range C.GoString(text) {
		ei := glwDeps.em.CreateInt(eventpkg.EVENT_UNICODE, int(r))
		e := &ei.Event
		e.Flags |= eventpkg.EventKeypress
		glwInjectEvent(&g.gr, e)
	}
}

// glfwWlPacingStart — allocates the private event queue used by the
// frame-callback pacing. Returns 0 on success (Wayland only).
func glfwWlPacingStart(g *glwGlfw) int {
	return int(C.glw_wl_pacing_init(g.window))
}

// glfwWlFrameRequest — wl_surface_frame before the commit (swap).
func glfwWlFrameRequest(g *glwGlfw) {
	C.glw_wl_frame_request(g.window)
}

// glfwWlFrameWait — bounded wait for the frame callback.
func glfwWlFrameWait(g *glwGlfw, timeoutMs int) int {
	return int(C.glw_wl_frame_wait(g.window, C.int(timeoutMs)))
}

// glfwWlPacingFini — releases the pending callback and event queue.
func glfwWlPacingFini(g *glwGlfw) {
	C.glw_wl_pacing_fini()
}

// glfwWlFrameProbe — occluded-state visibility probe: frame callback +
// empty commit (no buffer attach → no EGL queue pressure).
func glfwWlFrameProbe(g *glwGlfw) {
	C.glw_wl_frame_probe(g.window)
}
