package libav

/*
#include <libavformat/avformat.h>
#include <libavcodec/avcodec.h>
#include <libavutil/avutil.h>
#include <libavutil/dict.h>
#include <libavutil/error.h>
#include <libavutil/time.h>
#include <stdlib.h>
#include <string.h>

// Network timeout state for AVIOInterruptCB
// Mirrors C's fa_deadline() concept — provides a timeout mechanism for
// network reads to avoid indefinite blocking on stalled streams.
// C uses SO_RCVTIMEO via tcp_set_read_timeout; Go uses AVIOInterruptCB
// since it doesn't have the fileaccess layer.
struct net_timeout_state {
    int64_t deadline_us;  // Absolute deadline in microseconds (0 = no timeout)
};

// Wrapper to get AVFormatContext pointer
void* avformat_alloc_context_wrapper() {
    return avformat_alloc_context();
}

// Check if an av_read_frame return code is EOF
// Returns 1 if EOF, 0 otherwise
int ml_is_eof(int ret) {
    return ret == AVERROR_EOF;
}

// Check if an av_read_frame return code is EOF or EIO.
// C's fa_audio.c:245 treats both AVERROR_EOF and AVERROR(EIO) as EOF.
// Returns 1 if EOF/EIO, 0 otherwise
int ml_is_eof_or_eio(int ret) {
    return ret == AVERROR_EOF || ret == AVERROR(EIO);
}

// Check if an av_read_frame return code is EAGAIN (temporary, retry).
// C's fa_video.c:224 and fa_audio.c:242 retry on EAGAIN.
// Returns 1 if EAGAIN, 0 otherwise
int ml_is_eagain(int ret) {
    return ret == AVERROR(EAGAIN);
}

// Refresh the network timeout deadline for a format context.
// Called before each av_read_frame to implement per-operation timeout
// (mirrors C's fa_deadline() which is refreshed on each read).
// Returns 0 on success, -1 if context has no interrupt callback.
int ml_refresh_net_timeout(void *ctx, int64_t timeout_us) {
    AVFormatContext *s = (AVFormatContext*)ctx;
    if (s == NULL || s->interrupt_callback.callback == NULL)
        return -1;
    struct net_timeout_state *ts = (struct net_timeout_state*)s->interrupt_callback.opaque;
    if (ts == NULL)
        return -1;
    // Set deadline to now + timeout (per-operation, not global)
    ts->deadline_us = av_gettime_relative() + timeout_us;
    return 0;
}

// Clear the network timeout (for local files or after successful open)
int ml_clear_net_timeout(void *ctx) {
    AVFormatContext *s = (AVFormatContext*)ctx;
    if (s == NULL || s->interrupt_callback.callback == NULL)
        return -1;
    struct net_timeout_state *ts = (struct net_timeout_state*)s->interrupt_callback.opaque;
    if (ts == NULL)
        return -1;
    ts->deadline_us = 0; // No timeout
    return 0;
}

// Wrapper for avformat_open_input
int ml_avformat_open_input(void **ps, const char *url, void *fmt, void **options) {
    return avformat_open_input((AVFormatContext**)ps, url, (AVInputFormat*)fmt, (AVDictionary**)options);
}

static int net_interrupt_callback(void *opaque) {
    struct net_timeout_state *state = (struct net_timeout_state*)opaque;
    if (state == NULL || state->deadline_us == 0)
        return 0; // No timeout set, continue

    // Check current time vs deadline
    int64_t now = av_gettime_relative();
    if (now >= state->deadline_us)
        return 1; // Timeout expired — interrupt
    return 0; // Continue
}

// Wrapper to open format with network timeout for HTTP/HTTPS URLs.
// Sets AVIOInterruptCB with a 30-second timeout for network protocols.
int ml_avformat_open_input_with_timeout(void **ps, const char *url, void *fmt, void **options) {
    AVFormatContext *ctx = avformat_alloc_context();
    if (ctx == NULL)
        return -1;

    // Check if URL is a network protocol
    if (url != NULL &&
        (strncmp(url, "http://", 7) == 0 || strncmp(url, "https://", 8) == 0 ||
         strncmp(url, "rtmp://", 7) == 0 || strncmp(url, "rtsp://", 7) == 0 ||
         strncmp(url, "udp://", 6) == 0 || strncmp(url, "tcp://", 6) == 0)) {

        // Allocate timeout state (freed when context is closed)
        struct net_timeout_state *ts = (struct net_timeout_state*)av_malloc(sizeof(struct net_timeout_state));
        if (ts != NULL) {
            ts->deadline_us = av_gettime_relative() + 30000000; // 30 second timeout
            ctx->interrupt_callback.callback = net_interrupt_callback;
            ctx->interrupt_callback.opaque = ts;
        }
    }

    int ret = avformat_open_input(&ctx, url, (AVInputFormat*)fmt, (AVDictionary**)options);
    if (ret < 0) {
        // avformat_open_input frees ctx on failure, including interrupt_callback.opaque
        return ret;
    }
    *ps = ctx;
    return 0;
}

// Wrapper for avformat_find_stream_info
int ml_avformat_find_stream_info(void *ic, void **options) {
    return avformat_find_stream_info((AVFormatContext*)ic, (AVDictionary**)options);
}

// Wrapper for av_read_frame
int ml_av_read_frame(void *ic, void *pkt) {
    return av_read_frame((AVFormatContext*)ic, (AVPacket*)pkt);
}

// Wrapper for avformat_seek_file
int ml_avformat_seek_file(void *ic, int stream_index, int64_t min_ts, int64_t ts, int64_t max_ts, int flags) {
    return avformat_seek_file((AVFormatContext*)ic, stream_index, min_ts, ts, max_ts, flags);
}

// Wrapper for av_seek_frame — used for backward-compatible seeking
// that matches C's fa_video.c seek behavior.
int ml_av_seek_frame(void *ic, int stream_index, int64_t timestamp, int flags) {
    return av_seek_frame((AVFormatContext*)ic, stream_index, timestamp, flags);
}

// Get format start_time (in AV_TIME_BASE units = microseconds)
int64_t ml_get_start_time(void *ic) {
    AVFormatContext *ctx = (AVFormatContext*)ic;
    return ctx ? ctx->start_time : 0;
}

// Wrapper for avformat_close_input
void ml_avformat_close_input(void **ps) {
    avformat_close_input((AVFormatContext**)ps);
}

// Get format context fields
const char* get_format_name(void *ic) {
    AVFormatContext *ctx = (AVFormatContext*)ic;
    if (ctx->iformat && ctx->iformat->name) return ctx->iformat->name;
    return "unknown";
}

// C: fctx->iformat->long_name (fa_audio.c:231, fa_video.c)
const char* get_format_long_name(void *ic) {
    AVFormatContext *ctx = (AVFormatContext*)ic;
    if (ctx->iformat && ctx->iformat->long_name) return ctx->iformat->long_name;
    return "";
}

int64_t get_format_duration(void *ic) {
    AVFormatContext *ctx = (AVFormatContext*)ic;
    return ctx->duration;
}

int get_format_nb_streams(void *ic) {
    AVFormatContext *ctx = (AVFormatContext*)ic;
    return ctx->nb_streams;
}

int get_format_bit_rate(void *ic) {
    AVFormatContext *ctx = (AVFormatContext*)ic;
    return ctx->bit_rate;
}

// Get stream info
int get_stream_codec_type(void *ic, int index) {
    AVFormatContext *ctx = (AVFormatContext*)ic;
    if (index < 0 || index >= (int)ctx->nb_streams) return -1;
    return ctx->streams[index]->codecpar->codec_type;
}

int get_stream_codec_id(void *ic, int index) {
    AVFormatContext *ctx = (AVFormatContext*)ic;
    if (index < 0 || index >= (int)ctx->nb_streams) return -1;
    return ctx->streams[index]->codecpar->codec_id;
}

int get_stream_width(void *ic, int index) {
    AVFormatContext *ctx = (AVFormatContext*)ic;
    if (index < 0 || index >= (int)ctx->nb_streams) return 0;
    return ctx->streams[index]->codecpar->width;
}

int get_stream_height(void *ic, int index) {
    AVFormatContext *ctx = (AVFormatContext*)ic;
    if (index < 0 || index >= (int)ctx->nb_streams) return 0;
    return ctx->streams[index]->codecpar->height;
}

// Debug: log codecpar fields for a stream
void log_codecpar(void *ic, int index) {
    AVFormatContext *ctx = (AVFormatContext*)ic;
    if (index < 0 || index >= (int)ctx->nb_streams) return;
    AVCodecParameters *par = ctx->streams[index]->codecpar;
    av_log(NULL, AV_LOG_WARNING, "[CODECPAR] stream=%d codec_id=%d codec_type=%d w=%d h=%d extradata_size=%d\n",
           index, par->codec_id, par->codec_type, par->width, par->height, par->extradata_size);
}

int get_stream_sample_rate(void *ic, int index) {
    AVFormatContext *ctx = (AVFormatContext*)ic;
    if (index < 0 || index >= (int)ctx->nb_streams) return 0;
    return ctx->streams[index]->codecpar->sample_rate;
}

int get_stream_channels(void *ic, int index) {
    AVFormatContext *ctx = (AVFormatContext*)ic;
    if (index < 0 || index >= (int)ctx->nb_streams) return 0;
    return ctx->streams[index]->codecpar->ch_layout.nb_channels;
}

int64_t get_stream_start_time(void *ic, int index) {
    AVFormatContext *ctx = (AVFormatContext*)ic;
    if (index < 0 || index >= (int)ctx->nb_streams) return 0;
    return ctx->streams[index]->start_time;
}

AVRational get_stream_time_base(void *ic, int index) {
    AVFormatContext *ctx = (AVFormatContext*)ic;
    AVRational r = {0, 0};
    if (index < 0 || index >= (int)ctx->nb_streams) return r;
    return ctx->streams[index]->time_base;
}

int64_t get_stream_duration(void *ic, int index) {
    AVFormatContext *ctx = (AVFormatContext*)ic;
    if (index < 0 || index >= (int)ctx->nb_streams) return 0;
    return ctx->streams[index]->duration;
}

// Get packet fields
int64_t get_packet_pts(void *pkt) {
    return ((AVPacket*)pkt)->pts;
}

int64_t get_packet_dts(void *pkt) {
    return ((AVPacket*)pkt)->dts;
}

int get_packet_stream_index(void *pkt) {
    return ((AVPacket*)pkt)->stream_index;
}

int get_packet_size(void *pkt) {
    return ((AVPacket*)pkt)->size;
}

void* get_packet_data(void *pkt) {
    return ((AVPacket*)pkt)->data;
}

int64_t get_packet_duration(void *pkt) {
    return ((AVPacket*)pkt)->duration;
}

int get_packet_flags(void *pkt) {
    return ((AVPacket*)pkt)->flags;
}

// Copy codec parameters to context
int copy_codec_params_to_context(void *ic, int stream_index, void *codec_ctx) {
    AVFormatContext *fmtCtx = (AVFormatContext*)ic;
    if (stream_index < 0 || stream_index >= (int)fmtCtx->nb_streams) return -1;
    return avcodec_parameters_to_context((AVCodecContext*)codec_ctx, fmtCtx->streams[stream_index]->codecpar);
}

// Allocate an AVCodecContext holding a stream's codec parameters.
// This materializes what older FFmpeg exposed as stream->codec —
// the AVCodecContext* that media_codec_create callers pass as `ctx`.
void *codec_ctx_from_stream(void *ic, int stream_index) {
    AVFormatContext *fmtCtx = (AVFormatContext*)ic;
    if (stream_index < 0 || stream_index >= (int)fmtCtx->nb_streams) return NULL;
    AVCodecContext *ctx = avcodec_alloc_context3(NULL);
    if (ctx == NULL) return NULL;
    if (avcodec_parameters_to_context(ctx, fmtCtx->streams[stream_index]->codecpar) < 0) {
        avcodec_free_context(&ctx);
        return NULL;
    }
    return ctx;
}

// Copy codec context dst <- src (avcodec_copy_context replacement —
// FFmpeg 7 removed it; parameters round-trip is the equivalent).
int copy_codec_context(void *dst, void *src) {
    AVCodecParameters *par = avcodec_parameters_alloc();
    int ret;
    if (par == NULL) return -1;
    ret = avcodec_parameters_from_context(par, (AVCodecContext*)src);
    if (ret >= 0)
        ret = avcodec_parameters_to_context((AVCodecContext*)dst, par);
    avcodec_parameters_free(&par);
    return ret;
}

void free_codec_ctx(void *ctx) {
    AVCodecContext *c = (AVCodecContext*)ctx;
    avcodec_free_context(&c);
}

// Get extradata
int get_stream_extradata_size(void *ic, int stream_index) {
    AVFormatContext *ctx = (AVFormatContext*)ic;
    if (stream_index < 0 || stream_index >= (int)ctx->nb_streams) return 0;
    return ctx->streams[stream_index]->codecpar->extradata_size;
}

void* get_stream_extradata(void *ic, int stream_index) {
    AVFormatContext *ctx = (AVFormatContext*)ic;
    if (stream_index < 0 || stream_index >= (int)ctx->nb_streams) return NULL;
    return ctx->streams[stream_index]->codecpar->extradata;
}

// --- fa_video.c support helpers (C: stream/codec/chapter field access) ---

// avg_frame_rate — used for mb_duration and mp->mp_framerate (fa_video.c:247-254)
AVRational get_stream_avg_frame_rate(void *ic, int index) {
    AVFormatContext *ctx = (AVFormatContext*)ic;
    AVRational r = {0, 0};
    if (index < 0 || index >= (int)ctx->nb_streams) return r;
    return ctx->streams[index]->avg_frame_rate;
}

// sample_aspect_ratio — mcp.sar_num/sar_den (fa_video.c:777-778)
AVRational get_stream_sample_aspect_ratio(void *ic, int index) {
    AVFormatContext *ctx = (AVFormatContext*)ic;
    AVRational r = {0, 0};
    if (index < 0 || index >= (int)ctx->nb_streams) return r;
    return ctx->streams[index]->sample_aspect_ratio;
}

// codecpar time_base analog — C: ctx->time_base (codec context) used as
// the frame-rate fallback in fa_video.c:783-784. FFmpeg 7 has no
// stream->codec; the demuxer's packet time_base is the equivalent source.
AVRational get_stream_codec_time_base(void *ic, int index) {
    AVFormatContext *ctx = (AVFormatContext*)ic;
    AVRational r = {0, 1};
    if (index < 0 || index >= (int)ctx->nb_streams) return r;
    return ctx->streams[index]->time_base;
}

// disposition — AV_DISPOSITION_ATTACHED_PIC check (fa_probe.c:457)
int get_stream_disposition(void *ic, int index) {
    AVFormatContext *ctx = (AVFormatContext*)ic;
    if (index < 0 || index >= (int)ctx->nb_streams) return 0;
    return ctx->streams[index]->disposition;
}

// codecpar profile/level — mcp.profile/mcp.level (fa_video.c:775-776)
int get_stream_profile(void *ic, int index) {
    AVFormatContext *ctx = (AVFormatContext*)ic;
    if (index < 0 || index >= (int)ctx->nb_streams) return 0;
    return ctx->streams[index]->codecpar->profile;
}

int get_stream_level(void *ic, int index) {
    AVFormatContext *ctx = (AVFormatContext*)ic;
    if (index < 0 || index >= (int)ctx->nb_streams) return 0;
    return ctx->streams[index]->codecpar->level;
}

// C: ctx->channels = 0 for AV_CODEC_ID_DTS (fa_video.c:792-793) —
// writes through to codecpar (the FFmpeg7 analog of stream->codec).
void set_stream_channels(void *ic, int index, int channels) {
    AVFormatContext *ctx = (AVFormatContext*)ic;
    if (index < 0 || index >= (int)ctx->nb_streams) return;
    ctx->streams[index]->codecpar->ch_layout.nb_channels = channels;
}

// av_dict_get(st->metadata, key) — stream metadata (fa_video.c:797-798,
// chapter titles, language/title for fa_lavf_load_meta)
const char* get_stream_metadata(void *ic, int index, const char *key) {
    AVFormatContext *ctx = (AVFormatContext*)ic;
    if (index < 0 || index >= (int)ctx->nb_streams) return NULL;
    AVDictionaryEntry *e = av_dict_get(ctx->streams[index]->metadata,
                                       key, NULL, AV_DICT_IGNORE_SUFFIX);
    return e ? e->value : NULL;
}

// Format-level metadata — av_dict_get(fctx->metadata, key)
const char* get_format_metadata_value(void *ic, const char *key) {
    AVFormatContext *ctx = (AVFormatContext*)ic;
    AVDictionaryEntry *e = av_dict_get(ctx->metadata, key, NULL,
                                       AV_DICT_IGNORE_SUFFIX);
    return e ? e->value : NULL;
}

// Chapters — fctx->nb_chapters / fctx->chapters[i] (fa_video.c:478-497)
int get_nb_chapters(void *ic) {
    AVFormatContext *ctx = (AVFormatContext*)ic;
    return ctx ? (int)ctx->nb_chapters : 0;
}

// Returns chapter start/end in AV_TIME_BASE (microseconds) via
// av_rescale_q, plus the chapter title (or NULL).
void get_chapter(void *ic, int index, int64_t *start_us, int64_t *end_us,
                 const char **title) {
    AVFormatContext *ctx = (AVFormatContext*)ic;
    *start_us = 0; *end_us = 0; *title = NULL;
    if (index < 0 || index >= (int)ctx->nb_chapters) return;
    AVChapter *ch = ctx->chapters[index];
    *start_us = av_rescale_q(ch->start, ch->time_base, AV_TIME_BASE_Q);
    *end_us   = av_rescale_q(ch->end,   ch->time_base, AV_TIME_BASE_Q);
    AVDictionaryEntry *e = av_dict_get(ch->metadata, "title", NULL,
                                       AV_DICT_IGNORE_SUFFIX);
    if (e) *title = e->value;
}

// avio_size (fa_video.c:671) — va.filesize
int64_t ml_avio_size(void *avio) {
    return avio_size((AVIOContext*)avio);
}

// convergence_duration (fa_video.c:264 — subtitle packets).
// FFmpeg 7 removed AVPacket.convergence_duration; duration is the
// surviving field (C's `convergence_duration ?: duration` collapses to it).
int64_t get_packet_convergence_duration(void *pkt) {
    return ((AVPacket*)pkt)->duration;
}

// codecname(codec_id) — C: libav.c codecname() → avcodec_get_name
const char* ml_codec_name(int codec_id) {
    return avcodec_get_name((enum AVCodecID)codec_id);
}

// avcodec_find_decoder != NULL check (fa_lavf_load_meta has_video/audio)
int ml_has_decoder(int codec_id) {
    return avcodec_find_decoder((enum AVCodecID)codec_id) != NULL;
}

// av_dict_get_int equivalent — read an integer metadata value
// C: libav_metadata_int (fa_probe.c:129-139) — non-digit first char → def.
int64_t get_format_metadata_int(void *ic, const char *key, int64_t defval) {
    AVFormatContext *ctx = (AVFormatContext*)ic;
    AVDictionaryEntry *e = av_dict_get(ctx->metadata, key, NULL,
                                       AV_DICT_IGNORE_SUFFIX);
    if (!e || !e->value) return defval;
    return e->value[0] >= '0' && e->value[0] <= '9' ?
        strtoll(e->value, NULL, 10) : defval;
}

// metadata_from_libav (libav.c:489-535) — defined in libav_audio.go's
// preamble; declared here for format.go callers.
void ml_metadata_from_codec(void *codecCtx, char *dst, int dstlen);

// get_format_metadata extracts a metadata value from the format context.
// Returns NULL if the key is not found.
const char* get_format_metadata(void *ic, const char *key) {
    AVFormatContext *ctx = (AVFormatContext*)ic;
    if (!ctx || !ctx->metadata) return NULL;
    AVDictionaryEntry *entry = av_dict_get(ctx->metadata, key, NULL, 0);
    return entry ? entry->value : NULL;
}
*/
import "C"
import (
	"fmt"
	"time"
	"unsafe"

	"github.com/czz/movian-go/internal/libav"
)

// AVFormatCtx is a wrapper around AVFormatContext
type AVFormatCtx struct {
	ctx *libav.AVFormatContext
}

// StreamInfo holds info about a single stream
type StreamInfo struct {
	Index       int
	CodecType   int // AVMEDIA_TYPE_VIDEO=0, AVMEDIA_TYPE_AUDIO=1, AVMEDIA_TYPE_SUBTITLE=3
	CodecID     int
	Width       int
	Height      int
	SampleRate  int
	Channels    int
	StartTime   int64
	Duration    int64
	TimeBaseNum int
	TimeBaseDen int
}

// AVMediaType constants
const (
	AVMediaTypeVideo    = 0
	AVMediaTypeAudio    = 1
	AVMediaTypeSubtitle = 3
)

// AVPacketInfo holds extracted packet info
type AVPacketInfo struct {
	StreamIndex int
	PTS         int64
	DTS         int64
	Duration    int64
	Size        int
	Flags       int
	Data        []byte
}

// OpenFormat opens a media file with avformat_open_input.
// For network URLs (http, https, rtmp, etc.), sets a 30-second I/O timeout
// via AVIOInterruptCB to prevent indefinite blocking on stalled streams.
// This mirrors C's fa_deadline() + tcp_set_read_timeout() mechanism.
func OpenFormat(url string) (*AVFormatCtx, error) {
	cUrl := C.CString(url)
	defer C.free(unsafe.Pointer(cUrl))

	var fmtCtx unsafe.Pointer
	ret := C.ml_avformat_open_input_with_timeout(&fmtCtx, cUrl, nil, nil)
	if ret < 0 {
		return nil, fmt.Errorf("avformat_open_input failed for %s: %d", url, ret)
	}

	// Find stream info
	ret = C.ml_avformat_find_stream_info(fmtCtx, nil)
	if ret < 0 {
		C.ml_avformat_close_input(&fmtCtx)
		return nil, fmt.Errorf("avformat_find_stream_info failed: %d", ret)
	}

	return &AVFormatCtx{ctx: libav.WrapAVFormatContext(unsafe.Pointer(fmtCtx))}, nil
}

// WrapFormatCtx wraps a raw AVFormatContext (e.g. from
// pkg/libav's FALibavOpenFormat) as an AVFormatCtx for stream/packet
// access. Non-owning: the caller manages the context lifetime.
func WrapFormatCtx(ctx *libav.AVFormatContext) *AVFormatCtx {
	return &AVFormatCtx{ctx: ctx}
}

// ReadPacketRaw reads the next packet and returns it as *libav.AVPacket.
// C: av_read_frame(fctx, &pkt) — the caller owns the returned packet and
// must av_packet_free it (libav.AvPacketFree). Returns nil on EOF/EIO
// and an error on other failures; EAGAIN is retried like ReadPacket.
func (f *AVFormatCtx) ReadPacketRaw() (*libav.AVPacket, error) {
	if f.ctx == nil {
		return nil, fmt.Errorf("format context is nil")
	}
	pkt := C.av_packet_alloc()
	if pkt == nil {
		return nil, fmt.Errorf("failed to allocate packet")
	}
	// C: av_read_frame's AVERROR(EAGAIN) → `continue` (fa_audio.c:242)
	// — retry indefinitely; the caller has no other cancellation path.
	for {
		ret := C.ml_av_read_frame(f.ctx.CPtr(), unsafe.Pointer(pkt))
		if ret < 0 {
			if C.ml_is_eagain(C.int(ret)) == 1 {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			C.av_packet_free(&pkt)
			if C.ml_is_eof_or_eio(C.int(ret)) == 1 {
				return nil, nil // EOF
			}
			return nil, fmt.Errorf("av_read_frame error: %d", int(ret))
		}
		return libav.WrapAVPacket(unsafe.Pointer(pkt)), nil
	}
}

// SeekFrame seeks with AVSEEK_FLAG_BACKWARD on the raw timestamp.
// C: av_seek_frame(fctx, -1, ts, AVSEEK_FLAG_BACKWARD) (fa_audio.c:319)
// — unlike SeekTimestamp this does NOT add start_time; the caller
// computes the target like C does.
func (f *AVFormatCtx) SeekFrame(ts int64) error {
	if f.ctx == nil {
		return fmt.Errorf("format context is nil")
	}
	ret := C.ml_av_seek_frame(f.ctx.CPtr(), -1, C.int64_t(ts), C.AVSEEK_FLAG_BACKWARD)
	if ret < 0 {
		return fmt.Errorf("av_seek_frame failed: %d", int(ret))
	}
	return nil
}

// SeekFrameStream seeks on a specific stream index.
// C: av_seek_frame(fctx, stream_index, ts, AVSEEK_FLAG_BACKWARD)
// (fa_imageloader.c:594 — the video-thumbnail seek).
func (f *AVFormatCtx) SeekFrameStream(streamIndex int, ts int64) error {
	if f.ctx == nil {
		return fmt.Errorf("format context is nil")
	}
	ret := C.ml_av_seek_frame(f.ctx.CPtr(), C.int(streamIndex), C.int64_t(ts),
		C.AVSEEK_FLAG_BACKWARD)
	if ret < 0 {
		return fmt.Errorf("av_seek_frame failed: %d", int(ret))
	}
	return nil
}

// SetGenPTS sets AVFMT_FLAG_GENPTS on the format context.
// C: fctx->flags |= AVFMT_FLAG_GENPTS (fa_imageloader.c:517 — the avi
// workaround in fa_image_from_video2).
func (f *AVFormatCtx) SetGenPTS() {
	if f.ctx != nil {
		(*C.AVFormatContext)(f.ctx.CPtr()).flags |= C.AVFMT_FLAG_GENPTS
	}
}

// Close closes the format context
func (f *AVFormatCtx) Close() {
	if f.ctx != nil {
		cCtx := f.ctx.CPtr()
		C.ml_avformat_close_input(&cCtx)
		f.ctx = nil
	}
}

// GetFormatName returns the format name (e.g. "matroska,webm")
func (f *AVFormatCtx) GetFormatName() string {
	if f.ctx == nil {
		return ""
	}
	return C.GoString(C.get_format_name(f.ctx.CPtr()))
}

// GetFormatLongName returns iformat->long_name
// C: fctx->iformat->long_name (fa_audio.c:231)
func (f *AVFormatCtx) GetFormatLongName() string {
	if f.ctx == nil {
		return ""
	}
	return C.GoString(C.get_format_long_name(f.ctx.CPtr()))
}

// GetDuration returns duration in microseconds (AV_TIME_BASE)
func (f *AVFormatCtx) GetDuration() int64 {
	if f.ctx == nil {
		return 0
	}
	return int64(C.get_format_duration(f.ctx.CPtr()))
}

// GetMetadata extracts metadata key-value pairs from the format context.
// This mirrors C's metadata_from_fctx() in fa_video.c.
func (f *AVFormatCtx) GetMetadata() map[string]string {
	md := make(map[string]string)
	if f.ctx == nil {
		return md
	}
	for _, key := range []string{"title", "artist", "author", "album", "comment", "genre", "date", "track", "description"} {
		cKey := C.CString(key)
		cVal := C.get_format_metadata(f.ctx.CPtr(), cKey)
		C.free(unsafe.Pointer(cKey))
		if cVal != nil {
			md[key] = C.GoString(cVal)
		}
	}
	return md
}

// GetBitRate returns the bit rate
func (f *AVFormatCtx) GetBitRate() int {
	if f.ctx == nil {
		return 0
	}
	return int(C.get_format_bit_rate(f.ctx.CPtr()))
}

// GetNumStreams returns the number of streams
func (f *AVFormatCtx) GetNumStreams() int {
	if f.ctx == nil {
		return 0
	}
	return int(C.get_format_nb_streams(f.ctx.CPtr()))
}

// GetStreamInfo returns info about a stream
func (f *AVFormatCtx) GetStreamInfo(index int) *StreamInfo {
	if f.ctx == nil {
		return nil
	}
	nbStreams := int(C.get_format_nb_streams(f.ctx.CPtr()))
	if index < 0 || index >= nbStreams {
		return nil
	}

	tb := C.get_stream_time_base(f.ctx.CPtr(), C.int(index))
	return &StreamInfo{
		Index:       index,
		CodecType:   int(C.get_stream_codec_type(f.ctx.CPtr(), C.int(index))),
		CodecID:     int(C.get_stream_codec_id(f.ctx.CPtr(), C.int(index))),
		Width:       int(C.get_stream_width(f.ctx.CPtr(), C.int(index))),
		Height:      int(C.get_stream_height(f.ctx.CPtr(), C.int(index))),
		SampleRate:  int(C.get_stream_sample_rate(f.ctx.CPtr(), C.int(index))),
		Channels:    int(C.get_stream_channels(f.ctx.CPtr(), C.int(index))),
		StartTime:   int64(C.get_stream_start_time(f.ctx.CPtr(), C.int(index))),
		Duration:    int64(C.get_stream_duration(f.ctx.CPtr(), C.int(index))),
		TimeBaseNum: int(tb.num),
		TimeBaseDen: int(tb.den),
	}
}

// GetStreams returns info about all streams
func (f *AVFormatCtx) GetStreams() []*StreamInfo {
	n := f.GetNumStreams()
	streams := make([]*StreamInfo, n)
	for i := range n {
		streams[i] = f.GetStreamInfo(i)
	}
	return streams
}

// FindBestStream finds the best stream of a given type
func (f *AVFormatCtx) FindBestStream(codecType int) int {
	for i := range f.GetNumStreams() {
		si := f.GetStreamInfo(i)
		if si != nil && si.CodecType == codecType {
			return i
		}
	}
	return -1
}

// ReadPacket reads the next packet from the format context.
// Returns (nil, nil) at EOF, (nil, error) on network/IO error.
// This distinction is critical: EOF triggers handleEOF + track advancement,
// while network errors should trigger retry/error handling, NOT EOF.
func (f *AVFormatCtx) ReadPacket() (*AVPacketInfo, error) {
	if f.ctx == nil {
		return nil, fmt.Errorf("format context is nil")
	}

	pkt := C.av_packet_alloc()
	if pkt == nil {
		return nil, fmt.Errorf("failed to allocate packet")
	}
	defer C.av_packet_free((**C.AVPacket)(unsafe.Pointer(&pkt)))

	// Refresh network timeout deadline before each read (per-operation,
	// not global). This mirrors C's fa_deadline() which is refreshed
	// on each read operation. For local files, this is a no-op.
	C.ml_refresh_net_timeout(f.ctx.CPtr(), 30000000) // 30s per-read

	// Retry loop for EAGAIN (mirrors C's fa_video.c:224 and fa_audio.c:242
	// which do `if(r == AVERROR(EAGAIN)) continue;`)
	const maxEAGAINRetries = 10
	for range maxEAGAINRetries {
		ret := C.ml_av_read_frame(f.ctx.CPtr(), unsafe.Pointer(pkt))
		if ret < 0 {
			// EAGAIN — temporary, retry (mirrors C's continue on EAGAIN)
			if C.ml_is_eagain(C.int(ret)) == 1 {
				// Brief sleep before retry to avoid busy-loop
				time.Sleep(10 * time.Millisecond)
				continue
			}
			// EOF or EIO — treat as EOF (mirrors C's fa_audio.c:245
			// which treats both AVERROR_EOF and AVERROR(EIO) as EOF)
			if C.ml_is_eof_or_eio(C.int(ret)) == 1 {
				return nil, nil // EOF
			}
			// Other error — return as error, NOT EOF
			return nil, fmt.Errorf("av_read_frame error: %d", int(ret))
		}
		// Success — break out of retry loop
		break
	}

	// If we exhausted EAGAIN retries, return as error
	// (C retries indefinitely, but we limit to avoid infinite loops)
	// Check if the last call left a valid packet
	dataPtr := C.get_packet_data(unsafe.Pointer(pkt))
	if dataPtr == nil {
		return nil, fmt.Errorf("av_read_frame: exhausted EAGAIN retries")
	}

	// Extract packet data
	size := int(C.get_packet_size(unsafe.Pointer(pkt)))
	var data []byte
	if dataPtr != nil && size > 0 {
		data = C.GoBytes(dataPtr, C.int(size))
	}

	return &AVPacketInfo{
		StreamIndex: int(C.get_packet_stream_index(unsafe.Pointer(pkt))),
		PTS:         int64(C.get_packet_pts(unsafe.Pointer(pkt))),
		DTS:         int64(C.get_packet_dts(unsafe.Pointer(pkt))),
		Duration:    int64(C.get_packet_duration(unsafe.Pointer(pkt))),
		Size:        size,
		Flags:       int(C.get_packet_flags(unsafe.Pointer(pkt))),
		Data:        data,
	}, nil
}

// SeekTimestamp seeks to a given timestamp (in AV_TIME_BASE units).
// Renamed from Seek to avoid go vet false positive (io.Seeker signature check);
// this is a media-specific timestamp seek, not an io.Seeker implementation.
func (f *AVFormatCtx) SeekTimestamp(timestamp int64) error {
	if f.ctx == nil {
		return fmt.Errorf("format context is nil")
	}

	// Get duration and start_time from format context
	duration := int64(C.get_format_duration(f.ctx.CPtr()))
	startTime := int64(C.ml_get_start_time(f.ctx.CPtr()))

	// Clamp to [0, duration] and add start_time (matches C's fa_video.c)
	pos := timestamp
	if duration > 0 && pos > duration {
		pos = duration
	}
	if pos < 0 {
		pos = 0
	}
	pos += startTime

	// Use av_seek_frame with AVSEEK_FLAG_BACKWARD (matches C exactly).
	// AVSEEK_FLAG_BACKWARD finds the keyframe BEFORE the timestamp.
	// Without it, the seek fails for MP4 when no keyframe exists at/after
	// the exact timestamp (confirmed by C test on same file).
	ret := C.ml_av_seek_frame(f.ctx.CPtr(), -1, C.int64_t(pos), C.AVSEEK_FLAG_BACKWARD)
	if ret < 0 {
		return fmt.Errorf("av_seek_frame failed: %d", int(ret))
	}
	return nil
}

// GetStartTime returns the format context's start_time in microseconds
// (AV_TIME_BASE units). This is needed by the caller to align seekTarget
// to the same coordinate system as packet PTS values, matching C's
// mq_seektarget = pos where pos = timestamp + start_time.
func (f *AVFormatCtx) GetStartTime() int64 {
	if f.ctx == nil {
		return 0
	}
	return int64(C.ml_get_start_time(f.ctx.CPtr()))
}

// LibavCtx returns the wrapped *libav.AVFormatContext (borrow).
func (f *AVFormatCtx) LibavCtx() *libav.AVFormatContext {
	return f.ctx
}

// CodecCtxFromStream materializes an AVCodecContext holding the stream's
// codec parameters — the FFmpeg 7 equivalent of what older FFmpeg exposed
// as stream->codec. The returned pointer is owned by the caller (free
// with FreeCodecCtx) and is passed to media_codec_create as `ctx`
// (mc->fmt_ctx), which the lavc codec open copies via copy_codec_context.
func (f *AVFormatCtx) CodecCtxFromStream(streamIndex int) *libav.AVCodecContext {
	if f.ctx == nil {
		return nil
	}
	return libav.WrapAVCodecContext(C.codec_ctx_from_stream(f.ctx.CPtr(), C.int(streamIndex)))
}

// FreeCodecCtx frees a context allocated by CodecCtxFromStream.
func FreeCodecCtx(ctx *libav.AVCodecContext) {
	if ctx != nil {
		C.free_codec_ctx(ctx.CPtr())
	}
}

// GetStreamExtradata returns the extradata for a stream
func (f *AVFormatCtx) GetStreamExtradata(streamIndex int) []byte {
	if f.ctx == nil {
		return nil
	}
	size := int(C.get_stream_extradata_size(f.ctx.CPtr(), C.int(streamIndex)))
	if size <= 0 {
		return nil
	}
	dataPtr := C.get_stream_extradata(f.ctx.CPtr(), C.int(streamIndex))
	if dataPtr == nil {
		return nil
	}
	return C.GoBytes(dataPtr, C.int(size))
}

// --- fa_video.c support accessors ---

// GetStreamAvgFrameRate — C: fctx->streams[i]->avg_frame_rate
func (f *AVFormatCtx) GetStreamAvgFrameRate(index int) (num, den int) {
	if f.ctx == nil {
		return 0, 0
	}
	r := C.get_stream_avg_frame_rate(f.ctx.CPtr(), C.int(index))
	return int(r.num), int(r.den)
}

// GetStreamSampleAspectRatio — C: st->sample_aspect_ratio
func (f *AVFormatCtx) GetStreamSampleAspectRatio(index int) (num, den int) {
	if f.ctx == nil {
		return 0, 0
	}
	r := C.get_stream_sample_aspect_ratio(f.ctx.CPtr(), C.int(index))
	return int(r.num), int(r.den)
}

// GetStreamCodecTimeBase — C: ctx->time_base of the stream's codec ctx
// (fa_video.c:783-784 frame-rate fallback).
func (f *AVFormatCtx) GetStreamCodecTimeBase(index int) (num, den int) {
	if f.ctx == nil {
		return 0, 1
	}
	r := C.get_stream_codec_time_base(f.ctx.CPtr(), C.int(index))
	return int(r.num), int(r.den)
}

// GetStreamDisposition — C: st->disposition
func (f *AVFormatCtx) GetStreamDisposition(index int) int {
	if f.ctx == nil {
		return 0
	}
	return int(C.get_stream_disposition(f.ctx.CPtr(), C.int(index)))
}

// GetStreamProfile / GetStreamLevel — C: ctx->profile / ctx->level
func (f *AVFormatCtx) GetStreamProfile(index int) int {
	if f.ctx == nil {
		return 0
	}
	return int(C.get_stream_profile(f.ctx.CPtr(), C.int(index)))
}

func (f *AVFormatCtx) GetStreamLevel(index int) int {
	if f.ctx == nil {
		return 0
	}
	return int(C.get_stream_level(f.ctx.CPtr(), C.int(index)))
}

// SetStreamChannels — C: ctx->channels = 0 for AV_CODEC_ID_DTS
// (fa_video.c:792-793)
func (f *AVFormatCtx) SetStreamChannels(index, channels int) {
	if f.ctx == nil {
		return
	}
	C.set_stream_channels(f.ctx.CPtr(), C.int(index), C.int(channels))
}

// GetStreamMetadata — C: av_dict_get(st->metadata, key, NULL,
// AV_DICT_IGNORE_SUFFIX)
func (f *AVFormatCtx) GetStreamMetadata(index int, key string) string {
	if f.ctx == nil {
		return ""
	}
	ck := C.CString(key)
	defer C.free(unsafe.Pointer(ck))
	v := C.get_stream_metadata(f.ctx.CPtr(), C.int(index), ck)
	if v == nil {
		return ""
	}
	return C.GoString(v)
}

// GetFormatMetadataValue — C: av_dict_get(fctx->metadata, key)
func (f *AVFormatCtx) GetFormatMetadataValue(key string) string {
	if f.ctx == nil {
		return ""
	}
	ck := C.CString(key)
	defer C.free(unsafe.Pointer(ck))
	v := C.get_format_metadata_value(f.ctx.CPtr(), ck)
	if v == nil {
		return ""
	}
	return C.GoString(v)
}

// GetFormatMetadataInt — C: libav_metadata_int (numeric dict value)
func (f *AVFormatCtx) GetFormatMetadataInt(key string, defval int64) int64 {
	if f.ctx == nil {
		return defval
	}
	ck := C.CString(key)
	defer C.free(unsafe.Pointer(ck))
	return int64(C.get_format_metadata_int(f.ctx.CPtr(), ck, C.int64_t(defval)))
}

// ChapterInfo — C: AVChapter fields used by build_chapters
type ChapterInfo struct {
	StartUS int64 // microseconds (rescaled to AV_TIME_BASE)
	EndUS   int64
	Title   string // may be empty
}

// GetNumChapters — C: fctx->nb_chapters
func (f *AVFormatCtx) GetNumChapters() int {
	if f.ctx == nil {
		return 0
	}
	return int(C.get_nb_chapters(f.ctx.CPtr()))
}

// GetChapter — C: fctx->chapters[i] with av_rescale_q + title dict entry
func (f *AVFormatCtx) GetChapter(index int) *ChapterInfo {
	if f.ctx == nil {
		return nil
	}
	var startUS, endUS C.int64_t
	var title *C.char
	C.get_chapter(f.ctx.CPtr(), C.int(index), &startUS, &endUS, &title)
	ci := &ChapterInfo{StartUS: int64(startUS), EndUS: int64(endUS)}
	if title != nil {
		ci.Title = C.GoString(title)
	}
	return ci
}

// PacketFlags — C: pkt.flags (fa_video.c:311 AV_PKT_FLAG_KEY)
func PacketFlags(pkt *libav.AVPacket) int {
	if pkt == nil {
		return 0
	}
	return int(C.get_packet_flags(pkt.CPtr()))
}

// PacketConvergenceDuration — C: pkt.convergence_duration (fa_video.c:264)
func PacketConvergenceDuration(pkt *libav.AVPacket) int64 {
	if pkt == nil {
		return 0
	}
	return int64(C.get_packet_convergence_duration(pkt.CPtr()))
}

// CodecName — C: codecname() (libav.c) → avcodec_get_name
func CodecName(codecID int) string {
	return C.GoString(C.ml_codec_name(C.int(codecID)))
}

// HasDecoder — C: avcodec_find_decoder(id) != NULL
func HasDecoder(codecID int) bool {
	return C.ml_has_decoder(C.int(codecID)) == 1
}

// MetadataFromCodec — C: metadata_from_libav (libav.c:489-535) —
// "H264 (Level 4.0), 1920x1080"-style codec description for metadata.
// Takes a materialized stream codec context (CodecCtxFromStream).
func MetadataFromCodec(ctx *libav.AVCodecContext) string {
	if ctx == nil {
		return ""
	}
	buf := make([]byte, 256)
	C.ml_metadata_from_codec(ctx.CPtr(),
		(*C.char)(unsafe.Pointer(&buf[0])), C.int(len(buf)))
	return C.GoString((*C.char)(unsafe.Pointer(&buf[0])))
}
