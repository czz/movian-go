package glw

// Canonical port of src/ui/glw/glw_cube.c — the `cube` widget class
// (renders the child on all 6 faces of a rotating cube).

import "unsafe"

// C: glw_cube_t
type glwCube struct {
	w     Glw
	theta float32
}

// C: glw_cube_layout
func glwCubeLayout(w *Glw, rc *glwRctx) {
	gc := (*glwCube)(unsafe.Pointer(w))

	gc.theta -= 1
	c := gc.w.glwChilds.tqhFirst
	if c != nil {
		glwLayout0(c, rc)
	}
}

// C: glw_cube_render
func glwCubeRender(w *Glw, rc *glwRctx) {
	gc := (*glwCube)(unsafe.Pointer(w))
	var rc0, rc1 glwRctx

	c := w.glwChilds.tqhFirst
	if c == nil {
		return
	}

	rc0 = *rc

	glwScaleToAspect(&rc0, 1.0)

	glwTranslatef(&rc0, 0, 0, -2.0)

	glwRotatef(&rc0, gc.theta, 1.1, 0.5, 1.0)

	// 6 faces: 4 around Y, 2 around X
	for _, face := range [6][4]float32{
		{0, 0, 1, 0},
		{90, 0, 1, 0},
		{180, 0, 1, 0},
		{270, 0, 1, 0},
		{90, 1, 0, 0},
		{270, 1, 0, 0},
	} {
		rc1 = rc0
		glwRotatef(&rc1, face[0], face[1], face[2], face[3])
		glwTranslatef(&rc1, 0, 0, 1.0)
		glwRender0(c, &rc1)
	}
}

// C: glw_class_t glw_cube
var glwCubeClass = &glwClass{
	gcName:         "cube",
	gcInstanceSize: int(unsafe.Sizeof(glwCube{})),
	gcNew: func(parent *Glw) *Glw {
		gc := &glwCube{}
		return &gc.w
	},
	gcLayout: glwCubeLayout,
	gcRender: glwCubeRender,
}

func registerCube() {
	glwRegisterClass(glwCubeClass)
}
