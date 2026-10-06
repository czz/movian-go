package text

// Variant-neutral FreeType handle types used by the pure-Go wasm2go
// backend (ft_ftwasm.go, all platforms — the former cgo freetypecgo
// backend was removed after differential testing reached parity).
//
// All FreeType objects are opaque uintptrs so freetype_render.go
// stays a single canonical source: each handle is a wasm32 address in
// the module's linear memory. The C roles of freetype_glue.c /
// freetype.h macros are implemented per-backend (ml_* helpers in
// the cgo preamble, ftw_* exports in the wasm glue).

type ftLibrary uintptr   // C: FT_Library
type ftStroker uintptr   // C: FT_Stroker
type ftFace uintptr      // C: FT_Face
type ftGlyphSlot uintptr // C: FT_GlyphSlot
type ftGlyph uintptr     // C: FT_Glyph
type ftBitmap uintptr    // C: *FT_Bitmap

// ftPos — C: FT_Pos (26.6 fixed point on screen-size axes).
type ftPos = int64

// ftBBox — C: FT_BBox (used as a plain value in freetype.c).
type ftBBox struct {
	xMin, yMin, xMax, yMax ftPos
}

// ftVec — C: FT_Vector (pen/delta locals in freetype.c).
type ftVec struct {
	x, y ftPos
}

// Constants are defined in ft_ftwasm.go from
// the respective headers so canonical names are preserved:
// C.FT_LOAD_FORCE_AUTOHINT vs the FreeType header values.
