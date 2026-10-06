//go:build linux && !android && cgo && avahicgo

package sd

/*
#cgo pkg-config: avahi-client
#include <stdlib.h>
#include <string.h>
#include <avahi-client/client.h>
#include <avahi-client/publish.h>
#include <avahi-client/lookup.h>
#include <avahi-common/alternative.h>
#include <avahi-common/simple-watch.h>
#include <avahi-common/error.h>
#include <avahi-common/malloc.h>

extern void goAvahiClientStateChange(AvahiClient *c, int state,
	uintptr_t userdata);
extern void goAvahiClientCallback(AvahiClient *c, int state,
	uintptr_t userdata);
extern void goAvahiResolve(AvahiServiceResolver *r, AvahiIfIndex iface,
	AvahiProtocol protocol, AvahiResolverEvent event, char *name, char *type,
	char *domain, char *host_name, AvahiAddress *address, uint16_t port,
	AvahiStringList *txt, AvahiLookupResultFlags flags, uintptr_t userdata);
extern void goAvahiBrowse(AvahiServiceBrowser *b, AvahiIfIndex iface,
	AvahiProtocol protocol, AvahiBrowserEvent event, char *name, char *type,
	char *domain, AvahiLookupResultFlags flags, uintptr_t userdata);
extern void goAvahiEntryGroup(AvahiEntryGroup *g, AvahiEntryGroupState state,
	uintptr_t userdata);

// Callback trampolines — C passes void* userdata; Go objects cross the
// boundary as uintptr_t cgo.Handle values.
static void clientStateChangeTramp(AvahiClient *c, AvahiClientState state,
	void *userdata) {
	goAvahiClientStateChange(c, state, (uintptr_t)userdata);
}

static void clientCallbackTramp(AvahiClient *c, AvahiClientState state,
	void *userdata) {
	goAvahiClientCallback(c, state, (uintptr_t)userdata);
}

static void resolveTramp(AvahiServiceResolver *r, AvahiIfIndex iface,
	AvahiProtocol protocol, AvahiResolverEvent event, const char *name,
	const char *type, const char *domain, const char *host_name,
	const AvahiAddress *address, uint16_t port, AvahiStringList *txt,
	AvahiLookupResultFlags flags, void *userdata) {
	goAvahiResolve(r, iface, protocol, event, (char *)name, (char *)type,
		(char *)domain, (char *)host_name, (AvahiAddress *)address, port,
		txt, flags, (uintptr_t)userdata);
}

static void browseTramp(AvahiServiceBrowser *b, AvahiIfIndex iface,
	AvahiProtocol protocol, AvahiBrowserEvent event, const char *name,
	const char *type, const char *domain, AvahiLookupResultFlags flags,
	void *userdata) {
	goAvahiBrowse(b, iface, protocol, event, (char *)name, (char *)type,
		(char *)domain, flags, (uintptr_t)userdata);
}

static void entryGroupTramp(AvahiEntryGroup *g, AvahiEntryGroupState state,
	void *userdata) {
	goAvahiEntryGroup(g, state, (uintptr_t)userdata);
}

// Constructor wrappers — install the trampolines (cgo cannot take the
// address of a C function to pass as a callback argument).
static AvahiClient *newBrowseClient(AvahiSimplePoll *asp, uintptr_t dh) {
	return avahi_client_new(avahi_simple_poll_get(asp), AVAHI_CLIENT_NO_FAIL,
		clientStateChangeTramp, (void *)dh, NULL);
}

static AvahiClient *newPublishClient(AvahiSimplePoll *asp, uintptr_t dh) {
	return avahi_client_new(avahi_simple_poll_get(asp), AVAHI_CLIENT_NO_FAIL,
		clientCallbackTramp, (void *)dh, NULL);
}

static AvahiServiceBrowser *newBrowser(AvahiClient *c, const char *type,
	uintptr_t sa) {
	return avahi_service_browser_new(c, AVAHI_IF_UNSPEC, AVAHI_PROTO_INET,
		type, NULL, 0, browseTramp, (void *)sa);
}

static AvahiServiceResolver *newResolver(AvahiClient *c, AvahiIfIndex iface,
	AvahiProtocol protocol, const char *name, const char *type,
	const char *domain, uintptr_t si) {
	return avahi_service_resolver_new(c, iface, protocol, name, type, domain,
		AVAHI_PROTO_INET, 0, resolveTramp, (void *)si);
}

static AvahiEntryGroup *newEntryGroup(AvahiClient *c, uintptr_t dh) {
	return avahi_entry_group_new(c, entryGroupTramp, (void *)dh);
}

// Varargs wrappers — cgo cannot call variadic C functions.
static AvahiStringList *txtFind(AvahiStringList *l, const char *key) {
	return avahi_string_list_find(l, key);
}

static int txtGetPair(AvahiStringList *l, char **value) {
	return avahi_string_list_get_pair(l, NULL, value, NULL);
}

static int entryGroupAddService(AvahiEntryGroup *g, const char *name) {
	return avahi_entry_group_add_service(g, AVAHI_IF_UNSPEC,
		AVAHI_PROTO_UNSPEC, 0, name, "_airplay._tcp", NULL, NULL, 42000,
		NULL);
}

// C: avahi_client_set_host_name(c, gconf.system_name) — #if STOS
static void setHostName(AvahiClient *c, const char *name) {
	avahi_client_set_host_name(c, name);
}
*/
import "C"

import (
	"fmt"
	"runtime/cgo"
	"slices"
	"unsafe"

	"github.com/czz/movian-go/internal/app"
	"github.com/czz/movian-go/internal/trace"
)

// avahiDaemon — C: static AvahiSimplePoll *asp; static AvahiEntryGroup
// *group; static char *name; static int hostname_changed; static struct
// service_instance_list services — consolidated single-daemon state
// (avahi.c), now owned by System.
type avahiDaemon struct {
	sys             *System // owning sd system (deps live there)
	asp             *C.AvahiSimplePoll
	group           *C.AvahiEntryGroup
	airplayName     *C.char // avahi-managed string
	hostnameChanged bool
	services        []*serviceInstance // C: service_instance_list
	handle          cgo.Handle         // self-handle for trampolines' userdata
}

// systemName — C: gconf.system_name read.
func (a *avahiDaemon) systemName() string {
	if a.sys.gconf == nil {
		return ""
	}
	return a.sys.gconf.SystemName
}

// C: static struct service_instance_list services (avahi.c file scope —
// bonjour.c has its own same-named static; Go package scope requires
// distinct names)

// C: service_aux_t
type serviceAux struct {
	av     *avahiDaemon   // owning daemon (Go: was file-global state)
	c      *C.AvahiClient // C: sa_c
	class  int            // C: sa_class
	handle cgo.Handle     // Go-only: userdata handle
}

// C: ENABLE_AIRPLAY — configure.linux: "#enable airplay -- not functional
// yet" (disabled → the block is compiled out, exactly as #if ENABLE_AIRPLAY)
const enableAirplay = false

/**
 * C: client_state_change
 */
func (a *avahiDaemon) clientStateChange(c *C.AvahiClient, state C.AvahiClientState) {
	a.sys.ts.Trace(trace.TRACE_DEBUG, "AVAHI", "Client state change %d", int(state))
}

/**
 * C: resolve_callback — userdata is the service_instance_t (as cgo handle)
 */
func resolveCallback(r *C.AvahiServiceResolver, iface C.AvahiIfIndex,
	protocol C.AvahiProtocol, event C.AvahiResolverEvent,
	name, typ, domain, hostName *C.char, address *C.AvahiAddress,
	port C.uint16_t, txt *C.AvahiStringList, flags C.AvahiLookupResultFlags,
	si *serviceInstance) {
	sa := si.opaque.(*serviceAux)
	d := sa.av

	switch event {
	case C.AVAHI_RESOLVER_FAILURE:
		d.sys.ts.Trace(trace.TRACE_ERROR, "AVAHI",
			"Failed to resolve service '%s' of type '%s' in domain '%s': %s\n",
			C.GoString(name), C.GoString(typ), C.GoString(domain),
			C.GoString(C.avahi_strerror(C.avahi_client_errno(sa.c))))
		d.sys.siDestroy(&d.services, si)

	case C.AVAHI_RESOLVER_FOUND:
		var a [C.AVAHI_ADDRESS_STR_MAX]C.char
		C.avahi_address_snprint(&a[0], C.AVAHI_ADDRESS_STR_MAX, address)
		addr := C.GoString(&a[0])

		d.sys.ts.Trace(trace.TRACE_DEBUG, "AVAHI",
			"Found service '%s' of type '%s' at %s:%d (%s)",
			C.GoString(name), C.GoString(typ), addr, int(port),
			C.GoString(hostName))

		switch sa.class {
		case ServiceHtsp:
			d.sys.sdAddServiceHtsp(si, C.GoString(name), addr, int(port))

		case ServiceWebdav:
			// C: avahi_string_list_find(txt, "path") + get_pair
			var cpath *C.char
			keyPath := C.CString("path")
			apath := C.txtFind(txt, keyPath)
			C.free(unsafe.Pointer(keyPath))
			path := ""
			if apath != nil && C.txtGetPair(apath, &cpath) == 0 {
				path = C.GoString(cpath)
			}

			var ccontents *C.char
			keyContents := C.CString("contents")
			acontents := C.txtFind(txt, keyContents)
			C.free(unsafe.Pointer(keyContents))
			contents := ""
			if acontents != nil && C.txtGetPair(acontents, &ccontents) == 0 {
				contents = C.GoString(ccontents)
			}

			d.sys.sdAddServiceWebdav(si, C.GoString(name), addr, int(port), path, contents)

			if cpath != nil {
				C.avahi_free(unsafe.Pointer(cpath))
			}
			// C: contents is never avahi_free'd — upstream leak preserved
		}
	}
	C.avahi_service_resolver_free(r)
}

/**
 * C: browser
 */
func browserCallback(b *C.AvahiServiceBrowser, iface C.AvahiIfIndex,
	protocol C.AvahiProtocol, event C.AvahiBrowserEvent,
	name, typ, domain *C.char, flags C.AvahiLookupResultFlags,
	sa *serviceAux) {
	d := sa.av

	// C: snprintf(fullname, ..., "%s.%s.%s.%d.%d", name, type, domain,
	//             interface, protocol)
	fullname := fmt.Sprintf("%s.%s.%s.%d.%d", C.GoString(name),
		C.GoString(typ), C.GoString(domain), int(iface), int(protocol))

	switch event {
	case C.AVAHI_BROWSER_NEW:
		si := &serviceInstance{opaque: sa, id: fullname}
		si.handle = cgo.NewHandle(si)
		// C: LIST_INSERT_HEAD(&services, si, si_link)
		d.services = slices.Insert(d.services, 0, si)

		if C.newResolver(sa.c, iface, protocol, name, typ, domain,
			C.uintptr_t(si.handle)) == nil {
			d.sys.siDestroy(&d.services, si)
			d.sys.ts.Trace(trace.TRACE_ERROR, "AVAHI",
				"Failed to resolve service '%s': %s\n",
				C.GoString(name),
				C.GoString(C.avahi_strerror(C.avahi_client_errno(sa.c))))
		}

	case C.AVAHI_BROWSER_REMOVE:
		if si := siFind(d.services, fullname); si != nil {
			d.sys.siDestroy(&d.services, si)
		}
	}
}

/**
 * C: service_type_add
 */
func (a *avahiDaemon) serviceTypeAdd(name string, class int, c *C.AvahiClient) {
	sa := &serviceAux{av: a, c: c, class: class}
	sa.handle = cgo.NewHandle(sa) // C: malloc — never freed (upstream leak)

	cname := C.CString(name)
	C.newBrowser(c, cname, C.uintptr_t(sa.handle))
	C.free(unsafe.Pointer(cname))
}

// C: #if ENABLE_AIRPLAY — compiled out on Linux (enableAirplay == false)

// AvahiUpdateHostname — C: avahi_update_hostname (avahi.c:443-451),
// #if STOS. Called by set_system_name so the next poll iteration
// re-announces gconf.system_name via avahi_client_set_host_name.
func (s *System) AvahiUpdateHostname() {
	a := s.avahi
	if a == nil {
		return
	}
	a.hostnameChanged = true
	if a.asp != nil {
		C.avahi_simple_poll_wakeup(a.asp)
	}
}

/**
 * C: entry_group_callback
 */
func (a *avahiDaemon) entryGroupCallback(g *C.AvahiEntryGroup, state C.AvahiEntryGroupState) {
	// C: assert(g == group || group == NULL); group = g
	a.group = g

	switch state {
	case C.AVAHI_ENTRY_GROUP_ESTABLISHED:
		a.sys.ts.Trace(trace.TRACE_INFO, "AVAHI",
			"Service '%s' successfully established.", C.GoString(a.airplayName))

	case C.AVAHI_ENTRY_GROUP_COLLISION:
		// A service name collision with a remote service happened.
		// Let's pick a new name
		n := C.avahi_alternative_service_name(a.airplayName)
		C.avahi_free(unsafe.Pointer(a.airplayName))
		a.airplayName = n

		a.sys.ts.Trace(trace.TRACE_ERROR, "AVAHI",
			"Service name collision, renaming service to '%s'",
			C.GoString(a.airplayName))
		// And recreate the services
		a.createServices(C.avahi_entry_group_get_client(g))

	case C.AVAHI_ENTRY_GROUP_FAILURE:
		a.sys.ts.Trace(trace.TRACE_ERROR, "AVAHI",
			"Entry group failure: %s",
			C.GoString(C.avahi_strerror(C.avahi_client_errno(
				C.avahi_entry_group_get_client(g)))))

	case C.AVAHI_ENTRY_GROUP_UNCOMMITED, C.AVAHI_ENTRY_GROUP_REGISTERING:
	}
}

/**
 * C: create_services
 */
func (a *avahiDaemon) createServices(c *C.AvahiClient) {
	// If this is the first time we're called, let's create a new
	// entry group if necessary
	if a.group == nil {
		a.group = C.newEntryGroup(c, C.uintptr_t(a.handle))
		if a.group == nil {
			a.sys.ts.Trace(trace.TRACE_ERROR, "AVAHI",
				"avahi_enty_group_new() failed: %s",
				C.GoString(C.avahi_strerror(C.avahi_client_errno(c))))
			return // C: goto fail
		}
	}

	// If the group is empty (either because it was just created, or
	// because it was reset previously, add our entries.
	if C.avahi_entry_group_is_empty(a.group) != 0 {
		a.sys.ts.Trace(trace.TRACE_DEBUG, "AVAHI",
			"Adding service '%s'", C.GoString(a.airplayName))

		// Add the service for HTSP
		ret := C.entryGroupAddService(a.group, a.airplayName)
		if ret < 0 {
			if ret == C.AVAHI_ERR_COLLISION {
				// collision: pick a new name
				n := C.avahi_alternative_service_name(a.airplayName)
				C.avahi_free(unsafe.Pointer(a.airplayName))
				a.airplayName = n
				a.sys.ts.Trace(trace.TRACE_ERROR, "AVAHI",
					"Service name collision, renaming service to '%s'",
					C.GoString(a.airplayName))
				C.avahi_entry_group_reset(a.group)
				a.createServices(c)
				return
			}
			a.sys.ts.Trace(trace.TRACE_ERROR, "AVAHI",
				"Failed to add _airplay._tcp service: %s",
				C.GoString(C.avahi_strerror(ret)))
			return // C: goto fail
		}

		// Tell the server to register the service
		if ret := C.avahi_entry_group_commit(a.group); ret < 0 {
			a.sys.ts.Trace(trace.TRACE_ERROR, "AVAHI",
				"Failed to commit entry group: %s",
				C.GoString(C.avahi_strerror(ret)))
			return // C: goto fail
		}
	}
}

/**
 * C: client_callback (airplay publishing client)
 */
func (a *avahiDaemon) clientCallback(c *C.AvahiClient, state C.AvahiClientState) {
	// Called whenever the client or server state changes
	a.sys.ts.Trace(trace.TRACE_DEBUG, "AVAHI", "State %d", int(state))

	switch state {
	case C.AVAHI_CLIENT_S_RUNNING:
		// The server has startup successfully and registered its host
		// name on the network, so it's time to create our services
		a.createServices(c)

	case C.AVAHI_CLIENT_FAILURE:
		a.sys.ts.Trace(trace.TRACE_ERROR, "AVAHI",
			"Client failure: %s",
			C.GoString(C.avahi_strerror(C.avahi_client_errno(c))))

	case C.AVAHI_CLIENT_S_COLLISION, C.AVAHI_CLIENT_S_REGISTERING:
		// C: S_COLLISION falls through to S_REGISTERING —
		// drop our registered services
		if a.group != nil {
			C.avahi_entry_group_reset(a.group)
		}

	case C.AVAHI_CLIENT_CONNECTING:
	}
}

/**
 * C: avahi_thread
 */
func (a *avahiDaemon) avahiThread() {
	c := C.newBrowseClient(a.asp, C.uintptr_t(a.handle))

	a.serviceTypeAdd("_webdav._tcp", ServiceWebdav, c)
	a.serviceTypeAdd("_htsp._tcp", ServiceHtsp, c)

	if enableAirplay {
		// C: #if ENABLE_AIRPLAY — name = strdup(APPNAMEUSER);
		//    avahi_client_new(ap, AVAHI_CLIENT_NO_FAIL, client_callback, ...)
		a.airplayName = C.CString(app.AppNameUser)
		C.newPublishClient(a.asp, C.uintptr_t(a.handle))
	}

	// C: #if STOS — avahi_client_set_host_name(c, gconf.system_name)
	if mgosAvahi {
		cs := C.CString(a.systemName())
		C.setHostName(c, cs)
		C.free(unsafe.Pointer(cs))
	}

	for C.avahi_simple_poll_iterate(a.asp, -1) != -1 {
		// C: #if STOS (avahi.c:412-419) — pick up hostname changes once
		//    the client left S_REGISTERING.
		if mgosAvahi &&
			C.avahi_client_get_state(c) != C.AVAHI_CLIENT_S_REGISTERING {
			if a.hostnameChanged {
				a.hostnameChanged = false
				cs := C.CString(a.systemName())
				C.setHostName(c, cs)
				C.free(unsafe.Pointer(cs))
			}
		}
	}
}

/**
 * C: avahi_init
 */
func (s *System) avahiStart() {
	a := &avahiDaemon{sys: s}
	a.handle = cgo.NewHandle(a)
	s.avahi = a
	a.asp = C.avahi_simple_poll_new()
	// C: hts_thread_create_detached("AVAHI", avahi_thread, NULL,
	//                             THREAD_PRIO_BGTASK)
	go a.avahiThread()
}
