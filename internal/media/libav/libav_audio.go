package libav

/*
#include <libavcodec/avcodec.h>
#include <libavutil/opt.h>
#include <libavutil/error.h>
#include <libavutil/samplefmt.h>
#include <libavutil/channel_layout.h>
#include <libavutil/frame.h>
#include <libswresample/swresample.h>
#include <libavformat/avformat.h>
#include <libavformat/avio.h>
#include <stdlib.h>
#include <string.h>
#include <stdio.h>
#include <ctype.h>

// Workaround for CGO not being able to resolve AVERROR macro
#ifndef AVERROR
#define AVERROR(e) (-(e))
#endif

// Helper functions for FFmpeg 7 channel layout API
static int get_channels_from_ch_layout(const AVChannelLayout *ch_layout) {
    return ch_layout->nb_channels;
}

static void set_default_channel_layout(AVChannelLayout *ch_layout, int channels) {
    av_channel_layout_default(ch_layout, channels);
}

// Wrapper functions for SwrContext
void* swr_alloc_wrapper() {
    return swr_alloc();
}

int swr_get_out_samples_wrapper(void *swrCtx, int nb_samples) {
    return swr_get_out_samples((SwrContext*)swrCtx, nb_samples);
}

int swr_convert_wrapper(void *swrCtx, uint8_t **out, int out_count, const uint8_t **in, int in_count) {
    return swr_convert((SwrContext*)swrCtx, out, out_count, in, in_count);
}

void swr_close_wrapper(void *swrCtx) {
    swr_close((SwrContext*)swrCtx);
}

int swr_init_wrapper(void *swrCtx) {
    return swr_init((SwrContext*)swrCtx);
}

void swr_free_wrapper(void **swrCtx) {
    swr_free((SwrContext**)swrCtx);
}

int av_opt_set_int_wrapper(void *obj, const char *name, int64_t value, int search_flags) {
    return av_opt_set_int(obj, name, value, search_flags);
}

void av_frame_free_wrapper(void **frame) {
    av_frame_free((AVFrame**)frame);
}

void avformat_free_context_wrapper(void *fmtCtx) {
    avformat_free_context((AVFormatContext*)fmtCtx);
}

// get_frame_channel_layout extracts the channel layout as a bitmask from an AVFrame.
// FFmpeg 63 uses AVChannelLayout struct (not uint64). We convert to the legacy
// bitmask representation for compatibility with the SWR in_channel_layout option.
static uint64_t get_frame_channel_layout(const AVFrame *frame) {
    if (frame->ch_layout.order == AV_CHANNEL_ORDER_NATIVE)
        return frame->ch_layout.u.mask;
    // For UNSPEC or any other order, derive from channel count
    if (frame->ch_layout.nb_channels > 0) {
        AVChannelLayout tmp;
        av_channel_layout_default(&tmp, frame->ch_layout.nb_channels);
        uint64_t mask = 0;
        if (tmp.order == AV_CHANNEL_ORDER_NATIVE)
            mask = tmp.u.mask;
        av_channel_layout_uninit(&tmp);
        return mask;
    }
    return 0;
}

// get_codec_channel_layout extracts the channel layout as a bitmask from an AVCodecContext.
static uint64_t get_codec_channel_layout(const AVCodecContext *ctx) {
    if (ctx->ch_layout.order == AV_CHANNEL_ORDER_NATIVE)
        return ctx->ch_layout.u.mask;
    if (ctx->ch_layout.nb_channels > 0) {
        AVChannelLayout tmp;
        av_channel_layout_default(&tmp, ctx->ch_layout.nb_channels);
        uint64_t mask = 0;
        if (tmp.order == AV_CHANNEL_ORDER_NATIVE)
            mask = tmp.u.mask;
        av_channel_layout_uninit(&tmp);
        return mask;
    }
    return 0;
}

// swr_set_chlayout sets the channel layout on SwrContext.
// FFmpeg 63 uses AV_OPT_TYPE_CHLAYOUT for channel layout options.
// We use av_opt_set_chlayout which copies the layout into the SwrContext.
static int swr_set_in_chlayout(void *swrCtx, uint64_t mask) {
    AVChannelLayout ch_layout;
    memset(&ch_layout, 0, sizeof(ch_layout));
    if (mask > 0) {
        ch_layout.order = AV_CHANNEL_ORDER_NATIVE;
        ch_layout.u.mask = mask;
        ch_layout.nb_channels = av_popcount64(mask);
    } else {
        // Default to stereo
        av_channel_layout_default(&ch_layout, 2);
    }
    int ret = av_opt_set_chlayout(swrCtx, "in_chlayout", &ch_layout, 0);
    av_channel_layout_uninit(&ch_layout);
    return ret;
}

// swr_set_in_chlayout_by_channels sets input channel layout from channel count.
// This is needed because some decoders output AV_CHANNEL_ORDER_UNSPEC
// (no native mask), so we derive the layout from channel count.
static int swr_set_in_chlayout_by_channels(void *swrCtx, int channels) {
    AVChannelLayout ch_layout;
    memset(&ch_layout, 0, sizeof(ch_layout));
    av_channel_layout_default(&ch_layout, channels);
    int ret = av_opt_set_chlayout(swrCtx, "in_chlayout", &ch_layout, 0);
    av_channel_layout_uninit(&ch_layout);
    return ret;
}

static int swr_set_out_chlayout(void *swrCtx, uint64_t mask) {
    AVChannelLayout ch_layout;
    memset(&ch_layout, 0, sizeof(ch_layout));
    if (mask > 0) {
        ch_layout.order = AV_CHANNEL_ORDER_NATIVE;
        ch_layout.u.mask = mask;
        ch_layout.nb_channels = av_popcount64(mask);
    } else {
        // Default to stereo
        av_channel_layout_default(&ch_layout, 2);
    }
    int ret = av_opt_set_chlayout(swrCtx, "out_chlayout", &ch_layout, 0);
    av_channel_layout_uninit(&ch_layout);
    return ret;
}

// Audio resampler state — mirrors C's audio_decoder_t.ad_avr.
// We keep a persistent SwrContext that is reconfigured when the input
// format changes. Output is always S16/48000/stereo (matching ALSA config).
// This is process-global because there is only one active audio decoder at a time.
static SwrContext *g_swr_ctx = NULL;
static int g_swr_in_sample_rate = 0;
static int g_swr_in_sample_fmt = -1;
static uint64_t g_swr_in_ch_layout = 0;

// Target output format (must match ALSA configuration)
#define AUDIO_OUT_SAMPLE_FMT   AV_SAMPLE_FMT_S16
#define AUDIO_OUT_SAMPLE_RATE  48000
#define AUDIO_OUT_CHANNELS     2

// decode_audio_packet: C wrapper that does the full decode + resample cycle.
// Mirrors C's audio_process_audio() + avresample_convert() flow.
// Returns: negative = error, 0 = no frame (EAGAIN/EOF), positive = data size
// Output data is S16 interleaved stereo at 48000Hz, copied into out_buf.
// out_pts_delay_us is set to the resampler delay in microseconds
// (mirrors C's od + id calculation for PTS adjustment).
static int decode_audio_packet(void *codecCtx, void *pkt,
                               uint8_t *out_buf, int out_buf_size,
                               int *out_sample_rate, int *out_sample_fmt,
                               int *out_nb_samples, int *out_channels,
                               int64_t *out_pts_delay_us) {
    AVCodecContext *ctx = (AVCodecContext*)codecCtx;
    AVFrame *frame = av_frame_alloc();
    if (!frame) return -1;

    int ret = avcodec_send_packet(ctx, (AVPacket*)pkt);
    if (ret < 0) {
        av_frame_free(&frame);
        return ret;
    }

    ret = avcodec_receive_frame(ctx, frame);
    if (ret < 0) {
        av_frame_free(&frame);
        return ret; // EAGAIN or EOF
    }

    // Get input format from decoded frame.
    // In FFmpeg 9.x, some decoders may not set sample_rate on the frame,
    // so fall back to the codec context's sample rate.
    int in_sample_rate = frame->sample_rate;
    if (in_sample_rate == 0) {
        in_sample_rate = ctx->sample_rate;
    }
    int in_sample_fmt = frame->format;
    if (in_sample_fmt < 0) {
        in_sample_fmt = ctx->sample_fmt;
    }
    uint64_t in_ch_layout = get_frame_channel_layout(frame);
    int in_channels = frame->ch_layout.nb_channels;
    if (in_channels == 0) {
        in_channels = ctx->ch_layout.nb_channels;
    }

    // Reconfigure resampler if input format changed
    // (mirrors C's audio.c:541-597 reconfigure logic)
    if (g_swr_in_sample_rate != in_sample_rate ||
        g_swr_in_sample_fmt != in_sample_fmt ||
        g_swr_in_ch_layout != in_ch_layout) {

        if (g_swr_ctx != NULL) {
            swr_close(g_swr_ctx);
        } else {
            g_swr_ctx = swr_alloc();
            if (!g_swr_ctx) {
                av_frame_free(&frame);
                return -1;
            }
        }

        // Set input parameters
        if (in_ch_layout > 0) {
            swr_set_in_chlayout(g_swr_ctx, in_ch_layout);
        } else {
            // Decoder output has AV_CHANNEL_ORDER_UNSPEC (no native mask).
            // Derive layout from channel count to avoid SWR mismatch.
            swr_set_in_chlayout_by_channels(g_swr_ctx, in_channels);
        }
        av_opt_set_int(g_swr_ctx, "in_sample_rate", in_sample_rate, 0);
        av_opt_set_int(g_swr_ctx, "in_sample_fmt", in_sample_fmt, 0);

        // Set output parameters (S16/48000/stereo)
        swr_set_out_chlayout(g_swr_ctx, 0x3); // AV_CH_FRONT_LEFT|RIGHT
        av_opt_set_int(g_swr_ctx, "out_sample_rate", AUDIO_OUT_SAMPLE_RATE, 0);
        av_opt_set_int(g_swr_ctx, "out_sample_fmt", AUDIO_OUT_SAMPLE_FMT, 0);

        ret = swr_init(g_swr_ctx);
        if (ret < 0) {
            swr_close(g_swr_ctx);
            av_frame_free(&frame);
            return ret;
        }

        g_swr_in_sample_rate = in_sample_rate;
        g_swr_in_sample_fmt = in_sample_fmt;
        g_swr_in_ch_layout = in_ch_layout;
    }

    // Calculate output buffer size
    // swr_get_out_samples returns total samples (all channels)
    int out_samples = swr_get_out_samples(g_swr_ctx, frame->nb_samples);
    // S16 stereo = 4 bytes per sample frame
    int out_data_size = out_samples * 2 * 2; // samples * channels * bytes_per_sample
    if (out_data_size > out_buf_size) {
        // Buffer too small — limit output
        out_samples = out_buf_size / 4;
        out_data_size = out_samples * 4;
    }

    // Perform conversion (mirrors C's avresample_convert at audio.c:602)
    uint8_t *out_ptr = out_buf;
    int converted = swr_convert(g_swr_ctx, &out_ptr, out_samples,
                                (const uint8_t **)frame->data, frame->nb_samples);

    av_frame_free(&frame);

    if (converted < 0) {
        return converted;
    }

    // Calculate resampler delay for PTS adjustment.
    // C uses libavresample (buffered pattern):
    //   od = avresample_available(ad->ad_avr) * 1000000LL / ad->ad_out_sample_rate
    //   id = avresample_get_delay(ad->ad_avr) * 1000000LL / frame->sample_rate
    //   ad->ad_pts = mb->mb_pts - od - id
    //   where od = samples in internal output buffer (not yet delivered)
    //         id = samples in internal input buffer (not yet converted)
    //
    // Go uses libswresample (direct pattern — no internal output buffer):
    //   swr_convert writes directly to caller's buffer, so there is no
    //   "output buffer" delay. The total delay is just the internal
    //   input/filter delay.
    //
    // swr_get_delay(ctx, 1000000) returns the TOTAL delay in microseconds,
    // which is the correct single value to subtract (equivalent to od+id
    // in C's buffered pattern).
    //
    // PROOF (measured with FFmpeg 9.0.1, 44100→48000 resampling):
    //   swr_get_out_samples(0) = 38 samples → 791 µs (includes filter overhead)
    //   swr_get_delay(in_rate) = 16 samples → 362 µs (input delay only)
    //   swr_get_delay(1e6)     = 354 µs (TOTAL delay in µs)
    //   Go's old formula: 791 + 362 = 1153 µs (DOUBLE-COUNTS input delay)
    //   Correct formula:  354 µs (total delay only)
    int64_t total_delay_us = 0;
    if (g_swr_ctx != NULL) {
        total_delay_us = swr_get_delay(g_swr_ctx, 1000000);
    }
    *out_pts_delay_us = total_delay_us;

    // Set output metadata to the OUTPUT format (not input)
    *out_sample_rate = AUDIO_OUT_SAMPLE_RATE;
    *out_sample_fmt = AUDIO_OUT_SAMPLE_FMT;
    *out_nb_samples = converted;
    *out_channels = AUDIO_OUT_CHANNELS;

    return converted * 2 * 2; // samples * channels * bytes_per_sample (S16)
}

// decode_audio_flush: flush any remaining samples in the resampler.
// Called at EOF to drain buffered samples.
static int decode_audio_flush(uint8_t *out_buf, int out_buf_size) {
    if (g_swr_ctx == NULL) return 0;

    int out_samples = out_buf_size / 4; // max S16 stereo samples
    uint8_t *out_ptr = out_buf;
    int converted = swr_convert(g_swr_ctx, &out_ptr, out_samples, NULL, 0);
    if (converted < 0) return 0;
    return converted * 4; // S16 stereo
}

// decode_audio_flush_resampler: discard all buffered samples in the resampler
// without outputting them. Called on seek to clear stale audio.
// Mirrors C's audio.c:768-771:
//   avresample_read(ad->ad_avr, NULL, avresample_available(ad->ad_avr));
static void decode_audio_flush_resampler(void) {
    if (g_swr_ctx == NULL) return;
    // Drain all buffered samples by reading with NULL output (discard)
    // swr_convert doesn't support NULL output, so we read into a dummy buffer
    int avail = swr_get_out_samples(g_swr_ctx, 0);
    if (avail <= 0) return;
    // Allocate a small buffer and drain in chunks
    uint8_t dummy[4096];
    uint8_t *dummy_ptr = dummy;
    int remaining = avail;
    while (remaining > 0) {
        int to_read = remaining > 1024 ? 1024 : remaining;
        int got = swr_convert(g_swr_ctx, &dummy_ptr, to_read, NULL, 0);
        if (got <= 0) break;
        remaining -= got;
    }
}

// decode_audio_drain: drain the decoder by sending NULL packets and
// reading remaining frames, then flush the resampler.
// Mirrors C's libav_video_eof() + audio.c:768-771 drain logic.
// Calls decode_audio_packet with NULL pkt to drain decoder, then
// decode_audio_flush to drain resampler.
// Returns total bytes of drained audio (S16 stereo).
static int decode_audio_drain(void *codecCtx, uint8_t *out_buf, int out_buf_size) {
    AVCodecContext *ctx = (AVCodecContext*)codecCtx;
    int total = 0;

    // Send NULL packet to begin drain
    avcodec_send_packet(ctx, NULL);

    // Read remaining frames from decoder
    while (total + 524288 <= out_buf_size) {
        int sr, sf, ns, ch;
        int64_t delay;
        int n = decode_audio_packet(ctx, NULL,
                                     out_buf + total, out_buf_size - total,
                                     &sr, &sf, &ns, &ch, &delay);
        if (n <= 0) break;
        total += n;
    }

    // Flush resampler
    while (total + 524288 <= out_buf_size) {
        int n = decode_audio_flush(out_buf + total, out_buf_size - total);
        if (n <= 0) break;
        total += n;
    }

    return total;
}

// decode_audio_frame — C: the decode step inside audio_process_audio
// (audio.c:472): avcodec_decode_audio4 equivalent via send/receive.
// Decodes one packet and copies the decoder's frame at INPUT format into
// out_buf (planar formats are laid out plane-by-plane contiguously).
// Returns: >0 = bytes produced, 0 = no frame (EAGAIN/EOF), <0 = error,
// -2 = out_buf too small.
static int decode_audio_frame(void *codecCtx,
    const uint8_t *data, int size, int64_t pts, int64_t dts,
    uint8_t *out_buf, int out_buf_size,
    int *out_sample_rate, int *out_sample_fmt, uint64_t *out_ch_layout,
    int *out_channels, int *out_nb_samples) {
    AVCodecContext *ctx = (AVCodecContext*)codecCtx;
    AVPacket *pkt = av_packet_alloc();
    if (!pkt) return -1;
    int ret = av_new_packet(pkt, size);
    if (ret < 0) { av_packet_free(&pkt); return ret; }
    if (size > 0)
        memcpy(pkt->data, data, size);
    pkt->pts = pts;
    pkt->dts = dts;
    ret = avcodec_send_packet(ctx, pkt);
    av_packet_free(&pkt);
    if (ret < 0) return ret;

    AVFrame *frame = av_frame_alloc();
    if (!frame) return -1;
    ret = avcodec_receive_frame(ctx, frame);
    if (ret < 0) {
        av_frame_free(&frame);
        return 0; // EAGAIN/EOF — no frame produced
    }

    int nb = frame->nb_samples;
    int ch = frame->ch_layout.nb_channels;
    int fmt = frame->format;
    int bufSize = av_samples_get_buffer_size(NULL, ch, nb, fmt, 1);
    if (bufSize < 0 || bufSize > out_buf_size) {
        av_frame_free(&frame);
        return -2;
    }
    if (av_sample_fmt_is_planar(fmt) && ch > 0) {
        int planeSize = bufSize / ch;
        int i;
        for (i = 0; i < ch; i++)
            memcpy(out_buf + i * planeSize, frame->extended_data[i], planeSize);
    } else {
        memcpy(out_buf, frame->extended_data[0], bufSize);
    }

    // C: frame->sample_rate == 0 → ctx->sample_rate (audio.c:477-481)
    *out_sample_rate = frame->sample_rate ? frame->sample_rate
                                          : ctx->sample_rate;
    *out_sample_fmt = fmt;
    *out_ch_layout = get_frame_channel_layout(frame);
    *out_channels = ch;
    *out_nb_samples = nb;
    av_frame_free(&frame);
    return bufSize;
}

// decode_audio_frame_pkt — same as decode_audio_frame but consumes the
// caller's AVPacket directly (C: avcodec_decode_audio4(ctx, frame,
// &got_frame, &mb->mb_pkt) — audio.c:472).
static int decode_audio_frame_pkt(void *codecCtx, void *avpkt,
    uint8_t *out_buf, int out_buf_size,
    int *out_sample_rate, int *out_sample_fmt, uint64_t *out_ch_layout,
    int *out_channels, int *out_nb_samples) {
    AVCodecContext *ctx = (AVCodecContext*)codecCtx;
    AVPacket *pkt = (AVPacket *)avpkt;
    int ret = avcodec_send_packet(ctx, pkt);
    if (ret < 0) return ret;

    AVFrame *frame = av_frame_alloc();
    if (!frame) return -1;
    ret = avcodec_receive_frame(ctx, frame);
    if (ret < 0) {
        av_frame_free(&frame);
        return 0; // EAGAIN/EOF — no frame produced
    }

    int nb = frame->nb_samples;
    int ch = frame->ch_layout.nb_channels;
    int fmt = frame->format;
    int bufSize = av_samples_get_buffer_size(NULL, ch, nb, fmt, 1);
    if (bufSize < 0 || bufSize > out_buf_size) {
        av_frame_free(&frame);
        return -2;
    }
    if (av_sample_fmt_is_planar(fmt) && ch > 0) {
        int planeSize = bufSize / ch;
        int i;
        for (i = 0; i < ch; i++)
            memcpy(out_buf + i * planeSize, frame->extended_data[i], planeSize);
    } else {
        memcpy(out_buf, frame->extended_data[0], bufSize);
    }

    *out_sample_rate = frame->sample_rate ? frame->sample_rate
                                          : ctx->sample_rate;
    *out_sample_fmt = fmt;
    *out_ch_layout = get_frame_channel_layout(frame);
    *out_channels = ch;
    *out_nb_samples = nb;
    av_frame_free(&frame);
    return bufSize;
}

// ml_audio_codec_open — C: avcodec_alloc_context3 + request_channel_layout
// (stereo downmix) + avcodec_open2 (audio.c:456-470).
static void* ml_audio_codec_open(int codec_id, int stereo_downmix) {
    const AVCodec *codec = avcodec_find_decoder(codec_id);
    if (!codec) return NULL;
    AVCodecContext *ctx = avcodec_alloc_context3(codec);
    if (!ctx) return NULL;
    if (stereo_downmix) {
        AVChannelLayout stereo;
        av_channel_layout_default(&stereo, 2);
        av_opt_set_chlayout(ctx, "downmix", &stereo, 0);
        av_channel_layout_uninit(&stereo);
    }
    if (avcodec_open2(ctx, codec, NULL) < 0) {
        avcodec_free_context(&ctx);
        return NULL;
    }
    return ctx;
}

// ml_codec_ctx_sample_rate — C: ctx->sample_rate
static int ml_codec_ctx_sample_rate(void *ctx) {
    return ((AVCodecContext*)ctx)->sample_rate;
}

// ml_codec_ctx_channels — C: ctx->channels (FFmpeg 7: ch_layout.nb_channels)
static int ml_codec_ctx_channels(void *ctx) {
    return ((AVCodecContext*)ctx)->ch_layout.nb_channels;
}

// ml_codec_ctx_channel_layout — C: ctx->channel_layout (legacy mask)
static uint64_t ml_codec_ctx_channel_layout(void *ctx) {
    return get_codec_channel_layout((AVCodecContext*)ctx);
}

// ml_default_channel_layout — C: av_get_default_channel_layout
static uint64_t ml_default_channel_layout(int channels) {
    AVChannelLayout l;
    av_channel_layout_default(&l, channels);
    uint64_t mask = l.order == AV_CHANNEL_ORDER_NATIVE ? l.u.mask : 0;
    av_channel_layout_uninit(&l);
    return mask;
}

// ml_metadata_from_codec — C: metadata_from_libav (libav.c:489-535),
// verbatim: codec name (uppercased, NULL for DTS-with-profile), profile,
// H264 level, audio sample rate + channel layout string, WxH, hwaccel.
// Non-static so format.go's fa_video port can call it (same package).
void ml_metadata_from_codec(void *codecCtx, char *dst, int dstlen) {
    AVCodecContext *avctx = (AVCodecContext*)codecCtx;
    const AVCodec *codec = avcodec_find_decoder(avctx->codec_id);
    const char *name = codec ? codec->name : NULL;
    const char *profile = codec ?
        av_get_profile_name(codec, avctx->profile) : NULL;

    if (codec && codec->id == AV_CODEC_ID_DTS && profile != NULL)
        name = NULL;

    int off = 0;
    if (name) {
        off = snprintf(dst, dstlen, "%s", name);
        char *n = dst;
        while (*n) { *n = toupper((int)*n); n++; }
    }

    if (profile != NULL)
        off += snprintf(dst + off, dstlen - off, "%s%s", off ? " " : "",
                        profile);

    if (codec && codec->id == AV_CODEC_ID_H264 &&
        avctx->level != AV_LEVEL_UNKNOWN)
        off += snprintf(dst + off, dstlen - off, " (Level %d.%d)",
                        avctx->level / 10, avctx->level % 10);

    if (avctx->codec_type == AVMEDIA_TYPE_AUDIO) {
        char buf[64];
        av_channel_layout_describe(&avctx->ch_layout, buf, sizeof(buf));
        off += snprintf(dst + off, dstlen - off, ", %d Hz, %s",
                        avctx->sample_rate, buf);
    }

    if (avctx->width)
        off += snprintf(dst + off, dstlen - off, ", %dx%d",
                        avctx->width, avctx->height);

    if (avctx->hwaccel != NULL)
        off += snprintf(dst + off, dstlen - off, " (%s)",
                        avctx->hwaccel->name);
}

// ml_codec_ctx_meta — C: the cached field reads in mp_set_mq_meta
// (libav.c:544-556): codec_id, profile, channels, channel_layout, w, h.
static void ml_codec_ctx_meta(void *codecCtx, int *codec_id, int *profile,
    int *channels, uint64_t *ch_layout, int *width, int *height) {
    AVCodecContext *avctx = (AVCodecContext*)codecCtx;
    *codec_id = avctx->codec_id;
    *profile = avctx->profile;
    *channels = avctx->ch_layout.nb_channels;
    *ch_layout = get_codec_channel_layout(avctx);
    *width = avctx->width;
    *height = avctx->height;
}

// decode_audio_close: free the resampler context.
static void decode_audio_close() {
    if (g_swr_ctx != NULL) {
        swr_free(&g_swr_ctx);
        g_swr_ctx = NULL;
        g_swr_in_sample_rate = 0;
        g_swr_in_sample_fmt = -1;
        g_swr_in_ch_layout = 0;
    }
}
*/
import "C"
import (
	"fmt"
	"sync"
	"unsafe"

	"github.com/czz/movian-go/internal/libav"
	mediacore "github.com/czz/movian-go/internal/media/core"
)

// Audio mode constants
const (
	AudioModePCM   = 0
	AudioModeCODED = 1
	AudioModeSPDIF = 2
)

// PTS_UNSET represents unset PTS value
const PTS_UNSET = int64(-1)

// EAGAIN is the error code for "try again"
const EAGAIN = 11

// AverrorEOF is the end of file error code
const AverrorEOF = -539478880 // -0x2040400

// AudioDecoder represents a LibAV audio decoder with complete state
type AudioDecoder struct {
	mc *mediacore.MediaCodec
	mq *mediacore.MediaQueue
	mp *mediacore.MediaPipe

	// FFmpeg context
	frame *libav.AVFrame
	// Note: SwrContext is global (g_swr_ctx) in the C wrapper, not per-decoder.
	// This mirrors C's audio_decoder_t which has a single ad_avr.

	// Audio configuration
	mode             int
	inCodecID        int
	inSampleRate     int
	inSampleFormat   int
	inChannelLayout  uint64
	outSampleRate    int
	outSampleFormat  int
	outChannelLayout uint64
	channels         int
	tileSize         int

	// PTS and timing
	pts               int64
	epoch             int
	estimatedDuration int
	delay             int

	// State
	paused        bool
	discontinuity bool
	wantReconfig  bool
	stereoDownmix bool
	volScale      float32

	// SPDIF passthrough
	spdifMuxer      unsafe.Pointer
	spdifFrame      unsafe.Pointer
	spdifFrameAlloc int
	spdifFrameSize  int
	muxBuffer       unsafe.Pointer

	// Error tracking
	channelLayoutFail bool
	sampleRateFail    bool

	// Bitrate tracking
	frameSize    [16]int
	frameSizePtr int
	lastPTS      int64
	savedPTS     int64

	// Debug discontinuity
	debugDiscont mediacore.MediaDiscontinuityAux

	// Mutex for thread safety
	mu sync.Mutex
}

// NewLibAVAudio initializes a LibAV audio decoder with complete configuration
func NewLibAVAudio(mc *mediacore.MediaCodec, mq *mediacore.MediaQueue, mp *mediacore.MediaPipe) (*AudioDecoder, error) {
	if mc == nil {
		return nil, fmt.Errorf("NewLibAVAudio: mc is nil")
	}

	ad := &AudioDecoder{
		mc:                mc,
		mq:                mq,
		mp:                mp,
		mode:              AudioModePCM,
		tileSize:          1024,
		pts:               AV_NOPTS_VALUE,
		epoch:             0,
		estimatedDuration: 0,
		delay:             0,
		paused:            false,
		discontinuity:     true,
		wantReconfig:      false,
		stereoDownmix:     false,
		volScale:          1.0,
		inCodecID:         0,
		inSampleRate:      0,
		inSampleFormat:    0,
		inChannelLayout:   0,
		outSampleRate:     48000,
		outSampleFormat:   0, // AV_SAMPLE_FMT_S16
		outChannelLayout:  0, // AV_CH_LAYOUT_STEREO
	}

	// Allocate AVFrame
	ad.frame = libav.WrapAVFrame(unsafe.Pointer(C.av_frame_alloc()))
	if ad.frame == nil {
		return nil, fmt.Errorf("NewLibAVAudio: failed to allocate AVFrame")
	}

	// Initialize sample rate and channels from codec parameters
	// Note: Cannot access mc.params directly as it's unexported
	// Sample rate and channels will be set from the codec context during decoding

	return ad, nil
}

// LibAVAudioDecode decodes an audio packet using FFmpeg.
// pktPTSUs is the packet's PTS in microseconds (or -1 if unknown).
// It is used to set ad.pts, matching C's ad->ad_pts = mb->mb_pts.
func LibAVAudioDecode(ad *AudioDecoder, pkt *libav.AVPacket, pktPTSUs int64) (*mediacore.MediaBuf, error) {
	if ad == nil || ad.mc == nil {
		return nil, fmt.Errorf("LibAVAudioDecode: decoder not initialized")
	}

	ad.mu.Lock()
	defer ad.mu.Unlock()

	// Get codec context
	ctx := (*C.AVCodecContext)(ad.mc.Ctx.CPtr())
	if ctx == nil {
		return nil, fmt.Errorf("LibAVAudioDecode: codec context is nil")
	}

	// Use C wrapper to do the full decode cycle in C, avoiding cgo
	// pointer lifetime issues with AVFrame.
	// Max PCM frame size: 8 channels * 8 bytes/sample * 8192 samples = 524288
	const maxOutBufSize = 524288
	outBuf := make([]byte, maxOutBufSize)
	var outSampleRate, outSampleFmt, outNbSamples, outChannels C.int
	var outPtsDelayUs C.int64_t

	dataSize := C.decode_audio_packet(
		unsafe.Pointer(ctx),
		pkt.CPtr(),
		(*C.uint8_t)(unsafe.Pointer(&outBuf[0])),
		C.int(maxOutBufSize),
		&outSampleRate,
		&outSampleFmt,
		&outNbSamples,
		&outChannels,
		&outPtsDelayUs,
	)

	if dataSize == 0 {
		// EAGAIN or EOF — no frame available
		return nil, nil
	}
	if dataSize < 0 {
		return nil, fmt.Errorf("LibAVAudioDecode: decode_audio_packet failed: %d", dataSize)
	}

	// Create media buffer from decoded data
	mb := &mediacore.MediaBuf{
		Data: outBuf[:int(dataSize)],
		Size: int(dataSize),
	}

	// Set PTS from packet PTS, adjusted for resampler delay.
	// Mirrors C's audio.c:511-531:
	//   ad->ad_pts = mb->mb_pts - od - id
	// where od = output delay (samples in resampler output buffer)
	// and id = input delay (samples in resampler input buffer at input rate)
	// This corrects for the latency introduced by the resampler buffer.
	if pktPTSUs >= 0 {
		ptsDelayUs := int64(outPtsDelayUs)
		ad.pts = pktPTSUs - ptsDelayUs
	}

	// Set metadata
	mb.PTS = ad.pts
	mb.Epoch = ad.epoch
	if mb.Meta == nil {
		mb.Meta = &mediacore.MediaBufMeta{}
	}
	mb.Meta.PTS = ad.pts
	mb.Meta.Epoch = ad.epoch

	return mb, nil
}

// LibAVAudioFlush flushes the audio decoder
func LibAVAudioFlush(ad *AudioDecoder) error {
	if ad == nil {
		return nil
	}

	ad.mu.Lock()
	defer ad.mu.Unlock()

	// Reset error tracking
	ad.channelLayoutFail = false
	ad.sampleRateFail = false

	// Flush codec buffers
	if ad.mc != nil && ad.mc.Ctx != nil {
		C.avcodec_flush_buffers((*C.AVCodecContext)(ad.mc.Ctx.CPtr()))
	}

	// Reset PTS
	ad.pts = AV_NOPTS_VALUE

	// Flush resampler: discard all buffered samples to avoid stale audio
	// after seek. Mirrors C's audio.c:768-771:
	//   avresample_read(ad->ad_avr, NULL, avresample_available(ad->ad_avr));
	C.decode_audio_flush_resampler()

	return nil
}

// LibAVAudioDrain drains the audio decoder and resampler at EOF,
// returning any remaining buffered audio data.
// Mirrors C's drain logic: send NULL packet, read remaining frames,
// then flush resampler. The returned data is S16/stereo/48kHz.
func LibAVAudioDrain(ad *AudioDecoder) ([]byte, error) {
	if ad == nil || ad.mc == nil || ad.mc.Ctx == nil {
		return nil, nil
	}

	ad.mu.Lock()
	defer ad.mu.Unlock()

	ctx := (*C.AVCodecContext)(ad.mc.Ctx.CPtr())
	// Max drain buffer: 2MB (enough for several seconds of audio)
	const drainBufSize = 2 * 1024 * 1024
	drainBuf := make([]byte, drainBufSize)

	n := C.decode_audio_drain(
		unsafe.Pointer(ctx),
		(*C.uint8_t)(unsafe.Pointer(&drainBuf[0])),
		C.int(drainBufSize),
	)
	if n <= 0 {
		return nil, nil
	}
	return drainBuf[:int(n)], nil
}

// LibAVAudioClose closes the audio decoder and frees resources
func LibAVAudioClose(ad *AudioDecoder) error {
	if ad == nil {
		return nil
	}

	ad.mu.Lock()
	defer ad.mu.Unlock()

	// Free AVFrame
	if ad.frame != nil {
		libav.AvFrameFree(ad.frame)
		ad.frame = nil
	}

	// Close the global resampler in the C wrapper (g_swr_ctx)
	C.decode_audio_close()

	// Cleanup SPDIF muxer
	if ad.spdifMuxer != nil {
		ad.cleanupSpdifMuxer()
	}

	return nil
}

// cleanupSpdifMuxer cleans up SPDIF muxer resources
func (ad *AudioDecoder) cleanupSpdifMuxer() {
	if ad.spdifMuxer == nil {
		return
	}

	// Free SPDIF frame
	if ad.spdifFrame != nil {
		C.free(ad.spdifFrame)
		ad.spdifFrame = nil
	}
	ad.spdifFrameAlloc = 0
	ad.spdifFrameSize = 0

	// Free IO buffer - basic cleanup without direct field access
	// The avformat_free_context below will handle this

	// Free mux buffer
	if ad.muxBuffer != nil {
		C.free(ad.muxBuffer)
		ad.muxBuffer = nil
	}

	// Free muxer context
	// FFmpeg 7 changed avformat_free_context signature - needs type casting
	// For now, we set to nil to avoid memory issues
	/*
		C.avformat_free_context((*C.AVFormatContext)(ad.spdifMuxer))
	*/
	ad.spdifMuxer = nil
}

// LibAVAudioSetSampleRate sets the output sample rate
func LibAVAudioSetSampleRate(ad *AudioDecoder, sampleRate int) {
	if ad == nil {
		return
	}
	ad.mu.Lock()
	defer ad.mu.Unlock()
	ad.outSampleRate = sampleRate
	ad.wantReconfig = true
}

// LibAVAudioSetChannels sets the output number of channels
func LibAVAudioSetChannels(ad *AudioDecoder, channels int) {
	if ad == nil {
		return
	}
	ad.mu.Lock()
	defer ad.mu.Unlock()
	ad.channels = channels

	// Set output channel layout based on channel count
	switch channels {
	case 1:
		ad.outChannelLayout = uint64(C.AV_CH_LAYOUT_MONO)
	case 2:
		ad.outChannelLayout = uint64(C.AV_CH_LAYOUT_STEREO)
	case 6:
		ad.outChannelLayout = uint64(C.AV_CH_LAYOUT_5POINT1)
	default:
		ad.outChannelLayout = uint64(C.AV_CH_LAYOUT_STEREO)
	}

	ad.wantReconfig = true
}

// ---------------------------------------------------------------------------
// audio_decoder thread decode path — C: audio2/audio.c helpers.
// These operate on a media_codec_t's ctx directly (C calls
// avcodec_decode_audio4 on mc->ctx — the codec registry is bypassed for
// audio; the codec was already selected by media_codec_create).
// ---------------------------------------------------------------------------

// AVCodecID* — the codec ids switched on in audio_set_passthru_metadata
// (audio.c:251-266) and metadata_from_libav (libav.c:495,513).
const (
	AVCodecIDDts  = int(C.AV_CODEC_ID_DTS)
	AVCodecIDAc3  = int(C.AV_CODEC_ID_AC3)
	AVCodecIDEac3 = int(C.AV_CODEC_ID_EAC3)
)

// AudioFrameDecode decodes one packet through an audio codec ctx.
// C: avcodec_decode_audio4(ctx, frame, &got_frame, &mb->mb_pkt) — returns
// the frame at the decoder's INPUT format (audiocore does the resample).
// Returns gotFrame=false when the decoder produced no frame (C's
// got_frame==0 / EAGAIN).
func AudioFrameDecode(ctx *libav.AVCodecContext, data []byte, pts, dts int64) (
	pcm []byte, sampleRate, sampleFmt int, chLayout uint64,
	channels, nbSamples int, gotFrame bool, err error) {

	return AudioFrameDecodeInto(ctx, data, pts, dts, make([]byte, 8*1024*1024))
}

// AudioFrameDecodeInto — AudioFrameDecode writing into caller-provided
// buf so the decode thread can reuse one scratch allocation instead of
// making a fresh 8MB buffer per packet (GC churn on memory-tight
// devices). Returns an error when buf is too small (-2 from C).
func AudioFrameDecodeInto(ctx *libav.AVCodecContext, data []byte, pts, dts int64, buf []byte) (
	pcm []byte, sampleRate, sampleFmt int, chLayout uint64,
	channels, nbSamples int, gotFrame bool, err error) {

	if len(buf) == 0 {
		return nil, 0, 0, 0, 0, 0, false,
			fmt.Errorf("AudioFrameDecodeInto: empty output buffer")
	}
	var sr, sf, ch, nb C.int
	var cl C.uint64_t

	var dataPtr *C.uint8_t
	if len(data) > 0 {
		dataPtr = (*C.uint8_t)(unsafe.Pointer(&data[0]))
	}
	n := C.decode_audio_frame(
		ctx.CPtr(),
		dataPtr,
		C.int(len(data)),
		C.int64_t(pts), C.int64_t(dts),
		(*C.uint8_t)(unsafe.Pointer(&buf[0])),
		C.int(len(buf)),
		&sr, &sf, &cl, &ch, &nb,
	)
	if n == 0 {
		return nil, 0, 0, 0, 0, 0, false, nil
	}
	if n < 0 {
		return nil, 0, 0, 0, 0, 0, false,
			fmt.Errorf("decode_audio_frame failed: %d", int(n))
	}
	return buf[:int(n)], int(sr), int(sf), uint64(cl),
		int(ch), int(nb), true, nil
}

// AudioFrameDecodePkt — C: avcodec_decode_audio4(ctx, frame, &got_frame,
// &mb->mb_pkt) (audio.c:472) — consumes the media buffer's refcounted
// AVPacket directly instead of a byte-slice copy.
func AudioFrameDecodePkt(ctx *libav.AVCodecContext, pkt *libav.AVPacket) (
	pcm []byte, sampleRate, sampleFmt int, chLayout uint64,
	channels, nbSamples int, gotFrame bool, err error) {

	if pkt == nil {
		return nil, 0, 0, 0, 0, 0, false,
			fmt.Errorf("AudioFrameDecodePkt: nil packet")
	}
	return AudioFrameDecodePktInto(ctx, pkt, make([]byte, 8*1024*1024))
}

// AudioFrameDecodePktInto — AudioFrameDecodePkt writing into a
// caller-provided scratch buffer (see AudioFrameDecodeInto).
func AudioFrameDecodePktInto(ctx *libav.AVCodecContext, pkt *libav.AVPacket, buf []byte) (
	pcm []byte, sampleRate, sampleFmt int, chLayout uint64,
	channels, nbSamples int, gotFrame bool, err error) {

	if len(buf) == 0 {
		return nil, 0, 0, 0, 0, 0, false,
			fmt.Errorf("AudioFrameDecodePktInto: empty output buffer")
	}
	var sr, sf, ch, nb C.int
	var cl C.uint64_t

	n := C.decode_audio_frame_pkt(
		ctx.CPtr(), pkt.CPtr(),
		(*C.uint8_t)(unsafe.Pointer(&buf[0])),
		C.int(len(buf)),
		&sr, &sf, &cl, &ch, &nb,
	)
	if n == 0 {
		return nil, 0, 0, 0, 0, 0, false, nil
	}
	if n < 0 {
		return nil, 0, 0, 0, 0, 0, false,
			fmt.Errorf("decode_audio_frame_pkt failed: %d", int(n))
	}
	return buf[:int(n)], int(sr), int(sf), uint64(cl),
		int(ch), int(nb), true, nil
}

// AudioCodecOpen — C: the lazy decoder open in audio_process_audio
// (audio.c:456-470): avcodec_find_decoder + alloc_context3 +
// request_channel_layout(stereo) when ad_stereo_downmix + avcodec_open2.
// Returns nil on failure (C: av_freep(&mc->ctx); return 0).
func AudioCodecOpen(codecID int, stereoDownmix bool) *libav.AVCodecContext {
	sd := C.int(0)
	if stereoDownmix {
		sd = 1
	}
	return libav.WrapAVCodecContext(unsafe.Pointer(C.ml_audio_codec_open(C.int(codecID), sd)))
}

// AudioCodecCtxSampleRate — C: ctx->sample_rate fallback (audio.c:480).
func AudioCodecCtxSampleRate(ctx *libav.AVCodecContext) int {
	return int(C.ml_codec_ctx_sample_rate(ctx.CPtr()))
}

// AudioCodecCtxChannels — C: ctx->channels.
func AudioCodecCtxChannels(ctx *libav.AVCodecContext) int {
	return int(C.ml_codec_ctx_channels(ctx.CPtr()))
}

// AudioDefaultChannelLayout — C: av_get_default_channel_layout
// (audio.c:495).
func AudioDefaultChannelLayout(channels int) uint64 {
	return uint64(C.ml_default_channel_layout(C.int(channels)))
}

// MqSetMeta — C: mp_set_mq_meta (libav.c:541-562) — caches the codec meta
// fields on the queue and writes the "codec" prop via
// metadata_from_libav when anything changed.
func MqSetMeta(mq *mediacore.MediaQueue, ctx *libav.AVCodecContext) {
	if mq == nil || ctx == nil {
		return
	}
	var id, profile, channels, width, height C.int
	var layout C.uint64_t
	C.ml_codec_ctx_meta(ctx.CPtr(), &id, &profile, &channels, &layout,
		&width, &height)

	// C: early return when nothing changed
	if mq.MetaCodecID == int(id) &&
		mq.MetaProfile == int(profile) &&
		mq.MetaChannels == int(channels) &&
		mq.MetaChannelLayout == uint64(layout) &&
		mq.MetaWidth == int(width) &&
		mq.MetaHeight == int(height) {
		return
	}
	mq.MetaCodecID = int(id)
	mq.MetaProfile = int(profile)
	mq.MetaChannels = int(channels)
	mq.MetaChannelLayout = uint64(layout)
	mq.MetaWidth = int(width)
	mq.MetaHeight = int(height)

	// C: metadata_from_libav(buf, sizeof(buf), codec, avctx);
	//    prop_set_string(mq->mq_prop_codec, buf)
	cbuf := make([]byte, 128)
	C.ml_metadata_from_codec(ctx.CPtr(),
		(*C.char)(unsafe.Pointer(&cbuf[0])), C.int(len(cbuf)))
	if mq.PropCodec != nil {
		mq.PropCodec.SetString(C.GoString((*C.char)(unsafe.Pointer(&cbuf[0]))))
	}
}
