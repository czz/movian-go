package glw

// Canonical port of src/ui/glw/glw_clist.c — the `clist` widget class
// (vertical list with lerped item positions, spacing, and
// top/bottom clip fades).

import (
	"unsafe"

	eventpkg "github.com/czz/movian-go/internal/event"
)

// C: glw_clist_t
type glwClist struct {
	w Glw

	childAspect float32

	totalSize  int
	currentPos int
	pageSize   int

	scrollToMe *Glw

	suggested  *Glw
	suggestCnt int

	metrics glwSliderMetrics

	savedHeight int16
	savedWidth  int16

	trail float32

	spacing int

	center float32

	childHeight int
}

// C: glw_clist_item_t (per-child parent data)
type glwClistItem struct {
	pos     float32
	height  int16
	height2 int16
	started uint8
}

// C: glw_parent_data(c, glw_clist_item_t)
func clistItem(c *Glw) *glwClistItem {
	return c.glwParentData.(*glwClistItem)
}

// C: glw_clist_layout
func glwClistLayout(w *Glw, rc *glwRctx) {
	l := (*glwClist)(unsafe.Pointer(w))
	ypos := 0
	rc0 := *rc
	itemh0 := l.childHeight
	if itemh0 == 0 {
		itemh0 = int(float32(rc.rcHeight) * 0.1)
	}
	var itemh int
	lptrail := 1

	for c := w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		if c.glwFlags&glwHidden != 0 {
			continue
		}
		cd := clistItem(c)
		tpos := float32(ypos-l.currentPos) + float32(rc.rcHeight)*l.center

		if cd.started != 0 {
			glwLp(&cd.pos, w.glwRoot, tpos, 0.25)
			lptrail = 1
		} else {
			cd.pos = tpos
			cd.started = 1
			lptrail = 0
		}

		f := glwFilterConstraints(c)

		if f&glwConstraintY != 0 {
			itemh = glwReqHeight(c)
		} else {
			itemh = itemh0
		}
		cd.height2 = int16(itemh)
		ypos += itemh
		if c == w.glwFocused {
			l.currentPos = ypos - itemh/2
		}
		ypos += l.spacing
	}

	if lptrail != 0 {
		glwLp(&l.trail, w.glwRoot,
			float32(ypos-l.currentPos)+float32(rc.rcHeight)*l.center,
			0.25)
	} else {
		l.trail = float32(ypos-l.currentPos) + float32(rc.rcHeight)*l.center
	}

	for c := w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		if c.glwFlags&glwHidden != 0 {
			continue
		}

		cd := clistItem(c)

		n := glwNextWidget(c)

		var nextPos float32
		if n != nil {
			nextPos = clistItem(n).pos
		} else {
			nextPos = l.trail
		}
		rc0.rcHeight = int16(nextPos - cd.pos - float32(l.spacing))

		cd.height = rc0.rcHeight
		if cd.height < 1 {
			continue
		}
		rc0.rcHeight = cd.height2

		glwLayout0(c, &rc0)
	}
}

// C: render
func glwClistRender(w *Glw, rc *glwRctx) {
	var rc0, rc1 glwRctx

	if rc.rcAlpha < glwAlphaEpsilon {
		return
	}

	glwStoreMatrix(w, rc)

	rc0 = *rc

	for c := w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		if c.glwFlags&glwHidden != 0 {
			continue
		}
		cd := clistItem(c)
		if cd.height < 1 {
			continue
		}

		y := cd.pos
		if y+float32(cd.height) < 0 || y > float32(rc.rcHeight) {
			c.glwFlags |= glwClipped
			continue
		} else {
			c.glwFlags &^= glwClipped
		}

		t := -1
		if y < 0 {
			t = glwClipEnable(w.glwRoot, rc, glwClipTop, 0, 0, 1)
		}

		b := -1
		if y+float32(cd.height) > float32(rc.rcHeight) {
			b = glwClipEnable(w.glwRoot, rc, glwClipBottom, 0, 0, 1)
		}

		rc1 = rc0

		adj := (int(cd.height2) - int(cd.height)) / 2

		glwReposition(&rc1,
			0,
			int(rc.rcHeight)-int(cd.pos)+adj,
			int(rc.rcWidth),
			int(rc.rcHeight)-int(cd.pos)-int(cd.height2)+adj)

		glwRender0(c, &rc1)

		if t != -1 {
			glwClipDisable(w.glwRoot, t)
		}
		if b != -1 {
			glwClipDisable(w.glwRoot, b)
		}
	}
}

// C: signal_handler
func glwClistCallback(w *Glw, opaque any, signal glwSignal, extra any) int {
	if signal == glwSignalFocusChildInteractive {
		w.glwFlags &^= glwFloatingFocus
		return 0
	}
	return 0
}

// C: ctor
func glwClistCtor(w *Glw) {
	l := (*glwClist)(unsafe.Pointer(w))
	w.glwFlags |= glwFloatingFocus
	l.center = 0.5
}

// C: glw_clist_set_int
func glwClistSetInt(w *Glw, attrib glwAttribute, value int, gs *GlwStyle) int {
	l := (*glwClist)(unsafe.Pointer(w))

	if attrib == glwAttribSpacing {
		if l.spacing == value {
			return 0
		}
		l.spacing = value
		return 1
	}
	return -1
}

// C: glw_clist_set_float
func glwClistSetFloat(w *Glw, attrib glwAttribute, value float32, gs *GlwStyle) int {
	l := (*glwClist)(unsafe.Pointer(w))

	if attrib == glwAttribCenter {
		if l.center == value {
			return 0
		}
		l.center = value
		return 1
	}
	return -1
}

// C: glw_class_t glw_clist
var glwClistClass = &glwClass{
	gcName:           "clist",
	gcInstanceSize:   int(unsafe.Sizeof(glwClist{})),
	gcParentDataSize: int(unsafe.Sizeof(glwClistItem{})),
	gcNewParentData:  func() any { return &glwClistItem{} },
	gcNew: func(parent *Glw) *Glw {
		l := &glwClist{}
		return &l.w
	},
	gcFlags:         glwNavigationSearchBoundary | glwCanHideChilds,
	gcLayout:        glwClistLayout,
	gcRender:        glwClistRender,
	gcCtor:          glwClistCtor,
	gcSignalHandler: glwClistCallback,
	gcSetInt:        glwClistSetInt,
	gcSetFloat:      glwClistSetFloat,
	gcBubbleEvent: func(w *Glw, e *eventpkg.Event) int {
		return glwNavigateVertical(w, e)
	},
}

func registerClist() {
	glwRegisterClass(glwClistClass)
}
