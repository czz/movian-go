//go:build linux && rpi && connman && connmancgo

package connman

// connman.go — C: src/networking/connman.c (+ src/prop/prop_glib_courier.c).
// ConnMan network manager client over the system D-Bus via GDBus/GLib,
// matching the C implementation 1:1: a dedicated thread runs a
// GMainContext+GMainLoop, a prop Courier is dispatched by a custom
// GSource, services are kept in the same ordered queue (TAILQ → slice),
// and the net.connman.Agent object is exported at /showtime/netagent.

/*
#include <gio/gio.h>
#include <stdlib.h>
#include "csrc/connman_glue.h"
*/
import "C"

import (
	"runtime"
	"runtime/cgo"
	"slices"
	"time"
	"unsafe"

	backendcore "github.com/czz/movian-go/internal/backend/core"
	eventpkg "github.com/czz/movian-go/internal/event"
	"github.com/czz/movian-go/internal/misc"
	"github.com/czz/movian-go/internal/nls"
	"github.com/czz/movian-go/internal/notifications"
	propcore "github.com/czz/movian-go/internal/prop"
	settings "github.com/czz/movian-go/internal/settings"
	"github.com/czz/movian-go/internal/trace"
)

// C: static prop_t *netconf_model / *service_nodes / *net_state /
// prop_courier_t *connman_courier / prop_t *connman_settings /
// int have_wifi (connman.c:33-38)
var (
	pm              *propcore.PropManager
	sm              *settings.SettingsManager
	bs              *backendcore.BackendSystem
	nm              *notifications.NotificationManager
	ts              *trace.TraceSystem
	netconfModel    *propcore.Prop
	serviceNodes    *propcore.Prop
	netState        *propcore.Prop
	connmanCourier  *propcore.Courier
	connmanSettings *propcore.Prop
	haveWifi        bool
	connmanServices []*connmanService // C: connman_services TAILQ
)

// C: connman_service_t (connman.c:76-96). cs_proxy/cs_input_req_inv are
// C objects; cs is a Go object referenced from C user_data via handle.
type connmanService struct {
	refcount int                    // C: cs_refcount
	prop     *propcore.Prop         // C: cs_prop
	path     string                 // C: cs_path
	sub      *propcore.Subscription // C: cs_sub
	proxy    *C.GDBusProxy          // C: cs_proxy
	name     string                 // C: cs_name

	inputReqProp         *propcore.Prop           // C: cs_input_req_prop
	inputReqSub          *propcore.Subscription   // C: cs_input_req_sub
	inputReqInv          *C.GDBusMethodInvocation // C: cs_input_req_inv
	inputReqWantIdentity bool                     // C: cs_input_req_want_identity

	handle cgo.Handle // this cs as C user_data
}

// courierShim pairs the Go Courier with its glib GSource.
// C: glib_courier_t (prop_glib_courier.c:24-28).
type courierShim struct {
	c   *propcore.Courier
	src *C.GSource
}

/* ---- interned C strings ---- */

var (
	cNetConnman        = C.CString("net.connman")
	cNetConnmanManager = C.CString("net.connman.Manager")
	cNetConnmanService = C.CString("net.connman.Service")
	cNetConnmanTech    = C.CString("net.connman.Technology")
	cSlash             = C.CString("/")
	cNetagentPath      = C.CString("/showtime/netagent")
	cWifiTechPath      = C.CString("/net/connman/technology/wifi")
	cRegisterAgent     = C.CString("RegisterAgent")
	cGetServices       = C.CString("GetServices")
	cGetTechnologies   = C.CString("GetTechnologies")
	cGetProperties     = C.CString("GetProperties")
	cDisconnect        = C.CString("Disconnect")
	cSetProperty       = C.CString("SetProperty")
	cPowered           = C.CString("Powered")
	cPrevPassphrase    = C.CString("PreviousPassphrase")
	cIdentity          = C.CString("Identity")
	cPassphrase        = C.CString("Passphrase")
	cName              = C.CString("Name")
	cState             = C.CString("State")
	cErrCanceled       = C.CString("net.connman.Agent.Error.Canceled")
	cCanceledByUser    = C.CString("Canceled by user")
	cUnknownService    = C.CString("Unknown service")
)

func gostring(s *C.gchar) string { return C.GoString((*C.char)(s)) }

func gvariantPrint(v *C.GVariant) string {
	s := C.g_variant_print(v, C.TRUE)
	out := C.GoString((*C.char)(s))
	C.g_free(C.gpointer(unsafe.Pointer(s)))
	return out
}

// propGetString — C: prop_get_string(p, name, NULL) → rstr_get().
func propGetString(p *propcore.Prop, name string) string {
	c := p.GetChild(name)
	if c == nil {
		return ""
	}
	return pm.GetString(c, "")
}

/*
 * TAILQ helpers over connmanServices — C: TAILQ_INSERT_HEAD /
 * TAILQ_INSERT_AFTER / TAILQ_REMOVE / TAILQ_NEXT / TAILQ_FOREACH.
 */
func serviceIndex(cs *connmanService) int {
	for i, s := range connmanServices {
		if s == cs {
			return i
		}
	}
	return -1
}

// C: after == NULL → TAILQ_INSERT_HEAD, else TAILQ_INSERT_AFTER.
func serviceInsert(cs, after *connmanService) {
	if after == nil {
		connmanServices = slices.Insert(connmanServices, 0, cs)
		return
	}
	i := serviceIndex(after)
	if i < 0 {
		return
	}
	connmanServices = append(connmanServices, nil)
	copy(connmanServices[i+2:], connmanServices[i+1:])
	connmanServices[i+1] = cs
}

// C: TAILQ_REMOVE.
func serviceRemove(cs *connmanService) {
	i := serviceIndex(cs)
	if i < 0 {
		return
	}
	connmanServices = slices.Delete(connmanServices, i, i+1)
}

// C: TAILQ_NEXT(cs, cs_link).
func serviceNext(cs *connmanService) *connmanService {
	i := serviceIndex(cs)
	if i < 0 || i+1 >= len(connmanServices) {
		return nil
	}
	return connmanServices[i+1]
}

// connmanServiceFind — C: connman_service_find (connman.c:103-111).
func connmanServiceFind(path string) *connmanService {
	for _, cs := range connmanServices {
		if cs.path == path {
			return cs
		}
	}
	return nil
}

// connmanStopInputRequest — C: connman_stop_input_request
// (connman.c:118-132).
func connmanStopInputRequest(cs *connmanService) {
	if cs.inputReqSub != nil {
		cs.inputReqSub.Unsubscribe()
		cs.inputReqSub = nil
	}

	if cs.inputReqProp != nil {
		pm.Destroy(cs.inputReqProp)
		cs.inputReqProp = nil
	}

	if cs.inputReqInv != nil {
		C.g_object_unref(C.gpointer(unsafe.Pointer(cs.inputReqInv)))
		cs.inputReqInv = nil
	}
}

// connmanConnectCb — C: connman_connect_cb (connman.c:144-160).
// Called via movianConnectDone export from the async D-Bus completion.
func connmanConnectCb(cs *connmanService, res *C.GAsyncResult) {
	var err *C.GError
	v := C.g_dbus_proxy_call_finish(cs.proxy, res, &err)

	if v == nil {
		connmanStopInputRequest(cs)
		ts.Trace(trace.TRACE_ERROR, "CONNMAN",
			"Unable to connect to %s -- %s", cs.path,
			gostring(err.message))
		C.g_error_free(err)
		return
	}
	C.g_variant_unref(v)
}

// connmanServiceConnect — C: connman_service_connect (connman.c:163-173).
func connmanServiceConnect(cs *connmanService) {
	var err *C.GError
	v := C.g_dbus_proxy_call_sync(cs.proxy, cDisconnect, nil,
		C.G_DBUS_CALL_FLAGS_NONE, -1, nil, &err)
	if v == nil {
		C.g_error_free(err)
	} else {
		C.g_variant_unref(v)
	}

	ts.Trace(trace.TRACE_DEBUG, "CONNMAN",
		"User request connect to %s", cs.path)

	C.movian_call_connect(cs.proxy, C.uintptr_t(cs.handle))
}

// connmanServiceEvent — C: connman_service_event (connman.c:176-182).
// PROP_TAG_CALLBACK_EVENT on cs_prop → EventExtEvent delivery.
func connmanServiceEvent(opaque any, eventType propcore.EventType,
	args ...any) {
	cs := opaque.(*connmanService)
	if eventType != propcore.EventExtEvent || len(args) == 0 {
		return
	}
	e, ok := args[0].(*eventpkg.Event)
	if !ok || e == nil {
		return
	}
	// C: event_is_type(e, EVENT_DYNAMIC_ACTION)
	if e.GetType() == eventpkg.EVENT_DYNAMIC_ACTION {
		connmanServiceConnect(cs)
	}
}

// connmanSvcSignal — C: connman_svc_signal (connman.c:196-213).
// Invoked via the movianSvcSignal export on the connman thread.
func connmanSvcSignal(cs *connmanService, sender, signal string,
	params *C.GVariant) {
	ts.Trace(trace.TRACE_DEBUG, "CONNMAN",
		"Service-signal %s %s from %s", cs.path, signal, sender)
	ts.Trace(trace.TRACE_DEBUG, "CONNMAN", "%s", gvariantPrint(params))

	if signal == "PropertyChanged" {
		propSetFromTuple(params,
			pm.CreateEx(cs.prop, "metadata", nil, false, false))
	}
}

// connmanServiceRelease — C: connman_service_release (connman.c:217-228).
func connmanServiceRelease(cs *connmanService) {
	cs.refcount--
	if cs.refcount > 0 {
		return
	}
	C.g_object_unref(C.gpointer(unsafe.Pointer(cs.proxy)))
	cs.handle.Delete()
	// C: free(cs_name); free(cs_path); free(cs) — GC reclaims.
}

// connmanServiceDestroy — C: connman_service_destroy (connman.c:232-241).
func connmanServiceDestroy(cs *connmanService) {
	connmanStopInputRequest(cs)
	cs.sub.Unsubscribe()
	serviceRemove(cs)
	pm.Destroy(cs.prop)
	cs.prop = nil
	connmanServiceRelease(cs)
}

// inputReqEvent — C: input_req_event (connman.c:245-287).
// PROP_TAG_CALLBACK_EVENT on the auth popup's eventSink.
func inputReqEvent(opaque any, eventType propcore.EventType,
	args ...any) {
	cs := opaque.(*connmanService)
	if eventType != propcore.EventExtEvent || len(args) == 0 {
		return
	}
	e, ok := args[0].(*eventpkg.Event)
	if !ok || e == nil {
		return
	}
	if cs.inputReqInv == nil {
		return
	}

	if e.IsAction(eventpkg.ACTION_OK) {
		username := propGetString(cs.inputReqProp, "username")
		password := propGetString(cs.inputReqProp, "password")

		builder := C.movian_vardict_builder_new()

		cpw := C.CString(password)
		C.movian_builder_add_sv_string(builder, cPassphrase, cpw)
		C.free(unsafe.Pointer(cpw))

		if cs.inputReqWantIdentity {
			cu := C.CString(username)
			C.movian_builder_add_sv_string(builder, cIdentity, cu)
			C.free(unsafe.Pointer(cu))
		}

		result := C.movian_variant_new_tuple_builder(builder)

		ts.Trace(trace.TRACE_DEBUG, "CONNMAN",
			"Auth response: %s", gvariantPrint(result))

		C.g_dbus_method_invocation_return_value(cs.inputReqInv, result)

		C.g_variant_builder_unref(builder)
		connmanStopInputRequest(cs)
	}

	if e.IsAction(eventpkg.ACTION_CANCEL) {
		C.g_dbus_method_invocation_return_dbus_error(cs.inputReqInv,
			cErrCanceled, cCanceledByUser)
		connmanStopInputRequest(cs)
	}
}

// agentRequestInput — C: agent_request_input (connman.c:299-345).
func agentRequestInput(cs *connmanService, req *C.GVariant,
	inv *C.GDBusMethodInvocation) {
	ts.Trace(trace.TRACE_INFO, "CONNMAN",
		"Requesting credentials for %s", cs.path)
	ts.Trace(trace.TRACE_DEBUG, "CONNMAN",
		"RequestInput: %s", gvariantPrint(req))

	p := pm.CreateRoot("")

	pm.SetVEx(nil, p, "type", "auth")
	pm.SetVEx(nil, p, "id", cs.path)
	pm.SetVEx(nil, p, "source", "Network")

	prev := C.g_variant_lookup_value(req, cPrevPassphrase, nil)
	if prev != nil {
		pm.SetVEx(nil, p, "reason", "Password incorrect")
	} else {
		pm.SetVEx(nil, p, "reason", "Password needed")
	}

	identity := C.g_variant_lookup_value(req, cIdentity, nil)
	cs.inputReqWantIdentity = identity != nil
	pm.SetVEx(nil, p, "disableUsername",
		misc.BoolToInt(!cs.inputReqWantIdentity))
	pm.SetVEx(nil, p, "disableDomain", 1)

	r := pm.CreateEx(p, "eventSink", nil, false, false)

	// C: prop_subscribe(0, PROP_TAG_CALLBACK_EVENT, input_req_event, cs,
	//   PROP_TAG_NAMED_ROOT, r, "popup", PROP_TAG_COURIER,
	//   connman_courier, NULL) — no name vector → subscribes to r.
	cs.inputReqSub = r.Subscribe(inputReqEvent, cs,
		propcore.SubCourier{C: connmanCourier})

	cs.inputReqProp = p

	// C: prop_set_parent(p, prop_create(prop_get_global(), "popups"))
	// — abort() when the popup root is a zombie.
	popups := pm.CreateEx(pm.GetGlobal(), "popups", nil, false, false)
	if pm.SetParentEx(p, popups, nil, "") != 0 {
		panic("connman: popup root is a zombie") // C: abort()
	}

	cs.inputReqInv = inv
	C.g_object_ref(C.gpointer(unsafe.Pointer(inv)))
}

// updateService — C: update_service (connman.c:352-434).
func updateService(v *C.GVariant, path string,
	after *connmanService) *connmanService {
	cs := connmanServiceFind(path)

	if cs == nil {
		var err *C.GError
		cpath := C.CString(path)
		proxy := C.g_dbus_proxy_new_for_bus_sync(
			C.G_BUS_TYPE_SYSTEM,
			C.G_DBUS_PROXY_FLAGS_DO_NOT_AUTO_START,
			nil, cNetConnman, cpath, cNetConnmanService, nil, &err)
		C.free(unsafe.Pointer(cpath))
		if proxy == nil {
			ts.Trace(trace.TRACE_ERROR, "CONNMAN",
				"Unable to connect to service %s -- %s",
				path, gostring(err.message))
			C.g_error_free(err)
			return nil
		}

		cs = &connmanService{
			refcount: 1,
			proxy:    proxy,
		}
		cs.handle = cgo.NewHandle(cs)

		serviceInsert(cs, after)

		next := serviceNext(cs)

		cs.prop = pm.CreateRoot(path)

		// C: prop_subscribe(0, PROP_TAG_CALLBACK_EVENT,
		//   connman_service_event, cs, PROP_TAG_ROOT, cs->cs_prop,
		//   PROP_TAG_COURIER, connman_courier, NULL)
		cs.sub = cs.prop.Subscribe(connmanServiceEvent, cs,
			propcore.SubCourier{C: connmanCourier})

		C.movian_connect_svc_signal(cs.proxy, C.uintptr_t(cs.handle))

		// Insert at correct position
		var before *propcore.Prop
		if next != nil {
			before = next.prop
		}
		if pm.SetParentEx(cs.prop, serviceNodes, before, "") != 0 {
			panic("connman: prop_set_parent_ex failed") // C: abort()
		}

		m := pm.CreateEx(cs.prop, "metadata", nil, false, false)
		pm.Link(pm.CreateEx(m, "name", nil, false, false),
			pm.CreateEx(m, "title", nil, false, false),
			nil, false, false)
		pm.SetVEx(nil, cs.prop, "type", "network")
	} else {
		// Possibly move
		serviceRemove(cs)
		serviceInsert(cs, after)

		next := serviceNext(cs)

		var before *propcore.Prop
		if next != nil {
			before = next.prop
		}
		pm.Move(cs.prop, before)
	}

	// Update metadata
	propSetFromVardict(v, pm.CreateEx(cs.prop, "metadata", nil, false, false))

	// C: GVariant *name = g_variant_lookup_value(v, "Name", NULL);
	//   const gchar *val = name ? g_variant_get_string(name, NULL) : NULL;
	//   if(val) mystrset(&cs->cs_name, val);
	name := C.g_variant_lookup_value(v, cName, nil)
	if name != nil {
		if s := C.g_variant_get_string(name, nil); s != nil {
			cs.name = C.GoString(s)
		}
	}
	return cs
}

// servicesChanged — C: services_changed (connman.c:438-474).
func servicesChanged(params *C.GVariant) {
	var prev *connmanService

	del := C.g_variant_get_child_value(params, 1)
	for i, n := 0, int(C.g_variant_n_children(del)); i < n; i++ {
		v := C.g_variant_get_child_value(del, C.gsize(i))
		if C.GoString(C.g_variant_get_type_string(v)) == "o" {
			name := gostring(C.g_variant_get_string(v, nil))
			ts.Trace(trace.TRACE_DEBUG, "CONNMAN",
				"Deleted network %s", name)
			if cs := connmanServiceFind(name); cs != nil {
				connmanServiceDestroy(cs)
			}
		}
	}

	add := C.g_variant_get_child_value(params, 0)
	for i, n := 0, int(C.g_variant_n_children(add)); i < n; i++ {
		v := C.g_variant_get_child_value(add, C.gsize(i))
		id := gostring(C.g_variant_get_string(
			C.g_variant_get_child_value(v, 0), nil))

		cs := updateService(C.g_variant_get_child_value(v, 1), id, prev)
		if cs != nil {
			prev = cs
		}
	}
}

// connmanMgrSignal — C: connman_mgr_signal (connman.c:480-497).
// Invoked via the movianMgrSignal export on the connman thread.
func connmanMgrSignal(sender, signal string, params *C.GVariant) {
	ts.Trace(trace.TRACE_DEBUG, "CONNMAN",
		"Manager-signal %s from %s", signal, sender)
	ts.Trace(trace.TRACE_DEBUG, "CONNMAN", "%s", gvariantPrint(params))

	if signal == "ServicesChanged" {
		servicesChanged(params)
	}
}

// connmanGetServices — C: connman_getservices (connman.c:502-541).
func connmanGetServices(connman *C.GDBusProxy) {
	var err *C.GError

	v := C.g_dbus_proxy_call_sync(connman, cGetServices, nil,
		C.G_DBUS_CALL_FLAGS_NONE, -1, nil, &err)

	if v == nil {
		ts.Trace(trace.TRACE_ERROR, "CONNMAN",
			"Unable to GetServices -- %s", gostring(err.message))
		C.g_error_free(err)
		return
	}

	list := C.g_variant_get_child_value(v, 0)
	var prev *connmanService
	if list != nil {
		pm.DestroyChilds(serviceNodes)

		numServices := int(C.g_variant_n_children(list))
		for i := range numServices {
			svc := C.g_variant_get_child_value(list, C.gsize(i))
			path := gostring(C.g_variant_get_string(
				C.g_variant_get_child_value(svc, 0), nil))

			cs := updateService(
				C.g_variant_get_child_value(svc, 1), path, prev)
			if cs != nil {
				prev = cs
			}
		}
	}
	C.g_variant_unref(v)
}

// connmanGetTechnologies — C: connman_gettechnologies (connman.c:545-585).
func connmanGetTechnologies(connman *C.GDBusProxy) {
	var err *C.GError

	v := C.g_dbus_proxy_call_sync(connman, cGetTechnologies, nil,
		C.G_DBUS_CALL_FLAGS_NONE, -1, nil, &err)

	if v == nil {
		ts.Trace(trace.TRACE_ERROR, "CONNMAN",
			"Unable to GetTechnologies -- %s", gostring(err.message))
		C.g_error_free(err)
		return
	}

	ts.Trace(trace.TRACE_DEBUG, "CONNMAN",
		"Technologies: %s", gvariantPrint(v))

	list := C.g_variant_get_child_value(v, 0)
	if list != nil {
		numTech := int(C.g_variant_n_children(list))
		for i := range numTech {
			tech := C.g_variant_get_child_value(list, C.gsize(i))
			path := gostring(C.g_variant_get_string(
				C.g_variant_get_child_value(tech, 0), nil))
			if path == "/net/connman/technology/wifi" {
				haveWifi = true
			}
		}
	}
	C.g_variant_unref(v)
}

// connmanGetProperties — C: connman_getpropreties (connman.c:589-616).
func connmanGetProperties(connman *C.GDBusProxy) {
	var err *C.GError

	v := C.g_dbus_proxy_call_sync(connman, cGetProperties, nil,
		C.G_DBUS_CALL_FLAGS_NONE, -1, nil, &err)

	if v == nil {
		ts.Trace(trace.TRACE_ERROR, "CONNMAN",
			"Unable to GetProperties -- %s", gostring(err.message))
		C.g_error_free(err)
		return
	}

	dict := C.g_variant_get_child_value(v, 0)
	if dict != nil {
		state := C.g_variant_lookup_value(dict, cState, nil)
		if state != nil {
			val := gostring(C.g_variant_get_string(state, nil))
			pm.SetStringEx(netState, nil, val, propcore.StringUTF8)
		}
	}
	C.g_variant_unref(v)
}

// connmanServiceFromParams — C: connman_service_from_params
// (connman.c:618-626).
func connmanServiceFromParams(param *C.GVariant) *connmanService {
	path := gostring(C.g_variant_get_string(
		C.g_variant_get_child_value(param, 0), nil))
	if path == "" {
		return nil
	}
	return connmanServiceFind(path)
}

// agentMethodCall — C: handle_method_call (connman.c:625-666).
// Invoked via the movianAgentMethodCall export.
func agentMethodCall(method string, param *C.GVariant,
	inv *C.GDBusMethodInvocation) {
	ts.Trace(trace.TRACE_DEBUG, "CONNMAN",
		"agent method call: %s", method)

	switch method {
	case "ReportError":
		cs := connmanServiceFromParams(param)

		msg := gostring(C.g_variant_get_string(
			C.g_variant_get_child_value(param, 1), nil))

		name := "Unknown network"
		if cs != nil {
			name = cs.name
		}
		// C: notify_add(NULL, NOTIFY_ERROR, NULL, 3,
		//   rstr_alloc("%s\n%s"), cs ? cs->cs_name : "Unknown network",
		//   msg)
		if nm != nil {
			nm.NotifyAdd(nil, notifications.NotifyError, "", 3,
				"%s\n%s", name, msg)
		}
		C.g_dbus_method_invocation_return_value(inv, nil)

	case "RequestInput":
		cs := connmanServiceFromParams(param)
		if cs == nil {
			C.movian_inv_return_error_invalid_args(inv,
				cUnknownService)
			return
		}
		agentRequestInput(cs,
			C.g_variant_get_child_value(param, 1), inv)

	default:
		cmethod := C.CString(method)
		C.movian_inv_return_error_unknown_method(inv, cmethod)
		C.free(unsafe.Pointer(cmethod))
	}
}

// onBusAcquired — C: on_bus_acquired (connman.c:683-708).
// Invoked via the movianBusAcquired export.
func onBusAcquired(conn *C.GDBusConnection, connman *C.GDBusProxy) {
	var err *C.GError

	// C: g_dbus_node_info_new_for_xml + g_dbus_connection_register_object
	C.movian_register_agent_object(conn)

	// C: params = g_variant_new("(o)", "/showtime/netagent")
	params := C.movian_variant_new_o(cNetagentPath)
	v := C.g_dbus_proxy_call_sync(connman, cRegisterAgent, params,
		C.G_DBUS_CALL_FLAGS_NONE, -1, nil, &err)
	if v == nil {
		ts.Trace(trace.TRACE_ERROR, "CONNMAN",
			"Unable to register agent -- %s", gostring(err.message))
		C.g_error_free(err)
		return
	}
	C.g_variant_unref(v)
}

// setWifiEnable — C: set_wifi_enable (connman.c:730-769).
// SETTING_CALLBACK for the "Enable Wi-Fi" bool setting.
func setWifiEnable(opaque any, value any) {
	enable := 0
	switch v := value.(type) {
	case int:
		enable = v
	case bool:
		if v {
			enable = 1
		}
	case float64:
		enable = int(v)
	}

	var err *C.GError
	proxy := C.g_dbus_proxy_new_for_bus_sync(C.G_BUS_TYPE_SYSTEM,
		C.G_DBUS_PROXY_FLAGS_DO_NOT_AUTO_START, nil,
		cNetConnman, cWifiTechPath, cNetConnmanTech, nil, &err)

	if proxy == nil {
		ts.Trace(trace.TRACE_ERROR, "CONNMAN",
			"Unable to connect to technology %s -- %s",
			"/net/connman/technology/wifi", gostring(err.message))
		C.g_error_free(err)
		return
	}

	params := C.movian_variant_new_sv_bool(cPowered,
		C.gboolean(enable))

	v := C.g_dbus_proxy_call_sync(proxy, cSetProperty, params,
		C.G_DBUS_CALL_FLAGS_NONE, -1, nil, &err)

	if v == nil {
		if err.code != 36 {
			ts.Trace(trace.TRACE_ERROR, "CONNMAN",
				"Unable to set power %s -- %s",
				"/net/connman/technology/wifi",
				gostring(err.message))
		}
		C.g_error_free(err)
	} else {
		C.g_variant_unref(v)
	}
	C.g_object_unref(C.gpointer(unsafe.Pointer(proxy)))
}

// glibCourierCreate — C: glib_courier_create (prop_glib_courier.c:89-97).
// The Go Courier's notify fn pokes the GMainContext; the GSource (built
// in connman_glue.c) polls it from the main loop.
func glibCourierCreate(ctx *C.GMainContext) *propcore.Courier {
	shim := &courierShim{}
	// C: prop_courier_create_notify(glib_courier_wakeup, ctx) — the
	// wakeup is g_main_context_wakeup(ctx).
	shim.c = propcore.NewCourierNotify("connman",
		func(opaque any) {
			C.g_main_context_wakeup(ctx)
		}, nil)
	shim.src = C.movian_courier_source_new(
		C.uintptr_t(cgo.NewHandle(shim)))
	C.g_source_attach(shim.src, ctx)
	return shim.c
}

// connmanThread — C: connman_thread (connman.c:772-823). Runs the glib
// main loop that drives both D-Bus signals and the prop courier.
func connmanThread() {
	// The GMainContext is thread-default for this OS thread — pin the
	// goroutine (C: dedicated hts_thread).
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	ctx := C.g_main_context_new()
	mainloop := C.g_main_loop_new(ctx, C.FALSE)
	connmanCourier = glibCourierCreate(ctx)

	C.g_main_context_push_thread_default(ctx)

	for {
		var err *C.GError

		mgr := C.g_dbus_proxy_new_for_bus_sync(C.G_BUS_TYPE_SYSTEM,
			C.G_DBUS_PROXY_FLAGS_DO_NOT_AUTO_START, nil,
			cNetConnman, cSlash, cNetConnmanManager, nil, &err)
		if mgr == nil {
			ts.Trace(trace.TRACE_ERROR, "CONNMAN",
				"Unable to connect to connman -- %s",
				gostring(err.message))
			C.g_error_free(err)
			time.Sleep(5 * time.Second) // C: sleep(5); goto again
			continue
		}

		// C: g_signal_connect(mgr, "g-signal", connman_mgr_signal)
		C.movian_connect_mgr_signal(mgr)

		// C: g_bus_own_name(..., on_bus_acquired, ..., mgr, NULL)
		C.movian_own_agent_name(mgr)

		connmanGetProperties(mgr)
		connmanGetServices(mgr)
		connmanGetTechnologies(mgr)

		if haveWifi {
			// C: setting_create(SETTING_BOOL, connman_settings,
			//   SETTINGS_INITIAL_UPDATE,
			//   SETTING_TITLE(_p("Enable Wi-Fi")),
			//   SETTING_CALLBACK(set_wifi_enable, NULL),
			//   SETTING_STORE("connman", "enable_wifi"), NULL)
			sm.SettingCreate(settings.SettingBool, connmanSettings,
				settings.SettingsInitialUpdate,
				settings.SettingTagTitle, sm.P("Enable Wi-Fi"),
				settings.SettingTagCallback, setWifiEnable, nil,
				settings.SettingTagStore, "connman", "enable_wifi")
		}

		C.g_main_loop_run(mainloop)
		return
	}
}

const myURL = "settings:networkconnections" // C: MYURL

// beNetconfCanhandle — C: be_netconf_canhandle (connman.c:893-899).
func beNetconfCanhandle(url string) int {
	if url == myURL {
		return 20
	}
	return 0
}

// beNetconfOpen — C: be_netconf_open (connman.c:903-908).
func beNetconfOpen(page any, url string, sync bool) error {
	p, ok := page.(*propcore.Prop)
	if !ok || p == nil {
		return nil
	}
	// C: prop_link(netconf_model, prop_create(page, "model"))
	pm.Link(netconfModel, pm.CreateEx(p, "model", nil, false, false),
		nil, false, false)
	return nil
}

// System — rollback variant: this cgo implementation is still
// process-scoped (C callbacks without userdata) — the returned handle
// is a marker only; documented limit, see godbus System for the
// per-instance state.
type System struct{}

// Start — C: connman_init (connman.c:825-887). Requires the prop,
// settings and backend managers (C uses globals).
func Start(propMgr *propcore.PropManager,
	settingsMgr *settings.SettingsManager,
	backendSys *backendcore.BackendSystem,
	notifMgr *notifications.NotificationManager) *System {
	pm = propMgr
	sm = settingsMgr
	bs = backendSys
	nm = notifMgr
	ts = backendSys.TraceSystem()

	// C: TAILQ_INIT(&connman_services)
	connmanServices = nil

	netconfModel = pm.CreateRoot("")
	pc := propcore.PropConcatCreate(pm,
		pm.CreateEx(netconfModel, "nodes", nil, false, false))

	netState = pm.CreateEx(netconfModel, "status", nil, false, false)
	pm.SetVEx(nil, netconfModel, "type", "directory")

	m := pm.CreateEx(netconfModel, "metadata", nil, false, false)
	// C: prop_set(m, "title", PROP_SET_RSTRING,
	//   _("Network connections"))
	pm.SetVEx(nil, m, "title", nls.GetRString("Network connections"))

	// service_nodes contains list of items we receive from connman
	serviceNodes = pm.CreateRoot("")
	pc.AddSource(serviceNodes, nil)

	// settings
	connmanSettings = pm.CreateRoot("")

	delim := pm.CreateRoot("")
	pm.SetStringEx(pm.CreateEx(delim, "type", nil, false, false),
		nil, "separator", propcore.StringUTF8)
	pc.AddSource(pm.CreateEx(connmanSettings, "nodes", nil, false, false),
		delim)

	// C: settings_add_url(gconf.settings_network,
	//   _p("Network connections"), NULL, NULL, NULL, MYURL,
	//   SETTINGS_FIRST)
	sm.AddUrl(sm.Network(), sm.P("Network connections"), "", "", nil,
		myURL, settings.SettingsFirst)

	// C: BE_REGISTER(netconf) — static backend registration
	bs.Register(&backendcore.Backend{
		CanHandle: beNetconfCanhandle,
		Open:      beNetconfOpen,
	})

	// C: hts_thread_create_detached("connman", connman_thread, NULL,
	//   THREAD_PRIO_BGTASK)
	go connmanThread()
	return &System{}
}

// Stop — the cgo/GIO rollback keeps upstream lifecycle (thread runs
// until process exit); no-op placeholder for signature parity with
// the godbus variant.
func (s *System) Stop() {}
