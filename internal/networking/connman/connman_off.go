//go:build !linux || !rpi || !connman

package connman

// connman_off.go — stub for builds without the `connman` tag
// (C: #if ENABLE_CONNMAN gates connman_init in rpi_main.c:950-955).

import (
	backendcore "github.com/czz/movian-go/internal/backend/core"
	"github.com/czz/movian-go/internal/notifications"
	propcore "github.com/czz/movian-go/internal/prop"
	settings "github.com/czz/movian-go/internal/settings"
)

// System — empty handle for the disabled variant (signature parity
// with the godbus/cgo builds).
type System struct{}

// Start — no-op when CONNMAN is disabled (C: the connman_init() call
// in rpi_main.c main() is compiled out).
func Start(propMgr *propcore.PropManager,
	settingsMgr *settings.SettingsManager,
	backendSys *backendcore.BackendSystem,
	notifMgr *notifications.NotificationManager) *System {
	return nil
}

// Stop — no-op when CONNMAN is disabled (nil-safe signature parity).
func (s *System) Stop() {}
