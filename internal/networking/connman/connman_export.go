//go:build linux && rpi && connman && connmancgo

package connman

// connman_export.go — //export trampolines invoked by connman_glue.c.
// (cgo //export rule: this file's preamble holds declarations only.)

/*
#include "csrc/connman_glue.h"
*/
import "C"

import (
	"runtime/cgo"
)

// movianCourierCheck — C: glib_courier_check (prop_glib_courier.c:56-60)
// → prop_courier_check (prop_core.c:5436): true when the courier has
// pending notifications.
//
//export movianCourierCheck
func movianCourierCheck(h C.uintptr_t) C.gboolean {
	c := cgo.Handle(h).Value().(*courierShim)
	if c.c.PendingCount() > 0 {
		return C.TRUE
	}
	return C.FALSE
}

// movianCourierDispatch — C: glib_courier_dispatch
// (prop_glib_courier.c:67-72) → prop_courier_poll.
//
//export movianCourierDispatch
func movianCourierDispatch(h C.uintptr_t) C.gboolean {
	c := cgo.Handle(h).Value().(*courierShim)
	c.c.Poll()
	return C.TRUE
}

// movianMgrSignal — C: connman_mgr_signal (connman.c:480-497).
//
//export movianMgrSignal
func movianMgrSignal(sender *C.char, signal *C.char, params *C.GVariant) {
	connmanMgrSignal(C.GoString(sender), C.GoString(signal), params)
}

// movianSvcSignal — C: connman_svc_signal (connman.c:196-213).
//
//export movianSvcSignal
func movianSvcSignal(csh C.uintptr_t, sender *C.char, signal *C.char,
	params *C.GVariant) {
	cs := cgo.Handle(csh).Value().(*connmanService)
	connmanSvcSignal(cs, C.GoString(sender), C.GoString(signal), params)
}

// movianConnectDone — C: connman_connect_cb (connman.c:144-160).
//
//export movianConnectDone
func movianConnectDone(csh C.uintptr_t, res *C.GAsyncResult) {
	cs := cgo.Handle(csh).Value().(*connmanService)
	connmanConnectCb(cs, res)
}

// movianAgentMethodCall — C: handle_method_call (connman.c:625-666).
//
//export movianAgentMethodCall
func movianAgentMethodCall(conn *C.GDBusConnection, sender *C.char,
	path *C.char, iface *C.char, method *C.char, params *C.GVariant,
	inv *C.GDBusMethodInvocation) {
	agentMethodCall(C.GoString(method), params, inv)
}

// movianBusAcquired — C: on_bus_acquired (connman.c:683-708).
//
//export movianBusAcquired
func movianBusAcquired(conn *C.GDBusConnection, name *C.char,
	mgr *C.GDBusProxy) {
	onBusAcquired(conn, mgr)
}
