package libav

/*
#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <stdint.h>
#include <stdlib.h>

// Helper: fctx->streams[i] codecpar -> newly allocated AVCodecContext.
// C: ctx = fctx->streams[s]->codec (pre-FFmpeg-5 direct pointer; now the
// ctx is allocated and codecpar is copied in — same observable state).
static void *ml_fctx_stream_codec_ctx(void *fctx_ptr, int idx) {
    AVFormatContext *fctx = (AVFormatContext *)fctx_ptr;
    AVCodecContext *ctx = avcodec_alloc_context3(NULL);
    if (!ctx)
        return NULL;
    if (avcodec_parameters_to_context(ctx, fctx->streams[idx]->codecpar) < 0) {
        avcodec_free_context(&ctx);
        return NULL;
    }
    return ctx;
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// AVFormatContext represents a format context (Go wrapper for C.AVFormatContext)
type AVFormatContext struct {
	Filename   string
	Duration   int64
	NbStreams  int
	NbChapters int
	StartTime  int64
	Streams    []*AVStream
	Chapters   []*AVChapter
	Metadata   map[string]string
	avio       *AVIOContext   // Custom AVIOContext for fileaccess integration
	cPtr       unsafe.Pointer // Pointer to C AVFormatContext
}

// CPtr returns the underlying C AVFormatContext pointer (non-owning view).
func (fc *AVFormatContext) CPtr() unsafe.Pointer {
	if fc == nil {
		return nil
	}
	return fc.cPtr
}

// WrapAVFormatContext wraps a raw *C.AVFormatContext for typed Go
// plumbing (borrow — the caller retains ownership of the C object).
func WrapAVFormatContext(ptr unsafe.Pointer) *AVFormatContext {
	if ptr == nil {
		return nil
	}
	return &AVFormatContext{cPtr: ptr}
}

// AVChapter represents a chapter
type AVChapter struct {
	ID       int64
	Start    int64
	End      int64
	TimeBase AVRational
	Metadata map[string]string
}

// AVStream represents a stream (Go wrapper for C.AVStream)
type AVStream struct {
	CodecCtx     *AVCodecContext
	TimeBase     AVRational
	TimeBaseDen  int
	TimeBaseNum  int
	Index        int
	CodecType    int
	Metadata     map[string]string
	AvgFrameRate AVRational
}

// avformatOpenInput opens an input stream
func avformatOpenInput(url string) (*AVFormatContext, error) {
	cURL := C.CString(url)
	defer C.free(unsafe.Pointer(cURL))

	var ctxPtr *C.AVFormatContext
	ret := C.avformat_open_input(&ctxPtr, cURL, nil, nil)
	if ret < 0 {
		return nil, fmt.Errorf("avformat_open_input failed: %d", ret)
	}
	return &AVFormatContext{Filename: url, cPtr: unsafe.Pointer(ctxPtr)}, nil
}

// avformatCloseInput closes an input stream
func avformatCloseInput(fc *AVFormatContext) {
	if fc != nil && fc.cPtr != nil {
		c := (*C.AVFormatContext)(fc.cPtr)
		C.avformat_close_input(&c)
		fc.cPtr = nil
	}
}

// avformatFindStreamInfo finds stream information
func avformatFindStreamInfo(fc *AVFormatContext) error {
	if fc == nil || fc.cPtr == nil {
		return fmt.Errorf("avformat_find_stream_info: nil context")
	}
	ret := C.avformat_find_stream_info((*C.AVFormatContext)(fc.cPtr), nil)
	if ret < 0 {
		return fmt.Errorf("avformat_find_stream_info failed: %d", ret)
	}
	return nil
}

// AvReadFrame reads a frame
func AvReadFrame(fc *AVFormatContext, pkt *AVPacket) error {
	if fc == nil || fc.cPtr == nil || pkt == nil || pkt.cPtr == nil {
		return fmt.Errorf("av_read_frame: nil context or packet")
	}
	ret := C.av_read_frame((*C.AVFormatContext)(fc.cPtr), (*C.AVPacket)(pkt.cPtr))
	if ret < 0 {
		return fmt.Errorf("av_read_frame failed: %d", ret)
	}
	pkt.Flags = avPacketGetFlags(pkt)
	return nil
}

// avSeekFrame seeks to a timestamp
func avSeekFrame(fc *AVFormatContext, streamIndex int, timestamp int64, flags int) error {
	if fc == nil || fc.cPtr == nil {
		return fmt.Errorf("av_seek_frame: nil context")
	}
	ret := C.av_seek_frame((*C.AVFormatContext)(fc.cPtr), C.int(streamIndex), C.int64_t(timestamp), C.int(flags))
	if ret < 0 {
		return fmt.Errorf("av_seek_frame failed: %d", ret)
	}
	return nil
}

// ReadFrame reads a frame (method for AVFormatContext)
func (fc *AVFormatContext) ReadFrame(pkt *AVPacket) error {
	return AvReadFrame(fc, pkt)
}

// SeekFrame seeks to a timestamp (method for AVFormatContext)
func (fc *AVFormatContext) SeekFrame(streamIndex int, timestamp int64, flags int) error {
	return avSeekFrame(fc, streamIndex, timestamp, flags)
}

// ---------------------------------------------------------------------------
// Canonical audio_test.c support accessors (encoder-side fields)
// ---------------------------------------------------------------------------

// AvFormatCtxNbStreams — C: fctx->nb_streams.
func (fc *AVFormatContext) GetNbStreams() int {
	if fc == nil || fc.cPtr == nil {
		return 0
	}
	return int((*C.AVFormatContext)(fc.cPtr).nb_streams)
}

// AvFormatCtxStreamCodecCtx — C: fctx->streams[s]->codec.
// FFmpeg 7 adaptation: allocates a context and copies codecpar into it;
// the caller owns it (avcodec_close/avcodec_free_context).
func (fc *AVFormatContext) StreamCodecCtx(idx int) *AVCodecContext {
	if fc == nil || fc.cPtr == nil {
		return nil
	}
	p := C.ml_fctx_stream_codec_ctx(fc.cPtr, C.int(idx))
	if p == nil {
		return nil
	}
	return &AVCodecContext{cPtr: unsafe.Pointer(p)}
}

// AvReadFrameCode — C: r = av_read_frame(fctx, &pkt). Returns the raw
// libav return code (0 ok, AVERROR(EAGAIN)/EOF negatives).
func AvReadFrameCode(fc *AVFormatContext, pkt *AVPacket) int {
	if fc == nil || fc.cPtr == nil || pkt == nil || pkt.cPtr == nil {
		return -1
	}
	return int(C.av_read_frame((*C.AVFormatContext)(fc.cPtr), (*C.AVPacket)(pkt.cPtr)))
}

// ---------------------------------------------------------------------------
// Canonical audio_test.c support accessors (cont.)
// ---------------------------------------------------------------------------

// AvFormatCtxStreamCodecCtxRaw — C: ctx = fctx->streams[s]->codec.
// FFmpeg 7: allocates a ctx and copies streams[i]->codecpar into it.
// The caller owns it (AvcodecClose + AvcodecFreeContext).
func AvFormatCtxStreamCodecCtx(fcptr unsafe.Pointer, idx int) unsafe.Pointer {
	if fcptr == nil {
		return nil
	}
	return C.ml_fctx_stream_codec_ctx(fcptr, C.int(idx))
}
