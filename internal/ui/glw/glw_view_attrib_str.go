package glw

// C: src/ui/glw/glw_view_attrib.c — canonical 1:1 port.

import (
	"strconv"
	"strings"

	facore "github.com/czz/movian-go/internal/fileaccess"
	miscpkg "github.com/czz/movian-go/internal/misc"
	tracepkg "github.com/czz/movian-go/internal/trace"
)

// C: strtod / atoi helpers
func strtod(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}

// C: rstr_t *glw_resolve_path(rstr_t *filename, rstr_t *at, glw_root_t *gr,
// int *flags) (glw_view_attrib.c:36-61)
func glwResolvePath(filename *miscpkg.Rstr, at *miscpkg.Rstr, gr *glwRoot,
	flags *int) *miscpkg.Rstr {
	if filename == nil {
		return nil
	}

	fn := miscpkg.RstrGet(filename)
	if strings.HasPrefix(fn, "skin://") {
		x := fn[len("skin://"):]
		if flags != nil {
			*flags = glwSourceFlagAlwaysLocal
		}
		return miscpkg.RstrAllocStr(facore.FAPathjoin(gr.grSkin, x))
	}

	if flags != nil {
		if strings.HasPrefix(fn, "dataroot://") {
			*flags = glwSourceFlagAlwaysLocal
		}
	}

	return miscpkg.RstrAllocStr(
		facore.FAAbsolutePath(fn, miscpkg.RstrGet(at)))
}

// C: static void respond_error(glw_t *w, const token_t *t, const char *name)
// (glw_view_attrib.c:67-77)
func respondError(w *Glw, t *Token, name string) {
	gc := w.glwClass
	glwDeps.ts.Trace(tracepkg.TRACE_DEBUG, "GLW", "Widget %s (%s:%d) "+
		"assignment at %s:%d does not respond to attribute %s",
		gc.gcName,
		miscpkg.RstrGet(w.glwFile), w.glwLine,
		miscpkg.RstrGet(t.file), t.line, name)
}

// C: static int set_rstring(glw_view_eval_context_t *ec,
// const token_attrib_t *a, struct token *t) (glw_view_attrib.c:84-128)
func setRstring(ec *glwViewEvalContext, a *tokenAttrib, t *Token) int {
	var buf string
	fn := a.fn.(func(w *Glw, str *miscpkg.Rstr))
	w := ec.w

	switch t.typ {
	case tokenVoid:
		buf = ""

	case tokenCstring:
		rstr := miscpkg.RstrAllocStr(t.tCstring)
		fn(w, rstr)
		miscpkg.RstrRelease(rstr)
		return 0

	case tokenRstring, tokenURI:
		fn(w, t.tRstring)
		return 0

	case tokenInt:
		buf = strconv.Itoa(t.tInt)

	case tokenFloat:
		buf = strconv.FormatFloat(float64(t.tFloat), 'f', -1, 32)

	case tokenEm:
		ec.dynamicEval |= GLW_VIEW_EVAL_EM
		buf = strconv.FormatFloat(float64(t.tFloat*
			float32(w.glwRoot.grCurrentSize)), 'f', -1, 32)

	default:
		return glwViewSeterr(ec.ei, t,
			"Attribute '%s' expects a string or scalar, got %s",
			a.name, token2name(t))
	}

	rstr := miscpkg.RstrAllocStr(buf)
	fn(w, rstr)
	miscpkg.RstrRelease(rstr)
	return 0
}

// C: static void set_id_rstr(glw_t *w, rstr_t *rstr) (glw_view_attrib.c:136)
func setIdRstr(w *Glw, rstr *miscpkg.Rstr) {
	miscpkg.RstrSet(&w.glwIdRstr, rstr)
}

// C: static void set_how_rstr(glw_t *w, rstr_t *rstr) (glw_view_attrib.c:143)
func setHowRstr(w *Glw, rstr *miscpkg.Rstr) {
	if w.glwClass.gcSetHow != nil {
		w.glwClass.gcSetHow(w, miscpkg.RstrGet(rstr))
	}
}

// C: static void set_description_rstr(glw_t *w, rstr_t *str)
// (glw_view_attrib.c:150)
func setDescriptionRstr(w *Glw, str *miscpkg.Rstr) {
	if w.glwClass.gcSetDesc != nil {
		w.glwClass.gcSetDesc(w, miscpkg.RstrGet(str))
	}
}

// C: static void set_parent_url_rstr(glw_t *w, rstr_t *rstr)
// (glw_view_attrib.c:157)
func setParentURLRstr(w *Glw, rstr *miscpkg.Rstr) {
	if w.glwClass.gcSetRstr != nil {
		w.glwClass.gcSetRstr(w, glwAttribParentURL, rstr, nil)
	}
}

// C: static int set_caption(glw_view_eval_context_t *ec,
// const token_attrib_t *a, struct token *t) (glw_view_attrib.c:168-221)
func setCaption(ec *glwViewEvalContext, a *tokenAttrib, t *Token) int {
	var str string
	var hasStr bool
	typ := 0
	w := ec.w

	switch t.typ {
	case tokenVoid:
		str, hasStr = "", false

	case tokenCstring:
		str, hasStr = t.tCstring, true

	case tokenRstring:
		typ = t.tRstrType
		fallthrough
	case tokenURI:
		str, hasStr = miscpkg.RstrGet(t.tRstring), true

	case tokenInt:
		str, hasStr = strconv.Itoa(t.tInt), true

	case tokenFloat:
		str, hasStr = strconv.FormatFloat(float64(t.tFloat), 'f', -1, 32), true

	case tokenEm:
		ec.dynamicEval |= GLW_VIEW_EVAL_EM
		str, hasStr = strconv.FormatFloat(float64(t.tFloat*
			float32(w.glwRoot.grCurrentSize)), 'f', -1, 32), true

	default:
		return glwViewSeterr(ec.ei, t,
			"Attribute '%s' expects a string or scalar, got %s",
			a.name, token2name(t))
	}

	if w.glwClass.gcSetCaption != nil {
		if hasStr {
			w.glwClass.gcSetCaption(ec.w, str, typ)
		} else {
			w.glwClass.gcSetCaption(ec.w, "", typ) // C: str = NULL
		}
	}
	return 0
}

// C: static int set_font(glw_view_eval_context_t *ec,
// const token_attrib_t *a, struct token *t) (glw_view_attrib.c:225-243)
func setFont(ec *glwViewEvalContext, a *tokenAttrib, t *Token) int {
	var str *miscpkg.Rstr
	if t.typ == tokenRstring {
		str = t.tRstring
	}
	w := ec.w
	str = glwResolvePath(str, t.file, w.glwRoot, nil)

	if w.glwClass.gcSetRstr != nil {
		w.glwClass.gcSetRstr(w, glwAttribFont, str, nil)
	}
	miscpkg.RstrRelease(str)
	return 0
}

// C: static int set_fs(glw_view_eval_context_t *ec, const token_attrib_t *a,
// struct token *t) (glw_view_attrib.c:250-267)
func setFs(ec *glwViewEvalContext, a *tokenAttrib, t *Token) int {
	var str *miscpkg.Rstr
	if t.typ == tokenRstring {
		str = t.tRstring
	}
	w := ec.w
	str = glwResolvePath(str, t.file, w.glwRoot, nil)

	if w.glwClass.gcSetFs != nil {
		w.glwClass.gcSetFs(ec.w, str)
	}
	miscpkg.RstrRelease(str)
	return 0
}
