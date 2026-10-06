package libav

/*
#include <libavcodec/avcodec.h>
#include <libavutil/mem.h>
#include <stdlib.h>
*/
import "C"

import "unsafe"

// AvParser — typed wrapper over AVCodecParserContext (C: parser_ctx).
// Owns the C parser context; Close frees it (av_parser_close).
// Replaces the raw unsafe.Pointer handle crossing package boundaries.
type AvParser struct {
	cPtr unsafe.Pointer // *C.AVCodecParserContext
}

// NewAvParser initializes a parser.
// C: av_parser_init(codec_id).
func NewAvParser(codecID int) *AvParser {
	ptr := C.av_parser_init(C.enum_AVCodecID(C.int(codecID)))
	if ptr == nil {
		return nil
	}
	return &AvParser{cPtr: unsafe.Pointer(ptr)}
}

// Parse2 — C: av_parser_parse2(parser, ctx, &outbuf, &outlen, buf, len,
// pts, dts, 0). ctx is the wrapped AVCodecContext.
func (p *AvParser) Parse2(ctx *AVCodecContext, buf []byte, pts, dts int64) ([]byte, int) {
	var cctx *C.AVCodecContext
	if ctx != nil {
		cctx = (*C.AVCodecContext)(ctx.cPtr)
	}
	return p.parse2(cctx, buf, pts, dts, 0)
}

// Parse2Pos — av_parser_parse2 with the pos argument.
// C: hls_ts.c passes te_current_seq as pos and reads it back via
// parser_ctx->pos.
func (p *AvParser) Parse2Pos(ctx *AVCodecContext, buf []byte,
	pts, dts, pos int64) ([]byte, int) {
	var cctx *C.AVCodecContext
	if ctx != nil {
		cctx = (*C.AVCodecContext)(ctx.cPtr)
	}
	return p.parse2(cctx, buf, pts, dts, pos)
}

func (p *AvParser) parse2(cctx *C.AVCodecContext, buf []byte,
	pts, dts, pos int64) ([]byte, int) {
	var outbuf *C.uint8_t
	var outlen C.int
	var inbuf *C.uint8_t
	if len(buf) > 0 {
		inbuf = (*C.uint8_t)(unsafe.Pointer(&buf[0]))
	}
	rlen := C.av_parser_parse2((*C.AVCodecParserContext)(p.cPtr), cctx,
		&outbuf, &outlen, inbuf, C.int(len(buf)),
		C.int64_t(pts), C.int64_t(dts), C.int64_t(pos))
	var out []byte
	if outlen > 0 {
		out = C.GoBytes(unsafe.Pointer(outbuf), outlen)
	}
	return out, int(rlen)
}

// Close — C: av_parser_close(parser).
func (p *AvParser) Close() {
	if p != nil && p.cPtr != nil {
		C.av_parser_close((*C.AVCodecParserContext)(p.cPtr))
		p.cPtr = nil
	}
}

// Ptr — raw C pointer for C-parity paths that still need it.
func (p *AvParser) Ptr() unsafe.Pointer {
	if p == nil {
		return nil
	}
	return p.cPtr
}

// Dts — C: parser_ctx->dts.
func (p *AvParser) Dts() int64 {
	if p == nil || p.cPtr == nil {
		return 0
	}
	return int64((*C.AVCodecParserContext)(p.cPtr).dts)
}

// Pts — C: parser_ctx->pts.
func (p *AvParser) Pts() int64 {
	if p == nil || p.cPtr == nil {
		return 0
	}
	return int64((*C.AVCodecParserContext)(p.cPtr).pts)
}

// Pos — C: parser_ctx->pos.
func (p *AvParser) Pos() int64 {
	if p == nil || p.cPtr == nil {
		return -1
	}
	return int64((*C.AVCodecParserContext)(p.cPtr).pos)
}

// KeyFrame — C: parser_ctx->key_frame.
func (p *AvParser) KeyFrame() int {
	if p == nil || p.cPtr == nil {
		return 0
	}
	return int((*C.AVCodecParserContext)(p.cPtr).key_frame)
}
