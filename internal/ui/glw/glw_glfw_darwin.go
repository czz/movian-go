//go:build darwin && glfw

package glw

// GLFW platform layer — darwin. Split from glw_glfw.go: upstream's osx
// frontend (arch/osx/GLWUI.m/GLWView.m, NOT ported — GLFW owns Cocoa)
// has no fullscreen grab, no media-key mapping and no playback-scoped
// screensaver suspend; it pings UpdateSystemActivity(OverallAct) on a
// 3s CFRunLoopTimer armed unconditionally at window open
// (GLWUI.m:181-182, update_sys_activity at :209-211).

/*
#cgo LDFLAGS: -framework CoreServices
#include <stdint.h>

// C: UpdateSystemActivity(OverallAct) — declared in
// CoreServices/Power.h; declared extern here because recent SDKs no
// longer expose it in the headers (symbol still exists in the
// framework). OverallAct = 1 (UsrActivity in C sources).
extern int16_t UpdateSystemActivity(uint8_t activity);

static int16_t glfw_update_sys_activity(void) {
  return UpdateSystemActivity(1); // OverallAct
}
*/
import "C"

import (
	"time"

	eventpkg "github.com/czz/movian-go/internal/event"
	medialibav "github.com/czz/movian-go/internal/media/libav"
)

// glwGlfwPlat — darwin platform state: the UpdateSystemActivity timer
// (C: GLWUI.m CFRunLoopTimer, 3s interval).
type glwGlfwPlat struct {
	sysActivity *time.Timer
}

// glfwScreensaverState — no darwin counterpart: upstream pings
// UpdateSystemActivity unconditionally (not playback-scoped like
// x11_screensaver_suspend). Kept empty so the neutral code compiles.
type glfwScreensaverState struct{}

// glfwPlatformWindowOpen — C: the CFRunLoopTimer armed at GLWUI window
// open (GLWUI.m:181-182): UpdateSystemActivity(OverallAct) every 3s,
// unconditionally.
func glfwPlatformWindowOpen(g *glwGlfw) {
	g.plat.sysActivity = time.AfterFunc(3*time.Second, func() {
		C.glfw_update_sys_activity()
		glfwPlatformWindowOpen(g) // re-arm (C: repeating timer)
	})
}

// glfwPlatformWindowClose — C: the timer is released with the window.
func glfwPlatformWindowClose(g *glwGlfw) {
	if g.plat.sysActivity != nil {
		g.plat.sysActivity.Stop()
		g.plat.sysActivity = nil
	}
}

// glfwScreensaverSuspend — no-op on darwin (documented): upstream osx
// never suspends the screensaver per-playback; the window-open timer
// already prevents display sleep.
func glfwScreensaverSuspend(g *glwGlfw) *glfwScreensaverState {
	return &glfwScreensaverState{}
}

// glfwScreensaverResume — no-op counterpart.
func glfwScreensaverResume(s *glfwScreensaverState) {}

// glfwFullscreenGrab/Ungrab — no-op on darwin: upstream osx has no
// fullscreen grab (Cocoa fullscreen owns input via the window itself).
func glfwFullscreenGrab(g *glwGlfw)   {}
func glfwFullscreenUngrab(g *glwGlfw) {}

// glfwMediaKeyAction — nil: upstream GLWView.m keyDown maps no media
// keys (macOS media keys never reach the app via NSEvent keyDown).
func glfwMediaKeyAction(scancode int) *eventpkg.Event {
	return nil
}

// glwVaapiEGLProbe — no-op for EGL: VAAPI zero-copy is linux-only
// (glw_video_vaapi.go is linux-tagged); darwin hw decode is
// VideoToolbox (vtb_darwin.go, codec-registered). The eager probe
// logs the backend at startup.
func glwVaapiEGLProbe() { medialibav.ProbeHwAccel(glwDeps.bs.MediaSys().VS) }

// Wayland frame-callback pacing — no Wayland on darwin.
func glfwWlPacingStart(g *glwGlfw) int              { return 1 }
func glfwWlFrameRequest(g *glwGlfw)                 {}
func glfwWlFrameWait(g *glwGlfw, timeoutMs int) int { return 0 }
func glfwWlPacingFini(g *glwGlfw)                   {}
func glfwWlFrameProbe(g *glwGlfw)                   {}
