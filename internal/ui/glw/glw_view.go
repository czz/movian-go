package glw

// C: src/ui/glw/glw_view.h — canonical 1:1 port.

import (
	"fmt"
	"unsafe"

	archpkg "github.com/czz/movian-go/internal/arch"
	eventpkg "github.com/czz/movian-go/internal/event"
	facore "github.com/czz/movian-go/internal/fileaccess"
	miscpkg "github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
)

// C: token_type_t (glw_view.h:34-102)
type tokenType int

const (
	tokenStart                tokenType = iota // TOKEN_START
	tokenEnd                                   // TOKEN_END
	tokenHash                                  // TOKEN_HASH              #
	tokenAssignment                            // TOKEN_ASSIGNMENT        =
	tokenCondAssignment                        // TOKEN_COND_ASSIGNMENT   ?=
	tokenRefAssignment                         // TOKEN_REF_ASSIGNMENT    :=
	tokenDebugAssignment                       // TOKEN_DEBUG_ASSIGNMENT  _=_
	tokenLinkAssignment                        // TOKEN_LINK_ASSIGNMENT   <-
	tokenEndOfExpr                             // TOKEN_END_OF_EXPR       ;
	tokenSeparator                             // TOKEN_SEPARATOR         ,
	tokenBlockOpen                             // TOKEN_BLOCK_OPEN        {
	tokenBlockClose                            // TOKEN_BLOCK_CLOSE       }
	tokenLeftParenthesis                       // TOKEN_LEFT_PARENTHESIS  (
	tokenRightParenthesis                      // TOKEN_RIGHT_PARENTHESIS )
	tokenLeftBracket                           // TOKEN_LEFT_BRACKET      [
	tokenRightBracket                          // TOKEN_RIGHT_BRACKET     ]
	tokenDot                                   // TOKEN_DOT               .
	tokenAdd                                   // TOKEN_ADD               +
	tokenSub                                   // TOKEN_SUB               -
	tokenMultiply                              // TOKEN_MULTIPLY          *
	tokenDivide                                // TOKEN_DIVIDE            /
	tokenModulo                                // TOKEN_MODULO            %
	tokenDollar                                // TOKEN_DOLLAR            $
	tokenAmpersand                             // TOKEN_AMPERSAND         &
	tokenBooleanAnd                            // TOKEN_BOOLEAN_AND       &&
	tokenBooleanOr                             // TOKEN_BOOLEAN_OR        ||
	tokenBooleanXor                            // TOKEN_BOOLEAN_XOR       ^^
	tokenEq                                    // TOKEN_EQ                ==
	tokenNeq                                   // TOKEN_NEQ               !=
	tokenBooleanNot                            // TOKEN_BOOLEAN_NOT       !
	tokenNullCoalesce                          // TOKEN_NULL_COALESCE     ??
	tokenLt                                    // TOKEN_LT                <
	tokenGt                                    // TOKEN_GT                >
	tokenQuestionmark                          // TOKEN_QUESTIONMARK      ?
	tokenColon                                 // TOKEN_COLON             :
	tokenRstring                               // TOKEN_RSTRING
	tokenCstring                               // TOKEN_CSTRING
	tokenFloat                                 // TOKEN_FLOAT
	tokenEm                                    // TOKEN_EM
	tokenInt                                   // TOKEN_INT
	tokenIdentifier                            // TOKEN_IDENTIFIER
	tokenFunction                              // TOKEN_FUNCTION
	tokenPropertyRef                           // TOKEN_PROPERTY_REF
	tokenPropertyOwner                         // TOKEN_PROPERTY_OWNER
	tokenPropertyName                          // TOKEN_PROPERTY_NAME
	tokenPropertySubscription                  // TOKEN_PROPERTY_SUBSCRIPTION
	tokenResolvedAttribute                     // TOKEN_RESOLVED_ATTRIBUTE
	tokenUnresolvedAttribute                   // TOKEN_UNRESOLVED_ATTRIBUTE
	tokenVoid                                  // TOKEN_VOID
	tokenDirectory                             // TOKEN_DIRECTORY
	// Synthetic tokens (after parser)
	tokenExpr        // TOKEN_EXPR
	tokenRpn         // TOKEN_RPN
	tokenPureRpn     // TOKEN_PURE_RPN
	tokenBlock       // TOKEN_BLOCK
	tokenNop         // TOKEN_NOP
	tokenVectorFloat // TOKEN_VECTOR_FLOAT
	tokenEvent       // TOKEN_EVENT
	tokenGem         // TOKEN_GEM
	tokenURI         // TOKEN_URI
	tokenVector      // TOKEN_VECTOR
	tokenModFlags    // TOKEN_MOD_FLAGS
	tokenTenary      // TOKEN_TENARY
	tokenNum         // TOKEN_num
)

// C: t_flags bits (glw_view.h:121-123)
const (
	tokenFSelected      = 0x1 // TOKEN_F_SELECTED
	tokenFCanonicalPath = 0x2 // TOKEN_F_CANONICAL_PATH
	tokenFPropLink      = 0x4 // TOKEN_F_PROP_LINK
)

// C: #define TOKEN_PROPERTY_NAME_VEC_SIZE (16 / __SIZEOF_POINTER__) = 2
const tokenPropertyNameVecSize = 2

// C: typedef struct token (glw_view.h:108-207)
//
// The C unions are flattened into named fields — the same field names as the
// C #defines (t_elements, t_extra, t_extra_float, t_extra_int, t_attrib,
// t_propsubr, t_prop_name_id, t_pnvec, t_cstring, t_rstring, t_rstrtype,
// t_float, t_float_vector, t_int, t_func, t_func_arg, t_gem, t_event, t_prop,
// t_uri_title, t_uri, t_rpn_origin, t_set, t_clr).
type Token struct {
	next  *Token // C: next
	child *Token // C: child
	// C: tmp — token_t* used as eval-stack link AND (abused) as macro_arg_t*
	// in the preproc; unsafe.Pointer holds both faithfully.
	tmp unsafe.Pointer // C: tmp

	file *miscpkg.Rstr // C: file
	line int           // C: line

	typ          tokenType // C: type
	tNumArgs     int16     // C: t_num_args
	tFlags       uint8     // C: t_flags (TOKEN_F_*)
	tDynamicEval uint8     // C: t_dynamic_eval

	// C: union arg { int elements; void *extra; float f; int args; int i; }
	tElements   int     // C: t_elements
	tExtra      any     // C: t_extra
	tExtraFloat float32 // C: t_extra_float
	tExtraInt   int     // C: t_extra_int

	// C: union { t_attrib / t_propsubr / t_prop_name_id }
	tAttrib     *tokenAttrib // C: t_attrib
	tPropsubr   *GlwPropSub  // C: t_propsubr
	tPropNameID int          // C: t_prop_name_id

	// C: union u { ... }
	tInt         int                                     // C: t_int (u.ival) / t_rpn_origin
	tPnvec       [tokenPropertyNameVecSize]*miscpkg.Rstr // C: t_pnvec
	tFloat       float32                                 // C: t_float (u.f.value)
	tFloatHow    int                                     // C: u.f.how (same as PROP_SET_ ...)
	tFloatVector [4]float32                              // C: t_float_vector
	tFunc        *tokenFunc                              // C: t_func
	tFuncArg     any                                     // C: t_func_arg
	tGem         *GlwEventMap                            // C: t_gem
	tEvent       *eventpkg.Event                         // C: t_event
	tProp        *propcore.Prop                          // C: t_prop
	tURITitle    *miscpkg.Rstr                           // C: t_uri_title
	tURI         *miscpkg.Rstr                           // C: t_uri
	tRstring     *miscpkg.Rstr                           // C: t_rstring (u.rstr.rstr)
	tRstrType    int                                     // C: t_rstrtype (prop_str_type_t)
	tCstring     string                                  // C: t_cstring
	tSet         int                                     // C: t_set (u.modflags.set)
	tClr         int                                     // C: t_clr (u.modflags.clr)
}

// C: t_rpn_origin aliases u.ival — same storage as t_int (glw_view.h:205)
func (t *Token) getRpnOrigin() int  { return t.tInt }
func (t *Token) setRpnOrigin(v int) { t.tInt = v }

// ---------------------------------------------------------------------------
// C: errorinfo_t (glw_view.h:212-216)
const errorinfoPathMax = 4096 // C: PATH_MAX

type errorinfoT struct {
	file  string // C: char file[PATH_MAX]
	error string // C: char error[128]
	line  int    // C: line
}

// ---------------------------------------------------------------------------
// C: glw_view_eval_context_t (glw_view.h:223-244)
type glwViewEvalContext struct {
	stack *Token      // C: stack
	ei    *errorinfoT // C: ei
	alloc *Token      // C: alloc
	w     *Glw        // C: w

	scope *glwScope // C: scope

	gr              *glwRoot         // C: gr
	rc              *glwRctx         // C: rc
	rpn             *Token           // C: rpn
	sublist         *glwPropSubSlist // C: sublist
	sublistRpnlocal glwPropSubSlist  // C: sublist_rpnlocal
	tgtprop         *propcore.Prop   // C: tgtprop

	dynamicEval          uint16 // C: dynamic_eval
	debug                int8   // C: debug
	passiveSubscriptions int8   // C: passive_subscriptions
	mask                 int8   // C: mask
	styleIndexTally      uint8  // C: style_index_tally
}

// ---------------------------------------------------------------------------
// C: glw_clone_t (glw_view.h:251-261)
type glwClone struct {
	cLinkNext  *glwClone // C: LIST_ENTRY c_link
	cLinkPrev  **glwClone
	cSc        *subCloner     // C: c_sc
	cW         *Glw           // C: c_w
	cProp      *propcore.Prop // C: c_prop
	cCloneRoot *propcore.Prop // C: c_clone_root

	cPos       uint16 // C: c_pos
	cEvaluated int8   // C: c_evaluated
}

// ---------------------------------------------------------------------------
// C: token_func_t (glw_view.h:266-274)
type tokenFunc struct {
	name    string                                                                  // C: name
	nargs   int                                                                     // C: nargs
	cb      func(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int // C: cb
	ctor    func(self *Token)                                                       // C: ctor
	dtor    func(gr *glwRoot, self *Token)                                          // C: dtor
	preproc func(gr *glwRoot, ei *errorinfoT, t *Token) *Token                      // C: preproc
}

// ---------------------------------------------------------------------------
// C: token_attrib_t (glw_view.h:280-288)
type tokenAttrib struct {
	name   string                                                     // C: name
	set    func(ec *glwViewEvalContext, a *tokenAttrib, t *Token) int // C: set
	attrib int                                                        // C: attrib
	fn     any                                                        // C: fn (void * — setter fnptr of varying signature)
	flags  int                                                        // C: flags
}

// C: #define GLW_ATTRIB_FLAG_NO_SUBSCRIPTION 0x1
const glwAttribFlagNoSubscription = 0x1

// ===========================================================================
// C: src/ui/glw/glw_view.c — canonical 1:1 port.
// ===========================================================================

// C: typedef struct glw_view (glw_view.c:34-38)
type glwView struct {
	w        Glw            // C: glw_t w
	viewprop *propcore.Prop // C: prop_t *viewprop
}

// C: static void glw_view_layout(glw_t *w, const glw_rctx_t *rc)
// (glw_view.c:43-48)
func glwViewLayout(w *Glw, rc *glwRctx) {
	c := w.glwChilds.tqhFirst
	if c != nil {
		glwLayout0(c, rc)
	}
}

// C: static int glw_view_callback(glw_t *w, void *opaque, glw_signal_t
// signal, void *extra) (glw_view.c:54-67)
func glwViewCallback(w *Glw, opaque any, signal glwSignal, extra any) int {
	switch signal {
	case glwSignalChildConstraintsChanged:
		glwCopyConstraints(w, extra.(*Glw))
		return 1
	}
	return 0
}

// C: static void glw_view_dtor(glw_t *w) (glw_view.c:73-77)
func glwViewDtor(w *Glw) {
	v := (*glwView)(unsafe.Pointer(w))
	glwDeps.pm.Destroy(v.viewprop)
}

// C: static void glw_view_render(glw_t *w, const glw_rctx_t *rc)
// (glw_view.c:83-88)
func glwViewRender(w *Glw, rc *glwRctx) {
	c := w.glwChilds.tqhFirst
	if c != nil {
		glwRender0(c, rc)
	}
}

// C: static glw_class_t glw_view (glw_view.c:93-99) + GLW_REGISTER_CLASS
var glwViewClass = glwClass{
	gcName:          "view",
	gcInstanceSize:  int(unsafe.Sizeof(glwView{})),
	gcNew:           func(parent *Glw) *Glw { gv := &glwView{}; return &gv.w },
	gcLayout:        glwViewLayout,
	gcRender:        glwViewRender,
	gcDtor:          glwViewDtor,
	gcSignalHandler: glwViewCallback,
}

// C: static void glw_view_error(glw_t *parent, const char *error, const
// char *file, int line, glw_scope_t *scope) (glw_view.c:139-155)
func glwViewError(parent *Glw, error string, file string, line int, scope *glwScope) {
	var buf string
	if file != "" {
		buf = fmt.Sprintf("GLW %s:%d: Error: %s", file, line, error)
	} else {
		buf = fmt.Sprintf("Error: %s", error)
	}

	w := glwCreate(parent.glwRoot, glwClassFindByName("label"),
		parent, nil, nil, scope, nil, 0)
	w.glwClass.gcSetCaption(w, buf, 0)
}

// C: static void gcv_release(glw_root_t *gr, glw_cached_view_t *gcv)
// (glw_view.c:119-134)
func gcvRelease(gr *glwRoot, gcv *GlwCachedView) {
	gcv.gcvRefcount--
	if gcv.gcvRefcount > 0 {
		return
	}

	if gcv.gcvSof != nil {
		glwViewFreeChain(gr, gcv.gcvSof)
	}

	miscpkg.RstrRelease(gcv.gcvURL)
	miscpkg.RstrRelease(gcv.gcvAlturl)
}

// C: static void eval_loaded_view(glw_root_t *gr, glw_cached_view_t *gcv,
// glw_view_t *view, glw_scope_t *scope) (glw_view.c:140-179)
func evalLoadedView(gr *glwRoot, gcv *GlwCachedView, view *glwView, scope *glwScope) {
	if gcv.gcvError != "" {
		glwViewError(&view.w, gcv.gcvError, gcv.gcvErrorFile, gcv.gcvErrorLine, scope)
		return
	}

	t := glwViewCloneChain(gr, gcv.gcvSof, nil)

	var ec glwViewEvalContext
	var ei errorinfoT

	ec.gr = gr
	ec.rc = nil
	ec.w = &view.w
	ec.ei = &ei

	view.viewprop = glwDeps.pm.CreateRoot("")
	ec.scope = glwScopeDup(scope, 1<<glwRootParentview)
	ec.scope.gsRoots[glwRootParentview].P = ec.scope.gsRoots[glwRootView].P
	ec.scope.gsRoots[glwRootView].P = glwDeps.pm.RefInc(view.viewprop)

	ec.sublist = &ec.w.glwPropSubscriptions

	rc := glwViewEvalBlock(t, &ec, nil)
	if rc != 0 {
		glwDestroyChilds(ec.w)
		glwViewError(ec.w, ei.error, ei.file, ei.line, scope)
	}
	glwViewFreeChain(gr, t)

	if gr.grPendingFocus != nil {
		glwFocusCheckPending(ec.w.glwParent)
	}

	glwScopeRelease(ec.scope)
}

// C: static void gcv_load(glw_root_t *gr, glw_cached_view_t *gcv,
// int may_unlock) (glw_view.c:185-229)
func gcvLoad(gr *glwRoot, gcv *GlwCachedView, mayUnlock int) {
	var ei errorinfoT

	if mayUnlock != 0 {
		glwUnlock(gr)
	}

	file := gcv.gcvURL
	var lerr error
	buf, lerr := facore.FALoad2(glwDeps.fam, miscpkg.RstrGet(gcv.gcvURL), nil)

	if buf == nil && gcv.gcvAlturl != nil {
		file = gcv.gcvAlturl
		buf, lerr = facore.FALoad2(glwDeps.fam, miscpkg.RstrGet(gcv.gcvAlturl), nil)
	}

	if mayUnlock != 0 {
		glwLock(gr)
	}

	if buf == nil {
		msg := "load failed"
		if lerr != nil {
			msg = lerr.Error()
		}
		gcv.gcvError = fmt.Sprintf("Unable to open \"%s\" -- %s",
			miscpkg.RstrGet(file), msg)
		return
	}

	sof := glwViewTokenAlloc(gr)
	sof.typ = tokenStart
	sof.file = miscpkg.RstrDup(file)

	l := glwViewLexer(gr, string(buf.Data), &ei, file, sof)
	if l != nil {
		eof := glwViewTokenAlloc(gr)
		eof.typ = tokenEnd
		eof.file = miscpkg.RstrDup(file)
		l.next = eof

		if glwViewPreproc(gr, sof, &ei, mayUnlock) == 0 &&
			glwViewParse(sof, &ei, gr) == 0 {
			gcv.gcvSof = sof
			gcv.gcvLoaded = 1
			return
		}
	}
	glwViewFreeChain(gr, sof)

	// C: bad: — A view is also "loaded" when there is an error
	gcv.gcvLoaded = 1
	gcv.gcvError = ei.error
	gcv.gcvErrorFile = ei.file
	gcv.gcvErrorLine = ei.line
}

// C: static void gvlr_destroy(glw_root_t *gr, glw_view_load_request_t *r)
// (glw_view.c:301-309)
func gvlrDestroy(gr *glwRoot, r *GlwViewLoadRequest) {
	miscpkg.RstrRelease(r.url)
	miscpkg.RstrRelease(r.alturl)
	glwUnref(r.w)
	glwScopeRelease(r.scope)
	gcvRelease(gr, r.gcv)
}

// C: static void *viewloader_thread(void *aux) (glw_view.c:315-339)
func viewloaderThread(aux any) any {
	gr := aux.(*glwRoot)

	glwLock(gr)

	for gr.grViewLoaderRun != 0 {
		r := gr.grViewLoadRequests.tqhFirst
		if r == nil {
			gr.grViewLoaderCond.Wait() // C: hts_cond_wait
			continue
		}

		gcv := r.gcv

		if gcv.gcvLoaded == 0 {
			gcvLoad(gr, gcv, 1)
		}

		// C: TAILQ_REMOVE(&gr->gr_view_load_requests, r, link);
		//    TAILQ_INSERT_TAIL(&gr->gr_view_eval_requests, r, link);
		if r.linkNext != nil {
			r.linkNext.linkPrev = r.linkPrev
		} else {
			gr.grViewLoadRequests.tqhLast = r.linkPrev
		}
		*r.linkPrev = r.linkNext
		r.linkPrev = gr.grViewEvalRequests.tqhLast
		r.linkNext = nil
		*r.linkPrev = r
		gr.grViewEvalRequests.tqhLast = &r.linkNext
	}

	glwUnlock(gr)
	return nil
}

// C: void glw_view_loader_eval(glw_root_t *gr) (glw_view.c:345-357)
func glwViewLoaderEval(gr *glwRoot) {
	for {
		r := gr.grViewEvalRequests.tqhFirst
		if r == nil {
			break
		}
		// C: TAILQ_REMOVE(&gr->gr_view_eval_requests, r, link)
		if r.linkNext != nil {
			r.linkNext.linkPrev = r.linkPrev
		} else {
			gr.grViewEvalRequests.tqhLast = r.linkPrev
		}
		*r.linkPrev = r.linkNext

		if r.w.glwFlags&glwDestroying == 0 {
			evalLoadedView(gr, r.gcv, (*glwView)(unsafe.Pointer(r.w)), r.scope)
		}
		gvlrDestroy(gr, r)
	}
}

// C: void glw_view_loader_flush(glw_root_t *gr) (glw_view.c:363-377)
func glwViewLoaderFlush(gr *glwRoot) {
	for {
		r := gr.grViewEvalRequests.tqhFirst
		if r == nil {
			break
		}
		if r.linkNext != nil {
			r.linkNext.linkPrev = r.linkPrev
		} else {
			gr.grViewEvalRequests.tqhLast = r.linkPrev
		}
		*r.linkPrev = r.linkNext
		gvlrDestroy(gr, r)
	}

	for {
		r := gr.grViewLoadRequests.tqhFirst
		if r == nil {
			break
		}
		if r.linkNext != nil {
			r.linkNext.linkPrev = r.linkPrev
		} else {
			gr.grViewLoadRequests.tqhLast = r.linkPrev
		}
		*r.linkPrev = r.linkNext
		gvlrDestroy(gr, r)
	}
}

// C: glw_t *glw_view_create(glw_root_t *gr, rstr_t *url, rstr_t *alturl,
// glw_t *parent, glw_scope_t *scope, rstr_t *file, int line)
// (glw_view.c:387-462)
func glwViewCreate(gr *glwRoot, url *miscpkg.Rstr, alturl *miscpkg.Rstr,
	parent *Glw, scope *glwScope, file *miscpkg.Rstr, line int) *Glw {

	if url == nil {
		url = alturl
		alturl = nil
	}

	w := glwCreate(gr, &glwViewClass, parent, nil, nil, scope, file, line)

	var gcv *GlwCachedView
	for g := gr.grViews.lhFirst; g != nil; g = g.gcvLinkNext {
		if miscpkg.RstrEq(g.gcvURL, url) != 0 && miscpkg.RstrEq(g.gcvAlturl, alturl) != 0 {
			gcv = g
			break
		}
	}

	if gcv == nil {
		gcv = &GlwCachedView{}
		gcv.gcvRefcount = 1
		gcv.gcvURL = miscpkg.RstrDup(url)
		gcv.gcvAlturl = miscpkg.RstrDup(alturl)
		// C: LIST_INSERT_HEAD(&gr->gr_views, gcv, gcv_link)
		gcv.gcvLinkNext = gr.grViews.lhFirst
		if gcv.gcvLinkNext != nil {
			gcv.gcvLinkNext.gcvLinkPrev = &gcv.gcvLinkNext
		}
		gr.grViews.lhFirst = gcv
		gcv.gcvLinkPrev = &gr.grViews.lhFirst
	}

	if gcv.gcvLoaded == 0 {
		const asyncLoad = 0 // C: !__native_client__
		if asyncLoad != 0 {
			r := &GlwViewLoadRequest{}
			w.glwRefcnt++
			gcv.gcvRefcount++

			r.url = miscpkg.RstrDup(url)
			r.alturl = miscpkg.RstrDup(alturl)
			r.w = w
			r.scope = glwScopeRetain(scope)
			r.gcv = gcv

			// C: TAILQ_INSERT_TAIL(&gr->gr_view_load_requests, r, link)
			r.linkPrev = gr.grViewLoadRequests.tqhLast
			r.linkNext = nil
			*r.linkPrev = r
			gr.grViewLoadRequests.tqhLast = &r.linkNext

			if gr.grViewLoaderRun == 0 {
				gr.grViewLoaderRun = 1
				gr.grViewLoaderThread = archpkg.ThreadCreateJoinable(
					"viewloader", viewloaderThread, gr, 0)
			} else {
				gr.grViewLoaderCond.Signal() // C: hts_cond_signal
			}
			return w
		}
		gcvLoad(gr, gcv, 0)
	} else {
		// C: LIST_REMOVE(gcv, gcv_link)
		if gcv.gcvLinkNext != nil {
			gcv.gcvLinkNext.gcvLinkPrev = gcv.gcvLinkPrev
		}
		*gcv.gcvLinkPrev = gcv.gcvLinkNext
		// C: LIST_INSERT_HEAD(&gr->gr_views, gcv, gcv_link)
		gcv.gcvLinkNext = gr.grViews.lhFirst
		if gcv.gcvLinkNext != nil {
			gcv.gcvLinkNext.gcvLinkPrev = &gcv.gcvLinkNext
		}
		gr.grViews.lhFirst = gcv
		gcv.gcvLinkPrev = &gr.grViews.lhFirst
	}

	evalLoadedView(gr, gcv, (*glwView)(unsafe.Pointer(w)), scope)
	return w
}

// C: void glw_view_cache_flush(glw_root_t *gr) (glw_view.c:468-476)
func glwViewCacheFlush(gr *glwRoot) {
	for {
		gcv := gr.grViews.lhFirst
		if gcv == nil {
			break
		}
		if gcv.gcvLinkNext != nil {
			gcv.gcvLinkNext.gcvLinkPrev = gcv.gcvLinkPrev
		}
		*gcv.gcvLinkPrev = gcv.gcvLinkNext
		gcvRelease(gr, gcv)
	}
}
