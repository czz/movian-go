package glw

// Canonical port of src/ui/glw/glw_segway.c — the `segway` widget class
// (child slides in/out from a screen edge with interpolated width+alpha).

import (
	"math"
	"unsafe"

	miscpkg "github.com/czz/movian-go/internal/misc"
)

// C: glw_segway_t — direction values reuse segwayDirectionLeft/Right
// declared in glw_deck.go (same C strtab values: LEFT=1, RIGHT=2).
type glwSegway struct {
	w                  Glw
	direction          int
	reqWidth           int
	actualWidth        float32
	alpha              float32
	actualWidthRounded int
}

// C: glw_segway_layout
func glwSegwayLayout(w *Glw, rc *glwRctx) {
	s := (*glwSegway)(unsafe.Pointer(w))

	c := w.glwChilds.tqhFirst
	if c == nil {
		return
	}

	s.reqWidth = 0
	f := glwFilterConstraints(c)

	if f&glwConstraintX != 0 {
		s.reqWidth = glwReqWidth(c)
	} else if f&glwConstraintW != 0 && c.glwReqWeight < 0 {
		s.reqWidth = int(float32(rc.rcHeight) * -c.glwReqWeight)
	}

	rc0 := *rc
	var alpha float32
	if s.reqWidth == 0 || s.direction == 0 {
		rc0.rcWidth = int16(w.glwRoot.grWidth)
		rc0.rcSegwayed = 1
		alpha = 0
	} else {
		rc0.rcWidth = int16(s.reqWidth)
		alpha = 1
	}

	glwLp(&s.alpha, w.glwRoot, alpha, 0.25)
	glwLp(&s.actualWidth, w.glwRoot, float32(s.reqWidth), 0.25)
	s.actualWidthRounded = int(math.Round(float64(s.actualWidth)))

	rc0.rcAlpha *= s.alpha
	glwLayout0(c, &rc0)
	glwSetConstraints(w, s.actualWidthRounded, 0, 0, glwConstraintX)
}

// C: glw_segway_render
func glwSegwayRender(w *Glw, rc *glwRctx) {
	s := (*glwSegway)(unsafe.Pointer(w))

	c := w.glwChilds.tqhFirst
	if c == nil {
		return
	}

	if s.reqWidth == 0 || s.direction == 0 {
		return
	}

	rc0 := *rc

	displacement := s.reqWidth - s.actualWidthRounded

	switch s.direction {
	case segwayDirectionLeft:
		glwReposition(&rc0, -displacement, int(rc.rcHeight),
			int(rc.rcWidth), 0)
	case segwayDirectionRight:
		glwReposition(&rc0, 0, int(rc.rcHeight),
			int(rc.rcWidth)+displacement, 0)
	}
	rc0.rcWidth = int16(s.reqWidth)
	rc0.rcAlpha *= s.alpha
	glwRender0(c, &rc0)
}

// C: static struct strtab segway_directions[]
var segwayDirections = []struct {
	str string
	val int
}{
	{"left", segwayDirectionLeft},
	{"right", segwayDirectionRight},
}

// C: glw_segway_set_rstr_unresolved
func glwSegwaySetRstrUnresolved(w *Glw, a string, value *miscpkg.Rstr, gs *GlwStyle) int {
	if a == "direction" {
		s := (*glwSegway)(unsafe.Pointer(w))
		s.direction = str2val(miscpkg.RstrGet(value), segwayDirections)
		return glwSetRerenderRequired
	}
	return glwSetNotResponding
}

// C: glw_segway_ctor
func glwSegwayCtor(w *Glw) {
	glwSetConstraints(w, 0, 0, 0, glwConstraintX)
}

// C: glw_class_t glw_segway
var glwSegwayClass = &glwClass{
	gcName:         "segway",
	gcInstanceSize: int(unsafe.Sizeof(glwSegway{})),
	gcNew: func(parent *Glw) *Glw {
		s := &glwSegway{}
		return &s.w
	},
	gcCtor:              glwSegwayCtor,
	gcLayout:            glwSegwayLayout,
	gcRender:            glwSegwayRender,
	gcSetRstrUnresolved: glwSegwaySetRstrUnresolved,
}

func registerSegway() {
	glwRegisterClass(glwSegwayClass)
}
