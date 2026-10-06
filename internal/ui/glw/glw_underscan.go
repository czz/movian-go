package glw

// Canonical port of src/ui/glw/glw_underscan.c — the `underscan` widget
// class (shrinks children by the root underscan margins).

import "unsafe"

// C: layout
func glwUnderscanLayout(w *Glw, rc *glwRctx) {
	rc0 := *rc

	if w.glwAlpha < glwAlphaEpsilon {
		return
	}

	if rc0.rcOverscanning != 0 {
		rc0.rcWidth = rc.rcWidth - int16(2*w.glwRoot.grUnderscanH)
		rc0.rcHeight = rc.rcHeight - int16(2*w.glwRoot.grUnderscanV)
		rc0.rcOverscanning = 0
	}

	for c := w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		if c.glwFlags&glwHidden != 0 {
			continue
		}
		glwLayout0(c, &rc0)
	}
}

// C: render
func glwUnderscanRender(w *Glw, rc *glwRctx) {
	alpha := rc.rcAlpha * w.glwAlpha

	if alpha < glwAlphaEpsilon {
		return
	}
	rc0 := *rc
	if rc0.rcOverscanning != 0 {
		ho := w.glwRoot.grUnderscanH
		vo := w.glwRoot.grUnderscanV
		glwReposition(&rc0, ho, int(rc.rcHeight)-vo, int(rc.rcWidth)-ho, vo)
		rc0.rcOverscanning = 0
	}

	for c := w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		if c.glwFlags&glwHidden != 0 {
			continue
		}
		glwRender0(c, &rc0)
	}
	if w.glwFlags2&glw2Debug != 0 {
		glwWirebox(w.glwRoot, &rc0)
	}
}

// C: glw_class_t glw_underscan
var glwUnderscanClass = &glwClass{
	gcName:         "underscan",
	gcInstanceSize: int(unsafe.Sizeof(Glw{})),
	gcNew: func(parent *Glw) *Glw {
		return &Glw{}
	},
	gcLayout: glwUnderscanLayout,
	gcRender: glwUnderscanRender,
}

func registerUnderscan() {
	glwRegisterClass(glwUnderscanClass)
}
