package glw

// C: src/ui/glw/glw_view_eval.c — builtin function table (funcvec) and all
// glwf_* handlers. Ported 1:1 from the canonical C implementation.

import (
	"fmt"
	"unsafe"

	backendcore "github.com/czz/movian-go/internal/backend/core"
	eventpkg "github.com/czz/movian-go/internal/event"
	miscpkg "github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
	propproxy "github.com/czz/movian-go/internal/prop/proxy"
	tracepkg "github.com/czz/movian-go/internal/trace"
)

// ---------------------------------------------------------------------------
// C: glwf_widget (glw_view_eval.c:2919)
func glwfWidget(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	c, _ := self.tFuncArg.(*glwClass)
	b := argv[0]

	if ec.w == nil {
		return glwViewSeterr(ec.ei, self, "Widget can not be created in this scope")
	}
	if b.typ != tokenBlock {
		return glwViewSeterr(ec.ei, self, "widget: Invalid second argument, expected block")
	}

	var n glwViewEvalContext
	n.scope = ec.scope
	n.ei = ec.ei
	n.gr = ec.gr
	n.rc = ec.rc
	n.w = glwCreate(ec.gr, c, ec.w, nil, nil, ec.scope, self.file, self.line)

	if c.gcFreeze != nil {
		c.gcFreeze(n.w)
	}
	n.sublist = &n.w.glwPropSubscriptions

	r := glwViewEvalBlock(b, &n, nil)

	if c.gcThaw != nil {
		c.gcThaw(n.w)
	}
	if n.w.glwRoot.grPendingFocus != nil {
		glwFocusCheckPending(n.w)
	}
	if r != 0 {
		return -1
	}
	return 0
}

// C: glwf_resolve_widget_class (glw_view_eval.c:2969)
func glwfResolveWidgetClass(gr *glwRoot, ei *errorinfoT, t *Token) *Token {
	a := t.next.next
	if a.typ != tokenIdentifier {
		glwViewSeterr(ei, t, "widget: Invalid first argument, expected widget class")
		return nil
	}
	c := glwClassFindByName(miscpkg.RstrGet(a.tRstring))
	if c == nil {
		glwViewSeterr(ei, t, "widget: No such widget: %s", miscpkg.RstrGet(a.tRstring))
		return nil
	}
	t.tFuncArg = c

	r := t.next.next.next.next
	glwViewTokenFree(gr, t.next.next.next)
	glwViewTokenFree(gr, t.next.next)
	t.next.next = r
	return r
}

// C: glwf_cloner (glw_view_eval.c:3004)
func glwfCloner(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	var a, b, c *Token
	if argc > 0 {
		a = argv[0]
	}
	if argc > 1 {
		b = argv[1]
	}
	if argc > 2 {
		c = argv[2]
	}
	parent := ec.w

	if parent == nil {
		return glwViewSeterr(ec.ei, self, "Cloner can not be created in this scope")
	}
	if a = tokenResolveEx(ec, a, gpsCloner); a == nil {
		return -1
	}
	if b.typ != tokenIdentifier {
		return glwViewSeterr(ec.ei, self, "cloner: Invalid second argument, expected widget class")
	}
	cl := glwClassFindByName(miscpkg.RstrGet(b.tRstring))
	if cl == nil {
		return glwViewSeterr(ec.ei, self, "cloner: Invalid class")
	}
	if c.typ != tokenBlock {
		return glwViewSeterr(ec.ei, self, "cloner: Invalid third argument, expected block")
	}
	if parent.glwClass.gcFlags&glwCanHideChilds == 0 {
		panic(fmt.Sprintf("Parent %s can not hide childs, cannot clone", parent.glwClass.gcName))
	}

	if self.tExtra == nil {
		dummy := glwClassFindByName("dummy")
		self.tExtra = glwCreate(ec.gr, dummy, parent, nil, nil, ec.scope, self.file, self.line)
		glwHide(self.tExtra.(*Glw))
	}

	// Destroy any previous cloned entries
	for w := glwTAILQPrev(self.tExtra.(*Glw)); w != nil && w.glwClone != nil; w = glwTAILQPrev(w) {
		glwDeps.pm.TagClear(w.glwClone.cProp, w.glwClone.cSc)
		cloneFree(ec.gr, w.glwClone)
	}

	if a.typ == tokenDirectory {
		sc := (*subCloner)(unsafe.Pointer(a.tPropsubr))
		sc.scAnchor = self.tExtra.(*Glw)
		clonerCleanup(ec.gr, sc)
		sc.scClonerBody = glwViewCloneChain(ec.gr, c, nil)
		sc.scClonerClass = cl

		for gpsp := sc.scPending.tqhFirst; gpsp != nil; {
			next := gpsp.gpspLinkNext
			f := 0
			if gpsp.gpspProp == sc.scPendingSelect {
				f = 0x1 /* PROP_ADD_SELECTED */
			}
			clonerAddChild0(sc, gpsp.gpspProp, nil, parent, ec.ei, f)
			glwDeps.pm.TagClear(gpsp.gpspProp, &sc.scPending)
			glwDeps.pm.RefDec(gpsp.gpspProp)
			gpsp = next
		}
		sc.scPending.tqhFirst = nil
		sc.scPending.tqhLast = &sc.scPending.tqhFirst
		sc.scPendingSelect = nil
	}
	return 0
}

// C: glwf_style0 (glw_view_eval.c:3092)
func glwfStyle0(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint, inherit int) int {
	a := argv[0]
	b := argv[1]

	if a.typ != tokenIdentifier {
		return glwViewSeterr(ec.ei, a, "style: Invalid second argument, expected identifier")
	}
	if b.typ != tokenBlock {
		return glwViewSeterr(ec.ei, b, "style: Invalid second argument, expected block")
	}

	var n glwViewEvalContext
	n.scope = ec.scope
	n.ei = ec.ei
	n.gr = ec.gr

	gs := glwStyleCreate(ec.w, a.tRstring, self.file, self.line, inherit)
	n.w = &gs.w
	n.sublist = &n.w.glwPropSubscriptions

	var nonpure *Token
	r := glwViewEvalBlock(b, &n, &nonpure)

	if nonpure != nil {
		glwStyleAttachRpns(gs, nonpure)
	}
	if r == 0 {
		gss := glwStylesetAdd(ec.w.glwStyles, gs)
		glwStylesetRelease(ec.w.glwStyles)
		ec.w.glwStyles = gss
	}
	if r != 0 {
		return -1
	}
	return 0
}

// C: glwf_style / glwf_newstyle (glw_view_eval.c:3145-3163)
func glwfStyle(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	return glwfStyle0(ec, self, argv, argc, 1)
}

func glwfNewstyle(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	return glwfStyle0(ec, self, argv, argc, 0)
}

// C: glwf_space (glw_view_eval.c:3171)
func glwfSpace(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := argv[0]
	if ec.w == nil {
		return glwViewSeterr(ec.ei, self, "Widget can not be created in this scope")
	}
	if a = tokenResolve(ec, a); a == nil {
		return -1
	}
	dummy := glwClassFindByName("dummy")
	w := glwCreate(ec.gr, dummy, ec.w, nil, nil, ec.scope, self.file, self.line)
	glwConfConstraints(w, 0, 0, token2float(ec, a), glwConstraintConfW)
	return 0
}

// C: glwf_coreAttach_dtor (glw_view_eval.c:2915)
func glwfCoreAttachDtor(gr *glwRoot, self *Token) {
	if self.tExtra != nil {
		propProxyClose(self.tExtra)
	}
	glwTexFlushAll(gr)
}

// C: glwf_coreAttach (glw_view_eval.c:2927)
// STPP client is wired: propProxyConnect → propproxy.NewProxyConnection
// (pkg/prop/prop_proxy.go), the port of prop_proxy_connect.
func glwfCoreAttach(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a1 := argv[0]
	a2 := argv[1]
	a3 := argv[2]

	if a1 = tokenResolve(ec, a1); a1 == nil {
		return -1
	}
	if a1.typ != tokenRstring {
		return 0
	}
	if a2 = resolvePropertyName2(ec, a2); a2 == nil {
		return -1
	}
	if a3.typ != tokenBlock {
		return glwViewSeterr(ec.ei, self, "coreAttach: Invalid third argument, expected block")
	}
	if self.tExtra == nil {
		self.tExtra = propProxyConnect(miscpkg.RstrGet(a1.tRstring), a2.tProp)
	} else {
		return 0
	}

	n := *ec
	scope := glwScopeDup(ec.scope, 1<<glwRootCore)
	scope.gsRoots[glwRootCore].P = propProxyGetRoot(self.tExtra)
	scope.gsBackend = propProxyGetBackend(self.tExtra)

	n.scope = scope
	n.dynamicEval = 0
	r := glwViewEvalBlock(a3, &n, nil)
	ec.dynamicEval |= n.dynamicEval
	glwScopeRelease(scope)
	if r != 0 {
		return -1
	}
	return 0
}

// C: glwf_navOpen (glw_view_eval.c:3546)
func glwfNavOpen(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	var url, view, how, purl string
	var itemModel, parentModel *propcore.Prop

	if argc < 1 || argc > 6 {
		return glwViewSeterr(ec.ei, self, "navOpen(): Invalid number of args")
	}
	a := tokenResolve(ec, argv[0])
	if a == nil {
		return -1
	}
	switch a.typ {
	case tokenRstring:
		url = miscpkg.RstrGet(a.tRstring)
	case tokenCstring:
		url = a.tCstring
	case tokenURI:
		url = miscpkg.RstrGet(a.tURI)
	}

	if argc > 1 {
		b := tokenResolve(ec, argv[1])
		if b == nil {
			return -1
		}
		switch b.typ {
		case tokenVoid:
		case tokenRstring:
			view = miscpkg.RstrGet(b.tRstring)
		default:
			return glwViewSeterr(ec.ei, b, "navOpen(): Second argument is not a string or (void)")
		}
	}
	if argc > 2 && argv[2].typ != tokenVoid {
		c := resolvePropertyName2(ec, argv[2])
		if c == nil {
			return -1
		}
		if c.typ != tokenPropertyRef {
			return glwViewSeterr(ec.ei, c, "navOpen(): Third argument (itemModel) is not a property")
		}
		itemModel = c.tProp
	}
	if argc > 3 && argv[3].typ != tokenVoid {
		d := resolvePropertyName2(ec, argv[3])
		if d == nil {
			return -1
		}
		if d.typ != tokenPropertyRef {
			return glwViewSeterr(ec.ei, d, "navOpen(): Fourth argument (parentModel) is not a property")
		}
		parentModel = d.tProp
	}
	if argc > 4 {
		e := tokenResolve(ec, argv[4])
		if e == nil {
			return -1
		}
		switch e.typ {
		case tokenVoid:
		case tokenRstring:
			how = miscpkg.RstrGet(e.tRstring)
		default:
			return glwViewSeterr(ec.ei, e, "navOpen(): Fifth argument (how) is not a string or (void)")
		}
	}
	if argc > 5 {
		f := tokenResolve(ec, argv[5])
		if f == nil {
			return -1
		}
		switch f.typ {
		case tokenVoid:
		case tokenRstring:
			purl = miscpkg.RstrGet(f.tRstring)
		default:
			return glwViewSeterr(ec.ei, f, "navOpen(): Sixth argument (parent url) is not a string or (void)")
		}
	}

	r := evalAlloc(self, ec, tokenEvent)
	r.tEvent = glwDeps.em.CreateOpenURLArgs(&eventpkg.EventOpenURLArgs{
		URL:         url,
		View:        view,
		How:         how,
		ParentURL:   purl,
		ItemModel:   itemModel,
		ParentModel: parentModel,
	}).AsEvent()
	r.tEvent.Nav = glwDeps.pm.RefInc(ec.w.glwRoot.grPropNav)
	evalPush(ec, r)
	return 0
}

// C: glwf_changed_extra_t (glw_view_eval.c:3965)
type glwfChangedExtra struct {
	deadline   int64
	typ        tokenType
	rstr       *miscpkg.Rstr
	value      float32
	i          int
	cstr       string
	prop       *propcore.Prop
	transition bool
}

// C: glwf_changed (glw_view_eval.c:3985)
func glwfChanged(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	gr := ec.w.glwRoot
	e, _ := self.tExtra.(*glwfChangedExtra)
	change := 0
	suppFirst := 0

	if argc < 2 || argc > 3 {
		return glwViewSeterr(ec.ei, self, "changed(): Invalid number of arguments")
	}
	a := tokenResolve(ec, argv[0])
	if a == nil {
		return -1
	}
	b := tokenResolve(ec, argv[1])
	if b == nil {
		return -1
	}
	if argc == 3 {
		c := tokenResolve(ec, argv[2])
		if c == nil {
			return -1
		}
		if token2bool(c) {
			suppFirst = 1
		}
	}

	if a.typ != tokenFloat && a.typ != tokenRstring && a.typ != tokenVoid &&
		a.typ != tokenCstring && a.typ != tokenInt && a.typ != tokenPropertyRef {
		return glwViewSeterr(ec.ei, self, "Invalid first operand (%s) to changed()", token2name(a))
	}
	if b.typ != tokenFloat {
		return glwViewSeterr(ec.ei, self, "Invalid second operand to changed(), expected scalar")
	}

	if a.typ != e.typ {
		if e.typ == tokenRstring {
			miscpkg.RstrRelease(e.rstr)
		} else if e.typ == tokenPropertyRef {
			glwDeps.pm.RefDec(e.prop)
		}
		e.typ = a.typ
		switch a.typ {
		case tokenRstring:
			e.rstr = miscpkg.RstrDup(a.tRstring)
		case tokenCstring:
			e.cstr = a.tCstring
		case tokenFloat:
			e.value = a.tFloat
		case tokenInt:
			e.i = a.tInt
		case tokenPropertyRef:
			e.prop = glwDeps.pm.RefInc(a.tProp)
		}
		change = 1
	} else {
		switch a.typ {
		case tokenRstring:
			if miscpkg.RstrGet(e.rstr) != miscpkg.RstrGet(a.tRstring) {
				change = 1
				miscpkg.RstrRelease(e.rstr)
				e.rstr = miscpkg.RstrDup(a.tRstring)
			}
		case tokenCstring:
			if e.cstr != a.tCstring {
				change = 1
				e.cstr = a.tCstring
			}
		case tokenFloat:
			if e.value != a.tFloat {
				e.value = a.tFloat
				change = 1
			}
		case tokenInt:
			if e.i != a.tInt {
				e.i = a.tInt
				change = 1
			}
		case tokenPropertyRef:
			if e.prop != a.tProp {
				glwDeps.pm.RefDec(e.prop)
				e.prop = glwDeps.pm.RefInc(a.tProp)
				change = 1
			}
		}
	}

	if change == 1 {
		if e.transition || suppFirst == 0 {
			e.deadline = int64(b.tFloat*1000000.0) + gr.grFrameStart
		}
		e.transition = true
	}

	r := evalAlloc(self, ec, tokenFloat)
	if e.deadline > gr.grFrameStart {
		r.tFloat = 1
		ec.dynamicEval |= GLW_VIEW_EVAL_LAYOUT
		glwScheduleRefresh(gr, e.deadline)
	}
	evalPush(ec, r)
	return 0
}

// C: glwf_changed_ctor/dtor (glw_view_eval.c:4142-4163)
func glwfChangedCtor(self *Token) {
	self.tExtra = &glwfChangedExtra{}
}

func glwfChangedDtor(gr *glwRoot, self *Token) {
	e, _ := self.tExtra.(*glwfChangedExtra)
	if e == nil {
		return
	}
	if e.typ == tokenRstring {
		miscpkg.RstrRelease(e.rstr)
	} else if e.typ == tokenPropertyRef {
		glwDeps.pm.RefDec(e.prop)
	}
}

// C: glwf_iir (glw_view_eval.c:4168)
func glwfIir(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	var f float32
	springmode := false

	if argc < 2 || argc > 3 {
		glwViewSeterr(ec.ei, self, "iir(): Invalid number of arguments: %d", argc)
		return -1
	}
	a := tokenResolve(ec, argv[0])
	if a == nil {
		return -1
	}
	b := tokenResolve(ec, argv[1])
	if b == nil {
		return -1
	}
	if argc == 3 {
		c := tokenResolve(ec, argv[2])
		if c == nil {
			return -1
		}
		springmode = token2bool(c)
	}
	if a.typ != tokenFloat && a.typ != tokenInt && a.typ != tokenRstring && a.typ != tokenVoid {
		return glwViewSeterr(ec.ei, self, "Invalid first operand to iir()")
	}
	if a.typ == tokenRstring || a.typ == tokenVoid {
		f = 0
	} else if a.typ == tokenInt {
		f = float32(a.tInt)
	} else {
		f = a.tFloat
	}
	if b.typ != tokenFloat {
		return glwViewSeterr(ec.ei, self, "Invalid second operand to iir()")
	}

	if ec.w.glwFlags&glwActive != 0 || ec.mask&GLW_VIEW_EVAL_ACTIVE == 0 {
		x := int(self.tExtraFloat * 1000.)
		if springmode && f > self.tExtraFloat {
			self.tExtraFloat = f
		} else {
			glwLp(&self.tExtraFloat, ec.w.glwRoot, f, 1.0/b.tFloat)
		}
		y := int(self.tExtraFloat * 1000.)
		if x != y {
			ec.dynamicEval |= GLW_VIEW_EVAL_LAYOUT | GLW_VIEW_EVAL_ACTIVE
		} else {
			self.tExtraFloat = f
		}
	} else {
		self.tExtraFloat = f
	}

	r := evalAlloc(self, ec, tokenFloat)
	r.tFloat = self.tExtraFloat
	evalPush(ec, r)
	return 0
}

// C: glw_scurve_extra_t (glw_view_eval.c:4245)
type glwScurveExtra struct {
	deadline  int64
	starttime int64
	total     int
	startval  float32
	current   float32
	target    float32
	timeUp    float32
	timeDown  float32
}

// C: glwf_scurve (glw_view_eval.c:4262)
func glwfScurve(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	gr := ec.w.glwRoot
	s, _ := self.tExtra.(*glwScurveExtra)

	if argc < 2 {
		return glwViewSeterr(ec.ei, self, "scurve() requires at least two arguments")
	}
	a := tokenResolve(ec, argv[0])
	if a == nil {
		return -1
	}
	b := tokenResolve(ec, argv[1])
	if b == nil {
		return -1
	}
	var c *Token
	if argc > 2 {
		if c = tokenResolve(ec, argv[2]); c == nil {
			return -1
		}
	}
	f := token2float(ec, a)
	tup := token2float(ec, b)
	tdown := token2float(ec, b)
	if c != nil {
		tdown = token2float(ec, c)
	}

	if s.target != f || s.timeUp != tup || s.timeDown != tdown {
		s.startval = s.target
		s.target = f
		s.timeUp = tup
		s.timeDown = tdown
		t := tup
		if s.target < s.startval {
			t = tdown
		}
		s.starttime = gr.grFrameStart
		s.total = int(1000000.0 * t)
		if s.total == 0 {
			s.total = 1
		}
		s.deadline = gr.grFrameStart + int64(s.total)
	}

	if gr.grFrameStart < s.deadline {
		cur := gr.grFrameStart - s.starttime
		x := float32(cur) / float32(s.total)
		v := glwS(x)
		s.current = glwLerp(v, s.startval, s.target)
		ec.dynamicEval |= GLW_VIEW_EVAL_LAYOUT
		glwNeedRefresh(gr, 0)
	} else {
		s.current = s.target
	}

	r := evalAlloc(self, ec, tokenFloat)
	r.tFloat = s.current
	evalPush(ec, r)
	return 0
}

func glwfScurveCtor(self *Token) { self.tExtra = &glwScurveExtra{} }

func glwfScurveDtor(gr *glwRoot, self *Token) { self.tExtra = nil }

// C: glwf_createchild (glw_view_eval.c:4874)
func glwfCreatechild(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := argv[0]
	if a.typ != tokenPropertyName {
		return 0
	}
	var pname16 [16]string
	glwPropnameToArray(&pname16, a)
	p := propGetByNameTags(glwPropnameSlice(&pname16), 1, ec, self)
	if p != nil {
		glwDeps.pm.RequestNewChild(p)
		glwDeps.pm.RefDec(p)
	}
	return 0
}

// C: glwf_delete (glw_view_eval.c:4904)
func glwfDelete(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := argv[0]
	switch a.typ {
	case tokenPropertyName:
		follow := 1
		if a.tFlags&tokenFCanonicalPath != 0 {
			follow = 0
		}
		if resolvePropertyName(ec, a, follow) != 0 {
			return -1
		}
	case tokenPropertyRef:
	default:
		return glwViewSeterr(ec.ei, a, "Invalid operand to delete()")
	}
	a.tProp.RequestDelete()
	return 0
}

// C: glwf_isFocused / isHovered / isPressed (glw_view_eval.c:4932-4987)
func glwfIsFocused(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	ec.dynamicEval |= GLW_VIEW_EVAL_FHP_CHANGE
	r := evalAlloc(self, ec, tokenInt)
	if glwIsFocused(ec.w) && ec.w.glwRoot.grKeyboardMode != 0 {
		r.tInt = 1
	}
	evalPush(ec, r)
	return 0
}

func glwfIsHovered(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	ec.dynamicEval |= GLW_VIEW_EVAL_FHP_CHANGE
	r := evalAlloc(self, ec, tokenInt)
	if glwIsHovered(ec.w) {
		r.tInt = 1
	}
	evalPush(ec, r)
	return 0
}

func glwfIsPressed(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	ec.dynamicEval |= GLW_VIEW_EVAL_FHP_CHANGE
	r := evalAlloc(self, ec, tokenInt)
	if glwIsPressed(ec.w) {
		r.tInt = 1
	}
	evalPush(ec, r)
	return 0
}

// C: glwf_focusedChild (glw_view_eval.c:4994)
func glwfFocusedChild(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	w := ec.w
	if w == nil {
		return glwViewSeterr(ec.ei, self, "focusedChild() without widget")
	}
	ec.dynamicEval |= GLW_VIEW_EVAL_OTHER
	c := w.glwFocused
	var r *Token
	if c != nil && c.glwOriginatingProp != nil {
		r = evalAlloc(self, ec, tokenPropertyRef)
		r.tProp = glwDeps.pm.RefInc(c.glwOriginatingProp)
	} else {
		r = evalAlloc(self, ec, tokenVoid)
	}
	evalPush(ec, r)
	return 0
}

// C: glwf_focusedClone (glw_view_eval.c:5025)
func glwfFocusedClone(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	w := ec.w
	if w == nil {
		return glwViewSeterr(ec.ei, self, "focusedClone() without widget")
	}
	ec.dynamicEval |= GLW_VIEW_EVAL_OTHER
	c := w.glwFocused
	var r *Token
	if c != nil && c.glwClone != nil {
		r = evalAlloc(self, ec, tokenPropertyRef)
		r.tProp = glwDeps.pm.RefInc(c.glwClone.cCloneRoot)
	} else {
		r = evalAlloc(self, ec, tokenVoid)
	}
	evalPush(ec, r)
	return 0
}

// C: glwf_focusedIndex (glw_view_eval.c:5057)
func glwfFocusedIndex(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	w := ec.w
	ec.dynamicEval |= GLW_VIEW_EVAL_OTHER
	var r *Token
	if w.glwFocused == nil || w.glwFocused.glwClone == nil {
		r = evalAlloc(self, ec, tokenVoid)
		evalPush(ec, r)
		return 0
	}
	c := w.glwFocused.glwClone
	sc := c.cSc
	if !sc.scPositionsValid {
		clonerResequence(sc)
	}
	r = evalAlloc(self, ec, tokenInt)
	r.tInt = int(c.cPos)
	evalPush(ec, r)
	return 0
}

// C: glwf_getCaption (glw_view_eval.c:5087)
func glwfGetCaption(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := tokenResolve(ec, argv[0])
	if a == nil {
		return -1
	}
	var r *Token
	if a.typ == tokenRstring {
		w := glwFindNeighbour(ec.w, miscpkg.RstrGet(a.tRstring))
		if w != nil && w.glwClass.gcGetText != nil {
			r = evalAlloc(self, ec, tokenRstring)
			r.tRstring = miscpkg.RstrAllocStr(w.glwClass.gcGetText(w))
			evalPush(ec, r)
			return 0
		}
	}
	r = evalAlloc(self, ec, tokenVoid)
	evalPush(ec, r)
	return 0
}

// C: glwf_bind (glw_view_eval.c:5119)
func glwfBind(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := argv[0]
	if a != nil && a.typ == tokenPropertyName {
		var pname16 [16]string
		glwPropnameToArray(&pname16, a)
		if ec.w.glwClass.gcBindToProperty != nil {
			ec.w.glwClass.gcBindToProperty(ec.w, ec.scope, glwPropnameSlice(&pname16))
		}
	} else if a != nil && a.typ == tokenRstring {
		ec.w.glwClass.gcBindToId(ec.w, miscpkg.RstrGet(a.tRstring))
	} else {
		if ec.w.glwClass.gcBindToProperty != nil {
			ec.w.glwClass.gcBindToProperty(ec.w, nil, nil)
		}
	}
	return 0
}

// C: glwf_delta_extra_t (glw_view_eval.c:5150)
type glwfDeltaExtra struct {
	p *propcore.Prop
	f float32
}

// C: glwf_delta (glw_view_eval.c:5160)
func glwfDelta(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	de, _ := self.tExtra.(*glwfDeltaExtra)
	a := argv[0]
	b := argv[1]
	var f float32

	if a = resolvePropertyName2(ec, a); a == nil {
		return -1
	}
	if b = tokenResolve(ec, b); b == nil {
		return -1
	}
	switch b.typ {
	case tokenFloat:
		f = b.tFloat
	case tokenInt:
		f = float32(b.tInt)
	case tokenRstring:
		if len(miscpkg.RstrGet(b.tRstring)) > 0 {
			f = 1
		}
	case tokenURI:
		if len(miscpkg.RstrGet(b.tURITitle)) > 0 {
			f = 1
		}
	}
	p := glwDeps.pm.RefInc(a.tProp)
	ec.dynamicEval |= GLW_VIEW_EVAL_KEEP

	if p == de.p && de.f+f == 0 {
		glwDeps.pm.RefDec(p)
		return 0
	}
	p.AddFloat(f)
	if de.p != nil {
		de.p.AddFloat(de.f)
		glwDeps.pm.RefDec(de.p)
	}
	de.f = -f
	de.p = p
	return 0
}

func glwfDeltaCtor(self *Token) { self.tExtra = &glwfDeltaExtra{} }

func glwfDeltaDtor(gr *glwRoot, self *Token) {
	de, _ := self.tExtra.(*glwfDeltaExtra)
	if de == nil {
		return
	}
	if de.p != nil {
		de.p.AddFloat(de.f)
		glwDeps.pm.RefDec(de.p)
	}
}

// C: glwf_set (glw_view_eval.c:5192)
func glwfSet(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := argv[0]
	b := argv[1]

	if a = resolvePropertyName2(ec, a); a == nil {
		return -1
	}
	if b = tokenResolve(ec, b); b == nil {
		return -1
	}
	if self.tExtra != nil {
		glwDeps.pm.RefDec(self.tExtra.(*propcore.Prop))
	}
	p := glwDeps.pm.RefInc(a.tProp)
	self.tExtra = p

	switch b.typ {
	case tokenFloat:
		glwDeps.pm.SetFloatEx(p, nil, b.tFloat)
	case tokenInt:
		glwDeps.pm.SetIntEx(p, nil, b.tInt)
	case tokenCstring:
		glwDeps.pm.SetStringEx(p, nil, b.tCstring, propcore.StringType(b.tRstrType))
	case tokenRstring:
		glwDeps.pm.SetStringEx(p, nil, miscpkg.RstrGet(b.tRstring), propcore.StringType(b.tRstrType))
	case tokenURI:
		glwDeps.pm.SetURIEx(p, nil, miscpkg.RstrGet(b.tURITitle), miscpkg.RstrGet(b.tURI))
	default:
		glwDeps.pm.SetVoidEx(p, nil)
	}
	ec.dynamicEval |= GLW_VIEW_EVAL_KEEP
	return 0
}

// C: glwf_set_dtor (glw_view_eval.c:5241)
func glwfSetDtor(gr *glwRoot, self *Token) {
	p, _ := self.tExtra.(*propcore.Prop)
	if p == nil {
		return
	}
	glwDeps.pm.SetVoidEx(p, nil)
	glwDeps.pm.RefDec(p)
}

// C: glwf_isVisible (glw_view_eval.c:5255)
func glwfIsVisible(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	ec.dynamicEval |= GLW_VIEW_EVAL_ACTIVE
	r := evalAlloc(self, ec, tokenInt)
	if ec.w.glwFlags&glwActive != 0 {
		r.tInt = 1
	}
	evalPush(ec, r)
	return 0
}

// C: glwf_isPreloaded (glw_view_eval.c:5274)
func glwfIsPreloaded(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	ec.dynamicEval |= GLW_VIEW_EVAL_ACTIVE
	r := evalAlloc(self, ec, tokenInt)
	if ec.w.glwFlags&glwPreloaded != 0 {
		r.tInt = 1
	}
	evalPush(ec, r)
	return 0
}

// C: glwf_canScroll (glw_view_eval.c:5294)
func glwfCanScroll(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	ec.dynamicEval |= GLW_VIEW_EVAL_OTHER
	r := evalAlloc(self, ec, tokenInt)
	if ec.w.glwFlags&glwCanScroll != 0 {
		r.tInt = 1
	}
	evalPush(ec, r)
	return 0
}

// C: glwf_select (glw_view_eval.c:5314)
func glwfSelect(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := tokenResolve(ec, argv[0])
	if a == nil {
		return -1
	}
	if token2bool(a) {
		evalPush(ec, argv[1])
	} else {
		evalPush(ec, argv[2])
	}
	return 0
}

// C: glwf_trace (glw_view_eval.c:5334)
func glwfTrace(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	ec.debug++
	a := tokenResolve(ec, argv[0])
	if a == nil {
		ec.debug--
		return -1
	}
	b := tokenResolve(ec, argv[1])
	if b == nil {
		ec.debug--
		return -1
	}
	ec.debug--
	if a.typ != tokenRstring {
		return 0
	}
	prefix := miscpkg.RstrGet(a.tRstring)
	switch b.typ {
	case tokenRstring, tokenIdentifier:
		glwDeps.ts.Trace(tracepkg.TRACE_DEBUG, "GLW", "%s: %s", prefix, miscpkg.RstrGet(b.tRstring))
	case tokenCstring:
		glwDeps.ts.Trace(tracepkg.TRACE_DEBUG, "GLW", "%s: %s", prefix, b.tCstring)
	case tokenFloat:
		glwDeps.ts.Trace(tracepkg.TRACE_DEBUG, "GLW", "%s: %f", prefix, b.tFloat)
	case tokenInt:
		glwDeps.ts.Trace(tracepkg.TRACE_DEBUG, "GLW", "%s: %d", prefix, b.tInt)
	case tokenVoid:
		glwDeps.ts.Trace(tracepkg.TRACE_DEBUG, "GLW", "%s: (void)", prefix)
	case tokenURI:
		glwDeps.ts.Trace(tracepkg.TRACE_DEBUG, "GLW", "%s: %s %s (URI)", prefix,
			miscpkg.RstrGet(b.tURITitle), miscpkg.RstrGet(b.tURI))
	default:
		glwDeps.ts.Trace(tracepkg.TRACE_DEBUG, "GLW", "%s: %s", prefix, token2name(b))
	}
	return 0
}

// C: glwf_browse_extra_t (glw_view_eval.c:5405)
type glwfBrowseExtra struct {
	p   *propcore.Prop
	url *miscpkg.Rstr
}

func glwfBrowseCtor(self *Token) { self.tExtra = &glwfBrowseExtra{} }

func glwfBrowseDtor(gr *glwRoot, self *Token) {
	be, _ := self.tExtra.(*glwfBrowseExtra)
	if be == nil {
		return
	}
	if be.p != nil {
		glwDeps.pm.Destroy(be.p)
	}
	miscpkg.RstrRelease(be.url)
}

// C: glwf_browse (glw_view_eval.c:5432)
func glwfBrowse(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := argv[0]
	be, _ := self.tExtra.(*glwfBrowseExtra)

	if a = tokenResolve(ec, a); a == nil {
		return -1
	}
	var url *miscpkg.Rstr
	switch a.typ {
	case tokenRstring:
		url = a.tRstring
	case tokenURI:
		url = a.tURI
	default:
		return glwViewSeterr(ec.ei, a, "browse(): Invalid first arg (%s)", token2name(a))
	}

	if be.url == nil || miscpkg.RstrGet(be.url) != miscpkg.RstrGet(url) {
		miscpkg.RstrRelease(be.url)
		be.url = nil
		if be.p != nil {
			glwDeps.pm.Destroy(be.p)
		}
		be.p = glwDeps.pm.CreateRoot("")
		if glwDeps.bs.Open(be.p, miscpkg.RstrGet(url), false) != nil {
			glwDeps.pm.Destroy(be.p)
			be.p = nil
			return glwViewSeterr(ec.ei, a, "browse(%s): open failed", miscpkg.RstrGet(url))
		}
		be.url = miscpkg.RstrDup(url)
	}

	r := evalAlloc(self, ec, tokenPropertyRef)
	r.tProp = glwDeps.pm.RefInc(be.p)
	ec.dynamicEval |= GLW_VIEW_EVAL_KEEP
	evalPush(ec, r)
	return 0
}

// C: glwf_null_ctor (glw_view_eval.c:5469)
func glwfNullCtor(self *Token) { self.tExtra = nil }

// C: glwf_isLink (glw_view_eval.c:5479)
func glwfIsLink(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := tokenResolve(ec, argv[0])
	if a == nil {
		return -1
	}
	r := evalAlloc(self, ec, tokenInt)
	if a.typ == tokenURI {
		r.tInt = 1
	}
	evalPush(ec, r)
	return 0
}

// C: glwf_makeUri (glw_view_eval.c:5496)
func glwfMakeUri(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := tokenResolve(ec, argv[0])
	if a == nil {
		return -1
	}
	b := tokenResolve(ec, argv[1])
	if b == nil {
		return -1
	}
	var r *Token
	if a.typ != tokenRstring || b.typ != tokenRstring {
		r = evalAlloc(self, ec, tokenVoid)
	} else {
		r = evalAlloc(self, ec, tokenURI)
		r.tURITitle = miscpkg.RstrDup(a.tRstring)
		r.tURI = miscpkg.RstrDup(b.tRstring)
	}
	evalPush(ec, r)
	return 0
}

// C: glwf_delay_extra_t (glw_view_eval.c:5609)
type glwfDelayExtra struct {
	curval   float32
	nextval  float32
	deadline int64
	waiting  bool
}

// C: glwf_delay (glw_view_eval.c:5623)
func glwfDelay(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	gr := ec.w.glwRoot
	e, _ := self.tExtra.(*glwfDelayExtra)

	a := tokenResolve(ec, argv[0])
	if a == nil {
		return -1
	}
	b := tokenResolve(ec, argv[1])
	if b == nil {
		return -1
	}
	c := tokenResolve(ec, argv[2])
	if c == nil {
		return -1
	}
	f := token2float(ec, a)

	if f != e.nextval {
		dt := c
		if f >= e.curval {
			dt = b
		}
		e.deadline = int64(token2float(ec, dt)*1000000.0) + gr.grFrameStart
		e.nextval = f
	}
	if gr.grFrameStart < e.deadline {
		e.waiting = true
		f = e.curval
		ec.dynamicEval |= GLW_VIEW_EVAL_LAYOUT
		glwScheduleRefresh(gr, e.deadline)
	} else {
		e.waiting = false
		e.curval = e.nextval
		f = e.curval
	}
	r := evalAlloc(self, ec, tokenFloat)
	r.tFloat = f
	evalPush(ec, r)
	return 0
}

func glwfDelayCtor(self *Token) { self.tExtra = &glwfDelayExtra{} }

func glwfDelayDtor(gr *glwRoot, self *Token) { self.tExtra = nil }

// C: glwf_isLoading / isLoaded / isError (glw_view_eval.c:5687-5751)
func glwfIsLoading(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	r := evalAlloc(self, ec, tokenInt)
	gc := ec.w.glwClass
	if gc.gcStatus != nil && gc.gcStatus(ec.w) == glwStatusLoading {
		r.tInt = 1
	}
	ec.dynamicEval |= GLW_VIEW_EVAL_OTHER
	evalPush(ec, r)
	return 0
}

func glwfIsLoaded(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	r := evalAlloc(self, ec, tokenInt)
	gc := ec.w.glwClass
	if gc.gcStatus == nil || gc.gcStatus(ec.w) == glwStatusLoaded {
		r.tInt = 1
	}
	ec.dynamicEval |= GLW_VIEW_EVAL_OTHER
	evalPush(ec, r)
	return 0
}

func glwfIsError(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	r := evalAlloc(self, ec, tokenInt)
	gc := ec.w.glwClass
	if gc.gcStatus != nil && gc.gcStatus(ec.w) == glwStatusError {
		r.tInt = 1
	}
	ec.dynamicEval |= GLW_VIEW_EVAL_OTHER
	evalPush(ec, r)
	return 0
}

// C: glwf_primary_color (glw_view_eval.c:5760)
func glwfPrimaryColor(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	gc := ec.w.glwClass
	var r *Token
	if gc.gcPrimaryColor == nil {
		r = evalAlloc(self, ec, tokenVoid)
	} else {
		var rgb [3]float32
		valid := gc.gcPrimaryColor(ec.w, &rgb[0])
		if valid != 0 && rgb[0] != 0 && rgb[1] != 0 && rgb[2] != 0 {
			r = evalAlloc(self, ec, tokenVectorFloat)
			r.tElements = 3
			r.tFloatVector[0] = rgb[0]
			r.tFloatVector[1] = rgb[1]
			r.tFloatVector[2] = rgb[2]
		} else {
			r = evalAlloc(self, ec, tokenVoid)
		}
		ec.dynamicEval |= GLW_VIEW_EVAL_OTHER
	}
	evalPush(ec, r)
	return 0
}

// C: glwf_suggestFocus (glw_view_eval.c:5794)
func glwfSuggestFocus(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := tokenResolve(ec, argv[0])
	if a == nil {
		return -1
	}
	if token2bool(a) {
		glwFocusSuggest(ec.w)
	}
	return 0
}

// C: glwf_count (glw_view_eval.c:5811)
func glwfCount(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := tokenResolveEx(ec, argv[0], gpsCounter)
	if a == nil {
		return -1
	}
	evalPush(ec, a)
	return 0
}

// C: glwf_vectorize (glw_view_eval.c:5826)
func glwfVectorize(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := tokenResolveEx(ec, argv[0], gpsVectorizer)
	if a == nil {
		return -1
	}
	evalPush(ec, a)
	return 0
}

// C: glwf_propGrouper_dtor / glwf_propGrouper (glw_view_eval.c:5842-5890)
func glwfPropGrouperDtor(gr *glwRoot, self *Token) {
	if self.tExtra != nil {
		self.tExtra.(interface{ Destroy() }).Destroy()
	}
}

func glwfPropGrouper(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := resolvePropertyName2(ec, argv[0])
	if a == nil {
		return -1
	}
	b := tokenResolve(ec, argv[1])
	if b == nil {
		return -1
	}
	if b.typ != tokenRstring {
		return glwViewSeterr(ec.ei, a, "propGrouper(): Second argument is not a string")
	}
	if self.tExtra != nil {
		self.tExtra.(interface{ Destroy() }).Destroy()
	}
	r := evalAlloc(self, ec, tokenPropertyRef)
	r.tProp = glwDeps.pm.RefInc(glwDeps.pm.CreateRoot(""))
	ec.dynamicEval |= GLW_VIEW_EVAL_KEEP
	evalPush(ec, r)
	self.tExtra = glwDeps.pm.PropGrouperCreate(r.tProp, a.tProp,
		miscpkg.RstrGet(b.tRstring), propcore.PropGrouperTakeDstOwnership)
	return 0
}

// C: glwf_propSorter_dtor / glwf_propSorter (glw_view_eval.c:5897-6067)
func glwfPropSorterDtor(gr *glwRoot, self *Token) {
	if self.tExtra != nil {
		propcore.PropNFRelease(self.tExtra.(*propcore.PropNF))
	}
}

func glwfPropSorter(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	sortidx := 0
	if argc < 1 {
		return glwViewSeterr(ec.ei, self, "propSorter(): Too few arguments")
	}
	a := resolvePropertyName2(ec, argv[0])
	if a == nil {
		return -1
	}
	if self.tExtra != nil {
		propcore.PropNFRelease(self.tExtra.(*propcore.PropNF))
	}
	r := evalAlloc(self, ec, tokenPropertyRef)
	r.tProp = glwDeps.pm.RefInc(glwDeps.pm.CreateRoot(""))
	ec.dynamicEval |= GLW_VIEW_EVAL_KEEP
	evalPush(ec, r)

	nf := propcore.PropNFCreate(r.tProp, a.tProp, nil, propcore.PropNFTakeDstOwnership)
	self.tExtra = nf

	i := uint(1)
	for i < argc {
		cmdTok := tokenResolve(ec, argv[i])
		if cmdTok == nil {
			return -1
		}
		if cmdTok.typ != tokenIdentifier {
			return glwViewSeterr(ec.ei, argv[i], "propSorter(): invalid command token")
		}
		cmd := miscpkg.RstrGet(cmdTok.tRstring)
		i++

		if cmd == "filter" {
			if argc-i < 4 {
				return glwViewSeterr(ec.ei, argv[i-1], "propSorter(): too few arguments (%d) for filter command", argc-i)
			}
			ta := tokenResolve(ec, argv[i])
			if ta == nil {
				return -1
			}
			tb := tokenResolve(ec, argv[i+1])
			if tb == nil {
				return -1
			}
			tc := tokenResolve(ec, argv[i+2])
			if tc == nil {
				return -1
			}
			td := tokenResolve(ec, argv[i+3])
			if td == nil {
				return -1
			}
			i += 4
			path, ok := tokenAsString(ta)
			if !ok {
				continue
			}
			if tb.typ != tokenIdentifier || td.typ != tokenIdentifier {
				continue
			}
			var cf propcore.PropNFCmp
			switch miscpkg.RstrGet(tb.tRstring) {
			case "eq":
				cf = propcore.PropNFCmpEq
			case "neq":
				cf = propcore.PropNFCmpNeq
			default:
				continue
			}
			var mode propcore.PropNFMode
			switch miscpkg.RstrGet(td.tRstring) {
			case "include":
				mode = propcore.PropNFModeInclude
			case "exclude":
				mode = propcore.PropNFModeExclude
			default:
				continue
			}
			val, ok := tokenAsString(tc)
			if ok {
				propcore.PropNFPredStrAdd(nf, path, cf, val, nil, mode)
			} else {
				propcore.PropNFPredIntAdd(nf, path, cf, token2int(ec, tc), nil, mode)
			}
		} else if cmd == "sort" && sortidx < 4 {
			if argc-i < 3 {
				return glwViewSeterr(ec.ei, argv[i-1], "propSorter(): too few arguments (%d) for sort command", argc-i)
			}
			ta := tokenResolve(ec, argv[i])
			if ta == nil {
				return -1
			}
			tb := tokenResolve(ec, argv[i+1])
			if tb == nil {
				return -1
			}
			tc := tokenResolve(ec, argv[i+2])
			if tc == nil {
				return -1
			}
			i += 3
			propcore.PropNFSort(nf, miscpkg.RstrGet(ta.tRstring),
				token2bool(tb), uint(sortidx), nil, token2bool(tc))
			sortidx++
		} else {
			return glwViewSeterr(ec.ei, argv[i-1], "propSorter(): unknown command token")
		}
	}
	return 0
}

// C: glwf_getLayer / getWidth / getHeight (glw_view_eval.c:6074-6157)
func glwfGetLayer(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	ec.dynamicEval |= GLW_VIEW_EVAL_LAYOUT
	r := evalAlloc(self, ec, tokenInt)
	if ec.rc != nil {
		r.tInt = int(ec.rc.rcLayer)
	}
	evalPush(ec, r)
	return 0
}

func glwfGetWidth(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	w := ec.w
	ec.dynamicEval |= GLW_VIEW_EVAL_LAYOUT
	var r *Token
	if w.glwFlags&glwConstraintConfX != 0 {
		r = evalAlloc(self, ec, tokenInt)
		r.tInt = int(w.glwReqSizeX)
	} else if ec.rc == nil {
		r = evalAlloc(self, ec, tokenVoid)
	} else {
		r = evalAlloc(self, ec, tokenInt)
		r.tInt = int(ec.rc.rcWidth)
	}
	evalPush(ec, r)
	return 0
}

func glwfGetHeight(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	w := ec.w
	ec.dynamicEval |= GLW_VIEW_EVAL_LAYOUT
	var r *Token
	if w.glwFlags&glwConstraintConfY != 0 {
		r = evalAlloc(self, ec, tokenInt)
		r.tInt = int(w.glwReqSizeY)
	} else if ec.rc == nil {
		r = evalAlloc(self, ec, tokenVoid)
	} else {
		r = evalAlloc(self, ec, tokenInt)
		r.tInt = int(ec.rc.rcHeight)
	}
	evalPush(ec, r)
	return 0
}

// C: glwf_canSelectNext / canSelectPrev (glw_view_eval.c:6764-6795)
func glwfCanSelectNext(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	r := evalAlloc(self, ec, tokenInt)
	w := ec.w
	if w.glwClass.gcCanSelectChild != nil && w.glwClass.gcCanSelectChild(w, 1) != 0 {
		r.tInt = 1
	}
	ec.dynamicEval |= GLW_VIEW_EVAL_OTHER
	evalPush(ec, r)
	return 0
}

func glwfCanSelectPrev(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	r := evalAlloc(self, ec, tokenInt)
	w := ec.w
	if w.glwClass.gcCanSelectChild != nil && w.glwClass.gcCanSelectChild(w, 0) != 0 {
		r.tInt = 1
	}
	ec.dynamicEval |= GLW_VIEW_EVAL_OTHER
	evalPush(ec, r)
	return 0
}

// C: glwf_propWindow_dtor / glwf_propWindow (glw_view_eval.c:6802-6840)
func glwfPropWindowDtor(gr *glwRoot, self *Token) {
	if self.tExtra != nil {
		self.tExtra.(interface{ Destroy() }).Destroy()
	}
}

func glwfPropWindow(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := resolvePropertyName2(ec, argv[0])
	if a == nil {
		return -1
	}
	b := tokenResolve(ec, argv[1])
	if b == nil {
		return -1
	}
	c := tokenResolve(ec, argv[2])
	if c == nil {
		return -1
	}
	if self.tExtra != nil {
		self.tExtra.(interface{ Destroy() }).Destroy()
	}
	r := evalAlloc(self, ec, tokenPropertyRef)
	r.tProp = glwDeps.pm.RefInc(glwDeps.pm.CreateRoot(""))
	ec.dynamicEval |= GLW_VIEW_EVAL_KEEP
	evalPush(ec, r)
	self.tExtra = glwDeps.pm.PropWindowCreate(r.tProp, a.tProp,
		uint(token2int(ec, b)), uint(token2int(ec, c)), propcore.PropNFTakeDstOwnership)
	return 0
}

// C: glwf_setDefaultFont (glw_view_eval.c:6846)
func glwfSetDefaultFont(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := tokenResolve(ec, argv[0])
	if a == nil {
		return -1
	}
	gr := ec.w.glwRoot
	p := token2rstr(a)
	r := glwResolvePath(p, a.file, gr, nil)
	miscpkg.RstrRelease(p)
	miscpkg.RstrSet(&gr.grDefaultFont, r)
	miscpkg.RstrRelease(r)
	glwTextFlush(ec.w.glwRoot)
	return 0
}

// C: glwf_selectedElement (glw_view_eval.c:6868)
func glwfSelectedElement(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := tokenResolve(ec, argv[0])
	if a == nil {
		return -1
	}
	var r *Token
	if a.typ == tokenVector {
		for r = a.child; r != nil; r = r.next {
			if r.tFlags&tokenFSelected != 0 {
				break
			}
		}
	}
	if r == nil {
		r = evalAlloc(self, ec, tokenVoid)
	}
	evalPush(ec, r)
	return 0
}

// C: glwf_cloneIndex (glw_view_eval.c:6893)
func glwfCloneIndex(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	c := ec.w.glwClone
	r := evalAlloc(self, ec, tokenInt)
	if c != nil {
		sc := c.cSc
		if !sc.scPositionsValid {
			clonerResequence(sc)
		}
		r.tInt = int(c.cPos)
	}
	ec.dynamicEval |= GLW_VIEW_EVAL_LAYOUT
	evalPush(ec, r)
	return 0
}

// C: glwf_propName (glw_view_eval.c:6951)
func glwfPropName(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := resolvePropertyName2(ec, argv[0])
	if a == nil {
		return -1
	}
	var r *Token
	if a.typ == tokenPropertyRef && a.tProp != nil {
		r = evalAlloc(self, ec, tokenRstring)
		r.tRstring = miscpkg.RstrAllocStr(glwDeps.pm.GetName(a.tProp))
	} else {
		r = evalAlloc(self, ec, tokenVoid)
	}
	evalPush(ec, r)
	return 0
}

// C: glwf_propSelect (glw_view_eval.c:6975)
func glwfPropSelect(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := resolvePropertyName2(ec, argv[0])
	if a == nil {
		return -1
	}
	if a.typ == tokenPropertyRef {
		glwDeps.pm.Select(a.tProp)
	}
	return 0
}

// C: glwf_focus (glw_view_eval.c:6993)
func glwfFocus(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	gr := ec.w.glwRoot
	a := tokenResolve(ec, argv[0])
	if a == nil {
		return -1
	}
	if a.typ == tokenRstring {
		w := glwFindNeighbour(ec.w, miscpkg.RstrGet(a.tRstring))
		w = glwGetFocusableChild(w)
		if w != nil {
			glwFocusSet(gr, w, glwFocusSetInteractive, "FocusMethod")
		} else {
			miscpkg.RstrSet(&gr.grPendingFocus, a.tRstring)
		}
	}
	return 0
}

// C: glwf_toggle (glw_view_eval.c:7018)
func glwfToggle(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := argv[0]
	if resolvePropertyName(ec, a, 1) != 0 {
		return -1
	}
	if a.typ == tokenPropertyRef {
		glwDeps.pm.ToggleInt(a.tProp)
	}
	return 0
}

// C: glwf_lookup_dtor / glwf_lookup (glw_view_eval.c:7070-7115)
func glwfLookupDtor(gr *glwRoot, self *Token) {
	if self.tExtra != nil {
		glwDeps.pm.RefDec(self.tExtra.(*propcore.Prop))
	}
}

func glwfLookup(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := resolvePropertyName2(ec, argv[0])
	if a == nil {
		return -1
	}
	b := tokenResolve(ec, argv[1])
	if b == nil {
		return -1
	}
	if a.typ != tokenPropertyRef {
		return glwViewSeterr(ec.ei, a, "lookup(): First argument is not a property")
	}
	key := token2rstr(b)
	if key == nil {
		return glwViewSeterr(ec.ei, b, "lookup(): Second arg is not a string")
	}
	if self.tExtra == nil {
		self.tExtra = glwDeps.pm.RefInc(glwDeps.pm.CreateEx(a.tProp, "", nil, false, false)) // C: prop_create_r
	}
	glwDeps.pm.SetStringEx(self.tExtra.(*propcore.Prop), nil, miscpkg.RstrGet(key), 0)

	r := evalAlloc(self, ec, tokenPropertyRef)
	r.tProp = glwDeps.pm.RefInc(glwDeps.pm.CreateEx(self.tExtra.(*propcore.Prop), "value", nil, false, false)) // C: prop_create_r
	evalPush(ec, r)
	miscpkg.RstrRelease(key)
	return 0
}

// propProxyConnect — C: prop_proxy_connect (prop_proxy.c)
func propProxyConnect(url string, p *propcore.Prop) *propproxy.ProxyConnection {
	return propproxy.NewProxyConnection(glwDeps.aio, url, p)
}

// propProxyClose — C: prop_proxy_close (prop_proxy.c)
func propProxyClose(pp any) {
	if c, ok := pp.(*propproxy.ProxyConnection); ok {
		c.Close()
	}
}

// propProxyGetRoot — C: prop_proxy_get_root (prop_proxy.c)
func propProxyGetRoot(pp any) *propcore.Prop {
	if c, ok := pp.(*propproxy.ProxyConnection); ok {
		return c.GetRoot()
	}
	return nil
}

// propProxyGetBackend — C: prop_proxy_get_backend (prop_proxy.c)
func propProxyGetBackend(pp any) *backendcore.Backend {
	if c, ok := pp.(*propproxy.ProxyConnection); ok {
		return c.GetBackend()
	}
	return nil
}

// ---------------------------------------------------------------------------
// C: funcvec[] (glw_view_eval.c:7274-7384)
var funcvec = []tokenFunc{
	// Fundamentals
	{name: "widget", nargs: 1, cb: glwfWidget, preproc: glwfResolveWidgetClass},
	{name: "cloner", nargs: 3, cb: glwfCloner},
	{name: "coreAttach", nargs: 3, cb: glwfCoreAttach, ctor: glwfNullCtor, dtor: glwfCoreAttachDtor},
	{name: "style", nargs: 2, cb: glwfStyle},
	{name: "newstyle", nargs: 2, cb: glwfNewstyle},
	{name: "space", nargs: 1, cb: glwfSpace},

	// Events
	{name: "onEvent", nargs: -1, cb: glwfOnEvent},
	{name: "navOpen", nargs: -1, cb: glwfNavOpen},
	{name: "playTrackFromSource", nargs: -1, cb: glwfPlayTrackFromSource},
	{name: "enqueuetrack", nargs: 1, cb: glwfEnqueueTrack},
	{name: "selectAudioTrack", nargs: 1, cb: glwfSelectAudioTrack},
	{name: "selectSubtitleTrack", nargs: 1, cb: glwfSelectSubtitleTrack},
	{name: "fireEvent", nargs: 1, cb: glwfFireEvent},
	{name: "event", nargs: 1, cb: glwfEvent},
	{name: "targetedEvent", nargs: 2, cb: glwfTargetedEvent},
	{name: "deliverEvent", nargs: -1, cb: glwfDeliverEvent},
	{name: "deliverRef", nargs: 2, cb: glwfDeliverRef},
	{name: "currentEvent", nargs: 1, cb: glwfCurrentEvent},
	{name: "onInactivity", nargs: 2, cb: glwfOnInactivity, ctor: glwfNullCtor, dtor: glwfOnInactivityDtor},
	{name: "changed", nargs: -1, cb: glwfChanged, ctor: glwfChangedCtor, dtor: glwfChangedDtor},
	{name: "iir", nargs: -1, cb: glwfIir},
	{name: "scurve", nargs: -1, cb: glwfScurve, ctor: glwfScurveCtor, dtor: glwfScurveDtor},
	{name: "translate", nargs: -1, cb: glwfTranslate},
	{name: "strftime", nargs: 2, cb: glwfStrftime},
	{name: "isSet", nargs: 1, cb: glwfIsset},
	{name: "isVoid", nargs: 1, cb: glwfIsvoid},
	{name: "value2duration", nargs: -1, cb: glwfValue2duration},
	{name: "value2size", nargs: 1, cb: glwfValue2size},
	{name: "value2quantity", nargs: 1, cb: glwfValue2quantity},
	{name: "createChild", nargs: 1, cb: glwfCreatechild},
	{name: "delete", nargs: 1, cb: glwfDelete},
	{name: "isFocused", nargs: 0, cb: glwfIsFocused},
	{name: "isNavFocused", nargs: 0, cb: glwfIsFocused},
	{name: "isHovered", nargs: 0, cb: glwfIsHovered},
	{name: "isPressed", nargs: 0, cb: glwfIsPressed},
	{name: "focusedChild", nargs: 0, cb: glwfFocusedChild},
	{name: "focusedClone", nargs: 0, cb: glwfFocusedClone},
	{name: "focusedIndex", nargs: 0, cb: glwfFocusedIndex},
	{name: "getCaption", nargs: 1, cb: glwfGetCaption},
	{name: "bind", nargs: 1, cb: glwfBind},
	{name: "delta", nargs: 2, cb: glwfDelta, ctor: glwfDeltaCtor, dtor: glwfDeltaDtor},
	{name: "isVisible", nargs: 0, cb: glwfIsVisible},
	{name: "isPreloaded", nargs: 0, cb: glwfIsPreloaded},
	{name: "canScroll", nargs: 0, cb: glwfCanScroll},
	{name: "select", nargs: 3, cb: glwfSelect},
	{name: "trace", nargs: 2, cb: glwfTrace},
	{name: "browse", nargs: 1, cb: glwfBrowse, ctor: glwfBrowseCtor, dtor: glwfBrowseDtor},
	{name: "isLink", nargs: 1, cb: glwfIsLink},
	{name: "sin", nargs: 1, cb: glwfSin},
	{name: "sinewave", nargs: 1, cb: glwfSinewave},
	{name: "monotime", nargs: 0, cb: glwfMonotime},
	{name: "delay", nargs: 3, cb: glwfDelay, ctor: glwfDelayCtor, dtor: glwfDelayDtor},
	{name: "isLoading", nargs: 0, cb: glwfIsLoading},
	{name: "isLoaded", nargs: 0, cb: glwfIsLoaded},
	{name: "isError", nargs: 0, cb: glwfIsError},
	{name: "primaryColor", nargs: 0, cb: glwfPrimaryColor},
	{name: "suggestFocus", nargs: 1, cb: glwfSuggestFocus},
	{name: "count", nargs: 1, cb: glwfCount},
	{name: "vectorize", nargs: 1, cb: glwfVectorize},
	{name: "propGrouper", nargs: 2, cb: glwfPropGrouper, ctor: glwfNullCtor, dtor: glwfPropGrouperDtor},
	{name: "propSorter", nargs: -1, cb: glwfPropSorter, ctor: glwfNullCtor, dtor: glwfPropSorterDtor},
	{name: "propWindow", nargs: 3, cb: glwfPropWindow, ctor: glwfNullCtor, dtor: glwfPropWindowDtor},
	{name: "getLayer", nargs: 0, cb: glwfGetLayer},
	{name: "getWidth", nargs: 0, cb: glwfGetWidth},
	{name: "getHeight", nargs: 0, cb: glwfGetHeight},
	{name: "int", nargs: 1, cb: glwfInt},
	{name: "clamp", nargs: 3, cb: glwfClamp},
	{name: "join", nargs: -1, cb: glwfJoin},
	{name: "fmt", nargs: -1, cb: glwfFmt},
	{name: "_pl", nargs: 3, cb: glwfPluralise},
	{name: "multiopt", nargs: -1, cb: glwfMultiopt, ctor: glwfMultioptCtor, dtor: glwfMultioptDtor},
	{name: "makeUri", nargs: 2, cb: glwfMakeUri},
	{name: "canSelectNext", nargs: 0, cb: glwfCanSelectNext},
	{name: "canSelectPrevious", nargs: 0, cb: glwfCanSelectPrev},
	{name: "setDefaultFont", nargs: 1, cb: glwfSetDefaultFont},
	{name: "rand", nargs: 0, cb: glwfRand},
	{name: "selectedElement", nargs: 1, cb: glwfSelectedElement},
	{name: "set", nargs: 2, cb: glwfSet, ctor: glwfNullCtor, dtor: glwfSetDtor},
	{name: "cloneIndex", nargs: 0, cb: glwfCloneIndex},
	{name: "abs", nargs: 1, cb: glwfAbs},
	{name: "propName", nargs: 1, cb: glwfPropName},
	{name: "propSelect", nargs: 1, cb: glwfPropSelect},
	{name: "focus", nargs: 1, cb: glwfFocus},
	{name: "toggle", nargs: 1, cb: glwfToggle},
	{name: "eventWithProp", nargs: 2, cb: glwfEventWithProp},
	{name: "timeAgo", nargs: 1, cb: glwfTimeAgo},
	{name: "lookup", nargs: 2, cb: glwfLookup, ctor: glwfNullCtor, dtor: glwfLookupDtor},
	{name: "injectEventsFrom", nargs: 1, cb: glwfInjectEvents},
	{name: "RGBToString", nargs: 1, cb: glwfRgbToString},
}

// glwPropnameSlice — converts the [16]string output of glwPropnameToArray
// into a []string slice trimmed at the first empty entry (C: NULL sentinel).
func glwPropnameSlice(pname *[16]string) []string {
	n := 0
	for n < 16 && pname[n] != "" {
		n++
	}
	return pname[:n]
}
