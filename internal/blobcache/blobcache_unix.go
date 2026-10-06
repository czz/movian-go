//go:build linux || android || darwin || freebsd || netbsd || openbsd

package blobcache

import "syscall"

// diskAvailBytes — C: the statfs() call in computeMaxSize
// (blobcache_file.c:128-131): Bavail * Bsize.
func diskAvailBytes(path string) (uint64, bool) {
	var stat syscall.Statfs_t
	if syscall.Statfs(path, &stat) != nil {
		return 0, false
	}
	return uint64(stat.Bavail) * uint64(stat.Bsize), true
}
