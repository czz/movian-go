//go:build linux && rpi && connman && !connmancgo

package connman

// connman_godbus.go — pure-Go D-Bus variant of connman.go (Phase 10
// experiment). Same ConnMan protocol and prop/settings wiring as the
// cgo/GDBus version, but over github.com/godbus/dbus instead of
// gio/GVariant. Default when -tags connman; the canonical cgo
// implementation (connman.go) is selected with -tags "connman connmancgo".
//
// DECISION: after a successful real-hardware test (connmand running,
// service list + auth popup + wifi toggle), the cgo/GDBus files are to
// be REMOVED and this becomes the only implementation: connman.go,
// prop_gvariant.go, connman_export.go, connman_glue.c/.h, z_cgo_gio.go
// get deleted and the `!godbus`/`godbus` tag split is dropped.
//
// Mapping vs the cgo version:
//   GDBusProxy call_sync      → BusObject.Call
//   g_dbus_proxy_call async   → BusObject.Go + s.workCh dispatch
//   g-signal trampolines      → conn.Signal channel + AddMatchSignal
//   GSource courier pump      → NewCourierNotify → wakeCh → Poll
//   GDBusMethodInvocation     → blocking exported method returning
//                               (map, *dbus.Error); inputReqResp chan
//   GVariant                  → dbus.Variant

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"

	backendcore "github.com/czz/movian-go/internal/backend/core"
	eventpkg "github.com/czz/movian-go/internal/event"
	"github.com/czz/movian-go/internal/misc"
	"github.com/czz/movian-go/internal/nls"
	"github.com/czz/movian-go/internal/notifications"
	propcore "github.com/czz/movian-go/internal/prop"
	settings "github.com/czz/movian-go/internal/settings"
	"github.com/czz/movian-go/internal/trace"
)

// Same globals as connman.go (C: connman.c:33-38).
// System — per-instance ConnMan state (was package globals). Owned by
// the composition root (platform state); Start returns it.
type System struct {
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

	systemConn   *dbus.Conn
	pendingInput *connmanService // agent request awaiting user reply

	workCh chan func()       // posts onto the connman goroutine
	sigCh  chan *dbus.Signal // D-Bus signals drained by connmanThread

	done     chan struct{} // closed by Stop — connmanThread exits
	stopOnce sync.Once
	stopped  chan struct{} // closed when connmanThread returned
}

const (
	busName       = "net.connman"
	managerIface  = "net.connman.Manager"
	serviceIface  = "net.connman.Service"
	techIface     = "net.connman.Technology"
	agentIface    = "net.connman.Agent"
	agentPath     = dbus.ObjectPath("/showtime/netagent")
	agentBusName  = "com.showtimemediacenter.showtime.network.agent"
	wifiTechPath  = dbus.ObjectPath("/net/connman/technology/wifi")
	errCanceled   = "net.connman.Agent.Error.Canceled"
	errInvalidArg = "org.freedesktop.DBus.Error.InvalidArgs"
)

// C: connman_service_t (connman.c:76-96).
type connmanService struct {
	sys      *System // owning instance (was implicit package global)
	refcount int
	prop     *propcore.Prop
	path     string // C: cs_path — also the D-Bus object path
	sub      *propcore.Subscription
	name     string

	inputReqProp         *propcore.Prop
	inputReqSub          *propcore.Subscription
	inputReqWantIdentity bool
	inputReqResp         chan agentReply // replaces cs_input_req_inv
}

// agentReply carries the deferred RequestInput answer (the cgo version
// held a GDBusMethodInvocation instead).
type agentReply struct {
	fields map[string]dbus.Variant
	err    *dbus.Error
}

// svcObj — the BusObject for this service (lazy; replaces GDBusProxy).
func (cs *connmanService) svcObj() dbus.BusObject {
	s := cs.sys
	return s.systemConn.Object(busName, dbus.ObjectPath(cs.path))
}

// propGetString — C: prop_get_string(p, name, NULL) → rstr_get().
func (s *System) propGetString(p *propcore.Prop, name string) string {
	c := p.GetChild(name)
	if c == nil {
		return ""
	}
	return s.pm.GetString(c, "")
}

/* ---- TAILQ helpers (C: TAILQ_* over connman_services) ---- */

func (s *System) serviceIndex(cs *connmanService) int {
	for i, s := range s.connmanServices {
		if s == cs {
			return i
		}
	}
	return -1
}

func (s *System) serviceInsert(cs, after *connmanService) {
	if after == nil {
		s.connmanServices = append([]*connmanService{cs}, s.connmanServices...)
		return
	}
	i := s.serviceIndex(after)
	if i < 0 {
		return
	}
	s.connmanServices = append(s.connmanServices, nil)
	copy(s.connmanServices[i+2:], s.connmanServices[i+1:])
	s.connmanServices[i+1] = cs
}

func (s *System) serviceRemove(cs *connmanService) {
	if i := s.serviceIndex(cs); i >= 0 {
		s.connmanServices = append(s.connmanServices[:i],
			s.connmanServices[i+1:]...)
	}
}

func (s *System) serviceNext(cs *connmanService) *connmanService {
	i := s.serviceIndex(cs)
	if i < 0 || i+1 >= len(s.connmanServices) {
		return nil
	}
	return s.connmanServices[i+1]
}

// connmanServiceFind — C: connman_service_find (connman.c:103-111).
func (s *System) connmanServiceFind(path string) *connmanService {
	for _, cs := range s.connmanServices {
		if cs.path == path {
			return cs
		}
	}
	return nil
}

// connmanStopInputRequest — C: connman_stop_input_request
// (connman.c:118-132).
func (s *System) connmanStopInputRequest(cs *connmanService) {
	if cs.inputReqSub != nil {
		cs.inputReqSub.Unsubscribe()
		cs.inputReqSub = nil
	}
	if cs.inputReqProp != nil {
		s.pm.Destroy(cs.inputReqProp)
		cs.inputReqProp = nil
	}
	cs.inputReqResp = nil
	if s.pendingInput == cs {
		s.pendingInput = nil
	}
}

// connmanConnectCb — C: connman_connect_cb (connman.c:144-160).
func (s *System) connmanConnectCb(cs *connmanService, call *dbus.Call) {
	if call.Err != nil {
		s.connmanStopInputRequest(cs)
		s.ts.Trace(trace.TRACE_ERROR, "CONNMAN",
			"Unable to connect to %s -- %s", cs.path, call.Err)
	}
}

// connmanServiceConnect — C: connman_service_connect
// (connman.c:163-173). Disconnect sync, then async Connect.
func (s *System) connmanServiceConnect(cs *connmanService) {
	cs.svcObj().Call(serviceIface+".Disconnect", 0)

	s.ts.Trace(trace.TRACE_DEBUG, "CONNMAN",
		"User request connect to %s", cs.path)

	// C: g_dbus_proxy_call(proxy, "Connect", ..., 600s timeout, cb, cs)
	ch := make(chan *dbus.Call, 1)
	cs.svcObj().Go(serviceIface+".Connect", 0, ch)
	go func() {
		call := <-ch
		select {
		case s.workCh <- func() { s.connmanConnectCb(cs, call) }:
		default:
		}
	}()
}

// connmanServiceEvent — C: connman_service_event (connman.c:176-182).
func connmanServiceEvent(opaque any, eventType propcore.EventType,
	args ...any) {
	cs := opaque.(*connmanService)
	s := cs.sys
	if eventType != propcore.EventExtEvent || len(args) == 0 {
		return
	}
	e, ok := args[0].(*eventpkg.Event)
	if !ok || e == nil {
		return
	}
	if e.GetType() == eventpkg.EVENT_DYNAMIC_ACTION {
		s.connmanServiceConnect(cs)
	}
}

// connmanSvcSignal — C: connman_svc_signal (connman.c:196-213).
func (s *System) connmanSvcSignal(cs *connmanService, sender, signal string,
	body []interface{}) {
	s.ts.Trace(trace.TRACE_DEBUG, "CONNMAN",
		"Service-signal %s %s from %s", cs.path, signal, sender)

	if signal == "PropertyChanged" {
		s.propSetFromTuple(body,
			s.pm.CreateEx(cs.prop, "metadata", nil, false, false))
	}
}

// connmanServiceRelease — C: connman_service_release
// (connman.c:217-228).
func (s *System) connmanServiceRelease(cs *connmanService) {
	cs.refcount--
	// C: g_object_unref(cs_proxy); free(cs_name/path/cs) — GC reclaims.
}

// connmanServiceDestroy — C: connman_service_destroy
// (connman.c:232-241).
func (s *System) connmanServiceDestroy(cs *connmanService) {
	s.connmanStopInputRequest(cs)
	cs.sub.Unsubscribe()
	s.serviceRemove(cs)
	s.pm.Destroy(cs.prop)
	cs.prop = nil
	s.connmanServiceRelease(cs)
}

// inputReqEvent — C: input_req_event (connman.c:245-287).
func inputReqEvent(opaque any, eventType propcore.EventType,
	args ...any) {
	cs := opaque.(*connmanService)
	s := cs.sys
	if eventType != propcore.EventExtEvent || len(args) == 0 {
		return
	}
	e, ok := args[0].(*eventpkg.Event)
	if !ok || e == nil {
		return
	}
	if cs.inputReqResp == nil {
		return
	}

	if e.IsAction(eventpkg.ACTION_OK) {
		username := s.propGetString(cs.inputReqProp, "username")
		password := s.propGetString(cs.inputReqProp, "password")

		fields := map[string]dbus.Variant{
			"Passphrase": dbus.MakeVariant(password),
		}
		if cs.inputReqWantIdentity {
			fields["Identity"] = dbus.MakeVariant(username)
		}
		cs.inputReqResp <- agentReply{fields: fields}
		s.connmanStopInputRequest(cs)
	}

	if e.IsAction(eventpkg.ACTION_CANCEL) {
		cs.inputReqResp <- agentReply{err: dbus.NewError(errCanceled,
			[]interface{}{"Canceled by user"})}
		s.connmanStopInputRequest(cs)
	}
}

// agentRequestInput — C: agent_request_input (connman.c:299-345).
// Sets up the auth popup; the reply is delivered via cs.inputReqResp.
func (s *System) agentRequestInput(cs *connmanService, req map[string]dbus.Variant) {
	s.ts.Trace(trace.TRACE_INFO, "CONNMAN",
		"Requesting credentials for %s", cs.path)
	s.ts.Trace(trace.TRACE_DEBUG, "CONNMAN", "RequestInput: %v", req)

	p := s.pm.CreateRoot("")

	s.pm.SetVEx(nil, p, "type", "auth")
	s.pm.SetVEx(nil, p, "id", cs.path)
	s.pm.SetVEx(nil, p, "source", "Network")

	if _, ok := req["PreviousPassphrase"]; ok {
		s.pm.SetVEx(nil, p, "reason", "Password incorrect")
	} else {
		s.pm.SetVEx(nil, p, "reason", "Password needed")
	}

	_, cs.inputReqWantIdentity = req["Identity"]
	s.pm.SetVEx(nil, p, "disableUsername",
		misc.BoolToInt(!cs.inputReqWantIdentity))
	s.pm.SetVEx(nil, p, "disableDomain", 1)

	r := s.pm.CreateEx(p, "eventSink", nil, false, false)

	cs.inputReqSub = r.Subscribe(inputReqEvent, cs,
		propcore.SubCourier{C: s.connmanCourier})

	cs.inputReqProp = p

	popups := s.pm.CreateEx(s.pm.GetGlobal(), "popups", nil, false, false)
	if s.pm.SetParentEx(p, popups, nil, "") != 0 {
		panic("connman: popup root is a zombie") // C: abort()
	}

	cs.inputReqResp = make(chan agentReply, 1)
	s.pendingInput = cs
}

/* ---- GVariant → dbus.Variant prop mapping (C: prop_gvariant.c) ---- */

// fixupTitle — C: fixup_title (prop_gvariant.c:20-28).
func fixupTitle(k string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + 32
		}
		if r == '.' {
			return '_'
		}
		return r
	}, k)
}

// propSetFromValue — Go-value half of C: prop_set_from_gvariant
// (prop_gvariant.c:36-94). dbus.Variant elements decode to plain Go
// values; the signature decides the exact C-type mapping.
func (s *System) propSetFromValue(sig string, val any, p *propcore.Prop) {
	switch sig {
	case "b":
		b, _ := val.(bool)
		s.pm.SetIntEx(p, nil, misc.BoolToInt(b))
	case "y":
		v, _ := val.(byte)
		s.pm.SetIntEx(p, nil, int(v))
	case "n":
		v, _ := val.(int16)
		s.pm.SetIntEx(p, nil, int(v))
	case "q":
		v, _ := val.(uint16)
		s.pm.SetIntEx(p, nil, int(v))
	case "i":
		v, _ := val.(int32)
		s.pm.SetIntEx(p, nil, int(v))
	case "u":
		v, _ := val.(uint32)
		s.pm.SetIntEx(p, nil, int(v))
	case "x":
		if v, ok := val.(int64); ok && v <= 0x7fffffff {
			s.pm.SetIntEx(p, nil, int(v))
		} else if ok {
			s.pm.SetFloatEx(p, nil, float32(v))
		}
	case "t":
		if v, ok := val.(uint64); ok && v <= 0x7fffffff {
			s.pm.SetIntEx(p, nil, int(v))
		} else if ok {
			s.pm.SetFloatEx(p, nil, float32(v))
		}
	case "s":
		v, _ := val.(string)
		s.pm.SetStringEx(p, nil, v, propcore.StringUTF8)
	case "o":
		v, _ := val.(dbus.ObjectPath)
		s.pm.SetStringEx(p, nil, string(v), propcore.StringUTF8)
	case "g":
		v, _ := val.(dbus.Signature)
		s.pm.SetStringEx(p, nil, v.String(), propcore.StringUTF8)
	case "a{sv}":
		s.pm.VoidChilds(p)
		if m, ok := val.(map[string]dbus.Variant); ok {
			s.propSetFromVardict(m, p)
		}
	default:
		rv := reflect.ValueOf(val)
		if rv.IsValid() && (rv.Kind() == reflect.Slice ||
			rv.Kind() == reflect.Array) {
			s.pm.DestroyChilds(p)
			for i := 0; i < rv.Len(); i++ {
				s.propSetFromAnyValue(rv.Index(i).Interface(),
					s.pm.CreateEx(p, "", nil, true, false))
			}
		} else {
			fmt.Printf("s.propSetFromValue(): can't deal with type %s\n", sig)
		}
	}
}

// propSetFromAnyValue — variant/array element without an explicit
// signature (arrays decode to plain Go values in godbus).
func (s *System) propSetFromAnyValue(val any, p *propcore.Prop) {
	if v, ok := val.(dbus.Variant); ok {
		s.propSetFromValue(v.Signature().String(), v.Value(), p)
		return
	}
	switch v := val.(type) {
	case bool:
		s.propSetFromValue("b", v, p)
	case byte:
		s.propSetFromValue("y", v, p)
	case int16:
		s.propSetFromValue("n", v, p)
	case uint16:
		s.propSetFromValue("q", v, p)
	case int32:
		s.propSetFromValue("i", v, p)
	case uint32:
		s.propSetFromValue("u", v, p)
	case int64:
		s.propSetFromValue("x", v, p)
	case uint64:
		s.propSetFromValue("t", v, p)
	case string:
		s.propSetFromValue("s", v, p)
	case dbus.ObjectPath:
		s.propSetFromValue("o", v, p)
	case dbus.Signature:
		s.propSetFromValue("g", v, p)
	case map[string]dbus.Variant:
		s.propSetFromValue("a{sv}", v, p)
	default:
		s.propSetFromValue("?", val, p)
	}
}

// propSetFromVardict — C: prop_set_from_vardict (prop_gvariant.c:102-115).
func (s *System) propSetFromVardict(m map[string]dbus.Variant, parent *propcore.Prop) {
	for k, v := range m {
		s.propSetFromValue(v.Signature().String(), v.Value(),
			s.pm.CreateEx(parent, fixupTitle(k), nil, false, false))
	}
}

// propSetFromTuple — C: prop_set_from_tuple (prop_gvariant.c:121-131).
// body is the (sv) signal payload decoded as [name, variant].
func (s *System) propSetFromTuple(body []interface{}, parent *propcore.Prop) {
	if len(body) < 2 {
		return
	}
	key, _ := body[0].(string)
	v, ok := body[1].(dbus.Variant)
	if !ok {
		return
	}
	s.propSetFromValue(v.Signature().String(), v.Value(),
		s.pm.CreateEx(parent, fixupTitle(key), nil, false, false))
}

/* ---- Manager/Service calls ---- */

// svcProps mirrors a(oa{sv}) elements from GetServices/ServicesChanged.
type svcProps struct {
	Path  dbus.ObjectPath
	Props map[string]dbus.Variant
}

// updateService — C: update_service (connman.c:352-434).
func (s *System) updateService(props map[string]dbus.Variant, path string,
	after *connmanService) *connmanService {
	cs := s.connmanServiceFind(path)

	if cs == nil {
		cs = &connmanService{refcount: 1, path: path}
		s.serviceInsert(cs, after)

		next := s.serviceNext(cs)

		cs.prop = s.pm.CreateRoot(path)

		cs.sub = cs.prop.Subscribe(connmanServiceEvent, cs,
			propcore.SubCourier{C: s.connmanCourier})

		var before *propcore.Prop
		if next != nil {
			before = next.prop
		}
		if s.pm.SetParentEx(cs.prop, s.serviceNodes, before, "") != 0 {
			panic("connman: prop_set_parent_ex failed") // C: abort()
		}

		m := s.pm.CreateEx(cs.prop, "metadata", nil, false, false)
		s.pm.Link(s.pm.CreateEx(m, "name", nil, false, false),
			s.pm.CreateEx(m, "title", nil, false, false),
			nil, false, false)
		s.pm.SetVEx(nil, cs.prop, "type", "network")
	} else {
		s.serviceRemove(cs)
		s.serviceInsert(cs, after)

		next := s.serviceNext(cs)
		var before *propcore.Prop
		if next != nil {
			before = next.prop
		}
		s.pm.Move(cs.prop, before)
	}

	s.propSetFromVardict(props, s.pm.CreateEx(cs.prop, "metadata", nil, false, false))

	if name, ok := props["Name"]; ok {
		if sv, ok := name.Value().(string); ok {
			cs.name = sv
		}
	}
	return cs
}

// servicesChanged — C: services_changed (connman.c:438-474).
// body = (a(oa{sv}) added, ao removed).
func (s *System) servicesChanged(body []interface{}) {
	var prev *connmanService

	if len(body) > 1 {
		var removed []dbus.ObjectPath
		if err := dbus.Store(body[1:2], &removed); err == nil {
			for _, p := range removed {
				s.ts.Trace(trace.TRACE_DEBUG, "CONNMAN",
					"Deleted network %s", string(p))
				if cs := s.connmanServiceFind(string(p)); cs != nil {
					s.connmanServiceDestroy(cs)
				}
			}
		}
	}

	if len(body) > 0 {
		var added []svcProps
		if err := dbus.Store(body[:1], &added); err == nil {
			for _, sv := range added {
				cs := s.updateService(sv.Props, string(sv.Path), prev)
				if cs != nil {
					prev = cs
				}
			}
		}
	}
}

// connmanMgrSignal — C: connman_mgr_signal (connman.c:480-497).
func (s *System) connmanMgrSignal(sender, signal string, body []interface{}) {
	s.ts.Trace(trace.TRACE_DEBUG, "CONNMAN",
		"Manager-signal %s from %s", signal, sender)

	if signal == "ServicesChanged" {
		s.servicesChanged(body)
	}
}

func (s *System) mgrObj() dbus.BusObject {
	return s.systemConn.Object(busName, dbus.ObjectPath("/"))
}

// connmanGetServices — C: connman_getservices (connman.c:502-541).
func (s *System) connmanGetServices() {
	call := s.mgrObj().Call(managerIface+".GetServices", 0)
	if call.Err != nil {
		s.ts.Trace(trace.TRACE_ERROR, "CONNMAN",
			"Unable to GetServices -- %s", call.Err)
		return
	}

	var list []svcProps
	if err := dbus.Store(call.Body, &list); err != nil {
		return
	}
	s.pm.DestroyChilds(s.serviceNodes)
	var prev *connmanService
	for _, sv := range list {
		if cs := s.updateService(sv.Props, string(sv.Path), prev); cs != nil {
			prev = cs
		}
	}
}

// connmanGetTechnologies — C: connman_gettechnologies
// (connman.c:545-585).
func (s *System) connmanGetTechnologies() {
	call := s.mgrObj().Call(managerIface+".GetTechnologies", 0)
	if call.Err != nil {
		s.ts.Trace(trace.TRACE_ERROR, "CONNMAN",
			"Unable to GetTechnologies -- %s", call.Err)
		return
	}

	s.ts.Trace(trace.TRACE_DEBUG, "CONNMAN", "Technologies: %v", call.Body)

	var list []svcProps
	if err := dbus.Store(call.Body, &list); err != nil {
		return
	}
	for _, t := range list {
		if t.Path == wifiTechPath {
			s.haveWifi = true
		}
	}
}

// connmanGetProperties — C: connman_getpropreties (connman.c:589-616).
func (s *System) connmanGetProperties() {
	call := s.mgrObj().Call(managerIface+".GetProperties", 0)
	if call.Err != nil {
		s.ts.Trace(trace.TRACE_ERROR, "CONNMAN",
			"Unable to GetProperties -- %s", call.Err)
		return
	}

	var dict map[string]dbus.Variant
	if err := dbus.Store(call.Body, &dict); err != nil {
		return
	}
	if state, ok := dict["State"]; ok {
		if val, ok := state.Value().(string); ok {
			s.pm.SetStringEx(s.netState, nil, val, propcore.StringUTF8)
		}
	}
}

// connmanServiceFromPath — C: connman_service_from_params
// (connman.c:618-626).
func (s *System) connmanServiceFromPath(path dbus.ObjectPath) *connmanService {
	if path == "" {
		return nil
	}
	return s.connmanServiceFind(string(path))
}

/* ---- net.connman.Agent object (C: connman_agent_vtable) ---- */

// connmanAgent — C: the object exported at /showtime/netagent.
// godbus dispatches each method on its own goroutine; RequestInput
// blocks until the user answers, matching the C async invocation.
type connmanAgent struct{ sys *System }

// ReportError — C: handle_method_call's "ReportError" case
// (connman.c:631-640).
func (a *connmanAgent) ReportError(path dbus.ObjectPath,
	msg string) *dbus.Error {
	s := a.sys
	cs := s.connmanServiceFromPath(path)

	name := "Unknown network"
	if cs != nil {
		name = cs.name
	}
	if s.nm != nil {
		s.nm.NotifyAdd(nil, notifications.NotifyError, "", 3,
			"%s\n%s", name, msg)
	}
	return nil
}

// RequestInput — C: "RequestInput" case + agent_request_input
// (connman.c:640-651, 299-345). Blocks until the user answers or
// cancels — same observable behavior as holding the invocation.
func (a *connmanAgent) RequestInput(path dbus.ObjectPath,
	req map[string]dbus.Variant) (map[string]dbus.Variant, *dbus.Error) {
	s := a.sys
	cs := s.connmanServiceFromPath(path)
	if cs == nil {
		return nil, dbus.NewError(errInvalidArg,
			[]interface{}{"Unknown service"})
	}

	// Popup setup + subscription must run on the connman goroutine
	// (prop tree ownership) — same as C running it on the connman thread.
	resp := make(chan agentReply, 1)
	done := make(chan struct{})
	select {
	case s.workCh <- func() {
		s.agentRequestInput(cs, req)
		resp <- agentReply{}
		close(done)
	}:
	case <-time.After(10 * time.Second):
		return nil, dbus.NewError(errInvalidArg,
			[]interface{}{"connman thread busy"})
	}
	<-done

	reply := <-cs.inputReqResp
	return reply.fields, reply.err
}

// Release / Cancel — declared in the C agent XML but the C vtable
// falls through to "unknown method"; here they are implemented
// (Cancel unblocks a pending RequestInput so its goroutine can exit).
func (a *connmanAgent) Release() *dbus.Error { return nil } // no system state touched

func (a *connmanAgent) Cancel() *dbus.Error {
	s := a.sys
	// pendingInput/connmanStopInputRequest live on the connman
	// goroutine (prop ownership) — dispatch through workCh like every
	// other external callback, and wait so the D-Bus reply ordering
	// matches the C synchronous Cancel.
	done := make(chan struct{})
	select {
	case s.workCh <- func() {
		defer close(done)
		if cs := s.pendingInput; cs != nil && cs.inputReqResp != nil {
			cs.inputReqResp <- agentReply{err: dbus.NewError(errCanceled,
				[]interface{}{"Canceled"})}
			s.connmanStopInputRequest(cs)
		}
	}:
	case <-time.After(10 * time.Second):
		return dbus.NewError(errInvalidArg,
			[]interface{}{"connman thread busy"})
	}
	<-done
	return nil
}

// onBusAcquired — C: on_bus_acquired (connman.c:683-708) wrapped in
// g_bus_own_name (connman.c:789-795).
func (s *System) onBusAcquired(conn *dbus.Conn) {
	if err := conn.Export(&connmanAgent{sys: s}, agentPath, agentIface); err != nil {
		s.ts.Trace(trace.TRACE_ERROR, "CONNMAN",
			"Unable to export agent object -- %s", err)
		return
	}
	if _, err := conn.RequestName(agentBusName,
		dbus.NameFlagDoNotQueue); err != nil {
		s.ts.Trace(trace.TRACE_ERROR, "CONNMAN",
			"Unable to own agent name -- %s", err)
	}

	// C: g_dbus_proxy_call_sync(connman, "RegisterAgent", "(o)", path)
	call := s.mgrObj().Call(managerIface+".RegisterAgent", 0, agentPath)
	if call.Err != nil {
		s.ts.Trace(trace.TRACE_ERROR, "CONNMAN",
			"Unable to register agent -- %s", call.Err)
	}
}

// setWifiEnable — C: set_wifi_enable (connman.c:730-769).
func setWifiEnable(opaque any, value any) {
	s := opaque.(*System)
	enable := false
	switch v := value.(type) {
	case int:
		enable = v != 0
	case bool:
		enable = v
	case float64:
		enable = v != 0
	}

	obj := s.systemConn.Object(busName, wifiTechPath)
	call := obj.Call(techIface+".SetProperty", 0, "Powered",
		dbus.MakeVariant(enable))

	if call.Err != nil {
		// C: ignore err->code 36 (G_DBUS_ERROR_NOT_SUPPORTED) — the
		// remote name for it ends in ".NotSupported".
		name := call.Err.Error()
		if derr, ok := call.Err.(dbus.Error); ok {
			name = derr.Name
		}
		if !strings.HasSuffix(name, "NotSupported") {
			s.ts.Trace(trace.TRACE_ERROR, "CONNMAN",
				"Unable to set power %s -- %s",
				string(wifiTechPath), call.Err)
		}
	}
}

// s.workCh/s.sigCh bridge external callbacks onto the connman goroutine —
// the pure-Go counterpart of the glib main-context dispatch.

// connmanThread — C: connman_thread (connman.c:772-823). A goroutine
// + select replaces the GMainContext/GMainLoop + GSource plumbing.
func (s *System) connmanThread() {
	wakeCh := make(chan struct{}, 1)
	s.connmanCourier = propcore.NewCourierNotify("connman",
		func(opaque any) {
			select {
			case wakeCh <- struct{}{}:
			default:
			}
		}, nil)

	defer close(s.stopped)

	for {
		conn, err := dbus.ConnectSystemBus()
		if err != nil {
			select {
			case <-s.done:
				return
			default:
			}
			s.ts.Trace(trace.TRACE_ERROR, "CONNMAN",
				"Unable to connect to connman -- %s", err)
			select {
			case <-s.done:
				return
			case <-time.After(5 * time.Second): // C: sleep(5); goto again
			}
			continue
		}
		s.systemConn = conn

		// C: g_signal_connect(mgr, "g-signal", connman_mgr_signal) and
		// per-service g-signal on each service proxy.
		conn.Signal(s.sigCh)
		conn.AddMatchSignal(
			dbus.WithMatchSender(busName),
			dbus.WithMatchInterface(managerIface))
		conn.AddMatchSignal(
			dbus.WithMatchSender(busName),
			dbus.WithMatchInterface(serviceIface))

		// C: g_bus_own_name(..., on_bus_acquired, ...) + RegisterAgent
		s.onBusAcquired(conn)

		s.connmanGetProperties()
		s.connmanGetServices()
		s.connmanGetTechnologies()

		if s.haveWifi {
			// C: setting_create(SETTING_BOOL, ..., "Enable Wi-Fi", ...)
			s.sm.SettingCreate(settings.SettingBool, s.connmanSettings,
				settings.SettingsInitialUpdate,
				settings.SettingTagTitle, s.sm.P("Enable Wi-Fi"),
				settings.SettingTagCallback, setWifiEnable, nil,
				settings.SettingTagStore, "connman", "enable_wifi")
		}

		for {
			select {
			case <-s.done:
				conn.RemoveSignal(s.sigCh)
				conn.Close()
				s.systemConn = nil
				return
			case sig := <-s.sigCh:
				iface, member := splitSignalName(sig.Name)
				switch iface {
				case managerIface:
					s.connmanMgrSignal(string(sig.Sender), member, sig.Body)
				case serviceIface:
					if cs := s.connmanServiceFind(string(sig.Path)); cs != nil {
						s.connmanSvcSignal(cs, string(sig.Sender), member,
							sig.Body)
					}
				}
			case fn := <-s.workCh:
				fn()
			case <-wakeCh:
				s.connmanCourier.Poll()
			}
		}
	}
}

func splitSignalName(name string) (iface, member string) {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[:i], name[i+1:]
	}
	return name, ""
}

const myURL = "settings:networkconnections" // C: MYURL

// beNetconfCanhandle — C: be_netconf_canhandle (connman.c:893-899).
func beNetconfCanhandle(url string) int {
	if url == myURL {
		return 20
	}
	return 0
}

// beNetconfOpen — C: be_netconn_open (connman.c:903-908).
func (s *System) beNetconfOpen(page any, url string, sync bool) error {
	p, ok := page.(*propcore.Prop)
	if !ok || p == nil {
		return nil
	}
	s.pm.Link(s.netconfModel, s.pm.CreateEx(p, "model", nil, false, false),
		nil, false, false)
	return nil
}

// Start — C: connman_init (connman.c:825-887).
func Start(propMgr *propcore.PropManager,
	settingsMgr *settings.SettingsManager,
	backendSys *backendcore.BackendSystem,
	notifMgr *notifications.NotificationManager) *System {
	s := &System{
		workCh:  make(chan func(), 32),
		sigCh:   make(chan *dbus.Signal, 64),
		done:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
	s.pm = propMgr
	s.sm = settingsMgr
	s.bs = backendSys
	s.nm = notifMgr
	s.ts = backendSys.TraceSystem()

	s.connmanServices = nil

	s.netconfModel = s.pm.CreateRoot("")
	pc := propcore.PropConcatCreate(s.pm,
		s.pm.CreateEx(s.netconfModel, "nodes", nil, false, false))

	s.netState = s.pm.CreateEx(s.netconfModel, "status", nil, false, false)
	s.pm.SetVEx(nil, s.netconfModel, "type", "directory")

	m := s.pm.CreateEx(s.netconfModel, "metadata", nil, false, false)
	s.pm.SetVEx(nil, m, "title", nls.GetRString("Network connections"))

	s.serviceNodes = s.pm.CreateRoot("")
	pc.AddSource(s.serviceNodes, nil)

	s.connmanSettings = s.pm.CreateRoot("")

	delim := s.pm.CreateRoot("")
	s.pm.SetStringEx(s.pm.CreateEx(delim, "type", nil, false, false),
		nil, "separator", propcore.StringUTF8)
	pc.AddSource(s.pm.CreateEx(s.connmanSettings, "nodes", nil, false, false),
		delim)

	s.sm.AddUrl(s.sm.Network(), s.sm.P("Network connections"), "", "", nil,
		myURL, settings.SettingsFirst)

	s.bs.Register(&backendcore.Backend{
		CanHandle: beNetconfCanhandle,
		Open:      s.beNetconfOpen,
	})

	go s.connmanThread()
	return s
}

// Stop — idempotent instance shutdown: unblocks the retry sleep,
// closes the bus connection and lets connmanThread return. NOT wired
// into the app shutdown (upstream C leaves the thread running until
// process exit); reserved for the owner to decide where it belongs —
// see upstream C connman.c lifecycle.
func (s *System) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() { close(s.done) })
}
