package glw

// glw_text_bitmap.c — text widget (label + text classes): layout, render,
// font render thread, editing, prop binding. 1:1 port of
// src/ui/glw/glw_text_bitmap.c.

import (
	"math"
	"unsafe"

	archpkg "github.com/czz/movian-go/internal/arch"
	eventpkg "github.com/czz/movian-go/internal/event"
	facore "github.com/czz/movian-go/internal/fileaccess"
	imagepkg "github.com/czz/movian-go/internal/image"
	miscpkg "github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
	textpkg "github.com/czz/movian-go/internal/text"
	tracepkg "github.com/czz/movian-go/internal/trace"
)

// C: GTB_* constants are declared in glw_h.go (glw.h:208-216).

// C: gtb_state enum (glw_text_bitmap.c:83-91)
const (
	gtbIdle = iota
	gtbQueuedForDimensioning
	gtbDimensioning
	gtbNeedRender
	gtbQueuedForRendering
	gtbRendering
	gtbValid
)

const gtbUpdateRealize = 2 // C: GTB_UPDATE_REALIZE

// glwTexWidth / glwTexHeight / glwIsTexStarted — C: glw_tex_width /
// glw_tex_height / glw_is_tex_inited (glw_opengl.h:165-168)
func glwTexWidth(tex *GlwBackendTexture) int  { return int(tex.Width) }
func glwTexHeight(tex *GlwBackendTexture) int { return int(tex.Height) }
func glwIsTexStarted(tex *GlwBackendTexture) bool {
	return tex.Textures[0] != 0
}

// C: LIST_INSERT_HEAD(&gr->gr_gtbs, gtb, gtb_global_link)
func gtbListInsertHead(l *glwTextBitmapList, e *GlwTextBitmap) {
	e.gtbGlobalLinkNext = l.lhFirst
	if e.gtbGlobalLinkNext != nil {
		e.gtbGlobalLinkNext.gtbGlobalLinkPrev = &e.gtbGlobalLinkNext
	}
	l.lhFirst = e
	e.gtbGlobalLinkPrev = &l.lhFirst
}

// C: LIST_REMOVE(gtb, gtb_global_link)
func gtbListRemove(e *GlwTextBitmap) {
	if e.gtbGlobalLinkNext != nil {
		e.gtbGlobalLinkNext.gtbGlobalLinkPrev = e.gtbGlobalLinkPrev
	}
	*e.gtbGlobalLinkPrev = e.gtbGlobalLinkNext
}

// C: LIST_FOREACH over &gr->gr_gtbs
func gtbListEach(l *glwTextBitmapList) *GlwTextBitmap { return l.lhFirst }

// C: TAILQ_INSERT_TAIL(&gr->gr_gtb_*_queue, gtb, gtb_workq_link)
func gtbTailqInsertTail(q *glwTextBitmapQueue, e *GlwTextBitmap) {
	e.gtbWorkqLinkNext = nil
	e.gtbWorkqLinkPrev = q.tqhLast
	*q.tqhLast = e
	q.tqhLast = &e.gtbWorkqLinkNext
}

// C: TAILQ_REMOVE(&gr->gr_gtb_*_queue, gtb, gtb_workq_link)
func gtbTailqRemove(q *glwTextBitmapQueue, e *GlwTextBitmap) {
	if e.gtbWorkqLinkNext != nil {
		e.gtbWorkqLinkNext.gtbWorkqLinkPrev = e.gtbWorkqLinkPrev
	} else {
		q.tqhLast = e.gtbWorkqLinkPrev
	}
	*e.gtbWorkqLinkPrev = e.gtbWorkqLinkNext
}

// C: TAILQ_INIT
func gtbTailqSetup(q *glwTextBitmapQueue) {
	q.tqhFirst = nil
	q.tqhLast = &q.tqhFirst
}

// C: static void glw_text_bitmap_layout (glw_text_bitmap.c:132-353)
func glwTextBitmapLayout(w *Glw, rc *glwRctx) {
	gtb := (*GlwTextBitmap)(unsafe.Pointer(w))
	gr := w.glwRoot

	gr.grCanExternalize = 0

	// Initialize renderers

	if !glwRendererStarted(&gtb.gtbTextRenderer) {
		glwRendererSetupQuad(&gtb.gtbTextRenderer)
	}

	if w.glwClass == &glwTextClass &&
		!glwRendererStarted(&gtb.gtbCursorRenderer) {
		glwRendererSetupQuad(&gtb.gtbCursorRenderer)
	}

	if gtb.gtbBackgroundAlpha > glwAlphaEpsilon &&
		!glwRendererStarted(&gtb.gtbBackgroundRenderer) {
		glwRendererSetupQuad(&gtb.gtbBackgroundRenderer)
		glwRendererVtxPos(&gtb.gtbBackgroundRenderer, 0, -1, -1, 0)
		glwRendererVtxPos(&gtb.gtbBackgroundRenderer, 1, 1, -1, 0)
		glwRendererVtxPos(&gtb.gtbBackgroundRenderer, 2, 1, 1, 0)
		glwRendererVtxPos(&gtb.gtbBackgroundRenderer, 3, -1, 1, 0)
	}

	// Upload texture

	ic := gtb.gtbImage.FindComponent(imagepkg.ComponentPixmap)
	if ic != nil {
		glwTexUpload(gr, &gtb.gtbTexture, ic.Pixmap, 0)
		gtb.gtbMargin = int16(ic.Pixmap.Margin)
		imagepkg.ClearComponent(ic)
		gtb.gtbNeedLayout = true
	}

	texWidth := glwTexWidth(&gtb.gtbTexture)
	texHeight := glwTexHeight(&gtb.gtbTexture)

	ic = gtb.gtbImage.FindComponent(imagepkg.ComponentTextInfo)
	var ti *imagepkg.TextInfoComponent
	if ic != nil {
		ti = &ic.TextInfo
	}

	// Check if we need to repaint

	if gtb.gtbSavedWidth != int16(rc.rcWidth) ||
		gtb.gtbSavedHeight != int16(rc.rcHeight) {

		if ti != nil && gtb.gtbState == gtbValid {

			if ti.Flags&imagepkg.TextWrapped != 0 {
				gtb.gtbState = gtbNeedRender
			}

			if int(rc.rcWidth) > int(gtb.gtbSavedWidth) &&
				ti.Flags&imagepkg.TextTruncated != 0 {
				gtb.gtbState = gtbNeedRender
			}

			if gtb.gtbFlags&gtbEllipsize != 0 {

				if ti.Flags&imagepkg.TextTruncated != 0 {
					gtb.gtbState = gtbNeedRender
				} else {

					if int(rc.rcWidth)-int(gtb.gtbPadding[2])-int(gtb.gtbPadding[0]) <
						texWidth-int(gtb.gtbMargin)*2 {
						gtb.gtbState = gtbNeedRender
					}

					if int(rc.rcHeight)-int(gtb.gtbPadding[1])-int(gtb.gtbPadding[3]) <
						texHeight-int(gtb.gtbMargin)*2 {
						gtb.gtbState = gtbNeedRender
					}
				}
			}
		}

		gtb.gtbSavedWidth = int16(rc.rcWidth)
		gtb.gtbSavedHeight = int16(rc.rcHeight)
		gtb.gtbUpdateCursor = true
		gtb.gtbNeedLayout = true
	}

	if ti != nil && gtb.gtbNeedLayout {

		margin := int(gtb.gtbMargin)

		left := int(gtb.gtbPadding[0]) - margin
		top := int(rc.rcHeight) - int(gtb.gtbPadding[1]) + margin
		right := int(rc.rcWidth) - int(gtb.gtbPadding[2]) + margin
		bottom := int(gtb.gtbPadding[3]) - margin

		textWidth := texWidth
		textHeight := texHeight

		// Vertical
		if textHeight > top-bottom {
			// Oversized, must cut
			textHeight = top - bottom
		} else {
			switch w.glwAlignment {
			case layoutAlignCenter,
				layoutAlignLeft,
				layoutAlignRight:
				bottom = (bottom + top - textHeight) / 2
				top = bottom + textHeight

			case layoutAlignTopLeft,
				layoutAlignTopRight,
				layoutAlignTop,
				layoutAlignJustified:
				bottom = top - texHeight

			case layoutAlignBottom,
				layoutAlignBottomLeft,
				layoutAlignBottomRight:
				top = bottom + texHeight
			}
		}

		y1 := -1.0 + 2.0*float32(bottom)/float32(rc.rcHeight)
		y2 := -1.0 + 2.0*float32(top)/float32(rc.rcHeight)

		// Horizontal
		if textWidth > right-left || ti.Flags&imagepkg.TextTruncated != 0 {

			// Oversized, must cut
			textWidth = right - left
		} else {

			glwRendererVtxColReset(&gtb.gtbTextRenderer)

			switch w.glwAlignment {
			case layoutAlignJustified,
				layoutAlignCenter,
				layoutAlignBottom,
				layoutAlignTop:
				left = (left + right - textWidth) / 2
				right = left + textWidth

			case layoutAlignLeft,
				layoutAlignTopLeft,
				layoutAlignBottomLeft:
				right = left + texWidth

			case layoutAlignRight,
				layoutAlignTopRight,
				layoutAlignBottomRight:
				left = right - texWidth
			}
		}

		x1 := -1.0 + 2.0*float32(left)/float32(rc.rcWidth)
		x2 := -1.0 + 2.0*float32(right)/float32(rc.rcWidth)

		s := float32(textWidth) / float32(texWidth)
		t := float32(textHeight) / float32(texHeight)

		glwRendererVtxPos(&gtb.gtbTextRenderer, 0, x1, y1, 0.0)
		glwRendererVtxSt(&gtb.gtbTextRenderer, 0, 0, t)

		glwRendererVtxPos(&gtb.gtbTextRenderer, 1, x2, y1, 0.0)
		glwRendererVtxSt(&gtb.gtbTextRenderer, 1, s, t)

		glwRendererVtxPos(&gtb.gtbTextRenderer, 2, x2, y2, 0.0)
		glwRendererVtxSt(&gtb.gtbTextRenderer, 2, s, 0)

		glwRendererVtxPos(&gtb.gtbTextRenderer, 3, x1, y2, 0.0)
		glwRendererVtxSt(&gtb.gtbTextRenderer, 3, 0, 0)
	}

	if w.glwClass == &glwTextClass && gtb.gtbUpdateCursor {

		i := int(gtb.gtbEditPtr)
		var left int

		if ti != nil && ti.CharPos != nil {

			if i < int(ti.CharPosLen) {
				left = ti.CharPos[i*2]
			} else {
				left = ti.CharPos[2*int(ti.CharPosLen)-1]
			}
		} else {
			left = 0
		}

		left += int(gtb.gtbPadding[0])

		x1 := -1.0 + 2.0*float32(left-1)/float32(rc.rcWidth)
		x2 := -1.0 + 2.0*float32(left)/float32(rc.rcWidth)
		y1 := -1.0 + 2.0*float32(gtb.gtbPadding[3])/float32(rc.rcHeight)
		y2 := 1.0 - 2.0*float32(gtb.gtbPadding[1])/float32(rc.rcHeight)

		glwRendererVtxPos(&gtb.gtbCursorRenderer, 0, x1, y1, 0.0)
		glwRendererVtxPos(&gtb.gtbCursorRenderer, 1, x2, y1, 0.0)
		glwRendererVtxPos(&gtb.gtbCursorRenderer, 2, x2, y2, 0.0)
		glwRendererVtxPos(&gtb.gtbCursorRenderer, 3, x1, y2, 0.0)

		gtb.gtbUpdateCursor = false
	}

	gtb.gtbPaintCursor =
		gtb.gtbFlags&gtbPermanentCursor != 0 ||
			(w.glwClass == &glwTextClass &&
				(glwIsFocused(w) || gr.grOskWidget == w))

	if gtb.gtbPaintCursor && rc.rcAlpha > glwAlphaEpsilon {
		glwNeedRefresh(gr, 0)
	}

	gtb.gtbNeedLayout = false

	if gtb.gtbState == gtbValid && gtb.gtbDeferredRealize {
		gtb.gtbDeferredRealize = false
		gtbRealize(gtb)
	}

	if gtb.gtbState != gtbNeedRender {
		return
	}

	gtbTailqInsertTail(&gr.grGtbRenderQueue, gtb)
	gtb.gtbState = gtbQueuedForRendering

	gr.grGtbWorkCond.Signal()
}

// C: static void glw_text_bitmap_render (glw_text_bitmap.c:359-407)
func glwTextBitmapRender(w *Glw, rc *glwRctx) {
	gtb := (*GlwTextBitmap)(unsafe.Pointer(w))
	blur := 1 - rc.rcSharpness*w.glwSharpness
	rc0 := *rc

	if glwIsFocusableOrClickable(w) {
		glwStoreMatrix(w, rc)
	}

	alpha := rc.rcAlpha * w.glwAlpha

	if alpha < glwAlphaEpsilon {
		return
	}

	if gtb.gtbBackgroundAlpha > glwAlphaEpsilon {
		glwRendererDraw(&gtb.gtbBackgroundRenderer, w.glwRoot, &rc0,
			nil, nil,
			&gtb.gtbBackgroundColor, nil,
			gtb.gtbBackgroundAlpha*alpha, blur, nil)
		glwZinc(&rc0)
	}

	if glwIsTexStarted(&gtb.gtbTexture) && gtb.gtbImage != nil {
		glwRendererDraw(&gtb.gtbTextRenderer, w.glwRoot, &rc0,
			&gtb.gtbTexture, nil,
			&gtb.gtbColor, nil, alpha, blur, nil)
	}

	if gtb.gtbPaintCursor {
		gr := w.glwRoot
		a := float32(math.Cos(float64(gr.grFrames&2047)*(360.0/2048.0)))*0.5 + 0.5

		glwZinc(&rc0)

		glwRendererDraw(&gtb.gtbCursorRenderer, w.glwRoot, &rc0,
			nil, nil, nil, nil, alpha*a, blur, nil)
	}
}

// C: static void glw_text_bitmap_dtor (glw_text_bitmap.c:412-446)
func glwTextBitmapDtor(w *Glw) {
	gtb := (*GlwTextBitmap)(unsafe.Pointer(w))
	gr := w.glwRoot

	miscpkg.RstrRelease(gtb.gtbFont)

	gtb.gtbImage.Release()

	gtbListRemove(gtb)

	glwTexDestroy(gr, &gtb.gtbTexture)

	glwRendererFree(&gtb.gtbTextRenderer)
	glwRendererFree(&gtb.gtbCursorRenderer)
	glwRendererFree(&gtb.gtbBackgroundRenderer)

	switch gtb.gtbState {
	case gtbIdle,
		gtbDimensioning,
		gtbNeedRender,
		gtbRendering,
		gtbValid:

	case gtbQueuedForDimensioning:
		gtbTailqRemove(&gr.grGtbDimQueue, gtb)

	case gtbQueuedForRendering:
		gtbTailqRemove(&gr.grGtbRenderQueue, gtb)
	}
}

// C: static void gtb_set_constraints (glw_text_bitmap.c:451-476)
func gtbSetConstraints(gr *glwRoot, gtb *GlwTextBitmap, im *imagepkg.Image) {
	flags := glwConstraintY

	xs := int(im.Width) - int(im.Margin)*2 +
		int(gtb.gtbPadding[0]) + int(gtb.gtbPadding[2])

	ys := int(im.Height) - int(im.Margin)*2 +
		int(gtb.gtbPadding[1]) + int(gtb.gtbPadding[3])

	if gtb.gtbMaxlines == 1 && gtb.gtbFlags&gtbEllipsize == 0 {
		flags |= glwConstraintX
	}

	glwSetConstraints(&gtb.w, xs, ys, 0, flags)
}

// C: static void gtb_recompute_constraints_after_padding_change
// (glw_text_bitmap.c:479-502)
func gtbRecomputeConstraintsAfterPaddingChange(gtb *GlwTextBitmap,
	oldpad [4]int16) {
	w := &gtb.w
	flags := w.glwFlags & (glwConstraintY | glwConstraintX)
	if flags == 0 {
		return
	}

	xs := int(w.glwReqSizeX) - int(oldpad[0]) - int(oldpad[2]) +
		int(gtb.gtbPadding[0]) + int(gtb.gtbPadding[2])

	ys := int(w.glwReqSizeY) - int(oldpad[1]) - int(oldpad[3]) +
		int(gtb.gtbPadding[1]) + int(gtb.gtbPadding[3])

	glwSetConstraints(w, xs, ys, 0, flags)
}

// C: static void gtb_inactive (glw_text_bitmap.c:508-516)
func gtbInactive(gtb *GlwTextBitmap) {
	glwTexDestroy(gtb.w.glwRoot, &gtb.gtbTexture)

	// Make sure it is rerendered once we get back to life
	if gtb.gtbState == gtbValid {
		gtb.gtbState = gtbNeedRender
	}
}

// C: static int del_char (glw_text_bitmap.c:521-537)
func delChar(gtb *GlwTextBitmap) int {
	if gtb.gtbEditPtr == 0 {
		return 0
	}

	gtb.gtbUcLen--
	gtb.gtbEditPtr--
	gtb.gtbUpdateCursor = true

	for i := int(gtb.gtbEditPtr); i != int(gtb.gtbUcLen); i++ {
		gtb.gtbUcBuffer[i] = gtb.gtbUcBuffer[i+1]
	}
	gtb.gtbCaptionDirty = true
	return 1
}

// C: static int insert_char (glw_text_bitmap.c:543-564)
func insertChar(gtb *GlwTextBitmap, ch int) int {
	if gtb.gtbUcLen == gtb.gtbUcSize {
		gtb.gtbUcSize += 10
		nb := make([]uint32, gtb.gtbUcSize)
		copy(nb, gtb.gtbUcBuffer)
		gtb.gtbUcBuffer = nb
	}

	i := int(gtb.gtbUcLen)
	for ; i != int(gtb.gtbEditPtr); i-- {
		gtb.gtbUcBuffer[i] = gtb.gtbUcBuffer[i-1]
	}

	gtb.gtbUcBuffer[i] = uint32(ch)
	gtb.gtbUcLen++
	gtb.gtbEditPtr++
	gtb.gtbUpdateCursor = true
	gtb.gtbCaptionDirty = true
	return 1
}

// C: static void gtb_unbind (glw_text_bitmap.c:570-581)
func gtbUnbind(gtb *GlwTextBitmap) {
	if gtb.gtbSub != nil {
		gtb.gtbSub.Unsubscribe()
		gtb.gtbSub = nil
	}

	if gtb.gtbP != nil {
		glwDeps.pm.RefDec(gtb.gtbP)
		gtb.gtbP = nil
	}
}

// C: static int glw_text_bitmap_callback (glw_text_bitmap.c:587-602)
func glwTextBitmapCallback(w *Glw, opaque any, signal glwSignal,
	extra any) int {
	gtb := (*GlwTextBitmap)(unsafe.Pointer(w))

	switch signal {
	case glwSignalDestroy:
		gtbUnbind(gtb)
	case glwSignalInactive:
		gtbInactive(gtb)
	}
	return 0
}

// C: static void insert_str (glw_text_bitmap.c:605-612)
func insertStr(gtb *GlwTextBitmap, str string) {
	b := []byte(str)
	for {
		uc := miscpkg.Utf8Get(&b)
		if uc == 0 {
			break
		}
		insertChar(gtb, uc)
	}
	gtbNotify(gtb)
}

// C: static int glw_text_bitmap_event (glw_text_bitmap.c:617-708)
func glwTextBitmapEvent(w *Glw, e *eventpkg.Event) int {
	gtb := (*GlwTextBitmap)(unsafe.Pointer(w))

	if eventpkg.IsAction(e, eventpkg.ACTION_BS) {

		delChar(gtb)
		gtbNotify(gtb)
		return 1

	} else if e.Type == eventpkg.EVENT_UNICODE {

		eu := (*eventpkg.EventInt)(unsafe.Pointer(e))

		if insertChar(gtb, eu.Val) != 0 {
			gtbNotify(gtb)
		}
		return 1

	} else if e.Type == eventpkg.EVENT_INSERT_STRING {
		ep := (*eventpkg.EventPayload)(unsafe.Pointer(e))
		insertStr(gtb, ep.Payload)
		return 1

	} else if eventpkg.IsAction(e, eventpkg.ACTION_PASTE) {

		if glwDeps.clip != nil {
			if str := glwDeps.clip.Get(); str != nil {
				insertStr(gtb, *str)
			}
		}
		return 1

	} else if eventpkg.IsAction(e, eventpkg.ACTION_LEFT) {

		if gtb.gtbEditPtr > 0 {
			gtb.gtbEditPtr--
			gtb.gtbUpdateCursor = true
		}
		return 1

	} else if eventpkg.IsAction(e, eventpkg.ACTION_RIGHT) {

		if gtb.gtbEditPtr < gtb.gtbUcLen {
			gtb.gtbEditPtr++
			gtb.gtbUpdateCursor = true
		}
		return 1

	} else if eventpkg.IsAction(e, eventpkg.ACTION_ACTIVATE) {

		gtbCaptionRefresh(gtb)

		if gtb.gtbFlags&(gtbFileRequest|gtbDirRequest) != 0 {

			if gtb.gtbP == nil {
				glwDeps.ts.Trace(tracepkg.TRACE_ERROR, "GLW",
					"File requests on unbound widgets is not supported")
			} else {

				flags := 0
				if gtb.gtbFlags&gtbFileRequest != 0 {
					flags |= facore.FilepickerFiles
				}
				if gtb.gtbFlags&gtbDirRequest != 0 {
					flags |= facore.FilepickerDirectories
				}

				facore.FilepickerPickToProp(
					glwDeps.fam,
					gtb.gtbDescription,
					gtb.gtbP, gtb.gtbCaption,
					flags)
			}

		} else {

			if eventpkg.IsAction(e, eventpkg.ACTION_ACTIVATE) &&
				e.Flags&eventpkg.EventMouse != 0 {
				return 1
			}

			glwOskOpen(w.glwRoot,
				gtb.gtbDescription,
				gtb.gtbCaption, w,
				gtb.gtbFlags&gtbPassword)
		}
		return 1
	}
	return 0
}

// C: static void gtb_realize (glw_text_bitmap.c:713-738)
func gtbRealize(gtb *GlwTextBitmap) {
	gr := gtb.w.glwRoot
	direct := gtb.gtbMaxlines > 1

	if gtb.gtbState != gtbIdle && gtb.gtbState != gtbValid {
		gtb.gtbDeferredRealize = true
		return
	}

	if direct {
		if gtb.w.glwFlags2&glw2Autohide != 0 {
			glwUnhide(&gtb.w)
		}

		gtb.gtbState = gtbNeedRender
		glwNeedRefresh(gr, 0)
	} else {
		gtbTailqInsertTail(&gr.grGtbDimQueue, gtb)
		gtb.gtbState = gtbQueuedForDimensioning
		gr.grGtbWorkCond.Signal()
	}
}

// C: static void caption_set_internal (glw_text_bitmap.c:747-792)
func captionSetInternal(gtb *GlwTextBitmap, str string, typ int) {
	gtbCaptionRefresh(gtb)

	if gtb.gtbCaptionSet && str == gtb.gtbCaption &&
		typ == gtb.gtbCaptionType {
		return
	}

	gtb.gtbCaption = str
	gtb.gtbCaptionSet = true
	gtb.gtbCaptionType = typ

	if gtb.w.glwFlags2&glw2Autohide != 0 {
		if str == "" {
			glwHide(&gtb.w)
		}
	}

	flags := 0
	if gtb.gtbCaptionType == propcore.PropStrRich {
		flags |= textpkg.TEXT_PARSE_HTML_TAGS | textpkg.TEXT_PARSE_HTML_ENTITIES
	}

	uc, length := glwDeps.textSys.TextParse(gtb.gtbCaption, flags, nil, 0)
	gtb.gtbUcBuffer = uc[:length]
	gtb.gtbCaptionDirty = false
	gtb.gtbUcLen = int16(length)
	gtb.gtbUcSize = int16(length)

	if gtb.w.glwClass == &glwTextClass {
		gtb.gtbEditPtr = gtb.gtbUcLen
		gtb.gtbUpdateCursor = true
	}

	gtbUpdateEpilogue(gtb, gtbUpdateRealize)
}

// C: static void gtb_update_epilogue (glw_text_bitmap.c:798-810)
func gtbUpdateEpilogue(gtb *GlwTextBitmap, flags int) {
	if gtb.gtbFrozen {
		gtb.gtbPendingUpdates |= uint8(flags)
	} else {

		if flags&gtbUpdateRealize != 0 {
			gtbRealize(gtb)
		}

		gtb.gtbPendingUpdates = 0
	}
}

// C: void glw_gtb_set_caption_raw (glw_text_bitmap.c:816-827)
func glwGtbSetCaptionRaw(w *Glw, uc []uint32, length int) {
	gtb := (*GlwTextBitmap)(unsafe.Pointer(w))
	gtb.gtbCaptionDirty = true

	gtb.gtbUcBuffer = uc[:length]
	gtb.gtbUcLen = int16(length)
	gtb.gtbUcSize = int16(length)

	gtbUpdateEpilogue(gtb, gtbUpdateRealize)
}

// C: static void prop_callback (glw_text_bitmap.c:833-860)
func gtbPropCallback(opaque any, event propcore.EventType,
	args ...any) {
	gtb := opaque.(*GlwTextBitmap)
	var caption string
	var typ int

	switch event {
	case propcore.EventSetVoid:
		// caption = NULL
	case propcore.EventSetRString:
		caption = strFromArg(args[0])
		typ = intFromArg(args[1])
	case propcore.EventValueProp:
		if gtb.gtbP != nil {
			glwDeps.pm.RefDec(gtb.gtbP)
		}
		gtb.gtbP = glwDeps.pm.RefInc(args[0].(*propcore.Prop))
		return
	default:
		return
	}

	captionSetInternal(gtb, caption, typ)
}

// C: static void glw_text_bitmap_ctor (glw_text_bitmap.c:866-880)
func glwTextBitmapCtor(w *Glw) {
	gtb := (*GlwTextBitmap)(unsafe.Pointer(w))
	gr := w.glwRoot

	w.glwFlags2 |= glw2FocusOnClick
	gtb.gtbEditPtr = 0
	gtb.gtbSizeScale = 1.0
	gtb.gtbColor.r = 1.0
	gtb.gtbColor.g = 1.0
	gtb.gtbColor.b = 1.0
	gtb.gtbMaxlines = 1
	gtb.gtbUpdateCursor = true
	gtbListInsertHead(&gr.grGtbs, gtb)
}

// C: static int glw_text_bitmap_set_float3 (glw_text_bitmap.c:886-899)
func glwTextBitmapSetFloat3(w *Glw, attrib glwAttribute, rgb []float32,
	origin *GlwStyle) int {
	gtb := (*GlwTextBitmap)(unsafe.Pointer(w))

	switch attrib {
	case glwAttribRGB:
		return glwAttribSetRgb(&gtb.gtbColor, (*[3]float32)(rgb))
	case glwAttribBackgroundColor:
		return glwAttribSetRgb(&gtb.gtbBackgroundColor, (*[3]float32)(rgb))
	default:
		return -1
	}
}

// C: static int gtb_set_int16_4 (glw_text_bitmap.c:904-924)
func gtbSetInt16_4(w *Glw, attrib glwAttribute, v []int16,
	origin *GlwStyle) int {
	gtb := (*GlwTextBitmap)(unsafe.Pointer(w))
	switch attrib {
	case glwAttribPadding:
		oldpad := gtb.gtbPadding
		if glwAttribSetInt164(&gtb.gtbPadding, (*[4]int16)(v)) == 0 {
			return 0
		}
		gtbRecomputeConstraintsAfterPaddingChange(gtb, oldpad)
		gtb.gtbNeedLayout = true
		return 1
	default:
		return -1
	}
}

// C: static void mod_text_flags (glw_text_bitmap.c:928-935)
func gtbModTextFlags(w *Glw, set int, clr int, origin *GlwStyle) {
	gtb := (*GlwTextBitmap)(unsafe.Pointer(w))
	gtb.gtbFlags = (gtb.gtbFlags | set) &^ clr

	gtbUpdateEpilogue(gtb, gtbUpdateRealize)
}

// C: static void set_caption (glw_text_bitmap.c:940-946)
func gtbSetCaption(w *Glw, caption string, typ int) {
	gtb := (*GlwTextBitmap)(unsafe.Pointer(w))
	gtbUnbind(gtb)
	captionSetInternal(gtb, caption, typ)
}

// C: static int gtb_set_rstr (glw_text_bitmap.c:952-970)
func gtbSetRstr(w *Glw, a glwAttribute, str *miscpkg.Rstr,
	origin *GlwStyle) int {
	gtb := (*GlwTextBitmap)(unsafe.Pointer(w))
	switch a {
	case glwAttribFont:
		if miscpkg.RstrEq(gtb.gtbFont, str) != 0 {
			return 0
		}
		miscpkg.RstrSet(&gtb.gtbFont, str)
	default:
		return -1
	}

	gtbUpdateEpilogue(gtb, gtbUpdateRealize)
	return 1
}

// C: static void bind_to_property (glw_text_bitmap.c:976-996)
func gtbBindToProperty(w *Glw, scope *glwScope, pname []string) {
	gtb := (*GlwTextBitmap)(unsafe.Pointer(w))
	gtbUnbind(gtb)

	gtb.gtbSub = glwPropSubscribeTags(
		propcore.SubFlagDirectUpdate|propcore.SubFlagSendValueProp,
		propTagName, pname,
		propTagCallback, gtbPropCallback, gtb,
		propTagCourier, w.glwRoot.grCourier,
		propTagRootVector, scope.gsRoots[:scope.gsNumRoots],
		propTagRoot, w.glwRoot.grPropUi,
		propTagNamedRoot, w.glwRoot.grPropNav, "nav")

	if w.glwFlags2&glw2Autohide != 0 {
		glwUnhide(w)
	}
}

// C: static void freeze (glw_text_bitmap.c:1001-1006)
func gtbFreeze(w *Glw) {
	gtb := (*GlwTextBitmap)(unsafe.Pointer(w))
	gtb.gtbFrozen = true
}

// C: static void thaw (glw_text_bitmap.c:1011-1029)
func gtbThaw(w *Glw) {
	gtb := (*GlwTextBitmap)(unsafe.Pointer(w))
	gtb.gtbFrozen = false

	if w.glwFlags&glwConstraintY == 0 {
		lh := int(gtb.gtbDefaultSize)
		if lh == 0 {
			lh = w.glwRoot.grCurrentSize
		}
		lh = int(float32(lh) * gtb.gtbSizeScale)
		ys := int(gtb.gtbPadding[1]) + int(gtb.gtbPadding[3]) + lh
		glwSetConstraints(&gtb.w, 0, ys, 0, glwConstraintY)
	}

	if gtb.gtbPendingUpdates&gtbUpdateRealize != 0 {
		gtbRealize(gtb)
	}

	gtb.gtbPendingUpdates = 0
}

// C: static int gtb_set_em (glw_text_bitmap.c:1035-1053)
func gtbSetEm(w *Glw, a glwAttribute, v float32) int {
	gtb := (*GlwTextBitmap)(unsafe.Pointer(w))

	switch a {
	case glwAttribSize:
		if gtb.gtbSizeScale == v {
			return 0
		}
		gtb.gtbSizeScale = v
	default:
		return -1
	}
	gtbUpdateEpilogue(gtb, gtbUpdateRealize)
	return 1
}

// C: static int gtb_set_float (glw_text_bitmap.c:1058-1079)
func gtbSetFloat(w *Glw, a glwAttribute, v float32, origin *GlwStyle) int {
	gtb := (*GlwTextBitmap)(unsafe.Pointer(w))

	switch a {
	case glwAttribSizeScale:
		return gtbSetEm(w, glwAttribSize, v)

	case glwAttribBackgroundAlpha:
		if gtb.gtbBackgroundAlpha == v {
			return 0
		}
		gtb.gtbBackgroundAlpha = v
		return 1

	default:
		return -1
	}
	// C: unreachable gtb_update_epilogue + return 1 after the switch —
	// all branches return first (dead code in C too, omitted for vet).
}

// C: static int gtb_set_int (glw_text_bitmap.c:1085-1118)
func gtbSetInt(w *Glw, a glwAttribute, v int, origin *GlwStyle) int {
	gtb := (*GlwTextBitmap)(unsafe.Pointer(w))

	switch a {
	case glwAttribSize:
		if gtb.gtbDefaultSize == int16(v) {
			return 0
		}
		gtb.gtbDefaultSize = int16(v)

	case glwAttribMaxWidth:
		if gtb.gtbMaxWidth == int16(v) {
			return 0
		}
		gtb.gtbMaxWidth = int16(v)

	case glwAttribMaxLines:
		if gtb.gtbMaxlines == int16(v) {
			return 0
		}
		gtb.gtbMaxlines = int16(v)

	default:
		return -1
	}
	gtbUpdateEpilogue(gtb, gtbUpdateRealize)
	return 1
}

// C: static void do_render (glw_text_bitmap.c:1123-1250)
func gtbDoRender(gtb *GlwTextBitmap, gr *glwRoot, noOutput int) {
	var uc []uint32
	var im *imagepkg.Image
	var maxWidth int

	/* We are going to render unlocked so we cannot use gtb at all */

	length := int(gtb.gtbUcLen)
	if length > 0 {
		uc = make([]uint32, length+3)

		if gtb.gtbFlags&gtbPassword != 0 {
			showOne := 0
			if gtb.gtbFlags&gtbOskPassword != 0 {
				showOne = 1
			}
			for i := range length - showOne {
				uc[i] = '*'
			}
			if showOne != 0 && length > 0 {
				uc[length-1] = gtb.gtbUcBuffer[length-1]
			}
		} else {
			copy(uc, gtb.gtbUcBuffer[:length])
		}
	}

	defaultSize := int(gtb.gtbDefaultSize)
	if defaultSize == 0 {
		defaultSize = gr.grCurrentSize
	}
	scale := gtb.gtbSizeScale
	minSize := 0

	flags := 0

	if noOutput != 0 {
		maxWidth = min(int(gtb.gtbMaxWidth), gr.grWidth)
		flags |= textpkg.TR_RENDER_NO_OUTPUT
	} else {
		maxWidth =
			max(int(gtb.gtbMaxWidth), int(gtb.gtbSavedWidth)) -
				int(gtb.gtbPadding[0]) - int(gtb.gtbPadding[2])
	}

	maxLines := int(gtb.gtbMaxlines)

	if gtb.w.glwFlags2&glw2Debug != 0 {
		flags |= textpkg.TR_RENDER_DEBUG
	}

	if gtb.gtbFlags&gtbEllipsize != 0 {
		flags |= textpkg.TR_RENDER_ELLIPSIZE
	}

	if gtb.gtbFlags&gtbBold != 0 {
		flags |= textpkg.TR_RENDER_BOLD
	}

	if gtb.gtbFlags&gtbItalic != 0 {
		flags |= textpkg.TR_RENDER_ITALIC
	}

	if gtb.gtbFlags&gtbOutline != 0 {
		flags |= textpkg.TR_RENDER_OUTLINE
	}

	if gtb.w.glwClass == &glwTextClass {
		flags |= textpkg.TR_RENDER_CHARACTER_POS
	}

	trAlign := textpkg.TR_ALIGN_JUSTIFIED

	if gtb.w.glwFlags2&glw2Shadow != 0 {
		flags |= textpkg.TR_RENDER_SHADOW
	}

	switch gtb.w.glwAlignment {
	case layoutAlignCenter,
		layoutAlignBottom,
		layoutAlignTop:
		trAlign = textpkg.TR_ALIGN_CENTER

	case layoutAlignLeft,
		layoutAlignTopLeft,
		layoutAlignBottomLeft:
		trAlign = textpkg.TR_ALIGN_LEFT

	case layoutAlignRight,
		layoutAlignTopRight,
		layoutAlignBottomRight:
		trAlign = textpkg.TR_ALIGN_RIGHT
	}

	var font *miscpkg.Rstr
	if gtb.gtbFont != nil {
		font = miscpkg.RstrDup(gtb.gtbFont)
	} else {
		font = miscpkg.RstrDup(gr.grDefaultFont)
	}

	/* gtb (i.e the widget) may be destroyed directly after we unlock,
	   so we can't access it after this point. We can hold a reference
	   though. But it will only guarantee that the pointer stays valid */

	glwRef(&gtb.w)
	glwUnlock(gr)

	if uc != nil && uc[0] != 0 {
		im = glwDeps.textSys.TextRender(uc, length, flags, defaultSize, scale,
			trAlign, maxWidth, maxLines, miscpkg.RstrGet(font),
			gr.grFontDomain, minSize)
	}

	miscpkg.RstrRelease(font)
	glwLock(gr)

	if gtb.w.glwFlags&glwDestroying != 0 {
		/* widget got destroyed while we were away, throw away the results */
		glwUnref(&gtb.w)
		im.Release()
		return
	}

	glwUnref(&gtb.w)

	glwNeedRefresh(gr, 0)

	if gtb.gtbState == gtbRendering {
		gtb.gtbState = gtbValid
		gtb.gtbImage.Release()
		gtb.gtbImage = im
		gtb.gtbUpdateCursor = true
		if im != nil && gtb.gtbMaxlines > 1 {
			gtbSetConstraints(gr, gtb, im)
		}
	}

	if gtb.gtbState == gtbDimensioning {
		if im != nil {
			gtbSetConstraints(gr, gtb, im)
			im.Release()
			gtb.gtbState = gtbNeedRender
			if gtb.w.glwFlags2&glw2Autohide != 0 {
				glwUnhide(&gtb.w)
			}
		} else {
			gtb.gtbState = gtbIdle
			gtb.gtbImage.Release()
			gtb.gtbImage = nil
			lh := int(gtb.gtbDefaultSize)
			if lh == 0 {
				lh = gr.grCurrentSize
			}
			lh = int(float32(lh) * gtb.gtbSizeScale)
			lh += int(gtb.gtbPadding[1]) + int(gtb.gtbPadding[3])
			glwSetConstraints(&gtb.w, 0, lh, 0, glwConstraintY)
		}
	}
}

// C: static void *font_render_thread (glw_text_bitmap.c:1256-1288)
func fontRenderThread(aux any) any {
	gr := aux.(*glwRoot)

	glwLock(gr)

	for gr.grFontThreadRunning != 0 {

		if gtb := gr.grGtbDimQueue.tqhFirst; gtb != nil {

			gtbTailqRemove(&gr.grGtbDimQueue, gtb)
			gtb.gtbState = gtbDimensioning
			gtbDoRender(gtb, gr, 1)
			continue
		}

		if gtb := gr.grGtbRenderQueue.tqhFirst; gtb != nil {

			gtbTailqRemove(&gr.grGtbRenderQueue, gtb)
			gtb.gtbState = gtbRendering
			gtbDoRender(gtb, gr, 0)
			continue
		}
		glwCondWait(gr, &gr.grGtbWorkCond)
	}

	glwUnlock(gr)
	return nil
}

// C: void glw_text_flush (glw_text_bitmap.c:1294-1302)
func glwTextFlush(gr *glwRoot) {
	for gtb := gtbListEach(&gr.grGtbs); gtb != nil; gtb = gtb.gtbGlobalLinkNext {
		gtbInactive(gtb)
		gtbRealize(gtb)
	}
}

// C: static void gtb_notify (glw_text_bitmap.c:1307-1316)
func gtbNotify(gtb *GlwTextBitmap) {
	gtbRealize(gtb)

	if gtb.gtbP != nil {
		gtbCaptionRefresh(gtb)
		glwDeps.pm.SetStringEx(gtb.gtbP, gtb.gtbSub, gtb.gtbCaption, 0)
	}
}

// C: static void gtb_caption_refresh (glw_text_bitmap.c:1321-1339)
func gtbCaptionRefresh(gtb *GlwTextBitmap) {
	if !gtb.gtbCaptionDirty {
		return
	}

	length := 0
	for i := range int(gtb.gtbUcLen) {
		length += miscpkg.Utf8Put(nil, int(gtb.gtbUcBuffer[i]))
	}

	s := make([]byte, length)
	pos := 0
	for i := range int(gtb.gtbUcLen) {
		pos += miscpkg.Utf8Put(s[pos:], int(gtb.gtbUcBuffer[i]))
	}
	gtb.gtbCaption = string(s[:pos])
	gtb.gtbCaptionSet = true
	gtb.gtbCaptionDirty = false
}

// C: void glw_text_bitmap_init (glw_text_bitmap.c:1345-1356)
func glwTextBitmapStart(gr *glwRoot) {
	gtbTailqSetup(&gr.grGtbDimQueue)
	gtbTailqSetup(&gr.grGtbRenderQueue)

	gr.grGtbWorkCond = syncCond(&gr.grMutex)

	gr.grFontThreadRunning = 1
	gr.grFontThread = archpkg.ThreadCreateJoinable("GLW font renderer",
		fontRenderThread, gr, 0)
}

// C: void glw_text_bitmap_fini (glw_text_bitmap.c:1361-1370)
func glwTextBitmapFini(gr *glwRoot) {
	gr.grMutex.Lock()
	gr.grFontThreadRunning = 0
	gr.grGtbWorkCond.Signal()
	gr.grMutex.Unlock()
	gr.grFontThread.Join()
}

// C: static const char *glw_text_bitmap_get_text (glw_text_bitmap.c:1376-1382)
func glwTextBitmapGetText(w *Glw) string {
	gtb := (*GlwTextBitmap)(unsafe.Pointer(w))
	gtbCaptionRefresh(gtb)
	return gtb.gtbCaption
}

// C: static void mod_flags2 (glw_text_bitmap.c:1387-1402)
func gtbModFlags2(w *Glw, set int, clr int) {
	gtb := (*GlwTextBitmap)(unsafe.Pointer(w))

	if set&glw2Autohide != 0 && !gtb.gtbCaptionSet {
		glwHide(w)
	}

	if clr&glw2Autohide != 0 {
		glwUnhide(w)
	}

	if (set|clr)&glw2Shadow != 0 {
		gtbUpdateEpilogue(gtb, gtbUpdateRealize)
	}
}

// C: static void update_text (glw_text_bitmap.c:1407-1416)
func gtbUpdateText(w *Glw, str string) {
	gtb := (*GlwTextBitmap)(unsafe.Pointer(w))
	if gtb.gtbP != nil {
		gtb.gtbP.SetString(str)
	} else {
		gtbSetCaption(w, str, 0)
	}
}

// C: static void set_description (glw_text_bitmap.c:1421-1426)
func gtbSetDescription(w *Glw, str string) {
	gtb := (*GlwTextBitmap)(unsafe.Pointer(w))
	gtb.gtbDescription = str
}

// C: static glw_class_t glw_label / glw_text (glw_text_bitmap.c:1432-1497)
// + forward decls. Populated in init() — a package-var initializer cannot
// reference funcs that refer back to the var (Go init-cycle rule), same
// pattern as glwStyleClass.
var glwLabelClass glwClass
var glwTextClass glwClass

func registerTextBitmap() {
	glwLabelClass = glwClass{
		gcName:             "label",
		gcInstanceSize:     int(unsafe.Sizeof(GlwTextBitmap{})),
		gcNew:              func(parent *Glw) *Glw { gtb := &GlwTextBitmap{}; return &gtb.w },
		gcLayout:           glwTextBitmapLayout,
		gcRender:           glwTextBitmapRender,
		gcCtor:             glwTextBitmapCtor,
		gcDtor:             glwTextBitmapDtor,
		gcSignalHandler:    glwTextBitmapCallback,
		gcGetText:          glwTextBitmapGetText,
		gcDefaultAlignment: layoutAlignLeft,
		gcSetFloat3:        glwTextBitmapSetFloat3,
		gcSetInt16_4:       gtbSetInt16_4,
		gcModTextFlags:     gtbModTextFlags,
		gcSetCaption:       gtbSetCaption,
		gcSetRstr:          gtbSetRstr,
		gcBindToProperty:   gtbBindToProperty,
		gcModFlags2:        gtbModFlags2,
		gcFreeze:           gtbFreeze,
		gcThaw:             gtbThaw,
		gcSetFloat:         gtbSetFloat,
		gcSetEm:            gtbSetEm,
		gcSetInt:           gtbSetInt,
	}

	glwTextClass = glwClass{
		gcName:             "text",
		gcInstanceSize:     int(unsafe.Sizeof(GlwTextBitmap{})),
		gcNew:              func(parent *Glw) *Glw { gtb := &GlwTextBitmap{}; return &gtb.w },
		gcLayout:           glwTextBitmapLayout,
		gcRender:           glwTextBitmapRender,
		gcCtor:             glwTextBitmapCtor,
		gcDtor:             glwTextBitmapDtor,
		gcSignalHandler:    glwTextBitmapCallback,
		gcGetText:          glwTextBitmapGetText,
		gcDefaultAlignment: layoutAlignLeft,
		gcSetFloat3:        glwTextBitmapSetFloat3,
		gcSetInt16_4:       gtbSetInt16_4,
		gcModTextFlags:     gtbModTextFlags,
		gcSetCaption:       gtbSetCaption,
		gcSetRstr:          gtbSetRstr,
		gcBindToProperty:   gtbBindToProperty,
		gcFreeze:           gtbFreeze,
		gcThaw:             gtbThaw,
		gcSetFloat:         gtbSetFloat,
		gcSetEm:            gtbSetEm,
		gcSetInt:           gtbSetInt,
		gcUpdateText:       gtbUpdateText,
		gcSetDesc:          gtbSetDescription,
		gcBubbleEvent:      glwTextBitmapEvent,
	}

	// C: GLW_REGISTER_CLASS(glw_label) + GLW_REGISTER_CLASS(glw_text)
	glwRegisterClass(&glwLabelClass)
	glwRegisterClass(&glwTextClass)
}
