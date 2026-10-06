//go:build windows && glfw

package glw

// GLFW platform layer — windows. No upstream C counterpart (upstream
// Movian has no win32 arch); modeled on the x11/darwin platform files.
// Windows has real per-thread execution-state control, so the
// screensaver seam maps to SetThreadExecutionState (playback-scoped,
// like x11_screensaver_suspend) rather than darwin's repeating ping.

/*
#include <windows.h>

// ES_CONTINUOUS|ES_SYSTEM_REQUIRED|ES_DISPLAY_REQUIRED — same role as
// XResetScreenSaver + wayland idle-inhibit on linux.
static void glfw_es_suspend(void) {
    SetThreadExecutionState(ES_CONTINUOUS | ES_SYSTEM_REQUIRED |
                            ES_DISPLAY_REQUIRED);
}
static void glfw_es_resume(void) {
    SetThreadExecutionState(ES_CONTINUOUS);
}
*/
import "C"

import (
	eventpkg "github.com/czz/movian-go/internal/event"
	medialibav "github.com/czz/movian-go/internal/media/libav"
)

// glwGlfwPlat — windows needs no per-window platform state (GLFW owns
// the HWND; no grab/pointer-constraint machinery like wayland).
type glwGlfwPlat struct{}

// glfwScreensaverState — marker for the execution-state suspend.
type glfwScreensaverState struct{}

// glfwPlatformWindowOpen — no-op on windows: nothing to arm (no
// UpdateSystemActivity-style ping needed — SetThreadExecutionState is
// playback-scoped via glfwScreensaverSuspend).
func glfwPlatformWindowOpen(g *glwGlfw) {}

// glfwPlatformWindowClose — no-op counterpart.
func glfwPlatformWindowClose(g *glwGlfw) {}

// glfwScreensaverSuspend — C-equivalent role: linux x11 suspend /
// wayland idle-inhibit. Keeps display+system awake while held.
func glfwScreensaverSuspend(g *glwGlfw) *glfwScreensaverState {
	C.glfw_es_suspend()
	return &glfwScreensaverState{}
}

// glfwScreensaverResume — release the execution-state block.
func glfwScreensaverResume(s *glfwScreensaverState) {
	C.glfw_es_resume()
}

// glfwFullscreenGrab/Ungrab — no-op on windows: win32 has no pointer
// grab concept needed here (like darwin — fullscreen HWND owns input).
func glfwFullscreenGrab(g *glwGlfw)   {}
func glfwFullscreenUngrab(g *glwGlfw) {}

// Win32 extended scancodes (lParam bits 16-23 + extended bit 0x100, as
// GLFW reports them on win32) for the multimedia keys GLFW maps to
// GLFW_KEY_UNKNOWN. Same actions as the linux XF86 table
// (glw_glfw_linux.go glfwMediaKey2action).
var glfwMediaKey2action = []struct {
	scancode int
	action   eventpkg.ActionType
}{
	{0x122, eventpkg.ACTION_PLAYPAUSE},          // VK_MEDIA_PLAY_PAUSE
	{0x124, eventpkg.ACTION_STOP},               // VK_MEDIA_STOP
	{0x119, eventpkg.ACTION_SKIP_FORWARD},       // VK_MEDIA_NEXT_TRACK
	{0x110, eventpkg.ACTION_SKIP_BACKWARD},      // VK_MEDIA_PREV_TRACK
	{0x12E, eventpkg.ACTION_VOLUME_DOWN},        // VK_VOLUME_DOWN
	{0x130, eventpkg.ACTION_VOLUME_UP},          // VK_VOLUME_UP
	{0x120, eventpkg.ACTION_VOLUME_MUTE_TOGGLE}, // VK_VOLUME_MUTE
}

// glfwMediaKeyAction — resolves a win32 scancode to a media action.
func glfwMediaKeyAction(scancode int) *eventpkg.Event {
	for i := range len(glfwMediaKey2action) {
		if glfwMediaKey2action[i].scancode == scancode {
			eav := glwDeps.em.CreateActionMulti([]eventpkg.ActionType{
				glfwMediaKey2action[i].action})
			return &eav.Event
		}
	}
	return nil
}

// glwVaapiEGLProbe — no-op for EGL: windows hw decode is
// D3D11VA/DXVA2 (pkg/media/libav, hw→sw transfer → 'YUVP'). The eager
// probe creates the D3D device and logs the adapter/backend at
// startup.
func glwVaapiEGLProbe() { medialibav.ProbeHwAccel(glwDeps.bs.MediaSys().VS) }

// Wayland frame-callback pacing — no Wayland on windows.
func glfwWlPacingStart(g *glwGlfw) int              { return 1 }
func glfwWlFrameRequest(g *glwGlfw)                 {}
func glfwWlFrameWait(g *glwGlfw, timeoutMs int) int { return 0 }
func glfwWlPacingFini(g *glwGlfw)                   {}
func glfwWlFrameProbe(g *glwGlfw)                   {}
