//go:build !windows

package lan

import (
	"net"
	"net/netip"
	"syscall"
)

// setMulticastIfAddr sets IP_MULTICAST_IF to the interface owning addr.
func setMulticastIfAddr(conn *net.UDPConn, addr netip.Addr) error {
	rc, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var serr error
	err = rc.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInet4Addr(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_IF, addr.As4())
	})
	if err != nil {
		return err
	}
	return serr
}
