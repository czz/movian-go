package glw

// C: src/ui/glw/glw.c — canonical 1:1 port.
//
// Global singletons used by C (event_mgr, the prop context, gconf.*,
// runcontrol) map to package-level seams configured at init time, matching
// the established convention (GconfClipboardSet, EsSetPropDeps, ...).

import (
	"reflect"
	"unsafe"

	eventpkg "github.com/czz/movian-go/internal/event"
	propcore "github.com/czz/movian-go/internal/prop"
	tracepkg "github.com/czz/movian-go/internal/trace"
)

// C: event_sprint (event.c)
func eventSprint(e *eventpkg.Event) string {
	if glwDeps.em == nil {
		return ""
	}
	return glwDeps.em.Sprint(e)
}

// C: static void glw_signal_handler_clean (glw.c:596-604)
func glwSignalHandlerClean(w *Glw) {
	var gsh *glwSignalHandler
	for gsh = w.glwSignalHandlers.lhFirst; gsh != nil; gsh = w.glwSignalHandlers.lhFirst {
		// C: LIST_REMOVE(gsh, gsh_link); free(gsh)
		if gsh.gshLinkNext != nil {
			gsh.gshLinkNext.gshLinkPrev = gsh.gshLinkPrev
		}
		*gsh.gshLinkPrev = gsh.gshLinkNext
	}
}

// C: void glw_signal_handler_register (glw.c:912-929)
func glwSignalHandlerRegister(w *Glw, fn glwCallback, opaque unsafe.Pointer) {
	for gsh := w.glwSignalHandlers.lhFirst; gsh != nil; gsh = gsh.gshLinkNext {
		// C: gsh->gsh_func == func — function pointer equality
		if gsh.gshFunc != nil && reflect.ValueOf(gsh.gshFunc).Pointer() ==
			reflect.ValueOf(fn).Pointer() && gsh.gshOpaque == opaque {
			return
		}
	}

	gsh := &glwSignalHandler{} // C: malloc(sizeof(glw_signal_handler_t))
	gsh.gshFunc = fn
	gsh.gshOpaque = opaque
	gsh.gshDeferRemove = 0

	// C: LIST_INSERT_HEAD(&w->glw_signal_handlers, gsh, gsh_link)
	gsh.gshLinkNext = w.glwSignalHandlers.lhFirst
	if gsh.gshLinkNext != nil {
		gsh.gshLinkNext.gshLinkPrev = &gsh.gshLinkNext
	}
	w.glwSignalHandlers.lhFirst = gsh
	gsh.gshLinkPrev = &w.glwSignalHandlers.lhFirst
}

// C: void glw_signal_handler_unregister (glw.c:934-953)
func glwSignalHandlerUnregister(w *Glw, fn glwCallback, opaque unsafe.Pointer) {
	var gsh *glwSignalHandler
	for gsh = w.glwSignalHandlers.lhFirst; gsh != nil; gsh = gsh.gshLinkNext {
		// C: gsh->gsh_func == func
		if gsh.gshFunc != nil && reflect.ValueOf(gsh.gshFunc).Pointer() ==
			reflect.ValueOf(fn).Pointer() && gsh.gshOpaque == opaque {
			break
		}
	}

	if gsh != nil {
		if gsh.gshDeferRemove != 0 {
			gsh.gshFunc = nil
			gsh.gshOpaque = nil
		} else {
			// C: LIST_REMOVE(gsh, gsh_link); free(gsh)
			if gsh.gshLinkNext != nil {
				gsh.gshLinkNext.gshLinkPrev = gsh.gshLinkPrev
			}
			*gsh.gshLinkPrev = gsh.gshLinkNext
		}
	}
}

// C: void glw_signal0 (glw.c:959-992)
func glwSignal0(w *Glw, sig glwSignal, extra any) {
	gsh := w.glwSignalHandlers.lhFirst

	if w.glwClass.gcSignalHandler != nil {
		w.glwClass.gcSignalHandler(w, nil, sig, extra)
	}

	glwViewEvalSignal(w, sig)

	for gsh != nil {
		if gsh.gshFunc != nil {
			gsh.gshDeferRemove = 1

			r := gsh.gshFunc(w, gsh.gshOpaque, sig, extra)

			if gsh.gshFunc == nil {
				// Was inteded to be removed during call
				x := gsh
				gsh = gsh.gshLinkNext
				// C: LIST_REMOVE(x, gsh_link); free(x)
				if x.gshLinkNext != nil {
					x.gshLinkNext.gshLinkPrev = x.gshLinkPrev
				}
				*x.gshLinkPrev = x.gshLinkNext
				continue
			}

			if r != 0 {
				return
			}
		}
		gsh = gsh.gshLinkNext
	}
}

// C: static int glw_root_event_handler (glw.c:1875-1898)
func glwRootEventHandler(gr *glwRoot, e *eventpkg.Event) int {
	if e.Type == eventpkg.EVENT_KEYDESC {
		return 0
	}

	if e.IsAction(eventpkg.ACTION_ENABLE_SCREENSAVER) {
		gr.grScreensaverForceEnable = 1

	} else if e.IsAction(eventpkg.ACTION_NAV_BACK) ||
		e.IsAction(eventpkg.ACTION_NAV_FWD) ||
		e.IsAction(eventpkg.ACTION_HOME) ||
		e.IsAction(eventpkg.ACTION_PLAYQUEUE) ||
		e.IsAction(eventpkg.ACTION_RELOAD_DATA) ||
		e.Type == eventpkg.EVENT_OPENURL {

		p := glwDeps.pm.GetByName([]string{"nav", "eventSink"}, 0, nil, nil,
			&propcore.PropRootNode{P: gr.grPropNav, Name: "nav"})
		glwDeps.pm.SendExtEvent(p, e)
		glwDeps.pm.RefDec(p)
	} else {
		e.AddRef()
		glwDeps.em.Dispatch(e)
	}
	return 0
}

// C: int glw_event_to_widget (glw.c:1904-1974)
func glwEventToWidget(w *Glw, e *eventpkg.Event) int {
	gr := w.glwRoot

	// First, descend in the view hierarchy

	glwTrace("Event '%s' route start at widget '%s'%s",
		eventSprint(e), glwGetName(w),
		map[bool]string{true: ", Nothing is focused", false: ""}[gr.grCurrentFocus == nil])

	if glwEventMapIntercept(w, e, 1) != 0 {
		// glw_event_map_intercept() does GLW_TRACE() by itself
		return 1
	}

	for {
		if glwPathInFocus(w) == 0 {
			break
		}

		if w.glwFlags2&glw2PositionalNavigation != 0 &&
			glwNavigateMatrix(w, e) != 0 {
			glwTrace("Event '%s' intercepted by matrix-nav at '%s' (descending)",
				eventSprint(e), glwGetName(w))
			return 1
		}

		if glwSendEvent2(w, e) {
			glwTrace("Event '%s' intercepted by widget '%s' (descending)",
				eventSprint(e), glwGetName(w))
			return 1
		}

		if w.glwFocused == nil {
			break
		}

		w = w.glwFocused
		if glwEventMapIntercept(w, e, 1) != 0 {
			return 1
		}
	}

	// Then ascend all the way up to root

	glwTrace("Event '%s' bounced at widget '%s'",
		eventSprint(e), glwGetName(w))

	for w != nil {
		w.glwFlags &^= glwFloatingFocus // Correct ??

		if glwEventMapIntercept(w, e, 0) != 0 {
			return 1
		}

		if glwBubbleEvent2(w, e) {
			glwTrace("Event '%s' intercepted by widget '%s' (ascending)",
				eventSprint(e), glwGetName(w))
			return 1
		}
		w = w.glwParent
	}

	glwTrace("Event '%s' relayed to root handler", eventSprint(e))

	// Nothing grabbed the event, default it

	return glwRootEventHandler(gr, e)
}

// C: static int glw_event (glw.c:1980-1997)
// glwWebFocusGrab — while the embedded web surface is focused it
// owns the whole input domain: Tab/shift-Tab are page navigation,
// not Movian focus moves. Set by WebsurfaceShow/Hide (cef builds).
var glwWebFocusGrab *Glw

func glwEvent(gr *glwRoot, e *eventpkg.Event) int {
	if gr.grCurrentFocus != nil {
		if e.IsAction(eventpkg.ACTION_FOCUS_NEXT) {
			if gr.grCurrentFocus == glwWebFocusGrab {
				return glwEventToWidget(gr.grUniverse, e)
			}
			glwFocusCrawl(gr.grCurrentFocus, 1, 1)
			return 1
		}
		if e.IsAction(eventpkg.ACTION_FOCUS_PREV) {
			if gr.grCurrentFocus == glwWebFocusGrab {
				return glwEventToWidget(gr.grUniverse, e)
			}
			glwFocusCrawl(gr.grCurrentFocus, 0, 1)
			return 1
		}
	}
	return glwEventToWidget(gr.grUniverse, e)
}

// C: int glw_pointer_event_deliver (glw.c:2003-2054)
func glwPointerEventDeliver(w *Glw, gpe *glwPointerEventT) int {
	gr := w.glwRoot

	if glwSendPointerEvent(w, gpe) {
		return 1
	}

	if !glwIsFocusableOrClickable(w) {
		return 0
	}

	var r int
	var flags int
	switch gpe.typ {

	case glwPointerRightPress:
		e := glwDeps.em.CreateAction(eventpkg.ACTION_ITEMMENU).AsEvent()
		e.Flags |= eventpkg.EventMouse | eventpkg.EventScreenPosition
		e.ScreenX = gpe.screenX
		e.ScreenY = gpe.screenY
		r = glwEventToWidget(w, e)
		e.Release()
		return r

	case glwPointerTouchStart:
		gr.grPointerPressTime = gr.grFrameStart
		fallthrough
	case glwPointerLeftPress:
		gr.grPointerPress = w
		glwPathModify(w, glwInPressedPath, 0, nil)
		return 1

	case glwPointerLeftRelease:
		flags = eventpkg.EventMouse
		fallthrough
	case glwPointerTouchEnd:
		if gr.grPointerPress == w {
			if w.glwFlags2&glw2FocusOnClick != 0 {
				glwFocusSet(gr, w, glwFocusSetInteractive, "LeftPress")
			}

			glwPathModify(w, 0, glwInPressedPath, nil)
			e := glwDeps.em.CreateActionMulti([]eventpkg.ActionType{
				eventpkg.ACTION_CLICK, eventpkg.ACTION_ACTIVATE}).AsEvent()
			e.Flags |= flags | eventpkg.EventScreenPosition
			e.ScreenX = gpe.screenX
			e.ScreenY = gpe.screenY
			glwEventToWidget(w, e)
			e.Release()
			gr.grPointerPress = nil
		}
		return 1

	default:
	}
	return 0
}

// C: static void glw_touch_longpress (glw.c:2060-2071)
func glwTouchLongpress(gr *glwRoot) {
	gr.grPointerPressTime = 0
	w := gr.grPointerPress
	e := glwDeps.em.CreateAction(eventpkg.ACTION_ITEMMENU).AsEvent()
	r := glwEventToWidget(w, e)
	e.Release()
	if r != 0 {
		glwPathModify(w, 0, glwInPressedPath, nil)
		gr.grPointerPress = nil
	}
}

// C: int glw_pointer_event0 (glw.c:2077-2128)
func glwPointerEvent0(gr *glwRoot, w *Glw, gpe *glwPointerEventT,
	hoverp **Glw, p Vec3, dir Vec3) int {
	var gpe0 *glwPointerEventT
	r := 0

	if w.glwFlags&(glwFocusBlocked|glwClipped|glwHidden) != 0 {
		return 0
	}

	if w.glwMatrix != nil {

		var x, y float32
		ur := glwWidgetUnproject(w.glwMatrix, &x, &y, p, dir)
		if ur != 0 &&
			x <= 1 && y <= 1 && x >= -1 && y >= -1 {
			var tmp glwPointerEventT
			gpe0 = &tmp

			gc := w.glwClass
			*gpe0 = *gpe
			gpe0.localX = x
			gpe0.localY = y

			if gc.gcPointerEventFilter != nil {
				if gc.gcPointerEventFilter(w, gpe0) != 0 {
					return 1
				}
			}

			if gpe.typ < glwPointerMotionUpdate {
				r = 1
			}

			if glwIsFocusableOrClickable(w) {
				*hoverp = w
			}

		} else {
			return 0
		}
	}

	// C: TAILQ_FOREACH_REVERSE — iterate children tail to head
	for c := glwTAILQPrevSlot(w.glwChilds.tqhLast); c != nil; c = glwTAILQPrev(c) {
		if glwPointerEvent0(gr, c, gpe, hoverp, p, dir) != 0 {
			return 1
		}
	}

	if gpe0 == nil {
		return 0
	}

	return glwPointerEventDeliver(w, gpe0) | r
}

// C: void glw_pointer_event (glw.c:2135-2282)
func glwPointerEvent(gr *glwRoot, gpe *glwPointerEventT) {
	var gpe0 glwPointerEventT
	var x, y float32
	hover := gr.grPointerGrab

	if glwDeps.gconf.ConvertPointerToTouch != 0 {
		switch gpe.typ {
		case glwPointerLeftPress:
			gr.grLeftPressed = 1
			gpe.typ = glwPointerTouchStart

		case glwPointerLeftRelease:
			gpe.typ = glwPointerTouchEnd
			gr.grLeftPressed = 0

		case glwPointerMotionUpdate:
			if gr.grLeftPressed != 0 {
				gpe.typ = glwPointerTouchMove
				break
			}
			return

		default:
		}
	}

	p := glwVec3Make(gpe.screenX, gpe.screenY, -2.41)
	var dir Vec3
	glwVec3Sub(&dir, &p, &Vec3{gpe.screenX * 42.38,
		gpe.screenY * 42.38, -100})

	if gpe.typ != glwPointerMotionRefresh &&
		gpe.typ != glwPointerGone {
		if glwDeps.runcontrolActivity != nil {
			glwDeps.runcontrolActivity()
		}
		glwRegisterActivity(gr)
		gr.grScreensaverForceEnable = 0
		glwSetKeyboardMode(gr, 0)
	}

	if gpe.typ == glwPointerTouchStart {
		gr.grTouchStartX = gpe.screenX
		gr.grTouchStartY = gpe.screenY
		if gr.grTouchMode == 0 {
			glwDeps.ts.Trace(tracepkg.TRACE_DEBUG, "GLW", "Operating in touch mode")
			gr.grTouchMode = 1
			gr.grPropUi.CreateInt("touch", 1) // C: prop_set touch PROP_SET_INT
		}
	}

	if gpe.typ == glwPointerTouchMove {
		gr.grTouchMoveX = gpe.screenX
		gr.grTouchMoveY = gpe.screenY
	}

	if gpe.typ == glwPointerTouchEnd {
		gr.grTouchEndX = gpe.screenX
		gr.grTouchEndY = gpe.screenY
	}

	/* If a widget has grabbed to pointer (such as when holding the button
	   on a slider), dispatch events there */

	if gpe.screenX != gr.grMouseX || gpe.screenY != gr.grMouseY {
		gr.grMouseX = gpe.screenX
		gr.grMouseY = gpe.screenY
		gr.grMouseValid = 1

		if gpe.typ == glwPointerMotionUpdate ||
			gpe.typ == glwPointerTouchMove ||
			gpe.typ == glwPointerMotionRefresh {

			if gpe.typ == glwPointerMotionUpdate {
				gr.grPointerVisible.SetInt(1)
			}

			var w *Glw
			if w = gr.grPointerGrab; w != nil && w.glwMatrix != nil {
				glwWidgetUnproject(w.glwMatrix, &x, &y, p, dir)
				gpe0 = *gpe
				gpe0.typ = glwPointerFocusMotion
				gpe0.localX = x
				gpe0.localY = y
				glwSendPointerEvent(w, &gpe0)
			} else if w = gr.grPointerGrabScroll; w != nil && w.glwMatrix != nil {
				glwWidgetUnproject(w.glwMatrix, &x, &y, p, dir)
				gpe0 = *gpe
				gpe0.typ = glwPointerFocusMotion
				gpe0.localX = x
				gpe0.localY = y
				glwSendPointerEvent(w, &gpe0)
			} else if w = gr.grPointerPress; w != nil && w.glwMatrix != nil {

				lossPress := 0

				if glwWidgetUnproject(w.glwMatrix, &x, &y, p, dir) == 0 ||
					x < -1 || y < -1 || x > 1 || y > 1 {
					lossPress = 1
				}

				if lossPress != 0 {
					// Moved outside button, release
					glwPathModify(w, 0, glwInPressedPath, nil)
					gr.grPointerPress = nil
				}
			}
		}
	}

	if (gpe.typ == glwPointerLeftRelease ||
		gpe.typ == glwPointerTouchEnd) && gr.grPointerGrab != nil {
		w := gr.grPointerGrab
		glwWidgetUnproject(w.glwMatrix, &x, &y, p, dir)
		gpe0 = *gpe
		gpe0.localX = x
		gpe0.localY = y

		glwSendPointerEvent(w, &gpe0)
		gr.grPointerGrab = nil
		return
	}

	if gpe.typ == glwPointerGone {
		// Mouse pointer left our screen
		glwRootSetHover(gr, nil)
		gr.grMouseValid = 0
		gr.grPointerVisible.SetInt(0)
		return
	}

	for c := gr.grUniverse.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		if glwPointerEvent0(gr, c, gpe, &hover, p, dir) != 0 {
			break
		}
	}

	if gr.grTouchMode != 0 {
		return
	}

	if gpe.typ == glwPointerMotionUpdate ||
		gpe.typ == glwPointerMotionRefresh {
		glwRootSetHover(gr, hover)
	}
}

// C: static void glw_dispatch_event (glw.c:2414-2521)
func glwDispatchEvent(gr *glwRoot, e *eventpkg.Event) {
	if glwDeps.runcontrolActivity != nil {
		glwDeps.runcontrolActivity()
	}

	if gr.grOskWidget != nil {
		if e.Type == eventpkg.EVENT_INSERT_STRING ||
			e.IsAction(eventpkg.ACTION_ENTER) ||
			e.IsAction(eventpkg.ACTION_BS) {
			glwEventToWidget(gr.grOskWidget, e)
			return
		}
	}

	if e.Type == eventpkg.EVENT_REPAINT_UI {
		glwTextFlush(gr)
		return
	}

	if e.Type == eventpkg.EVENT_MAKE_SCREENSHOT {
		glwScreenshot(gr)
		return
	}

	// C: #if CONFIG_GLW_REC — event_is_action(e, ACTION_RECORD_UI)
	//    (glw.c:2441-2445); glwRecIsRecordAction returns false without the
	//    "glwrec" build tag so the event falls through like C.
	if glwRecIsRecordAction(e) {
		glwRecToggle(gr)
		return
	}

	if e.Type == eventpkg.EVENT_KEYDESC {

		if glwEvent(gr, e) != 0 {
			return // Was consumed
		}

		return
	}

	if !(e.IsAction(eventpkg.ACTION_SEEK_BACKWARD) ||
		e.IsAction(eventpkg.ACTION_SEEK_FORWARD) ||
		e.IsAction(eventpkg.ACTION_PLAYPAUSE) ||
		e.IsAction(eventpkg.ACTION_PLAY) ||
		e.IsAction(eventpkg.ACTION_PAUSE) ||
		e.IsAction(eventpkg.ACTION_STOP) ||
		e.IsAction(eventpkg.ACTION_EJECT) ||
		e.IsAction(eventpkg.ACTION_SKIP_BACKWARD) ||
		e.IsAction(eventpkg.ACTION_SKIP_FORWARD) ||
		e.IsAction(eventpkg.ACTION_SHOW_MEDIA_STATS) ||
		e.IsAction(eventpkg.ACTION_SHUFFLE) ||
		e.IsAction(eventpkg.ACTION_REPEAT) ||
		e.IsAction(eventpkg.ACTION_NEXT_CHANNEL) ||
		e.IsAction(eventpkg.ACTION_PREV_CHANNEL) ||
		e.IsAction(eventpkg.ACTION_VOLUME_UP) ||
		e.IsAction(eventpkg.ACTION_VOLUME_DOWN) ||
		e.IsAction(eventpkg.ACTION_VOLUME_MUTE_TOGGLE) ||
		e.IsAction(eventpkg.ACTION_POWER_OFF) ||
		e.IsAction(eventpkg.ACTION_RESTART) ||
		e.IsAction(eventpkg.ACTION_STANDBY) ||
		e.Type == eventpkg.EVENT_SELECT_AUDIO_TRACK ||
		e.Type == eventpkg.EVENT_SELECT_SUBTITLE_TRACK) {

		if e.Flags&eventpkg.EventKeypress != 0 {
			if glwSetKeyboardMode(gr, 1) != 0 {
				/*
				   Ok, we switched form mouse to keyboard mode.
				   For some events we don't want to actually execute on the
				   action but rather "use" the event to do the switch
				*/

				if e.IsAction(eventpkg.ACTION_UP) ||
					e.IsAction(eventpkg.ACTION_DOWN) ||
					e.IsAction(eventpkg.ACTION_LEFT) ||
					e.IsAction(eventpkg.ACTION_RIGHT) {
					return
				}
			}
		}
		if glwKillScreensaver(gr) != 0 {
			if e.Flags&eventpkg.EventKeypress != 0 {
				return
			}
		}
	}

	if e.IsAction(eventpkg.ACTION_RELOAD_UI) {
		glwLoadUniverse(gr)
		return

	} else if e.IsAction(eventpkg.ACTION_ZOOM_UI_INCR) {

		GlwSettingsAdjSize(1)
		return

	} else if e.IsAction(eventpkg.ACTION_ZOOM_UI_DECR) {

		GlwSettingsAdjSize(-1)
		return

	} else if e.IsAction(eventpkg.ACTION_ZOOM_UI_RESET) {

		GlwSettingsAdjSize(0)
		return

	}

	glwEvent(gr, e)
}

// C: static void glw_eventsink (glw.c:2527-2543)
func glwEventsink(opaque any, event propcore.EventType, args ...any) {
	gr := opaque.(*glwRoot)

	switch event {
	case propcore.EventExtEvent:
		if e, ok := args[0].(*eventpkg.Event); ok {
			glwDispatchEvent(gr, e)
		}
	}
}

// C: void glw_inject_event (glw.c:2549-2571)
func glwInjectEvent(gr *glwRoot, e *eventpkg.Event) {
	var p *propcore.Prop

	if gr.grCurrentFocus == nil &&
		(e.IsAction(eventpkg.ACTION_NAV_BACK) ||
			e.IsAction(eventpkg.ACTION_NAV_FWD) ||
			e.IsAction(eventpkg.ACTION_HOME) ||
			e.IsAction(eventpkg.ACTION_PLAYQUEUE) ||
			e.IsAction(eventpkg.ACTION_RELOAD_DATA) ||
			e.Type == eventpkg.EVENT_OPENURL) {
		p = glwDeps.pm.GetByName([]string{"nav", "eventSink"}, 0, nil, nil,
			&propcore.PropRootNode{P: gr.grPropNav, Name: "nav"})
	} else {
		p = glwDeps.pm.GetByName([]string{"ui", "eventSink"}, 0, nil, nil,
			&propcore.PropRootNode{P: gr.grPropUi, Name: "nav"})
	}
	glwDeps.pm.SendExtEvent(p, e)
	e.Release()
	glwDeps.pm.RefDec(p)
}

// C: static void glw_osk_text (glw.c:3139-3147)
func glwOskText(opaque any, str string) {
	gr := opaque.(*glwRoot)
	w := gr.grOskWidget
	if w != nil {
		w.glwClass.gcUpdateText(w, str)
	}
}

// C: void glw_osk_close (glw.c:3153-3158)
func glwOskClose(gr *glwRoot) {
	glwUnref(gr.grOskWidget)
	gr.grOskWidget = nil
}

// C: static void glw_osk_done (glw.c:3164-3185)
func glwOskDone(gr *glwRoot, submit int) {
	w := gr.grOskWidget
	if w != nil {
		if submit != 0 {
			e := glwDeps.em.CreateAction(eventpkg.ACTION_SUBMIT).AsEvent()
			glwEventToWidget(w, e)
			e.Release()
		} else {
			w := gr.grOskWidget
			w.glwClass.gcUpdateText(w, gr.grOskRevert)
		}

		glwDeps.pm.Unsubscribe(gr.grOskTextSub)
		glwDeps.pm.Unsubscribe(gr.grOskEvSub)

		gr.grOskTextSub = nil
		gr.grOskEvSub = nil
		glwOskClose(gr)
	}

	osk := glwDeps.pm.CreateEx(gr.grPropUi, "osk", nil, false, false)
	osk.CreateInt("show", 0) // C: prop_set(osk, "show", PROP_SET_INT, 0)
}

// C: static void glw_osk_event (glw.c:3191-3205)
func glwOskEvent(opaque any, event propcore.EventType, args ...any) {
	if event != propcore.EventExtEvent {
		return
	}

	e, _ := args[0].(*eventpkg.Event)

	if e.IsAction(eventpkg.ACTION_OK) {
		glwOskDone(opaque.(*glwRoot), 1)
	} else if e.IsAction(eventpkg.ACTION_CANCEL) {
		glwOskDone(opaque.(*glwRoot), 0)
	}
}

// C: void glw_osk_open (glw.c:3211-3223)
func glwOskOpen(gr *glwRoot, title, input string, w *Glw, password int) {
	gr.grOskRevert = input // C: mystrset

	if gr.grOskWidget != nil {
		glwUnref(gr.grOskWidget)
	}

	gr.grOskWidget = w
	glwRef(w)

	gr.grOpenOsk(gr, title, input, w, password)
}

// C: static void glw_osk_open_default (glw.c:3229-3269)
func glwOskOpenDefault(gr *glwRoot, title, input string, w *Glw, password int) {
	osk := glwDeps.pm.CreateEx(gr.grPropUi, "osk", nil, false, false)

	glwDeps.pm.Unsubscribe(gr.grOskTextSub)
	glwDeps.pm.Unsubscribe(gr.grOskEvSub)

	osk.CreateString("title", title)    // C: PROP_SET_STRING
	osk.CreateString("text", input)     // C: PROP_SET_STRING
	osk.CreateInt("password", password) // C: PROP_SET_INT
	osk.CreateInt("show", 1)            // C: PROP_SET_INT

	gr.grOskTextSub = glwPropSubscribeTags(0,
		propTagCallbackString, glwOskText, gr,
		propTagName, []string{"ui", "osk", "text"},
		propTagRoot, gr.grPropUi,
		propTagCourier, gr.grCourier,
	)

	gr.grOskEvSub = glwPropSubscribeTags(0,
		propTagCallback, glwOskEvent, gr,
		propTagName, []string{"ui", "osk", "eventSink"},
		propTagRoot, gr.grPropUi,
		propTagCourier, gr.grCourier,
	)
}
