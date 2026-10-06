//go:build windows

package fileaccess

import (
	"syscall"
	"unsafe"
)

var (
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	procGetDiskFreeSpaceExW = kernel32.NewProc("GetDiskFreeSpaceExW")
)

// FSInfo returns filesystem information for Windows systems
func (p *FSProtocol) FSInfo(url string) (*FileSystemInfo, error) {
	path := p.urlToPath(url)

	// Convert to UNC path if needed
	if len(path) >= 2 && path[1] == ':' {
		path = path[:2] + "\\" // Ensure it has backslash
	}

	var freeBytes, totalBytes, totalFreeBytes uint64

	// Call GetDiskFreeSpaceExW
	ret, _, err := procGetDiskFreeSpaceExW.Call(
		uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(path))),
		uintptr(unsafe.Pointer(&freeBytes)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFreeBytes)),
	)

	if ret == 0 {
		return nil, err
	}

	return &FileSystemInfo{
		Size:  totalBytes,
		Avail: freeBytes,
	}, nil
}
