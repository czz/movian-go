package libav

/*
#include <libavutil/pixfmt.h>
#include <libswscale/swscale.h>
#include <stdint.h>

// Helper: call sws_scale with a single source plane.
// Constructs srcSlice[4] and srcStride[4] in C stack memory,
// avoiding the need for the caller to pass a Go array of Go pointers.
static int ml_sws_scale(struct SwsContext *ctx,
                        const uint8_t *src, int srcStride,
                        int srcSliceY, int srcSliceH,
                        uint8_t *const dst[], const int dstStride[]) {
    const uint8_t *const srcSlice[4] = {src, NULL, NULL, NULL};
    const int srcStrides[4] = {srcStride, 0, 0, 0};
    return sws_scale(ctx, srcSlice, srcStrides, srcSliceY, srcSliceH, dst, dstStride);
}
*/
import "C"

import (
	"unsafe"
)

// SwsContext — C: struct SwsContext * (libswscale scaling context).
// Opaque handle wrapping the C pointer.
type SwsContext struct {
	ptr unsafe.Pointer
}

// Ptr — raw *SwsContext for C call sites.
func (s *SwsContext) Ptr() unsafe.Pointer {
	if s == nil {
		return nil
	}
	return s.ptr
}

// WrapSwsContext wraps a C SwsContext* allocated outside this wrapper
// (e.g. by a media/libav shim calling sws_getContext). Nil-safe: a nil
// C pointer yields nil.
func WrapSwsContext(ptr unsafe.Pointer) *SwsContext {
	if ptr == nil {
		return nil
	}
	return &SwsContext{ptr: ptr}
}

// SwsGetContext creates a scaling context
func SwsGetContext(srcW, srcH, srcFormat, dstW, dstH, dstFormat, flags int) *SwsContext {
	ptr := C.sws_getCachedContext(nil,
		C.int(srcW), C.int(srcH), C.enum_AVPixelFormat(C.int(srcFormat)),
		C.int(dstW), C.int(dstH), C.enum_AVPixelFormat(C.int(dstFormat)),
		C.int(flags),
		nil, nil, nil)
	if ptr == nil {
		return nil
	}
	return &SwsContext{ptr: unsafe.Pointer(ptr)}
}

// SwsGetCachedContext gets or creates a scaling context (C: sws_getCachedContext)
func SwsGetCachedContext(ctx *SwsContext, srcW, srcH int, srcFormat int,
	dstW, dstH int, dstFormat int, flags int) *SwsContext {
	var ctxPtr *C.struct_SwsContext
	if ctx != nil {
		ctxPtr = (*C.struct_SwsContext)(ctx.ptr)
	}
	ptr := C.sws_getCachedContext(ctxPtr,
		C.int(srcW), C.int(srcH), C.enum_AVPixelFormat(C.int(srcFormat)),
		C.int(dstW), C.int(dstH), C.enum_AVPixelFormat(C.int(dstFormat)),
		C.int(flags),
		nil, nil, nil)
	if ptr == nil {
		return nil
	}
	return &SwsContext{ptr: unsafe.Pointer(ptr)}
}

// SwsScale scales the image
func SwsScale(ctx *SwsContext, srcData unsafe.Pointer, srcStride unsafe.Pointer,
	srcSliceY, srcSliceH int, dstData unsafe.Pointer, dstStride unsafe.Pointer) int {
	ret := C.sws_scale((*C.struct_SwsContext)(ctx.ptr),
		(**C.uint8_t)(srcData), (*C.int)(srcStride),
		C.int(srcSliceY), C.int(srcSliceH),
		(**C.uint8_t)(dstData), (*C.int)(dstStride))
	return int(ret)
}

// SwsScaleSingleSrc scales a single-plane source image into a multi-plane destination.
// The C helper ml_sws_scale constructs the srcSlice[4] and srcStride[4] arrays
// in C stack memory, avoiding the cgo violation of passing a Go array of Go pointers.
// src is a pointer to the source pixel data (e.g. &rgba.Pix[0]).
// srcStride is the source stride in bytes (may be negative for vertical flip).
func SwsScaleSingleSrc(ctx *SwsContext, src unsafe.Pointer, srcStride int,
	srcSliceY, srcSliceH int, dstData unsafe.Pointer, dstStride unsafe.Pointer) int {
	return int(C.ml_sws_scale((*C.struct_SwsContext)(ctx.ptr),
		(*C.uint8_t)(src), C.int(srcStride),
		C.int(srcSliceY), C.int(srcSliceH),
		(**C.uint8_t)(dstData), (*C.int)(dstStride)))
}

// SwsFreeContext frees a scaling context
func SwsFreeContext(ctx *SwsContext) {
	if ctx != nil && ctx.ptr != nil {
		C.sws_freeContext((*C.struct_SwsContext)(ctx.ptr))
		ctx.ptr = nil
	}
}
