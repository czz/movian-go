package glw

// C: src/ui/glw/glw.c — canonical 1:1 port.
//
// Global singletons used by C (event_mgr, the prop context, gconf.*,
// runcontrol) map to package-level seams configured at init time, matching
// the established convention (GconfClipboardSet, EsSetPropDeps, ...).

import (
	"fmt"
	"math"
	"os"
	"strings"
	"unsafe"

	imagepkg "github.com/czz/movian-go/internal/image"
	miscpkg "github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
)

// C: void glw_render0 (glw.c:492-537)
func glwRender0(w *Glw, rc *glwRctx) {
	if w.glwZoffset != 0 {
		rc0 := *rc
		zmax := 0
		rc0.rcZmax = &zmax
		rc0.rcZindex = w.glwZoffset

		if w.glwFlags&glwHaveMargins != 0 {
			glwReposition(&rc0,
				int(w.glwMargin[0]),
				int(rc.rcHeight)-int(w.glwMargin[1]),
				int(rc.rcWidth)-int(w.glwMargin[2]),
				int(w.glwMargin[3]))
			if rc0.rcWidth < 1 || rc0.rcHeight < 1 {
				return
			}
		}
		w.glwClass.gcRender(w, &rc0)

	} else if w.glwFlags&glwHaveMargins != 0 {
		rc0 := *rc
		glwReposition(&rc0,
			int(w.glwMargin[0]),
			int(rc.rcHeight)-int(w.glwMargin[1]),
			int(rc.rcWidth)-int(w.glwMargin[2]),
			int(w.glwMargin[3]))
		if rc0.rcWidth < 1 || rc0.rcHeight < 1 {
			return
		}
		w.glwClass.gcRender(w, &rc0)
	} else {
		w.glwClass.gcRender(w, rc)
	}

	if w.glwFlags2&glw2Debug != 0 {
		glwWirebox(w.glwRoot, rc)
	}
}

// C: glw_t *glw_create (glw.c:543-590)
func glwCreate(gr *glwRoot, class *glwClass, parent *Glw, before *Glw,
	originator *propcore.Prop, scope *glwScope, file *miscpkg.Rstr, line int) *Glw {

	// C: w = calloc(1, class->gc_instance_size +
	//      (parent ? parent->glw_class->gc_parent_data_size : 0))
	//
	// Every C class struct embeds `glw_t w` as its first member and
	// glw_create() allocates gc_instance_size bytes; Go reproduces the same
	// layout by allocating the class struct (which embeds Glw first) and
	// returning &inst.w — class code recovers the subtype via
	// (*glwX)(unsafe.Pointer(w)) exactly as C casts.
	w := class.gcNew(parent)

	w.glwRoot = gr
	w.glwClass = class
	w.glwAlpha = 1.0
	w.glwSharpness = 1.0
	w.glwRefcnt = 1
	w.glwAlignment = uint8(class.gcDefaultAlignment)
	w.glwFlags2 = glw2Enabled | glw2NavFocusable | glw2Cursor
	w.glwFile = miscpkg.RstrDup(file)
	w.glwLine = line

	if parent != nil { // C: likely
		w.glwStyles = glwStylesetRetain(parent.glwStyles)
	}

	// C: LIST_INSERT_HEAD(&gr->gr_active_dummy_list, w, glw_active_link)
	w.glwActiveLinkNext = gr.grActiveDummyList.lhFirst
	if w.glwActiveLinkNext != nil {
		w.glwActiveLinkNext.glwActiveLinkPrev = &w.glwActiveLinkNext
	}
	gr.grActiveDummyList.lhFirst = w
	w.glwActiveLinkPrev = &gr.grActiveDummyList.lhFirst

	if class.gcNewframe != nil {
		// C: LIST_INSERT_HEAD(&gr->gr_every_frame_list, w, glw_every_frame_link)
		w.glwEveryFrameLinkNext = gr.grEveryFrameList.lhFirst
		if w.glwEveryFrameLinkNext != nil {
			w.glwEveryFrameLinkNext.glwEveryFrameLinkPrev = &w.glwEveryFrameLinkNext
		}
		gr.grEveryFrameList.lhFirst = w
		w.glwEveryFrameLinkPrev = &gr.grEveryFrameList.lhFirst
	}

	// C: TAILQ_INIT(&w->glw_childs)
	w.glwChilds.tqhFirst = nil
	w.glwChilds.tqhLast = &w.glwChilds.tqhFirst

	w.glwOriginatingProp = glwDeps.pm.RefInc(originator)

	w.glwParent = parent
	if parent != nil {
		// C: trailing parent-data bytes in the child's calloc block
		if parent.glwClass.gcNewParentData != nil {
			w.glwParentData = parent.glwClass.gcNewParentData()
		}
		updateInPath(w)

		if before != nil {
			// C: TAILQ_INSERT_BEFORE(before, w, glw_parent_link)
			w.glwParentLinkPrev = before.glwParentLinkPrev
			w.glwParentLinkNext = before
			*before.glwParentLinkPrev = w
			before.glwParentLinkPrev = &w.glwParentLinkNext
		} else {
			// C: TAILQ_INSERT_TAIL(&parent->glw_childs, w, glw_parent_link)
			w.glwParentLinkNext = nil
			w.glwParentLinkPrev = parent.glwChilds.tqhLast
			*parent.glwChilds.tqhLast = w
			parent.glwChilds.tqhLast = &w.glwParentLinkNext
		}

		glwSignal0(parent, glwSignalChildCreated, w)
	}

	w.glwScope = glwScopeRetain(scope)

	if class.gcCtor != nil {
		class.gcCtor(w)
	}

	return w
}

// C: void glw_unref (glw.c:779-790)
func glwUnref(w *Glw) {
	if w.glwRefcnt > 1 {
		w.glwRefcnt--
		return
	}
	// C: assert(w->glw_clone == NULL)
	miscpkg.RstrRelease(w.glwFile)
	glwScopeRelease(w.glwScope)
	// C: free(w) — GC
}

// C: void glw_remove_from_parent (glw.c:796-812)
func glwRemoveFromParent(w *Glw, p *Glw) {
	// C: assert(w->glw_parent == p)
	glwFocusLeave(w)

	if p.glwFocused == w {
		p.glwFocused = nil
	}

	// C: assert(w->glw_root->gr_current_focus != w)

	if p.glwSelected == w {
		p.glwSelected = w.glwParentLinkNext // C: TAILQ_NEXT
	}

	// C: TAILQ_REMOVE(&p->glw_childs, w, glw_parent_link)
	if w.glwParentLinkNext != nil {
		w.glwParentLinkNext.glwParentLinkPrev = w.glwParentLinkPrev
	} else {
		p.glwChilds.tqhLast = w.glwParentLinkPrev
	}
	*w.glwParentLinkPrev = w.glwParentLinkNext
	w.glwParent = nil
}

// C: void glw_suspend_subscriptions (glw.c:818-826)
func glwSuspendSubscriptions(w *Glw) {
	glwPropSubscriptionSuspendList(&w.glwPropSubscriptions)

	for c := w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		glwSuspendSubscriptions(c)
	}
}

// C: void glw_destroy (glw.c:832-894)
func glwDestroy(w *Glw) {
	gr := w.glwRoot

	if gr.grLastFocus == w {
		gr.grLastFocus = nil
	}

	w.glwFlags |= glwDestroying

	if w.glwOriginatingProp != nil {
		glwDeps.pm.RefDec(w.glwOriginatingProp)
	}

	if gr.grPointerGrab == w {
		gr.grPointerGrab = nil
	}

	if gr.grPointerHover == w {
		glwRootSetHover(gr, nil)
	}

	if gr.grPointerPress == w {
		gr.grPointerPress = nil
	}

	glwPropSubscriptionDestroyList(gr, &w.glwPropSubscriptions)

	for {
		gem := w.glwEventMaps.lhFirst
		if gem == nil {
			break
		}
		// C: LIST_REMOVE(gem, gem_link)
		if gem.gemLinkNext != nil {
			gem.gemLinkNext.gemLinkPrev = gem.gemLinkPrev
		}
		*gem.gemLinkPrev = gem.gemLinkNext
		glwEventMapDestroy(gr, gem)
	}

	w.glwMatrix = nil // C: free(w->glw_matrix)

	if w.glwClass.gcNewframe != nil {
		// C: LIST_REMOVE(w, glw_every_frame_link)
		if w.glwEveryFrameLinkNext != nil {
			w.glwEveryFrameLinkNext.glwEveryFrameLinkPrev = w.glwEveryFrameLinkPrev
		}
		*w.glwEveryFrameLinkPrev = w.glwEveryFrameLinkNext
	}

	// C: LIST_REMOVE(w, glw_active_link)
	if w.glwActiveLinkNext != nil {
		w.glwActiveLinkNext.glwActiveLinkPrev = w.glwActiveLinkPrev
	}
	*w.glwActiveLinkPrev = w.glwActiveLinkNext

	for {
		c := w.glwChilds.tqhFirst
		if c == nil {
			break
		}
		glwDestroy(c)
	}

	glwSignal0(w, glwSignalDestroy, nil)

	if p := w.glwParent; p != nil {
		// Some classes needs to do some stuff is a child is destroyed

		if p.glwFlags&glwDestroying == 0 {
			glwSignal0(p, glwSignalChildDestroyed, w)
		}

		glwRemoveFromParent(w, p)
	}

	glwStyleUnbindAll(w)

	glwStylesetRelease(w.glwStyles)

	miscpkg.RstrRelease(w.glwIdRstr)

	// C: TAILQ_INSERT_TAIL(&gr->gr_destroyer_queue, w, glw_parent_link)
	w.glwParentLinkNext = nil
	w.glwParentLinkPrev = gr.grDestroyerQueue.tqhLast
	*gr.grDestroyerQueue.tqhLast = w
	gr.grDestroyerQueue.tqhLast = &w.glwParentLinkNext

	glwViewFreeChain(gr, w.glwDynamicExpressions)
}

// C: void glw_destroy_childs (glw.c:900-906)
func glwDestroyChilds(w *Glw) {
	for {
		c := w.glwChilds.tqhFirst
		if c == nil {
			break
		}
		glwDestroy(c)
	}
}

// C: glw_t *glw_get_prev_n (glw.c:998-1013)
func glwGetPrevN(c *Glw, count int) *Glw {
	t := c
	c = nil
	for i := 0; i < count; i++ {
		if t = glwTAILQPrev(t); t == nil {
			break
		}
		if t.glwFlags&glwHidden != 0 {
			i--
		} else {
			c = t
		}
	}
	return c
}

// C: glw_t *glw_get_next_n (glw.c:1019-1036)
func glwGetNextN(c *Glw, count int) *Glw {
	t := c
	c = nil
	for i := 0; i < count; i++ {
		if t = t.glwParentLinkNext; t == nil {
			break
		}
		if t.glwFlags&glwHidden != 0 {
			i--
		} else {
			c = t
		}
	}
	return c
}

// C: static LIST_HEAD(, glw_gf_ctrl) ggcs (glw.c:1043)
var ggcs glwGfCtrlList

// C: void glw_gf_register (glw.c:1045-1049)
func glwGfRegister(ggc *glwGfCtrl) {
	ggc.linkNext = ggcs.lhFirst
	if ggc.linkNext != nil {
		ggc.linkNext.linkPrev = &ggc.linkNext
	}
	ggcs.lhFirst = ggc
	ggc.linkPrev = &ggcs.lhFirst
}

// C: void glw_gf_unregister (glw.c:1051-1055)
func glwGfUnregister(ggc *glwGfCtrl) {
	if ggc.linkNext != nil {
		ggc.linkNext.linkPrev = ggc.linkPrev
	}
	*ggc.linkPrev = ggc.linkNext
}

// C: void glw_gf_do (glw.c:1057-1063)
func glwGfDo() {
	for ggc := ggcs.lhFirst; ggc != nil; ggc = ggc.linkNext {
		ggc.flush(ggc.opaque)
	}
}

// C: void glw_retire_child (glw.c:1083-1092)
func glwRetireChild(w *Glw) {
	p := w.glwParent
	if p != nil && p.glwClass.gcRetireChild != nil {
		p.glwClass.gcRetireChild(p, w)
		return
	}
	glwDestroy(w)
}

// C: static void glw_screenshot (glw.c:2375-2385)
func glwScreenshot(gr *glwRoot) {
	if gr.grBrReadPixels == nil {
		glwDeps.screenshotDeliver(nil)
		return
	}

	pm := gr.grBrReadPixels(gr)
	glwDeps.screenshotDeliver(pm)
	if pm != nil {
		imagepkg.PixmapRelease(pm)
	}
}

// screenshotDeliver — C: screenshot_deliver (src/api/screenshot.c:285).
// Wired at init to the screenshot HTTP handler (pixmap → image adapter).
// GlwSetScreenshotDeliver wires the C screenshot_deliver() seam
// (src/api/screenshot.c:285 — pixmap → image adapter).
func GlwSetScreenshotDeliver(fn func(pm *imagepkg.Pixmap)) {
	glwDeps.screenshotDeliver = fn
}

// C: static void glw_get_path_r (glw.c:2846-2867)
func glwGetPathR(buf *strings.Builder, w *Glw) {
	if w.glwParent != nil {
		glwGetPathR(buf, w.glwParent)
	}
	var ident string
	if w.glwClass.gcGetIdentity != nil {
		var tmp [32]byte
		ident = w.glwClass.gcGetIdentity(w, tmp[:])
	}

	if ident == "" {
		ident = miscpkg.RstrGet(w.glwIdRstr)
	}

	sep := ""
	if buf.Len() > 0 {
		sep = "."
	}
	fb, fd, fh := "", "", ""
	if w.glwFlags&glwFocusBlocked != 0 {
		fb = "<B>"
	}
	if w.glwFlags&glwDestroying != 0 {
		fd = "<D>"
	}
	if w.glwFlags&glwHidden != 0 {
		fh = "<H>"
	}
	o, c := "", ""
	if ident != "" {
		o, c = "(", ")"
	}
	fmt.Fprintf(buf, "%s[%s%s%s%s%s%s%s@%s:%d]",
		sep, w.glwClass.gcName, fb, fd, fh, o, ident, c,
		miscpkg.RstrGet(w.glwFile), w.glwLine)
}

// C: const char *glw_get_path (glw.c:2873-2882)
func glwGetPath(w *Glw) string {
	if w == nil {
		return "<null>"
	}

	var buf strings.Builder
	glwGetPathR(&buf, w)
	return buf.String()
}

// C: void glw_set_fullscreen (glw.c:2888-2892)
func glwSetFullscreen(gr *glwRoot, fullscreen int) {
	gr.grIsFullscreen = fullscreen
}

// C: static void glw_print_tree0 (glw.c:2898-2912)
func glwPrintTree0(w *Glw, indent int) {
	text := ""
	if w.glwClass.gcGetText != nil {
		text = w.glwClass.gcGetText(w)
	}
	hidden := ""
	if w.glwFlags&glwHidden != 0 {
		hidden = " <hidden>"
	}
	fmt.Fprintf(os.Stderr, "%*s%p %s: %s [%08x] %s\n",
		indent, "",
		w,
		w.glwClass.gcName,
		text,
		w.glwFlags,
		hidden)

	for c := w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		glwPrintTree0(c, indent+2)
	}
}

// C: void glw_print_tree (glw.c:2918-2922)
func glwPrintTree(w *Glw) {
	glwPrintTree0(w, 0)
}

// C: glw_t *glw_next_widget (glw.c:2928-2933)
func glwNextWidget(w *Glw) *Glw {
	for {
		w = w.glwParentLinkNext
		if w == nil || w.glwFlags&glwHidden == 0 {
			break
		}
	}
	return w
}

// C: glw_t *glw_prev_widget (glw.c:2939-2944)
func glwPrevWidget(w *Glw) *Glw {
	for {
		w = glwTAILQPrev(w)
		if w == nil || w.glwFlags&glwHidden == 0 {
			break
		}
	}
	return w
}

// C: glw_t *glw_first_widget (glw.c:2952-2960)
func glwFirstWidget(w *Glw) *Glw {
	c := w.glwChilds.tqhFirst

	for c != nil && c.glwFlags&glwHidden != 0 {
		c = c.glwParentLinkNext
	}
	return c
}

// C: glw_t *glw_last_widget (glw.c:2966-2974)
func glwLastWidget(w *Glw) *Glw {
	c := glwTAILQPrevSlot(w.glwChilds.tqhLast)

	for c != nil && c.glwFlags&glwHidden != 0 {
		c = glwTAILQPrev(c)
	}
	return c
}

// C: void glw_hide (glw.c:2977-2989)
func glwHide(w *Glw) {
	if w.glwFlags&glwHidden != 0 {
		return
	}
	w.glwFlags |= glwHidden

	if w.glwParent == nil { // C: unlikely
		return // For style widgets
	}

	glwSignal0(w.glwParent, glwSignalChildHidden, w)

	if glwIsFocused(w) {
		glwFocusCrawl(w, 1, 0)
	}
}

// C: void glw_unhide (glw.c:2992-3002)
func glwUnhide(w *Glw) {
	if w.glwFlags&glwHidden == 0 {
		return
	}
	w.glwFlags &^= glwHidden

	if w.glwParent == nil {
		return // For style widgets
	}

	glwSignal0(w.glwParent, glwSignalChildUnhidden, w)
}

// C: void glw_mod_flags2 (glw.c:3008-3037)
func glwModFlags2(w *Glw, set int, clr int) {
	gc := w.glwClass

	set &^= w.glwFlags2
	w.glwFlags2 |= set

	clr &= w.glwFlags2
	w.glwFlags2 &^= clr

	if w.glwParent != nil {
		p := w.glwParent
		if set&glw2FhpSpill != 0 {
			p.glwFlags |= glwFhpSpillToChilds
		}

		if clr&glw2FhpSpill != 0 {
			p.glwFlags &^= glwFhpSpillToChilds
			for c := p.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
				if c.glwFlags2&glw2FhpSpill != 0 {
					p.glwFlags |= glwFhpSpillToChilds
					break
				}
			}
		}
	}

	if (set|clr) != 0 && gc.gcModFlags2 != nil {
		gc.gcModFlags2(w, set, clr)
	}
}

// C: void glw_store_matrix (glw.c:3043-3060)
func glwStoreMatrix(w *Glw, rc *glwRctx) {
	if rc.rcInhibitMatrixStore != 0 {
		return
	}

	if w.glwMatrix == nil {
		w.glwMatrix = &Mtx{}
	}

	*w.glwMatrix = rc.rcMtx

	if w.glwFlags&(glwInFocusPath|glwInHoverPath) == 0 { // C: likely
		return
	}

	if w.glwRoot.grCursorFocusTracker != nil {
		w.glwRoot.grCursorFocusTracker(w, rc,
			w.glwRoot.grCurrentCursor)
	}
}

// C: void glw_rctx_init (glw.c:3066-3077)
func glwRctxSetup(rc *glwRctx, width, height int, overscan int, zmax *int) {
	*rc = glwRctx{}
	rc.rcWidth = int16(width)
	rc.rcHeight = int16(height)
	rc.rcAlpha = 1.0
	rc.rcSharpness = 1.0
	rc.rcOverscanning = uint8(overscan)
	rc.rcZmax = zmax
	glwLoadIdentity(rc)
	glwTranslatef(rc, 0, 0, float32(-1/math.Tan(45*math.Pi/360)))
}

// glwWidgetUnproject — C: glw_widget_unproject (glw.c:3087-3133)
// m   Model matrix
// x   Return x in model space
// y   Return y in model space
// p   Mouse pointer at camera z plane
// dir Mouse pointer direction vector
func glwWidgetUnproject(m *Mtx, xp *float32, yp *float32,
	p Vec3, dir Vec3) int {
	var u, v, n, w0, T0, T1, T2, out, I Vec3
	var inv Mtx

	var tm PMtx
	glwPmtxMulPrepare(&tm, m)

	glwPmtxMulVec3(&T0, &tm, &Vec3{-1, -1, 0})
	glwPmtxMulVec3(&T1, &tm, &Vec3{1, -1, 0})
	glwPmtxMulVec3(&T2, &tm, &Vec3{1, 1, 0})

	glwVec3Sub(&u, &T1, &T0)
	glwVec3Sub(&v, &T2, &T0)
	glwVec3Cross(&n, &u, &v)

	glwVec3Sub(&w0, &p, &T0)
	b := glwVec3Dot(&n, &dir)

	if math.Abs(float64(b)) < 0.000001 {
		return 0
	}

	glwVec3Addmul(&I, &p, &dir, -glwVec3Dot(&n, &w0)/b)

	if glwMtxInvert(&inv, m) == 0 {
		return 0
	}

	glwPmtxMulPrepare(&tm, &inv)
	glwPmtxMulVec3(&out, &tm, &I)

	*xp = out[0]
	*yp = out[1]
	return 1
}

// C: static void glw_register_activity (glw.c:3275-3280)
func glwRegisterActivity(gr *glwRoot) {
	gr.grScreensaverResetAt = gr.grFrameStart
	gr.grLastActivityAt = gr.grFrameStart
}

// C: const static float projection[16] (glw.c:3283-3288)
var glwProjection = [16]float32{
	2.414213, 0.000000, 0.000000, 0.000000,
	0.000000, 2.414213, 0.000000, 0.000000,
	0.000000, 0.000000, 1.033898, -1.000000,
	0.000000, 0.000000, 2.033898, 0.000000,
}

// C: void glw_project_matrix (glw.c:3294-3326)
func glwProjectMatrix(r *glwRect, m *Mtx, gr *glwRoot) {
	var tmp Mtx

	var tm, tp PMtx
	var T0, T1 Vec4
	var V0, V1 Vec4

	glwPmtxMulPrepare(&tm, m)
	glwPmtxMulVec4(&T0, &tm, &Vec4{-1, 1, 0, 1})
	glwPmtxMulVec4(&T1, &tm, &Vec4{1, -1, 0, 1})

	copy(tmp.r[:], (*[4]Vec4)(unsafe.Pointer(&glwProjection))[:])
	glwPmtxMulPrepare(&tp, &tmp)

	glwPmtxMulVec4(&V0, &tp, &T0)
	glwPmtxMulVec4(&V1, &tp, &T1)

	var w float32

	w = V0[3]

	r.x1 = int(math.Round(float64((1.0 + (V0[0] / w)) * float32(gr.grWidth) / 2.0)))
	r.y1 = int(math.Round(float64((1.0 - (V0[1] / w)) * float32(gr.grHeight) / 2.0)))

	w = V1[3]

	r.x2 = int(math.Round(float64((1.0 + (V1[0] / w)) * float32(gr.grWidth) / 2.0)))
	r.y2 = int(math.Round(float64((1.0 - (V1[1] / w)) * float32(gr.grHeight) / 2.0)))
}

// C: void glw_project (glw.c:3332-3336)
func glwProject(r *glwRect, rc *glwRctx, gr *glwRoot) {
	glwProjectMatrix(r, &rc.rcMtx, gr)
}

// C: void glw_lp (glw.c:3342-3357)
func glwLp(v *float32, gr *glwRoot, target float32, alpha float32) {
	in := *v

	x := int(in * 1000.0)
	out := in + alpha*(target-in)
	y := int(out * 1000.0)

	if x == y {
		*v = target
		return
	}
	*v = out
	glwNeedRefresh(gr, 0)
}

// C: int glw_attrib_set_float3_clamped (glw.c:3363-3369)
func glwAttribSetFloat3Clamped(dst *[3]float32, src *[3]float32) int {
	var v [3]float32
	for i := range 3 {
		v[i] = glwClamp(src[i], 0.0, 1.0)
	}
	return glwAttribSetFloat3(dst, &v)
}

// C: int glw_attrib_set_float3 (glw.c:3371-3377)
func glwAttribSetFloat3(dst *[3]float32, src *[3]float32) int {
	if *dst == *src {
		return 0
	}
	*dst = *src
	return 1
}

// C: int glw_attrib_set_float4 (glw.c:3379-3385)
func glwAttribSetFloat4(dst *[4]float32, src *[4]float32) int {
	if *dst == *src {
		return 0
	}
	*dst = *src
	return 1
}

// C: int glw_attrib_set_rgb (glw.c:3388-3391)
func glwAttribSetRgb(rgb *glwRgb, src *[3]float32) int {
	return glwAttribSetFloat3((*[3]float32)(unsafe.Pointer(rgb)), src)
}

// C: int glw_attrib_set_int16_4 (glw.c:3394-3400)
func glwAttribSetInt164(dst *[4]int16, src *[4]int16) int {
	if *dst == *src {
		return 0
	}
	*dst = *src
	return 1
}

// glwAttribSetInt16_4 — C: glw_attrib_set_int16_4 (glw.c:3395).
func glwAttribSetInt16_4(dst []int16, src []int16) int {
	if dst[0] == src[0] && dst[1] == src[1] && dst[2] == src[2] && dst[3] == src[3] {
		return 0
	}
	copy(dst, src[:4])
	return 1
}
