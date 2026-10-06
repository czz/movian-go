/*
 * vc_gencmd_shim.h — vc_gencmd is variadic (fmt, ...); cgo cannot call
 * variadic C functions. Fixed-arity wrappers forwarding to the C API.
 */
#pragma once

#include <interface/vmcs_host/vc_vchi_gencmd.h>

static inline int vc_gencmd0(char *response, int maxlen, const char *fmt) {
	return vc_gencmd(response, maxlen, fmt);
}

static inline int vc_gencmd1(char *response, int maxlen, const char *fmt,
			     int arg) {
	return vc_gencmd(response, maxlen, fmt, arg);
}

/* TV_DISPLAY_STATE_T.display is a union (opaque to cgo) — accessors. */
#include <interface/vmcs_host/vc_tvservice.h>
static inline HDMI_DISPLAY_STATE_T *tv_hdmi_(TV_DISPLAY_STATE_T *s) {
	return &s->display.hdmi;
}

/* TV_SUPPORTED_MODE_NEW_T uses C bitfields (invisible to cgo). */
static inline uint32_t tvmode_scan_mode_(TV_SUPPORTED_MODE_NEW_T *m){ return m->scan_mode; }
static inline uint32_t tvmode_native_(TV_SUPPORTED_MODE_NEW_T *m){ return m->native; }
static inline uint32_t tvmode_code_(TV_SUPPORTED_MODE_NEW_T *m){ return m->code; }
static inline uint32_t tvmode_aspect_(TV_SUPPORTED_MODE_NEW_T *m){ return m->aspect_ratio; }
static inline uint16_t tvmode_frame_rate_(TV_SUPPORTED_MODE_NEW_T *m){ return m->frame_rate; }
static inline uint16_t tvmode_width_(TV_SUPPORTED_MODE_NEW_T *m){ return m->width; }
static inline uint16_t tvmode_height_(TV_SUPPORTED_MODE_NEW_T *m){ return m->height; }
