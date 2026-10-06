//go:build !mgos

package mgos

import (
	"github.com/czz/movian-go/internal/arch"
	"github.com/czz/movian-go/internal/service"
)

// Enabled — C: #if STOS / #ifdef STOS.
const Enabled = false

// StopSplash — C: stos_stop_splash (rpi_main.c:749). No-op without mgos.
func StopSplash() {}

// RegisterInit — C: INITME(INIT_GROUP_IPC, stos_automount_start,
// stos_automount_stop, 0). No-op without mgos.
func RegisterInit(igs *arch.InitGroupSystem, ss *service.ServiceSystem) {}
