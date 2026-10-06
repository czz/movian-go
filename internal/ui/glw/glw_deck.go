package glw

// Canonical port of src/ui/glw/glw_deck.c — the `deck` widget class
// (shows one selected child at a time with transition effects).

import (
	"strconv"
	"unsafe"

	eventpkg "github.com/czz/movian-go/internal/event"
	miscpkg "github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
)

// C: segway_direction enum in glw_deck_t
const (
	segwayDirectionLeft  = 1
	segwayDirectionRight = 2
)

// C: glw_deck_t — the `char x : 1` bitfields store only the low bit
// (signed 1-bit: -1 or 0); we keep them as int with value&1 semantics.
type glwDeck struct {
	w    Glw
	last *Glw // Widget we are transitioning from

	efxConf glwTransitionType
	time    float32
	delta   float32

	v float32

	rev                 int // C: char rev : 1
	keepNextHot         int // C: char : 1
	keepPrevHot         int // C: char : 1
	keepLastHot         int // C: char : 1
	preloadedAreVisible int // C: char : 1
	allowTransition     int // C: char : 1

	segwayDirection int
}

// C: clear_constraints (static, glw_deck.c)
func deckClearConstraints(w *Glw) {
	glwSetConstraints(w, 0, 0, 0, glwConstraintX|glwConstraintY)
}

// C: glw_deck_update_constraints
func glwDeckUpdateConstraints(w *Glw) {
	glwCopyConstraints(w, w.glwSelected)
}

// C: setprev
func deckSetprev(gd *glwDeck, c *Glw) {
	l := gd.w.glwSelected
	rev := 0

	gd.last = l
	if c == nil {
		return
	}

	for p := c.glwParentLinkNext; p != nil; p = p.glwParentLinkNext {
		if p == l {
			rev = 1
			break
		}
	}
	gd.rev = rev
}

// C: deck_select_child
func deckSelectChild(w *Glw, c *Glw, origin *propcore.Prop) int {
	gd := (*glwDeck)(unsafe.Pointer(w))
	if w.glwSelected == c {
		return 0
	}

	glwNeedRefresh(w.glwRoot, 0)

	deckSetprev(gd, c)
	w.glwSelected = c
	if w.glwSelected != nil {
		glwFocusOpenPathCloseAllOther(w.glwSelected)
		glwDeckUpdateConstraints(w)
	} else {
		deckClearConstraints(w)
	}

	if gd.efxConf != glwTransNone && gd.allowTransition != 0 &&
		(gd.last != nil || w.glwFlags2&glw2NoInitialTrans == 0) {
		gd.v = 0
	}

	glwSignal0(w, glwSignalReselectChanged, nil)
	gd.allowTransition = 1
	return glwSetRerenderRequired
}

// C: glw_deck_layout
func glwDeckLayout(w *Glw, rc *glwRctx) {
	gd := (*glwDeck)(unsafe.Pointer(w))

	if rc.rcSegwayed != 0 { // C: unlikely
		gd.allowTransition = 0
	}

	gd.delta = 1 / (gd.time * float32(1000000/w.glwRoot.grFrameduration))

	if w.glwAlpha < glwAlphaEpsilon {
		return
	}

	v := glwMin(gd.v+gd.delta, 1.0)
	if v != gd.v {
		gd.v = v
		glwNeedRefresh(w.glwRoot, 0)
	}

	if gd.v == 1 && gd.keepLastHot == 0 {
		gd.last = nil
	}

	rc0 := *rc
	rc0.rcPreloaded = 1
	if gd.preloadedAreVisible == 0 {
		rc0.rcInvisible = 1
	}

	if gd.last != nil {
		glwLayout0(gd.last, &rc0)
	}

	if w.glwSelected == nil {
		return
	}

	if w.glwSelected != gd.last {
		glwLayout0(w.glwSelected, rc)
	}

	var p *Glw

	if gd.keepPrevHot != 0 {
		p = glwPrevWidget(w.glwSelected)
		if p == nil {
			p = glwLastWidget(w)
		}
		if p != nil && p != w.glwSelected && p != gd.last {
			glwLayout0(p, &rc0)
		}
	} else {
		p = nil
	}

	if gd.keepNextHot != 0 {
		n := glwNextWidget(w.glwSelected)
		if n == nil {
			n = glwFirstWidget(w)
		}
		if n != nil && n != w.glwSelected && n != gd.last && n != p {
			glwLayout0(n, &rc0)
		}
	}
}

// C: glw_deck_callback
func glwDeckCallback(w *Glw, opaque any, signal glwSignal, extra any) int {
	gd := (*glwDeck)(unsafe.Pointer(w))
	var c *Glw
	switch signal {
	case glwSignalChildConstraintsChanged:
		if w.glwSelected == extra.(*Glw) {
			glwDeckUpdateConstraints(w)
		}
		return 1

	case glwSignalChildDestroyed:
		if w.glwSelected == extra.(*Glw) {
			deckClearConstraints(w)
		}
		if gd.last == extra.(*Glw) {
			gd.last = nil
		}
		fallthrough
	case glwSignalChildCreated:
		// Initially all pages are blocked from focus
		c = extra.(*Glw)
		c.glwFlags |= glwFocusBlocked
		fallthrough
	case glwSignalChildMoved, glwSignalChildHidden, glwSignalChildUnhidden:
		glwSignal0(w, glwSignalReselectChanged, nil)
		return 0
	}
	return 0
}

// C: glw_deck_event
func glwDeckEvent(w *Glw, e *eventpkg.Event) int {
	c := w.glwSelected
	var n *Glw

	if c != nil && e.IsAction(eventpkg.ACTION_INCR) {
		n = glwGetNextN(c, 1)
	} else if c != nil && e.IsAction(eventpkg.ACTION_DECR) {
		n = glwGetPrevN(c, 1)
	} else {
		n = nil
	}

	if n != c && n != nil {
		if n.glwOriginatingProp != nil {
			// This will bounce back via .gc_select_child
			glwDeps.pm.Select(n.glwOriginatingProp)
		} else {
			deckSelectChild(w, n, nil)
		}
		return 1
	}
	return 0
}

// C: deck_render
func deckRender(rc *glwRctx, gd *glwDeck, w *Glw, v float32) {
	if gd.efxConf != glwTransNone {
		rc0 := *rc
		if gd.rev != 0 {
			v = 1 - (v + 1)
		}
		glwTransitionRender(gd.efxConf, v,
			rc.rcAlpha*gd.w.glwAlpha, &rc0)
		glwRender0(w, &rc0)
	} else {
		glwRender0(w, rc)
	}
}

// C: glw_deck_render
func glwDeckRender(w *Glw, rc *glwRctx) {
	gd := (*glwDeck)(unsafe.Pointer(w))

	if w.glwAlpha < glwAlphaEpsilon {
		return
	}

	glwStoreMatrix(w, rc)

	if gd.last != nil && gd.v < 1 {
		deckRender(rc, gd, gd.last, gd.v)
	}

	if w.glwSelected != nil {
		deckRender(rc, gd, w.glwSelected, -1+gd.v)
	}
}

// C: set_page
func deckSetPage(gd *glwDeck, n int) int {
	var c *Glw
	for c = gd.w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		n--
		if n < 0 {
			break
		}
	}
	return deckSelectChild(&gd.w, c, nil)
}

// C: set_page_by_id
func deckSetPageByID(gd *glwDeck, str string) int {
	var c *Glw
	for c = gd.w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		if c.glwIdRstr != nil && miscpkg.RstrGet(c.glwIdRstr) == str {
			break
		}
	}
	return deckSelectChild(&gd.w, c, nil)
}

// C: deck_can_select_child
func deckCanSelectChild(w *Glw, next int) int {
	c := w.glwSelected
	if c == nil {
		return 0
	}
	if next != 0 {
		if glwGetNextN(c, 1) != nil {
			return 1
		}
		return 0
	}
	if glwGetPrevN(c, 1) != nil {
		return 1
	}
	return 0
}

// C: glw_deck_ctor
func glwDeckCtor(w *Glw) {
	gd := (*glwDeck)(unsafe.Pointer(w))
	gd.v = 1.0
	gd.allowTransition = 1
	deckClearConstraints(w)
}

// C: glw_deck_set_int
func glwDeckSetInt(w *Glw, attrib glwAttribute, value int, gs *GlwStyle) int {
	gd := (*glwDeck)(unsafe.Pointer(w))
	switch attrib {
	case glwAttribTransitionEffect:
		if gd.efxConf == glwTransitionType(value) {
			return 0
		}
		gd.efxConf = glwTransitionType(value)
	default:
		return -1
	}
	return 1
}

// C: glw_deck_set_float
func glwDeckSetFloat(w *Glw, attrib glwAttribute, value float32, gs *GlwStyle) int {
	gd := (*glwDeck)(unsafe.Pointer(w))
	switch attrib {
	case glwAttribTime:
		if gd.time == value {
			return 0
		}
		gd.time = value
	default:
		return -1
	}
	return 1
}

// C: glw_deck_set_int_unresolved
func glwDeckSetIntUnresolved(w *Glw, a string, value int, gs *GlwStyle) int {
	gd := (*glwDeck)(unsafe.Pointer(w))

	switch a {
	case "page":
		return deckSetPage(gd, value)
	case "keepPreviousActive":
		gd.keepPrevHot = value & 1
		return glwSetLayoutOnly
	case "keepNextActive":
		gd.keepNextHot = value & 1
		return glwSetLayoutOnly
	case "keepLastActive":
		gd.keepLastHot = value & 1
		return glwSetLayoutOnly
	case "preloadedAreVisible":
		gd.preloadedAreVisible = value & 1
		return glwSetLayoutOnly
	}
	return glwSetNotResponding
}

// C: segway_directions strtab
var deckSegwayDirections = []struct {
	str string
	val int
}{
	{"left", segwayDirectionLeft},
	{"right", segwayDirectionRight},
}

// C: glw_deck_set_str_unresolved
func glwDeckSetStrUnresolved(w *Glw, a string, value *miscpkg.Rstr, gs *GlwStyle) int {
	gd := (*glwDeck)(unsafe.Pointer(w))

	switch a {
	case "page":
		return deckSetPageByID(gd, miscpkg.RstrGet(value))
	case "segway":
		gd.segwayDirection = str2val(miscpkg.RstrGet(value), deckSegwayDirections)
		return glwSetRerenderRequired
	}
	return glwSetNotResponding
}

// C: get_identity
func glwDeckGetIdentity(w *Glw, tmp []byte) string {
	var c *Glw
	num := 0
	for c = w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		if c == w.glwSelected {
			break
		}
		num++
	}
	if c == nil {
		return "None"
	}
	return strconv.Itoa(num)
}

// C: glw_class_t glw_deck
var glwDeckClass = &glwClass{
	gcName:         "deck",
	gcInstanceSize: int(unsafe.Sizeof(glwDeck{})),
	gcFlags:        glwCanHideChilds,
	gcNew: func(parent *Glw) *Glw {
		gd := &glwDeck{}
		return &gd.w
	},
	gcLayout:           glwDeckLayout,
	gcRender:           glwDeckRender,
	gcSetInt:           glwDeckSetInt,
	gcSetFloat:         glwDeckSetFloat,
	gcSetIntUnresolved: glwDeckSetIntUnresolved,
	gcSetRstrUnresolved: func(w *Glw, a string, value *miscpkg.Rstr, gs *GlwStyle) int {
		return glwDeckSetStrUnresolved(w, a, value, gs)
	},
	gcCtor:           glwDeckCtor,
	gcSignalHandler:  glwDeckCallback,
	gcSelectChild:    deckSelectChild,
	gcCanSelectChild: deckCanSelectChild,
	gcSendEvent:      glwDeckEvent,
	gcGetIdentity:    glwDeckGetIdentity,
}

func registerDeck() {
	glwRegisterClass(glwDeckClass)
}
