package glw

// glw_image.c — image widget (image/icon/backdrop/frontdrop/repeatedimage
// classes): texture loading, scaling modes, constraints, render. 1:1 port of
// src/ui/glw/glw_image.c.

import (
	"unsafe"

	miscpkg "github.com/czz/movian-go/internal/misc"
)

// C: gi_mode (glw_image.c:55-59)
const (
	giModeNormal            = 0 // GI_MODE_NORMAL
	giModeBorderScaling     = 1 // GI_MODE_BORDER_SCALING
	giModeRepeatedTexture   = 2 // GI_MODE_REPEATED_TEXTURE
	giModeAlphaEdges        = 3 // GI_MODE_ALPHA_EDGES
	giModeBorderOnlyScaling = 4 // GI_MODE_BORDER_ONLY_SCALING
)

// C: static glw_class_t glw_image, glw_icon, glw_backdrop,
// glw_repeatedimage, glw_frontdrop (glw_image.c:110-111). Populated in
// init() — same Go init-cycle pattern as glwTextClass.
var glwImageClass glwClass
var glwIconClass glwClass
var glwBackdropClass glwClass
var glwRepeatedimageClass glwClass
var glwFrontdropClass glwClass

// C: static int8_t tex_transform[9][4] (glw_image.c:113-123)
var texTransform = [9][4]int8{
	{1, 0, 0, 1},   // No transform
	{1, 0, 0, 1},   // No transform
	{-1, 0, 0, 1},  // Mirror X
	{-1, 0, 0, -1}, // 180° rotate
	{1, 0, 0, -1},  // Mirror Y
	{0, 1, 1, 0},   // Transpose
	{0, 1, -1, 0},  // 90° rotate
	{0, 1, 1, 0},   // Transverse ???
	{0, -1, 1, 0},  // 270° rotate
}

// C: static const float alphaborder[4][4] (glw_image.c:425-430)
var alphaborder = [4][4]float32{
	{0, 0, 0, 0},
	{0, 1, 1, 0},
	{0, 1, 1, 0},
	{0, 0, 0, 0},
}

// C: static uint16_t borderobject[] (glw_image.c:625-644)
var borderobject = []uint16{
	4, 1, 0,
	4, 5, 1,
	5, 2, 1,
	5, 6, 2,
	6, 7, 2,
	2, 7, 3,
	8, 5, 4,
	8, 9, 5,
	9, 6, 5,
	9, 10, 6,
	10, 7, 6,
	10, 11, 7,
	12, 13, 8,
	8, 13, 9,
	13, 10, 9,
	13, 14, 10,
	14, 11, 10,
	14, 15, 11,
}

// C: static uint16_t borderonlyobject[] (glw_image.c:646-668)
var borderonlyobject = []uint16{
	4, 1, 0,
	4, 5, 1,
	5, 2, 1,
	5, 6, 2,
	6, 7, 2,
	2, 7, 3,
	8, 5, 4,
	8, 9, 5,
	//  9, 6, 5,
	//  9, 10, 6,
	10, 7, 6,
	10, 11, 7,
	12, 13, 8,
	8, 13, 9,
	13, 10, 9,
	13, 14, 10,
	14, 11, 10,
	14, 15, 11,
}

// C: LIST_INSERT_HEAD(&gr->gr_icons, gi, gi_link)
func giListInsertHead(l *glwImageList, e *GlwImage) {
	e.giLinkNext = l.lhFirst
	if e.giLinkNext != nil {
		e.giLinkNext.giLinkPrev = &e.giLinkNext
	}
	l.lhFirst = e
	e.giLinkPrev = &l.lhFirst
}

// C: LIST_REMOVE(gi, gi_link)
func giListRemove(e *GlwImage) {
	if e.giLinkNext != nil {
		e.giLinkNext.giLinkPrev = e.giLinkPrev
	}
	*e.giLinkPrev = e.giLinkNext
}

func iabs(x int) int { // C: abs
	if x < 0 {
		return -x
	}
	return x
}

// C: static void set_load_status (glw_image.c:132-139)
func giSetLoadStatus(gi *GlwImage, status glwWidgetStatus) {
	if gi.giWidgetStatus == status {
		return
	}
	gi.giWidgetStatus = status
	glwSignal0(&gi.w, glwSignalStatusChanged, nil)
}

// C: static void sources_free (glw_image.c:145-155)
func giSourcesFree(v []*miscpkg.Rstr) {
	for i := range v {
		miscpkg.RstrRelease(v[i])
	}
}

// C: static void glw_image_dtor (glw_image.c:161-179)
func glwImageDtor(w *Glw) {
	gi := (*GlwImage)(unsafe.Pointer(w))

	giSourcesFree(gi.giSources)

	miscpkg.RstrRelease(gi.giPendingUrl)

	if gi.giCurrent != nil {
		glwTexDeref(w.glwRoot, gi.giCurrent)
	}

	if gi.giPending != nil {
		glwTexDeref(w.glwRoot, gi.giPending)
	}
	glwRendererFree(&gi.giGr)
	miscpkg.RstrRelease(gi.giFs)
	glwDestroyProgram(w.glwRoot, gi.giGpa.GpaProg)
}

// C: static void glw_image_set_fs (glw_image.c:184-192)
func glwImageSetFs(w *Glw, fs *miscpkg.Rstr) {
	gi := (*GlwImage)(unsafe.Pointer(w))
	miscpkg.RstrSet(&gi.giFs, fs)
	gi.giRecompile = true
}

// C: static void glw_icon_dtor (glw_image.c:196-203)
func glwIconDtor(w *Glw) {
	gi := (*GlwImage)(unsafe.Pointer(w))
	giListRemove(gi)
	glwImageDtor(w)
}

// C: static void render_child_simple (glw_image.c:208-218)
func renderChildSimple(w *Glw, rc *glwRctx) {
	rc0 := *rc
	c := w.glwChilds.tqhFirst

	rc0.rcAlpha = rc.rcAlpha * w.glwAlpha
	glwRender0(c, &rc0)
}

// C: static void render_child_autocentered (glw_image.c:223-239)
func renderChildAutocentered(gi *GlwImage, rc *glwRctx) {
	c := gi.w.glwChilds.tqhFirst

	rc0 := *rc

	glwReposition(&rc0,
		int(gi.giBoxLeft),
		int(rc.rcHeight)-int(gi.giBoxTop),
		int(rc.rcWidth)-int(gi.giBoxRight),
		int(gi.giBoxBottom))

	rc0.rcAlpha *= gi.w.glwAlpha
	glwRender0(c, &rc0)
}

// C: static void glw_scale_to_pixels (glw_image.c:245-257)
func glwScaleToPixels(rc *glwRctx, w int, h int) {
	xs := float32(w) / float32(rc.rcWidth)
	ys := float32(h) / float32(rc.rcHeight)

	glwScalef(rc, xs, ys, 1.0)
	rc.rcWidth = int16(w)
	rc.rcHeight = int16(h)
}

// C: static void glw_image_render (glw_image.c:261-382)
func glwImageRender(w *Glw, rc *glwRctx) {
	gi := (*GlwImage)(unsafe.Pointer(w))

	if gi.giExternalized {
		return
	}

	glt := gi.giCurrent
	blur := 1 - rc.rcSharpness*w.glwSharpness

	if gi.giRecompile {
		glwDestroyProgram(w.glwRoot, gi.giGpa.GpaProg)
		gi.giGpa.GpaProg =
			glwMakeProgram(w.glwRoot, "", miscpkg.RstrGet(gi.giFs))
		gi.giRecompile = false
	}

	alphaSelf := rc.rcAlpha * w.glwAlpha * gi.giAlphaSelf * gi.giAutofade

	var rgbOff *glwRgb
	if gi.giSaturated {
		rgbOff = &gi.giColOff
	}

	if gi.giMode == giModeNormal || gi.giMode == giModeAlphaEdges {

		if glt == nil || !glwIsTexStarted(&glt.gltTexture) {
			return
		}

		rc0 := *rc

		glwAlign1(&rc0, int(w.glwAlignment))

		if gi.giBitmapFlags&glwImageFixedSize != 0 {
			glwScaleToPixels(&rc0, int(glt.gltXs), int(glt.gltYs))

		} else if w.glwClass == &glwImageClass || w.glwClass == &glwIconClass {

			extraYMargin := 0
			if w.glwClass == &glwIconClass {

				ys := int(gi.giFixedSize)
				if ys == 0 {
					ys = int(gi.giSizeScale * float32(w.glwRoot.grCurrentSize))
				}
				if ys > int(rc.rcHeight) {
					ys = int(rc.rcHeight)
				}

				extraYMargin = max(int(rc.rcHeight)-ys, 0) / 2
				glwReposition(&rc0, 0,
					int(rc0.rcHeight)-extraYMargin,
					int(rc0.rcWidth),
					extraYMargin)
			}
			glwScaleToAspect(&rc0, glt.gltAspect)
		}
		if gi.giAngle != 0 {
			glwRotatef(&rc0, -gi.giAngle, 0, 0, 1)
		}

		glwAlign2(&rc0, int(w.glwAlignment))

		if glwIsFocusableOrClickable(w) {
			glwStoreMatrix(w, &rc0)
		}

		if w.glwClass == &glwFrontdropClass && w.glwChilds.tqhFirst != nil {
			renderChildSimple(w, &rc0)
			glwZinc(&rc0)
		}

		if alphaSelf > glwAlphaEpsilon {

			if gi.giBitmapFlags&glwImageAdditive != 0 {
				glwBlendmode(w.glwRoot, glwBlendAdditive)
			}

			var gpa *GlwProgramArgs
			if gi.giGpa.GpaProg != nil {
				gpa = &gi.giGpa
			}
			glwRendererDraw(&gi.giGr, w.glwRoot, &rc0,
				&glt.gltTexture, nil,
				&gi.giColMul, rgbOff, alphaSelf, blur, gpa)

			if gi.giBitmapFlags&glwImageAdditive != 0 {
				glwBlendmode(w.glwRoot, glwBlendNormal)
			}
		}

		if w.glwClass != &glwFrontdropClass && w.glwChilds.tqhFirst != nil {
			glwZinc(&rc0)
			renderChildSimple(w, &rc0)
		}

	} else {

		rc0 := *rc

		if glwIsFocusableOrClickable(w) {
			glwStoreMatrix(w, &rc0)
		}

		if w.glwClass == &glwFrontdropClass && w.glwChilds.tqhFirst != nil {
			renderChildAutocentered(gi, &rc0)
			glwZinc(&rc0)
		}

		if glt != nil && glwIsTexStarted(&glt.gltTexture) &&
			alphaSelf > glwAlphaEpsilon {

			if gi.giBitmapFlags&glwImageAdditive != 0 {
				glwBlendmode(w.glwRoot, glwBlendAdditive)
			}

			var gpa *GlwProgramArgs
			if gi.giGpa.GpaProg != nil {
				gpa = &gi.giGpa
			}
			glwRendererDraw(&gi.giGr, w.glwRoot, &rc0,
				&glt.gltTexture, nil,
				&gi.giColMul, rgbOff, alphaSelf, blur, gpa)

			if gi.giBitmapFlags&glwImageAdditive != 0 {
				glwBlendmode(w.glwRoot, glwBlendNormal)
			}
		}
		if w.glwClass != &glwFrontdropClass && w.glwChilds.tqhFirst != nil {
			glwZinc(&rc0)
			renderChildAutocentered(gi, &rc0)
		}
	}
}

// C: static void glw_image_layout_tesselated (glw_image.c:388-423)
func glwImageLayoutTesselated(gr *glwRoot, rc *glwRctx,
	gi *GlwImage, glt *GlwLoadableTexture) {
	var tex [4][2]float32
	var vex [4][2]float32

	i := 0

	tex[1][0] = 0.0 + float32(gi.giBorder[0])/float32(glt.gltXs)
	tex[2][0] = glt.gltS - float32(gi.giBorder[2])/float32(glt.gltXs)
	if gi.giBitmapFlags&glwImageBorderLeft != 0 {
		tex[0][0] = 0.0
	} else {
		tex[0][0] = tex[1][0]
	}
	if gi.giBitmapFlags&glwImageBorderRight != 0 {
		tex[3][0] = glt.gltS
	} else {
		tex[3][0] = tex[2][0]
	}

	tex[0][1] = 0.0
	tex[1][1] = 0.0 + float32(gi.giBorder[1])/float32(glt.gltYs)
	tex[2][1] = glt.gltT - float32(gi.giBorder[3])/float32(glt.gltYs)
	tex[3][1] = glt.gltT

	vex[0][0] = -1.0
	vex[1][0] = glwMin(-1.0+2.0*float32(gi.giBorder[0])/float32(rc.rcWidth), 0.0)
	vex[2][0] = glwMax(1.0-2.0*float32(gi.giBorder[2])/float32(rc.rcWidth), 0.0)
	vex[3][0] = 1.0

	vex[0][1] = -1.0
	vex[1][1] = glwMax(1.0-2.0*float32(gi.giBorder[1])/float32(rc.rcHeight), 0.0)
	vex[2][1] = glwMin(-1.0+2.0*float32(gi.giBorder[3])/float32(rc.rcHeight), 0.0)
	vex[3][1] = 1.0

	for y := range 4 {
		for x := range 4 {
			glwRendererVtxPos(&gi.giGr, i, vex[x][0], vex[y][1], 0.0)
			glwRendererVtxSt(&gi.giGr, i, tex[x][0], tex[y][1])
			i++
		}
	}
}

// C: static void settexcoord (glw_image.c:437-453)
func settexcoord(gr *glwRenderer, c int, s0 float32, t0 float32,
	root *glwRoot, glt *GlwLoadableTexture) {
	var m *[4]int8
	if glt.gltOrientation < 9 {
		m = &texTransform[glt.gltOrientation]
	} else {
		m = &texTransform[0]
	}

	s0 = s0*2 - 1
	t0 = t0*2 - 1

	s := s0*float32(m[0]) + t0*float32(m[1])
	t := s0*float32(m[2]) + t0*float32(m[3])

	s = (s + 1.0) * 0.5
	t = (t + 1.0) * 0.5

	glwRendererVtxSt(gr, c, s, t)
}

// C: static void glw_image_layout_alpha_edges (glw_image.c:458-498)
func glwImageLayoutAlphaEdges(gr *glwRoot, rc *glwRctx,
	gi *GlwImage, glt *GlwLoadableTexture) {
	var tex [4][2]float32
	var vex [4][2]float32

	i := 0

	tex[0][0] = 0
	tex[1][0] = 0.0 + float32(gi.giAlphaEdge)/float32(glt.gltXs)
	tex[2][0] = glt.gltS - float32(gi.giAlphaEdge)/float32(glt.gltXs)
	tex[3][0] = glt.gltS

	tex[0][1] = 0
	tex[1][1] = 0.0 + float32(gi.giAlphaEdge)/float32(glt.gltYs)
	tex[2][1] = glt.gltT - float32(gi.giAlphaEdge)/float32(glt.gltYs)
	tex[3][1] = glt.gltT

	vex[0][0] = -1.0
	vex[1][0] = glwMin(-1.0+2.0*float32(gi.giAlphaEdge)/float32(rc.rcWidth), 0.0)
	vex[2][0] = glwMax(1.0-2.0*float32(gi.giAlphaEdge)/float32(rc.rcWidth), 0.0)
	vex[3][0] = 1.0

	vex[0][1] = 1.0
	vex[1][1] = glwMax(1.0-2.0*float32(gi.giAlphaEdge)/float32(rc.rcHeight), 0.0)
	vex[2][1] = glwMin(-1.0+2.0*float32(gi.giAlphaEdge)/float32(rc.rcHeight), 0.0)
	vex[3][1] = -1.0

	for y := range 4 {
		for x := range 4 {
			glwRendererVtxPos(&gi.giGr, i, vex[x][0], vex[y][1], 0.0)
			settexcoord(&gi.giGr, i, tex[x][0], tex[y][1], gr, glt)
			glwRendererVtxCol(&gi.giGr, i, 1, 1, 1, alphaborder[x][y])
			i++
		}
	}
}

// C: static void glw_image_layout_normal (glw_image.c:501-527)
func glwImageLayoutNormal(gr *glwRoot, gi *GlwImage,
	glt *GlwLoadableTexture) {
	m := float32(glt.gltMargin)

	x1 := -1.0 + 2.0*-m/float32(glt.gltXs)
	y1 := -1.0 + 2.0*-m/float32(glt.gltYs)
	x2 := 1.0 + 2.0*m/float32(glt.gltXs)
	y2 := 1.0 + 2.0*m/float32(glt.gltYs)

	glwRendererVtxPos(&gi.giGr, 0, x1, y1, 0.0)
	settexcoord(&gi.giGr, 0, 0, 1, gr, glt)

	glwRendererVtxPos(&gi.giGr, 1, x2, y1, 0.0)
	settexcoord(&gi.giGr, 1, 1, 1, gr, glt)

	glwRendererVtxPos(&gi.giGr, 2, x2, y2, 0.0)
	settexcoord(&gi.giGr, 2, 1, 0, gr, glt)

	glwRendererVtxPos(&gi.giGr, 3, x1, y2, 0.0)
	settexcoord(&gi.giGr, 3, 0, 0, gr, glt)
}

// C: static void glw_image_layout_repeated (glw_image.c:529-550)
func glwImageLayoutRepeated(gr *glwRoot, rc *glwRctx,
	gi *GlwImage, glt *GlwLoadableTexture) {
	xs := glt.gltS
	ys := glt.gltT

	glwRendererVtxPos(&gi.giGr, 0, -1.0, -1.0, 0.0)
	glwRendererVtxSt(&gi.giGr, 0, 0, ys)

	glwRendererVtxPos(&gi.giGr, 1, 1.0, -1.0, 0.0)
	glwRendererVtxSt(&gi.giGr, 1, xs, ys)

	glwRendererVtxPos(&gi.giGr, 2, 1.0, 1.0, 0.0)
	glwRendererVtxSt(&gi.giGr, 2, xs, 0)

	glwRendererVtxPos(&gi.giGr, 3, -1.0, 1.0, 0.0)
	glwRendererVtxSt(&gi.giGr, 3, 0, 0)
}

// C: static void glw_image_update_constraints (glw_image.c:553-622)
func glwImageUpdateConstraints(gi *GlwImage) {
	glt := gi.giCurrent

	if gi.giBitmapFlags&glwImageFixedSize != 0 {

		glwSetConstraints(&gi.w,
			int(glt.gltXs),
			int(glt.gltYs),
			0,
			glwConstraintX|glwConstraintY)

	} else if gi.w.glwClass == &glwBackdropClass ||
		gi.w.glwClass == &glwFrontdropClass {

		c := gi.w.glwChilds.tqhFirst

		if c != nil {
			glwSetConstraints(&gi.w,
				glwReqWidth(c)+
					int(gi.giBoxLeft)+int(gi.giBoxRight),
				glwReqHeight(c)+
					int(gi.giBoxTop)+int(gi.giBoxBottom),
				c.glwReqWeight,
				c.glwFlags&glwConstraintFlags)

		} else if glt != nil {
			glwSetConstraints(&gi.w,
				int(glt.gltXs)-int(glt.gltMargin)*2,
				int(glt.gltYs)-int(glt.gltMargin)*2,
				0, 0)
		}

	} else if gi.w.glwClass == &glwImageClass && glt != nil {

		if glt.gltState == gltStateError {
			glwClearConstraints(&gi.w)
		} else if gi.giBitmapFlags&glwImageSetAspect != 0 {
			glwSetConstraints(&gi.w, 0, 0, -glt.gltAspect, glwConstraintW)
		} else if gi.w.glwFlags&glwConstraintConfX != 0 {

			ys := int(float32(glwReqWidth(&gi.w)) / glt.gltAspect)
			glwSetConstraints(&gi.w, 0, ys, 0, glwConstraintY)
		} else if gi.w.glwFlags&glwConstraintConfY != 0 {

			xs := int(float32(glwReqHeight(&gi.w)) * glt.gltAspect)
			glwSetConstraints(&gi.w, xs, 0, 0, glwConstraintX)
		}
	}
}

// C: static glw_loadable_texture_t *glw_image_tex_load
// (glw_image.c:672-693)
func glwImageTexLoad(gi *GlwImage, url *miscpkg.Rstr, width int,
	height int) *GlwLoadableTexture {
	flags := gi.giBitmapFlags & glwImageTexOverlap

	if gi.w.glwClass == &glwRepeatedimageClass {
		flags |= glwTexRepeat
	}

	if gi.giMaxIntensity < 1.0 {
		flags |= glwTexIntensityAnalysis
	}

	if gi.giWantPrimaryColor {
		flags |= glwTexPrimaryColorAnalysis
	}

	return glwTexCreate(gi.w.glwRoot, url, flags, width, height,
		int(gi.giRadius), int(gi.giShadow), gi.giAspect,
		gi.giPendingUrlFlags,
		gi.w.glwScope.gsBackend)
}

// C: static void glw_image_layout (glw_image.c:696-953)
func glwImageLayout(w *Glw, rc *glwRctx) {
	gi := (*GlwImage)(unsafe.Pointer(w))
	gr := w.glwRoot
	var glt *GlwLoadableTexture
	var c *Glw

	hq := gi.giMode == giModeNormal || gi.giMode == giModeAlphaEdges
	gi.giSwitchTgt = 180

	if gi.giSources != nil && gi.giSwitchTgt != 0 {
		gi.giSwitchCnt++
		if gi.giSwitchCnt == gi.giSwitchTgt {
			pickSource(gi, 1)
			gi.giSwitchCnt = 0
		}
	}

	if gr.grCanExternalize != 0 &&
		int(rc.rcWidth) == gr.grWidth &&
		int(rc.rcHeight) == gr.grHeight &&
		w.glwClass == &glwBackdropClass {

		if gr.grExternalizeCnt < glwMaxExternalized {
			gr.grExternalized[gr.grExternalizeCnt] = w
			gr.grExternalizeCnt++
			gi.giExternalized = true
			return
		}
	}

	gi.giExternalized = false

	if gi.giPendingUrl != nil {
		// Request to load
		xs, ys := -1, -1
		gi.giLoadingNewUrl = true

		if gi.giPending != nil {
			glwTexDeref(w.glwRoot, gi.giPending)
			gi.giPending = nil
		}

		if miscpkg.RstrGet(gi.giPendingUrl) == "" {
			// Empty string, unload all

			if gi.giCurrent != nil {
				glwTexDeref(w.glwRoot, gi.giCurrent)
				gi.giCurrent = nil
			}

			gi.giUpdate = true

		} else {

			if hq {

				if w.glwClass == &glwIconClass {

					ys = int(gi.giFixedSize)
					if ys == 0 {
						ys = int(gi.giSizeScale * float32(w.glwRoot.grCurrentSize))
					}

					if ys > int(rc.rcHeight) {
						ys = int(rc.rcHeight)
					}

				} else if w.glwClass == &glwImageClass {
					if rc.rcWidth < rc.rcHeight {
						xs = int(rc.rcWidth)
					} else {
						ys = int(rc.rcHeight)
					}
				} else {
					xs = int(rc.rcWidth)
					ys = int(rc.rcHeight)
				}
			}

			if xs != 0 && ys != 0 {

				gi.giPending = glwImageTexLoad(gi, gi.giPendingUrl, xs, ys)

				miscpkg.RstrRelease(gi.giPendingUrl)
				gi.giPendingUrl = nil
			}
		}
	}

	if glt = gi.giPending; glt != nil {
		glwTexLayout(gr, glt)

		if gi.giCurrent == nil {
			giSetLoadStatus(gi, glwStatusLoading)
		}

		if glwIsTexStarted(&glt.gltTexture) ||
			glt.gltState == gltStateError {
			// Pending texture completed, ok or error: transfer to current

			if gi.giCurrent != nil {
				glwTexDeref(w.glwRoot, gi.giCurrent)
			}

			if glt.gltState == gltStateError {
				giSetLoadStatus(gi, glwStatusError)
			}

			gi.giCurrent = gi.giPending
			gi.giPending = nil
			gi.giUpdate = true
			gi.giLoadingNewUrl = false
			computeColors(gi)
		}
	}

	if glt = gi.giCurrent; glt == nil {
		return
	}

	target := float32(0)
	if !gi.giLoadingNewUrl {
		target = 1
	}
	glwLp(&gi.giAutofade, w.glwRoot, target, 0.25)

	glwTexLayout(gr, glt)

	if glt.gltState == gltStateError {
		giSetLoadStatus(gi, glwStatusError)
	} else if glwIsTexStarted(&glt.gltTexture) {

		gr.grCanExternalize = 0

		giSetLoadStatus(gi, glwStatusLoaded)

		if gi.giUpdate {
			gi.giUpdate = false

			glwRendererFree(&gi.giGr)

			switch gi.giMode {

			case giModeNormal:
				glwRendererSetupQuad(&gi.giGr)
				glwImageLayoutNormal(gr, gi, glt)
			case giModeBorderScaling:
				glwRendererSetup(&gi.giGr, 16, 18, borderobject)
				glwImageLayoutTesselated(gr, rc, gi, glt)
			case giModeRepeatedTexture:
				glwRendererSetupQuad(&gi.giGr)
				glwImageLayoutRepeated(gr, rc, gi, glt)
			case giModeAlphaEdges:
				glwRendererSetup(&gi.giGr, 16, 18, borderobject)
				glwImageLayoutAlphaEdges(gr, rc, gi, glt)
			case giModeBorderOnlyScaling:
				glwRendererSetup(&gi.giGr, 16, 16, borderonlyobject)
				glwImageLayoutTesselated(gr, rc, gi, glt)
			default:
				panic("glw_image_layout: bad gi_mode")
			}

		} else if gi.giLastWidth != rc.rcWidth ||
			gi.giLastHeight != rc.rcHeight {

			gi.giLastWidth = rc.rcWidth
			gi.giLastHeight = rc.rcHeight

			switch gi.giMode {

			case giModeNormal:
			case giModeBorderScaling,
				giModeBorderOnlyScaling:
				glwImageLayoutTesselated(gr, rc, gi, glt)
			case giModeRepeatedTexture:
				glwImageLayoutRepeated(gr, rc, gi, glt)
			case giModeAlphaEdges:
				glwImageLayoutAlphaEdges(gr, rc, gi, glt)
			}
			gi.giNeedReload = hq
		}

		if gi.giNeedReload && gi.giPending == nil &&
			gi.giPendingUrl == nil && rc.rcWidth > 0 && rc.rcHeight > 0 {

			xs, ys := -1, -1
			var rescale int

			if w.glwClass == &glwImageClass || w.glwClass == &glwIconClass {

				if rc.rcWidth < rc.rcHeight {
					rescale = iabs(int(rc.rcWidth) - int(glt.gltXs) - int(glt.gltMargin)*2)
					xs = int(rc.rcWidth)
				} else {
					rescale = iabs(int(rc.rcHeight) - int(glt.gltYs) - int(glt.gltMargin)*2)
					ys = int(rc.rcHeight)
				}
			} else {
				if int(rc.rcWidth)-int(glt.gltXs) != 0 ||
					int(rc.rcHeight)-int(glt.gltYs) != 0 {
					rescale = 1
				}
				xs = int(rc.rcWidth)
				ys = int(rc.rcHeight)
			}

			// Requesting aspect cause a lot of rounding errors
			// so to avoid ending up in infinite reload loops,
			// consider 1px off as nothing
			if gi.giBitmapFlags&glwImageSetAspect != 0 && rescale == 1 {
				rescale = 0
			}

			if rescale != 0 {
				if gi.giRescaleHold < 5 {
					gi.giRescaleHold++
				} else {
					gi.giRescaleHold = 0
					gi.giPending = glwImageTexLoad(gi, glt.gltURL, xs, ys)
					gi.giNeedReload = false
				}
			} else {
				gi.giNeedReload = false
			}
		}
	} else {
		giSetLoadStatus(gi, glwStatusLoading)
	}

	glwImageUpdateConstraints(gi)

	if c = gi.w.glwChilds.tqhFirst; c != nil {
		rc0 := *rc

		rc0.rcWidth -= gi.giBoxLeft + gi.giBoxRight
		rc0.rcHeight -= gi.giBoxTop + gi.giBoxBottom

		if rc0.rcHeight >= 0 && rc0.rcWidth >= 0 {
			glwLayout0(c, &rc0)
		}
	}
}

// C: static int glw_image_callback (glw_image.c:957-975)
func glwImageCallback(w *Glw, opaque any, signal glwSignal,
	extra any) int {
	switch signal {

	case glwSignalChildConstraintsChanged,
		glwSignalChildCreated:
		glwImageUpdateConstraints((*GlwImage)(unsafe.Pointer(w)))
		return 1
	case glwSignalChildDestroyed:
		glwSetConstraints(w, 0, 0, 0, 0)
		return 1
	}
	return 0
}

// C: static void compute_colors (glw_image.c:980-996)
func computeColors(gi *GlwImage) {
	scale := float32(1) - gi.giSaturation

	if gi.giMaxIntensity < 1.0 && gi.giCurrent != nil &&
		gi.giCurrent.gltIntensity > gi.giMaxIntensity {
		scale *= gi.giMaxIntensity / gi.giCurrent.gltIntensity
	}

	gi.giColMul.r = gi.giColor.r * scale
	gi.giColMul.g = gi.giColor.g * scale
	gi.giColMul.b = gi.giColor.b * scale

	gi.giColOff.r = gi.giSaturation
	gi.giColOff.g = gi.giSaturation
	gi.giColOff.b = gi.giSaturation

	gi.giSaturated = gi.giSaturation > 0
}

// C: static void glw_image_ctor (glw_image.c:1001-1032)
func glwImageCtor(w *Glw) {
	gi := (*GlwImage)(unsafe.Pointer(w))

	gi.giBitmapFlags =
		glwTexCornerTopleft |
			glwTexCornerTopright |
			glwTexCornerBottomleft |
			glwTexCornerBottomright |
			glwImageBorderLeft |
			glwImageBorderRight

	gi.giAutofade = 1
	gi.giAlphaSelf = 1
	gi.giColor.r = 1.0
	gi.giColor.g = 1.0
	gi.giColor.b = 1.0
	gi.giSizeScale = 1.0
	gi.giMaxIntensity = 1.0

	computeColors(gi)

	if w.glwClass == &glwRepeatedimageClass {
		gi.giMode = giModeRepeatedTexture
	}
}

// C: static void glw_icon_ctor (glw_image.c:1035-1047)
func glwIconCtor(w *Glw) {
	glwImageCtor(w)
	gi := (*GlwImage)(unsafe.Pointer(w))
	gr := w.glwRoot
	siz := float32(w.glwRoot.grCurrentSize)
	glwSetConstraints(w, int(siz), int(siz), 0, glwConstraintX|glwConstraintY)

	giListInsertHead(&gr.grIcons, gi)
}

// C: static int glw_image_set_float3 (glw_image.c:1050-1069)
func glwImageSetFloat3(w *Glw, attrib glwAttribute, rgb []float32,
	gs *GlwStyle) int {
	gi := (*GlwImage)(unsafe.Pointer(w))

	switch attrib {
	case glwAttribRGB:
		if glwAttribSetRgb(&gi.giColor, (*[3]float32)(rgb)) == 0 {
			return 0
		}
		computeColors(gi)
		return 1

	default:
		return -1
	}
}

// C: static void update_box (glw_image.c:1072-1090)
func updateBox(gi *GlwImage) {
	gi.giBoxLeft = gi.giBorder[0] + gi.giPadding[0]
	gi.giBoxTop = gi.giBorder[1] + gi.giPadding[1]
	gi.giBoxRight = gi.giBorder[2] + gi.giPadding[2]
	gi.giBoxBottom = gi.giBorder[3] + gi.giPadding[3]
}

// C: static int image_set_int16_4 (glw_image.c:1095-1123)
func imageSetInt16_4(w *Glw, attrib glwAttribute, v []int16,
	gs *GlwStyle) int {
	gi := (*GlwImage)(unsafe.Pointer(w))

	switch attrib {
	case glwAttribBorder:
		if glwAttribSetInt164(&gi.giBorder, (*[4]int16)(v)) == 0 {
			return 0
		}
		if gi.giMode != giModeBorderOnlyScaling {
			gi.giMode = giModeBorderScaling
		}

	case glwAttribPadding:
		if glwAttribSetInt164(&gi.giPadding, (*[4]int16)(v)) == 0 {
			return 0
		}

	default:
		return -1
	}

	updateBox(gi)
	gi.giUpdate = true
	glwImageUpdateConstraints(gi)
	return 1
}

// C: static void mod_image_flags (glw_image.c:1126-1140)
func modImageFlags(w *Glw, set int, clr int, gs *GlwStyle) {
	gi := (*GlwImage)(unsafe.Pointer(w))
	gi.giBitmapFlags = (gi.giBitmapFlags | set) &^ clr
	gi.giUpdate = true

	if set&glwImageBorderOnly != 0 {
		gi.giMode = giModeBorderOnlyScaling
	}
	if clr&glwImageBorderOnly != 0 {
		gi.giMode = giModeBorderScaling
	}
}

// C: static rstr_t *get_curname (glw_image.c:1145-1161)
func getCurname(gi *GlwImage) *miscpkg.Rstr {
	var curname *miscpkg.Rstr

	if gi.giPendingUrl != nil {
		curname = gi.giPendingUrl
	} else if gi.giPending != nil {
		curname = gi.giPending.gltURL
	} else if gi.giCurrent != nil {
		curname = gi.giCurrent.gltURL
	}
	return curname
}

// C: static void set_pending (glw_image.c:1166-1173)
func setPending(gi *GlwImage, filename *miscpkg.Rstr, flags int) {
	if gi.giPendingUrl != nil {
		miscpkg.RstrRelease(gi.giPendingUrl)
	}
	if filename != nil {
		gi.giPendingUrl = miscpkg.RstrDup(filename)
	} else {
		gi.giPendingUrl = miscpkg.RstrAllocStr("")
	}
	gi.giPendingUrlFlags = flags
}

// C: static void mod_flags2 (glw_image.c:1178-1195)
func imageModFlags2(w *Glw, set int, clr int) {
	gi := (*GlwImage)(unsafe.Pointer(w))
	if (set|clr)&glw2Shadow != 0 {
		if set&glw2Shadow != 0 {
			gi.giShadow = 4
		} else {
			gi.giShadow = 0
		}

		if gi.giCurrent != nil || gi.giPending != nil {
			curname := getCurname(gi)
			if curname != nil {
				setPending(gi, curname, gi.giPendingUrlFlags)
			}
		}
	}
}

// C: static void set_path (glw_image.c:1200-1211)
func setPath(gi *GlwImage, filename *miscpkg.Rstr, flags int) {
	curname := getCurname(gi)

	if curname != nil && filename != nil &&
		miscpkg.RstrGet(filename) == miscpkg.RstrGet(curname) {
		return
	}

	setPending(gi, filename, flags)
}

// C: static void set_source (glw_image.c:1216-1222)
func giSetSource(w *Glw, filename *miscpkg.Rstr, flags int, gs *GlwStyle) {
	gi := (*GlwImage)(unsafe.Pointer(w))
	setPath(gi, filename, flags)
}

// C: static void pick_source (glw_image.c:1227-1252)
func pickSource(gi *GlwImage, next int) {
	curname := getCurname(gi)
	if curname == nil {
		setPending(gi, gi.giSources[0], 0)
	} else {

		found := -1
		for i := 0; i < len(gi.giSources) && found == -1; i++ {
			if miscpkg.RstrGet(gi.giSources[i]) == miscpkg.RstrGet(curname) {
				found = i
			}
		}
		if found == -1 {
			setPending(gi, gi.giSources[0], 0)
		} else if next != 0 {
			if found+1 >= len(gi.giSources) {
				setPending(gi, gi.giSources[0], 0)
			} else {
				setPending(gi, gi.giSources[found+1], 0)
			}
		}
	}
}

// C: static void set_sources (glw_image.c:1257-1266)
func setSources(w *Glw, filenames []*miscpkg.Rstr) {
	gi := (*GlwImage)(unsafe.Pointer(w))
	giSourcesFree(gi.giSources)
	gi.giSources = filenames

	pickSource(gi, 0)
}

// C: static int glw_image_set_em (glw_image.c:1269-1296)
func glwImageSetEm(w *Glw, attrib glwAttribute, value float32) int {
	gi := (*GlwImage)(unsafe.Pointer(w))

	switch attrib {
	case glwAttribSize:
		if w.glwClass != &glwIconClass {
			return -1
		}

		if gi.giSizeScale == value {
			return 0
		}

		gi.giSizeScale = value

		siz := gi.giSizeScale * float32(w.glwRoot.grCurrentSize)
		glwSetConstraints(w, int(siz), int(siz), 0,
			glwConstraintX|glwConstraintY)

	default:
		return -1
	}
	return 1
}

// C: static int glw_image_set_float (glw_image.c:1299-1345)
func glwImageSetFloat(w *Glw, attrib glwAttribute, value float32,
	gs *GlwStyle) int {
	gi := (*GlwImage)(unsafe.Pointer(w))

	switch attrib {
	case glwAttribAngle:
		if gi.giAngle == value {
			return 0
		}
		gi.giAngle = value

	case glwAttribSaturation:
		if gi.giSaturation == value {
			return 0
		}
		gi.giSaturation = value
		computeColors(gi)

	case glwAttribAspect:
		if gi.giAspect == value {
			return 0
		}
		gi.giAspect = value
		gi.giUpdate = true

	case glwAttribChildAspect:
		if gi.giChildAspect == value {
			return 0
		}
		gi.giChildAspect = value

	case glwAttribAlphaSelf:
		if gi.giAlphaSelf == value {
			return 0
		}
		gi.giAlphaSelf = value

	case glwAttribSizeScale:
		return glwImageSetEm(w, glwAttribSize, value)

	default:
		return -1
	}
	return 1
}

// C: static int glw_image_set_int (glw_image.c:1350-1383)
func glwImageSetInt(w *Glw, attrib glwAttribute, value int,
	gs *GlwStyle) int {
	gi := (*GlwImage)(unsafe.Pointer(w))

	switch attrib {

	case glwAttribAlphaEdges:
		if gi.giAlphaEdge == uint8(value) {
			return 0
		}

		gi.giAlphaEdge = uint8(value)
		gi.giMode = giModeAlphaEdges

	case glwAttribRadius:
		if gi.giRadius == int16(value) {
			return 0
		}

		gi.giRadius = int16(value)
		gi.giUpdate = true

	case glwAttribSize:
		if w.glwClass != &glwIconClass {
			return -1
		}

		if gi.giFixedSize == int16(value) {
			return 0
		}

		gi.giFixedSize = int16(value)
		glwSetConstraints(w, value, value, 0,
			glwConstraintX|glwConstraintY)

	default:
		return -1
	}
	return 1
}

// C: static glw_widget_status_t glw_image_status (glw_image.c:1388-1393)
func glwImageStatus(w *Glw) glwWidgetStatus {
	gi := (*GlwImage)(unsafe.Pointer(w))
	return gi.giWidgetStatus
}

// C: static const char *get_identity (glw_image.c:1398-1412)
func getIdentity(w *Glw, tmp []byte) string {
	gi := (*GlwImage)(unsafe.Pointer(w))
	glt := gi.giCurrent
	if glt != nil {
		return miscpkg.RstrGet(glt.gltURL)
	}

	glt = gi.giPending
	if glt != nil {
		return miscpkg.RstrGet(glt.gltURL)
	}
	return miscpkg.RstrGet(gi.giPendingUrl)
}

// C: void glw_icon_flush (glw_image.c:1415-1428)
func glwIconFlush(gr *glwRoot) {
	for gi := gr.grIcons.lhFirst; gi != nil; gi = gi.giLinkNext {
		if gi.giFixedSize != 0 {
			continue
		}
		siz := gi.giSizeScale * float32(gr.grCurrentSize)

		glwSetConstraints(&gi.w, int(siz), int(siz), 0,
			glwConstraintX|glwConstraintY)
	}
}

// C: static int glw_image_set_float_unresolved (glw_image.c:1433-1448)
func glwImageSetFloatUnresolved(w *Glw, a string, value float32,
	gs *GlwStyle) int {
	gi := (*GlwImage)(unsafe.Pointer(w))

	if a == "maxIntensity" {
		if gi.giMaxIntensity == value {
			return 0
		}
		gi.giMaxIntensity = value
		computeColors(gi)
		return 1
	}

	return glwSetNotResponding
}

// C: int glw_image_get_details (glw_image.c:1453-1466)
func glwImageGetDetails(w *Glw, path []byte, alpha *float32) int {
	if w.glwClass != &glwBackdropClass {
		return -1
	}

	gi := (*GlwImage)(unsafe.Pointer(w))
	p := getIdentity(w, nil)
	if p == "" && gi.giPendingUrl == nil {
		return -1
	}
	copy(path, p)
	*alpha = w.glwAlpha * gi.giColMul.g
	return 0
}

// C: static int glw_image_primary_color (glw_image.c:1471-1482)
func glwImagePrimaryColor(w *Glw, rgb *float32) int {
	gi := (*GlwImage)(unsafe.Pointer(w))
	gi.giWantPrimaryColor = true
	if gi.giCurrent != nil {
		out := unsafe.Slice(rgb, 3)
		out[0] = gi.giCurrent.gltPrimaryColor[0]
		out[1] = gi.giCurrent.gltPrimaryColor[1]
		out[2] = gi.giCurrent.gltPrimaryColor[2]
		return 1
	}
	return 0
}

func registerImage() {
	// C: static glw_class_t glw_image (glw_image.c:1488-1511)
	glwImageClass = glwClass{
		gcName:               "image",
		gcInstanceSize:       int(unsafe.Sizeof(GlwImage{})),
		gcNew:                func(parent *Glw) *Glw { gi := &GlwImage{}; return &gi.w },
		gcLayout:             glwImageLayout,
		gcRender:             glwImageRender,
		gcDtor:               glwImageDtor,
		gcCtor:               glwImageCtor,
		gcSetFloat:           glwImageSetFloat,
		gcSetInt:             glwImageSetInt,
		gcSignalHandler:      glwImageCallback,
		gcDefaultAlignment:   layoutAlignCenter,
		gcStatus:             glwImageStatus,
		gcPrimaryColor:       glwImagePrimaryColor,
		gcSetFloat3:          glwImageSetFloat3,
		gcModImageFlags:      modImageFlags,
		gcSetSource:          giSetSource,
		gcSetSources:         setSources,
		gcGetIdentity:        getIdentity,
		gcSetFs:              glwImageSetFs,
		gcModFlags2:          imageModFlags2,
		gcSetInt16_4:         imageSetInt16_4,
		gcSetFloatUnresolved: glwImageSetFloatUnresolved,
	}

	// C: static glw_class_t glw_icon (glw_image.c:1517-1542)
	glwIconClass = glwClass{
		gcName:               "icon",
		gcInstanceSize:       int(unsafe.Sizeof(GlwImage{})),
		gcNew:                func(parent *Glw) *Glw { gi := &GlwImage{}; return &gi.w },
		gcLayout:             glwImageLayout,
		gcRender:             glwImageRender,
		gcCtor:               glwIconCtor,
		gcDtor:               glwIconDtor,
		gcSetFloat:           glwImageSetFloat,
		gcSetEm:              glwImageSetEm,
		gcSetInt:             glwImageSetInt,
		gcSignalHandler:      glwImageCallback,
		gcDefaultAlignment:   layoutAlignCenter,
		gcStatus:             glwImageStatus,
		gcPrimaryColor:       glwImagePrimaryColor,
		gcSetFloat3:          glwImageSetFloat3,
		gcModImageFlags:      modImageFlags,
		gcSetSource:          giSetSource,
		gcSetSources:         setSources,
		gcGetIdentity:        getIdentity,
		gcSetFs:              glwImageSetFs,
		gcModFlags2:          imageModFlags2,
		gcSetInt16_4:         imageSetInt16_4,
		gcSetFloatUnresolved: glwImageSetFloatUnresolved,
	}

	// C: static glw_class_t glw_backdrop (glw_image.c:1548-1572)
	glwBackdropClass = glwClass{
		gcName:               "backdrop",
		gcInstanceSize:       int(unsafe.Sizeof(GlwImage{})),
		gcNew:                func(parent *Glw) *Glw { gi := &GlwImage{}; return &gi.w },
		gcLayout:             glwImageLayout,
		gcRender:             glwImageRender,
		gcCtor:               glwImageCtor,
		gcDtor:               glwImageDtor,
		gcSetFloat:           glwImageSetFloat,
		gcSetInt:             glwImageSetInt,
		gcSignalHandler:      glwImageCallback,
		gcDefaultAlignment:   layoutAlignCenter,
		gcStatus:             glwImageStatus,
		gcPrimaryColor:       glwImagePrimaryColor,
		gcSetFloat3:          glwImageSetFloat3,
		gcSetInt16_4:         imageSetInt16_4,
		gcModImageFlags:      modImageFlags,
		gcSetSource:          giSetSource,
		gcSetSources:         setSources,
		gcGetIdentity:        getIdentity,
		gcSetFs:              glwImageSetFs,
		gcModFlags2:          imageModFlags2,
		gcSetFloatUnresolved: glwImageSetFloatUnresolved,
	}

	// C: static glw_class_t glw_frontdrop (glw_image.c:1578-1601)
	glwFrontdropClass = glwClass{
		gcName:               "frontdrop",
		gcInstanceSize:       int(unsafe.Sizeof(GlwImage{})),
		gcNew:                func(parent *Glw) *Glw { gi := &GlwImage{}; return &gi.w },
		gcLayout:             glwImageLayout,
		gcRender:             glwImageRender,
		gcCtor:               glwImageCtor,
		gcDtor:               glwImageDtor,
		gcSetFloat:           glwImageSetFloat,
		gcSetInt:             glwImageSetInt,
		gcSignalHandler:      glwImageCallback,
		gcStatus:             glwImageStatus,
		gcPrimaryColor:       glwImagePrimaryColor,
		gcDefaultAlignment:   layoutAlignCenter,
		gcSetFloat3:          glwImageSetFloat3,
		gcSetInt16_4:         imageSetInt16_4,
		gcModImageFlags:      modImageFlags,
		gcSetSource:          giSetSource,
		gcSetSources:         setSources,
		gcGetIdentity:        getIdentity,
		gcSetFs:              glwImageSetFs,
		gcModFlags2:          imageModFlags2,
		gcSetFloatUnresolved: glwImageSetFloatUnresolved,
	}

	// C: static glw_class_t glw_repeatedimage (glw_image.c:1607-1630)
	glwRepeatedimageClass = glwClass{
		gcName:               "repeatedimage",
		gcInstanceSize:       int(unsafe.Sizeof(GlwImage{})),
		gcNew:                func(parent *Glw) *Glw { gi := &GlwImage{}; return &gi.w },
		gcLayout:             glwImageLayout,
		gcRender:             glwImageRender,
		gcCtor:               glwImageCtor,
		gcDtor:               glwImageDtor,
		gcSetFloat:           glwImageSetFloat,
		gcSetInt:             glwImageSetInt,
		gcSignalHandler:      glwImageCallback,
		gcStatus:             glwImageStatus,
		gcPrimaryColor:       glwImagePrimaryColor,
		gcDefaultAlignment:   layoutAlignCenter,
		gcSetFloat3:          glwImageSetFloat3,
		gcModImageFlags:      modImageFlags,
		gcSetSource:          giSetSource,
		gcGetIdentity:        getIdentity,
		gcSetFs:              glwImageSetFs,
		gcModFlags2:          imageModFlags2,
		gcSetInt16_4:         imageSetInt16_4,
		gcSetFloatUnresolved: glwImageSetFloatUnresolved,
	}

	// C: GLW_REGISTER_CLASS × 5
	glwRegisterClass(&glwImageClass)
	glwRegisterClass(&glwIconClass)
	glwRegisterClass(&glwBackdropClass)
	glwRegisterClass(&glwFrontdropClass)
	glwRegisterClass(&glwRepeatedimageClass)
}
