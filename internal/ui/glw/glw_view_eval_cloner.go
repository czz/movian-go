package glw

// C: src/ui/glw/glw_view_eval.c — canonical 1:1 port (core engine).

import (
	"unsafe"

	miscpkg "github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
)

// ---------------------------------------------------------------------------
// C: clone_eval (glw_view_eval.c:1229-1252)
func cloneEval(c *glwClone, scope *glwScope) {
	sc := c.cSc
	body := glwViewCloneChain(c.cW.glwRoot, sc.scClonerBody, nil)
	gc := c.cW.glwClass

	if gc.gcFreeze != nil {
		gc.gcFreeze(c.cW)
	}

	var n glwViewEvalContext
	n.scope = scope
	n.gr = c.cW.glwRoot
	n.w = c.cW
	n.sublist = &n.w.glwPropSubscriptions

	glwViewEvalBlock(body, &n, nil)
	glwViewFreeChain(n.gr, body)

	if gc.gcThaw != nil {
		gc.gcThaw(c.cW)
	}
}

// C: cloner_add_child0 (glw_view_eval.c:1414-1463)
func clonerAddChild0(sc *subCloner, p *propcore.Prop, before *propcore.Prop,
	parent *Glw, ei *errorinfoT, flags int) {
	var b *Glw
	gr := parent.glwRoot
	c := (*glwClone)(gr.grClonePool.PoolGet())

	// C: LIST_INSERT_HEAD(&sc->sc_clones, c, c_link)
	c.cLinkNext = sc.scClones.lhFirst
	if sc.scClones.lhFirst != nil {
		sc.scClones.lhFirst.cLinkPrev = &c.cLinkNext
	}
	sc.scClones.lhFirst = c
	c.cLinkPrev = &sc.scClones.lhFirst

	if before != nil {
		bb, _ := glwDeps.pm.TagGet(before, sc).(*glwClone)
		if bb == nil {
			panic("cloner_add_child0: no clone tag on before prop")
		}
		sc.scPositionsValid = false
		b = bb.cW
	} else {
		b = sc.scAnchor
		c.cPos = uint16(sc.scEntries)
	}
	c.cSc = sc
	c.cProp = glwDeps.pm.RefInc(p)

	sc.scEntries++

	c.cCloneRoot = glwDeps.pm.CreateRoot("")

	scope := glwScopeDup(sc.scSub.gpsScope,
		(1<<glwRootSelf)|(1<<glwRootParent)|(1<<glwRootClone))

	scope.gsRoots[glwRootSelf].P = glwDeps.pm.RefInc(c.cProp)
	scope.gsRoots[glwRootParent].P = glwDeps.pm.RefInc(sc.scOriginatingProp)
	scope.gsRoots[glwRootClone].P = glwDeps.pm.RefInc(c.cCloneRoot)

	c.cW = glwCreate(gr, sc.scClonerClass, parent, b, p,
		scope,
		sc.scClonerBody.file,
		sc.scClonerBody.line)
	c.cW.glwClone = c

	glwDeps.pm.TagSet(p, sc, c)

	glwSignalHandlerRegister(c.cW, cloneSigHandler, unsafe.Pointer(c))

	if flags&propAddSelected != 0 && parent.glwClass.gcSelectChild != nil {
		parent.glwClass.gcSelectChild(parent, c.cW, nil)
	}

	cloneEval(c, scope)

	glwScopeRelease(scope)
}

// C: prop_add_selected flag (prop.h) — 0x1
const propAddSelected = 0x1

// addSelectedFlag — C: gen_add_flags(c, parent) —
// (c == parent->hp_selected ? PROP_ADD_SELECTED : 0). Go's EventAddChild
// args are (child, parent); flag recomputed from selectedChild.
func addSelectedFlag(parent *propcore.Prop, child *propcore.Prop) int {
	if parent == nil || child == nil {
		return 0
	}
	if parent.GetSelectedChild() == child {
		return propAddSelected
	}
	return 0
}

// C: cloner_add_child (glw_view_eval.c:1466-1503)
func clonerAddChild(sc *subCloner, p *propcore.Prop, before *propcore.Prop,
	parent *Glw, ei *errorinfoT, flags int) {
	if sc.scClonerBody != nil {
		clonerAddChild0(sc, p, before, parent, ei, flags)
		return
	}

	// The cloner body has not been evaluated yet — pending list.
	var b *glwPropSubPending
	if before != nil {
		b, _ = glwDeps.pm.TagGet(before, &sc.scPending).(*glwPropSubPending)
	}

	gpsp := &glwPropSubPending{}
	gpsp.gpspProp = glwDeps.pm.RefInc(p)

	glwDeps.pm.TagSet(p, &sc.scPending, gpsp)

	if before != nil {
		// TAILQ_INSERT_BEFORE(b, gpsp, gpsp_link)
		gpsp.gpspLinkPrev = b.gpspLinkPrev
		gpsp.gpspLinkNext = b
		*b.gpspLinkPrev = gpsp
		b.gpspLinkPrev = &gpsp.gpspLinkNext
	} else {
		// TAILQ_INSERT_TAIL(&sc->sc_pending, gpsp, gpsp_link)
		gpsp.gpspLinkNext = nil
		gpsp.gpspLinkPrev = sc.scPending.tqhLast
		*sc.scPending.tqhLast = gpsp
		sc.scPending.tqhLast = &gpsp.gpspLinkNext
	}
	if flags&propAddSelected != 0 {
		sc.scPendingSelect = p
	}
}

// C: cloner_move_child0 (glw_view_eval.c:1506-1516)
func clonerMoveChild0(sc *subCloner, p *propcore.Prop, before *propcore.Prop,
	parent *Glw, ei *errorinfoT) {
	c, _ := glwDeps.pm.TagGet(p, sc).(*glwClone)
	var b *glwClone
	if before != nil {
		b, _ = glwDeps.pm.TagGet(before, sc).(*glwClone)
	}
	sc.scPositionsValid = false
	var bw *Glw
	if b != nil {
		bw = b.cW
	} else {
		bw = sc.scAnchor
	}
	glwMove(c.cW, bw)
}

// C: cloner_move_child (glw_view_eval.c:1519-1543)
func clonerMoveChild(sc *subCloner, p *propcore.Prop, before *propcore.Prop,
	parent *Glw, ei *errorinfoT) {
	if sc.scClonerBody != nil {
		clonerMoveChild0(sc, p, before, parent, ei)
		return
	}

	t, _ := glwDeps.pm.TagGet(p, &sc.scPending).(*glwPropSubPending)
	var b *glwPropSubPending
	if before != nil {
		b, _ = glwDeps.pm.TagGet(before, &sc.scPending).(*glwPropSubPending)
	}

	// TAILQ_REMOVE(&sc->sc_pending, t, gpsp_link)
	if t.gpspLinkNext != nil {
		t.gpspLinkNext.gpspLinkPrev = t.gpspLinkPrev
	} else {
		sc.scPending.tqhLast = t.gpspLinkPrev
	}
	*t.gpspLinkPrev = t.gpspLinkNext

	if b != nil {
		// TAILQ_INSERT_BEFORE(b, t, gpsp_link)
		t.gpspLinkPrev = b.gpspLinkPrev
		t.gpspLinkNext = b
		*b.gpspLinkPrev = t
		b.gpspLinkPrev = &t.gpspLinkNext
	} else {
		// TAILQ_INSERT_TAIL(&sc->sc_pending, t, gpsp_link)
		t.gpspLinkNext = nil
		t.gpspLinkPrev = sc.scPending.tqhLast
		*sc.scPending.tqhLast = t
		sc.scPending.tqhLast = &t.gpspLinkNext
	}
}

// C: cloner_del_child (glw_view_eval.c:1573-1602)
func clonerDelChild(gr *glwRoot, sc *subCloner, p *propcore.Prop, parent *Glw) {
	if c, _ := glwDeps.pm.TagClear(p, sc).(*glwClone); c != nil {
		sc.scEntries--
		w := c.cW
		if w != nil && w.glwParentLinkNext != nil {
			sc.scPositionsValid = false
		}
		cloneFree(gr, c)
		return
	}

	if sc.scPendingSelect == p {
		sc.scPendingSelect = nil
	}

	gpsp, _ := glwDeps.pm.TagClear(p, &sc.scPending).(*glwPropSubPending)
	if gpsp == nil {
		return
	}
	// assert(gpsp->gpsp_prop == p)
	glwDeps.pm.RefDec(p)
	// TAILQ_REMOVE(&sc->sc_pending, gpsp, gpsp_link)
	if gpsp.gpspLinkNext != nil {
		gpsp.gpspLinkNext.gpspLinkPrev = gpsp.gpspLinkPrev
	} else {
		sc.scPending.tqhLast = gpsp.gpspLinkPrev
	}
	*gpsp.gpspLinkPrev = gpsp.gpspLinkNext
}

// C: cloner_select_child (glw_view_eval.c:1605-1625)
func clonerSelectChild(sc *subCloner, p *propcore.Prop, parent *Glw, extra *propcore.Prop) {
	if p == nil {
		parent.glwClass.gcSelectChild(parent, nil, extra)
		return
	}
	if c, _ := glwDeps.pm.TagGet(p, sc).(*glwClone); c != nil {
		if parent.glwClass.gcSelectChild != nil {
			parent.glwClass.gcSelectChild(parent, c.cW, extra)
		}
		sc.scPendingSelect = nil
		return
	}
	sc.scPendingSelect = p
}

// C: cloner_suggest_focus (glw_view_eval.c:1628-1639)
func clonerSuggestFocus(sc *subCloner, p *propcore.Prop, parent *Glw) {
	if c, _ := glwDeps.pm.TagGet(p, sc).(*glwClone); c != nil {
		if parent.glwClass.gcSuggestFocus != nil {
			parent.glwClass.gcSuggestFocus(parent, c.cW)
		}
	}
}

// C: prop_callback_alloc_token (glw_view_eval.c:1644-1654)
func propCallbackAllocToken(gr *glwRoot, gps *GlwPropSub, typ tokenType) *Token {
	t := glwViewTokenAlloc(gr)
	t.typ = typ
	t.file = miscpkg.RstrDup(gps.gpsFile)
	t.line = int(gps.gpsLine)
	return t
}

// C: prop_callback_cloner (glw_view_eval.c:1659-1789)
func propCallbackCloner(opaque any, event propcore.EventType, args ...any) {
	// C: opaque is &sc->sc_sub; container recovered via container_of (derived)
	gps := opaque.(*GlwPropSub)
	sc := gps.derived.(*subCloner)
	var rpn *Token
	var t *Token
	gr := gps.gpsWidget.glwRoot

	switch event {
	case propcore.EventSetVoid, propcore.EventSetRString,
		propcore.EventSetCString, propcore.EventSetInt,
		propcore.EventSetFloat, propcore.EventSetProp:
		t = propCallbackAllocToken(gr, gps, tokenVoid)
		t.tPropsubr = gps
		rpn = gps.gpsRpn

	case propcore.EventSetDir:
		t = propCallbackAllocToken(gr, gps, tokenDirectory)
		t.tPropsubr = gps
		rpn = gps.gpsRpn

	case propcore.EventAddChild:
		p, _ := args[0].(*propcore.Prop)
		parent, _ := args[1].(*propcore.Prop)
		clonerAddChild(sc, p, nil, gps.gpsWidget, nil, addSelectedFlag(parent, p))

	case propcore.EventAddChildVector, propcore.EventAddChildVectorDirect:
		pv, _ := args[0].([]*propcore.Prop)
		for _, c := range pv {
			clonerAddChild(sc, c, nil, gps.gpsWidget, nil, 0)
		}

	case propcore.EventAddChildBefore:
		p, _ := args[0].(*propcore.Prop)
		parent, _ := args[1].(*propcore.Prop)
		p2, _ := args[2].(*propcore.Prop)
		clonerAddChild(sc, p, p2, gps.gpsWidget, nil, addSelectedFlag(parent, p))

	case propcore.EventAddChildVectorBefore:
		pv, _ := args[0].([]*propcore.Prop)
		p2, _ := args[1].(*propcore.Prop)
		for _, c := range pv {
			clonerAddChild(sc, c, p2, gps.gpsWidget, nil, 0)
		}

	case propcore.EventMoveChild:
		p, _ := args[0].(*propcore.Prop)
		p2, _ := args[2].(*propcore.Prop)
		clonerMoveChild(sc, p, p2, gps.gpsWidget, nil)

	case propcore.EventDelChild:
		p, _ := args[0].(*propcore.Prop)
		clonerDelChild(gr, sc, p, gps.gpsWidget)

	case propcore.EventSelectChild:
		p, _ := args[0].(*propcore.Prop)
		var extra *propcore.Prop
		if len(args) > 2 {
			extra, _ = args[2].(*propcore.Prop)
		}
		clonerSelectChild(sc, p, gps.gpsWidget, extra)

	case propcore.EventSuggestFocus:
		p, _ := args[0].(*propcore.Prop)
		clonerSuggestFocus(sc, p, gps.gpsWidget)

	case propcore.EventSetURI:
		t = propCallbackAllocToken(gr, gps, tokenURI)
		t.tPropsubr = gps
		t.tURITitle = miscpkg.RstrAllocStr(strFromArg(args[0]))
		t.tURI = miscpkg.RstrAllocStr(strFromArg(args[1]))
		rpn = gps.gpsRpn

	case propcore.EventHaveMoreChildsYes:
		sc.scHaveMore = true
		sc.scPendingMore = false
		clonerPaginationCheck(sc)

	case propcore.EventHaveMoreChildsNo:
		sc.scHaveMore = false
		sc.scPendingMore = false

	case propcore.EventReqNewChild, propcore.EventReqDeleteVector,
		propcore.EventDestroyed, propcore.EventExtEvent,
		propcore.EventSubscriptionMonitorActive, propcore.EventWantMoreChilds,
		propcore.EventReqMoveChild, propcore.EventValueProp,
		propcore.EventReqDelete:
		// ignored
	case propcore.EventTypePropNotify:
		// C: PROP_INVALID_EVENTS → abort
		panic("prop_callback_cloner: invalid event")
	}

	if t != nil {
		if gps.gpsToken != nil {
			glwViewTokenFree(gr, gps.gpsToken)
			gps.gpsToken = nil
		}
		gps.gpsToken = t
	}

	if rpn != nil {
		evalDynamic(gps.gpsWidget, rpn, nil, gps.gpsScope)
	}
}
