//go:build !linux || !mgos

package htsmsg

// syncPath — C: arch_sync_path is only called under #ifdef STOS.
func syncPath(path string) {}
