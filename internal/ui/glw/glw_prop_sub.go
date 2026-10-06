package glw

// C: src/ui/glw/glw_view_eval.c — glw_prop_sub_t + subscription lifecycle,
// plus the prop_subscribe(PROP_TAG_...) front-end used throughout GLW.

import (
	"reflect"
	"unsafe"

	miscpkg "github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
)

// C: enum { GPS_VALUE, ... } (glw_view_eval.c:60-67)
const (
	gpsValue = iota
	gpsValueSlave
	gpsCloner
	gpsCounter
	gpsVectorizer
	gpsEventInjector
)

// GlwPropSub — C: glw_prop_sub_t (glw_view_eval.c:77-93)
type GlwPropSub struct {
	gpsLinkNext    *GlwPropSub // C: SLIST_ENTRY gps_link
	gpsRpnLinkNext *GlwPropSub // C: SLIST_ENTRY gps_rpn_link (temp during rpn eval)
	gpsWidget      *Glw        // C: gps_widget

	gpsSub   *propcore.Subscription // C: gps_sub
	gpsScope *glwScope              // C: gps_scope
	gpsRpn   *Token                 // C: gps_rpn

	gpsSlaves glwPropSubSlist // C: gps_slaves

	// C: union { token_t *gps_token; struct glw_prop_sub *gps_master; }
	gpsToken  *Token
	gpsMaster *GlwPropSub

	// derived is the enclosing sub_cloner_t/sub_vectorizer_t/sub_counter_t
	// when gps_type is GPS_CLONER/GPS_VECTORIZER/GPS_COUNTER — C recovers it
	// by casting gps (first-member embedding). Go can't, so we keep the
	// pointer here.
	derived any

	gpsFile       *miscpkg.Rstr // C: gps_file
	gpsPropNameID int           // C: gps_prop_name_id
	gpsLine       uint16        // C: gps_line
	gpsType       uint16        // C: gps_type
}

// subCloner — C: sub_cloner_t (glw_view_eval.c:99-118)
type subCloner struct {
	scSub GlwPropSub // C: sc_sub (first member — cast-compatible)

	scClonerBody  *Token    // C: sc_cloner_body
	scClonerClass *glwClass // C: sc_cloner_class

	scPending         glwPropSubPendingQueue // C: sc_pending
	scPendingSelect   *propcore.Prop         // C: sc_pending_select
	scEntries         int                    // C: sc_entries
	scOriginatingProp *propcore.Prop         // C: sc_originating_prop
	scAnchor          *Glw                   // C: sc_anchor

	scPositionsValid bool // C: sc_positions_valid
	scLowestActive   int  // C: sc_lowest_active
	scHighestActive  int  // C: sc_highest_active

	scHaveMore    bool // C: sc_have_more
	scPendingMore bool // C: sc_pending_more

	scClones glwCloneList // C: sc_clones
}

// subCounter — C: sub_counter_t (glw_view_eval.c:127-131)
type subCounter struct {
	scSub     GlwPropSub // C: sc_sub
	scEntries int        // C: sc_entries
}

// glwPropSubPending — C: glw_prop_sub_pending_t (glw_view_eval.c:48-51)
type glwPropSubPending struct {
	gpspProp *propcore.Prop
	// TAILQ_ENTRY
	gpspLinkNext *glwPropSubPending
	gpspLinkPrev **glwPropSubPending
}

// C: TAILQ_HEAD(glw_prop_sub_pending_queue, glw_prop_sub_pending)
type glwPropSubPendingQueue struct {
	tqhFirst *glwPropSubPending
	tqhLast  **glwPropSubPending
}

// vectorizerElement — C: vectorizer_element_t (glw_view_eval.c:137-144)
type vectorizerElement struct {
	veLinkNext *vectorizerElement // TAILQ_ENTRY ve_link
	veLinkPrev **vectorizerElement
	veSub      *propcore.Subscription // C: ve_sub
	veToken    *Token                 // C: ve_token
	veSv       *subVectorizer         // C: ve_sv
	veProp     *propcore.Prop         // C: ve_prop
}

// subVectorizer — C: sub_vectorizer_t (glw_view_eval.c:149-154)
type subVectorizer struct {
	svSub      GlwPropSub // C: sv_sub
	svElements struct {   // C: TAILQ_HEAD sv_elements
		tqhFirst *vectorizerElement
		tqhLast  **vectorizerElement
	}
	svSelected *vectorizerElement // C: sv_selected
}

// glwCloneList — C: LIST_HEAD(clone_list, glw_clone) (glw_view_eval.c:43)
type glwCloneList struct {
	lhFirst *glwClone
}

// C: static void cloner_cleanup (glw_view_eval.c:170-181)
func clonerCleanup(gr *glwRoot, sc *subCloner) {
	for c := sc.scClones.lhFirst; c != nil; {
		next := c.cLinkNext
		glwDeps.pm.TagClear(c.cProp, sc)
		cloneFree(gr, c)
		c = next
	}
	sc.scClones.lhFirst = nil

	if sc.scClonerBody != nil {
		glwViewFreeChain(gr, sc.scClonerBody)
	}
}

// C: static void vectorizer_clean (glw_view_eval.c:187-198)
func vectorizerClean(gr *glwRoot, sv *subVectorizer) {
	for ve := sv.svElements.tqhFirst; ve != nil; {
		next := ve.veLinkNext
		glwDeps.pm.Unsubscribe(ve.veSub)
		glwViewTokenFree(gr, ve.veToken)
		glwDeps.pm.TagClear(ve.veProp, sv)
		glwDeps.pm.RefDec(ve.veProp)
		ve = next
	}
	sv.svElements.tqhFirst = nil
	sv.svElements.tqhLast = &sv.svElements.tqhFirst
}

// C: void glw_prop_subscription_destroy_list (glw_view_eval.c:205-252)
func glwPropSubscriptionDestroyList(gr *glwRoot, l *glwPropSubSlist) {
	for gps := l.slhFirst; gps != nil; {
		next := gps.gpsLinkNext

		if gps.gpsType != gpsValueSlave {
			glwDeps.pm.Unsubscribe(gps.gpsSub)

			if gps.gpsToken != nil {
				glwViewTokenFree(gr, gps.gpsToken)
			}

			switch gps.gpsType {
			case gpsValue, gpsCounter, gpsEventInjector:
			case gpsCloner:
				sc := gps.derived.(*subCloner)
				clonerCleanup(gr, sc)
				if sc.scOriginatingProp != nil {
					glwDeps.pm.RefDec(sc.scOriginatingProp)
				}
			case gpsVectorizer:
				sv := gps.derived.(*subVectorizer)
				vectorizerClean(gr, sv)
			}
			glwScopeRelease(gps.gpsScope)
			glwPropSubscriptionDestroyList(gr, &gps.gpsSlaves)
		}
		miscpkg.RstrRelease(gps.gpsFile)
		gps = next
	}
	l.slhFirst = nil
}

// C: void glw_prop_subscription_suspend_list (glw_view_eval.c:257-268)
func glwPropSubscriptionSuspendList(l *glwPropSubSlist) {
	for gps := l.slhFirst; gps != nil; gps = gps.gpsLinkNext {
		if gps.gpsSub != nil {
			glwDeps.pm.Unsubscribe(gps.gpsSub)
			gps.gpsSub = nil
		}
	}
}

// ---------------------------------------------------------------------------
// C: prop_subscribe(0, PROP_TAG_..., NULL) — tag-driven subscribe front-end.
// The C tags are varargs; in Go we pass flat pairs.

type propTagKind int

const (
	propTagName              propTagKind = iota // C: PROP_TAG_NAME(...) → NAME_VECTOR
	propTagNamestr                              // C: PROP_TAG_NAMESTR
	propTagCallback                             // C: PROP_TAG_CALLBACK
	propTagCallbackUserInt                      // C: PROP_TAG_CALLBACK_USER_INT
	propTagCallbackString                       // C: PROP_TAG_CALLBACK_STRING
	propTagCallbackRstr                         // C: PROP_TAG_CALLBACK_RSTR
	propTagCallbackInt                          // C: PROP_TAG_CALLBACK_INT
	propTagCallbackFloat                        // C: PROP_TAG_CALLBACK_FLOAT
	propTagCallbackEvent                        // C: PROP_TAG_CALLBACK_EVENT
	propTagCallbackDestroyed                    // C: PROP_TAG_CALLBACK_DESTROYED
	propTagCourier                              // C: PROP_TAG_COURIER
	propTagRoot                                 // C: PROP_TAG_ROOT
	propTagNamedRoot                            // C: PROP_TAG_NAMED_ROOT
	propTagRootVector                           // C: PROP_TAG_ROOT_VECTOR
)

// C: trampoline_int (prop_core.c:568-589)
func trampolineInt(cb func(opaque any, v int), opaque any,
	ignoreVoid bool) func(any, propcore.EventType, ...any) {
	return func(_ any, ev propcore.EventType, args ...any) {
		switch ev {
		case propcore.EventSetInt:
			cb(opaque, intFromArg(args[0]))
		case propcore.EventSetFloat:
			cb(opaque, int(floatFromArg(args[0])))
		case propcore.EventSetRString, propcore.EventSetCString:
			cb(opaque, miscpkg.Atoi(strFromArg(args[0])))
		default:
			if !ignoreVoid {
				cb(opaque, 0)
			}
		}
	}
}

// C: trampoline_float (prop_core.c:594-611)
func trampolineFloat(cb func(opaque any, v float64), opaque any,
	ignoreVoid bool) func(any, propcore.EventType, ...any) {
	return func(_ any, ev propcore.EventType, args ...any) {
		switch ev {
		case propcore.EventSetInt:
			cb(opaque, float64(intFromArg(args[0])))
		case propcore.EventSetFloat:
			cb(opaque, float64(floatFromArg(args[0])))
		default:
			if !ignoreVoid {
				cb(opaque, 0)
			}
		}
	}
}

// C: trampoline_string (prop_core.c:683-700)
func trampolineString(cb func(opaque any, s string), opaque any,
	ignoreVoid bool) func(any, propcore.EventType, ...any) {
	return func(_ any, ev propcore.EventType, args ...any) {
		switch ev {
		case propcore.EventSetRString, propcore.EventSetCString:
			cb(opaque, strFromArg(args[0]))
		case propcore.EventSetURI:
			cb(opaque, strFromArg(args[0])) // C: title rstr
		default:
			if !ignoreVoid {
				cb(opaque, "")
			}
		}
	}
}

// C: trampoline_rstr (prop_core.c:705-727)
func trampolineRstr(cb func(opaque any, r *miscpkg.Rstr), opaque any,
	ignoreVoid bool) func(any, propcore.EventType, ...any) {
	return func(_ any, ev propcore.EventType, args ...any) {
		switch ev {
		case propcore.EventSetRString:
			cb(opaque, rstrFromArg(args[0]))
		case propcore.EventSetCString:
			t := miscpkg.RstrAllocStr(strFromArg(args[0]))
			cb(opaque, t)
			miscpkg.RstrRelease(t)
		case propcore.EventSetURI:
			cb(opaque, rstrFromArg(args[0]))
		default:
			if !ignoreVoid {
				cb(opaque, nil)
			}
		}
	}
}

// C: trampoline_event (prop_core.c:732-743)
func trampolineEvent(cb func(opaque any, e any), opaque any) func(any, propcore.EventType, ...any) {
	return func(_ any, ev propcore.EventType, args ...any) {
		if ev == propcore.EventExtEvent {
			cb(opaque, args[0])
		}
	}
}

// C: trampoline_destroyed (prop_core.c:748-758)
func trampolineDestroyed(cb func(opaque any, sub *propcore.Subscription), opaque any) func(any, propcore.EventType, ...any) {
	return func(_ any, ev propcore.EventType, args ...any) {
		if ev == propcore.EventDestroyed {
			var sub *propcore.Subscription
			if len(args) > 0 {
				sub, _ = args[0].(*propcore.Subscription)
			}
			cb(opaque, sub)
		}
	}
}

// glwPropSubscribeTags — C: prop_subscribe (prop_core.c:2922-3390), the
// tag-parsing front-end + path resolution + subscription.
// Returns the subscription (C: prop_sub_t *).
func glwPropSubscribeTags(flags int, tags ...any) *propcore.Subscription {
	var name []string
	var roots []*propcore.PropRootNode
	var rootVector []propcore.PropRoot
	var courier *propcore.Courier
	var cb func(any, propcore.EventType, ...any)
	var opaque any
	var userInt int
	var haveUserInt bool

	ignoreVoid := flags&propcore.SubFlagIgnoreVoid != 0

	for i := 0; i < len(tags); {
		tag, _ := tags[i].(propTagKind)
		i++
		switch tag {
		case propTagName:
			name, _ = tags[i].([]string)
			i++
		case propTagNamestr:
			s, _ := tags[i].(string)
			i++
			if name == nil && s != "" {
				name = splitNamestr(s)
			}
		case propTagCallback:
			cb, _ = tags[i].(func(any, propcore.EventType, ...any))
			opaque = tags[i+1]
			i += 2
		case propTagCallbackUserInt:
			cb, _ = tags[i].(func(any, propcore.EventType, ...any))
			opaque = tags[i+1]
			userInt = intFromArg(tags[i+2])
			haveUserInt = true
			i += 3
		case propTagCallbackString:
			f, _ := tags[i].(func(any, string))
			o := tags[i+1]
			cb = trampolineString(f, o, ignoreVoid)
			opaque = o
			i += 2
		case propTagCallbackRstr:
			f, _ := tags[i].(func(any, *miscpkg.Rstr))
			o := tags[i+1]
			cb = trampolineRstr(f, o, ignoreVoid)
			opaque = o
			i += 2
		case propTagCallbackInt:
			f, _ := tags[i].(func(any, int))
			o := tags[i+1]
			cb = trampolineInt(f, o, ignoreVoid)
			opaque = o
			i += 2
		case propTagCallbackFloat:
			f, _ := tags[i].(func(any, float64))
			o := tags[i+1]
			cb = trampolineFloat(f, o, ignoreVoid)
			opaque = o
			i += 2
		case propTagCallbackEvent:
			f, _ := tags[i].(func(any, any))
			o := tags[i+1]
			cb = trampolineEvent(f, o)
			opaque = o
			i += 2
		case propTagCallbackDestroyed:
			f, _ := tags[i].(func(any, *propcore.Subscription))
			o := tags[i+1]
			cb = trampolineDestroyed(f, o)
			opaque = o
			i += 2
		case propTagCourier:
			courier, _ = tags[i].(*propcore.Courier)
			i++
		case propTagRoot:
			if p, _ := tags[i].(*propcore.Prop); p != nil {
				roots = append(roots, &propcore.PropRootNode{P: p})
			}
			i++
		case propTagNamedRoot:
			p, _ := tags[i].(*propcore.Prop)
			nm, _ := tags[i+1].(string)
			if p != nil && nm != "" {
				roots = append(roots, &propcore.PropRootNode{P: p, Name: nm})
			}
			i += 2
		case propTagRootVector:
			rootVector, _ = tags[i].([]propcore.PropRoot)
			i++
		default:
			i++
		}
	}

	// C: prop_subscribe0 (prop_core.c:3125) — if(name == NULL) the
	// subscription targets the supplied root prop directly; otherwise
	// prop_get_by_name resolves name against the roots.
	var p *propcore.Prop
	var origins []*propcore.Prop
	var valueProp *propcore.Prop
	if len(name) == 0 {
		if len(roots) > 0 && roots[0].P != nil {
			p = glwDeps.pm.RefInc(roots[0].P)
		}
	} else {
		// C: prop_subscribe (prop_core.c:3163-3170):
		//   canonical = prop_subfind(p, name, 0, 0, NULL)      — no symlinks
		//   value     = prop_subfind(p, name, 1, 0, origin_chain)
		// The canonical walk creates the mirror children on the link
		// dsts; the value walk records every traversed link as origins.
		root := glwDeps.pm.ResolveTree(name[0], roots, rootVector)
		if root == nil {
			return nil
		}
		if root.GetType() == propcore.PropTypeProxy {
			// C: else if(p->hp_type == PROP_PROXY)
			//      { ppc = p->hp_proxy_ppc; canonical = value = p; }
			// The remaining name elements are serialized on the wire
			// by prop_proxy_subscribe. Go folds them into an owned
			// pfx node via GetByName — the serialized pfx is
			// byte-identical to C's (pfx elements + name elements).
			p = glwDeps.pm.RefInc(root)
			valueProp = glwDeps.pm.GetByName(name, 0, rootVector, nil, roots...)
			if valueProp == nil {
				return nil
			}
			defer glwDeps.pm.RefDec(valueProp)
		} else {
			p = glwDeps.pm.Subfind(root, name[1:], 0, 0, nil)
			valueProp = glwDeps.pm.Subfind(root, name[1:], 1, 0, &origins)
			if p == nil || valueProp == nil {
				return nil
			}
			p = glwDeps.pm.RefInc(p)
		}
	}
	if p == nil {
		return nil
	}
	defer glwDeps.pm.RefDec(p)

	subArgs := []any{flags}
	if courier != nil {
		subArgs = append(subArgs, propcore.SubCourier{C: courier})
	}
	if haveUserInt {
		subArgs = append(subArgs, propcore.SubUserInt(userInt))
	}
	if valueProp != nil {
		subArgs = append(subArgs, propcore.SubValueProp{P: valueProp})
	}
	if len(origins) > 0 {
		subArgs = append(subArgs, propcore.SubOrigins{Props: origins})
	}
	return p.Subscribe(cb, opaque, subArgs...)
}

// C: PROP_TAG_NAMESTR path split on '.'
func splitNamestr(s string) []string {
	var out []string
	start := 0
	for i := range len(s) {
		if s[i] == '.' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

func intFromArg(a any) int {
	switch v := a.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float32:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}

func floatFromArg(a any) float32 {
	switch v := a.(type) {
	case float32:
		return v
	case float64:
		return float32(v)
	case int:
		return float32(v)
	case int64:
		return float32(v)
	}
	return 0
}

func strFromArg(a any) string {
	switch v := a.(type) {
	case string:
		return v
	case *miscpkg.Rstr:
		return miscpkg.RstrGet(v)
	}
	return ""
}

func rstrFromArg(a any) *miscpkg.Rstr {
	switch v := a.(type) {
	case *miscpkg.Rstr:
		return v
	case string:
		return miscpkg.RstrAllocStr(v)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Cloner machinery (C: glw_view_eval.c)

// C: static void clone_free (glw_view_eval.c:1553-1567)
func cloneFree(gr *glwRoot, c *glwClone) {
	w := c.cW
	if w != nil {
		w.glwClone = nil
		glwSignalHandlerUnregister(w, cloneSigHandler, unsafe.Pointer(c))
		glwRetireChild(w)
	}

	// C: LIST_REMOVE(c, c_link)
	if c.cLinkNext != nil {
		c.cLinkNext.cLinkPrev = c.cLinkPrev
	}
	*c.cLinkPrev = c.cLinkNext

	glwDeps.pm.RefDec(c.cProp)
	glwDeps.pm.Destroy(c.cCloneRoot)
	gr.grClonePool.PoolPut(unsafe.Pointer(c))
}

// C: static void cloner_resequence (glw_view_eval.c:1384-1400)
func clonerResequence(sc *subCloner) {
	pos := 0
	for w := sc.scSub.gpsWidget.glwChilds.tqhFirst; w != nil; w = w.glwParentLinkNext {
		for gsh := w.glwSignalHandlers.lhFirst; gsh != nil; gsh = gsh.gshLinkNext {
			if reflect.ValueOf(gsh.gshFunc).Pointer() ==
				reflect.ValueOf(cloneSigHandler).Pointer() {
				c := (*glwClone)(gsh.gshOpaque.(unsafe.Pointer))
				c.cPos = uint16(pos)
				break
			}
		}
		pos++
	}
	sc.scPositionsValid = true
}

// C: static void cloner_pagination_check (glw_view_eval.c:1256-1267)
func clonerPaginationCheck(sc *subCloner) {
	if sc.scPendingMore || !sc.scHaveMore {
		return
	}

	if sc.scHighestActive >= int(float32(sc.scEntries)*0.95) ||
		sc.scHighestActive == sc.scEntries-1 {
		sc.scPendingMore = true
		if sc.scSub.gpsSub != nil {
			glwDeps.pm.WantMoreChilds(sc.scSub.gpsSub)
		}
	}
}

// C: static void clone_req_move (glw_view_eval.c:1273-1310)
func cloneReqMove(sc *subCloner, w *Glw, mop *glwMoveOp) {
	var b *Glw
	steps := mop.steps

	if steps == 0 {
		return
	}

	w.glwParent.glwFlags &^= glwFloatingFocus

	if steps < 0 {
		x := w
		b = x
		for steps < 0 && x != nil {
			x = glwTAILQPrev(x)
			if x != nil {
				b = x
			}
			steps++
		}
	} else {
		b = w.glwParentLinkNext
		for steps > 0 && b != nil {
			b = b.glwParentLinkNext
			steps--
		}
	}

	var d *glwClone
	if b != nil {
		d = b.glwClone
		if b != w.glwClone.cSc.scAnchor {
			if d == nil {
				return
			}
		}
	}
	var before *propcore.Prop
	if d != nil {
		before = d.cProp
	}
	glwDeps.pm.ReqMove(w.glwClone.cProp, before)
	mop.didMove = 1
}

// C: static int clone_sig_handler (glw_view_eval.c:1315-1380)
func cloneSigHandler(w *Glw, opaque any, signal glwSignal, extra any) int {
	c := (*glwClone)(opaque.(unsafe.Pointer))
	sc := c.cSc
	var gr *glwRoot
	switch signal {
	case glwSignalActive:
		if sc.scSub.gpsWidget.glwClass.gcFlags&glwDrivePagination == 0 {
			break
		}

		if !sc.scPositionsValid {
			clonerResequence(sc)
		}

		if int(c.cPos) < sc.scLowestActive {
			sc.scLowestActive = int(c.cPos)
		}

		if int(c.cPos) > sc.scHighestActive {
			sc.scHighestActive = int(c.cPos)
		}

		clonerPaginationCheck(sc)

	case glwSignalInactive:
		if sc.scSub.gpsWidget.glwClass.gcFlags&glwDrivePagination == 0 {
			break
		}

		if !sc.scPositionsValid {
			clonerResequence(sc)
		}

		if int(c.cPos) >= sc.scLowestActive && int(c.cPos) < sc.scHighestActive {
			sc.scLowestActive = int(c.cPos) + 1
		}

		if int(c.cPos) <= sc.scHighestActive && int(c.cPos) > sc.scLowestActive {
			sc.scHighestActive = int(c.cPos) - 1
		}

		clonerPaginationCheck(sc)

	case glwSignalMove:
		cloneReqMove(sc, w, extra.(*glwMoveOp))
		return 1

	case glwSignalDestroy:
		gr = w.glwRoot
		sc.scEntries--
		if w.glwParentLinkNext != nil {
			sc.scPositionsValid = false
		}
		c.cW = nil
		cloneFree(gr, c)

	case glwSignalWrapCheck:
		*(extra.(*int)) = map[bool]int{true: 0, false: 1}[sc.scHaveMore]
		return 0

	default:
	}
	return 0
}
