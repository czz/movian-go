//go:build !cef

package glw

// glw_websurface_off.go — no embedded webview: the root lifecycle
// hooks are no-ops.
func websurfaceInit(gr *glwRoot) {}
func websurfaceFini(gr *glwRoot) {}
