package glw

// C: src/ui/glw/glw_view_eval.c — canonical 1:1 port (core engine).

import (
	"fmt"

	miscpkg "github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
)

// C: subscribe_prop (glw_view_eval.c:2412-2572)
func subscribeProp(ec *glwViewEvalContext, self *Token, typ int) int {
	var gps *GlwPropSub
	var prop *propcore.Prop
	w := ec.w

	if w == nil {
		return glwViewSeterr(ec.ei, self,
			"Properties can not be mapped in this scope")
	}
	if typ == gpsValue && self.typ == tokenPropertyName {
		for g := ec.sublistRpnlocal.slhFirst; g != nil; g = g.gpsRpnLinkNext {
			if g.gpsPropNameID == self.tPropNameID {
				// Found matching subscription in this RPN — create slave
				slave := &GlwPropSub{}
				slave.gpsType = gpsValueSlave
				slave.gpsFile = miscpkg.RstrDup(self.file)
				slave.gpsLine = uint16(self.line)
				slave.gpsWidget = w
				slave.gpsMaster = g
				// SLIST_INSERT_HEAD(&gps->gps_slaves, slave, gps_link)
				slave.gpsLinkNext = g.gpsSlaves.slhFirst
				g.gpsSlaves.slhFirst = slave
				gps = slave
				goto done
			}
		}
	}

	switch self.typ {
	case tokenPropertyName:
		// propname[] built inside subscribe tags path
	case tokenPropertyRef:
		prop = self.tProp
	default:
		panic("subscribe_prop: bad token type")
	}

	{
		f := 0
		var cb func(any, propcore.EventType, ...any)

		switch typ {
		case gpsValue:
			gps = &GlwPropSub{}
			cb = propCallbackValue
			f |= propcore.SubFlagDirectUpdate

		case gpsCloner:
			sc := &subCloner{}
			gps = &sc.scSub
			gps.derived = sc
			sc.scPendingMore = true
			sc.scOriginatingProp = glwDeps.pm.RefInc(ec.scope.gsRoots[glwRootSelf].P)
			sc.scPending.tqhFirst = nil
			sc.scPending.tqhLast = &sc.scPending.tqhFirst
			cb = propCallbackCloner
			f |= propcore.SubFlagDirectUpdate

		case gpsCounter:
			sc := &subCounter{}
			gps = &sc.scSub
			gps.derived = sc
			cb = propCallbackCounter
			f |= propcore.SubFlagDirectUpdate

		case gpsVectorizer:
			sv := &subVectorizer{}
			gps = &sv.svSub
			gps.derived = sv
			sv.svElements.tqhFirst = nil
			sv.svElements.tqhLast = &sv.svElements.tqhFirst
			cb = propCallbackVectorizer

		case gpsEventInjector:
			gps = &GlwPropSub{}
			cb = propCallbackEventInjector

		default:
			panic("subscribe_prop: bad gps type")
		}

		gps.gpsType = uint16(typ)
		gps.gpsScope = glwScopeRetain(ec.scope)
		gps.gpsFile = miscpkg.RstrDup(self.file)
		gps.gpsLine = uint16(self.line)
		gps.gpsWidget = w

		if ec.w.glwFlags2&glw2ExpediteSubscriptions != 0 {
			f |= propcore.SubFlagExpedite
		}
		if ec.debug != 0 || ec.w.glwFlags2&glw2Debug != 0 {
			f |= propcore.SubFlagDebug
		}

		var s *propcore.Subscription
		if prop != nil {
			s = glwPropSubscribeTags(f,
				propTagCallback, cb, gps,
				propTagCourier, w.glwRoot.grCourier,
				propTagRoot, prop,
			)
			// prop came from self->t_prop which we overwrite — release our ref
			glwDeps.pm.RefDec(prop)
		} else {
			var pname [16]string
			glwPropnameToArray(&pname, self)
			roots := []*propcore.PropRootNode{
				{P: w.glwRoot.grPropUi},
				{P: w.glwRoot.grPropNav, Name: "nav"},
			}
			var rv []propcore.PropRoot
			if ec.scope != nil {
				rv = ec.scope.gsRoots[:ec.scope.gsNumRoots]
			}
			_ = roots
			_ = rv
			s = subscribeByNameVector(f, cb, gps, glwPropnameSlice(&pname), ec, w)
		}

		gps.gpsSub = s
		// SLIST_INSERT_HEAD(ec->sublist, gps, gps_link)
		gps.gpsLinkNext = ec.sublist.slhFirst
		ec.sublist.slhFirst = gps

		gps.gpsPropNameID = self.tPropNameID
		if typ == gpsValue {
			gps.gpsRpnLinkNext = ec.sublistRpnlocal.slhFirst
			ec.sublistRpnlocal.slhFirst = gps
		}
		if ec.passiveSubscriptions != 0 {
			gps.gpsRpn = nil
		} else {
			gps.gpsRpn = ec.rpn
		}
	}

done:
	if self.typ == tokenPropertyName {
		for j := range self.tElements {
			miscpkg.RstrRelease(self.tPnvec[j])
		}
		glwViewFreeChain(ec.gr, self.child)
		self.child = nil
	}

	self.tPropsubr = gps
	self.typ = tokenPropertySubscription
	return 0
}

// subscribeByNameVector — C: prop_subscribe(f, PROP_TAG_CALLBACK, cb, gps,
//
//	PROP_TAG_NAME_VECTOR, propname, PROP_TAG_COURIER, courier,
//	PROP_TAG_ROOT_VECTOR, scope roots, PROP_TAG_ROOT, gr_prop_ui,
//	PROP_TAG_NAMED_ROOT, gr_prop_nav, "nav", NULL)
func subscribeByNameVector(f int,
	cb func(any, propcore.EventType, ...any),
	opaque any, pname []string,
	ec *glwViewEvalContext, w *Glw) *propcore.Subscription {
	var roots []*propcore.PropRootNode
	roots = append(roots, &propcore.PropRootNode{P: w.glwRoot.grPropUi})
	if w.glwRoot.grPropNav != nil {
		roots = append(roots, &propcore.PropRootNode{
			P: w.glwRoot.grPropNav, Name: "nav"})
	}
	var rv []propcore.PropRoot
	if ec.scope != nil {
		rv = ec.scope.gsRoots[:ec.scope.gsNumRoots]
	}
	// C: prop_subscribe (prop_core.c:3163-3170):
	//   canonical = prop_subfind(p, name, 0, 0, NULL)      — no symlinks
	//   value     = prop_subfind(p, name, 1, 0, origin_chain)
	root := glwDeps.pm.ResolveTree(pname[0], roots, rv)
	if root == nil {
		return nil
	}
	var p *propcore.Prop
	var origins []*propcore.Prop
	var valueProp *propcore.Prop
	if root.GetType() == propcore.PropTypeProxy {
		// C: else if(p->hp_type == PROP_PROXY)
		//      { ppc = p->hp_proxy_ppc; canonical = value = p; }
		// The remaining name elements are serialized on the wire by
		// prop_proxy_subscribe. Go folds them into an owned pfx node
		// via GetByName — the serialized pfx is byte-identical to C's
		// (pfx elements + name elements).
		p = glwDeps.pm.RefInc(root)
		valueProp = glwDeps.pm.GetByName(pname, 0, rv, nil, roots...)
		if valueProp == nil {
			return nil
		}
		defer glwDeps.pm.RefDec(valueProp)
	} else {
		p = glwDeps.pm.Subfind(root, pname[1:], 0, 0, nil)
		valueProp = glwDeps.pm.Subfind(root, pname[1:], 1, 0, &origins)
		if p == nil || valueProp == nil {
			return nil
		}
		p = glwDeps.pm.RefInc(p)
	}
	defer glwDeps.pm.RefDec(p)

	subArgs := []any{f}
	subArgs = append(subArgs, propcore.SubCourier{C: w.glwRoot.grCourier})
	subArgs = append(subArgs, propcore.SubValueProp{P: valueProp})
	if len(origins) > 0 {
		subArgs = append(subArgs, propcore.SubOrigins{Props: origins})
	}
	return p.Subscribe(cb, opaque, subArgs...)
}

// ---------------------------------------------------------------------------
// C: invoke_func (glw_view_eval.c:2574-2599)
func invokeFunc(ec *glwViewEvalContext, t *Token) int {
	if t.tFunc.nargs >= 0 && t.tFunc.nargs != int(t.tNumArgs) {
		glwViewSeterr(ec.ei, t, "%s(): Invalid number of arguments: %d, "+
			"expected %d", t.tFunc.name, t.tNumArgs, t.tFunc.nargs)
		return -1
	}
	if t.tNumArgs == 0 {
		return t.tFunc.cb(ec, t, nil, 0)
	}
	vec := make([]*Token, t.tNumArgs)
	for i := int(t.tNumArgs) - 1; i >= 0; i-- {
		vec[i] = evalPop(ec)
	}
	return t.tFunc.cb(ec, t, vec, uint(t.tNumArgs))
}

// C: make_vector (glw_view_eval.c:2602-2664)
func makeVector(ec *glwViewEvalContext, t *Token) int {
	var r *Token
	v := ec.stack

	numerics := 0
	strings := 0

	for range int(t.tNumArgs) {
		switch v.typ {
		case tokenInt, tokenEm, tokenFloat:
			numerics++
		case tokenRstring, tokenURI:
			strings++
		}
		v = (*Token)(v.tmp)
	}

	if strings > numerics {
		r = evalAlloc(t, ec, tokenVector)
		r.tElements = int(t.tNumArgs)

		tp := &r.child
		for range int(t.tNumArgs) {
			a := tokenResolve(ec, evalPop(ec))
			if a == nil {
				return -1
			}
			a = glwViewTokenCopy(ec.w.glwRoot, a)
			*tp = a
			tp = &a.next
		}
		evalPush(ec, r)
		return 0
	}

	if t.tNumArgs < 1 || t.tNumArgs > 4 {
		return glwViewSeterr(ec.ei, t, "Invalid numeric vector length (%d)",
			t.tNumArgs)
	}

	r = evalAlloc(t, ec, tokenVectorFloat)
	r.tElements = int(t.tNumArgs)

	for i := int(t.tNumArgs) - 1; i >= 0; i-- {
		a := tokenResolve(ec, evalPop(ec))
		if a == nil {
			return -1
		}
		r.tFloatVector[i] = token2float(ec, a)
	}
	evalPush(ec, r)
	return 0
}

// C: glw_view_eval_rpn0 (glw_view_eval.c:2667-2783)
func glwViewEvalRpn0(t0 *Token, ec *glwViewEvalContext) int {
	for t := t0.child; t != nil; t = t.next {
		switch t.typ {
		case tokenBlock, tokenRstring, tokenCstring, tokenURI,
			tokenFloat, tokenEm, tokenInt, tokenIdentifier,
			tokenResolvedAttribute, tokenUnresolvedAttribute,
			tokenVoid, tokenPropertyRef, tokenPropertyOwner,
			tokenPropertyName, tokenPropertySubscription:
			evalPush(ec, t)

		case tokenAdd, tokenSub, tokenMultiply, tokenDivide, tokenModulo:
			if evalOp(ec, t) != 0 {
				return -1
			}

		case tokenBooleanOr, tokenBooleanXor, tokenBooleanAnd:
			if evalBoolOp(ec, t) != 0 {
				return -1
			}

		case tokenBooleanNot:
			if evalBoolNot(ec, t) != 0 {
				return -1
			}

		case tokenNullCoalesce:
			if evalNullCoalesce(ec, t) != 0 {
				return -1
			}

		case tokenEq, tokenNeq:
			if evalEq(ec, t, t.typ == tokenNeq) != 0 {
				return -1
			}

		case tokenLt, tokenGt:
			if evalLt(ec, t, t.typ == tokenGt) != 0 {
				return -1
			}

		case tokenFunction:
			if invokeFunc(ec, t) != 0 {
				return -1
			}

		case tokenLeftBracket:
			if makeVector(ec, t) != 0 {
				return -1
			}

		case tokenAssignment:
			if evalAssign(ec, t, 0) != 0 {
				return -1
			}

		case tokenCondAssignment:
			if evalAssign(ec, t, 1) != 0 {
				return -1
			}

		case tokenDebugAssignment:
			if evalAssign(ec, t, 2) != 0 {
				return -1
			}

		case tokenRefAssignment:
			if evalAssign(ec, t, 3) != 0 {
				return -1
			}

		case tokenLinkAssignment:
			if evalLinkAssign(ec, t) != 0 {
				return -1
			}

		case tokenTenary:
			if evalTenary(ec, t) != 0 {
				return -1
			}

		default:
			fmt.Printf("Can not handle token %s\n", token2name(t))
			panic("glw_view_eval_rpn0")
		}
	}
	return 0
}

// C: glw_view_eval_rpn (glw_view_eval.c:2786-2804)
func glwViewEvalRpn(t *Token, pec *glwViewEvalContext, copyp *int) int {
	ec := *pec

	ec.rpn = t
	ec.alloc = nil
	ec.stack = nil
	ec.sublistRpnlocal.slhFirst = nil

	r := glwViewEvalRpn0(t, &ec)

	*copyp = int(ec.dynamicEval)
	glwViewFreeChain(ec.gr, ec.alloc)
	return r
}

// C: glw_view_eval_static_block (glw_view_eval.c:2807-2838)
func glwViewEvalStaticBlock(t *Token, ec *glwViewEvalContext) int {
	var copy int
	for ; t != nil; t = t.next {
		switch t.typ {
		case tokenNop:
		case tokenRpn, tokenPureRpn:
			if glwViewEvalRpn(t, ec, &copy) != 0 {
				return -1
			}
		case tokenFloat, tokenInt, tokenVectorFloat, tokenVoid,
			tokenCstring, tokenRstring, tokenIdentifier, tokenModFlags:
			if t.tAttrib.set(ec, t.tAttrib, t) != 0 {
				return -1
			}
		default:
			panic("glw_view_eval_static_block: bad token")
		}
	}
	return 0
}

// C: glw_view_eval_block (glw_view_eval.c:2841-2913)
func glwViewEvalBlock(t *Token, ec *glwViewEvalContext, nonpure **Token) int {
	var copy int
	// assert(ec->dynamic_eval == 0)

	p := &t.child

	for {
		t = *p
		if t == nil {
			break
		}

		switch t.typ {
		case tokenNop:

		case tokenRpn:
			if nonpure != nil {
				// Extract non-pure expressions (styles)
				*p = t.next
				t.next = *nonpure
				*nonpure = t
				continue
			}
			fallthrough // C: FALLTHRU

		case tokenPureRpn:
			if glwViewEvalRpn(t, ec, &copy) != 0 {
				return -1
			}
			if copy == 0 || ec.passiveSubscriptions != 0 {
				break
			}
			*p = t.next
			w := ec.w
			t.next = w.glwDynamicExpressions
			w.glwDynamicExpressions = t
			t.tDynamicEval = uint8(copy)
			w.glwDynamicEval |= uint8(copy)
			continue

		case tokenFloat, tokenInt, tokenVectorFloat, tokenVoid,
			tokenCstring, tokenRstring, tokenIdentifier, tokenModFlags:
			if t.tAttrib.set(ec, t.tAttrib, t) != 0 {
				return -1
			}

		default:
			glwViewSeterr(ec.ei, t, "Unexpected token %s in evaluator",
				token2name(t))
			panic("glw_view_eval_block")
		}
		p = &t.next
	}
	return 0
}

// ---------------------------------------------------------------------------
// C: glw_view_function_resolve (glw_view_eval.c:7385-7420)
func glwViewFunctionResolve(gr *glwRoot, ei *errorinfoT, t *Token) *Token {
	fname := miscpkg.RstrGet(t.tRstring)

	for i := range funcvec {
		if funcvec[i].name == fname {
			miscpkg.RstrRelease(t.tRstring)
			t.tFunc = &funcvec[i]
			t.typ = tokenFunction
			if t.tFunc.ctor != nil {
				t.tFunc.ctor(t)
			}
			if t.tFunc.preproc != nil {
				return t.tFunc.preproc(gr, ei, t)
			}
			return t.next.next
		}
	}

	// Syntactic sugar: resolve a function directly into a widget name
	gc := glwClassFindByName(fname)
	if gc != nil {
		miscpkg.RstrRelease(t.tRstring)
		t.typ = tokenFunction
		t.tFunc = &funcvec[0]
		t.tFuncArg = gc
		return t.next.next
	}

	glwViewSeterr(ei, t, "Unknown function: %s", fname)
	return nil
}
