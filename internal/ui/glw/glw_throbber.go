package glw

// Canonical port of src/ui/glw/glw_throbber.c — throbber3d, throbber and
// throbbertri widget classes (busy-spinners rendered with glw_renderer).

import "unsafe"

// ---------------------------------------------------------------------------
// throbber3d

// C: glw_throbber3d_t
type glwThrobber3d struct {
	w Glw

	angle float32

	renderer glwRenderer
}

const (
	pinWidth  = 0.05
	pinBottom = 0.2
	pinTop    = 1.0
)

// C: pin[] vertex table
var throbberPin = [8][3]float32{
	{-pinWidth, pinBottom, pinWidth},
	{pinWidth, pinBottom, pinWidth},
	{pinWidth, pinBottom, -pinWidth},
	{-pinWidth, pinBottom, -pinWidth},

	{-pinWidth, pinTop, pinWidth},
	{pinWidth, pinTop, pinWidth},
	{pinWidth, pinTop, -pinWidth},
	{-pinWidth, pinTop, -pinWidth},
}

// C: surfaces[]
var throbberSurfaces = []uint16{
	0, 1, 5, 0, 5, 4,
	2, 3, 7, 2, 7, 6,

	8 + 3, 8 + 0, 8 + 4, 8 + 3, 8 + 4, 8 + 7,
	8 + 1, 8 + 2, 8 + 6, 8 + 1, 8 + 6, 8 + 5,
	8 + 3, 8 + 2, 8 + 1, 8 + 3, 8 + 1, 8 + 0,
	8 + 4, 8 + 5, 8 + 6, 8 + 4, 8 + 6, 8 + 7,
}

// C: glw_throbber3d_layout
func glwThrobber3dLayout(w *Glw, rc *glwRctx) {
	gt := (*glwThrobber3d)(unsafe.Pointer(w))
	if w.glwAlpha < glwAlphaEpsilon {
		return
	}

	gt.angle += 2
	if gt.angle > 3600 {
		gt.angle -= 3600
	}
	glwNeedRefresh(w.glwRoot, 0)
}

// C: glw_throbber3d_render
func glwThrobber3dRender(w *Glw, rc *glwRctx) {
	gt := (*glwThrobber3d)(unsafe.Pointer(w))
	var rc0, rc1 glwRctx
	gr := w.glwRoot
	a0 := w.glwAlpha * rc.rcAlpha
	if a0 < glwAlphaEpsilon {
		return
	}

	if !glwRendererStarted(&gt.renderer) {
		glwRendererSetup(&gt.renderer, 16, 12, throbberSurfaces)

		for i := range 16 {
			p := throbberPin[i&7]
			glwRendererVtxPos(&gt.renderer, i, p[0], p[1], p[2])
			if i < 8 {
				glwRendererVtxCol(&gt.renderer, i, 1, 1, 1, 1)
			} else {
				glwRendererVtxCol(&gt.renderer, i, 0.25, 0.25, 0.25, 1)
			}
		}
	}

	rc0 = *rc
	glwScaleToAspect(&rc0, 1.0)

	glwBlendmode(gr, glwBlendAdditive)

	const numpins = 15

	for i := 1; i < numpins; i++ {
		alpha := (1 - (float32(i) / numpins)) * rc.rcAlpha * w.glwAlpha

		rc1 = rc0
		glwRotatef(&rc1, 0.1*gt.angle-float32(i)*((360.0/numpins)/3), 0, 1, 0)
		glwRotatef(&rc1, gt.angle-float32(i)*(360.0/numpins), 0, 0, 1)

		glwRendererDraw(&gt.renderer, gr, &rc1,
			nil, nil, nil, nil, alpha, 0, nil)
	}
	glwBlendmode(gr, glwBlendNormal)
}

// C: glw_throbber3d_dtor
func glwThrobber3dDtor(w *Glw) {
	gt := (*glwThrobber3d)(unsafe.Pointer(w))
	glwRendererFree(&gt.renderer)
}

// ---------------------------------------------------------------------------
// throbber

// C: glw_throbber_t
type glwThrobber struct {
	w Glw

	angle float32

	renderer glwRenderer
	o        int
	color    glwRgb
}

// C: glw_throbber_layout
func glwThrobberLayout(w *Glw, rc *glwRctx) {
	gt := (*glwThrobber)(unsafe.Pointer(w))
	if w.glwAlpha < glwAlphaEpsilon {
		return
	}

	glwNeedRefresh(w.glwRoot, 0)
	gt.angle += 0.5
	if gt.angle > 360 {
		gt.angle -= 360
	}
	gt.o++
}

// C: glw_throbber_render
func glwThrobberRender(w *Glw, rc *glwRctx) {
	gt := (*glwThrobber)(unsafe.Pointer(w))
	var rc0, rc1 glwRctx
	gr := w.glwRoot
	a0 := w.glwAlpha * rc.rcAlpha
	spokes := 16

	if a0 < glwAlphaEpsilon {
		return
	}

	if !glwRendererStarted(&gt.renderer) {
		glwRendererSetupQuad(&gt.renderer)

		glwRendererVtxPos(&gt.renderer, 0, -0.05, 0.4, 0)
		glwRendererVtxPos(&gt.renderer, 1, 0.05, 0.4, 0)
		glwRendererVtxPos(&gt.renderer, 2, 0.05, 1, 0)
		glwRendererVtxPos(&gt.renderer, 3, -0.05, 1, 0)
	}

	rc0 = *rc
	glwScaleToAspect(&rc0, 1.0)

	for i := range spokes {
		a := float32(i) * 360.0 / 16
		alpha := 1 - (float32((i+(gt.o/6))%spokes) / 16.0)
		if alpha < 0.1 {
			alpha = 0.1
		}

		rc1 = rc0
		glwRotatef(&rc1, -gt.angle-a, 0, 0, -1)

		glwRendererDraw(&gt.renderer, gr, &rc1,
			nil, nil,
			&gt.color, nil, a0*alpha, 0, nil)
	}
}

// C: glw_throbber_dtor
func glwThrobberDtor(w *Glw) {
	gt := (*glwThrobber)(unsafe.Pointer(w))
	glwRendererFree(&gt.renderer)
}

// C: glw_throbber_ctor
func glwThrobberCtor(w *Glw) {
	gt := (*glwThrobber)(unsafe.Pointer(w))
	gt.color.r = 1.0
	gt.color.g = 1.0
	gt.color.b = 1.0
}

// C: glw_throbber_set_float3
func glwThrobberSetFloat3(w *Glw, attrib glwAttribute, rgb []float32, origin *GlwStyle) int {
	gt := (*glwThrobber)(unsafe.Pointer(w))

	switch attrib {
	case glwAttribRGB:
		return glwAttribSetRgb(&gt.color, (*[3]float32)(rgb))
	default:
		return -1
	}
}

// ---------------------------------------------------------------------------
// throbbertri

// C: glw_throbber_tri_t
type glwThrobberTri struct {
	w Glw

	angle float32

	renderer glwRenderer
	o        int
}

// C: glw_throbber_tri_layout
func glwThrobberTriLayout(w *Glw, rc *glwRctx) {
	gt := (*glwThrobberTri)(unsafe.Pointer(w))
	if w.glwAlpha < glwAlphaEpsilon {
		return
	}

	glwNeedRefresh(w.glwRoot, 0)
	gt.angle += 0.8
	if gt.angle > 360 {
		gt.angle -= 360
	}
	gt.o++
}

// C: glw_throbber_tri_render
func glwThrobberTriRender(w *Glw, rc *glwRctx) {
	gt := (*glwThrobberTri)(unsafe.Pointer(w))
	var rc0, rc1 glwRctx
	gr := w.glwRoot
	a0 := w.glwAlpha * rc.rcAlpha

	if a0 < glwAlphaEpsilon {
		return
	}

	if !glwRendererStarted(&gt.renderer) {
		glwRendererSetupTriangle(&gt.renderer)

		glwRendererVtxPos(&gt.renderer, 0, -0.866, -1, 0)
		glwRendererVtxCol(&gt.renderer, 0, 0.20, 0.25, 0.81, 1)
		glwRendererVtxPos(&gt.renderer, 1, 0.886, 0, 0)
		glwRendererVtxCol(&gt.renderer, 1, 0.19, 0.39, 0.90, 1)
		glwRendererVtxPos(&gt.renderer, 2, -0.866, 1, 0)
		glwRendererVtxCol(&gt.renderer, 2, 0.28, 0.58, 0.90, 1)
	}

	rc0 = *rc
	glwScaleToAspect(&rc0, 1.0)

	glwRotatef(&rc0, gt.angle/3, 0, 0, -1)

	rc1 = rc0
	glwTranslatef(&rc1, -0.886/2, 0.5, 0)
	glwScalef(&rc1, 0.5, 0.5, 1.0)
	glwRotatef(&rc1, -gt.angle, 0, 0, -1)
	glwRendererDraw(&gt.renderer, gr, &rc1,
		nil, nil, nil, nil, a0, 0, nil)

	rc1 = rc0
	glwTranslatef(&rc1, -0.886/2, -0.5, 0)
	glwScalef(&rc1, 0.5, 0.5, 1.0)
	glwRotatef(&rc1, -gt.angle, 0, 0, -1)
	glwRendererDraw(&gt.renderer, gr, &rc1,
		nil, nil, nil, nil, a0, 0, nil)

	rc1 = rc0
	glwTranslatef(&rc1, 0.886/2, 0, 0)
	glwScalef(&rc1, 0.5, 0.5, 1.0)
	glwRotatef(&rc1, -gt.angle, 0, 0, -1)
	glwRendererDraw(&gt.renderer, gr, &rc1,
		nil, nil, nil, nil, a0, 0, nil)
}

// C: glw_throbber_tri_dtor
func glwThrobberTriDtor(w *Glw) {
	gt := (*glwThrobberTri)(unsafe.Pointer(w))
	glwRendererFree(&gt.renderer)
}

// ---------------------------------------------------------------------------
// C: glw_class_t glw_throbber3d / glw_throbber / glw_throbber_tri

var glwThrobber3dClass = &glwClass{
	gcName:         "throbber3d",
	gcInstanceSize: int(unsafe.Sizeof(glwThrobber3d{})),
	gcNew: func(parent *Glw) *Glw {
		gt := &glwThrobber3d{}
		return &gt.w
	},
	gcRender: glwThrobber3dRender,
	gcLayout: glwThrobber3dLayout,
	gcDtor:   glwThrobber3dDtor,
}

var glwThrobberClass = &glwClass{
	gcName:         "throbber",
	gcInstanceSize: int(unsafe.Sizeof(glwThrobber{})),
	gcNew: func(parent *Glw) *Glw {
		gt := &glwThrobber{}
		return &gt.w
	},
	gcRender:    glwThrobberRender,
	gcLayout:    glwThrobberLayout,
	gcCtor:      glwThrobberCtor,
	gcDtor:      glwThrobberDtor,
	gcSetFloat3: glwThrobberSetFloat3,
}

var glwThrobberTriClass = &glwClass{
	gcName:         "throbbertri",
	gcInstanceSize: int(unsafe.Sizeof(glwThrobberTri{})),
	gcNew: func(parent *Glw) *Glw {
		gt := &glwThrobberTri{}
		return &gt.w
	},
	gcRender: glwThrobberTriRender,
	gcLayout: glwThrobberTriLayout,
	gcDtor:   glwThrobberTriDtor,
}

func registerThrobber() {
	glwRegisterClass(glwThrobber3dClass)
	glwRegisterClass(glwThrobberClass)
	glwRegisterClass(glwThrobberTriClass)
}
