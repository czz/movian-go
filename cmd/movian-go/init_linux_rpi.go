//go:build linux && rpi

package main

// init_linux_rpi.go — C: the rpi-specific steps of src/arch/rpi/rpi_main.c
// main() (rpi_main.c:898-948) wired into the generic linux init flow.

import (
	"fmt"
	"os"

	"github.com/czz/movian-go/internal/arch"
	"github.com/czz/movian-go/internal/arch/mgos"
	archrpi "github.com/czz/movian-go/internal/arch/rpi"
	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/networking/connman"
	"github.com/czz/movian-go/internal/notifications"
	uiglw "github.com/czz/movian-go/internal/ui/glw"
)

// platformArchEarlyStart — C: rpi_main.c main() early steps (rpi_main.c:900-919):
// fpscr flush-to-zero, rpi_get_revision(), linux_check_capabilities(),
// bcm_host_init(), rpi_set_display_framerate(60), vcos_set_vlog_impl.
// (C also sigprocmask-blocks SIGALRM — the Go courier needs no alarm,
// see ui_create.) Runs before ctx.platform.linuxSystem.Start() (C's linux_init).
func platformArchEarlyStart(ctx *appContext, ls *arch.LinuxSystem) {
	archrpi.SetFpscrFlushToZero()
	archrpi.RpiGetRevision(ctx.gconf)
	// C: rpi_get_revision writes gconf.device_type (rpi_main.c:183-187)
	ctx.usageCfg.DeviceType = archrpi.DeviceType()
	// C: VPI_REGISTER(rpi_tv_vpi) — rpi_tv.c:230
	vpiHandlers = append(vpiHandlers, archrpi.RpiTvVpi)
	ls.CheckCapabilities() // C: linux_check_capabilities (rpi_main.c:906)
	archrpi.BcmHostStart()
	archrpi.RpiSetDisplayFramerate(60, 0, 0)
	archrpi.InstallVcosLog()
}

// platformArchMainStart — C: rpi_main.c main() steps after main_init()
// (rpi_main.c:920-943): kill_framebuffer, omx_init, tv_init, the
// CAP_SYS_NICE warning, and INITME(INIT_GROUP_API, rpi_monitor_init).
func platformArchMainStart(ctx *appContext) {
	// C: if(gconf.shell_fd != -1) kill_framebuffer();
	if ctx.gconf.ShellFD != -1 {
		archrpi.KillFramebuffer()
	}

	// C: omx_init() — registers pipe extras on the media system
	// (created earlier in wireMedia; Core() is set by Start()).
	var ms *mediacore.MediaSystem
	if ctx.mediaSystem != nil {
		ms = ctx.mediaSystem.Core()
	}
	archrpi.OmxStart(ms)

	// C: tv_init()
	archrpi.TvStart()

	// C: rpi_main.c:931-942 — realtime-scheduling warning when the
	// process lacks CAP_SYS_NICE.
	if !ctx.platform.linuxSystem.SetThreadPriorities() {
		if ctx.notifMgr != nil {
			ctx.notifMgr.NotifyAdd(nil, notifications.NotifyWarning, "", 10,
				"%s", fmt.Sprintf("Movian Go runs without realtime scheduling on your Raspberry Pi\n"+
					"This may impact performance during video playback.\n"+
					"You have been warned! Please set SYS_CAP_NICE:\n"+
					"  sudo setcap 'cap_sys_nice+ep %s'", os.Args[0]))
		}
	}

	// C: INITME(INIT_GROUP_API, rpi_monitor_init, NULL, 0)
	ctx.initGroupSystem.RegisterInitHelper(&arch.InitHelper{
		Group: arch.InitGroupAPI,
		Prio:  0,
		Start: func() { archrpi.RpiMonitorStart(ctx.propManager, ctx.calloutSystem, ctx.traceSystem) },
	})

	// C: INITME(INIT_GROUP_IPC, rpi_tv_init, NULL, 10) — registers the
	// rpi_tv VPI handler and the "Match display and content framerate"
	// setting under settings:tv.
	ctx.initGroupSystem.RegisterInitHelper(&arch.InitHelper{
		Group: arch.InitGroupIPC,
		Prio:  10,
		Start: func() { archrpi.RpiTvStart(ctx.settingsManager) },
	})

	// C: swrefresh() (main.c:321-329) — pokes the swthread to recheck
	// for upgrades; invoked from rpi_mainloop on every UI (re)start.
	uiglw.SetSwrefresh(func() {
		select {
		case swrefreshKick <- struct{}{}:
		default:
		}
	})

	// C: rpi_main.c:950-954 — connman uses dbus (via glib); under
	// #if ENABLE_CONNMAN. In Go the `connman` build tag selects the
	// real implementation; without it Start is a no-op stub.
	ctx.platform.connman = connman.Start(ctx.propManager, ctx.settingsManager, ctx.backendSystem, ctx.notifMgr)

	// C: stos_stop_splash() (rpi_main.c:957) — SIGINT the bootsplash
	//    process (via /var/run/mgos-splash.pid) before rpi_mainloop.
	mgos.StopSplash()

}
