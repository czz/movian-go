package glw

// Canonical port of src/ui/glw/glw_detachable.c — the `detachable`
// widget class (child can be detached and re-rendered elsewhere,
// e.g. by glw_playfield transitions).

import "unsafe"

// C: glw_detachable_t
type glwDetachable struct {
	w             Glw
	on            int
	width, height int16
}

// C: glw_detachable_layout
func glwDetachableLayout(w *Glw, rc *glwRctx) {
	c := w.glwChilds.tqhFirst
	if c != nil {
		glwLayout0(c, rc)
	}
}

// C: glw_detachable_callback
func glwDetachableCallback(w *Glw, opaque any, signal glwSignal, extra any) int {
	if signal == glwSignalChildConstraintsChanged {
		c, _ := extra.(*Glw)
		glwCopyConstraints(w, c)
		return 1
	}
	return 0
}

// C: glw_detachable_render
func glwDetachableRender(w *Glw, rc *glwRctx) {
	gd := (*glwDetachable)(unsafe.Pointer(w))

	glwStoreMatrix(w, rc)
	gd.width = rc.rcWidth
	gd.height = rc.rcHeight

	if gd.on != 0 {
		return
	}

	c := w.glwChilds.tqhFirst
	if c == nil {
		return
	}

	glwRender0(c, rc)
}

// C: glw_detach_control
func glwDetachControl(w *Glw, on int) {
	gd := (*glwDetachable)(unsafe.Pointer(w))
	gd.on = on
}

// C: glw_detach_get_rctx
func glwDetachGetRctx(w *Glw, rc *glwRctx) {
	gd := (*glwDetachable)(unsafe.Pointer(w))

	rc.rcMtx = *w.glwMatrix
	rc.rcWidth = gd.width
	rc.rcHeight = gd.height
}

// C: glw_class_t glw_detachable
var glwDetachableClass = &glwClass{
	gcName:         "detachable",
	gcInstanceSize: int(unsafe.Sizeof(glwDetachable{})),
	gcNew: func(parent *Glw) *Glw {
		gd := &glwDetachable{}
		return &gd.w
	},
	gcLayout:        glwDetachableLayout,
	gcRender:        glwDetachableRender,
	gcSignalHandler: glwDetachableCallback,
	gcDetachControl: glwDetachControl,
	gcGetRctx:       glwDetachGetRctx,
}

func registerDetachable() {
	glwRegisterClass(glwDetachableClass)
}
