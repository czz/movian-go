package prop

// Split from prop.go — Fase 4 pure-move refactor.
// No symbol, lock, or event-order changes.

import (
	"reflect"
	"slices"
	"strconv"
	"sync/atomic"
)

func (pm *PropManager) retargetSubscription(src *Prop, s *Subscription, skipme *Subscription, origin string) {
	old := s.valueProp
	if old == src {
		return // C: if(s->hps_value_prop == p) return
	}
	var oldIsDir bool
	if old != nil {
		old.mu.Lock()
		oldIsDir = old.propType == PropTypeDir
		old.valueSubs = removeSubFromList(old.valueSubs, s)
		old.subs = removeSubFromList(old.subs, s)
		old.mu.Unlock()
	}
	// C: if previous was DIR && s != skipme → prop_notify_void(s)
	if oldIsDir && s != skipme {
		notifyVoidSub(s, old)
	}

	equal := propValueCompare(old, src)

	src.mu.Lock()
	// C: LIST_INSERT_HEAD(&p->hp_value_subscriptions, s, ...)
	src.valueSubs = slices.Insert(src.valueSubs, 0, s)
	src.subs = append(src.subs, s)
	s.valueProp = src
	monitored := src.monitored
	var srcValue any
	var srcType PropertyType
	var srcChildren []*Prop
	srcValue = src.value
	srcType = src.propType
	srcChildren = make([]*Prop, len(src.children))
	copy(srcChildren, src.children)
	src.mu.Unlock()

	// C: if(p->hp_flags & PROP_MONITORED) prop_send_subscription_monitor_active(p)
	if monitored {
		sendMonitorActive(src)
	}
	// C: if(s == skipme || equal) return
	if s == skipme || equal {
		return
	}

	// C: prop_build_notify_value(s, 0, origin, s->hps_value_prop, NULL)
	buildNotifyValue(s, srcValue, srcType, false)

	// C: if(p->hp_type != PROP_DIR) return; else individual ADD_CHILD
	if srcType != PropTypeDir {
		return
	}
	for _, c := range srcChildren {
		notifySub(s, EventAddChild, c, src)
	}
}

// valuesEqual compares two prop values for equality.
// Used by setPropValue to implement C's value-unchanged check.
// C: prop_value_compare (prop_core.c) performs cross-type int↔float comparison.
// Go: Also handles int↔float cross-type comparison to avoid spurious notifications.
func notifySubscriber(sub *Subscription, value any) {
	// C: prop_build_notify_value — if PROP_SUB_SEND_VALUE_PROP,
	// emit PROP_VALUE_PROP with backing prop BEFORE the value event.
	// C: cb(opaque, PROP_VALUE_PROP, p) where p = s->hps_value_prop.
	// Go: Use sub.valueProp (which is the retargeted prop if linked).
	vp := sub.valueProp
	if vp == nil {
		vp = sub.prop
	}
	if sub.sendValueProp && vp != nil {
		notifyValueProp(sub, vp, false)
	}

	switch v := value.(type) {
	case nil:
		// SetVoidEx: value is nil → PROP_SET_VOID
		// C: prop_notify_void(s) passes s->hps_value_prop
		notifyVoidSub(sub, vp)
	case int:
		notifySub(sub, EventSetInt, nil, v)
	case int64:
		notifySub(sub, EventSetInt, nil, int(v))
	case string:
		// C: prop_set_string → hp_type = PROP_RSTRING → PROP_SET_RSTRING
		// C: cb(opaque, PROP_SET_RSTRING, rstring, rstrtype, user_int)
		// Go: fire EventSetRString with string value + rstrType.
		rstrType := PropStrUTF8
		if vp != nil {
			vp.mu.RLock()
			rstrType = vp.rstrType
			vp.mu.RUnlock()
		}
		notifySub(sub, EventSetRString, nil, v, rstrType)
	case float32:
		notifySub(sub, EventSetFloat, nil, v)
	case float64:
		notifySub(sub, EventSetFloat, nil, float32(v))
	case URIValue:
		// C: cb(opaque, PROP_SET_URI, hp_uri_title, hp_uri, user_int)
		notifySub(sub, EventSetURI, nil, v.Title, v.URL)
	}
}

// TrampolineInt — C: trampoline_int (prop_core.c:568-589).
// Converts the delivered event into the int a PROP_TAG_CALLBACK_INT
// callback receives: SET_INT → v, SET_FLOAT → (int)v,
// RSTRING/CSTRING → atoi(v), any other event → 0. ignoreVoid mirrors
// PROP_SUB_IGNORE_VOID: when set, non-value events are not delivered
// (deliver=false) instead of calling back with 0.
func TrampolineInt(event EventType, args []any, ignoreVoid bool) (v int, deliver bool) {
	switch event {
	case EventSetInt:
		if len(args) > 0 {
			switch x := args[0].(type) {
			case int:
				v = x
			case int64:
				v = int(x)
			case float32:
				v = int(x)
			case float64:
				v = int(x)
			}
		}
		return v, true
	case EventSetFloat:
		if len(args) > 0 {
			switch x := args[0].(type) {
			case float32:
				v = int(x)
			case float64:
				v = int(x)
			case int:
				v = x
			case int64:
				v = int(x)
			}
		}
		return v, true
	case EventSetRString, EventSetCString:
		// C: atoi(rstr_get(...)) / atoi(const char *)
		if len(args) > 0 {
			if s, ok := args[0].(string); ok {
				n, _ := strconv.Atoi(s)
				v = n
			}
		}
		return v, true
	default:
		if ignoreVoid {
			return 0, false
		}
		return 0, true
	}
}

// notifyValueProp emits EventValueProp with the backing prop pointer.
// C: cb(opaque, PROP_VALUE_PROP, p) — NO user_int (special case).
// C direct path (prop_core.c:1323-1326): synchronous, no ref_inc.
// C courier path (prop_core.c:1395-1404): prop_ref_inc, enqueue, then
// prop_ref_dec after callback (prop_core.c:902-906).
func notifyValueProp(sub *Subscription, prop *Prop, direct bool) {
	if !direct && !sub.internal &&
		(sub.dispatchMode == DispatchModeCourier && sub.courier != nil ||
			sub.dispatchMode == DispatchModeGlobal ||
			sub.dispatchMode == DispatchModeGroup && sub.hpsDispatch != nil) {
		// Queued path: increment ref, enqueue, decrement after callback
		// C: n->hpn_prop = prop_ref_inc(p)
		atomic.AddInt32(&prop.refCount, 1)
		subRefInc(sub)
		courierEnqueue(sub, &Notification{
			event: EventValueProp,
			prop:  prop,
			sub:   sub,
			// No args — EventValueProp has no value args, just the prop
			// C: cb(opaque, PROP_VALUE_PROP, prop) — NO user_int
			noUserInt: true,
		})
	} else {
		// Direct path: synchronous, no ref_inc needed (prop is alive)
		// C: cb(opaque, PROP_VALUE_PROP, p) — NO user_int
		if sub.callback != nil {
			sub.callback(sub.opaque, EventValueProp, prop)
		}
	}
}

// MakeDir makes a property a directory.
// C: prop_make_dir(p, skipme, origin) — if p is already PROP_DIR, return.
// Otherwise: clean prop, init children, set hp_type = PROP_DIR, then
// prop_notify_value(p, skipme, origin) which fires PROP_SET_DIR to all
// value subscribers.
func (pm *PropManager) NotifySimple(prop *Prop, opaque any, name string, eventType EventType) {
	if prop == nil {
		return
	}
	notifySubs(prop, eventType, nil, opaque)
}

// HaveMoreChilds0 sets the have-more flags and sends the event.
// C: prop_have_more_childs0 (prop_core.c:5769-5774)
func (pm *PropManager) HaveMoreChilds0(p *Prop, yes bool) {
	if yes {
		p.flags |= FlagHaveMore | FlagHaveMoreYes
	} else {
		p.flags |= FlagHaveMore
	}
	evt := EventHaveMoreChildsNo
	if yes {
		evt = EventHaveMoreChildsYes
	}
	notifySubs(p, evt, nil, nil)
}

// HaveMoreChilds — C: prop_have_more_childs (prop_core.c:5781-5785)
func (pm *PropManager) HaveMoreChilds(p *Prop, yes bool) {
	pm.HaveMoreChilds0(p, yes)
}

// SendExtEvent sends an external event to all active value subscribers of
// a prop via EventExtEvent notifications. This is the Go equivalent of
// C's prop_send_ext_event (prop_core.c:1779).
//
// C: prop_send_ext_event0 (prop_core.c:1704):
//
//	LIST_FOREACH(s, &p->hp_value_subscriptions, hps_value_prop_link) {
//	    n = prop_get_notify(s);
//	    n->hpn_event = PROP_EXT_EVENT;
//	    atomic_inc(&e->e_refcount);       // AddRef per subscriber
//	    n->hpn_ext_event = e;
//	    prop_courier_enqueue(s, n);
//	}
//
// Ownership contract:
//   - SendExtEvent does AddRef(e) per active subscriber
//   - notifySub transfers ownership to Notification.extEvent (courier)
//     or defer Release (direct)
//   - freeNotification releases extEvent after callback
//   - The callback receives a BORROWED reference and must NOT call Release
//   - SendExtEvent does NOT release the producer's reference
//   - The caller is responsible for releasing the producer's reference
//     (matching C's event_dispatch which calls event_release after routing)
//
// This means the caller can send the same event to multiple sinks:
//
//	SendExtEvent(globalSink, e)  // AddRef per global sub
//	SendExtEvent(navSink, e)     // AddRef per nav sub
//	e.Release()                  // producer's release
//
// SendExtEvent sends an external event to all value subscribers of a prop.
// C: prop_send_ext_event0 (prop_core.c:1704) — follows hp_originator chain
// to the root, then iterates the root's hp_value_subscriptions.
func (pm *PropManager) SendExtEvent(p *Prop, e ExtEvent) {
	if p == nil || e == nil {
		return
	}
	// C: if(p->hp_type == PROP_PROXY) { prop_proxy_send_event(p, e); return; }
	// Go: Check for proxy type and forward via ProxyExtEventSender if available
	p.mu.RLock()
	isProxy := p.propType == PropTypeProxy
	proxyConn := p.proxyConn
	p.mu.RUnlock()
	if isProxy {
		if pes, ok := proxyConn.(ProxyExtEventSender); ok && pes != nil {
			pes.ProxySendExtEvent(p, e)
			return
		}
		// If proxyConn doesn't implement ProxyExtEventSender, fall through
	}

	// C: follow hp_originator chain to root
	root := p
	root.mu.RLock()
	for root.originator != nil {
		root = root.originator
	}
	root.mu.RUnlock()

	root.mu.RLock()
	subsCopy := make([]*Subscription, len(root.valueSubs))
	copy(subsCopy, root.valueSubs)
	root.mu.RUnlock()

	for _, sub := range subsCopy {
		if !sub.active.Load() {
			continue
		}
		e.AddRef()
		notifySub(sub, EventExtEvent, nil, e)
	}
}

// NotifyChild sends a child notification to all subscribers of a prop
// NotifyChild notifies the parent's value subscribers about a child event.
// C: prop_notify_child — iterates parent's hp_value_subscriptions and
// calls prop_build_notify_child(s, child, event, 0, flags).
func (pm *PropManager) NotifyChild(prop *Prop, parent *Prop, eventType EventType, opaque any, flags int) {
	if prop == nil || parent == nil {
		return
	}
	notifySubs(parent, eventType, prop, opaque, flags)
}

// GetString gets a string property value
func notifyMultiParentChain(prop *Prop, skipme *Subscription, value any) {
	prop.mu.RLock()
	hasMultiNotify := prop.flags&FlagMultiNotify != 0
	prop.mu.RUnlock()
	if !hasMultiNotify {
		return
	}

	// Walk up parents
	current := prop
	for {
		current.mu.RLock()
		parent := current.parent
		current.mu.RUnlock()
		if parent == nil {
			break
		}
		parent.mu.RLock()
		isMultiSub := parent.flags&FlagMultiSub != 0
		if !isMultiSub {
			parent.mu.RUnlock()
			current = parent
			continue
		}
		// Collect MULTI subs
		// C: prop_notify_value does NOT skip skipme for MULTI subs (prop_core.c:1554-1556)
		var multiSubs []*Subscription
		for _, sub := range parent.valueSubs {
			if sub.multiSub {
				multiSubs = append(multiSubs, sub)
			}
		}
		parent.mu.RUnlock()

		for _, sub := range multiSubs {
			// C: prop_build_notify_value(s, 0, origin, p, NULL) where p
			// is the PARENT (MULTI_SUB) prop. The callback receives
			// (opaque, PROP_SET_DIR, parent, child, user_int).
			notifyMultiSub(sub, parent, prop, value)
		}
		current = parent
	}
}

// notifyMultiSub dispatches a value-change notification to a MULTI subscriber.
// C: prop_build_notify_value(s, 0, origin, p, NULL) where p is the PARENT prop
// (the MULTI_SUB prop), and the event is PROP_SET_DIR.
// The callback receives (opaque, PROP_SET_DIR, parent, child, user_int).
func notifyMultiSub(sub *Subscription, parent *Prop, child *Prop, value any) {
	if sub == nil || !sub.active.Load() {
		return
	}
	// C: prop_build_notify_value sets n->hpn_event based on p->hp_type.
	// For a MULTI_SUB parent (which is a DIR), the event is PROP_SET_DIR.
	// The child prop is passed as the changed prop.
	notifySub(sub, EventSetDir, parent, child, value)
}

// notifyValueSubsWithFlag notifies value subs that HAVE the specified flag.
// Used for EARLY_DEL_CHILD notifications.
func notifyValueSubsWithFlag(p *Prop, child *Prop, flag int, event EventType) {
	if p == nil {
		return
	}
	p.mu.RLock()
	var subs []*Subscription
	for _, sub := range p.valueSubs {
		if sub.earlyDelChild { // Currently only EARLY_DEL_CHILD uses this
			subs = append(subs, sub)
		}
	}
	p.mu.RUnlock()
	for _, sub := range subs {
		notifySub(sub, event, child)
	}
}

// notifyValueSubsWithoutFlag notifies value subs that DON'T have the specified flag.
// Used for late DEL_CHILD notifications.
func notifyValueSubsWithoutFlag(p *Prop, child *Prop, flag int, event EventType) {
	if p == nil {
		return
	}
	p.mu.RLock()
	var subs []*Subscription
	for _, sub := range p.valueSubs {
		if !sub.earlyDelChild { // Subs without EARLY_DEL_CHILD get late notification
			subs = append(subs, sub)
		}
	}
	p.mu.RUnlock()
	for _, sub := range subs {
		notifySub(sub, event, child)
	}
}

// DestroyChilds destroys all children of a property.
//
// C equivalent: prop_destroy_childs0(prop_t *p)
//
// Semantics:
//  1. Move children to a local slice, clear children + childIndex, set type to VOID.
//  2. Notify subscribers of prop with EventSetVoid (outside lock).
//  3. For each child: set parent=nil, then Destroy(child) for recursive cleanup.
//
// Destroy(child) with parent==nil skips parent removal and EventDelChild,
// matching C's prop_destroy_childs0 which sets c->hp_parent = NULL before
// calling prop_destroy0(c).
func (p *Prop) SetCallback(callback func(opaque any, value any), opaque any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.callback = callback
	p.opaque = opaque
}

// SetInt sets an integer value
func (s *Subscription) SetProxySubID(id uint32) {
	s.proxySubID = id
}

// GetProxySubID returns the proxy subscription ID
func (s *Subscription) GetProxySubID() uint32 {
	return s.proxySubID
}

// GetValueProp returns the value property
func (s *Subscription) GetValueProp() *Prop {
	return s.valueProp
}

// SetValueProp sets the value property
func (s *Subscription) SetValueProp(p *Prop) {
	s.valueProp = p
}

// GetHpsFlags returns the subscription flag bitmask in canonical C values
// (PROP_SUB_* from prop.h). C: s->hps_flags. Used by the STPP wire encoder.
func (s *Subscription) GetHpsFlags() uint32 {
	if s == nil {
		return 0
	}
	return s.hpsFlags
}

// GetProxyConn returns the proxy connection owning this subscription
// (C: hps_ppc). Nil for normal subscriptions.
func (s *Subscription) GetProxyConn() any {
	if s == nil {
		return nil
	}
	return s.proxyConn
}

// Notify delivers an event to this subscription.
// C: prop_get_notify + prop_courier_enqueue (prop_core.c:1252, 1735) —
// takes a notification ref on the sub and dispatches per hps_dispatch_mode.
// Used by the STPP wire decoder to deliver remote notifications.
func (s *Subscription) Notify(et EventType, prop *Prop, args ...any) {
	if s == nil {
		return
	}
	notifySub(s, et, prop, args...)
}

// PropDestroy0 destroys a property
const ValueVoid = 0

// ==================== ADDITIONAL METHODS FOR COMPATIBILITY ====================

// Subscription represents a property subscription
type Subscription struct {
	prop     *Prop
	callback func(opaque any, eventType EventType, args ...any)
	opaque   any
	active   atomic.Bool

	// refCount is the subscription's own refcount (C: hps_refcount).
	// Incremented for each pending notification, decremented after dispatch.
	// Keeps the subscription alive while notifications are in courier queues.
	refCount int32

	// dispatchMode controls how notifications are delivered (C: hps_dispatch_mode).
	// DispatchModeDirect = synchronous callback (C: PROP_SUB_INTERNAL)
	// DispatchModeCourier = async via courier queue
	dispatchMode DispatchMode

	// courier is the courier queue for async dispatch (C: hps_dispatch).
	// nil when dispatchMode is DispatchModeDirect.
	courier *Courier

	// internal marks PROP_SUB_INTERNAL subscriptions (C: hps_flags & 0x20).
	// Internal subs dispatch synchronously and never enter the courier /
	// global / group dispatch machinery (C: prop_dispatch_one asserts this).
	internal bool

	// hpsLock is the subscription's lock pointer (C: hps_lock).
	// For PROP_TAG_MUTEX it is the caller-supplied lock; for COURIER mode
	// it is the courier's pc_entry_lock. nil means no locking during
	// dispatch (C: PROP_SUB_DONTLOCK / no PROP_TAG_MUTEX).
	hpsLock any

	// hpsLockmgr is the lock manager for hpsLock (C: hps_lockmgr).
	// Always non-nil; defaults to proplockmgr (C: lockmgr = proplockmgr).
	hpsLockmgr *Lockmgr

	// hpsDispatch is the prop_sub_dispatch for GLOBAL and GROUP modes
	// (C: hps_dispatch). GLOBAL: per-sub psd created lazily on first
	// notification and freed when drained. GROUP: the shared group psd.
	hpsDispatch *propSubDispatch

	// trackDestroy indicates this subscription should receive EventDestroyed
	// (instead of EventSetVoid) when the prop is destroyed.
	// C equivalent: PROP_SUB_TRACK_DESTROY flag.
	trackDestroy bool

	// earlyDelChild indicates this subscription receives EventDelChild
	// BEFORE the child is destroyed (while still accessible).
	// C equivalent: PROP_SUB_EARLY_DEL_CHILD flag.
	earlyDelChild bool

	// expedite indicates notifications from this subscription should be
	// dispatched before normal notifications.
	// C equivalent: PROP_SUB_EXPEDITE flag.
	expedite bool

	// directUpdate indicates the initial update should be synchronous
	// even if a courier is attached. C: PROP_SUB_DIRECT_UPDATE (0x10000).
	// Runtime notifications still go through the courier.
	// Used by glw_slider, glw_keyintercept, glw_text_bitmap, gu_pages, etc.
	directUpdate bool

	// multiSub indicates this subscription uses PROP_SUB_MULTI mode,
	// receiving notifications from all children of a MULTI_SUB prop.
	// C: PROP_SUB_MULTI flag (0x10)
	multiSub bool

	// ignoreVoid indicates this subscription should NOT receive EventSetVoid
	// notifications. C: PROP_SUB_IGNORE_VOID (0x40).
	// Used by settings, navigator, ecmascript to skip void transitions.
	ignoreVoid bool

	// sendValueProp indicates this subscription should receive EventValueProp
	// (with the backing prop pointer) before each value update.
	// C: PROP_SUB_SEND_VALUE_PROP (0x100).
	// C: cb(opaque, PROP_VALUE_PROP, p) — NO user_int (special case).
	sendValueProp bool

	// subscriptionMonitor indicates this subscription monitors for other
	// subscriptions being added to the same prop. When a non-monitor
	// subscription is added, this sub receives EventSubscriptionMonitorActive.
	// C: PROP_SUB_SUBSCRIPTION_MONITOR (0x4).
	subscriptionMonitor bool

	// singleton indicates this subscription should not be created if an
	// existing subscription with the same (callback, opaque) pair already
	// exists on this prop. C: PROP_SUB_SINGLETON (0x40000).
	// C: prop_core.c:3176-3183 — scans value subs, returns NULL if duplicate.
	singleton bool

	// userInt is the user-supplied integer passed as the last arg in callbacks.
	// C: hps_user_int (set via PROP_TAG_CALLBACK_USER_INT).
	// Passed as last arg in ALL events EXCEPT PROP_VALUE_PROP.
	userInt int

	// Proxy-specific fields
	proxySubID uint32
	valueProp  *Prop

	// hpsOrigin is the link-destination prop this subscription traversed
	// during resolution (C: hps_origin). Set when the resolved path
	// passed through a prop with hp_originator (a link). When that link
	// is broken/relinked, restore_and_descend pulls the sub back so it
	// can be re-resolved through the new originator.
	// C: s->hps_origin (prop_core.c:3235).
	hpsOrigin *Prop

	// hpsMultipleOrigins indicates the sub traversed more than one link
	// (C: hps_multiple_origins). In that case hpsPots holds the list
	// (C: hps_pots — prop_originator_tracking_t linked list, head order).
	hpsMultipleOrigins bool
	hpsPots            []*Prop

	// hpsFlags is the subscription flag bitmask in canonical C values
	// (PROP_SUB_* from prop.h). Accumulated during Subscribe arg parsing
	// (Go Sub* constants are mapped to their C bit values, which differ
	// for TrackDestroy/NoInitialUpdate/IgnoreVoid). Written on the STPP
	// wire for proxy subscriptions. C: hps_flags.
	hpsFlags uint32

	// proxyConn is the proxy connection owning this subscription
	// (C: hps_ppc). Non-nil means this is a proxy subscription
	// (C: s->hps_proxy == 1). Proxy subs are registered on the
	// connection's ppc_subs list, not on local canonical/value lists.
	proxyConn any
}

// GetTarget returns the prop this subscription is attached to.
func (s *Subscription) GetTarget() *Prop {
	if s == nil {
		return nil
	}
	return s.prop
}

// IsActive reports whether the subscription is still active (debug).
func (s *Subscription) IsActive() bool {
	if s == nil {
		return false
	}
	return s.active.Load()
}

// Unsubscribe unsubscribes from the property events.
//
// C: prop_unsubscribe0 (prop_core.c:3397):
//  1. Set hps_zombie = 1 (active = false in Go).
//  2. If courier mode: pc->pc_refcount--.
//  3. prop_ref_dec_locked(s->hps_origin) — drop memory ref on origin prop.
//  4. LIST_REMOVE from hp_value_prop_link and hp_canonical_prop_link.
//  5. prop_sub_ref_dec_locked(s) — may free subscription.
func (s *Subscription) Unsubscribe() {
	if s == nil {
		return
	}
	p := s.prop
	if p != nil {
		p.mu.Lock()
		if !s.active.Load() {
			p.mu.Unlock()
			return
		}
		s.active.Store(false) // C: hps_zombie = 1
		// Remove from all three lists (C: LIST_REMOVE from both
		// hps_value_prop_link and hps_canonical_prop_link)
		p.subs = removeSubFromList(p.subs, s)
		p.canonicalSubs = removeSubFromList(p.canonicalSubs, s)
		p.valueSubs = removeSubFromList(p.valueSubs, s)

		// C: prop_unsubscribe0 (prop_core.c:3430-3460) — when the
		// removed sub had SUBSCRIPTION_MONITOR or MULTI, recount BOTH
		// over the remaining canonical subs; clear PROP_MONITORED if
		// none left and prop_clr_multi if no MULTI left.
		needClrMulti := false
		if s.subscriptionMonitor || s.multiSub {
			mon := false
			multi := false
			for _, t := range p.canonicalSubs {
				if t.subscriptionMonitor {
					mon = true
				}
				if t.multiSub {
					multi = true
				}
			}
			if !mon {
				p.monitored = false
			}
			if !multi {
				needClrMulti = true
			}
		}

		p.mu.Unlock()
		if needClrMulti && p.manager != nil {
			p.manager.propClrMulti(p)
		}

		// C: If courier mode: pc->pc_refcount-- (prop_core.c:3407-3410)
		// s->hps_dispatch is NOT cleared in C — and must not be here
		// either: a notify already in flight (snapshot taken under
		// p.mu.RLock before our removal) would dereference nil. The
		// Go pointer keeps the (possibly stopped) courier valid.
		if s.dispatchMode == DispatchModeCourier && s.courier != nil {
			s.courier.Release()
		}

		// C: prop_unsubscribe0 (prop_core.c:3418-3424):
		//   if(s->hps_proxy) prop_proxy_unsubscribe(s); else {...}
		// Proxy subs were never inserted into local lists (the removals
		// above are no-ops); the connection sends STPP_CMD_UNSUBSCRIBE and
		// destroys the sub's prop tree.
		if s.proxyConn != nil {
			if pu, ok := s.proxyConn.(ProxyUnsubscriber); ok {
				pu.ProxyUnsubscribe(s)
			}
			s.proxyConn = nil
		}

		// C: prop_ref_dec_locked(s->hps_origin) — drop memory ref on prop.
		// This may trigger final cleanup if refcount hits 0 and prop is ZOMBIE.
		p.Release()
	} else {
		s.active.Store(false)
		// C: If courier mode: pc->pc_refcount-- (hps_dispatch kept)
		if s.dispatchMode == DispatchModeCourier && s.courier != nil {
			s.courier.Release()
		}
	}

	// C: prop_sub_ref_dec_locked(s) — drop the subscription's own refcount.
	// This is the initial ref of 1 from prop_subscribe_ex. If there are no
	// pending notifications, this drops ref to 0 and the sub is freed.
	// In Go, GC handles memory, but we decrement for numeric parity with C.
	subRefDec(s)
}

// removeSubFromList removes a subscription from a slice, preserving order.
func removeSubFromList(list []*Subscription, s *Subscription) []*Subscription {
	for i, sub := range list {
		if sub == s {
			return slices.Delete(list, i, i+1)
		}
	}
	return list
}

// GetChild returns a child property by name
func (pm *PropManager) Unsubscribe(sub *Subscription) {
	if sub != nil {
		sub.Unsubscribe()
	}
}

// SetPropEx sets a property as a child
func (pm *PropManager) GetSubscriptionStats() (int, int) {
	if pm == nil || pm.globalProp == nil {
		return 0, 0
	}
	return countSubs(pm.globalProp)
}

// countSubs recursively counts total and active subscriptions in the prop tree
func countSubs(p *Prop) (int, int) {
	if p == nil {
		return 0, 0
	}
	p.mu.RLock()
	total := len(p.subs)
	active := 0
	for _, s := range p.subs {
		if s.active.Load() {
			active++
		}
	}
	children := make([]*Prop, len(p.children))
	copy(children, p.children)
	p.mu.RUnlock()

	for _, child := range children {
		t, a := countSubs(child)
		total += t
		active += a
	}
	return total, active
}

// SetLogCallback sets a logging callback for property system events
func (pm *PropManager) SetLogCallback(callback func(format string, args ...any)) {
	if pm == nil {
		return
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.logCallback = callback
}

// Subscribe creates a subscription to property changes.
//
// C semantics (prop_core.c:prop_subscribe): by default, the subscriber
// receives an initial update consisting of:
//  1. PROP_SET_* with the current value (via prop_build_notify_value)
//  2. PROP_ADD_CHILD_VECTOR with all existing children (if the prop is a directory)
//
// This is suppressed when PROP_SUB_NO_INITIAL_UPDATE is set.
//
// Go: We match this behavior. The initial update fires after the sub is
// registered, outside the prop lock (to avoid re-entrant deadlock).
// Callers can opt out by passing SubNoInitialUpdate as an arg.
func (p *Prop) Subscribe(callback func(opaque any, eventType EventType, args ...any), opaque any, args ...any) *Subscription {
	sub := &Subscription{
		prop:     p,
		callback: callback,
		opaque:   opaque,
		refCount: 1, // C: atomic_set(&s->hps_refcount, 1) in prop_subscribe_ex
		// C: int dispatch_mode = PROP_SUB_DISPATCH_MODE_GLOBAL (prop_core.c:2961)
		// — the default when no courier or dispatch group is supplied.
		dispatchMode: DispatchModeGlobal,
	}
	sub.active.Store(true)

	// Check for subscription flags in args
	// C: flags are passed as bitmask to prop_subscribe()
	noInitialUpdate := false
	trackDestroy := false
	earlyDelChild := false
	expedite := false
	directUpdate := false
	ignoreVoid := false
	dontLock := false
	var lock any         // C: void *lock (PROP_TAG_MUTEX)
	var lockmgr *Lockmgr // C: lockmgr_fn_t *lockmgr (PROP_TAG_LOCKMGR)
	// C: prop_t *origin_chain[16] — link dsts traversed during resolution,
	// recorded on the sub as hps_origin / hps_pots.
	var subOrigins []*Prop
	// C: `value` from the name path — pre-resolved via prop_subfind.
	var subValueProp *Prop
	// cFlags accumulates the subscription flags in canonical C bit values
	// (PROP_SUB_* from prop.h). Go's Sub* constants deliberately keep their
	// own numbering; the C-value mask is what goes on the STPP wire
	// (C: s->hps_flags = flags).
	//
	// Each int arg is a bitmask, not an enumerated tag — callers pass
	// combinations like SubFlagDirectUpdate|SubFlagSendValueProp
	// (glw_keyintercept, glw_slider, glw_text_bitmap), so flags are
	// detected by bit test, not equality.
	var cFlags uint32
	for _, arg := range args {
		if i, ok := arg.(int); ok {
			if i&SubNoInitialUpdate != 0 {
				noInitialUpdate = true
				cFlags |= 0x200 // C: PROP_SUB_NO_INITIAL_UPDATE
			}
			if i&SubFlagTrackDestroy != 0 {
				trackDestroy = true
				cFlags |= 0x1 // C: PROP_SUB_TRACK_DESTROY
			}
			if i&SubFlagEarlyDelChild != 0 {
				earlyDelChild = true
				cFlags |= 0x400 // C: PROP_SUB_EARLY_DEL_CHILD
			}
			if i&SubFlagExpedite != 0 {
				expedite = true
				cFlags |= 0x8 // C: PROP_SUB_EXPEDITE
			}
			if i&SubFlagDirectUpdate != 0 {
				directUpdate = true
				cFlags |= 0x10000 // C: PROP_SUB_DIRECT_UPDATE
			}
			if i&SubFlagIgnoreVoid != 0 {
				ignoreVoid = true
				cFlags |= 0x40 // C: PROP_SUB_IGNORE_VOID
			}
			if i&SubFlagMulti != 0 {
				sub.multiSub = true
				cFlags |= 0x10 // C: PROP_SUB_MULTI
			}
			if i&SubFlagTrackDestroyExp != 0 {
				// C: PROP_SUB_TRACK_DESTROY_EXP — TRACK_DESTROY + expedited.
				// NOT IMPLEMENTED. See constant doc for proof of non-requirement.
				// Treat as TrackDestroy + Expedite as a best-effort approximation.
				trackDestroy = true
				expedite = true
				cFlags |= 0x80 // C: PROP_SUB_TRACK_DESTROY_EXP
			}
			if i&SubFlagSendValueProp != 0 {
				// C: PROP_SUB_SEND_VALUE_PROP (0x100) — emit PROP_VALUE_PROP
				// with backing prop pointer before each value update.
				sub.sendValueProp = true
				cFlags |= 0x100 // C: PROP_SUB_SEND_VALUE_PROP
			}
			if i&SubFlagSubscriptionMonitor != 0 {
				// C: PROP_SUB_SUBSCRIPTION_MONITOR (0x4) — monitor for
				// non-monitor subscriptions being added to the same prop.
				// When a non-monitor sub is added, this sub receives
				// EventSubscriptionMonitorActive with user_int.
				sub.subscriptionMonitor = true
				cFlags |= 0x4 // C: PROP_SUB_SUBSCRIPTION_MONITOR
			}
			if i&SubFlagSingleton != 0 {
				// C: PROP_SUB_SINGLETON (0x40000) — prevent duplicate
				// subscriptions with same (callback, opaque) pair.
				// C: prop_core.c:3176-3183 — scans value subs, returns
				// NULL if (callback, opaque) already exists.
				sub.singleton = true
				cFlags |= 0x40000 // C: PROP_SUB_SINGLETON
			}
			if i&SubFlagAltPath != 0 {
				// C: PROP_SUB_ALT_PATH — handled by pre-resolution in STPP.
				// Go resolves path before Subscribe, so this flag is a no-op.
				cFlags |= 0x80000 // C: PROP_SUB_ALT_PATH
			}
			if i&SubFlagDebug != 0 {
				// C: PROP_SUB_DEBUG — diagnostic only, no functional effect.
				cFlags |= 0x2 // C: PROP_SUB_DEBUG
			}
			if i&SubFlagInternal != 0 {
				// C: PROP_SUB_INTERNAL — synchronous direct dispatch,
				// never enters the dispatch machinery.
				sub.internal = true
				cFlags |= 0x20 // C: PROP_SUB_INTERNAL
			}
			if i&SubFlagDontLock != 0 {
				// C: PROP_SUB_DONTLOCK — hps_lock = NULL
				dontLock = true
				cFlags |= 0x20000 // C: PROP_SUB_DONTLOCK
			}
		} else if ui, ok := arg.(SubUserInt); ok {
			// C: PROP_TAG_CALLBACK_USER_INT — sets hps_user_int
			sub.userInt = int(ui)
		} else if dg, ok := arg.(SubDispatchGroup); ok {
			// C: PROP_TAG_DISPATCH_GROUP — hps_dispatch_mode = GROUP,
			// hps_dispatch = shared psd.
			sub.dispatchMode = DispatchModeGroup
			sub.hpsDispatch = dg.G
		} else if sc, ok := arg.(SubCourier); ok {
			// C: PROP_SUB_DISPATCH_MODE_COURIER (prop_core.c:3244-3249)
			// — hps_dispatch_mode = COURIER, hps_dispatch = pc.
			// Must be set before the initial update fires so the
			// initial notify is enqueued, not invoked synchronously.
			sub.dispatchMode = DispatchModeCourier
			sub.courier = sc.C
			sc.C.Retain() // C: pc->pc_refcount++
		} else if sm, ok := arg.(SubMutex); ok {
			// C: PROP_TAG_MUTEX — s->hps_lock = lock
			lock = sm.Ptr
		} else if sl, ok := arg.(SubLockmgr); ok {
			// C: PROP_TAG_LOCKMGR — s->hps_lockmgr = lockmgr
			lockmgr = sl.L
		} else if sv, ok := arg.(SubValueProp); ok {
			// C: value = prop_subfind(p, name, 1, 0, origin_chain)
			// — pre-resolved value prop from the name path.
			subValueProp = sv.P
		} else if so, ok := arg.(SubOrigins); ok {
			// C: origin_chain collected by prop_subfind during the
			// name-path resolution.
			subOrigins = so.Props
		}
	}
	sub.trackDestroy = trackDestroy
	sub.earlyDelChild = earlyDelChild
	sub.expedite = expedite
	sub.directUpdate = directUpdate
	sub.ignoreVoid = ignoreVoid
	sub.hpsFlags = cFlags

	// C: if(lockmgr == NULL) lockmgr = proplockmgr (prop_core.c:3239)
	if lockmgr == nil {
		lockmgr = proplockmgr
	}
	if dontLock {
		lock = nil // C: PROP_SUB_DONTLOCK
	}

	// C: dispatch_mode switch (prop_core.c:3243-3264)
	switch sub.dispatchMode {
	case DispatchModeCourier:
		// C: s->hps_lock = pc->pc_entry_lock;
		//    s->hps_lockmgr = pc->pc_lockmgr ?: lockmgr;
		sub.hpsLock = sub.courier.entryLock
		if sub.courier.entryLockmgr != nil {
			sub.hpsLockmgr = sub.courier.entryLockmgr
		} else {
			sub.hpsLockmgr = lockmgr
		}
	case DispatchModeGlobal:
		// C: s->hps_dispatch = NULL; s->hps_lock = lock; s->hps_lockmgr = lockmgr;
		sub.hpsLock = lock
		sub.hpsLockmgr = lockmgr
	case DispatchModeGroup:
		// C: psd->psd_refcount++; s->hps_dispatch = psd;
		//    s->hps_lock = lock; s->hps_lockmgr = lockmgr;
		sub.hpsDispatch.refCount++
		sub.hpsLock = lock
		sub.hpsLockmgr = lockmgr
	default:
		// Direct/INTERNAL — mode field unused, but hpsLockmgr must be
		// non-nil for the unconditional RETAIN/RELEASE calls (C: 3267).
		sub.hpsLockmgr = lockmgr
		sub.hpsLock = lock
	}

	// C: s->hps_lockmgr(s->hps_lock, LOCKMGR_RETAIN) (prop_core.c:3267)
	sub.hpsLockmgr.Fn(sub.hpsLock, LockmgrRetain)

	// C: prop_subscribe0 (prop_core.c:3128-3187): when the root/name does
	// not resolve, canonical = value = NULL and the subscription is
	// created dormant — it is valid, can be unsubscribed, but never gets
	// inserted into any prop's lists and receives no initial update.
	// If TRACK_DESTROY is set, C fires PROP_DESTROYED right away
	// (prop_core.c:3374-3387): synchronous cb for direct/INTERNAL subs
	// (then the sub is dropped — prop_subscribe returns NULL), otherwise
	// queued via prop_notify_destroyed → courier_enqueue0.
	if p == nil {
		if sub.trackDestroy {
			if sub.directUpdate || sub.internal {
				// C: cb(opaque, PROP_DESTROYED, s, user_int); s = NULL
				if sub.callback != nil {
					sub.callback(sub.opaque, EventDestroyed, sub, sub.userInt)
				}
				return nil
			}
			notifySub(sub, EventDestroyed, nil, sub)
		}
		return sub
	}

	// C: PROP_SUB_SINGLETON check (prop_core.c:3176-3183)
	// If SINGLETON flag is set, scan existing value subs for a duplicate
	// (callback, opaque) pair. If found, return nil (no new sub created).
	// Go: functions can't be compared with ==, so we use reflect to
	// compare function pointers (same underlying function value).
	if sub.singleton {
		subCbPtr := reflect.ValueOf(sub.callback).Pointer()
		p.mu.Lock()
		for _, existing := range p.valueSubs {
			if existing.active.Load() &&
				reflect.ValueOf(existing.callback).Pointer() == subCbPtr &&
				existing.opaque == sub.opaque {
				p.mu.Unlock()
				return nil
			}
		}
		p.mu.Unlock()
	}

	// C: prop_subscribe_ex does prop_ref_inc(origin) (prop_core.c:3235).
	// The subscription holds a MEMORY reference on the origin prop,
	// keeping the prop's memory alive while the subscription exists.
	// This does NOT prevent content destruction (xref can still hit 0 → ZOMBIE),
	// but it prevents memory free (refcount stays > 0).
	atomic.AddInt32(&p.refCount, 1)

	// C: Follow originator chain to find the actual value prop.
	// C: prop_subscribe (prop_core.c:3140-3143):
	//   while(value->hp_originator != NULL)
	//     { origin_chain[ocnum++] = value; value = value->hp_originator; }
	// The sub is added to the source's value_subscriptions, not to p's.
	// The canonical prop stays as p (the original prop the caller subscribed to).
	var valueProp *Prop
	if subValueProp != nil {
		// Name path: value was already resolved by Subfind with
		// follow_symlinks=1, collecting subOrigins along the way.
		valueProp = subValueProp
	} else {
		valueProp = p
		p.mu.RLock()
		originator := p.originator
		p.mu.RUnlock()
		for originator != nil {
			subOrigins = append(subOrigins, valueProp)
			valueProp = originator
			valueProp.mu.RLock()
			originator = valueProp.originator
			valueProp.mu.RUnlock()
		}
	}

	// C: prop_subscribe_ex (prop_core.c:3211-3237) — store the collected
	// origin chain on the subscription: single → hps_origin, multiple →
	// hps_pots list (reversed so index order matches C's tail-to-head
	// build). Each origin holds a memory ref on the prop.
	if len(subOrigins) == 1 {
		sub.hpsOrigin = subOrigins[0]
		subOrigins[0].manager.RefInc(subOrigins[0])
	} else if len(subOrigins) > 1 {
		sub.hpsMultipleOrigins = true
		// C builds pots prepending each chain element from last to
		// first, so the head ends up origin_chain[0] — same order as
		// the slice here.
		sub.hpsPots = append(sub.hpsPots, subOrigins...)
		for _, o := range subOrigins {
			o.manager.RefInc(o)
		}
	}

	// C: prop_subscribe_ex (prop_core.c:3364-3368):
	//   if(ppc != NULL) {
	//     s->hps_proxy = 1;
	//     prop_proxy_subscribe(ppc, s, value, name);
	//   } else { ...local lists + initial update... }
	// A resolved PROP_PROXY value prop delegates the whole subscription to
	// the proxy connection — no local list insertion, no initial update
	// (state arrives over the STPP wire instead).
	valueProp.mu.RLock()
	var ppc any
	if valueProp.propType == PropTypeProxy {
		ppc = valueProp.proxyConn
	}
	valueProp.mu.RUnlock()
	if ppc != nil {
		if ps, ok := ppc.(ProxySubscriber); ok {
			sub.proxyConn = ppc
			sub.valueProp = valueProp
			ps.ProxySubscribe(sub, valueProp)
			return sub
		}
	}

	// Add sub to canonical prop's canonical subs (always p)
	p.mu.Lock()
	p.canonicalSubs = append(p.canonicalSubs, sub)

	// C: SUBSCRIPTION_MONITOR logic (prop_core.c:3285-3298, 3360-3363)
	var activateOnCanonical bool
	var monitorTargets []*Subscription
	if sub.subscriptionMonitor {
		if !p.monitored {
			p.monitored = true
			for _, t := range p.valueSubs {
				if t == sub {
					continue
				}
				if !t.subscriptionMonitor {
					activateOnCanonical = true
					break
				}
			}
		}
	} else {
		if p.monitored {
			for _, t := range p.valueSubs {
				if t.subscriptionMonitor {
					monitorTargets = append(monitorTargets, t)
				}
			}
		}
	}
	p.mu.Unlock()

	// Add sub to value prop's value subs (may be different from p if linked)
	valueProp.mu.Lock()
	sub.valueProp = valueProp
	valueProp.valueSubs = append(valueProp.valueSubs, sub)
	valueProp.subs = append(valueProp.subs, sub)
	// If activating due to existing non-monitor subs, collect all monitor subs
	if activateOnCanonical {
		for _, t := range valueProp.valueSubs {
			if t.subscriptionMonitor {
				monitorTargets = append(monitorTargets, t)
			}
		}
	}

	// Capture current state from value prop
	destroyed := valueProp.destroyed
	var currentValue any
	var currentPropType PropertyType
	var childrenCopy []*Prop
	var selectedChild *Prop
	var haveMore, haveMoreYes bool
	if !destroyed && !noInitialUpdate {
		currentValue = valueProp.value
		currentPropType = valueProp.propType
		selectedChild = valueProp.selectedChild
		haveMore = valueProp.flags&FlagHaveMore != 0
		haveMoreYes = valueProp.flags&FlagHaveMoreYes != 0
		if len(valueProp.children) > 0 {
			childrenCopy = make([]*Prop, len(valueProp.children))
			copy(childrenCopy, valueProp.children)
		}
	}
	valueProp.mu.Unlock()

	// C: SUBSCRIPTION_MONITOR logic (prop_core.c:3285-3298, 3360-3363)
	//
	// Case 1: New sub has MONITOR flag and prop is not yet monitored:
	//   - Set PROP_MONITORED on prop
	//   - Check if there are existing non-monitor subs
	//   - If yes: activate (send MONITOR_ACTIVE to all monitor subs)
	//
	// Case 2: New sub does NOT have MONITOR flag and prop IS monitored:
	//   - Send MONITOR_ACTIVE to all monitor subs (this sub triggered activation)
	// NOTE: Monitor logic is handled above (in the canonical prop lock section).

	// C: prop_send_subscription_monitor_active (prop_core.c:1760-1772)
	// Send MONITOR_ACTIVE to all monitor subs outside the lock.
	// C: cb(opaque, PROP_SUBSCRIPTION_MONITOR_ACTIVE, user_int)
	// The notification is enqueued on the courier (or direct if no courier).
	for _, t := range monitorTargets {
		notifySub(t, EventSubscriptionMonitorActive, nil)
	}

	// Fire initial update outside the lock to avoid re-entrant deadlock.
	// C: prop_build_notify_value(s, direct, "prop_subscribe()", ...) +
	//    prop_build_notify_childv(s, pv, PROP_ADD_CHILD_VECTOR, ...)
	// C: direct = !!(flags & (PROP_SUB_DIRECT_UPDATE | PROP_SUB_INTERNAL))
	//    If direct=1: callback is synchronous even with a courier.
	//    If direct=0 and courier: enqueued on courier.
	//    If direct=0 and no courier (INTERNAL): callback is synchronous.
	if !destroyed && !noInitialUpdate {
		// 1. Fire value event (matching C's prop_build_notify_value)
		fireInitialValueEvent(sub, currentValue, currentPropType)

		// 2. C: if(value->hp_type == PROP_DIR && !(s->hps_flags & PROP_SUB_MULTI))
		//    MULTI subscriptions receive no initial child replay.
		//    C-canonical (prop_core.c:3310-3350): no selected child →
		//    PROP_ADD_CHILD_VECTOR (possibly empty; _DIRECT variant for
		//    direct subs); selected child → individual PROP_ADD_CHILD
		//    carrying gen_add_flags (PROP_ADD_SELECTED on the selected one).
		//    Go's EventAddChild args are (child, parent); receivers recompute
		//    the flag as child == parent.selectedChild.
		if currentPropType == PropTypeDir && !sub.multiSub {
			direct := sub.directUpdate || sub.internal
			if selectedChild == nil {
				evt := EventAddChildVector
				if direct {
					evt = EventAddChildVectorDirect
				}
				// C: prop_build_notify_childv(s, pv, ..., direct) —
				// direct=1 invokes the callback synchronously even
				// for GLOBAL subs (PROP_SUB_DIRECT_UPDATE).
				fireInitialNotify(sub, evt, direct, nil, childrenCopy)
			} else {
				for _, c := range childrenCopy {
					fireInitialNotify(sub, EventAddChild, direct, c, valueProp)
				}
			}

			// 3. C: if(value->hp_flags & PROP_HAVE_MORE) — initial
			//    HAVE_MORE_CHILDS_YES/NO. Direct/INTERNAL subs get a
			//    synchronous cb(opaque, e, user_int); others a queued
			//    notification (prop_core.c:3341-3358).
			if haveMore {
				evt := EventHaveMoreChildsNo
				if haveMoreYes {
					evt = EventHaveMoreChildsYes
				}
				if sub.directUpdate || sub.internal {
					fireInitialNotify(sub, evt, true, nil)
				} else {
					notifySub(sub, evt, nil)
				}
			}
		}
	}

	// C: if canonical == NULL and TRACK_DESTROY, fire PROP_DESTROYED immediately.
	// Go: if prop is already destroyed, fire EventDestroyed (TRACK_DESTROY)
	// or EventSetVoid (non-TRACK_DESTROY), matching C's prop_destroy0 behavior.
	if destroyed {
		if sub.trackDestroy {
			notifySub(sub, EventDestroyed, nil, sub)
		} else {
			// C: prop_notify_void(s) — direct passes value_prop, courier does not
			notifyVoidSub(sub, p)
		}
	}

	return sub
}

// sendMonitorActive — C: prop_send_subscription_monitor_active
// (prop_core.c:1757-1772). Sends MONITOR_ACTIVE to every monitor sub on
// p's value-subscription list.
func sendMonitorActive(p *Prop) {
	p.mu.RLock()
	targets := make([]*Subscription, 0, len(p.valueSubs))
	for _, t := range p.valueSubs {
		if t.subscriptionMonitor {
			targets = append(targets, t)
		}
	}
	p.mu.RUnlock()
	for _, t := range targets {
		notifySub(t, EventSubscriptionMonitorActive, nil)
	}
}

// fireInitialValueEvent fires the appropriate EventSet* event for the
// initial update, matching C's prop_build_notify_value switch on hp_type.
// C: prop_build_notify_value(s, direct, ...) — if direct=1 (PROP_SUB_DIRECT_UPDATE
// or PROP_SUB_INTERNAL), callback is synchronous even with a courier.
// Go: If sub.directUpdate is true, force direct callback regardless of courier.
func fireInitialValueEvent(sub *Subscription, value any, propType PropertyType) {
	// C: direct = !!(flags & (PROP_SUB_DIRECT_UPDATE | PROP_SUB_INTERNAL))
	// If direct, prop_build_notify_value calls callback directly (no courier enqueue).
	// C: COURIER, GLOBAL, and GROUP dispatch modes all enqueue (courier_enqueue0);
	// only INTERNAL/direct subs fire synchronously.
	buildNotifyValue(sub, value, propType, sub.directUpdate || sub.internal)
}

// buildNotifyValue — C: prop_build_notify_value(s, direct, origin, p, pnq)
// with pnq==NULL. direct=0 → only INTERNAL subs fire synchronously.
func buildNotifyValue(sub *Subscription, value any, propType PropertyType, direct bool) {

	// C: prop_build_notify_value — if PROP_SUB_SEND_VALUE_PROP,
	// emit PROP_VALUE_PROP with backing prop BEFORE the value event.
	// C: cb(opaque, PROP_VALUE_PROP, p) — NO user_int, and p is
	// s->hps_value_prop (the originator-resolved prop the sub is
	// attached to), not the canonical one.
	vp := sub.valueProp
	if vp == nil {
		vp = sub.prop
	}
	if sub.sendValueProp && vp != nil {
		notifyValueProp(sub, vp, direct)
	}

	switch propType {
	case PropTypeDir:
		fireInitialNotify(sub, EventSetDir, direct, nil)
	case PropTypeURI:
		// C: cb(opaque, PROP_SET_URI, hp_uri_title, hp_uri, user_int)
		if uv, ok := value.(URIValue); ok {
			fireInitialNotify(sub, EventSetURI, direct, nil, uv.Title, uv.URL)
		} else {
			fireInitialNotify(sub, EventSetURI, direct, nil, "", "")
		}
	case PropTypeInt:
		if v, ok := value.(int); ok {
			fireInitialNotify(sub, EventSetInt, direct, nil, v)
		} else if v, ok := value.(int64); ok {
			fireInitialNotify(sub, EventSetInt, direct, nil, int(v))
		} else {
			fireInitialNotify(sub, EventSetInt, direct, nil, 0)
		}
	case PropTypeFloat:
		if v, ok := value.(float32); ok {
			fireInitialNotify(sub, EventSetFloat, direct, nil, v)
		} else if v, ok := value.(float64); ok {
			fireInitialNotify(sub, EventSetFloat, direct, nil, float32(v))
		} else {
			fireInitialNotify(sub, EventSetFloat, direct, nil, float32(0))
		}
	case PropTypeString:
		// C: prop_build_notify_value fires PROP_SET_RSTRING for PROP_RSTRING props.
		// C: cb(opaque, PROP_SET_RSTRING, rstring, rstrtype, user_int)
		rstrType := PropStrUTF8
		if vp != nil {
			vp.mu.RLock()
			rstrType = vp.rstrType
			vp.mu.RUnlock()
		}
		if v, ok := value.(string); ok {
			fireInitialNotify(sub, EventSetRString, direct, nil, v, rstrType)
		} else {
			fireInitialNotify(sub, EventSetRString, direct, nil, "", rstrType)
		}
	case PropTypeProp:
		// C: n->hpn_prop = prop_ref_inc(p->hp_prop); n->hpn_event = PROP_SET_PROP;
		// C: cb(opaque, PROP_SET_PROP, p->hp_prop, user_int)
		if sub.prop != nil && sub.prop.propRef != nil {
			fireInitialNotify(sub, EventSetProp, direct, nil, sub.prop.propRef)
		}
	default:
		// PropTypeVoid or PropTypeProxy — fire EventSetVoid
		// C: case PROP_VOID: n->hpn_event = PROP_SET_VOID;
		// C: PROP_SUB_IGNORE_VOID skips initial void notification too
		// (prop_core.c:1270: if(flags & PROP_SUB_IGNORE_VOID && p->hp_type == PROP_VOID) return)
		if !sub.ignoreVoid {
			fireInitialNotify(sub, EventSetVoid, direct, nil)
		}
	}
}

// fireInitialNotify dispatches the initial update notification.
// If direct is true, the callback is invoked synchronously (C: direct mode).
// If direct is false and a courier is attached, the notification is enqueued.
func fireInitialNotify(sub *Subscription, event EventType, direct bool, prop *Prop, args ...any) {
	if sub == nil || !sub.active.Load() {
		return
	}
	if !direct {
		// Queued dispatch modes (COURIER / GLOBAL / GROUP) all enqueue via
		// courier_enqueue0 — C: prop_build_notify_value !direct path.
		// notifySub handles mode routing, refcounting, and user_int.
		notifySub(sub, event, prop, args...)
		return
	}
	// Direct mode: invoke callback synchronously
	// C: passes s->hps_user_int as last arg
	if sub.callback != nil {
		var fullArgs []any
		if prop != nil {
			fullArgs = make([]any, 0, len(args)+2)
			fullArgs = append(fullArgs, prop)
		} else {
			fullArgs = make([]any, 0, len(args)+1)
		}
		fullArgs = append(fullArgs, args...)
		fullArgs = append(fullArgs, sub.userInt)
		sub.callback(sub.opaque, event, fullArgs...)
	}
}

// GetRawValue returns the prop's stored value — C: hp_rstring/hp_cstring/
// hp_float/hp_int/hp_uri union. Used by prop_http's emit_value.
func (pm *PropManager) RequestNewChild(p *Prop) {
	if p == nil {
		return
	}
	p.mu.RLock()
	isDir := p.propType == PropTypeDir || p.propType == PropTypeVoid
	p.mu.RUnlock()
	if !isDir {
		return
	}
	p.mu.Lock()
	subs := make([]*Subscription, len(p.valueSubs))
	copy(subs, p.valueSubs)
	p.mu.Unlock()
	for _, sub := range subs {
		notifySub(sub, EventReqNewChild, nil, p)
	}
}

// ValueFloat creates a float value property
const SubNoInitialUpdate = 0x200 // C: PROP_SUB_NO_INITIAL_UPDATE

// SubFlagTrackDestroy flag for subscriptions.
// When set, the subscription receives EventDestroyed on prop destruction
// instead of EventSetVoid. C equivalent: PROP_SUB_TRACK_DESTROY.
const SubFlagTrackDestroy = 0x1 // C: PROP_SUB_TRACK_DESTROY

// SubFlagMulti flag for subscriptions.
// When set, the subscription receives notifications from all children of
// a MULTI_SUB prop. C equivalent: PROP_SUB_MULTI (0x10).
// Requires the parent prop to have FlagMultiSub set (via SetMulti).
const SubFlagMulti = 0x10

// SubFlagEarlyDelChild flag for subscriptions.
// When set, the subscription receives EventDelChild BEFORE the child is
// destroyed (early notification), while the child is still accessible.
// Subs without this flag receive EventDelChild AFTER the child is ZOMBIE.
// C equivalent: PROP_SUB_EARLY_DEL_CHILD (0x400).
const SubFlagEarlyDelChild = 0x400

// SubFlagExpedite flag for subscriptions.
// When set, notifications from this subscription are placed in the expedited
// queue and dispatched before normal notifications.
// C equivalent: PROP_SUB_EXPEDITE (0x8).
const SubFlagExpedite = 8

// SubFlagDirectUpdate flag for subscriptions.
// When set, the initial update is synchronous even if a courier is attached.
// Runtime notifications still go through the courier.
// C equivalent: PROP_SUB_DIRECT_UPDATE (0x10000).
const SubFlagDirectUpdate = 0x10000

// SubFlagIgnoreVoid flag for subscriptions.
// When set, the subscription does not receive EventSetVoid notifications.
// Used by settings/navigator to ignore void transitions.
// C equivalent: PROP_SUB_IGNORE_VOID (0x40).
const SubFlagIgnoreVoid = 0x40

// --- Deferred flag constants (defined but NOT wired to behavior) ---
// These constants exist so that callers passing C flag values do not
// silently get wrong behavior. Subscribe() recognizes them and returns
// an error or logs a warning if they are used.
// Each is documented with its C semantics and the proof of why it is
// not required by currently ported Go code.

// SubFlagTrackDestroyExp flag for subscriptions.
// C: PROP_SUB_TRACK_DESTROY_EXP (0x80) — TRACK_DESTROY + expedited dispatch.
// All notifications (including DESTROYED) are placed on the expedited queue.
// Implemented in propSubscribe0 as trackDestroy + expedite (see subscribe
// flag handling ~line 4629). Used by pkg/metadata/mlp.go subscriptions
// (mlp.c:292, 378, 1639, 1698, 1722, 1898 counterparts).
const SubFlagTrackDestroyExp = 0x80

// SubFlagSendValueProp flag for subscriptions.
// C: PROP_SUB_SEND_VALUE_PROP (0x100) — emits PROP_VALUE_PROP event
// with the backing prop_t* before each value update.
// NOT IMPLEMENTED in Go.
// Proof of non-requirement: The only C callers are glw_slider.c:577,
// glw_keyintercept.c:209, glw_text_bitmap.c:1007. The Go GLW widgets
// in pkg/glw/view/functions.go obtain the backing prop via the DSL
// bind() function (line 2789: widget.Props["__boundProp"] = prop),
// not via subscription callbacks. No Go code requires EventValueProp
// to be auto-emitted.
// Scope: Deferred until GLW widgets are ported to use prop subscriptions
// instead of DSL bind().
const SubFlagSendValueProp = 0x100

// SubFlagSubscriptionMonitor flag for subscriptions.
// C: PROP_SUB_SUBSCRIPTION_MONITOR (0x4) — receives
// PROP_SUBSCRIPTION_MONITOR_ACTIVE when subscriptions are added/removed.
// Implemented: propSubscribe0 sets sub.subscriptionMonitor and
// EventSubscriptionMonitorActive is fired to monitor subs when
// non-monitor subscriptions attach (see ~lines 4643, 4930, 5015).
// Used by pkg/metadata/mlp.go subscriptions.
const SubFlagSubscriptionMonitor = 0x4

// SubFlagSingleton flag for subscriptions.
// C: PROP_SUB_SINGLETON (0x40000) — prevents duplicate subscriptions
// for the same (callback, opaque) pair on the same value prop.
// NOT IMPLEMENTED in Go.
// Proof of non-requirement: C callers are fontstash.c:251,270 and
// plugins.c:421. Go equivalents: pkg/text/text.go (FontStashManager)
// does not create prop subscriptions. pkg/plugins/plugin_props.go:232-246
// uses its own PropertySubscription list, not prop.Subscribe.
// No Go code could create duplicate subscriptions via this path.
// Scope: Deferred until fontstash/plugins prop subscriptions are ported.
const SubFlagSingleton = 0x40000

// SubFlagAltPath flag for subscriptions.
// C: PROP_SUB_ALT_PATH (0x80000) — path resolution starts from
// PROP_TAG_ROOT instead of named-root lookup.
// NOT IMPLEMENTED in Go.
// Proof of non-requirement: C callers are prop_jni.c:259 and stpp.c:564.
// Go has no JNI prop code. Go STPP (pkg/api/mgpp/mgpp.go:682-716)
// resolves propRef to a *Prop directly and subscribes to it — it does
// not perform path resolution. The path and nameVec arguments are
// dropped (_ = path, _ = nameVec at pkg/mgpp/mgpp.go:508-510).
// Go's Subscribe takes a *Prop directly, making ALT_PATH semantics
// inapplicable to the current API.
// Scope: Deferred until STPP path-based subscription is implemented.
const SubFlagAltPath = 0x80000

// SubFlagDebug flag for subscriptions.
// C: PROP_SUB_DEBUG (0x2) — enables PROPTRACE logging for each
// notification dispatched to this subscription.
// NOT IMPLEMENTED in Go.
// Proof of non-requirement: C callers are es_prop.c:736-737 (JS debug
// option) and glw_view_eval.c:2518-2519 (widget debug mode).
// PROP_SUB_DEBUG has NO functional effect beyond logging output.
// Go does not expose a per-subscription debug trace.
// Scope: FUNCTIONAL PARITY: NOT REQUIRED. DEBUG/DIAGNOSTIC PARITY: NOT IMPLEMENTED.
const SubFlagDebug = 0x2

// SubFlagInternal flag for subscriptions.
// C: PROP_SUB_INTERNAL (0x20) — synchronous direct dispatch: the callback
// is invoked inline by the notifier and the subscription never enters the
// courier / global / group dispatch machinery (C: prop_dispatch_one
// asserts INTERNAL == 0). Used by the prop package's internal helpers
// (nodefilter, concat, grouper, reorder, window).
const SubFlagInternal = 0x20 // C: PROP_SUB_INTERNAL

// SubFlagDontLock flag for subscriptions.
// C: PROP_SUB_DONTLOCK (0x20000) — forces hps_lock = NULL so dispatch
// performs no lock-manager operations even if a mutex was supplied.
const SubFlagDontLock = 0x20000

// SubUserInt is a tag value passed in Subscribe args to set the subscription's
// user_int field. C equivalent: PROP_TAG_CALLBACK_USER_INT.
// The user_int value is passed as the LAST argument in every callback
// invocation, matching C's notify_invoke behavior (prop_core.c:767).
//
// Usage: pm.Subscribe(p, cb, opaque, SubUserInt(0x12345678))
type SubUserInt int

// SubCourier is a Subscribe() argument attaching a courier for async
// notification dispatch (C: PROP_SUB_DISPATCH_MODE_COURIER).
type SubCourier struct {
	C *Courier
}

// SubDispatchGroup is a Subscribe() argument marking the subscription as
// group-dispatched on the given shared dispatch group
// (C: PROP_TAG_DISPATCH_GROUP — a shared prop_sub_dispatch_t).
type SubDispatchGroup struct {
	G *DispatchGroup
}

// SubMutex is a Subscribe() argument setting the subscription's lock
// (C: PROP_TAG_MUTEX). The lock is held while the subscription's callback
// is dispatched by a courier or global/group worker (LOCKMGR ops).
// With the default lock manager, Ptr must be a *sync.Mutex; with a custom
// SubLockmgr it is whatever pointer the lock manager expects
// (e.g. *misc.Lockmgr for lockmgr_handler, or an opaque context pointer).
type SubMutex struct {
	Ptr any
}

// SubLockmgr is a Subscribe() argument setting a custom lock manager
// (C: PROP_TAG_LOCKMGR). The *Lockmgr pointer identity plays the role of
// the C function pointer (used for contention piggybacking).
type SubLockmgr struct {
	L *Lockmgr
}

// SubValueProp is a Subscribe() argument supplying the pre-resolved value
// prop (C: prop_subscribe name path — `value = prop_subfind(p, name, 1, 0,
// origin_chain)` resolved with follow_symlinks=1). When present, Subscribe
// uses it as hps_value_prop instead of following the canonical prop's own
// originator chain.
type SubValueProp struct {
	P *Prop
}

// SubOrigins is a Subscribe() argument supplying the origin chain — the
// link-dst props traversed during name resolution (C: origin_chain →
// s->hps_origin / s->hps_pots, prop_core.c:3211-3237).
type SubOrigins struct {
	Props []*Prop
}

// CreatePropVec creates a property vector
func (p *Prop) FireEvent(event EventType, args ...any) {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.destroyed {
		p.mu.Unlock()
		return
	}
	// C: prop_notify_simple iterates hp_value_subscriptions
	subsCopy := make([]*Subscription, len(p.valueSubs))
	copy(subsCopy, p.valueSubs)
	p.mu.Unlock()

	for _, sub := range subsCopy {
		notifySub(sub, event, nil, args...)
	}
}

// GetType returns the property type
func (pm *PropManager) Subscribe(prop *Prop, callback func(opaque any, eventType EventType, args ...any), opaque any, args ...any) *Subscription {
	return prop.Subscribe(callback, opaque, args...)
}

// SubscribeWithCourier creates a subscription that dispatches notifications
// via a courier queue (async dispatch) instead of synchronous callbacks.
// C: prop_subscribe with PROP_SUB_DISPATCH_MODE_COURIER (prop_core.c:3244-3249)
func (pm *PropManager) SubscribeWithCourier(prop *Prop, courier *Courier, callback func(opaque any, eventType EventType, args ...any), opaque any, args ...any) *Subscription {
	// C: hps_dispatch_mode/hps_dispatch are set during prop_subscribe_ex
	// before the initial update — the initial notify is enqueued on the
	// courier (courier_enqueue0), not invoked synchronously.
	if courier != nil {
		args = append(args, SubCourier{courier})
	}
	return prop.Subscribe(callback, opaque, args...)
}

// DestroyByName destroys a property by name
func (pm *PropManager) WantMoreChilds(sub *Subscription) {
	if sub == nil {
		return
	}
	// C: prop_want_more_childs (prop_core.c) → prop_proxy_want_more_childs
	// when s->hps_proxy (sends STPP_CMD_WANT_MORE_CHILDS over the wire).
	if sub.proxyConn != nil {
		if w, ok := sub.proxyConn.(ProxyWantMorer); ok {
			w.ProxyWantMoreChilds(sub)
			return
		}
	}
	if sub.prop == nil {
		return
	}
	p := sub.prop
	p.mu.RLock()
	subs := make([]*Subscription, len(p.valueSubs))
	copy(subs, p.valueSubs)
	p.mu.RUnlock()
	for _, s := range subs {
		notifySub(s, EventWantMoreChilds, nil)
	}
}

// FireWantMoreChilds fires EventWantMoreChilds to all subscribers of this prop.
// Used by the GLW cloner to request more children from the backend when
// the focused child is near the end of the list (pagination).
// C: prop_want_more_childs(sub) — fires PROP_WANT_MORE_CHILDS on the subscription.
func (p *Prop) FireWantMoreChilds() {
	if p == nil {
		return
	}
	p.mu.RLock()
	subs := make([]*Subscription, len(p.valueSubs))
	copy(subs, p.valueSubs)
	p.mu.RUnlock()
	for _, s := range subs {
		notifySub(s, EventWantMoreChilds, nil)
	}
}

// MarkChilds marks all children of a property
// MarkChilds sets PROP_MARKED flag on all children of a directory prop.
// C: prop_mark_childs (prop_core.c:5793) — if p is PROP_DIR, set PROP_MARKED on each child.
// MarkChilds marks all children of a prop with PROP_MARKED.
// C: prop_mark_childs (prop_core.c:5793-5804) — only marks if hp_type == PROP_DIR.
func (pm *PropManager) SubReemit(s *Subscription) {
	if s == nil {
		return
	}
	vp := s.valueProp
	if vp == nil {
		vp = s.prop
	}
	if vp == nil {
		return
	}
	vp.mu.RLock()
	value := vp.value
	pt := vp.propType
	vp.mu.RUnlock()
	buildNotifyValue(s, value, pt, false)
}

// CopyEx copies the value of src into dst by type.
// C: prop_copy_ex (prop_core.c:4239) — src==NULL voids dst; INT/FLOAT/
// RSTRING/CSTRING are copied, everything else voids dst.
