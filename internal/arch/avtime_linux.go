//go:build linux && !pulse && !rpi

package arch

import "golang.org/x/sys/unix"

// GetAvtime — C: arch_get_avtime (alsa_default.c:28-33, the
// CONFIG_LIBASOUND variant). CLOCK_MONOTONIC in microseconds — used by
// the audio driver's clock anchoring and glw frame timing.
func GetAvtime() int64 {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts); err != nil {
		return GetTS()
	}
	return int64(ts.Sec)*1000000 + int64(ts.Nsec)/1000
}
