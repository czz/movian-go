//go:build windows

package asyncio

// Socket compatibility seam for Windows. NO upstream C counterpart
// (upstream Movian never shipped win32 asyncio; asyncio_posix.c is
// unix-only). Post-migration only WSAStartup and UDP pre-bind sockopts
// remain: all sockets are Go-native and the WSAPoll/wakeup-pipe
// machinery was removed along with the event loop's fd plumbing.

import (
	"syscall"

	"golang.org/x/sys/windows"
)

func sysSetup() error {
	// WSAStartup(2.2) — refcounted, cheap to call repeatedly.
	var d windows.WSAData
	return windows.WSAStartup(0x202, &d)
}

// udpSockopts applies the C socket options before bind via
// net.ListenConfig.Control. SO_REUSEPORT does not exist on Windows —
// SO_REUSEADDR is the closest match.
func udpSockopts(broadcast bool) func(_, _ string,
	c syscall.RawConn) error {
	return func(_, _ string, c syscall.RawConn) error {
		var serr error
		c.Control(func(fd uintptr) {
			windows.SetsockoptInt(windows.Handle(fd),
				windows.SOL_SOCKET, windows.SO_REUSEADDR, 1)
			if broadcast {
				if e := windows.SetsockoptInt(windows.Handle(fd),
					windows.SOL_SOCKET, windows.SO_BROADCAST, 1); e != nil {
					serr = e
				}
			}
		})
		return serr
	}
}
