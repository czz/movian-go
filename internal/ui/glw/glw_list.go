package glw

// Canonical port of src/ui/glw/glw_list.c — the `list_y` and `list_x`
// widget classes (scrollable 1-D lists).

import (
	"math"
	"unsafe"

	eventpkg "github.com/czz/movian-go/internal/event"
)

// C: glw_list_t
type glwList struct {
	w Glw

	savedHeight int16
	savedWidth  int16
	spacing     int16

	padding [4]int16

	gsc glwScrollControl
}

// C: glw_list_item_t — per-child parent data
type glwListItem struct {
	pos    float32
	height int16
	width  int16
	inst   uint8 // C: char
}

func listItemData(w *Glw) *glwListItem {
	return w.glwParentData.(*glwListItem)
}

// C: glw_list_layout_y
func glwListLayoutY(w *Glw, rc *glwRctx) {
	l := (*glwList)(unsafe.Pointer(w))
	var c *Glw
	var ypos int

	rc0 := *rc

	glwReposition(&rc0, int(l.padding[0]), int(rc.rcHeight)-int(l.padding[1]),
		int(rc.rcWidth)-int(l.padding[2]), int(l.padding[3]))

	bottomScrollPos := int(rc0.rcHeight) - l.gsc.scrollThresholdPost

	if l.savedHeight != rc0.rcHeight {
		l.savedHeight = rc0.rcHeight
		l.gsc.pageSize = int(rc0.rcHeight)
		l.w.glwFlags |= glwUpdateMetrics

		if w.glwFocused != nil {
			l.gsc.scrollToMe = w.glwFocused
		}
	}

	if l.gsc.scrollToMe != nil {
		ypos = l.gsc.scrollThresholdPre
		for c = w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
			if c.glwFlags&glwHidden != 0 {
				continue
			}

			f := glwFilterConstraints(c)
			var height int

			if f&glwConstraintY != 0 {
				height = glwReqHeight(c)
			} else {
				height = int(rc0.rcWidth) / 10
			}

			if c == l.gsc.scrollToMe {
				screenPos := int(float32(ypos) - l.gsc.roundedPos)
				if screenPos < l.gsc.scrollThresholdPre {
					l.gsc.targetPos = ypos - l.gsc.scrollThresholdPre
					if glwIsFocused(w) {
						l.w.glwFlags |= glwUpdateMetrics
					}
					glwScheduleRefresh(w.glwRoot, 0)
				} else if screenPos+height > bottomScrollPos {
					l.gsc.targetPos = ypos + height - bottomScrollPos
					if glwIsFocused(w) {
						l.w.glwFlags |= glwUpdateMetrics
					}
					glwScheduleRefresh(w.glwRoot, 0)
				}
			}

			ypos += height
			ypos += int(l.spacing)
		}
		l.gsc.scrollToMe = nil
	}

	glwScrollLayout(&l.gsc, w, int(rc.rcHeight))

	ypos = l.gsc.scrollThresholdPre
	for c = w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		if c.glwFlags&glwHidden != 0 {
			continue
		}

		f := glwFilterConstraints(c)

		if f&glwConstraintY != 0 {
			rc0.rcHeight = int16(glwReqHeight(c))
		} else {
			rc0.rcHeight = rc0.rcWidth / 10
		}

		cd := listItemData(c)

		if cd.inst != 0 {
			cd.pos = float32(ypos)
			cd.inst = 0
		} else {
			glwLp(&cd.pos, w.glwRoot, float32(ypos), 0.25)
		}

		cd.height = rc0.rcHeight

		if float32(ypos)-l.gsc.roundedPos > float32(-int(rc.rcHeight)) &&
			float32(ypos)-l.gsc.roundedPos < float32(int(rc.rcHeight)*2) {
			glwLayout0(c, &rc0)
		}

		ypos += int(rc0.rcHeight)
		ypos += int(l.spacing)
	}

	if l.gsc.totalSize != ypos {
		l.gsc.totalSize = ypos
		l.w.glwFlags |= glwUpdateMetrics
	}

	if l.w.glwFlags&glwUpdateMetrics != 0 {
		glwScrollUpdateMetrics(&l.gsc, w)
	}
}

// C: glw_list_layout_x
func glwListLayoutX(w *Glw, rc *glwRctx) {
	l := (*glwList)(unsafe.Pointer(w))
	var c *Glw
	xpos := l.gsc.scrollThresholdPre
	rc0 := *rc

	glwReposition(&rc0, int(l.padding[0]), int(rc.rcHeight)-int(l.padding[1]),
		int(rc.rcWidth)-int(l.padding[2]), int(l.padding[3]))
	width0 := int(rc0.rcWidth) -
		l.gsc.scrollThresholdPre -
		l.gsc.scrollThresholdPost

	if l.savedWidth != rc0.rcWidth {
		l.savedWidth = rc0.rcWidth
		l.gsc.pageSize = int(rc0.rcWidth)
		l.w.glwFlags |= glwUpdateMetrics

		if w.glwFocused != nil {
			l.gsc.scrollToMe = w.glwFocused
		}
	}

	mx := l.gsc.totalSize - l.gsc.pageSize
	if l.gsc.targetPos > mx {
		l.gsc.targetPos = mx
	}
	if l.gsc.targetPos < 0 {
		l.gsc.targetPos = 0
	}

	if math.Abs(float64(float32(l.gsc.targetPos)-l.gsc.filteredPos)) > float64(int(rc.rcWidth)*2) {
		l.gsc.filteredPos = float32(l.gsc.targetPos)
	} else {
		glwLp(&l.gsc.filteredPos, w.glwRoot, float32(l.gsc.targetPos), 0.25)
	}

	l.gsc.roundedPos = l.gsc.filteredPos

	for c = w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		if c.glwFlags&glwHidden != 0 {
			continue
		}

		f := glwFilterConstraints(c)

		if f&glwConstraintX != 0 {
			rc0.rcWidth = int16(glwReqWidth(c))
		} else {
			rc0.rcWidth = rc0.rcHeight
		}

		cd := listItemData(c)

		cd.pos = float32(xpos)
		cd.width = rc0.rcWidth

		if float32(xpos)-l.gsc.roundedPos > float32(-width0) &&
			float32(xpos)-l.gsc.roundedPos < float32(width0*2) {
			glwLayout0(c, &rc0)
		}

		if c == l.gsc.scrollToMe {
			l.gsc.scrollToMe = nil
			if float32(xpos)-l.gsc.roundedPos < float32(l.gsc.scrollThresholdPre) {
				l.gsc.targetPos = xpos - l.gsc.scrollThresholdPre
				if glwIsFocused(w) {
					l.w.glwFlags |= glwUpdateMetrics
				}
			} else if float32(xpos)-l.gsc.roundedPos+float32(rc0.rcWidth) > float32(width0) {
				l.gsc.targetPos = int(float32(xpos) + float32(rc0.rcWidth) - float32(width0))
				if glwIsFocused(w) {
					l.w.glwFlags |= glwUpdateMetrics
				}
			}
		}

		xpos += int(rc0.rcWidth)
		xpos += int(l.spacing)
	}

	xpos += l.gsc.scrollThresholdPost

	if l.gsc.totalSize != xpos {
		l.gsc.totalSize = xpos
		l.w.glwFlags |= glwUpdateMetrics
	}

	if l.w.glwFlags&glwUpdateMetrics != 0 {
		glwScrollUpdateMetrics(&l.gsc, w)
	}
}

// C: glw_list_y_render_one
func glwListYRenderOne(l *glwList, c *Glw, width, height int,
	rc0, rc1 *glwRctx, clipTop, clipBottom int) {
	cd := listItemData(c)

	var ct, cb int = -1, -1
	gr := l.w.glwRoot
	y := cd.pos - l.gsc.roundedPos
	var rc2 glwRctx

	if y+float32(cd.height) < 0 || y > float32(height) {
		c.glwFlags |= glwClipped
		return
	}
	c.glwFlags &^= glwClipped

	if y < float32(clipTop) {
		k := float32(clipTop) / float32(rc0.rcHeight)
		ct = glwClipEnable(gr, rc0, glwClipTop, k,
			l.gsc.clipAlpha, 1.0-l.gsc.clipBlur)
	}

	if y+float32(cd.height) > float32(int(rc0.rcHeight)-clipBottom) {
		k := float32(clipBottom) / float32(rc0.rcHeight)
		cb = glwClipEnable(gr, rc0, glwClipBottom, k,
			l.gsc.clipAlpha, 1.0-l.gsc.clipBlur)
	}

	rc2 = *rc1
	glwReposition(&rc2,
		0,
		int(float32(height)-cd.pos),
		width,
		int(float32(height)-cd.pos-float32(cd.height)))

	glwRender0(c, &rc2)

	if ct != -1 {
		glwClipDisable(gr, ct)
	}
	if cb != -1 {
		glwClipDisable(gr, cb)
	}
}

// C: glw_list_render_y
func glwListRenderY(w *Glw, rc *glwRctx) {
	var c *Glw
	l := (*glwList)(unsafe.Pointer(w))
	var rc0, rc1 glwRctx

	rc0 = *rc
	rc0.rcAlpha *= w.glwAlpha

	glwReposition(&rc0, int(l.padding[0]), int(rc.rcHeight)-int(l.padding[1]),
		int(rc.rcWidth)-int(l.padding[2]), int(l.padding[3]))

	glwStoreMatrix(w, &rc0)

	rc1 = rc0
	glwReposition(&rc1, 0,
		int(rc1.rcHeight)-int(l.gsc.clipOffsetPre),
		int(rc1.rcWidth),
		int(l.gsc.clipOffsetPost))

	glwStoreMatrix(w, &rc1)

	if rc.rcAlpha < glwAlphaEpsilon {
		return
	}

	clipTop := int(l.gsc.clipOffsetPre) - 1
	clipBottom := int(l.gsc.clipOffsetPost) - 1

	rc1 = rc0
	glwTranslatef(&rc1, 0, 2.0*l.gsc.roundedPos/float32(rc0.rcHeight), 0)

	for c = w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		if c.glwFlags&glwHidden != 0 {
			continue
		}
		glwListYRenderOne(l, c, int(rc0.rcWidth), int(rc0.rcHeight), &rc0, &rc1,
			clipTop, clipBottom)
	}
}

// C: glw_list_render_x
func glwListRenderX(w *Glw, rc *glwRctx) {
	var c *Glw
	l := (*glwList)(unsafe.Pointer(w))
	var rc0, rc1, rc2 glwRctx
	var lc, rclip int

	rc0 = *rc
	rc0.rcAlpha *= w.glwAlpha

	glwReposition(&rc0, int(l.padding[0]), int(rc.rcHeight)-int(l.padding[1]),
		int(rc.rcWidth)-int(l.padding[2]), int(l.padding[3]))

	height := int(rc0.rcHeight)
	width := int(rc0.rcWidth)

	glwStoreMatrix(w, &rc0)

	if rc.rcAlpha < glwAlphaEpsilon {
		return
	}

	rc1 = rc0

	glwTranslatef(&rc1, -2.0*l.gsc.roundedPos/float32(width), 0, 0)

	for c = w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		if c.glwFlags&glwHidden != 0 {
			continue
		}

		cd := listItemData(c)

		x := cd.pos - l.gsc.roundedPos
		if x+float32(cd.width) < 0 || x > float32(width) {
			c.glwFlags |= glwClipped
			continue
		}
		c.glwFlags &^= glwClipped

		lc, rclip = -1, -1

		if x < 0 {
			lc = glwClipEnable(w.glwRoot, &rc0, glwClipLeft, 0, 0, 1)
		}

		if x+float32(cd.width) > float32(width) {
			rclip = glwClipEnable(w.glwRoot, &rc0, glwClipRight, 0, 0, 1)
		}

		rc2 = rc1
		glwReposition(&rc2,
			int(cd.pos),
			height,
			int(cd.pos+float32(cd.width)),
			0)

		glwRender0(c, &rc2)

		if lc != -1 {
			glwClipDisable(w.glwRoot, lc)
		}
		if rclip != -1 {
			glwClipDisable(w.glwRoot, rclip)
		}
	}
}

// C: glw_list_find_visible_child — used when scrolling to maintain focus
func glwListFindVisibleChild(w *Glw) *Glw {
	l := (*glwList)(unsafe.Pointer(w))
	top := l.gsc.targetPos + l.gsc.scrollThresholdPre
	bottom := l.gsc.targetPos + l.gsc.pageSize - l.gsc.scrollThresholdPre
	c := l.w.glwFocused

	if c == nil {
		return nil
	}

	if listItemData(c).pos < float32(top) {
		for c != nil && listItemData(c).pos < float32(top) {
			c = glwNextWidget(c)
		}
		if c != nil && glwGetFocusableChild(c) == nil {
			c = glwNextWidget(c)
		}
	} else if listItemData(c).pos > float32(bottom) {
		for c != nil && listItemData(c).pos > float32(bottom) {
			c = glwPrevWidget(c)
		}
		if c != nil && glwGetFocusableChild(c) == nil {
			c = glwPrevWidget(c)
		}
	}
	return c
}

// C: glw_list_scroll
func glwListScroll(l *glwList, gs *glwScroll) {
	glwScrollHandleScroll(&l.gsc, &l.w, gs)
}

// C: scroll_to_me — helper to make sure we can show items in list that are
// not focusable even if they are at the top
func listScrollToMe(l *glwList, c *Glw) {
	d, e := c, c
	if c == nil {
		return
	}

	for {
		d = glwTAILQPrev(d)
		if d == nil {
			c = e
			break
		}
		if d.glwFlags&glwHidden != 0 {
			continue
		}
		if glwIsChildFocusable(d) != 0 {
			break
		}
		e = d
	}
	l.gsc.scrollToMe = c
	glwScheduleRefresh(l.w.glwRoot, 0)
}

// C: glw_list_callback
func glwListCallback(w *Glw, opaque any, signal glwSignal, extra any) int {
	l := (*glwList)(unsafe.Pointer(w))

	switch signal {
	case glwSignalFocusChildInteractive:
		listScrollToMe(l, extra.(*Glw))
		l.gsc.suggestCnt = 0
		w.glwFlags &^= glwFloatingFocus
		return 0

	case glwSignalChildDestroyed:
		if l.gsc.scrollToMe == extra.(*Glw) {
			l.gsc.scrollToMe = nil
		}
		if l.gsc.suggested == extra.(*Glw) {
			l.gsc.suggested = nil
		}

		if extra.(*Glw) == w.glwChilds.tqhFirst &&
			glwNextWidget(extra.(*Glw)) == nil {
			// Last item went away, make sure to reset
			l.gsc.targetPos = 0
			l.gsc.filteredPos = 0
			w.glwFlags |= glwFloatingFocus
			l.gsc.suggestCnt = 1
		}

	case glwSignalScroll:
		glwListScroll(l, extra.(*glwScroll))

	case glwSignalChildCreated, glwSignalChildUnhidden:
		listItemData(extra.(*Glw)).inst = 1

	case glwSignalFhpPathChanged:
		if !glwIsFocused(w) {
			l.gsc.suggestCnt = 1
		}
	}
	return 0
}

// C: glw_list_set_int
func glwListSetInt(w *Glw, attrib glwAttribute, value int, gs *GlwStyle) int {
	l := (*glwList)(unsafe.Pointer(w))

	switch attrib {
	case glwAttribSpacing:
		if l.spacing == int16(value) {
			return 0
		}
		l.spacing = int16(value)
	default:
		return -1
	}
	return 1
}

// C: glw_list_ctor
func glwListCtor(w *Glw) {
	l := (*glwList)(unsafe.Pointer(w))
	l.gsc.suggestCnt = 1
	w.glwFlags |= glwFloatingFocus
}

// C: glw_list_suggest_focus
func glwListSuggestFocus(w *Glw, c *Glw) {
	l := (*glwList)(unsafe.Pointer(w))
	glwScrollSuggestFocus(&l.gsc, w, c)
}

// C: glw_list_set_int16_4
func glwListSetInt16_4(w *Glw, attrib glwAttribute, v []int16, gs *GlwStyle) int {
	l := (*glwList)(unsafe.Pointer(w))

	switch attrib {
	case glwAttribPadding:
		return glwAttribSetInt16_4(l.padding[:], v)
	default:
		return -1
	}
}

// C: glw_list_set_float_unresolved
func glwListSetFloatUnresolved(w *Glw, a string, value float32, gs *GlwStyle) int {
	l := (*glwList)(unsafe.Pointer(w))
	return glwScrollSetFloatAttributes(&l.gsc, a, value)
}

// C: glw_list_set_int_unresolved
func glwListSetIntUnresolved(w *Glw, a string, value int, gs *GlwStyle) int {
	l := (*glwList)(unsafe.Pointer(w))
	return glwScrollSetIntAttributes(&l.gsc, a, value)
}

// C: handle_pointer_event
func glwListPointerEvent(w *Glw, gpe *glwPointerEventT) int {
	l := (*glwList)(unsafe.Pointer(w))
	return glwScrollHandlePointerEvent(&l.gsc, w, gpe)
}

// C: handle_pointer_event_filter
func glwListPointerEventFilter(w *Glw, gpe *glwPointerEventT) int {
	l := (*glwList)(unsafe.Pointer(w))
	return glwScrollHandlePointerEventFilter(&l.gsc, w, gpe)
}

// C: glw_class_t glw_list_y / glw_list_x
var glwListYClass = &glwClass{
	gcName:           "list_y",
	gcInstanceSize:   int(unsafe.Sizeof(glwList{})),
	gcParentDataSize: int(unsafe.Sizeof(glwListItem{})),
	gcNewParentData:  func() any { return &glwListItem{} },
	gcFlags:          glwNavigationSearchBoundary | glwCanHideChilds | glwDrivePagination,
	gcNew: func(parent *Glw) *Glw {
		l := &glwList{}
		return &l.w
	},
	gcLayout:             glwListLayoutY,
	gcRender:             glwListRenderY,
	gcSetInt:             glwListSetInt,
	gcCtor:               glwListCtor,
	gcSignalHandler:      glwListCallback,
	gcSuggestFocus:       glwListSuggestFocus,
	gcSetInt16_4:         glwListSetInt16_4,
	gcPointerEvent:       glwListPointerEvent,
	gcPointerEventFilter: glwListPointerEventFilter,
	gcBubbleEvent: func(w *Glw, e *eventpkg.Event) int {
		return glwNavigateVertical(w, e)
	},
	gcSetIntUnresolved:   glwListSetIntUnresolved,
	gcSetFloatUnresolved: glwListSetFloatUnresolved,
	gcFindVisibleChild:   glwListFindVisibleChild,
}

var glwListXClass = &glwClass{
	gcName:           "list_x",
	gcInstanceSize:   int(unsafe.Sizeof(glwList{})),
	gcParentDataSize: int(unsafe.Sizeof(glwListItem{})),
	gcNewParentData:  func() any { return &glwListItem{} },
	gcFlags:          glwNavigationSearchBoundary | glwCanHideChilds | glwDrivePagination,
	gcNew: func(parent *Glw) *Glw {
		l := &glwList{}
		return &l.w
	},
	gcLayout:        glwListLayoutX,
	gcRender:        glwListRenderX,
	gcSetInt:        glwListSetInt,
	gcCtor:          glwListCtor,
	gcSignalHandler: glwListCallback,
	gcSuggestFocus:  glwListSuggestFocus,
	gcSetInt16_4:    glwListSetInt16_4,
	gcBubbleEvent: func(w *Glw, e *eventpkg.Event) int {
		return glwNavigateHorizontal(w, e)
	},
	gcSetIntUnresolved:   glwListSetIntUnresolved,
	gcSetFloatUnresolved: glwListSetFloatUnresolved,
}

func registerList() {
	glwRegisterClass(glwListYClass)
	glwRegisterClass(glwListXClass)
}
