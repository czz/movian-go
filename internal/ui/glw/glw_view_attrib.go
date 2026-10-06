package glw

// C: src/ui/glw/glw_view_attrib.c — canonical 1:1 port.

import (
	"reflect"
	"strings"

	miscpkg "github.com/czz/movian-go/internal/misc"
)

// C: str2val (misc/strtab.h:35-45)
func str2val(str string, tab []struct {
	str string
	val int
}) int {
	for _, e := range tab {
		if strings.EqualFold(str, e.str) {
			return e.val
		}
	}
	return -1
}

// C: static int set_align(glw_view_eval_context_t *ec,
// const token_attrib_t *a, struct token *t) (glw_view_attrib.c:1033-1044)
func setAlign(ec *glwViewEvalContext, a *tokenAttrib, t *Token) int {
	if t.typ != tokenIdentifier {
		return glwViewSeterr(ec.ei, t, "Invalid assignment for attribute %s",
			a.name)
	}
	v := str2val(miscpkg.RstrGet(t.tRstring), aligntab)
	if v < 0 {
		return glwViewSeterr(ec.ei, t, "Invalid assignment for attribute %s",
			a.name)
	}
	glwSetAlign(ec.w, v, nil)
	return 0
}

// C: static struct strtab transitiontab[] (glw_view_attrib.c:1047-1053)
var transitiontab = []struct {
	str string
	val int
}{
	{"blend", int(glwTransBlend)},
	{"flipHorizontal", int(glwTransFlipHorizontal)},
	{"flipVertical", int(glwTransFlipVertical)},
	{"slideHorizontal", int(glwTransSlideHorizontal)},
	{"slideVertical", int(glwTransSlideVertical)},
}

// C: static int set_transition_effect(glw_view_eval_context_t *ec,
// const token_attrib_t *a, struct token *t) (glw_view_attrib.c:1060-1075)
func setTransitionEffect(ec *glwViewEvalContext, a *tokenAttrib, t *Token) int {
	if t.typ != tokenIdentifier {
		return glwViewSeterr(ec.ei, t, "Invalid assignment for attribute %s",
			a.name)
	}
	v := str2val(miscpkg.RstrGet(t.tRstring), transitiontab)
	if v < 0 {
		return glwViewSeterr(ec.ei, t, "Invalid assignment for attribute %s",
			a.name)
	}
	if ec.w.glwClass.gcSetInt != nil {
		ec.w.glwClass.gcSetInt(ec.w, glwAttribTransitionEffect, v, nil)
	}
	glwNeedRefresh(ec.w.glwRoot, 0)
	return 0
}

// C: static int mod_flag(glw_view_eval_context_t *ec,
// const token_attrib_t *a, struct token *t) (glw_view_attrib.c:1081-1107)
func modFlag(ec *glwViewEvalContext, a *tokenAttrib, t *Token) int {
	v := 0
	if t.typ == tokenInt {
		v = t.tInt
	} else if t.typ == tokenFloat {
		if t.tFloat > 0.5 {
			v = 1
		}
	} else if t.typ == tokenVoid {
		v = 0
	} else {
		return glwViewSeterr(ec.ei, t, "Invalid assignment for attribute %s",
			a.name)
	}

	fn := a.fn.(func(w *Glw, set, clr int))
	if v != 0 {
		fn(ec.w, a.attrib, 0)
	} else {
		fn(ec.w, 0, a.attrib)
	}
	return 0
}

// C: static int mod_hidden(glw_view_eval_context_t *ec,
// const token_attrib_t *a, struct token *t) (glw_view_attrib.c:1110-1131)
func modHidden(ec *glwViewEvalContext, a *tokenAttrib, t *Token) int {
	v := 0
	if t.typ == tokenInt {
		v = t.tInt
	} else if t.typ == tokenFloat {
		if t.tFloat > 0.5 {
			v = 1
		}
	} else if t.typ == tokenVoid {
		v = 0
	} else {
		return glwViewSeterr(ec.ei, t, "Invalid assignment for attribute %s",
			a.name)
	}
	glwSetHidden(ec.w, v, nil)
	return 0
}

// C: static void mod_flags2(glw_t *w, int set, int clr)
// (glw_view_attrib.c:1133-1148)
func modFlags2(w *Glw, set, clr int) {
	gc := w.glwClass
	if gc.gcModFlags2Always != nil {
		gc.gcModFlags2Always(w, set, clr, nil)
	}
	glwModFlags2(w, set, clr)
	glwNeedRefresh(w.glwRoot, 0)
}

// C: static void mod_text_flags(glw_t *w, int set, int clr)
// (glw_view_attrib.c:1151-1158)
func modTextFlags(w *Glw, set, clr int) {
	if w.glwClass.gcModTextFlags != nil {
		w.glwClass.gcModTextFlags(w, set, clr, nil)
	}
	glwNeedRefresh(w.glwRoot, 0)
}

// C: static void mod_img_flags(glw_t *w, int set, int clr)
// (glw_view_attrib.c:1163-1170)
func modImgFlags(w *Glw, set, clr int) {
	if w.glwClass.gcModImageFlags != nil {
		w.glwClass.gcModImageFlags(w, set, clr, nil)
	}
	glwNeedRefresh(w.glwRoot, 0)
}

// C: static void mod_video_flags(glw_t *w, int set, int clr)
// (glw_view_attrib.c:1175-1182)
func modVideoFlags(w *Glw, set, clr int) {
	if w.glwClass.gcModVideoFlags != nil {
		w.glwClass.gcModVideoFlags(w, set, clr)
	}
	glwNeedRefresh(w.glwRoot, 0)
}

// C: static rstr_t **build_rstr_vector(struct token *t0)
// (glw_view_attrib.c:1187-1210)
func buildRstrVector(t0 *Token) []*miscpkg.Rstr {
	cnt := 0
	for t := t0.child; t != nil; t = t.next {
		if t.typ == tokenRstring || t.typ == tokenURI {
			cnt++
		}
	}
	rv := make([]*miscpkg.Rstr, 0, cnt)
	for t := t0.child; t != nil; t = t.next {
		if t.typ == tokenRstring || t.typ == tokenURI {
			rv = append(rv, miscpkg.RstrDup(t.tRstring))
		}
	}
	return rv
}

// C: static int set_alt(glw_view_eval_context_t *ec,
// const token_attrib_t *a, struct token *t) (glw_view_attrib.c:1214-1244)
func setAlt(ec *glwViewEvalContext, a *tokenAttrib, t *Token) int {
	w := ec.w
	var r *miscpkg.Rstr

	switch t.typ {
	default:
		if w.glwClass.gcSetAlt != nil {
			w.glwClass.gcSetAlt(w, nil)
		}
		return 0
	case tokenRstring:
		r = t.tRstring
	case tokenURI:
		r = t.tURI
	}

	r = glwResolvePath(r, t.file, w.glwRoot, nil)

	if w.glwClass.gcSetAlt != nil {
		w.glwClass.gcSetAlt(w, r)
	}
	miscpkg.RstrRelease(r)
	return 0
}

// C: static int set_source(glw_view_eval_context_t *ec,
// const token_attrib_t *a, struct token *t) (glw_view_attrib.c:1247-1287)
func setSource(ec *glwViewEvalContext, a *tokenAttrib, t *Token) int {
	w := ec.w
	var r *miscpkg.Rstr

	switch t.typ {
	default:
		if w.glwClass.gcSetSource != nil {
			w.glwClass.gcSetSource(w, nil, 0, nil)
		}
		return 0
	case tokenVector:
		if w.glwClass.gcSetSources != nil {
			w.glwClass.gcSetSources(w, buildRstrVector(t))
		}
		return 0
	case tokenRstring:
		r = t.tRstring
	case tokenURI:
		r = t.tURI
	}

	flags := 0
	r = glwResolvePath(r, t.file, w.glwRoot, &flags)

	if w.glwClass.gcSetSource != nil {
		w.glwClass.gcSetSource(w, r, flags, nil)
	}

	glwNeedRefresh(w.glwRoot, 0)
	miscpkg.RstrRelease(r)
	return 0
}

// C: static int set_args(glw_view_eval_context_t *ec,
// const token_attrib_t *a, struct token *t) (glw_view_attrib.c:1290-1302)
func setArgs(ec *glwViewEvalContext, a *tokenAttrib, t *Token) int {
	if t.typ == tokenPropertyOwner || t.typ == tokenPropertyRef {
		if ec.w.glwClass.gcSetProp != nil {
			ec.w.glwClass.gcSetProp(ec.w, glwAttribArgs, t.tProp)
		}
	}
	return 0
}

// C: static int set_propref(glw_view_eval_context_t *ec,
// const token_attrib_t *a, struct token *t) (glw_view_attrib.c:1305-1329)
func setPropref(ec *glwViewEvalContext, a *tokenAttrib, t *Token) int {
	if ec.w.glwClass.gcSetProp == nil {
		return 0
	}
	if t.typ == tokenVoid {
		ec.w.glwClass.gcSetProp(ec.w, glwAttribute(a.attrib), nil)
		return 0
	}
	if t.typ != tokenPropertyRef {
		return glwViewSeterr(ec.ei, t,
			"Attribute '%s' expects a property ref, got %s",
			a.name, token2name(t))
	}
	p := glwDeps.pm.PropGetProp(t.tProp)
	ec.w.glwClass.gcSetProp(ec.w, glwAttribute(a.attrib), p)
	glwDeps.pm.RefDec(p)
	return 0
}

// C: static int set_style(glw_view_eval_context_t *ec,
// const token_attrib_t *a, struct token *t) (glw_view_attrib.c:1331-1367)
func setStyle(ec *glwViewEvalContext, a *tokenAttrib, t *Token) int {
	var r int
	switch t.typ {
	default:
		r = glwStylesetForWidget(ec.w, "", ec)
	case tokenCstring:
		r = glwStylesetForWidget(ec.w, t.tCstring, ec)
	case tokenRstring, tokenURI:
		r = glwStylesetForWidget(ec.w, miscpkg.RstrGet(t.tRstring), ec)
	case tokenVector:
		r = glwStylesetForWidgetMultiple(ec.w, t.child, ec)
	}
	if r != 0 {
		attrNeedRefresh(ec.w.glwRoot, t, a.name, r)
	}
	return 0
}

// C: static const token_attrib_t attribtab[] (glw_view_attrib.c:1371-1507)
var attribtab = []tokenAttrib{
	{name: "style", set: setStyle},

	{name: "id", set: setRstring, fn: setIdRstr},
	{name: "how", set: setRstring, fn: setHowRstr},
	{name: "description", set: setRstring, fn: setDescriptionRstr},
	{name: "parentUrl", set: setRstring, fn: setParentURLRstr},
	{name: "caption", set: setCaption},
	{name: "font", set: setFont},
	{name: "fragmentShader", set: setFs},
	{name: "source", set: setSource},
	{name: "alt", set: setAlt},

	{name: "hidden", set: modHidden},
	{name: "filterConstraintX", set: modFlag, attrib: glw2ConstraintIgnoreX, fn: modFlags2},
	{name: "filterConstraintY", set: modFlag, attrib: glw2ConstraintIgnoreY, fn: modFlags2},
	{name: "filterConstraintWeight", set: modFlag, attrib: glw2ConstraintIgnoreW, fn: modFlags2},
	{name: "debug", set: modFlag, attrib: glw2Debug, fn: modFlags2},
	{name: "noInitialTransform", set: modFlag, attrib: glw2NoInitialTrans, fn: modFlags2},
	{name: "focusOnClick", set: modFlag, attrib: glw2FocusOnClick, fn: modFlags2},
	{name: "autoRefocusable", set: modFlag, attrib: glw2Autorefocusable, fn: modFlags2},
	{name: "navFocusable", set: modFlag, attrib: glw2NavFocusable, fn: modFlags2},
	{name: "homogenous", set: modFlag, attrib: glw2Homogenous, fn: modFlags2},
	{name: "enabled", set: modFlag, attrib: glw2Enabled, fn: modFlags2},
	{name: "alwaysGrabKnob", set: modFlag, attrib: glw2AlwaysGrabKnob, fn: modFlags2},
	{name: "autohide", set: modFlag, attrib: glw2Autohide, fn: modFlags2},
	{name: "shadow", set: modFlag, attrib: glw2Shadow, fn: modFlags2},
	{name: "autofade", set: modFlag, attrib: glw2Autofade, fn: modFlags2},
	{name: "expediteSubscriptions", set: modFlag, attrib: glw2ExpediteSubscriptions, fn: modFlags2},
	{name: "navWrap", set: modFlag, attrib: glw2NavWrap, fn: modFlags2},
	{name: "autoFocusLimit", set: modFlag, attrib: glw2AutoFocusLimit, fn: modFlags2},
	{name: "cursor", set: modFlag, attrib: glw2Cursor, fn: modFlags2},
	{name: "navPositional", set: modFlag, attrib: glw2PositionalNavigation, fn: modFlags2},
	{name: "clickable", set: modFlag, attrib: glw2Clickable, fn: modFlags2},
	{name: "fhpSpill", set: modFlag, attrib: glw2FhpSpill, fn: modFlags2},
	{name: "selectOnFocus", set: modFlag, attrib: glw2SelectOnFocus, fn: modFlags2},
	{name: "selectOnHover", set: modFlag, attrib: glw2SelectOnHover, fn: modFlags2},

	{name: "fixedSize", set: modFlag, attrib: glwImageFixedSize, fn: modImgFlags},
	{name: "bevelLeft", set: modFlag, attrib: glwImageBevelLeft, fn: modImgFlags},
	{name: "bevelTop", set: modFlag, attrib: glwImageBevelTop, fn: modImgFlags},
	{name: "bevelRight", set: modFlag, attrib: glwImageBevelRight, fn: modImgFlags},
	{name: "bevelBottom", set: modFlag, attrib: glwImageBevelBottom, fn: modImgFlags},
	{name: "aspectConstraint", set: modFlag, attrib: glwImageSetAspect, fn: modImgFlags},
	{name: "additive", set: modFlag, attrib: glwImageAdditive, fn: modImgFlags},
	{name: "borderOnly", set: modFlag, attrib: glwImageBorderOnly, fn: modImgFlags},
	{name: "leftBorder", set: modFlag, attrib: glwImageBorderLeft, fn: modImgFlags},
	{name: "rightBorder", set: modFlag, attrib: glwImageBorderRight, fn: modImgFlags},

	{name: "cornerTopLeft", set: modFlag, attrib: glwImageCornerTopleft, fn: modImgFlags},
	{name: "cornerTopRight", set: modFlag, attrib: glwImageCornerTopright, fn: modImgFlags},
	{name: "cornerBottomLeft", set: modFlag, attrib: glwImageCornerBottomleft, fn: modImgFlags},
	{name: "cornerBottomRight", set: modFlag, attrib: glwImageCornerBottomright, fn: modImgFlags},

	{name: "password", set: modFlag, attrib: gtbPassword, fn: modTextFlags},
	{name: "ellipsize", set: modFlag, attrib: gtbEllipsize, fn: modTextFlags},
	{name: "bold", set: modFlag, attrib: gtbBold, fn: modTextFlags},
	{name: "italic", set: modFlag, attrib: gtbItalic, fn: modTextFlags},
	{name: "outline", set: modFlag, attrib: gtbOutline, fn: modTextFlags},
	{name: "permanentCursor", set: modFlag, attrib: gtbPermanentCursor, fn: modTextFlags},
	{name: "oskPassword", set: modFlag, attrib: gtbOskPassword, fn: modTextFlags},
	{name: "fileRequest", set: modFlag, attrib: gtbFileRequest, fn: modTextFlags},
	{name: "dirRequest", set: modFlag, attrib: gtbDirRequest, fn: modTextFlags},

	{name: "primary", set: modFlag, attrib: glwVideoPrimary, fn: modVideoFlags},
	{name: "noAudio", set: modFlag, attrib: glwVideoNoAudio, fn: modVideoFlags},

	{name: "alpha", set: setFloat, fn: glwSetAlpha},
	{name: "blur", set: setFloat, fn: glwSetBlur},
	{name: "weight", set: setFloat, fn: glwSetWeight},
	{name: "focusable", set: setFloat, fn: glwSetFocusWeight},
	{name: "height", set: setInt, fn: glwSetHeight},
	{name: "width", set: setInt, fn: glwSetWidth},
	{name: "divider", set: setInt, fn: glwSetDivider},
	{name: "zoffset", set: setInt, fn: glwSetZoffset},

	{name: "maxlines", set: setNumber, attrib: int(glwAttribMaxLines)},
	{name: "sizeScale", set: setNumber, attrib: int(glwAttribSizeScale)},
	{name: "size", set: setNumber, attrib: int(glwAttribSize)},
	{name: "maxWidth", set: setNumber, attrib: int(glwAttribMaxWidth)},
	{name: "alphaSelf", set: setNumber, attrib: int(glwAttribAlphaSelf)},
	{name: "bgalpha", set: setNumber, attrib: int(glwAttribBackgroundAlpha)},
	{name: "saturation", set: setNumber, attrib: int(glwAttribSaturation)},
	{name: "time", set: setNumber, attrib: int(glwAttribTime)},
	{name: "transitionTime", set: setNumber, attrib: int(glwAttribTransitionTime)},
	{name: "angle", set: setNumber, attrib: int(glwAttribAngle)},
	{name: "expansion", set: setNumber, attrib: int(glwAttribExpansion)},
	{name: "min", set: setNumber, attrib: int(glwAttribIntMin)},
	{name: "max", set: setNumber, attrib: int(glwAttribIntMax)},
	{name: "step", set: setNumber, attrib: int(glwAttribIntStep)},
	{name: "value", set: setNumber, attrib: int(glwAttribValue)},
	{name: "childAspect", set: setNumber, attrib: int(glwAttribChildAspect)},
	{name: "center", set: setNumber, attrib: int(glwAttribCenter)},
	{name: "audioVolume", set: setNumber, attrib: int(glwAttribAudioVolume)},
	{name: "aspect", set: setNumber, attrib: int(glwAttribAspect)},
	{name: "alphaFallOff", set: setNumber, attrib: int(glwAttribAlphaFallOff)},
	{name: "blurFallOff", set: setNumber, attrib: int(glwAttribBlurFallOff)},
	{name: "fill", set: setNumber, attrib: int(glwAttribFill)},
	{name: "childScale", set: setNumber, attrib: int(glwAttribChildScale)},

	{name: "childTilesX", set: setNumber, attrib: int(glwAttribChildTilesX)},
	{name: "childTilesY", set: setNumber, attrib: int(glwAttribChildTilesY)},

	{name: "alphaEdges", set: setNumber, attrib: int(glwAttribAlphaEdges)},
	{name: "priority", set: setNumber, attrib: int(glwAttribPriority)},
	{name: "spacing", set: setNumber, attrib: int(glwAttribSpacing)},
	{name: "Xspacing", set: setNumber, attrib: int(glwAttribXSpacing)},
	{name: "Yspacing", set: setNumber, attrib: int(glwAttribYSpacing)},
	{name: "cornerRadius", set: setNumber, attrib: int(glwAttribRadius)},

	{name: "color", set: setFloat3, attrib: int(glwAttribRGB)},
	{name: "translation", set: setFloat3, attrib: int(glwAttribTranslation)},
	{name: "scaling", set: setFloat3, attrib: int(glwAttribScaling)},
	{name: "color1", set: setFloat3, attrib: int(glwAttribColor1)},
	{name: "color2", set: setFloat3, attrib: int(glwAttribColor2)},
	{name: "bgcolor", set: setFloat3, attrib: int(glwAttribBackgroundColor)},

	{name: "rotation", set: setFloat4, attrib: int(glwAttribRotation)},
	{name: "plane", set: setFloat4, attrib: int(glwAttribPlane)},

	{name: "padding", set: setInt16_4, attrib: int(glwAttribPadding)},
	{name: "border", set: setInt16_4, attrib: int(glwAttribBorder)},
	{name: "margin", set: setMargin},

	{name: "align", set: setAlign},
	{name: "effect", set: setTransitionEffect},

	{name: "args", set: setArgs},
	{name: "self", set: setPropref, attrib: int(glwAttribPropSelf),
		flags: glwAttribFlagNoSubscription},
	{name: "itemModel", set: setPropref, attrib: int(glwAttribPropItemModel),
		flags: glwAttribFlagNoSubscription},
	{name: "parentModel", set: setPropref, attrib: int(glwAttribPropParentModel),
		flags: glwAttribFlagNoSubscription},
	{name: "tentative", set: setPropref, attrib: int(glwAttribTentativeValue),
		flags: glwAttribFlagNoSubscription},
}

// C: static void unresolved_set_float(glw_t *w, const char *attrib,
// const token_t *t, float val) (glw_view_attrib.c:1510-1534)
func unresolvedSetFloat(w *Glw, attrib string, t *Token, val float32) {
	gc := w.glwClass
	r := -1
	if gc.gcSetFloatUnresolved != nil {
		r = gc.gcSetFloatUnresolved(w, attrib, val, nil)
	}
	if r == -1 && gc.gcSetIntUnresolved != nil {
		r = gc.gcSetIntUnresolved(w, attrib, int(val), nil)
	}
	if r == -1 {
		respondError(w, t, attrib)
		return
	}
	if r != 0 {
		attrNeedRefresh(w.glwRoot, t, attrib, r)
	}
}

// C: static void unresolved_set_int(glw_t *w, const char *attrib,
// const token_t *t, int val) (glw_view_attrib.c:1537-1561)
func unresolvedSetInt(w *Glw, attrib string, t *Token, val int) {
	gc := w.glwClass
	r := -1
	if gc.gcSetIntUnresolved != nil {
		r = gc.gcSetIntUnresolved(w, attrib, val, nil)
	}
	if r == -1 && gc.gcSetFloatUnresolved != nil {
		r = gc.gcSetFloatUnresolved(w, attrib, float32(val), nil)
	}
	if r == -1 {
		respondError(w, t, attrib)
		return
	}
	if r != 0 {
		attrNeedRefresh(w.glwRoot, t, attrib, r)
	}
}

// C: static void unresolved_set_str(glw_t *w, const char *attrib,
// const token_t *t, rstr_t *val) (glw_view_attrib.c:1564-1582)
func unresolvedSetStr(w *Glw, attrib string, t *Token, val *miscpkg.Rstr) {
	gc := w.glwClass
	r := -1
	if gc.gcSetRstrUnresolved != nil {
		r = gc.gcSetRstrUnresolved(w, attrib, val, nil)
	}
	if r == -1 {
		respondError(w, t, attrib)
		return
	}
	if r != 0 {
		attrNeedRefresh(w.glwRoot, t, attrib, r)
	}
}

// C: int glw_view_unresolved_attribute_set(glw_view_eval_context_t *ec,
// const char *attrib, struct token *t) (glw_view_attrib.c:1587-1630)
func glwViewUnresolvedAttributeSet(ec *glwViewEvalContext, attrib string,
	t *Token) int {
	w := ec.w

	switch t.typ {
	case tokenVoid:
	case tokenCstring:
		rstr := miscpkg.RstrAllocStr(t.tCstring)
		unresolvedSetStr(w, attrib, t, rstr)
		miscpkg.RstrRelease(rstr)
	case tokenRstring, tokenURI:
		unresolvedSetStr(w, attrib, t, t.tRstring)
	case tokenInt:
		unresolvedSetInt(w, attrib, t, t.tInt)
	case tokenFloat:
		unresolvedSetFloat(w, attrib, t, t.tFloat)
	case tokenEm:
		ec.dynamicEval |= GLW_VIEW_EVAL_EM
		unresolvedSetFloat(w, attrib, t,
			t.tFloat*float32(w.glwRoot.grCurrentSize))
	default:
		return glwViewSeterr(ec.ei, t,
			"Attribute '%s' expects a different type",
			attrib)
	}
	return 0
}

// C: void glw_view_attrib_resolve(token_t *t) (glw_view_attrib.c:1635-1651)
func glwViewAttribResolve(t *Token) {
	for i := range attribtab {
		if attribtab[i].name == miscpkg.RstrGet(t.tRstring) {
			miscpkg.RstrRelease(t.tRstring)
			t.tAttrib = &attribtab[i]
			t.typ = tokenResolvedAttribute
			return
		}
	}
	t.typ = tokenUnresolvedAttribute
}

// C: static int or_flags2_fn(glw_view_eval_context_t *ec,
// const token_attrib_t *a, struct token *t) (glw_view_attrib.c:1657-1664)
func orFlags2Fn(ec *glwViewEvalContext, a *tokenAttrib, t *Token) int {
	modFlags2(ec.w, t.tSet, t.tClr)
	return 0
}

// C: static int or_img_flags_fn(glw_view_eval_context_t *ec,
// const token_attrib_t *a, struct token *t) (glw_view_attrib.c:1672-1679)
func orImgFlagsFn(ec *glwViewEvalContext, a *tokenAttrib, t *Token) int {
	modImgFlags(ec.w, t.tSet, t.tClr)
	return 0
}

// C: static int or_txt_flags_fn(glw_view_eval_context_t *ec,
// const token_attrib_t *a, struct token *t) (glw_view_attrib.c:1686-1693)
func orTxtFlagsFn(ec *glwViewEvalContext, a *tokenAttrib, t *Token) int {
	modTextFlags(ec.w, t.tSet, t.tClr)
	return 0
}

var orFlags2 = tokenAttrib{name: "or_flags2", set: orFlags2Fn}

var orImgFlags = tokenAttrib{name: "or_img_flags", set: orImgFlagsFn}

var orTxtFlags = tokenAttrib{name: "or_txt_flags", set: orTxtFlagsFn}

// fnIs — C: a->fn == &f function-pointer comparison. A NULL fn never
// matches a real function address (C pointer semantics).
func fnIs(a, b any) bool {
	if a == nil || b == nil {
		return false
	}
	return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
}

// C: static int merge_token(token_t *t, glw_root_t *gr, token_t **p,
// token_t **fp, const token_attrib_t *a) (glw_view_attrib.c:1704-1730)
func mergeToken(t *Token, gr *glwRoot, p **Token, fp **Token,
	a *tokenAttrib) int {
	if *fp == nil {
		*fp = t
		if t.tInt != 0 {
			t.tSet = t.tAttrib.attrib
			t.tClr = 0
		} else {
			t.tSet = 0
			t.tClr = t.tAttrib.attrib
		}
		t.tInt = t.tSet // C: u.ival aliases u.modflags.set
		t.tAttrib = a
		t.typ = tokenModFlags
		return 0
	}
	if t.tInt != 0 {
		(*fp).tSet |= t.tAttrib.attrib
	} else {
		(*fp).tClr |= t.tAttrib.attrib
	}
	(*fp).tInt = (*fp).tSet // C: u.ival aliases u.modflags.set
	*p = t.next
	glwViewTokenFree(gr, t)
	return 1
}

// C: void glw_view_attrib_optimize(token_t *t, glw_root_t *gr)
// (glw_view_attrib.c:1736-1756)
func glwViewAttribOptimize(t *Token, gr *glwRoot) {
	var f2, img, txt *Token
	p := &t

	for {
		t = *p
		if t == nil {
			break
		}
		if t.typ == tokenInt {
			// C compares fn pointers; Go compares via reflect.ValueOf().Pointer()
			if fnIs(t.tAttrib.fn, modFlags2) &&
				mergeToken(t, gr, p, &f2, &orFlags2) != 0 {
				continue
			}
			if fnIs(t.tAttrib.fn, modImgFlags) &&
				mergeToken(t, gr, p, &img, &orImgFlags) != 0 {
				continue
			}
			if fnIs(t.tAttrib.fn, modTextFlags) &&
				mergeToken(t, gr, p, &txt, &orTxtFlags) != 0 {
				continue
			}
		}
		p = &t.next
	}
}
