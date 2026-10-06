package glw

// Canonical port of src/ui/glw/glw_bar.c — the `bar` widget class
// (a two-color progress/fill bar drawn with a quad renderer).

import "unsafe"

// C: glw_bar_t
type glwBar struct {
	w Glw

	gbCol1 [3]float32
	gbCol2 [3]float32

	gbGr glwRenderer

	gbFill float32

	gbUpdate   uint8 // C: char
	gbIsActive uint8 // C: char
}

// C: glw_bar_dtor
func glwBarDtor(w *Glw) {
	gb := (*glwBar)(unsafe.Pointer(w))
	glwRendererFree(&gb.gbGr)
}

// C: glw_bar_render
func glwBarRender(w *Glw, rc *glwRctx) {
	gb := (*glwBar)(unsafe.Pointer(w))
	a := rc.rcAlpha * w.glwAlpha

	if gb.gbCol1[0] < 0.001 &&
		gb.gbCol1[1] < 0.001 &&
		gb.gbCol1[2] < 0.001 &&
		gb.gbCol2[0] < 0.001 &&
		gb.gbCol2[1] < 0.001 &&
		gb.gbCol2[2] < 0.001 {
		return
	}
	if a > glwAlphaEpsilon {
		glwRendererDraw(&gb.gbGr, w.glwRoot, rc,
			nil, nil, nil, nil, a, 0, nil)
	}
}

// C: glw_bar_layout
func glwBarLayout(w *Glw, rc *glwRctx) {
	gb := (*glwBar)(unsafe.Pointer(w))
	var r, g, b, x float32

	if w.glwAlpha < glwAlphaEpsilon {
		return
	}

	if !glwRendererStarted(&gb.gbGr) {
		glwRendererSetupQuad(&gb.gbGr)
		gb.gbUpdate = 1
	}

	if gb.gbUpdate != 0 {
		gb.gbUpdate = 0

		r = glwLerp(gb.gbFill, gb.gbCol1[0], gb.gbCol2[0])
		g = glwLerp(gb.gbFill, gb.gbCol1[1], gb.gbCol2[1])
		b = glwLerp(gb.gbFill, gb.gbCol1[2], gb.gbCol2[2])
		x = glwLerp(gb.gbFill, -1, 1)

		glwRendererVtxPos(&gb.gbGr, 0, -1.0, -1.0, 0.0)
		glwRendererVtxCol(&gb.gbGr, 0,
			gb.gbCol1[0], gb.gbCol1[1], gb.gbCol1[2], 1.0)

		glwRendererVtxPos(&gb.gbGr, 1, x, -1.0, 0.0)
		glwRendererVtxCol(&gb.gbGr, 1, r, g, b, 1.0)

		glwRendererVtxPos(&gb.gbGr, 2, x, 1.0, 0.0)
		glwRendererVtxCol(&gb.gbGr, 2, r, g, b, 1.0)

		glwRendererVtxPos(&gb.gbGr, 3, -1.0, 1.0, 0.0)
		glwRendererVtxCol(&gb.gbGr, 3,
			gb.gbCol1[0], gb.gbCol1[1], gb.gbCol1[2], 1.0)

		glwNeedRefresh(w.glwRoot, 0)
	}
}

// C: glw_bar_set_float
func glwBarSetFloat(w *Glw, attrib glwAttribute, value float32, gs *GlwStyle) int {
	gb := (*glwBar)(unsafe.Pointer(w))

	switch attrib {
	case glwAttribFill:
		if value > 1.0 {
			value = 1.0
		}
		if gb.gbFill == value {
			return 0
		}
		gb.gbFill = value
		gb.gbUpdate = 1
		if w.glwFlags&glwActive != 0 {
			return glwRefreshLayoutOnly
		}
		return 0
	default:
		return -1
	}
}

// C: glw_bar_set_float3
func glwBarSetFloat3(w *Glw, attrib glwAttribute, vector []float32, gs *GlwStyle) int {
	gb := (*glwBar)(unsafe.Pointer(w))

	switch attrib {
	case glwAttribColor1:
		if glwAttribSetFloat3Clamped(&gb.gbCol1, (*[3]float32)(vector)) == 0 {
			return 0
		}
	case glwAttribColor2:
		if glwAttribSetFloat3Clamped(&gb.gbCol2, (*[3]float32)(vector)) == 0 {
			return 0
		}
	default:
		return -1
	}
	gb.gbUpdate = 1
	return 1
}

// C: glw_class_t glw_bar
var glwBarClass = &glwClass{
	gcName:         "bar",
	gcInstanceSize: int(unsafe.Sizeof(glwBar{})),
	gcNew: func(parent *Glw) *Glw {
		gb := &glwBar{}
		return &gb.w
	},
	gcRender:    glwBarRender,
	gcSetFloat:  glwBarSetFloat,
	gcDtor:      glwBarDtor,
	gcSetFloat3: glwBarSetFloat3,
	gcLayout:    glwBarLayout,
}

func registerBar() {
	glwRegisterClass(glwBarClass)
}
