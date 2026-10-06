//go:build linux

package arch

// C: mallinfo() (malloc.h) — used by meminfo_do in
// linux_process_monitor.c. Go's runtime.MemStats cannot provide the
// glibc allocator counters, so the canonical values come via cgo.

/*
#cgo CFLAGS: -Wno-deprecated-declarations
#include <malloc.h>
*/
import "C"
import "unsafe"

// MallInfo — C: struct mallinfo fields used by meminfo_do.
type MallInfo struct {
	Arena    int // C: mi.arena — non-mmapped space (bytes)
	Hblks    int // C: mi.hblks — number of mmapped regions
	Ordblks  int // C: mi.ordblks — number of free chunks
	Uordblks int // C: mi.uordblks — total allocated space
	Fordblks int // C: mi.fordblks — total free space
}

// GetMallInfo — C: mallinfo()
func GetMallInfo() MallInfo {
	mi := C.mallinfo()
	return MallInfo{
		Arena:    int(mi.arena),
		Hblks:    int(mi.hblks),
		Ordblks:  int(mi.ordblks),
		Uordblks: int(mi.uordblks),
		Fordblks: int(mi.fordblks),
	}
}

// MallocSizeLinux — C: arch_malloc_size (linux_misc.c:167-172).
// malloc_usable_size() — used by ecmascript's ec_mem_active accounting.
func MallocSizeLinux(ptr unsafe.Pointer) uintptr {
	return uintptr(C.malloc_usable_size(ptr))
}
