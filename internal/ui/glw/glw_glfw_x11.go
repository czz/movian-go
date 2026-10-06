//go:build linux && glfw && x11

package glw

// X11-native helpers for the GLFW backend — extension, no C
// counterpart (upstream has no GLFW backend). When GLFW runs on the
// X11 platform these are the same X calls the C code makes on its own
// window (fullscreen_grab in glw_x11.c, reset_screensaver in
// x11_common.c), applied to GLFW's window via glfw3native.h.
//
// Separated from glw_glfw.go so that a build without the x11 tag
// (pure Wayland) drops the libX11 dependency entirely — the off file
// provides no-op stubs.

/*
#cgo linux pkg-config: glfw3 x11
#include <GLFW/glfw3.h>
#include <unistd.h>
#define GLFW_EXPOSE_NATIVE_X11
#include <GLFW/glfw3native.h>
#include <X11/Xlib.h>

// X11: the same calls as C fullscreen_grab on GLFW's window via
// native access. The retry loop is bounded (100 x 100us) because a
// managed window does not depend on the grab like C's
// override-redirect one did; XWarpPointer is done Go-side via
// glfwSetCursorPos.
static int glfw_x11_grab(GLFWwindow *win) {
  Display *dpy = glfwGetX11Display();
  Window w = glfwGetX11Window(win);
  int tries;
  if (!dpy || !w)
    return 0;
  XSync(dpy, False);
  for (tries = 0; tries < 100; tries++) {
    if (XGrabPointer(dpy, w, True,
        ButtonPressMask | ButtonReleaseMask | ButtonMotionMask |
        PointerMotionMask, GrabModeAsync, GrabModeAsync,
        w, None, CurrentTime) == GrabSuccess)
      break;
    usleep(100);
  }
  if (tries == 100)
    return 0;
  XSetInputFocus(dpy, w, RevertToNone, CurrentTime);
  XGrabKeyboard(dpy, w, False, GrabModeAsync, GrabModeAsync, CurrentTime);
  return 1;
}

static void glfw_x11_ungrab(GLFWwindow *win) {
  Display *dpy = glfwGetX11Display();
  if (!dpy)
    return;
  XUngrabPointer(dpy, CurrentTime);
  XUngrabKeyboard(dpy, CurrentTime);
}

// X11: C reset_screensaver (x11_common.c:65-73) — XResetScreenSaver
// re-armed every 30s by the Go-side timer.
static void glfw_x11_ss_reset(GLFWwindow *win) {
  Display *dpy = glfwGetX11Display();
  if (dpy)
    XResetScreenSaver(dpy);
}
*/
import "C"

import "unsafe"

// glfwX11Grab — XGrabPointer + XSetInputFocus + XGrabKeyboard on
// GLFW's window (C: fullscreen_grab, glw_x11.c:198-226).
func glfwX11Grab(win unsafe.Pointer) int {
	return int(C.glfw_x11_grab((*C.GLFWwindow)(win)))
}

// glfwX11Ungrab — XUngrabPointer + XUngrabKeyboard.
func glfwX11Ungrab(win unsafe.Pointer) {
	C.glfw_x11_ungrab((*C.GLFWwindow)(win))
}

// glfwX11SsReset — XResetScreenSaver (C: reset_screensaver).
func glfwX11SsReset(win unsafe.Pointer) {
	C.glfw_x11_ss_reset((*C.GLFWwindow)(win))
}
