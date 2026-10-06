package prop

// Split from prop.go — Fase 4 pure-move refactor.
// No symbol, lock, or event-order changes.

import (
	"github.com/czz/movian-go/internal/gconf"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
)

type PropManager struct {
	globalProp  *Prop
	mu          sync.RWMutex
	logCallback func(format string, args ...any)

	// store — C: the global htsmsg_store reached by prop_reorder
	// persistence. `any` because htsmsg imports this package's
	// consumers (cycle); concrete *htsmsg.Store set at init.
	store any

	// reorder persistence — C: prop_reorder.c htsmsg_store_save/load.
	// Same htsmsg cycle seam as store; wired via SetReorderBridge.
	reorderSave func(storeDep any, id string, order []string)
	reorderLoad func(storeDep any, id string) []string

	// gconf — C: gconf_t fields prop_nodefilter reads
	// (ignore_the_prefix for sorting). Injected via SetGconf.
	gconf *gconf.T
}

// SetGconf injects the process gconf (C: gconf_t — owned by main).
func (pm *PropManager) SetGconf(g *gconf.T) { pm.gconf = g }

// IgnoreThePrefix — C: gconf.ignore_the_prefix read by sorting code.
func (pm *PropManager) IgnoreThePrefix() int {
	if pm == nil || pm.gconf == nil {
		return 0
	}
	return pm.gconf.IgnoreThePrefix
}

// NewPropManager creates a new property manager
// SetReorderBridge wires prop_reorder persistence
// (C: htsmsg_store_save/load — prop_reorder.c). Provider: pkg/htsmsg
// (import-cycle seam).
func (pm *PropManager) SetReorderBridge(
	save func(storeDep any, id string, order []string),
	load func(storeDep any, id string) []string) {
	pm.reorderSave = save
	pm.reorderLoad = load
}
func NewPropManager() *PropManager {
	pm := &PropManager{
		globalProp: &Prop{
			name:     "global",
			children: make([]*Prop, 0),
			refCount: 1, // C: prop_make sets hp_refcount = 1
			xref:     1, // C: prop_make sets hp_xref = 1
		},
	}
	pm.globalProp.manager = pm
	return pm
}

// StartPropertySystem initializes the property system
func (pm *PropManager) StartPropertySystem() {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.globalProp == nil {
		pm.globalProp = &Prop{
			name:     "global",
			children: make([]*Prop, 0),
			refCount: 1,
			xref:     1,
			manager:  pm,
		}
	}
}

// SetStore injects the htsmsg store (C: global htsmsg_store —
// prop_reorder persistence). `any` across the htsmsg cycle seam.
func (pm *PropManager) SetStore(st any) { pm.store = st }

// Store returns the injected store (nil until wired).
func (pm *PropManager) Store() any { return pm.store }

// StartPropertySystemLate initializes late property system features
func (pm *PropManager) StartPropertySystemLate() {
	// Late initialization if needed
}

// Start initializes the property system (alias for StartPropertySystem)
func (pm *PropManager) Start() {
	pm.StartPropertySystem()
}

// GetGlobal returns the global property
func (pm *PropManager) GetGlobal() *Prop {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.globalProp
}

// CreateEx creates a new property. If parent already has a child with the
// same name, the existing child is returned instead of creating a duplicate.
func (pm *PropManager) CreateEx(parent *Prop, name string, opaque any, canBeAnonymous bool, fromSubscriptions bool) *Prop {
	pm.mu.Lock()

	if parent != nil {
		parent.mu.Lock()

		// C: if(parent->hp_type == PROP_ZOMBIE) return NULL (prop_core.c:1940-1975)
		// Don't create children on a destroyed parent.
		if parent.destroyed {
			parent.mu.Unlock()
			pm.mu.Unlock()
			return nil
		}

		// C: if(parent->hp_type == PROP_PROXY) return prop_proxy_create(parent, name)
		// Proxy parents delegate creation to the proxy target.
		if parent.propType == PropTypeProxy {
			parent.mu.Unlock()
			pm.mu.Unlock()
			// Go: if the proxy has a ProxyCreator, delegate; otherwise return nil.
			if parent.proxyConn != nil {
				if creator, ok := parent.proxyConn.(ProxyCreator); ok {
					return creator.CreateChild(parent, name, opaque, canBeAnonymous, fromSubscriptions)
				}
			}
			return nil
		}

		// Check for existing child with same name via index map.
		// C's prop_create0 skips this check when name == NULL (always creates
		// a new prop). In Go, empty string "" is the equivalent of NULL.
		// Without this guard, all CreateEx(parent, "", ...) calls return the
		// same child, which breaks settings nodes and any other code that
		// creates multiple unnamed children (matching C's prop_create(parent, NULL)).
		if name != "" && parent.childIndex != nil {
			if child, ok := parent.childIndex[name]; ok {
				parent.mu.Unlock()
				pm.mu.Unlock()
				return child
			}
		}
	}

	prop := &Prop{
		name:       name,
		children:   make([]*Prop, 0),
		childIndex: make(map[string]*Prop),
		parent:     parent,
		opaque:     opaque,
		refCount:   1, // C: prop_make sets hp_refcount = 1
		xref:       1, // C: prop_make sets hp_xref = 1
		manager:    pm,
	}

	if parent != nil {
		// C: prop_make_dir(parent) — transition parent to DIR if needed
		if parent.propType != PropTypeDir {
			parent.propType = PropTypeDir
		}

		parent.children = append(parent.children, prop)
		if parent.childIndex == nil {
			parent.childIndex = make(map[string]*Prop)
		}
		parent.childIndex[name] = prop

		// C: prop_create0 (prop_core.c:1970-1971):
		//   if(parent->hp_flags & (PROP_MULTI_SUB | PROP_MULTI_NOTIFY))
		//     prop_flood_flag(hp, PROP_MULTI_NOTIFY, 0);
		// New children under a MULTI_SUB or MULTI_NOTIFY parent inherit MULTI_NOTIFY.
		if parent.flags&(FlagMultiSub|FlagMultiNotify) != 0 {
			prop.flags |= FlagMultiNotify
		}

		// Collect parent's value subscribers for EventAddChild notification
		// C: prop_notify_child iterates parent->hp_value_subscriptions
		parentSubs := make([]*Subscription, len(parent.valueSubs))
		copy(parentSubs, parent.valueSubs)

		// Collect targets that need mirror children
		targetsToMirror := make([]*Prop, 0, len(parent.targets))
		for _, target := range parent.targets {
			if target != nil && !target.destroyed {
				targetsToMirror = append(targetsToMirror, target)
			}
		}

		parent.mu.Unlock()
		pm.mu.Unlock()

		// Notify parent's subscribers with EventAddChild.
		// C: prop_create0 → prop_notify_child(parent, PROP_ADD_CHILD, child)
		// Note: pm.mu and parent.mu are released before notification to
		// prevent re-entrant deadlock when subscribers call CreateEx.
		for _, sub := range parentSubs {
			notifySub(sub, EventAddChild, prop, parent)
		}

		// C-canonical: No mirror children creation in linked targets.
		// In C, after prop_link0, subscriptions are retargeted to source.
		// When a child is added to source, prop_notify_child notifies all subs
		// on source, which includes retargeted subs from target.
		// GetChildren on target follows originator to source, so target
		// appears to have source's children without explicit mirror creation.
	} else {
		pm.mu.Unlock()
	}

	return prop
}

// CreateRootEx creates a root property
func (pm *PropManager) CreateRootEx(name string, canBeAnonymous bool) *Prop {
	return pm.CreateEx(nil, name, nil, canBeAnonymous, false)
}

// CreateMulti creates a multi-value property
// CreateMulti creates a prop with MULTI_SUB flag.
// C: prop_create_multi (prop_core.c:2017) — variadic path creation + extra ref.
// Go: Single-name creation + SetMulti. All Go callers pass a single name.
// The variadic path feature is available via CreateMultiPath.
// CreateMulti creates a child prop. C: prop_create_multi — does NOT set MULTI_SUB.
// C: prop_create_multi just calls prop_create0 for the name and prop_ref_inc on the result.
func (pm *PropManager) CreateMulti(parent *Prop, name string) *Prop {
	p := pm.CreateEx(parent, name, nil, false, false)
	if p != nil {
		// C: prop_ref_inc — extra ref returned to caller
		atomic.AddInt32(&p.refCount, 1)
	}
	return p
}

// CreateMultiPath creates a chain of props. C: prop_create_multi — consumes
// NULL-terminated variadic name list, calling prop_create0 for each,
// then prop_ref_inc on the leaf. Does NOT set MULTI_SUB.
func (pm *PropManager) CreateMultiPath(parent *Prop, names ...string) *Prop {
	if parent == nil || len(names) == 0 {
		return nil
	}
	current := parent
	for _, name := range names {
		current = pm.CreateEx(current, name, nil, false, false)
		if current == nil {
			return nil
		}
	}
	if current != nil {
		// C: prop_ref_inc — extra ref returned to caller
		atomic.AddInt32(&current.refCount, 1)
	}
	return current
}

// CreateAfter creates a child prop positioned after 'after' in the parent's children list.
// C: prop_create_after (prop_core.c:2047) — if child exists, moves it after 'after';
// if not, creates it and inserts after 'after' (or at head if after==NULL).
func (pm *PropManager) CreateAfter(parent *Prop, name string, after *Prop, opaque any) *Prop {
	if parent == nil || name == "" {
		return nil
	}
	parent.mu.RLock()
	if parent.destroyed {
		parent.mu.RUnlock()
		return nil
	}
	parent.mu.RUnlock()

	// C: prop_make_dir(parent)
	pm.MakeDir(parent)

	// Check if child already exists
	parent.mu.Lock()
	// Re-check under the write lock: destroy may have interleaved
	// between the read-check above and here (C: atomic under prop_mutex).
	if parent.destroyed {
		parent.mu.Unlock()
		return nil
	}
	var existing *Prop
	if parent.childIndex != nil {
		existing = parent.childIndex[name]
	}

	if existing != nil {
		// C: if prev != after, move existing after 'after'
		// Find current position and 'after' position
		existingIdx := -1
		afterIdx := -1
		for i, c := range parent.children {
			if c == existing {
				existingIdx = i
			}
			if c == after {
				afterIdx = i
			}
		}
		// Check if already in the right position
		needsMove := false
		if after == nil {
			// Should be at head (index 0)
			if existingIdx != 0 {
				needsMove = true
			}
		} else {
			// Should be right after 'after'
			if existingIdx != afterIdx+1 {
				needsMove = true
			}
		}
		if needsMove {
			// Remove from current position
			parent.children = slices.Delete(parent.children, existingIdx, existingIdx+1)
			// Insert at new position
			var nextSibling *Prop
			if after == nil {
				// Insert at head
				if len(parent.children) > 0 {
					nextSibling = parent.children[0]
				}
				parent.children = slices.Insert(parent.children, 0, existing)
			} else {
				// Find 'after' again (index may have shifted)
				newAfterIdx := -1
				for i, c := range parent.children {
					if c == after {
						newAfterIdx = i
						break
					}
				}
				if newAfterIdx >= 0 {
					// nextSibling is the child currently after 'after'
					if newAfterIdx+1 < len(parent.children) {
						nextSibling = parent.children[newAfterIdx+1]
					}
					parent.children = slices.Insert(parent.children, newAfterIdx+1, existing)
				} else {
					parent.children = append(parent.children, existing)
				}
			}
			subs := make([]*Subscription, len(parent.valueSubs))
			copy(subs, parent.valueSubs)
			parent.mu.Unlock()
			// C: prop_notify_child2(p, parent, next, PROP_MOVE_CHILD, skipme, 0)
			for _, sub := range subs {
				notifySub(sub, EventMoveChild, existing, parent, nextSibling)
			}
		} else {
			parent.mu.Unlock()
		}
		return existing
	}

	// Create new child
	child := &Prop{
		name:       name,
		children:   make([]*Prop, 0),
		childIndex: make(map[string]*Prop),
		parent:     parent,
		propType:   PropTypeVoid,
		refCount:   1,
		xref:       1,
		manager:    pm,
	}

	// C: TAILQ_INSERT_AFTER or TAILQ_INSERT_HEAD
	if after == nil {
		parent.children = slices.Insert(parent.children, 0, child)
	} else {
		afterIdx := -1
		for i, c := range parent.children {
			if c == after {
				afterIdx = i
				break
			}
		}
		if afterIdx >= 0 {
			parent.children = slices.Insert(parent.children, afterIdx+1, child)
		} else {
			parent.children = append(parent.children, child)
		}
	}
	if parent.childIndex == nil {
		parent.childIndex = make(map[string]*Prop)
	}
	parent.childIndex[name] = child
	subs := make([]*Subscription, len(parent.valueSubs))
	copy(subs, parent.valueSubs)
	parent.mu.Unlock()

	// C: prop_notify_child (PROP_ADD_CHILD) or prop_notify_child2 (PROP_ADD_CHILD_BEFORE)
	// C: if next == NULL (inserted at tail): PROP_ADD_CHILD
	// C: if next != NULL (inserted before a sibling): PROP_ADD_CHILD_BEFORE
	// Determine the next sibling (the child that comes after the newly inserted one)
	var nextSibling *Prop
	// Find the index of the newly inserted child
	childIdx := -1
	for i, c := range parent.children {
		if c == child {
			childIdx = i
			break
		}
	}
	if childIdx >= 0 && childIdx+1 < len(parent.children) {
		nextSibling = parent.children[childIdx+1]
	}
	for _, sub := range subs {
		if nextSibling != nil {
			notifySub(sub, EventAddChildBefore, child, parent, nextSibling)
		} else {
			notifySub(sub, EventAddChild, child, parent)
		}
	}
	return child
}

// to all children. This enables multi-parent notification chain.
// C: prop_set_multi (prop_core.c:2574)
func (pm *PropManager) Create(name string) *Prop {
	return pm.CreateEx(pm.globalProp, name, nil, false, false)
}

// SetStringEx sets a string property value
// C: prop_set_string_ex (prop_core.c:3583) — sets hp_rstring and hp_rstrtype.
func propClean(p *Prop) bool {
	if p.flags&FlagClippedValue != 0 {
		return true
	}
	switch p.propType {
	case PropTypeDir:
		// Check for canonical subs on any descendant
		if hasCanonicalSubsDescendingLocked(p) {
			return true
		}
		// C: prop_destroy_childs0(p):
		//   TAILQ_MOVE(&childs, &p->hp_childs, ...);
		//   TAILQ_INIT(&p->hp_childs);
		//   p->hp_type = PROP_VOID;
		//   p->hp_selected = NULL;
		//   prop_notify_value(p, NULL, "prop_destroy_childs0()");
		//   for each child: EARLY_DEL_CHILD notifs, hp_parent=NULL, prop_destroy0(c)
		children := make([]*Prop, len(p.children))
		copy(children, p.children)
		p.children = nil
		p.childIndex = nil
		p.selectedChild = nil
		p.propType = PropTypeVoid // C: p->hp_type = PROP_VOID
		p.value = nil
		return false
	case PropTypeProxy:
		return true
	case PropTypeProp:
		// C: prop_ref_dec_locked(p->hp_prop)
		// C-canonical: no propRefSub (no live forwarding in C)
		// Just decrement the referenced prop's refcount
		if p.propRefSub != nil {
			p.propRefSub.active.Store(false)
			p.propRefSub = nil
		}
		if p.propRef != nil {
			atomic.AddInt32(&p.propRef.refCount, -1)
			p.propRef = nil
		}
		return false
	case PropTypeString, PropTypeURI:
		// C: rstr_release(p->hp_rstring) / rstr_release(p->hp_uri_title); rstr_release(p->hp_uri)
		// Go: clear value so GC can collect (no explicit rstr_release needed)
		p.value = nil
		return false
	default:
		// PropTypeInt, PropTypeFloat, PropTypeVoid — no cleanup needed
		return false
	}
}

// hasCanonicalSubsDescendingLocked checks if any descendant has a canonical
// subscriber. CAUTION: caller must hold p.mu.Lock() — does NOT re-lock p.
func hasCanonicalSubsDescendingLocked(p *Prop) bool {
	if p.propType != PropTypeDir {
		return false
	}
	// p.mu is already held — copy children list
	children := make([]*Prop, len(p.children))
	copy(children, p.children)
	for _, child := range children {
		child.mu.RLock()
		hasCanonical := len(child.canonicalSubs) > 0
		child.mu.RUnlock()
		if hasCanonical {
			return true
		}
		if hasCanonicalSubsDescendingLocked(child) {
			return true
		}
	}
	return false
}

// clipInt clamps an int value to the prop's clipping range (C: PROP_CLIPPED_VALUE).
func (pm *PropManager) MakeDir(prop *Prop) int {
	if prop == nil {
		return -1
	}
	prop.mu.Lock()
	// C: if(p->hp_type == PROP_DIR) return;
	if prop.propType == PropTypeDir {
		prop.mu.Unlock()
		return 0
	}
	// C: prop_clean(p) — release existing value payload and destroy children.
	// A non-DIR prop should not have children, but clean them up for safety.
	prop.value = nil
	childrenToDestroy := make([]*Prop, len(prop.children))
	copy(childrenToDestroy, prop.children)
	prop.children = make([]*Prop, 0)
	prop.childIndex = make(map[string]*Prop)
	prop.selectedChild = nil
	// C: p->hp_type = PROP_DIR;
	prop.propType = PropTypeDir
	// Snapshot value subs for notification (outside lock to avoid re-entrant deadlock)
	subs := make([]*Subscription, len(prop.valueSubs))
	copy(subs, prop.valueSubs)
	prop.mu.Unlock()

	// Destroy any children that existed on the non-DIR prop (matching C's prop_clean)
	for _, child := range childrenToDestroy {
		pm.Destroy(child)
	}

	// C: prop_notify_value(p, skipme, origin) → PROP_SET_DIR to all value subs
	for _, sub := range subs {
		notifySub(sub, EventSetDir, prop, nil)
	}
	return 0
}

// SetURIEx sets a URI property value with separate title and URL.
// C: prop_set_uri_ex(p, skipme, title, url) — stores hp_uri_title and hp_uri
// as separate rstrings, sets hp_type = PROP_URI, calls prop_set_epilogue
// which calls prop_notify_value(p, skipme, origin).
// Go: stores URIValue{title, url} in prop.value, sets PropTypeURI,
// then goes through setPropValue for notification (matching C's epilogue).
// If both title and url are NULL, sets void (matching C line 3734-3736).
type SetParentOpaque struct {
	Before *Prop
	Skipme *Subscription
}

func (pm *PropManager) SetParentEx(prop *Prop, newParent *Prop, opaque any, name string) int {
	if prop == nil {
		return -1
	}

	// Go extension: nil parent means unparent (C uses prop_unparent_ex for this).
	// C: prop_set_parent_ex returns -1 for NULL parent, but Go callers use nil for unparent.
	if newParent == nil {
		// C: prop_unparent0 — DEL_CHILD to ALL subs (no skipme)
		prop.mu.RLock()
		oldParent := prop.parent
		prop.mu.RUnlock()
		if oldParent != nil {
			// C order (prop_core.c:2202-2207): prop_notify_child
			// (PROP_DEL_CHILD) fires BEFORE TAILQ_REMOVE — direct
			// callbacks still see the child in hp_childs.
			oldParent.mu.RLock()
			oldSubs := make([]*Subscription, len(oldParent.valueSubs))
			copy(oldSubs, oldParent.valueSubs)
			oldParent.mu.RUnlock()
			// C: prop_notify_child(p, parent, PROP_DEL_CHILD, NULL, 0)
			// C: skipme is NOT skipped for DEL_CHILD (prop_unparent0 passes NULL)
			for _, sub := range oldSubs {
				notifySub(sub, EventDelChild, prop, oldParent)
			}
			oldParent.mu.Lock()
			for i, child := range oldParent.children {
				if child == prop {
					oldParent.children = slices.Delete(oldParent.children, i, i+1)
					break
				}
			}
			if prop.name != "" && oldParent.childIndex != nil {
				delete(oldParent.childIndex, prop.name)
			}
			if oldParent.selectedChild == prop {
				oldParent.selectedChild = nil
			}
			oldParent.mu.Unlock()
			prop.mu.Lock()
			prop.parent = nil
			prop.mu.Unlock()
		}
		return 0
	}

	// C: if(parent->hp_type == PROP_ZOMBIE) return -1;
	if newParent.IsDestroyed() {
		return -1
	}

	// Extract 'before' and 'skipme' from opaque
	var before *Prop
	var skipme *Subscription
	if opaque != nil {
		if b, ok := opaque.(*Prop); ok {
			before = b
		} else if s, ok := opaque.(*Subscription); ok {
			skipme = s
		} else if so, ok := opaque.(*SetParentOpaque); ok {
			// C: prop_set_parent0(p, parent, before, skipme)
			before = so.Before
			skipme = so.Skipme
		}
	}

	// C: prop_make_dir(parent, skipme, "prop_set_parent()")
	// Convert parent to DIR if needed (destroys existing value/children via prop_clean)
	pm.MakeDir(newParent)

	prop.mu.Lock()
	oldParent := prop.parent
	prop.mu.Unlock()

	if oldParent != newParent {
		// C: prop_unparent0(p, skipme) — fire DEL_CHILD, remove from old parent, clear selected
		if oldParent != nil {
			// C order: prop_notify_child(PROP_DEL_CHILD) BEFORE
			// TAILQ_REMOVE (prop_core.c:2202-2207).
			oldParent.mu.RLock()
			oldSubs := make([]*Subscription, len(oldParent.valueSubs))
			copy(oldSubs, oldParent.valueSubs)
			oldParent.mu.RUnlock()
			// C: prop_notify_child(p, parent, PROP_DEL_CHILD, NULL, 0)
			// C: skipme is NOT skipped for DEL_CHILD (prop_unparent0 passes NULL)
			for _, sub := range oldSubs {
				notifySub(sub, EventDelChild, prop, oldParent)
			}
			oldParent.mu.Lock()
			for i, child := range oldParent.children {
				if child == prop {
					oldParent.children = slices.Delete(oldParent.children, i, i+1)
					break
				}
			}
			if prop.name != "" && oldParent.childIndex != nil {
				delete(oldParent.childIndex, prop.name)
			}
			// C: if(parent->hp_selected == p) parent->hp_selected = NULL;
			if oldParent.selectedChild == prop {
				oldParent.selectedChild = nil
			}
			oldParent.mu.Unlock()
		}

		// C: p->hp_parent = parent;
		prop.mu.Lock()
		prop.parent = newParent
		if prop.manager == nil {
			prop.manager = newParent.manager
		}
		prop.mu.Unlock()

		// C: if(parent->hp_flags & (PROP_MULTI_SUB | PROP_MULTI_NOTIFY))
		//       prop_flood_flag(p, PROP_MULTI_NOTIFY, 0);
		newParent.mu.RLock()
		parentMulti := newParent.flags&(FlagMultiSub|FlagMultiNotify) != 0
		newParent.mu.RUnlock()
		if parentMulti {
			pm.floodFlagOnChildren(prop, FlagMultiNotify, 0)
		}

		// C: prop_insert(p, parent, before, skipme)
		newParent.mu.Lock()
		// Re-check under the parent lock: C's entry ZOMBIE check and the
		// insert are atomic under prop_mutex; without this, a destroy
		// interleaving between the entry check and the insert leaves the
		// child parented to a dead prop, invisible to its destroy cascade.
		if newParent.destroyed {
			newParent.mu.Unlock()
			prop.mu.Lock()
			prop.parent = nil
			prop.mu.Unlock()
			return -1
		}
		if before != nil {
			// C: TAILQ_INSERT_BEFORE(before, p, hp_parent_link)
			// Insert prop before 'before' in children list
			insertIdx := -1
			for i, child := range newParent.children {
				if child == before {
					insertIdx = i
					break
				}
			}
			if insertIdx >= 0 {
				newParent.children = slices.Insert(newParent.children, insertIdx, prop)
			} else {
				// 'before' not found — append to tail
				newParent.children = append(newParent.children, prop)
			}
		} else {
			// C: TAILQ_INSERT_TAIL(&parent->hp_childs, p, hp_parent_link)
			newParent.children = append(newParent.children, prop)
		}
		// Update childIndex
		if prop.name != "" {
			if newParent.childIndex == nil {
				newParent.childIndex = make(map[string]*Prop)
			}
			newParent.childIndex[prop.name] = prop
		}
		subs := make([]*Subscription, len(newParent.valueSubs))
		copy(subs, newParent.valueSubs)
		newParent.mu.Unlock()

		// C: prop_notify_child (PROP_ADD_CHILD) or prop_notify_child2 (PROP_ADD_CHILD_BEFORE)
		// C: if before == NULL: PROP_ADD_CHILD (child, parent, user_int)
		// C: if before != NULL: PROP_ADD_CHILD_BEFORE (child, before, user_int)
		for _, sub := range subs {
			if sub == skipme {
				continue
			}
			if before != nil {
				notifySub(sub, EventAddChildBefore, prop, newParent, before)
			} else {
				notifySub(sub, EventAddChild, prop, newParent)
			}
		}
	} else {
		// C: prop_move0(p, before, skipme) — same parent, just reorder
		// C: if before == NULL and p is not already last, move to tail + PROP_MOVE_CHILD
		// C: if before != NULL and TAILQ_NEXT(p) != before, move + PROP_MOVE_CHILD
		// C: if already in position, NOP (no notification)
		// C: assert(p != before)
		if before == prop {
			// C: assert(p != before)
			return 0
		}

		newParent.mu.Lock()

		// Find current position of prop
		srcIdx := -1
		for i, child := range newParent.children {
			if child == prop {
				srcIdx = i
				break
			}
		}
		if srcIdx < 0 {
			newParent.mu.Unlock()
			return 0
		}

		// Check if already in position
		if before == nil {
			// Move to tail — check if already at tail
			if srcIdx == len(newParent.children)-1 {
				newParent.mu.Unlock()
				return 0 // already last, NOP
			}
		} else {
			// C: if before->hp_parent != p->hp_parent, return
			beforeFound := slices.Contains(newParent.children, before)
			if !beforeFound {
				newParent.mu.Unlock()
				return 0 // before not in same parent, NOP
			}
			// C: if TAILQ_NEXT(p) == before, already in position
			if srcIdx+1 < len(newParent.children) && newParent.children[srcIdx+1] == before {
				newParent.mu.Unlock()
				return 0 // already before 'before', NOP
			}
		}

		// Remove prop from current position
		newParent.children = slices.Delete(newParent.children, srcIdx, srcIdx+1)

		// Insert at new position
		if before == nil {
			// Move to tail
			newParent.children = append(newParent.children, prop)
		} else {
			// Insert before 'before'
			insertIdx := -1
			for i, child := range newParent.children {
				if child == before {
					insertIdx = i
					break
				}
			}
			if insertIdx >= 0 {
				newParent.children = slices.Insert(newParent.children, insertIdx, prop)
			} else {
				newParent.children = append(newParent.children, prop)
			}
		}

		subs := make([]*Subscription, len(newParent.valueSubs))
		copy(subs, newParent.valueSubs)
		newParent.mu.Unlock()

		// C: prop_notify_child2(p, parent, PROP_MOVE_CHILD, before, skipme, 0)
		// The callback receives (child, before_sibling, user_int)
		for _, sub := range subs {
			if sub == skipme {
				continue
			}
			notifySub(sub, EventMoveChild, prop, newParent, before)
		}
	}

	return 0
}

// SetParentVector sets the parent of each prop in a vector.
// C: prop_set_parent_vector(pv, parent, before, skipme) —
//  1. If parent is NULL/ZOMBIE, destroy all vector entries.
//  2. prop_make_dir(parent).
//  3. For each prop in vector: set hp_parent, flood MULTI_NOTIFY,
//     insert before 'before' or tail.
//  4. Single prop_notify_childv with PROP_ADD_CHILD_VECTOR.
//
// Go: Routes through SetParentEx for each child (per-child ADD_CHILD
// events instead of a single vector event — observable difference
// but functionally equivalent for most subscribers).
func (pm *PropManager) SetParentVector(vec *Prop, newParent *Prop, opaque any, name string) int {
	if vec == nil {
		return -1
	}

	// Get the vector's children (the props to add)
	vec.mu.RLock()
	children := make([]*Prop, len(vec.children))
	copy(children, vec.children)
	vec.mu.RUnlock()

	// C: if parent == NULL || parent->hp_type == PROP_ZOMBIE, destroy all
	if newParent == nil || newParent.destroyed {
		for _, child := range children {
			pm.Destroy(child)
		}
		return 0
	}

	// C: prop_make_dir(parent)
	newParent.mu.Lock()
	if newParent.propType != PropTypeDir {
		newParent.propType = PropTypeDir
	}
	newParent.mu.Unlock()

	// Set each child's parent to newParent
	for _, child := range children {
		pm.SetParentEx(child, newParent, opaque, "")
	}

	return 0
}

// NotifySimple sends a simple notification to all subscribers of a prop
func (pm *PropManager) Unlink(prop *Prop) {
	if prop == nil {
		return
	}
	pm.unlinkInternal(prop, nil, "prop_unlink()")
}

// unlinkInternal is the internal unlink that can skip a specific subscriber.
// C: prop_unlink0(dst, skipme, origin, pnq) → prop_follow_and_unlink →
// restore_and_descend → retarget_subscription
//
// C semantics: restore_and_descend retargets the target's subs back to the
// target's own value_prop. The target's value was NEVER changed during link
// (subs were retargeted to source). So after unlink, subs see the target's
// original value.
//
// Go semantics: Link copies source's value to target. Unlink must restore
// the target's pre-link value (saved in preLinkValue/preLinkType/preLinkChildren)
// so subs see the original value, matching C.
func (pm *PropManager) unlinkInternal(target *Prop, skipme *Subscription, origin string) {
	target.mu.Lock()
	if target.originator == nil {
		target.mu.Unlock()
		return
	}

	src := target.originator

	// C: if dst->hp_flags & PROP_XREFED_ORIGINATOR, src->hp_xref--
	wasXrefed := target.flags&FlagXrefedOrigin != 0
	if wasXrefed {
		target.flags &^= FlagXrefedOrigin
	}

	// Clear link state
	target.linkedTo = nil
	target.originator = nil
	// C-canonical: NO value restoration (target's value was never changed)
	// C-canonical: NO mirror child cleanup (target never had mirror children)
	target.mu.Unlock()

	// C: if wasXrefed, prop_destroy0(src) (decrement xref)
	if wasXrefed {
		pm.Destroy(src)
	}

	// C: prop_follow_and_unlink → restore_and_descend
	// Move subs whose origin chain passes through dst (or a descendant
	// of dst) back from source to target.
	pm.propFollowAndUnlink(target, src, skipme, origin, nil)

	// Remove target from source's targets list
	src.mu.Lock()
	for i, t := range src.targets {
		if t == target {
			src.targets = slices.Delete(src.targets, i, i+1)
			break
		}
	}
	src.mu.Unlock()
}

// propFollowAndUnlink — C: prop_follow_and_unlink (prop_core.c:4666-4687).
// Follows src's own originator chain to the final source, accumulating
// each traversed link into prependvec; if any sub in src's subtree came
// through dst (the broken link), restores them onto dst's tree.
func (pm *PropManager) recursiveUnlink(prop *Prop) {
	prop.mu.RLock()
	targets := make([]*Prop, len(prop.targets))
	copy(targets, prop.targets)
	prop.mu.RUnlock()

	for _, target := range targets {
		if target == nil || target == prop {
			continue
		}
		// Recursively unlink target's own targets first
		pm.recursiveUnlink(target)
		// Unlink target from this prop
		pm.unlinkInternal(target, nil, "prop_destroy0/recursive_unlink")
	}
}

// The recursive destruction of children before the parent is critical:
// subscriptions on child props (e.g. model.nodes) must receive EventDestroyed
// so that cleanup callbacks (e.g. scanner_nodes_callback) fire correctly.
// Destroy destroys a property, following C's prop_destroy0 semantics.
//
// C destruction order (prop_core.c:2275-2399):
//  1. If already ZOMBIE, return 0.
//  2. Decrement hp_xref. If xref > 0, return 0 (content still owned).
//  3. recursive_unlink(p) — unlink all targets.
//  4. EARLY_DEL_CHILD notifications to parent's subs.
//  5. Type-specific cleanup (destroy children, release rstr, etc).
//  6. Set hp_type = PROP_ZOMBIE.
//  7. Notify canonical subs: PROP_DESTROYED (if TRACK_DESTROY).
//  8. Notify value subs: PROP_SET_VOID (if not TRACK_DESTROY), detach.
//  9. Remove from originator.
//
// 10. Remove from parent + late DEL_CHILD notifications.
// 11. prop_ref_dec_locked(p) — may free memory if refcount hits 0.
// Destroy decrements xref and if xref==0, performs full destruction.
// C: prop_destroy0 (prop_core.c:2275) — returns 1 if destroyed, 0 if xref>0.
// Go: Returns true if destroyed, false if xref>0 (content still owned).
func (pm *PropManager) Destroy(prop *Prop) bool {
	if prop == nil {
		return false
	}

	// Step 1: Check if already ZOMBIE (C: if hp_type == PROP_ZOMBIE return 0)
	prop.mu.RLock()
	alreadyDestroyed := prop.destroyed
	prop.mu.RUnlock()
	if alreadyDestroyed {
		return false
	}

	// Step 2: Decrement xref. If xref > 0, content is still owned — return.
	// C: p->hp_xref--; if(p->hp_xref) return 0;
	prop.mu.Lock()
	if prop.destroyed {
		prop.mu.Unlock()
		return false
	}
	if prop.xref > 0 {
		prop.xref--
	}
	if prop.xref > 0 {
		// Content still owned by another xref holder. Not destroyed.
		prop.mu.Unlock()
		return false
	}
	// xref == 0: content is ours to destroy. Proceed.
	prop.mu.Unlock()

	// Step 3: recursive_unlink(p) — unlink all targets.
	// C: recursive_unlink(p) iterates p->hp_targets and unlinks each.
	prop.mu.RLock()
	originator := prop.originator
	prop.mu.RUnlock()
	if originator != nil {
		pm.unlinkInternal(prop, nil, "prop_destroy()")
	}
	pm.recursiveUnlink(prop)

	// Step 4: EARLY_DEL_CHILD notifications.
	// C: LIST_FOREACH(s, &parent->hp_value_subscriptions, ...)
	//       if(s->hps_flags & PROP_SUB_EARLY_DEL_CHILD)
	//         prop_build_notify_child(s, p, PROP_DEL_CHILD, 0, 0);
	// The child (prop) is still accessible at this point — not yet ZOMBIE.
	prop.mu.RLock()
	parent := prop.parent
	prop.mu.RUnlock()
	if parent != nil {
		notifyValueSubsWithFlag(parent, prop, SubFlagEarlyDelChild, EventDelChild)
	}

	// Step 5: Type-specific cleanup — destroy children.
	// C: case PROP_DIR: for each child: prop_destroy_child(p, c)
	// prop_destroy_child(p, c) calls prop_destroy0(c). If c can't be destroyed
	// (xref > 0), it notifies DEL_CHILD to p's subs and removes c from p anyway.
	prop.mu.RLock()
	children := make([]*Prop, len(prop.children))
	copy(children, prop.children)
	propType := prop.propType
	propRef := prop.propRef
	propRefSub := prop.propRefSub
	prop.mu.RUnlock()

	// C: case PROP_PROXY: prop_proxy_destroy(p); p->hp_type = PROP_ZOMBIE; goto finale;
	if propType == PropTypeProxy {
		// C: prop_proxy_destroy — release proxy connection, destroy owned props
		prop.mu.RLock()
		pConn := prop.proxyConn
		prop.mu.RUnlock()
		if pd, ok := pConn.(ProxyDestroyer); ok && pd != nil {
			pd.ProxyDestroy(prop)
		}
		prop.mu.Lock()
		prop.proxyConn = nil
		prop.mu.Unlock()
		// C: goto finale — skip type-specific cleanup, go to ZOMBIE + notifications
	} else if propType == PropTypeProp && propRef != nil {
		// C: case PROP_PROP: prop_ref_dec_locked(p->hp_prop)
		if propRefSub != nil {
			propRefSub.active.Store(false)
		}
		atomic.AddInt32(&propRef.refCount, -1)
		prop.mu.Lock()
		prop.propRef = nil
		prop.propRefSub = nil
		prop.mu.Unlock()
	}

	for _, child := range children {
		pm.destroyChild(prop, child)
	}

	// Step 6: Set hp_type = PROP_ZOMBIE.
	// Also capture state for notifications while holding lock.
	prop.mu.Lock()
	if prop.destroyed {
		// Another goroutine destroyed it while we held the lock
		prop.mu.Unlock()
		return false
	}
	prop.destroyed = true
	prop.propType = PropTypeZombie // C: p->hp_type = PROP_ZOMBIE
	parent = prop.parent
	// C: canonical subs are notified with DESTROYED (if TRACK_DESTROY),
	//    then removed from canonical list.
	canonicalSubs := make([]*Subscription, len(prop.canonicalSubs))
	copy(canonicalSubs, prop.canonicalSubs)
	// C: value subs are notified with SET_VOID (if not TRACK_DESTROY),
	//    then removed from value list.
	valueSubs := make([]*Subscription, len(prop.valueSubs))
	copy(valueSubs, prop.valueSubs)
	// C: hp_parent stays set until after the late DEL_CHILD notify
	// (prop_core.c:2384-2393) — cleared in step 10.
	prop.children = make([]*Prop, 0)
	prop.childIndex = make(map[string]*Prop)
	prop.canonicalSubs = nil
	prop.valueSubs = nil
	prop.subs = nil
	prop.linkedTo = nil
	prop.originator = nil
	prop.targets = nil
	prop.value = nil
	prop.mu.Unlock()

	// Step 7: Notify canonical subs with TRACK_DESTROY → PROP_DESTROYED.
	// C: while(s = LIST_FIRST(&p->hp_canonical_subscriptions)) {
	//       LIST_REMOVE(s, hps_canonical_prop_link);
	//       if(s->hps_flags & (PROP_SUB_TRACK_DESTROY | PROP_SUB_TRACK_DESTROY_EXP))
	//         prop_notify_destroyed(s);
	//    }
	// C: prop_notify_destroyed passes `s` (the subscription) as arg.
	for _, sub := range canonicalSubs {
		if sub.trackDestroy {
			notifySub(sub, EventDestroyed, nil, sub)
		}
	}

	// Step 8: Notify value subs without TRACK_DESTROY → PROP_SET_VOID.
	// C: while(s = LIST_FIRST(&p->hp_value_subscriptions)) {
	//       if(!(s->hps_flags & (PROP_SUB_TRACK_DESTROY | PROP_SUB_TRACK_DESTROY_EXP)))
	//         prop_notify_void(s);
	//       LIST_REMOVE(s, hps_value_prop_link);
	//    }
	// C: prop_notify_void (prop_core.c:1468-1489):
	//   Direct (INTERNAL): cb(opaque, PROP_SET_VOID, value_prop, user_int)
	//     — passes value_prop as arg (non-refcounted)
	//   Courier: prop_get_notify(s) + n->hpn_event = PROP_SET_VOID + enqueue
	//     — n->hpn_prop NOT set (no prop ref, no prop in callback args)
	//     — notify_invoke SET_VOID: cb(opaque, PROP_SET_VOID, user_int)
	// Go: notifyVoidSub handles both modes correctly.
	for _, sub := range valueSubs {
		if !sub.trackDestroy {
			notifyVoidSub(sub, prop)
		}
	}

	// Step 9: Remove from originator.
	// C: if(p->hp_originator != NULL) prop_remove_from_originator(p);
	// Already handled by unlinkInternal in step 3.

	// Step 10: Late DEL_CHILD notifications THEN remove from parent.
	// C order (prop_core.c:2384-2389):
	//   LIST_FOREACH(s, &parent->hp_value_subscriptions, ...)
	//     if(!(s->hps_flags & PROP_SUB_EARLY_DEL_CHILD))
	//       prop_build_notify_child(s, p, PROP_DEL_CHILD, 0, 0);
	//   TAILQ_REMOVE(&parent->hp_childs, p, hp_parent_link);
	//   p->hp_parent = NULL;
	//   if(parent->hp_selected == p) parent->hp_selected = NULL;
	if parent != nil {
		// Late DEL_CHILD: notify value subs that DON'T have EARLY_DEL_CHILD
		// Child is ZOMBIE at this point (matching C behavior)
		notifyValueSubsWithoutFlag(parent, prop, SubFlagEarlyDelChild, EventDelChild)

		// Remove from parent AFTER notifications (matching C order)
		parent.mu.Lock()
		for i, c := range parent.children {
			if c == prop {
				parent.children = slices.Delete(parent.children, i, i+1)
				break
			}
		}
		if prop.name != "" {
			delete(parent.childIndex, prop.name)
		}
		// C: if(parent->hp_selected == p) parent->hp_selected = NULL;
		if parent.selectedChild == prop {
			parent.selectedChild = nil
		}
		parent.mu.Unlock()

		// C: p->hp_parent = NULL (prop_core.c:2392) — after DEL_CHILD
		// notify and TAILQ_REMOVE.
		prop.mu.Lock()
		prop.parent = nil
		prop.mu.Unlock()
	}

	// Step 11: prop_ref_dec_locked(p) — decrement memory refcount.
	// C: if refcount hits 0 and ZOMBIE, free memory (pool_put).
	// Go: decrement; if 0 and ZOMBIE, clear is already done. GC handles rest.
	pm.refDecInternal(prop)
	return true
}

// destroyChild implements C's prop_destroy_child(p, c) semantics.
// C (prop_core.c:2246-2253):
//
//	prop_destroy_child(p, c) {
//	  if(!prop_destroy0(c)) {
//	    prop_notify_child(c, p, PROP_DEL_CHILD, NULL, 0);
//	    TAILQ_REMOVE(&p->hp_childs, c, hp_parent_link);
//	    c->hp_parent = NULL;
//	  }
//	}
//
// If the child can be destroyed (xref==0), prop_destroy0 handles everything
// including DEL_CHILD notification. If the child CANNOT be destroyed (xref>0),
// we still need to notify DEL_CHILD to the parent's value subs and remove
// the child from the parent's child list.
func (pm *PropManager) destroyChild(parent *Prop, child *Prop) {
	// Try to destroy the child. If xref > 0, Destroy returns without destroying.
	child.mu.RLock()
	alreadyDestroyed := child.destroyed
	child.mu.RUnlock()
	if alreadyDestroyed {
		return
	}

	// Attempt destruction
	pm.Destroy(child)

	// Check if child was actually destroyed
	child.mu.RLock()
	wasDestroyed := child.destroyed
	child.mu.RUnlock()

	if !wasDestroyed {
		// Child was NOT destroyed (xref > 0). C: prop_destroy_child does:
		//   prop_notify_child(c, p, PROP_DEL_CHILD, NULL, 0);
		//   TAILQ_REMOVE(&p->hp_childs, c, hp_parent_link);
		//   c->hp_parent = NULL;
		notifySubs(parent, EventDelChild, child)

		parent.mu.Lock()
		for i, c := range parent.children {
			if c == child {
				parent.children = slices.Delete(parent.children, i, i+1)
				break
			}
		}
		if child.name != "" {
			delete(parent.childIndex, child.name)
		}
		parent.mu.Unlock()

		child.mu.Lock()
		child.parent = nil
		child.mu.Unlock()
	}
}

// notifyMultiParentChain walks up the parent chain and notifies MULTI subs.
// C: prop_notify_value (prop_core.c:1551-1556)
//
//	if(p->hp_flags & PROP_MULTI_NOTIFY)
//	  while((p = p->hp_parent) != NULL)
//	    if(p->hp_flags & PROP_MULTI_SUB)
//	      LIST_FOREACH(s, &p->hp_value_subscriptions, ...)
//	        if(s->hps_flags & PROP_SUB_MULTI)
//	          prop_build_notify_value(s, 0, origin, p, NULL);
func (pm *PropManager) DestroyChilds(prop *Prop) {
	if prop == nil {
		return
	}

	prop.mu.Lock()

	// C: prop_destroy_childs0 only runs if p->hp_type == PROP_DIR
	if prop.propType != PropTypeDir {
		prop.mu.Unlock()
		return
	}

	children := make([]*Prop, len(prop.children))
	copy(children, prop.children)

	prop.children = make([]*Prop, 0)
	prop.childIndex = make(map[string]*Prop)
	// C: p->hp_selected = NULL (prop_core.c:2427)
	prop.selectedChild = nil
	prop.propType = PropTypeVoid

	// C: prop_notify_value(p, NULL, "prop_destroy_childs0()")
	// uses hp_value_subscriptions
	valueSubs := make([]*Subscription, len(prop.valueSubs))
	copy(valueSubs, prop.valueSubs)

	prop.mu.Unlock()

	// Notify value subs of SET_VOID (type changed to VOID)
	// C: prop_notify_value(p, NULL, "prop_destroy_childs0()")
	// → prop_build_notify_value(s, 0, origin, s->hps_value_prop, NULL)
	// For SET_VOID: direct passes value_prop, courier does not.
	for _, sub := range valueSubs {
		notifyVoidSub(sub, prop)
	}

	// C: for each child:
	//   LIST_FOREACH(s, &p->hp_value_subscriptions, ...)
	//     if(s->hps_flags & PROP_SUB_EARLY_DEL_CHILD)
	//       prop_build_notify_child(s, c, PROP_DEL_CHILD, 0, 0);
	//   c->hp_parent = NULL;
	//   prop_destroy0(c);
	for _, child := range children {
		// EARLY_DEL_CHILD: notify while child is still accessible
		notifyValueSubsWithFlag(prop, child, SubFlagEarlyDelChild, EventDelChild)

		child.mu.Lock()
		child.parent = nil
		child.mu.Unlock()

		pm.Destroy(child)
	}
}

// PropGetProp — C: prop_get_prop (prop_core.c:4909-4921).
// If p is PROP_PROP, returns a new ref to the referenced prop;
// otherwise returns a new ref to p itself.
func (pm *PropManager) RefInc(prop *Prop) *Prop {
	if prop == nil {
		return nil
	}
	atomic.AddInt32(&prop.refCount, 1)
	return prop
}

// refDecInternal is the internal refcount decrement (C: prop_ref_dec_locked).
// If refcount hits 0 and prop is ZOMBIE, the prop is logically freed.
// In Go, we clear remaining fields; GC handles actual memory reclamation.
// Must NOT be called while prop.mu is held (to avoid lock ordering issues).
func (pm *PropManager) refDecInternal(prop *Prop) {
	if prop == nil {
		return
	}
	newRef := atomic.AddInt32(&prop.refCount, -1)
	if newRef == 0 {
		// C invariant: assert(p->hp_type == PROP_ZOMBIE)
		// refcount == 0 => prop MUST already be ZOMBIE.
		// This is a semantic invariant, not just a memory management detail.
		// If a prop reaches refcount 0 without being ZOMBIE, it means
		// a reference was over-decremented (bug in refcount management).
		prop.mu.RLock()
		isZombie := prop.destroyed
		prop.mu.RUnlock()
		if !isZombie {
			panic("prop_ref_dec: refcount reached 0 on non-ZOMBIE prop (refcount over-decremented)")
		}
	}
	if newRef < 0 {
		panic("prop_ref_dec: refcount went negative (double free)")
	}
}

// XrefAddref increments the content refcount (C: prop_xref_addref).
// This keeps the prop's content alive (prevents Destroy from proceeding)
// while the xref holder exists. Returns the prop.
func (pm *PropManager) XrefAddref(prop *Prop) *Prop {
	if prop == nil {
		return nil
	}
	prop.mu.Lock()
	defer prop.mu.Unlock()
	if prop.xref < 255 {
		prop.xref++
	}
	return prop
}

// XrefCount returns the current xref count (for debugging/testing).
func (pm *PropManager) XrefCount(prop *Prop) uint8 {
	if prop == nil {
		return 0
	}
	prop.mu.RLock()
	defer prop.mu.RUnlock()
	return prop.xref
}

// RefCount returns the current memory refcount (for debugging/testing).
func (pm *PropManager) RefCount(prop *Prop) int32 {
	if prop == nil {
		return 0
	}
	return atomic.LoadInt32(&prop.refCount)
}

// IsZombie returns true if the prop has been destroyed (C: hp_type == PROP_ZOMBIE).
func (pm *PropManager) IsZombie(prop *Prop) bool {
	if prop == nil {
		return true
	}
	prop.mu.RLock()
	defer prop.mu.RUnlock()
	return prop.destroyed
}

// SuggestFocus suggests focus on a property.
// C: prop_suggest_focus0(p) — checks ZOMBIE, gets parent (assert DIR),
// then prop_notify_child(p, parent, PROP_SUGGEST_FOCUS, NULL, 0).
// The event goes to the PARENT's value subscribers with the child as
// the prop argument.
func (pm *PropManager) SuggestFocus(prop *Prop) {
	if prop == nil {
		return
	}
	// C: if(p->hp_type == PROP_ZOMBIE) return;
	if prop.destroyed {
		return
	}
	parent := prop.parent
	if parent == nil {
		return
	}
	// C: prop_notify_child(p, parent, PROP_SUGGEST_FOCUS, NULL, 0)
	// → iterates parent->hp_value_subscriptions, sends event with child
	parent.mu.Lock()
	subs := make([]*Subscription, len(parent.valueSubs))
	copy(subs, parent.valueSubs)
	parent.mu.Unlock()

	for _, sub := range subs {
		notifySub(sub, EventSuggestFocus, prop, parent)
	}
}

// Link links two properties so that target mirrors source's value and children.
// This matches C's prop_link(src, dst) semantics where dst mirrors src.
//
// C's prop_link0 works by:
//  1. If dst already has an originator, unlink it first (prop_unlink0)
//  2. If dst->hp_originator == src, it's a NOP (duplicate link)
//  3. Set dst->hp_originator = src
//  4. Insert dst into src->hp_targets list
//  5. relink_subscriptions: retarget all of dst's value subscriptions to src
//     (so when src changes, dst's subscribers are notified)
//  6. Copy src's children to dst (via prop_make_dir + recursive relink)
//
// Go's implementation uses event forwarding instead of subscription retargeting:
// - target.linkedTo = source (for lazy reads via FindChild/GetChildren/GetString)
// - target.originator = source (for proper unlink tracking)
// - source.targets includes target (for event forwarding on value/child changes)
// - On Link: copy source's current value and children to target
// - On setPropValue: forward value to all targets
// - On AddChild: forward child to all targets
// This achieves the same observable behavior as C's subscription retargeting.
// Link establishes a soft link from source to target.
// C: prop_link(src, dst, skipme) — hard = PROP_LINK_SOFT.
func (p *Prop) createChildCanonical(name string) *Prop {
	if p == nil {
		return nil
	}
	// C: prop_create0 checks for existing child with same name
	p.mu.Lock()
	if name != "" && p.childIndex != nil {
		if existing, ok := p.childIndex[name]; ok {
			p.mu.Unlock()
			return existing
		}
	}

	// C: prop_make_dir(parent) — if parent not DIR, transition
	if p.propType != PropTypeDir {
		p.propType = PropTypeDir
	}

	child := &Prop{
		name:       name,
		children:   make([]*Prop, 0),
		childIndex: make(map[string]*Prop),
		parent:     p,
		refCount:   1, // C: prop_make sets hp_refcount = 1
		xref:       1, // C: prop_make sets hp_xref = 1
		manager:    p.manager,
	}

	// C: prop_insert — add to parent's children list
	p.children = append(p.children, child)
	if p.childIndex == nil {
		p.childIndex = make(map[string]*Prop)
	}
	p.childIndex[name] = child

	// C: MULTI_NOTIFY flag inheritance (prop_core.c:1970-1971)
	if p.flags&(FlagMultiSub|FlagMultiNotify) != 0 {
		child.flags |= FlagMultiNotify
	}

	// Snapshot parent's value subs for EventAddChild notification
	parentSubs := make([]*Subscription, len(p.valueSubs))
	copy(parentSubs, p.valueSubs)

	// Snapshot targets for mirror creation
	targetsToMirror := make([]*Prop, 0, len(p.targets))
	for _, target := range p.targets {
		if target != nil && !target.destroyed {
			targetsToMirror = append(targetsToMirror, target)
		}
	}

	p.mu.Unlock()

	// C: prop_notify_child(parent, PROP_ADD_CHILD, child)
	for _, sub := range parentSubs {
		notifySub(sub, EventAddChild, child, p)
	}

	// C: mirror creation for linked targets
	for _, target := range targetsToMirror {
		mirrorChild := &Prop{
			name:       name,
			children:   make([]*Prop, 0),
			childIndex: make(map[string]*Prop),
			parent:     target,
			refCount:   1,
			xref:       1,
			manager:    p.manager,
		}
		target.mu.Lock()
		target.children = append(target.children, mirrorChild)
		if target.childIndex == nil {
			target.childIndex = make(map[string]*Prop)
		}
		target.childIndex[name] = mirrorChild
		target.mu.Unlock()
		child.mu.Lock()
		child.targets = append(child.targets, mirrorChild)
		mirrorChild.originator = child
		child.mu.Unlock()
	}

	return child
}

// SetPropExl sets a property value from another property
// C: prop_set_prop_exl — prop_clean(target), ref_inc(source), set type=PROP_PROP, notify.
// SetPropExl sets target to PROP_PROP type pointing to source.
// C: prop_set_prop_exl (prop_core.c:4198-4218)
// C semantics:
//   - If target is ZOMBIE, return
//   - If target is already PROP_PROP and points to same source, NOP
//   - If target is already PROP_PROP pointing to different source, dec old ref
//   - If target is other type, prop_clean (can refuse)
//   - Set hp_prop = ref_inc(source), hp_type = PROP_PROP
//   - prop_notify_value → sends PROP_SET_PROP with source prop pointer
//   - NO live forwarding subscription (C does not forward later changes)
func (pm *PropManager) CreateInfo(parent *Prop, image string, description *Prop) {
	r := pm.CreateEx(parent, "", nil, false, false)
	if description != nil {
		// C: prop_set(r, "description", PROP_SET_LINK, description)
		descProp := pm.CreateEx(r, "description", nil, false, false)
		if descProp != nil {
			pm.Link(description, descProp, nil, false, false)
		}
	}
	if image != "" {
		imageProp := pm.CreateEx(r, "image", nil, false, false)
		if imageProp != nil {
			pm.SetStringEx(imageProp, nil, image, StringUTF8)
		}
	}
}

// ==================== PROXY-SPECIFIC METHODS ====================

// GetProxyID returns the proxy ID
func (pm *PropManager) Destroy0(p *Prop) {
	if p == nil {
		return
	}
	pm.Destroy(p)
}

// DestroyBool returns true if the prop was destroyed (xref==0), false otherwise.
// This is the bool-returning version of Destroy for callers that need to know.
func (pm *PropManager) DestroyBool(p *Prop) bool {
	if p == nil {
		return false
	}
	return pm.Destroy(p)
}

// PropProxyWantMoreChilds requests more children for a proxy property.
// C: prop_want_more_childs → prop_proxy_want_more_childs when s->hps_proxy
// (sends STPP_CMD_WANT_MORE_CHILDS). The linkedTo fallback keeps the
// historical scaffold behavior for non-wire callers.
func (pm *PropManager) RefDec(prop *Prop) {
	pm.refDecInternal(prop)
}

// Unsubscribe removes a subscription
func (p *Prop) Release() {
	if p != nil {
		newRef := atomic.AddInt32(&p.refCount, -1)
		if newRef == 0 {
			p.mu.RLock()
			isZombie := p.destroyed
			p.mu.RUnlock()
			if !isZombie {
				panic("Prop.Release: refcount reached 0 on non-ZOMBIE prop")
			}
		}
	}
}

// Ref increments the property's reference count (C: prop_ref_inc).
// Returns the prop for convenience. Used by callers that need to hold
// an explicit memory reference separate from subscription-based refs,
// matching C's pattern of x->storage = prop_ref_inc(storage->t_prop).
func (p *Prop) Ref() *Prop {
	if p != nil {
		atomic.AddInt32(&p.refCount, 1)
	}
	return p
}

// Retain retains the property (increments reference count)
func (p *Prop) Retain() {
	atomic.AddInt32(&p.refCount, 1)
}

// SetParent sets the parent of a property.
// C: prop_set_parent_ex(p, parent, NULL, "") → prop_set_parent0.
// Go: Implements canonical semantics directly:
// - Remove from old parent's children + fire DEL_CHILD
// - MakeDir on new parent
// - Add to new parent's children + fire ADD_CHILD
// - Inherit MULTI_NOTIFY flag from new parent
func (p *Prop) SetParent(parent *Prop) {
	if p == nil {
		return
	}

	// Read current parent
	p.mu.RLock()
	oldParent := p.parent
	p.mu.RUnlock()

	if oldParent == parent {
		return // no change
	}

	// Remove from old parent
	if oldParent != nil {
		oldParent.mu.Lock()
		for i, c := range oldParent.children {
			if c == p {
				oldParent.children = slices.Delete(oldParent.children, i, i+1)
				break
			}
		}
		if p.name != "" && oldParent.childIndex != nil {
			delete(oldParent.childIndex, p.name)
		}
		if oldParent.selectedChild == p {
			oldParent.selectedChild = nil
		}
		oldSubs := make([]*Subscription, len(oldParent.valueSubs))
		copy(oldSubs, oldParent.valueSubs)
		oldParent.mu.Unlock()

		// Fire DEL_CHILD to old parent's value subs
		for _, sub := range oldSubs {
			notifySub(sub, EventDelChild, p, oldParent)
		}
	}

	// Add to new parent
	if parent != nil {
		parent.mu.Lock()

		// C: prop_make_dir(parent)
		if parent.propType != PropTypeDir {
			parent.propType = PropTypeDir
		}

		// Check for existing child with same name
		if p.name != "" && parent.childIndex != nil {
			if existing, ok := parent.childIndex[p.name]; ok && existing != p {
				parent.mu.Unlock()
				return // don't overwrite existing child
			}
		}

		p.mu.Lock()
		p.parent = parent
		// Inherit manager from parent if not already set
		if p.manager == nil {
			p.manager = parent.manager
		}
		p.mu.Unlock()

		parent.children = append(parent.children, p)
		if p.name != "" {
			if parent.childIndex == nil {
				parent.childIndex = make(map[string]*Prop)
			}
			parent.childIndex[p.name] = p
		}

		// C: MULTI_NOTIFY flag inheritance
		if parent.flags&(FlagMultiSub|FlagMultiNotify) != 0 {
			p.flags |= FlagMultiNotify
		}

		newSubs := make([]*Subscription, len(parent.valueSubs))
		copy(newSubs, parent.valueSubs)
		parent.mu.Unlock()

		// Fire ADD_CHILD to new parent's value subs
		for _, sub := range newSubs {
			notifySub(sub, EventAddChild, p, parent)
		}
	} else {
		// parent == nil: just clear
		p.mu.Lock()
		p.parent = nil
		p.mu.Unlock()
	}
}

// ==================== ADDITIONAL PROP MANAGER METHODS ====================

// CreateRoot creates a root property.
// C: prop_create_root_ex(name, noalloc) — creates a prop with NO parent
// (parent == NULL), not under the global root.
// Go: Routes through CreateRootEx which calls CreateEx(nil, ...).
func (pm *PropManager) CreateRoot(name string) *Prop {
	return pm.CreateRootEx(name, false)
}

// NewStandaloneProp creates a bare *Prop without a PropManager.
// Used for per-clone prop roots in the GLW cloner when a PropManager
// is not available (e.g. in test contexts).
// C: prop_create_root(NULL) creates a prop with no name and no parent.
func NewStandaloneProp(name string) *Prop {
	return &Prop{
		name:       name,
		children:   make([]*Prop, 0),
		childIndex: make(map[string]*Prop),
		refCount:   1,
	}
}

// SetEx sets a property value.
// C: prop_set_* family — full canonical path with notification.
// Go: Routes through setPropValue for full canonical semantics.
func CreatePropVec(size ...int) *Prop {
	capacity := 0
	if len(size) > 0 {
		capacity = size[0]
	}
	return &Prop{
		name:     "vector",
		children: make([]*Prop, 0, capacity),
		refCount: 1,
		xref:     1,
		// manager is set when the vector is attached to a parent via
		// SetParent/SetParentVector/AddChild, which inherit the parent's manager.
	}
}

// SetVoid sets a void value
func (pm *PropManager) Vec() *Prop {
	return CreatePropVec()
}

// Subscribe subscribes to property events
func (pm *PropManager) DestroyByName(parent *Prop, name string) {
	if parent == nil {
		return
	}
	child := parent.GetChild(name)
	if child != nil {
		pm.Destroy(child)
	}
}

// ReqMove sends a PROP_REQ_MOVE_CHILD request to the parent's value
// subscribers (skipme = NULL). C: prop_req_move (prop_core.c:2672).
func (pm *PropManager) MarkChilds(prop *Prop) {
	if prop == nil {
		return
	}
	prop.mu.RLock()
	// C: if(p->hp_type == PROP_DIR)
	if prop.propType != PropTypeDir {
		prop.mu.RUnlock()
		return
	}
	children := make([]*Prop, len(prop.children))
	copy(children, prop.children)
	prop.mu.RUnlock()
	for _, child := range children {
		child.mu.Lock()
		child.flags |= FlagMarked
		child.mu.Unlock()
	}
}

// Unmark clears the PROP_MARKED flag from a prop.
// C: prop_unmark (prop_core.c:5811) — p->hp_flags &= ~PROP_MARKED
func (pm *PropManager) Unmark(prop *Prop) {
	if prop == nil {
		return
	}
	prop.mu.Lock()
	defer prop.mu.Unlock()
	prop.flags &^= FlagMarked
}

// DestroyMarkedChilds destroys all children with PROP_MARKED flag set.
// C: prop_destroy_marked_childs — iterates children, calls prop_destroy0
// on each child with PROP_MARKED flag set.
func (pm *PropManager) DestroyMarkedChilds(prop *Prop) {
	if prop == nil {
		return
	}
	prop.mu.Lock()
	var marked []*Prop
	var newChildren []*Prop
	for _, child := range prop.children {
		child.mu.RLock()
		isMarked := child.flags&FlagMarked != 0
		child.mu.RUnlock()
		if isMarked {
			marked = append(marked, child)
		} else {
			newChildren = append(newChildren, child)
		}
	}
	prop.children = newChildren
	if prop.childIndex != nil {
		prop.childIndex = make(map[string]*Prop)
		for _, c := range newChildren {
			if c.name != "" {
				prop.childIndex[c.name] = c
			}
		}
	}
	prop.mu.Unlock()

	for _, child := range marked {
		pm.Destroy(child)
	}
}

// IsMarked reports whether the PROP_MARKED flag is set on a prop.
// C: prop_is_marked (prop_core.c:5825)
func (pm *PropManager) IsMarked(p *Prop) bool {
	if p == nil {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.flags&FlagMarked != 0
}

// UnparentChilds detaches all children of a directory prop.
// C: prop_unparent_childs (prop_core.c:2228) — dead upstream code
// (no callers), ported for canonical completeness.
func (pm *PropManager) UnparentChilds(p *Prop) {
	if p == nil {
		return
	}
	p.mu.RLock()
	isDir := p.propType == PropTypeDir
	children := make([]*Prop, len(p.children))
	copy(children, p.children)
	p.mu.RUnlock()
	if !isDir {
		return
	}
	// C: for(c = TAILQ_FIRST; c; c = next) prop_unparent0(p, NULL)
	for _, c := range children {
		pm.SetParentEx(c, nil, nil, "")
	}
}

// GetNameOfChilds returns the names of a directory's children.
// C: prop_get_name_of_childs (prop_core.c:5707) — skips VOID/ZOMBIE
// children; unnamed children yield "*<index>".
func (pm *PropManager) GetNameOfChilds(p *Prop) []string {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	if p.propType != PropTypeDir {
		p.mu.RUnlock()
		return nil
	}
	children := make([]*Prop, len(p.children))
	copy(children, p.children)
	p.mu.RUnlock()

	var rval []string
	for i, c := range children {
		c.mu.RLock()
		t := c.propType
		n := c.name
		c.mu.RUnlock()
		if t == PropTypeVoid || t == PropTypeZombie {
			continue
		}
		if n != "" {
			rval = append(rval, n)
		} else {
			rval = append(rval, "*"+strconv.Itoa(i))
		}
	}
	return rval
}

// DestroyFirst destroys the first child of a directory prop.
// C: prop_destroy_first (prop_core.c:2529)
func (pm *PropManager) DestroyFirst(p *Prop) {
	if p == nil {
		return
	}
	p.mu.RLock()
	if p.propType != PropTypeDir || len(p.children) == 0 {
		p.mu.RUnlock()
		return
	}
	c := p.children[0]
	p.mu.RUnlock()
	pm.destroyChild(p, c)
}

// voidChilds0 — C: prop_void_childs0 (prop_core.c:2466-2479).
// Recurses into DIR children; leaf children are cleaned and set to
// PROP_VOID with a value notification.
func (pm *PropManager) voidChilds0(p *Prop) {
	p.mu.RLock()
	if p.propType == PropTypeZombie {
		p.mu.RUnlock()
		return
	}
	if p.propType == PropTypeDir {
		children := make([]*Prop, len(p.children))
		copy(children, p.children)
		p.mu.RUnlock()
		for _, c := range children {
			pm.voidChilds0(c)
		}
		return
	}
	p.mu.RUnlock()

	p.mu.Lock()
	// C: if(prop_clean(p)) return;
	if p.flags&FlagClippedValue != 0 || p.propType == PropTypeProxy {
		p.mu.Unlock()
		return
	}
	p.propType = PropTypeVoid
	p.value = nil
	subs := make([]*Subscription, len(p.valueSubs))
	copy(subs, p.valueSubs)
	p.mu.Unlock()

	// C: prop_notify_value(p, NULL, "prop_void_childs()")
	for _, sub := range subs {
		notifySubscriber(sub, nil)
	}
}

// VoidChilds sets all (leaf) children of a prop to PROP_VOID.
// C: prop_void_childs (prop_core.c:2487)
func (pm *PropManager) VoidChilds(p *Prop) {
	if p == nil {
		return
	}
	pm.voidChilds0(p)
}

// SubReemit re-emits the current value of a subscription's value prop.
// C: prop_sub_reemit (prop_core.c:3489) — prop_build_notify_value(s, 0,
// "reemit", s->hps_value_prop, NULL).
func (pm *PropManager) Unparent(prop *Prop) {
	pm.SetParentEx(prop, nil, nil, "")
}

// Move reorders prop before another sibling (same parent only).
// C: prop_move (prop_core.c) → prop_move0 — PROP_MOVE_CHILD notification.
