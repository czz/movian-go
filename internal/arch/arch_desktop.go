//go:build !android

package arch

import (
	"fmt"
	"os"
	"runtime"
	"time"
)

// TraceArch outputs a trace message with architecture-specific formatting
func TraceArch(level int, prefix, str string) {
	var sgr, sgroff string

	switch level {
	case 0: // TraceEmerg
		sgr = "\033[31m"
	case 1: // TRACE_ERROR
		sgr = "\033[31m"
	case 2: // TRACE_INFO
		sgr = "\033[33m"
	case 3: // TRACE_DEBUG
		sgr = "\033[32m"
	default:
		sgr = "\033[35m"
	}

	// Check if stderr is a TTY
	if isatty := isTerminal(2); !isatty {
		sgr = ""
		sgroff = ""
	} else {
		sgroff = "\033[0m"
	}

	tv := time.Now()
	tm := tv.Local()
	// C: trace_arch writes to stderr directly. It is invoked from
	// tracev() while trace_mutex is held — calling ts.Debug() here
	// would re-enter Tracev and self-deadlock on the same mutex.
	fmt.Fprintf(os.Stderr, "%s%02d:%02d:%02d.%03d: %s %s%s\n",
		sgr,
		tm.Hour(),
		tm.Minute(),
		tm.Second(),
		tv.Nanosecond()/1000000,
		prefix, str, sgroff)
}

// isTerminal checks if a file descriptor is a terminal (fallback for non-Unix platforms)
func isTerminal(fd int) bool {
	// Fallback implementation for platforms without ioctl
	return false
}

// GetSystemType returns the system type string
func GetSystemType() string {
	osName := runtime.GOOS
	archName := runtime.GOARCH

	switch osName {
	case "linux":
		return fmt.Sprintf("Linux/%s", archName)
	case "darwin":
		// C: arch_get_system_type (osx_app.m:257) returns "Apple"
		return "Apple"
	case "windows":
		return fmt.Sprintf("Windows/%s", archName)
	case "android":
		return fmt.Sprintf("Android/%s", archName)
	default:
		return fmt.Sprintf("%s/%s", osName, archName)
	}
}
