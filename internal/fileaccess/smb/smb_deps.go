// Canonical port of src/fileaccess/smb/fa_nativesmb.c — native SMBv1
// (CIFS) client. Architecture: a global pool of cifs_connection_t, each
// owning a tcpcon and a dispatch goroutine that demultiplexes replies by
// MID onto a pending-request list guarded by smb_global_mutex; per-share
// cifs_tree_t with cond-signaled connect; DCE/RPC share enumeration;
// pipelined READ_ANDX; 30s SMB_ECHO keepalive with auto-disconnect.
package smb

import (
	"github.com/czz/movian-go/internal/callout"
	facore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/gconf"
	"github.com/czz/movian-go/internal/trace"
)

// SetGettext wires _() (C: nls.h). Provider: pkg/nls via the app layer.
func (sys *System) SetGettext(fn func(string) string) { sys.gettextFn = fn }

// SetUsageEvent wires usage_event (C: usage.c direct call).
func (sys *System) SetUsageEvent(fn func(key string, count int)) { sys.usageEvent = fn }

// SetCalloutSystem injects the callout system (C: callout_* globals
// used by cifsPeriodic/cifs connection keepalive). Start-time wiring.
func (sys *System) SetCalloutSystem(cs *callout.CalloutSystem) { sys.cs = cs }

// SetTraceSystem injects the trace system (C: trace() global).
func (sys *System) SetTraceSystem(ts *trace.TraceSystem) { sys.ts = ts }

// SetKeyringLookup wires keyring_lookup (C: direct call from smb auth).
func (sys *System) SetKeyringLookup(fn func(id string, username, password, domain *string,
	rememberMe *int, source, reason string, flags int) int) {
	sys.kr = fn
}

func (sys *System) gettext(s string) string {
	if sys.gettextFn != nil {
		return sys.gettextFn(s)
	}
	return s
}

func (sys *System) smbTrace(format string, args ...any) {
	if sys.gconf == nil {
		sys.gconf = gconf.New()
	}
	if sys.gconf.EnableSMBDebug.Load() {
		sys.ts.Trace(trace.TRACE_DEBUG, "SMB", format, args...)
	}
}

// NewSystem — allocates the SMB subsystem and registers the canonical
// smb fap ops into fileaccess/core (C: FAP_REGISTER(smb)).
func NewSystem() *System {
	sys := &System{}
	facore.SetSMBOps(smbFAP{sys: sys})
	return sys
}

// SetGconf injects the process gconf (C: gconf_t — owned by main).
func (sys *System) SetGconf(g *gconf.T) { sys.gconf = g }
