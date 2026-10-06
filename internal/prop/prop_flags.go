package prop

// Split from prop.go — Fase 4 pure-move refactor.
// No symbol, lock, or event-order changes.

type EventType int

const (
	EventSetVoid EventType = iota
	EventSetRString
	EventSetCString
	EventSetInt
	EventSetFloat
	EventSetDir
	EventSetURI
	EventSetProp
	EventAddChild
	EventAddChildBefore
	EventAddChildVector
	EventAddChildVectorBefore
	EventAddChildVectorDirect
	// C: PROP_ADD_SELECTED is a FLAG (0x1) passed to prop_add_child, NOT
	// a separate event type. Go previously had EventAddChildSelected here
	// which shifted all subsequent event values by 1 vs C's enum.
	EventDelChild
	EventMoveChild
	EventSelectChild
	EventReqNewChild
	EventReqDeleteVector
	EventReqDelete
	EventDestroyed
	EventValueProp
	EventExtEvent
	EventSubscriptionMonitorActive
	EventHaveMoreChildsYes
	EventHaveMoreChildsNo
	EventWantMoreChilds
	EventSuggestFocus
	EventReqMoveChild
	EventTypePropNotify
	// C: PROP_SET_LINK / PROP_ADOPT_RSTRING exist only as prop_seti
	// event tags (prop_core.c:5568-5574), never delivered to callbacks.
	EventSetLink
	EventAdoptRString
)

// String types for RString props.
// C: prop_str_type_t (prop.h:97-100) — PROP_STR_UTF8 (0) or PROP_STR_RICH (1).
const (
	FlagMonitored     = 0x08  // C: PROP_MONITORED
	FlagMultiSub      = 0x10  // C: PROP_MULTI_SUB
	FlagMultiNotify   = 0x20  // C: PROP_MULTI_NOTIFY
	FlagClippedValue  = 0x40  // C: PROP_CLIPPED_VALUE
	FlagMarked        = 0x80  // C: PROP_MARKED
	PropFlagDebugThis = 0x200 // C: PROP_DEBUG_THIS (prop_http POST debug=on)
	FlagXrefedOrigin  = 0x100 // C: PROP_XREFED_ORIGINATOR
	// C: PROP_PROXY_FOLLOW_SYMLINK (prop_i.h:298) — proxy node resolves
	// links on the remote end. Set by the PROP_PROXY branch of
	// prop_get_by_name and inherited by owned proxy props
	// (prop_proxy_make, prop_proxy.c:289-290).
	// C: PROP_PROXY_OWNED_BY_PROP (0x800) is not a Go flag — ownership is
	// tracked by proxyMeta.owner in pkg/prop/proxy.
	FlagProxyFollowSymlink = 0x400
	FlagHaveMore           = 0x1000 // C: PROP_HAVE_MORE
	FlagHaveMoreYes        = 0x2000 // C: PROP_HAVE_MORE_YES
)

// Link hardness constants (C: prop_link hardness parameter)
type PropertyType int

// Property types
const (
	PropTypeVoid PropertyType = iota
	PropTypeString
	PropTypeInt
	PropTypeFloat
	PropTypeDir
	PropTypeURI
	PropTypeProxy
	PropTypeProp   // C: PROP_PROP — live reference to another prop
	PropTypeZombie // C: PROP_ZOMBIE — destroyed, can never be changed again
)

// PropManager manages the property system
func (pm *PropManager) SetMulti(p *Prop) {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.flags&FlagMultiSub != 0 {
		p.mu.Unlock()
		return
	}
	p.flags |= FlagMultiSub
	isDir := p.propType == PropTypeDir
	p.mu.Unlock()

	if isDir {
		// C: prop_flood_flag_on_childs(p, PROP_MULTI_NOTIFY, 0)
		pm.propFloodFlagOnChilds(p, FlagMultiNotify, 0)
	}
}

// propClrMulti — C: prop_clr_multi (prop_core.c:2590-2598).
// Clears PROP_MULTI_SUB; if p is a DIR not itself MULTI_NOTIFY, floods
// PROP_MULTI_NOTIFY clear onto all children.
func (pm *PropManager) propClrMulti(p *Prop) {
	p.mu.Lock()
	p.flags &^= FlagMultiSub
	isDir := p.propType == PropTypeDir
	multiNotify := p.flags&FlagMultiNotify != 0
	p.mu.Unlock()

	if isDir && !multiNotify {
		// C: prop_flood_flag_on_childs(p, 0, PROP_MULTI_NOTIFY)
		pm.propFloodFlagOnChilds(p, 0, FlagMultiNotify)
	}
}

// propFloodFlag — C: prop_flood_flag (prop_core.c:2545-2554).
// p->hp_flags = (p->hp_flags | set) & ~clr, then recurses into children
// iff p is a DIR.
func (pm *PropManager) propFloodFlag(p *Prop, set, clr, depth int) {
	if depth > 100 {
		return
	}
	p.mu.Lock()
	p.flags = (p.flags | set) &^ clr
	isDir := p.propType == PropTypeDir
	children := make([]*Prop, len(p.children))
	copy(children, p.children)
	p.mu.Unlock()

	if isDir {
		for _, child := range children {
			pm.propFloodFlag(child, set, clr, depth+1)
		}
	}
}

// propFloodFlagOnChilds — C: prop_flood_flag_on_childs
// (prop_core.c:2559-2565) — applies prop_flood_flag to each child of p.
func (pm *PropManager) propFloodFlagOnChilds(p *Prop, set, clr int) {
	p.mu.RLock()
	children := make([]*Prop, len(p.children))
	copy(children, p.children)
	p.mu.RUnlock()
	for _, child := range children {
		pm.propFloodFlag(child, set, clr, 0)
	}
}

// floodFlagOnChildren recursively sets a flag on all children.
// C: prop_flood_flag_on_childs
// floodFlagOnChildren sets a flag on p itself AND recursively on all DIR descendants.
// C: prop_flood_flag (prop_core.c:2545-2554) — sets p->hp_flags, then recurses into children.
func (pm *PropManager) floodFlagOnChildren(p *Prop, flag int, depth int) {
	pm.propFloodFlag(p, flag, 0, depth)
}

// Create creates a property with a name
func (pm *PropManager) GetFlags(prop *Prop) int {
	if prop == nil {
		return 0
	}
	prop.mu.RLock()
	defer prop.mu.RUnlock()
	return prop.flags
}

// SetFlags sets property flags
func (pm *PropManager) SetFlags(prop *Prop, flags int) {
	if prop == nil {
		return
	}
	prop.mu.Lock()
	defer prop.mu.Unlock()
	prop.flags = flags
}

// DelChild deletes a child property
// DelChild removes a child from its parent and destroys it.
// C: prop_destroy_child (prop_core.c:2246) — calls prop_destroy0(child).
// If xref > 0 (can't destroy): fire DEL_CHILD, remove from parent, clear parent.
// If xref == 0 (destroyed): prop_destroy0 handles everything (DEL_CHILD, remove, ZOMBIE).
// Go: Delegates to destroyChild which matches C exactly.
