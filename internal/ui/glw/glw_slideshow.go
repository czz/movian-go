package glw

// Canonical port of src/ui/glw/glw_slideshow.c — the `slideshow`
// widget class (auto-advancing crossfade between children; keeps the
// previous/next children "hot" in layout).

import (
	"math"
	"unsafe"

	eventpkg "github.com/czz/movian-go/internal/event"
)

// C: glw_slideshow_t
type glwSlideshow struct {
	w Glw

	deadline int64

	hold           int
	displayTime    int
	transitionTime int

	visibleChilds int
}

// C: glw_slideshow_item_t (per-child parent data)
type glwSlideshowItem struct {
	alpha float32
}

// C: #define itemdata(w) glw_parent_data(w, glw_slideshow_item_t)
func slideshowItem(w *Glw) *glwSlideshowItem {
	return w.glwParentData.(*glwSlideshowItem)
}

// C: glw_slideshow_render
func glwSlideshowRender(w *Glw, rc *glwRctx) {
	var rc0 glwRctx
	sa := w.glwAlpha

	c := w.glwFocused
	if c == nil {
		return
	}

	p := glwPrevWidget(c)
	if p == nil {
		p = glwLastWidget(w)
	}
	if p != nil && p != c {
		a := sa * slideshowItem(p).alpha
		if a > glwAlphaEpsilon {
			rc0 = *rc
			rc0.rcAlpha *= a
			glwRender0(p, &rc0)
		}
	}

	rc0 = *rc
	rc0.rcAlpha *= sa * slideshowItem(c).alpha
	glwRender0(c, &rc0)

	n := glwNextWidget(c)
	if n == nil {
		n = glwFirstWidget(w)
	}
	if n != nil && n != c {
		a := sa * slideshowItem(n).alpha
		if a > glwAlphaEpsilon {
			rc0 = *rc
			rc0.rcAlpha *= a
			glwRender0(n, &rc0)
		}
	}
}

// C: update_parent_alpha
func slideshowUpdateParentAlpha(w *Glw, v float32) int {
	if slideshowItem(w).alpha == v {
		return 0
	}
	slideshowItem(w).alpha = v
	return 1
}

// C: glw_slideshow_layout
func glwSlideshowLayout(w *Glw, rc *glwRctx) {
	gr := w.glwRoot
	s := (*glwSlideshow)(unsafe.Pointer(w))
	r := 0

	delta := float32(s.w.glwRoot.grFrameduration) / float32(s.transitionTime)

	c := s.w.glwFocused
	if c == nil {
		s.w.glwFocused = glwFirstWidget(&s.w)
		c = s.w.glwFocused
		s.deadline = gr.grFrameStart + int64(s.displayTime)
		if c != nil {
			glwCopyConstraints(&s.w, c)
		}
	}

	if c == nil {
		return
	}

	if s.visibleChilds > 1 {
		glwScheduleRefresh(gr, s.deadline)
		if s.deadline <= gr.grFrameStart {
			s.deadline = gr.grFrameStart + int64(s.displayTime)
			c = glwNextWidget(c)
			if c == nil {
				c = glwFirstWidget(&s.w)
			}
			if c != nil {
				s.w.glwFocused = c
				glwFocusOpenPathCloseAllOther(c)
				glwCopyConstraints(&s.w, c)
			}
		}
	}

	glwLayout0(c, rc)
	a := slideshowItem(c).alpha
	r |= slideshowUpdateParentAlpha(c, glwMin(a+delta, 1.0))

	/**
	 * Keep previous and next images 'hot' (ie, loaded into texture memory)
	 */

	rc0 := *rc

	p := glwPrevWidget(c)
	if p == nil {
		p = glwLastWidget(&s.w)
	}
	if p != nil && p != c {
		a = slideshowItem(p).alpha
		r |= slideshowUpdateParentAlpha(p, glwMax(a-delta, 0.0))
		if slideshowItem(p).alpha < glwAlphaEpsilon {
			rc0.rcInvisible = 1
		} else {
			rc0.rcInvisible = 0
		}
		glwLayout0(p, &rc0)
	}

	n := glwNextWidget(c)
	if n == nil {
		n = glwFirstWidget(&s.w)
	}
	if n != nil && n != c {
		a = slideshowItem(n).alpha
		r |= slideshowUpdateParentAlpha(n, glwMax(a-delta, 0.0))
		if slideshowItem(n).alpha < glwAlphaEpsilon {
			rc0.rcInvisible = 1
		} else {
			rc0.rcInvisible = 0
		}
		glwLayout0(n, &rc0)
	}

	if r != 0 {
		glwNeedRefresh(w.glwRoot, 0)
	}
}

// C: glw_slideshow_update_playstatus
func glwSlideshowUpdatePlaystatus(s *glwSlideshow) {
	if s.hold != 0 {
		s.deadline = math.MaxInt64
	} else {
		s.deadline = 0
	}
	glwNeedRefresh(s.w.glwRoot, 0)
}

// C: action_update_hold_by_event (event.c:386-399) — event-layer
// helper; the pkg/media variant is hold-flag-typed, this one is the
// canonical int version used by glw_slideshow.
func actionUpdateHoldByEvent(hold int, e *eventpkg.Event) int {
	if e.IsAction(eventpkg.ACTION_PLAYPAUSE) {
		if hold != 0 {
			return 0
		}
		return 1
	}
	if e.IsAction(eventpkg.ACTION_PAUSE) {
		return 1
	}
	if e.IsAction(eventpkg.ACTION_PLAY) {
		return 0
	}
	return 0
}

// C: glw_slideshow_event
func glwSlideshowEvent(w *Glw, e *eventpkg.Event) int {
	gr := w.glwRoot
	s := (*glwSlideshow)(unsafe.Pointer(w))
	var c *Glw

	if e.IsAction(eventpkg.ACTION_SKIP_FORWARD) ||
		e.IsAction(eventpkg.ACTION_RIGHT) {
		if w.glwFocused != nil {
			c = glwNextWidget(w.glwFocused)
		}
		if c == nil {
			c = glwFirstWidget(w)
		}
		w.glwFocused = c
		s.deadline = 0
		glwNeedRefresh(gr, 0)

	} else if e.IsAction(eventpkg.ACTION_SKIP_BACKWARD) ||
		e.IsAction(eventpkg.ACTION_LEFT) {

		if w.glwFocused != nil {
			c = glwPrevWidget(w.glwFocused)
		}
		if c == nil {
			c = glwLastWidget(w)
		}
		w.glwFocused = c
		s.deadline = 0
		glwNeedRefresh(gr, 0)

	} else if e.Type == eventpkg.EVENT_UNICODE &&
		(*eventpkg.EventInt)(unsafe.Pointer(e)).Val == 32 {

		if s.hold != 0 {
			s.hold = 0
		} else {
			s.hold = 1
		}
		glwSlideshowUpdatePlaystatus(s)

	} else if e.IsAction(eventpkg.ACTION_PLAYPAUSE) ||
		e.IsAction(eventpkg.ACTION_PLAY) ||
		e.IsAction(eventpkg.ACTION_PAUSE) {

		s.hold = actionUpdateHoldByEvent(s.hold, e)
		glwSlideshowUpdatePlaystatus(s)

	} else if e.IsAction(eventpkg.ACTION_STOP) {

		// prop_set_string(s->playstatus, "stop");

	} else {
		return 0
	}

	return 1
}

// C: glw_slideshow_callback
func glwSlideshowCallback(w *Glw, opaque any, signal glwSignal, extra any) int {
	s := (*glwSlideshow)(unsafe.Pointer(w))

	switch signal {
	case glwSignalChildCreated, glwSignalChildUnhidden:
		s.visibleChilds++
		glwNeedRefresh(w.glwRoot, 0)

	case glwSignalChildDestroyed:
		if w.glwFlags&glwHidden != 0 {
			return 0
		}
		s.visibleChilds--
	case glwSignalChildHidden:
		s.visibleChilds--

	case glwSignalChildConstraintsChanged:
		if w.glwFocused == extra {
			c, _ := extra.(*Glw)
			glwCopyConstraints(w, c)
		}
		return 1
	}
	return 0
}

// C: glw_slideshow_ctor
func glwSlideshowCtor(w *Glw) {
	s := (*glwSlideshow)(unsafe.Pointer(w))
	s.displayTime = 5000000
	s.transitionTime = 500000
}

// C: glw_slideshow_set_float
func glwSlideshowSetFloat(w *Glw, attrib glwAttribute, value float32, gs *GlwStyle) int {
	s := (*glwSlideshow)(unsafe.Pointer(w))
	v := int(value * 1000000)

	switch attrib {
	case glwAttribTime:
		if s.displayTime == v {
			return 0
		}
		s.displayTime = v

	case glwAttribTransitionTime:
		if s.transitionTime == v {
			return 0
		}
		s.transitionTime = v

	default:
		return -1
	}
	return 1
}

// C: glw_class_t glw_slideshow
var glwSlideshowClass = &glwClass{
	gcName:           "slideshow",
	gcInstanceSize:   int(unsafe.Sizeof(glwSlideshow{})),
	gcParentDataSize: int(unsafe.Sizeof(glwSlideshowItem{})),
	gcNewParentData:  func() any { return &glwSlideshowItem{} },
	gcNew: func(parent *Glw) *Glw {
		s := &glwSlideshow{}
		return &s.w
	},
	gcFlags:         glwCanHideChilds,
	gcCtor:          glwSlideshowCtor,
	gcSetFloat:      glwSlideshowSetFloat,
	gcLayout:        glwSlideshowLayout,
	gcRender:        glwSlideshowRender,
	gcSignalHandler: glwSlideshowCallback,
	gcBubbleEvent:   glwSlideshowEvent,
}

func registerSlideshow() {
	glwRegisterClass(glwSlideshowClass)
}
