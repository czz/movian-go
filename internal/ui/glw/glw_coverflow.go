package glw

// Canonical port of src/ui/glw/glw_coverflow.c — the `coverflow`
// widget class (centered cover-flow carousel with rotated side items,
// rendered outside-in from the nearest-to-center item).

import (
	"math"
	"slices"
	"unsafe"

	eventpkg "github.com/czz/movian-go/internal/event"
)

// C: glw_coverflow_t
type glwCoverflow struct {
	w Glw

	scrollToMe *Glw

	pos       float32
	posTarget float32

	rstart *Glw

	xs float32
}

// C: glw_coverflow_item_t (per-child parent data)
type glwCoverflowItem struct {
	pos float32
}

// C: glw_parent_data(c, glw_coverflow_item_t)
func coverflowItem(c *Glw) *glwCoverflowItem {
	return c.glwParentData.(*glwCoverflowItem)
}

// C: glw_coverflow_layout
func glwCoverflowLayout(w *Glw, rc *glwRctx) {
	gc := (*glwCoverflow)(unsafe.Pointer(w))
	var n float32
	var rstart *Glw
	var rc0 glwRctx
	gc.xs = float32(rc.rcHeight) / float32(rc.rcWidth)

	rc0 = *rc
	rc0.rcWidth = rc.rcHeight

	glwLp(&gc.pos, gc.w.glwRoot, gc.posTarget, 0.2)

	for c := gc.w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		if c.glwFlags&glwHidden != 0 {
			continue
		}

		cd := coverflowItem(c)

		cd.pos = n - gc.pos

		if rstart == nil ||
			math.Abs(float64(coverflowItem(rstart).pos)) > math.Abs(float64(cd.pos)) {
			rstart = c
		}

		nv := cd.pos * gc.xs
		if nv > -2 && nv < 2 {
			glwLayout0(c, &rc0)
		}

		if gc.scrollToMe == c {
			gc.posTarget = n
		}
		n++
	}
	gc.rstart = rstart
}

// C: renderone
func coverflowRenderOne(rc *glwRctx, c *Glw, gc *glwCoverflow) {
	if c.glwFlags&glwHidden != 0 {
		return
	}

	cd := coverflowItem(c)
	v := cd.pos

	if v < -1.5 || v > 1.5 {
		return
	}

	rc0 := *rc

	r := glwMax(glwMin(v, 1.0), -1.0) * 60

	if v < -1 {
		v = v - 1
	} else if v > 1 {
		v = v + 1
	} else {
		v *= 2
	}

	glwTranslatef(&rc0, v, 0.0, 0.0)
	glwRotatef(&rc0, -r, 0, 1.0, 0.0)
	rc0.rcZindex = int16(-math.Abs(float64(v)))

	glwRender0(c, &rc0)
}

// C: reposition (local helper — same math as glwReposition but float args)
func coverflowReposition(rc *glwRctx, left, top, right, bottom int) {
	sx := float32(right-left) / float32(rc.rcWidth)
	tx := -1.0 + float32(right+left)/float32(rc.rcWidth)
	sy := float32(top-bottom) / float32(rc.rcHeight)
	ty := -1.0 + float32(top+bottom)/float32(rc.rcHeight)

	glwTranslatef(rc, tx, ty, 0)
	glwScalef(rc, sx, sy, 1.0)

	rc.rcWidth = int16(right - left)
	rc.rcHeight = int16(top - bottom)
}

// C: glw_coverflow_render — renders children from the center out
// (TAILQ rqueue rebuilt per frame; Go slice replicates insert-head).
func glwCoverflowRender(w *Glw, rc *glwRctx) {
	gc := (*glwCoverflow)(unsafe.Pointer(w))
	rc0 := *rc

	c := gc.rstart
	if c == nil {
		return
	}

	left := int(rc.rcWidth)/2 - int(rc.rcHeight)/2
	right := left + int(rc.rcHeight)

	coverflowReposition(&rc0, left, int(rc.rcHeight), right, 0)

	rqueue := []*Glw{c}

	p, n := c, c

	for {
		if p != nil {
			p = glwPrevWidget(p)
		}
		if n != nil {
			n = glwNextWidget(n)
		}

		if p == nil && n == nil {
			break
		}

		if p != nil {
			rqueue = slices.Insert(rqueue, 0, p)
		}
		if n != nil {
			rqueue = slices.Insert(rqueue, 0, n)
		}
	}

	for _, cc := range rqueue {
		coverflowRenderOne(&rc0, cc, gc)
	}
}

// C: glw_coverflow_ctor
func glwCoverflowCtor(w *Glw) {
	w.glwFlags |= glwFloatingFocus
}

// C: glw_coverflow_callback
func glwCoverflowCallback(w *Glw, opaque any, signal glwSignal, extra any) int {
	gc := (*glwCoverflow)(unsafe.Pointer(w))

	switch signal {
	case glwSignalFocusChildInteractive:
		gc.scrollToMe, _ = extra.(*Glw)
		w.glwFlags &^= glwFloatingFocus
		return 0

	case glwSignalChildDestroyed:
		if gc.scrollToMe == extra {
			gc.scrollToMe = nil
		}
		if gc.rstart == extra {
			gc.rstart = nil
		}
	}
	return 0
}

// C: glw_class_t glw_coverflow
var glwCoverflowClass = &glwClass{
	gcName:           "coverflow",
	gcInstanceSize:   int(unsafe.Sizeof(glwCoverflow{})),
	gcParentDataSize: int(unsafe.Sizeof(glwCoverflowItem{})),
	gcNewParentData:  func() any { return &glwCoverflowItem{} },
	gcNew: func(parent *Glw) *Glw {
		gc := &glwCoverflow{}
		return &gc.w
	},
	gcCtor:             glwCoverflowCtor,
	gcFlags:            glwNavigationSearchBoundary | glwCanHideChilds,
	gcLayout:           glwCoverflowLayout,
	gcRender:           glwCoverflowRender,
	gcSignalHandler:    glwCoverflowCallback,
	gcDefaultAlignment: layoutAlignCenter,
	gcBubbleEvent: func(w *Glw, e *eventpkg.Event) int {
		return glwNavigateHorizontal(w, e)
	},
}

func registerCoverflow() {
	glwRegisterClass(glwCoverflowClass)
}
