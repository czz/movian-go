//go:build linux && mgos

package htsmsg

import "github.com/czz/movian-go/internal/arch"

// syncPath — C: arch_sync_path (linux_misc.c:114-122), called under
// #ifdef STOS from persistent_store_sync.
func syncPath(path string) {
	arch.SyncPathLinux(path)
}
