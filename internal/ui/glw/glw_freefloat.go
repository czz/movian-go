package glw

// Canonical port of src/ui/glw/glw_freefloat.c — the `freefloat`
// widget class (items float in/out with random rotation and depth
// ordering; retired visible children keep animating until gone).

import (
	"cmp"
	"math"
	"slices"
	"unsafe"

	archpkg "github.com/czz/movian-go/internal/arch"
)

const glwFreefloatMaxVisible = 5

// C: glw_freefloat_t
type glwFreefloat struct {
	w          Glw
	xpos       int
	numVisible int
	pick       *Glw
	visible    [glwFreefloatMaxVisible]*Glw
	rand       int32 // C: int — 32-bit wrap semantics preserved
}

// C: glw_freefloat_item_t (per-child parent data)
type glwFreefloatItem struct {
	v  float32
	s  float32
	s2 float32
	a  float32
	x  float32
	y  float32
}

// C: glw_parent_data(c, glw_freefloat_item_t)
func freefloatItem(c *Glw) *glwFreefloatItem {
	return c.glwParentData.(*glwFreefloatItem)
}

// C: is_visible
func freefloatIsVisible(ff *glwFreefloat, c *Glw) int {
	for i := range glwFreefloatMaxVisible {
		if ff.visible[i] == c {
			return 1
		}
	}
	return 0
}

// C: glw_freefloat_render
func glwFreefloatRender(w *Glw, rc *glwRctx) {
	ff := (*glwFreefloat)(unsafe.Pointer(w))
	var rc0 glwRctx
	zmax := 0

	for i := range ff.numVisible {
		c := ff.visible[i]
		if c == nil {
			continue
		}

		cd := freefloatItem(c)

		rc0 = *rc
		rc0.rcZmax = &zmax

		a := 1 - float32(math.Abs(float64(-1+glwMax(0, -0.1+cd.v*2.1))))

		rc0.rcAlpha *= a

		glwTranslatef(&rc0,
			cd.x,
			cd.y,
			-5+cd.v*5)

		glwRotatef(&rc0,
			-30+cd.v*60,
			float32(math.Abs(math.Sin(float64(cd.a)))),
			float32(math.Abs(math.Cos(float64(cd.a)))),
			0.0)

		rc0.rcZindex = int16(max(zmax, int(rc.rcZindex)))
		glwRender0(c, &rc0)
		zmax = max(zmax, int(rc0.rcZindex)+1)
	}
	if rc.rcZmax != nil {
		*rc.rcZmax = max(*rc.rcZmax, zmax)
	}
}

// C: setup_floater
func freefloatSetupFloater(ff *glwFreefloat, c *Glw) {
	ff.xpos++
	cd := freefloatItem(c)
	cd.v = 0
	cd.s = 0.001
	cd.s2 = 0

	cd.a = float32(archpkg.GetTS())
	cd.x = -1.0 + float32(ff.xpos%ff.numVisible)*2/
		float32(ff.numVisible-1)

	ff.rand = ff.rand*1664525 + 1013904223

	cd.y = float32(float64(ff.rand&0xffff)/32768.0 - 1.0)
}

// C: glw_freefloat_layout
func glwFreefloatLayout(w *Glw, rc *glwRctx) {
	ff := (*glwFreefloat)(unsafe.Pointer(w))
	candpos := -1

	vmin := float32(1.0)

	for i := range ff.numVisible {
		if ff.visible[i] == nil {
			candpos = i
		} else {
			cd := freefloatItem(ff.visible[i])
			vmin = glwMin(cd.v, vmin)
		}
	}

	if vmin > 1.0/float32(ff.numVisible) && candpos != -1 {
		/* Insert new entry */

		if ff.pick != nil {
			ff.pick = glwNextWidget(ff.pick)
		}

		if ff.pick == nil {
			ff.pick = glwFirstWidget(w)
		}

		if ff.pick != nil && freefloatIsVisible(ff, ff.pick) == 0 {
			ff.visible[candpos] = ff.pick
			freefloatSetupFloater(ff, ff.pick)
		}
	}

	for i := range ff.numVisible {
		c := ff.visible[i]
		if c == nil {
			continue
		}

		cd := freefloatItem(c)

		if cd.v >= 1 {
			ff.visible[i] = nil
		} else {
			glwLayout0(c, rc)
		}

		if c.glwClass.gcStatus == nil || c.glwClass.gcStatus(c) == glwStatusLoaded {
			cd.v += cd.s
		}
		cd.s += cd.s2
	}

	// C: qsort(ff->visible, ff->num_visible, ..., zsort) — ascending by
	// item v (nil entries sort first via -100 sentinel).
	slices.SortFunc(ff.visible[:ff.numVisible], func(a, b *Glw) int {
		az := float32(-100)
		bz := float32(-100)
		if a != nil {
			az = freefloatItem(a).v
		}
		if b != nil {
			bz = freefloatItem(b).v
		}
		return cmp.Compare(az, bz)
	})

	c := ff.pick

	// Layout next few items to pick, to preload textures, etc
	for i := 0; i < 3 && c != nil; i++ {
		if freefloatIsVisible(ff, c) == 0 {
			glwLayout0(c, rc)
		}
		c = glwNextWidget(c)
	}
}

// C: glw_freefloat_retire_child
func glwFreefloatRetireChild(w *Glw, c *Glw) {
	ff := (*glwFreefloat)(unsafe.Pointer(w))
	if freefloatIsVisible(ff, c) != 0 {
		// This one is visible, keep it for a while

		cd := freefloatItem(c)

		cd.s2 = 0.001

		if c == ff.pick {
			ff.pick = ff.pick.glwParentLinkNext
		}

		glwRemoveFromParent(c, w)
		return
	}
	// Destroy at once
	glwDestroy(c)
}

// C: glw_freefloat_callback — C asserts !is_visible(ff, c) on
// CHILD_DESTROYED; the assert is compiled out in release builds,
// omitted here.
func glwFreefloatCallback(w *Glw, opaque any, signal glwSignal, extra any) int {
	ff := (*glwFreefloat)(unsafe.Pointer(w))

	if signal == glwSignalChildDestroyed {
		c, _ := extra.(*Glw)
		if c == ff.pick {
			ff.pick = ff.pick.glwParentLinkNext
		}
	}
	return 0
}

// C: glw_freefloat_ctor
func glwFreefloatCtor(w *Glw) {
	ff := (*glwFreefloat)(unsafe.Pointer(w))
	ff.rand = int32(archpkg.GetTS())
	ff.numVisible = glwFreefloatMaxVisible
}

// C: glw_class_t glw_freefloat
var glwFreefloatClass = &glwClass{
	gcName:           "freefloat",
	gcInstanceSize:   int(unsafe.Sizeof(glwFreefloat{})),
	gcParentDataSize: int(unsafe.Sizeof(glwFreefloatItem{})),
	gcNewParentData:  func() any { return &glwFreefloatItem{} },
	gcNew: func(parent *Glw) *Glw {
		ff := &glwFreefloat{}
		return &ff.w
	},
	gcFlags:         glwCanHideChilds,
	gcCtor:          glwFreefloatCtor,
	gcLayout:        glwFreefloatLayout,
	gcRender:        glwFreefloatRender,
	gcRetireChild:   glwFreefloatRetireChild,
	gcSignalHandler: glwFreefloatCallback,
}

func registerFreefloat() {
	glwRegisterClass(glwFreefloatClass)
}
