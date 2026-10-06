package glw

// C: src/ui/glw/glw_view_eval.c — builtin function table (funcvec) and all
// glwf_* handlers. Ported 1:1 from the canonical C implementation.

import (
	"strconv"
	"unsafe"

	eventpkg "github.com/czz/movian-go/internal/event"
	miscpkg "github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
)

// ---------------------------------------------------------------------------
// C: glw_captured_block_t (glw_view_eval.c:3249-3291)
type glwCapturedBlock struct {
	block *Token
	scope *glwScope
}

// C: glw_captured_block_release (glw_view_eval.c:3259)
func glwCapturedBlockRelease(gr *glwRoot, gcb *glwCapturedBlock) {
	glwViewFreeChain(gr, gcb.block)
	glwScopeRelease(gcb.scope)
}

// C: glw_captured_block_init (glw_view_eval.c:3270)
func glwCapturedBlockSetup(gcb *glwCapturedBlock, ec *glwViewEvalContext, block *Token) {
	gcb.block = glwViewCloneChain(ec.gr, block, nil)
	gcb.scope = glwScopeRetain(ec.scope)
}

// C: glw_captured_block_prepare_invoke (glw_view_eval.c:3283)
func glwCapturedBlockPrepareInvoke(ec *glwViewEvalContext, gcb *glwCapturedBlock) {
	ec.scope = gcb.scope
}

// ---------------------------------------------------------------------------
// C: glw_event_map_eval_block_t (glw_view_eval.c:3293-3356)
type glwEventMapEvalBlock struct {
	Map     GlwEventMap
	capture glwCapturedBlock
}

// C: glw_event_map_eval_block_fire (glw_view_eval.c:3303)
func glwEventMapEvalBlockFire(w *Glw, gem *GlwEventMap, src *eventpkg.Event) {
	b := (*glwEventMapEvalBlock)(unsafe.Pointer(gem))
	var n glwViewEvalContext
	var l glwPropSubSlist

	n.scope = glwScopeDup(b.capture.scope, 0)
	n.scope.gsEvent.Release()
	n.scope.gsEvent = src
	src.AddRef()

	n.gr = w.glwRoot
	n.w = w
	n.passiveSubscriptions = 1
	n.sublist = &l

	body := glwViewCloneChain(n.gr, b.capture.block, nil)
	glwViewEvalStaticBlock(body, &n)
	glwPropSubscriptionDestroyList(w.glwRoot, &l)
	glwViewFreeChain(n.gr, body)
	glwScopeRelease(n.scope)
}

// C: glw_event_map_eval_block_dtor (glw_view_eval.c:3333)
func glwEventMapEvalBlockDtor(gr *glwRoot, gem *GlwEventMap) {
	b := (*glwEventMapEvalBlock)(unsafe.Pointer(gem))
	glwCapturedBlockRelease(gr, &b.capture)
}

// C: glw_event_map_eval_block_create (glw_view_eval.c:3345)
func glwEventMapEvalBlockCreate(ec *glwViewEvalContext, block *Token) *GlwEventMap {
	b := &glwEventMapEvalBlock{}
	glwCapturedBlockSetup(&b.capture, ec, block)
	b.Map.gemDtor = glwEventMapEvalBlockDtor
	b.Map.gemFire = glwEventMapEvalBlockFire
	return &b.Map
}

// C: glwf_onEvent (glw_view_eval.c:3362)
func glwfOnEvent(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	var a, b, c, d, e *Token
	if argc > 0 {
		a = argv[0]
	}
	if argc > 1 {
		b = argv[1]
	}
	if argc > 2 {
		c = argv[2]
	}
	if argc > 3 {
		d = argv[3]
	}
	if argc > 4 {
		e = argv[4]
	}
	enabled := true
	final := true
	early := false
	w := ec.w

	if w == nil {
		return glwViewSeterr(ec.ei, self, "Events can not be mapped in this scope")
	}
	if a == nil || b == nil {
		return glwViewSeterr(ec.ei, self, "Missing operands")
	}
	if c != nil {
		if c = tokenResolve(ec, c); c == nil {
			return -1
		}
		enabled = token2bool(c)
	}
	if d != nil {
		if d = tokenResolve(ec, d); d == nil {
			return -1
		}
		final = token2bool(d)
	}
	if e != nil {
		if e = tokenResolve(ec, e); e == nil {
			return -1
		}
		early = token2bool(e)
	}

	filter := token2rstr(a)
	if filter == nil {
		return glwViewSeterr(ec.ei, a, "Invalid source event type")
	}

	if enabled {
		var gem *GlwEventMap
		switch b.typ {
		case tokenGem:
			b.typ = tokenNop // steal gem pointer
			gem = b.tGem
		case tokenEvent:
			b.tEvent.AddRef()
			gem = glwEventMapExternalCreate(b.tEvent)
		case tokenBlock:
			gem = glwEventMapEvalBlockCreate(ec, b.child)
		case tokenVoid:
			goto disable
		default:
			miscpkg.RstrRelease(filter)
			return glwViewSeterr(ec.ei, a, "onEvent: Second arg is invalid")
		}

		gem.gemFilter = filter
		if final {
			gem.gemFinal = 1
		} else {
			gem.gemFinal = 0
		}
		if self.tExtraInt == 0 {
			w.glwRoot.grGemIdTally++
			self.tExtraInt = w.glwRoot.grGemIdTally
		}
		gem.gemID = self.tExtraInt
		if early {
			gem.gemEarly = 1
		} else {
			gem.gemEarly = 0
		}
		gem.gemFile = miscpkg.RstrDup(self.file)
		gem.gemLine = self.line
		glwEventMapAdd(w, gem)
		return 0
	}
disable:
	if self.tExtraInt != 0 {
		glwEventMapRemoveByID(w, self.tExtraInt)
		self.tExtraInt = 0
	}
	miscpkg.RstrRelease(filter)
	return 0
}

// C: glw_oninactivity_extra_t (glw_view_eval.c:3448)
type glwOnInactivityExtra struct {
	trigged bool
	capture glwCapturedBlock
}

// C: glwf_onInactivity_dtor (glw_view_eval.c:3458)
func glwfOnInactivityDtor(gr *glwRoot, self *Token) {
	goe, _ := self.tExtra.(*glwOnInactivityExtra)
	if goe == nil {
		return
	}
	glwCapturedBlockRelease(gr, &goe.capture)
}

// C: glwf_onInactivity (glw_view_eval.c:3471)
func glwfOnInactivity(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := argv[0]
	b := argv[1]
	w := ec.w
	gr := w.glwRoot

	if a = tokenResolve(ec, a); a == nil {
		return -1
	}
	if b.typ != tokenBlock {
		return glwViewSeterr(ec.ei, a, "Second argument is not a block")
	}

	goe, _ := self.tExtra.(*glwOnInactivityExtra)
	if goe == nil {
		goe = &glwOnInactivityExtra{}
		self.tExtra = goe
		glwCapturedBlockSetup(&goe.capture, ec, b)
	}

	timeout := int64(token2int(ec, a)) * 1000000
	if timeout == 0 {
		goe.trigged = false
		return 0
	}

	if gr.grFrameStart >= gr.grLastActivityAt+timeout {
		glwNeedRefresh(gr, 0)
		if !goe.trigged {
			goe.trigged = true
			var n glwViewEvalContext
			var l glwPropSubSlist
			glwCapturedBlockPrepareInvoke(&n, &goe.capture)
			n.gr = w.glwRoot
			n.w = w
			n.passiveSubscriptions = 1
			n.sublist = &l
			body := glwViewCloneChain(gr, goe.capture.block, nil)
			glwViewEvalBlock(body, &n, nil)
			glwPropSubscriptionDestroyList(gr, &l)
			glwViewFreeChain(gr, body)
			glwNeedRefresh(gr, 0)
		}
	} else {
		goe.trigged = false
		glwScheduleRefresh(gr, gr.grLastActivityAt+timeout)
	}
	ec.dynamicEval |= GLW_VIEW_EVAL_LAYOUT
	return 0
}

// C: glwf_deliverRef (glw_view_eval.c:3639)
func glwfDeliverRef(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := resolvePropertyName2(ec, argv[0])
	if a == nil {
		return -1
	}
	b := resolvePropertyName2(ec, argv[1])
	if b == nil {
		return -1
	}
	if a.typ != tokenPropertyRef {
		return glwViewSeterr(ec.ei, a, "deliverRef(): First argument is not a property")
	}
	if b.typ != tokenPropertyRef {
		return glwViewSeterr(ec.ei, a, "deliverRef(): Second argument is not a property")
	}
	r := evalAlloc(self, ec, tokenGem)
	r.tGem = glwEventMapProprefCreate(b.tProp, a.tProp)
	evalPush(ec, r)
	return 0
}

// C: glwf_deliverEvent (glw_view_eval.c:3673)
func glwfDeliverEvent(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	var event *eventpkg.Event

	if argc < 1 || argc > 2 {
		return glwViewSeterr(ec.ei, self, "deliverEvent(): Invalid number of args")
	}
	a := resolvePropertyName2(ec, argv[0])
	if a == nil {
		return -1
	}
	if a.typ != tokenPropertyRef {
		return glwViewSeterr(ec.ei, a, "deliverEvent(): First argument is not a property")
	}
	if argc == 2 {
		b := tokenResolve(ec, argv[1])
		if b == nil {
			return -1
		}
		switch b.typ {
		case tokenIdentifier, tokenRstring, tokenURI:
			event = glwDeps.em.CreateActionStr(miscpkg.RstrGet(b.tRstring))
			event.Nav = glwDeps.pm.RefInc(ec.w.glwRoot.grPropNav)
		case tokenCstring:
			event = glwDeps.em.CreateActionStr(b.tCstring)
			event.Nav = glwDeps.pm.RefInc(ec.w.glwRoot.grPropNav)
		case tokenInt:
			event = glwDeps.em.CreateActionStr(strconv.Itoa(b.tInt))
			event.Nav = glwDeps.pm.RefInc(ec.w.glwRoot.grPropNav)
		case tokenEvent:
			event = b.tEvent
			event.AddRef()
		}
	}
	r := evalAlloc(self, ec, tokenGem)
	r.tGem = glwEventMapDeliverEventCreate(a.tProp, event)
	evalPush(ec, r)
	return 0
}

// C: glwf_playTrackFromSource (glw_view_eval.c:3740)
func glwfPlayTrackFromSource(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	dontskip := 0
	a := resolvePropertyName2(ec, argv[0])
	if a == nil {
		return -1
	}
	b := resolvePropertyName2(ec, argv[1])
	if b == nil {
		return -1
	}
	if argc > 2 {
		c := tokenResolve(ec, argv[2])
		if c == nil {
			return -1
		}
		if token2bool(c) {
			dontskip = 1
		}
	}
	r := evalAlloc(self, ec, tokenGem)
	r.tGem = glwEventMapPlayTrackCreate(a.tProp, b.tProp, dontskip)
	evalPush(ec, r)
	return 0
}

// C: glwf_enqueueTrack (glw_view_eval.c:3768)
func glwfEnqueueTrack(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := resolvePropertyName2(ec, argv[0])
	if a == nil {
		return -1
	}
	r := evalAlloc(self, ec, tokenGem)
	r.tGem = glwEventMapPlayTrackCreate(a.tProp, nil, 0)
	evalPush(ec, r)
	return 0
}

// C: glwf_selectTrack (glw_view_eval.c:3786)
func glwfSelectTrack(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint, evtype eventpkg.EventType) int {
	a := tokenResolve(ec, argv[0])
	var str string
	var r *Token

	if a != nil && a.typ == tokenRstring {
		str = miscpkg.RstrGet(a.tRstring)
	} else if a != nil && a.typ == tokenInt {
		str = strconv.Itoa(a.tInt)
	} else {
		r = evalAlloc(self, ec, tokenVoid)
		evalPush(ec, r)
		return 0
	}
	r = evalAlloc(self, ec, tokenEvent)
	r.tEvent = glwDeps.em.CreateSelectTrack(str, evtype, true).AsEvent()
	r.tEvent.Nav = glwDeps.pm.RefInc(ec.w.glwRoot.grPropNav)
	evalPush(ec, r)
	return 0
}

func glwfSelectAudioTrack(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	return glwfSelectTrack(ec, self, argv, argc, eventpkg.EVENT_SELECT_AUDIO_TRACK)
}

func glwfSelectSubtitleTrack(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	return glwfSelectTrack(ec, self, argv, argc, eventpkg.EVENT_SELECT_SUBTITLE_TRACK)
}

// C: glwf_fireEvent (glw_view_eval.c:3831)
func glwfFireEvent(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := argv[0]
	switch a.typ {
	case tokenGem:
		a.typ = tokenNop
		gem := a.tGem
		gem.gemFire(ec.w, gem, nil)
		return 0
	case tokenEvent:
		glwEventToWidget(ec.w, a.tEvent)
		return 0
	default:
		return glwViewSeterr(ec.ei, a, "fireEvent(): Invalid first argument (%s)", token2name(a))
	}
}

// C: glwf_currentEvent (glw_view_eval.c:3859)
func glwfCurrentEvent(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	s, ok := tokenAsString(argv[0])
	var r *Token
	e := ec.scope.gsEvent
	if ok && e != nil {
		if s == "screenX" && e.Flags&eventpkg.EventScreenPosition != 0 {
			r = evalAlloc(self, ec, tokenFloat)
			r.tFloat = e.ScreenX
		} else if s == "screenY" && e.Flags&eventpkg.EventScreenPosition != 0 {
			r = evalAlloc(self, ec, tokenFloat)
			r.tFloat = e.ScreenY
		} else if s == "prop" && e.Type == eventpkg.EVENT_PROP_ACTION {
			epa := (*eventpkg.EventPropAction)(unsafe.Pointer(e))
			r = evalAlloc(self, ec, tokenPropertyRef)
			r.tProp = glwDeps.pm.RefInc(epa.P.(*propcore.Prop))
		}
	}
	if r == nil {
		r = evalAlloc(self, ec, tokenVoid)
	}
	evalPush(ec, r)
	return 0
}

// C: glwf_targetedEvent (glw_view_eval.c:3901)
func glwfTargetedEvent(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := argv[0]
	b := argv[1]
	var action eventpkg.ActionType
	uc := 0

	if a = tokenResolve(ec, a); a == nil {
		return -1
	}
	if a.typ != tokenRstring {
		return glwViewSeterr(ec.ei, a, "targetedEvent(): First argument is not a string")
	}
	if b.typ == tokenIdentifier {
		action = glwDeps.em.ActionStr2Code(miscpkg.RstrGet(b.tRstring))
		if action < 0 {
			return glwViewSeterr(ec.ei, b, "targetedEvent(): Invalid action")
		}
	} else if b.typ == tokenRstring {
		str := miscpkg.RstrGet(b.tRstring)
		sb := []byte(str)
		uc = miscpkg.Utf8Get(&sb)
	} else {
		return glwViewSeterr(ec.ei, b, "targetedEvent(): Invalid second argument")
	}
	r := evalAlloc(self, ec, tokenGem)
	r.tGem = glwEventMapInternalCreate(miscpkg.RstrGet(a.tRstring), action, uc)
	evalPush(ec, r)
	return 0
}

// C: glwf_event (glw_view_eval.c:3943)
func glwfEvent(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := tokenResolve(ec, argv[0])
	if a == nil {
		return -1
	}
	action, ok := tokenAsString(a)
	var r *Token
	if !ok {
		r = evalAlloc(self, ec, tokenVoid)
	} else {
		r = evalAlloc(self, ec, tokenEvent)
		r.tEvent = glwDeps.em.CreateActionStr(action)
		r.tEvent.Nav = glwDeps.pm.RefInc(ec.w.glwRoot.grPropNav)
	}
	evalPush(ec, r)
	return 0
}

// C: glwf_eventWithProp (glw_view_eval.c:7037)
func glwfEventWithProp(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := tokenResolve(ec, argv[0])
	if a == nil {
		return -1
	}
	b := resolvePropertyName2(ec, argv[1])
	if b == nil {
		return -1
	}
	name := token2rstr(a)
	if name == nil {
		return glwViewSeterr(ec.ei, a, "eventWithProp(): First arg is not a string")
	}
	if b.typ != tokenPropertyRef {
		return glwViewSeterr(ec.ei, b, "eventWithProp(): Second argument is not a property")
	}
	r := evalAlloc(self, ec, tokenEvent)
	r.tEvent = glwDeps.em.CreatePropAction(b.tProp, miscpkg.RstrGet(name)).AsEvent()
	r.tEvent.Nav = glwDeps.pm.RefInc(ec.w.glwRoot.grPropNav)
	evalPush(ec, r)
	miscpkg.RstrRelease(name)
	return 0
}

// C: glwf_inject_events (glw_view_eval.c:7205)
func glwfInjectEvents(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := tokenResolveEx(ec, argv[0], gpsEventInjector)
	if a == nil {
		return -1
	}
	evalPush(ec, a)
	return 0
}
