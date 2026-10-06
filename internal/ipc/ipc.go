package ipc

import (
	"github.com/czz/movian-go/internal/gconf"
	"github.com/czz/movian-go/internal/trace"
	"github.com/czz/movian-go/internal/video"
)

// IPC owns the ipc subsystem (C: the static globals of devevent.c /
// lirc.c / stdin.c / libcec.c plus the injected gconf/trace deps).
// Constructed once by main and passed to consumers — no package
// globals.
type IPC struct {
	gcfg *gconf.T           // C: gconf_t gconf
	ts   *trace.TraceSystem // C: trace() global
	cec  *cecIPC            // C: libcec state (libcec builds only)

	// LibcecVpiHandler — C: VPI_REGISTER(libcec_vpi) (libcec.c:409).
	// Nil on !libcec builds; cmd/movian-go init appends it to vpiHandlers.
	LibcecVpiHandler video.VPIHandler
}

// New constructs the IPC subsystem with its injected dependencies
// (C: gconf_t gconf and trace() — owned by main).
func New(g *gconf.T, ts *trace.TraceSystem) *IPC {
	i := &IPC{gcfg: g, ts: ts}
	i.initPlatform()
	return i
}

// SetEnableCecDebug — C: gconf.enable_cec_debug written by the
// "cecdebug" dev bool (settings.c).
func (i *IPC) SetEnableCecDebug(b bool) { i.gcfg.EnableCecDebug.Store(b) }
