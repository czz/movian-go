/*
 * fcglue.c — wasm32 glue exports for fontconfig.
 *
 * Mirrors the Fc* call surface used by freetype.c's fontconfig.c
 * (Go: internal/text/fontconfig.go). Handle+result pairs are packed
 * u64: (err_or_result << 32) | ptr. Pattern objects are selected by
 * an integer to avoid exporting symbol addresses.
 */

#include <fontconfig/fontconfig.h>
#include <stdint.h>
#include <string.h>

/* object selector */
enum {
  FCW_OBJ_FAMILY = 0,   /* FC_FAMILY   */
  FCW_OBJ_OUTLINE,      /* FC_OUTLINE  */
  FCW_OBJ_WEIGHT,       /* FC_WEIGHT   */
  FCW_OBJ_SLANT,        /* FC_SLANT    */
  FCW_OBJ_SCALABLE,     /* FC_SCALABLE */
  FCW_OBJ_CHARSET,      /* FC_CHARSET  */
  FCW_OBJ_FILE,         /* FC_FILE     */
};

static const char *
fcw_obj_name (int sel)
{
  switch (sel) {
  case FCW_OBJ_FAMILY:   return FC_FAMILY;
  case FCW_OBJ_OUTLINE:  return FC_OUTLINE;
  case FCW_OBJ_WEIGHT:   return FC_WEIGHT;
  case FCW_OBJ_SLANT:    return FC_SLANT;
  case FCW_OBJ_SCALABLE: return FC_SCALABLE;
  case FCW_OBJ_CHARSET:  return FC_CHARSET;
  case FCW_OBJ_FILE:     return FC_FILE;
  }
  return "family";
}

/* C: fontconfig_init — FcInitLoadConfig + FcConfigBuildFonts.
 * Returns (err<<32)|config: err=1 when config can't be built. */
uint64_t
fcw_init (void)
{
  FcConfig *conf = FcInitLoadConfig ();
  if (!conf)
    return 1ULL << 32;
  FcConfigBuildFonts (conf);
  return (uint64_t) (uintptr_t) conf;
}

/* patterns */
uint32_t fcw_pat_create (void) { return (uint32_t) (uintptr_t) FcPatternCreate (); }
void fcw_pat_destroy (uint32_t p) { FcPatternDestroy ((FcPattern *) (uintptr_t) p); }

uint32_t
fcw_pat_add_str (uint32_t p, int32_t sel, uint32_t s)
{
  return FcPatternAddString ((FcPattern *) (uintptr_t) p,
			     fcw_obj_name (sel),
			     (const FcChar8 *) (uintptr_t) s);
}
uint32_t
fcw_pat_add_bool (uint32_t p, int32_t sel, int32_t v)
{
  return FcPatternAddBool ((FcPattern *) (uintptr_t) p,
			   fcw_obj_name (sel), v);
}
uint32_t
fcw_pat_add_int (uint32_t p, int32_t sel, int32_t v)
{
  return FcPatternAddInteger ((FcPattern *) (uintptr_t) p,
			      fcw_obj_name (sel), v);
}

uint64_t
fcw_pat_get_bool (uint32_t p, int32_t sel, int32_t n)
{
  FcBool b = FcFalse;
  FcResult r = FcPatternGetBool ((FcPattern *) (uintptr_t) p,
				fcw_obj_name (sel), n, &b);
  return ((uint64_t) r << 32) | (uint64_t) b;
}

uint64_t
fcw_pat_get_charset (uint32_t p, int32_t sel, int32_t n)
{
  FcCharSet *cs = NULL;
  FcResult r = FcPatternGetCharSet ((FcPattern *) (uintptr_t) p,
				    fcw_obj_name (sel), n, &cs);
  return ((uint64_t) r << 32) | (uint64_t) (uintptr_t) cs;
}

uint64_t
fcw_pat_get_string (uint32_t p, int32_t sel, int32_t n)
{
  FcChar8 *s = NULL;
  FcResult r = FcPatternGetString ((FcPattern *) (uintptr_t) p,
				   fcw_obj_name (sel), n, &s);
  return ((uint64_t) r << 32) | (uint64_t) (uintptr_t) s;
}

uint32_t
fcw_charset_has_char (uint32_t cs, uint32_t uc)
{
  return FcCharSetHasChar ((const FcCharSet *) (uintptr_t) cs, uc);
}

void fcw_default_substitute (uint32_t p)
{
  FcDefaultSubstitute ((FcPattern *) (uintptr_t) p);
}

uint32_t
fcw_config_substitute (uint32_t conf, uint32_t p, int32_t kind)
{
  return FcConfigSubstitute ((FcConfig *) (uintptr_t) conf,
			     (FcPattern *) (uintptr_t) p,
			     (FcMatchKind) kind);
}

/* C: FcFontSort (config, pat, FcTrue, NULL, &result) */
uint64_t
fcw_font_sort (uint32_t conf, uint32_t p, int32_t trim)
{
  FcResult result;
  FcFontSet *fs = FcFontSort ((FcConfig *) (uintptr_t) conf,
			      (FcPattern *) (uintptr_t) p, trim,
			      NULL, &result);
  return ((uint64_t) result << 32) | (uint64_t) (uintptr_t) fs;
}

/* FcFontSet access */
int32_t fcw_fs_nfont (uint32_t fs)
{
  FcFontSet *s = (FcFontSet *) (uintptr_t) fs;
  return s ? s->nfont : 0;
}
uint32_t fcw_fs_font (uint32_t fs, int32_t i)
{
  FcFontSet *s = (FcFontSet *) (uintptr_t) fs;
  return (uint32_t) (uintptr_t) s->fonts[i];
}
void fcw_fs_destroy (uint32_t fs)
{
  FcFontSetDestroy ((FcFontSet *) (uintptr_t) fs);
}
