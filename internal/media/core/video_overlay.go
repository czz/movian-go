package core

// Canonical port of src/subtitles/video_overlay.c — the media_pipe-coupled
// half (type, queue ops, dup/destroy).
//
// Go layering note: video_overlay_render_cleartext, calculate_subtitle_duration
// and video_overlay_decode need pkg/text (text_parse), and pkg/text transitively
// reaches this package via backend/rtmp. To keep the import DAG acyclic those
// functions live in pkg/subtitles/ext — the same coupling exists in C where
// src/subtitles/*.c both uses and is used by media_pipe_t. The ext package
// registers MediaSystem.SubtitlesLoad/SubtitlesDestroy via RegisterMediaHooks,
// which is how mp_load_ext_sub reaches subtitles_load (media_track.c → ext_subtitles.c).

import (
	"slices"

	"github.com/czz/movian-go/internal/image"
)

// C: enum { VO_BITMAP, VO_TEXT, VO_FLUSH, VO_TIMED_FLUSH }
const (
	VOBitmap = iota
	VOText
	VOFlush
	VOTimedFlush
)

// VideoOverlay — C: video_overlay_t (video_overlay.h)
type VideoOverlay struct {
	Type          int           // C: vo_type
	Start         int64         // C: vo_start
	Stop          int64         // C: vo_stop
	Pixmap        *image.Pixmap // C: vo_pixmap
	Text          []uint32      // C: vo_text (uc32 buffer)
	TextLength    int           // C: vo_text_length
	StopEstimated bool          // C: vo_stop_estimated
	Alignment     int           // C: vo_alignment (LAYOUT_ALIGN_*)
	Layer         int           // C: vo_layer
	AbsPos        bool          // C: vo_abspos
	X             int16         // C: vo_x
	Y             int16         // C: vo_y
	FadeIn        int           // C: vo_fadein
	FadeOut       int           // C: vo_fadeout
	PaddingLeft   int16         // C: vo_padding_left (-1 = auto)
	PaddingTop    int16         // C: vo_padding_top
	PaddingRight  int16         // C: vo_padding_right
	PaddingBottom int16         // C: vo_padding_bottom
	CanvasWidth   int16         // C: vo_canvas_width (-1 = frame width)
	CanvasHeight  int16         // C: vo_canvas_height
}

// VideoOverlayEnqueue — C: video_overlay_enqueue
func VideoOverlayEnqueue(mp *MediaPipe, vo *VideoOverlay) {
	mp.OverlayMutex.Lock()
	defer mp.OverlayMutex.Unlock()
	mp.OverlayQueue = append(mp.OverlayQueue, vo)
}

// VideoOverlayDestroy — C: video_overlay_destroy
func VideoOverlayDestroy(vo *VideoOverlay) {
	if vo == nil {
		return
	}
	if vo.Pixmap != nil {
		image.PixmapRelease(vo.Pixmap)
	}
	vo.Text = nil
}

// VideoOverlayDup — C: video_overlay_dup
func VideoOverlayDup(src *VideoOverlay) *VideoOverlay {
	if src == nil {
		return nil
	}
	dst := *src // C: memcpy

	if src.Pixmap != nil {
		dst.Pixmap = image.PixmapDup(src.Pixmap)
	}
	if src.Text != nil {
		dst.Text = make([]uint32, src.TextLength)
		copy(dst.Text, src.Text[:src.TextLength])
	}
	return &dst
}

// VideoOverlayDequeueDestroy — C: video_overlay_dequeue_destroy
// Caller must hold mp.OverlayMutex (C: called under mp_overlay_mutex).
func VideoOverlayDequeueDestroy(mp *MediaPipe, vo *VideoOverlay) {
	for i, x := range mp.OverlayQueue {
		if x == vo {
			mp.OverlayQueue = slices.Delete(mp.OverlayQueue, i, i+1)
			break
		}
	}
	VideoOverlayDestroy(vo)
}

// VideoOverlayFlushLocked — C: video_overlay_flush_locked
// Caller must hold mp.OverlayMutex.
func VideoOverlayFlushLocked(mp *MediaPipe, send bool) {
	for len(mp.OverlayQueue) > 0 {
		VideoOverlayDequeueDestroy(mp, mp.OverlayQueue[0])
	}
	if !send {
		return
	}
	vo := &VideoOverlay{Type: VOFlush}
	mp.OverlayQueue = append(mp.OverlayQueue, vo)
}

// MediaHooks.SubtitlesLoad — C: subtitles_load(mp, url) called from media_track.c's
// subtitle_loader_thread. Registered by pkg/subtitles/ext init() to keep the
// Go import DAG acyclic (ext → mediacore, never the reverse).

// MediaSystem.SubtitlesDestroy — C: subtitles_destroy called from ext_sub_dtor and
// video_decoder.c's MB_CTRL_EXT_SUBTITLE path.

// MediaHooks.VideoOverlayDecode — C: video_overlay_decode(mp, mb) called from
// video_decoder.c's MB_SUBTITLE path. Registered by pkg/subtitles/ext init().

// MediaHooks.SubAssRender — C: sub_ass_render(mp, ass, header, header_size,
// fontdomain) called from video_overlay.c's video_subtitles_lavc for
// SUBTITLE_ASS rects. Registered by pkg/subtitles/ext init() — the lavc
// decode lives in pkg/media/libav which cannot import ext.
