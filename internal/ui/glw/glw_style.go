package glw

// C: src/ui/glw/glw_style.c — type/constant definitions (canonical 1:1).
// Function bodies of glw_style.c are ported separately.

import (
	"fmt"
	"unsafe"

	miscpkg "github.com/czz/movian-go/internal/misc"
)

// C: LIST_HEAD(glw_style_attribute_list, glw_style_attribute);
type glwStyleAttributeList struct {
	lhFirst *glwStyleAttribute
}

// C: typedef enum gsa_type (glw_style.c:30-37)
type gsaType int

const (
	gsaNone    gsaType = iota // GSA_NONE
	gsaInt                    // GSA_INT
	gsaFloat                  // GSA_FLOAT
	gsaRstr                   // GSA_RSTR
	gsaFvec3                  // GSA_FVEC3
	gsaIvec164                // GSA_IVEC16_4
)

// C: typedef struct glw_style_attribute (glw_style.c:40-64)
//
// The C unions are flattened into named fields. gsa_attribute and
// gsa_unresolved_attribute share storage in C (first union); the value
// fields i32/f/rstr/fvec/i16vec/flags share storage in C (second union).
type glwStyleAttribute struct {
	gsaLinkNext *glwStyleAttribute // C: LIST_ENTRY gsa_link
	gsaLinkPrev **glwStyleAttribute

	// C: union { glw_attribute_t gsa_attribute; char *gsa_unresolved_attribute; }
	gsaAttribute           int    // C: gsa_attribute (glw_attribute_t enum)
	gsaUnresolvedAttribute string // C: gsa_unresolved_attribute

	gsaType       gsaType // C: gsa_type
	gsaUnresolved int8    // C: gsa_unresolved
	gsaLocal      int8    // C: gsa_local (not inherited)

	// C: union { int i32; float f; rstr_t *rstr; float fvec[4];
	//           int16_t i16vec[4]; struct { int set; int clr; } flags; }
	gsaI32      int32         // C: i32
	gsaF        float32       // C: f
	gsaRstr     *miscpkg.Rstr // C: rstr
	gsaFvec     [4]float32    // C: fvec
	gsaI16vec   [4]int16      // C: i16vec
	gsaFlagsSet int           // C: flags.set
	gsaFlagsClr int           // C: flags.clr
}

// C: struct glw_style (glw_style.c:72-118)
type GlwStyle struct {
	w Glw // C: glw_t w (embedded first member)

	gsFile *miscpkg.Rstr // C: gs_file
	gsLine int           // C: gs_line

	gsID int // C: gs_id

	gsAncestor   *GlwStyle             // C: gs_ancestor
	gsRpns       *Token                // C: gs_rpns
	gsName       *miscpkg.Rstr         // C: gs_name
	gsBindings   glwStyleBindingList   // C: gs_bindings
	gsAttributes glwStyleAttributeList // C: gs_attributes

	gsLinkNext *GlwStyle // C: LIST_ENTRY gs_link
	gsLinkPrev **GlwStyle

	gsSource      *miscpkg.Rstr // C: gs_source
	gsSourceFlags int           // C: gs_source_flags

	gsRefcount int // C: gs_refcount

	gsFlags2Set   uint32 // C: gs_flags2_set
	gsFlags2Clr   uint32 // C: gs_flags2_clr
	gsFlags2Local uint32 // C: gs_flags2_local

	gsImageFlagsSet   uint32 // C: gs_image_flags_set
	gsImageFlagsClr   uint32 // C: gs_image_flags_clr
	gsImageFlagsLocal uint32 // C: gs_image_flags_local

	gsTextFlagsSet   uint32 // C: gs_text_flags_set
	gsTextFlagsClr   uint32 // C: gs_text_flags_clr
	gsTextFlagsLocal uint32 // C: gs_text_flags_local

	gsFlags      uint32 // C: gs_flags (GS_SET_*)
	gsFlagsLocal uint32 // C: gs_flags_local

	gsHidden uint8 // C: gs_hidden
}

// C: gs_flags bits (glw_style.c:105-111)
const (
	gsFlagFocusWeight = 0x1   // GS_SET_FOCUS_WEIGHT
	gsFlagAlpha       = 0x2   // GS_SET_ALPHA
	gsFlagBlur        = 0x4   // GS_SET_BLUR
	gsFlagWeight      = 0x8   // GS_SET_WEIGHT
	gsFlagWidth       = 0x10  // GS_SET_WIDTH
	gsFlagHeight      = 0x20  // GS_SET_HEIGHT
	gsFlagSource      = 0x40  // GS_SET_SOURCE
	gsFlagAlign       = 0x80  // GS_SET_ALIGN
	gsFlagHidden      = 0x100 // GS_SET_HIDDEN
	gsFlagMargin      = 0x200 // GS_SET_MARGIN
)

// ===========================================================================
// C: glw_style.c — function bodies (canonical 1:1 port).
// ===========================================================================

// C: static glw_class_t glw_style (glw_style.c:995-1020) + forward decl.
// Registered in glw_style_register below (Go has no GLW_REGISTER_CLASS
// constructor attribute; glw_class_register_all wires it).
var glwStyleClass glwClass

func registerStyle() {
	glwStyleClass = glwClass{
		gcName: "style",
		gcNew: func(parent *Glw) *Glw {
			gs := &GlwStyle{}
			return &gs.w
		},
		gcSetFloat3:  gsSetFloat3,
		gcSetInt16_4: gsSetInt16_4,
		gcSetRstr:    gsSetRstr,
		gcSetFloat:   gsSetFloat,
		gcSetInt:     gsSetInt,

		gcModFlags2Always: gsModFlags2,
		gcModTextFlags:    gsModTextFlags,
		gcModImageFlags:   gsModImageFlags,

		gcSetFocusWeight: gsSetFocusWeight,
		gcSetAlpha:       gsSetAlpha,
		gcSetBlur:        gsSetBlur,
		gcSetWeight:      gsSetWeight,
		gcSetWidth:       gsSetWidth,
		gcSetHeight:      gsSetHeight,
		gcSetSource:      gsSetSource,
		gcSetAlign:       gsSetAlign,
		gcSetHidden:      gsSetHidden,
		gcSetMargin:      gsSetMargin,

		gcSetFloatUnresolved: gsSetFloatUnresolved,
		gcSetIntUnresolved:   gsSetIntUnresolved,
		gcSetRstrUnresolved:  gsSetStringUnresolved,
	}
}

// C: static glw_style_t *glw_style_find(glw_t *w, const char *name)
// (glw_style.c:122-134)
func glwStyleFind(w *Glw, name string) *GlwStyle {
	gss := w.glwStyles
	if gss != nil {
		for i := range gss.gssNumstyles {
			if name == miscpkg.RstrGet(gss.gssStyles[i].gsName) {
				return gss.gssStyles[i]
			}
		}
	}
	return nil
}

// C: static int set_float_on_widget(glw_t *w, glw_style_attribute_t *gsa,
// glw_style_t *o) (glw_style.c:140-172)
func setFloatOnWidget(w *Glw, gsa *glwStyleAttribute, o *GlwStyle) int {
	gc := w.glwClass
	var x int

	if gsa.gsaUnresolved != 0 {
		x = -1
		if gc.gcSetFloatUnresolved != nil {
			x = gc.gcSetFloatUnresolved(w, gsa.gsaUnresolvedAttribute,
				gsa.gsaF, o)
		}
		if x == -1 && gc.gcSetIntUnresolved != nil {
			x = gc.gcSetIntUnresolved(w, gsa.gsaUnresolvedAttribute,
				int(gsa.gsaF), o)
		}
	} else {
		x = -1
		if gc.gcSetFloat != nil {
			x = gc.gcSetFloat(w, glwAttribute(gsa.gsaAttribute), gsa.gsaF, o)
		}
		if x == -1 && gc.gcSetInt != nil {
			x = gc.gcSetInt(w, glwAttribute(gsa.gsaAttribute),
				int(gsa.gsaF), o)
		}
	}
	return x
}

// C: static int set_int_on_widget(glw_t *w, glw_style_attribute_t *gsa,
// glw_style_t *o) (glw_style.c:177-209)
func setIntOnWidget(w *Glw, gsa *glwStyleAttribute, o *GlwStyle) int {
	gc := w.glwClass
	var x int

	if gsa.gsaUnresolved != 0 {
		x = -1
		if gc.gcSetIntUnresolved != nil {
			x = gc.gcSetIntUnresolved(w, gsa.gsaUnresolvedAttribute,
				int(gsa.gsaI32), o)
		}
		if x == -1 && gc.gcSetFloatUnresolved != nil {
			x = gc.gcSetFloatUnresolved(w, gsa.gsaUnresolvedAttribute,
				float32(gsa.gsaI32), o)
		}
	} else {
		x = -1
		if gc.gcSetInt != nil {
			x = gc.gcSetInt(w, glwAttribute(gsa.gsaAttribute),
				int(gsa.gsaI32), o)
		}
		if x == -1 && gc.gcSetFloat != nil {
			x = gc.gcSetFloat(w, glwAttribute(gsa.gsaAttribute),
				float32(gsa.gsaI32), o)
		}
	}
	return x
}

// C: static int set_rstr_on_widget(glw_t *w, glw_style_attribute_t *gsa,
// glw_style_t *o) (glw_style.c:215-233)
func setRstrOnWidget(w *Glw, gsa *glwStyleAttribute, o *GlwStyle) int {
	gc := w.glwClass
	var x int

	if gsa.gsaUnresolved != 0 {
		x = -1
		if gc.gcSetRstrUnresolved != nil {
			x = gc.gcSetRstrUnresolved(w, gsa.gsaUnresolvedAttribute,
				gsa.gsaRstr, o)
		}
	} else {
		x = -1
		if gc.gcSetRstr != nil {
			x = gc.gcSetRstr(w, glwAttribute(gsa.gsaAttribute),
				gsa.gsaRstr, o)
		}
	}
	return x
}

// C: static void set_flags2_on_widget(glw_t *w, int set, int clr,
// glw_style_t *origin) (glw_style.c:238-249)
func setFlags2OnWidget(w *Glw, set int, clr int, origin *GlwStyle) {
	gc := w.glwClass
	if gc == &glwStyleClass {
		gsModFlags2(w, set, clr, origin)
		return
	}
	glwModFlags2(w, set, clr)
}

// C: static void glw_style_attribute_clean(glw_style_attribute_t *gsa)
// (glw_style.c:255-261)
func glwStyleAttributeClean(gsa *glwStyleAttribute) {
	if gsa.gsaType == gsaRstr {
		miscpkg.RstrRelease(gsa.gsaRstr)
		gsa.gsaType = gsaNone
	}
}

// C: static glw_style_attribute_t *glw_style_attribute_create(
// glw_style_t *gs, char unresolved) (glw_style.c:264-273)
func glwStyleAttributeCreate(gs *GlwStyle, unresolved int8) *glwStyleAttribute {
	gsa := &glwStyleAttribute{
		gsaUnresolved: unresolved,
		gsaLocal:      0,
		gsaType:       gsaNone,
	}
	// C: LIST_INSERT_HEAD(&gs->gs_attributes, gsa, gsa_link)
	gsa.gsaLinkNext = gs.gsAttributes.lhFirst
	if gsa.gsaLinkNext != nil {
		gsa.gsaLinkNext.gsaLinkPrev = &gsa.gsaLinkNext
	}
	gs.gsAttributes.lhFirst = gsa
	gsa.gsaLinkPrev = &gs.gsAttributes.lhFirst
	return gsa
}

// C: static glw_style_attribute_t *glw_style_find_attribute(
// glw_style_t *gs, glw_attribute_t attrib, int *created)
// (glw_style.c:279-294)
func glwStyleFindAttribute(gs *GlwStyle, attrib glwAttribute,
	created *int) *glwStyleAttribute {
	for gsa := gs.gsAttributes.lhFirst; gsa != nil; gsa = gsa.gsaLinkNext {
		if gsa.gsaUnresolved == 0 && gsa.gsaAttribute == int(attrib) {
			glwStyleAttributeClean(gsa)
			*created = 0
			return gsa
		}
	}
	gsa := glwStyleAttributeCreate(gs, 0)
	gsa.gsaAttribute = int(attrib)
	*created = 1
	return gsa
}

// C: static glw_style_attribute_t *glw_style_find_unresolved_attribute(
// glw_style_t *gs, const char *attrib, int *created)
// (glw_style.c:300-316)
func glwStyleFindUnresolvedAttribute(gs *GlwStyle, attrib string,
	created *int) *glwStyleAttribute {
	for gsa := gs.gsAttributes.lhFirst; gsa != nil; gsa = gsa.gsaLinkNext {
		if gsa.gsaUnresolved != 0 && gsa.gsaUnresolvedAttribute == attrib {
			glwStyleAttributeClean(gsa)
			*created = 0
			return gsa
		}
	}
	gsa := glwStyleAttributeCreate(gs, 1)
	gsa.gsaUnresolvedAttribute = attrib
	*created = 1
	return gsa
}

// C: static glw_style_t *glw_style_retain(glw_style_t *gs)
// (glw_style.c:322-327)
func glwStyleRetain(gs *GlwStyle) *GlwStyle {
	if gs != nil {
		gs.gsRefcount++
	}
	return gs
}

// C: static void glw_style_release(glw_style_t *gs) (glw_style.c:333-368)
func glwStyleRelease(gs *GlwStyle) {
	if gs == nil {
		return
	}
	w := &gs.w
	gs.gsRefcount--
	if gs.gsRefcount != 0 {
		return
	}

	for {
		gsb := w.glwStyleBindings.lhFirst
		if gsb == nil {
			break
		}
		glwStyleBindingDestroy(gsb, w.glwRoot)
	}

	glwStyleRelease(gs.gsAncestor)
	glwStylesetRelease(w.glwStyles)

	// C: LIST_REMOVE(gs, gs_link)
	if gs.gsLinkNext != nil {
		gs.gsLinkNext.gsLinkPrev = gs.gsLinkPrev
	}
	*gs.gsLinkPrev = gs.gsLinkNext

	glwPropSubscriptionDestroyList(w.glwRoot, &w.glwPropSubscriptions)
	glwViewFreeChain(w.glwRoot, w.glwDynamicExpressions)

	for {
		gsa := gs.gsAttributes.lhFirst
		if gsa == nil {
			break
		}
		glwStyleAttributeClean(gsa)
		if gsa.gsaLinkNext != nil {
			gsa.gsaLinkNext.gsaLinkPrev = gsa.gsaLinkPrev
		}
		*gsa.gsaLinkPrev = gsa.gsaLinkNext
	}

	glwViewFreeChain(w.glwRoot, gs.gsRpns)

	miscpkg.RstrRelease(gs.gsName)
	miscpkg.RstrRelease(gs.gsSource)
	miscpkg.RstrRelease(gs.gsFile)
}

// C: static void glw_style_binding_destroy(glw_style_binding_t *gsb,
// glw_root_t *gr) (glw_style.c:374-382)
func glwStyleBindingDestroy(gsb *glwStyleBinding, gr *glwRoot) {
	if gsb.gsbStyle != nil {
		// C: LIST_REMOVE(gsb, gsb_style_link)
		if gsb.gsbStyleLinkNext != nil {
			gsb.gsbStyleLinkNext.gsbStyleLinkPrev = gsb.gsbStyleLinkPrev
		}
		*gsb.gsbStyleLinkPrev = gsb.gsbStyleLinkNext
		glwStyleRelease(gsb.gsbStyle)
	}
	// C: LIST_REMOVE(gsb, gsb_widget_link)
	if gsb.gsbWidgetLinkNext != nil {
		gsb.gsbWidgetLinkNext.gsbWidgetLinkPrev = gsb.gsbWidgetLinkPrev
	}
	*gsb.gsbWidgetLinkPrev = gsb.gsbWidgetLinkNext
	gr.grStyleBindingPool.PoolPut(unsafe.Pointer(gsb))
}

// C: static void setr(int in, int *outp) (glw_style.c:388-398)
func setr(in int, outp *int) {
	if in == -1 { // Widget does not respond to attribute, skip
		return
	}
	if *outp == 1 { // We will already rerender, avoid setting to layout only
		return
	}
	if in > 0 { // Need to do something
		*outp = in
	}
}

// C: static int gsa_check_blocking(glw_style_attribute_t *gsa,
// glw_style_t *origin) (glw_style.c:403-414)
func gsaCheckBlocking(gsa *glwStyleAttribute, origin *GlwStyle) int {
	// If setted attribute is inherited and we have a local conf, bail out
	if origin != nil && gsa.gsaLocal != 0 {
		return 1
	}
	if origin == nil {
		gsa.gsaLocal = 1
	}
	return 0
}

// C: static int gs_apply_float3(glw_style_t *gs, glw_style_attribute_t *gsa)
// (glw_style.c:420-430)
func gsApplyFloat3(gs *GlwStyle, gsa *glwStyleAttribute) int {
	r := 0
	for gsb := gs.gsBindings.lhFirst; gsb != nil; gsb = gsb.gsbStyleLinkNext {
		w := gsb.gsbWidget
		setr(w.glwClass.gcSetFloat3(w, glwAttribute(gsa.gsaAttribute),
			gsa.gsaFvec[:], gs), &r)
	}
	return r
}

// C: static int gs_set_float3(struct glw *w, glw_attribute_t a,
// const float *vector, glw_style_t *origin) (glw_style.c:433-451)
func gsSetFloat3(w *Glw, a glwAttribute, vector []float32,
	origin *GlwStyle) int {
	gs := (*GlwStyle)(unsafe.Pointer(w))
	var created int
	gsa := glwStyleFindAttribute(gs, a, &created)

	if created == 0 && gsa.gsaFvec[0] == vector[0] &&
		gsa.gsaFvec[1] == vector[1] && gsa.gsaFvec[2] == vector[2] {
		return 0
	}
	if gsaCheckBlocking(gsa, origin) != 0 {
		return 0
	}
	gsa.gsaType = gsaFvec3
	copy(gsa.gsaFvec[:3], vector[:3])
	return gsApplyFloat3(gs, gsa)
}

// C: static int gs_apply_int16_4(glw_style_t *gs, glw_style_attribute_t *gsa)
// (glw_style.c:458-470)
func gsApplyInt16_4(gs *GlwStyle, gsa *glwStyleAttribute) int {
	r := 0
	for gsb := gs.gsBindings.lhFirst; gsb != nil; gsb = gsb.gsbStyleLinkNext {
		w := gsb.gsbWidget
		if w.glwClass.gcSetInt16_4 != nil {
			setr(w.glwClass.gcSetInt16_4(w, glwAttribute(gsa.gsaAttribute),
				gsa.gsaI16vec[:], gs), &r)
		}
	}
	return r
}

// C: static int gs_set_int16_4(struct glw *w, glw_attribute_t a,
// const int16_t *vector, glw_style_t *origin) (glw_style.c:473-493)
func gsSetInt16_4(w *Glw, a glwAttribute, vector []int16,
	origin *GlwStyle) int {
	gs := (*GlwStyle)(unsafe.Pointer(w))
	var created int
	gsa := glwStyleFindAttribute(gs, a, &created)

	if created == 0 && gsa.gsaI16vec == [4]int16{
		vector[0], vector[1], vector[2], vector[3]} {
		return 0
	}
	if gsaCheckBlocking(gsa, origin) != 0 {
		return 0
	}
	gsa.gsaType = gsaIvec164
	copy(gsa.gsaI16vec[:], vector[:4])
	return gsApplyInt16_4(gs, gsa)
}

// C: static int gs_apply_rstr(glw_style_t *gs, glw_style_attribute_t *gsa)
// (glw_style.c:519-529)
func gsApplyRstr(gs *GlwStyle, gsa *glwStyleAttribute) int {
	r := 0
	for gsb := gs.gsBindings.lhFirst; gsb != nil; gsb = gsb.gsbStyleLinkNext {
		w := gsb.gsbWidget
		setr(setRstrOnWidget(w, gsa, gs), &r)
	}
	return r
}

// C: static int gs_set_rstr(struct glw *w, glw_attribute_t a, rstr_t *rstr,
// glw_style_t *origin) (glw_style.c:532-551)
func gsSetRstr(w *Glw, a glwAttribute, rstr *miscpkg.Rstr,
	origin *GlwStyle) int {
	gs := (*GlwStyle)(unsafe.Pointer(w))
	var created int
	gsa := glwStyleFindAttribute(gs, a, &created)

	if created == 0 && miscpkg.RstrEq(rstr, gsa.gsaRstr) != 0 {
		return 0
	}
	if gsaCheckBlocking(gsa, origin) != 0 {
		return 0
	}
	gsa.gsaType = gsaRstr
	gsa.gsaRstr = miscpkg.RstrDup(rstr)
	return gsApplyRstr(gs, gsa)
}

// C: static int gs_set_string_unresolved(struct glw *w, const char *a,
// rstr_t *value, glw_style_t *origin) (glw_style.c:555-576)
func gsSetStringUnresolved(w *Glw, a string, value *miscpkg.Rstr,
	origin *GlwStyle) int {
	gs := (*GlwStyle)(unsafe.Pointer(w))
	var created int
	gsa := glwStyleFindUnresolvedAttribute(gs, a, &created)

	if created == 0 && gsa.gsaType == gsaRstr &&
		miscpkg.RstrEq(value, gsa.gsaRstr) != 0 {
		return 0
	}
	if gsaCheckBlocking(gsa, origin) != 0 {
		return 0
	}
	gsa.gsaType = gsaRstr
	gsa.gsaRstr = miscpkg.RstrDup(value)
	return gsApplyRstr(gs, gsa)
}

// C: static int gs_apply_int(glw_style_t *gs, glw_style_attribute_t *gsa)
// (glw_style.c:580-590)
func gsApplyInt(gs *GlwStyle, gsa *glwStyleAttribute) int {
	r := 0
	for gsb := gs.gsBindings.lhFirst; gsb != nil; gsb = gsb.gsbStyleLinkNext {
		w := gsb.gsbWidget
		setr(setIntOnWidget(w, gsa, gs), &r)
	}
	return r
}

// C: static int gs_set_int(struct glw *w, glw_attribute_t a, int i32,
// glw_style_t *origin) (glw_style.c:593-612)
func gsSetInt(w *Glw, a glwAttribute, i32 int, origin *GlwStyle) int {
	gs := (*GlwStyle)(unsafe.Pointer(w))
	var created int
	gsa := glwStyleFindAttribute(gs, a, &created)

	if created == 0 && int32(i32) == gsa.gsaI32 {
		return 0
	}
	if gsaCheckBlocking(gsa, origin) != 0 {
		return 0
	}
	gsa.gsaType = gsaInt
	gsa.gsaI32 = int32(i32)
	return gsApplyInt(gs, gsa)
}

// C: static int gs_set_int_unresolved(struct glw *w, const char *a,
// int i32, glw_style_t *origin) (glw_style.c:615-636)
func gsSetIntUnresolved(w *Glw, a string, i32 int, origin *GlwStyle) int {
	gs := (*GlwStyle)(unsafe.Pointer(w))
	var created int
	gsa := glwStyleFindUnresolvedAttribute(gs, a, &created)

	if created == 0 && int32(i32) == gsa.gsaI32 {
		return 0
	}
	if gsaCheckBlocking(gsa, origin) != 0 {
		return 0
	}
	gsa.gsaType = gsaInt
	gsa.gsaI32 = int32(i32)
	return gsApplyInt(gs, gsa)
}

// C: static int gs_apply_float(glw_style_t *gs, glw_style_attribute_t *gsa)
// (glw_style.c:643-653)
func gsApplyFloat(gs *GlwStyle, gsa *glwStyleAttribute) int {
	r := 0
	for gsb := gs.gsBindings.lhFirst; gsb != nil; gsb = gsb.gsbStyleLinkNext {
		w := gsb.gsbWidget
		setr(setFloatOnWidget(w, gsa, gs), &r)
	}
	return r
}

// C: static int gs_set_float(struct glw *w, glw_attribute_t a, float f,
// glw_style_t *origin) (glw_style.c:656-675)
func gsSetFloat(w *Glw, a glwAttribute, f float32, origin *GlwStyle) int {
	gs := (*GlwStyle)(unsafe.Pointer(w))
	var created int
	gsa := glwStyleFindAttribute(gs, a, &created)

	if created == 0 && f == gsa.gsaF {
		return 0
	}
	if gsaCheckBlocking(gsa, origin) != 0 {
		return 0
	}
	gsa.gsaType = gsaFloat
	gsa.gsaF = f
	return gsApplyFloat(gs, gsa)
}

// C: static int gs_set_float_unresolved(struct glw *w, const char *a,
// float f, glw_style_t *origin) (glw_style.c:678-699)
func gsSetFloatUnresolved(w *Glw, a string, f float32, origin *GlwStyle) int {
	gs := (*GlwStyle)(unsafe.Pointer(w))
	var created int
	gsa := glwStyleFindUnresolvedAttribute(gs, a, &created)

	if created == 0 && f == gsa.gsaF {
		return 0
	}
	if gsaCheckBlocking(gsa, origin) != 0 {
		return 0
	}
	gsa.gsaType = gsaFloat
	gsa.gsaF = f
	return gsApplyFloat(gs, gsa)
}

// C: static void gs_mod_flags2(struct glw *w, int set, int clr,
// glw_style_t *origin) (glw_style.c:703-728)
func gsModFlags2(w *Glw, set int, clr int, origin *GlwStyle) {
	gs := (*GlwStyle)(unsafe.Pointer(w))

	if origin == nil {
		gs.gsFlags2Local |= uint32(set | clr)
	} else {
		set &^= int(gs.gsFlags2Local)
		clr &^= int(gs.gsFlags2Local)
	}

	gs.gsFlags2Set |= uint32(set)
	gs.gsFlags2Clr |= uint32(clr)

	gs.gsFlags2Clr &^= uint32(set)
	gs.gsFlags2Set &^= uint32(clr)

	for gsb := gs.gsBindings.lhFirst; gsb != nil; gsb = gsb.gsbStyleLinkNext {
		setFlags2OnWidget(gsb.gsbWidget, set, clr, gs)
	}
}

// C: static void gs_mod_text_flags(struct glw *w, int set, int clr,
// glw_style_t *origin) (glw_style.c:731-755)
func gsModTextFlags(w *Glw, set int, clr int, origin *GlwStyle) {
	gs := (*GlwStyle)(unsafe.Pointer(w))

	if origin == nil {
		gs.gsTextFlagsLocal |= uint32(set | clr)
	} else {
		set &^= int(gs.gsTextFlagsLocal)
		clr &^= int(gs.gsTextFlagsLocal)
	}

	gs.gsTextFlagsSet |= uint32(set)
	gs.gsTextFlagsClr |= uint32(clr)

	gs.gsTextFlagsClr &^= uint32(set)
	gs.gsTextFlagsSet &^= uint32(clr)

	for gsb := gs.gsBindings.lhFirst; gsb != nil; gsb = gsb.gsbStyleLinkNext {
		w2 := gsb.gsbWidget
		if w2.glwClass.gcModTextFlags != nil {
			w2.glwClass.gcModTextFlags(w2, set, clr, gs)
		}
	}
}

// C: static void gs_mod_image_flags(struct glw *w, int set, int clr,
// glw_style_t *origin) (glw_style.c:758-782)
func gsModImageFlags(w *Glw, set int, clr int, origin *GlwStyle) {
	gs := (*GlwStyle)(unsafe.Pointer(w))

	if origin == nil {
		gs.gsImageFlagsLocal |= uint32(set | clr)
	} else {
		set &^= int(gs.gsImageFlagsLocal)
		clr &^= int(gs.gsImageFlagsLocal)
	}

	gs.gsImageFlagsSet |= uint32(set)
	gs.gsImageFlagsClr |= uint32(clr)

	gs.gsImageFlagsClr &^= uint32(set)
	gs.gsImageFlagsSet &^= uint32(clr)

	for gsb := gs.gsBindings.lhFirst; gsb != nil; gsb = gsb.gsbStyleLinkNext {
		w2 := gsb.gsbWidget
		if w2.glwClass.gcModImageFlags != nil {
			w2.glwClass.gcModImageFlags(w2, set, clr, gs)
		}
	}
}

// C: static int check_set_flags(glw_style_t *gs, int flag,
// glw_style_t *origin) (glw_style.c:788-799)
func checkSetFlags(gs *GlwStyle, flag uint32, origin *GlwStyle) int {
	if origin != nil {
		if gs.gsFlagsLocal&flag != 0 {
			return 1
		}
	} else {
		gs.gsFlagsLocal |= flag
	}
	gs.gsFlags |= flag
	return 0
}

// C: static void gs_set_focus_weight(struct glw *w, float v,
// glw_style_t *origin) (glw_style.c:806-819)
func gsSetFocusWeight(w *Glw, v float32, origin *GlwStyle) {
	gs := (*GlwStyle)(unsafe.Pointer(w))
	if checkSetFlags(gs, gsFlagFocusWeight, origin) != 0 {
		return
	}
	w.glwFocusWeight = v
	for gsb := gs.gsBindings.lhFirst; gsb != nil; gsb = gsb.gsbStyleLinkNext {
		glwSetFocusWeight(gsb.gsbWidget, v, gs)
	}
}

// C: static void gs_set_alpha(struct glw *w, float v, glw_style_t *origin)
// (glw_style.c:822-835)
func gsSetAlpha(w *Glw, v float32, origin *GlwStyle) {
	gs := (*GlwStyle)(unsafe.Pointer(w))
	if checkSetFlags(gs, gsFlagAlpha, origin) != 0 {
		return
	}
	w.glwAlpha = v
	for gsb := gs.gsBindings.lhFirst; gsb != nil; gsb = gsb.gsbStyleLinkNext {
		glwSetAlpha(gsb.gsbWidget, v, gs)
	}
}

// C: static void gs_set_blur(struct glw *w, float v, glw_style_t *origin)
// (glw_style.c:838-851)
func gsSetBlur(w *Glw, v float32, origin *GlwStyle) {
	gs := (*GlwStyle)(unsafe.Pointer(w))
	if checkSetFlags(gs, gsFlagBlur, origin) != 0 {
		return
	}
	w.glwSharpness = v // We borrow this even though it's not sharpness
	for gsb := gs.gsBindings.lhFirst; gsb != nil; gsb = gsb.gsbStyleLinkNext {
		glwSetBlur(gsb.gsbWidget, v, gs)
	}
}

// C: static void gs_set_weight(struct glw *w, float v, glw_style_t *origin)
// (glw_style.c:854-867)
func gsSetWeight(w *Glw, v float32, origin *GlwStyle) {
	gs := (*GlwStyle)(unsafe.Pointer(w))
	if checkSetFlags(gs, gsFlagWeight, origin) != 0 {
		return
	}
	w.glwReqWeight = v
	for gsb := gs.gsBindings.lhFirst; gsb != nil; gsb = gsb.gsbStyleLinkNext {
		glwSetWeight(gsb.gsbWidget, v, gs)
	}
}

// C: static void gs_set_width(struct glw *w, int v, glw_style_t *origin)
// (glw_style.c:897-910)
func gsSetWidth(w *Glw, v int, origin *GlwStyle) {
	gs := (*GlwStyle)(unsafe.Pointer(w))
	if checkSetFlags(gs, gsFlagWidth, origin) != 0 {
		return
	}
	w.glwReqSizeX = int16(v)
	for gsb := gs.gsBindings.lhFirst; gsb != nil; gsb = gsb.gsbStyleLinkNext {
		glwSetWidth(gsb.gsbWidget, v, gs)
	}
}

// C: static void gs_set_height(struct glw *w, int v, glw_style_t *origin)
// (glw_style.c:913-926)
func gsSetHeight(w *Glw, v int, origin *GlwStyle) {
	gs := (*GlwStyle)(unsafe.Pointer(w))
	if checkSetFlags(gs, gsFlagHeight, origin) != 0 {
		return
	}
	w.glwReqSizeY = int16(v)
	for gsb := gs.gsBindings.lhFirst; gsb != nil; gsb = gsb.gsbStyleLinkNext {
		glwSetHeight(gsb.gsbWidget, v, gs)
	}
}

// C: static void gs_set_align(struct glw *w, int v, glw_style_t *origin)
// (glw_style.c:929-942)
func gsSetAlign(w *Glw, v int, origin *GlwStyle) {
	gs := (*GlwStyle)(unsafe.Pointer(w))
	if checkSetFlags(gs, gsFlagAlign, origin) != 0 {
		return
	}
	w.glwAlignment = uint8(v)
	for gsb := gs.gsBindings.lhFirst; gsb != nil; gsb = gsb.gsbStyleLinkNext {
		glwSetAlign(gsb.gsbWidget, v, gs)
	}
}

// C: static void gs_set_hidden(struct glw *w, int v, glw_style_t *origin)
// (glw_style.c:945-958)
func gsSetHidden(w *Glw, v int, origin *GlwStyle) {
	gs := (*GlwStyle)(unsafe.Pointer(w))
	if checkSetFlags(gs, gsFlagHidden, origin) != 0 {
		return
	}
	gs.gsHidden = uint8(v)
	for gsb := gs.gsBindings.lhFirst; gsb != nil; gsb = gsb.gsbStyleLinkNext {
		glwSetHidden(gsb.gsbWidget, v, gs)
	}
}

// C: static void gs_set_margin(struct glw *w, const int16_t *v,
// glw_style_t *origin) (glw_style.c:961-974)
func gsSetMargin(w *Glw, v []int16, origin *GlwStyle) {
	gs := (*GlwStyle)(unsafe.Pointer(w))
	if checkSetFlags(gs, gsFlagMargin, origin) != 0 {
		return
	}
	copy(w.glwMargin[:], v[:4])
	for gsb := gs.gsBindings.lhFirst; gsb != nil; gsb = gsb.gsbStyleLinkNext {
		glwSetMargin(gsb.gsbWidget, v, gs)
	}
}

// C: static void gs_set_source(struct glw *w, rstr_t *r, int flags,
// glw_style_t *origin) (glw_style.c:977-992)
func gsSetSource(w *Glw, r *miscpkg.Rstr, flags int, origin *GlwStyle) {
	gs := (*GlwStyle)(unsafe.Pointer(w))
	if checkSetFlags(gs, gsFlagSource, origin) != 0 {
		return
	}
	miscpkg.RstrSet(&gs.gsSource, r)
	gs.gsSourceFlags = flags

	for gsb := gs.gsBindings.lhFirst; gsb != nil; gsb = gsb.gsbStyleLinkNext {
		w2 := gsb.gsbWidget
		if w2.glwClass.gcSetSource != nil {
			w2.glwClass.gcSetSource(w2, r, flags, gs)
		}
	}
}

// C: glw_style_t *glw_style_create(glw_t *parent, rstr_t *name,
// rstr_t *file, int line, int inherit) (glw_style.c:1027-1050)
func glwStyleCreate(parent *Glw, name *miscpkg.Rstr, file *miscpkg.Rstr,
	line int, inherit int) *GlwStyle {
	var ancestor *GlwStyle
	if inherit != 0 {
		ancestor = glwStyleFind(parent, miscpkg.RstrGet(name))
	}

	gr := parent.glwRoot
	gs := &GlwStyle{}
	// C: LIST_INSERT_HEAD(&gr->gr_all_styles, gs, gs_link)
	gs.gsLinkNext = gr.grAllStyles.lhFirst
	if gs.gsLinkNext != nil {
		gs.gsLinkNext.gsLinkPrev = &gs.gsLinkNext
	}
	gr.grAllStyles.lhFirst = gs
	gs.gsLinkPrev = &gr.grAllStyles.lhFirst

	gs.w.glwClass = &glwStyleClass
	gs.w.glwRoot = gr
	gs.w.glwStyles = glwStylesetRetain(parent.glwStyles)

	gr.grStyleTally++
	gs.gsID = gr.grStyleTally
	gs.gsName = miscpkg.RstrDup(name)
	gs.gsFile = miscpkg.RstrDup(file)
	gs.gsLine = line
	gs.gsAncestor = glwStyleRetain(ancestor)

	glwStyleBindAncestor(gs, ancestor)

	return gs
}

// C: void glw_style_attach_rpns(glw_style_t *gs, struct token *t)
// (glw_style.c:1056-1066)
func glwStyleAttachRpns(gs *GlwStyle, t *Token) {
	gs.gsRpns = t
	for ; t != nil; t = t.next {
		t.setRpnOrigin(gs.gsID)
	}
}

// C: void glw_styleset_release(glw_styleset_t *gss) (glw_style.c:1104-1120)
func glwStylesetRelease(gss *glwStyleset) {
	if gss == nil {
		return
	}
	gss.gssRefcount--
	if gss.gssRefcount != 0 {
		return
	}
	for i := range gss.gssNumstyles {
		glwStyleRelease(gss.gssStyles[i])
	}
}

// C: glw_styleset_t *glw_styleset_retain(glw_styleset_t *gss)
// (glw_style.c:1070-1076 — inlined in C? actually defined in glw_style.h)
func glwStylesetRetain(gss *glwStyleset) *glwStyleset {
	if gss != nil {
		gss.gssRefcount++
	}
	return gss
}

// C: glw_styleset_t *glw_styleset_add(glw_styleset_t *gss, glw_style_t *gs)
// (glw_style.c:1125-1156)
func glwStylesetAdd(gss *glwStyleset, gs *GlwStyle) *glwStyleset {
	var i, items int

	if gss == nil {
		i = 0
		items = 1
	} else {
		for i = 0; i < gss.gssNumstyles; i++ {
			if miscpkg.RstrGet(gs.gsName) ==
				miscpkg.RstrGet(gss.gssStyles[i].gsName) {
				break
			}
		}
		if i == gss.gssNumstyles {
			items = i + 1
		} else {
			items = gss.gssNumstyles
		}
	}

	copy_ := &glwStyleset{
		gssRefcount:  1,
		gssNumstyles: items,
		gssStyles:    make([]*GlwStyle, items),
	}
	if gss != nil {
		j := 0
		for j = 0; j < gss.gssNumstyles; j++ {
			if j != i {
				copy_.gssStyles[j] = glwStyleRetain(gss.gssStyles[j])
			}
		}
	}
	copy_.gssStyles[i] = glwStyleRetain(gs)
	return copy_
}

// C: static void glw_style_remove_styling_rpns(glw_root_t *gr,
// token_t **p, int origin) (glw_style.c:1162-1177)
func glwStyleRemoveStylingRpns(gr *glwRoot, p **Token, origin int) {
	for {
		t := *p
		if t == nil {
			break
		}
		if t.getRpnOrigin() == origin {
			*p = t.next
			t.next = nil
			glwViewFreeChain(gr, t)
		} else {
			p = &t.next
		}
	}
}

// C: static void glw_style_remove_styling_rpns_on_style(glw_style_t *gs,
// int origin) (glw_style.c:1184-1194)
func glwStyleRemoveStylingRpnsOnStyle(gs *GlwStyle, origin int) {
	gr := gs.w.glwRoot
	glwStyleRemoveStylingRpns(gr, &gs.gsRpns, origin)

	for gsb := gs.gsBindings.lhFirst; gsb != nil; gsb = gsb.gsbStyleLinkNext {
		glwStyleRemoveStylingRpnsOnWidget(gsb.gsbWidget, origin)
	}
}

// C: static void glw_style_remove_styling_rpns_on_widget(glw_t *w,
// int origin) (glw_style.c:1200-1208)
func glwStyleRemoveStylingRpnsOnWidget(w *Glw, origin int) {
	if w.glwClass == &glwStyleClass {
		glwStyleRemoveStylingRpnsOnStyle((*GlwStyle)(unsafe.Pointer(w)), origin)
	} else {
		glwStyleRemoveStylingRpns(w.glwRoot, &w.glwDynamicExpressions, origin)
	}
}

// C: static void glw_style_insert_styling_rpns_on_widget(glw_t *w,
// token_t *rpns, glw_view_eval_context_t *ec0) (glw_style.c:1215-1240)
func glwStyleInsertStylingRpnsOnWidget(w *Glw, rpns *Token,
	ec0 *glwViewEvalContext) {
	rpn := glwViewCloneChain(w.glwRoot, rpns, nil)
	var copy int

	ec := *ec0
	ec.w = w

	for rpn != nil {
		t := rpn
		rpn = t.next
		glwViewEvalRpn(t, &ec, &copy)

		if copy != 0 {
			t.next = w.glwDynamicExpressions
			w.glwDynamicExpressions = t
			t.tDynamicEval = uint8(copy)
			w.glwDynamicEval |= uint8(copy)
		} else {
			t.next = nil
			glwViewFreeChain(w.glwRoot, t)
		}
	}
}

// C: static void glw_style_insert_styling_rpns_on_style(glw_style_t *dst,
// glw_style_t *src, glw_view_eval_context_t *ec) (glw_style.c:1246-1269)
func glwStyleInsertStylingRpnsOnStyle(dst *GlwStyle, src *GlwStyle,
	ec *glwViewEvalContext) {
	if src.gsRpns == nil {
		return
	}
	gr := src.w.glwRoot

	var last *Token
	first := glwViewCloneChain(gr, src.gsRpns, &last)

	last.next = dst.gsRpns
	dst.gsRpns = first

	for gsb := dst.gsBindings.lhFirst; gsb != nil; gsb = gsb.gsbStyleLinkNext {
		w := gsb.gsbWidget
		if w.glwClass == &glwStyleClass {
			glwStyleInsertStylingRpnsOnStyle((*GlwStyle)(unsafe.Pointer(w)),
				src, ec)
		} else {
			glwStyleInsertStylingRpnsOnWidget(w, src.gsRpns, ec)
		}
	}
}

// C: static int glw_style_insert(glw_t *w, glw_style_t *gs,
// glw_view_eval_context_t *ec) (glw_style.c:1275-1370)
func glwStyleInsert(w *Glw, gs *GlwStyle, ec *glwViewEvalContext) int {
	r := 0

	gsb := (*glwStyleBinding)(w.glwRoot.grStyleBindingPool.PoolGet())
	gsb.gsbStyle = glwStyleRetain(gs)
	gsb.gsbWidget = w
	// C: LIST_INSERT_HEAD(&w->glw_style_bindings, gsb, gsb_widget_link)
	gsb.gsbWidgetLinkNext = w.glwStyleBindings.lhFirst
	if gsb.gsbWidgetLinkNext != nil {
		gsb.gsbWidgetLinkNext.gsbWidgetLinkPrev = &gsb.gsbWidgetLinkNext
	}
	w.glwStyleBindings.lhFirst = gsb
	gsb.gsbWidgetLinkPrev = &w.glwStyleBindings.lhFirst
	// C: LIST_INSERT_HEAD(&gs->gs_bindings, gsb, gsb_style_link)
	gsb.gsbStyleLinkNext = gs.gsBindings.lhFirst
	if gsb.gsbStyleLinkNext != nil {
		gsb.gsbStyleLinkNext.gsbStyleLinkPrev = &gsb.gsbStyleLinkNext
	}
	gs.gsBindings.lhFirst = gsb
	gsb.gsbStyleLinkPrev = &gs.gsBindings.lhFirst
	gsb.gsbMark = 0

	if w.glwClass == &glwStyleClass {
		dst := (*GlwStyle)(unsafe.Pointer(w))
		glwStyleInsertStylingRpnsOnStyle(dst, gs, ec)
	} else {
		glwStyleInsertStylingRpnsOnWidget(w, gs.gsRpns, ec)
	}

	for gsa := gs.gsAttributes.lhFirst; gsa != nil; gsa = gsa.gsaLinkNext {
		switch gsa.gsaType {
		case gsaInt:
			setr(setIntOnWidget(w, gsa, gs), &r)
		case gsaFloat:
			setr(setFloatOnWidget(w, gsa, gs), &r)
		case gsaRstr:
			setr(setRstrOnWidget(w, gsa, gs), &r)
		case gsaFvec3:
			if w.glwClass.gcSetFloat3 != nil {
				setr(w.glwClass.gcSetFloat3(w, glwAttribute(gsa.gsaAttribute),
					gsa.gsaFvec[:], gs), &r)
			}
		case gsaIvec164:
			if w.glwClass.gcSetInt16_4 != nil {
				setr(w.glwClass.gcSetInt16_4(w, glwAttribute(gsa.gsaAttribute),
					gsa.gsaI16vec[:], gs), &r)
			}
		}
	}

	if gs.gsFlags2Set != 0 || gs.gsFlags2Clr != 0 {
		setFlags2OnWidget(w, int(gs.gsFlags2Set), int(gs.gsFlags2Clr), gs)
		r = 1
	}

	if w.glwClass.gcModImageFlags != nil &&
		(gs.gsImageFlagsSet != 0 || gs.gsImageFlagsClr != 0) {
		w.glwClass.gcModImageFlags(w, int(gs.gsImageFlagsSet),
			int(gs.gsImageFlagsClr), gs)
	}

	if w.glwClass.gcModTextFlags != nil &&
		(gs.gsTextFlagsSet != 0 || gs.gsTextFlagsClr != 0) {
		w.glwClass.gcModTextFlags(w, int(gs.gsTextFlagsSet),
			int(gs.gsTextFlagsClr), gs)
	}

	if gs.gsFlags&gsFlagFocusWeight != 0 {
		glwSetFocusWeight(w, gs.w.glwFocusWeight, gs)
	}
	if gs.gsFlags&gsFlagMargin != 0 {
		glwSetMargin(w, gs.w.glwMargin[:], gs)
	}
	if gs.gsFlags&gsFlagAlpha != 0 {
		glwSetAlpha(w, gs.w.glwAlpha, gs)
	}
	if gs.gsFlags&gsFlagBlur != 0 {
		glwSetBlur(w, gs.w.glwSharpness, gs)
	}
	if gs.gsFlags&gsFlagWeight != 0 {
		glwSetWeight(w, gs.w.glwReqWeight, gs)
	}
	if gs.gsFlags&gsFlagWidth != 0 {
		glwSetWidth(w, int(gs.w.glwReqSizeX), gs)
	}
	if gs.gsFlags&gsFlagHeight != 0 {
		glwSetHeight(w, int(gs.w.glwReqSizeY), gs)
	}
	if gs.gsFlags&gsFlagAlign != 0 {
		glwSetAlign(w, int(gs.w.glwAlignment), gs)
	}
	if gs.gsFlags&gsFlagHidden != 0 {
		glwSetHidden(w, int(gs.gsHidden), gs)
	}
	if gs.gsFlags&gsFlagSource != 0 && w.glwClass.gcSetSource != nil {
		w.glwClass.gcSetSource(w, gs.gsSource, gs.gsSourceFlags, gs)
	}

	return r
}

// C: static void glw_style_bindings_sweep(glw_t *w, int all)
// (glw_style.c:1376-1400)
func glwStyleBindingsSweep(w *Glw, all int) {
	gr := w.glwRoot

	for gsb := w.glwStyleBindings.lhFirst; gsb != nil; {
		next := gsb.gsbWidgetLinkNext

		if all != 0 || gsb.gsbMark != 0 {
			gs := gsb.gsbStyle

			// C: LIST_REMOVE(gsb, gsb_style_link)
			if gsb.gsbStyleLinkNext != nil {
				gsb.gsbStyleLinkNext.gsbStyleLinkPrev = gsb.gsbStyleLinkPrev
			}
			*gsb.gsbStyleLinkPrev = gsb.gsbStyleLinkNext

			for gs != nil {
				glwStyleRemoveStylingRpnsOnWidget(w, gs.gsID)
				gs = gs.gsAncestor
			}

			glwStyleRelease(gsb.gsbStyle)
			gsb.gsbStyle = nil
			glwStyleBindingDestroy(gsb, gr)
		}
		gsb = next
	}
}

// C: int glw_styleset_for_widget(glw_t *w, const char *name,
// glw_view_eval_context_t *ec) (glw_style.c:1406-1414)
func glwStylesetForWidget(w *Glw, name string, ec *glwViewEvalContext) int {
	glwStyleBindingsSweep(w, 1)
	gs := glwStyleFind(w, name)
	if gs != nil {
		return glwStyleInsert(w, gs, ec)
	}
	return 0
}

// C: int glw_styleset_for_widget_multiple(glw_t *w, struct token *t,
// struct glw_view_eval_context *ec) (glw_style.c:1419-1450)
func glwStylesetForWidgetMultiple(w *Glw, t *Token,
	ec *glwViewEvalContext) int {
	r := 0

	for gsb := w.glwStyleBindings.lhFirst; gsb != nil; gsb = gsb.gsbWidgetLinkNext {
		gsb.gsbMark = 1
	}

	for ; t != nil; t = t.next {
		if t.typ != tokenRstring && t.typ != tokenURI {
			continue
		}
		gs := glwStyleFind(w, miscpkg.RstrGet(t.tRstring))
		if gs == nil {
			continue
		}

		var gsb *glwStyleBinding
		for gsb = w.glwStyleBindings.lhFirst; gsb != nil; gsb = gsb.gsbWidgetLinkNext {
			if gsb.gsbStyle == gs {
				break
			}
		}
		if gsb != nil {
			gsb.gsbMark = 0
			continue
		}
		setr(glwStyleInsert(w, gs, ec), &r)
	}
	glwStyleBindingsSweep(w, 0)
	return r
}

// C: void glw_style_bind_ancestor(glw_style_t *gs, glw_style_t *ancestor)
// (glw_style.c:1456-1460)
func glwStyleBindAncestor(gs *GlwStyle, ancestor *GlwStyle) {
	if ancestor != nil {
		glwStyleInsert(&gs.w, ancestor, nil)
	}
}

// C: void glw_style_unbind_all(glw_t *w) (glw_style.c:1481-1485)
func glwStyleUnbindAll(w *Glw) {
	glwStyleBindingsSweep(w, 1)
}

// C: void glw_style_update_em(glw_root_t *gr) (glw_style.c:1492-1498)
func glwStyleUpdateEm(gr *glwRoot) {
	for gs := gr.grAllStyles.lhFirst; gs != nil; gs = gs.gsLinkNext {
		if gs.w.glwDynamicEval&GLW_VIEW_EVAL_EM != 0 {
			glwViewEvalDynamics(&gs.w, GLW_VIEW_EVAL_EM)
		}
	}
}

// C: void glw_style_cleanup(glw_root_t *gr) (glw_style.c:1505-1519)
func glwStyleCleanup(gr *glwRoot) {
	for gs := gr.grAllStyles.lhFirst; gs != nil; gs = gs.gsLinkNext {
		fmt.Printf("Style %s %s:%d still in use ancestor:%p refcnt=%d\n",
			miscpkg.RstrGet(gs.gsName),
			miscpkg.RstrGet(gs.gsFile),
			gs.gsLine,
			gs.gsAncestor,
			gs.gsRefcount)
		fmt.Printf("Bindings:%p\n", gs.gsBindings.lhFirst)
	}
}
