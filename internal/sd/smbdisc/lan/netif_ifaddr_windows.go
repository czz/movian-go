//go:build windows

package lan

import (
	"errors"
	"net"
	"net/netip"
)

// setMulticastIfAddr is only reached when Interface.Index is 0, which
// the netlink-less fallback produces; Windows never takes that path.
func setMulticastIfAddr(*net.UDPConn, netip.Addr) error {
	return errors.New("multicast interface by address: not supported")
}
