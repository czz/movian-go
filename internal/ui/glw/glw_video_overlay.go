package glw

// C: src/ui/glw/glw_video_overlay.c — canonical 1:1 port.
// Overlay lifecycle (DVD SPU / bitmap / text), layout, render, DVD menu
// pointer routing.

import (
	"github.com/czz/movian-go/internal/backend/dvd/dvdlib"
	eventpkg "github.com/czz/movian-go/internal/event"
	imagepkg "github.com/czz/movian-go/internal/image"
	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/video/decoder"
	"slices"
)

// ---------------------------------------------------------------------------
// LIST ops for glw_video_overlay_list

func gvoLISTInsertHead(l *glwVideoOverlayList, gvo *GlwVideoOverlay) {
	gvo.linkNext = l.lhFirst
	if gvo.linkNext != nil {
		gvo.linkNext.linkPrev = &gvo.linkNext
	}
	l.lhFirst = gvo
	gvo.linkPrev = &l.lhFirst
}

func gvoLISTRemove(gvo *GlwVideoOverlay) {
	if gvo.linkNext != nil {
		gvo.linkNext.linkPrev = gvo.linkPrev
	}
	*gvo.linkPrev = gvo.linkNext
}

// C: LIST_INSERT_SORTED — insert gvo so cmp(prev, gvo) <= 0 ordering holds.
func gvoLISTInsertSorted(l *glwVideoOverlayList, gvo *GlwVideoOverlay,
	cmp func(a, b *GlwVideoOverlay) int) {
	var elm *GlwVideoOverlay
	for elm = l.lhFirst; elm != nil; elm = elm.linkNext {
		if cmp(gvo, elm) < 0 {
			break
		}
	}
	if elm == nil {
		// insert at tail
		var last *GlwVideoOverlay
		for last = l.lhFirst; last != nil && last.linkNext != nil; last = last.linkNext {
		}
		gvo.linkNext = nil
		if last == nil {
			gvo.linkPrev = &l.lhFirst
			l.lhFirst = gvo
		} else {
			gvo.linkPrev = &last.linkNext
			last.linkNext = gvo
		}
	} else {
		gvo.linkNext = elm
		gvo.linkPrev = elm.linkPrev
		*elm.linkPrev = gvo
		elm.linkPrev = &gvo.linkNext
	}
}

// C: static glw_video_overlay_t *gvo_create (glw_video_overlay.c:85-93)
func gvoCreate(pts int64, gvoType int) *GlwVideoOverlay {
	gvo := &GlwVideoOverlay{}
	gvo.gvoStart = pts
	gvo.gvoStop = mediacore.PTSUnset
	gvo.gvoAlpha = 1
	gvo.gvoType = gvoType
	return gvo
}

// C: static void gvo_destroy (glw_video_overlay.c:96-104)
func gvoDestroy(gv *GlwVideo, gvo *GlwVideoOverlay) {
	gvoLISTRemove(gvo)
	glwTexDestroy(gv.w.glwRoot, &gvo.gvoTexture)
	glwRendererFree(&gvo.gvoRenderer)
	if gvo.gvoWidget != nil {
		glwDestroy(gvo.gvoWidget)
	}
}

// C: static void gvo_flush_all (glw_video_overlay.c:108-114)
func gvoFlushAll(gv *GlwVideo) {
	for gvo := gv.gvOverlays.lhFirst; gvo != nil; gvo = gv.gvOverlays.lhFirst {
		gvoDestroy(gv, gvo)
	}
}

// C: static void gvo_flush_infinite (glw_video_overlay.c:119-129)
// Destroy all overlays without an end time
func gvoFlushInfinite(gv *GlwVideo) {
	for gvo := gv.gvOverlays.lhFirst; gvo != nil; {
		next := gvo.linkNext
		if gvo.gvoStop == mediacore.PTSUnset || gvo.gvoStopEstimated != 0 {
			gvoDestroy(gv, gvo)
		}
		gvo = next
	}
}

// C: static void gvo_set_pts (glw_video_overlay.c:133-172)
func gvoSetPts(gv *GlwVideo, pts int64) {
	for gvo := gv.gvOverlays.lhFirst; gvo != nil; {
		next := gvo.linkNext

		if gvo.gvoStop != mediacore.PTSUnset && gvo.gvoStop <= pts {
			glwNeedRefresh(gv.w.glwRoot, 0)
			gvoDestroy(gv, gvo)
			gvo = next
			continue
		}

		var a float32
		if gvo.gvoFadein != 0 {
			a = glwRescale(float32(pts), float32(gvo.gvoStart),
				float32(gvo.gvoStart+int64(gvo.gvoFadein)))
			if a > 0.99 {
				a = 1
				gvo.gvoFadein = 0
			}
		} else if gvo.gvoFadeout != 0 {
			a = glwRescale(float32(pts), float32(gvo.gvoStop),
				float32(gvo.gvoStop-int64(gvo.gvoFadeout)))
		} else {
			a = 1
		}

		a = glwClamp(a, 0, 1)
		if a != gvo.gvoAlpha {
			gvo.gvoAlpha = a
			glwNeedRefresh(gv.w.glwRoot, 0)
		}

		gvo = next
	}
}

// C: typedef struct layer (glw_video_overlay.c:188-192). C builds the list
// on the stack with alloca; Go uses a slice — same semantics.
type gvoLayer struct {
	id         int
	usedHeight [10]int // consumed height for each alignment
}

// C: void glw_video_overlay_layout (glw_video_overlay.c:197-285)
func glwVideoOverlayLayout(gv *GlwVideo, frc, vrc *glwRctx) {
	var layers []*gvoLayer

	for gvo := gv.gvOverlays.lhFirst; gvo != nil; gvo = gvo.linkNext {
		w := gvo.gvoWidget
		if w == nil {
			continue
		}
		var rc *glwRctx
		if gv.gvVoOnVideo != 0 || gvo.gvoVideoframeAlign != 0 {
			rc = vrc
		} else {
			rc = frc
		}
		gc := w.glwClass

		var l *gvoLayer
		for _, ll := range layers {
			if ll.id == gvo.gvoLayer {
				l = ll
				break
			}
		}
		if l == nil {
			l = &gvoLayer{id: gvo.gvoLayer}
			layers = slices.Insert(layers, 0, l)

			bd := gv.gvBottomOverlayDisplacement
			l.usedHeight[layoutAlignBottom] = bd
			l.usedHeight[layoutAlignBottomLeft] = bd
			l.usedHeight[layoutAlignBottomRight] = bd
		}

		scaling := float32(1)

		if gvo.gvoCanvasHeight == -1 {
			if gv.gvVheight != 0 {
				scaling *= float32(rc.rcHeight) / float32(gv.gvVheight)
			}
		} else if gvo.gvoCanvasHeight != 0 {
			scaling *= float32(rc.rcHeight) / float32(gvo.gvoCanvasHeight)
		}

		if gv.gvVoScaling > 0 {
			scaling = scaling * gv.gvVoScaling / 100.0
		}

		gc.gcSetFloat(w, glwAttribSizeScale, scaling, nil)

		if gvo.gvoAbspos != 0 {

			glwLayout0(w, rc)

		} else {

			var f [4]int16
			f[0] = int16(scaling * float32(gvo.gvoPaddingLeft))
			f[1] = 0
			f[2] = int16(scaling * float32(gvo.gvoPaddingRight))
			f[3] = 0

			switch gvo.gvoAlignment {
			case layoutAlignTop, layoutAlignTopLeft, layoutAlignTopRight:
				f[1] = int16(glwMax(scaling*float32(gvo.gvoPaddingTop),
					float32(l.usedHeight[gvo.gvoAlignment])))
				l.usedHeight[gvo.gvoAlignment] = int(f[1])

			case layoutAlignBottom, layoutAlignBottomLeft, layoutAlignBottomRight:
				f[3] = int16(glwMax(scaling*float32(gvo.gvoPaddingBottom),
					float32(l.usedHeight[gvo.gvoAlignment])))
				l.usedHeight[gvo.gvoAlignment] = int(f[3])
			}

			gc.gcSetInt16_4(w, glwAttribPadding, f[:], nil)
			glwLayout0(w, rc)
			l.usedHeight[gvo.gvoAlignment] += int(w.glwReqSizeY)
		}
	}
}

// C: void glw_video_overlay_render (glw_video_overlay.c:289-451)
func glwVideoOverlayRender(gv *GlwVideo, frc, vrc *glwRctx) {
	gr := gv.w.glwRoot
	showDvdOverlays := 1
	var rc0 glwRctx

	// ENABLE_DVD — canonical quirk: the block only ever assigns 1 to
	// show_dvd_overlays (initialized to 1); it can never turn overlays
	// off. Preserved verbatim.
	vd := gv.gvVd
	if gv.gvWidth > 0 &&
		(glwIsFocused(&gv.w) ||
			!(len(vd.PCI) > 0 && dvdlib.PCIFromBytes(vd.PCI).HliSS() != 0)) {
		showDvdOverlays = 1
	}
	_ = vd

	for gvo := gv.gvOverlays.lhFirst; gvo != nil; gvo = gvo.linkNext {

		if gv.gvVoOnVideo != 0 || gvo.gvoVideoframeAlign != 0 {
			rc0 = *vrc
		} else {
			rc0 = *frc
		}

		glwZinc(&rc0)

		// Never do user displacement if in DVD menu, it will fail
		if gv.gvSpuInMenu == 0 {
			glwTranslatef(&rc0,
				float32(gv.gvVoDisplaceX)*2.0/float32(rc0.rcWidth),
				float32(gv.gvVoDisplaceY)*2.0/float32(rc0.rcHeight), 0)
		}

		switch gvo.gvoType {
		case gvoDVDSPU:
			if showDvdOverlays == 0 {
				continue
			}
			fallthrough

		case gvoBitmap:
			if gvo.gvoAlignment != 0 {

				left := gvo.gvoPaddingLeft
				top := int(rc0.rcHeight) - gvo.gvoPaddingTop
				right := int(rc0.rcWidth) - gvo.gvoPaddingRight
				bottom := gvo.gvoPaddingBottom

				width := gvo.gvoWidth
				height := gvo.gvoHeight

				var x1, y1, x2, y2 float32

				// Horizontal
				if width > right-left {
					// Oversized, must cut
					width = right - left
				} else {
					switch gvo.gvoAlignment {
					case 2, 5, 8:
						left = (left + right - width) / 2
						right = left + width

					case 1, 4, 7:
						right = left + gvo.gvoWidth

					case 3, 6, 9:
						left = right - gvo.gvoWidth
					}
				}

				// Vertical
				if height > top-bottom {
					// Oversized, must cut
					height = top - bottom
				} else {
					switch {
					case gvo.gvoAlignment >= 4 && gvo.gvoAlignment <= 6:
						bottom = (bottom + top - height) / 2
						top = bottom + height

					case gvo.gvoAlignment >= 7 && gvo.gvoAlignment <= 9:
						bottom = top - gvo.gvoHeight

					case gvo.gvoAlignment >= 1 && gvo.gvoAlignment <= 3:
						top = bottom + gvo.gvoHeight
					}
				}

				x1 = -1.0 + 2.0*float32(left)/float32(rc0.rcWidth)
				y1 = -1.0 + 2.0*float32(bottom)/float32(rc0.rcHeight)
				x2 = -1.0 + 2.0*float32(right)/float32(rc0.rcWidth)
				y2 = -1.0 + 2.0*float32(top)/float32(rc0.rcHeight)

				glwRendererVtxPos(&gvo.gvoRenderer, 0, x1, y1, 0)
				glwRendererVtxPos(&gvo.gvoRenderer, 1, x2, y1, 0)
				glwRendererVtxPos(&gvo.gvoRenderer, 2, x2, y2, 0)
				glwRendererVtxPos(&gvo.gvoRenderer, 3, x1, y2, 0)

			} else {
				var w, h float32

				if gvo.gvoCanvasWidth != 0 && gvo.gvoCanvasHeight != 0 {
					w = float32(gvo.gvoCanvasWidth)
					h = float32(gvo.gvoCanvasHeight)
				} else {
					w = float32(gv.gvWidth)
					h = float32(gv.gvHeight)
				}

				glwScalef(&rc0, 2/w, -2/h, 1.0)
				glwTranslatef(&rc0, -w/2, -h/2, 0)
			}

			glwRendererDraw(&gvo.gvoRenderer, gr, &rc0,
				&gvo.gvoTexture, nil, nil, nil,
				gvo.gvoAlpha*rc0.rcAlpha, 0, nil)

		case gvoText:
			rc0.rcAlpha *= gvo.gvoAlpha

			if gvo.gvoAbspos != 0 {

				x := gvo.gvoX * int(rc0.rcWidth) / gvo.gvoCanvasWidth
				y := gvo.gvoY * int(rc0.rcHeight) / gvo.gvoCanvasHeight

				glwReposition(&rc0,
					x,
					int(rc0.rcHeight)-y,
					int(rc0.rcWidth)+x,
					0-y)

				glwRender0(gvo.gvoWidget, &rc0)

			} else {

				glwRender0(gvo.gvoWidget, &rc0)
			}
		}
	}
}

// C: int glw_video_overlay_pointer_event (glw_video_overlay.c:455-506)
// ENABLE_DVD
func glwVideoOverlayPointerEvent(vd *decoder.VideoDecoder, width, height int,
	gpe *glwPointerEventT, mp *mediacore.MediaPipe) int {
	var best, dist, d int32

	if len(vd.PCI) == 0 {
		return 0
	}
	pci := dvdlib.PCIFromBytes(vd.PCI)
	if pci.HliSS() == 0 {
		return 0
	}

	x := int32((0.5 + 0.5*float64(gpe.localX)) * float64(width))
	y := int32((0.5 + -0.5*float64(gpe.localY)) * float64(height))

	best = 0
	dist = 0x08000000 // >> than (720*720)+(567*567)

	// Loop through all buttons
	for button := int32(1); button <= int32(pci.BtnNs()); button++ {
		xStart, xEnd, yStart, yEnd := pci.Btni(int(button - 1))

		if x >= xStart && x <= xEnd && y >= yStart && y <= yEnd {
			mx := (xStart + xEnd) / 2
			my := (yStart + yEnd) / 2
			dx := mx - x
			dy := my - y
			d = dx*dx + dy*dy
			// If the mouse is within the button and the mouse is closer
			// to the center of this button then it is the best choice.
			if d < dist {
				dist = d
				best = button
			}
		}
	}

	if best == 0 {
		return 1
	}

	// C: ep = event_create(EVENT_DVD_*, sizeof(event_t) + 1);
	//    ep->payload[0] = best; mp_enqueue_event(mp, &ep->h);
	// Go convention: MediaEvent.Data carries the raw payload bytes
	// (consumed by pkg/backend/dvd as []byte).
	var evType eventpkg.EventType
	switch gpe.typ {
	case glwPointerLeftPress:
		evType = eventpkg.EVENT_DVD_ACTIVATE_BUTTON

	case glwPointerMotionUpdate:
		if int32(vd.SPUCurBut) == best {
			return 1
		}
		evType = eventpkg.EVENT_DVD_SELECT_BUTTON

	default:
		return 1
	}

	mediacore.MpEnqueueEvent(mp, &mediacore.MediaEvent{
		Type: int(evType), Data: []byte{byte(best)}})
	return 1
}

// C: static void spu_repaint (glw_video_overlay.c:511-606)
func spuRepaint(gv *GlwVideo, d *mediacore.DVDSPU) {
	width := d.X2 - d.X1
	height := d.Y2 - d.Y1
	buf := d.Bitmap

	if width < 1 || height < 1 {
		return
	}

	// ENABLE_DVD
	vd := gv.gvVd
	var hiPalette [4]int
	var hiAlpha [4]int
	var ha *dvdlib.HighlightArea
	var pci *dvdlib.PCI
	hliSS := false

	if len(vd.PCI) > 0 {
		pci = dvdlib.PCIFromBytes(vd.PCI)
		hliSS = pci.HliSS() != 0
		if hliSS {
			gv.gvSpuInMenu = 1
		} else {
			gv.gvSpuInMenu = 0
		}

		if r, st := pci.GetHighlightArea(vd.SPUCurBut, 0); st == dvdlib.StatusOk {
			ha = r
			hiAlpha[0] = int(ha.Palette>>0) & 0xf
			hiAlpha[1] = int(ha.Palette>>4) & 0xf
			hiAlpha[2] = int(ha.Palette>>8) & 0xf
			hiAlpha[3] = int(ha.Palette>>12) & 0xf

			hiPalette[0] = int(ha.Palette>>16) & 0xf
			hiPalette[1] = int(ha.Palette>>20) & 0xf
			hiPalette[2] = int(ha.Palette>>24) & 0xf
			hiPalette[3] = int(ha.Palette>>28) & 0xf
		}
	}

	var haSX, haSY, haEX, haEY int
	if ha != nil {
		haSX = int(ha.SX) - d.X1
		haEX = int(ha.EX) - d.X1
		haSY = int(ha.SY) - d.Y1
		haEY = int(ha.EY) - d.Y1
	}

	pm := imagepkg.PixmapCreate(width, height, imagepkg.PixmapBGR32, 0)

	// XXX: this can be optimized in many ways
	for y := range height {
		for x := range width {
			i := int(buf[0])
			var px uint32

			if hliSS && ha != nil &&
				x >= haSX && y >= haSY && x <= haEX && y <= haEY {

				if hiAlpha[i] == 0 {
					px = 0
				} else {
					px = d.CLUT[hiPalette[i]&0xf] |
						uint32(hiAlpha[i]*0x11)<<24
				}
			} else {

				if d.Alpha[i] == 0 {
					// If it's 100% transparent, write RGB as zero too, or
					// weird aliasing effect will occur when GL scales
					// the texture
					px = 0
				} else {
					px = d.CLUT[d.Palette[i]&0xf] |
						uint32(d.Alpha[i]*0x11)<<24
				}
			}

			off := y*pm.Stride + x*4
			pm.Data[off+0] = byte(px)
			pm.Data[off+1] = byte(px >> 8)
			pm.Data[off+2] = byte(px >> 16)
			pm.Data[off+3] = byte(px >> 24)
			buf = buf[1:]
		}
	}

	gvoFlushAll(gv)

	gvo := gvoCreate(mediacore.PTSUnset, gvoDVDSPU)

	gvo.gvoCanvasWidth = d.CanvasWidth
	gvo.gvoCanvasHeight = d.CanvasHeight

	gvoLISTInsertHead(&gv.gvOverlays, gvo)
	gr := gv.w.glwRoot
	gvo.gvoVideoframeAlign = 1

	glwRendererSetupQuad(&gvo.gvoRenderer)

	const w = 1.0
	const h = 1.0
	r := &gvo.gvoRenderer

	glwRendererVtxPos(r, 0, float32(d.X1), float32(d.Y2), 0)
	glwRendererVtxSt(r, 0, 0, h)

	glwRendererVtxPos(r, 1, float32(d.X2), float32(d.Y2), 0)
	glwRendererVtxSt(r, 1, w, h)

	glwRendererVtxPos(r, 2, float32(d.X2), float32(d.Y1), 0)
	glwRendererVtxSt(r, 2, w, 0)

	glwRendererVtxPos(r, 3, float32(d.X1), float32(d.Y1), 0)
	glwRendererVtxSt(r, 3, 0, 0)

	glwTexUpload(gr, &gvo.gvoTexture, pm, 0)
	imagepkg.PixmapRelease(pm)
}

// C: static void glw_video_overlay_spu_layout (glw_video_overlay.c:611-661)
func glwVideoOverlaySpuLayout(gv *GlwVideo, pts int64) {
	gr := gv.w.glwRoot
	mp := gv.gvMp
	vd := gv.gvVd

	mp.OverlayMutex.Lock()

again:
	var d *mediacore.DVDSPU
	if len(mp.SpuQueue) > 0 {
		d = mp.SpuQueue[0]
	}

	if d == nil {
		mp.OverlayMutex.Unlock()
		return
	}

	glwNeedRefresh(gr, 0)

	if d.DestroyMe {
		goto destroy
	}

	{
		x := mediacore.DvdspuDecode(mp, d, pts)

		switch x {
		case -1:
			goto destroy

		case 0:
			if !vd.SPURepaint {
				break
			}
			vd.SPURepaint = false
			fallthrough

		case 1:
			spuRepaint(gv, d)
		}
		mp.OverlayMutex.Unlock()
		return
	}

destroy:
	mediacore.DvdspuDestroyOne(mp, d)
	gv.gvSpuInMenu = 0
	gvoFlushAll(gv)
	goto again
}

// C: static void gvo_create_from_vo_bitmap (glw_video_overlay.c:666-740)
func gvoCreateFromVoBitmap(gv *GlwVideo, vo *mediacore.VideoOverlay) {
	gvo := gvoCreate(vo.Start, gvoBitmap)
	gr := gv.w.glwRoot

	pm := vo.Pixmap
	W := pm.Width
	H := pm.Height

	gvoLISTInsertHead(&gv.gvOverlays, gvo)

	gvo.gvoStop = vo.Stop
	gvo.gvoFadein = vo.FadeIn
	gvo.gvoFadeout = vo.FadeOut
	gvo.gvoCanvasWidth = int(vo.CanvasWidth)
	gvo.gvoCanvasHeight = int(vo.CanvasHeight)

	glwRendererSetupQuad(&gvo.gvoRenderer)

	const w = 1.0
	const h = 1.0

	r := &gvo.gvoRenderer

	glwRendererVtxSt(r, 0, 0, h)
	glwRendererVtxSt(r, 1, w, h)
	glwRendererVtxSt(r, 2, w, 0)
	glwRendererVtxSt(r, 3, 0, 0)

	gvo.gvoAlignment = vo.Alignment

	if vo.Alignment == 0 {
		// Absolute coordinates

		gvo.gvoVideoframeAlign = 1

		if gvo.gvoCanvasHeight == 0 &&
			(int(vo.Y)+H+16 >= gv.gvHeight ||
				int(vo.X)+W+16 >= gv.gvWidth) {
			// Will display outside visible frame

			if int(vo.Y)+H < 720 && float32(vo.X)+w < 1280 {
				gvo.gvoCanvasWidth = 1280
				gvo.gvoCanvasHeight = 720
			} else {
				gvo.gvoCanvasWidth = 1920
				gvo.gvoCanvasHeight = 1080
			}
		}

		glwRendererVtxPos(r, 0, float32(vo.X), float32(int(vo.Y)+H), 0)
		glwRendererVtxPos(r, 1, float32(int(vo.X)+W), float32(int(vo.Y)+H), 0)
		glwRendererVtxPos(r, 2, float32(int(vo.X)+W), float32(vo.Y), 0)
		glwRendererVtxPos(r, 3, float32(vo.X), float32(vo.Y), 0)
	} else {
		gvo.gvoPaddingLeft = int(vo.PaddingLeft)
		gvo.gvoPaddingTop = int(vo.PaddingTop)
		gvo.gvoPaddingRight = int(vo.PaddingRight)
		gvo.gvoPaddingBottom = int(vo.PaddingBottom)
		gvo.gvoWidth = pm.Width
		gvo.gvoHeight = pm.Height
	}

	glwTexUpload(gr, &gvo.gvoTexture, pm, 0)
}

// C: static int gvo_padding_cmp (glw_video_overlay.c:745-758)
func gvoPaddingCmp(a, b *GlwVideoOverlay) int {
	aa := (a.gvoAlignment - 1) / 3
	ba := (b.gvoAlignment - 1) / 3

	if aa != ba {
		return aa - ba
	}

	if aa == 0 {
		return a.gvoPaddingBottom - b.gvoPaddingBottom
	}
	if aa == 2 {
		return a.gvoPaddingTop - b.gvoPaddingTop
	}
	return 0
}

// C: static void gvo_create_from_vo_text (glw_video_overlay.c:763-834)
func gvoCreateFromVoText(gv *GlwVideo, vo *mediacore.VideoOverlay) {
	gc := glwClassFindByName("label")

	if gc == nil {
		return // huh?
	}

	gvo := gvoCreate(vo.Start, gvoText)

	gvo.gvoStop = vo.Stop
	if vo.StopEstimated {
		gvo.gvoStopEstimated = 1
	}
	gvo.gvoFadein = vo.FadeIn
	gvo.gvoFadeout = vo.FadeOut
	gvo.gvoCanvasWidth = int(vo.CanvasWidth)
	gvo.gvoCanvasHeight = int(vo.CanvasHeight)
	gvo.gvoLayer = vo.Layer
	gvo.gvoX = int(vo.X)
	gvo.gvoY = int(vo.Y)
	if vo.AbsPos {
		gvo.gvoAbspos = 1
	}

	w := glwCreate(gv.w.glwRoot, gc, nil, nil, nil,
		gv.w.glwScope, nil, 0)

	gvo.gvoWidget = w

	gc.gcFreeze(w)

	gc.gcSetInt(w, glwAttribSize,
		int(float32(gv.w.glwRoot.grCurrentSize)*1.5), nil)

	if gvo.gvoAbspos != 0 {

		gvo.gvoVideoframeAlign = 1
		w.glwAlignment = layoutAlignTopLeft

		gvoLISTInsertHead(&gv.gvOverlays, gvo)

	} else {

		if vo.Alignment != 0 {
			w.glwAlignment = uint8(vo.Alignment)
		} else {
			w.glwAlignment = layoutAlignBottom
		}
		gvo.gvoAlignment = int(w.glwAlignment)

		if vo.PaddingLeft == -1 {
			defaultPad := gv.w.glwRoot.grCurrentSize
			gvo.gvoPaddingLeft = defaultPad
			gvo.gvoPaddingTop = defaultPad
			gvo.gvoPaddingRight = defaultPad
			gvo.gvoPaddingBottom = defaultPad
		} else {
			gvo.gvoPaddingLeft = int(vo.PaddingLeft)
			gvo.gvoPaddingTop = int(vo.PaddingTop)
			gvo.gvoPaddingRight = int(vo.PaddingRight)
			gvo.gvoPaddingBottom = int(vo.PaddingBottom)
		}
		gvoLISTInsertSorted(&gv.gvOverlays, gvo, gvoPaddingCmp)
	}

	gc.gcSetInt(w, glwAttribMaxLines, 10, nil)

	glwGtbSetCaptionRaw(w, vo.Text, vo.TextLength)
	vo.Text = nil // Steal it

	gc.gcThaw(w)
}

// C: static void glw_video_overlay_sub_set_pts (glw_video_overlay.c:839-885)
func glwVideoOverlaySubSetPts(gv *GlwVideo, pts int64) {
	gr := gv.w.glwRoot
	mp := gv.gvMp

	mp.OverlayMutex.Lock()

	for len(mp.OverlayQueue) > 0 {
		vo := mp.OverlayQueue[0]
		switch vo.Type {
		case mediacore.VOTimedFlush:
			if vo.Start > pts {
				goto done
			}
			fallthrough
		case mediacore.VOFlush:
			glwNeedRefresh(gr, 0)
			gvoFlushAll(gv)
			mediacore.VideoOverlayDequeueDestroy(mp, vo)
			continue

		case mediacore.VOBitmap:
			if vo.Start > pts {
				goto done
			}
			glwNeedRefresh(gr, 0)
			gvoFlushInfinite(gv)
			if vo.Pixmap != nil {
				gvoCreateFromVoBitmap(gv, vo)
			}
			mediacore.VideoOverlayDequeueDestroy(mp, vo)
			continue

		case mediacore.VOText:
			if vo.Start > pts {
				goto done
			}
			glwNeedRefresh(gr, 0)
			gvoFlushInfinite(gv)
			gvoCreateFromVoText(gv, vo)
			mediacore.VideoOverlayDequeueDestroy(mp, vo)
			continue
		}
		break
	}
done:
	mp.OverlayMutex.Unlock()
	gvoSetPts(gv, pts)
}

// C: void glw_video_overlay_set_pts (glw_video_overlay.c:890-898)
func glwVideoOverlaySetPts(gv *GlwVideo, pts int64) {
	vd := gv.gvVd

	glwVideoOverlaySpuLayout(gv, pts)
	pts -= vd.MP.SVDelta // C: vd->vd_mp->mp_svdelta
	glwVideoOverlaySubSetPts(gv, pts)
}

// C: void glw_video_overlay_deinit (glw_video_overlay.c:903-907)
func glwVideoOverlayTeardown(gv *GlwVideo) {
	gvoFlushAll(gv)
}
