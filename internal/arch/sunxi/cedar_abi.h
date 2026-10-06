/*
 * cedar_abi.h — internal types shared between the Go port of
 * src/video/cedar.c and the C ABI shim (cedar_abi.c). Mirrors the
 * private structs from cedar.c:63-94 plus TAILQ helpers (the TAILQ_*
 * macros are not callable from cgo).
 */
#ifndef CEDAR_ABI_H
#define CEDAR_ABI_H

#include <stdint.h>
#include <string.h>
#include <stdio.h>
#include <sys/queue.h>
#include <libve.h>
#include <commom_type.h>

TAILQ_HEAD(cedar_packet_queue, cedar_packet);
TAILQ_HEAD(picture_queue, picture);

typedef struct picture {
	vpicture_t pic;  /* Must be first — C: cedar.c:69 */
	TAILQ_ENTRY(picture) link;
	int frameid;
	struct fbm *fbm;
} picture_t;

typedef struct fbm {
	int32_t refcount;
	int numpics;
	pixel_format_e fmt;

	uint64_t mutex_opaque; /* Go sync.Mutex overlaid — C: hts_mutex_t */
	struct picture_queue fbm_avail;
	struct picture_queue fbm_queued;
	struct picture_queue fbm_display;
	char fbm_name[64];

	picture_t pics[0];
} fbm_t;

typedef struct cedar_packet {
	vstream_data_t cp_vsd; /* Must be first — C: cedar.c:92 */
	TAILQ_ENTRY(cedar_packet) cp_link;
} cedar_packet_t;

/* ---- TAILQ helpers (macro wrappers) ---- */

static __inline void cpq_init(struct cedar_packet_queue *q) {
	TAILQ_INIT(q);
}
static __inline cedar_packet_t *cpq_first(struct cedar_packet_queue *q) {
	return TAILQ_FIRST(q);
}
static __inline void cpq_insert_tail(struct cedar_packet_queue *q,
				     cedar_packet_t *e) {
	TAILQ_INSERT_TAIL(q, e, cp_link);
}
static __inline void cpq_insert_head(struct cedar_packet_queue *q,
				     cedar_packet_t *e) {
	TAILQ_INSERT_HEAD(q, e, cp_link);
}
static __inline void cpq_remove(struct cedar_packet_queue *q,
				cedar_packet_t *e) {
	TAILQ_REMOVE(q, e, cp_link);
}

static __inline void picq_init(struct picture_queue *q) {
	TAILQ_INIT(q);
}
static __inline picture_t *picq_first(struct picture_queue *q) {
	return TAILQ_FIRST(q);
}
static __inline void picq_insert_tail(struct picture_queue *q,
				      picture_t *e) {
	TAILQ_INSERT_TAIL(q, e, link);
}
static __inline void picq_insert_head(struct picture_queue *q,
				      picture_t *e) {
	TAILQ_INSERT_HEAD(q, e, link);
}
static __inline void picq_remove(struct picture_queue *q, picture_t *e) {
	TAILQ_REMOVE(q, e, link);
}
static __inline int picq_count(struct picture_queue *q) {
	int cnt = 0;
	picture_t *p;
	TAILQ_FOREACH(p, q, link)
		cnt++;
	return cnt;
}

/* ---- Go exports wired into IVE/IOS/IFBM/IVBV (cedar_export.go) ---- */

extern void   cedar_ve_reset_hardware(void);
extern void   cedar_ve_enable_clock(uint8_t enable, uint32_t speed);
extern void   cedar_ve_enable_intr(uint8_t enable);
extern int32_t cedar_ve_wait_intr(void);
extern uint32_t cedar_ve_get_reg_base_addr(void);
extern memtype_e cedar_ve_get_memtype(void);

extern void  *cedar_mem_alloc(uint32_t size);
extern void   cedar_mem_free(void *p);
extern void  *cedar_mem_palloc(uint32_t size, uint32_t align);
extern void   cedar_mem_pfree(void *p);
extern void   cedar_mem_set(void *mem, uint32_t value, uint32_t size);
extern void   cedar_mem_cpy(void *dst, void *src, uint32_t size);
extern void   cedar_mem_flush_cache(uint8_t *mem, uint32_t size);
extern uint32_t cedar_mem_get_phy_addr(uint32_t virtual_addr);
extern int32_t cedar_sys_print(uint8_t *func, uint32_t line);
extern void   cedar_sys_sleep(uint32_t ms);

extern void    cedar_fbm_deinit(Handle h, void *parent);
extern vpicture_t *cedar_fbm_request_frame(Handle h);
extern void    cedar_fbm_return_frame(vpicture_t *f, uint8_t valid, Handle h);
extern void    cedar_fbm_share_frame(vpicture_t *f, Handle h);
extern Handle  cedar_fbm_init_ex(uint32_t max_frame_num, uint32_t min_frame_num,
				 uint32_t size_y[], uint32_t size_u[],
				 uint32_t size_v[], uint32_t size_alpha[],
				 _3d_mode_e _3d_mode, pixel_format_e format,
				 uint8_t unknown, void *parent);
extern Handle  cedar_fbm_init_ex_yv12(uint32_t max_frame_num,
				      uint32_t min_frame_num,
				      uint32_t size_y[], uint32_t size_u[],
				      uint32_t size_v[], uint32_t size_alpha[],
				      _3d_mode_e _3d_mode, pixel_format_e format,
				      uint8_t unknown, void *parent);
extern Handle  cedar_fbm_init_ex_yv32(uint32_t max_frame_num,
				      uint32_t min_frame_num,
				      uint32_t size_y[], uint32_t size_u[],
				      uint32_t size_v[], uint32_t size_alpha[],
				      _3d_mode_e _3d_mode, pixel_format_e format,
				      uint8_t unknown, void *parent);
extern void    cedar_fbm_flush_frame(Handle h, s64 pts);
extern void    cedar_fbm_print_status(Handle h);
extern void    cedar_fbm_alloc_yv12_frame_buffer(Handle h);

extern vstream_data_t *cedar_vbv_request_stream_frame(Handle vbv);
extern void   cedar_vbv_return_stream_frame(vstream_data_t *stream, Handle vbv);
extern void   cedar_vbv_flush_stream_frame(vstream_data_t *stream, Handle vbv);
extern uint8_t *cedar_vbv_get_base_addr(Handle vbv);
extern uint32_t cedar_vbv_get_buffer_size(Handle vbv);

#endif /* CEDAR_ABI_H */

/* flexible-array accessor — fbm_t.pics is not visible to cgo */
static __inline picture_t *fbm_pics(fbm_t *f) { return f->pics; }
