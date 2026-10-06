package media

import (
	"github.com/czz/movian-go/internal/event"
	"github.com/czz/movian-go/internal/gconf"
	mediacore "github.com/czz/movian-go/internal/media/core"
	propcore "github.com/czz/movian-go/internal/prop"
)

const (
	PlaybackStateStopped PlaybackState = iota
	PlaybackStatePlaying
	PlaybackStatePaused
	PlaybackStateSeeking
)

// GetState returns the current playback state
func (p *PlaybackPipeline) GetState() PlaybackState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

// GetVideoFrame returns the last delivered video frame, or nil if none.
func (p *PlaybackPipeline) GetVideoFrame() *mediacore.FrameInfo {
	p.lastVideoFrameMu.Lock()
	defer p.lastVideoFrameMu.Unlock()
	return p.lastVideoFrame
}

// SetVideoFrame stores the last delivered video frame (called by DeliverCallback).
func (p *PlaybackPipeline) SetVideoFrame(fi *mediacore.FrameInfo) {
	p.lastVideoFrameMu.Lock()
	defer p.lastVideoFrameMu.Unlock()
	p.lastVideoFrame = fi
}

// onSubtitleTrackSelected is called when the subtitle/current prop changes.
// Mirrors C's track-node selection path: EVENT_SELECT_SUBTITLE_TRACK is
// enqueued on mp via mp_enqueue_event (media_track.c:756-759), which runs
// mp_track_mgr_select_track under mp_mutex — "sub:" selects nothing,
// "libav:N" sets mp_video.mq_stream2, other ids go through mp_load_ext_sub.
func (p *PlaybackPipeline) onSubtitleTrackSelected(trackID string) {
	p.mu.Lock()
	mp := p.mediaPipe
	p.mu.Unlock()
	if mp == nil {
		return
	}
	e := &event.EventSelectTrack{ID: trackID, Manual: true}
	e.Event.SetConcrete(e)
	e.Type = event.EVENT_SELECT_SUBTITLE_TRACK
	mediacore.MpEnqueueEvent(mp, &mediacore.MediaEvent{
		Type: int(e.Type), Data: e,
	})
}

// GetDuration returns the duration in seconds
func (p *PlaybackPipeline) GetDuration() int64 {
	if p.format == nil {
		return 0
	}
	return p.format.GetDuration() / 1000000
}

// GetMediaPropRoot returns the media property root for prop_link to a video page.
// C uses prop_link(mp->mp_prop_root, prop_create(self, "media")) in
// glw_video_common.c:641 to link the media prop tree to the video widget.
func (p *PlaybackPipeline) GetMediaPropRoot() *propcore.Prop {
	return p.propMediaRoot
}

// SetOnEOFCallback sets the callback to be called when playback reaches EOF.
// Used by PlayQueue to advance to the next track.
func (p *PlaybackPipeline) SetOnEOFCallback(fn func()) {
	p.OnEOFCallback = fn
}

// GetFramePacer returns the frame pacer for render-thread frame scheduling.
func (p *PlaybackPipeline) GetFramePacer() *FramePacer {
	return p.framePacer
}

// GetMediaClock returns the media clock.
func (p *PlaybackPipeline) GetMediaClock() *MediaClock {
	return p.mediaClock
}

// GetPosition returns the current position in seconds.
// Uses mediaPipe.CurrentTime (updated from packet PTS) if available,
// otherwise falls back to the media clock.
func (p *PlaybackPipeline) GetPosition() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.mediaPipe != nil && p.mediaPipe.CurrentTime > 0 {
		return p.mediaPipe.CurrentTime / 1000000
	}
	if p.mediaClock != nil {
		return p.mediaClock.GetTime() / 1000000
	}
	return 0
}

// EnableDetailedAvdiff — C: gconf.enable_detailed_avdiff
// (cross-pkg readers: ui/glw video paths).
func EnableDetailedAvdiff(g *gconf.T) bool { return g != nil && g.EnableDetailedAvdiff.Load() }
