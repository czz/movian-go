package glw

// Canonical port of src/ui/glw/glw_array.c — the `array` widget class
// (a scrollable grid of equally-sized tiles).

import (
	"math"
	"unsafe"

	eventpkg "github.com/czz/movian-go/internal/event"
)

// C: glw_array_t
type glwArray struct {
	w Glw

	metrics glwSliderMetrics

	childTilesX int
	childTilesY int

	childWidthPx  int
	childHeightPx int

	xentries int

	savedHeight int16
	savedWidth  int16

	xspacing int16
	yspacing int16

	scrollThreshold int16

	numVisibleChilds int

	gsc glwScrollControl
}

// C: glw_array_item_t — per-child parent data
type glwArrayItem struct {
	posY int
	posX int

	posFx float32
	posFy float32

	width  uint16
	height uint16

	justInserted uint8
	col          int8
}

func arrayItemData(w *Glw) *glwArrayItem {
	return w.glwParentData.(*glwArrayItem)
}

// C: grid_layout_row
func gridLayoutRow(a *glwArray, rc *glwRctx, rowvector []*Glw,
	numColumnsp *int, reqRowHeightp *int, height int) int {
	cols := *numColumnsp
	if cols == 0 {
		return 0
	}

	rh := *reqRowHeightp
	if rh == math.MaxInt32 {
		rh = a.childHeightPx
	}

	for i := range cols {
		c := rowvector[i]
		cd := arrayItemData(c)

		cd.height = uint16(rh)

		if c == a.gsc.scrollToMe {
			ypos := cd.posY
			screenPos := int(float32(ypos) - a.gsc.roundedPos)
			bottomScrollPos := height - a.gsc.scrollThresholdPost

			a.gsc.scrollToMe = nil
			if screenPos < a.gsc.scrollThresholdPre {
				a.gsc.targetPos = ypos - a.gsc.scrollThresholdPre
				if glwIsFocused(&a.w) {
					a.w.glwFlags |= glwUpdateMetrics
				}
				glwScheduleRefresh(a.w.glwRoot, 0)
			} else if screenPos+rh > bottomScrollPos {
				a.gsc.targetPos = ypos + rh - bottomScrollPos
				if glwIsFocused(&a.w) {
					a.w.glwFlags |= glwUpdateMetrics
				}
				glwScheduleRefresh(a.w.glwRoot, 0)
			}
		}

		if cd.posFy-a.gsc.roundedPos > float32(-height) &&
			cd.posFy-a.gsc.roundedPos < float32(height*2) {
			rc.rcWidth = int16(cd.width)
			rc.rcHeight = int16(cd.height)
			glwLayout0(c, rc)
		}
	}
	*numColumnsp = 0
	*reqRowHeightp = 0
	return rh
}

// C: glw_array_layout
func glwArrayLayout(w *Glw, rc *glwRctx) {
	a := (*glwArray)(unsafe.Pointer(w))
	var c *Glw
	rc0 := *rc
	var xspacing, yspacing float32
	var rows int
	xpos := 0
	ypos := a.gsc.scrollThresholdPre

	height := int(rc0.rcHeight)
	width := int(rc0.rcWidth)

	if a.childTilesX != 0 && a.childTilesY != 0 {

		xspacing = float32(a.xspacing)
		yspacing = float32(a.yspacing)

		a.xentries = a.childTilesX
		yentries := a.childTilesY

		if yentries == 0 {
			dw := width
			if dw == 0 {
				dw = 1
			}
			yentries = a.xentries * height / dw
		} else if a.xentries == 0 {
			dh := height
			if dh == 0 {
				dh = 1
			}
			a.xentries = yentries * width / dh
		}

		xe := a.xentries
		if xe == 0 {
			xe = 1
		}
		a.childWidthPx = int((float32(rc0.rcWidth) - float32(a.xentries-1)*xspacing) /
			float32(xe))
		a.childHeightPx = int((float32(rc0.rcHeight) - float32(yentries-1)*yspacing) /
			float32(yentries))

		rows = (a.numVisibleChilds-1)/a.childTilesX + 1

		if w.glwAlignment == layoutAlignCenter && rows < a.childTilesY {
			ypos = int(float32(a.childTilesY-rows) * (yspacing + float32(a.childHeightPx)) / 2)
		}

	} else if a.childTilesX != 0 {

		xspacing = float32(a.xspacing)
		yspacing = float32(a.yspacing)

		a.xentries = a.childTilesX

		xe := a.xentries
		if xe == 0 {
			xe = 1
		}
		a.childWidthPx = int((float32(rc0.rcWidth) - float32(a.xentries-1)*xspacing) /
			float32(xe))
		a.childHeightPx = a.childWidthPx

	} else {
		tileW := 100
		tileH := 100

		a.xentries = max(int(rc0.rcWidth)/tileW, 1)
		yentries := max(int(rc0.rcHeight)/tileH, 1)

		a.childWidthPx = tileW
		a.childHeightPx = tileH

		xspill := int(rc0.rcWidth) - (a.xentries * tileW)
		yspill := int(rc0.rcHeight) - (yentries * tileH)

		xspacing = float32(xspill / (a.xentries + 1))
		yspacing = float32(yspill / (yentries + 1))
	}

	if a.savedHeight != rc0.rcHeight {
		a.savedHeight = rc0.rcHeight
		a.gsc.pageSize = int(rc0.rcHeight)
		a.w.glwFlags |= glwUpdateMetrics

		if w.glwFocused != nil {
			a.gsc.scrollToMe = w.glwFocused
		}
	}

	glwScrollLayout(&a.gsc, w, int(rc.rcHeight))

	rowvector := make([]*Glw, a.xentries)
	column := 0
	reqRowHeight := 0

	for c = w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		if c.glwFlags&glwHidden != 0 {
			continue
		}

		cd := arrayItemData(c)

		if c.glwFlags&glwConstraintD != 0 {
			ypos += gridLayoutRow(a, &rc0, rowvector, &column,
				&reqRowHeight, height)

			cd.width = uint16(width)
			if c.glwFlags&glwConstraintY != 0 {
				reqRowHeight = glwReqHeight(c)
			}

			cd.col = -1

		} else {

			if column == a.xentries {
				ypos += int(a.yspacing) + gridLayoutRow(a, &rc0, rowvector,
					&column, &reqRowHeight, height)
				reqRowHeight = 0
				column = 0
			}

			cd.width = uint16(a.childWidthPx)
			cd.col = int8(column)

			reqItemHeight := 0

			if c.glwFlags&glwConstraintY != 0 {
				reqItemHeight += glwReqHeight(c)
			}

			if c.glwFlags&glwConstraintW != 0 && c.glwReqWeight < 0 {
				reqItemHeight += int(float32(a.childWidthPx) / -c.glwReqWeight)
			}

			if reqItemHeight != 0 {
				if reqItemHeight > reqRowHeight {
					reqRowHeight = reqItemHeight
				}
			} else {
				reqRowHeight = math.MaxInt32
			}
		}

		cd.posY = ypos
		cd.posX = int(float32(column)*(xspacing+float32(a.childWidthPx)) + float32(xpos))
		cd.height = uint16(rc0.rcHeight)

		if cd.justInserted != 0 {
			cd.posFy = float32(cd.posY)
			cd.posFx = float32(cd.posX)
			cd.justInserted = 0
		} else {
			glwLp(&cd.posFy, w.glwRoot, float32(cd.posY), 0.25)
			glwLp(&cd.posFx, w.glwRoot, float32(cd.posX), 0.25)
		}

		rowvector[column] = c
		column++

		if c.glwFlags&glwConstraintD != 0 {
			ypos += gridLayoutRow(a, &rc0, rowvector, &column,
				&reqRowHeight, height)
		}
	}

	ypos += gridLayoutRow(a, &rc0, rowvector, &column, &reqRowHeight, height)

	if a.gsc.totalSize != ypos {
		a.gsc.totalSize = ypos
		a.w.glwFlags |= glwUpdateMetrics
	}

	if a.w.glwFlags&glwUpdateMetrics != 0 {
		glwScrollUpdateMetrics(&a.gsc, w)
	}
}

// C: glw_array_render_one
func glwArrayRenderOne(a *glwArray, c *Glw, width, height int,
	rc0, rc2 *glwRctx, clipTop, clipBottom int) {
	var rc3 glwRctx
	cd := arrayItemData(c)
	y := cd.posFy - a.gsc.roundedPos
	var ct, cb int = -1, -1
	gr := a.w.glwRoot
	ch := int(cd.height)
	cw := int(cd.width)

	if y+float32(ch*2) < 0 || y-float32(ch) > float32(height) {
		c.glwFlags |= glwClipped
		return
	}
	c.glwFlags &^= glwClipped

	if y < float32(clipTop) {
		k := float32(clipTop) / float32(rc0.rcHeight)
		ct = glwClipEnable(gr, rc0, glwClipTop, k,
			a.gsc.clipAlpha, 1.0-a.gsc.clipBlur)
	}

	if y+float32(cd.height) > float32(rc0.rcHeight)-float32(clipBottom) {
		k := float32(clipBottom) / float32(rc0.rcHeight)
		cb = glwClipEnable(gr, rc0, glwClipBottom, k,
			a.gsc.clipAlpha, 1.0-a.gsc.clipBlur)
	}
	rc3 = *rc2
	glwReposition(&rc3,
		int(cd.posFx),
		int(float32(height)-cd.posFy),
		int(cd.posFx+float32(cw)),
		int(float32(height)-cd.posFy-float32(ch)))

	glwRender0(c, &rc3)

	if ct != -1 {
		glwClipDisable(gr, ct)
	}
	if cb != -1 {
		glwClipDisable(gr, cb)
	}
}

// C: glw_array_render
func glwArrayRender(w *Glw, rc *glwRctx) {
	a := (*glwArray)(unsafe.Pointer(w))
	var rc0, rc1 glwRctx

	rc0 = *rc
	rc0.rcAlpha *= w.glwAlpha

	if rc0.rcAlpha < glwAlphaEpsilon {
		return
	}

	rc1 = rc0
	glwReposition(&rc1, 0,
		int(rc1.rcHeight)-int(a.gsc.clipOffsetPre),
		int(rc1.rcWidth),
		int(a.gsc.clipOffsetPost))

	glwStoreMatrix(w, &rc1)

	rc1 = rc0

	width := int(rc1.rcWidth)
	height := int(rc1.rcHeight)

	clipTop := int(a.gsc.clipOffsetPre) - 1
	clipBottom := int(a.gsc.clipOffsetPost) - 1

	glwTranslatef(&rc1, 0, 2.0*a.gsc.roundedPos/float32(height), 0)

	for c := w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		if c.glwFlags&glwHidden != 0 {
			continue
		}
		glwArrayRenderOne(a, c, width, height, &rc0, &rc1,
			clipTop, clipBottom)
	}
}

// C: glw_array_scroll
func glwArrayScroll(a *glwArray, gs *glwScroll) {
	glwScrollHandleScroll(&a.gsc, &a.w, gs)
}

// C: scroll_to_me — helper to make sure we can show items in list that are
// not focusable even if they are at the top
func arrayScrollToMe(a *glwArray, c *Glw) {
	glwScheduleRefresh(a.w.glwRoot, 0)

	for c != nil && arrayItemData(c).col > 0 {
		c = glwTAILQPrev(c)
	}
	if c == nil {
		return
	}

	d, e := c, c
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
	a.gsc.scrollToMe = c
}

// C: glw_array_callback
func glwArrayCallback(w *Glw, opaque any, signal glwSignal, extra any) int {
	a := (*glwArray)(unsafe.Pointer(w))

	switch signal {
	case glwSignalFocusChildInteractive:
		arrayScrollToMe(a, extra.(*Glw))
		a.gsc.suggestCnt = 0
		w.glwFlags &^= glwFloatingFocus
		return 0

	case glwSignalChildDestroyed:
		if a.gsc.scrollToMe == extra.(*Glw) {
			a.gsc.scrollToMe = nil
		}
		if a.gsc.suggested == extra.(*Glw) {
			a.gsc.suggested = nil
		}
		a.numVisibleChilds--

	case glwSignalChildHidden:
		a.numVisibleChilds--

	case glwSignalChildCreated, glwSignalChildUnhidden:
		c := extra.(*Glw)
		a.numVisibleChilds++
		arrayItemData(c).justInserted = 1

	case glwSignalScroll:
		glwArrayScroll(a, extra.(*glwScroll))

	case glwSignalFhpPathChanged:
		if !glwIsFocused(w) {
			a.gsc.suggestCnt = 1
		}
	}
	return 0
}

// C: glw_array_pointer_event
func glwArrayPointerEvent(w *Glw, gpe *glwPointerEventT) int {
	a := (*glwArray)(unsafe.Pointer(w))
	return glwScrollHandlePointerEvent(&a.gsc, w, gpe)
}

// C: handle_pointer_event_filter
func glwArrayPointerEventFilter(w *Glw, gpe *glwPointerEventT) int {
	a := (*glwArray)(unsafe.Pointer(w))
	return glwScrollHandlePointerEventFilter(&a.gsc, w, gpe)
}

// C: glw_array_ctor
func glwArrayCtor(w *Glw) {
	a := (*glwArray)(unsafe.Pointer(w))
	w.glwFlags |= glwFloatingFocus
	a.gsc.suggestCnt = 1
}

// C: glw_array_set_int
func glwArraySetInt(w *Glw, attrib glwAttribute, value int, gs *GlwStyle) int {
	a := (*glwArray)(unsafe.Pointer(w))

	switch attrib {
	case glwAttribChildTilesX:
		if a.childTilesX == value {
			return 0
		}
		a.childTilesX = value

	case glwAttribChildTilesY:
		if a.childTilesY == value {
			return 0
		}
		a.childTilesY = value

	case glwAttribXSpacing:
		if a.xspacing == int16(value) {
			return 0
		}
		a.xspacing = int16(value)

	case glwAttribYSpacing:
		if a.yspacing == int16(value) {
			return 0
		}
		a.yspacing = int16(value)

	default:
		return -1
	}
	return 1
}

// C: glw_array_get_next_row
func glwArrayGetNextRow(c *Glw, reverse int) *Glw {
	currentCol := int(arrayItemData(c).col)
	if currentCol == -1 {
		currentCol = 0
	}
	if reverse != 0 {
		for c = glwGetPrevN(c, 1); c != nil; c = glwGetPrevN(c, 1) {
			if (int(arrayItemData(c).col) == currentCol ||
				c.glwFlags&glwConstraintD != 0) &&
				glwGetFocusableChild(c) != nil {
				return c
			}
		}
	} else {
		for c = glwGetNextN(c, 1); c != nil; c = glwGetNextN(c, 1) {
			if int(arrayItemData(c).col) == currentCol &&
				glwGetFocusableChild(c) != nil {
				return c
			}
		}
	}
	return nil
}

// C: glw_array_bubble_event
func glwArrayBubbleEvent(w *Glw, e *eventpkg.Event) int {
	a := (*glwArray)(unsafe.Pointer(w))
	c := w.glwFocused

	if c == nil {
		return 0
	}

	mayWrap := glwNavigateMayWrap(c)
	currentCol := int(arrayItemData(c).col)

	switch {
	case e.IsAction(eventpkg.ACTION_RIGHT):
		if currentCol == a.xentries-1 {
			return 0
		}
		return glwNavigateStep(c, 1, mayWrap)

	case e.IsAction(eventpkg.ACTION_LEFT):
		if currentCol == 0 {
			return 0
		}
		return glwNavigateStep(c, -1, mayWrap)

	case e.IsAction(eventpkg.ACTION_UP):
		return glwFocusChild(glwArrayGetNextRow(c, 1))

	case e.IsAction(eventpkg.ACTION_DOWN):
		return glwFocusChild(glwArrayGetNextRow(c, 0))

	case e.IsAction(eventpkg.ACTION_PAGE_UP):
		rows := max(a.childTilesY-1, 1)
		for range rows {
			if n := glwArrayGetNextRow(c, 1); n != nil {
				c = n
			}
		}
		return glwFocusChild(c)

	case e.IsAction(eventpkg.ACTION_PAGE_DOWN):
		rows := max(a.childTilesY-1, 1)
		for range rows {
			if n := glwArrayGetNextRow(c, 0); n != nil {
				c = n
			}
		}
		return glwFocusChild(c)

	case e.IsAction(eventpkg.ACTION_MOVE_RIGHT):
		return glwNavigateMove(c, 1)

	case e.IsAction(eventpkg.ACTION_MOVE_LEFT):
		return glwNavigateMove(c, -1)

	case e.IsAction(eventpkg.ACTION_MOVE_DOWN):
		return glwNavigateMove(c, a.xentries)

	case e.IsAction(eventpkg.ACTION_MOVE_UP):
		return glwNavigateMove(c, -a.xentries)

	case e.IsAction(eventpkg.ACTION_TOP):
		return glwNavigateFirst(w)

	case e.IsAction(eventpkg.ACTION_BOTTOM):
		return glwNavigateLast(w)
	}
	return 0
}

// C: glw_array_set_float_unresolved
func glwArraySetFloatUnresolved(w *Glw, a string, value float32, gs *GlwStyle) int {
	l := (*glwArray)(unsafe.Pointer(w))
	return glwScrollSetFloatAttributes(&l.gsc, a, value)
}

// C: glw_array_set_int_unresolved
func glwArraySetIntUnresolved(w *Glw, a string, value int, gs *GlwStyle) int {
	l := (*glwArray)(unsafe.Pointer(w))
	return glwScrollSetIntAttributes(&l.gsc, a, value)
}

// C: glw_array_suggest_focus
func glwArraySuggestFocus(w *Glw, c *Glw) {
	a := (*glwArray)(unsafe.Pointer(w))
	glwScrollSuggestFocus(&a.gsc, w, c)
}

// C: glw_array_find_visible_child — find a child widget that's visible;
// used when scrolling to maintain focus on screen
func glwArrayFindVisibleChild(w *Glw) *Glw {
	l := (*glwArray)(unsafe.Pointer(w))
	top := l.gsc.targetPos + l.gsc.scrollThresholdPre
	bottom := l.gsc.targetPos + l.gsc.pageSize - l.gsc.scrollThresholdPre
	c := l.w.glwFocused

	if c == nil {
		return nil
	}

	if arrayItemData(c).posY < top {
		for c != nil && arrayItemData(c).posY < top {
			c = glwNextWidget(c)
		}
		if c != nil && glwGetFocusableChild(c) == nil {
			c = glwNextWidget(c)
		}
	} else if arrayItemData(c).posY > bottom {
		for c != nil && arrayItemData(c).posY > bottom {
			c = glwPrevWidget(c)
		}
		if c != nil && glwGetFocusableChild(c) == nil {
			c = glwPrevWidget(c)
		}
		for c != nil {
			p := glwPrevWidget(c)
			if p == nil {
				break
			}
			if arrayItemData(p).posY != arrayItemData(c).posY {
				break
			}
			c = p
		}
	}
	return c
}

// C: glw_class_t glw_array
var glwArrayClass = &glwClass{
	gcName:           "array",
	gcInstanceSize:   int(unsafe.Sizeof(glwArray{})),
	gcParentDataSize: int(unsafe.Sizeof(glwArrayItem{})),
	gcNewParentData:  func() any { return &glwArrayItem{} },
	gcFlags:          glwNavigationSearchBoundary | glwCanHideChilds | glwDrivePagination,
	gcNew: func(parent *Glw) *Glw {
		a := &glwArray{}
		return &a.w
	},
	gcRender:             glwArrayRender,
	gcCtor:               glwArrayCtor,
	gcSetInt:             glwArraySetInt,
	gcSignalHandler:      glwArrayCallback,
	gcLayout:             glwArrayLayout,
	gcPointerEvent:       glwArrayPointerEvent,
	gcPointerEventFilter: glwArrayPointerEventFilter,
	gcBubbleEvent:        glwArrayBubbleEvent,
	gcSetIntUnresolved:   glwArraySetIntUnresolved,
	gcSetFloatUnresolved: glwArraySetFloatUnresolved,
	gcSuggestFocus:       glwArraySuggestFocus,
	gcFindVisibleChild:   glwArrayFindVisibleChild,
}

func registerArray() {
	glwRegisterClass(glwArrayClass)
}
