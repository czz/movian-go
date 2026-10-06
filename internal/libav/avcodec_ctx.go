package libav

/*
#include <libavcodec/avcodec.h>
#include <libavutil/channel_layout.h>
#include <libavutil/pixfmt.h>
#include <stdint.h>
#include <stdlib.h>

// Helper: set ctx->ch_layout from a channel-layout bitmask
// (C: ctx->channel_layout = AV_CH_LAYOUT_*; FFmpeg 7 uses ch_layout).
static void ml_ctx_set_channel_layout(void *ctx_ptr, uint64_t mask) {
    AVCodecContext *ctx = (AVCodecContext *)ctx_ptr;
    av_channel_layout_from_mask(&ctx->ch_layout, mask);
}

// Helper: free an AVCodecContext via its C pointer, avoiding &wrapper.cPtr
// which the cgo checker flags as "Go pointer to Go pointer".
static void ml_avcodec_free_context(void *ctx_ptr) {
    AVCodecContext *ctx = (AVCodecContext *)ctx_ptr;
    avcodec_free_context(&ctx);
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// AVCodec represents a codec (Go wrapper for C.AVCodec)
type AVCodec struct {
	id         int
	name       string
	pixFmts    []int
	sampleFmts []int
}

// GetPixFmts returns the supported pixel formats
func (c *AVCodec) GetPixFmts() []int {
	if c.pixFmts == nil {
		// Default pixel formats for common codecs
		switch c.id {
		case 7: // MJPEG
			c.pixFmts = []int{12} // AV_PIX_FMT_YUVJ420P
		case 61: // PNG
			c.pixFmts = []int{2} // AV_PIX_FMT_RGB24
		default:
			c.pixFmts = []int{0}
		}
	}
	return c.pixFmts
}

// AVCodecContext represents a codec context (Go wrapper for C.AVCodecContext)
type AVCodecContext struct {
	CodecID       int
	Profile       int
	Level         int
	CodecType     int
	Extradata     []byte
	ExtradataSize int
	Channels      int
	PixFmt        int
	Width         int
	Height        int
	TimeBase      AVRational
	SampleAspect  AVRational
	cPtr          unsafe.Pointer // Pointer to C AVCodecContext
}

// CPtr returns the underlying C AVCodecContext pointer.
func (c *AVCodecContext) CPtr() unsafe.Pointer {
	if c == nil {
		return nil
	}
	return c.cPtr
}

// SetPixFmt sets the pixel format
func (ctx *AVCodecContext) SetPixFmt(pixFmt int) {
	ctx.PixFmt = pixFmt
	if ctx.cPtr != nil {
		(*C.AVCodecContext)(ctx.cPtr).pix_fmt = C.enum_AVPixelFormat(C.int(pixFmt))
	}
}

// GetPixFmt gets the pixel format
func (ctx *AVCodecContext) GetPixFmt() int {
	return ctx.PixFmt
}

// SetTimeBase sets the time base
func (ctx *AVCodecContext) SetTimeBase(num, den int) {
	ctx.TimeBase = AVRational{Num: num, Den: den}
	if ctx.cPtr != nil {
		(*C.AVCodecContext)(ctx.cPtr).time_base = C.AVRational{num: C.int(num), den: C.int(den)}
	}
}

// SetSampleAspectRatio sets the sample aspect ratio
func (ctx *AVCodecContext) SetSampleAspectRatio(num, den int) {
	ctx.SampleAspect = AVRational{Num: num, Den: den}
	if ctx.cPtr != nil {
		(*C.AVCodecContext)(ctx.cPtr).sample_aspect_ratio = C.AVRational{num: C.int(num), den: C.int(den)}
	}
}

// SetWidth sets the width
func (ctx *AVCodecContext) SetWidth(width int) {
	ctx.Width = width
	if ctx.cPtr != nil {
		(*C.AVCodecContext)(ctx.cPtr).width = C.int(width)
	}
}

// SetHeight sets the height
func (ctx *AVCodecContext) SetHeight(height int) {
	ctx.Height = height
	if ctx.cPtr != nil {
		(*C.AVCodecContext)(ctx.cPtr).height = C.int(height)
	}
}

// AvcodecFindDecoder finds a decoder with the given codec ID
func AvcodecFindDecoder(codecID int) *AVCodec {
	ptr := C.avcodec_find_decoder(C.enum_AVCodecID(C.int(codecID)))
	if ptr == nil {
		return nil
	}
	return &AVCodec{id: codecID, name: C.GoString(C.avcodec_get_name(C.enum_AVCodecID(C.int(codecID))))}
}

// AvcodecFindEncoder finds an encoder with the given codec ID
func AvcodecFindEncoder(codecID int) *AVCodec {
	ptr := C.avcodec_find_encoder(C.enum_AVCodecID(C.int(codecID)))
	if ptr == nil {
		return nil
	}
	return &AVCodec{id: codecID, name: C.GoString(C.avcodec_get_name(C.enum_AVCodecID(C.int(codecID))))}
}

// AvcodecAllocContext3 allocates a codec context
func AvcodecAllocContext3(codec *AVCodec) *AVCodecContext {
	var codecPtr *C.AVCodec
	if codec != nil {
		codecPtr = C.avcodec_find_encoder(C.enum_AVCodecID(C.int(codec.id)))
	}
	ptr := C.avcodec_alloc_context3(codecPtr)
	if ptr == nil {
		return nil
	}
	// Store the C pointer in cPtr so other CGO calls and AvcodecFreeContext
	// can access it. Previous code dropped ptr → memory leak + nil cPtr.
	id := 0
	if codec != nil {
		id = codec.id
	}
	return &AVCodecContext{CodecID: id, cPtr: unsafe.Pointer(ptr)}
}

// AvcodecOpen2 opens a codec
func AvcodecOpen2(ctx *AVCodecContext, codec *AVCodec, options any) error {
	if ctx == nil || ctx.cPtr == nil {
		return fmt.Errorf("AvcodecOpen2: nil context or cPtr")
	}
	var codecPtr *C.AVCodec
	if codec != nil {
		codecPtr = C.avcodec_find_decoder(C.enum_AVCodecID(C.int(codec.id)))
	}
	ret := C.avcodec_open2((*C.AVCodecContext)(ctx.cPtr), codecPtr, nil)
	if ret < 0 {
		return fmt.Errorf("avcodec_open2 failed: %d", ret)
	}
	return nil
}

// AvcodecOpen2Encoder opens an encoder codec
func AvcodecOpen2Encoder(ctx *AVCodecContext, codec *AVCodec, options any) error {
	if ctx == nil || ctx.cPtr == nil {
		return fmt.Errorf("AvcodecOpen2Encoder: nil context or cPtr")
	}
	var codecPtr *C.AVCodec
	if codec != nil {
		codecPtr = C.avcodec_find_encoder(C.enum_AVCodecID(C.int(codec.id)))
	}
	ret := C.avcodec_open2((*C.AVCodecContext)(ctx.cPtr), codecPtr, nil)
	if ret < 0 {
		return fmt.Errorf("avcodec_open2 (encoder) failed: %d", ret)
	}
	return nil
}

// WrapAVCodecContext wraps a raw *C.AVCodecContext (e.g. allocated by a
// C-side shim) into the typed handle. Returns nil for nil.
func WrapAVCodecContext(cPtr unsafe.Pointer) *AVCodecContext {
	if cPtr == nil {
		return nil
	}
	return &AVCodecContext{cPtr: cPtr}
}

// AvcodecClose closes a codec.
// avcodec_close() is deprecated and removed in modern FFmpeg (>= 5.x).
// The recommended replacement is avcodec_free_context(), which both closes
// and frees the context. Callers should use AvcodecFreeContext for full
// cleanup. This function is kept as a no-op for backward compatibility with
// callers that defer both AvcodecClose and AvcodecFreeContext (the Free call
// handles the actual resource release).
func AvcodecClose(ctx *AVCodecContext) error {
	// No-op: avcodec_close is removed in modern FFmpeg.
	// AvcodecFreeContext (which calls avcodec_free_context) handles cleanup.
	return nil
}

// AvcodecFreeContext frees a codec context.
// Calls avcodec_free_context on the underlying C pointer (ctx.cPtr) and
// clears it. Matches C: avcodec_close(ctx); av_free(ctx); which in modern
// FFmpeg is equivalent to avcodec_free_context(&ctx).
func AvcodecFreeContext(ctx *AVCodecContext) {
	if ctx == nil || ctx.cPtr == nil {
		return
	}
	C.ml_avcodec_free_context(ctx.cPtr)
	ctx.cPtr = nil
}

// AvcodecSendFrame sends a frame to the encoder (FFmpeg 7+ API)
func AvcodecSendFrame(ctx *AVCodecContext, frame *AVFrame) error {
	var framePtr *C.AVFrame
	if frame != nil && frame.cPtr != nil {
		framePtr = (*C.AVFrame)(frame.cPtr)
	}
	ret := C.avcodec_send_frame((*C.AVCodecContext)(ctx.cPtr), framePtr)
	if ret < 0 {
		return fmt.Errorf("avcodec_send_frame failed: %d", ret)
	}
	return nil
}

// AvcodecReceivePacket receives a packet from the encoder (FFmpeg 7+ API)
func AvcodecReceivePacket(ctx *AVCodecContext, pkt *AVPacket) error {
	var pktPtr *C.AVPacket
	if pkt != nil {
		pktPtr = (*C.AVPacket)(pkt.cPtr)
	}
	ret := C.avcodec_receive_packet((*C.AVCodecContext)(ctx.cPtr), pktPtr)
	if ret < 0 {
		return fmt.Errorf("avcodec_receive_packet failed: %d", ret)
	}
	// Sync encoded data from C AVPacket to Go wrapper
	if pkt != nil && pkt.cPtr != nil {
		cPkt := (*C.AVPacket)(pkt.cPtr)
		if cPkt.data != nil && cPkt.size > 0 {
			pkt.Data = C.GoBytes(unsafe.Pointer(cPkt.data), cPkt.size)
		} else {
			pkt.Data = nil
		}
	}
	return nil
}

// AvcodecSendPacket sends a packet to the decoder (FFmpeg 7 API)
func AvcodecSendPacket(ctx *AVCodecContext, pkt *AVPacket) error {
	var pktPtr *C.AVPacket
	if pkt != nil {
		pktPtr = (*C.AVPacket)(pkt.cPtr)
	}
	ret := C.avcodec_send_packet((*C.AVCodecContext)(ctx.cPtr), pktPtr)
	if ret < 0 {
		return fmt.Errorf("avcodec_send_packet failed: %d", ret)
	}
	return nil
}

// AvcodecReceiveFrame receives a frame from the decoder (FFmpeg 7 API)
func AvcodecReceiveFrame(ctx *AVCodecContext, frame *AVFrame) error {
	ret := C.avcodec_receive_frame((*C.AVCodecContext)(ctx.cPtr), (*C.AVFrame)(frame.cPtr))
	if ret < 0 {
		return fmt.Errorf("avcodec_receive_frame failed: %d", ret)
	}
	return nil
}

// AvcodecFlushBuffers flushes the codec buffers
func AvcodecFlushBuffers(ctx *AVCodecContext) {
	C.avcodec_flush_buffers((*C.AVCodecContext)(ctx.cPtr))
}

// avGetProfileName gets the profile name
func avGetProfileName(codec *AVCodec, profile int) string {
	var codecPtr *C.AVCodec
	if codec != nil {
		codecPtr = C.avcodec_find_decoder(C.enum_AVCodecID(C.int(codec.id)))
	}
	cName := C.av_get_profile_name(codecPtr, C.int(profile))
	if cName == nil {
		return ""
	}
	return C.GoString(cName)
}

// avGetChannelLayoutString gets the channel layout string (FFmpeg 7: uses av_channel_layout_describe)
func avGetChannelLayoutString(buf *byte, bufSize int, nbChannels int, channelLayout uint64) {
	// In FFmpeg 7, use av_channel_layout_describe
	// Create AVChannelLayout from channelLayout
	var chLayout C.AVChannelLayout
	C.av_channel_layout_default(&chLayout, C.int(nbChannels))
	C.av_channel_layout_from_mask(&chLayout, C.uint64_t(channelLayout))

	// Describe the layout
	C.av_channel_layout_describe(&chLayout, (*C.char)(unsafe.Pointer(buf)), C.size_t(bufSize))

	// Free the layout
	C.av_channel_layout_uninit(&chLayout)
}

// avcodecDefaultGetFormat selects the default pixel format
func avcodecDefaultGetFormat(ctx *AVCodecContext, fmt []int) int {
	if len(fmt) == 0 {
		return 0
	}
	// Convert Go slice to C array and call avcodec_default_get_format
	cFmt := make([]C.enum_AVPixelFormat, len(fmt))
	for i, f := range fmt {
		cFmt[i] = C.enum_AVPixelFormat(f)
	}
	return int(C.avcodec_default_get_format((*C.AVCodecContext)(ctx.cPtr), &cFmt[0]))
}

// avcodecDefaultGetBuffer2 allocates a buffer for the frame
func avcodecDefaultGetBuffer2(s *AVCodecContext, frame *AVFrame, flags int) int {
	return int(C.avcodec_default_get_buffer2((*C.AVCodecContext)(s.cPtr), (*C.AVFrame)(frame.cPtr), C.int(flags)))
}

// AvCodecCtxSetSampleFmt — C: ctx->sample_fmt = AV_SAMPLE_FMT_FLTP.
func (ctx *AVCodecContext) SetSampleFmt(fmt int) {
	if ctx != nil && ctx.cPtr != nil {
		(*C.AVCodecContext)(ctx.cPtr).sample_fmt = C.enum_AVSampleFormat(C.int(fmt))
	}
}

// AvCodecCtxSetSampleRate — C: ctx->sample_rate = 48000.
func (ctx *AVCodecContext) SetSampleRate(rate int) {
	if ctx != nil && ctx.cPtr != nil {
		(*C.AVCodecContext)(ctx.cPtr).sample_rate = C.int(rate)
	}
}

// AvCodecCtxSetChannelLayout — C: ctx->channel_layout = AV_CH_LAYOUT_*.
// FFmpeg 7: stores into ctx->ch_layout via av_channel_layout_from_mask.
func (ctx *AVCodecContext) SetChannelLayout(mask uint64) {
	if ctx != nil && ctx.cPtr != nil {
		C.ml_ctx_set_channel_layout(ctx.cPtr, C.uint64_t(mask))
	}
}

// AvCodecCtxFrameSize — C: ctx->frame_size.
func (ctx *AVCodecContext) FrameSize() int {
	if ctx == nil || ctx.cPtr == nil {
		return 0
	}
	return int((*C.AVCodecContext)(ctx.cPtr).frame_size)
}

// AvCodecCtxCodecType — C: ctx->codec_type.
func (ctx *AVCodecContext) GetCodecType() int {
	if ctx == nil || ctx.cPtr == nil {
		return AvmediaTypeUnknown
	}
	return int((*C.AVCodecContext)(ctx.cPtr).codec_type)
}

// AvCodecCtxGetCodecID — C: ctx->codec_id.
func (ctx *AVCodecContext) GetCodecID() int {
	if ctx == nil || ctx.cPtr == nil {
		return 0
	}
	return int((*C.AVCodecContext)(ctx.cPtr).codec_id)
}
