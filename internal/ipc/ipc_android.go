//go:build android

package ipc

// Android ipc stubs — evdev is unreachable for untrusted apps
// (SELinux denies /dev/input); input arrives via Java/GLW.
// Upstream movian-android never scanned evdev either.

import (
	eventpkg "github.com/czz/movian-go/internal/event"
)

// DeveventStart — /dev/input is SELinux-denied on Android (no-op).
func (i *IPC) DeveventStart(em *eventpkg.EventManager) {}
