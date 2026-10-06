//go:build !linux || !mgos

package plugins

// mgosSyncPath — C: arch_sync_path is only called under #ifdef STOS.
func mgosSyncPath(path string) {}
