#include <libswresample/swresample.h>
#include <libavutil/samplefmt.h>
#include <libavutil/channel_layout.h>
#include <libavutil/opt.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

// Helper functions for swresample CGO bindings
// These wrap FFmpeg swresample functions to avoid complex type issues in CGO

void *swr_alloc_helper(void) {
    return swr_alloc();
}

// set_chlayout_from_mask sets a channel layout option using the FFmpeg 7
// AVChannelLayout API. in_channel_layout/out_channel_layout (uint64 mask)
// were removed; the equivalent options are in_chlayout/out_chlayout.
static int set_chlayout_from_mask(void *s, const char *name, int64_t mask) {
    AVChannelLayout ch_layout;
    memset(&ch_layout, 0, sizeof(ch_layout));
    if (mask > 0) {
        ch_layout.order = AV_CHANNEL_ORDER_NATIVE;
        ch_layout.u.mask = (uint64_t)mask;
        ch_layout.nb_channels = av_popcount64(ch_layout.u.mask);
    } else {
        av_channel_layout_default(&ch_layout, 2);
    }
    int ret = av_opt_set_chlayout(s, name, &ch_layout, 0);
    av_channel_layout_uninit(&ch_layout);
    return ret;
}

void *swr_alloc_set_opts_helper(int64_t out_ch_layout, int out_sample_fmt, int out_sample_rate,
                                int64_t in_ch_layout, int in_sample_fmt, int in_sample_rate) {
    struct SwrContext *s = swr_alloc();
    if (!s) return NULL;

    // Set channel layouts (FFmpeg 7 AVChannelLayout options)
    set_chlayout_from_mask(s, "in_chlayout", in_ch_layout);
    set_chlayout_from_mask(s, "out_chlayout", out_ch_layout);
    
    // Set sample formats
    av_opt_set_int(s, "in_sample_fmt", in_sample_fmt, 0);
    av_opt_set_int(s, "out_sample_fmt", out_sample_fmt, 0);
    
    // Set sample rates
    av_opt_set_int(s, "in_sample_rate", in_sample_rate, 0);
    av_opt_set_int(s, "out_sample_rate", out_sample_rate, 0);
    
    return s;
}

int swr_init_helper(void *s) {
    return swr_init((struct SwrContext *)s);
}

void swr_free_helper(void **s) {
    swr_free((struct SwrContext **)s);
}

int swr_convert_helper(void *s, uint8_t **out, int out_count, uint8_t **in, int in_count) {
    return swr_convert((struct SwrContext *)s, out, out_count, (const uint8_t **)in, in_count);
}

int64_t swr_get_delay_helper(void *s, int64_t base) {
    return swr_get_delay((struct SwrContext *)s, base);
}

int swr_set_compensation_helper(void *s, int sample_delta, int compensation_distance) {
    return swr_set_compensation((struct SwrContext *)s, sample_delta, compensation_distance);
}
