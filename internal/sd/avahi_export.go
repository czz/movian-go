//go:build linux && !android && cgo && avahicgo

package sd

/*
#include <avahi-client/client.h>
#include <avahi-client/publish.h>
#include <avahi-client/lookup.h>
#include <avahi-common/simple-watch.h>
*/
import "C"

import "runtime/cgo"

// Callback exports invoked from the avahi trampolines in avahi.go.
// userdata arrives as a uintptr_t cgo.Handle.

//export goAvahiClientStateChange
func goAvahiClientStateChange(c *C.AvahiClient, state C.int,
	userdata C.uintptr_t) {
	cgo.Handle(userdata).Value().(*avahiDaemon).
		clientStateChange(c, C.AvahiClientState(state))
}

//export goAvahiClientCallback
func goAvahiClientCallback(c *C.AvahiClient, state C.int,
	userdata C.uintptr_t) {
	cgo.Handle(userdata).Value().(*avahiDaemon).
		clientCallback(c, C.AvahiClientState(state))
}

//export goAvahiResolve
func goAvahiResolve(r *C.AvahiServiceResolver, iface C.AvahiIfIndex,
	protocol C.AvahiProtocol, event C.AvahiResolverEvent,
	name, typ, domain, hostName *C.char, address *C.AvahiAddress,
	port C.uint16_t, txt *C.AvahiStringList, flags C.AvahiLookupResultFlags,
	userdata C.uintptr_t) {
	resolveCallback(r, iface, protocol, event, name, typ, domain, hostName,
		address, port, txt, flags,
		cgo.Handle(userdata).Value().(*serviceInstance))
}

//export goAvahiBrowse
func goAvahiBrowse(b *C.AvahiServiceBrowser, iface C.AvahiIfIndex,
	protocol C.AvahiProtocol, event C.AvahiBrowserEvent,
	name, typ, domain *C.char, flags C.AvahiLookupResultFlags,
	userdata C.uintptr_t) {
	browserCallback(b, iface, protocol, event, name, typ, domain, flags,
		cgo.Handle(userdata).Value().(*serviceAux))
}

//export goAvahiEntryGroup
func goAvahiEntryGroup(g *C.AvahiEntryGroup, state C.AvahiEntryGroupState,
	userdata C.uintptr_t) {
	cgo.Handle(userdata).Value().(*avahiDaemon).
		entryGroupCallback(g, state)
}
