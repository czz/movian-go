package glw

// Canonical port of src/ui/glw/glw_flicker.c — the `flicker` widget
// class (alternating two-phase quad renderer).

import "unsafe"

// C: glw_flicker_t
type glwFlicker struct {
	w Glw

	gfGrStarted int
	gfGr        [2]glwRenderer

	gfPhase int
}

// C: glw_flicker_dtor
func glwFlickerDtor(w *Glw) {
	gf := (*glwFlicker)(unsafe.Pointer(w))

	glwRendererFree(&gf.gfGr[0])
	glwRendererFree(&gf.gfGr[1])
}

// C: glw_flicker_render
func glwFlickerRender(w *Glw, rc *glwRctx) {
	gf := (*glwFlicker)(unsafe.Pointer(w))
	a := rc.rcAlpha * w.glwAlpha

	if a > glwAlphaEpsilon {
		v := 1.0*float32(gf.gfPhase) + 0.25

		rgb := glwRgb{v, v, v}
		glwRendererDraw(&gf.gfGr[0], w.glwRoot, rc,
			nil, nil, &rgb, nil, a, 0, nil)

		v = 1.0*float32(1-gf.gfPhase) + 0.25
		rgb2 := glwRgb{v, v, v}
		glwRendererDraw(&gf.gfGr[1], w.glwRoot, rc,
			nil, nil, &rgb2, nil, a, 0, nil)
	}
}

// C: glw_flicker_layout
// NOTE: C never sets gf_gr_initialized, so the quads are re-initialized
// on every layout — preserved verbatim.
func glwFlickerLayout(w *Glw, rc *glwRctx) {
	gf := (*glwFlicker)(unsafe.Pointer(w))

	if gf.gfGrStarted == 0 {
		glwRendererSetupQuad(&gf.gfGr[0])
		glwRendererSetupQuad(&gf.gfGr[1])

		for i := range 2 {
			glwRendererVtxPos(&gf.gfGr[i], 0, float32(i)+-1.0, -1.0, 0.0)
			glwRendererVtxPos(&gf.gfGr[i], 1, float32(i)+0.0, -1.0, 0.0)
			glwRendererVtxPos(&gf.gfGr[i], 2, float32(i)+0.0, 1.0, 0.0)
			glwRendererVtxPos(&gf.gfGr[i], 3, float32(i)+-1.0, 1.0, 0.0)
		}
	}
	gf.gfPhase = 1 - gf.gfPhase
}

// C: glw_class_t glw_flicker
var glwFlickerClass = &glwClass{
	gcName:         "flicker",
	gcInstanceSize: int(unsafe.Sizeof(glwFlicker{})),
	gcNew: func(parent *Glw) *Glw {
		gf := &glwFlicker{}
		return &gf.w
	},
	gcLayout: glwFlickerLayout,
	gcRender: glwFlickerRender,
	gcDtor:   glwFlickerDtor,
}

func registerFlicker() {
	glwRegisterClass(glwFlickerClass)
}
