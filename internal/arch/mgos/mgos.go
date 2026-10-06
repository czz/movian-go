//go:build linux && mgos

package mgos

import (
	"os"
	"strconv"
	"syscall"
	"time"

	"github.com/czz/movian-go/internal/arch"
	"github.com/czz/movian-go/internal/service"
	"github.com/czz/movian-go/internal/trace"
)

// mgosDeps — injected deps for the mgos package (C's implicit
// service registry global).
var mgosDeps struct{ ss *service.ServiceSystem }

// StopSplash — C: stos_stop_splash (rpi_main.c:748-770). Reads the
// bootsplash pidfile, SIGINTs the process, then polls up to ~1s for the
// pidfile to disappear.
func StopSplash() {
	data, err := os.ReadFile(SplashPidFile)
	if err != nil {
		return
	}
	val, err := strconv.Atoi(string(trimSpace(data)))
	if err != nil {
		return
	}
	mgosTS.ts.Trace(trace.TRACE_DEBUG, "STOS",
		"Asking mgos-splash (pid: %d) to stop", val)
	syscall.Kill(val, syscall.SIGINT)

	for i := 0; i < 100; i++ {
		if _, err := os.Stat(SplashPidFile); err != nil {
			mgosTS.ts.Trace(trace.TRACE_DEBUG, "STOS", "mgos-splash is gone")
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	mgosTS.ts.Trace(trace.TRACE_ERROR, "STOS", "mgos-splash fails to terminate")
}

// trimSpace — fscanf("%d") skips leading whitespace and stops at the
// first non-digit; emulate with a manual prefix scan.
func trimSpace(b []byte) []byte {
	i := 0
	for i < len(b) && (b[i] == ' ' || b[i] == '\t' || b[i] == '\n' || b[i] == '\r') {
		i++
	}
	j := i
	for j < len(b) && b[j] >= '0' && b[j] <= '9' {
		j++
	}
	return b[i:j]
}

// RegisterInit — C: INITME(INIT_GROUP_IPC, stos_automount_start,
// stos_automount_stop, 0) (stos_automount.c:478).
func RegisterInit(igs *arch.InitGroupSystem, ss *service.ServiceSystem) {
	mgosDeps.ss = ss
	igs.RegisterInitHelper(&arch.InitHelper{
		Group: arch.InitGroupIPC,
		Prio:  0,
		Start: automountStart,
		Fini:  automountStop,
	})
}
