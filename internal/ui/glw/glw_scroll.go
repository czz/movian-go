package glw

// Canonical port of src/ui/glw/glw_scroll.c — shared scroll-control logic
// used by scrollable container widgets (array, list, slider, clist, ...).
//
// C: glw_scroll_control_t (glw_scroll.h) — embedded in each widget struct.

import (
	"math"
	"unsafe"

	eventpkg "github.com/czz/movian-go/internal/event"
)

// C: glw_scroll_control_t (glw_scroll.h)
type glwScrollControl struct {
	suggested  *Glw
	scrollToMe *Glw

	targetPos   int     // This is where we want to go
	filteredPos float32 // This is where we are
	roundedPos  float32 // Position rounded to pixels

	totalSize int
	pageSize  int

	scrollThresholdPre  int
	scrollThresholdPost int

	initialTouchX float32
	initialTouchY float32
	initialPos    int
	lastTouchX    float32
	lastTouchY    float32
	lastTouchTime int64

	touchVelocity float32
	kineticScroll float32

	metrics glwSliderMetrics

	clipOffsetPre  int16
	clipOffsetPost int16
	clipAlpha      float32
	clipBlur       float32

	chaseFocus     int
	suggestCnt     int
	bottomAnchored int
	bottomGravity  int
}

// glwClampI — integer variant of GLW_CLAMP
func glwClampI(x, min, max int) int {
	if x < min {
		return min
	}
	if x > max {
		return max
	}
	return x
}

// C: glw_scroll_handle_pointer_event_filter
func glwScrollHandlePointerEventFilter(gs *glwScrollControl, w *Glw, gpe *glwPointerEventT) int {
	gr := w.glwRoot
	grabbed := gr.grPointerGrabScroll == w

	switch gpe.typ {
	case glwPointerTouchStart:
		gr.grPointerGrabScroll = w

		gs.initialPos = gs.targetPos
		gs.initialTouchX = gpe.localX
		gs.initialTouchY = gpe.localY
		gs.lastTouchX = gpe.localX
		gs.lastTouchY = gpe.localY
		gs.lastTouchTime = gpe.ts
		gs.touchVelocity = 0
		gs.kineticScroll = 0
		return 0

	case glwPointerTouchEnd:
		if float32(math.Abs(float64(gs.touchVelocity))) > 10 {
			gs.kineticScroll = gs.touchVelocity
			glwScheduleRefresh(w.glwRoot, 0)
		}
		if grabbed {
			gr.grPointerGrabScroll = nil
		}
		return 0
	}
	return 0
}

// C: glw_scroll_handle_pointer_event
func glwScrollHandlePointerEvent(gs *glwScrollControl, w *Glw, gpe *glwPointerEventT) int {
	gr := w.glwRoot
	var dt int64
	grabbed := gr.grPointerGrabScroll == w
	var v float32

	switch gpe.typ {
	case glwPointerScroll:
		gs.bottomAnchored = 0
		gs.targetPos += int(float32(gs.pageSize) * gpe.deltaY)
		w.glwFlags |= glwUpdateMetrics
		glwScheduleRefresh(w.glwRoot, 0)
		return 1

	case glwPointerFineScroll:
		gs.bottomAnchored = 0
		gs.targetPos += int(gpe.deltaY)
		w.glwFlags |= glwUpdateMetrics
		glwScheduleRefresh(w.glwRoot, 0)
		return 1

	case glwPointerTouchCancel:
		if grabbed {
			gr.grPointerGrabScroll = nil
		}
		return 1

	case glwPointerFocusMotion:
		if !grabbed {
			return 0
		}

		gs.bottomAnchored = 0
		gs.targetPos = int((gpe.localY-gs.initialTouchY)*
			float32(gs.pageSize)*0.5) + gs.initialPos

		maxValue := max(gs.totalSize-gs.pageSize+gs.scrollThresholdPost, 0)
		gs.targetPos = glwClampI(gs.targetPos, 0, maxValue)

		if gs.targetPos-gs.initialPos > 15 || gs.initialPos-gs.targetPos > 15 {
			if gr.grPointerPress != nil {
				glwPathModify(gr.grPointerPress, 0, glwInPressedPath, nil)
				gr.grPointerPress = nil
			}
		}

		dt = gpe.ts - gs.lastTouchTime
		if dt > 100 {
			v = 1000000.0 * (gpe.localY - gs.lastTouchY) / float32(dt)
			gs.touchVelocity = v * 10
		}
		gs.lastTouchTime = gpe.ts
		gs.lastTouchX = gpe.localX
		gs.lastTouchY = gpe.localY
		w.glwFlags |= glwUpdateMetrics
		glwScheduleRefresh(w.glwRoot, 0)

	default:
		return 0
	}
	return 0
}

// C: glw_scroll_layout
func glwScrollLayout(gsc *glwScrollControl, w *Glw, height int) {
	maxValue := max(gsc.totalSize-gsc.pageSize+gsc.scrollThresholdPost, 0)

	if w.glwRoot.grPointerGrabScroll == w {

		gsc.filteredPos = float32(gsc.targetPos)
		gsc.filteredPos = glwClamp(gsc.filteredPos, 0, float32(maxValue))

	} else if float32(math.Abs(float64(gsc.kineticScroll))) > 0.5 {

		gsc.filteredPos += gsc.kineticScroll
		if float32(gsc.targetPos) != gsc.filteredPos {
			gsc.targetPos = int(gsc.filteredPos)
			glwNeedRefresh(w.glwRoot, 0)
		}
		gsc.kineticScroll *= 0.95
		gsc.bottomAnchored = 0
		gsc.filteredPos = glwClamp(gsc.filteredPos, 0, float32(maxValue))

	} else {
		gsc.kineticScroll = 0

		if gsc.bottomGravity != 0 {
			if gsc.targetPos == maxValue {
				gsc.bottomAnchored = 1
			}
			if gsc.bottomAnchored != 0 {
				if gsc.targetPos != maxValue {
					w.glwFlags |= glwUpdateMetrics
				}
				gsc.targetPos = maxValue
			}
		}

		gsc.targetPos = glwClampI(gsc.targetPos, 0, maxValue)

		if float32(math.Abs(float64(float32(gsc.targetPos)-gsc.filteredPos))) > float32(height)*2 {
			gsc.filteredPos = float32(gsc.targetPos)
		} else {
			glwLp(&gsc.filteredPos, w.glwRoot, float32(gsc.targetPos), 0.25)
		}
	}

	gsc.roundedPos = float32(math.Round(float64(gsc.filteredPos*5.0))) / 5.0
}

// C: glw_scroll_update_metrics
func glwScrollUpdateMetrics(gsc *glwScrollControl, w *Glw) {
	var v float32
	doUpdate := false

	w.glwFlags &^= glwUpdateMetrics

	v = glwMin(1.0, float32(gsc.pageSize)/float32(gsc.totalSize))

	if v != gsc.metrics.knobSize {
		doUpdate = true
		gsc.metrics.knobSize = v
	}

	v = glwMax(0, float32(gsc.targetPos)/float32(gsc.totalSize-gsc.pageSize+gsc.scrollThresholdPost))

	if v != gsc.metrics.position {
		doUpdate = true
		gsc.metrics.position = v
	}
	if !doUpdate {
		return
	}

	if gsc.totalSize > gsc.pageSize && w.glwFlags&glwCanScroll == 0 {
		w.glwFlags |= glwCanScroll
		glwSignal0(w, glwSignalCanScrollChanged, nil)

	} else if gsc.totalSize <= gsc.pageSize && w.glwFlags&glwCanScroll != 0 {
		w.glwFlags &^= glwCanScroll
		glwSignal0(w, glwSignalCanScrollChanged, nil)
	}

	glwSignal0(w, glwSignalSliderMetrics, &gsc.metrics)
}

// C: glw_scroll_set_float_attributes
func glwScrollSetFloatAttributes(gsc *glwScrollControl, a string, value float32) int {
	switch a {
	case "clipAlpha":
		if value == gsc.clipAlpha {
			return glwSetNoChange
		}
		gsc.clipAlpha = value
		return glwSetRerenderRequired

	case "clipBlur":
		if value == gsc.clipBlur {
			return glwSetNoChange
		}
		gsc.clipBlur = value
		return glwSetRerenderRequired
	}
	return glwSetNotResponding
}

// C: glw_scroll_set_int_attributes
func glwScrollSetIntAttributes(gsc *glwScrollControl, a string, value int) int {
	switch a {
	case "chaseFocus":
		gsc.chaseFocus = value
		return glwSetNoChange

	case "scrollThresholdTop":
		if gsc.scrollThresholdPre == value {
			return glwSetNoChange
		}
		gsc.scrollThresholdPre = value
		return glwSetRerenderRequired

	case "scrollThresholdBottom":
		if gsc.scrollThresholdPost == value {
			return glwSetNoChange
		}
		gsc.scrollThresholdPost = value
		return glwSetRerenderRequired

	case "clipOffsetTop":
		if gsc.clipOffsetPre == int16(value) {
			return glwSetNoChange
		}
		gsc.clipOffsetPre = int16(value)
		return glwSetRerenderRequired

	case "clipOffsetBottom":
		if gsc.clipOffsetPost == int16(value) {
			return glwSetNoChange
		}
		gsc.clipOffsetPost = int16(value)
		return glwSetRerenderRequired

	case "bottomGravity":
		bg := 0
		if value != 0 {
			bg = 1
		}
		if gsc.bottomGravity == bg {
			return glwSetNoChange
		}
		gsc.bottomGravity = bg
		gsc.bottomAnchored = gsc.bottomGravity
		return glwSetRerenderRequired
	}
	return glwSetNotResponding
}

// C: glw_scroll_suggest_focus
func glwScrollSuggestFocus(gsc *glwScrollControl, w *Glw, c *Glw) {
	if !glwIsFocused(w) {
		w.glwFocused = c
		glwSignal0(w, glwSignalFocusChildInteractive, c)
		gsc.scrollToMe = c
		return
	}

	if gsc.suggested == w.glwFocused || gsc.suggestCnt > 0 {
		c = glwFocusByPath(c)
		if c != nil {
			glwFocusSet(c.glwRoot, c, glwFocusSetSuggested, "Suggested")
		}
		gsc.suggestCnt = 1
	}
	gsc.suggested = c
	gsc.suggestCnt++
}

// C: glw_scroll_handle_scroll
func glwScrollHandleScroll(gsc *glwScrollControl, w *Glw, gs *glwScroll) {
	gsc.bottomAnchored = 0
	top := max(int(gs.value*float32(gsc.totalSize-gsc.pageSize+gsc.scrollThresholdPost)), 0)
	gsc.targetPos = top
	glwScheduleRefresh(w.glwRoot, 0)

	if gsc.chaseFocus == 0 {
		return
	}

	c := w.glwClass.gcFindVisibleChild(w)
	if c == nil {
		return
	}

	if glwIsFocused(w) {
		glwFocusSet(w.glwRoot, c, glwFocusSetSuggested, "Scroll")
	} else {
		w.glwFocused = c
		glwSignal0(w, glwSignalFocusChildInteractive, c)
	}
}

// C: glw_scroll_handle_event
func glwScrollHandleEvent(gs *glwScrollControl, w *Glw, e *eventpkg.Event) int {
	if e.Type == eventpkg.EVENT_SCROLL {
		es := (*eventpkg.EventScroll)(unsafe.Pointer(e))

		gs.bottomAnchored = 0
		gs.targetPos += int(es.DY)
		w.glwFlags |= glwUpdateMetrics
		glwScheduleRefresh(w.glwRoot, 0)
		return 1
	}
	return 0
}
