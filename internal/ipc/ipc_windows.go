//go:build windows

package ipc

// Windows ipc stubs — devevent/lirc/stdin are unix-only subsystems
// (evdev, lircd socket, termios). Upstream never shipped win32 ipc;
// GLFW already delivers keyboard/pointer input on this platform.

import (
	eventpkg "github.com/czz/movian-go/internal/event"
)

// DeveventStart — no /dev/input on windows (no-op).
func (i *IPC) DeveventStart(em *eventpkg.EventManager) {}

// LircOpen — no lircd on windows (no-op).
func (i *IPC) LircOpen(em *eventpkg.EventManager) {}

// StdinStart — no termios on windows (no-op).
func (i *IPC) StdinStart(em *eventpkg.EventManager, listenOnStdin bool) {}
