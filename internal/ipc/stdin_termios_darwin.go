//go:build darwin

package ipc

import "golang.org/x/sys/unix"

// Darwin termios ioctls — x/sys/unix has no TCGETS/TCSETS aliases on
// macOS; the equivalents are TIOCGETA/TIOCSETA.
const (
	stdinTermGet = unix.TIOCGETA
	stdinTermSet = unix.TIOCSETA
)
