//go:build windows

package arch

/*
#include <windows.h>
#include <stdint.h>

static int64_t ml_avtime_us(void) {
    static LARGE_INTEGER freq;
    LARGE_INTEGER now;
    if(freq.QuadPart == 0)
        QueryPerformanceFrequency(&freq);
    QueryPerformanceCounter(&now);
    return now.QuadPart * 1000000LL / freq.QuadPart;
}
*/
import "C"

// GetAvtime — C: arch_get_avtime. No upstream Windows counterpart
// (upstream never shipped a win32 arch); modeled on the osx
// mach_absolute_time port — QPC is the Windows equivalent.
func GetAvtime() int64 {
	return int64(C.ml_avtime_us())
}
