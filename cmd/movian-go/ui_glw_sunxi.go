//go:build linux && sunxi

package main

import (
	"os"

	"github.com/czz/movian-go/internal/arch"
	archsunxi "github.com/czz/movian-go/internal/arch/sunxi"
	uiglw "github.com/czz/movian-go/internal/ui/glw"
)

// uiGlw — C: the sunxi frontend (sunxi_main.c ui_init/ui_run), wired
// through the shared linux_ui_t seam.
var uiGlw = &arch.LinuxUI{Start: uiglw.GlwSunxiStart, Stop: uiglw.GlwSunxiStop}

// renderLoop — C: sunxi_main.c main() (sunxi_main.c:357-383): unblock
// SIGTERM/SIGINT → doexit, ui_init + ui_run, then sunxi_fini →
// main_fini → arch_exit.
func renderLoop(ctx *appContext) {
	arch.UILoopStart(ctx.propManager, ctx.eventManager)
	arch.SubscribeGlobalEventsink()

	// C: shutdown_hook_add(arch_stop_req, NULL, 1) — sunxi arch_stop_req
	// sets running=0; UILoopStopReq wakes the courier pump.
	arch.ShutdownHookAdd(func(any, int) {
		archsunxi.ArchStopReq()
		arch.UILoopStopReq()
	}, nil, 1)

	// C: signal(SIGTERM, doexit) + signal(SIGINT, doexit)
	// (sunxi_main.c:365-366) — ctrlc flag read by ui_run.
	arch.SetSignalHandler(func(os.Signal) { archsunxi.Doexit() })

	// C: app_shutdown(0) from ui_run on ctrlc (sunxi_main.c:301).
	uiglw.SetSunxiAppShutdown(func(code int) { app_shutdown(ctx, code) })

	arch.Mainloop(uiGlw, nil)

	// C: sunxi_fini() (sunxi_main.c:379) — after ui_run, before
	// main_fini.
	archsunxi.SunxiFini()
}
