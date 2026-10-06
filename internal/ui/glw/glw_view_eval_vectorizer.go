package glw

// C: src/ui/glw/glw_view_eval.c — canonical 1:1 port (core engine).

import (
	eventpkg "github.com/czz/movian-go/internal/event"
	miscpkg "github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
	tracepkg "github.com/czz/movian-go/internal/trace"
)

// C: prop_callback_value (glw_view_eval.c:1792-1895)
func propCallbackValue(opaque any, event propcore.EventType, args ...any) {
	gps := opaque.(*GlwPropSub)
	gr := gps.gpsWidget.glwRoot
	var rpn *Token
	var t *Token

	switch event {
	case propcore.EventSetVoid, propcore.EventSetDir:
		t = propCallbackAllocToken(gr, gps, tokenVoid)
		t.tPropsubr = gps
		rpn = gps.gpsRpn

	case propcore.EventSetRString:
		t = propCallbackAllocToken(gr, gps, tokenRstring)
		t.tPropsubr = gps
		t.tRstring = miscpkg.RstrAllocStr(strFromArg(args[0]))
		t.tRstrType = intFromArg(args[1])
		rpn = gps.gpsRpn

	case propcore.EventSetCString:
		t = propCallbackAllocToken(gr, gps, tokenCstring)
		t.tPropsubr = gps
		t.tCstring = strFromArg(args[0])
		rpn = gps.gpsRpn

	case propcore.EventSetInt:
		t = propCallbackAllocToken(gr, gps, tokenInt)
		t.tPropsubr = gps
		t.tInt = intFromArg(args[0])
		rpn = gps.gpsRpn

	case propcore.EventSetFloat:
		t = propCallbackAllocToken(gr, gps, tokenFloat)
		t.tPropsubr = gps
		t.tFloat = floatFromArg(args[0])
		rpn = gps.gpsRpn

	case propcore.EventSetURI:
		t = propCallbackAllocToken(gr, gps, tokenURI)
		t.tPropsubr = gps
		t.tURITitle = miscpkg.RstrAllocStr(strFromArg(args[0]))
		t.tURI = miscpkg.RstrAllocStr(strFromArg(args[1]))
		rpn = gps.gpsRpn

	case propcore.EventSetProp:
		t = propCallbackAllocToken(gr, gps, tokenPropertyRef)
		t.tPropsubr = gps
		rpn = gps.gpsRpn
		if p, ok := args[0].(*propcore.Prop); ok {
			t.tProp = glwDeps.pm.RefInc(p)
		}

	case propcore.EventTypePropNotify:
		panic("prop_callback_value: invalid event")
	}

	if t != nil {
		if gps.gpsToken != nil {
			glwViewTokenFree(gps.gpsWidget.glwRoot, gps.gpsToken)
			gps.gpsToken = nil
		}
		gps.gpsToken = t
	}

	if rpn != nil {
		evalDynamic(gps.gpsWidget, rpn, nil, gps.gpsScope)
	}
}

// C: prop_callback_counter (glw_view_eval.c:1898-1975)
func propCallbackCounter(opaque any, event propcore.EventType, args ...any) {
	// C: opaque is &sc->sc_sub; container recovered via container_of (derived)
	gps := opaque.(*GlwPropSub)
	sc := gps.derived.(*subCounter)
	gr := gps.gpsWidget.glwRoot
	var rpn *Token

	switch event {
	case propcore.EventSetVoid, propcore.EventSetRString,
		propcore.EventSetCString, propcore.EventSetInt,
		propcore.EventSetFloat, propcore.EventSetDir,
		propcore.EventSetURI, propcore.EventSetProp:
		sc.scEntries = 0

	case propcore.EventAddChild, propcore.EventAddChildBefore:
		sc.scEntries++

	case propcore.EventAddChildVector, propcore.EventAddChildVectorBefore,
		propcore.EventAddChildVectorDirect:
		pv, _ := args[0].([]*propcore.Prop)
		sc.scEntries += len(pv)

	case propcore.EventDelChild:
		sc.scEntries--

	case propcore.EventTypePropNotify:
		panic("prop_callback_counter: invalid event")
	}

	t := propCallbackAllocToken(gr, gps, tokenInt)
	t.tPropsubr = gps
	t.tInt = sc.scEntries

	rpn = gps.gpsRpn

	if gps.gpsToken != nil {
		glwViewTokenFree(gr, gps.gpsToken)
	}
	gps.gpsToken = t

	if rpn != nil {
		evalDynamic(gps.gpsWidget, rpn, nil, gps.gpsScope)
	}
}

// ---------------------------------------------------------------------------
// C: ve_cb (glw_view_eval.c:1978-2177)
func veCb(opaque any, event propcore.EventType, args ...any) {
	ve := opaque.(*vectorizerElement)
	gps := &ve.veSv.svSub
	gr := gps.gpsWidget.glwRoot
	var rpn *Token
	var t *Token

	switch event {
	case propcore.EventSetVoid, propcore.EventSetDir, propcore.EventSetProp:
		t = propCallbackAllocToken(gr, gps, tokenVoid)
		rpn = gps.gpsRpn

	case propcore.EventSetRString:
		t = propCallbackAllocToken(gr, gps, tokenRstring)
		t.tRstring = miscpkg.RstrAllocStr(strFromArg(args[0]))
		t.tRstrType = intFromArg(args[1])
		rpn = gps.gpsRpn

	case propcore.EventSetCString:
		t = propCallbackAllocToken(gr, gps, tokenCstring)
		t.tCstring = strFromArg(args[0])
		rpn = gps.gpsRpn

	case propcore.EventSetInt:
		t = propCallbackAllocToken(gr, gps, tokenInt)
		t.tInt = intFromArg(args[0])
		rpn = gps.gpsRpn

	case propcore.EventSetFloat:
		t = propCallbackAllocToken(gr, gps, tokenFloat)
		t.tFloat = floatFromArg(args[0])
		rpn = gps.gpsRpn

	case propcore.EventSetURI:
		t = propCallbackAllocToken(gr, gps, tokenURI)
		t.tURITitle = miscpkg.RstrAllocStr(strFromArg(args[0]))
		t.tURI = miscpkg.RstrAllocStr(strFromArg(args[1]))
		rpn = gps.gpsRpn

	case propcore.EventTypePropNotify:
		panic("ve_cb: invalid event")
	}

	if t != nil {
		if ve.veToken == nil {
			panic("ve_cb: ve_token is NULL")
		}
		prev := veTAILQPrev(ve)

		t.next = ve.veToken.next

		if prev == nil {
			// First element — TOKEN element's child pointer points to us
			ve.veSv.svSub.gpsToken.child = t
		} else {
			prev.veToken.next = t
		}

		glwViewTokenFree(gr, ve.veToken)
		ve.veToken = t

		if ve.veSv.svSelected == ve {
			t.tFlags |= tokenFSelected
		} else {
			t.tFlags &^= tokenFSelected
		}
	}

	if rpn != nil {
		evalDynamic(gps.gpsWidget, rpn, nil, gps.gpsScope)
	}
}

// veTAILQPrev — C: TAILQ_PREV(ve, vectorizer_element_queue, ve_link)
func veTAILQPrev(ve *vectorizerElement) *vectorizerElement {
	if ve.veSv == nil {
		return nil
	}
	head := &ve.veSv.svElements
	if head.tqhFirst == ve {
		return nil
	}
	var prev *vectorizerElement
	for c := head.tqhFirst; c != nil && c != ve; c = c.veLinkNext {
		prev = c
	}
	return prev
}

// C: vectorizer_add_element (glw_view_eval.c:2180-2215)
func vectorizerAddElement(sv *subVectorizer, p *propcore.Prop,
	before *propcore.Prop, gr *glwRoot, flags int) {
	ve := &vectorizerElement{}
	gps := &sv.svSub
	ve.veSv = sv
	ve.veProp = glwDeps.pm.RefInc(p)

	if flags&propAddSelected != 0 {
		if sv.svSelected != nil {
			sv.svSelected.veToken.tFlags &^= tokenFSelected
		}
		sv.svSelected = ve
	}

	ve.veToken = propCallbackAllocToken(gr, &sv.svSub, tokenVoid)

	if before != nil {
		b, _ := glwDeps.pm.TagGet(before, sv).(*vectorizerElement)
		// TAILQ_INSERT_BEFORE(b, ve, ve_link)
		ve.veLinkPrev = b.veLinkPrev
		ve.veLinkNext = b
		*b.veLinkPrev = ve
		b.veLinkPrev = &ve.veLinkNext
		ve.veToken.next = b.veToken
	} else {
		// TAILQ_INSERT_TAIL(&sv->sv_elements, ve, ve_link)
		ve.veLinkNext = nil
		ve.veLinkPrev = sv.svElements.tqhLast
		*sv.svElements.tqhLast = ve
		sv.svElements.tqhLast = &ve.veLinkNext
	}

	prev := veTAILQPrev(ve)

	if prev == nil {
		gps.gpsToken.child = ve.veToken
	} else {
		prev.veToken.next = ve.veToken
	}

	glwDeps.pm.TagSet(p, sv, ve)

	ve.veSub = glwPropSubscribeTags(0,
		propTagCallback, veCb, ve,
		propTagCourier, gr.grCourier,
		propTagRoot, p,
	)
}

// C: vectorizer_move_element (glw_view_eval.c:2218-2270 approx)
func vectorizerMoveElement(sv *subVectorizer, p *propcore.Prop,
	before *propcore.Prop, gr *glwRoot) {
	gps := &sv.svSub
	ve, _ := glwDeps.pm.TagGet(p, sv).(*vectorizerElement)
	if ve == nil {
		panic("vectorizer_move_element: no element")
	}

	prev := veTAILQPrev(ve)

	if prev == nil {
		gps.gpsToken.child = ve.veToken.next
	} else {
		prev.veToken.next = ve.veToken.next
	}

	if before != nil {
		b, _ := glwDeps.pm.TagGet(before, sv).(*vectorizerElement)
		// TAILQ_REMOVE then INSERT_BEFORE
		if ve.veLinkNext != nil {
			ve.veLinkNext.veLinkPrev = ve.veLinkPrev
		} else {
			sv.svElements.tqhLast = ve.veLinkPrev
		}
		*ve.veLinkPrev = ve.veLinkNext
		ve.veLinkPrev = b.veLinkPrev
		ve.veLinkNext = b
		*b.veLinkPrev = ve
		b.veLinkPrev = &ve.veLinkNext
		ve.veToken.next = b.veToken
	} else {
		if ve.veLinkNext != nil {
			ve.veLinkNext.veLinkPrev = ve.veLinkPrev
		} else {
			sv.svElements.tqhLast = ve.veLinkPrev
		}
		*ve.veLinkPrev = ve.veLinkNext
		ve.veLinkNext = nil
		ve.veLinkPrev = sv.svElements.tqhLast
		*sv.svElements.tqhLast = ve
		sv.svElements.tqhLast = &ve.veLinkNext
	}

	prev = veTAILQPrev(ve)

	if prev == nil {
		gps.gpsToken.child = ve.veToken
	} else {
		prev.veToken.next = ve.veToken
	}

	rpn := gps.gpsRpn
	if rpn != nil {
		evalDynamic(gps.gpsWidget, rpn, nil, gps.gpsScope)
	}
}

// C: vectorizer_del_element (glw_view_eval.c:2180-2215 region)
func vectorizerDelElement(sv *subVectorizer, p *propcore.Prop, gr *glwRoot) {
	ve, _ := glwDeps.pm.TagClear(p, sv).(*vectorizerElement)
	gps := &sv.svSub
	if ve == nil {
		panic("vectorizer_del_element: no element")
	}

	prev := veTAILQPrev(ve)

	if prev == nil {
		gps.gpsToken.child = ve.veToken.next
	} else {
		prev.veToken.next = ve.veToken.next
	}

	if sv.svSelected == ve {
		sv.svSelected = nil
	}

	glwDeps.pm.Unsubscribe(ve.veSub)
	glwViewTokenFree(gr, ve.veToken)
	glwDeps.pm.RefDec(ve.veProp)
	// TAILQ_REMOVE(&sv->sv_elements, ve, ve_link)
	if ve.veLinkNext != nil {
		ve.veLinkNext.veLinkPrev = ve.veLinkPrev
	} else {
		sv.svElements.tqhLast = ve.veLinkPrev
	}
	*ve.veLinkPrev = ve.veLinkNext

	rpn := gps.gpsRpn
	if rpn != nil {
		evalDynamic(gps.gpsWidget, rpn, nil, gps.gpsScope)
	}
}

// C: vectorizer_select_element
func vectorizerSelectElement(sv *subVectorizer, p *propcore.Prop, gr *glwRoot) {
	gps := &sv.svSub
	ve, _ := glwDeps.pm.TagGet(p, sv).(*vectorizerElement)

	if sv.svSelected == ve {
		return
	}
	if sv.svSelected != nil {
		sv.svSelected.veToken.tFlags &^= tokenFSelected
	}
	sv.svSelected = ve
	if ve != nil {
		ve.veToken.tFlags |= tokenFSelected
	}

	rpn := gps.gpsRpn
	if rpn != nil {
		evalDynamic(gps.gpsWidget, rpn, nil, gps.gpsScope)
	}
}

// C: prop_callback_vectorizer (glw_view_eval.c:2240-2386)
func propCallbackVectorizer(opaque any, event propcore.EventType, args ...any) {
	// C: opaque is &sv->sv_sub; container recovered via container_of (derived)
	gps := opaque.(*GlwPropSub)
	sv := gps.derived.(*subVectorizer)
	var rpn *Token
	var t *Token
	gr := gps.gpsWidget.glwRoot

	switch event {
	case propcore.EventSetVoid, propcore.EventSetProp:
		vectorizerClean(gr, sv)
		t = propCallbackAllocToken(gr, gps, tokenVoid)
		t.tPropsubr = gps
		rpn = gps.gpsRpn

	case propcore.EventSetRString:
		vectorizerClean(gr, sv)
		t = propCallbackAllocToken(gr, gps, tokenRstring)
		t.tPropsubr = gps
		t.tRstring = miscpkg.RstrAllocStr(strFromArg(args[0]))
		t.tRstrType = intFromArg(args[1])
		rpn = gps.gpsRpn

	case propcore.EventSetCString:
		vectorizerClean(gr, sv)
		t = propCallbackAllocToken(gr, gps, tokenCstring)
		t.tPropsubr = gps
		t.tCstring = strFromArg(args[0])
		rpn = gps.gpsRpn

	case propcore.EventSetInt:
		vectorizerClean(gr, sv)
		t = propCallbackAllocToken(gr, gps, tokenInt)
		t.tPropsubr = gps
		t.tInt = intFromArg(args[0])
		rpn = gps.gpsRpn

	case propcore.EventSetFloat:
		vectorizerClean(gr, sv)
		t = propCallbackAllocToken(gr, gps, tokenFloat)
		t.tPropsubr = gps
		t.tFloat = floatFromArg(args[0])
		rpn = gps.gpsRpn

	case propcore.EventSetURI:
		vectorizerClean(gr, sv)
		t = propCallbackAllocToken(gr, gps, tokenURI)
		t.tPropsubr = gps
		t.tURITitle = miscpkg.RstrAllocStr(strFromArg(args[0]))
		t.tURI = miscpkg.RstrAllocStr(strFromArg(args[1]))
		rpn = gps.gpsRpn

	case propcore.EventSetDir:
		t = propCallbackAllocToken(gr, gps, tokenVector)
		t.tPropsubr = gps

	case propcore.EventAddChild:
		p, _ := args[0].(*propcore.Prop)
		parent, _ := args[1].(*propcore.Prop)
		vectorizerAddElement(sv, p, nil, gr, addSelectedFlag(parent, p))

	case propcore.EventAddChildVector, propcore.EventAddChildVectorDirect:
		pv, _ := args[0].([]*propcore.Prop)
		for _, c := range pv {
			vectorizerAddElement(sv, c, nil, gr, 0)
		}

	case propcore.EventAddChildBefore:
		p, _ := args[0].(*propcore.Prop)
		parent, _ := args[1].(*propcore.Prop)
		p2, _ := args[2].(*propcore.Prop)
		vectorizerAddElement(sv, p, p2, gr, addSelectedFlag(parent, p))

	case propcore.EventAddChildVectorBefore:
		pv, _ := args[0].([]*propcore.Prop)
		p2, _ := args[1].(*propcore.Prop)
		for _, c := range pv {
			vectorizerAddElement(sv, c, p2, gr, 0)
		}

	case propcore.EventMoveChild:
		p, _ := args[0].(*propcore.Prop)
		p2, _ := args[2].(*propcore.Prop)
		vectorizerMoveElement(sv, p, p2, gr)

	case propcore.EventDelChild:
		p, _ := args[0].(*propcore.Prop)
		vectorizerDelElement(sv, p, gr)

	case propcore.EventSelectChild:
		p, _ := args[0].(*propcore.Prop)
		vectorizerSelectElement(sv, p, gr)

	case propcore.EventTypePropNotify:
		panic("prop_callback_vectorizer: invalid event")
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

// C: prop_callback_event_injector (glw_view_eval.c:2389-2410)
func propCallbackEventInjector(opaque any, event propcore.EventType, args ...any) {
	gps := opaque.(*GlwPropSub)
	if event == propcore.EventExtEvent {
		var e *eventpkg.Event
		if len(args) > 0 {
			e, _ = args[0].(*eventpkg.Event)
		}
		if e == nil {
			return
		}
		if glwSendEvent2(gps.gpsWidget, e) {
			glwDeps.ts.Trace(tracepkg.TRACE_DEBUG, "GLW", "Event '%s' intercepted by widget '%s' (start)",
				eventSprint(e), glwGetName(gps.gpsWidget))
		} else {
			glwEventToWidget(gps.gpsWidget, e)
		}
	}
}
