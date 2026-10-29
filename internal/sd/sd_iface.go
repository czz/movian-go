package sd

// DiscoveryService is the seam between the app and service-discovery
// backends (bonjour/avahi — the sd_* cgo files). NewSystem returns
// the handle; this interface names the contract so a backend can be
// swapped for a pure-Go (zeroconf — already a dep) or stub.
//
// contract: the System owns its browse threads; callers hold it only
// to register/unregister browsers — all callbacks fire on sd's own
// dispatch, not the caller's.
type DiscoveryService interface {
	// Register — start browsing for service type; cb fires per event.
	Register(stype string, cb func(ev any))
}
