//go:build darwin

package ipc

// macOS ipc stubs — devevent is evdev-specific (/dev/input/event*).
// lirc and stdin already compile on darwin; GLFW delivers
// keyboard/pointer input regardless.

import (
	eventpkg "github.com/czz/movian-go/internal/event"
)

// DeveventStart — no /dev/input on macOS (no-op).
func (i *IPC) DeveventStart(em *eventpkg.EventManager) {}
