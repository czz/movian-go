//go:build !libcec

package ipc

import (
	eventpkg "github.com/czz/movian-go/internal/event"
	settings "github.com/czz/movian-go/internal/settings"
	"github.com/czz/movian-go/internal/task"
)

// cecIPC — C: libcec.c statics; empty without CONFIG_LIBCEC so IPC.cec
// compiles on every build.
type cecIPC struct{}

// initPlatform — nothing to wire without CONFIG_LIBCEC;
// IPC.LibcecVpiHandler stays nil and is skipped by the init wiring.
func (i *IPC) initPlatform() {}

// LibcecEnabled — CONFIG_LIBCEC is off on this build.
const LibcecEnabled = false

// C: INITME(INIT_GROUP_IPC, libcec_init, libcec_fini, 10) does not exist
// when CONFIG_LIBCEC is disabled — the calls compile to nothing.
func (i *IPC) LibcecStart(em *eventpkg.EventManager, sm *settings.SettingsManager, tasks *task.TaskSystem) {
}

// LibcecFini — C: libcec_fini (INIT_GROUP_IPC fini slot, absent without
// CONFIG_LIBCEC).
func (i *IPC) LibcecFini() {}
