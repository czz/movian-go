package glw

// Canonical port of src/ui/glw/glw_container.c — container_x/hbox,
// container_y/vbox, container_z/zbox and table widget classes.
//
// C: LIST_HEAD(glw_container_list, glw_container) gt_rows — Go: slice.

import (
	"math"
	"slices"
	"unsafe"

	eventpkg "github.com/czz/movian-go/internal/event"
)

// C: glw_table_t
type glwTable struct {
	w Glw

	gtNumColumns int
	gtWidthSum   int
	gtColumns    []int16
	gtRows       []*glwContainer // C: LIST_HEAD glw_container_list
}

// C: glw_container_t
type glwContainer struct {
	w Glw

	coTable        *glwTable
	coColumnWidths []int16
	cflags         int
	weightSum      float32
	width          int16
	spacingWidth   int16
	paddingWidth   int16
	height         int16
	coSpacing      int16
	coPadding      [4]int16
	coBiggest      int16
	coNumColumns   uint16
	coUsingAspect  bool // C: char
}

// C: glw_container_item_t — per-child parent data
type glwContainerItem struct {
	pos     float32
	scale   float32
	fade    float32
	size    int16
	started bool // C: char
}

func containerItemData(w *Glw) *glwContainerItem {
	return w.glwParentData.(*glwContainerItem)
}

// C: LIST_INSERT_HEAD(&gt->gt_rows, co, co_table_link)
func tableRowInsert(gt *glwTable, co *glwContainer) {
	gt.gtRows = slices.Insert(gt.gtRows, 0, co)
}

// C: LIST_REMOVE(co, co_table_link)
func tableRowRemove(co *glwContainer) {
	rows := co.coTable.gtRows
	for i, r := range rows {
		if r == co {
			co.coTable.gtRows = slices.Delete(rows, i, i+1)
			return
		}
	}
}

// C: table_recompute
func tableRecompute(gt *glwTable) {
	columns := 0
	spacingWidth := 0

	for _, co := range gt.gtRows {
		if int(co.coNumColumns) > columns {
			columns = int(co.coNumColumns)
		}
		if int(co.spacingWidth) > spacingWidth {
			spacingWidth = int(co.spacingWidth)
		}
	}

	if columns != gt.gtNumColumns {
		gt.gtColumns = make([]int16, columns)
		gt.gtNumColumns = columns
	}

	widthSum := 0
	for i := range columns {
		w := int16(math.MinInt16)
		for _, co := range gt.gtRows {
			if i >= int(co.coNumColumns) {
				continue
			}
			if co.coColumnWidths[i] >= 0 && co.coColumnWidths[i] > w {
				w = co.coColumnWidths[i]
			}
		}
		gt.gtColumns[i] = w
		if w >= 0 {
			widthSum += int(w)
		}
	}
	gt.gtWidthSum = widthSum
	glwModConstraints(&gt.w,
		widthSum+spacingWidth, 0, 0,
		glwConstraintX, glwConstraintX)
}

// C: glw_container_x_constraints
func glwContainerXConstraints(co *glwContainer, skip *Glw) int {
	height := 0
	paddingWidth := int(co.coPadding[0]) + int(co.coPadding[2])
	width := 0
	var weight float32
	cflags := 0
	elements := 0
	numfix := 0
	tab := co.coTable

	co.coBiggest = 0
	co.coUsingAspect = false

	if co.w.glwFlags2&glw2Debug != 0 {
		println("Constraint round")
	}

	if tab != nil { // C: unlikely
		numChilds := 0
		for c := co.w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
			if c.glwFlags&glwHidden != 0 || c == skip {
				continue
			}
			numChilds++
		}

		if int(co.coNumColumns) != numChilds {
			co.coColumnWidths = make([]int16, numChilds)
			co.coNumColumns = uint16(numChilds)
		}
		for i := range co.coColumnWidths {
			co.coColumnWidths[i] = 0
		}
	}

	i := -1
	for c := co.w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		i++
		if c.glwFlags&glwHidden != 0 || c == skip {
			continue
		}

		f := glwFilterConstraints(c)
		cflags |= f & (glwConstraintX | glwConstraintY)

		if co.w.glwFlags2&glw2Debug != 0 {
			println("child constraints", glwReqWidth(c), glwReqHeight(c), c.glwReqWeight)
		}

		if f&glwConstraintY != 0 {
			rh := glwReqHeight(c)
			if rh > height {
				height = rh
			}
		}
		if tab != nil { // C: unlikely
			if f&glwConstraintX != 0 {
				co.coColumnWidths[i] = int16(glwReqWidth(c))
			} else {
				co.coColumnWidths[i] = math.MinInt16
				weight += 1.0
			}
		} else {
			if f&glwConstraintX != 0 {
				rw := glwReqWidth(c)

				if co.w.glwFlags2&glw2Homogenous != 0 {
					if int16(rw) > co.coBiggest {
						co.coBiggest = int16(rw)
					}
					numfix++
				} else {
					width += rw
				}

				if f&glwConstraintW != 0 && c.glwReqWeight < 0 {
					// C: glw_req_height(c) + rw / -c->glw_req_weight —
					// FLOAT division (glw_req_weight is float): a weight
					// in (-1,0) yields a huge rh, not a div-by-zero.
					rh := int(float32(glwReqHeight(c)) + float32(rw)/-c.glwReqWeight)
					if rh > height {
						height = rh
					}
					cflags |= glwConstraintY
				}
			} else if f&glwConstraintW != 0 {
				if c.glwReqWeight == 0 {
					continue
				}
				if c.glwReqWeight > 0 {
					weight += c.glwReqWeight
				} else {
					co.coUsingAspect = true
				}
			} else {
				weight += 1.0
			}
		}
		elements++
	}

	if co.w.glwFlags2&glw2Homogenous != 0 {
		width += numfix * int(co.coBiggest)
	}

	var spacingWidth int16
	if elements > 0 {
		spacingWidth = int16(elements-1) * co.coSpacing
	}
	if co.w.glwFlags2&glw2Debug != 0 {
		println("Total width:", width)
	}

	if co.weightSum != weight ||
		co.width != int16(width) ||
		co.paddingWidth != int16(paddingWidth) ||
		co.spacingWidth != spacingWidth ||
		co.cflags != cflags {

		co.weightSum = weight
		co.width = int16(width)
		co.spacingWidth = spacingWidth
		co.paddingWidth = int16(paddingWidth)
		co.cflags = cflags
		glwNeedRefresh(co.w.glwRoot, 0)
	}

	height += int(co.coPadding[3]) + int(co.coPadding[1])
	if tab != nil {
		tableRecompute(tab)
	}

	glwSetConstraints(&co.w, width+int(spacingWidth)+paddingWidth,
		height, 0, cflags)
	return 1
}

// C: glw_container_x_layout
func glwContainerXLayout(w *Glw, rc *glwRctx) {
	co := (*glwContainer)(unsafe.Pointer(w))
	aspectWidth := 0
	var IW float32
	var weightavail int
	var pos float32
	var fixscale float32
	var rc0 glwRctx
	tab := co.coTable

	rc0 = *rc

	rc0.rcHeight = rc.rcHeight - co.coPadding[1] - co.coPadding[3]

	if co.coUsingAspect {
		for c := co.w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
			f := glwFilterConstraints(c)
			cw := float32(1.0)
			if f&glwConstraintW != 0 {
				cw = c.glwReqWeight
			}
			if cw < 0 {
				aspectWidth += int(float32(rc0.rcHeight) * -cw)
			}
		}
	}

	spacePad := int(co.spacingWidth) + int(co.paddingWidth)
	wsum := aspectWidth + spacePad

	if tab != nil {
		wsum += tab.gtWidthSum
	} else {
		wsum += int(co.width)
	}

	if wsum > int(rc.rcWidth) {
		weightavail = 0
		fixscale = float32(int(rc.rcWidth)-aspectWidth-spacePad) / float32(co.width)
		pos = float32(co.coPadding[0]) * fixscale
	} else {
		fixscale = 1
		weightavail = int(rc.rcWidth) - wsum
		pos = float32(co.coPadding[0])

		if co.weightSum == 0 {
			if co.w.glwAlignment == layoutAlignCenter {
				pos = float32(rc.rcWidth)/2 - float32(wsum-int(co.paddingWidth))/2
			} else if co.w.glwAlignment == layoutAlignRight {
				pos = float32(rc.rcWidth) - float32(wsum-int(co.paddingWidth))
			}
		}
	}

	left := int(math.Round(float64(pos)))
	var right int

	IW = 1.0 / float32(rc.rcWidth)
	i := -1
	for c := co.w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		var cw float32
		i++
		if c.glwFlags&glwHidden != 0 {
			continue
		}

		f := glwFilterConstraints(c)

		if tab != nil { // C: unlikely
			if tab.gtColumns[i] >= 0 {
				cw = float32(tab.gtColumns[i])
			} else {
				cw = float32(weightavail) / co.weightSum
			}
		} else if f&glwConstraintX != 0 {
			if co.w.glwFlags2&glw2Homogenous != 0 {
				cw = float32(co.coBiggest) * fixscale
			} else {
				cw = float32(glwReqWidth(c)) * fixscale
			}
		} else {
			cwt := float32(1.0)
			if f&glwConstraintW != 0 {
				cwt = c.glwReqWeight
			}
			if cwt == 0 {
				continue
			}
			if cwt > 0 {
				cw = float32(weightavail) * cwt / co.weightSum
			} else {
				cw = float32(rc0.rcHeight) * -cwt
			}
		}

		pos += cw
		right = int(math.Round(float64(pos)))

		rc0.rcWidth = int16(right - left)

		cd := containerItemData(c)

		cd.pos = -1.0 + float32(right+left)*IW
		cd.scale = float32(rc0.rcWidth) * IW
		cd.size = int16(right - left)
		glwLayout0(c, &rc0)
		left = right + int(co.coSpacing)
		pos += float32(co.coSpacing)
	}
}

// C: glw_container_y_constraints
func glwContainerYConstraints(co *glwContainer, skip *Glw) int {
	width := 0
	height := int(co.coPadding[3]) + int(co.coPadding[1])
	var weight float32
	cflags := 0
	elements := 0
	var reqAspect float32
	co.coUsingAspect = false

	if co.w.glwFlags2&glw2Debug != 0 {
		println("Constraint round")
	}

	for c := co.w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		if c.glwFlags&glwHidden != 0 || c == skip {
			continue
		}

		f := glwFilterConstraints(c)

		cflags |= f & (glwConstraintX | glwConstraintY)

		if f&glwConstraintX != 0 {
			rw := glwReqWidth(c)
			if rw > width {
				width = rw
			}
		}

		if f&glwConstraintY != 0 {
			height += glwReqHeight(c)
		} else if f&glwConstraintW != 0 {
			if c.glwReqWeight > 0 {
				weight += c.glwReqWeight
			} else if c.glwReqWeight < 0 {
				co.coUsingAspect = true
				reqAspect = c.glwReqWeight
				cflags |= glwConstraintW
			}
		} else {
			weight += 1.0
		}
		elements++
	}

	if elements > 0 {
		height += (elements - 1) * int(co.coSpacing)
	}

	if co.height != int16(height) ||
		co.weightSum != weight ||
		co.cflags != cflags {

		co.height = int16(height)
		co.weightSum = weight
		co.cflags = cflags
		glwNeedRefresh(co.w.glwRoot, 0)
	}

	if weight != 0 {
		cflags &^= glwConstraintY
	}

	width += int(co.coPadding[0]) + int(co.coPadding[2])
	glwSetConstraints(&co.w, width, height, reqAspect, cflags)
	return 1
}

// C: glw_container_y_layout
func glwContainerYLayout(w *Glw, rc *glwRctx) {
	co := (*glwContainer)(unsafe.Pointer(w))
	rc0 := *rc
	height := int(co.height)
	var IH float32
	var weightavail int
	var pos float32
	var fixscale float32

	rc0.rcWidth = rc.rcWidth - co.coPadding[0] - co.coPadding[2]

	if co.coUsingAspect {
		for c := co.w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
			f := glwFilterConstraints(c)
			cw := float32(1.0)
			if f&glwConstraintW != 0 {
				cw = c.glwReqWeight
			}
			if cw < 0 {
				height += int(float32(rc0.rcWidth) / -cw)
			}
		}
	}

	if height > int(rc.rcHeight) {
		weightavail = 0
		fixscale = float32(rc.rcHeight) / float32(height)
		pos = float32(co.coPadding[1]) * fixscale
	} else {
		fixscale = 1
		weightavail = int(rc.rcHeight) - height
		pos = float32(co.coPadding[1])

		if co.weightSum == 0 {
			if co.w.glwAlignment == layoutAlignCenter {
				pos = float32(rc.rcHeight)/2 - float32(height)/2
			} else if co.w.glwAlignment == layoutAlignBottom {
				pos = float32(rc.rcHeight) - float32(height)
			}
		}
	}

	top := int(math.Round(float64(pos)))
	var bottom int
	IH = 1.0 / float32(rc.rcHeight)

	for c := co.w.glwChilds.tqhFirst; c != nil; {
		n := c.glwParentLinkNext
		var cw float32

		cd := containerItemData(c)

		if c.glwFlags&glwHidden != 0 {
			if co.w.glwFlags2&glw2Autofade == 0 {
				cd.fade = 0
				c = n
				continue
			}

			glwLp(&cd.fade, co.w.glwRoot, 0, 0.25)
			if cd.fade < glwAlphaEpsilon {
				cd.started = false
				c = n
				continue
			}
		}

		f := glwFilterConstraints(c)

		if f&glwConstraintY != 0 {
			cw = fixscale * float32(glwReqHeight(c))
		} else {
			cwt := float32(1.0)
			if f&glwConstraintW != 0 {
				cwt = c.glwReqWeight
			}
			if cwt > 0 {
				cw = float32(weightavail) * cwt / co.weightSum
			} else {
				cw = float32(rc0.rcWidth) / -cwt
			}
		}

		pos += cw
		bottom = int(math.Round(float64(pos)))
		rc0.rcHeight = int16(bottom - top)

		if co.w.glwFlags2&glw2Autofade != 0 {
			if c.glwFlags&glwRetired != 0 {
				glwLp(&cd.fade, co.w.glwRoot, 0, 0.25)
				if cd.fade < glwAlphaEpsilon {
					glwDestroy(c)
					c = n
					continue
				}
			} else if c.glwFlags&glwHidden == 0 {
				glwLp(&cd.fade, co.w.glwRoot, 1, 0.25)
			}

			if cd.started {
				glwLp(&cd.pos, co.w.glwRoot, float32(bottom+top)*IH, 0.25)
			} else {
				cd.pos = float32(bottom+top) * IH
				if c.glwFlags&glwHidden == 0 {
					cd.started = true
				}
			}
			cd.scale = float32(rc0.rcHeight) * IH * cd.fade
			cd.size = rc0.rcHeight
		} else {
			cd.fade = 1
			cd.pos = float32(bottom+top) * IH
			cd.scale = float32(rc0.rcHeight) * IH
			cd.size = rc0.rcHeight
		}

		glwLayout0(c, &rc0)
		top = bottom + int(co.coSpacing)
		pos += float32(co.coSpacing)
		c = n
	}
}

// C: glw_container_z_constraints
func glwContainerZConstraints(w *Glw, skip *Glw) int {
	var c *Glw
	for c = w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		if c.glwFlags&glwHidden != 0 || c == skip {
			continue
		}
		if glwFilterConstraints(c) != 0 {
			break
		}
	}

	if c != nil {
		glwCopyConstraints(w, c)
	} else {
		glwClearConstraints(w)
	}
	return 1
}

// C: glw_container_z_layout
func glwContainerZLayout(w *Glw, rc *glwRctx) {
	for c := w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		if c.glwFlags&glwHidden != 0 {
			continue
		}
		glwLayout0(c, rc)
	}
}

// C: glw_container_y_render
func glwContainerYRender(w *Glw, rc *glwRctx) {
	alpha := rc.rcAlpha * w.glwAlpha
	sharpness := rc.rcSharpness * w.glwSharpness
	co := (*glwContainer)(unsafe.Pointer(w))
	var rc0, rc1 glwRctx

	if alpha < glwAlphaEpsilon {
		return
	}

	if glwIsFocusableOrClickable(w) {
		glwStoreMatrix(w, rc)
	}

	if co.coPadding[0] != 0 || co.coPadding[2] != 0 {
		rc1 = *rc
		glwReposition(&rc1,
			int(co.coPadding[0]),
			int(rc.rcHeight),
			int(rc.rcWidth)-int(co.coPadding[2]),
			0)
		rc = &rc1
	}

	for c := w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		cd := containerItemData(c)

		if cd.fade < glwAlphaEpsilon {
			continue
		}

		rc0 = *rc
		rc0.rcAlpha = alpha * cd.fade
		rc0.rcSharpness = sharpness * cd.fade
		rc0.rcHeight = cd.size

		glwTranslatef(&rc0, 0, 1.0-cd.pos, 0)
		glwScalef(&rc0, 1.0, cd.scale, cd.scale)

		glwRender0(c, &rc0)
	}
}

// C: glw_container_x_render
func glwContainerXRender(w *Glw, rc *glwRctx) {
	alpha := rc.rcAlpha * w.glwAlpha
	sharpness := rc.rcSharpness * w.glwSharpness
	co := (*glwContainer)(unsafe.Pointer(w))
	var rc0, rc1 glwRctx

	if alpha < glwAlphaEpsilon {
		return
	}

	if glwIsFocusableOrClickable(w) {
		glwStoreMatrix(w, rc)
	}

	if co.coPadding[1] != 0 || co.coPadding[3] != 0 {
		rc1 = *rc
		glwReposition(&rc1,
			0,
			int(rc.rcHeight)-int(co.coPadding[1]),
			int(rc.rcWidth),
			int(co.coPadding[3]))
		rc = &rc1
	}

	for c := w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		if c.glwFlags&glwHidden != 0 {
			continue
		}

		rc0 = *rc
		rc0.rcAlpha = alpha
		rc0.rcSharpness = sharpness

		cd := containerItemData(c)
		rc0.rcWidth = cd.size

		glwTranslatef(&rc0, cd.pos, 0, 0)
		glwScalef(&rc0, cd.scale, 1.0, cd.scale)

		glwRender0(c, &rc0)
	}
}

// C: glw_container_z_render
func glwContainerZRender(w *Glw, rc *glwRctx) {
	alpha := rc.rcAlpha * w.glwAlpha
	sharpness := rc.rcSharpness * w.glwSharpness
	zmax := 0
	var rc0 glwRctx

	if alpha < glwAlphaEpsilon {
		return
	}

	if glwIsFocusableOrClickable(w) {
		glwStoreMatrix(w, rc)
	}

	rc0 = *rc
	rc0.rcAlpha = alpha
	rc0.rcSharpness = sharpness
	rc0.rcZmax = &zmax

	for c := w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		if c.glwFlags&glwHidden != 0 {
			continue
		}
		if zmax > int(rc.rcZindex) {
			rc0.rcZindex = int16(zmax)
		} else {
			rc0.rcZindex = rc.rcZindex
		}
		glwRender0(c, &rc0)
		glwZinc(&rc0)
	}
	if zmax > *rc.rcZmax {
		*rc.rcZmax = zmax
	}
}

// C: glw_container_x_callback
func glwContainerXCallback(w *Glw, opaque any, signal glwSignal, extra any) int {
	co := (*glwContainer)(unsafe.Pointer(w))
	switch signal {
	case glwSignalChildConstraintsChanged, glwSignalChildCreated,
		glwSignalChildHidden, glwSignalChildUnhidden:
		return glwContainerXConstraints(co, nil)
	case glwSignalChildDestroyed:
		return glwContainerXConstraints(co, extra.(*Glw))
	case glwSignalDestroy:
		if co.coTable != nil {
			tableRowRemove(co)
		}
		co.coColumnWidths = nil
		return 0
	default:
		return 0
	}
}

// C: glw_container_y_callback
func glwContainerYCallback(w *Glw, opaque any, signal glwSignal, extra any) int {
	switch signal {
	case glwSignalChildConstraintsChanged, glwSignalChildCreated,
		glwSignalChildHidden, glwSignalChildUnhidden:
		return glwContainerYConstraints((*glwContainer)(unsafe.Pointer(w)), nil)
	case glwSignalChildDestroyed:
		return glwContainerYConstraints((*glwContainer)(unsafe.Pointer(w)), extra.(*Glw))
	default:
		return 0
	}
}

// C: glw_container_z_callback
func glwContainerZCallback(w *Glw, opaque any, signal glwSignal, extra any) int {
	switch signal {
	case glwSignalChildConstraintsChanged, glwSignalChildCreated:
		return glwContainerZConstraints(w, nil)
	case glwSignalChildDestroyed:
		return glwContainerZConstraints(w, extra.(*Glw))
	default:
		return 0
	}
}

// C: glw_container_set_int
func glwContainerSetInt(w *Glw, attrib glwAttribute, value int, gs *GlwStyle) int {
	co := (*glwContainer)(unsafe.Pointer(w))
	switch attrib {
	case glwAttribSpacing:
		if co.coSpacing == int16(value) {
			return 0
		}
		co.coSpacing = int16(value)
	default:
		return -1
	}
	return 1
}

// C: container_set_int16_4
func containerSetInt16_4(w *Glw, attrib glwAttribute, v []int16, gs *GlwStyle) int {
	co := (*glwContainer)(unsafe.Pointer(w))
	switch attrib {
	case glwAttribPadding:
		if glwAttribSetInt16_4(co.coPadding[:], v) == 0 {
			return 0
		}
		glwSignal0(w, glwSignalChildConstraintsChanged, nil)
		return 1
	default:
		return -1
	}
}

// C: retire_child
func containerRetireChild(w *Glw, c *Glw) {
	if w.glwFlags2&glw2Autofade != 0 {
		c.glwFlags |= glwRetired
		glwNeedRefresh(w.glwRoot, 0)
		glwSuspendSubscriptions(c)
	} else {
		glwDestroy(c)
	}
}

// C: glw_container_find_table
func glwContainerFindTable(w *Glw) *Glw {
	for ; w != nil; w = w.glwParent {
		if w.glwClass == glwTableClass {
			return w
		}
	}
	return nil
}

// C: glw_container_set_int_unresolved
func glwContainerSetIntUnresolved(w *Glw, a string, value int, gs *GlwStyle) int {
	co := (*glwContainer)(unsafe.Pointer(w))

	if a == "tableMode" {
		if (value == 0) == (co.coTable == nil) {
			return glwSetNoChange
		}

		if co.coTable != nil {
			tableRowRemove(co)
		}

		if value != 0 {
			t := glwContainerFindTable(w)
			if t != nil {
				co.coTable = (*glwTable)(unsafe.Pointer(t))
				tableRowInsert(co.coTable, co)
			} else {
				co.coTable = nil
			}
		} else {
			co.coTable = nil
		}
		return glwSetRerenderRequired
	}
	return glwSetNotResponding
}

// C: glw_table_callback
func glwTableCallback(w *Glw, opaque any, signal glwSignal, extra any) int {
	gt := (*glwTable)(unsafe.Pointer(w))
	switch signal {
	case glwSignalChildConstraintsChanged:
		src := extra.(*Glw)
		glwModConstraints(w,
			0,
			glwReqHeight(src),
			src.glwReqWeight,
			src.glwFlags&glwConstraintFlags,
			glwConstraintY|glwConstraintW|glwConstraintD)
	case glwSignalDestroy:
		gt.gtColumns = nil
	}
	return 0
}

// C: glw_class_t glw_container_x / _y / _z / glw_table
var glwContainerXClass = &glwClass{
	gcName:           "container_x",
	gcName2:          "hbox",
	gcInstanceSize:   int(unsafe.Sizeof(glwContainer{})),
	gcParentDataSize: int(unsafe.Sizeof(glwContainerItem{})),
	gcNewParentData:  func() any { return &glwContainerItem{} },
	gcFlags:          glwCanHideChilds,
	gcNew: func(parent *Glw) *Glw {
		co := &glwContainer{}
		return &co.w
	},
	gcSetInt:           glwContainerSetInt,
	gcLayout:           glwContainerXLayout,
	gcRender:           glwContainerXRender,
	gcSignalHandler:    glwContainerXCallback,
	gcDefaultAlignment: layoutAlignLeft,
	gcSetInt16_4:       containerSetInt16_4,
	gcBubbleEvent: func(w *Glw, e *eventpkg.Event) int {
		return glwNavigateHorizontal(w, e)
	},
	gcSetIntUnresolved: glwContainerSetIntUnresolved,
}

var glwContainerYClass = &glwClass{
	gcName:           "container_y",
	gcName2:          "vbox",
	gcInstanceSize:   int(unsafe.Sizeof(glwContainer{})),
	gcParentDataSize: int(unsafe.Sizeof(glwContainerItem{})),
	gcNewParentData:  func() any { return &glwContainerItem{} },
	gcFlags:          glwCanHideChilds,
	gcNew: func(parent *Glw) *Glw {
		co := &glwContainer{}
		return &co.w
	},
	gcSetInt:           glwContainerSetInt,
	gcLayout:           glwContainerYLayout,
	gcRender:           glwContainerYRender,
	gcSignalHandler:    glwContainerYCallback,
	gcDefaultAlignment: layoutAlignTop,
	gcSetInt16_4:       containerSetInt16_4,
	gcRetireChild:      containerRetireChild,
	gcBubbleEvent: func(w *Glw, e *eventpkg.Event) int {
		return glwNavigateVertical(w, e)
	},
}

var glwContainerZClass = &glwClass{
	gcName:         "container_z",
	gcName2:        "zbox",
	gcFlags:        glwCanHideChilds,
	gcInstanceSize: int(unsafe.Sizeof(Glw{})),
	gcNew: func(parent *Glw) *Glw {
		return &Glw{}
	},
	gcLayout:        glwContainerZLayout,
	gcRender:        glwContainerZRender,
	gcSignalHandler: glwContainerZCallback,
}

var glwTableClass = &glwClass{
	gcName:         "table",
	gcInstanceSize: int(unsafe.Sizeof(glwTable{})),
	gcNew: func(parent *Glw) *Glw {
		gt := &glwTable{}
		return &gt.w
	},
	gcLayout:        glwContainerZLayout,
	gcRender:        glwContainerZRender,
	gcSignalHandler: glwTableCallback,
}

func registerContainer() {
	glwRegisterClass(glwContainerXClass)
	glwRegisterClass(glwContainerYClass)
	glwRegisterClass(glwContainerZClass)
	glwRegisterClass(glwTableClass)
}
