//go:build linux && pulse

package arch

import "time"

// GetAvtime — C: arch_get_avtime (pulseaudio.c:53-58, the CONFIG_LIBPULSE
// variant — pulseaudio.c provides it because alsa_default.c is not
// compiled in a libasound-less build). gettimeofday() wall clock in
// microseconds — unlike the alsa variant this is NOT monotonic.
func GetAvtime() int64 {
	return time.Now().UnixNano() / 1000
}
