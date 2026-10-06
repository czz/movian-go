//go:build windows

package blobcache

import (
	"syscall"
	"unsafe"
)

var (
	bcKernel32                = syscall.NewLazyDLL("kernel32.dll")
	bcProcGetDiskFreeSpaceExW = bcKernel32.NewProc("GetDiskFreeSpaceExW")
)

// diskAvailBytes — windows counterpart of the statfs() call in
// computeMaxSize (blobcache_file.c:128-131): free bytes available to
// the caller via GetDiskFreeSpaceExW. No upstream C counterpart
// (upstream has no win32 arch); modeled on protocols_windows.go.
func diskAvailBytes(path string) (uint64, bool) {
	var freeBytes, totalBytes, totalFreeBytes uint64
	ret, _, _ := bcProcGetDiskFreeSpaceExW.Call(
		uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(path))),
		uintptr(unsafe.Pointer(&freeBytes)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFreeBytes)),
	)
	if ret == 0 {
		return 0, false
	}
	return freeBytes, true
}
