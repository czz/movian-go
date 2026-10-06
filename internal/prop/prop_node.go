package prop

// Split from prop.go — Fase 4 pure-move refactor.
// No symbol, lock, or event-order changes.

import (
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
)

type URIValue struct {
	Title string
	URL   string
}

// String type for properties
// StringType represents the string type for prop_set_string.
// C: prop_str_type_t — PROP_STR_UTF8, PROP_STR_RICH.
// The type only affects rendering (rich text vs plain text).
type tagEntry struct {
	key   any
	value any
}

// Prop represents a property node
type Prop struct {
	name       string
	value      any
	propType   PropertyType
	children   []*Prop
	childIndex map[string]*Prop
	parent     *Prop
	linkedTo   *Prop
	// originator is the source prop this prop is linked from (C: hp_originator).
	// Used for proper unlink and destroy handling.
	originator *Prop
	// targets is the list of props linked from this prop (C: hp_targets).
	// Used for event forwarding when this prop's value or children change.
	targets   []*Prop
	mu        sync.RWMutex
	refCount  int32 // C: hp_refcount — memory refcount (atomic). Protects memory.
	xref      uint8 // C: hp_xref — content refcount (mutex-protected). Protects content.
	tagKey    string
	tagValue  any
	tags      []tagEntry // C: hp_tags — linked list of tags (Go: slice)
	destroyed bool
	callback  func(opaque any, value any)
	opaque    any

	// Subscriber lists for event dispatch
	// C: hp_canonical_subscriptions — subs monitoring the canonical prop
	// (notified first during destruction with DESTROYED)
	canonicalSubs []*Subscription
	// C: hp_value_subscriptions — subs monitoring the value prop
	// (notified for value/child changes, and VOID during destruction)
	valueSubs []*Subscription
	// subs is the merged list kept for backward compatibility with existing
	// code that iterates all subscribers. It mirrors canonicalSubs ∪ valueSubs.
	subs []*Subscription

	// Proxy-specific fields
	proxyID   uint32
	proxyConn any // *ProxyConnection

	// Property flags
	flags int

	// selectedChild is the currently selected child (C: hp_selected)
	selectedChild *Prop

	// preLinkValue stores the target's value before a link was established.
	// C: the target's hp_rstring/hp_int/etc. are never changed during link;
	// subs are retargeted instead. Go copies the value, so we must save the
	// original to restore it during unlink (matching C's restore_and_descend).
	preLinkValue    any
	preLinkType     PropertyType
	preLinkChildren []*Prop

	// rstrType is the string type for RString props.
	// C: hp_rstrtype (prop_str_type_t) — PROP_STR_UTF8 (0) or PROP_STR_RICH (1).
	// Used by text rendering to distinguish plain text from rich text.
	// Passed as second arg in PROP_SET_RSTRING notifications.
	rstrType int

	// monitored indicates this prop has at least one SUBSCRIPTION_MONITOR
	// subscriber. C: hp_flags & PROP_MONITORED (prop_core.c:3287).
	// When set, any new non-monitor subscriber triggers
	// EventSubscriptionMonitorActive to all monitor subs.
	monitored bool

	// manager is the PropManager that created this prop. Used by JS bridge
	// helpers (DecRef) to call Destroy without an explicit pm reference.
	manager *PropManager

	// Clipping range for int/float values (C: PROP_CLIPPED_VALUE flag + u.i.min/max or u.f.min/max)
	// When FlagClippedValue is set, SetIntEx/SetFloatEx/AddFloat clamp values to [clipMin, clipMax].
	clipMinInt   int
	clipMaxInt   int
	clipMinFloat float32
	clipMaxFloat float32

	// propRef is the referenced prop when propType == PropTypeProp.
	// C: hp_prop — a live reference to another prop. When the referenced prop
	// changes value, this prop's subscribers are notified.
	propRef    *Prop
	propRefSub *Subscription
}

// Prop flag constants (C: prop_i.h:264-274)
func (pm *PropManager) DelChild(parent *Prop, child *Prop) {
	if parent == nil || child == nil {
		return
	}
	pm.destroyChild(parent, child)
}

// GetParent gets the parent property
func (pm *PropManager) GetParent(prop *Prop) *Prop {
	if prop == nil {
		return nil
	}
	prop.mu.RLock()
	defer prop.mu.RUnlock()
	return prop.parent
}

// GetParent returns the parent prop.
func (p *Prop) GetParent() *Prop {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.parent
}

// Destroy destroys a property.
//
// Reproduces C's prop_destroy0 (src/prop/prop_core.c:2290):
//  1. If already zombie (destroyed), return (idempotent)
//  2. Recursively destroy all children first (each child notifies its own
//     subscribers with PROP_DESTROYED)
//  3. Mark this prop as zombie (destroyed)
//  4. Notify this prop's subscribers with PROP_DESTROYED
//  5. Remove from parent's children list
//
// recursiveUnlink iterates prop's targets and unlinks each one.
// C: recursive_unlink(p) in prop_core.c:2261.
// For each target c in p->hp_targets:
//
//	recursive_unlink(c)   // recursively unlink c's own targets
//	prop_unlink0(c, ...)  // unlink c from p, retarget c's subs back to c
//
// Go: We use unlinkInternal which clears the link and notifies target's
// subs with EventSetProp. The target retains its copied value.
// This is called BEFORE children destruction and own notifications,
// matching C's ordering.
func (pm *PropManager) PropGetProp(p *Prop) *Prop {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if p != nil && p.propType == PropTypeProp {
		return pm.RefInc(p.propRef)
	}
	return pm.RefInc(p)
}

// RefInc increments the reference count (C: prop_ref_inc)
func (pm *PropManager) TagSet(prop *Prop, key any, value any) {
	if prop == nil {
		return
	}
	prop.mu.Lock()
	defer prop.mu.Unlock()
	// C: always prepend new tag (LIST_INSERT_HEAD), duplicates allowed
	prop.tags = append([]tagEntry{{key: key, value: value}}, prop.tags...)
}

// TagClear clears a tag from a property and returns the old value.
// C: prop_tag_clear (prop_tags.c:102) — removes matching node, returns value.
func (pm *PropManager) TagClear(prop *Prop, key any) any {
	if prop == nil {
		return nil
	}
	prop.mu.Lock()
	defer prop.mu.Unlock()
	var oldValue any
	for i, tag := range prop.tags {
		if tag.key == key {
			oldValue = tag.value
			prop.tags = slices.Delete(prop.tags, i, i+1)
			break
		}
	}
	return oldValue
}

// GetName returns the name of a property
func (pm *PropManager) GetName(prop *Prop) string {
	if prop == nil {
		return ""
	}
	return prop.GetName()
}

// Find finds a child property by name
func (pm *PropManager) Find(parent *Prop, name string) *Prop {
	if parent == nil {
		return nil
	}
	return parent.GetChild(name)
}

// TagGet retrieves a tag value from a property.
// C: prop_tag_get (prop_tags.c:41) — walks hp_tags list, returns matching value.
func (pm *PropManager) TagGet(prop *Prop, key any) any {
	if prop == nil {
		return nil
	}
	prop.mu.RLock()
	defer prop.mu.RUnlock()
	for _, tag := range prop.tags {
		if tag.key == key {
			return tag.value
		}
	}
	return nil
}

// SetCallback sets a callback for property changes
func (p *Prop) GetProxyID() uint32 {
	return p.proxyID
}

// SetProxyID sets the proxy ID
func (p *Prop) SetProxyID(id uint32) {
	p.proxyID = id
}

// SetProxyConnection sets the proxy connection
func (p *Prop) SetProxyConnection(conn any) {
	p.proxyConn = conn
}

// GetProxyConnection returns the proxy connection (C: hp_proxy_ppc).
func (p *Prop) GetProxyConnection() any {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.proxyConn
}

// SetSelectedChild sets the dir's selected child without notifying.
// C: hp_selected assignment for proxy dirs — the STPP wire decoder sets
// this when the remote sends SELECT_CHILD / ADD_CHILD_SELECTED so that
// local receivers computing PROP_ADD_SELECTED flags see consistent state.
func (p *Prop) SetPropType(pt PropertyType) {
	p.propType = pt
}

// IsVoid returns true if the prop has no value set (PropTypeVoid).
// C: prop->type == PROP_VOID
func (p *Prop) IsVoid() bool {
	p.mu.RLock()
	pt := p.propType
	p.mu.RUnlock()
	return pt == PropTypeVoid
}

// GetPropType returns the property type.
func (p *Prop) GetPropType() PropertyType {
	p.mu.RLock()
	pt := p.propType
	p.mu.RUnlock()
	return pt
}

// SetClippedInt sets the PROP_CLIPPED_VALUE flag with an int clipping range.
// C: prop_set_clipped_int(p, min, max) — sets flag + range, then clips current value.
// SetClippedInt — C: prop_set_int_clipping_range (prop_core.c:4106).
// Sets PROP_CLIPPED_VALUE + range, coerces FLOAT→INT (prop_float_to_int),
// cleans other non-INT types, then clips the current value and notifies
// if it changed.
func (p *Prop) GetChild(name string) *Prop {
	p.mu.RLock()
	originator := p.originator
	for _, child := range p.children {
		if child.name == name {
			p.mu.RUnlock()
			return child
		}
	}
	p.mu.RUnlock()
	// Follow originator chain (C: linked props delegate to source)
	if originator != nil && originator != p {
		return originator.GetChild(name)
	}
	return nil
}

// AddChild adds a child property
// AddChild adds a child property to this prop.
// C: prop_insert(hp, parent, NULL, skipme) — adds child to parent's
// children list, fires PROP_ADD_CHILD to parent's value subscribers.
// Go: Also updates childIndex, inherits MULTI_NOTIFY flag, and fires
// EventAddChild to parent's value subscribers.
func (p *Prop) AddChild(child *Prop) {
	p.mu.Lock()

	// Check if child already exists by name (matching CreateEx behavior)
	if child.name != "" && p.childIndex != nil {
		if existing, ok := p.childIndex[child.name]; ok {
			p.mu.Unlock()
			_ = existing
			return
		}
	}

	// C: prop_make_dir(parent) — transition parent to DIR if needed
	if p.propType != PropTypeDir {
		p.propType = PropTypeDir
	}

	child.parent = p
	if child.manager == nil {
		child.manager = p.manager
	}
	p.children = append(p.children, child)
	if child.name != "" {
		if p.childIndex == nil {
			p.childIndex = make(map[string]*Prop)
		}
		p.childIndex[child.name] = child
	}

	// C: MULTI_NOTIFY flag inheritance (prop_core.c:1969-1971)
	if p.flags&(FlagMultiSub|FlagMultiNotify) != 0 {
		child.flags |= FlagMultiNotify
	}

	// Snapshot parent's value subs for EventAddChild notification
	parentSubs := make([]*Subscription, len(p.valueSubs))
	copy(parentSubs, p.valueSubs)

	p.mu.Unlock()

	// C: prop_notify_child(parent, PROP_ADD_CHILD, child)
	// Fire outside lock to avoid deadlock in direct dispatch mode
	for _, sub := range parentSubs {
		notifySub(sub, EventAddChild, child, p)
	}
}

// FindChild finds a child property by name (recursive)
// C: prop_find0 (prop_core.c:5085-5108) — iterates direct children only,
// matching hp_name; does NOT recurse. Multi-level lookup is done via
// successive name arguments (prop_find(p, "a", "b", NULL)).
func (p *Prop) FindChild(name string) *Prop {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// Follow link to target if linked (prop_link semantics: source mirrors target)
	if p.linkedTo != nil {
		return p.linkedTo.FindChild(name)
	}

	for _, child := range p.children {
		if child.name == name {
			return child
		}
	}
	return nil
}

// ResolvePath traverses children by name from this prop, matching C's
// prop_subfind semantics (prop_core.c:2682-2740).
//
// C behavior:
//   - For each name component, follow originator links (if followLinks=true)
//   - If prop is not PROP_DIR, convert VOID to DIR (create empty dir)
//   - If prop is not VOID, return NULL (don't overwrite real values)
//   - Find child by name in current children
//   - If child not found, create it (prop_create0)
//   - Return the resolved prop (or nil if path is invalid)
//
// Go: We do NOT auto-create children (Go STPP should not mutate the prop
// tree during subscription). If a child doesn't exist, return nil.
// This matches the observable behavior when the prop tree is already
// populated (the normal case for STPP subscriptions).
//
// If followLinks is true, follows linkedTo (originator) at each step,
// matching C's prop_subfind with follow_symlinks=1.
func (p *Prop) ResolvePath(components []string, followLinks bool) *Prop {
	if p == nil || len(components) == 0 {
		return p
	}

	current := p
	for _, name := range components {
		if name == "" {
			continue
		}

		// Follow originator links (C: while(follow_symlinks && p->hp_originator != NULL))
		if followLinks {
			for {
				current.mu.RLock()
				linked := current.linkedTo
				current.mu.RUnlock()
				if linked == nil {
					break
				}
				current = linked
			}
		}

		// C: prop_subfind allow_indexing — "*N" selects the Nth child
		// (prop_core.c:2711-2724), used by /api/prop links for
		// unnamed children.
		var child *Prop
		if len(name) > 1 && name[0] == '*' {
			idx, err := strconv.Atoi(name[1:])
			if err == nil && idx >= 0 {
				children := current.GetChildren()
				if idx < len(children) {
					child = children[idx]
				}
			}
		} else {
			// Find child by name (C: TAILQ_FOREACH matching hp_name)
			child = current.GetChild(name)
		}
		if child == nil {
			// C: prop_create0(p, name[0], NULL, 0) — creates child if not found
			// Go: Return nil — STPP subscription to non-existent path fails
			// This is a conservative choice; C auto-creates which has side effects
			// (prop becomes DIR, notification fired). If we need to match C's
			// auto-creation behavior, we would call pm.CreateEx here.
			return nil
		}
		current = child
	}

	// Follow final originator link (C: line 2734-2738)
	if followLinks {
		for {
			current.mu.RLock()
			linked := current.linkedTo
			current.mu.RUnlock()
			if linked == nil {
				break
			}
			current = linked
		}
	}

	return current
}

// GetProp returns a child property by name (for DOT expression support)
func (p *Prop) GetProp(name string) (any, bool) {
	child := p.FindChild(name)
	if child != nil {
		return child, true
	}
	return nil, false
}

// GetChildren returns all children
func (p *Prop) GetChildren() []*Prop {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// Follow link to target if linked
	if p.linkedTo != nil {
		return p.linkedTo.GetChildren()
	}

	result := make([]*Prop, len(p.children))
	copy(result, p.children)
	return result
}

// SetChildren reorders the children slice of this prop.
// This is used by PropNF to sort children after insertion.
// The children set must be the same set of props already children of p,
// just in a different order. No new props are added, none removed.
func (p *Prop) SetChildren(children []*Prop) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.children = children
	// Rebuild child index
	p.childIndex = make(map[string]*Prop, len(children))
	for _, c := range children {
		if c != nil && c.name != "" {
			p.childIndex[c.name] = c
		}
	}
}

// RefDec decrements the reference count
func (p *Prop) SetDebug(on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if on {
		p.flags |= PropFlagDebugThis
	} else {
		p.flags &^= PropFlagDebugThis
	}
}

// GetRstrType returns the string type — C: hp_rstrtype (PROP_STR_UTF8/
// PROP_STR_RICH).
func (p *Prop) GetRstrType() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.rstrType
}

// GetString returns the string value of a property
func (pm *PropManager) FirstChild(prop *Prop) *Prop {
	if prop == nil {
		return nil
	}
	prop.mu.RLock()
	defer prop.mu.RUnlock()
	if len(prop.children) > 0 {
		return pm.RefInc(prop.children[0])
	}
	return nil
}

// SelectChild selects a child property by name.
// C: prop_select_ex(p, extra, skipme)
//  1. If p is zombie, return
//  2. Get parent
//  3. Fire PROP_SELECT_CHILD to parent's subscribers
//  4. Set parent->hp_selected = p
//
// Go: Finds the child by name, fires EventSelectChild to parent's
// subscribers, and sets the parent's selected child.
// SelectChild — C: prop_select_by_value (prop_core.c:4996).
// Finds the named child, then notifies PROP_SELECT_CHILD and sets
// hp_selected — even when the child does not exist (deselection).
func (p *Prop) Append(child *Prop) {
	p.AddChild(child)
}

// Len returns the number of children
func (p *Prop) Len() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.linkedTo != nil {
		return p.linkedTo.Len()
	}
	return len(p.children)
}

// Get returns a child by index
func (p *Prop) Get(index int) *Prop {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.linkedTo != nil {
		return p.linkedTo.Get(index)
	}
	if index < 0 || index >= len(p.children) {
		return nil
	}
	return p.children[index]
}

// GetName returns the property name
// Manager returns the PropManager that owns this prop.
// C has a single global prop system; in Go the manager is per-instance.
func (p *Prop) Manager() *PropManager {
	if p == nil {
		return nil
	}
	return p.manager
}
func (p *Prop) GetName() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.name
}

// GetOriginator returns the source prop this prop is linked from (C: hp_originator).
// Used by PropNF to translate target requests back to the source.
func (p *Prop) GetOriginator() *Prop {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.originator
}

// FireEvent fires a custom event to all subscribers of this prop.
// Used by PropNF dstsub to forward requests (EventReqDeleteVector,
// EventReqMoveChild, EventWantMoreChilds) from target to source.
func (p *Prop) GetType() PropertyType {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.linkedTo != nil {
		return p.linkedTo.GetType()
	}
	return p.propType
}

// Link links this property to another property (target mirrors p)
// Link links this property to a target property.
// C: prop_link0 (prop_core.c:4714) — retargets target's subscriptions to source.
// Go: Delegates to PropManager.Link for full C-canonical semantics.
func (p *Prop) IsDestroyed() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.destroyed
}

// IsMonitored returns whether the prop has the PROP_MONITORED flag set.
// C: p->hp_flags & PROP_MONITORED
// Go: Uses the dedicated 'monitored' field (set when monitor subs exist).
func (p *Prop) IsMonitored() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.monitored
}

// SubCount returns the total number of subscribers (value + canonical).
// Used for testing and diagnostics.
func (p *Prop) SubCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	// C: a single prop_subscribe adds the subscription to both
	// hps_value_subscriptions and hps_canonical_subscriptions.
	// Count unique active subscriptions, not the sum of both lists.
	seen := make(map[*Subscription]bool)
	count := 0
	for _, s := range p.valueSubs {
		if s.active.Load() && !seen[s] {
			seen[s] = true
			count++
		}
	}
	for _, s := range p.canonicalSubs {
		if s.active.Load() && !seen[s] {
			seen[s] = true
			count++
		}
	}
	return count
}

// GetRefCount returns the current reference count (for testing/diagnostics).
func (p *Prop) GetRefCount() int32 {
	return atomic.LoadInt32(&p.refCount)
}

// Unparent removes prop from its parent without destroying it.
// C: prop_unparent (prop_core.c) → prop_unparent0 — DEL_CHILD to all subs,
// remove from parent's children, clear parent's selection.
