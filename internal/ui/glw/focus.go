package glw

// C: src/ui/glw/glw.c — canonical 1:1 port.
//
// Global singletons used by C (event_mgr, the prop context, gconf.*,
// runcontrol) map to package-level seams configured at init time, matching
// the established convention (GconfClipboardSet, EsSetPropDeps, ...).

import (
	eventpkg "github.com/czz/movian-go/internal/event"
	miscpkg "github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
)

// C: static void glw_path_flood (glw.c:1156-1168)
func glwPathFlood(w *Glw, or int, and int) {
	for c := w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		if c.glwFlags2&glw2Clickable != 0 ||
			c.glwFocusWeight > 0 {
			continue
		}
		glwPathFlood(c, or, and)
		glwFhpUpdate(c, or, and)
	}
}

// C: void glw_path_modify (glw.c:1174-1203)
func glwPathModify(w *Glw, set int, clr int, stop *Glw) {
	clr = ^clr // Invert so we can just AND it

	glwPathFlood(w, set, clr)

	for ; w != nil; w = w.glwParent {

		oldFlags := w.glwFlags
		glwFhpUpdate(w, set, clr)

		if (oldFlags^w.glwFlags)&glwInFocusPath != 0 {
			action := "GainedFocus"
			if w.glwFlags&glwInFocusPath == 0 {
				action = "LostFocus"
			}
			glwEventGlwAction(w, action)
		}

		if w.glwFlags&glwFhpSpillToChilds != 0 {
			for c := w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
				if c.glwFlags2&glw2FhpSpill != 0 {
					glwFhpUpdate(c, set, clr)
					glwPathFlood(c, set, clr)
				}
			}
		}
		if w == stop {
			break
		}
	}
}

// C: static glw_t *find_common_ancestor (glw.c:1209-1222)
func findCommonAncestor(a *Glw, b *Glw) *Glw {
	if a == nil {
		return nil
	}

	for ; b != nil; b = b.glwParent {
		for c := a; c != nil; c = c.glwParent {
			if c == b {
				return b
			}
		}
	}
	return nil
}

// C: static void glw_root_set_hover (glw.c:1228-1246)
func glwRootSetHover(gr *glwRoot, w *Glw) {
	if gr.grPointerHover == w {
		return
	}

	com := findCommonAncestor(gr.grPointerHover, w)

	if gr.grPointerHover != nil {
		glwPathModify(gr.grPointerHover, 0, glwInHoverPath, com)
	}

	gr.grPointerHover = w
	if w != nil {
		glwPathModify(w, glwInHoverPath, 0, com)
	}

	glwNeedRefresh(gr, 0)
}

// C: void glw_set_focus_weight (glw.c:1252-1271)
func glwSetFocusWeight(w *Glw, f float32, gs *GlwStyle) {
	if w.glwClass.gcSetFocusWeight != nil {
		w.glwClass.gcSetFocusWeight(w, f, gs)
		return
	}

	if f == w.glwFocusWeight {
		return
	}

	if w.glwFocusWeight > 0 && w.glwRoot.grCurrentFocus == w {
		w.glwRoot.grDelayedFocusLeave = 2
	}

	if f > 0 {
		glwFocusStartWidget(w, f)
	} else {
		w.glwFocusWeight = 0
	}
}

// C: static int glw_path_in_focus (glw.c:1276-1280)
func glwPathInFocus(w *Glw) int {
	if w.glwFlags&glwInFocusPath != 0 {
		return 1
	}
	return 0
}

// C: glw_t *glw_focus_by_path (glw.c:1286-1299)
func glwFocusByPath(w *Glw) *Glw {
	for w.glwFocused != nil {
		if w.glwFocused.glwFlags&(glwFocusBlocked|glwDestroying|glwHidden) != 0 {
			return nil
		}
		w = w.glwFocused
	}

	if w.glwFocusWeight == 0 {
		return nil
	}
	return w
}

// C: static prop_t *get_originating_prop (glw.c:1305-1312)
func getOriginatingProp(w *Glw) *propcore.Prop {
	for ; w != nil; w = w.glwParent {
		if w.glwOriginatingProp != nil {
			return w.glwOriginatingProp
		}
	}
	return nil
}

// C: static int check_autofocus_limit (glw.c:1318-1347)
func checkAutofocusLimit(n *Glw, o *Glw) int {
	limit := 0

	// Mark new tree
	for w := n; w != nil; w = w.glwParent {
		w.glwFlags |= glwMark
	}

	// Scan old tree and try to find fork point
	var x *Glw
	for x = o; x != nil; x = x.glwParent {
		if x.glwFlags&glwMark != 0 {
			break
		}
	}

	// Scan new tree up to intersection point
	for w := n; w != nil && w != x; w = w.glwParent {
		if w.glwFlags2&glw2AutoFocusLimit != 0 {
			limit = 1
			break
		}
	}

	// Unmark
	for w := n; w != nil; w = w.glwParent {
		w.glwFlags &^= glwMark
	}

	return limit
}

// C: int glw_focus_set (glw.c:1353-1479)
func glwFocusSet(gr *glwRoot, w *Glw, how int, whom string) int {
	var sig glwSignal
	var weight float32
	if w != nil {
		weight = w.glwFocusWeight
	}

	if gr.grFocusWork != 0 {
		return 0
	}

	gr.grFocusWork = 1

	if how == glwFocusSetAutomatic ||
		how == glwFocusSetAutomaticFf {
		sig = glwSignalFocusChildAutomatic
	} else {
		sig = glwSignalFocusChildInteractive
	}

	if w != nil {

		if how != glwFocusSetInteractive {
			if checkAutofocusLimit(w, gr.grLastFocus) != 0 {
				gr.grFocusWork = 0
				return 0
			}
		}

		for x := w; x.glwParent != nil; x = x.glwParent {

			if sig != glwSignalFocusChildInteractive &&
				(x.glwFlags&glwFocusBlocked != 0 ||
					x.glwFlags&glwHidden != 0) {
				gr.grFocusWork = 0
				return 0
			}

			if x.glwParent.glwFocused != x {
				// Path switches
				p := x.glwParent
				y := glwFocusByPath(p)

				// Floating focus: when the first widget of a child currently
				// has focus and we insert an entry with equal focus weight before
				// it. This allows the focus to "stay" at the first entry even if
				// we insert entries in random order.
				ff := p.glwFlags&glwFloatingFocus != 0 &&
					(x == p.glwChilds.tqhFirst ||
						how == glwFocusSetAutomaticFf)

				if y == nil || how == glwFocusSetInteractive ||
					weight > y.glwFocusWeight ||
					(ff && weight == y.glwFocusWeight) {
					x.glwParent.glwFocused = x
					glwSignal0(x.glwParent, sig, x)
				} else {
					// Other path outranks our weight, stop now
					gr.grFocusWork = 0
					return 0
				}
			}
		}
	}

	if gr.grCurrentFocus == w {
		gr.grFocusWork = 0
		return 1
	}
	com := findCommonAncestor(gr.grCurrentFocus, w)

	ww := gr.grCurrentFocus

	if ww != nil {
		glwPathModify(ww, 0, glwInFocusPath, com)
	}

	gr.grCurrentFocus = w
	gr.grDelayedFocusLeave = 0

	if w != nil {
		glwNeedRefresh(gr, 0)

		// C: GLW_TRACE("Focus set to %s:%d by %s", ...)

		gr.grLastFocus = w

		glwPathModify(w, glwInFocusPath, 0, nil)

		if how != 0 {
			p := getOriginatingProp(w)

			if p != nil {

				if gr.grLastFocusedInteractive != nil {
					glwDeps.pm.RefDec(gr.grLastFocusedInteractive)
				}

				gr.grLastFocusedInteractive = glwDeps.pm.RefInc(p)
			}
		}
	} else {
		// C: GLW_TRACE("Focus set to none by %s", whom)
	}
	gr.grFocusWork = 0
	if how == glwFocusSetInteractive {
		miscpkg.RstrSet(&gr.grPendingFocus, nil)
	}
	return 1
}

// C: void glw_focus_check_pending (glw.c:1486-1495)
func glwFocusCheckPending(w *Glw) {
	gr := w.glwRoot
	if miscpkg.RstrEq(gr.grPendingFocus, w.glwIdRstr) != 0 {
		w = glwGetFocusableChild(w)
		if w != nil {
			glwFocusSet(gr, w, glwFocusSetInteractive, "FocusMethodDelayed")
		}
	}
}

// C: static int was_interactive (glw.c:1501-1519)
func wasInteractive(w *Glw) int {
	last := w.glwRoot.grLastFocusedInteractive

	if last == nil {
		return 0
	}

	if w.glwOriginatingProp == last {
		return 1
	}

	for w = w.glwParent; w != nil; w = w.glwParent {
		if w.glwFocusWeight != 0 {
			return 0
		}
		if w.glwOriginatingProp == last {
			return 1
		}
	}
	return 0
}

// C: static void glw_focus_init_widget (glw.c:1525-1535)
func glwFocusStartWidget(w *Glw, weight float32) {
	w.glwFocusWeight = weight
	how := glwFocusSetAutomatic

	if w.glwFlags2&glw2Autorefocusable != 0 && wasInteractive(w) != 0 {
		how = glwFocusSetInteractive
	}

	glwFocusSet(w.glwRoot, w, how, "Start")
}

// C: static glw_t *glw_focus_find_focusable (glw.c:1541-1577)
func glwFocusFindFocusable(w *Glw, cur *Glw) *Glw {
	if w.glwFocused != nil {
		c := w.glwFocused
		if c.glwFlags&(glwDestroying|glwFocusBlocked) == 0 {
			if c.glwFocusWeight > 0 {
				return c
			}
			if c.glwChilds.tqhFirst != nil {
				if r := glwFocusFindFocusable(c, nil); r != nil {
					return r
				}
			}
		}
	}

	var c *Glw
	if cur != nil {
		c = cur.glwParentLinkNext
	} else {
		c = w.glwChilds.tqhFirst
	}
	for {
		if c == nil {
			if cur == nil {
				return nil
			}
			c = w.glwChilds.tqhFirst
		}

		if c == cur {
			return nil
		}
		if c.glwFlags&(glwDestroying|glwFocusBlocked) != 0 {
			goto next
		}
		if c.glwFocusWeight > 0 {
			return c
		}
		if c.glwChilds.tqhFirst != nil {
			if r := glwFocusFindFocusable(c, nil); r != nil {
				return r
			}
		}
	next:
		c = c.glwParentLinkNext
	}
}

// C: static void glw_focus_leave (glw.c:1583-1603)
func glwFocusLeave(w *Glw) {
	var r *Glw

	if w.glwRoot.grCurrentFocus != w {
		return
	}

	for w.glwParent != nil {

		// C: assert(w->glw_parent->glw_focused == w)

		if w.glwParent.glwFlags&glwDestroying == 0 {
			r = glwFocusFindFocusable(w.glwParent, w)
			if r != nil {
				break
			}
		}
		w = w.glwParent
	}
	glwFocusSet(w.glwRoot, r, glwFocusSetInteractive, "FocusLeave")
}

// C: static glw_t *glw_focus_crawl0 (glw.c:1609-1632)
func glwFocusCrawl0(w *Glw, cur *Glw, forward int) *Glw {
	var c *Glw

	if forward != 0 {
		if cur != nil {
			c = cur.glwParentLinkNext
		} else {
			c = w.glwChilds.tqhFirst
		}
	} else {
		if cur != nil {
			c = glwTAILQPrev(cur)
		} else {
			c = glwTAILQLast(w)
		}
	}

	for c != nil {
		if c.glwFlags&(glwFocusBlocked|glwHidden) == 0 {
			if c.glwFocusWeight > 0 {
				return c
			}
			if c.glwChilds.tqhFirst != nil {
				if r := glwFocusCrawl0(c, nil, forward); r != nil {
					return r
				}
			}
		}
		if forward != 0 {
			c = c.glwParentLinkNext
		} else {
			c = glwTAILQPrev(c)
		}
	}
	return nil
}

// C: static glw_t *glw_focus_crawl1 (glw.c:1638-1658)
func glwFocusCrawl1(w *Glw, forward int) *Glw {
	var c *Glw
	if forward != 0 {
		c = w.glwChilds.tqhFirst
	} else {
		c = glwTAILQLast(w)
	}

	for c != nil {
		if c.glwFlags&(glwFocusBlocked|glwHidden) == 0 {
			if c.glwFocusWeight > 0 {
				return c
			}
			if c.glwChilds.tqhFirst != nil {
				if r := glwFocusCrawl1(c, forward); r != nil {
					return r
				}
			}
		}
		if forward != 0 {
			c = c.glwParentLinkNext
		} else {
			c = glwTAILQPrev(c)
		}
	}
	return nil
}

// C: void glw_focus_crawl (glw.c:1664-1682)
func glwFocusCrawl(w *Glw, forward int, interactive int) {
	var r *Glw

	for w.glwParent != nil {
		if r = glwFocusCrawl0(w.glwParent, w, forward); r != nil {
			break
		}
		w = w.glwParent
	}

	if r == nil {
		r = glwFocusCrawl1(w, forward)
	}

	if r != nil {
		how := glwFocusSetAutomatic
		if interactive != 0 {
			how = glwFocusSetInteractive
		}
		glwFocusSet(w.glwRoot, r, how, "FocusCrawl")
	}
}

// C: void glw_focus_open_path_close_all_other (glw.c:1689-1734)
func glwFocusOpenPathCloseAllOther(w *Glw) {
	p := w.glwParent
	doClear := 0
	for c := p.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		if c == w {
			continue
		}
		c.glwFlags |= glwFocusBlocked

		if c.glwFlags&glwInFocusPath != 0 && p.glwFocused == c {
			doClear = 1
		}
	}

	w.glwFlags &^= glwFocusBlocked
	c := glwFocusByPath(w)

	if c != nil {
		glwFocusSet(w.glwRoot, c, glwFocusSetAutomatic,
			"OpenCloseFound")
		return
	} else if p.glwParent.glwFocused == p && doClear != 0 {
		r := glwFocusCrawl1(w, 1)
		if r != nil {
			glwFocusSet(w.glwRoot, r, glwFocusSetAutomatic,
				"OpenCloseCrawlDown")
			return
		}

		for w.glwParent != nil {
			if r = glwFocusFindFocusable(w.glwParent, w); r != nil {
				glwFocusSet(w.glwRoot, r, glwFocusSetAutomatic,
					"OpenCloseCrawlUp")
				return
			}
			w = w.glwParent
		}
	}

	if doClear != 0 {
		glwFocusSet(w.glwRoot, nil, glwFocusSetAutomatic,
			"OpenCloseNone")
	}
}

// C: void glw_focus_open_path (glw.c:1741-1755)
func glwFocusOpenPath(w *Glw) {
	if w.glwFlags&glwFocusBlocked == 0 {
		return
	}

	w.glwFlags &^= glwFocusBlocked

	c := glwFocusByPath(w)
	if c != nil {
		glwFocusSet(w.glwRoot, c, glwFocusSetAutomatic,
			"OpenPath")
	}
}

// C: void glw_focus_close_path (glw.c:1761-1773)
func glwFocusClosePath(w *Glw) {
	if w.glwFlags&glwFocusBlocked != 0 {
		return
	}

	w.glwFlags |= glwFocusBlocked

	if w.glwParent.glwFocused != w {
		return
	}

	glwFocusLeave(w)
}

// C: int glw_focus_step (glw.c:1779-1798)
func glwFocusStep(w *Glw, forward int) int {
	if glwPathInFocus(w) == 0 {
		return 0
	}

	action := eventpkg.ActionType(eventpkg.ACTION_UP)
	if forward != 0 {
		action = eventpkg.ACTION_DOWN
	}
	e := glwDeps.em.CreateAction(action).AsEvent()

	for w.glwFocused != nil {
		w = w.glwFocused
		if w.glwFocusWeight > 0 {
			if glwEventToWidget(w, e) != 0 {
				break
			}
		}
	}
	e.Release()
	return 1
}

// C: void glw_focus_suggest (glw.c:1804-1813)
func glwFocusSuggest(w *Glw) {
	for ; w.glwParent != nil; w = w.glwParent {
		if w.glwParent.glwClass.gcSuggestFocus != nil {
			w.glwParent.glwClass.gcSuggestFocus(w.glwParent, w)
			break
		}
	}
}

// C: int glw_is_child_focusable (glw.c:1819-1830)
func glwIsChildFocusable(w *Glw) int {
	if w.glwFocusWeight > 0 {
		return 1
	}
	for c := w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		if glwIsChildFocusable(c) != 0 {
			return 1
		}
	}
	return 0
}

// C: glw_t *glw_get_focusable_child (glw.c:1840-1852)
func glwGetFocusableChild(w *Glw) *Glw {
	if w == nil {
		return nil
	}

	c := glwFocusByPath(w)

	if c == nil {
		c = glwFocusCrawl1(w, 1)
	}

	return c
}

// C: int glw_focus_child (glw.c:1858-1867)
func glwFocusChild(w *Glw) int {
	c := glwGetFocusableChild(w)
	if c == nil {
		return 0
	}

	glwFocusSet(w.glwRoot, c, glwFocusSetInteractive, "FocusChild")
	return 1
}
