package lan

import (
	"errors"
	"fmt"
	"net"
	"net/netip"

	"golang.org/x/net/ipv4"
)

// Interface is a network interface the engine sends its Probes
// through.
type Interface struct {
	Name  string
	Index int        // as in net.Interface
	Addr  netip.Addr // the IPv4 address the Probe socket binds
}

// Interfaces lists the interfaces a Probe reaches the LAN through.
func Interfaces() ([]Interface, error) {
	ifis, err := net.Interfaces()
	if err != nil {
		// Android denies the netlink dump net.Interfaces needs.
		return fallbackInterfaces(err)
	}
	return probeInterfaces(ifis, (*net.Interface).Addrs), nil
}

// probeInterfaces returns each IPv4 address of every interface in ifis
// that is up, running and multicast-capable, and neither loopback nor
// point-to-point; addrs lists an interface's addresses.
// C: net_get_interfaces (net_ifaddr.c) keeps the IPv4 addresses of the
// interfaces that are up and running and not loopback.
// Go: the Probe is multicast, so an interface must also take multicast,
// and a point-to-point link such as a VPN tunnel has no LAN behind it.
func probeInterfaces(ifis []net.Interface, addrs func(*net.Interface) ([]net.Addr, error)) []Interface {
	const want = net.FlagUp | net.FlagRunning | net.FlagMulticast
	var out []Interface
	for i := range ifis {
		ifi := &ifis[i]
		if ifi.Flags&want != want || ifi.Flags&(net.FlagLoopback|net.FlagPointToPoint) != 0 {
			continue
		}
		as, err := addrs(ifi)
		if err != nil {
			continue
		}
		for _, a := range as {
			var ip net.IP
			switch a := a.(type) {
			case *net.IPNet:
				ip = a.IP
			case *net.IPAddr:
				ip = a.IP
			}
			addr, ok := netip.AddrFromSlice(ip.To4())
			if !ok || addr.IsUnspecified() {
				continue
			}
			out = append(out, Interface{Name: ifi.Name, Index: ifi.Index, Addr: addr})
		}
	}
	return out
}

// Listen opens a UDP socket on ifc's address at an ephemeral port,
// with ifc as its multicast interface.
// C: stpp_netif_update (src/api/stpp.c) binds each interface's socket
// to its address only; on Linux the bound address picks the interface
// a multicast goes out through.
// Go: other systems route multicast by their tables, so the socket
// names its multicast interface too.
func Listen(ifc Interface) (*net.UDPConn, error) {
	conn, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(netip.AddrPortFrom(ifc.Addr, 0)))
	if err != nil {
		return nil, err
	}
	if ifc.Index == 0 {
		// No interface index without netlink: name the interface by its address.
		if err := setMulticastIfAddr(conn, ifc.Addr); err != nil {
			conn.Close()
			return nil, fmt.Errorf("multicast interface: %w", err)
		}
		return conn, nil
	}
	err = ipv4.NewPacketConn(conn).SetMulticastInterface(&net.Interface{Index: ifc.Index, Name: ifc.Name})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("multicast interface: %w", err)
	}
	return conn, nil
}

// fallbackInterfaces finds the LAN address without netlink, which
// Android denies to apps: it asks the kernel which source address it
// would use to reach each probe destination. Connecting a UDP socket
// sends nothing.
func fallbackInterfaces(cause error) ([]Interface, error) {
	return sourceInterfaces(cause, func(dst string) (net.Addr, error) {
		c, err := net.Dial("udp4", dst)
		if err != nil {
			return nil, err
		}
		defer c.Close()
		return c.LocalAddr(), nil
	})
}

// sourceInterfaces builds an Interface, without an index, for each
// distinct usable source address that localAddr reports for the probe
// destinations. When there is none, the error carries cause.
func sourceInterfaces(cause error, localAddr func(dst string) (net.Addr, error)) ([]Interface, error) {
	var out []Interface
	seen := map[netip.Addr]bool{}
	for _, dst := range []string{"224.0.0.251:5353", "239.255.255.250:3702", "192.0.2.1:9"} {
		la, err := localAddr(dst)
		if err != nil {
			continue
		}
		ua, ok := la.(*net.UDPAddr)
		if !ok {
			continue
		}
		addr, ok := netip.AddrFromSlice(ua.IP.To4())
		if !ok || addr.IsUnspecified() || addr.IsLoopback() || seen[addr] {
			continue
		}
		seen[addr] = true
		out = append(out, Interface{Name: addr.String(), Addr: addr})
	}
	if len(out) == 0 {
		return nil, errors.Join(cause, errors.New("source-address fallback found no address"))
	}
	return out, nil
}
