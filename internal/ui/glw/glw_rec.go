//go:build glwrec && cgo

package glw

// C: src/ui/glw/glw_rec.c — canonical 1:1 port.
//
// FFmpeg adaptation notes (unavoidable — the C target APIs were removed
// upstream): avcodec_encode_video2/audio2 → avcodec_send_frame +
// avcodec_receive_packet; st->codec → own AVCodecContext exported via
// codecpar; oc->filename → oc->url; avresample_* → swr_*; AV_CH_LAYOUT_*
// → AVChannelLayout; av_free_packet → av_packet_unref.

/*
// Uses the bundled static FFmpeg (same flags as pkg/media/libav/cgo.go:
// the binary must link one FFmpeg only).
#cgo linux,!android,!arm CFLAGS: -I${SRCDIR}/../../../ffmpeg/include
#cgo linux,arm,!android CFLAGS: -I${SRCDIR}/../../../ffmpeg/rpi/include
#cgo android,arm64 CFLAGS: -I${SRCDIR}/../../../ffmpeg/android/arm64/include
#cgo android,arm CFLAGS: -I${SRCDIR}/../../../ffmpeg/android/arm/include
#cgo linux,!android,!arm LDFLAGS: -L${SRCDIR}/../../../ffmpeg/lib -lavformat -lavcodec -lavutil -lswresample -lm -lpthread -lz -lbz2 -llzma -lva -lva-drm -ldrm
#cgo linux,arm,!android LDFLAGS: -L${SRCDIR}/../../../ffmpeg/rpi/lib -lavformat -lavcodec -lavutil -lswresample -lm -lpthread -lz -lbz2
#cgo android,arm64 LDFLAGS: -L${SRCDIR}/../../../ffmpeg/android/arm64/lib -lavformat -lavcodec -lavutil -lswresample -lm -lz -ldl -llog -landroid -lmediandk
#cgo android,arm LDFLAGS: -L${SRCDIR}/../../../ffmpeg/android/arm/lib -lavformat -lavcodec -lavutil -lswresample -lm -lz -ldl -llog -landroid -lmediandk
#cgo darwin,arm64 CFLAGS: -I${SRCDIR}/../../../ffmpeg/darwin/arm64/include
#cgo darwin,amd64 CFLAGS: -I${SRCDIR}/../../../ffmpeg/darwin/amd64/include
#cgo darwin,arm64 LDFLAGS: -L${SRCDIR}/../../../ffmpeg/darwin/arm64/lib
#cgo darwin,amd64 LDFLAGS: -L${SRCDIR}/../../../ffmpeg/darwin/amd64/lib
#cgo darwin LDFLAGS: -lavcodec -lavformat -lavutil -lswresample -lswscale -lm -lz -lbz2 -liconv
#cgo windows,amd64 CFLAGS: -I${SRCDIR}/../../../ffmpeg/windows/amd64/include
#cgo windows,386 CFLAGS: -I${SRCDIR}/../../../ffmpeg/windows/386/include
#cgo windows,amd64 LDFLAGS: -L${SRCDIR}/../../../ffmpeg/windows/amd64/lib -L${SRCDIR}/../../../third_party/zlib/windows/amd64/lib -L${SRCDIR}/../../../third_party/libxml2/windows/amd64/lib -lavformat -lavcodec -lavutil -lswresample -lm -lz -lxml2 -lws2_32 -lbcrypt -lsecur32 -lole32 -luuid -ld3d11 -ldxgi -ld3d9
#cgo windows,386 LDFLAGS: -L${SRCDIR}/../../../ffmpeg/windows/386/lib -L${SRCDIR}/../../../third_party/zlib/windows/386/lib -L${SRCDIR}/../../../third_party/libxml2/windows/386/lib -lavformat -lavcodec -lavutil -lswresample -lm -lz -lxml2 -lws2_32 -lbcrypt -lsecur32 -lole32 -luuid -ld3d11 -ldxgi -ld3d9
#include <stdlib.h>
#include <string.h>
#include <libavformat/avformat.h>
#include <libavcodec/avcodec.h>
#include <libavutil/opt.h>
#include <libavutil/mathematics.h>
#include <libavutil/channel_layout.h>
#include <libswresample/swresample.h>

// C: av_guess_format(NULL, gr->filename, NULL)
static void *rec_guess_format(const char *filename) {
	return (void *)av_guess_format(NULL, filename, NULL);
}

// C: avformat_alloc_context(); oc->oformat = fmt;
//    snprintf(oc->filename, ...) — oc->url replaced oc->filename (FFmpeg 5+)
static void *rec_alloc_oc(const void *fmt, const char *filename) {
	AVFormatContext *oc = avformat_alloc_context();
	oc->oformat = (AVOutputFormat *)fmt;
	oc->url = av_strdup(filename);
	return oc;
}

// C: avformat_new_stream(gr->oc, 0)
static void *rec_new_stream(void *oc) {
	return avformat_new_stream((AVFormatContext *)oc, NULL);
}

static int rec_st_index(void *st) { return ((AVStream *)st)->index; }

static void rec_st_set_avg_rate(void *st, int num, int den) {
	((AVStream *)st)->avg_frame_rate.num = num;
	((AVStream *)st)->avg_frame_rate.den = den;
}

static int rec_st_tb_num(void *st) { return ((AVStream *)st)->time_base.num; }
static int rec_st_tb_den(void *st) { return ((AVStream *)st)->time_base.den; }
static int rec_ctx_tb_num(void *ctx) { return ((AVCodecContext *)ctx)->time_base.num; }
static int rec_ctx_tb_den(void *ctx) { return ((AVCodecContext *)ctx)->time_base.den; }

// C: gr->v_ctx = gr->v_st->codec — modern FFmpeg keeps the codec context
//    separate from the stream; codecpar carries it to the muxer.
static void *rec_codec_alloc(int codec_id) {
	const AVCodec *c = avcodec_find_encoder(codec_id);
	if(c == NULL)
		return NULL;
	return avcodec_alloc_context3(c);
}

static void rec_vctx_setup(void *ctx, int w, int h, int fps) {
	AVCodecContext *c = (AVCodecContext *)ctx;
	c->codec_type = AVMEDIA_TYPE_VIDEO;
	c->codec_id   = AV_CODEC_ID_FFVHUFF;
	c->width  = w;
	c->height = h;
	c->time_base.den = fps;
	c->time_base.num = 1;
	c->pix_fmt = AV_PIX_FMT_RGB32;
	// C: c->coder_type = 0 — field removed from AVCodecContext (FFmpeg 5+)
}

static void rec_actx_setup(void *ctx) {
	AVCodecContext *c = (AVCodecContext *)ctx;
	c->codec_type = AVMEDIA_TYPE_AUDIO;
	c->codec_id   = AV_CODEC_ID_PCM_S16LE;
	c->sample_rate = 48000;
	c->sample_fmt  = AV_SAMPLE_FMT_S16;
	av_channel_layout_default(&c->ch_layout, 2); // AV_CH_LAYOUT_STEREO
	c->time_base.den = 48000;
	c->time_base.num = 1;
}

// C: avcodec_open2(ctx, c, NULL) — c bound via avcodec_alloc_context3
static int rec_codec_open(void *ctx) {
	return avcodec_open2((AVCodecContext *)ctx, NULL, NULL);
}

static void rec_ctx_threads(void *ctx, int n) {
	((AVCodecContext *)ctx)->thread_count = n;
}

// C: implicit via st->codec; modern exports ctx to st->codecpar
static void rec_params_from_ctx(void *st, void *ctx) {
	avcodec_parameters_from_context(((AVStream *)st)->codecpar,
		(AVCodecContext *)ctx);
}

// C: avcodec_close(st->codec); free(st->codec)
static void rec_codec_free(void *ctx) {
	AVCodecContext *c = (AVCodecContext *)ctx;
	avcodec_free_context(&c);
}

static void rec_dump_format(void *oc, const char *f) {
	av_dump_format((AVFormatContext *)oc, 0, f, 1);
}

static int rec_avio_open(void *oc, const char *f) {
	return avio_open(&((AVFormatContext *)oc)->pb, f, AVIO_FLAG_WRITE);
}

static int rec_write_header(void *oc) {
	return avformat_write_header((AVFormatContext *)oc, NULL);
}

static int rec_interleaved_write(void *oc, void *pkt) {
	return av_interleaved_write_frame((AVFormatContext *)oc,
		(AVPacket *)pkt);
}

static int rec_write_trailer(void *oc) {
	return av_write_trailer((AVFormatContext *)oc);
}

static void rec_avio_close(void *oc) {
	avio_close(((AVFormatContext *)oc)->pb);
}

// C: free(st); free(oc) — avformat_free_context frees streams + ctx
static void rec_oc_free(void *oc) {
	avformat_free_context((AVFormatContext *)oc);
}

// C: av_rescale_q
static int64_t rec_rescale_q(int64_t a, int bn, int bd, int cn, int cd) {
	return av_rescale_q(a, (AVRational){bn, bd}, (AVRational){cn, cd});
}

// C: stack AVFrame filled for the encoder:
//   frame.data[0] = pm->pm_data + pm->pm_linesize * (h - 1)
//   frame.linesize[0] = -pm->pm_linesize   (vertical flip)
static void rec_vframe_fill(AVFrame *f, const uint8_t *data, int linesize,
	int w, int h, int64_t pts) {
	memset(f, 0, sizeof(*f));
	f->data[0] = (uint8_t *)data;
	f->linesize[0] = linesize;
	f->width = w;
	f->height = h;
	f->format = AV_PIX_FMT_RGB32;
	f->pts = pts;
}

// C: frame.data[0] = data; frame.nb_samples = SAMPLES_PER_FRAME
//    extended_data = data is required by the modern encode path
//    (avcodec_encode_audio2 used to fill it internally).
static void rec_aframe_fill(AVFrame *f, const int16_t *data, int nb) {
	memset(f, 0, sizeof(*f));
	f->data[0] = (uint8_t *)data;
	f->extended_data = f->data;
	f->nb_samples = nb;
	f->format = AV_SAMPLE_FMT_S16;
	f->sample_rate = 48000;
	av_channel_layout_default(&f->ch_layout, 2);
}

// C: avcodec_encode_video2/audio2(ctx, &pkt, &frame, &got_packet)
static int rec_send_frame(void *ctx, AVFrame *f) {
	return avcodec_send_frame((AVCodecContext *)ctx, f);
}
static int rec_recv_packet(void *ctx, AVPacket *p) {
	return avcodec_receive_packet((AVCodecContext *)ctx, p);
}

// C: av_init_packet + pkt.data = NULL + pkt.size = 0
static void rec_pkt_init(AVPacket *p) { memset(p, 0, sizeof(*p)); }

// C: av_free_packet(&pkt)
static void rec_pkt_unref(AVPacket *p) { av_packet_unref(p); }

static int64_t rec_pkt_pts(AVPacket *p) { return p->pts; }
static int rec_pkt_key(AVPacket *p) {
	return (p->flags & AV_PKT_FLAG_KEY) != 0;
}
static void rec_pkt_set_ts(AVPacket *p, int64_t pts, int64_t dts,
	int64_t dur, int idx, int key) {
	p->pts = pts;
	p->dts = dts;
	p->duration = dur;
	p->stream_index = idx;
	if(key)
		p->flags |= AV_PKT_FLAG_KEY;
}

// C: avresample_alloc_context → swr_alloc
static void *rec_swr_alloc(void) { return swr_alloc(); }

// C: avresample_free(&as->as_avr)
static void rec_swr_free(void **s) { swr_free((SwrContext **)s); }

// C: avresample_close(as->as_avr)
static void rec_swr_close(void *s) { swr_close((SwrContext *)s); }

// C: avresample_open(as->as_avr)
static int rec_swr_init(void *s) { return swr_init((SwrContext *)s); }

// C: av_opt_set_int(as->as_avr, "in_sample_fmt", fmt, 0)
static int rec_swr_set_in_fmt(void *s, int64_t v) {
	return av_opt_set_int(s, "in_sample_fmt", v, 0);
}

// C: av_opt_set_int(as->as_avr, "in_sample_rate", rate, 0)
static int rec_swr_set_in_rate(void *s, int64_t v) {
	return av_opt_set_int(s, "in_sample_rate", v, 0);
}

// C: av_opt_set_int(as->as_avr, "out_sample_fmt", AV_SAMPLE_FMT_S16, 0)
static int rec_swr_set_out_fmt(void *s, int64_t v) {
	return av_opt_set_int(s, "out_sample_fmt", v, 0);
}

// C: av_opt_set_int(as->as_avr, "out_sample_rate", 48000, 0)
static int rec_swr_set_out_rate(void *s, int64_t v) {
	return av_opt_set_int(s, "out_sample_rate", v, 0);
}

// C: av_opt_set_int(as->as_avr, "in_channel_layout"/"out_channel_layout",
//    mask, 0) — swr expects AV_OPT_TYPE_CHLAYOUT; build from the mask.
static int rec_swr_set_layout(void *s, int in, uint64_t mask) {
	AVChannelLayout l;
	memset(&l, 0, sizeof(l));
	if(mask) {
		l.order = AV_CHANNEL_ORDER_NATIVE;
		l.u.mask = mask;
		l.nb_channels = av_popcount64(mask);
	} else {
		av_channel_layout_default(&l, 2);
	}
	int r = av_opt_set_chlayout(s, in ? "in_chlayout" : "out_chlayout",
		&l, 0);
	av_channel_layout_uninit(&l);
	return r;
}

// C: avresample_convert(avr, NULL, 0, 0, frame->data, frame->linesize[0],
//    frame->nb_samples) — input is the packed data[0] pointer
static int rec_swr_convert(void *s, const void *in, int in_count) {
	const uint8_t *ip[8] = {0};
	ip[0] = (const uint8_t *)in;
	return swr_convert((SwrContext *)s, NULL, 0,
		in ? ip : NULL, in_count);
}

// C: avresample_available(as->as_avr)
static int rec_swr_avail(void *s) {
	return swr_get_out_samples((SwrContext *)s, 0);
}

// C: avresample_read(avr, data, avail) — NULL out discards samples
static int rec_swr_read(void *s, void *out, int count) {
	uint8_t *op[8] = {0};
	op[0] = (uint8_t *)out;
	return swr_convert((SwrContext *)s, out ? op : NULL, count, NULL, 0);
}

// C: av_get_channel_layout_string(buf1, 128, -1, layout)
static void rec_ch_layout_str(char *buf, int size, uint64_t mask) {
	AVChannelLayout l;
	memset(&l, 0, sizeof(l));
	if(mask) {
		l.order = AV_CHANNEL_ORDER_NATIVE;
		l.u.mask = mask;
		l.nb_channels = av_popcount64(mask);
	} else {
		av_channel_layout_default(&l, 2);
	}
	av_channel_layout_describe(&l, buf, (size_t)size);
	av_channel_layout_uninit(&l);
}

// C: av_get_sample_fmt_name
static const char *rec_sample_fmt_name(int fmt) {
	return av_get_sample_fmt_name(fmt);
}
*/
import "C"

import (
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	audiocore "github.com/czz/movian-go/internal/audio/core"
	eventpkg "github.com/czz/movian-go/internal/event"
	imagepkg "github.com/czz/movian-go/internal/image"
	tracepkg "github.com/czz/movian-go/internal/trace"
)

// C: LIST_HEAD(audio_source_list, audio_source)
type audioSourceList struct{ lhFirst *audioSource }

// C: TAILQ_HEAD(audio_buf_queue, audio_buf)
type audioBufQueue struct {
	tqhFirst *audioBuf
	tqhLast  **audioBuf
}

// C: TAILQ_HEAD(video_frame_queue, video_frame)
type videoFrameQueue struct {
	tqhFirst *videoFrame
	tqhLast  **videoFrame
}

// glwRecState — C: static glw_rec_mutex + static glw_rec_list glw_recs

// C: AV_NOPTS_VALUE
const avNoPtsValue = int64(-9223372036854775808)

// C: typedef struct audio_buf (glw_rec.c:46-52)
type audioBuf struct {
	abLinkNext *audioBuf  // C: TAILQ_ENTRY
	abLinkPrev **audioBuf // C: tqe_prev
	abBuf      *C.int16_t // C: int16_t *ab_buf
	abSamples  int        // C: ab_samples
	abUsed     int        // C: ab_used
	abTs       int64      // C: ab_ts
}

// C: typedef struct audio_source (glw_rec.c:55-73)
type audioSource struct {
	asLinkNext        *audioSource  // C: LIST_ENTRY
	asLinkPrev        **audioSource // C: le_prev
	asID              int           // C: as_id
	asFormat          int           // C: as_format (AVSampleFormat)
	asChannelLayout   int64         // C: as_channel_layout
	asSampleRate      int           // C: as_sample_rate
	asAvr             uintptr       // C: AVAudioResampleContext *as_avr
	asQueue           audioBufQueue // C: as_queue
	asStartDrop       int           // C: as_start_drop
	asSamplesQueued   int           // C: as_samples_queued
	asSamplesConsumed int           // C: as_samples_consumed
	asLastTs          int64         // C: as_last_ts
	asLastTsSample    int           // C: as_last_ts_sample
}

// C: typedef struct video_frame (glw_rec.c:77-80)
type videoFrame struct {
	vfLinkNext *videoFrame      // C: TAILQ_ENTRY
	vfLinkPrev **videoFrame     // C: tqe_prev
	vfPm       *imagepkg.Pixmap // C: pixmap_t *vf_pm
}

// C: struct glw_rec (glw_rec.c:84-112)
type GlwRec struct {
	globalLinkNext *GlwRec  // C: LIST_ENTRY global_link
	globalLinkPrev **GlwRec // C: le_prev
	filename       string   // C: char *filename (strdup'd)
	fmt            uintptr  // C: AVOutputFormat *fmt
	oc             uintptr  // C: AVFormatContext *oc
	vCtx           uintptr  // C: AVCodecContext *v_ctx
	vSt            uintptr  // C: AVStream *v_st
	aCtx           uintptr  // C: AVCodecContext *a_ctx
	aSt            uintptr  // C: AVStream *a_st
	width          int
	height         int
	fps            int
	framenum       int
	videoPts       int64           // C: video_pts
	audioPts       int64           // C: audio_pts
	samplesWritten int             // C: samples_written
	asources       audioSourceList // C: asources
	grCond         *sync.Cond      // C: gr_cond
	grVframes      videoFrameQueue // C: gr_vframes
	grVqlen        int             // C: gr_vqlen
	grStop         int             // C: gr_stop
}

// C: #define SAMPLES_PER_FRAME 1024
const recSamplesPerFrame = 1024

// C: static void emit_audio (glw_rec.c:119-219)
func emitAudio(gr *GlwRec, pts int64) {
	// C: int16_t data[SAMPLES_PER_FRAME * 2] — stack buffer; allocated on
	//    the C heap because a Go array pointer inside an AVFrame handed to
	//    avcodec_send_frame may be retained by the encoder past the call
	//    (Go stacks move on growth; C memory does not).
	data := (*C.int16_t)(C.malloc(recSamplesPerFrame * 2 * 2))
	defer C.free(unsafe.Pointer(data))
	dataSlice := unsafe.Slice((*int16)(data), recSamplesPerFrame*2)

	// C: AVFrame frame — stack; C heap here (see above)
	f := (*C.AVFrame)(C.malloc(C.size_t(unsafe.Sizeof(C.AVFrame{}))))
	defer C.free(unsafe.Pointer(f))
	var pkt C.AVPacket

	for {
		for i := range dataSlice {
			dataSlice[i] = 0
		}

		glwDeps.rec.mu.Lock()
		var as *audioSource

		for as = gr.asources.lhFirst; as != nil; as = as.asLinkNext {
			if as.asSamplesQueued < 1024 {
				glwDeps.rec.mu.Unlock()
				return
			}
		}

		for as = gr.asources.lhFirst; as != nil; as = as.asLinkNext {
			offset := 0

			for as.asSamplesQueued > 0 && offset < recSamplesPerFrame {
				ab := as.asQueue.tqhFirst

				if ab.abTs != avNoPtsValue {
					as.asLastTs = ab.abTs
					as.asLastTsSample = as.asSamplesConsumed
				}

				consume := min(int(ab.abSamples)-ab.abUsed,
					recSamplesPerFrame-offset)
				if consume <= 0 {
					panic("assertion failed: consume > 0") // C: assert
				}
				buf := unsafe.Slice(ab.abBuf, ab.abSamples*2)
				for i := range consume {
					dataSlice[(offset+i)*2+0] += int16(buf[(i+ab.abUsed)*2+0])
					dataSlice[(offset+i)*2+1] += int16(buf[(i+ab.abUsed)*2+1])
				}
				offset += consume
				ab.abUsed += consume
				as.asSamplesConsumed += consume
				// C: assert(ab->ab_used <= ab->ab_samples)
				if ab.abUsed > ab.abSamples {
					panic("assertion failed: ab_used <= ab_samples")
				}
				as.asSamplesQueued -= consume
				if ab.abUsed == ab.abSamples {
					C.free(unsafe.Pointer(ab.abBuf))
					// C: TAILQ_REMOVE(&as->as_queue, ab, ab_link)
					*ab.abLinkPrev = ab.abLinkNext
					if ab.abLinkNext != nil {
						ab.abLinkNext.abLinkPrev = ab.abLinkPrev
					} else {
						as.asQueue.tqhLast = ab.abLinkPrev
					}
					// C: free(ab) — GC
				}
			}
		}

		glwDeps.rec.mu.Unlock()

		C.rec_pkt_init(&pkt)

		ts := int64(C.rec_rescale_q(C.int64_t(gr.samplesWritten),
			1, 48000,
			C.rec_st_tb_num(unsafe.Pointer(gr.aSt)), C.rec_st_tb_den(unsafe.Pointer(gr.aSt))))

		if ts >= 0 {
			C.rec_aframe_fill(f, data, recSamplesPerFrame)

			r := C.rec_send_frame(unsafe.Pointer(gr.aCtx), f)
			got := C.int(-1)
			if r >= 0 {
				got = C.rec_recv_packet(unsafe.Pointer(gr.aCtx), &pkt)
			}
			if r < 0 || got < 0 {
				C.abort() // C: abort()
			}

			dur := int64(C.rec_rescale_q(1,
				recSamplesPerFrame, 48000,
				C.rec_st_tb_num(unsafe.Pointer(gr.vSt)), C.rec_st_tb_den(unsafe.Pointer(gr.vSt))))
			C.rec_pkt_set_ts(&pkt, C.int64_t(ts), C.int64_t(ts),
				C.int64_t(dur), C.rec_st_index(unsafe.Pointer(gr.aSt)), 0)
			C.rec_interleaved_write(unsafe.Pointer(gr.oc), unsafe.Pointer(&pkt))
			C.rec_pkt_unref(&pkt)
		}

		gr.samplesWritten += recSamplesPerFrame

		t := int64(C.rec_rescale_q(C.int64_t(gr.samplesWritten),
			1, 48000, 1, 1000000))
		if t >= pts {
			break
		}
	}
}

// C: static void encode_vframe (glw_rec.c:225-264)
func encodeVframe(gr *GlwRec, pm *imagepkg.Pixmap) {
	// C: AVFrame frame — stack; on the C heap here because it carries the
	//    Go-backed pixmap data pointer into avcodec_send_frame (cgo forbids
	//    Go memory containing unpinned Go pointers).
	f := (*C.AVFrame)(C.malloc(C.size_t(unsafe.Sizeof(C.AVFrame{}))))
	defer C.free(unsafe.Pointer(f))

	off := pm.Stride * (gr.height - 1)
	pts := int64(1000000) * int64(gr.framenum) / int64(gr.fps)
	C.rec_vframe_fill(f,
		(*C.uint8_t)(unsafe.Pointer(&pm.Data[off])),
		C.int(-pm.Stride), C.int(gr.width), C.int(gr.height),
		C.int64_t(pts))
	gr.framenum++

	var pkt C.AVPacket
	C.rec_pkt_init(&pkt)

	if C.rec_send_frame(unsafe.Pointer(gr.vCtx), f) < 0 {
		return // C: r < 0
	}
	if C.rec_recv_packet(unsafe.Pointer(gr.vCtx), &pkt) < 0 {
		return // C: !got_packet
	}
	runtime.KeepAlive(pm) // pm.Data must outlive the encoder's use of f.data[0]

	// C: int64_t pts = gr->v_ctx->coded_frame->pts — pkt->pts is the modern
	//    equivalent; the encoder emits it in AV_TIME_BASE (µs), the same
	//    units C assumed for coded_frame->pts.
	p := int64(C.rec_pkt_pts(&pkt))
	if p == avNoPtsValue {
		p = pts // C: pts == AV_NOPTS_VALUE → frame.pts
	}

	key := 0
	if C.rec_pkt_key(&pkt) != 0 {
		key = 1
	}

	ts := int64(C.rec_rescale_q(C.int64_t(p), 1, 1000000,
		C.rec_st_tb_num(unsafe.Pointer(gr.vSt)), C.rec_st_tb_den(unsafe.Pointer(gr.vSt))))
	dur := int64(C.rec_rescale_q(1, 1, C.int(gr.fps),
		C.rec_st_tb_num(unsafe.Pointer(gr.vSt)), C.rec_st_tb_den(unsafe.Pointer(gr.vSt))))
	C.rec_pkt_set_ts(&pkt, C.int64_t(ts), C.int64_t(ts), C.int64_t(dur),
		C.rec_st_index(unsafe.Pointer(gr.vSt)), C.int(key))
	C.rec_interleaved_write(unsafe.Pointer(gr.oc), unsafe.Pointer(&pkt))
	C.rec_pkt_unref(&pkt)

	emitAudio(gr, p)
}

// C: static void *rec_thread (glw_rec.c:270-384)
//
//	hts_thread_create_detached("rec", rec_thread, gr, THREAD_PRIO_BGTASK)
func recThread(gr *GlwRec) {
	cfilename := C.CString(gr.filename)
	defer C.free(unsafe.Pointer(cfilename))

	gr.fmt = uintptr(unsafe.Pointer(C.rec_guess_format(cfilename)))
	if gr.fmt == 0 {
		glwDeps.ts.Trace(tracepkg.TRACE_ERROR, "REC",
			"Unable to record to %s -- Unknown file format",
			gr.filename)
		return
	}

	gr.oc = uintptr(unsafe.Pointer(C.rec_alloc_oc(unsafe.Pointer(gr.fmt), cfilename)))

	gr.vSt = uintptr(unsafe.Pointer(C.rec_new_stream(unsafe.Pointer(gr.oc))))
	C.rec_st_set_avg_rate(unsafe.Pointer(gr.vSt), C.int(gr.fps), 1)

	gr.vCtx = uintptr(unsafe.Pointer(C.rec_codec_alloc(C.AV_CODEC_ID_FFVHUFF)))
	if gr.vCtx != 0 {
		C.rec_vctx_setup(unsafe.Pointer(gr.vCtx), C.int(gr.width), C.int(gr.height),
			C.int(gr.fps))
	}
	if gr.vCtx == 0 || C.rec_codec_open(unsafe.Pointer(gr.vCtx)) != 0 {
		glwDeps.ts.Trace(tracepkg.TRACE_ERROR, "REC",
			"Unable to record to %s -- Unable to open video codec",
			gr.filename)
		return
	}
	C.rec_ctx_threads(unsafe.Pointer(gr.vCtx), C.int(glwDeps.gconf.Concurrency))
	C.rec_params_from_ctx(unsafe.Pointer(gr.vSt), unsafe.Pointer(gr.vCtx))

	gr.aSt = uintptr(unsafe.Pointer(C.rec_new_stream(unsafe.Pointer(gr.oc))))

	gr.aCtx = uintptr(unsafe.Pointer(C.rec_codec_alloc(C.AV_CODEC_ID_PCM_S16LE)))
	if gr.aCtx != 0 {
		C.rec_actx_setup(unsafe.Pointer(gr.aCtx))
	}
	if gr.aCtx == 0 || C.rec_codec_open(unsafe.Pointer(gr.aCtx)) != 0 {
		glwDeps.ts.Trace(tracepkg.TRACE_ERROR, "REC",
			"Unable to record to %s -- Unable to open audio codec",
			gr.filename)
		return
	}
	C.rec_ctx_threads(unsafe.Pointer(gr.aCtx), C.int(glwDeps.gconf.Concurrency))
	C.rec_params_from_ctx(unsafe.Pointer(gr.aSt), unsafe.Pointer(gr.aCtx))

	// Output output file
	C.rec_dump_format(unsafe.Pointer(gr.oc), cfilename)

	if C.rec_avio_open(unsafe.Pointer(gr.oc), cfilename) < 0 {
		glwDeps.ts.Trace(tracepkg.TRACE_ERROR, "REC",
			"Unable to record to %s -- Unable to open file for writing",
			gr.filename)
		return
	}

	/* write the stream header, if any */
	C.rec_write_header(unsafe.Pointer(gr.oc))

	glwDeps.rec.mu.Lock()

	for gr.grStop == 0 {
		vf := gr.grVframes.tqhFirst
		if vf == nil {
			gr.grCond.Wait()
			continue
		}
		// C: TAILQ_REMOVE(&gr->gr_vframes, vf, vf_link)
		*vf.vfLinkPrev = vf.vfLinkNext
		if vf.vfLinkNext != nil {
			vf.vfLinkNext.vfLinkPrev = vf.vfLinkPrev
		} else {
			gr.grVframes.tqhLast = vf.vfLinkPrev
		}
		gr.grVqlen--
		glwDeps.rec.mu.Unlock()
		encodeVframe(gr, vf.vfPm)
		imagepkg.PixmapRelease(vf.vfPm)
		// C: free(vf) — GC
		glwDeps.rec.mu.Lock()
	}

	glwDeps.rec.mu.Unlock()

	C.rec_write_trailer(unsafe.Pointer(gr.oc))

	// C: for each stream: avcodec_close(st->codec); free(st->codec); free(st)
	C.rec_codec_free(unsafe.Pointer(gr.vCtx))
	C.rec_codec_free(unsafe.Pointer(gr.aCtx))

	C.rec_avio_close(unsafe.Pointer(gr.oc))
	C.rec_oc_free(unsafe.Pointer(gr.oc))
	// C: free(gr) — GC
}

// C: glw_rec_t *glw_rec_init (glw_rec.c:390-409)
func newGlwRec(filename string, width, height, fps int) *GlwRec {
	gr := &GlwRec{}

	// C: TAILQ_INIT(&gr->gr_vframes)
	gr.grVframes.tqhLast = &gr.grVframes.tqhFirst
	gr.width = width
	gr.height = height
	gr.fps = fps
	gr.filename = filename // C: strdup
	gr.grCond = sync.NewCond(&glwDeps.rec.mu)

	glwDeps.rec.mu.Lock()
	// C: LIST_INSERT_HEAD(&glw_recs, gr, global_link)
	gr.globalLinkNext = glwDeps.rec.recs.lhFirst
	if gr.globalLinkNext != nil {
		gr.globalLinkNext.globalLinkPrev = &gr.globalLinkNext
	}
	glwDeps.rec.recs.lhFirst = gr
	gr.globalLinkPrev = &glwDeps.rec.recs.lhFirst
	glwDeps.rec.mu.Unlock()

	go recThread(gr) // C: hts_thread_create_detached
	return gr
}

// C: void glw_rec_stop (glw_rec.c:415-423)
func glwRecStop(gr *GlwRec) {
	glwDeps.rec.mu.Lock()
	defer glwDeps.rec.mu.Unlock()
	gr.grStop = 1
	gr.grCond.Signal()
	// C: LIST_REMOVE(gr, global_link)
	*gr.globalLinkPrev = gr.globalLinkNext
	if gr.globalLinkNext != nil {
		gr.globalLinkNext.globalLinkPrev = gr.globalLinkPrev
	}
}

// C: void glw_rec_audio_send (glw_rec.c:429-546)
//
//	Signature adapted: the Go audio path carries decoded frames as packed
//	PCM (frame->data[0]) + nb_samples; frame == NULL → framePCM == nil.
func glwRecAudioSend(ad *audiocore.AudioDecoder, framePCM []byte, frameNb int, pts int64) {
	if glwDeps.rec.recs.lhFirst == nil {
		return
	}

	glwDeps.rec.mu.Lock()
	for gr := glwDeps.rec.recs.lhFirst; gr != nil; gr = gr.globalLinkNext {
		var as *audioSource

		for as = gr.asources.lhFirst; as != nil; as = as.asLinkNext {
			if as.asID == ad.ID {
				break
			}
		}

		if framePCM == nil {
			if as == nil {
				continue
			}
			// C: LIST_REMOVE(as, as_link)
			*as.asLinkPrev = as.asLinkNext
			if as.asLinkNext != nil {
				as.asLinkNext.asLinkPrev = as.asLinkPrev
			}
			fmt.Printf("Stream %d stopped\n", as.asID)
			continue
		}

		if as == nil {
			fmt.Printf("Delay = %d\n", ad.Delay)

			as = &audioSource{}
			as.asLastTs = avNoPtsValue
			// C: TAILQ_INIT(&as->as_queue)
			as.asQueue.tqhLast = &as.asQueue.tqhFirst
			// C: LIST_INSERT_HEAD(&gr->asources, as, as_link)
			as.asLinkNext = gr.asources.lhFirst
			if as.asLinkNext != nil {
				as.asLinkNext.asLinkPrev = &as.asLinkNext
			}
			gr.asources.lhFirst = as
			as.asLinkPrev = &gr.asources.lhFirst
			as.asID = ad.ID
			as.asStartDrop = 24000
			as.asAvr = uintptr(unsafe.Pointer(C.rec_swr_alloc()))
		}

		inFormat := audiocore.SampleFormatToAV(ad.InSampleFormat)
		if as.asFormat != inFormat ||
			as.asChannelLayout != ad.InChannelLayout ||
			as.asSampleRate != ad.InSampleRate {

			C.rec_swr_close(unsafe.Pointer(as.asAvr))

			as.asFormat = inFormat
			as.asChannelLayout = ad.InChannelLayout
			as.asSampleRate = ad.InSampleRate

			C.rec_swr_set_in_fmt(unsafe.Pointer(as.asAvr), C.int64_t(as.asFormat))
			C.rec_swr_set_in_rate(unsafe.Pointer(as.asAvr), C.int64_t(as.asSampleRate))
			C.rec_swr_set_layout(unsafe.Pointer(as.asAvr), 1,
				C.uint64_t(as.asChannelLayout))

			C.rec_swr_set_out_fmt(unsafe.Pointer(as.asAvr), C.AV_SAMPLE_FMT_S16)
			C.rec_swr_set_out_rate(unsafe.Pointer(as.asAvr), 48000)
			C.rec_swr_set_layout(unsafe.Pointer(as.asAvr), 0,
				3) // AV_CH_LAYOUT_STEREO

			var buf1 [128]C.char
			C.rec_ch_layout_str(&buf1[0], 128,
				C.uint64_t(as.asChannelLayout))

			glwDeps.ts.Trace(tracepkg.TRACE_DEBUG, "REC",
				"Converting from [%s %dHz %s]",
				C.GoString(&buf1[0]), as.asSampleRate,
				C.GoString(C.rec_sample_fmt_name(C.int(as.asFormat))))

			if C.rec_swr_init(unsafe.Pointer(as.asAvr)) != 0 {
				glwDeps.ts.Trace(tracepkg.TRACE_ERROR, "REC",
					"Unable to open resampler")
				C.rec_swr_free((*unsafe.Pointer)(unsafe.Pointer(&as.asAvr)))
			}
		}

		if as.asAvr == 0 {
			continue
		}

		// C: avresample_convert(avr, NULL,0,0, frame->data,
		//    frame->linesize[0], frame->nb_samples)
		var inPtr unsafe.Pointer
		if len(framePCM) > 0 {
			inPtr = unsafe.Pointer(&framePCM[0])
		}
		C.rec_swr_convert(unsafe.Pointer(as.asAvr), inPtr, C.int(frameNb))

		avail := int(C.rec_swr_avail(unsafe.Pointer(as.asAvr)))
		if avail == 0 {
			continue
		}

		if as.asStartDrop > 0 {
			C.rec_swr_read(unsafe.Pointer(as.asAvr), nil, C.int(avail))
			fmt.Printf("Dropped %d\n", avail)
			as.asStartDrop -= avail
		} else {
			bytes := avail * 2 * 2 // 16 bit stereo
			buf := C.malloc(C.size_t(bytes))
			C.rec_swr_read(unsafe.Pointer(as.asAvr), buf, C.int(avail))
			ab := &audioBuf{}
			ab.abBuf = (*C.int16_t)(buf)
			ab.abSamples = avail
			s := unsafe.Slice(ab.abBuf, avail*2)
			for i := range avail * 2 {
				// C: ab->ab_buf[i] *= ad->ad_vol_scale (int16_t)
				s[i] = C.int16_t(float32(s[i]) * ad.VolScale)
			}
			// C: TAILQ_INSERT_TAIL(&as->as_queue, ab, ab_link)
			ab.abLinkNext = nil
			ab.abLinkPrev = as.asQueue.tqhLast
			*as.asQueue.tqhLast = ab
			as.asQueue.tqhLast = &ab.abLinkNext
			as.asSamplesQueued += avail
		}
	}
	glwDeps.rec.mu.Unlock()
}

// C: void glw_rec_deliver_vframe (glw_rec.c:549-562)
func glwRecDeliverVframe(gr *GlwRec, pm *imagepkg.Pixmap) {
	vf := &videoFrame{}
	vf.vfPm = imagepkg.PixmapDup(pm)

	glwDeps.rec.mu.Lock()
	defer glwDeps.rec.mu.Unlock()
	gr.grCond.Signal()
	// C: TAILQ_INSERT_TAIL(&gr->gr_vframes, vf, vf_link)
	vf.vfLinkNext = nil
	vf.vfLinkPrev = gr.grVframes.tqhLast
	*gr.grVframes.tqhLast = vf
	gr.grVframes.tqhLast = &vf.vfLinkNext
	gr.grVqlen++
	if gr.grVqlen > 10 {
		glwDeps.ts.Trace(tracepkg.TRACE_ERROR, "REC",
			"Warning video queue length is %d", gr.grVqlen)
	}
}

// C: static void glw_rec_toggle (glw.c:2395-2408, CONFIG_GLW_REC)
func glwRecToggle(gr *glwRoot) {
	if gr.grRec != nil {
		// Stop
		glwDeps.ts.Trace(tracepkg.TRACE_DEBUG, "GLW", "Recording stopped")
		glwRecStop(gr.grRec)
		gr.grRec = nil
	} else {
		gr.grRec = newGlwRec("capture.mkv", gr.grWidth, gr.grHeight, 60)
		if gr.grRec != nil {
			glwDeps.ts.Trace(tracepkg.TRACE_DEBUG, "GLW",
				"Recording started")
		}
	}
}

// glwRecPostScene — C: the CONFIG_GLW_REC block of glw_post_scene
// (glw.c:767-772).
func glwRecPostScene(gr *glwRoot) {
	if gr.grRec != nil {
		pm := gr.grBrReadPixels(gr)
		glwRecDeliverVframe(gr.grRec, pm)
		imagepkg.PixmapRelease(pm)
	}
}

// glwRecIsRecordAction — C: event_is_action(e, ACTION_RECORD_UI)
// inside the CONFIG_GLW_REC block of glw_dispatch_event (glw.c:2441-2445).
func glwRecIsRecordAction(e *eventpkg.Event) bool {
	return e.IsAction(eventpkg.ACTION_RECORD_UI)
}

// C: the glw_rec_audio_send call sites are compiled only under
// CONFIG_GLW_REC — mirrored by registering the hook only under the
// "glwrec" build tag.
func init() {
	audiocore.SetGlwRecAudioSend(glwRecAudioSend)
}
