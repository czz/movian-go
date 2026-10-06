package glw

// C: src/ui/glw/glw.c — canonical 1:1 port.
//
// Global singletons used by C (event_mgr, the prop context, gconf.*,
// runcontrol) map to package-level seams configured at init time, matching
// the established convention (GconfClipboardSet, EsSetPropDeps, ...).

import (
	propcore "github.com/czz/movian-go/internal/prop"
)

// C: static void update_in_path (glw.c:429-445)
func updateInPath(w *Glw) {
	gr := w.glwRoot
	f := 0

	for p := w.glwParent; p != nil; p = p.glwParent {
		if p == gr.grCurrentFocus {
			f |= glwInFocusPath
		}
		if p == gr.grPointerHover {
			f |= glwInHoverPath
		}
		if p == gr.grPointerPress {
			f |= glwInPressedPath
		}
	}
	w.glwFlags |= f
}

// C: void glw_layout0 (glw.c:450-490)
func glwLayout0(w *Glw, rc *glwRctx) {
	gr := w.glwRoot
	mask := GLW_VIEW_EVAL_LAYOUT

	if rc.rcInvisible == 0 { // C: likely(!rc->rc_invisible)
		// C: LIST_REMOVE(w, glw_active_link); LIST_INSERT_HEAD(&gr->gr_active_list, ...)
		if w.glwActiveLinkNext != nil {
			w.glwActiveLinkNext.glwActiveLinkPrev = w.glwActiveLinkPrev
		}
		*w.glwActiveLinkPrev = w.glwActiveLinkNext
		w.glwActiveLinkNext = gr.grActiveList.lhFirst
		if w.glwActiveLinkNext != nil {
			w.glwActiveLinkNext.glwActiveLinkPrev = &w.glwActiveLinkNext
		}
		gr.grActiveList.lhFirst = w
		w.glwActiveLinkPrev = &gr.grActiveList.lhFirst

		if w.glwFlags&glwActive == 0 { // C: unlikely
			w.glwFlags |= glwActive
			mask |= GLW_VIEW_EVAL_ACTIVE
			glwSignal0(w, glwSignalActive, nil)
		}
	}

	if (rc.rcPreloaded != 0) != (w.glwFlags&glwPreloaded != 0) {
		if rc.rcPreloaded != 0 {
			w.glwFlags |= glwPreloaded
		} else {
			w.glwFlags &^= glwPreloaded
		}
		mask |= GLW_VIEW_EVAL_ACTIVE
	}

	if int(w.glwDynamicEval)&mask != 0 {
		glwViewEvalLayout(w, rc, mask)
	}

	if w.glwFlags&glwHaveMargins != 0 {
		rc0 := *rc
		glwReposition(&rc0,
			int(w.glwMargin[0]),
			int(rc.rcHeight)-int(w.glwMargin[1]),
			int(rc.rcWidth)-int(w.glwMargin[2]),
			int(w.glwMargin[3]))

		if rc0.rcWidth < 1 || rc0.rcHeight < 1 {
			return
		}
		w.glwClass.gcLayout(w, &rc0)
	} else {
		w.glwClass.gcLayout(w, rc)
	}
}

// C: void glw_move (glw.c:1098-1127)
func glwMove(w *Glw, b *Glw) {
	p := w.glwParent
	gr := w.glwRoot
	wasFirst := p.glwChilds.tqhFirst == w && w == p.glwFocused

	// C: TAILQ_REMOVE(&p->glw_childs, w, glw_parent_link)
	if w.glwParentLinkNext != nil {
		w.glwParentLinkNext.glwParentLinkPrev = w.glwParentLinkPrev
	} else {
		p.glwChilds.tqhLast = w.glwParentLinkPrev
	}
	*w.glwParentLinkPrev = w.glwParentLinkNext

	if b == nil {
		// C: TAILQ_INSERT_TAIL(&p->glw_childs, w, glw_parent_link)
		w.glwParentLinkNext = nil
		w.glwParentLinkPrev = p.glwChilds.tqhLast
		*p.glwChilds.tqhLast = w
		p.glwChilds.tqhLast = &w.glwParentLinkNext
	} else {
		// C: TAILQ_INSERT_BEFORE(b, w, glw_parent_link)
		w.glwParentLinkPrev = b.glwParentLinkPrev
		w.glwParentLinkNext = b
		*b.glwParentLinkPrev = w
		b.glwParentLinkPrev = &w.glwParentLinkNext
	}
	if p.glwFlags&glwFloatingFocus != 0 {
		if w == p.glwChilds.tqhFirst {
			w2 := w.glwParentLinkNext
			if w2 != nil && p.glwFocused == w2 {
				c := glwFocusByPath(w)
				glwFocusSet(gr, c, glwFocusSetAutomaticFf, "Move")
			}
		} else if wasFirst {
			w2 := p.glwChilds.tqhFirst
			c := glwFocusByPath(w2)
			glwFocusSet(gr, c, glwFocusSetAutomaticFf, "Move")
		}
	}
	glwSignal0(p, glwSignalChildMoved, w)
	glwNeedRefresh(gr, 0)
}

// C: static void glw_fhp_update (glw.c:1132-1150)
func glwFhpUpdate(w *Glw, or int, and int) {
	if w.glwFlags&glwDestroying != 0 {
		return
	}

	if w.glwFlags2&glw2SelectOnFocus != 0 && w.glwOriginatingProp != nil &&
		w.glwFlags&glwInFocusPath == 0 && or&glwInFocusPath != 0 {
		propcore.ProxySelect(w.glwOriginatingProp)
	}

	if w.glwFlags2&glw2SelectOnHover != 0 && w.glwOriginatingProp != nil &&
		w.glwFlags&glwInHoverPath == 0 && or&glwInHoverPath != 0 {
		propcore.ProxySelect(w.glwOriginatingProp)
	}

	w.glwFlags = (w.glwFlags | or) & and
	glwSignal0(w, glwSignalFhpPathChanged, nil)
}

// C: void glw_scale_to_aspect (glw.c:2288-2320)
func glwScaleToAspect(rc *glwRctx, tAspect float32) {
	if tAspect*float32(rc.rcHeight) < float32(rc.rcWidth) {
		// Shrink X
		border := float32(rc.rcWidth) - tAspect*float32(rc.rcHeight)
		left := int(border+1) / 2
		right := int(rc.rcWidth) - int(border)/2

		s := float32(right-left) / float32(rc.rcWidth)
		t := float32(-1.0) + float32(right+left)/float32(rc.rcWidth)

		glwTranslatef(rc, t, 0, 0)
		glwScalef(rc, s, 1.0, 1.0)

		rc.rcWidth = int16(right - left)

	} else {
		// Shrink Y
		border := float32(rc.rcHeight) - float32(rc.rcWidth)/tAspect
		bottom := int(border+1) / 2
		top := int(rc.rcHeight) - int(border)/2

		s := float32(top-bottom) / float32(rc.rcHeight)
		t := float32(-1.0) + float32(top+bottom)/float32(rc.rcHeight)

		glwTranslatef(rc, 0, t, 0)
		glwScalef(rc, 1.0, s, 1.0)
		rc.rcHeight = int16(top - bottom)
	}
}

// C: void glw_reposition (glw.c:2326-2338)
func glwReposition(rc *glwRctx, left, top, right, bottom int) {
	sx := float32(right-left) / float32(rc.rcWidth)
	tx := float32(-1.0) + float32(right+left)/float32(rc.rcWidth)
	sy := float32(top-bottom) / float32(rc.rcHeight)
	ty := float32(-1.0) + float32(top+bottom)/float32(rc.rcHeight)

	glwTranslatef(rc, tx, ty, 0)
	glwScalef(rc, sx, sy, glwMin(sx, sy))

	rc.rcWidth = int16(right - left)
	rc.rcHeight = int16(top - bottom)
}

// C: void glw_repositionf (glw.c:2344-2356)
func glwRepositionf(rc *glwRctx, left, top, right, bottom float32) {
	sx := (right - left) / float32(rc.rcWidth)
	tx := float32(-1.0) + (right+left)/float32(rc.rcWidth)
	sy := (top - bottom) / float32(rc.rcHeight)
	ty := float32(-1.0) + (top+bottom)/float32(rc.rcHeight)

	glwTranslatef(rc, tx, ty, 0)
	glwScalef(rc, sx, sy, glwMin(sx, sy))

	rc.rcWidth = int16(right - left)
	rc.rcHeight = int16(top - bottom)
}

// C: const glw_vertex_t align_vertices[] (glw.c:2574-2586)
var alignVertices = [10]Vec3{
	0:                      {0.0, 0.0, 0.0},
	layoutAlignCenter:      {0.0, 0.0, 0.0},
	layoutAlignLeft:        {-1.0, 0.0, 0.0},
	layoutAlignRight:       {1.0, 0.0, 0.0},
	layoutAlignBottom:      {0.0, -1.0, 0.0},
	layoutAlignTop:         {0.0, 1.0, 0.0},
	layoutAlignTopLeft:     {-1.0, 1.0, 0.0},
	layoutAlignTopRight:    {1.0, 1.0, 0.0},
	layoutAlignBottomLeft:  {-1.0, -1.0, 0.0},
	layoutAlignBottomRight: {1.0, -1.0, 0.0},
}

// C: void glw_align_1 (glw.c:2589-2597)
func glwAlign1(rc *glwRctx, a int) {
	if a != 0 && a != layoutAlignCenter {
		glwTranslatef(rc,
			alignVertices[a][0],
			alignVertices[a][1],
			alignVertices[a][2])
	}
}

// C: void glw_align_2 (glw.c:2600-2608)
func glwAlign2(rc *glwRctx, a int) {
	if a != 0 && a != 1 {
		glwTranslatef(rc,
			-alignVertices[a][0],
			-alignVertices[a][1],
			-alignVertices[a][2])
	}
}

// C: void glw_conf_constraints (glw.c:2614-2644)
func glwConfConstraints(w *Glw, x, y int, weight float32, conf int) {
	switch conf {
	case glwConstraintConfX:
		w.glwReqSizeX = int16(x)
		w.glwFlags |= glwConstraintX

	case glwConstraintConfY:
		w.glwReqSizeY = int16(y)
		w.glwFlags |= glwConstraintY

	case glwConstraintConfW:
		w.glwReqWeight = weight
		w.glwFlags |= glwConstraintW

	case glwConstraintConfD:
		w.glwFlags |= glwConstraintD

	default:
		panic("glw_conf_constraints")
	}
	w.glwFlags |= conf

	if w.glwParent != nil {
		glwSignal0(w.glwParent, glwSignalChildConstraintsChanged, w)
	}
}

// C: void glw_mod_constraints (glw.c:2650-2716)
func glwModConstraints(w *Glw, x, y int, weight float32, flags int,
	modflags int) {
	ch := 0
	fc := w.glwFlags ^ flags
	if modflags&glwConstraintX != 0 {
		if w.glwFlags&glwConstraintConfX == 0 {
			if fc&glwConstraintX != 0 {
				ch = 1
				w.glwFlags =
					(w.glwFlags &^ glwConstraintX) | (flags & glwConstraintX)
			}
			if w.glwFlags&glwConstraintX != 0 && w.glwReqSizeX != int16(x) {
				w.glwReqSizeX = int16(x)
				ch = 1
			}
		}
	}

	if modflags&glwConstraintY != 0 {
		if w.glwFlags&glwConstraintConfY == 0 {
			if fc&glwConstraintY != 0 {
				ch = 1
				w.glwFlags =
					(w.glwFlags &^ glwConstraintY) | (flags & glwConstraintY)
			}
			if w.glwFlags&glwConstraintY != 0 && w.glwReqSizeY != int16(y) {
				w.glwReqSizeY = int16(y)
				ch = 1
			}
		}
	}

	if modflags&glwConstraintW != 0 {
		if w.glwFlags&glwConstraintConfW == 0 {
			if fc&glwConstraintW != 0 {
				ch = 1
				w.glwFlags =
					(w.glwFlags &^ glwConstraintW) | (flags & glwConstraintW)
			}
			if w.glwFlags&glwConstraintW != 0 && w.glwReqWeight != weight {
				w.glwReqWeight = weight
				ch = 1
			}
		}
	}

	if modflags&glwConstraintD != 0 {
		if w.glwFlags&glwConstraintConfD == 0 {
			if fc&glwConstraintD != 0 {
				ch = 1
				w.glwFlags =
					(w.glwFlags &^ glwConstraintD) | (flags & glwConstraintD)
			}
		}
	}

	if ch != 0 && w.glwParent != nil {
		glwSignal0(w.glwParent, glwSignalChildConstraintsChanged, w)
	}
}

// C: void glw_set_constraints (glw.c:2721-2726)
func glwSetConstraints(w *Glw, x, y int, weight float32, flags int) {
	glwModConstraints(w, x, y, weight, flags, -1)
}

// C: void glw_clear_constraints (glw.c:2731-2771)
func glwClearConstraints(w *Glw) {
	ch := 0

	if w.glwFlags&glwConstraintConfX == 0 {
		if w.glwFlags&glwConstraintX != 0 {
			w.glwFlags &^= glwConstraintX
			w.glwReqSizeX = 0
			ch = 1
		}
	}

	if w.glwFlags&glwConstraintConfY == 0 {
		if w.glwFlags&glwConstraintY != 0 {
			w.glwFlags &^= glwConstraintY
			w.glwReqSizeY = 0
			ch = 1
		}
	}

	if w.glwFlags&glwConstraintConfW == 0 {
		if w.glwFlags&glwConstraintW != 0 {
			w.glwFlags &^= glwConstraintW
			w.glwReqWeight = 0
			ch = 1
		}
	}

	if w.glwFlags&glwConstraintConfD == 0 {
		if w.glwFlags&glwConstraintD != 0 {
			w.glwFlags &^= glwConstraintD
			ch = 1
		}
	}

	if ch == 0 {
		return
	}

	if w.glwParent != nil {
		glwSignal0(w.glwParent, glwSignalChildConstraintsChanged, w)
	}
}

// C: void glw_copy_constraints (glw.c:2777-2783)
func glwCopyConstraints(w *Glw, src *Glw) {
	glwSetConstraints(w,
		glwReqWidth(src),
		glwReqHeight(src),
		src.glwReqWeight,
		glwFilterConstraints(src))
}
