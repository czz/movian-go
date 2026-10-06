package libav

/*
#include <stdlib.h>
#include <libavcodec/avcodec.h>
#include <libavcodec/bsf.h>
#include <libavcodec/packet.h>

// Bitstream filter functions (for h264_mp4toannexb conversion)
static const AVBitStreamFilter *ml_av_bsf_get_by_name(const char *name) {
    return av_bsf_get_by_name(name);
}

static int ml_av_bsf_alloc(const AVBitStreamFilter *filter, void **ctx) {
    return av_bsf_alloc(filter, (AVBSFContext**)ctx);
}

static int ml_av_bsf_init(void *ctx) {
    return av_bsf_init((AVBSFContext*)ctx);
}

static int ml_av_bsf_send_packet(void *ctx, void *pkt) {
    return av_bsf_send_packet((AVBSFContext*)ctx, (AVPacket*)pkt);
}

static int ml_av_bsf_receive_packet(void *ctx, void *pkt) {
    return av_bsf_receive_packet((AVBSFContext*)ctx, (AVPacket*)pkt);
}

static void ml_av_bsf_free(void **ctx) {
    av_bsf_free((AVBSFContext**)ctx);
}

// Copy codec parameters from codec context to BSF par_in
static int ml_bsf_set_par_in_from_ctx(void *bsf_ctx, void *codec_ctx) {
    AVBSFContext *bsf = (AVBSFContext*)bsf_ctx;
    AVCodecContext *cc = (AVCodecContext*)codec_ctx;
    return avcodec_parameters_from_context(bsf->par_in, cc);
}

// Get codec parameters from BSF context (par_in)
static void *ml_bsf_get_par_in(void *ctx) {
    return &((AVBSFContext*)ctx)->par_in;
}

// Get codec parameters from BSF context (par_out)
static void *ml_bsf_get_par_out(void *ctx) {
    return &((AVBSFContext*)ctx)->par_out;
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// BSFContext wraps an AVBSFContext for bitstream filtering (e.g.
// h264_mp4toannexb). Lives in pkg/libav (the FFmpeg binding leaf) so
// video/decoder can hold the typed handle without a media/libav edge.
type BSFContext struct {
	ctx unsafe.Pointer
}

// NewBSF creates a new bitstream filter context by name.
// Common filters: "h264_mp4toannexb", "hevc_mp4toannexb".
func NewBSF(name string, codecCtx unsafe.Pointer) (*BSFContext, error) {
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	filter := C.ml_av_bsf_get_by_name(cName)
	if filter == nil {
		return nil, fmt.Errorf("bitstream filter %s not found", name)
	}
	var ctx unsafe.Pointer
	ret := C.ml_av_bsf_alloc(filter, &ctx)
	if ret < 0 {
		return nil, fmt.Errorf("av_bsf_alloc failed: %d", ret)
	}
	// Copy codec parameters from the codec context to the BSF's par_in
	if codecCtx != nil {
		ret := C.ml_bsf_set_par_in_from_ctx(ctx, codecCtx)
		if ret < 0 {
			C.ml_av_bsf_free(&ctx)
			return nil, fmt.Errorf("avcodec_parameters_from_context failed: %d", ret)
		}
	}
	ret = C.ml_av_bsf_init(ctx)
	if ret < 0 {
		C.ml_av_bsf_free(&ctx)
		return nil, fmt.Errorf("av_bsf_init failed: %d", ret)
	}
	return &BSFContext{ctx: unsafe.Pointer(ctx)}, nil
}

// SendPacket sends a packet to the BSF for filtering. A nil pkt drains
// the filter (C: av_bsf_send_packet with NULL packet signals EOF).
func (b *BSFContext) SendPacket(pkt *AVPacket) error {
	if b == nil || b.ctx == nil {
		return fmt.Errorf("BSF context is nil")
	}
	ret := C.ml_av_bsf_send_packet(b.ctx, pkt.CPtr())
	if ret < 0 {
		return fmt.Errorf("av_bsf_send_packet failed: %d", ret)
	}
	return nil
}

// ReceivePacket receives a filtered packet from the BSF.
// Returns AVERROR(EAGAIN) if no packet is available yet.
func (b *BSFContext) ReceivePacket(pkt *AVPacket) error {
	if b == nil || b.ctx == nil {
		return fmt.Errorf("BSF context is nil")
	}
	ret := C.ml_av_bsf_receive_packet(b.ctx, pkt.CPtr())
	if ret < 0 {
		return fmt.Errorf("av_bsf_receive_packet: %d", ret)
	}
	return nil
}

// Free releases the BSF context.
func (b *BSFContext) Free() {
	if b != nil && b.ctx != nil {
		cp := b.ctx
		C.ml_av_bsf_free(&cp)
		b.ctx = cp
	}
}
