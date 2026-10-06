//go:build linux && !android

package main

// Platform early init — linux_init (linux_misc.c) + the
// add_xdg_paths/process-monitor seams.

import (
	"log"

	arch "github.com/czz/movian-go/internal/arch"
	"github.com/czz/movian-go/internal/networking/connman"
)

// platformState — linux singleton (C: linux.c statics); empty struct
// on platforms without one.
type platformState struct {
	linuxSystem *arch.LinuxSystem
	connman     *connman.System // owned connman instance (rpi+connman only)
}

// platformEarlyStart — C: linux_init() (linux_misc.c:156-161) —
// get_device_id() (eth0 MAC-MD5 → gconf.device_id), linux_trap_init(),
// gconf.concurrency = get_system_concurrency().
func platformEarlyStart(ctx *appContext) {
	ctx.platform.linuxSystem = arch.NewLinuxSystem()
	// C: rpi_main.c main() — platform steps between fpscr and linux_init
	// (revision detect, cap check, bcm_host_init, framerate, vcos log).
	// No-op on non-rpi linux builds.
	platformArchEarlyStart(ctx, ctx.platform.linuxSystem)
	if err := ctx.platform.linuxSystem.Start(); err != nil {
		log.Printf("[MAIN] Warning: linux_init failed: %v", err)
	}
	ctx.gconf.DeviceID = ctx.platform.linuxSystem.DeviceID()
	ctx.gconf.Concurrency = arch.GetSystemConcurrency()
}

// platformProcessMonitorStart — C: INITME(INIT_GROUP_API,
// linux_process_monitor_init).
func platformProcessMonitorStart(ctx *appContext) {
	if ctx.platform.linuxSystem != nil {
		ctx.platform.linuxSystem.ProcessMonitorStart(ctx.propManager)
	}
}

// platformMainStart — C: add_xdg_paths() (linux_main.c:187) — MUSIC/
// PICTURES/VIDEOS services from xdg-user-dir.
func platformMainStart(ctx *appContext) {
	if ctx.platform.linuxSystem != nil {
		ctx.platform.linuxSystem.MainStart(ctx.serviceSystem)
	}
	// C: rpi_main.c main() — omx_init, tv_init, rt-prio warning,
	// rpi_monitor_init registration. No-op on non-rpi linux builds.
	platformArchMainStart(ctx)
	// connman is rpi-only upstream (rpi_main.c:950-954); no-op here.
	platformConnmanStart(ctx)
}
