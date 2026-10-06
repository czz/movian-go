//go:build darwin

package sd

/*
#cgo LDFLAGS: -framework CoreServices
#include <stdlib.h>
#include <string.h>
#include <arpa/inet.h>
#include <CoreServices/CoreServices.h>

// ntohs is a macro (OSSwapInt16) on Darwin — invisible to cgo.
static unsigned short mgl_ntohs(unsigned short v) { return ntohs(v); }

extern void goBonjourResolve(uintptr_t theService, uintptr_t info);
extern void goBonjourBrowser(CFOptionFlags flags, uintptr_t domainOrService,
	uintptr_t info);

// Callback trampolines — Go objects cross the boundary as uintptr_t
// cgo.Handle values.
static void bonjourResolveTramp(CFNetServiceRef theService,
	CFStreamError *error, void *info) {
	goBonjourResolve((uintptr_t)theService, (uintptr_t)info);
}

static void bonjourBrowserTramp(CFNetServiceBrowserRef browser,
	CFOptionFlags flags, CFTypeRef domainOrService, CFStreamError *error,
	void *info) {
	goBonjourBrowser(flags, (uintptr_t)domainOrService, (uintptr_t)info);
}

// Wrappers — cgo cannot take C callback addresses or call CFSTR/inline CF API.
static CFStringRef newCFString(const char *s) {
	return CFStringCreateWithCString(NULL, s, kCFStringEncodingUTF8);
}

static CFStringRef emptyCFString(void) {
	return CFSTR("");
}

static CFStringRef pathCFString(void) {
	return CFSTR("path");
}

static CFStringRef contentsCFString(void) {
	return CFSTR("contents");
}

static CFStringRef commonModes(void) {
	return kCFRunLoopCommonModes;
}

static CFNetServiceRef serviceCreate(CFNetServiceRef bservice) {
	return CFNetServiceCreate(kCFAllocatorDefault,
		CFNetServiceGetDomain(bservice), CFNetServiceGetType(bservice),
		CFNetServiceGetName(bservice), 0);
}

static void serviceSetClient(CFNetServiceRef s, uintptr_t info) {
	CFNetServiceClientContext context = {0, NULL, NULL, NULL, NULL};
	context.info = (void *)info;
	CFNetServiceSetClient(s, bonjourResolveTramp, &context);
}

static void serviceClearClient(CFNetServiceRef s) {
	CFNetServiceSetClient(s, NULL, NULL);
}

static void scheduleService(CFNetServiceRef s) {
	CFNetServiceScheduleWithRunLoop(s, CFRunLoopGetCurrent(),
		kCFRunLoopCommonModes);
}

static void unscheduleService(CFNetServiceRef s) {
	CFNetServiceUnscheduleFromRunLoop(s, CFRunLoopGetCurrent(),
		kCFRunLoopCommonModes);
}

static Boolean resolveWithTimeout(CFNetServiceRef s, CFStreamError *e) {
	return CFNetServiceResolveWithTimeout(s, 0, e);
}

static CFNetServiceBrowserRef browserCreate(uintptr_t sa) {
	CFNetServiceClientContext context = {0, NULL, NULL, NULL, NULL};
	context.info = (void *)sa;
	return CFNetServiceBrowserCreate(kCFAllocatorDefault,
		bonjourBrowserTramp, &context);
}

static void scheduleBrowser(CFNetServiceBrowserRef b) {
	CFNetServiceBrowserScheduleWithRunLoop(b, CFRunLoopGetCurrent(),
		kCFRunLoopCommonModes);
}

static void unscheduleBrowser(CFNetServiceBrowserRef b) {
	CFNetServiceBrowserUnscheduleFromRunLoop(b, CFRunLoopGetCurrent(),
		kCFRunLoopCommonModes);
}

static Boolean searchForServices(CFNetServiceBrowserRef b, CFStringRef type,
	CFStreamError *e) {
	return CFNetServiceBrowserSearchForServices(b, CFSTR(""), type, e);
}

static void getCString(CFStringRef s, char *buf, CFIndex size) {
	CFStringGetCString(s, buf, size, kCFStringEncodingUTF8);
}

static CFDictionaryRef txtToDict(CFDataRef txt) {
	return CFNetServiceCreateDictionaryWithTXTData(kCFAllocatorDefault, txt);
}

// cgo maps the CF typedef'd refs to uintptr-based Go types, so casts
// between them (CFArray element → CFDataRef → bytes) live here in C.
static const struct sockaddr *bj_sockaddr(CFArrayRef a, CFIndex i) {
	return (const struct sockaddr *)CFDataGetBytePtr(
		(CFDataRef)CFArrayGetValueAtIndex(a, i));
}

static const void *bj_dictValue(CFDictionaryRef d, CFStringRef k) {
	return CFDictionaryGetValue(d, k);
}

static CFIndex bj_dataLen(const void *d) {
	return CFDataGetLength((CFDataRef)d);
}

static const void *bj_dataBytes(const void *d) {
	return CFDataGetBytePtr((CFDataRef)d);
}
*/
import "C"

import (
	"fmt"
	"runtime/cgo"
	"slices"
	"unsafe"

	"github.com/czz/movian-go/internal/trace"
)

// C: main.h
const (
	urlMax      = 2048 // C: URL_MAX
	hostnameMax = 256  // C: HOSTNAME_MAX
)

// bonjourDaemon — C: static struct service_instance_list services
// (bonjour.c file scope), now owned by System.
type bonjourDaemon struct {
	sys      *System // owning sd system (deps live there)
	services []*serviceInstance
}

// C: service_aux_t
type bonjourServiceAux struct {
	bj      *bonjourDaemon    // owning daemon (Go: was file-global state)
	service C.CFNetServiceRef // C: sa_service
	class   int               // C: sa_class
	handle  cgo.Handle        // Go-only: userdata handle
}

/**
 * C: bonjour_resolve_callback — info is the service_instance_t (cgo handle)
 */
func bonjourResolveCallback(theService C.CFNetServiceRef,
	si *serviceInstance) {
	sa := si.opaque.(*bonjourServiceAux)
	b := sa.bj

	var name [256]C.char
	C.getCString(C.CFNetServiceGetName(theService), &name[0], 256)
	nameStr := C.GoString(&name[0])

	addresses := C.CFNetServiceGetAddressing(theService)
	count := int(C.CFArrayGetCount(addresses))

	b.sys.ts.Trace(trace.TRACE_DEBUG, "Bonjour",
		"Resolve service \"%s\" with %d addresses", nameStr, count)

	hasIPv4 := false
	for i := range count {
		addr := C.bj_sockaddr(addresses, C.CFIndex(i))
		if addr.sa_family == C.AF_INET {
			hasIPv4 = true
			break
		}
	}

	for i := range count {
		var host [hostnameMax]C.char
		var pathbuf [urlMax]C.char
		var contentsbuf [512]C.char
		var path, contents string
		var port int

		addr := C.bj_sockaddr(addresses, C.CFIndex(i))

		if addr == nil ||
			(hasIPv4 && addr.sa_family != C.AF_INET) ||
			!(addr.sa_family == C.AF_INET || addr.sa_family == C.AF_INET6) {
			continue
		}

		if addr.sa_family == C.AF_INET {
			addrIn := (*C.struct_sockaddr_in)(unsafe.Pointer(addr))
			C.inet_ntop(C.int(addrIn.sin_family), unsafe.Pointer(&addrIn.sin_addr),
				&host[0], C.socklen_t(hostnameMax))
			port = int(C.mgl_ntohs(addrIn.sin_port))
		} else {
			addrIn6 := (*C.struct_sockaddr_in6)(unsafe.Pointer(addr))
			C.inet_ntop(C.int(addrIn6.sin6_family), unsafe.Pointer(&addrIn6.sin6_addr),
				&host[0], C.socklen_t(hostnameMax))
			port = int(C.mgl_ntohs(addrIn6.sin6_port))
		}

		switch sa.class {
		case ServiceHtsp:
			b.sys.ts.Trace(trace.TRACE_DEBUG, "Bonjour",
				"Adding service htsp://%s:%d", C.GoString(&host[0]), port)
			b.sys.sdAddServiceHtsp(si, nameStr, C.GoString(&host[0]), port)

		case ServiceWebdav:
			txt := C.CFNetServiceGetTXTData(theService)
			dict := C.txtToDict(txt)

			if dict != 0 {
				key := C.bj_dictValue(dict, C.pathCFString())
				if key != nil {
					sn := int(C.bj_dataLen(key))
					if sn > len(pathbuf)-1 {
						sn = len(pathbuf) - 1
					}
					C.memcpy(unsafe.Pointer(&pathbuf[0]),
						C.bj_dataBytes(key), C.size_t(sn))
					path = C.GoString(&pathbuf[0])
				}

				key = C.bj_dictValue(dict, C.contentsCFString())
				if key != nil {
					sn := int(C.bj_dataLen(key))
					if sn > len(contentsbuf)-1 {
						sn = len(contentsbuf) - 1
					}
					C.memcpy(unsafe.Pointer(&contentsbuf[0]),
						C.bj_dataBytes(key), C.size_t(sn))
					contents = C.GoString(&contentsbuf[0])
				}
				C.CFRelease(C.CFTypeRef(dict))
			}

			b.sys.ts.Trace(trace.TRACE_DEBUG, "Bonjour",
				"Adding service webdav://%s:%d%s", C.GoString(&host[0]), port, path)
			b.sys.sdAddServiceWebdav(si, nameStr, C.GoString(&host[0]), port, path, contents)
		}
	}
}

/**
 * C: bonjour_browser_callback — info is the service_aux_t (cgo handle)
 */
func bonjourBrowserCallback(flags C.CFOptionFlags, bservice C.CFNetServiceRef,
	sa *bonjourServiceAux) {
	b := sa.bj

	if flags&C.kCFNetServiceFlagIsDomain != 0 {
		b.sys.ts.Trace(trace.TRACE_DEBUG, "Bonjour", "Browse domain, ignoring")
		return
	}

	var name, typ, domain [256]C.char
	C.getCString(C.CFNetServiceGetName(bservice), &name[0], 256)
	C.getCString(C.CFNetServiceGetType(bservice), &typ[0], 256)
	C.getCString(C.CFNetServiceGetDomain(bservice), &domain[0], 256)

	// unique enough? avahi has proto and interface too
	fullname := fmt.Sprintf("%s.%s.%s", C.GoString(&name[0]),
		C.GoString(&typ[0]), C.GoString(&domain[0]))

	action := "added"
	if flags&C.kCFNetServiceFlagRemove != 0 {
		action = "removed"
	}
	b.sys.ts.Trace(trace.TRACE_DEBUG, "Bonjour", "Browse service \"%s\" %s",
		fullname, action)

	// if exist, remove previous instance and resolver
	if si := siFind(b.services, fullname); si != nil {
		rservice := si.opaque.(*bonjourServiceAux).service
		C.unscheduleService(rservice)
		C.serviceClearClient(rservice)
		C.CFNetServiceCancel(rservice)
		C.CFRelease(C.CFTypeRef(rservice))
		b.sys.siDestroy(&b.services, si)
	}

	if flags&C.kCFNetServiceFlagRemove != 0 {
		// nothing
	} else {
		si := &serviceInstance{opaque: sa, id: fullname}
		si.handle = cgo.NewHandle(si)
		// C: LIST_INSERT_HEAD(&services, si, si_link)
		b.services = slices.Insert(b.services, 0, si)

		rservice := C.serviceCreate(bservice)
		C.serviceSetClient(rservice, C.uintptr_t(si.handle))
		C.scheduleService(rservice)

		sa.service = rservice // C: sa->sa_service = rservice

		var rerror C.CFStreamError
		if C.resolveWithTimeout(rservice, &rerror) == 0 {
			b.sys.ts.Trace(trace.TRACE_ERROR, "Bonjour",
				"CFNetServiceResolveWithTimeout (domain=%d, error=%ld)\n",
				int(rerror.domain), int64(rerror.error))

			C.unscheduleService(rservice)
			C.serviceClearClient(rservice)
			C.CFNetServiceCancel(rservice)
			C.CFRelease(C.CFTypeRef(rservice))
			b.sys.siDestroy(&b.services, si)
		}
	}
}

/**
 * C: bonjour_type_add
 */
func (b *bonjourDaemon) bonjourTypeAdd(typename string, class int) {
	var berror C.CFStreamError

	b.sys.ts.Trace(trace.TRACE_DEBUG, "Bonjour",
		"Starting search for type %s", typename)

	sa := &bonjourServiceAux{bj: b, class: class}
	sa.handle = cgo.NewHandle(sa) // C: calloc — freed only on search failure

	cftype := C.newCFString(C.CString(typename))
	browser := C.browserCreate(C.uintptr_t(sa.handle))
	C.scheduleBrowser(browser)

	if C.searchForServices(browser, cftype, &berror) == 0 {
		b.sys.ts.Trace(trace.TRACE_ERROR, "Bonjour",
			"CFNetServiceBrowserSearchForServices (domain=%d, error=%ld)\n",
			int(berror.domain), int64(berror.error))
		C.unscheduleBrowser(browser)
		C.CFNetServiceBrowserInvalidate(browser)
		C.CFNetServiceBrowserStopSearch(browser, &berror)
		C.CFRelease(C.CFTypeRef(browser))
		sa.handle.Delete() // C: free(sa)
	}

	C.CFRelease(C.CFTypeRef(cftype))
}

/**
 * C: bonjour_init
 */
func (s *System) bonjourStart() {
	b := &bonjourDaemon{sys: s}
	s.bonjour = b
	b.sys.ts.Trace(trace.TRACE_DEBUG, "Bonjour", "Start")

	b.bonjourTypeAdd("_webdav._tcp", ServiceWebdav)
	b.bonjourTypeAdd("_htsp._tcp", ServiceHtsp)
}
