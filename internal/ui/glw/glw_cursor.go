package glw

// Canonical port of src/ui/glw/glw_cursor.c — the `cursor` widget class
// (tracks the focused widget and renders cursor/hover indicator
// children at the focused/hovered position with lerped matrix).

import "unsafe"

// C: glw_cursor_t
type glwCursor struct {
	w Glw

	gcInitialized bool
	gcMtx         Mtx
	gcCursorRctx  glwRctx

	gcHoverRctx glwRctx
	gcHoverSet  bool
}

// C: glw_cursor_layout
func glwCursorLayout(w *Glw, rc *glwRctx) {
	c := w.glwChilds.tqhFirst
	if c != nil {
		glwLayout0(c, rc)
	}
}

// C: render_focus_widget
func cursorRenderFocusWidget(w *Glw, gc *glwCursor, saved *Mtx,
	rc0 *glwRctx, rc *glwRctx, zmax *int) {
	gr := w.glwRoot

	if gc.w.glwFlags&glwInFocusPath == 0 {
		return
	}

	f := gr.grCurrentFocus

	if f.glwMatrix != nil {
		var aInv Mtx
		glwMtxInvert(&aInv, saved)
		b := f.glwMatrix

		var x Mtx
		glwMtxMul(&x, &aInv, b)

		if !gc.gcInitialized {
			gc.gcMtx = x
			gc.gcInitialized = true
		} else {
			for r := range 4 {
				for c := range 4 {
					glwLp(&gc.gcMtx.r[r][c], gr, x.r[r][c], 0.75)
				}
			}
		}
		glwMtxMul(&gc.gcCursorRctx.rcMtx, saved, &gc.gcMtx)
	}

	var cursorRect glwRect

	glwProject(&cursorRect, &gc.gcCursorRctx, gr)

	gc.gcCursorRctx.rcWidth = int16(cursorRect.x2 - cursorRect.x1)
	gc.gcCursorRctx.rcHeight = int16(cursorRect.y2 - cursorRect.y1)

	if gc.gcCursorRctx.rcWidth <= 0 {
		return
	}

	if gc.gcCursorRctx.rcHeight <= 0 {
		return
	}

	gc.gcCursorRctx.rcAlpha = 1.0
	gc.gcCursorRctx.rcSharpness = 1.0

	glwLayout0(w, &gc.gcCursorRctx)

	rc0.rcZindex = int16(max(*zmax, int(rc.rcZindex)))
	gc.gcCursorRctx.rcZmax = rc0.rcZmax
	gc.gcCursorRctx.rcZindex = rc0.rcZindex
	glwRender0(w, &gc.gcCursorRctx)
}

// C: render_hover_widget
func cursorRenderHoverWidget(w *Glw, gc *glwCursor,
	rc0 *glwRctx, rc *glwRctx, zmax *int) {
	if gc.w.glwFlags&glwInHoverPath == 0 {
		return
	}

	gc.gcHoverRctx.rcAlpha = 1.0
	gc.gcHoverRctx.rcSharpness = 1.0

	glwLayout0(w, &gc.gcHoverRctx)

	rc0.rcZindex = int16(max(*zmax, int(rc.rcZindex)))
	gc.gcHoverRctx.rcZmax = rc0.rcZmax
	gc.gcHoverRctx.rcZindex = rc0.rcZindex
	glwRender0(w, &gc.gcHoverRctx)
}

// C: glw_cursor_focus_tracker
func glwCursorFocusTracker(w *Glw, rc *glwRctx, cursor *Glw) {
	gc := (*glwCursor)(unsafe.Pointer(cursor))

	if w.glwFlags&glwInHoverPath == 0 {
		return
	}

	gc.gcHoverRctx = *rc
	gc.gcHoverSet = true
}

// C: glw_cursor_render
func glwCursorRender(w *Glw, rc *glwRctx) {
	gr := w.glwRoot
	gc := (*glwCursor)(unsafe.Pointer(w))
	var rc0 glwRctx
	zmax := 0
	var saved Mtx

	if w.glwAlpha < glwAlphaEpsilon {
		return
	}

	c := w.glwChilds.tqhFirst
	if c == nil {
		return
	}

	saved = rc.rcMtx

	rc0 = *rc
	rc0.rcZmax = &zmax

	gc.gcHoverSet = false

	savedCursor := gr.grCurrentCursor
	savedFocusTracker := gr.grCursorFocusTracker

	gr.grCursorFocusTracker = glwCursorFocusTracker
	gr.grCurrentCursor = w

	glwRender0(c, &rc0)

	gr.grCursorFocusTracker = savedFocusTracker
	gr.grCurrentCursor = savedCursor

	glwZinc(&rc0)

	c = c.glwParentLinkNext
	if c != nil {
		cursorRenderFocusWidget(c, gc, &saved, &rc0, rc, &zmax)

		if gr.grKeyboardMode == 0 {
			c = c.glwParentLinkNext
			if c != nil {
				cursorRenderHoverWidget(c, gc, &rc0, rc, &zmax)
			}
		}
	}

	if rc.rcZmax != nil {
		*rc.rcZmax = max(*rc.rcZmax, zmax)
	}
}

// C: glw_cursor_callback
func glwCursorCallback(w *Glw, opaque any, signal glwSignal, extra any) int {
	switch signal {
	case glwSignalChildConstraintsChanged, glwSignalChildCreated:
		if extra == w.glwChilds.tqhFirst {
			glwCopyConstraints(w, extra.(*Glw))
		}
		return 1
	}
	return 0
}

// C: glw_class_t glw_cursor
var glwCursorClass = &glwClass{
	gcName:         "cursor",
	gcInstanceSize: int(unsafe.Sizeof(glwCursor{})),
	gcNew: func(parent *Glw) *Glw {
		gc := &glwCursor{}
		return &gc.w
	},
	gcRender:        glwCursorRender,
	gcSignalHandler: glwCursorCallback,
	gcLayout:        glwCursorLayout,
}

func registerCursor() {
	glwRegisterClass(glwCursorClass)
}
