//go:build !darwin

package arch

import (
	propcore "github.com/czz/movian-go/internal/prop"
)

// Non-darwin stubs — the real implementations live in darwin.go
// (mach cpu/mem monitor, IOPlatformUUID device id). Compiled out on
// every other OS; the call sites are runtime.GOOS-gated anyway.

// DarwinDeviceID — C: get_device_id (osx_app.m:72-90) — darwin only.
func DarwinDeviceID() string { return "" }

// DarwinStartCpuMonitor — C: darwin_init_cpu_monitor (darwin.c:140-181)
// — darwin only.
func DarwinStartCpuMonitor(pm *propcore.PropManager) {}
