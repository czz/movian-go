package glw

// Canonical port of src/ui/glw/glw_slider.c — the `slider_x` and
// `slider_y` widget classes (draggable knob bound to a float property
// or to a scrollable widget's metrics).

import (
	"unsafe"

	eventpkg "github.com/czz/movian-go/internal/event"
	propcore "github.com/czz/movian-go/internal/prop"
)

// C: glw_slider_t
type glwSlider struct {
	w Glw

	sub             *propcore.Subscription
	p               *propcore.Prop
	tentativeOutput *propcore.Prop

	boundWidget *Glw

	knobPosPx      float32
	knobSizeFixed  float32
	value          float32
	secondBarValue float32

	min, max, step, stepI float32
	grabDelta             float32

	knobSizePx    int16
	sliderSizePx  int16
	fixedKnobSize uint8 // C: char
	interpolate   uint8 // C: char
	keystep       uint8 // C: char
	knobOverEdges uint8 // C: char
}

// C: update_value_delta
func sliderUpdateValueDelta(s *glwSlider, d float32) {
	if s.p != nil {
		s.interpolate = 2
		s.p.AddFloat(d * (s.max - s.min))
	} else {
		s.value = glwMax(s.min, glwMin(s.max, s.value+d))
		s.interpolate = 1
		if s.boundWidget != nil {
			var gs glwScroll
			gs.value = s.value
			glwSignal0(s.boundWidget, glwSignalScroll, &gs)
		}
		glwNeedRefresh(s.w.glwRoot, 0)
	}
}

// C: update_value
func sliderUpdateValue(s *glwSlider, v float32, tentative int) {
	v = glwMax(0, glwMin(1.0, v))
	scaled := v*(s.max-s.min) + s.min
	s.value = v
	s.interpolate = 0

	if tentative != 0 && s.tentativeOutput != nil {
		glwDeps.pm.SetFloatEx(s.tentativeOutput, nil, scaled)
		return
	}
	glwDeps.pm.SetVoidEx(s.tentativeOutput, nil)

	if s.p != nil {
		glwDeps.pm.SetFloatEx(s.p, nil, scaled)
	} else {
		if s.boundWidget != nil {
			var gs glwScroll
			gs.value = s.value
			glwSignal0(s.boundWidget, glwSignalScroll, &gs)
		}
	}
}

// C: glw_slider_layout
func glwSliderLayout(w *Glw, rc *glwRctx) {
	s := (*glwSlider)(unsafe.Pointer(w))
	var rc0 glwRctx

	c := w.glwChilds.tqhFirst
	if c == nil || rc.rcWidth == 0 || rc.rcHeight == 0 {
		return
	}

	f := glwFilterConstraints(c)

	if s.fixedKnobSize != 0 {
		if w.glwClass == glwSliderXClass {
			s.knobSizePx = int16(s.knobSizeFixed * float32(rc.rcWidth))
		} else {
			s.knobSizePx = int16(s.knobSizeFixed * float32(rc.rcHeight))
		}
	} else if f&glwConstraintX != 0 && w.glwClass == glwSliderXClass {
		s.knobSizePx = int16(glwReqWidth(c))
	} else if f&glwConstraintY != 0 && w.glwClass == glwSliderYClass {
		s.knobSizePx = int16(glwReqHeight(c))
	} else if w.glwClass == glwSliderXClass {
		s.knobSizePx = rc.rcHeight
	} else {
		s.knobSizePx = rc.rcWidth
	}

	var p int

	rc0 = *rc

	var knobsize int16
	if s.knobOverEdges != 0 {
		knobsize = 0
	} else {
		knobsize = s.knobSizePx
	}
	if w.glwClass == glwSliderXClass {
		p = int(s.value*float32(rc.rcWidth-knobsize) + float32(knobsize/2))
		rc0.rcWidth = s.knobSizePx
		s.sliderSizePx = rc.rcWidth
	} else {
		p = int((1-s.value)*float32(rc.rcHeight-knobsize) + float32(knobsize/2))
		rc0.rcHeight = s.knobSizePx
		s.sliderSizePx = rc.rcHeight
	}

	if s.interpolate != 0 {
		glwLp(&s.knobPosPx, w.glwRoot, float32(p), 0.25)
	} else {
		s.knobPosPx = float32(p)
	}

	glwLayout0(c, &rc0)

	if w.glwClass != glwSliderXClass {
		return
	}

	c = c.glwParentLinkNext
	if c == nil {
		return
	}

	rc0 = *rc
	glwReposition(&rc0, 0,
		int(rc.rcHeight),
		int(s.knobPosPx),
		0)
	glwLayout0(c, &rc0)

	c = c.glwParentLinkNext
	if c == nil {
		return
	}

	rc0 = *rc
	glwReposition(&rc0, 0,
		int(rc.rcHeight),
		int(s.knobPosPx+float32(s.sliderSizePx)/(s.max-s.min)*s.secondBarValue),
		0)
	glwLayout0(c, &rc0)
}

// C: glw_slider_render_x
func glwSliderRenderX(w *Glw, rc *glwRctx) {
	s := (*glwSlider)(unsafe.Pointer(w))
	var rc0 glwRctx

	if glwIsFocusableOrClickable(w) {
		glwStoreMatrix(w, rc)
	}

	c := w.glwChilds.tqhFirst
	if c == nil {
		return
	}

	rc0 = *rc
	rc0.rcAlpha *= w.glwAlpha

	glwReposition(&rc0,
		int(s.knobPosPx-float32(s.knobSizePx/2)),
		int(rc.rcHeight),
		int(s.knobPosPx+float32(s.knobSizePx/2)),
		0)

	glwRender0(c, &rc0)

	c = c.glwParentLinkNext
	if c == nil {
		return
	}

	rc0 = *rc
	glwReposition(&rc0, 0,
		int(rc.rcHeight),
		int(s.knobPosPx),
		0)
	glwRender0(c, &rc0)

	c = c.glwParentLinkNext
	if c == nil {
		return
	}

	rc0 = *rc
	glwReposition(&rc0, 0,
		int(rc.rcHeight),
		int(s.knobPosPx+float32(s.sliderSizePx)/(s.max-s.min)*s.secondBarValue),
		0)
	glwRender0(c, &rc0)
}

// C: glw_slider_render_y
func glwSliderRenderY(w *Glw, rc *glwRctx) {
	s := (*glwSlider)(unsafe.Pointer(w))
	var rc0 glwRctx

	if glwIsFocusableOrClickable(w) {
		glwStoreMatrix(w, rc)
	}

	c := w.glwChilds.tqhFirst
	if c == nil {
		return
	}

	rc0 = *rc
	rc0.rcAlpha *= w.glwAlpha

	glwReposition(&rc0,
		0,
		int(s.knobPosPx+float32(s.knobSizePx/2)),
		int(rc.rcWidth),
		int(s.knobPosPx-float32(s.knobSizePx/2)))

	glwRender0(c, &rc0)
}

// C: glw_slider_event_y
func glwSliderEventY(w *Glw, e *eventpkg.Event) int {
	s := (*glwSlider)(unsafe.Pointer(w))
	var d float32

	if s.keystep == 0 {
		return 0
	}

	if e.IsAction(eventpkg.ACTION_UP) {
		d = -s.step
	} else if e.IsAction(eventpkg.ACTION_DOWN) {
		d = s.step
	} else {
		return 0
	}

	sliderUpdateValueDelta(s, d)
	return 1
}

// C: glw_slider_event_x
func glwSliderEventX(w *Glw, e *eventpkg.Event) int {
	s := (*glwSlider)(unsafe.Pointer(w))
	var d float32

	if s.keystep == 0 {
		return 0
	}

	if e.IsAction(eventpkg.ACTION_LEFT) {
		d = -s.stepI
	} else if e.IsAction(eventpkg.ACTION_RIGHT) {
		d = s.stepI
	} else {
		return 0
	}
	sliderUpdateValueDelta(s, d)
	return 1
}

// C: pointer_event
func glwSliderPointerEvent(w *Glw, gpe *glwPointerEventT) int {
	gr := w.glwRoot
	s := (*glwSlider)(unsafe.Pointer(w))
	hitpos := 0
	var v0 float32
	if w.glwClass == glwSliderXClass {
		v0 = gpe.localX
	} else {
		v0 = -gpe.localY
	}
	var v float32
	var knobPos float32
	knobSize := float32(s.knobSizePx) / float32(s.sliderSizePx)
	tentative := 0

	if w.glwClass == glwSliderXClass {
		knobPos = -1 + 2.0*s.knobPosPx/float32(s.sliderSizePx)
	} else {
		knobPos = 1 - 2.0*s.knobPosPx/float32(s.sliderSizePx)
	}

	if v0 < knobPos-knobSize {
		hitpos = -1
	} else if v0 > knobPos+knobSize {
		hitpos = 1
	}

	switch gpe.typ {
	case glwPointerLeftPress, glwPointerTouchStart:
		if w.glwFlags2&glw2AlwaysGrabKnob != 0 {
			v = glwRescale(v0+s.grabDelta,
				-1.0+knobSize, 1.0-knobSize)
			gr.grPointerGrab = w
		} else if hitpos == 0 {
			s.grabDelta = knobPos - v0
			gr.grPointerGrab = w
			v = s.value
		} else {
			sliderUpdateValueDelta(s, float32(hitpos)*knobSize)
			return 0
		}
		tentative = 1

	case glwPointerFocusMotion:
		if knobSize == 1.0 {
			break
		}
		v = glwRescale(v0+s.grabDelta,
			-1.0+knobSize, 1.0-knobSize)
		tentative = 1

	case glwPointerLeftRelease, glwPointerTouchEnd:
		v = s.value

	default:
		return 0
	}
	sliderUpdateValue(s, v, tentative)
	glwNeedRefresh(s.w.glwRoot, 0)
	return 0
}

// C: slider_bound_callback
func sliderBoundCallback(w *Glw, opaque any, signal glwSignal, extra any) int {
	s := (*glwSlider)(opaque.(unsafe.Pointer))

	switch signal {
	case glwSignalDestroy:
		s.boundWidget = nil

	case glwSignalSliderMetrics:
		m := extra.(*glwSliderMetrics)
		s.fixedKnobSize = 1
		s.value = m.position
		s.interpolate = 0
		s.knobSizeFixed = m.knobSize

		if (s.knobSizeFixed != 1.0) == (s.w.glwFlags&glwCanScroll == 0) {
			s.w.glwFlags ^= glwCanScroll
			glwSignal0(&s.w, glwSignalCanScrollChanged, nil)
		}
	}
	return 0
}

// C: slider_unbind
func sliderUnbind(s *glwSlider) {
	if s.sub != nil {
		glwDeps.pm.Unsubscribe(s.sub)
	}

	if s.p != nil {
		glwDeps.pm.RefDec(s.p)
		s.p = nil
	}

	if s.boundWidget != nil {
		glwSignalHandlerUnregister(s.boundWidget, sliderBoundCallback,
			unsafe.Pointer(s))
		s.boundWidget = nil
	}
}

// C: glw_slider_callback
func glwSliderCallback(w *Glw, opaque any, signal glwSignal, extra any) int {
	s := (*glwSlider)(unsafe.Pointer(w))

	switch signal {
	case glwSignalDestroy:
		sliderUnbind(s)

	case glwSignalChildConstraintsChanged:
		c, _ := extra.(*Glw)
		if c == w.glwChilds.tqhFirst {
			if w.glwClass == glwSliderYClass {
				glwSetConstraints(w, glwReqWidth(c), 0, 0, glwConstraintX)
			} else {
				glwSetConstraints(w, 0, glwReqHeight(c), 0, glwConstraintY)
			}
		}
		return 1
	}
	return 0
}

// C: prop_callback
func sliderPropCallback(opaque any, event propcore.EventType,
	args ...any) {
	sl := (*glwSlider)(opaque.(unsafe.Pointer))
	if sl == nil {
		return
	}
	gr := sl.w.glwRoot
	var v float32
	grabbed := gr.grPointerGrab == &sl.w

	switch event {
	case propcore.EventSetVoid:
		if grabbed {
			gr.grPointerGrab = nil
		}
		v = 0

	case propcore.EventSetFloat:
		if grabbed {
			return
		}
		v = floatFromArg(args[0])

	case propcore.EventSetInt:
		if grabbed {
			return
		}
		v = floatFromArg(args[0])

	case propcore.EventValueProp:
		glwDeps.pm.RefDec(sl.p)
		sl.p = glwDeps.pm.RefInc(args[0].(*propcore.Prop))
		return

	default:
		return
	}

	if sl.max-sl.min == 0 {
		return
	}

	v = glwRescale(v, sl.min, sl.max)
	sl.value = glwMax(0, glwMin(1.0, v))
	glwNeedRefresh(sl.w.glwRoot, 0)
	if sl.interpolate != 0 {
		sl.interpolate--
	}
}

// C: slider_bind_by_id
func sliderBindByID(s *glwSlider, name string) {
	t := glwFindNeighbour(&s.w, name)

	if t == nil {
		return
	}

	s.boundWidget = t
	glwSignalHandlerRegister(t, sliderBoundCallback, unsafe.Pointer(s))
	t.glwFlags |= glwUpdateMetrics
}

// C: glw_slider_ctor
func glwSliderCtor(w *Glw) {
	s := (*glwSlider)(unsafe.Pointer(w))
	s.max = 1.0
	s.stepI = 0.1
	s.step = 0.1
	s.keystep = 1
	s.secondBarValue = 0.0
}

// C: bind_to_property
func sliderBindToProperty(w *Glw, scope *glwScope, pname []string) {
	s := (*glwSlider)(unsafe.Pointer(w))
	sliderUnbind(s)

	s.sub = glwPropSubscribeTags(
		propcore.SubFlagDirectUpdate|propcore.SubFlagSendValueProp,
		propTagName, pname,
		propTagCallback, sliderPropCallback, unsafe.Pointer(s),
		propTagCourier, w.glwRoot.grCourier,
		propTagRootVector, scope.gsRoots[:scope.gsNumRoots],
		propTagRoot, w.glwRoot.grPropUi,
		propTagNamedRoot, w.glwRoot.grPropNav, "nav")
}

// C: glw_slider_set_float
func glwSliderSetFloat(w *Glw, attrib glwAttribute, value float32, gs *GlwStyle) int {
	s := (*glwSlider)(unsafe.Pointer(w))

	switch attrib {
	case glwAttribIntMin:
		if s.min == value {
			return 0
		}
		s.min = value
		s.stepI = s.step / (s.max - s.min)

	case glwAttribIntMax:
		if s.max == value {
			return 0
		}
		s.max = value
		s.stepI = s.step / (s.max - s.min)

	case glwAttribIntStep:
		if s.step == value {
			return 0
		}
		s.step = value
		s.stepI = s.step / (s.max - s.min)

	default:
		return -1
	}
	return 1
}

// C: glw_slider_bind_id
func glwSliderBindID(w *Glw, id string) int {
	s := (*glwSlider)(unsafe.Pointer(w))

	sliderUnbind(s)
	sliderBindByID(s, id)
	return 1
}

// C: glw_slider_set_int_unresolved
func glwSliderSetIntUnresolved(w *Glw, a string, value int, gs *GlwStyle) int {
	s := (*glwSlider)(unsafe.Pointer(w))

	switch a {
	case "keyStep":
		s.keystep = uint8(value)
		return glwSetNoChange
	case "knobOverEdges":
		if s.knobOverEdges == uint8(value) {
			return glwSetNoChange
		}
		s.knobOverEdges = uint8(value)
		return glwSetRerenderRequired
	}
	return glwSetNotResponding
}

// C: glw_slider_set_prop
func glwSliderSetProp(w *Glw, attrib glwAttribute, p *propcore.Prop) int {
	gs := (*glwSlider)(unsafe.Pointer(w))

	switch attrib {
	case glwAttribTentativeValue:
		glwDeps.pm.RefDec(gs.tentativeOutput)
		gs.tentativeOutput = glwDeps.pm.RefInc(p)
		return 0
	default:
		return -1
	}
}

// C: glw_slider_set_float_unresolved
func glwSliderSetFloatUnresolved(w *Glw, a string, value float32, gs *GlwStyle) int {
	s := (*glwSlider)(unsafe.Pointer(w))

	if a == "secondBarValue" {
		if s.secondBarValue == value {
			return glwSetNoChange
		}
		s.secondBarValue = value
		return glwSetRerenderRequired
	}
	return glwSetNotResponding
}

// C: glw_class_t glw_slider_x / glw_slider_y — populated in init() because
// the layout/render funcs reference the class vars (init cycle otherwise).
var glwSliderXClass = &glwClass{gcName: "slider_x"}
var glwSliderYClass = &glwClass{gcName: "slider_y"}

func registerSlider() {
	newSlider := func(parent *Glw) *Glw {
		s := &glwSlider{}
		return &s.w
	}
	*glwSliderXClass = glwClass{
		gcName:               "slider_x",
		gcInstanceSize:       int(unsafe.Sizeof(glwSlider{})),
		gcNew:                newSlider,
		gcLayout:             glwSliderLayout,
		gcRender:             glwSliderRenderX,
		gcSetFloat:           glwSliderSetFloat,
		gcBindToId:           glwSliderBindID,
		gcCtor:               glwSliderCtor,
		gcSignalHandler:      glwSliderCallback,
		gcBindToProperty:     sliderBindToProperty,
		gcSendEvent:          glwSliderEventX,
		gcPointerEvent:       glwSliderPointerEvent,
		gcSetIntUnresolved:   glwSliderSetIntUnresolved,
		gcSetProp:            glwSliderSetProp,
		gcSetFloatUnresolved: glwSliderSetFloatUnresolved,
	}
	*glwSliderYClass = glwClass{
		gcName:             "slider_y",
		gcInstanceSize:     int(unsafe.Sizeof(glwSlider{})),
		gcNew:              newSlider,
		gcLayout:           glwSliderLayout,
		gcRender:           glwSliderRenderY,
		gcSetFloat:         glwSliderSetFloat,
		gcBindToId:         glwSliderBindID,
		gcCtor:             glwSliderCtor,
		gcSignalHandler:    glwSliderCallback,
		gcBindToProperty:   sliderBindToProperty,
		gcSendEvent:        glwSliderEventY,
		gcPointerEvent:     glwSliderPointerEvent,
		gcSetIntUnresolved: glwSliderSetIntUnresolved,
		gcSetProp:          glwSliderSetProp,
	}
	glwRegisterClass(glwSliderXClass)
	glwRegisterClass(glwSliderYClass)
}
