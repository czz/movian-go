package glw

// Canonical port of src/ui/glw/glw_expander.c — the `expander_x` and
// `expander_y` widget classes (single child shown at `expansion` fraction).

import "unsafe"

// C: glw_expander_t
type glwExpander struct {
	w Glw

	expansion    float32
	last         int
	alwaysLayout int
}

// C: update_constraints
func expanderUpdateConstraints(exp *glwExpander) {
	c := exp.w.glwChilds.tqhFirst
	var e, o int

	f := 0
	if c != nil {
		f = glwFilterConstraints(c)
	}

	if exp.w.glwClass == glwExpanderXClass {
		e = int(exp.expansion * float32(glwReqWidthN(c)))
		o = glwReqHeightN(c)
		f &= glwConstraintY
	} else {
		e = int(exp.expansion * float32(glwReqHeightN(c)))
		o = glwReqWidthN(c)
		f &= glwConstraintX
	}

	if e == 0 {
		glwFocusClosePath(&exp.w)
	} else if exp.w.glwFlags&glwFocusBlocked != 0 {
		glwFocusOpenPath(&exp.w)
	}

	if exp.w.glwClass == glwExpanderXClass {
		glwSetConstraints(&exp.w, e, o, 0, glwConstraintX|f)
	} else {
		glwSetConstraints(&exp.w, o, e, 0, glwConstraintY|f)
	}
}

// glwReqWidthN — C: c != NULL ? glw_req_width(c) : 0
func glwReqWidthN(c *Glw) int {
	if c == nil {
		return 0
	}
	return glwReqWidth(c)
}

func glwReqHeightN(c *Glw) int {
	if c == nil {
		return 0
	}
	return glwReqHeight(c)
}

// C: glw_expander_layout
func glwExpanderLayout(w *Glw, rc *glwRctx) {
	exp := (*glwExpander)(unsafe.Pointer(w))
	var rc0 glwRctx

	if exp.expansion < glwAlphaEpsilon && exp.alwaysLayout == 0 {
		return
	}

	c := w.glwChilds.tqhFirst
	if c == nil {
		return
	}
	rc0 = *rc

	if exp.w.glwClass == glwExpanderXClass {
		rc0.rcWidth = int16(glwReqWidth(c))
		if rc0.rcWidth == 0 {
			rc0.rcWidth = int16(exp.last)
		} else {
			exp.last = int(rc0.rcWidth)
		}
	} else {
		rc0.rcHeight = int16(glwReqHeight(c))
		if rc0.rcHeight == 0 {
			rc0.rcHeight = int16(exp.last)
		} else {
			exp.last = int(rc0.rcHeight)
		}
	}

	glwLayout0(c, &rc0)
}

// C: glw_expander_callback
func glwExpanderCallback(w *Glw, opaque any, signal glwSignal, extra any) int {
	exp := (*glwExpander)(unsafe.Pointer(w))

	switch signal {
	case glwSignalChildConstraintsChanged:
		expanderUpdateConstraints(exp)
		return 1
	}
	return 0
}

// C: glw_expander_render
func glwExpanderRender(w *Glw, rc *glwRctx) {
	exp := (*glwExpander)(unsafe.Pointer(w))
	var rc0 glwRctx
	c := w.glwChilds.tqhFirst
	if c == nil {
		return
	}

	if exp.expansion < glwAlphaEpsilon {
		return
	}

	rc0 = *rc
	rc0.rcAlpha *= w.glwAlpha

	// Trick childs into rendering themselfs as if the widget is
	// fully expanded
	if w.glwClass == glwExpanderXClass {
		rc0.rcWidth = int16(glwReqWidth(c))
	} else {
		rc0.rcHeight = int16(glwReqHeight(c))
	}
	glwRender0(c, &rc0)
}

// C: glw_expander_ctor
func glwExpanderCtor(w *Glw) {
	exp := (*glwExpander)(unsafe.Pointer(w))
	expanderUpdateConstraints(exp)
}

// C: glw_expander_set_float
func glwExpanderSetFloat(w *Glw, attrib glwAttribute, value float32, gs *GlwStyle) int {
	exp := (*glwExpander)(unsafe.Pointer(w))

	switch attrib {
	case glwAttribExpansion:
		if exp.expansion == value {
			return 0
		}
		exp.expansion = value
		expanderUpdateConstraints(exp)
	default:
		return -1
	}
	return 1
}

// C: glw_expander_set_int_unresolved
func glwExpanderSetIntUnresolved(w *Glw, a string, value int, gs *GlwStyle) int {
	exp := (*glwExpander)(unsafe.Pointer(w))

	if a == "alwaysLayout" {
		if exp.alwaysLayout == value {
			return 0
		}
		exp.alwaysLayout = value
		return glwSetLayoutOnly
	}
	return glwSetNotResponding
}

// C: glw_class_t glw_expander_x / glw_expander_y — populated in init()
// because the funcs reference the class vars (init cycle otherwise).
var glwExpanderXClass = &glwClass{gcName: "expander_x"}
var glwExpanderYClass = &glwClass{gcName: "expander_y"}

func registerExpander() {
	newExpander := func(parent *Glw) *Glw {
		e := &glwExpander{}
		return &e.w
	}
	*glwExpanderXClass = glwClass{
		gcName:             "expander_x",
		gcInstanceSize:     int(unsafe.Sizeof(glwExpander{})),
		gcNew:              newExpander,
		gcLayout:           glwExpanderLayout,
		gcRender:           glwExpanderRender,
		gcSetFloat:         glwExpanderSetFloat,
		gcCtor:             glwExpanderCtor,
		gcSignalHandler:    glwExpanderCallback,
		gcSetIntUnresolved: glwExpanderSetIntUnresolved,
	}
	*glwExpanderYClass = glwClass{
		gcName:             "expander_y",
		gcInstanceSize:     int(unsafe.Sizeof(glwExpander{})),
		gcNew:              newExpander,
		gcLayout:           glwExpanderLayout,
		gcRender:           glwExpanderRender,
		gcSetFloat:         glwExpanderSetFloat,
		gcCtor:             glwExpanderCtor,
		gcSignalHandler:    glwExpanderCallback,
		gcSetIntUnresolved: glwExpanderSetIntUnresolved,
	}
	glwRegisterClass(glwExpanderXClass)
	glwRegisterClass(glwExpanderYClass)
}
