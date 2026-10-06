package image

// Canonical port of src/image/pixmap.{c,h}.
//
// C: pixmap_t — the low-level pixel buffer shared by all image code.
// pm_data is a single plane; pm_width/pm_height INCLUDE the margin
// (pixmap_create adds margin*2); pm_pixel() offsets accesses by
// pm_margin.

import (
	"math"
	"sync/atomic"

	"github.com/czz/movian-go/internal/arch"
)

// C: PIXMAP_ROW_ALIGN (pixmap.h:20-24) — 16 on PPC, 8 elsewhere. Kept at
// 8: GL_UNPACK_ALIGNMENT uses this value to derive the texture row pitch,
// so pm.Stride must stay ceil(w*bpp, 8) exactly like C. The base buffer
// is allocated 16-byte aligned (see PixmapCreate) and swscale call sites
// fall back to a scratch copy when stride/destination are not 16-aligned.
const PixmapRowAlign = 8

// PixmapType — C: pixmap_type_t (pixmap.h:32-41).
type PixmapType int

const (
	PixmapNone  PixmapType = iota // PIXMAP_none
	PixmapNull                    // PIXMAP_NULL
	PixmapBGR32                   // PIXMAP_BGR32 — 32bit, endian-dependent
	PixmapRGB24                   // PIXMAP_RGB24 — packed R,G,B
	PixmapRGBA                    // PIXMAP_RGBA — always R,G,B,A in RAM
	PixmapBGRA                    // PIXMAP_BGRA — always B,G,R,A in RAM
	PixmapIA                      // PIXMAP_IA
	PixmapI                       // PIXMAP_I
)

// Pixmap flags — C: pm_flags bits (pixmap.h:61-62).
const (
	PixmapVflip  = 0x1 // PIXMAP_VFLIP — pixmap is flipped vertically
	PixmapOpaque = 0x2 // PIXMAP_OPAQUE — no transparency at all
)

// PIXMAP_CORNER_ selects which corners to actually carve out
// (pixmap.h:96-102).
const (
	PixmapCornerTopLeft     = 0x1 // PIXMAP_CORNER_TOPLEFT
	PixmapCornerTopRight    = 0x2 // PIXMAP_CORNER_TOPRIGHT
	PixmapCornerBottomLeft  = 0x4 // PIXMAP_CORNER_BOTTOMLEFT
	PixmapCornerBottomRight = 0x8 // PIXMAP_CORNER_BOTTOMRIGHT
)

// Pixmap — C: pixmap_t (pixmap.h:46-66).
type Pixmap struct {
	Data     []byte // C: uint8_t *pm_data
	refcount int32  // C: atomic_t pm_refcount

	Aspect float32    // C: pm_aspect
	Type   PixmapType // C: pm_type
	Stride int        // C: pm_linesize

	Width  int // C: pm_width (includes margin)
	Height int // C: pm_height (includes margin)
	Margin int // C: pm_margin
	Flags  int // C: pm_flags

	Intensity    float32    // C: pm_intensity
	PrimaryColor [3]float32 // C: pm_primary_color[3]
}

// bytesPerPixel — C: bytes_per_pixel (pixmap.h:121-143).
func bytesPerPixel(t PixmapType) int {
	switch t {
	case PixmapBGR32, PixmapRGBA, PixmapBGRA:
		return 4
	case PixmapRGB24:
		return 3
	case PixmapIA:
		return 2
	case PixmapI:
		return 1
	default:
		return 0
	}
}

// pmPixelOff — C: pm_pixel (pixmap.h:145-151). Byte offset of pixel
// (x,y) INSIDE the margin-free view: data + (y+margin)*linesize +
// (x+margin)*bpp.
func pmPixelOff(pm *Pixmap, x, y int) int {
	return (y+pm.Margin)*pm.Stride + (x+pm.Margin)*bytesPerPixel(pm.Type)
}

// div255 — C: DIV255 (pixmap.c:36).
func div255(x int) int {
	return ((((x) + 255) >> 8) + x) >> 8
}

// ColorIsNotGray — C: color_is_not_gray (pixmap.c:41-47).
func ColorIsNotGray(rgb uint32) bool {
	r := uint8(rgb)
	g := uint8(rgb >> 8)
	b := uint8(rgb >> 16)
	return r != g || g != b
}

// PixmapDup — C: pixmap_dup (pixmap.c:55-59).
func PixmapDup(pm *Pixmap) *Pixmap {
	atomic.AddInt32(&pm.refcount, 1)
	return pm
}

// PixmapCreate — C: pixmap_create (pixmap.c:66-98).
// Signature follows C: (width, height, type, margin).
func PixmapCreate(width, height int, ptype PixmapType, margin int) *Pixmap {
	bpp := bytesPerPixel(ptype)
	const rowalign = PixmapRowAlign - 1

	pm := &Pixmap{refcount: 1}
	pm.Width = width + margin*2
	pm.Height = height + margin*2
	pm.Stride = ((pm.Width * bpp) + rowalign) &^ rowalign
	pm.Type = ptype
	pm.Margin = margin

	if pm.Stride > 0 {
		// C: mymemalign(PIXMAP_ROW_ALIGN, linesize*height + 8) —
		// swscale can write a bit after the buffer in its optimized
		// algo therefore we need to allocate a bit extra. The buffer
		// is zeroed like C's memset. We request 16-byte alignment
		// (superset of C's 8) so the swscale destination pointer is
		// aligned for SIMD paths whenever margin == 0.
		pm.Data = arch.MyMemalign(16,
			pm.Stride*pm.Height+8)
	}

	// C: pm->pm_aspect = (float)width / (float)height — unmargined dims.
	pm.Aspect = float32(width) / float32(height)
	return pm
}

// PixmapRelease — C: pixmap_release (pixmap.c:103-110).
func PixmapRelease(pm *Pixmap) {
	if pm == nil {
		return
	}
	if atomic.AddInt32(&pm.refcount, -1) != 0 {
		return
	}
	pm.Data = nil
}

// horizontalGradientRGB24 — C: horizontal_gradient_rgb24
// (pixmap.c:116-150).
func horizontalGradientRGB24(pm *Pixmap, top, bottom []int) {
	h := pm.Height - pm.Margin*2
	w := pm.Width - pm.Margin*2
	var X, Y, Z, T uint32 = 123456789, 362436069, 521288629, 0

	for y := range h {
		d := pmPixelOff(pm, 0, y)

		r := 255*top[0] + (255 * (bottom[0] - top[0]) * y / h)
		g := 255*top[1] + (255 * (bottom[1] - top[1]) * y / h)
		b := 255*top[2] + (255 * (bottom[2] - top[2]) * y / h)
		for range w {
			// Marsaglia's xorshf generator
			X ^= X << 16
			X ^= X >> 5
			X ^= X << 1

			T = X
			X = Y
			Y = Z
			Z = T ^ X ^ Y
			pm.Data[d] = uint8((r + int(Z&0xff)) >> 8)
			d++
			pm.Data[d] = uint8((g + int(Z&0xff)) >> 8)
			d++
			pm.Data[d] = uint8((b + int(Z&0xff)) >> 8)
			d++
		}
	}
}

// horizontalGradientBGR32 — C: horizontal_gradient_bgr32
// (pixmap.c:156-190).
func horizontalGradientBGR32(pm *Pixmap, top, bottom []int) {
	h := pm.Height - pm.Margin*2
	w := pm.Width - pm.Margin*2
	var X, Y, Z, T uint32 = 123456789, 362436069, 521288629, 0

	for y := range h {
		d := pmPixelOff(pm, 0, y)

		r := 255*top[0] + (255 * (bottom[0] - top[0]) * y / h)
		g := 255*top[1] + (255 * (bottom[1] - top[1]) * y / h)
		b := 255*top[2] + (255 * (bottom[2] - top[2]) * y / h)
		for range w {
			// Marsaglia's xorshf generator
			X ^= X << 16
			X ^= X >> 5
			X ^= X << 1

			T = X
			X = Y
			Y = Z
			Z = T ^ X ^ Y
			R := uint8((r + int(Z&0xff)) >> 8)
			G := uint8((g + int(Z&0xff)) >> 8)
			B := uint8((b + int(Z&0xff)) >> 8)
			u32 := uint32(0xff000000) | uint32(B)<<16 | uint32(G)<<8 | uint32(R)
			pm.Data[d+0] = byte(u32)
			pm.Data[d+1] = byte(u32 >> 8)
			pm.Data[d+2] = byte(u32 >> 16)
			pm.Data[d+3] = byte(u32 >> 24)
			d += 4
		}
	}
}

// PixmapHorizontalGradient — C: pixmap_horizontal_gradient
// (pixmap.c:195-208).
func PixmapHorizontalGradient(pm *Pixmap, top, bottom []int) {
	switch pm.Type {
	case PixmapRGB24:
		horizontalGradientRGB24(pm, top, bottom)
	case PixmapBGR32:
		horizontalGradientBGR32(pm, top, bottom)
	}
}

// rgb24ToBGR32 — C: rgb24_to_bgr32 (pixmap.c:214-233).
func rgb24ToBGR32(src *Pixmap) *Pixmap {
	dst := PixmapCreate(src.Width, src.Height, PixmapBGR32, src.Margin)

	for y := range src.Height {
		s := y * src.Stride
		d := y * dst.Stride
		for range src.Width {
			u32 := uint32(0xff000000) | uint32(src.Data[s+2])<<16 |
				uint32(src.Data[s+1])<<8 | uint32(src.Data[s+0])
			dst.Data[d+0] = byte(u32)
			dst.Data[d+1] = byte(u32 >> 8)
			dst.Data[d+2] = byte(u32 >> 16)
			dst.Data[d+3] = byte(u32 >> 24)
			s += 3
			d += 4
		}
	}
	return dst
}

// PixmapRoundedCorners — C: pixmap_rounded_corners (pixmap.c:239-299).
func PixmapRoundedCorners(pm *Pixmap, r, which int) *Pixmap {
	switch pm.Type {
	default:
		return pm

	case PixmapBGR32:

	case PixmapRGB24:
		tmp := rgb24ToBGR32(pm)
		PixmapRelease(pm)
		pm = tmp
	}

	r = min(pm.Height/2, r)

	r2 := r * r
	for i := range r {
		x := float64(r) - math.Sqrt(float64(r2-i*i))
		length := int(x)
		alpha := 255 - int((x-float64(length))*255)
		y := r - i - 1

		dst := pmPixelOff(pm, 0, y)

		if which&PixmapCornerTopLeft != 0 {
			for j := range length * 4 {
				pm.Data[dst+j] = 0
			}
			o := dst + length*4
			u32 := uint32(pm.Data[o]) | uint32(pm.Data[o+1])<<8 |
				uint32(pm.Data[o+2])<<16 | uint32(alpha)<<24
			pm.Data[o+0] = byte(u32)
			pm.Data[o+1] = byte(u32 >> 8)
			pm.Data[o+2] = byte(u32 >> 16)
			pm.Data[o+3] = byte(u32 >> 24)
		}

		if which&PixmapCornerTopRight != 0 {
			dst += (pm.Width - pm.Margin*2) * 4
			for j := range length * 4 {
				pm.Data[dst-length*4+j] = 0
			}
			o := dst - length*4 - 4
			u32 := uint32(pm.Data[o]) | uint32(pm.Data[o+1])<<8 |
				uint32(pm.Data[o+2])<<16 | uint32(alpha)<<24
			pm.Data[o+0] = byte(u32)
			pm.Data[o+1] = byte(u32 >> 8)
			pm.Data[o+2] = byte(u32 >> 16)
			pm.Data[o+3] = byte(u32 >> 24)
		}

		dst = pmPixelOff(pm, 0, pm.Height-1-y-pm.Margin*2)

		if which&PixmapCornerBottomLeft != 0 {
			for j := range length * 4 {
				pm.Data[dst+j] = 0
			}
			o := dst + length*4
			u32 := uint32(pm.Data[o]) | uint32(pm.Data[o+1])<<8 |
				uint32(pm.Data[o+2])<<16 | uint32(alpha)<<24
			pm.Data[o+0] = byte(u32)
			pm.Data[o+1] = byte(u32 >> 8)
			pm.Data[o+2] = byte(u32 >> 16)
			pm.Data[o+3] = byte(u32 >> 24)
		}

		if which&PixmapCornerBottomRight != 0 {
			dst += (pm.Width - pm.Margin*2) * 4
			for j := range length * 4 {
				pm.Data[dst-length*4+j] = 0
			}
			o := dst - length*4 - 4
			u32 := uint32(pm.Data[o]) | uint32(pm.Data[o+1])<<8 |
				uint32(pm.Data[o+2])<<16 | uint32(alpha)<<24
			pm.Data[o+0] = byte(u32)
			pm.Data[o+1] = byte(u32 >> 8)
			pm.Data[o+2] = byte(u32 >> 16)
			pm.Data[o+3] = byte(u32 >> 24)
		}
	}
	return pm
}

// fixMul — C: FIXMUL (pixmap.c:301).
func fixMul(a, b int) int {
	return (a*b + 255) >> 8
}

// fix3Mul — C: FIX3MUL (pixmap.c:302).
func fix3Mul(a, b, c int) int {
	return (a*b*c + 65535) >> 16
}

// compositeGRAY8OnIA — C: composite_GRAY8_on_IA (pixmap.c:307-337).
// dst/src are byte offsets into the pixmaps.
func compositeGRAY8OnIA(d *Pixmap, dst int, s *Pixmap, src int,
	i0, foo, bar, a0, width int) {
	for range width {
		if s.Data[src] != 0 {
			i := int(d.Data[dst])
			a := int(d.Data[dst+1])

			pa := a
			y := fixMul(a0, int(s.Data[src]))
			a = y + fixMul(a, 255-y)

			if a != 0 {
				i = ((fixMul(i0, y) + fix3Mul(i, pa, (255-y))) * 255) / a
			} else {
				i = 0
			}
			d.Data[dst] = byte(i)
			d.Data[dst+1] = byte(a)
		}
		src++
		dst += 2
	}
}

// compositeGRAY8OnIAFullAlpha — C: composite_GRAY8_on_IA_full_alpha
// (pixmap.c:342-371).
func compositeGRAY8OnIAFullAlpha(d *Pixmap, dst int, s *Pixmap, src int,
	i0, b0, g0, a0 int, width int) {
	for range width {
		if s.Data[src] == 255 {
			d.Data[dst] = byte(i0)
			d.Data[dst+1] = 255
		} else if s.Data[src] != 0 {
			i := int(d.Data[dst])
			a := int(d.Data[dst+1])

			pa := a
			y := int(s.Data[src])
			a = y + fixMul(a, 255-y)

			if a != 0 {
				i = ((fixMul(i0, y) + fix3Mul(i, pa, (255-y))) * 255) / a
			} else {
				i = 0
			}
			d.Data[dst] = byte(i)
			d.Data[dst+1] = byte(a)
		}
		src++
		dst += 2
	}
}

// compositeGRAY8OnBGR32 — C: composite_GRAY8_on_BGR32, the ACTIVE #else
// branch (pixmap.c:413-463) — DIV255 premultiplied blend. (The #if 0
// FIXMUL variant is dead upstream and not ported.)
func compositeGRAY8OnBGR32(d *Pixmap, dst int, s *Pixmap, src int,
	CR, CG, CB, CA, width int) {
	for range width {

		SA := div255(int(s.Data[src]) * CA)
		SR := CR
		SG := CG
		SB := CB

		u32 := uint32(d.Data[dst]) | uint32(d.Data[dst+1])<<8 |
			uint32(d.Data[dst+2])<<16 | uint32(d.Data[dst+3])<<24

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

			u32 = uint32(FA)<<24 | uint32(DB)<<16 | uint32(DG)<<8 | uint32(DR)
		}
		d.Data[dst+0] = byte(u32)
		d.Data[dst+1] = byte(u32 >> 8)
		d.Data[dst+2] = byte(u32 >> 16)
		d.Data[dst+3] = byte(u32 >> 24)

		src++
		dst += 4
	}
}

// PixmapComposite — C: pixmap_composite (pixmap.c:468-521).
func PixmapComposite(dst, src *Pixmap, xdisp, ydisp int, rgba uint32) {
	var fn func(d *Pixmap, doff int, s *Pixmap, soff int,
		r, g, b, a, width int)

	r := int(rgba & 0xff)
	g := int((rgba >> 8) & 0xff)
	b := int((rgba >> 16) & 0xff)
	a := int((rgba >> 24) & 0xff)

	switch {
	case src.Type == PixmapI && dst.Type == PixmapIA && a == 255:
		fn = compositeGRAY8OnIAFullAlpha
	case src.Type == PixmapI && dst.Type == PixmapIA:
		fn = compositeGRAY8OnIA
	case src.Type == PixmapI && dst.Type == PixmapBGR32:
		fn = compositeGRAY8OnBGR32
	default:
		return
	}

	readstep := bytesPerPixel(src.Type)
	writestep := bytesPerPixel(dst.Type)

	s0 := 0 // C: const uint8_t *s0 = src->pm_data — offset
	d0 := 0 // C: uint8_t *d0 = dst->pm_data

	xx := src.Width

	if xdisp < 0 {
		// Painting left of dst image
		s0 += readstep * (-xdisp)
		xx += xdisp
		xdisp = 0
	} else if xdisp > 0 {
		d0 += writestep * xdisp
	}

	if xx+xdisp > dst.Width {
		xx = dst.Width - xdisp
	}

	for y := range src.Height {
		wy := y + ydisp
		if wy >= 0 && wy < dst.Height {
			fn(dst, d0+wy*dst.Stride, src, s0+y*src.Stride,
				r, g, b, a, xx)
		}
	}
}

// boxBlurLine2Chan — C: box_blur_line_2chan (pixmap.c:527-559).
// a/b are prefix-sum accumulators indexed by BYTE offset (uint32 per
// source byte), d is the byte offset into the pixmap row.
func boxBlurLine2Chan(d *Pixmap, doff int, a, b []uint32, aoff, boff,
	width, boxw, m int) {
	x := 0
	for x < boxw {
		x1 := 2 * min(x+boxw, width-1)
		x2 := 0

		v := int(b[boff+x1+0]) + int(a[aoff+x2+0]) - int(b[boff+x2+0]) - int(a[aoff+x1+0])
		d.Data[doff] = byte((v * m) >> 16)
		doff++
		v = int(b[boff+x1+1]) + int(a[aoff+x2+1]) - int(b[boff+x2+1]) - int(a[aoff+x1+1])
		d.Data[doff] = byte((v * m) >> 16)
		doff++
		x++
	}

	for x < width-boxw {
		x1 := 2 * (x + boxw)
		x2 := 2 * (x - boxw)

		v := int(b[boff+x1+0]) + int(a[aoff+x2+0]) - int(b[boff+x2+0]) - int(a[aoff+x1+0])
		d.Data[doff] = byte((v * m) >> 16)
		doff++
		v = int(b[boff+x1+1]) + int(a[aoff+x2+1]) - int(b[boff+x2+1]) - int(a[aoff+x1+1])
		d.Data[doff] = byte((v * m) >> 16)
		doff++
		x++
	}

	for x < width {
		x1 := 2 * (width - 1)
		x2 := 2 * (x - boxw)

		v := int(b[boff+x1+0]) + int(a[aoff+x2+0]) - int(b[boff+x2+0]) - int(a[aoff+x1+0])
		d.Data[doff] = byte((v * m) >> 16)
		doff++
		v = int(b[boff+x1+1]) + int(a[aoff+x2+1]) - int(b[boff+x2+1]) - int(a[aoff+x1+1])
		d.Data[doff] = byte((v * m) >> 16)
		doff++
		x++
	}
}

// boxBlurLine4Chan — C: box_blur_line_4chan (pixmap.c:564-613).
func boxBlurLine4Chan(d *Pixmap, doff int, a, b []uint32, aoff, boff,
	width, boxw, m int) {
	x := 0
	for x < boxw {
		x1 := 4 * min(x+boxw, width-1)
		x2 := 0
		for c := range 4 {
			v := int(b[boff+x1+c]) + int(a[aoff+x2+c]) - int(b[boff+x2+c]) - int(a[aoff+x1+c])
			d.Data[doff] = byte((v * m) >> 16)
			doff++
		}
		x++
	}

	for x < width-boxw {
		x1 := 4 * (x + boxw)
		x2 := 4 * (x - boxw)
		for c := range 4 {
			v := int(b[boff+x1+c]) + int(a[aoff+x2+c]) - int(b[boff+x2+c]) - int(a[aoff+x1+c])
			d.Data[doff] = byte((v * m) >> 16)
			doff++
		}
		x++
	}

	for x < width {
		x1 := 4 * (width - 1)
		x2 := 4 * (x - boxw)
		for c := range 4 {
			v := int(b[boff+x1+c]) + int(a[aoff+x2+c]) - int(b[boff+x2+c]) - int(a[aoff+x1+c])
			d.Data[doff] = byte((v * m) >> 16)
			doff++
		}
		x++
	}
}

// PixmapBoxBlur — C: pixmap_box_blur (pixmap.c:618-692).
// tmp is a uint32 prefix-sum plane indexed by BYTE offset like C.
func PixmapBoxBlur(pm *Pixmap, boxw, boxh int) {
	w := pm.Width
	h := pm.Height
	ls := pm.Stride
	z := bytesPerPixel(pm.Type)

	boxw = min(boxw, w)

	var fn func(d *Pixmap, doff int, a, b []uint32, aoff, boff,
		width, boxw, m int)

	switch z {
	case 2:
		fn = boxBlurLine2Chan
	case 4:
		fn = boxBlurLine4Chan
	default:
		return
	}

	// C: tmp = mymalloc(ls * h * sizeof(unsigned int)) — ls*h uint32s
	// indexed by byte offset (prefix sums per channel byte).
	tmp := make([]uint32, ls*h)

	s := 0
	t := 0

	for range z {
		tmp[t] = uint32(pm.Data[s])
		s++
		t++
	}

	for range (w - 1) * z {
		tmp[t] = uint32(pm.Data[s]) + tmp[t-z]
		s++
		t++
	}

	for y := 1; y < h; y++ {
		s = y * ls
		t = y * ls

		for range z {
			tmp[t] = uint32(pm.Data[s]) + tmp[t-ls]
			s++
			t++
		}

		for range (w - 1) * z {
			tmp[t] = uint32(pm.Data[s]) + tmp[t-z] + tmp[t-ls] - tmp[t-ls-z]
			s++
			t++
		}
	}

	m := 65536 / ((boxw*2 + 1) * (boxh*2 + 1))

	for y := range h {
		d := y * ls
		aIdx := ls * max(0, y-boxh)
		bIdx := ls * min(h-1, y+boxh)
		fn(pm, d, tmp, tmp, aIdx, bIdx, w, boxw, m)
	}
}

// mixBGR32 — C: mix_bgr32 (pixmap.c:699-730).
func mixBGR32(src, dst uint32) uint32 {
	SR := int(src & 0xff)
	SG := int((src >> 8) & 0xff)
	SB := int((src >> 16) & 0xff)
	SA := int((src >> 24) & 0xff)

	DR := int(dst & 0xff)
	DG := int((dst >> 8) & 0xff)
	DB := int((dst >> 16) & 0xff)
	DA := int((dst >> 24) & 0xff)

	FA := SA + div255((255-SA)*DA)

	if FA == 0 {
		return 0
	}
	if FA != 255 {
		SA = SA * 255 / FA
	}

	DA = 255 - SA

	DB = div255(SB*SA + DB*DA)
	DG = div255(SG*SA + DG*DA)
	DR = div255(SR*SA + DR*DA)

	return uint32(FA)<<24 | uint32(DB)<<16 | uint32(DG)<<8 | uint32(DR)
}

// dropShadowRGBA — C: drop_shadow_rgba (pixmap.c:736-776).
func dropShadowRGBA(d *Pixmap, doff int, a, b []uint32, aoff, boff,
	width, boxw, m int) {
	x := 0
	for x < boxw {
		x1 := min(x+boxw, width-1)
		x2 := 0

		v := int(b[boff+x1]) + int(a[aoff+x2]) - int(b[boff+x2]) - int(a[aoff+x1])
		s := (v * m) >> 16

		u32 := uint32(d.Data[doff]) | uint32(d.Data[doff+1])<<8 |
			uint32(d.Data[doff+2])<<16 | uint32(d.Data[doff+3])<<24
		u32 = mixBGR32(u32, uint32(s)<<24)
		d.Data[doff+0] = byte(u32)
		d.Data[doff+1] = byte(u32 >> 8)
		d.Data[doff+2] = byte(u32 >> 16)
		d.Data[doff+3] = byte(u32 >> 24)
		doff += 4
		x++
	}

	for x < width-boxw {
		x1 := x + boxw
		x2 := x - boxw

		v := int(b[boff+x1]) + int(a[aoff+x2]) - int(b[boff+x2]) - int(a[aoff+x1])
		s := (v * m) >> 16
		u32 := uint32(d.Data[doff]) | uint32(d.Data[doff+1])<<8 |
			uint32(d.Data[doff+2])<<16 | uint32(d.Data[doff+3])<<24
		u32 = mixBGR32(u32, uint32(s)<<24)
		d.Data[doff+0] = byte(u32)
		d.Data[doff+1] = byte(u32 >> 8)
		d.Data[doff+2] = byte(u32 >> 16)
		d.Data[doff+3] = byte(u32 >> 24)
		doff += 4
		x++
	}

	for x < width {
		x1 := width - 1
		x2 := x - boxw

		v := int(b[boff+x1]) + int(a[aoff+x2]) - int(b[boff+x2]) - int(a[aoff+x1])
		s := (v * m) >> 16
		u32 := uint32(d.Data[doff]) | uint32(d.Data[doff+1])<<8 |
			uint32(d.Data[doff+2])<<16 | uint32(d.Data[doff+3])<<24
		u32 = mixBGR32(u32, uint32(s)<<24)
		d.Data[doff+0] = byte(u32)
		d.Data[doff+1] = byte(u32 >> 8)
		d.Data[doff+2] = byte(u32 >> 16)
		d.Data[doff+3] = byte(u32 >> 24)
		doff += 4
		x++
	}
}

// mixIA — C: mix_ia (pixmap.c:783-807). src/dst are byte offsets into
// the same pixmap (mix_ia(d, d, ...) in C).
func mixIA(s *Pixmap, src int, d *Pixmap, dst int, DR, DA int) {
	SR := int(s.Data[src])
	SA := int(s.Data[src+1])

	FA := SA + div255((255-SA)*DA)

	if FA == 0 {
		d.Data[dst] = 0
		d.Data[dst+1] = 0
	} else {
		if FA != 255 {
			SA = SA * 255 / FA
		}

		DA = 255 - SA

		DR = div255(SR*SA + DR*DA)
		d.Data[dst] = byte(DR)
		d.Data[dst+1] = byte(FA)
	}
}

// dropShadowIA — C: drop_shadow_ia (pixmap.c:812-848).
func dropShadowIA(d *Pixmap, doff int, a, b []uint32, aoff, boff,
	width, boxw, m int) {
	x := 0
	for x < boxw {
		x1 := 2 * min(x+boxw, width-1)
		x2 := 0

		v := int(b[boff+x1]) + int(a[aoff+x2]) - int(b[boff+x2]) - int(a[aoff+x1])
		s := (v * m) >> 16

		mixIA(d, doff, d, doff, 0, s)
		doff += 2
		x++
	}

	for x < width-boxw {
		x1 := 2 * (x + boxw)
		x2 := 2 * (x - boxw)

		v := int(b[boff+x1]) + int(a[aoff+x2]) - int(b[boff+x2]) - int(a[aoff+x1])
		s := (v * m) >> 16
		mixIA(d, doff, d, doff, 0, s)
		doff += 2
		x++
	}

	for x < width {
		x1 := 2 * (width - 1)
		x2 := 2 * (x - boxw)

		v := int(b[boff+x1]) + int(a[aoff+x2]) - int(b[boff+x2]) - int(a[aoff+x1])
		s := (v * m) >> 16
		mixIA(d, doff, d, doff, 0, s)
		doff += 2
		x++
	}
}

// PixmapDropShadow — C: pixmap_drop_shadow (pixmap.c:853-924).
func PixmapDropShadow(pm *Pixmap, boxw, boxh int) {
	var ach int // Alpha channel
	var z int

	w := pm.Width
	h := pm.Height
	ls := pm.Stride

	if boxw <= 0 || boxh <= 0 {
		panic("pixmap_drop_shadow: boxw/boxh must be > 0") // C: assert
	}

	boxw = min(boxw, w)

	var fn func(d *Pixmap, doff int, a, b []uint32, aoff, boff,
		width, boxw, m int)

	switch pm.Type {
	case PixmapBGR32:
		ach = 3
		z = 4
		fn = dropShadowRGBA
	case PixmapIA:
		ach = 1
		z = 2
		fn = dropShadowIA
	default:
		return
	}

	// C: tmp = mymalloc(w * h * sizeof(unsigned int)) — one uint32
	// alpha-accumulator per PIXEL (indexed by pixel, not byte).
	tmp := make([]uint32, w*h)

	s := ach
	t := 0

	y := 0
	for ; y < boxh; y++ {
		for range w {
			tmp[t] = 0
			t++
		}
	}

	for ; y < h; y++ {
		s = (y-boxh)*ls + ach
		x := 0
		for ; x < boxw; x++ {
			tmp[t] = 0
			t++
		}

		for ; x < w; x++ {
			tmp[t] = uint32(pm.Data[s]) + tmp[t-1] + tmp[t-w] - tmp[t-w-1]
			s += z
			t++
		}
	}

	m := 65536 / ((boxw*2 + 1) * (boxh*2 + 1))

	for y := range h {
		d := y * ls

		aIdx := w * max(0, y-boxh)
		bIdx := w * min(h-1, y+boxh)
		fn(pm, d, tmp, tmp, aIdx, bIdx, w, boxw, m)
	}
}

// PixmapIntensityAnalysis — C: pixmap_intensity_analysis
// (pixmap.c:978-1025) — 95th-percentile histogram variant (the #if 0
// mean-based variant is dead upstream).
func PixmapIntensityAnalysis(pm *Pixmap) {
	var bin [256]int

	switch pm.Type {
	case PixmapRGB24:
		for y := range pm.Height {
			src := pmPixelOff(pm, 0, y)
			for range pm.Width {
				v := int(pm.Data[src]) + int(pm.Data[src+1]) + int(pm.Data[src+2])
				bin[v/3]++
				src += 3
			}
		}

	case PixmapBGR32:
		for y := range pm.Height {
			src := pmPixelOff(pm, 0, y)
			for range pm.Width {
				u32 := uint32(pm.Data[src]) | uint32(pm.Data[src+1])<<8 |
					uint32(pm.Data[src+2])<<16 | uint32(pm.Data[src+3])<<24
				r := u32 & 0xff
				g := (u32 >> 8) & 0xff
				b := (u32 >> 16) & 0xff
				v := r + g + b
				bin[v/3]++
				src += 4
			}
		}

	default:
		// C: printf("Cant do intensity analysis for pixfmt %d\n")
	}

	pixels := pm.Width * pm.Height
	limit := int(float64(pixels) * 0.95)
	i := 255
	for ; i >= 0; i-- {
		pixels -= bin[i]
		if pixels < limit {
			break
		}
	}
	pm.Intensity = float32(i) / 255.0
}
