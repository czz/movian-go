// Package libav — cgo helpers for the fa_imageloader.c port
// (src/fileaccess/fa_imageloader.c). These operate on "materialized"
// codec contexts: FFmpeg 7 removed stream->codec, so the callers pass
// void* AVCodecContext produced by AVFormatCtx.CodecCtxFromStream
// (the canonical analog of C's st->codec).
package libav

/*
#include <libavformat/avformat.h>
#include <libavcodec/avcodec.h>
#include <libavutil/avutil.h>
#include <libavutil/imgutils.h>
#include <libswscale/swscale.h>
#include <stdlib.h>
#include <string.h>

// --- materialized codec-ctx accessors (C: ctx->field) ---

int ml_cctx_codec_id(void *ctx) {
    return ((AVCodecContext*)ctx)->codec_id;
}
int ml_cctx_width(void *ctx) {
    return ((AVCodecContext*)ctx)->width;
}
int ml_cctx_height(void *ctx) {
    return ((AVCodecContext*)ctx)->height;
}
int ml_cctx_pix_fmt(void *ctx) {
    return ((AVCodecContext*)ctx)->pix_fmt;
}
void ml_cctx_set_skip_frame(void *ctx, int v) {
    ((AVCodecContext*)ctx)->skip_frame = (enum AVDiscard)v;
}
void ml_cctx_flush(void *ctx) {
    avcodec_flush_buffers((AVCodecContext*)ctx);
}
void ml_cctx_close(void *ctx) {
    AVCodecContext *c = (AVCodecContext*)ctx;
    avcodec_free_context(&c);
}

// C: avcodec_find_decoder(ctx->codec_id) + avcodec_open2(ctx, codec, NULL)
int ml_cctx_open_decoder(void *ctx) {
    AVCodecContext *c = (AVCodecContext*)ctx;
    const AVCodec *codec = avcodec_find_decoder(c->codec_id);
    if (codec == NULL)
        return -1;
    return avcodec_open2(c, codec, NULL);
}

// C: avcodec_decode_video2(ctx, frame, &got_pic, pkt) — FFmpeg 7:
// send_packet + receive_frame; returns got_pic.
int ml_cctx_decode_video(void *ctx, void *pkt, void *frame) {
    int ret = avcodec_send_packet((AVCodecContext*)ctx, (AVPacket*)pkt);
    if (ret < 0 && ret != AVERROR(EAGAIN))
        return 0;
    ret = avcodec_receive_frame((AVCodecContext*)ctx, (AVFrame*)frame);
    return ret == 0 ? 1 : 0;
}

// C: av_rescale(sec, st->time_base.den, st->time_base.num)
int64_t ml_av_rescale(int64_t a, int64_t b, int64_t c) {
    return av_rescale(a, b, c);
}

int64_t ml_pkt_pts(void *pkt) {
    return ((AVPacket*)pkt)->pts;
}
int ml_pkt_stream_index(void *pkt) {
    return ((AVPacket*)pkt)->stream_index;
}

// --- thumbnail MJPEG encoder (C: thumbcodec / thumbctx) ---

// C: avcodec_find_encoder(AV_CODEC_ID_MJPEG) at fa_imageloader_init
int ml_thumbcodec_available(void) {
    return avcodec_find_encoder(AV_CODEC_ID_MJPEG) != NULL;
}

// C: ctx = avcodec_alloc_context3(thumbcodec)
void* ml_thumbctx_alloc(void) {
    const AVCodec *codec = avcodec_find_encoder(AV_CODEC_ID_MJPEG);
    if (codec == NULL)
        return NULL;
    return avcodec_alloc_context3(codec);
}

// C: ctx->pix_fmt=YUVJ420P, time_base=1/1, sar=1/1, width, height
void ml_thumbctx_set_params(void *ctx, int w, int h) {
    AVCodecContext *c = (AVCodecContext*)ctx;
    c->pix_fmt = AV_PIX_FMT_YUVJ420P;
    c->time_base.den = 1;
    c->time_base.num = 1;
    c->sample_aspect_ratio.num = 1;
    c->sample_aspect_ratio.den = 1;
    c->width = w;
    c->height = h;
}

// C: avcodec_open2(ctx, thumbcodec, NULL)
int ml_thumbctx_open(void *ctx) {
    const AVCodec *codec = avcodec_find_encoder(AV_CODEC_ID_MJPEG);
    if (codec == NULL)
        return -1;
    return avcodec_open2((AVCodecContext*)ctx, codec, NULL);
}

// C: avcodec_encode_video2(ctx, &out, oframe, &got_packet) — FFmpeg 7:
// send_frame + receive_packet; returns 1 with *out/*outsize on got_packet,
// 0 when no packet, -1 on send error. *out must be freed with av_freep.
int ml_thumbctx_encode(void *ctx, void *frame, void **out, int *outsize) {
    AVPacket *pkt = av_packet_alloc();
    if (pkt == NULL)
        return -1;
    int ret = avcodec_send_frame((AVCodecContext*)ctx, (AVFrame*)frame);
    if (ret < 0) {
        av_packet_free(&pkt);
        return -1;
    }
    ret = avcodec_receive_packet((AVCodecContext*)ctx, pkt);
    if (ret == 0) {
        *out = av_malloc(pkt->size);
        if (*out != NULL) {
            memcpy(*out, pkt->data, pkt->size);
            *outsize = pkt->size;
        } else {
            *outsize = 0;
        }
        av_packet_free(&pkt);
        return *out != NULL ? 1 : -1;
    }
    av_packet_free(&pkt);
    return 0;
}

// C: avpicture_alloc((AVPicture*)oframe, fmt, w, h) — FFmpeg 7 analog:
// av_frame_get_buffer (refcounted; released by av_frame_free).
int ml_frame_alloc_image(void *frame, int w, int h, int pixfmt) {
    AVFrame *f = (AVFrame*)frame;
    f->format = pixfmt;
    f->width = w;
    f->height = h;
    return av_frame_get_buffer(f, 0);
}

void ml_frame_set_nopts(void *frame) {
    ((AVFrame*)frame)->pts = AV_NOPTS_VALUE;
}

// --- swscale (C: sws_getContext/sws_scale/sws_freeContext) ---

void* ml_sws_get(int sw, int sh, int sfmt, int dw, int dh, int dfmt) {
    return sws_getContext(sw, sh, (enum AVPixelFormat)sfmt,
                          dw, dh, (enum AVPixelFormat)dfmt,
                          SWS_BILINEAR, NULL, NULL, NULL);
}

// C: sws_scale(sws, frame->data, frame->linesize, 0, srcH,
//              dstptrs, dststrides) — dst is a single-plane buffer.
int ml_sws_scale_to_buf(void *sws, void *frame, int srcH,
                        uint8_t *dst, int dstStride) {
    AVFrame *f = (AVFrame*)frame;
    uint8_t *dstData[4] = { dst, NULL, NULL, NULL };
    int dstStrides[4] = { dstStride, 0, 0, 0 };
    return sws_scale((struct SwsContext*)sws,
                     (const uint8_t * const*)f->data, f->linesize,
                     0, srcH, dstData, dstStrides);
}

// C: sws_scale(sws, sframe->data, sframe->linesize, 0, srcH,
//              oframe->data, oframe->linesize) — frame to frame.
int ml_sws_scale_frame(void *sws, void *src, int srcH, void *dst) {
    AVFrame *s = (AVFrame*)src;
    AVFrame *d = (AVFrame*)dst;
    return sws_scale((struct SwsContext*)sws,
                     (const uint8_t * const*)s->data, s->linesize,
                     0, srcH, d->data, d->linesize);
}

void ml_sws_free(void *sws) {
    sws_freeContext((struct SwsContext*)sws);
}

// --- constants (build-verified, not hardcoded) ---

int ml_codec_id_rv40(void) { return AV_CODEC_ID_RV40; }
int ml_codec_id_rv30(void) { return AV_CODEC_ID_RV30; }
int ml_pixfmt_yuvj420p(void) { return AV_PIX_FMT_YUVJ420P; }
int ml_pixfmt_bgr32(void) { return AV_PIX_FMT_BGR32; }
int ml_pixfmt_rgba(void) { return AV_PIX_FMT_RGBA; }
int ml_avdiscard_default(void) { return AVDISCARD_DEFAULT; }
int ml_avdiscard_nonref(void) { return AVDISCARD_NONREF; }
int ml_avmedia_attachment(void) { return AVMEDIA_TYPE_ATTACHMENT; }
int ml_avseek_backward(void) { return AVSEEK_FLAG_BACKWARD; }
*/
import "C"

import (
	"unsafe"

	"github.com/czz/movian-go/internal/libav"
)

// --- Go wrappers (C names in comments) ---

// cctxPtr extracts the raw AVCodecContext* for the shims. Nil-safe:
// the shims treat NULL like C's callers (crash-free no-op paths are
// guarded by the Go callers, mirroring C's NULL checks).
func cctxPtr(ctx *libav.AVCodecContext) unsafe.Pointer {
	if ctx == nil {
		return nil
	}
	return ctx.CPtr()
}

// CctxCodecID — C: ctx->codec_id
func CctxCodecID(ctx *libav.AVCodecContext) int {
	return int(C.ml_cctx_codec_id(cctxPtr(ctx)))
}

// CctxWidth — C: ctx->width
func CctxWidth(ctx *libav.AVCodecContext) int {
	return int(C.ml_cctx_width(cctxPtr(ctx)))
}

// CctxHeight — C: ctx->height
func CctxHeight(ctx *libav.AVCodecContext) int {
	return int(C.ml_cctx_height(cctxPtr(ctx)))
}

// CctxPixFmt — C: ctx->pix_fmt
func CctxPixFmt(ctx *libav.AVCodecContext) int {
	return int(C.ml_cctx_pix_fmt(cctxPtr(ctx)))
}

// CctxSetSkipFrame — C: ctx->skip_frame = AVDISCARD_*
func CctxSetSkipFrame(ctx *libav.AVCodecContext, v int) {
	C.ml_cctx_set_skip_frame(cctxPtr(ctx), C.int(v))
}

// CctxFlush — C: avcodec_flush_buffers(ctx)
func CctxFlush(ctx *libav.AVCodecContext) {
	C.ml_cctx_flush(cctxPtr(ctx))
}

// CctxClose — C: avcodec_close(ctx) + free (FFmpeg 7: avcodec_free_context)
func CctxClose(ctx *libav.AVCodecContext) {
	C.ml_cctx_close(cctxPtr(ctx))
}

// CctxOpenDecoder — C: avcodec_find_decoder + avcodec_open2
func CctxOpenDecoder(ctx *libav.AVCodecContext) int {
	return int(C.ml_cctx_open_decoder(cctxPtr(ctx)))
}

// CctxDecodeVideo — C: avcodec_decode_video2(...) → got_pic.
// pkt is the caller-owned *AVPacket from ReadPacketRaw; frame is a
// *libav.AVFrame.
func CctxDecodeVideo(ctx *libav.AVCodecContext, pkt *libav.AVPacket, frame *libav.AVFrame) int {
	return int(C.ml_cctx_decode_video(cctxPtr(ctx), pktPtr(pkt), frame.CPtr()))
}

// AvRescale — C: av_rescale(a, b, c)
func AvRescale(a, b, c int64) int64 {
	return int64(C.ml_av_rescale(C.int64_t(a), C.int64_t(b), C.int64_t(c)))
}

// pktPtr extracts the raw AVPacket* for the shims (nil-safe).
func pktPtr(pkt *libav.AVPacket) unsafe.Pointer {
	if pkt == nil {
		return nil
	}
	return pkt.CPtr()
}

// PacketPTS — C: pkt->pts
func PacketPTS(pkt *libav.AVPacket) int64 {
	return int64(C.ml_pkt_pts(pktPtr(pkt)))
}

// PacketStreamIndex — C: pkt->stream_index
func PacketStreamIndex(pkt *libav.AVPacket) int {
	return int(C.ml_pkt_stream_index(pktPtr(pkt)))
}

// ThumbcodecAvailable — C: thumbcodec = avcodec_find_encoder(MJPEG)
func ThumbcodecAvailable() bool {
	return C.ml_thumbcodec_available() != 0
}

// ThumbctxAlloc — C: avcodec_alloc_context3(thumbcodec)
func ThumbctxAlloc() *libav.AVCodecContext {
	return libav.WrapAVCodecContext(C.ml_thumbctx_alloc())
}

// ThumbctxSetParams — C: pix_fmt/time_base/sar/width/height assignment
func ThumbctxSetParams(ctx *libav.AVCodecContext, w, h int) {
	C.ml_thumbctx_set_params(cctxPtr(ctx), C.int(w), C.int(h))
}

// ThumbctxOpen — C: avcodec_open2(ctx, thumbcodec, NULL)
func ThumbctxOpen(ctx *libav.AVCodecContext) int {
	return int(C.ml_thumbctx_open(cctxPtr(ctx)))
}

// ThumbctxEncode — C: avcodec_encode_video2(...) → got_packet.
// Returns the packet bytes (owned copy) and got_packet.
func ThumbctxEncode(ctx *libav.AVCodecContext, frame *libav.AVFrame) ([]byte, bool) {
	var out unsafe.Pointer
	var outsize C.int
	r := C.ml_thumbctx_encode(cctxPtr(ctx), frame.CPtr(), &out, &outsize)
	if r <= 0 {
		return nil, false
	}
	defer C.av_freep(unsafe.Pointer(&out))
	return C.GoBytes(out, outsize), true
}

// FrameAllocImage — C: avpicture_alloc((AVPicture*)oframe, fmt, w, h)
func FrameAllocImage(frame *libav.AVFrame, w, h, pixfmt int) int {
	return int(C.ml_frame_alloc_image(frame.CPtr(), C.int(w), C.int(h),
		C.int(pixfmt)))
}

// FrameSetNoPTS — C: oframe->pts = AV_NOPTS_VALUE
func FrameSetNoPTS(frame *libav.AVFrame) {
	C.ml_frame_set_nopts(frame.CPtr())
}

// swsPtr extracts the raw SwsContext* for the shims (nil-safe).
func swsPtr(sws *libav.SwsContext) unsafe.Pointer {
	if sws == nil {
		return nil
	}
	return sws.Ptr()
}

// SwsGet — C: sws_getContext(..., SWS_BILINEAR, NULL, NULL, NULL)
func SwsGet(srcW, srcH, srcFmt, dstW, dstH, dstFmt int) *libav.SwsContext {
	p := C.ml_sws_get(C.int(srcW), C.int(srcH), C.int(srcFmt),
		C.int(dstW), C.int(dstH), C.int(dstFmt))
	return libav.WrapSwsContext(unsafe.Pointer(p))
}

// SwsScaleToBuf — C: sws_scale → single-plane dst buffer
// (C: ptr[0]=pm->pm_data, strides[0]=pm->pm_linesize).
func SwsScaleToBuf(sws *libav.SwsContext, frame *libav.AVFrame, srcH int,
	dst *byte, dstStride int) int {
	return int(C.ml_sws_scale_to_buf(swsPtr(sws), frame.CPtr(), C.int(srcH),
		(*C.uint8_t)(unsafe.Pointer(dst)), C.int(dstStride)))
}

// SwsScaleFrame — C: sws_scale frame → frame
func SwsScaleFrame(sws *libav.SwsContext, src, dst *libav.AVFrame,
	srcH int) int {
	return int(C.ml_sws_scale_frame(swsPtr(sws), src.CPtr(), C.int(srcH),
		dst.CPtr()))
}

// SwsFree — C: sws_freeContext
func SwsFree(sws *libav.SwsContext) {
	C.ml_sws_free(swsPtr(sws))
}

// Constants resolved against the build headers (C enum values).
func CodecIDRV40() int           { return int(C.ml_codec_id_rv40()) }
func CodecIDRV30() int           { return int(C.ml_codec_id_rv30()) }
func PixFmtYUVJ420P() int        { return int(C.ml_pixfmt_yuvj420p()) }
func PixFmtBGR32() int           { return int(C.ml_pixfmt_bgr32()) }
func PixFmtRGBA() int            { return int(C.ml_pixfmt_rgba()) }
func AVDiscardDefault() int      { return int(C.ml_avdiscard_default()) }
func AVDiscardNonRef() int       { return int(C.ml_avdiscard_nonref()) }
func AVMediaTypeAttachment() int { return int(C.ml_avmedia_attachment()) }
func AVSeekFlagBackward() int    { return int(C.ml_avseek_backward()) }
