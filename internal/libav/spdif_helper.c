#include <libavformat/avformat.h>
#include <libavcodec/avcodec.h>
#include <libavutil/opt.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

// SPDIF muxing helper functions for CGO

// Callback context for SPDIF write
typedef struct {
    uint8_t *buffer;
    int buffer_size;
    int data_size;
} SPDIFWriteContext;

// Write callback for SPDIF muxing
static int spdif_write_callback(void *opaque, const uint8_t *buf, int buf_size) {
    SPDIFWriteContext *ctx = (SPDIFWriteContext *)opaque;
    int new_size = ctx->data_size + buf_size;
    
    if (new_size > ctx->buffer_size) {
        // Reallocate buffer
        int new_alloc = new_size * 2;
        ctx->buffer = realloc(ctx->buffer, new_alloc);
        ctx->buffer_size = new_alloc;
    }
    
    memcpy(ctx->buffer + ctx->data_size, buf, buf_size);
    ctx->data_size = new_size;
    return buf_size;
}

// Create SPDIF muxer context
void *spdif_muxer_create(int codec_id, int sample_rate) {
    const AVOutputFormat *ofmt = av_guess_format("spdif", NULL, NULL);
    if (!ofmt) return NULL;
    
    AVFormatContext *fctx = avformat_alloc_context();
    if (!fctx) return NULL;
    
    fctx->oformat = ofmt;
    
    // Allocate write context
    SPDIFWriteContext *write_ctx = malloc(sizeof(SPDIFWriteContext));
    write_ctx->buffer = malloc(16384);
    write_ctx->buffer_size = 16384;
    write_ctx->data_size = 0;
    
    // Create AVIO context
    fctx->pb = avio_alloc_context(write_ctx->buffer, 16384, 1, write_ctx, 
                                   NULL, spdif_write_callback, NULL);
    if (!fctx->pb) {
        avformat_free_context(fctx);
        free(write_ctx->buffer);
        free(write_ctx);
        return NULL;
    }
    
    // Find codec
    const AVCodec *codec = avcodec_find_decoder(codec_id);
    if (!codec) {
        av_free(fctx->pb);
        avformat_free_context(fctx);
        free(write_ctx->buffer);
        free(write_ctx);
        return NULL;
    }
    
    // Create stream
    AVStream *s = avformat_new_stream(fctx, NULL);
    if (!s) {
        av_free(fctx->pb);
        avformat_free_context(fctx);
        free(write_ctx->buffer);
        free(write_ctx);
        return NULL;
    }
    
    // Set codec parameters
    s->codecpar->codec_type = AVMEDIA_TYPE_AUDIO;
    s->codecpar->codec_id = codec_id;
    s->codecpar->sample_rate = sample_rate;
    
    // Write header
    if (avformat_write_header(fctx, NULL) < 0) {
        av_free(fctx->pb);
        avformat_free_context(fctx);
        free(write_ctx->buffer);
        free(write_ctx);
        return NULL;
    }
    
    return fctx;
}

// Mux a packet through SPDIF
int spdif_mux_packet(void *fctx, uint8_t *data, int data_size) {
    AVFormatContext *fc = (AVFormatContext *)fctx;
    if (!fc) return -1;
    
    AVPacket pkt;
    av_new_packet(&pkt, data_size);
    memcpy(pkt.data, data, data_size);
    pkt.stream_index = 0;
    
    int ret = av_write_frame(fc, &pkt);
    av_packet_unref(&pkt);
    avio_flush(fc->pb);
    
    return ret;
}

// Get muxed data
int spdif_get_data(void *fctx, uint8_t **out_data) {
    AVFormatContext *fc = (AVFormatContext *)fctx;
    if (!fc || !fc->pb) return -1;
    
    SPDIFWriteContext *ctx = (SPDIFWriteContext *)fc->pb->opaque;
    *out_data = ctx->buffer;
    return ctx->data_size;
}

// Reset muxed data buffer
void spdif_reset_data(void *fctx) {
    AVFormatContext *fc = (AVFormatContext *)fctx;
    if (!fc || !fc->pb) return;
    
    SPDIFWriteContext *ctx = (SPDIFWriteContext *)fc->pb->opaque;
    ctx->data_size = 0;
}

// Destroy SPDIF muxer
void spdif_muxer_destroy(void *fctx) {
    AVFormatContext *fc = (AVFormatContext *)fctx;
    if (!fc) return;
    
    if (fc->pb) {
        SPDIFWriteContext *ctx = (SPDIFWriteContext *)fc->pb->opaque;
        if (ctx) {
            free(ctx->buffer);
            free(ctx);
        }
        av_free(fc->pb);
    }
    
    avformat_free_context(fc);
}
