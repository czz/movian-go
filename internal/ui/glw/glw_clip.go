package glw

// Canonical port of src/ui/glw/glw_clip.c — the `clip` widget class.
//
// The `fader`/`stencil` classes in the same C file are gated behind
// `#if NUM_FADERS > 0`; the canonical build has NUM_FADERS=0 (and
// NUM_STENCILERS=0) so they are intentionally not ported.
//
// Note: "blurOutside" sets gc_alpha_outside (not gc_sharpness_outside) in C —
// latent upstream quirk preserved verbatim. gc_sharpness_outside is never
// assigned and stays 0.

import "unsafe"

// C: glw_clip_t
type glwClip struct {
	w Glw

	gcClipping      [4]float32
	gcPixelClipping [4]float32

	gcAlphaOutside     float32
	gcSharpnessOutside float32
}

// C: setval
func clipSetval(vp *float32, v float32) int {
	if *vp == v {
		return glwSetNoChange
	}
	*vp = v
	return glwSetRerenderRequired
}

// C: glw_clip_set_float_unresolved
func glwClipSetFloatUnresolved(w *Glw, a string, value float32, gs *GlwStyle) int {
	gc := (*glwClip)(unsafe.Pointer(w))

	switch a {
	case "left":
		return clipSetval(&gc.gcClipping[0], value)
	case "top":
		return clipSetval(&gc.gcClipping[1], value)
	case "right":
		return clipSetval(&gc.gcClipping[2], value)
	case "bottom":
		return clipSetval(&gc.gcClipping[3], value)

	case "leftPx":
		return clipSetval(&gc.gcPixelClipping[0], value)
	case "topPx":
		return clipSetval(&gc.gcPixelClipping[1], value)
	case "rightPx":
		return clipSetval(&gc.gcPixelClipping[2], value)
	case "bottomPx":
		return clipSetval(&gc.gcPixelClipping[3], value)

	case "alphaOutside":
		return clipSetval(&gc.gcAlphaOutside, value)

	case "blurOutside":
		return clipSetval(&gc.gcAlphaOutside, 1.0-glwClamp(value, 0, 1))
	}
	return glwSetNotResponding
}

// C: glw_clip_layout
func glwClipLayout(w *Glw, rc *glwRctx) {
	if w.glwAlpha < glwAlphaEpsilon {
		return
	}
	for c := w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		if c.glwFlags&glwHidden != 0 {
			continue
		}
		glwLayout0(c, rc)
	}
}

// C: glw_clip_render
func glwClipRender(w *Glw, rc *glwRctx) {
	gc := (*glwClip)(unsafe.Pointer(w))
	gr := w.glwRoot
	var clippers [4]int

	for i := range 4 {
		if gc.gcClipping[i] > 0 {
			clippers[i] = glwClipEnable(gr, rc, glwClipBoundary(i),
				gc.gcClipping[i], gc.gcAlphaOutside,
				gc.gcSharpnessOutside)
		} else if gc.gcPixelClipping[i] > 0 {
			denom := float32(rc.rcWidth)
			if i&1 != 0 {
				denom = float32(rc.rcHeight)
			}
			clippers[i] = glwClipEnable(gr, rc, glwClipBoundary(i),
				gc.gcPixelClipping[i]/denom, gc.gcAlphaOutside,
				gc.gcSharpnessOutside)
		} else {
			clippers[i] = -1
		}
	}

	for c := w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		glwRender0(c, rc)
	}

	for i := range 4 {
		glwClipDisable(w.glwRoot, clippers[i])
	}
}

// C: glw_clip_callback
func glwClipCallback(w *Glw, opaque any, signal glwSignal, extra any) int {
	switch signal {
	case glwSignalChildConstraintsChanged, glwSignalChildCreated:
		glwCopyConstraints(w, extra.(*Glw))
		return 1
	default:
		return 0
	}
}

// C: glw_class_t glw_clip
var glwClipClass = &glwClass{
	gcName:         "clip",
	gcInstanceSize: int(unsafe.Sizeof(glwClip{})),
	gcNew: func(parent *Glw) *Glw {
		gc := &glwClip{}
		return &gc.w
	},
	gcLayout:             glwClipLayout,
	gcRender:             glwClipRender,
	gcSignalHandler:      glwClipCallback,
	gcSetFloatUnresolved: glwClipSetFloatUnresolved,
}

func registerClip() {
	glwRegisterClass(glwClipClass)
}
