//go:build !windows

package asyncio

// Socket compatibility seam for unix platforms. Post-migration only
// UDP pre-bind sockopts remain: all sockets are Go-native and the
// poll()/wakeup-pipe machinery (sysPoll, raw-fd read/write/close,
// getsockopt) was removed along with the event loop's fd plumbing.

import (
	"syscall"

	"golang.org/x/sys/unix"
)

func sysSetup() error { return nil }

// udpSockopts applies the C socket options (REUSEADDR/REUSEPORT/BROADCAST)
// before bind via net.ListenConfig.Control — same ordering as the C
// asyncio_udp_bind_socket setsockopt sequence.
func udpSockopts(broadcast bool) func(_, _ string,
	c syscall.RawConn) error {
	return func(_, _ string, c syscall.RawConn) error {
		var serr error
		c.Control(func(fd uintptr) {
			unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
			unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
			if broadcast {
				if e := unix.SetsockoptInt(int(fd), unix.SOL_SOCKET,
					unix.SO_BROADCAST, 1); e != nil {
					serr = e
				}
			}
		})
		return serr
	}
}
