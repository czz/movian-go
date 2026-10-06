package prop

import "fmt"

// C: src/prop/prop_reorder.c — prop_reorder_t.
//
// pr_order is an ordered list of prop IDs (C: htsmsg_t list of str
// fields). Persistence goes through ReorderStoreSave/Load — wired by
// pkg/htsmsg's prop_reorder_bridge.go (prop can't import htsmsg:
// htsmsg→fileaccess→arch→prop import cycle).
type propReorder struct {
	dst    *Prop
	srcsub *Subscription
	dstsub *Subscription
	order  []string
	store  string
	pm     *PropManager
}

// C: PROP_REORDER_TAKE_DST_OWNERSHIP (prop_reorder.h)
const PropReorderTakeDstOwnership = 0x1

// ReorderStoreSave / ReorderStoreLoad — htsmsg_store_save/load hooks.
// Wired by pkg/htsmsg (prop_reorder_bridge.go).
// reorderGetID — C: get_id (prop_reorder.c:43)
// Walks hp_originator to the terminal and returns its hp_name.
func reorderGetID(pm *PropManager, p *Prop) string {
	t := pm.Follow(p)
	if t == nil {
		return ""
	}
	name := pm.GetName(t)
	pm.RefDec(t)
	return name
}

// saveOrder — C: save_order (prop_reorder.c:55)
func (pr *propReorder) saveOrder() {
	out := []string{}
	if pr.dst.GetPropType() == PropTypeDir {
		for _, p := range pr.dst.GetChildren() {
			out = append(out, reorderGetID(pr.pm, p))
		}
	}
	if pr.pm != nil && pr.pm.reorderSave != nil {
		pr.pm.reorderSave(pr.pm.store, pr.store, out)
	}
	pr.order = out
}

// reorderGetBefore — C: get_before (prop_reorder.c:76)
// "This is soo slow but works OK for small datasets"
func (pr *propReorder) reorderGetBefore(id string) *Prop {
	idx := -1
	for i, s := range pr.order {
		if s == id {
			idx = i
			break
		}
	}
	if idx < 0 || pr.dst.GetPropType() != PropTypeDir {
		return nil
	}
	for _, s := range pr.order[idx+1:] {
		for _, p := range pr.dst.GetChildren() {
			if reorderGetID(pr.pm, p) == s {
				return p
			}
		}
	}
	return nil
}

// reorderAddChild — C: add_child (prop_reorder.c:108)
func (pr *propReorder) addChild(p *Prop) {
	out := pr.pm.CreateRootEx("", true)
	pr.pm.TagSet(p, pr, out)
	pr.pm.Link(p, out, nil, false, false)
	pr.pm.SetParentEx(out, pr.dst,
		&SetParentOpaque{Before: pr.reorderGetBefore(reorderGetID(pr.pm, p))}, "")
}

// srcCb — C: src_cb (prop_reorder.c:121)
func (pr *propReorder) srcCb(opaque any, event EventType, args ...any) {
	switch event {
	case EventAddChild, EventAddChildBefore:
		pr.addChild(args[0].(*Prop))

	case EventAddChildVector, EventAddChildVectorDirect:
		// Go wire format is []*Prop (vecProps handles *PropVec too)
		for _, p := range vecProps(args) {
			pr.addChild(p)
		}

	case EventDelChild:
		p := args[0].(*Prop)
		if out, ok := pr.pm.TagClear(p, pr).(*Prop); ok {
			pr.pm.Destroy0(out)
		}

	case EventMoveChild, EventSetVoid, EventSetDir,
		EventHaveMoreChildsYes, EventHaveMoreChildsNo,
		EventWantMoreChilds, EventReqDeleteVector, EventReqDelete:
		// C: explicit no-ops

	default:
		// C: printf + abort()
		panic(fmt.Sprintf("prop_reorder: Cant handle event %d", event))
	}
}

// dstCb — C: dst_cb (prop_reorder.c:175)
func (pr *propReorder) dstCb(opaque any, event EventType, args ...any) {
	if event == EventReqMoveChild {
		p := args[0].(*Prop)
		var before *Prop
		if len(args) > 1 {
			before, _ = args[1].(*Prop)
		}
		// C: prop_move0(p, before, pr->pr_dstsub) — skipme = dstsub
		if parent := p.GetParent(); parent != nil {
			pr.pm.SetParentEx(p, parent,
				&SetParentOpaque{Before: before, Skipme: pr.dstsub}, "")
		}
		pr.saveOrder()
	}
}

// PropReorderCreate — C: prop_reorder_create (prop_reorder.c:202)
func (pm *PropManager) PropReorderCreate(dst, src *Prop, flags int, id string) {
	pr := &propReorder{pm: pm, store: id}

	if pm != nil && pm.reorderLoad != nil {
		pr.order = pm.reorderLoad(pm.store, id)
	}
	if pr.order == nil {
		pr.order = []string{}
	}

	if flags&PropReorderTakeDstOwnership != 0 {
		pr.dst = dst
	} else {
		pr.dst = pm.XrefAddref(dst)
	}

	// C: prop_subscribe(PROP_SUB_INTERNAL | PROP_SUB_DONTLOCK |
	//   PROP_SUB_TRACK_DESTROY, PROP_TAG_CALLBACK, cb, PROP_TAG_ROOT, src)
	// Go: INTERNAL/DONTLOCK are N/A (no global prop_mutex); TRACK_DESTROY
	// mirrors the flag.
	pr.srcsub = pm.Subscribe(src, pr.srcCb, pr, SubFlagTrackDestroy, SubFlagInternal|SubFlagDontLock)
	pr.dstsub = pm.Subscribe(dst, pr.dstCb, pr, SubFlagTrackDestroy, SubFlagInternal|SubFlagDontLock)
}
