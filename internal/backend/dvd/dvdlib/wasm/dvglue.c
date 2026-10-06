/*
 * dvglue.c — wasm2go glue for the vendored ext/dvd sources
 * (libdvdcss + libdvdread + libdvdnav). Mirror of the cgo preamble
 * in ../dvdlib.go: same svfs_ops trampolines (via w2g host imports
 * instead of //export callbacks) and the same thin wrap_* helpers,
 * exported as dvw_* so Go can call them.
 *
 * All out-params go through one scratch cell exposed by
 * dvw_scratch(); the layout per call is documented at each export.
 */

#include <stdarg.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>

#include "dvdnav/dvdnav.h"
#include "fileaccess/svfs.h"

#define EXPORT __attribute__((used, visibility("default")))

/* ------------------------------------------------------------------ */
/* host imports (Go) — the fa_* bridge, C: struct svfs_ops faops       */
/* ------------------------------------------------------------------ */

__attribute__((import_module("w2g"), import_name("fa_open")))
extern uint64_t w2g_fa_open(uint32_t url);
__attribute__((import_module("w2g"), import_name("fa_close")))
extern void w2g_fa_close(uint64_t fh);
__attribute__((import_module("w2g"), import_name("fa_read")))
extern int32_t w2g_fa_read(uint64_t fh, uint32_t buf, int32_t size);
__attribute__((import_module("w2g"), import_name("fa_seek")))
extern int64_t w2g_fa_seek(uint64_t fh, int64_t pos, int32_t whence);
__attribute__((import_module("w2g"), import_name("fa_stat")))
extern int32_t w2g_fa_stat(uint32_t url, uint32_t out); /* out: {i64 size, i32 isdir, i64 mtime} */
__attribute__((import_module("w2g"), import_name("fa_findfile")))
extern int32_t w2g_fa_findfile(uint32_t path, uint32_t file,
                               uint32_t fullpath, int32_t len);
__attribute__((import_module("w2g"), import_name("dvd_trace")))
extern void w2g_dvd_trace(int32_t level, uint32_t subsys, uint32_t msg);

/* TRACE() sink — the bundled libs call dvdlib_trace (main.h shim) */
void dvdlib_trace(int level, const char *subsys, const char *fmt, ...)
{
	char buf[1024];
	va_list ap;
	va_start(ap, fmt);
	vsnprintf(buf, sizeof(buf), fmt, ap);
	va_end(ap);
	w2g_dvd_trace(level, (uint32_t)(uintptr_t)subsys,
	              (uint32_t)(uintptr_t)buf);
}

/* ------------------------------------------------------------------ */
/* svfs_ops trampolines → w2g fa_* bridge                              */
/* ------------------------------------------------------------------ */

static struct { int64_t size; int32_t isdir; int64_t mtime; } st_scratch;

static void *faOpenTramp(const char *url) {
	return (void *)(uintptr_t)w2g_fa_open((uint32_t)(uintptr_t)url);
}
static void faCloseTramp(void *fh) {
	w2g_fa_close((uint64_t)(uintptr_t)fh);
}
static int faReadTramp(void *fh, void *buf, size_t size) {
	return w2g_fa_read((uint64_t)(uintptr_t)fh,
	                   (uint32_t)(uintptr_t)buf, (int32_t)size);
}
static int64_t faSeekTramp(void *fh, int64_t pos, int whence) {
	return w2g_fa_seek((uint64_t)(uintptr_t)fh, pos, whence);
}
static int faStatTramp(const char *url, struct stat *st) {
	if (w2g_fa_stat((uint32_t)(uintptr_t)url,
	                (uint32_t)(uintptr_t)&st_scratch))
		return -1;
	memset(st, 0, sizeof(*st));
	st->st_size = st_scratch.size;
	st->st_mode = st_scratch.isdir ? S_IFDIR : S_IFREG;
	st->st_mtime = st_scratch.mtime;
	return 0;
}
static int faFindfileTramp(const char *path, const char *file,
			   char *fullpath, size_t fullpathlen) {
	return w2g_fa_findfile((uint32_t)(uintptr_t)path,
	                       (uint32_t)(uintptr_t)file,
	                       (uint32_t)(uintptr_t)fullpath,
	                       (int32_t)fullpathlen);
}

static struct svfs_ops faops = {
	.open     = faOpenTramp,
	.close    = faCloseTramp,
	.read     = faReadTramp,
	.seek     = faSeekTramp,
	.stat     = faStatTramp,
	.findfile = faFindfileTramp,
};

/* ------------------------------------------------------------------ */
/* scratch cell for out-params (Go reads via dvw_scratch + Mem())      */
/* ------------------------------------------------------------------ */

static union {
	uint32_t u32[8];
	int32_t  i32[8];
	uint64_t u64[4];
	uint8_t  bytes[64];
} scratch;

EXPORT uint32_t dvw_scratch(void)
{
	return (uint32_t)(uintptr_t)&scratch;
}

/* ------------------------------------------------------------------ */
/* dvdnav lifecycle                                                    */
/* ------------------------------------------------------------------ */

/* (st<<32)|handle */
EXPORT uint64_t dvw_open(uint32_t path, int32_t vfs)
{
	dvdnav_t *d = NULL;
	dvdnav_status_t st = dvdnav_open(&d,
	    (const char *)(uintptr_t)path, vfs ? &faops : NULL);
	return ((uint64_t)(uint32_t)st << 32) | (uint32_t)(uintptr_t)d;
}
EXPORT int32_t dvw_close(uint32_t d)
{
	return dvdnav_close((dvdnav_t *)(uintptr_t)d);
}

/* scratch: {u32 buf, i32 event, i32 len}; returns status */

/* C callers pass &buf where buf still points at the previous cache
 * block (lib-owned, stays valid after free_cache_block) — the lib
 * writes event payloads (highlight, clut, ...) into *buf and reads
 * NAV packets into it.  A NULL *buf makes DVDReadBlocks refuse and
 * makes the highlight path write through a NULL pointer (crash).
 * Keep a persistent default buffer replicating that contract. */
static uint8_t *dvw_prev_block;

EXPORT int32_t dvw_gncb(uint32_t d)
{
	uint8_t *buf = dvw_prev_block;
	int32_t event = 0, len = 0;
	if (buf == NULL)
		buf = dvw_prev_block = calloc(1, DVD_VIDEO_LB_LEN);
	dvdnav_status_t st = dvdnav_get_next_cache_block(
	    (dvdnav_t *)(uintptr_t)d, &buf, &event, &len);
	dvw_prev_block = buf;
	scratch.u32[0] = (uint32_t)(uintptr_t)buf;
	scratch.i32[1] = event;
	scratch.i32[2] = len;
	return st;
}
EXPORT void dvw_free_cache_block(uint32_t d, uint32_t buf)
{
	dvdnav_free_cache_block((dvdnav_t *)(uintptr_t)d,
	                        (uint8_t *)(uintptr_t)buf);
}

/* scratch: {i32 title, i32 part}; returns status */
EXPORT int32_t dvw_current_title_info(uint32_t d)
{
	int32_t title = 0, part = 0;
	dvdnav_status_t st = dvdnav_current_title_info(
	    (dvdnav_t *)(uintptr_t)d, &title, &part);
	scratch.i32[0] = title;
	scratch.i32[1] = part;
	return st;
}

/* scratch: u32 char* ; returns status */
EXPORT int32_t dvw_title_string(uint32_t d)
{
	const char *t = NULL;
	dvdnav_status_t st = dvdnav_get_title_string(
	    (dvdnav_t *)(uintptr_t)d, &t);
	scratch.u32[0] = (uint32_t)(uintptr_t)t;
	return st;
}

/* scratch: i32 count; returns status */
EXPORT int32_t dvw_num_parts(uint32_t d, int32_t title)
{
	int32_t parts = 0;
	dvdnav_status_t st = dvdnav_get_number_of_parts(
	    (dvdnav_t *)(uintptr_t)d, title, &parts);
	scratch.i32[0] = parts;
	return st;
}
EXPORT int32_t dvw_num_titles(uint32_t d)
{
	int32_t titles = 0;
	dvdnav_status_t st = dvdnav_get_number_of_titles(
	    (dvdnav_t *)(uintptr_t)d, &titles);
	scratch.i32[0] = titles;
	return st;
}

/* times: u64 out buffer the caller pre-allocated via malloc; scratch:
 * u64 duration; returns status (chapter count comes from num_parts) */
EXPORT int32_t dvw_describe_chapters(uint32_t d, int32_t title,
                                     uint32_t times)
{
	uint64_t *t = NULL;
	uint64_t duration = 0;
	int32_t parts = dvdnav_describe_title_chapters(
	    (dvdnav_t *)(uintptr_t)d, title, &t, &duration);
	scratch.u64[0] = duration;
	if (parts > 0 && t != NULL) {
		memcpy((void *)(uintptr_t)times, t,
		       parts * sizeof(uint64_t));
		free(t);
	}
	return parts;
}

EXPORT int64_t dvw_current_time(uint32_t d)
{
	return dvdnav_get_current_time((dvdnav_t *)(uintptr_t)d);
}

/* pci_t* into the lib's state (copy it via dvw_pci_size + Mem()) */
EXPORT uint32_t dvw_nav_pci(uint32_t d)
{
	return (uint32_t)(uintptr_t)
	    dvdnav_get_current_nav_pci((dvdnav_t *)(uintptr_t)d);
}

EXPORT int32_t dvw_video_aspect(uint32_t d)
{
	return dvdnav_get_video_aspect((dvdnav_t *)(uintptr_t)d);
}

/* scratch: {i32 w, i32 h} */
EXPORT void dvw_video_resolution(uint32_t d)
{
	int32_t w = 0, h = 0;
	dvdnav_get_video_resolution((dvdnav_t *)(uintptr_t)d, &w, &h);
	scratch.i32[0] = w;
	scratch.i32[1] = h;
}

EXPORT int32_t dvw_audio_logical(uint32_t d, int32_t idx)
{
	return dvdnav_get_audio_logical_stream((dvdnav_t *)(uintptr_t)d,
	                                       idx);
}
EXPORT int32_t dvw_audio_format(uint32_t d, int32_t idx)
{
	return dvdnav_audio_stream_format((dvdnav_t *)(uintptr_t)d,
	                                  (uint8_t)idx);
}
EXPORT int32_t dvw_audio_channels(uint32_t d, int32_t idx)
{
	return dvdnav_audio_stream_channels((dvdnav_t *)(uintptr_t)d,
	                                    (uint8_t)idx);
}
EXPORT int32_t dvw_audio_lang(uint32_t d, int32_t idx)
{
	return dvdnav_audio_stream_to_lang((dvdnav_t *)(uintptr_t)d,
	                                   (uint8_t)idx);
}
EXPORT int32_t dvw_spu_logical(uint32_t d, int32_t idx)
{
	return dvdnav_get_spu_logical_stream((dvdnav_t *)(uintptr_t)d, idx);
}
EXPORT int32_t dvw_spu_lang(uint32_t d, int32_t idx)
{
	return dvdnav_spu_stream_to_lang((dvdnav_t *)(uintptr_t)d,
	                                 (uint8_t)idx);
}

EXPORT int32_t dvw_btn_activate(uint32_t d, uint32_t pci)
{
	return dvdnav_button_activate((dvdnav_t *)(uintptr_t)d,
	                              (pci_t *)(uintptr_t)pci);
}
EXPORT int32_t dvw_btn_select(uint32_t d, uint32_t pci, int32_t btn)
{
	return dvdnav_button_select((dvdnav_t *)(uintptr_t)d,
	                            (pci_t *)(uintptr_t)pci, btn);
}
EXPORT int32_t dvw_btn_select_activate(uint32_t d, uint32_t pci,
                                       int32_t btn)
{
	return dvdnav_button_select_and_activate((dvdnav_t *)(uintptr_t)d,
	    (pci_t *)(uintptr_t)pci, btn);
}
EXPORT int32_t dvw_btn_upper(uint32_t d, uint32_t pci)
{
	return dvdnav_upper_button_select((dvdnav_t *)(uintptr_t)d,
	                                  (pci_t *)(uintptr_t)pci);
}
EXPORT int32_t dvw_btn_lower(uint32_t d, uint32_t pci)
{
	return dvdnav_lower_button_select((dvdnav_t *)(uintptr_t)d,
	                                  (pci_t *)(uintptr_t)pci);
}
EXPORT int32_t dvw_btn_left(uint32_t d, uint32_t pci)
{
	return dvdnav_left_button_select((dvdnav_t *)(uintptr_t)d,
	                                 (pci_t *)(uintptr_t)pci);
}
EXPORT int32_t dvw_btn_right(uint32_t d, uint32_t pci)
{
	return dvdnav_right_button_select((dvdnav_t *)(uintptr_t)d,
	                                  (pci_t *)(uintptr_t)pci);
}

EXPORT int32_t dvw_still_skip(uint32_t d)
{
	return dvdnav_still_skip((dvdnav_t *)(uintptr_t)d);
}
EXPORT int32_t dvw_wait_skip(uint32_t d)
{
	return dvdnav_wait_skip((dvdnav_t *)(uintptr_t)d);
}
EXPORT int32_t dvw_next_pg(uint32_t d)
{
	return dvdnav_next_pg_search((dvdnav_t *)(uintptr_t)d);
}
EXPORT int32_t dvw_prev_pg(uint32_t d)
{
	return dvdnav_prev_pg_search((dvdnav_t *)(uintptr_t)d);
}
EXPORT int32_t dvw_set_readahead(uint32_t d, int32_t f)
{
	return dvdnav_set_readahead_flag((dvdnav_t *)(uintptr_t)d, f);
}
EXPORT int32_t dvw_set_pgc_pos(uint32_t d, int32_t f)
{
	return dvdnav_set_PGC_positioning_flag((dvdnav_t *)(uintptr_t)d, f);
}

EXPORT uint32_t dvw_errstr(uint32_t d)
{
	return (uint32_t)(uintptr_t)
	    dvdnav_err_to_string((dvdnav_t *)(uintptr_t)d);
}

/* ------------------------------------------------------------------ */
/* pci_t blob access — the Go side keeps pci_t as sizeof-bytes blob    */
/* (wasm32 layout); these accessors read fields off a blob pointer.    */
/* ------------------------------------------------------------------ */

EXPORT int32_t dvw_pci_size(void) { return (int32_t)sizeof(pci_t); }

EXPORT int32_t dvw_pci_hli_ss(uint32_t p)
{
	return ((pci_t *)(uintptr_t)p)->hli.hl_gi.hli_ss;
}
EXPORT int32_t dvw_pci_btn_ns(uint32_t p)
{
	return ((pci_t *)(uintptr_t)p)->hli.hl_gi.btn_ns;
}
EXPORT int32_t dvw_pci_vobu_sptm(uint32_t p)
{
	return (int32_t)((pci_t *)(uintptr_t)p)->pci_gi.vobu_s_ptm;
}
EXPORT int32_t dvw_pci_vobu_eptm(uint32_t p)
{
	return (int32_t)((pci_t *)(uintptr_t)p)->pci_gi.vobu_e_ptm;
}

/* scratch: {i32 x_start, x_end, y_start, y_end} */
EXPORT void dvw_pci_btni(uint32_t p, int32_t i)
{
	pci_t *pci = (pci_t *)(uintptr_t)p;
	scratch.i32[0] = pci->hli.btnit[i].x_start;
	scratch.i32[1] = pci->hli.btnit[i].x_end;
	scratch.i32[2] = pci->hli.btnit[i].y_start;
	scratch.i32[3] = pci->hli.btnit[i].y_end;
}

/* scratch: {u32 palette, u32 sx|sy<<16, u32 ex|ey<<16, u32 pts,
 *           u32 buttonN}; returns status */
EXPORT int32_t dvw_pci_highlight(uint32_t p, int32_t btn, int32_t mode)
{
	dvdnav_highlight_area_t ha;
	dvdnav_status_t st = dvdnav_get_highlight_area(
	    (pci_t *)(uintptr_t)p, btn, mode, &ha);
	scratch.u32[0] = ha.palette;
	scratch.u32[1] = (uint32_t)ha.sx | ((uint32_t)ha.sy << 16);
	scratch.u32[2] = (uint32_t)ha.ex | ((uint32_t)ha.ey << 16);
	scratch.u32[3] = ha.pts;
	scratch.u32[4] = ha.buttonN;
	return st;
}
