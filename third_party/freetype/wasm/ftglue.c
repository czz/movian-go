/*
 * ftglue.c — glue shim for the wasm32 FreeType build.
 *
 * Mirrors freetype_glue.c: every struct-field access the Go side needs
 * becomes an exported scalar function; the three C->host callbacks
 * (stream read/close, gray_spans) are wasm imports implemented in Go.
 *
 * Handle+error pairs are packed into a single u64: (err << 32) | ptr.
 */

#include <ft2build.h>
#include FT_FREETYPE_H
#include FT_GLYPH_H
#include FT_OUTLINE_H
#include FT_SYNTHESIS_H
#include FT_STROKER_H
#include FT_IMAGE_H
#include FT_MODULE_H
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

#define EXPORT

/* ------------------------------------------------------------------ */
/* host imports (Go)                                                  */
/* ------------------------------------------------------------------ */

__attribute__((import_module("w2g"), import_name("face_read")))
extern uint32_t w2g_face_read(uint64_t fh, uint32_t offset,
                              uint32_t buffer, uint32_t count);
__attribute__((import_module("w2g"), import_name("face_close")))
extern void w2g_face_close(uint64_t fh);
__attribute__((import_module("w2g"), import_name("gray_spans")))
extern void w2g_gray_spans(int32_t y, int32_t count,
                           uint32_t spans, uint64_t user);
__attribute__((import_module("w2g"), import_name("throw_longjmp")))
extern void w2g_throw_longjmp(void); /* Go side panics */

/* ------------------------------------------------------------------ */
/* emscripten-style SjLj runtime (emulated setjmp/longjmp)            */
/*                                                                     */
/* Compiled with -mllvm -enable-emscripten-sjlj: LLVM emits calls to  */
/* the helpers below + invoke_* wrappers (kept as host imports so a   */
/* Go panic in emscripten_longjmp unwinds to the nearest invoke,      */
/* which recovers and lets the caller route via __wasm_setjmp_test).  */
/* ------------------------------------------------------------------ */

/* state shared with generated code (referenced as data symbols) */
uintptr_t __THREW__, __threwValue;
static uint32_t tempRet0;

void setTempRet0(uint32_t v)  { tempRet0 = v; }
uint32_t getTempRet0(void)    { return tempRet0; }

/* jmp_buf must be large enough for this (it is: 8 bytes used) */
struct jmp_buf_impl {
  void    *func_invocation_id;
  uint32_t label;
};

/* canonical implementation — emscripten compiler-rt emscripten_setjmp.c */
void __wasm_setjmp(void *env, uint32_t label, void *func_invocation_id)
{
  struct jmp_buf_impl *jb = env;
  jb->func_invocation_id = func_invocation_id;
  jb->label = label;
}

uint32_t __wasm_setjmp_test(void *env, void *func_invocation_id)
{
  struct jmp_buf_impl *jb = env;
  if (jb->func_invocation_id == func_invocation_id)
    return jb->label;
  return 0;
}

/* never returns to the C caller: the host import panics in Go */
void emscripten_longjmp(uintptr_t env, int val)
{
  if (val == 0)
    val = 1;
  if (!__THREW__) {            /* setThrew semantics */
    __THREW__    = env;
    __threwValue = val;
  }
  w2g_throw_longjmp();
  __builtin_unreachable();
}

/* C: static unsigned long face_read (freetype.c:243) */
static unsigned long
glue_face_read(FT_Stream stream, unsigned long offset,
               unsigned char *buffer, unsigned long count)
{
  return w2g_face_read((uint64_t)(uintptr_t)stream->descriptor.pointer,
                       (uint32_t)offset, (uint32_t)(uintptr_t)buffer,
                       (uint32_t)count);
}

/* C: static void face_close (freetype.c:255) */
static void
glue_face_close(FT_Stream stream)
{
  w2g_face_close((uint64_t)(uintptr_t)stream->descriptor.pointer);
  free(stream);
}

/* ------------------------------------------------------------------ */
/* library                                                            */
/* ------------------------------------------------------------------ */

EXPORT uint64_t ftw_init(void)
{
  FT_Library lib;
  uint64_t e = FT_Init_FreeType(&lib);
  return (e << 32) | (uint64_t)(uintptr_t)lib;
}

EXPORT uint32_t ftw_done_lib(uint32_t lib)
{
  return FT_Done_FreeType((FT_Library)(uintptr_t)lib);
}

/* ------------------------------------------------------------------ */
/* faces                                                              */
/* ------------------------------------------------------------------ */

/* C: face_create_from_fh stream open (freetype.c:325-360) */
EXPORT uint64_t ftw_open_stream(uint32_t lib, uint64_t fh, uint64_t size)
{
  FT_Stream srec = calloc(1, sizeof(FT_StreamRec));
  FT_Open_Args oa;
  FT_Face face;
  uint64_t e;

  srec->size = size;
  srec->descriptor.pointer = (void *)(uintptr_t)fh;
  srec->read = glue_face_read;
  srec->close = glue_face_close;

  memset(&oa, 0, sizeof(oa));
  oa.stream = srec;
  oa.flags = FT_OPEN_STREAM;

  e = FT_Open_Face((FT_Library)(uintptr_t)lib, &oa, 0, &face);
  if (e) {
    free(srec); /* C: fh leaks on the Go side too — mirrored */
    return e << 32;
  }
  return (uint64_t)(uintptr_t)face;
}

/* C: face_create_from_mem FT_OPEN_MEMORY (freetype.c:367-380) */
EXPORT uint64_t ftw_open_memory(uint32_t lib, uint32_t base, uint64_t size)
{
  FT_Open_Args oa;
  FT_Face face;
  uint64_t e;

  memset(&oa, 0, sizeof(oa));
  oa.flags = FT_OPEN_MEMORY;
  oa.memory_base = (const FT_Byte *)(uintptr_t)base;
  oa.memory_size = size;

  e = FT_Open_Face((FT_Library)(uintptr_t)lib, &oa, 0, &face);
  if (e)
    return e << 32;
  return (uint64_t)(uintptr_t)face;
}

EXPORT uint32_t ftw_alloc(uint64_t n) { return (uint32_t)(uintptr_t)malloc((size_t)n); }
EXPORT void ftw_free(uint32_t p)    { free((void *)(uintptr_t)p); }

EXPORT uint32_t ftw_select_charmap(uint32_t f, uint32_t enc)
{
  return FT_Select_Charmap((FT_Face)(uintptr_t)f, (FT_Encoding)enc);
}
EXPORT uint32_t ftw_get_char_index(uint32_t f, uint32_t uc)
{
  return FT_Get_Char_Index((FT_Face)(uintptr_t)f, uc);
}
EXPORT uint32_t ftw_load_glyph(uint32_t f, uint32_t gi, int32_t flags)
{
  return FT_Load_Glyph((FT_Face)(uintptr_t)f, gi, flags);
}
EXPORT void ftw_done_face(uint32_t f)
{
  FT_Done_Face((FT_Face)(uintptr_t)f);
}

/* C: face_set_size FT_Size_RequestRec (freetype.c:565-570) */
/* Extension: on bitmap-only faces (CBDT emoji) FT_Request_Size fails —
   fall back to nearest-strike selection via FT_Match_Size/FT_Select_Size. */
EXPORT void ftw_request_size(uint32_t f, int32_t size)
{
  FT_Size_RequestRec req;
  FT_Face face = (FT_Face)(uintptr_t)f;
  memset(&req, 0, sizeof(req));
  req.type = FT_SIZE_REQUEST_TYPE_REAL_DIM;
  req.width = 0;
  req.height = size << 6;
  req.horiResolution = 0;
  req.vertResolution = 0;
  if (FT_Request_Size(face, &req) && face->num_fixed_sizes > 0) {
    /* nearest strike by y_ppem (FT_Match_Size is internal API) */
    FT_Long want = size << 6;
    int best = 0;
    FT_Long bestd = 0x7fffffff;
    for (int i = 0; i < face->num_fixed_sizes; i++) {
      FT_Long d = face->available_sizes[i].y_ppem - want;
      if (d < 0)
        d = -d;
      if (d < bestd) {
        bestd = d;
        best = i;
      }
    }
    FT_Select_Size(face, best);
  }
}

/* C: FT_HAS_KERNING (freetype.c:649) */
EXPORT int32_t ftw_has_kerning(uint32_t f)
{
  return FT_HAS_KERNING((FT_Face)(uintptr_t)f);
}

/* face->family_name / style_name / descender / units_per_EM / y_scale */
EXPORT uint32_t ftw_face_family(uint32_t f)
{
  return (uint32_t)(uintptr_t)((FT_Face)(uintptr_t)f)->family_name;
}
EXPORT uint32_t ftw_face_style(uint32_t f)
{
  return (uint32_t)(uintptr_t)((FT_Face)(uintptr_t)f)->style_name;
}
EXPORT int32_t ftw_face_descender(uint32_t f)
{
  return ((FT_Face)(uintptr_t)f)->descender;
}
EXPORT int32_t ftw_face_upem(uint32_t f)
{
  return (int32_t)((FT_Face)(uintptr_t)f)->units_per_EM;
}
EXPORT int32_t ftw_face_yscale(uint32_t f)
{
  return (int32_t)((FT_Face)(uintptr_t)f)->size->metrics.y_scale;
}

/* ------------------------------------------------------------------ */
/* glyph slot                                                         */
/* ------------------------------------------------------------------ */

EXPORT uint32_t ftw_face_glyph(uint32_t f)
{
  return (uint32_t)(uintptr_t)((FT_Face)(uintptr_t)f)->glyph;
}
EXPORT void ftw_slot_oblique(uint32_t s)
{
  FT_GlyphSlot_Oblique((FT_GlyphSlot)(uintptr_t)s);
}
EXPORT int64_t ftw_mulfix(int64_t a, int64_t b)
{
  return (int64_t)FT_MulFix(a, b);
}
EXPORT uint32_t ftw_slot_format(uint32_t s)
{
  return ((FT_GlyphSlot)(uintptr_t)s)->format;
}
EXPORT int64_t ftw_slot_advance_x(uint32_t s)
{
  return (int64_t)((FT_GlyphSlot)(uintptr_t)s)->advance.x;
}
EXPORT uint32_t ftw_slot_face(uint32_t s)
{
  return (uint32_t)(uintptr_t)((FT_GlyphSlot)(uintptr_t)s)->face;
}

/* C: FT_Outline_Embolden(&slot->outline, v) (freetype.c:622) */
EXPORT void ftw_slot_embolden(uint32_t s, int64_t v)
{
  FT_Outline_Embolden(&((FT_GlyphSlot)(uintptr_t)s)->outline, (FT_Pos)v);
}

/* ------------------------------------------------------------------ */
/* glyph objects                                                      */
/* ------------------------------------------------------------------ */

EXPORT uint64_t ftw_get_glyph(uint32_t s)
{
  FT_Glyph g;
  uint64_t e = FT_Get_Glyph((FT_GlyphSlot)(uintptr_t)s, &g);
  return (e << 32) | (uint64_t)(uintptr_t)g;
}

/* writes into the module's scratch cell; Go reads 16B at ftw_scratch() */
static FT_BBox scratch_bbox;
static FT_Vector scratch_vec;

EXPORT void ftw_glyph_cbox(uint32_t g, uint32_t mode)
{
  FT_Glyph_Get_CBox((FT_Glyph)(uintptr_t)g, mode, &scratch_bbox);
}
EXPORT uint32_t ftw_scratch(void) { return (uint32_t)(uintptr_t)&scratch_bbox; }

/* in/out glyph pointer + error, packed (err<<32)|newptr */
EXPORT uint64_t ftw_glyph_stroke_border(uint32_t g, uint32_t s,
                                        int32_t inside, int32_t destroy)
{
  FT_Glyph gg = (FT_Glyph)(uintptr_t)g;
  uint64_t e = FT_Glyph_StrokeBorder(&gg, (FT_Stroker)(uintptr_t)s,
                                     inside, destroy);
  return (e << 32) | (uint64_t)(uintptr_t)gg;
}

EXPORT uint64_t ftw_glyph_to_bitmap(uint32_t g, int32_t mode,
                                    int64_t ox, int64_t oy, int32_t destroy)
{
  FT_Glyph gg = (FT_Glyph)(uintptr_t)g;
  FT_Vector v; v.x = ox; v.y = oy;
  uint64_t e = FT_Glyph_To_Bitmap(&gg, (FT_Render_Mode)mode, &v, destroy);
  return (e << 32) | (uint64_t)(uintptr_t)gg;
}

/* variant with explicit NULL origin */
EXPORT uint64_t ftw_glyph_to_bitmap0(uint32_t g, int32_t mode, int32_t destroy)
{
  FT_Glyph gg = (FT_Glyph)(uintptr_t)g;
  uint64_t e = FT_Glyph_To_Bitmap(&gg, (FT_Render_Mode)mode, NULL, destroy);
  return (e << 32) | (uint64_t)(uintptr_t)gg;
}

EXPORT void ftw_done_glyph(uint32_t g)
{
  FT_Done_Glyph((FT_Glyph)(uintptr_t)g);
}

/* C: FT_Get_Kerning — delta into scratch_vec */
EXPORT uint32_t ftw_get_kerning(uint32_t f, uint32_t prev, uint32_t gi,
                                uint32_t mode)
{
  return FT_Get_Kerning((FT_Face)(uintptr_t)f, prev, gi, mode, &scratch_vec);
}
EXPORT uint32_t ftw_scratch_vec(void) { return (uint32_t)(uintptr_t)&scratch_vec; }

/* ((FT_BitmapGlyph)g)->left/top/&bitmap */
EXPORT int32_t ftw_bg_left(uint32_t g)
{
  return ((FT_BitmapGlyph)(uintptr_t)g)->left;
}
EXPORT int32_t ftw_bg_top(uint32_t g)
{
  return ((FT_BitmapGlyph)(uintptr_t)g)->top;
}
EXPORT int32_t ftw_bmp_width(uint32_t g)
{
  return (int32_t)((FT_BitmapGlyph)(uintptr_t)g)->bitmap.width;
}
EXPORT int32_t ftw_bmp_rows(uint32_t g)
{
  return (int32_t)((FT_BitmapGlyph)(uintptr_t)g)->bitmap.rows;
}
EXPORT uint32_t ftw_bmp_buffer(uint32_t g)
{
  return (uint32_t)(uintptr_t)((FT_BitmapGlyph)(uintptr_t)g)->bitmap.buffer;
}
EXPORT int32_t ftw_bmp_pitch(uint32_t g)
{
  return ((FT_BitmapGlyph)(uintptr_t)g)->bitmap.pitch;
}
EXPORT int32_t ftw_bmp_pixel_mode(uint32_t g)
{
  return (int32_t)((FT_BitmapGlyph)(uintptr_t)g)->bitmap.pixel_mode;
}
EXPORT int32_t ftw_has_color(uint32_t f)
{
  return FT_HAS_COLOR((FT_Face)(uintptr_t)f);
}

/* bitmap strike inspection — extension for color emoji faces */
EXPORT int32_t ftw_num_fixed_sizes(uint32_t f)
{
  return ((FT_Face)(uintptr_t)f)->num_fixed_sizes;
}
EXPORT int32_t ftw_strike_size(uint32_t f, int32_t i)
{
  FT_Face face = (FT_Face)(uintptr_t)f;
  return (int32_t)face->available_sizes[i].size;
}
EXPORT int32_t ftw_select_size(uint32_t f, int32_t i)
{
  return FT_Select_Size((FT_Face)(uintptr_t)f, i);
}
EXPORT int32_t ftw_req_size_err(uint32_t f, int32_t size)
{
  FT_Size_RequestRec req;
  memset(&req, 0, sizeof(req));
  req.type = FT_SIZE_REQUEST_TYPE_REAL_DIM;
  req.width = 0;
  req.height = size << 6;
  req.horiResolution = 0;
  req.vertResolution = 0;
  return FT_Request_Size((FT_Face)(uintptr_t)f, &req);
}

/* ------------------------------------------------------------------ */
/* stroker + outline (image rasterizer)                               */
/* ------------------------------------------------------------------ */

EXPORT uint64_t ftw_stroker_create(uint32_t lib)
{
  FT_Stroker s;
  uint64_t e = FT_Stroker_New((FT_Library)(uintptr_t)lib, &s);
  return (e << 32) | (uint64_t)(uintptr_t)s;
}
EXPORT void ftw_stroker_set(uint32_t s, int64_t radius,
                            uint32_t cap, uint32_t join, int64_t miter)
{
  FT_Stroker_Set((FT_Stroker)(uintptr_t)s, radius,
                 (FT_Stroker_LineCap)cap, (FT_Stroker_LineJoin)join,
                 miter);
}
EXPORT void ftw_stroker_rewind(uint32_t s)
{
  FT_Stroker_Rewind((FT_Stroker)(uintptr_t)s);
}
EXPORT void ftw_stroker_begin(uint32_t s, int64_t x, int64_t y, int32_t open)
{
  FT_Vector v; v.x = x; v.y = y;
  FT_Stroker_BeginSubPath((FT_Stroker)(uintptr_t)s, &v, open);
}
EXPORT void ftw_stroker_lineto(uint32_t s, int64_t x, int64_t y)
{
  FT_Vector v; v.x = x; v.y = y;
  FT_Stroker_LineTo((FT_Stroker)(uintptr_t)s, &v);
}
EXPORT void ftw_stroker_cubicto(uint32_t s, int64_t x1, int64_t y1,
                                int64_t x2, int64_t y2, int64_t x3, int64_t y3)
{
  FT_Vector a, b, c;
  a.x = x1; a.y = y1; b.x = x2; b.y = y2; c.x = x3; c.y = y3;
  FT_Stroker_CubicTo((FT_Stroker)(uintptr_t)s, &a, &b, &c);
}
EXPORT void ftw_stroker_endsubpath(uint32_t s)
{
  FT_Stroker_EndSubPath((FT_Stroker)(uintptr_t)s);
}
EXPORT void ftw_stroker_done(uint32_t s)
{
  FT_Stroker_Done((FT_Stroker)(uintptr_t)s);
}

/* (points << 32) | contours */
EXPORT uint64_t ftw_stroker_get_counts(uint32_t s)
{
  FT_UInt p = 0, c = 0;
  FT_Stroker_GetCounts((FT_Stroker)(uintptr_t)s, &p, &c);
  return ((uint64_t)p << 32) | c;
}
EXPORT uint64_t ftw_stroker_get_border_counts(uint32_t s, uint32_t border)
{
  FT_UInt p = 0, c = 0;
  FT_Stroker_GetBorderCounts((FT_Stroker)(uintptr_t)s,
                             (FT_StrokerBorder)border, &p, &c);
  return ((uint64_t)p << 32) | c;
}
EXPORT void ftw_stroker_export(uint32_t s, uint32_t ol)
{
  FT_Stroker_Export((FT_Stroker)(uintptr_t)s, (FT_Outline *)(uintptr_t)ol);
}
EXPORT void ftw_stroker_export_border(uint32_t s, uint32_t border, uint32_t ol)
{
  FT_Stroker_ExportBorder((FT_Stroker)(uintptr_t)s,
                          (FT_StrokerBorder)border,
                          (FT_Outline *)(uintptr_t)ol);
}

/* ------------------------------------------------------------------ */
/* outline + gray spans                                               */
/* ------------------------------------------------------------------ */

static void glue_gray_spans(int y, int count, const FT_Span *spans, void *user)
{
  w2g_gray_spans(y, count, (uint32_t)(uintptr_t)spans, (uint64_t)(uintptr_t)user);
}

EXPORT uint64_t ftw_outline_new(uint32_t lib, uint32_t points, int32_t contours)
{
  FT_Outline *ol = calloc(1, sizeof(FT_Outline));
  uint64_t e = FT_Outline_New((FT_Library)(uintptr_t)lib, points,
                              contours, ol);
  if (e) { free(ol); return e << 32; }
  return (uint64_t)(uintptr_t)ol;
}
EXPORT void ftw_outline_clear(uint32_t ol)
{
  FT_Outline *o = (FT_Outline *)(uintptr_t)ol;
  o->n_contours = 0;
  o->n_points = 0;
}
EXPORT void ftw_outline_render(uint32_t lib, uint32_t ol)
{
  FT_Raster_Params params;
  memset(&params, 0, sizeof(params));
  params.flags = FT_RASTER_FLAG_AA | FT_RASTER_FLAG_DIRECT;
  params.gray_spans = glue_gray_spans;
  params.user = NULL;
  FT_Outline_Render((FT_Library)(uintptr_t)lib,
                    (FT_Outline *)(uintptr_t)ol, &params);
}
EXPORT void ftw_outline_done(uint32_t lib, uint32_t ol)
{
  FT_Outline_Done((FT_Library)(uintptr_t)lib, (FT_Outline *)(uintptr_t)ol);
  free((void *)(uintptr_t)ol);
}
