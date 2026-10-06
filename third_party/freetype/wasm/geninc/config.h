/* config.h — wasm32/wasi build of fontconfig 2.15.0.
 * Hand-crafted: mirrors a stock Debian autotools/meson configure
 * result adapted to the WASI target. Paths match Debian defaults.
 */

#define PACKAGE_NAME "fontconfig"
#define PACKAGE_TARNAME "fontconfig"
#define PACKAGE_VERSION "2.15.0"
#define PACKAGE_STRING "fontconfig 2.15.0"
#define PACKAGE_BUGREPORT "https://gitlab.freedesktop.org/fontconfig/fontconfig/issues/new"
#define PACKAGE_URL ""
#define VERSION "2.15.0"
#define GETTEXT_PACKAGE "fontconfig"
#define LT_OBJDIR ".libs/"

/* types / ABI (ILP32) */
#define SIZEOF_CHAR 1
#define SIZEOF_SHORT 2
#define SIZEOF_INT 4
#define SIZEOF_LONG 4
#define SIZEOF_VOIDP 4
#define SIZEOF_VOID_P 4
#define ALIGNOF_DOUBLE 8
#define ALIGNOF_VOID_P 4
#define FLEXIBLE_ARRAY_MEMBER /**/
#define FC_GPERF_SIZE_T size_t
#define FC_ARCHITECTURE "wasm32"

/* headers available in wasi-libc */
#define STDC_HEADERS 1
#define HAVE_DIRENT_H 1
#define HAVE_FCNTL_H 1
#define HAVE_INTTYPES_H 1
#define HAVE_STDINT_H 1
#define HAVE_STDIO_H 1
#define HAVE_STDLIB_H 1
#define HAVE_STRINGS_H 1
#define HAVE_STRING_H 1
#define HAVE_SYS_PARAM_H 1
#define HAVE_SYS_STAT_H 1
#define HAVE_SYS_TYPES_H 1
#define HAVE_UNISTD_H 1
#define HAVE_WCHAR_H 1
#define HAVE_SCHED_H 1

/* functions available in wasi-libc */
#define HAVE_GETPAGESIZE 1
#define HAVE_LINK 1
#define HAVE_LRAND48 1
#define HAVE_LSTAT 1

#define HAVE_POSIX_FADVISE 1
#define HAVE_RAND 1
#define HAVE_RAND_R 1
#define HAVE_RANDOM 1
#define HAVE_READLINK 1
#define HAVE_SCHED_YIELD 1
#define HAVE_STRERROR 1
#define HAVE_STRERROR_R 1
#define HAVE_VPRINTF 1
#define HAVE_GETOPT 1
#define HAVE_GETOPT_LONG 1

/* struct members */
#define HAVE_STRUCT_DIRENT_D_TYPE 1
#define HAVE_STRUCT_STAT_ST_MTIM 1

/* features intentionally off */
/* #undef USE_ICONV */
/* #undef ENABLE_LIBXML2 */
/* #undef ENABLE_NLS */
/* #undef HAVE_PTHREAD */

/* Debian-matching install paths (fontconfig.conf defaults) */
#define FONTCONFIG_PATH "/etc/fonts"
#define CONFIGDIR "/etc/fonts/conf.d"
#define FC_TEMPLATEDIR "/usr/share/fontconfig/conf.avail"
#define FC_CACHEDIR "/var/cache/fontconfig"
#define FC_DEFAULT_FONTS "\\t<dir>/usr/share/fonts</dir>\\n\\t<dir>/usr/local/share/fonts</dir>\\n\\t<dir>/system/fonts</dir>\\n\\t<dir>/system/product/fonts</dir>\\n\\t<dir>C:/Windows/Fonts</dir>\\n"
#define FC_FONTPATH ""

#define HAVE_FT_DONE_MM_VAR 1
#define HAVE_FT_GET_BDF_PROPERTY 1
#define HAVE_FT_GET_PS_FONT_INFO 1
#define HAVE_FT_HAS_PS_GLYPH_NAMES 1
#define HAVE_FT_GET_X11_FONT_FORMAT 1

#define HAVE_INTEL_ATOMIC_PRIMITIVES 1
#define HAVE_STDATOMIC_PRIMITIVES 1
