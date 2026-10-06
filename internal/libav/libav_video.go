package libav

/*
#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavutil/pixfmt.h>
#include <libavutil/pixdesc.h>
#include <libavutil/imgutils.h>
#include <libswscale/swscale.h>
#include <stdlib.h>
#include <string.h>
*/
import "C"
import "unsafe"

// NOTE: LibAVDeliverFrame, LibAVVideoFlush, LibAVVideoEOF, and LibAVDecodeVideo
// are implemented in pkg/media/libav_video.go to avoid import cycles.
// This package (pkg/libav) provides only CGO bindings and helper functions.

// LibAVGetFormat selects the pixel format for the decoder
// This is a callback function for AVCodecContext.get_format
func LibAVGetFormat(ctx *AVCodecContext, fmt []int) int {
	// Return the default format selected by FFmpeg
	return avcodecDefaultGetFormat(ctx, fmt)
}

// GetBuffer2Wrapper wraps the get_buffer2 callback
func GetBuffer2Wrapper(s *AVCodecContext, frame *AVFrame, flags int) int {
	// Use default buffer allocation
	return avcodecDefaultGetBuffer2(s, frame, flags)
}

// MetadataFromLibAV generates metadata string from codec information
func MetadataFromLibAV(codec *AVCodec, avctx *AVCodecContext) string {
	if codec == nil {
		return ""
	}

	name := codec.name
	profile := avGetProfileName(codec, avctx.Profile)

	// Special handling for DTS (AVCodecIDDts = 7609)
	if codec.id == 7609 && profile != "" {
		name = ""
	}

	result := ""
	if name != "" {
		result += name
	}
	if profile != "" {
		if result != "" {
			result += " "
		}
		result += profile
	}

	return result
}

// AvRescaleQ — C: av_rescale_q(a, bq, cq) from libavutil/mathematics.h.
// Rescales a from timebase bq into cq with AV_ROUND_NEAR_INF.
func AvRescaleQ(a int64, bq, cq AVRational) int64 {
	return int64(C.av_rescale_q(C.int64_t(a),
		C.AVRational{num: C.int(bq.Num), den: C.int(bq.Den)},
		C.AVRational{num: C.int(cq.Num), den: C.int(cq.Den)}))
}

// AvQ2d — C: av_q2d(rational)
func AvQ2d(r AVRational) float64 {
	return float64(r.Num) / float64(r.Den)
}

// AvCodecCtxFramerate — C: ctx->framerate on a raw AVCodecContext pointer
// (e.g. media_codec_t.fmt_ctx). Replaces the removed ticks_per_frame field.
func AvCodecCtxFramerate(cptr unsafe.Pointer) AVRational {
	if cptr == nil {
		return AVRational{Num: 0, Den: 1}
	}
	fr := (*C.AVCodecContext)(cptr).framerate
	return AVRational{Num: int(fr.num), Den: int(fr.den)}
}

// AvCodecCtxTimeBase — C: ctx->time_base on a raw AVCodecContext pointer.
func AvCodecCtxTimeBase(cptr unsafe.Pointer) AVRational {
	if cptr == nil {
		return AVRational{Num: 0, Den: 1}
	}
	tb := (*C.AVCodecContext)(cptr).time_base
	return AVRational{Num: int(tb.num), Den: int(tb.den)}
}

// AvFrameNbSamples — C: frame->nb_samples (audio decode probe).
func AvFrameNbSamples(cptr unsafe.Pointer) int {
	if cptr == nil {
		return 0
	}
	return int((*C.AVFrame)(cptr).nb_samples)
}

// AvFrameSampleRate — C: frame->sample_rate (audio decode probe).
func AvFrameSampleRate(cptr unsafe.Pointer) int {
	if cptr == nil {
		return 0
	}
	return int((*C.AVFrame)(cptr).sample_rate)
}
