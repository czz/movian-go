//go:build android

package arch

// Canonical port of src/arch/android/android.c (non-JNI parts) and
// android.h. The JNI plumbing and globals live in android_jni.go;
// the exported JNI entry points live in cmd/movian-go/android_jni.go.

import (
	"os/signal"
	"syscall"
	"time"
)

// AndroidInfo — C: the android_manufacturer/model/name/version/
// serialno/sysver string block (android.c:47-52) bundled for callers
// that want the device description as a unit.
type AndroidInfo struct {
	Manufacturer string
	Model        string
	Name         string
	Version      string
	Serialno     string
	SDK          int
}

// NewAndroidInfo — C: the sysprop reads in JNI_OnLoad
// (android.c:231-243) as a snapshot.
func NewAndroidInfo() *AndroidInfo {
	return &AndroidInfo{
		Manufacturer: AndroidManufacturer,
		Model:        AndroidModel,
		Name:         AndroidName,
		Version:      AndroidVersion,
		Serialno:     AndroidSerialno,
		SDK:          AndroidSDK,
	}
}

// CacheAvail — C: cache_avail (android.c:82-91): statfs on the cache
// directory, bavail * bsize.
func CacheAvail(cachePath string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(cachePath, &st); err != nil {
		return 0
	}
	return int64(st.Bavail) * int64(st.Bsize)
}

// GetSystemType — C: android_get_systemtype (android.c:97-100).
func GetSystemType() string {
	return "Android"
}

// StopReq — C: android_stop_req (android.c:259-262): on Android the
// caller must always handle the stop request — there is no OS-level
// quit path.
func StopReq() int {
	return ArchStopCallerMustHandle
}

// ArchStopCallerMustHandle — C: ARCH_STOP_CALLER_MUST_HANDLE
// (arch.h). Kept local to avoid depending on a linux-named const.
const ArchStopCallerMustHandle = 1

// MyLocaltime — C: my_localtime (android.c:112-115): localtime_r.
func MyLocaltime(t time.Time) time.Time {
	return t.Local()
}

// AndroidArchStart — C: the arch_init() parts of JNI_OnLoad
// (android.c:248-252): signal(SIGPIPE, SIG_IGN). Go's runtime already
// ignores SIGPIPE for non-stdout/stderr fds; signal.Ignore mirrors
// the C intent exactly.
func AndroidArchStart() {
	signal.Ignore(syscall.SIGPIPE)
}

// Halloc/Hfree — C: halloc/hfree (android.c:120-141): mmap/munmap
// backed allocation. arch.go's generic Halloc already wraps
// syscall.Mmap on unix — on android syscall.Mmap is present, so the
// generic implementation is the canonical one. No override needed.

// MallocSize — C: malloc_usable_size path (android.c:145-150):
// dlmalloc has no usable-size query; the generic arch.go
// implementation already returns 0, which is the canonical result.
