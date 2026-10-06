//go:build windows

package main

// Platform early init — NO upstream C counterpart (no win32 arch
// upstream); modeled on the osx branch: MachineGuid → MD5 →
// gconf.device_id, NumCPU → gconf.concurrency.

import (
	"os"
	"path/filepath"

	arch "github.com/czz/movian-go/internal/arch"
	service "github.com/czz/movian-go/internal/service"
)

// platformState — no platform singleton on windows.
type platformState struct{}

func platformEarlyStart(ctx *appContext) {
	ctx.gconf.DeviceID = arch.WindowsDeviceID()
	ctx.gconf.Concurrency = arch.GetSystemConcurrency()
}

// platformProcessMonitorStart — no windows process monitor yet.
func platformProcessMonitorStart(ctx *appContext) {}

// platformMainStart — the windows counterpart of add_xdg_paths():
// %USERPROFILE%\Music/Pictures/Videos as managed services.
func platformMainStart(ctx *appContext) {
	if ctx.serviceSystem == nil {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return
	}
	for _, m := range [][3]string{
		{"Music", "music", "Music"},
		{"Pictures", "photos", "Pictures"},
		{"Videos", "video", "Videos"},
	} {
		path := filepath.Join(home, m[0])
		if st, err := os.Stat(path); err != nil || !st.IsDir() {
			continue
		}
		ctx.serviceSystem.ServiceCreateManaged("user-dir-"+m[0], m[2],
			path, m[1], "", false, true, service.SvcOriginSystem)
	}
}
