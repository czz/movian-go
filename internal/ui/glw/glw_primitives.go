package glw

// Canonical port of src/ui/glw/glw_primitives.c — the `quad`, `border`,
// and `linebox` widget classes (colored quads, 9-patch-style borders,
// and a debug wireframe box).

import (
	"unsafe"

	miscpkg "github.com/czz/movian-go/internal/misc"
)

// C: glw_quad_t (shared by quad + border classes)
type glwQuad struct {
	w Glw

	color     glwRgb
	r         glwRenderer
	fs        *miscpkg.Rstr
	qPadding  [4]int16
	border    [4]int16
	gpa       GlwProgramArgs
	width     int16
	height    int16
	recompile uint8 // C: char
	relayout  uint8 // C: char
}

// C: glw_quad_render
func glwQuadRender(w *Glw, rc *glwRctx) {
	q := (*glwQuad)(unsafe.Pointer(w))

	if q.recompile != 0 {
		glwDestroyProgram(w.glwRoot, q.gpa.GpaProg)
		q.gpa.GpaProg = glwMakeProgram(w.glwRoot, "", miscpkg.RstrGet(q.fs))
		q.recompile = 0
	}

	rc0 := *rc
	glwReposition(&rc0, int(q.qPadding[0]), int(rc.rcHeight)-int(q.qPadding[1]),
		int(rc.rcWidth)-int(q.qPadding[2]), int(q.qPadding[3]))

	var gpa *GlwProgramArgs
	if q.gpa.GpaProg != nil {
		gpa = &q.gpa
	}
	glwRendererDraw(&q.r, w.glwRoot, &rc0,
		nil, nil,
		&q.color, nil, rc.rcAlpha*w.glwAlpha, 0,
		gpa)
}

// C: glw_quad_layout
func glwQuadLayout(w *Glw, rc *glwRctx) {
	q := (*glwQuad)(unsafe.Pointer(w))

	if !glwRendererStarted(&q.r) {
		glwRendererSetupQuad(&q.r)
		glwRendererVtxPos(&q.r, 0, -1, -1, 0)
		glwRendererVtxPos(&q.r, 1, 1, -1, 0)
		glwRendererVtxPos(&q.r, 2, 1, 1, 0)
		glwRendererVtxPos(&q.r, 3, -1, 1, 0)

		glwRendererVtxSt(&q.r, 0, 0, 1)
		glwRendererVtxSt(&q.r, 1, 1, 1)
		glwRendererVtxSt(&q.r, 2, 1, 0)
		glwRendererVtxSt(&q.r, 3, 0, 0)
	}
}

// C: static uint16_t borderobject[] — 16-vertex grid triangulation
var borderObject = []uint16{
	4, 1, 0,
	4, 5, 1,
	5, 2, 1,
	5, 6, 2,
	6, 7, 2,
	2, 7, 3,
	8, 5, 4,
	8, 9, 5,
	10, 7, 6,
	10, 11, 7,
	12, 13, 8,
	8, 13, 9,
	13, 10, 9,
	13, 14, 10,
	14, 11, 10,
	14, 15, 11,
}

// C: glw_border_layout
func glwBorderLayout(w *Glw, rc *glwRctx) {
	q := (*glwQuad)(unsafe.Pointer(w))

	if !glwRendererStarted(&q.r) {
		glwRendererSetup(&q.r, 16, 16, borderObject)
	} else if q.width == rc.rcWidth && q.height == rc.rcHeight &&
		q.relayout == 0 {
		return
	}

	q.width = rc.rcWidth
	q.height = rc.rcHeight
	q.relayout = 0

	var v [4][2]float32

	v[0][0] = -1.0
	v[1][0] = glwMin(-1.0+2.0*float32(q.border[0])/float32(rc.rcWidth), 0.0)
	v[2][0] = glwMax(1.0-2.0*float32(q.border[2])/float32(rc.rcWidth), 0.0)
	v[3][0] = 1.0

	v[0][1] = 1.0
	v[1][1] = glwMax(1.0-2.0*float32(q.border[1])/float32(rc.rcHeight), 0.0)
	v[2][1] = glwMin(-1.0+2.0*float32(q.border[3])/float32(rc.rcHeight), 0.0)
	v[3][1] = -1.0

	i := 0
	for y := range 4 {
		for x := range 4 {
			glwRendererVtxPos(&q.r, i, v[x][0], v[y][1], 0.0)
			i++
		}
	}
}

// C: glw_quad_init
func glwQuadSetup(w *Glw) {
	q := (*glwQuad)(unsafe.Pointer(w))
	q.color.r = 1.0
	q.color.g = 1.0
	q.color.b = 1.0
}

// C: glw_quad_set_float3
func glwQuadSetFloat3(w *Glw, attrib glwAttribute, vector []float32, gs *GlwStyle) int {
	q := (*glwQuad)(unsafe.Pointer(w))
	if attrib == glwAttribRGB {
		v := [3]float32{vector[0], vector[1], vector[2]}
		return glwAttribSetRgb(&q.color, &v)
	}
	return -1
}

// C: glw_quad_set_fs
func glwQuadSetFs(w *Glw, vs *miscpkg.Rstr) {
	q := (*glwQuad)(unsafe.Pointer(w))
	miscpkg.RstrSet(&q.fs, vs)
	q.recompile = 1
}

// C: glw_quad_dtor
func glwQuadDtor(w *Glw) {
	q := (*glwQuad)(unsafe.Pointer(w))
	glwRendererFree(&q.r)
	miscpkg.RstrRelease(q.fs)
	glwDestroyProgram(w.glwRoot, q.gpa.GpaProg)
}

// C: quad_set_int16_4
func quadSetInt16_4(w *Glw, attrib glwAttribute, v []int16, gs *GlwStyle) int {
	q := (*glwQuad)(unsafe.Pointer(w))

	switch attrib {
	case glwAttribBorder:
		if glwAttribSetInt16_4(q.border[:], v) == 0 {
			return 0
		}
		q.relayout = 1
		return 1

	case glwAttribPadding:
		return glwAttribSetInt16_4(q.qPadding[:], v)
	default:
		return -1
	}
}

// C: glw_class_t glw_quad / glw_border / glw_linebox
var glwQuadClass = &glwClass{
	gcName:         "quad",
	gcInstanceSize: int(unsafe.Sizeof(glwQuad{})),
	gcNew: func(parent *Glw) *Glw {
		q := &glwQuad{}
		return &q.w
	},
	gcCtor:       glwQuadSetup,
	gcLayout:     glwQuadLayout,
	gcRender:     glwQuadRender,
	gcSetFloat3:  glwQuadSetFloat3,
	gcDtor:       glwQuadDtor,
	gcSetFs:      glwQuadSetFs,
	gcSetInt16_4: quadSetInt16_4,
}

var glwBorderClass = &glwClass{
	gcName:         "border",
	gcInstanceSize: int(unsafe.Sizeof(glwQuad{})),
	gcNew: func(parent *Glw) *Glw {
		q := &glwQuad{}
		return &q.w
	},
	gcCtor:       glwQuadSetup,
	gcLayout:     glwBorderLayout,
	gcRender:     glwQuadRender,
	gcSetFloat3:  glwQuadSetFloat3,
	gcDtor:       glwQuadDtor,
	gcSetFs:      glwQuadSetFs,
	gcSetInt16_4: quadSetInt16_4,
}

// C: glw_linebox_render
func glwLineboxRender(w *Glw, rc *glwRctx) {
	rc0 := *rc
	glwReposition(&rc0, 1, int(rc.rcHeight), int(rc.rcWidth), 1)
	glwWirebox(w.glwRoot, &rc0)
}

// C: glw_linebox_layout
func glwLineboxLayout(w *Glw, rc *glwRctx) {
}

var glwLineboxClass = &glwClass{
	gcName:         "linebox",
	gcInstanceSize: int(unsafe.Sizeof(Glw{})),
	gcNew: func(parent *Glw) *Glw {
		return &Glw{}
	},
	gcLayout: glwLineboxLayout,
	gcRender: glwLineboxRender,
}

func registerPrimitives() {
	glwRegisterClass(glwQuadClass)
	glwRegisterClass(glwBorderClass)
	glwRegisterClass(glwLineboxClass)
}
