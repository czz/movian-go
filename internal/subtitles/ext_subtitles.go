package subtitles

// Canonical port of the ext_subtitles_t runtime pieces of
// src/subtitles/ext_subtitles.c — the type and the pick/destroy
// entry points that video_decoder.c calls directly.
//
// The type lives here (not in pkg/subtitles/ext) because
// video/decoder must reference it: ext depends on media/libav and
// pkg/text which both chain back to decoder — a real cycle. The
// subtitle loaders (vobsub/srt/ass/timedtext) remain in ext and fill
// Dtor/Picker on this struct.

import (
	mediacore "github.com/czz/movian-go/internal/media/core"
)

// ExtSubtitles — C: ext_subtitles_t (ext_subtitles.h)
type ExtSubtitles struct {
	Entries []*mediacore.VideoOverlay // C: es_entries (video_overlay_queue TAILQ)
	Cur     *mediacore.VideoOverlay   // C: es_cur

	Sys *System // owning subtitles subsystem (C: subtitles.c statics)

	Dtor   func(*ExtSubtitles)        // C: es_dtor
	Picker func(*ExtSubtitles, int64) // C: es_picker
}

// SubtitlesDestroy — C: subtitles_destroy (ext_subtitles.c)
func SubtitlesDestroy(es *ExtSubtitles) {
	if es == nil {
		return
	}
	for _, vo := range es.Entries {
		mediacore.VideoOverlayDestroy(vo)
	}
	es.Entries = nil
	if es.Dtor != nil {
		es.Dtor(es)
	}
}

// esNext — C: es_next (TAILQ_NEXT over es_entries)
func esNext(es *ExtSubtitles, vo *mediacore.VideoOverlay) *mediacore.VideoOverlay {
	for i, x := range es.Entries {
		if x == vo {
			if i+1 < len(es.Entries) {
				return es.Entries[i+1]
			}
			return nil
		}
	}
	return nil
}

// voDeliver — C: vo_deliver (ext_subtitles.c)
func voDeliver(es *ExtSubtitles, vo *mediacore.VideoOverlay,
	mp *mediacore.MediaPipe, userTime, userTimeToPts int64) {
	s := vo.Start
	for {
		es.Cur = vo

		dup := mediacore.VideoOverlayDup(vo)
		dup.Start += userTimeToPts
		dup.Stop += userTimeToPts

		mediacore.VideoOverlayEnqueue(mp, dup)

		vo = esNext(es, vo)
		if !(vo != nil && vo.Start == s && vo.Stop > userTime) {
			break
		}
	}
}

// SubtitlesPick — C: subtitles_pick (ext_subtitles.c), called from
// video_decoder.c:134 to deliver the overlay active at userTime.
func SubtitlesPick(es *ExtSubtitles, userTime, pts int64,
	mp *mediacore.MediaPipe) {
	vo := es.Cur

	if es.Picker != nil {
		es.Picker(es, pts)
		return
	}

	userTimeToPts := pts - userTime
	for vo != nil {
		vo = esNext(es, vo)
		if vo != nil && vo.Start <= userTime && vo.Stop > userTime {
			voDeliver(es, vo, mp, userTime, userTimeToPts)
			return
		}
		if vo != nil && vo.Start > userTime {
			break
		}
	}

	vo = es.Cur

	if vo != nil && vo.Start <= userTime && vo.Stop > userTime {
		return // Already sent
	}

	for _, vo = range es.Entries {
		if vo.Start <= userTime && vo.Stop > userTime &&
			vo.Start > userTime-1000000 {
			// don't re-delivery long standing item
			voDeliver(es, vo, mp, userTime, userTimeToPts)
			return
		}
		if vo.Start > userTime {
			break
		}
	}
	es.Cur = nil
}
