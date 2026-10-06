//go:build !(linux && rpi) && !(linux && sunxi)

package main

import (
	"github.com/czz/movian-go/internal/arch"
)

// uiGlw — nil: the canonical X11/GLX frontend (glw_x11.c) is removed;
// the GLFW backend is the only GLW UI. The variable stays because
// ui_glw_glfw.go passes it to arch.Mainloop for the (dead) F12 swap.
var uiGlw *arch.LinuxUI

// renderLoop — C: linux_main.c main() tail. When the GLFW frontend
// is built in it is the only GLW backend and takes the default UI
// slot; otherwise this build runs headless: the mainloop just pumps
// the UI courier until shutdown.
func renderLoop(ctx *appContext) {
	if uiGlfw != nil {
		renderLoopGlfw(ctx)
		return
	}
	arch.UILoopStart(ctx.propManager, ctx.eventManager)
	arch.SubscribeGlobalEventsink()
	// C: linux_main.c:139-140 — shutdown_hook_add(arch_stop_req,
	// NULL, 1) stops the mainloop on shutdown.
	arch.ShutdownHookAdd(func(any, int) { arch.UILoopStopReq() }, nil, 1)
	// C: nav_eventsink runs on the GLOBAL prop dispatch — events
	// (initial URL, NAV_*, OPENURL) dispatch without any UI. In Go the
	// nav courier is render-thread pumped, so headless needs a worker.
	go func() {
		for {
			select {
			case <-ctx.stopChan:
				return
			default:
			}
			c := ctx.navSystem.NavCourier()
			if c == nil {
				return
			}
			c.WaitTimed(500)
		}
	}()
	// C: mainloop() — no UI registered: courier pump only.
	arch.Mainloop(nil, nil)
}
