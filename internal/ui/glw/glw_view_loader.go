package glw

// Canonical port of src/ui/glw/glw_view_loader.c — the `loader` widget class.
//
// A loader instantiates a .view file (via glw_view_create) as its children and
// animates child swap in/out using a transition effect; each child carries
// per-child parent data (glw_loader_item_t) holding the current/target
// transition position.

import (
	"unsafe"

	miscpkg "github.com/czz/movian-go/internal/misc"

	propcore "github.com/czz/movian-go/internal/prop"
	tracepkg "github.com/czz/movian-go/internal/trace"
)

// C: glw_view_loader_t
type glwViewLoader struct {
	w Glw

	scope *glwScope

	url    *miscpkg.Rstr
	altUrl *miscpkg.Rstr

	delta float32
	time  float32

	efxConf glwTransitionType
	loaded  bool // C: char loaded
}

// C: glw_loader_item_t — per-child parent data (gc_parent_data_size)
type glwLoaderItem struct {
	vlCur float32
	vlTgt float32
}

// C: itemdata(w) → glw_parent_data(w, glw_loader_item_t)
func loaderItemData(w *Glw) *glwLoaderItem {
	return w.glwParentData.(*glwLoaderItem)
}

// C: glw_loader_layout
func glwLoaderLayout(w *Glw, rc *glwRctx) {
	a := (*glwViewLoader)(unsafe.Pointer(w))
	gr := w.glwRoot

	a.delta = 1 / (a.time * float32(1000000/gr.grFrameduration))

	for c := w.glwChilds.tqhFirst; c != nil; {
		n := c.glwParentLinkNext
		id := loaderItemData(c)

		nv := glwMin(id.vlCur+a.delta, id.vlTgt)
		if nv != id.vlCur {
			glwNeedRefresh(gr, 0)
		}
		id.vlCur = nv

		if id.vlCur == 1 {
			glwDestroy(c)
			if c = w.glwChilds.tqhFirst; c != nil {
				glwCopyConstraints(w, c)
			}
		} else {
			glwLayout0(c, rc)
		}
		c = n
	}
}

// C: unload_because_inactive
func loaderUnloadBecauseInactive(w *Glw) {
	for c := w.glwChilds.tqhFirst; c != nil; {
		next := c.glwParentLinkNext
		if loaderItemData(c).vlTgt > 0 {
			glwDestroy(c)
		}
		c = next
	}
}

// C: glw_loader_callback
func glwLoaderCallback(w *Glw, opaque any, signal glwSignal, extra any) int {
	switch signal {
	case glwSignalChildCreated:
		c := extra.(*Glw)
		if w.glwChilds.tqhFirst == c && c.glwParentLinkNext == nil &&
			w.glwFlags2&glw2NoInitialTrans != 0 {
			loaderItemData(c).vlCur = 0
		} else {
			loaderItemData(c).vlCur = -1
		}
		loaderItemData(c).vlTgt = 0

		glwFocusOpenPathCloseAllOther(c)

		for n := w.glwChilds.tqhFirst; n != nil; n = n.glwParentLinkNext {
			if c == n {
				continue
			}
			loaderItemData(n).vlTgt = 1
		}

		if c == w.glwChilds.tqhFirst {
			glwCopyConstraints(w, c)
		}

	case glwSignalChildConstraintsChanged:
		c := extra.(*Glw)
		if c == w.glwChilds.tqhFirst {
			glwCopyConstraints(w, c)
		}
		return 1

	case glwSignalInactive:
		loaderUnloadBecauseInactive(w)
		return 0
	}
	return 0
}

// C: glw_view_loader_render
func glwViewLoaderRender(w *Glw, rc *glwRctx) {
	alpha := rc.rcAlpha * w.glwAlpha
	sharpness := rc.rcSharpness * w.glwSharpness
	a := (*glwViewLoader)(unsafe.Pointer(w))
	var rc0 glwRctx

	for c := w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		rc0 = *rc
		if loaderItemData(c).vlCur == 0 {
			rc0.rcAlpha = alpha
			rc0.rcSharpness = sharpness
			glwRender0(c, &rc0)
			continue
		}
		glwTransitionRender(a.efxConf, loaderItemData(c).vlCur, alpha, &rc0)
		glwRender0(c, &rc0)
	}
}

// C: glw_view_loader_retire_child
func glwViewLoaderRetireChild(w *Glw, c *Glw) {
	glwSuspendSubscriptions(c)
	loaderItemData(c).vlTgt = 1
}

// C: glw_view_loader_ctor
func glwViewLoaderCtor(w *Glw) {
	vl := (*glwViewLoader)(unsafe.Pointer(w))
	vl.time = 0.00001
	vl.scope = glwScopeRetain(w.glwScope)
	w.glwFlags2 |= glw2ExpediteSubscriptions
}

// C: glw_view_loader_dtor
func glwViewLoaderDtor(w *Glw) {
	vl := (*glwViewLoader)(unsafe.Pointer(w))
	glwScopeRelease(vl.scope)
	miscpkg.RstrRelease(vl.url)
	miscpkg.RstrRelease(vl.altUrl)
}

// C: update_autohide
func loaderUpdateAutohide(l *glwViewLoader) {
	if l.w.glwFlags2&glw2Autohide == 0 {
		return
	}
	if l.loaded {
		glwUnhide(&l.w)
	} else {
		glwHide(&l.w)
		for c := l.w.glwChilds.tqhFirst; c != nil; {
			next := c.glwParentLinkNext
			glwDestroy(c)
			c = next
		}
	}
}

// C: set_source
func glwViewLoaderSetSource(w *Glw, url *miscpkg.Rstr, flags int, origin *GlwStyle) {
	a := (*glwViewLoader)(unsafe.Pointer(w))
	if w.glwFlags2&glw2Debug != 0 {
		us := "(void)"
		if url != nil {
			us = miscpkg.RstrGet(url)
		}
		glwDeps.ts.Trace(tracepkg.TRACE_DEBUG, "GLW", "%s: Loader loading %s",
			miscpkg.RstrGet(w.glwIdRstr), us)
	}

	us, as := "", ""
	if url != nil {
		us = miscpkg.RstrGet(url)
	}
	if a.url != nil {
		as = miscpkg.RstrGet(a.url)
	}
	if us == as {
		return
	}

	if w.glwFlags&glwActive == 0 {
		for c := w.glwChilds.tqhFirst; c != nil; {
			next := c.glwParentLinkNext
			glwDestroy(c)
			c = next
		}
	}

	miscpkg.RstrRelease(a.url)
	a.url = miscpkg.RstrDup(url)

	for c := w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		glwSuspendSubscriptions(c)
	}

	if url != nil && miscpkg.RstrGet(url) == "" {
		url = nil
	}

	altUrl := a.altUrl
	if altUrl != nil && miscpkg.RstrGet(altUrl) == "" {
		altUrl = nil
	}

	if url != nil || altUrl != nil {
		glwViewCreate(w.glwRoot, url, altUrl, w, a.scope, nil, 0)
		a.loaded = true
		loaderUpdateAutohide(a)
		return
	}

	a.loaded = false
	loaderUpdateAutohide(a)

	for c := w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		loaderItemData(c).vlTgt = 1
	}
}

// C: set_alt
func glwViewLoaderSetAlt(w *Glw, url *miscpkg.Rstr) {
	a := (*glwViewLoader)(unsafe.Pointer(w))
	miscpkg.RstrSet(&a.altUrl, url)
}

// C: glw_view_loader_set_int
func glwViewLoaderSetInt(w *Glw, attrib glwAttribute, value int, origin *GlwStyle) int {
	vl := (*glwViewLoader)(unsafe.Pointer(w))
	switch attrib {
	case glwAttribTransitionEffect:
		if vl.efxConf == glwTransitionType(value) {
			return 0
		}
		vl.efxConf = glwTransitionType(value)
	default:
		return -1
	}
	return 1
}

// C: glw_view_loader_set_float
func glwViewLoaderSetFloat(w *Glw, attrib glwAttribute, value float32, origin *GlwStyle) int {
	vl := (*glwViewLoader)(unsafe.Pointer(w))
	switch attrib {
	case glwAttribTime:
		if value < 0.00001 {
			value = 0.00001
		}
		if vl.time == value {
			return 0
		}
		vl.time = value
	default:
		return -1
	}
	return 1
}

// C: glw_view_loader_set_prop
func glwViewLoaderSetProp(w *Glw, attrib glwAttribute, p *propcore.Prop) int {
	vl := (*glwViewLoader)(unsafe.Pointer(w))
	var scope *glwScope

	switch attrib {
	case glwAttribArgs:
		scope = glwScopeDup(vl.scope, 1<<glwRootArgs)
		scope.gsRoots[glwRootArgs].P = glwDeps.pm.RefInc(p)
		glwScopeRelease(vl.scope)
		vl.scope = scope
		return 0

	case glwAttribPropSelf:
		scope = glwScopeDup(vl.scope, 1<<glwRootSelf)
		scope.gsRoots[glwRootSelf].P = glwDeps.pm.RefInc(p)
		glwScopeRelease(vl.scope)
		vl.scope = scope
		return 0

	default:
		return -1
	}
}

// C: get_identity
func glwViewLoaderGetIdentity(w *Glw, tmp []byte) string {
	l := (*glwViewLoader)(unsafe.Pointer(w))
	if l.url == nil {
		return "NULL"
	}
	return miscpkg.RstrGet(l.url)
}

// C: mod_flags2
func glwViewLoaderModFlags2(w *Glw, set, clr int) {
	gvl := (*glwViewLoader)(unsafe.Pointer(w))
	if set&glw2Autohide != 0 && !gvl.loaded {
		glwHide(w)
	}
	if clr&glw2Autohide != 0 {
		glwUnhide(w)
	}
}

// C: glw_class_t glw_view_loader
var glwViewLoaderClass = &glwClass{
	gcName:           "loader",
	gcInstanceSize:   int(unsafe.Sizeof(glwViewLoader{})),
	gcParentDataSize: int(unsafe.Sizeof(glwLoaderItem{})),
	gcNewParentData:  func() any { return &glwLoaderItem{} },
	gcNew: func(parent *Glw) *Glw {
		vl := &glwViewLoader{}
		return &vl.w
	},
	gcCtor:          glwViewLoaderCtor,
	gcDtor:          glwViewLoaderDtor,
	gcSetInt:        glwViewLoaderSetInt,
	gcSetFloat:      glwViewLoaderSetFloat,
	gcSetProp:       glwViewLoaderSetProp,
	gcLayout:        glwLoaderLayout,
	gcRender:        glwViewLoaderRender,
	gcRetireChild:   glwViewLoaderRetireChild,
	gcSignalHandler: glwLoaderCallback,
	gcSetSource:     glwViewLoaderSetSource,
	gcGetIdentity:   glwViewLoaderGetIdentity,
	gcSetAlt:        glwViewLoaderSetAlt,
	gcModFlags2:     glwViewLoaderModFlags2,
}

func registerViewLoader() {
	glwRegisterClass(glwViewLoaderClass)
}
