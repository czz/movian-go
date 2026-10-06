//go:build linux && mgos

package plugins

import "github.com/czz/movian-go/internal/arch"

// mgosSyncPath — C: arch_sync_path (linux_misc.c:114), called under
// #ifdef STOS after writing the installed plugin archive (plugins.c:1610).
func mgosSyncPath(path string) {
	arch.SyncPathLinux(path)
}
