package glw

// C: src/ui/glw/glw.c — canonical 1:1 port.
//
// Global singletons used by C (event_mgr, the prop context, gconf.*,
// runcontrol) map to package-level seams configured at init time, matching
// the established convention (GconfClipboardSet, EsSetPropDeps, ...).

import (
	tracepkg "github.com/czz/movian-go/internal/trace"
)

// C: static void glw_dis_screensaver_callback (glw.c:174-180)
func glwDisScreensaverCallback(opaque any, value int) {
	gr := opaque.(*glwRoot)
	gr.grInhibitScreensaver = value
	glwDeps.ts.Trace(tracepkg.TRACE_DEBUG, "GLW", "Screensaver %s",
		map[bool]string{true: "inhibited", false: "allowed"}[value != 0])
}

// C: static int glw_screensaver_is_active (glw.c:609-630)
func glwScreensaverIsActive(gr *glwRoot) int {
	if gr.grScreensaverForceEnable != 0 {
		return 1
	}

	if gr.grInhibitScreensaver != 0 {
		gr.grScreensaverResetAt = gr.grFrameStart
		return 0
	}

	if gr.grIsFullscreen == 0 {
		return 0
	}

	d := glwDeps.settings.gsScreensaverDelay

	if d == 0 {
		return 0
	}

	if gr.grFrameStart > gr.grScreensaverResetAt+int64(d)*60000000 {
		return 1
	}
	return 0
}

// C: int glw_kill_screensaver (glw.c:2362-2369)
func glwKillScreensaver(gr *glwRoot) int {
	r := glwScreensaverIsActive(gr)
	glwRegisterActivity(gr)
	gr.grScreensaverForceEnable = 0
	return r
}
