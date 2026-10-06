package glw

// Canonical port of src/ui/glw/glw_mirror.c — the `mirror` widget class
// (renders the child twice: normal + vertically flipped reflection).

import "unsafe"

// C: glw_mirror_layout
func glwMirrorLayout(w *Glw, rc *glwRctx) {
	c := w.glwChilds.tqhFirst
	if c != nil {
		glwLayout0(c, rc)
	}
}

// C: glw_mirror_callback
func glwMirrorCallback(w *Glw, opaque any, signal glwSignal, extra any) int {
	switch signal {
	case glwSignalChildConstraintsChanged, glwSignalChildCreated:
		c, _ := extra.(*Glw)
		glwCopyConstraints(w, c)
		return 1
	}
	return 0
}

// C: glw_mirror_render
func glwMirrorRender(w *Glw, rc *glwRctx) {
	var rc0 glwRctx

	c := w.glwChilds.tqhFirst
	if c == nil {
		return
	}

	b := glwClipEnable(w.glwRoot, rc, glwClipBottom, 0, 0, 1)
	glwRender0(c, rc)
	glwClipDisable(w.glwRoot, b)

	rc0 = *rc

	glwTranslatef(&rc0, 0, -1, 0)
	glwScalef(&rc0, 1.0, -1.0, 1.0)
	glwTranslatef(&rc0, 0, 1, 0)

	glwFrontface(w.glwRoot, glwCw)

	rc0.rcAlpha *= w.glwAlpha
	rc0.rcInhibitMatrixStore = 1

	b = glwClipEnable(w.glwRoot, &rc0, glwClipBottom, 0, 0, 1)
	glwRender0(c, &rc0)
	glwClipDisable(w.glwRoot, b)

	glwFrontface(w.glwRoot, glwCcw)
}

// C: glw_class_t glw_mirror
var glwMirrorClass = &glwClass{
	gcName:         "mirror",
	gcInstanceSize: int(unsafe.Sizeof(Glw{})),
	gcNew: func(parent *Glw) *Glw {
		return &Glw{}
	},
	gcLayout:        glwMirrorLayout,
	gcRender:        glwMirrorRender,
	gcSignalHandler: glwMirrorCallback,
}

func registerMirror() {
	glwRegisterClass(glwMirrorClass)
}
