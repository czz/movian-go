//go:build linux && rpi

package arch

import "golang.org/x/sys/unix"

// GetAvtime — C: arch_get_avtime (rpi_main.c:72-79). The RPi platform
// overrides the desktop implementation: gettimeofday (wall clock) in
// microseconds — NOT CLOCK_MONOTONIC. OMX media timestamps on the
// VideoCore align with this clock.
func GetAvtime() int64 {
	var tv unix.Timeval
	if err := unix.Gettimeofday(&tv); err != nil {
		return GetTS()
	}
	return int64(tv.Sec)*1000000 + int64(tv.Usec)
}
