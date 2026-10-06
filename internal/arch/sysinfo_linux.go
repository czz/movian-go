//go:build linux

package arch

import (
	"syscall"
)

// GetTotalMemory returns the total system memory
func GetTotalMemory() uint64 {
	var info syscall.Sysinfo_t
	err := syscall.Sysinfo(&info)
	if err != nil {
		return 0
	}
	return uint64(info.Totalram)
}

// GetFreeMemory returns the free system memory
func GetFreeMemory() uint64 {
	var info syscall.Sysinfo_t
	err := syscall.Sysinfo(&info)
	if err != nil {
		return 0
	}
	return uint64(info.Freeram)
}

// GetUptime returns the system uptime in seconds
func GetUptime() uint64 {
	var info syscall.Sysinfo_t
	err := syscall.Sysinfo(&info)
	if err != nil {
		return 0
	}
	return uint64(info.Uptime)
}

// GetLoadAverage returns the system load average
func GetLoadAverage() (float64, float64, float64) {
	var info syscall.Sysinfo_t
	err := syscall.Sysinfo(&info)
	if err != nil {
		return 0, 0, 0
	}
	return float64(info.Loads[0]) / 65536.0, float64(info.Loads[1]) / 65536.0, float64(info.Loads[2]) / 65536.0
}
