package prop

// Split from prop.go — Fase 4 pure-move refactor.
// No symbol, lock, or event-order changes.

import (
	"github.com/czz/movian-go/internal/misc"
	"strings"
	"sync/atomic"
)

const (
	PropStrUTF8 = 0 // C: PROP_STR_UTF8
	PropStrRich = 1 // C: PROP_STR_RICH
)

// URIValue stores a URI property's title and URL as separate values.
// C: prop_t stores hp_uri_title and hp_uri as separate rstring_t pointers.
// Go: stores a URIValue in prop.value when propType is PropTypeURI.
type StringType int

const (
	StringUTF8 StringType = 0 // C: PROP_STR_UTF8
	StringRich StringType = 1 // C: PROP_STR_RICH
)

// tagEntry represents a single tag in a prop's tag list.
// C: prop_tag_t (prop_tags.c) — linked list node with key/value.
// C's key is a void* (opaque owner pointer, e.g. an es_prop_sub_t);
// Go: any so both string and pointer keys compare correctly.
func (pm *PropManager) SetStringEx(prop *Prop, opaque any, value string, strType StringType) int {
	if prop == nil {
		return -1
	}
	// C: p->hp_rstrtype = type; (prop_core.c:3575, 3628)
	prop.mu.Lock()
	prop.rstrType = int(strType)
	prop.mu.Unlock()
	return setPropValue(prop, opaque, value)
}

// SetIntEx sets an integer property value
func (pm *PropManager) SetIntEx(prop *Prop, opaque any, value int) int {
	return setPropValue(prop, opaque, value)
}

// SetFloatEx sets a float property value
func (pm *PropManager) SetFloatEx(prop *Prop, opaque any, value float32) int {
	return setPropValue(prop, opaque, value)
}

// SetVEx sets a property value by name.
// C: prop_find(parent, name) + prop_set_*_ex(child, skipme, value).
// Go: Finds child by name, then routes through setPropValue for full
// canonical semantics (destroyed check, equality check, propType update,
// notification, multi-parent chain, target forwarding).
// SetVEx sets a value on a child by name, creating the child if it doesn't exist.
// C: prop_setv_ex (prop_core.c:5588) — each name argument is one path
// level; the C varargs list is traversed level by level, creating
// missing children (prop_create0) until the leaf, which gets the value.
// Go: name may be a dotted path ("skin.path") — split on '.' and
// traverse/create each level, matching C's multi-arg traversal.
func (pm *PropManager) SetVEx(opaque any, parent *Prop, name string, value any) int {
	if parent == nil {
		return -1
	}
	p := parent
	for n := range strings.SplitSeq(name, ".") {
		p.mu.RLock()
		if p.destroyed {
			p.mu.RUnlock()
			return -1
		}
		var child *Prop
		if p.childIndex != nil {
			child = p.childIndex[n]
		}
		p.mu.RUnlock()

		// C: if c == NULL, c = prop_create0(p, n, skipme, 0)
		if child == nil {
			child = pm.CreateEx(p, n, nil, false, false)
			if child == nil {
				return -1
			}
		}
		p = child
	}
	return setPropValue(p, opaque, value)
}

// SetVoidEx sets a void property value
func (pm *PropManager) SetVoidEx(prop *Prop, opaque any) int {
	return setPropValue(prop, opaque, nil)
}

// setPropValue is the shared implementation for SetStringEx, SetIntEx, SetFloatEx, SetVoidEx.
// opaque is the originating subscription (skipme in C's prop_set_int_ex).
// If opaque is a *Subscription, that subscription is skipped during notification,
// matching C's prop_notify_value(p, skipme, origin) behavior.
// propClean is the Go equivalent of C's prop_clean (prop_core.c:1819).
// It prepares a prop for a type transition by releasing its current content.
// Returns true if the prop CANNOT be cleaned (caller must abort the transition).
// Returns false if the prop was successfully cleaned (caller may proceed).
//
// C behavior:
//   - PROP_CLIPPED_VALUE → return 1 (refuse to clean)
//   - PROP_DIR → check for canonical subs descending; if found return 1;
//     otherwise destroy all children → return 0
//   - PROP_ZOMBIE / PROP_PROXY → return 1 (refuse)
//   - PROP_VOID / INT / FLOAT / CSTRING → no-op, return 0
//   - PROP_RSTRING → release rstring, return 0
//   - PROP_PROP → ref_dec referenced prop, return 0
//   - PROP_URI → release title + url rstrings, return 0
//
// propClean is the Go equivalent of C's prop_clean (prop_core.c:1819).
// CAUTION: caller must hold p.mu.Lock() — this function does NOT re-lock p.
func clipInt(p *Prop, v int) int {
	if p.flags&FlagClippedValue == 0 {
		return v
	}
	if v < p.clipMinInt {
		return p.clipMinInt
	}
	if v > p.clipMaxInt {
		return p.clipMaxInt
	}
	return v
}

// clipFloat clamps a float32 value to the prop's clipping range (C: PROP_CLIPPED_VALUE).
func clipFloat(p *Prop, v float32) float32 {
	if p.flags&FlagClippedValue == 0 {
		return v
	}
	if v < p.clipMinFloat {
		return p.clipMinFloat
	}
	if v > p.clipMaxFloat {
		return p.clipMaxFloat
	}
	return v
}
func setPropValue(prop *Prop, opaque any, value any) int {
	if prop == nil {
		return -1
	}

	// Extract skipme subscription from opaque (C: prop_sub_t *skipme)
	var skipme *Subscription
	if opaque != nil {
		if sub, ok := opaque.(*Subscription); ok {
			skipme = sub
		}
	}

	// C: If prop is PROP_PROXY, forward to proxy setter instead of setting locally.
	// C: prop_set_int_exl checks hp_type == PROP_PROXY → prop_proxy_set_int(p, v)
	// Go: Check proxyConn (set by pkg/proxy) and forward via ProxySetter interface.
	prop.mu.RLock()
	isProxy := prop.propType == PropTypeProxy
	proxyConn := prop.proxyConn
	strType := StringType(prop.rstrType)
	prop.mu.RUnlock()
	if isProxy {
		// C: prop_set_uri_exl on a PROP_PROXY is refused by prop_clean
		// (prop_core.c:1843 → return 1) — there is no prop_proxy_set_uri.
		if _, isURI := value.(URIValue); isURI {
			return 0
		}
		if ps, ok := proxyConn.(ProxySetter); ok && ps != nil {
			switch v := value.(type) {
			case bool:
				iv := 0
				if v {
					iv = 1
				}
				ps.ProxySetInt(prop, iv)
			case int:
				ps.ProxySetInt(prop, v)
			case int64:
				ps.ProxySetInt(prop, int(v))
			case float32:
				ps.ProxySetFloat(prop, v)
			case float64:
				ps.ProxySetFloat(prop, float32(v))
			case string:
				// C: prop_proxy_set_string(p, str, type) — strType is the
				// caller's type (prop_set_string_exl → rstrType).
				ps.ProxySetString(prop, v, strType)
			case nil:
				ps.ProxySetVoid(prop)
			}
			return 0
		}
		// If proxyConn doesn't implement ProxySetter, fall through to local set
	}

	prop.mu.Lock()
	// C: prop_set checks hp_type == PROP_ZOMBIE and returns early.
	if prop.destroyed {
		prop.mu.Unlock()
		return -1
	}

	// C: prop_clean is called before any type transition.
	// C: prop_set_int_exl: if hp_type != PROP_INT { if hp_type == PROP_FLOAT { convert } else { prop_clean } }
	// C: prop_set_string_exl: if hp_type != PROP_RSTRING { prop_clean }
	// Go: Determine the target type and call propClean if transitioning.
	newType := PropTypeVoid
	switch value.(type) {
	case bool, int, int64:
		newType = PropTypeInt
	case float32, float64:
		newType = PropTypeFloat
	case string:
		newType = PropTypeString
	case URIValue:
		newType = PropTypeURI
	case nil:
		newType = PropTypeVoid
	}

	// C: int↔float conversion preserves the value without prop_clean.
	isNumericTransition := (prop.propType == PropTypeInt && newType == PropTypeFloat) ||
		(prop.propType == PropTypeFloat && newType == PropTypeInt)

	if !isNumericTransition && prop.propType != newType {
		// Check if propClean would refuse (without side effects)
		needClean := true
		if prop.flags&FlagClippedValue != 0 {
			// propClean would refuse
			prop.mu.Unlock()
			return 0
		}
		if prop.propType == PropTypeProxy {
			prop.mu.Unlock()
			return 0
		}
		// For PropTypeDir: check canonical subs (needs lock, already held)
		if prop.propType == PropTypeDir {
			if hasCanonicalSubsDescendingLocked(prop) {
				prop.mu.Unlock()
				return 0
			}
			// C: prop_destroy_childs0(p):
			//   TAILQ_MOVE childs, TAILQ_INIT hp_childs
			//   p->hp_type = PROP_VOID; p->hp_selected = NULL;
			//   prop_notify_value(p, NULL, ...)
			//   for each child: EARLY_DEL_CHILD notifs, hp_parent=NULL, prop_destroy0(c)
			children := make([]*Prop, len(prop.children))
			copy(children, prop.children)
			prop.children = nil
			prop.childIndex = nil
			prop.selectedChild = nil
			prop.propType = PropTypeVoid // C: p->hp_type = PROP_VOID
			prop.value = nil
			// Notify value subs of the void transition (C: prop_notify_value)
			subsToNotify := make([]*Subscription, len(prop.valueSubs))
			copy(subsToNotify, prop.valueSubs)
			prop.mu.Unlock()

			// C: prop_notify_value(p, NULL, "prop_destroy_childs0()") — notify void FIRST
			for _, sub := range subsToNotify {
				notifySubscriber(sub, nil)
			}

			// Then fire EARLY_DEL_CHILD for each child, then destroy (outside lock)
			mgr := prop.manager
			for _, child := range children {
				// C: LIST_FOREACH(s, &p->hp_value_subscriptions, ...)
				//   if(s->hps_flags & PROP_SUB_EARLY_DEL_CHILD)
				//     prop_build_notify_child(s, c, PROP_DEL_CHILD, 0, 0);
				for _, sub := range subsToNotify {
					if sub.earlyDelChild {
						notifySub(sub, EventDelChild, child, prop)
					}
				}
				child.mu.Lock()
				child.parent = nil
				child.mu.Unlock()
				if mgr != nil {
					mgr.Destroy(child)
				}
			}

			// Re-lock and re-check
			prop.mu.Lock()
			if prop.destroyed {
				prop.mu.Unlock()
				return -1
			}
		} else if prop.propType == PropTypeProp {
			// Release prop reference (no unlock needed — no external calls)
			if prop.propRefSub != nil {
				prop.propRefSub.active.Store(false)
				prop.propRefSub = nil
			}
			if prop.propRef != nil {
				atomic.AddInt32(&prop.propRef.refCount, -1)
				prop.propRef = nil
			}
		}
		_ = needClean
	}

	// C ordering (prop_set_int_exl / prop_set_float_exl /
	// prop_set_rstring_exl): the same-value early return and the
	// PROP_CLIPPED_VALUE clamp are inside the same-type branch — they
	// apply ONLY when the prop already holds the target type (after
	// any int↔float storage conversion). Cross-type transitions and
	// float→int sets skip both and always notify.
	//
	// int set (prop_core.c:3968-3990):
	//   if hp_type != PROP_INT → convert/clean → assign → notify
	//   else if hp_int == v → return
	//   else if CLIPPED → clamp → assign → notify
	//
	// float set (prop_core.c:3858-3885): prop_get_float_locked
	//   converts INT→FLOAT first, then hp_float != v → clip → notify.
	//
	// bool: stored as int 0/1 (C: prop_set_int(p, v ? 1 : 0)) —
	// normalize before comparison so Int props hit the early return.
	if b, isBool := value.(bool); isBool {
		if b {
			value = 1
		} else {
			value = 0
		}
	}
	switch v := value.(type) {
	case int:
		if prop.propType == PropTypeInt {
			if valuesEqual(prop.value, value) {
				prop.mu.Unlock()
				return 0
			}
			value = clipInt(prop, v)
		}
	case int64:
		if prop.propType == PropTypeInt {
			if valuesEqual(prop.value, value) {
				prop.mu.Unlock()
				return 0
			}
			value = int64(clipInt(prop, int(v)))
		}
	case float32:
		// C: INT props convert to FLOAT first (prop_int_to_float), so
		// the equality check compares the converted current value.
		if prop.propType == PropTypeFloat || prop.propType == PropTypeInt {
			if valuesEqual(prop.value, value) {
				prop.mu.Unlock()
				return 0
			}
			value = clipFloat(prop, v)
		}
	case float64:
		if prop.propType == PropTypeFloat || prop.propType == PropTypeInt {
			if valuesEqual(prop.value, value) {
				prop.mu.Unlock()
				return 0
			}
			value = float64(clipFloat(prop, float32(v)))
		}
	default:
		// string / URIValue / nil(void): C strcmp / type check applies
		// only when already of the target type.
		if prop.propType == newType && valuesEqual(prop.value, value) {
			prop.mu.Unlock()
			return 0
		}
	}

	prop.value = value
	// Update propType based on value type (matching C's prop_set behavior)
	switch v := value.(type) {
	case bool:
		// C: booleans are stored as int (prop_set_int(p, v ? 1 : 0))
		prop.value = 0
		if v {
			prop.value = 1
		}
		prop.propType = PropTypeInt
	case int:
		prop.propType = PropTypeInt
	case int64:
		prop.propType = PropTypeInt
	case float32:
		prop.propType = PropTypeFloat
	case float64:
		prop.propType = PropTypeFloat
	case string:
		prop.propType = PropTypeString
	case URIValue:
		prop.propType = PropTypeURI
	case nil:
		prop.propType = PropTypeVoid
	}
	// Copy value subs and targets while holding lock
	// C: prop_notify_value iterates hp_value_subscriptions
	subs := make([]*Subscription, len(prop.valueSubs))
	copy(subs, prop.valueSubs)
	targets := make([]*Prop, len(prop.targets))
	copy(targets, prop.targets)
	prop.mu.Unlock()

	// Notify subscribers (matching C's prop_set behavior)
	// C: prop_notify_value(p, skipme, origin) skips the originating subscription.
	// Go: Skip the subscription identified by opaque (skipme).
	// notifySubscriber → notifySub handles active check and dispatch mode.
	for _, sub := range subs {
		if sub == skipme {
			continue
		}
		notifySubscriber(sub, value)
	}

	// C: prop_notify_value multi-parent chain (prop_core.c:1551-1556):
	//   if(p->hp_flags & PROP_MULTI_NOTIFY)
	//     while((p = p->hp_parent) != NULL)
	//       if(p->hp_flags & PROP_MULTI_SUB)
	//         LIST_FOREACH(s, &p->hp_value_subscriptions, ...)
	//           if(s->hps_flags & PROP_SUB_MULTI)
	//             prop_build_notify_value(s, 0, origin, p, NULL);
	// Go: Walk up parents and notify MULTI subs.
	notifyMultiParentChain(prop, skipme, value)

	// C-canonical: No value forwarding to targets.
	// In C, after prop_link0, subscriptions are retargeted to source.
	// prop_notify_value(source) notifies all subs on source, which includes
	// the retargeted subs from target. No need to update target's value or
	// notify target's subs separately.
	return 0
}

// propValueCompare — C: prop_value_compare (prop_core.c:4277-4303).
// Same-type value equality: VOID/ZOMBIE always equal; DIR/PROP/PROXY and
// unknown types never equal; int↔float is NOT cross-compared (type
// mismatch → unequal).
func propValueCompare(a, b *Prop) bool {
	if a == nil || b == nil {
		return false
	}
	a.mu.RLock()
	at, av := a.propType, a.value
	a.mu.RUnlock()
	b.mu.RLock()
	bt, bv := b.propType, b.value
	b.mu.RUnlock()
	if at != bt {
		return false
	}
	switch at {
	case PropTypeVoid, PropTypeZombie:
		return true
	case PropTypeString, PropTypeURI, PropTypeInt, PropTypeFloat:
		return valuesEqual(av, bv)
	default:
		return false
	}
}

// retargetSubscription — C: retarget_subscription (prop_core.c:4355-4405).
// Moves s onto src's value-subscription list (LIST_INSERT_HEAD), flushes
// the previous DIR value with SET_VOID, activates monitors on a
// MONITORED src, then pushes src's current value + DIR children as
// individual PROP_ADD_CHILD events.
func valuesEqual(a, b any) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	switch av := a.(type) {
	case int:
		switch bv := b.(type) {
		case int:
			return av == bv
		case int64:
			return int64(av) == bv
		case float32:
			return float32(av) == bv
		case float64:
			return float64(av) == bv
		}
	case int64:
		switch bv := b.(type) {
		case int:
			return av == int64(bv)
		case int64:
			return av == bv
		case float32:
			return float32(av) == bv
		case float64:
			return float64(av) == bv
		}
	case float32:
		switch bv := b.(type) {
		case int:
			return av == float32(bv)
		case int64:
			return av == float32(bv)
		case float32:
			return av == bv
		case float64:
			return float64(av) == bv
		}
	case float64:
		switch bv := b.(type) {
		case int:
			return av == float64(bv)
		case int64:
			return av == float64(bv)
		case float32:
			return av == float64(bv)
		case float64:
			return av == bv
		}
	case string:
		if bv, ok := b.(string); ok {
			return av == bv
		}
	case URIValue:
		if bv, ok := b.(URIValue); ok {
			return av.Title == bv.Title && av.URL == bv.URL
		}
	}
	return false
}

// notifySubscriber dispatches a value-change notification to a single subscriber.
func (pm *PropManager) SetURIEx(prop *Prop, opaque any, title, url string) int {
	if prop == nil {
		return -1
	}
	// C: if(title == NULL && url == NULL) { prop_set_void_ex(p, skipme); return; }
	if title == "" && url == "" {
		return setPropValue(prop, opaque, nil)
	}
	return setPropValue(prop, opaque, URIValue{Title: title, URL: url})
}

// SetParentEx sets the parent of a property.
//
// Reproduces C's prop_set_parent0 (prop_core.c:2112):
//   - If prop's parent is already newParent, do NOT re-insert or fire
//     EventAddChild (C calls prop_move0 for reorder only, no ADD_CHILD).
//   - If prop's parent differs from newParent, unparent from old, set new
//     parent, insert into children, and fire EventAddChild.
//   - If newParent is nil, just unparent (remove from old parent's children).
//
// SetParentEx sets the parent of a prop, optionally inserting before another child.
// C: prop_set_parent_ex (prop_core.c:2137) → prop_set_parent0 (prop_core.c:2112)
// C signature: prop_set_parent_ex(p, parent, before, skipme)
// Go: opaque can be *Prop (used as 'before') or *Subscription (used as 'skipme').
// SetParentOpaque carries both a 'before' sibling and a 'skipme'
// subscription for SetParentEx.
// C: prop_set_parent0(p, parent, before, skipme)
func (pm *PropManager) GetString(prop *Prop, defaultValue string) string {
	if prop == nil {
		return defaultValue
	}
	prop.mu.RLock()
	if prop.linkedTo != nil {
		target := prop.linkedTo
		prop.mu.RUnlock()
		return pm.GetString(target, defaultValue)
	}
	defer prop.mu.RUnlock()
	if str, ok := prop.value.(string); ok {
		return str
	}
	// URI props: return the URL as the string representation
	if uv, ok := prop.value.(URIValue); ok {
		return uv.URL
	}
	return defaultValue
}

// GetInt gets an integer property value
func (pm *PropManager) GetInt(prop *Prop, defaultValue int) int {
	if prop == nil {
		return defaultValue
	}
	prop.mu.RLock()
	if prop.linkedTo != nil {
		target := prop.linkedTo
		prop.mu.RUnlock()
		return pm.GetInt(target, defaultValue)
	}
	defer prop.mu.RUnlock()
	if i, ok := prop.value.(int); ok {
		return i
	}
	return defaultValue
}

// GetFloat gets a float property value
func (pm *PropManager) GetFloat(prop *Prop, defaultValue float32) float32 {
	if prop == nil {
		return defaultValue
	}
	prop.mu.RLock()
	if prop.linkedTo != nil {
		target := prop.linkedTo
		prop.mu.RUnlock()
		return pm.GetFloat(target, defaultValue)
	}
	defer prop.mu.RUnlock()
	if f, ok := prop.value.(float32); ok {
		return f
	}
	return defaultValue
}

// GetValue gets a property value, following the originator chain.
// C: Linked props delegate value reads to the source via subscriptions.
// Go: Direct read with originator chain following for linked props.
func (pm *PropManager) GetValue(prop *Prop) any {
	if prop == nil {
		return nil
	}
	prop.mu.RLock()
	originator := prop.originator
	propRef := prop.propRef
	propType := prop.propType
	val := prop.value
	prop.mu.RUnlock()
	if val != nil {
		return val
	}
	// C: PROP_PROP is a pointer to another prop — reads follow the pointer
	if propType == PropTypeProp && propRef != nil && propRef != prop {
		return pm.GetValue(propRef)
	}
	if originator != nil && originator != prop {
		return pm.GetValue(originator)
	}
	return nil
}

// SetValue sets a property value.
// C: prop_set_* family — full canonical path with notification.
// Go: Routes through setPropValue for full canonical semantics.
func (pm *PropManager) SetValue(prop *Prop, value any) {
	if prop == nil {
		return
	}
	setPropValue(prop, nil, value)
}

// CreateString creates a string property
func (pm *PropManager) CreateString(parent *Prop, name string, value string) *Prop {
	prop := pm.CreateEx(parent, name, nil, false, false)
	if prop != nil {
		pm.SetStringEx(prop, nil, value, StringUTF8)
	}
	return prop
}

// CreateInt creates an integer property
func (pm *PropManager) CreateInt(parent *Prop, name string, value int) *Prop {
	prop := pm.CreateEx(parent, name, nil, false, false)
	if prop != nil {
		pm.SetIntEx(prop, nil, value)
	}
	return prop
}

// CreateFloat creates a float property
func (pm *PropManager) CreateFloat(parent *Prop, name string, value float32) *Prop {
	prop := pm.CreateEx(parent, name, nil, false, false)
	if prop != nil {
		pm.SetFloatEx(prop, nil, value)
	}
	return prop
}

// Unlink unlinks a property from its originator.
// C: prop_unlink_exl → prop_unlink0
//  1. dst->hp_originator = NULL
//  2. LIST_REMOVE(dst, hp_originator_link) — remove from src's targets
//  3. prop_follow_and_unlink — recursively restore subscriptions to dst
//  4. If PROP_XREFED_ORIGINATOR: prop_destroy0(src)
//
// Go: removes target from originator's targets list, clears linkedTo and
// originator. Target keeps its current value and children (becomes independent).
func (p *Prop) SetInt(value int) {
	setPropValue(p, nil, value)
}

// SetString sets a string value
func (p *Prop) SetString(value string) {
	setPropValue(p, nil, value)
}

// SetFloat sets a float value
func (p *Prop) SetFloat(value float32) {
	setPropValue(p, nil, value)
}

// AddFloat adds delta to the current float value and notifies subscribers.
// C: prop_add_float_ex(p, NULL, delta) — reads current value, adds delta,
// calls prop_set_float_exl which does equality check and prop_notify_value.
// Go: Reads current value, then calls setPropValue (canonical path) which
// handles destroyed check, equality check, propType update, multi-parent
// chain notification, and target forwarding.
func (p *Prop) AddFloat(delta float32) {
	p.mu.Lock()
	if p.destroyed {
		p.mu.Unlock()
		return
	}
	// C: prop_get_float_locked(p) — converts INT to FLOAT in-place, or prop_clean for other types.
	// Go: Coerce current value to float32 (int↔float transition is allowed without prop_clean).
	cur := float32(0)
	if f, ok := p.value.(float32); ok {
		cur = f
	} else if f, ok := p.value.(float64); ok {
		cur = float32(f)
	} else if i, ok := p.value.(int); ok {
		cur = float32(i)
	} else if i, ok := p.value.(int64); ok {
		cur = float32(i)
	}
	// C: if PROP_CLIPPED_VALUE, clamp the result.
	result := cur + delta
	if p.flags&FlagClippedValue != 0 {
		result = clipFloat(p, result)
	}
	p.mu.Unlock()
	// C: if n != p->hp_float, set and notify. Go: setPropValue handles equality check.
	setPropValue(p, nil, result)
}

// AddInt adds delta to the prop's int value.
// C: prop_add_int_ex(p, NULL, v) (prop_core.c) — coerces FLOAT→INT,
// prop_clean for other types, clips via PROP_CLIPPED_VALUE, set+notify.
func (p *Prop) AddInt(delta int) {
	// C: if(p->hp_type == PROP_PROXY) { prop_proxy_add_int(p, v); return; }
	// (prop_core.c:4020-4030) — the remote end owns the real value; a
	// local read-modify-write would be meaningless.
	p.mu.RLock()
	isProxy := p.propType == PropTypeProxy
	pc := p.proxyConn
	p.mu.RUnlock()
	if isProxy {
		if pa, ok := pc.(ProxyAdder); ok && pa != nil {
			pa.ProxyAddInt(p, delta)
		}
		return
	}
	p.mu.Lock()
	if p.destroyed {
		p.mu.Unlock()
		return
	}
	// C: prop_get_int_locked(p) — converts FLOAT to INT in-place, or prop_clean.
	cur := 0
	if i, ok := p.value.(int); ok {
		cur = i
	} else if i, ok := p.value.(int64); ok {
		cur = int(i)
	} else if f, ok := p.value.(float32); ok {
		cur = int(f)
	} else if f, ok := p.value.(float64); ok {
		cur = int(f)
	}
	// C: if PROP_CLIPPED_VALUE, clamp the result.
	result := cur + delta
	if p.flags&FlagClippedValue != 0 {
		result = clipInt(p, result)
	}
	p.mu.Unlock()
	// C: if n != p->hp_int, set and notify. Go: setPropValue handles equality check.
	setPropValue(p, nil, result)
}

// CreateString creates a child property with a string value.
// C: prop_create(parent, name) + prop_set_string(child, value).
// Go: Creates child with full CreateEx semantics (childIndex, EventAddChild,
// MULTI_NOTIFY inheritance) and sets the initial value.
func (p *Prop) CreateString(name string, value string) *Prop {
	child := p.createChildCanonical(name)
	if child != nil {
		setPropValue(child, nil, value)
	}
	return child
}

// CreateInt creates a child property with an integer value.
// C: prop_create(parent, name) + prop_set_int(child, value).
func (p *Prop) CreateInt(name string, value int) *Prop {
	child := p.createChildCanonical(name)
	if child != nil {
		setPropValue(child, nil, value)
	}
	return child
}

// CreateFloat creates a child property with a float value.
// C: prop_create(parent, name) + prop_set_float(child, value).
func (p *Prop) CreateFloat(name string, value float64) *Prop {
	child := p.createChildCanonical(name)
	if child != nil {
		setPropValue(child, nil, float32(value))
	}
	return child
}

// createChildCanonical is the shared logic for CreateString/Int/Float.
// It matches C's prop_create0: prop_make_dir(parent) → prop_insert
// (with childIndex update, EventAddChild, MULTI_NOTIFY inheritance).
// The value and propType are set by the caller after this returns.
func (pm *PropManager) SetPropExl(target *Prop, opaque any, source *Prop) int {
	if target == nil || source == nil {
		return -1
	}

	// Extract skipme from opaque
	var skipme *Subscription
	if sub, ok := opaque.(*Subscription); ok {
		skipme = sub
	}

	target.mu.Lock()
	if target.destroyed {
		target.mu.Unlock()
		return -1
	}

	// C: if(p->hp_type == PROP_PROP && p->hp_prop == target) return;
	if target.propType == PropTypeProp && target.propRef == source {
		target.mu.Unlock()
		return 0
	}

	// C: if(p->hp_type != PROP_PROP) { if(prop_clean(p)) return; }
	// Go: For non-PropTypeProp, we need to clean the old value.
	// propClean can refuse (return true) for CLIPPED, PROXY, or DIR with canonical subs.
	if target.propType != PropTypeProp {
		// Check propClean refusal conditions
		if target.flags&FlagClippedValue != 0 {
			target.mu.Unlock()
			return -1
		}
		if target.propType == PropTypeProxy {
			target.mu.Unlock()
			return -1
		}
		if target.propType == PropTypeDir {
			if hasCanonicalSubsDescendingLocked(target) {
				target.mu.Unlock()
				return -1
			}
			// C: prop_destroy_childs0(p):
			//   TAILQ_MOVE childs, TAILQ_INIT hp_childs
			//   p->hp_type = PROP_VOID; p->hp_selected = NULL;
			//   prop_notify_value(p, NULL, ...)
			//   for each child: EARLY_DEL_CHILD notifs, hp_parent=NULL, prop_destroy0(c)
			children := make([]*Prop, len(target.children))
			copy(children, target.children)
			target.children = nil
			target.childIndex = nil
			target.selectedChild = nil
			target.propType = PropTypeVoid
			target.value = nil
			// Notify value subs of the void transition (C: prop_notify_value)
			subsToNotify := make([]*Subscription, len(target.valueSubs))
			copy(subsToNotify, target.valueSubs)
			target.mu.Unlock()

			// Fire EARLY_DEL_CHILD for each child, then destroy (outside lock)
			mgr := target.manager
			for _, child := range children {
				for _, sub := range subsToNotify {
					if sub.earlyDelChild {
						notifySub(sub, EventDelChild, child, target)
					}
				}
				child.mu.Lock()
				child.parent = nil
				child.mu.Unlock()
				if mgr != nil {
					mgr.Destroy(child)
				}
			}

			// Notify value subs of void (C: prop_notify_value(p, NULL, ...))
			for _, sub := range subsToNotify {
				notifySubscriber(sub, nil)
			}

			target.mu.Lock()
		} else {
			// For RSTRING, URI, etc: just clear the value
			target.value = nil
		}
	} else {
		// C: else { prop_ref_dec_locked(p->hp_prop); }
		oldRef := target.propRef
		oldSub := target.propRefSub
		target.propRef = nil
		target.propRefSub = nil
		target.mu.Unlock()

		if oldSub != nil {
			oldSub.active.Store(false)
		}
		if oldRef != nil {
			atomic.AddInt32(&oldRef.refCount, -1)
		}
		target.mu.Lock()
	}

	// C: p->hp_prop = prop_ref_inc(target); p->hp_type = PROP_PROP;
	atomic.AddInt32(&source.refCount, 1)
	target.propRef = source
	target.propType = PropTypeProp
	// C: does NOT copy value — PROP_PROP is a pointer, not a value copy
	// Go: keep value nil for C parity (Get* follows propRef for reads)

	// C: prop_notify_value(p, skipme, "prop_set_prop()")
	// Sends PROP_SET_PROP with source prop pointer to all value subs
	targetSubs := make([]*Subscription, len(target.valueSubs))
	copy(targetSubs, target.valueSubs)
	target.mu.Unlock()

	for _, sub := range targetSubs {
		if sub == skipme {
			continue
		}
		notifySub(sub, EventSetProp, source, source)
	}

	return 0
}

// CreateInfo creates an info setting (helper function for settings)
func (p *Prop) SetClippedInt(min, max int) {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.propType == PropTypeZombie {
		p.mu.Unlock()
		return
	}
	if p.propType != PropTypeInt {
		if p.propType == PropTypeFloat {
			// C: prop_float_to_int — preserve value + range
			if fv, ok := p.value.(float32); ok {
				p.value = int(fv)
			} else {
				p.value = 0
			}
			p.clipMinInt = int(p.clipMinFloat)
			p.clipMaxInt = int(p.clipMaxFloat)
			p.propType = PropTypeInt
		} else if p.flags&FlagClippedValue != 0 || p.propType == PropTypeProxy ||
			p.propType == PropTypeDir || p.propType == PropTypeProp {
			// C: prop_clean(p) refusal cases
			p.mu.Unlock()
			return
		} else {
			p.value = 0
			p.propType = PropTypeInt
		}
	}
	p.flags |= FlagClippedValue
	p.clipMinInt = min
	p.clipMaxInt = max

	n := 0
	if iv, ok := p.value.(int); ok {
		n = iv
	}
	if n > max {
		n = max
	}
	if n < min {
		n = min
	}
	changed := false
	if iv, ok := p.value.(int); ok {
		changed = n != iv
	}
	if changed {
		p.value = n
	}
	var subs []*Subscription
	if changed {
		subs = make([]*Subscription, len(p.valueSubs))
		copy(subs, p.valueSubs)
	}
	p.mu.Unlock()
	if changed {
		// C: prop_notify_value(p, NULL, "prop_set_int_clipping_range()")
		for _, sub := range subs {
			notifySubscriber(sub, n)
		}
	}
}

// SetClippedFloat — C: prop_set_float_clipping_range (prop_core.c:3926).
// Same as SetClippedInt but coerces INT→FLOAT (prop_int_to_float).
func (p *Prop) SetClippedFloat(min, max float32) {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.propType == PropTypeZombie {
		p.mu.Unlock()
		return
	}
	if p.propType != PropTypeFloat {
		if p.propType == PropTypeInt {
			// C: prop_int_to_float — preserve value + range
			if iv, ok := p.value.(int); ok {
				p.value = float32(iv)
			} else {
				p.value = float32(0)
			}
			p.clipMinFloat = float32(p.clipMinInt)
			p.clipMaxFloat = float32(p.clipMaxInt)
			p.propType = PropTypeFloat
		} else if p.flags&FlagClippedValue != 0 || p.propType == PropTypeProxy ||
			p.propType == PropTypeDir || p.propType == PropTypeProp {
			p.mu.Unlock()
			return
		} else {
			p.value = float32(0)
			p.propType = PropTypeFloat
		}
	}
	p.flags |= FlagClippedValue
	p.clipMinFloat = min
	p.clipMaxFloat = max

	n := float32(0)
	if fv, ok := p.value.(float32); ok {
		n = fv
	}
	if n > max {
		n = max
	}
	if n < min {
		n = min
	}
	changed := false
	if fv, ok := p.value.(float32); ok {
		changed = n != fv
	}
	if changed {
		p.value = n
	}
	var subs []*Subscription
	if changed {
		subs = make([]*Subscription, len(p.valueSubs))
		copy(subs, p.valueSubs)
	}
	p.mu.Unlock()
	if changed {
		// C: prop_notify_value(p, NULL, "prop_set_float_clipping_range()")
		for _, sub := range subs {
			notifySubscriber(sub, n)
		}
	}
}

// SetProxySubID sets the proxy subscription ID
func (p *Prop) GetRawValue() any {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.value
}

// SetDebug sets/clears C's PROP_DEBUG_THIS flag (hp_flags).
// Used by prop_http's POST ?debug=on|off handler (prop_http.c:132-139).
func (p *Prop) GetString() string {
	p.mu.RLock()
	originator := p.originator
	propRef := p.propRef
	propType := p.propType
	val := p.value
	p.mu.RUnlock()

	if str, ok := val.(string); ok {
		return str
	}
	if uv, ok := val.(URIValue); ok {
		return uv.URL
	}
	// C: PROP_PROP follows the pointer to the referenced prop
	if propType == PropTypeProp && propRef != nil && propRef != p {
		return propRef.GetString()
	}
	// Follow originator chain (C: linked props delegate to source)
	if originator != nil && originator != p {
		return originator.GetString()
	}
	return ""
}

// GetInt returns the int value of a property
func (p *Prop) GetInt() int {
	p.mu.RLock()
	originator := p.originator
	propRef := p.propRef
	propType := p.propType
	val := p.value
	p.mu.RUnlock()

	if i, ok := val.(int); ok {
		return i
	}
	// C: PROP_PROP follows the pointer to the referenced prop
	if propType == PropTypeProp && propRef != nil && propRef != p {
		return propRef.GetInt()
	}
	if originator != nil && originator != p {
		return originator.GetInt()
	}
	return 0
}

// GetFloat returns the float value of a property
func (p *Prop) GetFloat() float32 {
	p.mu.RLock()
	originator := p.originator
	propRef := p.propRef
	propType := p.propType
	val := p.value
	p.mu.RUnlock()

	if f, ok := val.(float32); ok {
		return f
	}
	if f, ok := val.(float64); ok {
		return float32(f)
	}
	if i, ok := val.(int); ok {
		return float32(i)
	}
	// C: PROP_PROP follows the pointer to the referenced prop
	if propType == PropTypeProp && propRef != nil && propRef != p {
		return propRef.GetFloat()
	}
	if originator != nil && originator != p {
		return originator.GetFloat()
	}
	return 0.0
}

// SetValue sets the value of a property.
// C: prop_set_* family — full canonical path with notification.
// Go: Routes through setPropValue for destroyed check, equality check,
// propType update, notification, multi-parent chain, and target forwarding.
func (p *Prop) SetValue(value any) {
	setPropValue(p, nil, value)
}

// Release releases the property (decrements reference count)
func (pm *PropManager) SetEx(prop *Prop, opaque any, value any) {
	if prop == nil {
		return
	}
	setPropValue(prop, opaque, value)
}

// PropCreate0 creates a property with minimal parameters.
// If called with a single name argument, creates under global root.
// If called with parent, name, opaque, broadcast — creates as child of parent.
func (pm *PropManager) Create0(args ...any) *Prop {
	if len(args) == 0 {
		return pm.CreateEx(pm.GetGlobal(), "", nil, false, false)
	}
	if name, ok := args[0].(string); ok && len(args) == 1 {
		return pm.CreateEx(pm.GetGlobal(), name, nil, false, false)
	}
	// Handle (parent, name, opaque, broadcast) form for test compatibility
	if len(args) >= 2 {
		parent, _ := args[0].(*Prop)
		name, _ := args[1].(string)
		var opaque any
		if len(args) >= 3 {
			opaque = args[2]
		}
		broadcast := false
		if len(args) >= 4 {
			broadcast, _ = args[3].(bool)
		}
		if parent == nil {
			parent = pm.GetGlobal()
		}
		return pm.CreateEx(parent, name, opaque, broadcast, false)
	}
	return pm.CreateEx(pm.GetGlobal(), "", nil, false, false)
}

// FirstChild returns the first child of a property.
// C: prop_first_child (prop_core.c:5130) — returns prop_ref_inc(c),
// the caller owns the reference.
func (pm *PropManager) ToggleInt(p *Prop) {
	if p == nil {
		return
	}
	// C: if(p->hp_type == PROP_PROXY) { prop_proxy_toggle_int(p); return; }
	// (prop_core.c:4073-4078) — sends STPP CMD_SET + TOGGLE_INT on the wire.
	p.mu.RLock()
	isProxy := p.propType == PropTypeProxy
	pc := p.proxyConn
	p.mu.RUnlock()
	if isProxy {
		if pt, ok := pc.(ProxyToggler); ok && pt != nil {
			pt.ProxyToggleInt(p)
		}
		return
	}
	p.mu.Lock()
	if p.destroyed {
		p.mu.Unlock()
		return
	}
	cur := 0
	switch v := p.value.(type) {
	case int:
		cur = v
	case int64:
		cur = int(v)
	case float32:
		cur = int(v)
	case float64:
		cur = int(v)
	default:
		// C: prop_clean(p) for non-numeric types, then hp_int=0
		if !propClean(p) {
			p.value = 0
			p.propType = PropTypeInt
		}
	}
	p.mu.Unlock()
	pm.SetIntEx(p, nil, misc.BoolToInt(cur == 0))
}

// Select selects a property (PROP_SELECT_CHILD on parent).
// C: prop_select_ex(p, NULL, NULL) (prop_core.c:4945).
func (pm *PropManager) ValueFloat(value float64) any {
	return value
}

// ValueInt creates an int value property
func (pm *PropManager) ValueInt(value int64) any {
	return value
}

// SubNoInitialUpdate flag for subscriptions
func (p *Prop) SetVoid() {
	p.SetValue(nil)
}

// Append appends a child property
func (pm *PropManager) CopyEx(dst *Prop, skipme *Subscription, src *Prop) {
	if dst == nil {
		return
	}
	if src == nil {
		setPropValue(dst, skipme, nil)
		return
	}
	src.mu.RLock()
	t := src.propType
	v := src.value
	src.mu.RUnlock()
	switch t {
	case PropTypeInt, PropTypeFloat, PropTypeString, PropTypeURI:
		setPropValue(dst, skipme, v)
	default:
		setPropValue(dst, skipme, nil)
	}
}

// PropCopy — C: prop_copy (prop_core.c:4231 wrapper w/o skipme).
func (pm *PropManager) PropCopy(dst, src *Prop) {
	pm.CopyEx(dst, nil, src)
}

// Findv walks a NULL-terminated-style name vector, following hp_originator
// chains on the start prop and each found child.
// C: prop_findv (prop_core.c:5049) — returns a new reference.
func (pm *PropManager) seti(skipme *Subscription, p *Prop, ev EventType, args ...any) {
	switch ev {
	case EventSetCString, EventSetRString:
		// C: PROP_SET_STRING → str==NULL ? void : prop_set_string_exl
		var str string
		if len(args) > 0 {
			str, _ = args[0].(string)
		}
		setPropValue(p, skipme, str)
	case EventSetInt:
		if len(args) > 0 {
			if v, ok := args[0].(int); ok {
				setPropValue(p, skipme, v)
			}
		}
	case EventSetFloat:
		if len(args) > 0 {
			switch v := args[0].(type) {
			case float32:
				setPropValue(p, skipme, v)
			case float64:
				setPropValue(p, skipme, float32(v))
			}
		}
	case EventSetVoid:
		setPropValue(p, skipme, nil)
	case EventSetProp:
		if len(args) > 0 {
			if t, ok := args[0].(*Prop); ok {
				pm.SetPropExl(p, skipme, t)
			}
		}
	case EventAdoptRString:
		// C: PROP_ADOPT_RSTRING — prop_set_rstring_exl + rstr_release.
		// Go strings are immutable; identical to EventSetRString.
		var s2 string
		if len(args) > 0 {
			s2, _ = args[0].(string)
		}
		setPropValue(p, skipme, s2)
	case EventSetLink:
		if len(args) > 0 {
			if src, ok := args[0].(*Prop); ok {
				pm.Link(src, p, skipme, false, false)
			}
		}
	}
}

// Setdn sets a value on a leaf reached by a dotted path, creating
// intermediate children.
// C: prop_setdn (prop_core.c:5630) — splits on '.', prop_create0 for
// missing components, then prop_seti on the leaf.
func (pm *PropManager) Setdn(skipme *Subscription, p *Prop, str string, ev EventType, args ...any) {
	if p == nil {
		return
	}
	for {
		p.mu.RLock()
		if p.propType == PropTypeZombie {
			p.mu.RUnlock()
			return
		}
		p.mu.RUnlock()
		if str == "" {
			break
		}
		name := str
		rest := ""
		if i := strings.IndexByte(str, '.'); i >= 0 {
			name = str[:i]
			rest = str[i+1:]
		}
		var c *Prop
		p.mu.RLock()
		if p.propType == PropTypeDir {
			for _, ch := range p.children {
				ch.mu.RLock()
				cn := ch.name
				ch.mu.RUnlock()
				if cn == name {
					c = ch
					break
				}
			}
		}
		p.mu.RUnlock()
		if c == nil {
			c = pm.CreateEx(p, name, skipme, false, false)
		}
		p = c
		str = rest
	}
	pm.seti(skipme, p, ev, args...)
}

// SetTypedEx creates/finds a named child and sets a typed value on it.
// C: prop_set_ex (prop_core.c:5684) — prop_create0(p, name, NULL, noalloc)
// then prop_seti.
func (pm *PropManager) SetTypedEx(p *Prop, name string, noalloc bool, ev EventType, args ...any) {
	if p == nil {
		return
	}
	p.mu.RLock()
	zombie := p.propType == PropTypeZombie
	p.mu.RUnlock()
	if zombie {
		return
	}
	c := pm.CreateEx(p, name, nil, noalloc, false)
	pm.seti(nil, c, ev, args...)
}

// ==================== PROP_LINKSELECTED ====================

// linkselectedPriv holds private data for linkselected
