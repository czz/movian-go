package glw

// C: src/ui/glw/glw_view_eval.c — canonical 1:1 port (core engine).

import (
	miscpkg "github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
	tracepkg "github.com/czz/movian-go/internal/trace"
)

// ---------------------------------------------------------------------------
// C: resolve_property_name (glw_view_eval.c:823-848)
func resolvePropertyName(ec *glwViewEvalContext, a *Token, followSymlinks int) int {
	var pname [16]string
	glwPropnameToArray(&pname, a)

	p := propGetByNameTags(glwPropnameSlice(&pname), followSymlinks, ec, a)

	// Transform TOKEN_PROPERTY_NAME -> TOKEN_PROPERTY_REF
	glwViewFreeChain(ec.gr, a.child)
	a.child = nil
	for j := range a.tElements {
		miscpkg.RstrRelease(a.tPnvec[j])
	}
	a.typ = tokenPropertyRef
	a.tProp = p
	return 0
}

// propGetByNameTags — C: prop_get_by_name(pname, follow_symlinks,
//
//	PROP_TAG_ROOT_VECTOR, ec->scope->gs_roots, ec->scope->gs_num_roots,
//	PROP_TAG_ROOT, gr->gr_prop_ui,
//	PROP_TAG_NAMED_ROOT, gr->gr_prop_nav, "nav", NULL)
func propGetByNameTags(pname []string, followSymlinks int,
	ec *glwViewEvalContext, self *Token) *propcore.Prop {
	var roots []*propcore.PropRootNode
	if ec.w != nil && ec.w.glwRoot != nil {
		roots = append(roots, &propcore.PropRootNode{P: ec.w.glwRoot.grPropUi})
		if ec.w.glwRoot.grPropNav != nil {
			roots = append(roots, &propcore.PropRootNode{
				P: ec.w.glwRoot.grPropNav, Name: "nav"})
		}
	}
	var rv []propcore.PropRoot
	if ec.scope != nil {
		rv = ec.scope.gsRoots[:ec.scope.gsNumRoots]
	}
	return glwDeps.pm.GetByName(pname, followSymlinks, rv, nil, roots...)
}

// C: resolve_property_name2 (glw_view_eval.c:851-870)
func resolvePropertyName2(ec *glwViewEvalContext, t *Token) *Token {
	switch t.typ {
	case tokenPropertyName:
		fs := 1
		if t.tFlags&tokenFCanonicalPath != 0 {
			fs = 0
		}
		if resolvePropertyName(ec, t, fs) != 0 {
			return nil
		}
		// FALLTHRU
		fallthrough
	case tokenPropertyRef:
	default:
		glwViewSeterr(ec.ei, t, "Argument '%s' is not a property", token2name(t))
		return nil
	}
	return t
}

// C: eval_link_assign (glw_view_eval.c:873-931)
func evalLinkAssign(ec *glwViewEvalContext, self *Token) int {
	right := evalPop(ec)
	left := evalPop(ec)

	switch right.typ {
	case tokenPropertyName:
		if resolvePropertyName(ec, right, 0) != 0 {
			return -1
		}
	case tokenPropertyRef:
	}

	switch left.typ {
	case tokenResolvedAttribute:
		return left.tAttrib.set(ec, left.tAttrib, right)

	case tokenUnresolvedAttribute:
		return glwViewUnresolvedAttributeSet(ec, miscpkg.RstrGet(left.tRstring), right)

	case tokenPropertyName:
		if resolvePropertyName(ec, left, 0) != 0 {
			return -1
		}

	case tokenIdentifier:
		if ec.tgtprop == nil {
			return glwViewSeterr(ec.ei, self,
				"Invalid link assignment outside block")
		}
		// C: prop_create_r(ec->tgtprop, ...) — caller holds a ref
		p := glwDeps.pm.RefInc(glwDeps.pm.CreateEx(ec.tgtprop, miscpkg.RstrGet(left.tRstring), nil, false, false))
		miscpkg.RstrRelease(left.tRstring)
		left.tRstring = nil
		left.typ = tokenPropertyRef
		left.tProp = p

	case tokenPropertyRef:

	default:
		glwViewSeterr(ec.ei, left,
			"Link target '%s' is not a property", token2name(left))
		return -1
	}

	if right.typ == tokenPropertyRef {
		glwDeps.pm.Link(right.tProp, left.tProp, nil, false, false)
	}
	return 0
}

// C: eval_assign (glw_view_eval.c:934-1085)
func evalAssign(ec *glwViewEvalContext, self *Token, how int) int {
	right := evalPop(ec)
	left := evalPop(ec)
	r := 0

	if left == nil || right == nil {
		return glwViewSeterr(ec.ei, self, "Invalid assignment")
	}

	// Catch some special cases here
	if right.typ == tokenBlock {
		var n glwViewEvalContext
		n.w = ec.w
		n.scope = ec.scope
		n.ei = ec.ei
		n.gr = ec.gr
		n.rc = ec.rc
		n.sublist = ec.sublist
		n.tgtprop = glwDeps.pm.CreateRoot("")

		if glwViewEvalBlock(right, &n, nil) != 0 {
			return -1
		}
		right = evalAlloc(right, ec, tokenPropertyOwner)
		right.tProp = n.tgtprop
	} else if right.typ == tokenPropertyRef && left.typ == tokenPropertyRef {
		if right.tProp != left.tProp {
			glwDeps.ts.Trace(tracepkg.TRACE_INFO, "GLW",
				"%s:%d: Prop linking via assignment is deprecated",
				miscpkg.RstrGet(self.file), self.line)
			glwDeps.pm.Link(right.tProp, left.tProp, nil, false, how == 2)
			left.tFlags |= tokenFPropLink
		}
		evalPush(ec, right)
		return 0
	} else {
		if how == 3 ||
			(left.typ == tokenResolvedAttribute &&
				left.tAttrib.flags&glwAttribFlagNoSubscription != 0) {
			if right.typ == tokenPropertyName {
				if resolvePropertyName(ec, right, 0) != 0 {
					return -1
				}
			}
		} else {
			if right = tokenResolve(ec, right); right == nil {
				return -1
			}
		}
	}

	if left.typ == tokenPropertyName {
		fs := 1
		if left.tFlags&tokenFCanonicalPath != 0 {
			fs = 0
		}
		if resolvePropertyName(ec, left, fs) != 0 {
			return -1
		}
	}

	// Conditional assignment: rvalue of (void) results in doing nothing
	if how == 1 && right.typ == tokenVoid {
		evalPush(ec, right)
		return 0
	}

	switch left.typ {
	case tokenResolvedAttribute:
		r = left.tAttrib.set(ec, left.tAttrib, right)

	case tokenUnresolvedAttribute:
		r = glwViewUnresolvedAttributeSet(ec, miscpkg.RstrGet(left.tRstring), right)

	case tokenIdentifier:
		if ec.tgtprop == nil {
			return glwViewSeterr(ec.ei, self, "Invalid assignment outside block")
		}
		// C: prop_create_r(ec->tgtprop, ...) — caller holds a ref
		p := glwDeps.pm.RefInc(glwDeps.pm.CreateEx(ec.tgtprop, miscpkg.RstrGet(left.tRstring), nil, false, false))
		miscpkg.RstrRelease(left.tRstring)
		left.tRstring = nil
		left.typ = tokenPropertyRef
		left.tProp = p
		fallthrough // C: FALLTHRU

	case tokenPropertyRef:
		switch right.typ {
		case tokenRstring:
			glwDeps.pm.SetStringEx(left.tProp, nil, miscpkg.RstrGet(right.tRstring), propcore.StringType(right.tRstrType))
		case tokenCstring:
			glwDeps.pm.SetStringEx(left.tProp, nil, right.tCstring, propcore.PropStrUTF8)
		case tokenURI:
			glwDeps.pm.SetURIEx(left.tProp, nil,
				miscpkg.RstrGet(right.tURITitle), miscpkg.RstrGet(right.tURI))
		case tokenInt:
			glwDeps.pm.SetIntEx(left.tProp, nil, right.tInt)
		case tokenFloat:
			glwDeps.pm.SetFloatEx(left.tProp, nil, right.tFloat)
		case tokenEm:
			glwDeps.pm.SetFloatEx(left.tProp, nil,
				right.tFloat*float32(ec.w.glwRoot.grCurrentSize))
			ec.dynamicEval |= GLW_VIEW_EVAL_EM
		case tokenPropertyRef:
			glwDeps.pm.SetPropExl(left.tProp, nil, right.tProp)
		case tokenVoid:
			if left.tFlags&tokenFPropLink != 0 {
				glwDeps.pm.Unlink(left.tProp)
				left.tFlags &^= tokenFPropLink
			} else {
				glwDeps.pm.SetVoidEx(left.tProp, nil)
			}
		default:
			glwDeps.pm.SetVoidEx(left.tProp, nil)
		}
		r = 0

	default:
		return glwViewSeterr(ec.ei, self, "Invalid assignment %s = %s",
			token2name(left), token2name(right))
	}

	evalPush(ec, right)
	return r
}

// C: eval_tenary (glw_view_eval.c:1088-1104)
func evalTenary(ec *glwViewEvalContext, self *Token) int {
	c := evalPop(ec)
	b := evalPop(ec)
	a := evalPop(ec)

	if a = tokenResolve(ec, a); a == nil {
		return -1
	}

	o := b
	if !token2bool(a) {
		o = c
	}
	evalPush(ec, o)
	return 0
}

// C: eval_dynamic (glw_view_eval.c:1107-1128)
func evalDynamic(w *Glw, rpn *Token, rc *glwRctx, scope *glwScope) {
	var ec glwViewEvalContext
	ec.w = w
	ec.gr = w.glwRoot
	ec.rc = rc
	ec.scope = scope
	ec.sublist = &w.glwPropSubscriptions

	glwViewEvalRpn0(rpn, &ec)
	rpn.tDynamicEval = uint8(ec.dynamicEval)
	w.glwDynamicEval |= uint8(ec.dynamicEval)

	glwViewFreeChain(ec.gr, ec.alloc)
}
