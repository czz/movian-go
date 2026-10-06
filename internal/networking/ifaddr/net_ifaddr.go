package ifaddr

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/czz/movian-go/internal/asyncio"
)

// NetInterface represents a network interface
type NetInterface struct {
	Name      string
	Addresses []string
	Flags     string
}

// NetworkChangeCallback is called when network interfaces change
type NetworkChangeCallback func()

// NetIfAddrManager manages network interface monitoring
type NetIfAddrManager struct {
	networkChangeCallbacks map[int]NetworkChangeCallback
	networkChangeMutex     sync.Mutex
	networkMonitorRunning  bool
	networkMonitorCancel   context.CancelFunc
	nextCallbackID         int
	asyncioInstance        *asyncio.AsyncIO
}

// NewNetIfAddrManager creates a new network interface manager
func NewNetIfAddrManager(asyncioInstance *asyncio.AsyncIO) *NetIfAddrManager {
	return &NetIfAddrManager{
		networkChangeCallbacks: make(map[int]NetworkChangeCallback),
		asyncioInstance:        asyncioInstance,
	}
}

// GetInterfaces returns all network interfaces
func (nim *NetIfAddrManager) GetInterfaces(ctx context.Context) ([]*NetInterface, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	result := make([]*NetInterface, 0, len(interfaces))

	for _, iface := range interfaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		ni := &NetInterface{
			Name:      iface.Name,
			Addresses: make([]string, 0, len(addrs)),
			Flags:     iface.Flags.String(),
		}

		for _, addr := range addrs {
			ni.Addresses = append(ni.Addresses, addr.String())
		}

		result = append(result, ni)
	}

	return result, nil
}

// RegisterNetworkChangeCallback registers a callback for network changes
func (nim *NetIfAddrManager) RegisterNetworkChangeCallback(ctx context.Context, cb NetworkChangeCallback) (int, error) {
	if nim == nil {
		return 0, nil
	}
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}
	nim.networkChangeMutex.Lock()
	defer nim.networkChangeMutex.Unlock()
	if nim.networkChangeCallbacks == nil {
		nim.networkChangeCallbacks = make(map[int]NetworkChangeCallback)
	}
	nim.nextCallbackID++
	nim.networkChangeCallbacks[nim.nextCallbackID] = cb
	return nim.nextCallbackID, nil
}

// UnregisterNetworkChangeCallback unregisters a network change callback
func (nim *NetIfAddrManager) UnregisterNetworkChangeCallback(ctx context.Context, id int) error {
	if nim == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	nim.networkChangeMutex.Lock()
	defer nim.networkChangeMutex.Unlock()
	delete(nim.networkChangeCallbacks, id)
	return nil
}

// triggerNetworkChange triggers all registered network change callbacks
func (nim *NetIfAddrManager) triggerNetworkChange() {
	nim.networkChangeMutex.Lock()
	callbacks := make([]NetworkChangeCallback, 0, len(nim.networkChangeCallbacks))
	for _, cb := range nim.networkChangeCallbacks {
		callbacks = append(callbacks, cb)
	}
	nim.networkChangeMutex.Unlock()

	for _, cb := range callbacks {
		if cb != nil {
			cb()
		}
	}
}

// StartNetworkMonitor starts monitoring network interface changes
func (nim *NetIfAddrManager) StartNetworkMonitor(ctx context.Context) error {
	if nim == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	nim.networkChangeMutex.Lock()
	if nim.networkMonitorRunning {
		nim.networkChangeMutex.Unlock()
		return nil
	}
	nim.networkMonitorRunning = true
	nim.networkChangeMutex.Unlock()

	if nim.asyncioInstance == nil {
		nim.asyncioInstance = asyncio.NewAsyncIO()
	}

	monitorCtx, cancel := context.WithCancel(ctx)
	nim.networkMonitorCancel = cancel

	nim.asyncioInstance.RunTask(func(aux any) {
		lastInterfaces := make(map[string][]string)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-monitorCtx.Done():
				nim.networkChangeMutex.Lock()
				nim.networkMonitorRunning = false
				nim.networkChangeMutex.Unlock()
				return
			case <-ticker.C:
				currentInterfaces, err := nim.GetInterfaces(monitorCtx)
				if err != nil {
					continue
				}
				currentMap := make(map[string][]string)

				for _, iface := range currentInterfaces {
					currentMap[iface.Name] = iface.Addresses
				}

				// Check if interfaces changed
				if !interfacesEqual(lastInterfaces, currentMap) {
					nim.triggerNetworkChange()
				}

				lastInterfaces = currentMap
			}
		}
	}, nil)

	return nil
}

// StopNetworkMonitor stops monitoring network interface changes
func (nim *NetIfAddrManager) StopNetworkMonitor(ctx context.Context) error {
	if nim == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	nim.networkChangeMutex.Lock()
	defer nim.networkChangeMutex.Unlock()
	if nim.networkMonitorCancel != nil {
		nim.networkMonitorCancel()
		nim.networkMonitorCancel = nil
	}
	nim.networkMonitorRunning = false
	return nil
}

// interfacesEqual compares two interface maps
func interfacesEqual(a, b map[string][]string) bool {
	if len(a) != len(b) {
		return false
	}

	for name, addrs := range a {
		if bAddrs, ok := b[name]; !ok || !stringSlicesEqual(addrs, bAddrs) {
			return false
		}
	}

	return true
}

// stringSlicesEqual compares two string slices
func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
