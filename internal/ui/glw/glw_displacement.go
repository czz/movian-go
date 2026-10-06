package glw

// Canonical port of src/ui/glw/glw_displacement.c — the `displacement`
// widget class (translate/scale/rotate/padding wrapper).

import "unsafe"

// C: glw_displacement_t
type glwDisplacement struct {
	w           Glw
	gdScale     [3]float32
	gdTranslate [3]float32
	gdRotate    [4]float32
	gdPadding   [4]int16
}

// C: glw_displacement_layout
func glwDisplacementLayout(w *Glw, rc *glwRctx) {
	gd := (*glwDisplacement)(unsafe.Pointer(w))
	var rc0 glwRctx

	c := w.glwChilds.tqhFirst
	if c == nil {
		return
	}

	width := int(rc.rcWidth) - int(gd.gdPadding[0]) - int(gd.gdPadding[2])
	height := int(rc.rcHeight) - int(gd.gdPadding[1]) - int(gd.gdPadding[3])

	rc0 = *rc
	rc0.rcWidth = int16(width)
	rc0.rcHeight = int16(height)

	glwLayout0(c, &rc0)
}

// C: glw_displacement_callback
func glwDisplacementCallback(w *Glw, opaque any, signal glwSignal, extra any) int {
	if signal == glwSignalChildConstraintsChanged {
		c, _ := extra.(*Glw)
		glwCopyConstraints(w, c)
		return 1
	}
	return 0
}

// C: glw_displacement_render
func glwDisplacementRender(w *Glw, rc *glwRctx) {
	gd := (*glwDisplacement)(unsafe.Pointer(w))
	rc0 := *rc

	rc0.rcAlpha = rc.rcAlpha * w.glwAlpha
	if rc0.rcAlpha < glwAlphaEpsilon {
		return
	}

	c := w.glwChilds.tqhFirst
	if c == nil {
		return
	}

	glwTranslatef(&rc0,
		gd.gdTranslate[0],
		gd.gdTranslate[1],
		gd.gdTranslate[2])

	glwScalef(&rc0,
		gd.gdScale[0],
		gd.gdScale[1],
		gd.gdScale[2])

	if gd.gdRotate[0] != 0 {
		glwRotatef(&rc0,
			gd.gdRotate[0],
			gd.gdRotate[1],
			gd.gdRotate[2],
			gd.gdRotate[3])
	}

	glwRepositionf(&rc0,
		float32(gd.gdPadding[0]),
		float32(rc.rcHeight)-float32(gd.gdPadding[1]),
		float32(rc.rcWidth)-float32(gd.gdPadding[2]),
		float32(gd.gdPadding[3]))

	if glwIsFocusableOrClickable(w) {
		glwStoreMatrix(w, &rc0)
	}

	glwRender0(c, &rc0)
}

// C: glw_displacement_ctor
func glwDisplacementCtor(w *Glw) {
	gd := (*glwDisplacement)(unsafe.Pointer(w))
	gd.gdScale[0] = 1.0
	gd.gdScale[1] = 1.0
	gd.gdScale[2] = 1.0
}

// C: set_float3
func displacementSetFloat3(w *Glw, attrib glwAttribute, vector []float32, gs *GlwStyle) int {
	gd := (*glwDisplacement)(unsafe.Pointer(w))
	v := [3]float32{vector[0], vector[1], vector[2]}

	switch attrib {
	case glwAttribTranslation:
		return glwAttribSetFloat3(&gd.gdTranslate, &v)
	case glwAttribScaling:
		return glwAttribSetFloat3(&gd.gdScale, &v)
	default:
		return -1
	}
}

// C: displacement_set_int16_4
func displacementSetInt16_4(w *Glw, attrib glwAttribute, v []int16, gs *GlwStyle) int {
	gd := (*glwDisplacement)(unsafe.Pointer(w))

	if attrib == glwAttribPadding {
		return glwAttribSetInt16_4(gd.gdPadding[:], v)
	}
	return -1
}

// C: set_float4
func displacementSetFloat4(w *Glw, attrib glwAttribute, vector []float32) int {
	gd := (*glwDisplacement)(unsafe.Pointer(w))
	v := [4]float32{vector[0], vector[1], vector[2], vector[3]}

	if attrib == glwAttribRotation {
		return glwAttribSetFloat4(&gd.gdRotate, &v)
	}
	return -1
}

// C: glw_class_t glw_displacement
var glwDisplacementClass = &glwClass{
	gcName:         "displacement",
	gcInstanceSize: int(unsafe.Sizeof(glwDisplacement{})),
	gcNew: func(parent *Glw) *Glw {
		gd := &glwDisplacement{}
		return &gd.w
	},
	gcCtor:          glwDisplacementCtor,
	gcLayout:        glwDisplacementLayout,
	gcRender:        glwDisplacementRender,
	gcSignalHandler: glwDisplacementCallback,
	gcSetFloat3:     displacementSetFloat3,
	gcSetFloat4:     displacementSetFloat4,
	gcSetInt16_4:    displacementSetInt16_4,
}

func registerDisplacement() {
	glwRegisterClass(glwDisplacementClass)
}
