package glw

// C: src/ui/glw/glw_view_attrib.c — canonical 1:1 port.

import (
	miscpkg "github.com/czz/movian-go/internal/misc"
)

// C: static int set_float(glw_view_eval_context_t *ec,
// const token_attrib_t *a, struct token *t) (glw_view_attrib.c:276-321)
func setFloat(ec *glwViewEvalContext, a *tokenAttrib, t *Token) int {
	var v float32
	w := ec.w

	switch t.typ {
	case tokenCstring:
		v = float32(strtod(t.tCstring))
	case tokenRstring, tokenURI:
		v = float32(strtod(miscpkg.RstrGet(t.tRstring)))
	case tokenFloat:
		v = t.tFloat
	case tokenEm:
		ec.dynamicEval |= GLW_VIEW_EVAL_EM
		v = t.tFloat * float32(w.glwRoot.grCurrentSize)
	case tokenInt:
		v = float32(t.tInt)
	case tokenVoid:
		v = 0.0
	default:
		return glwViewSeterr(ec.ei, t,
			"Attribute '%s' expects a scalar, got %s",
			a.name, token2name(t))
	}

	fn := a.fn.(func(w *Glw, v float32, origin *GlwStyle))
	fn(w, v, nil)
	return 0
}

// C: void glw_set_weight(glw_t *w, float v, glw_style_t *origin)
// (glw_view_attrib.c:324-334)
func glwSetWeight(w *Glw, v float32, origin *GlwStyle) {
	if w.glwClass.gcSetWeight != nil {
		w.glwClass.gcSetWeight(w, v, origin)
		return
	}
	glwConfConstraints(w, 0, 0, v, glwConstraintConfW)
	glwNeedRefresh(w.glwRoot, 0)
}

// C: void glw_set_alpha(glw_t *w, float v, glw_style_t *origin)
// (glw_view_attrib.c:340-353)
func glwSetAlpha(w *Glw, v float32, origin *GlwStyle) {
	if w.glwClass.gcSetAlpha != nil {
		w.glwClass.gcSetAlpha(w, v, origin)
		return
	}
	if w.glwAlpha == v {
		return
	}
	w.glwAlpha = v
	glwNeedRefresh(w.glwRoot, 0)
}

// C: void glw_set_blur(glw_t *w, float v, glw_style_t *origin)
// (glw_view_attrib.c:359-375)
func glwSetBlur(w *Glw, v float32, origin *GlwStyle) {
	if w.glwClass.gcSetBlur != nil {
		w.glwClass.gcSetBlur(w, v, origin)
		return
	}
	v = glwClamp(1-v, 0, 1)
	if w.glwSharpness == v {
		return
	}
	w.glwSharpness = v
	glwNeedRefresh(w.glwRoot, 0)
}

// C: static void attr_need_refresh(glw_root_t *gr, const token_t *t,
// const char *attribname, int how) (glw_view_attrib.c:379-403)
func attrNeedRefresh(gr *glwRoot, t *Token, attribname string, how int) {
	flags := glwRefreshFlagLayout
	if how != glwRefreshLayoutOnly {
		flags |= glwRefreshFlagRender
	}
	if gr.grNeedRefresh&flags == flags {
		return
	}
	gr.grNeedRefresh |= flags
}

// C: static void set_number_int(glw_t *w, const token_attrib_t *a,
// const token_t *t, int v) (glw_view_attrib.c:407-427)
func setNumberInt(w *Glw, a *tokenAttrib, t *Token, v int) {
	gc := w.glwClass
	r := -1
	if gc.gcSetInt != nil {
		r = gc.gcSetInt(w, glwAttribute(a.attrib), v, nil)
	}
	if r == -1 && gc.gcSetFloat != nil {
		r = gc.gcSetFloat(w, glwAttribute(a.attrib), float32(v), nil)
	}
	if r == -1 {
		respondError(w, t, a.name)
		return
	}
	if r != 0 {
		attrNeedRefresh(w.glwRoot, t, a.name, r)
	}
}

// C: static void set_number_float(glw_t *w, const token_attrib_t *a,
// const token_t *t, float v) (glw_view_attrib.c:431-451)
func setNumberFloat(w *Glw, a *tokenAttrib, t *Token, v float32) {
	gc := w.glwClass
	r := -1
	if gc.gcSetFloat != nil {
		r = gc.gcSetFloat(w, glwAttribute(a.attrib), v, nil)
	}
	if r == -1 && gc.gcSetInt != nil {
		r = gc.gcSetInt(w, glwAttribute(a.attrib), int(v), nil)
	}
	if r == -1 {
		respondError(w, t, a.name)
		return
	}
	if r != 0 {
		attrNeedRefresh(w.glwRoot, t, a.name, r)
	}
}

// C: static void set_number_em(glw_t *w, const token_attrib_t *a,
// const token_t *t, glw_view_eval_context_t *ec)
// (glw_view_attrib.c:455-487)
func setNumberEm(w *Glw, a *tokenAttrib, t *Token, ec *glwViewEvalContext) {
	gc := w.glwClass
	v := t.tFloat
	gr := w.glwRoot

	r := -1
	if gc.gcSetEm != nil {
		r = gc.gcSetEm(w, glwAttribute(a.attrib), v)
	}
	if r == -1 {
		v *= float32(gr.grCurrentSize)
		ec.dynamicEval |= GLW_VIEW_EVAL_EM
		if gc.gcSetFloat != nil {
			r = gc.gcSetFloat(w, glwAttribute(a.attrib), v, nil)
		}
		if r == -1 && gc.gcSetInt != nil {
			r = gc.gcSetInt(w, glwAttribute(a.attrib), int(v), nil)
		}
	}
	if r == -1 {
		respondError(w, t, a.name)
		return
	}
	if r != 0 {
		attrNeedRefresh(gr, t, a.name, r)
	}
}

// C: static int set_number(glw_view_eval_context_t *ec,
// const token_attrib_t *a, struct token *t) (glw_view_attrib.c:491-532)
func setNumber(ec *glwViewEvalContext, a *tokenAttrib, t *Token) int {
	w := ec.w

	switch t.typ {
	case tokenCstring:
		setNumberInt(w, a, t, miscpkg.Atoi(t.tCstring))
	case tokenRstring, tokenURI:
		setNumberInt(w, a, t, miscpkg.Atoi(miscpkg.RstrGet(t.tRstring)))
	case tokenFloat:
		setNumberFloat(w, a, t, t.tFloat)
	case tokenEm:
		setNumberEm(w, a, t, ec)
	case tokenInt:
		setNumberInt(w, a, t, t.tInt)
	case tokenVoid:
		setNumberInt(w, a, t, 0)
	default:
		return glwViewSeterr(ec.ei, t,
			"Attribute '%s' expects a scalar, got %s",
			a.name, token2name(t))
	}
	return 0
}

// C: static int set_int(glw_view_eval_context_t *ec,
// const token_attrib_t *a, struct token *t) (glw_view_attrib.c:541-586)
func setInt(ec *glwViewEvalContext, a *tokenAttrib, t *Token) int {
	var v int
	w := ec.w

	switch t.typ {
	case tokenCstring:
		v = miscpkg.Atoi(t.tCstring)
	case tokenRstring, tokenURI:
		v = miscpkg.Atoi(miscpkg.RstrGet(t.tRstring))
	case tokenFloat:
		v = int(t.tFloat)
	case tokenEm:
		ec.dynamicEval |= GLW_VIEW_EVAL_EM
		v = int(t.tFloat * float32(w.glwRoot.grCurrentSize))
	case tokenInt:
		v = t.tInt
	case tokenVoid:
		v = 0
	default:
		return glwViewSeterr(ec.ei, t,
			"Attribute '%s' expects a scalar, got %s",
			a.name, token2name(t))
	}

	fn := a.fn.(func(w *Glw, v int, origin *GlwStyle))
	fn(w, v, nil)
	return 0
}

// C: void glw_set_width(glw_t *w, int v, glw_style_t *origin)
// (glw_view_attrib.c:589-599)
func glwSetWidth(w *Glw, v int, origin *GlwStyle) {
	if w.glwClass.gcSetWidth != nil {
		w.glwClass.gcSetWidth(w, v, origin)
		return
	}
	glwConfConstraints(w, v, 0, 0, glwConstraintConfX)
	glwNeedRefresh(w.glwRoot, 0)
}

// C: void glw_set_height(glw_t *w, int v, glw_style_t *origin)
// (glw_view_attrib.c:605-615)
func glwSetHeight(w *Glw, v int, origin *GlwStyle) {
	if w.glwClass.gcSetHeight != nil {
		w.glwClass.gcSetHeight(w, v, origin)
		return
	}
	glwConfConstraints(w, 0, v, 0, glwConstraintConfY)
	glwNeedRefresh(w.glwRoot, 0)
}

// C: void glw_set_align(glw_t *w, int v, glw_style_t *origin)
// (glw_view_attrib.c:621-633)
func glwSetAlign(w *Glw, v int, origin *GlwStyle) {
	if w.glwClass.gcSetAlign != nil {
		w.glwClass.gcSetAlign(w, v, origin)
		return
	}
	if w.glwAlignment != uint8(v) {
		w.glwAlignment = uint8(v)
		glwNeedRefresh(w.glwRoot, 0)
	}
}

// C: void glw_set_hidden(glw_t *w, int v, glw_style_t *origin)
// (glw_view_attrib.c:639-661)
func glwSetHidden(w *Glw, v int, origin *GlwStyle) {
	if w.glwClass.gcSetHidden != nil {
		w.glwClass.gcSetHidden(w, v, origin)
		return
	}
	if v != 0 {
		if w.glwFlags&glwHidden != 0 {
			return
		}
		glwHide(w)
	} else {
		if w.glwFlags&glwHidden == 0 {
			return
		}
		glwUnhide(w)
	}
	glwNeedRefresh(w.glwRoot, 0)
}

// C: void glw_set_margin(glw_t *w, const int16_t *v, glw_style_t *origin)
// (glw_view_attrib.c:665-699)
func glwSetMargin(w *Glw, v []int16, origin *GlwStyle) {
	if w.glwClass.gcSetMargin != nil {
		w.glwClass.gcSetMargin(w, v, origin)
		return
	}

	ch := 0
	if v[0] != w.glwMargin[0] || v[2] != w.glwMargin[2] {
		w.glwMargin[0] = v[0]
		w.glwMargin[2] = v[2]
		if glwFilterConstraints(w)&glwConstraintX != 0 {
			ch = 1
		}
	}
	if v[1] != w.glwMargin[1] || v[3] != w.glwMargin[3] {
		w.glwMargin[1] = v[1]
		w.glwMargin[3] = v[3]
		if glwFilterConstraints(w)&(glwConstraintY|glwConstraintW) != 0 {
			ch = 1
		}
	}
	w.glwFlags |= glwHaveMargins

	if ch != 0 {
		glwSignal0(w.glwParent, glwSignalChildConstraintsChanged, w)
		glwNeedRefresh(w.glwRoot, 0)
	}
}

// C: void glw_set_divider(glw_t *w, int v) (glw_view_attrib.c:702-707)
func glwSetDivider(w *Glw, v int, origin *GlwStyle) {
	glwConfConstraints(w, 0, 0, 0, glwConstraintConfD)
	glwNeedRefresh(w.glwRoot, 0)
}

// C: static void glw_set_zoffset(glw_t *w, int v) (glw_view_attrib.c:713-725)
func glwSetZoffset(w *Glw, v int, origin *GlwStyle) {
	w.glwZoffset = int16(v)
	glwNeedRefresh(w.glwRoot, 0)
}

// C: static int set_float3(glw_view_eval_context_t *ec,
// const token_attrib_t *a, struct token *t) (glw_view_attrib.c:730-805)
func setFloat3(ec *glwViewEvalContext, a *tokenAttrib, t *Token) int {
	var vec3 []float32
	var v [3]float32
	w := ec.w

	switch t.typ {
	case tokenVectorFloat:
		switch t.tElements {
		case 3:
			vec3 = t.tFloatVector[:3]
		default:
			return glwViewSeterr(ec.ei, t,
				"Attribute '%s': invalid vector size %d",
				a.name, t.tElements)
		}
	case tokenFloat:
		v[0], v[1], v[2] = t.tFloat, t.tFloat, t.tFloat
		vec3 = v[:]
	case tokenEm:
		ec.dynamicEval |= GLW_VIEW_EVAL_EM
		v[0], v[1], v[2] = t.tFloat*float32(w.glwRoot.grCurrentSize),
			t.tFloat*float32(w.glwRoot.grCurrentSize),
			t.tFloat*float32(w.glwRoot.grCurrentSize)
		vec3 = v[:]
	case tokenInt:
		v[0], v[1], v[2] = float32(t.tInt), float32(t.tInt), float32(t.tInt)
		vec3 = v[:]
	case tokenVoid:
		v[0], v[1], v[2] = 0, 0, 0
		vec3 = v[:]
	case tokenRstring:
		s := miscpkg.RstrGet(t.tRstring)
		if len(s) > 0 && s[0] == '#' {
			var fv [3]float32
			miscpkg.RgbstrToFloatvec(s[1:], &fv)
			vec3 = fv[:]
			break
		}
		fallthrough
	default:
		return glwViewSeterr(ec.ei, t,
			"Attribute '%s' expects a vec3, got %s",
			a.name, token2name(t))
	}

	gc := w.glwClass
	r := -1
	if gc.gcSetFloat3 != nil {
		r = gc.gcSetFloat3(w, glwAttribute(a.attrib), vec3, nil)
	}
	if r == -1 {
		respondError(w, t, a.name)
		return 0
	}
	if r != 0 {
		attrNeedRefresh(w.glwRoot, t, a.name, r)
	}
	return 0
}

// C: static int set_float4(glw_view_eval_context_t *ec,
// const token_attrib_t *a, struct token *t) (glw_view_attrib.c:807-875)
func setFloat4(ec *glwViewEvalContext, a *tokenAttrib, t *Token) int {
	var vec4 []float32
	var v [4]float32
	w := ec.w

	switch t.typ {
	case tokenVectorFloat:
		switch t.tElements {
		case 4:
			vec4 = t.tFloatVector[:4]
		case 2:
			v[0] = t.tFloatVector[0]
			v[1] = t.tFloatVector[1]
			v[2] = t.tFloatVector[0]
			v[3] = t.tFloatVector[1]
			vec4 = v[:]
		default:
			return glwViewSeterr(ec.ei, t,
				"Attribute '%s': invalid vector size %d",
				a.name, t.tElements)
		}
	case tokenFloat:
		v[0], v[1], v[2], v[3] = t.tFloat, t.tFloat, t.tFloat, t.tFloat
		vec4 = v[:]
	case tokenEm:
		ec.dynamicEval |= GLW_VIEW_EVAL_EM
		f := t.tFloat * float32(w.glwRoot.grCurrentSize)
		v[0], v[1], v[2], v[3] = f, f, f, f
		vec4 = v[:]
	case tokenInt:
		f := float32(t.tInt)
		v[0], v[1], v[2], v[3] = f, f, f, f
		vec4 = v[:]
	default:
		return glwViewSeterr(ec.ei, t,
			"Attribute '%s' expects a vec4, got %s",
			a.name, token2name(t))
	}

	gc := w.glwClass
	r := -1
	if gc.gcSetFloat4 != nil {
		r = gc.gcSetFloat4(w, glwAttribute(a.attrib), vec4)
	}
	if r == -1 {
		respondError(w, t, a.name)
		return 0
	}
	if r != 0 {
		attrNeedRefresh(w.glwRoot, t, a.name, r)
	}
	return 0
}

// C: static int set_int16_4(glw_view_eval_context_t *ec,
// const token_attrib_t *a, struct token *t) (glw_view_attrib.c:877-951)
func setInt16_4(ec *glwViewEvalContext, a *tokenAttrib, t *Token) int {
	var v [4]int16
	w := ec.w

	switch t.typ {
	case tokenVectorFloat:
		switch t.tElements {
		case 4:
			for i := range 4 {
				v[i] = int16(t.tFloatVector[i])
			}
		case 2:
			v[0] = int16(t.tFloatVector[0])
			v[1] = int16(t.tFloatVector[1])
			v[2] = int16(t.tFloatVector[0])
			v[3] = int16(t.tFloatVector[1])
		default:
			return glwViewSeterr(ec.ei, t,
				"Attribute '%s': invalid vector size %d",
				a.name, t.tElements)
		}
	case tokenEm:
		ec.dynamicEval |= GLW_VIEW_EVAL_EM
		f := int16(t.tFloat * float32(w.glwRoot.grCurrentSize))
		v[0], v[1], v[2], v[3] = f, f, f, f
	case tokenFloat:
		f := int16(t.tFloat)
		v[0], v[1], v[2], v[3] = f, f, f, f
	case tokenInt:
		f := int16(t.tInt)
		v[0], v[1], v[2], v[3] = f, f, f, f
	case tokenVoid:
		v[0], v[1], v[2], v[3] = 0, 0, 0, 0
	default:
		return glwViewSeterr(ec.ei, t,
			"Attribute '%s' expects a vec4, got %s",
			a.name, token2name(t))
	}

	gc := w.glwClass
	r := -1
	if gc.gcSetInt16_4 != nil {
		r = gc.gcSetInt16_4(w, glwAttribute(a.attrib), v[:], nil)
	}
	if r == -1 {
		respondError(w, t, a.name)
		return 0
	}
	if r != 0 {
		attrNeedRefresh(w.glwRoot, t, a.name, r)
	}
	return 0
}

// C: static int set_margin(glw_view_eval_context_t *ec,
// const token_attrib_t *a, struct token *t) (glw_view_attrib.c:953-1030)
func setMargin(ec *glwViewEvalContext, a *tokenAttrib, t *Token) int {
	var v [4]int16
	w := ec.w

	switch t.typ {
	case tokenVectorFloat:
		switch t.tElements {
		case 4:
			for i := range 4 {
				v[i] = int16(t.tFloatVector[i])
			}
		case 2:
			v[0] = int16(t.tFloatVector[0])
			v[1] = int16(t.tFloatVector[1])
			v[2] = int16(t.tFloatVector[0])
			v[3] = int16(t.tFloatVector[1])
		default:
			return glwViewSeterr(ec.ei, t,
				"Attribute '%s': invalid vector size %d",
				a.name, t.tElements)
		}
	case tokenEm:
		ec.dynamicEval |= GLW_VIEW_EVAL_EM
		f := int16(t.tFloat * float32(w.glwRoot.grCurrentSize))
		v[0], v[1], v[2], v[3] = f, f, f, f
	case tokenFloat:
		f := int16(t.tFloat)
		v[0], v[1], v[2], v[3] = f, f, f, f
	case tokenInt:
		f := int16(t.tInt)
		v[0], v[1], v[2], v[3] = f, f, f, f
	case tokenVoid:
		v[0], v[1], v[2], v[3] = 0, 0, 0, 0
	default:
		return glwViewSeterr(ec.ei, t,
			"Attribute '%s' expects a vec4, got %s",
			a.name, token2name(t))
	}

	glwSetMargin(w, v[:], nil)
	return 0
}

// C: static struct strtab aligntab[] (glw_view_attrib.c:1015-1027)
var aligntab = []struct {
	str string
	val int
}{
	{"center", layoutAlignCenter},
	{"left", layoutAlignLeft},
	{"right", layoutAlignRight},
	{"top", layoutAlignTop},
	{"bottom", layoutAlignBottom},
	{"topLeft", layoutAlignTopLeft},
	{"topRight", layoutAlignTopRight},
	{"bottomLeft", layoutAlignBottomLeft},
	{"bottomRight", layoutAlignBottomRight},
	{"justified", layoutAlignJustified},
}
