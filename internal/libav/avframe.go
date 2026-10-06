package libav

/*
#include <libavcodec/avcodec.h>
#include <libavutil/channel_layout.h>
#include <libavutil/imgutils.h>
#include <libavutil/mem.h>
#include <libavutil/pixfmt.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

// Helper: allocate image data directly on a C AVFrame (replaces avpicture_alloc)
// Fills frame->data[4] and frame->linesize[4], wraps the buffer in frame->buf[0]
// so that av_frame_free() releases it automatically.
static int ml_av_image_alloc(AVFrame *frame, enum AVPixelFormat pix_fmt,
                             int width, int height, int align) {
    int ret = av_image_alloc(frame->data, frame->linesize, width, height, pix_fmt, align);
    if (ret < 0)
        return ret;
    frame->buf[0] = av_buffer_create(frame->data[0], (size_t)ret, av_buffer_default_free, NULL, 0);
    if (!frame->buf[0]) {
        av_free(frame->data[0]);
        memset(frame->data, 0, sizeof(frame->data));
        memset(frame->linesize, 0, sizeof(frame->linesize));
        return AVERROR(ENOMEM);
    }
    frame->format = pix_fmt;
    frame->width  = width;
    frame->height = height;
    return ret;
}

// Helper: free an AVFrame via its C pointer, avoiding &wrapper.cPtr
// which the cgo checker flags as "Go pointer to Go pointer".
static void ml_av_frame_free(void *frame_ptr) {
    AVFrame *frame = (AVFrame *)frame_ptr;
    av_frame_free(&frame);
}

// Helper: set frame->ch_layout from a channel-layout bitmask.
static void ml_frame_set_channel_layout(void *frame_ptr, uint64_t mask) {
    AVFrame *frame = (AVFrame *)frame_ptr;
    av_channel_layout_from_mask(&frame->ch_layout, mask);
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// AVFrame represents a frame (Go wrapper for C.AVFrame)
type AVFrame struct {
	Data     [][]byte
	Linesize []int
	PTS      int64
	cPtr     unsafe.Pointer // Pointer to C AVFrame
}

// SetPts sets the presentation timestamp
func (f *AVFrame) SetPts(pts int64) {
	f.PTS = pts
}

// CPtr returns the underlying C AVFrame pointer.
func (f *AVFrame) CPtr() unsafe.Pointer {
	if f == nil {
		return nil
	}
	return f.cPtr
}

// WrapAVFrame wraps a raw *C.AVFrame for typed Go plumbing
// (borrow — the caller retains ownership of the C object).
func WrapAVFrame(ptr unsafe.Pointer) *AVFrame {
	if ptr == nil {
		return nil
	}
	return &AVFrame{cPtr: ptr}
}

// AvFrameAlloc allocates a frame
func AvFrameAlloc() *AVFrame {
	ptr := C.av_frame_alloc()
	if ptr == nil {
		return nil
	}
	return &AVFrame{Data: make([][]byte, 8), Linesize: make([]int, 8), cPtr: unsafe.Pointer(ptr)}
}

// AvFrameFree frees a frame
func AvFrameFree(frame *AVFrame) {
	if frame != nil && frame.cPtr != nil {
		C.ml_av_frame_free(frame.cPtr)
		frame.cPtr = nil
	}
}

// avFrameUnref unreferences the frame data
func avFrameUnref(frame *AVFrame) {
	if frame != nil {
		C.av_frame_unref((*C.AVFrame)(frame.cPtr))
	}
}

// AvImageAlloc allocates an image (replaces avpicture_alloc in FFmpeg 7)
// Populates the C AVFrame.data[] and AVFrame.linesize[] directly via the
// ml_av_image_alloc helper. The buffer is wrapped in an AVBufferRef stored
// in frame->buf[0], so av_frame_free() releases it automatically.
// frame.Linesize[] is mirrored as a Go-side view for compatibility.
func AvImageAlloc(frame *AVFrame, width, height, pixFmt, align int) error {
	if frame == nil || frame.cPtr == nil {
		return fmt.Errorf("AvImageAlloc: nil frame or cPtr")
	}
	cFrame := (*C.AVFrame)(frame.cPtr)
	ret := C.ml_av_image_alloc(cFrame, C.enum_AVPixelFormat(C.int(pixFmt)),
		C.int(width), C.int(height), C.int(align))
	if ret < 0 {
		return fmt.Errorf("av_image_alloc failed: %d", ret)
	}
	// Mirror linesize into Go wrapper for compatibility
	for i := range 8 {
		frame.Linesize[i] = int(cFrame.linesize[i])
	}
	return nil
}

// GetCDataPtrs returns a pointer to the C AVFrame.data[8] array.
// Use this to pass destination data pointers to sws_scale.
func (f *AVFrame) GetCDataPtrs() unsafe.Pointer {
	if f == nil || f.cPtr == nil {
		return nil
	}
	return unsafe.Pointer(&(*C.AVFrame)(f.cPtr).data[0])
}

// GetCLinesizePtrs returns a pointer to the C AVFrame.linesize[8] array.
// Use this to pass destination linesize pointers to sws_scale.
func (f *AVFrame) GetCLinesizePtrs() unsafe.Pointer {
	if f == nil || f.cPtr == nil {
		return nil
	}
	return unsafe.Pointer(&(*C.AVFrame)(f.cPtr).linesize[0])
}

// AvFrameSetData — C: frame->data[i] = ptr.
func (f *AVFrame) SetData(idx int, p unsafe.Pointer) {
	if f == nil || f.cPtr == nil || idx < 0 || idx >= 8 {
		return
	}
	(*C.AVFrame)(f.cPtr).data[idx] = (*C.uint8_t)(p)
}

// AvFrameData — C: frame->data[i].
func (f *AVFrame) DataPtr(idx int) unsafe.Pointer {
	if f == nil || f.cPtr == nil || idx < 0 || idx >= 8 {
		return nil
	}
	return unsafe.Pointer((*C.AVFrame)(f.cPtr).data[idx])
}

// AvFrameSetNbSamples — C: frame->nb_samples = n.
func (f *AVFrame) SetNbSamples(n int) {
	if f != nil && f.cPtr != nil {
		(*C.AVFrame)(f.cPtr).nb_samples = C.int(n)
	}
}

// AvFrameSetFormat — C: frame->format = fmt.
func (f *AVFrame) SetFormat(fmt int) {
	if f != nil && f.cPtr != nil {
		(*C.AVFrame)(f.cPtr).format = C.int(fmt)
	}
}

// AvFrameSetSampleRate — C: frame->sample_rate.
func (f *AVFrame) SetSampleRate(rate int) {
	if f != nil && f.cPtr != nil {
		(*C.AVFrame)(f.cPtr).sample_rate = C.int(rate)
	}
}

// AvFrameSetChannelLayout — C: frame->channel_layout; FFmpeg 7 ch_layout.
func (f *AVFrame) SetChannelLayout(mask uint64) {
	if f != nil && f.cPtr != nil {
		C.ml_frame_set_channel_layout(f.cPtr, C.uint64_t(mask))
	}
}
