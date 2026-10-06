package media

import (
	"github.com/czz/movian-go/internal/db/kvstore"
	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	baselibav "github.com/czz/movian-go/internal/libav"
	"github.com/czz/movian-go/internal/trace"

	"sync"

	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/media/libav"
	propcore "github.com/czz/movian-go/internal/prop"
)

// PlaybackState represents the current playback state
type PlaybackState int

// PlaybackPipeline orchestrates the full media playback pipeline:
// format open → read packets → decode → deliver frames
type PlaybackPipeline struct {
	mu sync.Mutex

	ts *trace.TraceSystem // C: trace() global — injected

	url       string
	format    *libav.AVFormatCtx
	mediaPipe *mediacore.MediaPipe

	// fa_libav ownership for the canonical open path (fa AVIO over the
	// fileaccess handle): closed via baselibav.FALibavCloseFormat.
	fctx     *baselibav.AVFormatContext
	libavSys *baselibav.LibAVSystem

	// Stream indices
	videoStreamIndex int
	audioStreamIndex int

	// Decoders and codec contexts
	videoDecoder *libav.VideoDecoder
	audioDecoder *libav.AudioDecoder
	videoCodec   *mediacore.MediaCodec
	audioCodec   *mediacore.MediaCodec
	videoQueue   *mediacore.MediaQueue
	audioQueue   *mediacore.MediaQueue

	// Frame deliver callback (set by GL video engine)
	DeliverCallback mediacore.VideoFrameDeliver

	// Last delivered video frame (for fallback rendering)
	lastVideoFrame   *mediacore.FrameInfo
	lastVideoFrameMu sync.Mutex

	// State
	state    PlaybackState
	stopChan chan struct{}
	wg       sync.WaitGroup
	propMgr  *propcore.PropManager
	mediaSys *mediacore.MediaSystem            // C: media.c globals
	kvstore  *kvstore.KVStore                  // C: global kvstore_* — wired post-create
	fam      *fileaccesscore.FileAccessManager // C: implicit global fa context — wired post-create

	// cleanedUp tracks whether resource cleanup has been run.
	// Both Stop() and handleEOF() call cleanupResources(); this flag
	// ensures cleanup runs exactly once even if both paths are triggered.
	cleanedUp bool

	// stopRequested is set to true when Stop() is called by the user.
	// handleEOF() checks this to distinguish natural EOF (should advance
	// to next track) from user-initiated Stop (should NOT advance).
	stopRequested bool

	// readErrorCount tracks consecutive ReadPacket errors.
	// Reset to 0 on successful read. After 10 consecutive errors,
	// playback is stopped with "error" status (not EOF).
	readErrorCount int

	// seekTarget is the target PTS (in microseconds) after a seek.
	// Packets with PTS < seekTarget are skipped (not dispatched).
	// Mirrors C's mq->mq_seektarget in fa_video.c:284-292.
	// Set by SeekToPosition, cleared when a packet with PTS >= seekTarget arrives.
	seekTarget int64

	// epoch is the current playback epoch, incremented on seek.
	// Mirrors C's mp_epoch — used by ComputeAVDiff to detect
	// stale audio clock after seek.
	epoch int

	// OnEOFCallback is called when playback reaches end of file.
	// Used by PlayQueue to advance to the next track.
	OnEOFCallback func()

	// Frame pacing (MediaClock + FramePacer)
	mediaClock *MediaClock
	framePacer *FramePacer

	// Property tree nodes for playback control
	// These reference the C-compatible props created by glw_props.go (Set B)
	propMediaRoot     *propcore.Prop
	propCurrent       *propcore.Prop
	propPlayStatus    *propcore.Prop
	propCurrentTime   *propcore.Prop
	propDuration      *propcore.Prop
	propCanPause      *propcore.Prop
	propCanSeek       *propcore.Prop
	propCanSkipBack   *propcore.Prop
	propCanSkipFwd    *propcore.Prop
	propLoading       *propcore.Prop
	propMetadataTitle *propcore.Prop

	// Additional C-compatible media properties (GAP-3)
	propURL           *propcore.Prop
	propFPS           *propcore.Prop
	propPauseReason   *propcore.Prop
	propAVDelta       *propcore.Prop
	propAVDiff        *propcore.Prop
	propCanEject      *propcore.Prop
	propCanStop       *propcore.Prop
	propCanShuffle    *propcore.Prop
	propCanRepeat     *propcore.Prop
	propShuffle       *propcore.Prop
	propRepeat        *propcore.Prop
	propAudioTracks   *propcore.Prop
	propSubTracks     *propcore.Prop
	propBufferCurrent *propcore.Prop
	propBufferLimit   *propcore.Prop
	propVideo         *propcore.Prop

	// GAP-001: Missing C-compatible media props
	propPrimary       *propcore.Prop
	propIO            *propcore.Prop
	propNotifications *propcore.Prop
	propCtrl          *propcore.Prop
	propModel         *propcore.Prop
	propEventSink     *propcore.Prop
	propSeekTime      *propcore.Prop
	propSVDelta       *propcore.Prop
	propAVDiffError   *propcore.Prop
	propBufferDelay   *propcore.Prop
	propBuffer        *propcore.Prop
	propSettingRoot   *propcore.Prop

	// Subtitle track selection wiring (SUB-1)
	// C: mp->mp_prop_subtitle_track_current = prop_create(p, "current")
	//    mtm_current_sub = prop_subscribe(PROP_TAG_CALLBACK_STRING, mtm_set_current, ...)
	// When user selects a track, the "current" prop changes → callback
	// → mp_track_mgr_select_track → mp_load_ext_sub
	propSubCurrent *propcore.Prop
	subCurrentSub  *propcore.Subscription
}

// SetTraceSystem injects the trace system (C: trace() global).
func (p *PlaybackPipeline) SetTraceSystem(ts *trace.TraceSystem) { p.ts = ts }

// NewPlaybackPipeline creates a new playback pipeline
// SetKVStore injects the kvstore (C: global kvstore_* — track prefs).
func (p *PlaybackPipeline) SetKVStore(kvs *kvstore.KVStore) { p.kvstore = kvs }

// SetFAM injects the file access manager (C: implicit global fa context).
func (p *PlaybackPipeline) SetFAM(fam *fileaccesscore.FileAccessManager) { p.fam = fam }

func NewPlaybackPipeline(pm *propcore.PropManager, ms *mediacore.MediaSystem) *PlaybackPipeline {
	return &PlaybackPipeline{
		propMgr:          pm,
		mediaSys:         ms,
		videoStreamIndex: -1,
		audioStreamIndex: -1,
		state:            PlaybackStateStopped,
	}
}
