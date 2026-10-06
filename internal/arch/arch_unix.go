//go:build linux || android || darwin || freebsd || netbsd || openbsd

package arch

import (
	"syscall"
)

// Pipe creates a pipe
func Pipe() ([]int, error) {
	fds := make([]int, 2)
	err := syscall.Pipe(fds)
	return fds, err
}

// GetUID returns the user ID
func GetUID() int {
	return syscall.Getuid()
}

// GetGID returns the group ID
func GetGID() int {
	return syscall.Getgid()
}

// Setrlimit sets resource limits
func Setrlimit(resource int, rlim *syscall.Rlimit) error {
	return syscall.Setrlimit(resource, rlim)
}

// Getrlimit gets resource limits
func Getrlimit(resource int, rlim *syscall.Rlimit) error {
	return syscall.Getrlimit(resource, rlim)
}

// SetMemoryLimit sets the memory limit
func SetMemoryLimit(limit int64) error {
	var rlim syscall.Rlimit
	rlim.Cur = uint64(limit)
	rlim.Max = uint64(limit)
	return Setrlimit(syscall.RLIMIT_AS, &rlim)
}

// SetDataLimit sets the data segment limit
func SetDataLimit(limit int64) error {
	var rlim syscall.Rlimit
	rlim.Cur = uint64(limit)
	rlim.Max = uint64(limit)
	return Setrlimit(syscall.RLIMIT_DATA, &rlim)
}

// Mmap maps a file into memory
func Mmap(fd int, offset int64, length int, prot int, flags int) ([]byte, error) {
	return syscall.Mmap(fd, offset, length, prot, flags)
}

// Munmap unmaps a memory region
func Munmap(b []byte) error {
	return syscall.Munmap(b)
}

// Mprotect changes memory protection
func Mprotect(b []byte, prot int) error {
	return syscall.Mprotect(b, prot)
}
