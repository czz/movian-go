//go:build linux && glfw && !x11

package glw

// No-op stubs for a build without the x11 tag (pure Wayland):
// libX11 is not linked, GLFW_PLATFORM_X11 can never be selected at
// runtime, so these are unreachable — they exist only to satisfy the
// call sites in glw_glfw.go.

import "unsafe"

func glfwX11Grab(win unsafe.Pointer) int { return 0 }
func glfwX11Ungrab(win unsafe.Pointer)   {}
func glfwX11SsReset(win unsafe.Pointer)  {}
