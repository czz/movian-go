package text

// FreeType calls go through the ft* seam (ft.go) — implemented by
// ft_ftwasm.go (wasm2go-translated FreeType on all platforms).
// Handles are wasm32 addresses so this file stays the single
// canonical source.

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"unsafe"

	"github.com/czz/movian-go/internal/app"
	facore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/image"
	miscpkg "github.com/czz/movian-go/internal/misc"
	"github.com/czz/movian-go/internal/trace"
)

// C: src/text/freetype.c — canonical 1:1 port.
// Shares sys.ft.mutex, sys.ft.domainTally, idFromStr, idmapFind with freetype.go.

const horizontalEllipsisUnicode = 0x2026 // C: HORIZONTAL_ELLIPSIS_UNICODE

const glyphHashSize = 128 // C: GLYPH_HASH_SIZE
const glyphHashMask = glyphHashSize - 1

// ft — freetype subsystem state. C file-scope statics of freetype.c
// ftState — C: freetype.c file statics
// (text_mutex, text_library, text_stroker, font_domain_tally,
// idmap_id_tally, idmaps, static_faces, dynamic_faces, glyph_hash,
// allglyphs, num_glyphs). Lives on System.sys.ft.
// System — the text/freetype/fontstash subsystem context. C file
// statics of freetype.c + fontstash.c + fontconfig.c live here.
type System struct {
	ft ftState // C: freetype.c statics
	fs fsState // C: fontstash.c statics
}

// NewSystem — C: the implicit process-wide freetype/fontstash context.
func NewSystem() *System {
	return &System{ft: ftState{domainTally: 10}}
}

type ftState struct {
	mutex       sync.Mutex
	domainTally int // C: font_domain_tally = 10

	// name <-> id map
	idmapTally int       // C: idmap_id_tally
	idmaps     []*idmapT // C: static struct idmap_list idmaps (LIST_HEAD)

	library ftLibrary          // C: static FT_Library text_library
	ts      *trace.TraceSystem // C: trace() global — injected
	stroker ftStroker          // C: static FT_Stroker text_stroker

	staticFaces  []*faceT                 // C: static_faces
	dynamicFaces []*faceT                 // C: dynamic_faces
	glyphHash    [glyphHashSize][]*glyphT // C: glyph_hash[]
	allglyphs    []*glyphT                // C: allglyphs
	numGlyphs    int                      // C: num_glyphs

	fam *facore.FileAccessManager // C: implicit global fa context — injected

	// fc — C: static FcConfig *fc_config + fc_started (fontconfig.c).
	// unsafe.Pointer: the FcConfig type only exists on !android/!windows
	// (fontconfig.c has its own cgo preamble); fontconfig.go casts.
	fcConfig      unsafe.Pointer
	fcInitialized bool
}

//------------------------- Faces -----------------------

// C: typedef struct face (freetype.c:78)
type faceT struct {
	face        ftFace
	url         string
	currentSize int
	family      string
	fullname    string
	style       uint8
	fontDomain  int
	glyphs      []*glyphT // C: struct glyph_list glyphs
	prio        int
	refcount    int
	buf         *miscpkg.Buf // C: buf_t *buf — faces loaded from memory

	lookupName       string // C: char *lookup_name (NULL == "")
	lookupFontDomain int

	mem uintptr // C-owned copy for FT_OPEN_MEMORY faces
}

// C: static struct face_list static_faces / dynamic_faces — ft fields

//------------------------- Glyph cache -----------------------

// C: typedef struct glyph (freetype.c:104)
type glyphT struct {
	uc    int
	size  int16
	style uint8

	face *faceT

	gi uint32

	origGlyph  ftGlyph
	bmp        ftGlyph
	outline    ftGlyph
	outlineAmt int
	advX       int // C: int adv_x (FT_Pos, 1/64 units)

	// Color bitmap glyph extension (CBDT emoji): upstream C has no
	// color-glyph path and crashes on units_per_EM==0 faces.
	cbmp  *image.Pixmap // premultiplied BGRA, pre-scaled to size
	cleft int           // scaled bitmap_left (px)
	ctop  int           // scaled bitmap_top (px)

	bbox ftBBox
}

// C: static struct glyph_list glyph_hash[] / glyph_queue allglyphs /
// static int num_glyphs — ft fields

// C: static void glyph_destroy (freetype.c:168)
func (sys *System) glyphDestroy(g *glyphT) {
	g.face.glyphs = removeGlyph(g.face.glyphs, g)        // LIST_REMOVE(face_link)
	sys.ft.allglyphs = removeGlyphT(sys.ft.allglyphs, g) // TAILQ_REMOVE(lru_link)
	sys.ft.glyphHash[g.hash()] = removeGlyph(sys.ft.glyphHash[g.hash()], g)
	ftDoneGlyph(g.origGlyph)
	if g.bmp != 0 {
		ftDoneGlyph(g.bmp)
	}
	if g.outline != 0 {
		ftDoneGlyph(g.outline)
	}
	sys.ft.numGlyphs--
}

func (g *glyphT) hash() int {
	return (g.uc ^ int(g.size) ^ int(g.style)) & glyphHashMask
}

func removeGlyph(l []*glyphT, g *glyphT) []*glyphT {
	for i, x := range l {
		if x == g {
			return slices.Delete(l, i, i+1)
		}
	}
	return l
}

func removeGlyphT(l []*glyphT, g *glyphT) []*glyphT { return removeGlyph(l, g) }

// C: static void face_destroy (freetype.c:185)
func (sys *System) faceDestroy(f *faceT) {
	for len(f.glyphs) > 0 {
		sys.glyphDestroy(f.glyphs[0])
	}
	sys.ft.ts.Trace(trace.TRACE_DEBUG, "Freetype",
		"Unloading '%s' [%s] originally from %s",
		ftFaceFamilyName(f.face), ftFaceStyleName(f.face), f.url)
	sys.ft.dynamicFaces = removeFace(sys.ft.dynamicFaces, f)
	sys.ft.staticFaces = removeFace(sys.ft.staticFaces, f)
	if f.buf != nil {
		f.buf.Release() // C: buf_release(f->buf)
	}
	if f.mem != 0 {
		ftFreeMem(f.mem)
	}
	ftDoneFace(f.face)
}

func removeFace(l []*faceT, f *faceT) []*faceT {
	for i, x := range l {
		if x == f {
			return slices.Delete(l, i, i+1)
		}
	}
	return l
}

// C: static void faces_purge (freetype.c:203)
func (sys *System) facesPurge() {
	for i := 0; i < len(sys.ft.dynamicFaces); {
		f := sys.ft.dynamicFaces[i]
		if f.refcount == 0 && len(f.glyphs) == 0 {
			sys.faceDestroy(f)
			continue // f removed from slice; re-check index
		}
		i++
	}
}

// C: static void faces_flush_lookup (freetype.c:218)
func (sys *System) facesFlushLookup() {
	for _, f := range sys.ft.dynamicFaces {
		f.lookupName = "" // C: mystrset(&f->lookup_name, NULL)
		f.lookupFontDomain = -1
	}
	for _, f := range sys.ft.staticFaces {
		f.lookupName = ""
		f.lookupFontDomain = -1
	}
}

// C: static int face_cmp (freetype.c:261)
func faceCmp(a, b *faceT) int { return a.prio - b.prio }

// C: LIST_INSERT_SORTED(faces, face, link, face_cmp)
func insertSorted(faces []*faceT, f *faceT) []*faceT {
	i := 0
	for i < len(faces) && faceCmp(faces[i], f) <= 0 {
		i++
	}
	faces = append(faces, nil)
	copy(faces[i+1:], faces[i:])
	faces[i] = f
	return faces
}

// C: static face_t *face_create_epilogue (freetype.c:271)
func (sys *System) faceCreateEpilogue(face *faceT, url string, faces *[]*faceT,
	prio, fontDomain int) *faceT {
	family := ftFaceFamilyName(face.face)
	style := ftFaceStyleName(face.face)

	if style != "" {
		for tok := range strings.FieldsSeq(style) { // C: strtok_r(f, " ", &tmp)
			if strings.EqualFold(tok, "bold") {
				face.style = TR_STYLE_BOLD
			}
			if strings.EqualFold(tok, "italic") {
				face.style = TR_STYLE_ITALIC
			}
		}
	}
	face.url = url
	face.family = family
	face.fullname = fmt.Sprintf("%s %s", family, style)

	face.family = strings.ToLower(face.family)
	face.fullname = strings.ToLower(face.fullname)

	sys.ft.ts.Trace(trace.TRACE_DEBUG, "Freetype",
		"Loaded font family='%s' fullname='%s' style='%s' from %s domain:%d",
		face.family, face.fullname, style, face.url, fontDomain)

	ftSelectCharmap(face.face, ftEncUnicode)

	// Flush any lookup caches
	sys.facesFlushLookup()

	face.fontDomain = fontDomain
	face.prio = prio
	if prio == 0 {
		*faces = slices.Insert((*faces), 0, face) // LIST_INSERT_HEAD
	} else {
		*faces = insertSorted(*faces, face)
	}

	face.lookupFontDomain = -1
	return face
}

// C: static face_t *face_create_from_fh (freetype.c:325)
func (sys *System) faceCreateFromFH(fh *facore.Handle, url string,
	faces *[]*faceT, prio, fontDomain int) (*faceT, error) {
	s, err := facore.FSize(fh)
	if s < 0 || err != nil {
		facore.FAClose(fh)
		return nil, errors.New("Not a seekable file")
	}

	face := &faceT{}
	if e := ftStreamFace(sys.ft.library, fh, s, &face.face); e != 0 {
		// C: fh is leaked here too (FT_Open_Face failure does not
		// invoke stream->close); mirrored.
		return nil, fmt.Errorf("Unable to open face: %d", e)
	}
	return sys.faceCreateEpilogue(face, url, faces, prio, fontDomain), nil
}

// C: static face_t *face_create_from_mem (freetype.c:367)
func (sys *System) faceCreateFromMem(b *miscpkg.Buf,
	faces *[]*faceT, prio, fontDomain int) (*faceT, error) {
	face := &faceT{}
	// C: oa.memory_base = buf_cstr(b); face keeps b via buf_retain.
	// Go: copy into C memory (cgo may not retain Go pointers); the
	// retained Buf mirrors C's refcount shape.
	// C: oa.memory_base = buf_cstr(b); the backend copies into C
	// memory; face.mem is released on face_destroy.
	mem, e := ftMemoryFace(sys.ft.library, b.C8(), &face.face)
	if e != 0 {
		return nil, fmt.Errorf("Unable to open face: %d", e)
	}
	face.mem = mem
	face.buf = b.Retain() // C: face->buf = buf_retain(b)
	return sys.faceCreateEpilogue(face, "memory", faces, prio, fontDomain), nil
}

// C: static face_t *face_create_from_uri (freetype.c:390)
func (sys *System) faceCreateFromURI(fam *facore.FileAccessManager, path string, faces *[]*faceT,
	prio, fontDomain int) *faceT {
	for _, face := range *faces {
		if face.url == path && face.fontDomain == fontDomain {
			return face
		}
	}

	fh, err := facore.FAOpenResolver(fam,
		path, 0, nil)
	if fh == nil || err != nil {
		sys.ft.ts.Trace(trace.TRACE_ERROR, "Freetype",
			"Unable to load font: %s -- %v", path, err)
		return nil
	}

	face, ferr := sys.faceCreateFromFH(fh, path, faces, prio, fontDomain)
	if face == nil {
		sys.ft.ts.Trace(trace.TRACE_ERROR, "Freetype",
			"Unable to load font: %s -- %v", path, ferr)
		return nil
	}
	return face
}

// C: static face_t *face_resolve (freetype.c:420)
func (sys *System) faceResolve(uc int, style uint8, name string, fontDomain int) *faceT {
	if name != "" {
		if sys.ft.fam.FACanHandle(name) {
			f := sys.faceCreateFromURI(sys.ft.fam, name, &sys.ft.dynamicFaces, 0, fontDomain)
			if f != nil && ftGetCharIndex(f.face, uc) != 0 {
				return f
			}
		}

		var best *faceT
		bestScore := 0 // Higher is better

		// Try to find best matching font amongst our loaded faces
		lcname := strings.ToLower(name)

		for _, f := range sys.ft.dynamicFaces {
			// Faces that can't render our glyph is bad
			if ftGetCharIndex(f.face, uc) == 0 {
				continue
			}
			// Always want to match font domain here
			if f.fontDomain != fontDomain {
				continue
			}
			if f.url == name {
				return f
			}
			// If we want no style and face have style, ignore it since
			// we can create italic and bold glyphs from normal faces
			// but not the other way around
			if f.style != 0 && style == 0 {
				continue
			}

			score := 0
			// Correct style give some extra points
			if f.style == style {
				score += 4
			}
			switch {
			case f.fullname == lcname:
				score += 100
			case f.family == lcname:
				score += 90
			case strings.Contains(lcname, f.fullname):
				score += 80
			case strings.Contains(lcname, f.family):
				score += 70
			case strings.Contains(f.fullname, lcname):
				score += 60
			case strings.Contains(f.family, lcname):
				score += 50
			}
			if score > bestScore && score > 10 {
				bestScore = score
				best = f
			}
		}
		if best != nil {
			return best
		}
	}

	for _, f := range sys.ft.staticFaces {
		if f.style == style && ftGetCharIndex(f.face, uc) != 0 {
			return f
		}
	}
	for _, f := range sys.ft.staticFaces {
		if f.style == 0 && ftGetCharIndex(f.face, uc) != 0 {
			return f
		}
	}

	// ENABLE_LIBFONTCONFIG
	var url [4096]byte // C: URL_MAX
	if sys.FontconfigResolve(uc, style, name, url[:]) == 0 {
		f := sys.faceCreateFromURI(sys.ft.fam, errStr(url[:]), &sys.ft.dynamicFaces, 0, fontDomain)
		if f != nil {
			return f
		}
	}

	// Last resort, anything that has the glyph
	for _, f := range sys.ft.dynamicFaces {
		if ftGetCharIndex(f.face, uc) != 0 {
			return f
		}
	}
	return nil
}

// C: static face_t *face_find (freetype.c:544)
func (sys *System) faceFind(uc int, style uint8, name string, fontDomain int) *faceT {
	f := sys.faceResolve(uc, style, name, fontDomain)
	if f == nil {
		return nil
	}
	f.lookupFontDomain = fontDomain
	if name != f.lookupName {
		f.lookupName = name // C: mystrset(&f->lookup_name, name)
	}
	return f
}

// C: static void face_set_size (freetype.c:561)
func faceSetSize(f *faceT, size int) {
	if f.currentSize == size {
		return
	}
	ftRequestSize(f.face, size)
	f.currentSize = size
}

// C: static glyph_t *glyph_get (freetype.c:577)
func (sys *System) glyphGet(uc int, size int, style uint8, font string, fontDomain int) *glyphT {
	hash := (uc ^ size ^ int(style)) & glyphHashMask

	var g *glyphT
	for _, x := range sys.ft.glyphHash[hash] {
		if x.uc != uc || int(x.size) != size || x.style != style {
			continue
		}
		if x.face.lookupName == font && x.face.lookupFontDomain == fontDomain {
			g = x
			break
		}
	}

	if g == nil {
		f := sys.faceFind(uc, style, font, fontDomain)
		if f == nil {
			f = sys.faceFind(uc, 0, font, fontDomain)
			if f == nil {
				return nil
			}
		}

		gi := ftGetCharIndex(f.face, uc)

		faceSetSize(f, size)

		// Extension: FT_LOAD_COLOR on color faces (emoji) so embedded
		// BGRA bitmaps are delivered instead of nothing/gray slots.
		flags := int32(ftLoadForceAutohint)
		if ftHasColor(f.face) != 0 {
			flags |= ftLoadColor
		}
		if ftLoadGlyph(f.face, gi, flags) != 0 {
			return nil
		}

		gs := ftFaceGlyph(f.face)

		if style&TR_STYLE_ITALIC != 0 && f.style&TR_STYLE_ITALIC == 0 {
			ftGlyphSlotOblique(gs)
		}

		if style&TR_STYLE_BOLD != 0 && f.style&TR_STYLE_BOLD == 0 &&
			ftSlotFormat(gs) == ftGlyphFormatOutline {
			sf := ftSlotFace(gs)
			v := ftMulFix(ftPos(ftFaceUnitsPerEM(sf)),
				ftFaceYScale(sf)) / 64
			ftSlotEmboldenOutline(gs, v)
		}

		g = &glyphT{}
		if ftGetGlyph(gs, &g.origGlyph) != 0 {
			return nil
		}

		ftGlyphGetCBox(g.origGlyph, ftBBoxGridfit, &g.bbox)

		// Extension: color bitmap glyphs (CBDT/sbix emoji). The slot
		// already holds a BGRA bitmap — extract + scale it now so the
		// draw path can blit color instead of coverage. Must run after
		// the cbox since it rewrites bbox/advX at the scaled size.
		if ftSlotFormat(gs) == ftGlyphFormatBitmap {
			bm := ftBGBitmap(g.origGlyph)
			if ftBitmapPixelMode(bm) == ftPixelModeBGRA {
				g.cbmp = glyphColorPixmap(gs, g, bm, size)
			}
		}

		g.gi = gi
		f.glyphs = slices.Insert(f.glyphs, 0, g) // LIST_INSERT_HEAD(face_link)
		g.face = f
		g.uc = uc
		g.style = style
		g.size = int16(size)

		g.advX = int(ftSlotAdvanceX(gs))
		sys.ft.glyphHash[hash] = slices.Insert(sys.ft.glyphHash[hash], 0, g)
		sys.ft.numGlyphs++
	} else {
		sys.ft.allglyphs = removeGlyph(sys.ft.allglyphs, g) // TAILQ_REMOVE
	}
	sys.ft.allglyphs = append(sys.ft.allglyphs, g) // TAILQ_INSERT_TAIL
	return g
}

// C: static void glyph_flush_one (freetype.c:643)
func (sys *System) glyphFlushOne() {
	if len(sys.ft.allglyphs) == 0 {
		return // C: assert(g != NULL)
	}
	sys.glyphDestroy(sys.ft.allglyphs[0])
}

// C: static void draw_glyph (freetype.c:654)
func drawGlyph(pm *image.Pixmap, left, top int, bmp ftBitmap, color uint32) {
	w, h := ftBitmapDims(bmp)
	src := &image.Pixmap{
		Type:   image.PixmapI,
		Width:  w,
		Height: h,
		Stride: w, // C: pm_linesize = bmp->width
		Data:   ftBitmapSlice(bmp),
	}
	image.PixmapComposite(pm, src, left, top, color)
}

// glyphColorPixmap — extension (no upstream C counterpart): extract
// the premultiplied BGRA bitmap from a bitmap-format glyph slot and
// scale it to the requested pixel height. CBDT strikes are fixed-size
// (~128px) while text is rendered at size px, so we bilinear-rescale.
func glyphColorPixmap(gs ftGlyphSlot, g *glyphT, bm ftBitmap,
	size int) *image.Pixmap {
	sw, sh := ftBitmapDims(bm)
	if sw <= 0 || sh <= 0 {
		return nil
	}
	src := ftBitmapSlicePitch(bm)
	spitch := ftBitmapPitch(bm)

	// target height = requested size; keep aspect ratio
	th := size
	tw := sw * size / sh
	if tw < 1 {
		tw = 1
	}
	dst := make([]byte, tw*4*th)
	for y := 0; y < th; y++ {
		sy := y * sh / th
		for x := 0; x < tw; x++ {
			sx := x * sw / tw
			si := sy*spitch + sx*4
			di := (y*tw + x) * 4
			copy(dst[di:di+4], src[si:si+4])
		}
	}
	pm := &image.Pixmap{Type: image.PixmapBGRA, Width: tw, Height: th,
		Stride: tw * 4, Data: dst}

	// scale bearing + advance to the drawn size; the glyph occupies
	// the whole em square (no baseline offset for emoji)
	g.cleft = 0
	g.ctop = th
	g.advX = (tw + 1) << 6
	g.bbox = ftBBox{xMin: 0, yMin: 0, xMax: ftPos(tw) << 6,
		yMax: ftPos(th) << 6}
	return pm
}

// drawGlyphColor — extension: blit a premultiplied BGRA emoji pixmap
// onto the canvas. Only PixmapBGR32 destinations can carry color;
// on IA canvases it degrades to the alpha channel (silhouette).
func drawGlyphColor(pm *image.Pixmap, left, top int, src *image.Pixmap) {
	for y := 0; y < src.Height; y++ {
		wy := y + top
		if wy < 0 || wy >= pm.Height {
			continue
		}
		for x := 0; x < src.Width; x++ {
			wx := x + left
			if wx < 0 || wx >= pm.Width {
				continue
			}
			so := y*src.Stride + x*4
			sa := int(src.Data[so+3])
			if sa == 0 {
				continue
			}
			do := wy * pm.Stride
			switch pm.Type {
			case image.PixmapBGR32:
				do += wx * 4
				// dst is premultiplied B,G,R,A; src is too →
				// src-over blend
				inv := 255 - sa
				pm.Data[do+0] = byte(int(src.Data[so+0]) +
					(int(pm.Data[do+0])*inv+127)/255)
				pm.Data[do+1] = byte(int(src.Data[so+1]) +
					(int(pm.Data[do+1])*inv+127)/255)
				pm.Data[do+2] = byte(int(src.Data[so+2]) +
					(int(pm.Data[do+2])*inv+127)/255)
				pm.Data[do+3] = byte(sa +
					(int(pm.Data[do+3])*inv+127)/255)
			case image.PixmapIA:
				do += wx * 2
				lum := (int(src.Data[so+2])*77 +
					int(src.Data[so+1])*150 +
					int(src.Data[so+0])*29) >> 8
				i := int(pm.Data[do])
				a := int(pm.Data[do+1])
				y := sa
				na := y + (a*(255-y)+127)/255
				if na != 0 {
					i = (lum*y + i*a*(255-y)/255 + na/2) / na
				}
				pm.Data[do] = byte(i)
				pm.Data[do+1] = byte(na)
			}
		}
	}
}

// C: typedef struct line (freetype.c:671)
const (
	lineTypeText = 0 // C: LINE_TYPE_TEXT
	lineTypeHR   = 1 // C: LINE_TYPE_HR
)

type lineT struct {
	start         int
	count         int
	width         int
	xspace        int
	alignment     int
	height        int
	descender     int
	shadow        int
	outline       int
	defaultHeight int
	color         uint32
	xoffset       int
	ltype         int // C: enum type
}

// C: typedef struct item (freetype.c:692)
type itemT struct {
	g            *glyphT
	code         int
	color        uint32
	shadowColor  uint32
	outlineColor uint32
	kerning      int16
	advX         int16
	outline      uint16
	shadow       uint16
	setMargin    int8 // C: char set_margin
}

// C: static const float legacy_size_mult[16] (freetype.c:706)
var legacySizeMult = [16]float32{
	0, 0.5, 0.75, 1.0, 1.25, 1.5, 2.0, 3.0,
}

// lineQueue emulates C's TAILQ over alloca'd line_t.
type lineQueue struct{ v []*lineT }

func (q *lineQueue) tail(li *lineT) { q.v = append(q.v, li) }
func (q *lineQueue) first() *lineT  { return q.v[0] }
func (q *lineQueue) last() *lineT   { return q.v[len(q.v)-1] }
func (q *lineQueue) insertAfter(li, lix *lineT) {
	for i, l := range q.v {
		if l == li {
			q.v = slices.Insert(q.v, i+1, lix)
			return
		}
	}
}
func (q *lineQueue) remove(li *lineT) {
	for i, l := range q.v {
		if l == li {
			q.v = slices.Delete(q.v, i, i+1)
			return
		}
	}
}

// C: static void draw_glyphs (freetype.c:741)
func (sys *System) drawGlyphs(pm *image.Pixmap, lq *lineQueue, targetHeight,
	sizX int, items []itemT, startX, startY, originY, margin, pass int,
	ti *image.TextInfoComponent) {
	var pen ftVec
	penY := 0
	penX := 0

	for _, li := range lq.v {
		penY -= li.height * 64

		if li.ltype == lineTypeHR {
			ypos := targetHeight - (penY + li.height*64)
			ypos >>= 6
			if ypos < 0 {
				ypos = 0
			}
			if ypos > targetHeight {
				ypos = targetHeight
			}

			switch pm.Type {
			case image.PixmapBGR32: // C: PIXMAP_BGR32
				row := ypos * pm.Stride
				for i := range pm.Width {
					putU32(pm.Data[row+i*4:], li.color)
				}
				row = (ypos + 1) * pm.Stride
				r := uint8(li.color)
				g := uint8(li.color >> 8)
				b := uint8(li.color >> 16)
				a := uint8(li.color >> 24)
				color := uint32(a)<<24 | uint32(b>>1)<<16 |
					uint32(g>>1)<<8 | uint32(r>>1)
				for i := range pm.Width {
					putU32(pm.Data[row+i*4:], color)
				}

			case image.PixmapIA: // C: PIXMAP_IA
				row := ypos * pm.Stride
				for i := range pm.Width {
					pm.Data[row+i*2] = uint8(li.color)
					pm.Data[row+i*2+1] = uint8(li.color >> 24)
				}
				row = (ypos + 1) * pm.Stride
				r := uint8(li.color >> 1)
				a := uint8(li.color >> 24)
				for i := range pm.Width {
					pm.Data[row+i*2] = r
					pm.Data[row+i*2+1] = a
				}
			}
			continue
		}

		switch li.alignment {
		case TR_ALIGN_LEFT, TR_ALIGN_JUSTIFIED:
			penX = 0
		case TR_ALIGN_CENTER:
			penX = (sizX - li.width) / 2
		case TR_ALIGN_RIGHT:
			penX = sizX - li.width
		}
		if penX < 0 {
			penX = 0
		}
		penX += li.xoffset

		for i := li.start; i < li.start+li.count; i++ {
			g := items[i].g
			if g == nil {
				continue
			}

			penX += int(items[i].kerning)

			pen.x = ftPos(startX + penX + 31)
			pen.y = ftPos(startY + penY + originY + 31 - li.descender)

			pen.x &^= 63
			pen.y &^= 63
			pen.x >>= 6
			pen.y >>= 6

			if items[i].outline > 0 &&
				(g.outline == 0 || g.outlineAmt != int(items[i].outline)) {
				if g.outline != 0 {
					ftDoneGlyph(g.outline)
				}
				g.outline = g.origGlyph
				ftStrokerSet(sys.ft.stroker,
					ftPos(items[i].outline),
					ftLinecapRound, ftLinejoinRound, 0)
				g.outlineAmt = int(items[i].outline)
				if ftGlyphStrokeBorder(&g.outline, sys.ft.stroker, 0, 0) != 0 {
					g.outline = 0
				} else if ftGlyphToBitmap(&g.outline,
					ftRenderNormal, nil, 1) != 0 {
					g.outline = 0
				}
			}

			if g.bmp == 0 && g.cbmp == nil {
				g.bmp = g.origGlyph
				if ftGlyphToBitmap(&g.bmp,
					ftRenderNormal, nil, 0) != 0 {
					g.bmp = 0
				}
			}

			if pass == 0 && items[i].shadow != 0 &&
				(g.outline != 0 || g.bmp != 0) {
				src := g.outline
				if src == 0 {
					src = g.bmp
				}
				drawGlyph(pm,
					ftBGLeft(src)+int(items[i].shadow)+margin+int(pen.x),
					targetHeight-ftBGTop(src)+int(items[i].shadow)+margin-int(pen.y),
					ftBGBitmap(src), items[i].shadowColor)
			}

			if pass == 1 && items[i].outline > 0 && g.outline != 0 {
				drawGlyph(pm,
					ftBGLeft(g.outline)+margin+int(pen.x),
					targetHeight-ftBGTop(g.outline)+margin-int(pen.y),
					ftBGBitmap(g.outline), items[i].outlineColor)
			}

			if pass == 2 && g.cbmp != nil {
				// extension: premultiplied BGRA emoji glyph
				drawGlyphColor(pm,
					g.cleft+margin+int(pen.x),
					targetHeight-g.ctop+margin-int(pen.y),
					g.cbmp)

				if ti != nil && ti.CharPos != nil {
					ti.CharPos[i*2+0] = g.cleft + int(pen.x)
					ti.CharPos[i*2+1] = g.cleft + g.cbmp.Width +
						int(pen.x)
				}
			} else if pass == 2 && g.bmp != 0 {
				drawGlyph(pm,
					ftBGLeft(g.bmp)+margin+int(pen.x),
					targetHeight-ftBGTop(g.bmp)+margin-int(pen.y),
					ftBGBitmap(g.bmp), items[i].color)

				if ti != nil && ti.CharPos != nil {
					ti.CharPos[i*2+0] = ftBGLeft(g.bmp) + int(pen.x)
					bw, _ := ftBitmapDims(ftBGBitmap(g.bmp))
					ti.CharPos[i*2+1] = ftBGLeft(g.bmp) + bw + int(pen.x)
				}
			}

			if ti != nil && ti.CharPos != nil && items[i].code == ' ' {
				ti.CharPos[2*i+0] = penX / 64
			}

			penX += int(items[i].advX)
			if items[i].code == ' ' {
				penX += li.xspace
			}

			if ti != nil && ti.CharPos != nil && items[i].code == ' ' {
				ti.CharPos[2*i+1] = penX / 64
			}
		}
	}
}

// C: static struct image *text_render0 (freetype.c:923)
func (sys *System) textRender0(uc []uint32, length int, flags, defaultSize int,
	scale float32, globalAlignment, maxWidth, maxLines int,
	defaultFont string, defaultDomain, minSize int) *image.Image {
	var prev uint32
	var bbox ftBBox
	var delta ftVec

	var g *glyphT
	var lines []*lineT = nil
	_ = lines

	tiFlags := 0
	var style uint8
	colorOutput := 0

	currentSize := int(float32(defaultSize) * scale)
	var currentColor uint32 = 0xffffff
	var currentAlpha uint32 = 0xff000000

	currentOutline := 0
	var currentOutlineColor uint32
	var currentOutlineAlpha uint32 = 0xff000000

	currentShadow := 0
	var currentShadowColor uint32
	var currentShadowAlpha uint32 = 0xff000000

	needShadowPass := 0
	needOutlinePass := 0

	currentFont := defaultFont
	currentDomain := defaultDomain

	if minSize > 0 && currentSize < minSize {
		scale = float32(minSize) / float32(currentSize)
		currentSize = int(float32(defaultSize) * scale)
	}

	if currentSize < 3 || scale < 0.001 {
		return nil
	}

	maxWidth *= 64

	var lq lineQueue

	/* Compute position for each glyph */
	style = 0

	if flags&TR_RENDER_BOLD != 0 {
		style |= TR_STYLE_BOLD
	}
	if flags&TR_RENDER_ITALIC != 0 {
		style |= TR_STYLE_ITALIC
	}
	if flags&TR_RENDER_SHADOW != 0 {
		currentShadow = -1
	}
	if flags&TR_RENDER_OUTLINE != 0 {
		currentOutline = 64
	}

	prev = 0
	var li *lineT

	items := make([]itemT, length)

	out := 0
	alignment := globalAlignment
	setMargin := 0

	for i := range length {
		if li == nil {
			li = &lineT{}
			li.defaultHeight = currentSize
			li.ltype = lineTypeText
			li.start = -1
			li.alignment = alignment
			lq.tail(li)
			prev = 0
		}

		c := uc[i]
		switch {
		case c == TR_CODE_START:
			if i != 0 {
				li = nil
			}
			continue

		case c == '\n' || c == TR_CODE_NEWLINE:
			li = nil
			continue

		case c == TR_CODE_HR:
			li = &lineT{}
			li.defaultHeight = currentSize
			li.ltype = lineTypeHR
			li.start = -1
			li.height = 4
			li.color = currentColor | currentAlpha
			lq.tail(li)
			li = nil
			continue

		case c == TR_CODE_CENTER_ON:
			if i != 0 {
				li = nil
			} else {
				li.alignment = TR_ALIGN_CENTER
			}
			alignment = TR_ALIGN_CENTER
			continue

		case c == TR_CODE_CENTER_OFF:
			alignment = globalAlignment
			if i != 0 {
				li = nil
			} else {
				li.alignment = globalAlignment
			}
			continue

		case c == TR_CODE_ITALIC_ON:
			style |= TR_STYLE_ITALIC

		case c == TR_CODE_ITALIC_OFF:
			style &^= TR_STYLE_ITALIC

		case c == TR_CODE_BOLD_ON:
			style |= TR_STYLE_BOLD

		case c == TR_CODE_BOLD_OFF:
			style &^= TR_STYLE_BOLD

		case c == TR_CODE_FONT_RESET:
			currentSize = int(float32(defaultSize) * scale)
			currentColor = 0xffffff
			currentAlpha = 0xff000000
			currentFont = defaultFont
			currentDomain = defaultDomain

		case c >= TR_CODE_SIZE_PX && c <= TR_CODE_SIZE_PX+0xffff:
			currentSize = int(float32(c&0xffff) * scale)

		case c >= TR_CODE_COLOR && c <= TR_CODE_COLOR+0xffffff:
			currentColor = c & 0xffffff // BGR host order
			if image.ColorIsNotGray(currentColor) {
				colorOutput = 1
			}

		case c >= TR_CODE_SHADOW_COLOR && c <= TR_CODE_SHADOW_COLOR+0xffffff:
			currentShadowColor = c & 0xffffff
			if image.ColorIsNotGray(currentShadowColor) {
				colorOutput = 1
			}

		case c >= TR_CODE_OUTLINE_COLOR && c <= TR_CODE_OUTLINE_COLOR+0xffffff:
			currentOutlineColor = c & 0xffffff
			if image.ColorIsNotGray(currentOutlineColor) {
				colorOutput = 1
			}

		case c >= TR_CODE_FONT_FAMILY && c <= TR_CODE_FONT_FAMILY+0xffffff:
			if im := sys.idmapFind(int(c & 0xffffff)); im != nil {
				currentFont = im.name
				currentDomain = im.domain
			}

		case c >= TR_CODE_ALPHA && c <= TR_CODE_ALPHA+0xff:
			currentAlpha = (c & 0xff) << 24

		case c >= TR_CODE_SHADOW_ALPHA && c <= TR_CODE_SHADOW_ALPHA+0xff:
			currentShadowAlpha = (c & 0xff) << 24

		case c >= TR_CODE_OUTLINE_ALPHA && c <= TR_CODE_OUTLINE_ALPHA+0xff:
			currentOutlineAlpha = (c & 0xff) << 24

		case c >= TR_CODE_SHADOW && c <= TR_CODE_SHADOW+0xffff:
			currentShadow = int(float32(c&0xffff) * scale)

		case c >= TR_CODE_OUTLINE && c <= TR_CODE_OUTLINE+0xffff:
			currentOutline = int(64 * float32(c&0xffff) * scale)

		case c >= TR_CODE_SHADOW_US && c <= TR_CODE_SHADOW_US+0xffff:
			currentShadow = int(c & 0xffff)

		case c >= TR_CODE_OUTLINE_US && c <= TR_CODE_OUTLINE_US+0xffff:
			currentOutline = int(64 * (c & 0xffff))

		case c >= TR_CODE_FONT_SIZE+1 && c <= TR_CODE_FONT_SIZE+7:
			currentSize = int(legacySizeMult[c&0xf] *
				float32(defaultSize) * scale)

		case c == TR_CODE_SET_MARGIN:
			setMargin = 1
		}

		if c >= 0x70000000 {
			continue
		}

		if li.start == -1 {
			li.start = out
		}

		g = sys.glyphGet(int(c), currentSize, style, currentFont, currentDomain)
		if g == nil {
			continue
		}

		if ftHasKerning(g.face.face) != 0 && g.gi != 0 && prev != 0 {
			faceSetSize(g.face, currentSize)
			ftGetKerning(g.face.face, prev, g.gi,
				ftKerningDefault, &delta)
			items[out].kerning = int16(delta.x)
		} else {
			items[out].kerning = 0
		}
		items[out].advX = int16(g.advX)
		items[out].g = g
		items[out].code = int(c)
		items[out].color = currentColor | currentAlpha
		if g.cbmp != nil {
			colorOutput = 1 // extension: emoji force a color canvas
		}

		items[out].outline = uint16(currentOutline)
		items[out].outlineColor = currentOutlineColor | currentOutlineAlpha

		needOutlinePass |= int(items[i].outline)

		if currentShadow == -1 {
			items[out].shadow = uint16(1 + currentSize/20)
		} else {
			items[out].shadow = uint16(currentShadow)
		}

		items[out].setMargin = int8(setMargin)
		setMargin = 0

		needShadowPass |= int(items[out].shadow)

		items[out].shadowColor = currentShadowColor | currentShadowAlpha

		prev = g.gi
		li.count++
		out++
	}

	lines = nil
	sizX := 0
	wrapMargin := 0

	linesCnt := 0
	for idx := 0; idx < len(lq.v); {
		li = lq.v[idx]
		nextIdx := idx + 1

		if linesCnt == maxLines {
			lq.v = slices.Delete(lq.v, idx, idx+1) // TAILQ_REMOVE
			continue
		}

		w := li.xoffset

		if li.ltype != lineTypeHR {
			for j := range li.count {
				w += int(items[li.start+j].advX)

				if j > 0 {
					w += int(items[li.start+j].kerning)
				}

				if j == 0 {
					g = items[li.start+j].g
					if g != nil {
						w += int(g.bbox.xMin)
						if int(g.bbox.xMin) < int(bbox.xMin) {
							bbox.xMin = g.bbox.xMin
						}
					}
				}

				if items[li.start+j].setMargin != 0 {
					wrapMargin = w
				}

				if linesCnt < maxLines-1 && w >= maxWidth {
					k := j
					w2 := w
					for k > 0 && items[li.start+k-1].code != ' ' {
						k--
						kv := int(items[li.start+k].advX)
						if k > 0 {
							kv += int(items[li.start+k].kerning)
						}
						w2 -= kv
					}

					if k > 0 {
						lix := &lineT{}
						lix.defaultHeight = li.defaultHeight
						lix.ltype = lineTypeText
						lix.start = li.start + k
						lix.count = li.count - k
						lix.xoffset = wrapMargin
						lix.alignment = globalAlignment
						lq.insertAfter(li, lix)
						nextIdx = idx + 1 // C: next = lix
						tiFlags |= image.TextWrapped
						k--
						kv := int(items[li.start+k].advX)
						if k > 0 {
							kv += int(items[li.start+k].kerning)
						}
						w2 -= kv
						li.count = k
						w = w2
						break
					}
				}

				if linesCnt == maxLines-1 && g != nil && maxWidth != 0 {
					if flags&TR_RENDER_ELLIPSIZE != 0 {
						eg := sys.glyphGet(horizontalEllipsisUnicode,
							int(g.size), 0, g.face.url,
							g.face.fontDomain)
						if w > maxWidth-int(eg.advX) {
							for j > 0 && items[li.start+j-1].code == ' ' {
								j--
								jv := int(items[li.start+j].advX)
								if j > 0 {
									jv += int(items[li.start+j].kerning)
								}
								w -= jv
							}
							items[li.start+j].g = eg
							items[li.start+j].kerning = 0
							tiFlags |= image.TextTruncated
							w += int(eg.advX)
							li.count = j + 1
							break
						}
					} else {
						if w > maxWidth {
							tiFlags |= image.TextTruncated
							li.count = j
							break
						}
					}
				}
			}
		}

		li.width = w
		if w > sizX {
			sizX = w
		}
		linesCnt++
		idx = nextIdx
	}

	if sizX < 5 {
		return nil
	}

	if maxWidth != 0 && sizX > maxWidth {
		sizX = maxWidth
	}

	targetWidth := sizX / 64
	targetHeight := 0

	for _, li := range lq.v {
		if li.ltype == lineTypeHR {
			continue
		}
		w := 0
		for j := range li.count {
			d := int(items[li.start+j].advX)
			if j > 0 {
				d += int(items[li.start+j].kerning)
			}
			if g = items[li.start+j].g; g != nil {
				if w+int(g.bbox.xMin) < int(bbox.xMin) {
					bbox.xMin = ftPos(w + int(g.bbox.xMin))
				}
				if w+int(g.bbox.xMax) > int(bbox.xMax) {
					bbox.xMax = ftPos(w + int(g.bbox.xMax))
				}
			}
			w += d
		}
	}

	if maxWidth != 0 && int(bbox.xMax) > maxWidth {
		bbox.xMax = ftPos(maxWidth)
	}

	margin := -min(int(bbox.xMin), 0)
	if m := int(bbox.xMax) - sizX; m > margin {
		margin = m
	}
	if margin < 0 {
		margin = 0
	}

	for liIdx, li := range lq.v {
		height := 0
		descender := 0
		shadow := 0
		outline := 0
		topspill := 0

		if li.ltype == lineTypeText {
			for i := li.start; i < li.start+li.count; i++ {
				gg := items[i].g
				f := gg.face.face
				if int(gg.size) > height {
					height = int(gg.size)
				}
				// extension: guard units_per_EM==0 (bitmap-only
				// faces like NotoColorEmoji) — C SIGFPEs here
				if upem := ftFaceUnitsPerEM(f); upem != 0 {
					d := 64 * ftFaceDescender(f) * int(gg.size) /
						upem
					if d < descender {
						descender = d
					}
				}
				if int(items[i].shadow) > shadow {
					shadow = int(items[i].shadow)
				}
				if int(items[i].outline) > outline {
					outline = int(items[i].outline)
				}
				ts := int(gg.bbox.yMax) - height*64 - descender
				if ts > topspill {
					topspill = ts
				}
			}

			if height != 0 {
				li.height = height
			} else {
				li.height = li.defaultHeight
			}
			li.descender = descender
			li.shadow = shadow
			li.outline = outline

			if liIdx == 0 { // C: li == TAILQ_FIRST(&lq)
				if m := 2*li.outline + topspill; m > margin {
					margin = m
				}
			}
			if liIdx == len(lq.v)-1 { // C: li == TAILQ_LAST(&lq, line_queue)
				m := li.shadow * 64
				m = max(m, li.outline*2)
				if m > margin {
					margin = m
				}
			}

			if maxLines > 1 && li.alignment == TR_ALIGN_JUSTIFIED {
				spaces := 0
				spill := sizX - li.width
				for i := li.start; i < li.start+li.count; i++ {
					if items[i].code == ' ' {
						spaces++
					}
				}
				if float32(spill)/float32(li.width) < 0.2 {
					if spaces != 0 {
						li.xspace = spill / spaces
					} else {
						li.xspace = 0
					}
				}
			}
		}

		targetHeight += li.height
	}

	originY := targetHeight * 64
	startX := 0
	startY := 0

	margin = (margin + 63) / 64

	// --- allocate and init image
	numComp := 2
	if flags&TR_RENDER_NO_OUTPUT != 0 {
		numComp = 1
	}
	img := image.Alloc(numComp) // C: image_alloc

	img.Width = uint16(targetWidth + margin*2)
	img.Height = uint16(targetHeight + margin*2)
	img.Margin = uint16(margin)

	var pm *image.Pixmap
	if flags&TR_RENDER_NO_OUTPUT == 0 {
		format := image.PixmapIA
		if colorOutput != 0 {
			format = image.PixmapBGR32 // C: PIXMAP_BGR32
		}
		pm = image.PixmapCreate(targetWidth, targetHeight, format, margin)

		img.Components[1].Type = image.ComponentPixmap
		img.Components[1].Pixmap = pm
	}

	img.Components[0].Type = image.ComponentTextInfo
	ti := &img.Components[0].TextInfo
	ti.Lines = uint16(linesCnt)
	ti.Flags = uint16(tiFlags)

	if flags&TR_RENDER_CHARACTER_POS != 0 {
		ti.CharPosLen = uint16(length)
		ti.CharPos = make([]int, 2*length) // C: malloc(2*len*sizeof(int))
	}

	if pm != nil {
		if flags&TR_RENDER_DEBUG != 0 {
			for i := 0; i < pm.Height; i += 3 {
				for k := range pm.Stride {
					pm.Data[i*pm.Stride+k] = 0xc0
				}
			}
			l := 2
			if colorOutput != 0 {
				l = 4
			}
			for i := 0; i < pm.Width; i += 3 {
				for y := range pm.Height {
					for k := range l {
						pm.Data[y*pm.Stride+i*l+k] = 0xc0
					}
				}
			}
		}

		if needShadowPass != 0 {
			sys.drawGlyphs(pm, &lq, targetHeight, sizX, items, startX, startY,
				originY, margin, 0, nil)
			image.PixmapBoxBlur(pm, 4, 4)
		}

		if needOutlinePass != 0 {
			sys.drawGlyphs(pm, &lq, targetHeight, sizX, items, startX, startY,
				originY, margin, 1, nil)
		}

		sys.drawGlyphs(pm, &lq, targetHeight, sizX, items, startX, startY,
			originY, margin, 2, ti)
	}

	return img
}

// C: struct image *text_render (freetype.c:1466)
func (sys *System) TextRender(uc []uint32, length int, flags, defaultSize int,
	scale float32, alignment, maxWidth, maxLines int,
	family string, context, minSize int) *image.Image {
	sys.ft.mutex.Lock()
	im := sys.textRender0(uc, length, flags, defaultSize, scale, alignment,
		maxWidth, maxLines, family, context, minSize)
	for sys.ft.numGlyphs > 512 {
		sys.glyphFlushOne()
	}
	sys.facesPurge()
	sys.ft.mutex.Unlock()
	return im
}

// C: void freetype_load_default_font (freetype.c:1490)
func (sys *System) FreetypeLoadDefaultFont(url string, prio int) {
	sys.ft.mutex.Lock()
	sys.faceCreateFromURI(sys.ft.fam, url, &sys.ft.staticFaces, prio, 0)
	sys.ft.mutex.Unlock()
}

// C: static void freetype_init (freetype.c:1503)
// Go: exported; wired in cmd/movian-go/init.go where C's
// INITME(INIT_GROUP_GRAPHICS) ordering places it before fontstash_init.
// FreetypeSetFAM wires the file access manager (C: implicit global fa
// context used by face_create_from_uri → fa_open).
func (sys *System) FreetypeSetFAM(fam *facore.FileAccessManager) { sys.ft.fam = fam }

// FreetypeSetTraceSystem wires the trace system (C: trace() global).
func (sys *System) FreetypeSetTraceSystem(ts *trace.TraceSystem) { sys.ft.ts = ts }

func (sys *System) FreetypeStart() {
	if ftInitFreeType(&sys.ft.library) != 0 {
		sys.ft.ts.Trace(trace.TRACE_ERROR, "Freetype", "Freetype init error")
		panic("freetype init") // C: exit(1)
	}
	ftStrokerNew(sys.ft.library, &sys.ft.stroker)

	url := fmt.Sprintf("%s/res/fonts/liberation/LiberationSans-Regular.ttf",
		app.AppDataRoot())
	sys.FreetypeLoadDefaultFont(url, 0)
}

// C: void *freetype_load_dynamic_font_fh (freetype.c:1534)
func (sys *System) FreetypeLoadDynamicFontFH(fh *facore.Handle, url string, fontDomain int) (*faceT, error) {
	sys.ft.mutex.Lock()
	f, err := sys.faceCreateFromFH(fh, url, &sys.ft.dynamicFaces, 0, fontDomain)
	if f != nil {
		f.refcount++
	}
	sys.ft.mutex.Unlock()
	return f, err
}

// C: void *freetype_load_dynamic_font_buf (freetype.c:1553)
func (sys *System) FreetypeLoadDynamicFontBuf(b *miscpkg.Buf, fontDomain int) (*faceT, error) {
	sys.ft.mutex.Lock()
	f, err := sys.faceCreateFromMem(b, &sys.ft.dynamicFaces, 0, fontDomain)
	if f != nil {
		f.refcount++
	}
	sys.ft.mutex.Unlock()
	return f, err
}

// C: void freetype_unload_font (freetype.c:1572)
func (sys *System) FreetypeUnloadFont(ref *faceT) {
	f := ref
	sys.ft.mutex.Lock()
	f.refcount--
	if f.refcount == 0 {
		sys.faceDestroy(f)
	}
	sys.ft.mutex.Unlock()
}

// FreetypeGetFamily — C: freetype_get_family (text.h:114, declared only;
// no implementation or callers exist in freetype.c — omitted).

// helpers

func errStr(buf []byte) string {
	for i, b := range buf {
		if b == 0 {
			return string(buf[:i])
		}
	}
	return string(buf)
}

func putU32(dst []byte, v uint32) {
	dst[0] = byte(v)
	dst[1] = byte(v >> 8)
	dst[2] = byte(v >> 16)
	dst[3] = byte(v >> 24)
}
