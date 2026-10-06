//go:build linux && sunxi

package main

// init_linux_sunxi.go — C: the sunxi-specific steps of
// src/arch/sunxi/sunxi_main.c main() wired into the generic linux init
// flow.

import (
	"fmt"

	"github.com/czz/movian-go/internal/arch"
	archsunxi "github.com/czz/movian-go/internal/arch/sunxi"
)

// platformArchEarlyStart — C: sunxi_main.c main() early step
// (sunxi_main.c:342): linux_check_capabilities() — runs inside
// ctx.platform.linuxSystem.Start() on generic linux; the rpi port calls it
// explicitly. Runs before ctx.platform.linuxSystem.Start().
func platformArchEarlyStart(ctx *appContext, ls *arch.LinuxSystem) {
	ls.CheckCapabilities() // C: linux_check_capabilities()
}

// platformArchMainStart — C: sunxi_main.c main() (sunxi_main.c:350-372):
// sunxi_init() after main_init/trap_init, then the
// posix_set_thread_priorities check.
func platformArchMainStart(ctx *appContext) {
	// C: sunxi_init() (sunxi_main.c:354) — /dev/disp + /dev/fb0 +
	// /dev/cedar_dev, gfxmem tlsf pool, VE clock/reset, bg layer.
	archsunxi.SunxiStart(ctx.traceSystem)

	// C: sunxi_main.c:370-372 — posix_set_thread_priorities check.
	if !ctx.platform.linuxSystem.SetThreadPriorities() {
		fmt.Println("tut prio error WAT?!")
	}

	// backend_imageloader dep for the (dead-code) sunxi_set_bg body.
	archsunxi.SetBackendSystem(ctx.backendSystem)

}
