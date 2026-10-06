//go:build linux

package ipc

import "golang.org/x/sys/unix"

// Linux termios ioctls (C: TCGETS/TCSETS in stdin.c).
const (
	stdinTermGet = unix.TCGETS
	stdinTermSet = unix.TCSETS
)
