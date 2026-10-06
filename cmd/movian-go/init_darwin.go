//go:build darwin

package main

// Platform early init — osx_app.m main(): get_device_id()
// (IOPlatformUUID → MD5 → hex) + gconf.concurrency (sysctl HW_NCPU;
// NumCPU equivalent). Upstream has no darwin trap init.

import (
	arch "github.com/czz/movian-go/internal/arch"
)

// platformState — no platform singleton on darwin.
type platformState struct{}

func platformEarlyStart(ctx *appContext) {
	ctx.gconf.DeviceID = arch.DarwinDeviceID()
	ctx.gconf.Concurrency = arch.GetSystemConcurrency()
}

// platformProcessMonitorStart — C: INITME(INIT_GROUP_API,
// darwin_init_cpu_monitor).
func platformProcessMonitorStart(ctx *appContext) {
	arch.DarwinStartCpuMonitor(ctx.propManager)
}

// platformMainStart — upstream osx registers no per-user media-dir
// services at this point (XDG is linux-only).
func platformMainStart(ctx *appContext) {}
