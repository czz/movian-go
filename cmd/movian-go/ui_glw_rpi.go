//go:build linux && rpi

package main

import (
	"github.com/czz/movian-go/internal/arch"
	archrpi "github.com/czz/movian-go/internal/arch/rpi"
	uiglw "github.com/czz/movian-go/internal/ui/glw"
)

// uiGlw — C: the rpi frontend, wired through the shared linux_ui_t seam.
// In C, main() calls rpi_mainloop() directly on the main thread
// (rpi_main.c:946); the Go port runs the render loop on the frontend
// thread while the main goroutine pumps the UI courier — the same seam
// the X11/GLFW frontends use.
var uiGlw = &arch.LinuxUI{Start: uiglw.GlwRpiStart, Stop: uiglw.GlwRpiStop}

// renderLoop — C: rpi_main.c main() tail: stos_stop_splash() then
// rpi_mainloop() → main_fini() → arch_exit().
func renderLoop(ctx *appContext) {
	// C: stos_stop_splash() (rpi_main.c:945) — immediately before
	// rpi_mainloop.
	archrpi.StosStopSplash()

	arch.UILoopStart(ctx.propManager, ctx.eventManager)
	arch.SubscribeGlobalEventsink()
	// C: shutdown_hook_add(arch_stop_req, NULL, 1) — rpi arch_stop_req
	// sets runmode=RUNMODE_EXIT; UILoopStopReq wakes the courier pump.
	arch.ShutdownHookAdd(func(any, int) {
		archrpi.ArchStopReq()
		arch.UILoopStopReq()
	}, nil, 1)
	arch.Mainloop(uiGlw, nil)
}
