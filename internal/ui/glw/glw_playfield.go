package glw

// Canonical port of src/ui/glw/glw_playfield.c — the `playfield` widget
// class (page stack with cross-fade and "detach" sub-widget effect).

import (
	"math"
	"unsafe"

	propcore "github.com/czz/movian-go/internal/prop"
)

// C: glw_playfield_t
type glwPlayfield struct {
	w      Glw
	fsmode int8 // C: char
	speed  float32
}

// C: glw_playfield_item_t — per-child parent data
type glwPlayfieldItem struct {
	detached *Glw
	amount   float32
}

func playfieldItemData(w *Glw) *glwPlayfieldItem {
	return w.glwParentData.(*glwPlayfieldItem)
}

// C: clear_constraints (static, glw_playfield.c)
func playfieldClearConstraints(w *Glw) {
	glwSetConstraints(w, 0, 0, 0, glwConstraintX|glwConstraintY)

	glwSignal0(w, glwSignalFullwindowConstraintChanged, nil)
}

// C: glw_playfield_update_constraints
func glwPlayfieldUpdateConstraints(p *glwPlayfield) {
	c := p.w.glwSelected
	glwCopyConstraints(&p.w, c)
}

// C: find_by_prop
func playfieldFindByProp(w *Glw, p *propcore.Prop) *Glw {
	for c := w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		if c.glwOriginatingProp != nil &&
			glwDeps.pm.Compare(c.glwOriginatingProp, p) {
			return c
		}
		if r := playfieldFindByProp(c, p); r != nil {
			return r
		}
	}
	return nil
}

// C: find_detachable
func playfieldFindDetachable(w *Glw) *Glw {
	if w.glwClass.gcDetachControl != nil {
		return w
	}
	for c := w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		if r := playfieldFindDetachable(c); r != nil {
			return r
		}
	}
	return nil
}

// C: destroy_detached_callback
func playfieldDestroyDetachedCallback(w *Glw, opaque any,
	signal glwSignal, extra any) int {
	s := (*Glw)(opaque.(unsafe.Pointer))
	if signal == glwSignalDestroy {
		cd := playfieldItemData(s)
		// C: assert(cd->detached == w)
		cd.detached = nil
	}
	return 0
}

// C: detach
func playfieldDetach(s *Glw, d *Glw) {
	cd := playfieldItemData(s)

	if cd.detached != nil {
		p := cd.detached
		p.glwClass.gcDetachControl(p, 0)
		glwSignalHandlerUnregister(p, playfieldDestroyDetachedCallback,
			unsafe.Pointer(s))
	}

	cd.detached = d
	if d != nil {
		d.glwClass.gcDetachControl(d, 1)
		glwSignalHandlerRegister(d, playfieldDestroyDetachedCallback,
			unsafe.Pointer(s))
	}
}

// C: playfield_select_child
func playfieldSelectChild(w *Glw, c *Glw, origin *propcore.Prop) int {
	p := (*glwPlayfield)(unsafe.Pointer(w))
	speed := float32(0.5)

	glwNeedRefresh(w.glwRoot, 0)

	if origin != nil && w.glwSelected != nil &&
		w.glwSelected.glwParentLinkNext == c {
		x := playfieldFindByProp(w.glwSelected, origin)
		var d *Glw
		if x != nil {
			d = playfieldFindDetachable(x)
		}
		if d != nil {
			playfieldDetach(w.glwSelected, d)
			speed = 0.025
		}
	}

	if c != nil {
		cd := playfieldItemData(c)

		if w.glwSelected == nil && w.glwFlags2&glw2NoInitialTrans != 0 {
			cd.amount = 1
		}

		if cd.detached != nil {
			speed = 0.025
		}
	}

	p.speed = speed
	w.glwSelected = c

	if c != nil {
		glwFocusOpenPathCloseAllOther(c)
		glwPlayfieldUpdateConstraints(p)
	} else {
		playfieldClearConstraints(w)
	}
	return 1
}

// C: glw_playfield_layout
func glwPlayfieldLayout(w *Glw, rc *glwRctx) {
	p := (*glwPlayfield)(unsafe.Pointer(w))
	v := 0
	var s float32

	if w.glwAlpha < glwAlphaEpsilon {
		return
	}

	// C: TAILQ_FOREACH_REVERSE
	for c := glwTAILQLast(w); c != nil; c = glwTAILQPrev(c) {
		if w.glwSelected == c {
			v = 1
		} else if v == 1 {
			v = 2
		}

		s = p.speed

		cd := playfieldItemData(c)

		n := cd.amount

		if cd.amount < float32(v) {
			n = glwMin(float32(v), cd.amount+s)
		} else if cd.amount > float32(v) {
			n = glwMax(float32(v), cd.amount-s)
		}

		if cd.amount != n {
			cd.amount = n
			glwNeedRefresh(w.glwRoot, 0)
		}

		if cd.amount > 0 && cd.amount < 2 {
			glwLayout0(c, rc)
		}

		if cd.amount <= 1 && cd.detached != nil {
			playfieldDetach(c, nil)
		}
	}
}

// C: glw_playfield_callback
func glwPlayfieldCallback(w *Glw, opaque any, signal glwSignal, extra any) int {
	p := (*glwPlayfield)(unsafe.Pointer(w))

	switch signal {
	case glwSignalChildConstraintsChanged:
		if w.glwSelected == extra.(*Glw) {
			glwPlayfieldUpdateConstraints(p)
		}
		return 1

	case glwSignalChildDestroyed:
		if w.glwSelected == extra.(*Glw) {
			playfieldClearConstraints(w)
		}
	}
	return 0
}

// C: glw_playfield_render
func glwPlayfieldRender(w *Glw, rc *glwRctx) {
	zmax := 0
	var c, d, dd *Glw
	var rc0, rc1, rc2, rc3 glwRctx

	if w.glwAlpha < glwAlphaEpsilon {
		return
	}

	rc0 = *rc
	rc0.rcAlpha *= w.glwAlpha
	rc0.rcZmax = &zmax

	for c = w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		cd := playfieldItemData(c)

		if zmax > int(rc.rcZindex) {
			rc0.rcZindex = int16(zmax)
		} else {
			rc0.rcZindex = rc.rcZindex
		}

		if cd.amount > 0 && cd.amount < 2 {
			rc1 = rc0
			rc1.rcAlpha *= 1 - float32(math.Abs(float64(cd.amount-1)))
			glwRender0(c, &rc1)
		}

		if d = cd.detached; d != nil {
			a := glwMin(cd.amount, 1)
			if a > 0 {
				v := glwMax(cd.amount-1, 0)
				v = glwS(v)

				glwLerpMatrix(&rc0.rcMtx, v, d.glwMatrix, &rc0.rcMtx)

				if dd = d.glwChilds.tqhFirst; dd != nil {
					glwRotatef(&rc0, v*180, 1, 0, 0)
					rc3 = rc0
					d.glwClass.gcGetRctx(d, &rc2)
					rc3.rcWidth = rc2.rcWidth
					rc3.rcHeight = rc2.rcHeight
					rc3.rcAlpha *= a
					glwRender0(dd, &rc3)
					glwRotatef(&rc0, 180, 1, 0, 0)
				}
			}
		}
		if int(rc0.rcZindex)+1 > zmax {
			zmax = int(rc0.rcZindex) + 1
		}
	}
	if zmax > *rc.rcZmax {
		*rc.rcZmax = zmax
	}
}

// C: glw_class_t glw_playfield — ctor is C's clear_constraints
var glwPlayfieldClass = &glwClass{
	gcName:           "playfield",
	gcInstanceSize:   int(unsafe.Sizeof(glwPlayfield{})),
	gcParentDataSize: int(unsafe.Sizeof(glwPlayfieldItem{})),
	gcNewParentData:  func() any { return &glwPlayfieldItem{} },
	gcFlags:          glwCanHideChilds | glwNavigationSearchBoundary,
	gcNew: func(parent *Glw) *Glw {
		p := &glwPlayfield{}
		return &p.w
	},
	gcLayout:        glwPlayfieldLayout,
	gcRender:        glwPlayfieldRender,
	gcCtor:          playfieldClearConstraints,
	gcSignalHandler: glwPlayfieldCallback,
	gcSelectChild:   playfieldSelectChild,
}

func registerPlayfield() {
	glwRegisterClass(glwPlayfieldClass)
}
