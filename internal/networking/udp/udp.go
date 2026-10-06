package udp

import (
	"fmt"
	"net"
	"sync"
	"time"
)

// UDPConn represents a UDP connection
type UDPConn struct {
	conn       *net.UDPConn
	localAddr  *net.UDPAddr
	remoteAddr *net.UDPAddr
	broadcast  bool
	mu         sync.Mutex
	closed     bool
}

// NewUDPConn creates a new UDP connection
func NewUDPConn(bindAddr string, port int, broadcast bool) (*UDPConn, error) {
	addr := &net.UDPAddr{
		Port: port,
		IP:   net.ParseIP(bindAddr),
	}

	if addr.IP == nil && bindAddr != "" {
		return nil, fmt.Errorf("invalid bind address: %s", bindAddr)
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on UDP: %w", err)
	}

	// Enable broadcast if requested
	if broadcast {
		if err := conn.SetReadBuffer(1024 * 1024); err != nil {
			conn.Close()
			return nil, fmt.Errorf("failed to set read buffer: %w", err)
		}
		if err := conn.SetWriteBuffer(1024 * 1024); err != nil {
			conn.Close()
			return nil, fmt.Errorf("failed to set write buffer: %w", err)
		}
	}

	return &UDPConn{
		conn:      conn,
		localAddr: conn.LocalAddr().(*net.UDPAddr),
		broadcast: broadcast,
	}, nil
}

// NewUDPMulticast creates a new UDP multicast connection
func NewUDPMulticast(multicastAddr string, port int, iface *net.Interface) (*UDPConn, error) {
	addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", multicastAddr, port))
	if err != nil {
		return nil, fmt.Errorf("failed to resolve multicast address: %w", err)
	}

	conn, err := net.ListenMulticastUDP("udp", iface, addr)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on multicast: %w", err)
	}

	return &UDPConn{
		conn:      conn,
		localAddr: conn.LocalAddr().(*net.UDPAddr),
	}, nil
}

// Write sends data to a remote address
func (u *UDPConn) Write(data []byte, remoteAddr *net.UDPAddr) (int, error) {
	u.mu.Lock()
	defer u.mu.Unlock()

	if u.closed {
		return 0, fmt.Errorf("connection closed")
	}

	if remoteAddr == nil {
		if u.remoteAddr == nil {
			return 0, fmt.Errorf("no remote address specified")
		}
		remoteAddr = u.remoteAddr
	}

	return u.conn.WriteToUDP(data, remoteAddr)
}

// Read reads data from the connection
func (u *UDPConn) Read(buf []byte) (int, *net.UDPAddr, error) {
	u.mu.Lock()
	defer u.mu.Unlock()

	if u.closed {
		return 0, nil, fmt.Errorf("connection closed")
	}

	n, addr, err := u.conn.ReadFromUDP(buf)
	if err != nil {
		return 0, nil, err
	}
	return n, addr, nil
}

// SetReadTimeout sets the read timeout
func (u *UDPConn) SetReadTimeout(timeout time.Duration) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.conn.SetReadDeadline(time.Now().Add(timeout))
}

// Close closes the UDP connection
func (u *UDPConn) Close() error {
	u.mu.Lock()
	defer u.mu.Unlock()

	if u.closed {
		return nil
	}

	u.closed = true
	return u.conn.Close()
}

// IsClosed returns true if the connection is closed
func (u *UDPConn) IsClosed() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.closed
}

// Note: Multicast group operations (JoinGroup, LeaveGroup, SetInterface, SetTTL, SetLoopback)
// are handled by the underlying net.ListenMulticastUDP and are not directly exposed
// on net.UDPConn. Use NewUDPMulticast for multicast connections.
