// Canonical port of src/backend/hls/hls.c — HLS playlist/variant/segment
// management and the dual-demuxer playback loop. hls.h structures are
// reproduced as Go types; all 55 C functions are mapped in file order.
//
// Sentinel pointers (void*)-1/-2/-3 are modelled by mbRef/segRef codes.
package hls

import (
	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/metadata"
	"github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/subtitles"
	"github.com/czz/movian-go/internal/trace"
)

const (
	HLSQueueMerge         = 0x1          // C: HLS_QUEUE_MERGE
	HLSQueueKeyframeSeen  = 0x2          // C: HLS_QUEUE_KEYFRAME_SEEN
	HLSCryptoNone         = 0            // C: HLS_CRYPTO_NONE
	HLSCryptoAES128       = 1            // C: HLS_CRYPTO_AES128
	hlsCorruptionPeriodUs = 60 * 1000000 // C: HLS_CORRUPTION_MEASURE_PERIOD
)

// Sentinel codes for C's (void*)-1/-2/-3 — used for media_buf_t*,
// hls_segment_t* and hls_demuxer_t* return values alike.
const (
	codeNone = iota
	codeEOF  // C: HLS_EOF ((void*)-1)
	codeNYA  // C: HLS_NYA ((void*)-2)
	codeDIS  // C: HLS_DIS ((void*)-3)
)

// mbRef models C's media_buf_t* which may carry the HLS_EOF / HLS_DIS
// sentinels. code==0 means a real buffer (or nil, like NULL).
type mbRef struct {
	mb   *mediacore.MediaBuf
	code int
}

var (
	mrefEOF = mbRef{nil, codeEOF}
	mrefDIS = mbRef{nil, codeDIS}
)

// hlsError — C: hls_error_t (hls.h:91-102).
type hlsError int

const (
	hlsErrorOK hlsError = iota
	hlsErrorSegmentNotFound
	hlsErrorSegmentBroken
	hlsErrorSegmentAccessDenied
	hlsErrorSegmentBadKey
	hlsErrorVariantProbeError
	hlsErrorVariantNoVideo
	hlsErrorVariantUnknownAudio
	hlsErrorVariantEmpty
	hlsErrorVariantNotFound
)

// hlserrstr — C: hlserrstr (hls.c:307-317).
var hlserrstr = map[hlsError]string{
	hlsErrorSegmentNotFound:     "Segment not found",
	hlsErrorSegmentBroken:       "Unable to open segment",
	hlsErrorSegmentAccessDenied: "Access denied",
	hlsErrorSegmentBadKey:       "Unable to get encryption key",
	hlsErrorVariantProbeError:   "Probing error",
	hlsErrorVariantNoVideo:      "No video",
	hlsErrorVariantUnknownAudio: "Unknown audio codec",
	hlsErrorVariantEmpty:        "Variant is empty",
	hlsErrorVariantNotFound:     "Variant not found",
}

// hlsDiscontinuitySegment — C: hls_discontinuity_segment_t.
type hlsDiscontinuitySegment struct {
	Seq      int   // C: hds_seq
	Refcount int   // C: hds_refcount
	Offset   int64 // C: hds_offset
}

// hlsSegment — C: hls_segment_t.
type hlsSegment struct {
	URL                  string // C: hs_url
	Size                 int64  // C: hs_size
	ByteOffset           int    // C: hs_byte_offset
	ByteSize             int    // C: hs_byte_size
	Duration             int64  // C: hs_duration (usec)
	TimeOffset           int64  // C: hs_time_offset
	TSOffset             int64  // C: hs_ts_offset
	Seq                  int    // C: hs_seq
	DiscontinuitySegment *hlsDiscontinuitySegment
	Crypto               int    // C: hs_crypto
	OpenError            int    // C: hs_open_error
	KeyURL               string // C: hs_key_url (rstr)
	IV                   [16]byte
	Variant              *hlsVariant            // C: hs_variant
	PermanentError       bool                   // C: hs_permanent_error
	Mark                 bool                   // C: hs_mark
	FH                   *fileaccesscore.Handle // C: hs_fh
	OpenTime             int64                  // C: hs_open_time
	BlockedCounter       int                    // C: hs_blocked_counter
	// DLBase snapshots hd_download_counter at open; used to measure
	// segment size when the transport reports no Content-Length
	// (hs_size = -1) — h2/h3/QUIC responses can lack it where the
	// canonical h1 stack always had one.
	DLBase int64
}

// hlsVariant — C: hls_variant_t.
type hlsVariant struct {
	URL         string // C: hv_url
	Demuxer     *hlsDemuxer
	ByteCounter int64 // C: hv_byte_counter

	DemuxerPrivate *tsDemuxer        // C: hv_demuxer_private
	DemuxerClose   func(*hlsVariant) // C: hv_demuxer_close
	DemuxerFlush   func(*hlsVariant) // C: hv_demuxer_flush

	VStream int // C: hv_vstream
	AStream int // C: hv_astream

	LastSeq  int // C: hv_last_seq
	FirstSeq int // C: hv_first_seq

	Segments      []*hlsSegment // C: hv_segments (TAILQ)
	SegmentSearch *hlsSegment   // C: hv_segment_search

	Frozen    bool // C: hv_frozen
	AudioOnly bool // C: hv_audio_only
	Initial   bool // C: hv_initial

	H264Profile     int   // C: hv_h264_profile
	H264Level       int   // C: hv_h264_level
	TargetDuration  int   // C: hv_target_duration
	Loaded          int64 // C: hv_loaded (time_t, 0=not loaded)
	Program         int   // C: hv_program
	Bitrate         int   // C: hv_bitrate
	Width           int   // C: hv_width
	Height          int   // C: hv_height
	CorruptCounter  int   // C: hv_corrupt_counter
	CorruptionsLast int   // C: hv_corruptions_last_period
	CorruptTimer    int64 // C: hv_corrupt_timer

	SubsGroup  string // C: hv_subs_group
	AudioGroup string // C: hv_audio_group

	Duration int64 // C: hv_duration

	CurrentSeg *hlsSegment // C: hv_current_seg

	OpeningFile int // C: hv_opening_file

	KeyURL string                 // C: hv_key_url (rstr)
	Key    *fileaccesscore.Buffer // C: hv_key (buf_t)

	VideoHeaders []byte // C: hv_video_headers (+ _size)

	LastPosSearch   *hlsSegment // C: hv_last_pos_search
	ContinuityCtr   int         // C: hv_continuity_counter
	Name            string      // C: hv_name[32]
	AudioStream     int         // C: hv_audio_stream
	StartTimeOffset int64       // C: hv_start_time_offset
}

// hlsDemuxer — C: hls_demuxer_t.
type hlsDemuxer struct {
	Variants []*hlsVariant // C: hd_variants (TAILQ)
	Type     string        // C: hd_type

	Current *hlsVariant // C: hd_current
	Req     *hlsVariant // C: hd_req

	BW                     int   // C: hd_bw
	BWUpdated              int   // C: hd_bw_updated
	DownloadCounterResetAt int64 // C: hd_download_counter_reset_at
	DownloadCounter        int64 // C: hd_download_counter
	DownloadCounter2       int64 // C: hd_download_counter2
	DownloadBlocked        int   // C: hd_download_blocked

	CurrentStream int // C: hd_current_stream
	PendingStream int // C: hd_pending_stream

	AudioCodec *mediacore.MediaCodec // C: hd_audio_codec

	LastSwitch int64 // C: hd_last_switch

	Cancellable *misc.Cancellable // C: hd_cancellable

	HLS *hls // C: hd_hls

	NoFunctionalStreams bool // C: hd_no_functional_streams

	// C: hd_seek_to_segment — reset to PTS_UNSET once the segment
	// has been found; mq_seektarget then marks frames skipped.
	SeekToSegment int64

	Mb mbRef // C: hd_mb

	Discontinuity    int // C: hd_discontinuity
	DiscontinuitySeq int // C: hd_discontinuity_seq

	LastDTS int64 // C: hd_last_dts
}

// hlsAudioTrack — C: hls_audio_track_t.
type hlsAudioTrack struct {
	StreamID  int            // C: hat_stream_id
	Pid       int            // C: hat_pid
	MuxID     string         // C: hat_mux_id ("" = NULL)
	Trackprop *propcore.Prop // C: hat_trackprop
}

// hls — C: hls_t.
type hls struct {
	mm     *metadata.MetadataManager // C: implicit default manager — injected
	ts     *trace.TraceSystem        // C: trace() global — injected
	subSys *subtitles.System         // C: subtitles.c statics — injected

	BaseURL string // C: h_baseurl

	Debug bool // C: h_debug

	MP *mediacore.MediaPipe // C: h_mp

	CodecH264 *mediacore.MediaCodec // C: h_codec_h264

	Blocked int // C: h_blocked

	Primary hlsDemuxer // C: h_primary
	Audio   hlsDemuxer // C: h_audio

	PlaybackPriority int // C: h_playback_priority

	RestartposLast         int   // C: h_restartpos_last
	LastTimestampPresented int64 // C: h_last_timestamp_presented
	SubScanningDone        bool  // C: h_sub_scanning_done
	EnqueuedSomething      bool  // C: h_enqueued_something

	Duration int64 // C: h_duration

	AudioTracks []*hlsAudioTrack // C: h_audio_tracks (LIST)

	LastEnqueuedSeq int // C: h_last_enqueued_seq

	DiscontinuitySegments []*hlsDiscontinuitySegment // C: h_discontinuity_segments

	LastError hlsError // C: h_last_error
}

// hlsVariantParser — C: hls_variant_parser_t (hls.c:240-245).
type hlsVariantParser struct {
	KeyURL     string // C: hvp_key_url (rstr)
	Crypto     int    // C: hvp_crypto
	ExplicitIV int    // C: hvp_explicit_iv
	IV         [16]byte
}

const testURL = "http://devimages.apple.com.edgekey.net/resources/" +
	"http-streaming/examples/bipbop_16x9/bipbop_16x9_variant.m3u8"
