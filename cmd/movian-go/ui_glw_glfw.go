//go:build (linux || darwin || windows) && glfw

package main

import (
	"github.com/czz/movian-go/internal/arch"
	uiglw "github.com/czz/movian-go/internal/ui/glw"
)

// uiGlfw — GLFW frontend (extension: upstream C has no GLFW/Wayland
// backend). The only GLW UI: X11/GLX (glw_x11.c) was removed, so
// this is the unconditional default (--ui is gone).
var uiGlfw = &arch.LinuxUI{Start: uiglw.GlwGlfwStart, Stop: uiglw.GlwGlfwStop}

// renderLoopGlfw — same shape as renderLoop but the primary UI is the
// GLFW frontend (Wayland or X11 chosen by GLFW at runtime). The main
// thread pumps the UI courier; F12 is a no-op (no second UI).
func renderLoopGlfw(ctx *appContext) {
	arch.UILoopStart(ctx.propManager, ctx.eventManager)
	arch.SubscribeGlobalEventsink()
	arch.ShutdownHookAdd(func(any, int) { arch.UILoopStopReq() }, nil, 1)
	// mainloop() — ui_wanted=&ui_glfw; uiGlw is nil (X11 UI removed).
	arch.Mainloop(uiGlfw, uiGlw)
}
