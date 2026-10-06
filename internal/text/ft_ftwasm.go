package text

// Pure-Go FreeType backend — wasm2go-translated FreeType 2.13.3
// (internal/ftwasm): the vendored C source compiled once to wasm32
// and translated to Go, so it runs on every GOOS/GOARCH with
// identical rendering.
//
// wasm32 is ILP32: FT_Pos/FT_Long are 32-bit, FT_BBox/FT_Vector
// scratch cells are decoded as little-endian int32s. Handle+error
// returns are packed u64: (err<<32)|ptr.

import (
	"encoding/binary"
	"io"
	"runtime/cgo"

	facore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/ftwasm"
)

// ftwMod — the module instance owning this package's FT_Library.
// C: static FT_Library ft_lib (freetype.c) — the module stands in
// for the linked C object; set by ftInitFreeType (INITME ordering).
var ftwMod *ftwasm.Module

const (
	ftEncUnicode         = 0x756e6963 // FT_ENC_TAG('u','n','i','c')
	ftLoadForceAutohint  = 1 << 5     // FT_LOAD_FORCE_AUTOHINT
	ftLoadColor          = 1 << 20    // FT_LOAD_COLOR
	ftGlyphFormatOutline = 0x6f75746c // FT_IMAGE_TAG('o','u','t','l')
	ftGlyphFormatBitmap  = 0x62697473 // FT_IMAGE_TAG('b','i','t','s')
	ftPixelModeBGRA      = 7          // FT_PIXEL_MODE_BGRA
	ftBBoxGridfit        = 1          // FT_GLYPH_BBOX_GRIDFIT
	ftRenderNormal       = 0          // FT_RENDER_MODE_NORMAL
	ftKerningDefault     = 0          // FT_KERNING_DEFAULT
	ftLinecapRound       = 1          // FT_STROKER_LINECAP_ROUND
	ftLinejoinRound      = 0          // FT_STROKER_LINEJOIN_ROUND
)

// ftwHost — w2g imports for the text module: face stream I/O.
// GraySpans is never reached (text renders via FT_Glyph_To_Bitmap).
type ftwHost struct{}

// C: static unsigned long face_read (freetype.c:243).
// count==0 means seek; the stream descriptor carries a cgo.Handle
// to *facore.Handle exactly like the cgo backend.
func (ftwHost) FaceRead(fh int64, offset, buffer, count int32) int32 {
	h := cgo.Handle(fh)
	f := h.Value().(*facore.Handle)
	if count == 0 {
		// C: return fa_seek(stream->descriptor.pointer, offset, SEEK_SET) < 0
		n, err := facore.FASeek(f, int64(offset), io.SeekStart)
		if n < 0 || err != nil {
			return 1
		}
		return 0
	}
	// C: return fa_read(stream->descriptor.pointer, buffer, count)
	n, err := facore.FARead(f, ftwasm.Mem(ftwMod)[buffer:buffer+count])
	if err != nil || n < 0 {
		return 0
	}
	return int32(n)
}

// C: static void face_close (freetype.c:255)
func (ftwHost) FaceClose(fh int64) {
	h := cgo.Handle(fh)
	f := h.Value().(*facore.Handle)
	facore.FAClose(f)
	h.Delete()
}

func (ftwHost) GraySpans(_, _, _ int32, _ int64) {}

// ftwCStr reads a NUL-terminated string from module memory.
func ftwCStr(p int32) string {
	mem := ftwasm.Mem(ftwMod)
	end := p
	for mem[end] != 0 {
		end++
	}
	return string(mem[p:end])
}

// C: FT_Init_FreeType (freetype.c:1503)
func ftInitFreeType(out *ftLibrary) int {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	m := ftwasm.NewModule(ftwHost{})
	r := m.Xftw_init()
	if e := int32(r >> 32); e != 0 {
		return int(e)
	}
	ftwMod = m
	*out = ftLibrary(uint32(r))
	return 0
}

// C: FT_Stroker_New (freetype.c:1513)
func ftStrokerNew(lib ftLibrary, out *ftStroker) int {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	r := ftwMod.Xftw_stroker_create(int32(lib))
	*out = ftStroker(uint32(r))
	return int(int32(r >> 32))
}

// C: FT_Stroker_Set (freetype.c:1073)
func ftStrokerSet(s ftStroker, radius ftPos, cap, join uint32, miter ftPos) {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	ftwMod.Xftw_stroker_set(int32(s), radius, int32(cap), int32(join), miter)
}

// C: FT_Select_Charmap (freetype.c:317)
func ftSelectCharmap(f ftFace, enc uint32) int {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	return int(ftwMod.Xftw_select_charmap(int32(f), int32(enc)))
}

// C: FT_Get_Char_Index (freetype.c:437 etc)
func ftGetCharIndex(f ftFace, uc int) uint32 {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	return uint32(ftwMod.Xftw_get_char_index(int32(f), int32(uc)))
}

// C: FT_Load_Glyph (freetype.c:609)
func ftLoadGlyph(f ftFace, gi uint32, flags int32) int {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	return int(ftwMod.Xftw_load_glyph(int32(f), int32(gi), flags))
}

// C: face->glyph
func ftFaceGlyph(f ftFace) ftGlyphSlot {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	return ftGlyphSlot(uint32(ftwMod.Xftw_face_glyph(int32(f))))
}

// C: FT_GlyphSlot_Oblique (freetype.c:613)
func ftGlyphSlotOblique(s ftGlyphSlot) {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	ftwMod.Xftw_slot_oblique(int32(s))
}

// C: FT_MulFix (freetype.c:621)
func ftMulFix(a, b ftPos) ftPos {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	return ftPos(ftwMod.Xftw_mulfix(a, b))
}

// C: slot->format / slot->advance.x / slot->face / face->units_per_EM /
// face->size->metrics.y_scale
func ftSlotFormat(s ftGlyphSlot) uint32 {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	return uint32(ftwMod.Xftw_slot_format(int32(s)))
}
func ftSlotAdvanceX(s ftGlyphSlot) ftPos {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	return ftPos(ftwMod.Xftw_slot_advance_x(int32(s)))
}
func ftSlotFace(s ftGlyphSlot) ftFace {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	return ftFace(uint32(ftwMod.Xftw_slot_face(int32(s))))
}
func ftFaceUnitsPerEM(f ftFace) int {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	return int(ftwMod.Xftw_face_upem(int32(f)))
}
func ftFaceYScale(f ftFace) ftPos {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	return ftPos(int64(ftwMod.Xftw_face_yscale(int32(f))))
}

// C: FT_Outline_Embolden(&slot->outline, v) (freetype.c:622)
func ftSlotEmboldenOutline(s ftGlyphSlot, v ftPos) {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	ftwMod.Xftw_slot_embolden(int32(s), v)
}

// C: FT_Get_Glyph (freetype.c:625)
func ftGetGlyph(s ftGlyphSlot, out *ftGlyph) int {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	r := ftwMod.Xftw_get_glyph(int32(s))
	*out = ftGlyph(uint32(r))
	return int(int32(r >> 32))
}

// C: FT_Glyph_Get_CBox (freetype.c:631) — bbox via glue scratch cell
// (4 x int32, wasm32 FT_Pos).
func ftGlyphGetCBox(g ftGlyph, mode uint32, out *ftBBox) {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	ftwMod.Xftw_glyph_cbox(int32(g), int32(mode))
	p := ftwMod.Xftw_scratch()
	mem := ftwasm.Mem(ftwMod)
	out.xMin = ftPos(int32(binary.LittleEndian.Uint32(mem[p:])))
	out.yMin = ftPos(int32(binary.LittleEndian.Uint32(mem[p+4:])))
	out.xMax = ftPos(int32(binary.LittleEndian.Uint32(mem[p+8:])))
	out.yMax = ftPos(int32(binary.LittleEndian.Uint32(mem[p+12:])))
}

// C: FT_Glyph_StrokeBorder (freetype.c:1078)
func ftGlyphStrokeBorder(g *ftGlyph, s ftStroker, inside, destroy int) int {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	r := ftwMod.Xftw_glyph_stroke_border(int32(*g), int32(s),
		int32(inside), int32(destroy))
	*g = ftGlyph(uint32(r))
	return int(int32(r >> 32))
}

// C: FT_Glyph_To_Bitmap (freetype.c:1084 / 1100)
func ftGlyphToBitmap(g *ftGlyph, mode int32, origin *ftVec, destroy int) int {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	var r int64
	if origin != nil {
		r = ftwMod.Xftw_glyph_to_bitmap(int32(*g), mode,
			origin.x, origin.y, int32(destroy))
	} else {
		r = ftwMod.Xftw_glyph_to_bitmap0(int32(*g), mode, int32(destroy))
	}
	*g = ftGlyph(uint32(r))
	return int(int32(r >> 32))
}

// C: FT_Get_Kerning (freetype.c:878) — delta via scratch vec (2 x int32).
func ftGetKerning(f ftFace, prev, gi uint32, mode uint32, delta *ftVec) int {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	e := ftwMod.Xftw_get_kerning(int32(f), int32(prev), int32(gi), int32(mode))
	p := ftwMod.Xftw_scratch_vec()
	mem := ftwasm.Mem(ftwMod)
	delta.x = ftPos(int32(binary.LittleEndian.Uint32(mem[p:])))
	delta.y = ftPos(int32(binary.LittleEndian.Uint32(mem[p+4:])))
	return int(e)
}

// C: FT_Done_Glyph / FT_Done_Face
func ftDoneGlyph(g ftGlyph) {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	ftwMod.Xftw_done_glyph(int32(g))
}
func ftDoneFace(f ftFace) {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	ftwMod.Xftw_done_face(int32(f))
}

// C: face->family_name / face->style_name / face->descender
func ftFaceFamilyName(f ftFace) string {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	return ftwCStr(ftwMod.Xftw_face_family(int32(f)))
}
func ftFaceStyleName(f ftFace) string {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	return ftwCStr(ftwMod.Xftw_face_style(int32(f)))
}
func ftFaceDescender(f ftFace) int {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	return int(ftwMod.Xftw_face_descender(int32(f)))
}

// C: ml_ft_request_size — FT_Request_Size with REAL_DIM (freetype.c:565)
func ftRequestSize(f ftFace, size int) {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	ftwMod.Xftw_request_size(int32(f), int32(size))
}

// C: FT_HAS_KERNING(f) macro (freetype.c:649)
func ftHasKerning(f ftFace) int {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	return int(ftwMod.Xftw_has_kerning(int32(f)))
}

// C: ((FT_BitmapGlyph)g)->left/top/&bitmap — ftBitmap carries the
// glyph address; the FT_Bitmap fields are read per-access.
func ftBGLeft(g ftGlyph) int {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	return int(ftwMod.Xftw_bg_left(int32(g)))
}
func ftBGTop(g ftGlyph) int {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	return int(ftwMod.Xftw_bg_top(int32(g)))
}
func ftBGBitmap(g ftGlyph) ftBitmap {
	return ftBitmap(g)
}

// C: FT_Bitmap field access (bmp->width / bmp->rows / bmp->buffer)
func ftBitmapDims(b ftBitmap) (w, h int) {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	return int(ftwMod.Xftw_bmp_width(int32(b))),
		int(ftwMod.Xftw_bmp_rows(int32(b)))
}
func ftBitmapSlice(b ftBitmap) []byte {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	w := ftwMod.Xftw_bmp_width(int32(b))
	rows := ftwMod.Xftw_bmp_rows(int32(b))
	buf := ftwMod.Xftw_bmp_buffer(int32(b))
	// Copy: the wasm heap may be relocated by memory.grow before the
	// pixmap is consumed, so a raw Mem() slice is not stable here.
	px := make([]byte, int(w)*int(rows))
	copy(px, ftwasm.Mem(ftwMod)[buf:])
	return px
}

// Bitmap-only color faces (CBDT emoji) — extension beyond upstream C,
// which crashes on units_per_EM==0. FT_HAS_COLOR / FT_Bitmap fields.
func ftHasColor(f ftFace) int {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	return int(ftwMod.Xftw_has_color(int32(f)))
}
func ftBitmapPitch(b ftBitmap) int {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	return int(ftwMod.Xftw_bmp_pitch(int32(b)))
}
func ftBitmapPixelMode(b ftBitmap) int {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	return int(ftwMod.Xftw_bmp_pixel_mode(int32(b)))
}

// ftBitmapSlicePitch copies pitch*rows bytes — for BGRA bitmaps pitch
// (bytes/row) differs from width (pixels/row).
func ftBitmapSlicePitch(b ftBitmap) []byte {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	pitch := ftwMod.Xftw_bmp_pitch(int32(b))
	rows := ftwMod.Xftw_bmp_rows(int32(b))
	buf := ftwMod.Xftw_bmp_buffer(int32(b))
	n := int(pitch) * int(rows)
	if n < 0 {
		n = -n
	}
	px := make([]byte, n)
	copy(px, ftwasm.Mem(ftwMod)[buf:])
	return px
}

// C: face_create_from_fh's stream open (freetype.c:325-360) —
// stream descriptor carries a cgo.Handle to *facore.Handle.
func ftStreamFace(lib ftLibrary, fh *facore.Handle, size int64, out *ftFace) int {
	ch := cgo.NewHandle(fh)
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	r := ftwMod.Xftw_open_stream(int32(lib), int64(ch), size)
	if e := int32(r >> 32); e != 0 {
		ch.Delete()
		// C: fh is leaked here too (FT_Open_Face failure does not
		// invoke stream->close); mirrored.
		return int(e)
	}
	*out = ftFace(uint32(r))
	return 0
}

// C: face_create_from_mem's C buffer + FT_OPEN_MEMORY (freetype.c:367-380).
func ftMemoryFace(lib ftLibrary, data []byte, out *ftFace) (mem uintptr, e int) {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	p := ftwMod.Xftw_alloc(int64(len(data)))
	copy(ftwasm.Mem(ftwMod)[p:], data)
	r := ftwMod.Xftw_open_memory(int32(lib), p, int64(len(data)))
	if e := int32(r >> 32); e != 0 {
		ftwMod.Xftw_free(p)
		return 0, int(e)
	}
	*out = ftFace(uint32(r))
	return uintptr(p), 0
}

// C: free(face->mem)
func ftFreeMem(p uintptr) {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	ftwMod.Xftw_free(int32(p))
}
