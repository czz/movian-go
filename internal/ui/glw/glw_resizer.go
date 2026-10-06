package glw

// Canonical port of src/ui/glw/glw_resizer.c — the `resizer` widget
// class (expands the child's area beyond the incoming constraints).

import "unsafe"

// C: get_width
func glwResizerGetWidth(w *Glw, rc *glwRctx) int {
	c := w.glwChilds.tqhFirst
	if c == nil {
		return int(rc.rcWidth)
	}
	rw := glwReqWidth(c)
	return max(rw, int(rc.rcWidth))
}

// C: get_height
func glwResizerGetHeight(w *Glw, rc *glwRctx) int {
	c := w.glwChilds.tqhFirst
	if c == nil {
		return int(rc.rcHeight)
	}
	rh := glwReqHeight(c)
	return max(rh, int(rc.rcHeight))
}

// C: glw_resizer_layout
func glwResizerLayout(w *Glw, rc *glwRctx) {
	c := w.glwChilds.tqhFirst
	if c == nil {
		return
	}
	rc0 := *rc

	rc0.rcWidth = int16(glwResizerGetWidth(w, rc))
	rc0.rcHeight = int16(glwResizerGetHeight(w, rc))
	glwLayout0(c, &rc0)
}

// C: glw_resizer_render
func glwResizerRender(w *Glw, rc *glwRctx) {
	c := w.glwChilds.tqhFirst
	if c == nil {
		return
	}

	width := glwResizerGetWidth(w, rc)
	height := glwResizerGetHeight(w, rc)

	expX := max(width-int(rc.rcWidth), 0)
	expY := max(height-int(rc.rcHeight), 0)

	rc0 := *rc
	glwReposition(&rc0, 0, int(rc.rcHeight), int(rc.rcWidth)+expX, -expY)
	glwRender0(c, &rc0)
}

// C: glw_resizer_set_int_unresolved — `fixedWidth` parsed but unused
// in C (assignment commented out); preserved verbatim.
func glwResizerSetIntUnresolved(w *Glw, a string, value int, gs *GlwStyle) int {
	if a == "fixedWidth" {
		return glwSetRerenderRequired
	}
	return glwSetNotResponding
}

// C: glw_class_t glw_resizer
var glwResizerClass = &glwClass{
	gcName:         "resizer",
	gcInstanceSize: int(unsafe.Sizeof(Glw{})),
	gcNew: func(parent *Glw) *Glw {
		return &Glw{}
	},
	gcLayout:           glwResizerLayout,
	gcRender:           glwResizerRender,
	gcSetIntUnresolved: glwResizerSetIntUnresolved,
}

func registerResizer() {
	glwRegisterClass(glwResizerClass)
}
