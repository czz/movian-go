package glw

// Canonical port of src/ui/glw/glw_dummy.c — the `dummy` widget class
// (layout/render no-op used as a placeholder).

import "unsafe"

// C: glw_dummy_layout
func glwDummyLayout(w *Glw, rc *glwRctx) {
}

// C: glw_dummy_render
func glwDummyRender(w *Glw, rc *glwRctx) {
	if glwIsFocusableOrClickable(w) {
		glwStoreMatrix(w, rc)
	}
}

// C: glw_class_t glw_dummy
var glwDummyClass = &glwClass{
	gcName:         "dummy",
	gcInstanceSize: int(unsafe.Sizeof(Glw{})),
	gcNew: func(parent *Glw) *Glw {
		return &Glw{}
	},
	gcLayout: glwDummyLayout,
	gcRender: glwDummyRender,
}

func registerDummy() {
	glwRegisterClass(glwDummyClass)
}
