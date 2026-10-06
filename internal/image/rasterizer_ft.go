package image

// Canonical port of src/image/rasterizer_ft.c — FreeType stroker
// rasterization of image_component_vector_t command buffers.
//
// All FT_* calls go through the rt* seam: rasterizer_ftwasm.go
// (wasm2go-translated FreeType on all platforms). The
// gray_spans callback renders through rasterizeSpans.

// RasterizerFTStart — C: rasterizer_ft_init (rasterizer_ft.c:463-471),
// INITME(INIT_GROUP_GRAPHICS). C exits(1) on init failure.
func RasterizerFTStart() {
	ftStartOnce.Do(func() {
		if rtInitLib() != 0 {
			panic("Freetype init error") // C: exit(1)
		}
	})
}

// ftState — C: state_t (rasterizer_ft.c:40-48).
type ftState struct {
	stroker     rtStroker
	inpath      int
	strokeWidth int
	strokeColor int
	fillEnable  int
	fillColor   int
	cur         [2]float32
}

// ftv — C: FT_Vector.
type ftv struct {
	x, y int64
}

// toVector — C: toVector (rasterizer_ft.c:53-58).
func toVector(v *ftv, f [2]float32) {
	v.x = int64(f[0] * 64)
	v.y = int64(f[1] * 64)
}

// ftCmdMove — C: cmd_move (rasterizer_ft.c:62-73).
func ftCmdMove(state *ftState, pt [2]float32) {
	var v ftv
	state.cur[0] = pt[0]
	state.cur[1] = pt[1]
	toVector(&v, pt)

	if state.inpath > 1 {
		rtStrokerEndSubPath(state.stroker)
	}
	rtStrokerBeginSubPath(state.stroker, v.x, v.y, 0)
	state.inpath = 1
}

// ftIsSmall — C: FT_IS_SMALL (rasterizer_ft.c:77-78).
func ftIsSmall(x int64) bool {
	return x > -2 && x < 2
}

// ftCmdCurve — C: cmd_curve (rasterizer_ft.c:81-112).
func ftCmdCurve(state *ftState, s, c, d, e [2]float32) {
	var S, Cv, D, E ftv

	toVector(&S, s)
	toVector(&Cv, c)
	toVector(&D, d)
	toVector(&E, e)

	// If all control points are coincident this is a no-op. This is
	// checked by freetype but not reported and drawing a subpath
	// with 0 elements will result in a segfault so we need to
	// check this ourselves.
	if ftIsSmall(S.x-Cv.x) && ftIsSmall(S.y-Cv.y) &&
		ftIsSmall(Cv.x-D.x) && ftIsSmall(Cv.y-D.y) &&
		ftIsSmall(D.x-E.x) && ftIsSmall(D.y-E.y) {
		return
	}

	if state.inpath != 0 {
		rtStrokerCubicTo(state.stroker, Cv.x, Cv.y, D.x, D.y, E.x, E.y)
		state.inpath++

		state.cur[0] = e[0]
		state.cur[1] = e[1]
	}
}

// ftCmdLine — C: cmd_line (rasterizer_ft.c:115-126).
func ftCmdLine(state *ftState, a [2]float32) {
	var v ftv
	state.cur[0] = a[0]
	state.cur[1] = a[1]
	toVector(&v, a)
	if state.inpath != 0 {
		rtStrokerLineTo(state.stroker, v.x, v.y)
		state.inpath++
	}
}

// ftCmdClose — C: cmd_close (rasterizer_ft.c:132-141).
func ftCmdClose(state *ftState) {
	if state.inpath > 1 {
		rtStrokerEndSubPath(state.stroker)
		state.inpath = 0
	} else {
		state.inpath = 1
	}
}

// rasterParams — C: struct raster_params (rasterizer_ft.c:146-151).
type rasterParams struct {
	pm    *Pixmap
	left  int
	top   int
	color int
}

// curRP — current raster params for the gray_spans callback. Safe
// because the callback only runs inside FT_Outline_Render under
// ftMutex.
var curRP rasterParams

// ftSpan — mirrors C: FT_Span { short x; unsigned short len;
// unsigned char coverage; } (ftimage.h) — 5 bytes, padded to 6
// (struct alignment is 2: the widest field is unsigned short).
type ftSpan struct {
	x        int16
	len      uint16
	coverage uint8
	_        [1]byte // padding to 6 (C sizeof(FT_Span))
}

// rasterizeSpans — C: params.gray_spans dispatch → rasterize_bgr32 /
// rasterize_ia (rasterizer_ft.c:328-336). curRP carries the
// raster_params (user is NULL in the canonical shim).
func rasterizeSpans(y int, spans []ftSpan) {
	switch curRP.pm.Type {
	case PixmapBGR32:
		rasterizeBgr32(y, spans)
	case PixmapIA:
		rasterizeIa(y, spans)
	}
}

// rasterize — C: rasterize (rasterizer_ft.c:315-372).
func rasterize(s *ftState, pm *Pixmap) {
	curRP.pm = pm
	curRP.left = 0
	curRP.top = 0

	if s.fillColor != 0 {
		curRP.color = s.fillColor
		points, contours := rtStrokerGetBorderCounts(s.stroker,
			rtStrokerBorderLeft)

		ol := rtOutlineNew(points, contours)
		rtOutlineClearCounts(ol) // C: ol.n_contours = ol.n_points = 0
		rtStrokerExportBorder(s.stroker, rtStrokerBorderLeft, ol)
		rtOutlineRender(ol)
		rtOutlineDone(ol)
	}

	if s.strokeWidth != 0 {
		curRP.color = s.strokeColor
		points, contours := rtStrokerGetCounts(s.stroker)

		ol := rtOutlineNew(points, contours)
		rtOutlineClearCounts(ol)
		rtStrokerExport(s.stroker, ol)
		rtOutlineRender(ol)
		rtOutlineDone(ol)
	}
}

// ImageRasterizeFT — C: image_rasterize_ft (rasterizer_ft.c:378-457).
func ImageRasterizeFT(ic *Component, width, height, margin int) *Image {
	icv := &ic.Vector

	var s ftState
	s.strokeColor = -1 // C: 0xffffffff as int
	s.fillColor = -1

	var ptype PixmapType
	if icv.Colorized {
		ptype = PixmapBGR32
	} else {
		ptype = PixmapIA
	}
	pm := PixmapCreate(width, height, ptype, margin)

	RasterizerFTStart() // ensure ftLib — C relies on INITME ordering
	ftMutex.Lock()

	s.stroker = rtStrokerNew()

	i32 := icv.IntData

	for i := 0; i < icv.Used; {
		switch i32[i] {
		default:
			panic("image_rasterize_ft: bad vector cmd") // C: abort()
		case int32(VcSetFillEnable):
			i++
			s.fillEnable = int(i32[i])
			i++
		case int32(VcSetFillColor):
			i++
			s.fillColor = int(i32[i])
			i++
		case int32(VcSetStrokeWidth):
			i++
			s.strokeWidth = int(i32[i])
			i++
		case int32(VcSetStrokeColor):
			i++
			s.strokeColor = int(i32[i])
			i++

		case int32(VcBegin):
			i++
			rtStrokerRewind(s.stroker)
			sw := 64 * s.strokeWidth
			if sw == 0 {
				sw = 1 // C: 64 * s.stroke_width ?: 1
			}
			rtStrokerSet(s.stroker, int64(sw),
				rtStrokerLinecapButt, rtStrokerLinejoinBevel, 0)

		case int32(VcMoveTo):
			i++
			ftCmdMove(&s, [2]float32{VecFloat(icv, i), VecFloat(icv, i+1)})
			i += 2
		case int32(VcLineTo):
			i++
			ftCmdLine(&s, [2]float32{VecFloat(icv, i), VecFloat(icv, i+1)})
			i += 2
		case int32(VcCubicTo):
			i++
			ftCmdCurve(&s, s.cur,
				[2]float32{VecFloat(icv, i), VecFloat(icv, i+1)},
				[2]float32{VecFloat(icv, i+2), VecFloat(icv, i+3)},
				[2]float32{VecFloat(icv, i+4), VecFloat(icv, i+5)})
			i += 6
		case int32(VcEnd):
			i++
			ftCmdClose(&s)
			rasterize(&s, pm)
		case int32(VcClose):
			i++
			ftCmdClose(&s)
		}
	}

	rtStrokerDone(s.stroker)
	ftMutex.Unlock()

	r := CreateFromPixmap(pm)
	PixmapRelease(pm)
	return r
}

// rasterizeBgr32 — C: rasterize_bgr32 (rasterizer_ft.c:158-233).
func rasterizeBgr32(yy int, spans []ftSpan) {
	pm := curRP.pm
	rgba := uint32(curRP.color)
	a0 := int(uint8(curRP.color >> 24))
	b0 := int((curRP.color >> 16) & 0xff)
	g0 := int((curRP.color >> 8) & 0xff)
	r0 := int(curRP.color & 0xff)

	y := yy
	if y < 0 || y >= pm.Height {
		return
	}

	d := pmPixelOff(pm, 0, y)

	for s := range len(spans) {
		x := curRP.left + int(spans[s].x)
		l := int(spans[s].len)
		if x < 0 {
			l += x
			x = 0
		}
		if x+l >= pm.Width {
			l = pm.Width - x
		}

		if spans[s].coverage == 0xff && a0 == 0xff {
			dd := d + x*4
			for range l {
				pm.Data[dd+0] = byte(rgba)
				pm.Data[dd+1] = byte(rgba >> 8)
				pm.Data[dd+2] = byte(rgba >> 16)
				pm.Data[dd+3] = byte(rgba >> 24)
				dd += 4
			}
		} else {
			dst := d + x*4
			SA0 := div255(int(spans[s].coverage) * a0)
			for range l {

				SA := SA0
				SR := r0
				SG := g0
				SB := b0

				u32 := uint32(pm.Data[dst]) | uint32(pm.Data[dst+1])<<8 |
					uint32(pm.Data[dst+2])<<16 | uint32(pm.Data[dst+3])<<24
				DR := int(u32 & 0xff)
				DG := int((u32 >> 8) & 0xff)
				DB := int((u32 >> 16) & 0xff)
				DA := int((u32 >> 24) & 0xff)

				FA := SA + div255((255-SA)*DA)

				if FA == 0 {
					SA = 0
					u32 = 0
				} else {
					if FA != 255 {
						SA = SA * 255 / FA
					}

					DA = 255 - SA

					DB = div255(SB*SA + DB*DA)
					DG = div255(SG*SA + DG*DA)
					DR = div255(SR*SA + DR*DA)

					u32 = uint32(FA)<<24 | uint32(DB)<<16 |
						uint32(DG)<<8 | uint32(DR)
				}
				pm.Data[dst+0] = byte(u32)
				pm.Data[dst+1] = byte(u32 >> 8)
				pm.Data[dst+2] = byte(u32 >> 16)
				pm.Data[dst+3] = byte(u32 >> 24)
				dst += 4
			}
		}
	}
}

// rasterizeIa — C: rasterize_ia (rasterizer_ft.c:235-307).
// __LITTLE_ENDIAN__ build: col is a uint8, u16 = col | coverage<<8.
func rasterizeIa(yy int, spans []ftSpan) {
	pm := curRP.pm
	a0 := int(uint8(curRP.color >> 24))
	i0 := int(curRP.color & 0xff)
	col := uint8(curRP.color) // C: uint8_t col = rp->color (LE)

	y := yy
	if y < 0 || y >= pm.Height {
		return
	}

	d := pmPixelOff(pm, 0, y)

	for s := range len(spans) {
		x := curRP.left + int(spans[s].x)
		l := int(spans[s].len)
		if x < 0 {
			l += x
			x = 0
		}
		if x+l >= pm.Width {
			l = pm.Width - x
		}

		if spans[s].coverage == 0xff && a0 == 0xff {
			dd := d + x*2
			u16 := uint16(col) | uint16(spans[s].coverage)<<8
			for range l {
				pm.Data[dd+0] = byte(u16)
				pm.Data[dd+1] = byte(u16 >> 8)
				dd += 2
			}
		} else {
			dst := d + x*2

			yv := fixMul(a0, int(spans[s].coverage))

			for range l {
				i := int(pm.Data[dst])
				a := int(pm.Data[dst+1])

				pa := a
				a = yv + fixMul(a, 255-yv)

				if a != 0 {
					i = ((fixMul(i0, yv) +
						fix3Mul(i, pa, 255-yv)) * 255) / a
				} else {
					i = 0
				}
				pm.Data[dst] = byte(i)
				pm.Data[dst+1] = byte(a)
				dst += 2
			}
		}
	}
}
