package glw

import "math"

// glw_transitions.c — page/widget transition effects.
// 1:1 port of src/ui/glw/glw_transitions.c.

// glwS — C: GLW_S(a) = sin(GLW_LERP(a, -pi/2, pi/2)) * 0.5 + 0.5
func glwS(a float32) float32 {
	return float32(math.Sin(float64(glwLerp(a, math.Pi*-0.5, math.Pi*0.5))))*0.5 + 0.5
}

// transBlend — C: trans_blend
func transBlend(b, alpha float32, rc *glwRctx) {
	rc.rcAlpha = alpha * float32(1-math.Abs(float64(b)))
}

// transFlipHorizontal — C: trans_flip_horizontal
func transFlipHorizontal(b, alpha float32, rc *glwRctx) {
	var v float32

	if b > 0 {
		v = 1 - b
		glwTranslatef(rc, 0, 0, -1)
		glwRotatef(rc, (1-v)*-90, 1, 0, 0)
		glwTranslatef(rc, 0, 0, 1)
		rc.rcAlpha = alpha * v
	} else {
		v = -b
		glwTranslatef(rc, 0, 0, -1)
		glwRotatef(rc, v*90, 1, 0, 0)
		glwTranslatef(rc, 0, 0, 1)
		rc.rcAlpha = alpha * (1 - v)
	}
}

// transFlipVertical — C: trans_flip_vertical
func transFlipVertical(b, alpha float32, rc *glwRctx) {
	var v float32

	if b > 0 {
		v = 1 - b
		glwTranslatef(rc, 0, 0, -1)
		glwRotatef(rc, (1-v)*-90, 0, 1, 0)
		glwTranslatef(rc, 0, 0, 1)
		rc.rcAlpha = alpha * v
	} else {
		v = -b
		glwTranslatef(rc, 0, 0, -1)
		glwRotatef(rc, v*90, 0, 1, 0)
		glwTranslatef(rc, 0, 0, 1)
		rc.rcAlpha = alpha * (1 - v)
	}
}

// transSlideHorizontal — C: trans_slide_horizontal
func transSlideHorizontal(b, alpha float32, rc *glwRctx) {
	rc.rcAlpha = alpha * glwS(1-float32(math.Abs(float64(b))))
	glwTranslatef(rc, -2*b, 0, 0)
}

// transSlideVertical — C: trans_slide_vertical
func transSlideVertical(b, alpha float32, rc *glwRctx) {
	if b > 0 {
		b = glwS(b)
	} else {
		b = -glwS(-b)
	}

	rc.rcAlpha = alpha * (1 - b)

	glwTranslatef(rc, 0, 2*b, 0)
}

// transNone — C: trans_none
func transNone(b, alpha float32, rc *glwRctx) {
	rc.rcAlpha = alpha
}

// glwTransitionEffects — C: glw_transition_effects[]
var glwTransitionEffects = [glwTransNum]func(b, alpha float32, rc *glwRctx){
	glwTransNone:            transNone,
	glwTransBlend:           transBlend,
	glwTransFlipHorizontal:  transFlipHorizontal,
	glwTransFlipVertical:    transFlipVertical,
	glwTransSlideHorizontal: transSlideHorizontal,
	glwTransSlideVertical:   transSlideVertical,
}

// glwTransitionRender — C: glw_transition_render
func glwTransitionRender(t glwTransitionType, b, alpha float32, rc *glwRctx) {
	glwTransitionEffects[t](b, alpha, rc)
}
