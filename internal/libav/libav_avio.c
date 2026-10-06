#include <libavformat/avio.h>
#include <libavformat/avformat.h>
#include <libavcodec/packet.h>
#include <libavutil/mem.h>
#include <stdlib.h>
#include <string.h>

// Forward declarations for Go callbacks
extern int goReadCallback(void *opaque, uint8_t *buf, int size);
extern int64_t goSeekCallback(void *opaque, int64_t offset, int whence);

// Read callback for AVIOContext - calls Go function
static int read_callback(void *opaque, uint8_t *buf, int size) {
    int ret = goReadCallback(opaque, buf, size);
    // C: fa_read returns 0 at EOF, but modern FFmpeg's avio expects
    // read_packet to return AVERROR_EOF — a 0 return is treated as
    // "0 bytes read" and polled again, looping forever.
    if (ret == 0)
        return AVERROR_EOF;
    return ret;
}

// Seek callback for AVIOContext - calls Go function
static int64_t seek_callback(void *opaque, int64_t offset, int whence) {
    return goSeekCallback(opaque, offset, whence);
}

// C function to allocate AVIOContext with Go callbacks
void *c_alloc_avio_context(void *opaque, int seekable) {
    int buf_size = 32768;
    void *buf = av_malloc(buf_size);
    if (buf == NULL) {
        return NULL;
    }

    AVIOContext *avio = avio_alloc_context(buf, buf_size, 0, opaque,
                                            read_callback, NULL, seek_callback);
    if (avio == NULL) {
        av_free(buf);
        return NULL;
    }

    if (!seekable) {
        avio->seekable = 0;
    }

    return avio;
}

// C function to free AVIOContext and buffer
void c_free_avio_context(void *avio_ptr) {
    if (avio_ptr != NULL) {
        AVIOContext *avio = (AVIOContext *)avio_ptr;
        void *buf = avio->buffer;
        avio_context_free(&avio);
        if (buf != NULL) {
            av_free(buf);
        }
    }
}

// ---------------------------------------------------------------------------
// Nested I/O opens (HLS/DASH segments, sidecars): routed back through
// fileaccess — the fa AVIO path uses Go's TLS, so no FFmpeg TLS backend
// (openssl/gnutls) is required for https URLs.
// ---------------------------------------------------------------------------
extern int goFaIoOpen(uintptr_t key, const char *url, void **pb_out);
extern int goFaIoClose(uintptr_t key, void *pb);

static int fa_io_open_cb(AVFormatContext *s, AVIOContext **pb, const char *url,
                         int flags, AVDictionary **options) {
    static int logged_once;
    void *out = NULL;
    int ret;
    if (!logged_once) {
        logged_once = 1;
        av_log(s, AV_LOG_INFO,
               "custom io_open: nested opens via fileaccess (external TLS)\n");
    }
    ret = goFaIoOpen((uintptr_t)s->opaque, url, &out);
    if (ret < 0)
        return ret;
    *pb = (AVIOContext *)out;
    return 0;
}

static int fa_io_close_cb(AVFormatContext *s, AVIOContext *pb) {
    return goFaIoClose((uintptr_t)s->opaque, (void *)pb);
}

// C wrapper for avformat_open_input with AVIOContext parameter
AVFormatContext *avformat_open_input_wrapper(AVFormatContext *ps, const char *url,
                                               AVInputFormat *fmt, AVIOContext *pb,
                                               char *errbuf, int errlen) {
    if (errbuf != NULL && errlen > 0) {
        errbuf[0] = '\0';
    }

    // Allocate AVFormatContext if not provided
    if (ps == NULL) {
        ps = avformat_alloc_context();
        if (ps == NULL) {
            if (errbuf != NULL && errlen > 0) {
                av_strerror(AVERROR(ENOMEM), errbuf, errlen);
            }
            return NULL;
        }
    }

    // Set AVIOContext (pb) before calling avformat_open_input
    ps->pb = pb;

    int ret = avformat_open_input(&ps, url, fmt, NULL);
    if (ret < 0) {
        if (errbuf != NULL && errlen > 0) {
            av_strerror(ret, errbuf, errlen);
        }
        if (pb == NULL) {
            avformat_free_context(ps);
        }
        return NULL;
    }

    return ps;
}

// C wrapper to set fps_probe_size on AVFormatContext
void c_set_fps_probe_size(AVFormatContext *ps, int64_t value) {
    if (ps != NULL) {
        ps->fps_probe_size = value;
    }
}

// C wrapper to set max_analyze_duration on AVFormatContext
void c_set_max_analyze_duration(AVFormatContext *ps, int64_t value) {
    if (ps != NULL) {
        ps->max_analyze_duration = value;
    }
}

// C wrapper for av_packet_unref
void c_av_packet_unref(AVPacket *pkt) {
    if (pkt != NULL) {
        av_packet_unref(pkt);
    }
}

// Canonical fa_libav.c helpers (src/fileaccess/fa_libav.c)

// avio_seek(pb, 0, SEEK_SET) — C: fa_libav_open_format rewinds first
int64_t fa_avio_seek0(AVIOContext *pb) {
    return avio_seek(pb, 0, SEEK_SET);
}

// av_find_input_format — used for the mimetype2fmt table lookups
const AVInputFormat *fa_find_input_format(const char *name) {
    return av_find_input_format(name);
}

// av_probe_input_buffer(pb, fmt, url, NULL, 0, max_probe_size)
int fa_probe_input_buffer(AVIOContext *pb, const AVInputFormat **fmt,
                          const char *url, int max_probe_size) {
    return av_probe_input_buffer(pb, fmt, url, NULL, 0, max_probe_size);
}

// avformat_alloc_context + pb assignment + avformat_open_input
// Returns 0 on success (fctx_out set), AVERROR on failure.
// iokey != 0: install Go io_open/io_close2 callbacks before open —
// nested opens (HLS/DASH) happen inside read_header already.
int fa_avformat_open_pb(AVFormatContext **fctx_out, AVIOContext *pb,
                        const char *url, const AVInputFormat *fmt,
                        uintptr_t iokey) {
    AVFormatContext *fctx = avformat_alloc_context();
    if (fctx == NULL)
        return AVERROR(ENOMEM);
    fctx->pb = pb;
    if (iokey != 0) {
        fctx->opaque = (void *)iokey;
        fctx->io_open = fa_io_open_cb;
        fctx->io_close2 = fa_io_close_cb;
    }
    int ret = avformat_open_input(&fctx, url, fmt, NULL);
    if (ret < 0) {
        *fctx_out = NULL;
        return ret;
    }
    *fctx_out = fctx;
    return 0;
}

// avformat_find_stream_info(fctx, NULL)
int fa_find_stream_info(AVFormatContext *fctx) {
    return avformat_find_stream_info(fctx, NULL);
}

// avformat_close_input(&fctx) — NULLs the caller's pointer
void fa_avformat_close(AVFormatContext **fctx) {
    avformat_close_input(fctx);
}

// av_strerror into errbuf — C: fa_libav_error_to_txt fallback
void fa_av_strerror(int err, char *errbuf, int errlen) {
    if (errbuf == NULL || errlen <= 0)
        return;
    if (av_strerror(err, errbuf, errlen))
        snprintf(errbuf, errlen, "libav error %d", err);
}

// avio buffer/context free — C: fa_libav_close tail
void fa_avio_free(AVIOContext *avio) {
    if (avio == NULL)
        return;
    void *buf = avio->buffer;
    av_free(avio);
    if (buf != NULL)
        av_free(buf);
}
