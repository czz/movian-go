package glw

// Canonical port of src/ui/glw/glw_rotator.c — the `rotator` widget class.

import "unsafe"

// C: glw_rotator_t
type glwRotator struct {
	w     Glw
	theta float32
}

// C: glw_rotator_layout
func glwRotatorLayout(w *Glw, rc *glwRctx) {
	gr := (*glwRotator)(unsafe.Pointer(w))

	gr.theta -= 5
	c := w.glwChilds.tqhFirst
	if c != nil {
		glwLayout0(c, rc)
	}
}

// C: glw_rotator_render
func glwRotatorRender(w *Glw, rc *glwRctx) {
	gr := (*glwRotator)(unsafe.Pointer(w))
	var rc0 glwRctx

	c := w.glwChilds.tqhFirst
	if c == nil {
		return
	}

	rc0 = *rc

	glwScalef(&rc0, 0.8, 0.8, 0.8)
	glwScaleToAspect(&rc0, 1.0)

	glwRotatef(&rc0, gr.theta, 0.0, 0.0, 1.0)

	glwRender0(c, &rc0)
}

// C: glw_class_t glw_rotator
var glwRotatorClass = &glwClass{
	gcName:         "rotator",
	gcInstanceSize: int(unsafe.Sizeof(glwRotator{})),
	gcNew: func(parent *Glw) *Glw {
		gr := &glwRotator{}
		return &gr.w
	},
	gcLayout: glwRotatorLayout,
	gcRender: glwRotatorRender,
}

func registerRotator() {
	glwRegisterClass(glwRotatorClass)
}
