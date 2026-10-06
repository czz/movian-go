// Package libav — canonical port of video_subtitles_lavc
// (src/subtitles/video_overlay.c:40-121), the ENABLE_LIBAV else-branch of
// video_overlay_decode: decodes a subtitle packet through lavc and turns
// the resulting AVSubtitle into video_overlay_t entries on
// mp->mp_overlay_queue.
//
// FFmpeg-9 API adaptations (documented):
//   - AVSubtitleRect.pict.data/linesize renamed to data[]/linesize[]
//     (same layout: data[0] = palette indices, data[1] = 256-entry
//     BGR32 CLUT).
//   - AVSubtitle is heap-allocated via calloc instead of a stack struct
//     (avsubtitle_free works the same on it).
package libav

/*
#include <libavcodec/avcodec.h>
#include <stdlib.h>
#include <string.h>

// C: AVPacket avpkt; av_init_packet(&avpkt); avpkt.data = mb->mb_data;
//     avpkt.size = mb->mb_size; avcodec_decode_subtitle2(ctx, &sub,
//     &got_sub, &avpkt) < 1 || !got_sub → return
static int ml_decode_subtitle(void *avctx, const void *data, int size,
                              AVSubtitle *sub) {
    AVPacket avpkt;
    // FFmpeg ≥5 deprecates av_init_packet — expand it inline: zeroed
    // fields plus pts/dts=NOPTS_VALUE and pos=-1 (its exact body).
    memset(&avpkt, 0, sizeof(avpkt));
    avpkt.pts = avpkt.dts = AV_NOPTS_VALUE;
    avpkt.pos = -1;
    avpkt.data = (uint8_t*)data;
    avpkt.size = size;
    int got_sub = 0;
    int r = avcodec_decode_subtitle2((AVCodecContext*)avctx, sub,
                                     &got_sub, &avpkt);
    if (r < 1 || !got_sub)
        return 0;
    return 1;
}

// --- AVSubtitle / AVSubtitleRect accessors ---
static unsigned ml_sub_num_rects(AVSubtitle *sub) { return sub->num_rects; }
static uint32_t ml_sub_start_display_time(AVSubtitle *sub) {
    return sub->start_display_time;
}
static uint32_t ml_sub_end_display_time(AVSubtitle *sub) {
    return sub->end_display_time;
}
static AVSubtitleRect *ml_sub_rect(AVSubtitle *sub, int i) {
    return sub->rects[i];
}
static int ml_rect_type(AVSubtitleRect *r) { return r->type; }
static int ml_rect_x(AVSubtitleRect *r) { return r->x; }
static int ml_rect_y(AVSubtitleRect *r) { return r->y; }
static int ml_rect_w(AVSubtitleRect *r) { return r->w; }
static int ml_rect_h(AVSubtitleRect *r) { return r->h; }
static void *ml_rect_data(AVSubtitleRect *r, int i) { return r->data[i]; }
static int ml_rect_linesize(AVSubtitleRect *r, int i) {
    return r->linesize[i];
}
static char *ml_rect_ass(AVSubtitleRect *r) { return r->ass; }

// --- AVCodecContext accessors ---
static int ml_cctx_width(void *ctx) { return ((AVCodecContext*)ctx)->width; }
static int ml_cctx_height(void *ctx) { return ((AVCodecContext*)ctx)->height; }
static int ml_cctx_subtitle_header_size(void *ctx) {
    return ((AVCodecContext*)ctx)->subtitle_header_size;
}
static void *ml_cctx_subtitle_header(void *ctx) {
    return ((AVCodecContext*)ctx)->subtitle_header;
}
*/
import "C"

import (
	"encoding/binary"
	"unsafe"

	"github.com/czz/movian-go/internal/image"
	"github.com/czz/movian-go/internal/libav"
	mediacore "github.com/czz/movian-go/internal/media/core"
)

// VideoSubtitlesLavc — C: video_subtitles_lavc (video_overlay.c:44).
// ctx is mc->ctx, the AVCodecContext of the subtitle decoder.
func VideoSubtitlesLavc(mp *mediacore.MediaPipe, mb *mediacore.MediaBuf,
	ctx *libav.AVCodecContext) {

	var sub C.AVSubtitle

	var dataPtr unsafe.Pointer
	if len(mb.Data) > 0 {
		dataPtr = unsafe.Pointer(&mb.Data[0])
	}

	// C enqueues a TIMED_FLUSH at mb_pts unconditionally before the
	// decode attempt — and leaves it queued even when the decode fails.
	flush := &mediacore.VideoOverlay{
		Type:  mediacore.VOTimedFlush,
		Start: mb.PTS,
	}
	mediacore.VideoOverlayEnqueue(mp, flush)

	cctx := (*C.AVCodecContext)(ctx.CPtr())
	if C.ml_decode_subtitle(unsafe.Pointer(cctx), dataPtr,
		C.int(len(mb.Data)), &sub) == 0 {
		return
	}
	defer C.avsubtitle_free(&sub)

	startDT := int64(C.ml_sub_start_display_time(&sub))

	if C.ml_sub_num_rects(&sub) == 0 {
		// Flush screen
		vo := &mediacore.VideoOverlay{
			Type:  mediacore.VOTimedFlush,
			Start: mb.PTS + startDT*1000,
		}
		mediacore.VideoOverlayEnqueue(mp, vo)
		return
	}

	endDT := int64(C.ml_sub_end_display_time(&sub))

	n := int(C.ml_sub_num_rects(&sub))
	for i := range n {
		r := C.ml_sub_rect(&sub, C.int(i))

		switch C.ml_rect_type(r) {
		case C.SUBTITLE_BITMAP:
			vo := &mediacore.VideoOverlay{}

			vo.Start = mb.PTS + startDT*1000
			vo.Stop = mb.PTS + endDT*1000

			w := int(C.ml_cctx_width(unsafe.Pointer(cctx)))
			if w == 0 {
				w = 720
			}
			h := int(C.ml_cctx_height(unsafe.Pointer(cctx)))
			if h == 0 {
				h = 576
			}
			vo.CanvasWidth = int16(w)
			vo.CanvasHeight = int16(h)

			vo.X = int16(C.ml_rect_x(r))
			vo.Y = int16(C.ml_rect_y(r))

			rw := int(C.ml_rect_w(r))
			rh := int(C.ml_rect_h(r))

			vo.Pixmap = image.PixmapCreate(rw, rh, image.PixmapBGR32, 0)
			if vo.Pixmap == nil {
				// C: free(vo); break; — exits the switch, loop
				// continues to the next rect
				continue
			}

			// C: src = r->pict.data[0]; clut = r->pict.data[1]
			srcSize := int(C.ml_rect_linesize(r, 0)) * rh
			var src []byte
			if p := C.ml_rect_data(r, 0); p != nil && srcSize > 0 {
				src = unsafe.Slice((*byte)(p), srcSize)
			}
			clut := unsafe.Slice((*uint32)(C.ml_rect_data(r, 1)), 256)
			linesize := int(C.ml_rect_linesize(r, 0))

			for y := range rh {
				dst := vo.Pixmap.Data[y*vo.Pixmap.Stride : y*vo.Pixmap.Stride+rw*4]
				row := y * linesize
				for x := range rw {
					binary.NativeEndian.PutUint32(dst[x*4:],
						clut[src[row+x]])
				}
			}
			mediacore.VideoOverlayEnqueue(mp, vo)

		case C.SUBTITLE_ASS:
			if mp.Sys.SubAssRender != nil {
				hsz := int(C.ml_cctx_subtitle_header_size(unsafe.Pointer(cctx)))
				var header []byte
				if p := C.ml_cctx_subtitle_header(unsafe.Pointer(cctx)); p != nil && hsz > 0 {
					header = C.GoBytes(p, C.int(hsz))
				}
				mp.Sys.SubAssRender(mp, C.GoString(C.ml_rect_ass(r)),
					header, mbFontContext(mb))
			}
		}
	}
}

// mbFontContext — C: mb->mb_font_context (union member; int fontdomain)
func mbFontContext(mb *mediacore.MediaBuf) int {
	if v, ok := mb.FontContext.(int); ok {
		return v
	}
	return 0
}
