package core

import (
	"fmt"
	"net"
)

// NetAddr represents a network address (IPv4 or IPv6)
type NetAddr struct {
	Family uint8  // 4 for IPv4, 6 for IPv6
	Port   uint16 // host order
	Addr   [16]byte
}

// NetIF represents a network interface
type NetIF struct {
	Name     string
	IPv4Addr [4]byte
	IPv4Mask [4]byte
}

// TCPFlags defines connection flags
const (
	Tcpssl       = 0x1
	TCPDebug     = 0x2
	TCPNoProxy   = 0x4
	TCPSSLVerify = 0x8
)

// NetResolve resolves a hostname to a NetAddr (DNS only, no NetBIOS)
func NetResolve(hostname string) (*NetAddr, error) {
	addrs, err := net.LookupHost(hostname)
	if err != nil {
		return nil, err
	}

	for _, addr := range addrs {
		ip := net.ParseIP(addr)
		if ip != nil {
			na := &NetAddr{}
			if ip4 := ip.To4(); ip4 != nil {
				na.Family = 4
				copy(na.Addr[:], ip4)
			} else {
				na.Family = 6
				copy(na.Addr[:], ip)
			}
			return na, nil
		}
	}

	return nil, fmt.Errorf("no valid addresses found for %s", hostname)
}

// NetResolveNumeric resolves a numeric hostname to a NetAddr
func NetResolveNumeric(hostname string) (*NetAddr, error) {
	ip := net.ParseIP(hostname)
	if ip == nil {
		return nil, fmt.Errorf("invalid numeric address: %s", hostname)
	}

	na := &NetAddr{}
	if ip4 := ip.To4(); ip4 != nil {
		na.Family = 4
		copy(na.Addr[:], ip4)
	} else {
		na.Family = 6
		copy(na.Addr[:], ip)
	}
	return na, nil
}

// NetFmtHost formats a NetAddr to a string
func NetFmtHost(na *NetAddr) string {
	if na == nil {
		return ""
	}

	switch na.Family {
	case 4:
		if na.Port != 0 {
			return fmt.Sprintf("%d.%d.%d.%d:%d",
				na.Addr[0], na.Addr[1], na.Addr[2], na.Addr[3], na.Port)
		}
		return fmt.Sprintf("%d.%d.%d.%d",
			na.Addr[0], na.Addr[1], na.Addr[2], na.Addr[3])
	case 6:
		ip := net.IP(na.Addr[:])
		if na.Port != 0 {
			return fmt.Sprintf("[%s]:%d", ip.String(), na.Port)
		}
		return ip.String()
	default:
		return fmt.Sprintf("family-%d", na.Family)
	}
}

// NetAddrStr returns the string representation of a NetAddr
func NetAddrStr(na *NetAddr) string {
	return NetFmtHost(na)
}

// NetAddrCmp compares two NetAddr structures
func NetAddrCmp(a, b *NetAddr) bool {
	if a == nil || b == nil {
		return false
	}
	if a.Family != b.Family {
		return false
	}
	if a.Port != b.Port {
		return false
	}
	if a.Family == 4 {
		return a.Addr[0] == b.Addr[0] &&
			a.Addr[1] == b.Addr[1] &&
			a.Addr[2] == b.Addr[2] &&
			a.Addr[3] == b.Addr[3]
	}
	return a.Addr == b.Addr
}

// NetIsAddrInNetif checks if an address is in a network interface's subnet
func NetIsAddrInNetif(ni *NetIF, na *NetAddr) bool {
	if ni == nil || na == nil || na.Family != 4 {
		return false
	}

	// Calculate network address
	ifAddr := uint32(ni.IPv4Addr[0])<<24 | uint32(ni.IPv4Addr[1])<<16 |
		uint32(ni.IPv4Addr[2])<<8 | uint32(ni.IPv4Addr[3])
	mask := uint32(ni.IPv4Mask[0])<<24 | uint32(ni.IPv4Mask[1])<<16 |
		uint32(ni.IPv4Mask[2])<<8 | uint32(ni.IPv4Mask[3])
	addr := uint32(na.Addr[0])<<24 | uint32(na.Addr[1])<<16 |
		uint32(na.Addr[2])<<8 | uint32(na.Addr[3])

	return (addr & mask) == (ifAddr & mask)
}

// NetAddrV4Port creates a NetAddr for IPv4 with port
func NetAddrV4Port(port uint16) *NetAddr {
	return &NetAddr{Family: 4, Port: port}
}

// NetAddrV4Addr creates a NetAddr for IPv4 address
func NetAddrV4Addr(a, b, c, d byte) *NetAddr {
	return &NetAddr{
		Family: 4,
		Addr:   [16]byte{a, b, c, d},
	}
}

// ToNetIP converts NetAddr to net.IP
func (na *NetAddr) ToNetIP() net.IP {
	if na == nil {
		return nil
	}
	switch na.Family {
	case 4:
		return net.IP(na.Addr[:4])
	case 6:
		return net.IP(na.Addr[:])
	default:
		return nil
	}
}

// ToTCPAddr converts NetAddr to net.TCPAddr
func (na *NetAddr) ToTCPAddr() *net.TCPAddr {
	if na == nil {
		return nil
	}
	return &net.TCPAddr{
		IP:   na.ToNetIP(),
		Port: int(na.Port),
	}
}

// ToUDPAddr converts NetAddr to net.UDPAddr
func (na *NetAddr) ToUDPAddr() *net.UDPAddr {
	if na == nil {
		return nil
	}
	return &net.UDPAddr{
		IP:   na.ToNetIP(),
		Port: int(na.Port),
	}
}

// String returns the string representation
func (na *NetAddr) String() string {
	return NetFmtHost(na)
}

// NetIFNameSize is the maximum size for interface name
const NetIFNameSize = 64

// NetGetInterfaces returns all network interfaces
func NetGetInterfaces() ([]NetIF, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	var result []NetIF
	for _, iface := range interfaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}

			if ip == nil {
				continue
			}

			ip4 := ip.To4()
			if ip4 == nil {
				continue
			}

			ni := NetIF{
				Name: iface.Name,
			}
			copy(ni.IPv4Addr[:], ip4)

			// Get netmask
			if ipnet, ok := addr.(*net.IPNet); ok {
				mask := ipnet.Mask
				if len(mask) == 4 {
					copy(ni.IPv4Mask[:], mask)
				}
			}

			result = append(result, ni)
			break // Only take first IPv4 address per interface
		}
	}

	return result, nil
}
