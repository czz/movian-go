package glw

// Canonical port of src/ui/glw/glw_bloom.c — the `bloom` widget class
// (children rendered normally + additive glow passes from downsampled
// render-to-texture targets).
//
// NOTE: glw_rtt_* are the real glw_opengl_ogl.go FBO functions — the
// canonical C's glw_rtt_enter contains an unconditional abort()
// (glw_opengl_ogl.c:78), so RTT rendering crashes identically in the
// Go port. bloomDestroyRtt/init paths work as in C.

import "unsafe"

const bloomEdgeSize = 16.0
const bloomCount = 3

// C: glw_rtt_t + glw_rtt_init/enter/restore/destroy/texture live in
// glw_opengl_ogl.go (canonical glw_opengl.h/glw_opengl_ogl.c port).

// C: glw_bloom_t
type glwBloom struct {
	w Glw

	bFlushctrl glwGfCtrl

	bGlow float32

	bWidth  int
	bHeight int
	bRtt    [bloomCount]glwRtt

	bRenderStarted int
	bRender        glwRenderer

	bNeedRender bool
}

// C: bloom_destroy_rtt
func bloomDestroyRtt(gr *glwRoot, b *glwBloom) {
	for i := range bloomCount {
		glwRttDestroy(gr, &b.bRtt[i])
	}
	b.bWidth = 0
	b.bHeight = 0
}

// C: glw_bloom_dtor
func glwBloomDtor(w *Glw) {
	b := (*glwBloom)(unsafe.Pointer(w))

	glwGfUnregister(&b.bFlushctrl)

	if b.bWidth != 0 || b.bHeight != 0 {
		bloomDestroyRtt(w.glwRoot, b)
	}

	glwRendererFree(&b.bRender)
}

// C: glw_bloom_render
func glwBloomRender(w *Glw, rc *glwRctx) {
	b := (*glwBloom)(unsafe.Pointer(w))
	a := rc.rcAlpha * w.glwAlpha
	var rc0 glwRctx

	rc0 = *rc
	rc0.rcAlpha = a
	for c := w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		glwRender0(c, &rc0)
	}

	if b.bGlow < glwAlphaEpsilon {
		return
	}

	if a > glwAlphaEpsilon {
		b.bNeedRender = true
	} else {
		b.bNeedRender = false
	}

	if !b.bNeedRender {
		return
	}

	if glwIsFocusableOrClickable(w) {
		glwStoreMatrix(w, rc)
	}

	rc0 = *rc

	glwScalef(&rc0,
		1.0+bloomEdgeSize/float32(rc.rcWidth),
		1.0+bloomEdgeSize/float32(rc.rcHeight),
		1.0)

	a *= b.bGlow

	glwBlendmode(w.glwRoot, glwBlendAdditive)
	glwRendererDraw(&b.bRender, w.glwRoot, &rc0,
		glwRttTexture(&b.bRtt[0]), nil,
		nil, nil, a*0.50, 0, nil)

	glwRendererDraw(&b.bRender, w.glwRoot, &rc0,
		glwRttTexture(&b.bRtt[1]), nil,
		nil, nil, a*0.44, 0, nil)

	glwRendererDraw(&b.bRender, w.glwRoot, &rc0,
		glwRttTexture(&b.bRtt[2]), nil,
		nil, nil, a*0.33, 0, nil)

	glwBlendmode(w.glwRoot, glwBlendNormal)
}

// C: glw_bloom_layout
func glwBloomLayout(w *Glw, rc *glwRctx) {
	b := (*glwBloom)(unsafe.Pointer(w))
	gr := w.glwRoot
	var rc0 glwRctx

	for c := w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		glwLayout0(c, rc)
	}

	if b.bGlow < glwAlphaEpsilon {
		if b.bWidth != 0 || b.bHeight != 0 {
			bloomDestroyRtt(gr, b)
		}
		return
	}

	sizx := int(rc.rcWidth) + int(bloomEdgeSize)
	sizy := int(rc.rcHeight) + int(bloomEdgeSize)

	if b.bWidth != sizx || b.bHeight != sizy {
		if b.bWidth != 0 || b.bHeight != 0 {
			bloomDestroyRtt(gr, b)
		}

		b.bWidth = sizx
		b.bHeight = sizy

		if b.bWidth != 0 || b.bHeight != 0 {
			for i := range bloomCount {
				x := b.bWidth / (2 << i)
				y := b.bHeight / (2 << i)
				glwRttSetup(gr, &b.bRtt[i], x, y, 1)
			}
		}
	}

	// Initialize output texture
	if b.bRenderStarted == 0 {
		glwRendererSetupQuad(&b.bRender)

		glwRendererVtxPos(&b.bRender, 0, -1.0, -1.0, 0.0)
		glwRendererVtxSt(&b.bRender, 0, 0.0, 0)

		glwRendererVtxPos(&b.bRender, 1, 1.0, -1.0, 0.0)
		glwRendererVtxSt(&b.bRender, 1, 1.0, 0)

		glwRendererVtxPos(&b.bRender, 2, 1.0, 1.0, 0.0)
		glwRendererVtxSt(&b.bRender, 2, 1.0, 1.0)

		glwRendererVtxPos(&b.bRender, 3, -1.0, 1.0, 0.0)
		glwRendererVtxSt(&b.bRender, 3, 0.0, 1.0)
	}

	rc0 = glwRctx{}
	rc0.rcAlpha = 1
	rc0.rcWidth = int16(b.bWidth - int(bloomEdgeSize))
	rc0.rcHeight = int16(b.bHeight - int(bloomEdgeSize))
	rc0.rcInhibitShadows = 1

	if !b.bNeedRender {
		return
	}

	for i := range bloomCount {
		glwRttEnter(gr, &b.bRtt[i], &rc0)

		rc0.rcWidth = int16(b.bWidth - int(bloomEdgeSize))
		rc0.rcHeight = int16(b.bHeight - int(bloomEdgeSize))

		glwScalef(&rc0,
			1.0-bloomEdgeSize/float32(b.bWidth),
			1.0-bloomEdgeSize/float32(b.bHeight),
			1.0)
		for c := w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
			glwRender0(c, &rc0)
		}
		glwRttRestore(gr, &b.bRtt[i])
	}
}

// C: glw_bloom_callback
func glwBloomCallback(w *Glw, opaque any, signal glwSignal, extra any) int {
	if signal == glwSignalChildConstraintsChanged {
		c, _ := extra.(*Glw)
		glwCopyConstraints(w, c)
		return 1
	}
	return 0
}

// C: bflush
func bloomBflush(aux any) {
	b := (*glwBloom)(aux.(unsafe.Pointer))

	if b.bWidth != 0 || b.bHeight != 0 {
		bloomDestroyRtt(b.w.glwRoot, b)
	}
}

// C: glw_bloom_ctor
func glwBloomCtor(w *Glw) {
	b := (*glwBloom)(unsafe.Pointer(w))
	b.bFlushctrl.opaque = unsafe.Pointer(b)
	b.bFlushctrl.flush = bloomBflush
	glwGfRegister(&b.bFlushctrl)
}

// C: glw_bloom_set_float
func glwBloomSetFloat(w *Glw, attrib glwAttribute, value float32, gs *GlwStyle) int {
	b := (*glwBloom)(unsafe.Pointer(w))

	if attrib == glwAttribValue {
		if b.bGlow == value {
			return 0
		}
		b.bGlow = value
		return 1
	}
	return -1
}

// C: glw_class_t glw_bloom
var glwBloomClass = &glwClass{
	gcName:         "bloom",
	gcInstanceSize: int(unsafe.Sizeof(glwBloom{})),
	gcNew: func(parent *Glw) *Glw {
		b := &glwBloom{}
		return &b.w
	},
	gcCtor:          glwBloomCtor,
	gcSetFloat:      glwBloomSetFloat,
	gcLayout:        glwBloomLayout,
	gcRender:        glwBloomRender,
	gcDtor:          glwBloomDtor,
	gcSignalHandler: glwBloomCallback,
}

func registerBloom() {
	glwRegisterClass(glwBloomClass)
}
