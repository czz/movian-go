//go:build !linux || !mgos

package arch

// mgosGetDist — C: stos_get_dist exists only under #ifdef STOS.
func mgosGetDist() string { return "" }
