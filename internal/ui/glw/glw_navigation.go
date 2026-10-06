package glw

// C: src/ui/glw/glw_navigation.c — canonical 1:1 port.

import (
	"math"

	eventpkg "github.com/czz/movian-go/internal/event"
)

// C: static glw_t *glw_step_widget (glw_navigation.c:30-35)
func glwStepWidget(c *Glw, forward int) *Glw {
	if forward != 0 {
		return glwNextWidget(c)
	}
	return glwPrevWidget(c)
}

// C: int glw_navigate_first (glw_navigation.c:43-56)
func glwNavigateFirst(parent *Glw) int {
	c := glwFirstWidget(parent)

	for c != nil {
		toFocus := glwGetFocusableChild(c)
		if toFocus != nil && toFocus.glwFlags2&glw2NavFocusable != 0 {
			glwFocusSet(toFocus.glwRoot, toFocus, glwFocusSetInteractive,
				"NavFirst")
			return 1
		}
		c = glwNextWidget(c)
	}
	return 0
}

// C: int glw_navigate_last (glw_navigation.c:64-77)
func glwNavigateLast(parent *Glw) int {
	c := glwLastWidget(parent)

	for c != nil {
		toFocus := glwGetFocusableChild(c)
		if toFocus != nil && toFocus.glwFlags2&glw2NavFocusable != 0 {
			glwFocusSet(toFocus.glwRoot, toFocus, glwFocusSetInteractive,
				"NavLast")
			return 1
		}
		c = glwPrevWidget(c)
	}
	return 0
}

// C: int glw_navigate_step (glw_navigation.c:85-120)
func glwNavigateStep(c *Glw, count int, mayWrap int) int {
	parent := c.glwParent
	var toFocus *Glw
	forward := 1

	if count < 0 {
		forward = 0
		count = -count
	}

	for c = glwStepWidget(c, forward); c != nil; c = glwStepWidget(c, forward) {
		tentative := glwGetFocusableChild(c)
		if tentative != nil {
			if tentative.glwFlags2&glw2NavFocusable == 0 {
				continue
			}

			toFocus = tentative
			count--
			if count == 0 {
				break
			}
		}
	}

	if toFocus != nil {
		glwFocusSet(toFocus.glwRoot, toFocus, glwFocusSetInteractive,
			"NavStep")
		return 1
	} else if mayWrap != 0 {
		if forward != 0 {
			return glwNavigateFirst(parent)
		}
		return glwNavigateLast(parent)
	}

	return 0
}

// C: int glw_navigate_move (glw_navigation.c:129-135)
func glwNavigateMove(w *Glw, steps int) int {
	mop := glwMoveOp{steps: steps}
	glwSignal0(w, glwSignalMove, &mop)
	return mop.didMove
}

// C: int glw_navigate_may_wrap (glw_navigation.c:142-154)
func glwNavigateMayWrap(w *Glw) int {
	if w.glwParent.glwFlags2&glw2NavWrap == 0 {
		return 0
	}
	if glwDeps.settings.gsWrap == 0 {
		return 0
	}
	mayWrap := 1
	glwSignal0(w, glwSignalWrapCheck, &mayWrap)
	return mayWrap
}

// C: int glw_navigate_vertical (glw_navigation.c:161-196)
func glwNavigateVertical(w *Glw, e *eventpkg.Event) int {
	c := w.glwFocused

	if c == nil {
		return 0
	}

	mayWrap := glwNavigateMayWrap(c)

	if eventpkg.IsAction(e, eventpkg.ACTION_DOWN) {
		return glwNavigateStep(c, 1, mayWrap)

	} else if eventpkg.IsAction(e, eventpkg.ACTION_UP) {
		return glwNavigateStep(c, -1, mayWrap)

	} else if eventpkg.IsAction(e, eventpkg.ACTION_PAGE_UP) {
		return glwNavigateStep(c, -10, 0)

	} else if eventpkg.IsAction(e, eventpkg.ACTION_PAGE_DOWN) {
		return glwNavigateStep(c, 10, 0)

	} else if eventpkg.IsAction(e, eventpkg.ACTION_TOP) {
		return glwNavigateFirst(w)

	} else if eventpkg.IsAction(e, eventpkg.ACTION_BOTTOM) {
		return glwNavigateLast(w)

	} else if eventpkg.IsAction(e, eventpkg.ACTION_MOVE_DOWN) {
		return glwNavigateMove(c, 1)

	} else if eventpkg.IsAction(e, eventpkg.ACTION_MOVE_UP) {
		return glwNavigateMove(c, -1)

	}
	return 0
}

// C: int glw_navigate_horizontal (glw_navigation.c:203-226)
func glwNavigateHorizontal(w *Glw, e *eventpkg.Event) int {
	c := w.glwFocused

	if c == nil {
		return 0
	}

	mayWrap := glwNavigateMayWrap(c)

	if eventpkg.IsAction(e, eventpkg.ACTION_LEFT) {
		return glwNavigateStep(c, -1, mayWrap)

	} else if eventpkg.IsAction(e, eventpkg.ACTION_RIGHT) {
		return glwNavigateStep(c, 1, mayWrap)

	} else if eventpkg.IsAction(e, eventpkg.ACTION_MOVE_RIGHT) {
		return glwNavigateMove(c, 1)

	} else if eventpkg.IsAction(e, eventpkg.ACTION_MOVE_LEFT) {
		return glwNavigateMove(c, -1)

	}
	return 0
}

// C: typedef struct navigate_matrix_aux (glw_navigation.c:233-241)
type navigateMatrixAux struct {
	curX      int
	curY      int
	tgtX      int
	tgtY      int
	direction int
	best      *Glw
	distance  int
}

// C: static void glw_navigate_matrix_search (glw_navigation.c:249-299)
func glwNavigateMatrixSearch(w *Glw, nma *navigateMatrixAux) {
	for c := w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		glwNavigateMatrixSearch(c, nma)
	}

	if w.glwRoot.grCurrentFocus == w {
		return // Don't consider ourself
	}

	if w.glwMatrix == nil {
		return
	}

	var rect glwRect

	m := *w.glwMatrix

	glwProjectMatrix(&rect, &m, w.glwRoot)

	// Default current/target cordinates are center of focus
	tgtX := (rect.x2 + rect.x1) / 2
	tgtY := (rect.y2 + rect.y1) / 2

	// .. but will be adjusted to edge based on how we're moving

	switch nma.direction {
	case 0: // Moving left
		tgtX = rect.x2

		if tgtX >= nma.curX {
			return
		}

	case 1: // Moving up
		tgtY = rect.y2
		if tgtY >= nma.curY {
			return
		}

	case 2: // Moving right
		tgtX = rect.x1
		if tgtX <= nma.curX {
			return
		}

	case 3: // Moving down
		tgtY = rect.y1
		if tgtY <= nma.curY {
			return
		}
	default:
		panic("glw_navigate_matrix_search: bad direction")
	}

	dx := tgtX - nma.curX
	dy := tgtY - nma.curY

	distance := int(math.Sqrt(float64(dx*dx + dy*dy)))
	if distance > nma.distance {
		return
	}

	nma.best = w
	nma.distance = distance
}

// glwNavigateMatrix — C: glw_navigate_matrix (glw_navigation.c:329-392)
//
// This function tries to navigate based on the projected cordinates
// of widgets. Basically it tries to find a widget that's a decendant
// (in the view tree) of the parameter 'w' and is as close as possible
// to the currently focused widget.
//
// This is called from the main event send/bubble loop in glw.c and only
// if a widget has the 'navPositional' attribute set.
func glwNavigateMatrix(w *Glw, e *eventpkg.Event) int {
	nma := navigateMatrixAux{distance: math.MaxInt32}

	if eventpkg.IsAction(e, eventpkg.ACTION_LEFT) {
		nma.direction = 0
	} else if eventpkg.IsAction(e, eventpkg.ACTION_UP) {
		nma.direction = 1
	} else if eventpkg.IsAction(e, eventpkg.ACTION_RIGHT) {
		nma.direction = 2
	} else if eventpkg.IsAction(e, eventpkg.ACTION_DOWN) {
		nma.direction = 3
	} else {
		return 0
	}

	gr := w.glwRoot
	cur := gr.grCurrentFocus
	if cur == nil || cur.glwMatrix == nil {
		return 0
	}

	var rect glwRect

	glwProjectMatrix(&rect, cur.glwMatrix, gr)

	// Default current cordinates are center of focused area

	nma.curX = (rect.x2 + rect.x1) / 2
	nma.curY = (rect.y2 + rect.y1) / 2

	// Adjust center based on how we're moving

	switch nma.direction {
	case 0: // Moving left
		nma.curX = rect.x1

	case 1: // Moving up
		nma.curY = rect.y1

	case 2: // Moving right
		nma.curX = rect.x2

	case 3: // Moving down
		nma.curY = rect.y2
	default:
		panic("glw_navigate_matrix: bad direction")
	}

	glwNavigateMatrixSearch(w, &nma)

	if nma.best == nil {
		return 0
	}

	glwFocusSet(gr, nma.best, glwFocusSetInteractive, "NavPositional")
	return 1
}
