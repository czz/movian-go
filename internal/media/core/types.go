package core

import (
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/czz/movian-go/internal/db/kvstore"
	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"

	"github.com/czz/movian-go/internal/libav"
	"github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
	settingscore "github.com/czz/movian-go/internal/settings"
)

// AVRational represents a rational number (numerator/denominator)
type AVRational struct {
	Num int
	Den int
}

// CodecID represents codec identifier type
type CodecID int

// MediaType represents media type
type MediaType int

// MediaPipeFlags represents media pipe flags
type MediaPipeFlags int

// MediaHoldFlags represents media hold flags
type MediaHoldFlags int

// FrameInfo represents frame information
type FrameInfo struct {
	PTS           int64
	Duration      int64
	Width         int
	Height        int
	PixelFormat   int
	ColorSpace    int
	AspectNum     int
	AspectDen     int
	TopFieldFirst bool
	Interlaced    bool
	RefRelease    func(unsafe.Pointer)
	RefAux        unsafe.Pointer
	// C: fi_refop (glw_video_sunxi.c CED2 path — frame refcount op
	// refop(data2, delta); field exists in the cedar trees' media.h).
	Refop func(data2 unsafe.Pointer, delta int)
	Data  [4][]byte
	Pitch [4]int
	// C: fi_u32 — union member aliasing fi_data (media.h:105-111).
	// TEX-engine producers store GL texture handles here instead of
	// plane pointers; kept as a separate field since Go has no unions.
	U32        [4]uint32
	PixFmt     int
	AVFrame    *libav.AVFrame
	DARNum     int
	DARDen     int
	Epoch      int
	UserTime   int64
	DriveClock int
	TFF        bool
	Prescaled  bool
	Type       FourCC
	Opaque     uintptr // AVFrame.opaque value (0 = not set, N = Reorder[N-1])

	// C: fi_hshift/fi_vshift — chroma subsampling shifts, filled by
	// video_deliver_lavc (glw_video_common.c:1299) for planar YUV formats.
	HShift int
	VShift int

	// C: fi_update_pts_only (media.h:136) — set by the android SURF
	// path so the engine only refreshes the PTS bookkeeping
	// (android_video_codec.c:566).
	UpdatePtsOnly bool
}

// VideoFrameDeliver is a callback for delivering video frames
type VideoFrameDeliver func(fi *FrameInfo) error

// SetVideoCodec sets the video codec
type SetVideoCodec func(mc *MediaCodec) error

// MediaPipe represents a media pipeline
// C: media_pipe_t (media.h:163-383) — reference counted, global list, ~80 fields.
type MediaPipe struct {
	Mutex sync.RWMutex

	// Sys — the owning MediaSystem (C: ambient media.c globals).
	Sys *MediaSystem

	// KVStore — C: global kvstore_* funcs used by the track managers
	// (user selection persistence). Injected post-MpCreate.
	KVStore *kvstore.KVStore
	// FAM — C: the implicit global fa context used by subtitle probing
	// and playlist/archive loaders. Injected post-MpCreate.
	FAM         *fileaccesscore.FileAccessManager
	RefCount    int32 // C: mp_refcount (atomic) — memory refcount
	URL         string
	Duration    int64
	CurrentTime int64
	Epoch       int64 // C: mp_epoch (int, starts at 1)
	Flags       MediaPipeFlags
	HoldFlags   MediaHoldFlags // C: mp_hold_flags
	HoldGate    int            // C: mp_hold_gate — toggles 0/1 on hold changes
	Name        string         // C: mp_name

	// C: mp_eof — set when the demuxer hit EOF (media.h:183)
	EOF int32

	// C: mp_buffer_limit (unsigned int) — max buffer size in bytes
	BufferLimit      int
	BufferCurrent    int   // C: mp_buffer_current — bytes queued (all queues)
	BufferDelay      int64 // C: mp_buffer_delay — current delay in µs
	MaxRealtimeDelay int32 // C: mp_max_realtime_delay
	Satisfied        int   // C: mp_satisfied (-1 = unknown)
	PreBufferDelay   int64 // C: mp_pre_buffer_delay

	// C: mp_backpressure — signaled when queues drain
	Backpressure *sync.Cond
	// C: mp_mb_pool — media_buf pool
	MbPool *MediaBufPool
	// C: mp_stats_update_limiter
	StatsUpdateLimiter int
	// C: mp_vol_user (int), mp_vol_ui (float)
	VolUser int
	VolUI   float64

	Video *MediaQueue
	Audio *MediaQueue

	// Aliases for compatibility
	VideoQueue *MediaQueue
	AudioQueue *MediaQueue
	MQ         *MediaQueue

	VideoCodec *MediaCodec
	AudioCodec *MediaCodec

	Settings *MediaSettings
	Tracks   *MediaTrackList

	// Track manager for track selection
	TrackManager *struct {
		SetSubtitleTrack func(track int)
	}

	AVDelta int64
	SVDelta int64

	PropRoot            *propcore.Prop
	PropCtrl            *propcore.Prop
	PropSubtitle        *propcore.Prop
	SettingVideoRoot    *propcore.Prop
	SettingAudioRoot    *propcore.Prop
	SettingSubtitleRoot *propcore.Prop
	SettingRoot         *propcore.Prop

	PropAudioTrackCurrent       *propcore.Prop
	PropAudioTrackCurrentManual *propcore.Prop
	PropSubtitleTrackCurrent    *propcore.Prop
	// C: mp_prop_subtitle_track_current_manual
	PropSubtitleTrackCurrentManual *propcore.Prop

	// C: media_track_mgr_t mp_audio_track_mgr / mp_subtitle_track_mgr
	AudioTrackMgr    *MediaTrackMgr
	SubtitleTrackMgr *MediaTrackMgr

	// C: mp->mp_subtitle_loader_url / mp->mp_subtitle_loader_running
	// (guarded by mp.Mutex — C: mp_mutex)
	SubtitleLoaderURL     string
	SubtitleLoaderRunning bool

	// C: mp_overlay_mutex / mp_overlay_queue (media.h:230-231)
	// mp_overlay_mutex also protects mp_spu_queue in C.
	OverlayMutex sync.Mutex
	OverlayQueue []*VideoOverlay
	SpuQueue     []*DVDSPU // C: mp_spu_queue (media.h:232)

	// C: mp_prop_primary, mp_prop_playstatus, mp_prop_pausereason, etc.
	PropPrimary         *propcore.Prop
	PropPlayStatus      *propcore.Prop
	PropPauseReason     *propcore.Prop
	PropFPS             *propcore.Prop
	PropAVDiff          *propcore.Prop
	PropAVDiffError     *propcore.Prop
	PropAVDelta         *propcore.Prop
	PropSVDelta         *propcore.Prop
	PropShuffle         *propcore.Prop
	PropRepeat          *propcore.Prop
	PropCanSkipBackward *propcore.Prop
	PropCanSkipForward  *propcore.Prop
	PropCanSeek         *propcore.Prop
	PropCanPause        *propcore.Prop
	PropCanEject        *propcore.Prop
	PropCanShuffle      *propcore.Prop
	PropCanRepeat       *propcore.Prop
	PropBufferCurrent   *propcore.Prop
	PropBufferLimit     *propcore.Prop
	PropBufferDelay     *propcore.Prop
	PropModel           *propcore.Prop
	PropNotifications   *propcore.Prop
	PropAudioTracks     *propcore.Prop
	PropSubtitleTracks  *propcore.Prop
	PropVideo           *propcore.Prop
	PropAudio           *propcore.Prop

	EventQueue        []*MediaEvent
	EventCond         *sync.Cond
	HandleEvent       func(mp *MediaPipe, opaque any, e *MediaEvent) int
	HandleEventOpaque any

	// C: mp_video_frame_opaque / mp_video_frame_deliver /
	// mp_set_video_codec (media.h:226-228) and mp_seek_video_done
	// (media.h:354)
	VideoFrameOpaque  any
	VideoFrameDeliver func(fi *FrameInfo, opaque any) int
	SetVideoCodec     func(codecType FourCC, mc *MediaCodec, opaque any, fi *FrameInfo) int
	SeekVideoDone     func(mp *MediaPipe)

	// C: mp_vdpau_dev (media.h:320) — removed with VDPAU itself.
	// C: mp_seek_audio_done (media.h) — fired by the audio thread on
	// MB_CTRL_FLUSH.
	SeekAudioDone func(mp *MediaPipe)

	// Additional fields for compatibility
	SeekBase        int64
	Eof             bool
	PropCurrentTime *propcore.Prop
	PropIO          *propcore.Prop
	PropMetadata    *propcore.Prop
	// C: mp_prop_url / mp_prop_metadata_source (media.h:276,284)
	PropURL            *propcore.Prop
	PropMetadataSource *propcore.Prop
	AutoStandby        int // C: mp_auto_standby
	Framerate          AVRational

	// C: mp_reset_time / mp_reset_epoch (media.h:380-381) — timestamp
	// and epoch at which playback was (re)started; hls.c sets them
	// from video_args.load_request_timestamp / mp_epoch.
	ResetTime  int64
	ResetEpoch int64

	// C: mp_cancellable (media.h:369) — cancelled on stop/skip/eject so
	// in-flight fileaccess work aborts.
	Cancellable *misc.Cancellable

	// C: mp_seek_initiate (media.h:352) — backend hook fired by
	// mp_direct_seek before queue trimming.
	SeekInitiate func(mp *MediaPipe)

	// C: mp_sub_currenttime (media.h:313) — subscription on
	// mp_prop_currenttime driving user seeks; excluded as skipme when
	// the decoder writes currenttime back.
	SubCurrentTime *propcore.Subscription

	// C: mp_sub_eventsink (media.h:314) — subscription on the pipe's
	// "media.eventSink" prop routing ext events to mp_enqueue_event.
	SubEventSink *propcore.Subscription

	// C: mp_vol_setting (media.h:345, setting_t *) — per-file volume setting.
	VolSetting *settingscore.Setting

	// C: mp_audio_decoder (media.h) — opaque here; audio_decoder_create
	// lives above mediacore in the import DAG (pkg/media/libav), so the
	// field is any driven by AudioDecoderCreate/DestroyHook.
	AudioDecoder any

	// C: mp_clock_mutex (media.h) — guards clock state used by
	// mp_clock_*.
	ClockMutex sync.Mutex
	// C: mp_audio_clock_avtime / mp_audio_clock / mp_audio_clock_epoch
	// (media.h) — written by the audio driver's deliver path under
	// ClockMutex (alsa_audio_deliver / pulseaudio_audio_deliver).
	AudioClockAvtime int64
	AudioClock       int64
	AudioClockEpoch  int64
	// C: mp_realtime_delta (media.h:237) — audio_clock_avtime -
	// audio_clock, set by the android audio player callback and read
	// by the SURF MediaCodec deliver path (android_video_codec.c:548).
	RealtimeDelta int64
	// C: mp_clock_setup (media.h) — backend clock-setup hook invoked by
	// mp_configure.
	ClockSetup func(mp *MediaPipe, haveAudio bool)

	// C: mp_extra (media.h:350) — platform clock state created by
	// media_pipe_init_extra (e.g. omx_clk_t on RPi).
	Extra any

	// C: mp_hold_changed (media.h:355) — backend hook fired at the end
	// of mp_set_playstatus_by_hold_locked.
	HoldChanged func(mp *MediaPipe)
}

// MediaPipeSetupExtra / MediaPipeFiniExtra — C: media_pipe_init_extra /
// media_pipe_fini_extra — per-instance fields on MediaSystem
// (lifecycle.go); AudioDecoderCreate/Destroy likewise.

// MediaQueue represents a media queue
type MediaQueue struct {
	MP             *MediaPipe
	DataQueue      []*MediaBuf // C: mq_q_data
	CtrlQueue      []*MediaBuf // C: mq_q_ctrl
	AuxQueue       []*MediaBuf // C: mq_q_aux
	PacketsCurrent int
	PropDecodeAvg  *propcore.Prop
	PropDecodePeak *propcore.Prop
	PropTooSlow    *propcore.Prop
	PropBitrate    *propcore.Prop // C: mq_prop_bitrate
	PropCodec      *propcore.Prop // C: mq_prop_codec
	Stream         int
	Stream2        int
	SeekTarget     int64

	Avail          *sync.Cond     // C: mq_avail
	LastDeqDTS     int64          // C: mq_last_deq_dts
	BufferDelay    int64          // C: mq_buffer_delay
	NoDataInterest bool           // C: mq_no_data_interest
	DemuxerFlags   int            // C: mq_demuxer_flags
	PropQlenCur    *propcore.Prop // C: mq_prop_qlen_cur
	PropQlenMax    *propcore.Prop // C: mq_prop_qlen_max
	PropUploadAvg  *propcore.Prop // C: mq_prop_upload_avg
	PropUploadPeak *propcore.Prop // C: mq_prop_upload_peak

	// Copies to avoid updating codec user facing info too often
	MetaCodecID       int                   // C: mq_meta_codec_id
	MetaProfile       int                   // C: mq_meta_profile
	MetaChannels      int                   // C: mq_meta_channels
	MetaChannelLayout uint64                // C: mq_meta_channel_layout
	MetaWidth         int                   // C: mq_meta_width
	MetaHeight        int                   // C: mq_meta_height
	DemuxDebug        MediaDiscontinuityAux // C: mq_demux_debug
}

// MediaDiscontinuityAux represents discontinuity auxiliary data
type MediaDiscontinuityAux struct {
	Epoch int64
	DTS   int64
	PTS   int64
	Skip  int
}

// MediaBufFlags represents media buffer flags
type MediaBufFlags struct {
	Skip          bool
	Discontinuity bool
	EOF           bool
	Keyframe      bool
	Flush         bool
	DriveClock    int
}

// MediaBuf represents a media buffer
type MediaBuf struct {
	Data        []byte
	Size        int
	PTS         int64
	DTS         int64
	Duration    int64
	DataType    int
	Flags       MediaBufFlags
	Sequence    int
	FrameInfo   *FrameInfo
	Dtor        func(*MediaBuf)
	Meta        *MediaBufMeta
	Data32      int32
	UserTime    int64
	Epoch       int
	Codec       *MediaCodec // C: mb_cw
	CodecID     CodecID
	Stream      int
	FontContext any
	DriveClock  int
	Priority    int

	// C union members: mb_data32 (above), mb_rate, mb_float, mb_prop,
	// mb_font_context, mb_frame_info (above)
	Rate       int            // C: mb_rate
	Float      float32        // C: mb_float
	Prop       *propcore.Prop // C: mb_prop
	DataOpaque any            // C: mb_data as void* (mp_send_cmd_data)
	Channels   uint8          // C: mb_channels

	// C mb_flags: mb_aspect_override, mb_disable_deinterlacer
	AspectOverride      int  // C: mb_aspect_override
	DisableDeinterlacer bool // C: mb_disable_deinterlacer

	// Pkt — C: mb_pkt (embedded AVPacket). Set by MediaBufFromAVPkt via
	// av_packet_ref; released by mediaBufDtorAVPacket via
	// av_packet_unref. Holds a C-allocated AVPacket*.
	Pkt *libav.AVPacket
}

// EnqueueWithEvents enqueues the buffer with events
// C: mb_enqueue_with_events (media_queue.c:203)
func (mb *MediaBuf) EnqueueWithEvents(mp *MediaPipe, mq *MediaQueue) any {
	if mb == nil || mq == nil {
		return nil
	}
	return MbEnqueueWithEvents(mp, mq, mb)
}

// MediaBufMeta represents media buffer metadata
type MediaBufMeta struct {
	UserTime            int64
	PTS                 int64
	DTS                 int64
	Epoch               int
	Duration            int64
	Sequence            int
	Flags               MediaBufFlags
	AspectOverride      int
	DisableDeinterlacer bool
	DriveClock          int
}

// MediaFormat represents media format
type MediaFormat struct {
	RefCount int32
	FCtx     *libav.AVFormatContext // C: fctx — struct AVFormatContext *
}

// MediaCodec represents a media codec
// C: media_codec_t (media_codec.h:25-56)
type MediaCodec struct {
	Ctx           *libav.AVCodecContext // C: ctx — AVCodecContext owned by decoder thread
	FmtCtx        *libav.AVCodecContext // C: fmt_ctx — AVCodecContext owned by AVFormatContext
	ParserCtx     *libav.AvParser       // C: parser_ctx — AVCodecParserContext
	Opaque        any
	CodecID       CodecID
	MediaType     MediaType
	ExtraData     []byte
	ExtraDataSize int
	Format        *MediaFormat // C: fw
	Params        *MediaCodecParams
	MP            *MediaPipe // C: mc->mp
	RefCount      int32      // C: refcount (atomic)

	// C: function pointers on media_codec_t. vd is passed as any
	// because video_decoder_t lives in pkg/video/decoder which imports
	// this package — the parameter mirrors C's `struct video_decoder *vd`.
	Decode       func(mc *MediaCodec, vd any, mq *MediaQueue, mb *MediaBuf, reqsize int)
	DecodeLocked func(mc *MediaCodec, vd any, mq *MediaQueue, mb *MediaBuf) int
	Flush        func(mc *MediaCodec, vd any)
	Close        func(mc *MediaCodec)
	Restart      func(mc *MediaCodec)
	Reconfigure  func(mc *MediaCodec, fi *FrameInfo)

	Release func(*MediaCodec) error
	SARNum  int
	SARDen  int

	// Claimed — set when a registered codec's open() returned 0 in
	// MediaCodecCreate (C: the iteration break). Go callers that fall
	// back to the explicit lavc open check this flag.
	Claimed bool

	// DRM — Go extension, no C field. *drmpipe.Decryptor, lazily created
	// on the first cenc-encrypted packet seen in the decode path.
	// any keeps pkg/media/core free of the drm dependency.
	DRM any
}

// MediaCodecParams represents media codec parameters
type MediaCodecParams struct {
	CodecID            CodecID
	MediaType          MediaType
	Width              int
	Height             int
	SampleRate         int
	Channels           int
	BitRate            int
	ExtraData          []byte
	ExtraDataSize      int
	ExtradataSize      int // Alias for compatibility
	Format             *MediaFormat
	Profile            int
	Level              int
	CheatForSpeed      bool
	BrokenAudPlacement bool
	SARNum             int
	SARDen             int
	FrameRateNum       int // C: mcp.frame_rate_num
	FrameRateDen       int // C: mcp.frame_rate_den
}

// MediaEvent represents a media event
type MediaEvent struct {
	Type int
	Data any
	Next *MediaEvent
}

// MediaEventCallback represents a media event callback
type MediaEventCallback func(*MediaEvent)

// MediaSettings represents media settings
type MediaSettings struct {
	Deinterlace   bool
	Zoom          float64
	AspectRatio   float64
	Brightness    float64
	Contrast      float64
	Saturation    float64
	AudioVolume   float64
	AudioDelay    int64
	SubtitleDelay int64
}

// MediaTrack represents a media track
type MediaTrack struct {
	Enabled   bool
	Language  string
	Title     string
	Codec     *MediaCodec
	AudioInfo *AudioInfo
	VideoInfo *VideoInfo
	ExtraData []byte
}

// AudioInfo represents audio information
type AudioInfo struct {
	SampleRate int
	Channels   int
	BitRate    int
}

// VideoInfo represents video information
type VideoInfo struct {
	Width     int
	Height    int
	FrameRate AVRational
	BitRate   int
}

// MediaTrackList represents a list of media tracks
type MediaTrackList struct {
	Tracks []*MediaTrack
}

// MediaBufDataType represents different buffer data types
type MediaBufDataType int

// MediaBufPool represents a pool of media buffers
type MediaBufPool struct {
	Buffers []*MediaBuf
	Mutex   sync.Mutex
	Size    int
}

// MediaEventQueue represents a queue of media events
type MediaEventQueue struct {
	Head  *MediaEvent
	Tail  *MediaEvent
	Size  int
	Mutex sync.Mutex
}

// Flush flushes the media pipe
// C: mp_flush (media_queue.c:104)
func (mp *MediaPipe) Flush() error {
	MpFlush(mp)
	return nil
}

// Configure configures the media pipe
// C: mp_configure (media.c:729) — delegates to MpConfigure for full
// C-canonical behavior (flags, buffer_size, duration, type).
func (mp *MediaPipe) Configure(flags MediaPipeFlags, bufferSize int, duration int64, typeStr string) error {
	MpConfigure(mp, nil, flags, bufferSize, duration, typeStr)
	return nil
}

// BecomePrimary makes the media pipe primary
// C: mp_become_primary (media.c:500) — delegates to MpBecomePrimary.
func (mp *MediaPipe) BecomePrimary() error {
	MpBecomePrimary(mp, nil)
	return nil
}

// WaitForEmptyQueues waits for queues to empty
// C: mp_wait_for_empty_queues (media_queue.c:386)
func (mp *MediaPipe) WaitForEmptyQueues() any {
	if e := MpWaitForEmptyQueues(mp); e != nil {
		return e
	}
	return nil
}

// MediaBufFree frees a media buffer (caller must hold mp.Mutex)
// C: media_buf_free_locked (media_buf.c:96)
func MediaBufFree(mp *MediaPipe, mb *MediaBuf) {
	MediaBufFreeLocked(mp, mb)
}

// AVPacketFields mirrors the packet fields consumed by MediaBufFromAVPkt.
// It has the same field layout as libav.AVPacketInfo so callers can convert
// with a plain pointer cast (core cannot import libav — import cycle).
// C: AVPacket
type AVPacketFields struct {
	StreamIndex int
	PTS         int64
	DTS         int64
	Duration    int64
	Size        int
	Flags       int
	Data        []byte
}

// MediaBufFromAVPkt creates a media buffer from an AV packet.
// C: media_buf_from_avpkt_unlocked (media_buf.c:70-83) — pool_gets a
// buffer, sets the avpacket dtor, and av_packet_ref's the packet into
// mb->mb_pkt. Timestamp/duration rescaling is the CALLER's job
// (fa_video.c / fa_audio.c set mb->mb_pts/dts/duration afterwards).
// Accepts unsafe.Pointer (C AVPacket*), *libav.AVPacket, or the
// synthetic *AVPacketFields (test path).
func MediaBufFromAVPkt(mp *MediaPipe, pkt any) *MediaBuf {
	if pkt == nil {
		return nil
	}
	var mb *MediaBuf
	if mp != nil {
		mp.Mutex.Lock()
		mb = mp.MbPool.poolGet()
		mp.Mutex.Unlock()
	}
	if mb == nil {
		mb = &MediaBuf{}
	}

	// C: mb->mb_dtor = media_buf_dtor_avpacket
	mb.Dtor = mediaBufDtorAVPacket

	var src *libav.AVPacket
	switch p := pkt.(type) {
	case unsafe.Pointer:
		src = libav.WrapAVPacket(p)
	case *libav.AVPacket:
		src = p
	case *AVPacketFields:
		// Synthetic packet (test path) — no C packet to ref; carry the
		// fields on the buffer directly.
		mb.Data = p.Data
		mb.Size = p.Size
		mb.PTS = p.PTS
		mb.DTS = p.DTS
		mb.Duration = p.Duration
		mb.Stream = p.StreamIndex
		return mb
	default:
		return mb
	}

	// C: av_packet_ref(&mb->mb_pkt, pkt)
	if src != nil {
		p := libav.AvPacketAlloc()
		if p != nil && libav.AvPacketRefPtr(p.CPtr(), src.CPtr()) != 0 {
			libav.AvPacketFree(p)
			p = nil
		}
		mb.Pkt = p
	}
	return mb
}

// MediaCodecRef increments the reference count of a media codec and returns it
// C: media_codec_ref (media_codec.c:30) — atomic_inc(&cw->refcount)
func MediaCodecRef(mc *MediaCodec) *MediaCodec {
	if mc == nil {
		return nil
	}
	atomic.AddInt32(&mc.RefCount, 1)
	return mc
}

// MediaCodecDeref decrements the reference count of a media codec
// C: media_codec_deref (media_codec.c:38-69) — on last ref: close codec
// contexts, call cw->close, close parser, deref format, free.
func MediaCodecDeref(mc *MediaCodec) {
	if mc == nil {
		return
	}
	if atomic.AddInt32(&mc.RefCount, -1) != 0 {
		return
	}
	// C: avcodec_close(cw->ctx) / avcodec_close(cw->fmt_ctx) — the codec
	// open callback's ctx is freed via AvcodecFreeContext; callers that
	// own fmt_ctx elsewhere (fw != NULL) skip freeing it.
	if mc.Ctx != nil {
		libav.AvcodecFreeContext(mc.Ctx)
		mc.Ctx = nil
	}
	if mc.FmtCtx != nil && mc.Format == nil {
		libav.AvcodecFreeContext(mc.FmtCtx)
		mc.FmtCtx = nil
	}
	if mc.Close != nil {
		mc.Close(mc)
	}
	if mc.ParserCtx != nil {
		mc.ParserCtx.Close()
		mc.ParserCtx = nil
	}
	if mc.Format != nil {
		MediaFormatDeref(mc.Format)
	}
}

// MediaFormatDeref decrements the reference count of a media format
// C: media_format_deref (libav.c:476) — on last ref calls
// fa_libav_close_format(fw->fctx). Here the AVFormatContext is owned by
// the libav.AVFormatCtx wrapper (p.format), whose Close() performs
// avformat_close_input — closing it again on deref would double-free.
func MediaFormatDeref(mf *MediaFormat) {
	if mf == nil {
		return
	}
	mf.RefCount--
	if mf.RefCount <= 0 {
		mf.FCtx = nil
	}
}

// MediaFormatCreate creates a media format from an AV format context
// C: media_format_create — stores fctx for the codec layer.
func MediaFormatCreate(fctx *libav.AVFormatContext) *MediaFormat {
	if fctx == nil {
		return nil
	}
	return &MediaFormat{
		RefCount: 1,
		FCtx:     fctx,
	}
}

// CodecDef — C: codec_def_t (media_codec.h:91-97). REGISTER_CODEC sites
// register via MediaRegisterCodec; media_codec_create iterates the list
// in ascending prio order and the first open() returning 0 claims the
// codec ("Higher value of prio == better preference" — lavc registers
// last at 1000 as the fallback).
type CodecDef struct {
	Start func()
	Open  func(mc *MediaCodec, mcp *MediaCodecParams, mp *MediaPipe) int
	Prio  int
}

// registeredCodecs — C: static LIST_HEAD registeredcodecs
// (media_codec.c:22). Ascending-prio sorted, guarded by codecsMutex.
var (
	registeredCodecs []*CodecDef
	codecsMutex      sync.Mutex
)

// codecDefCmp — C: codec_def_cmp (media_codec.c:151)
func codecDefCmp(a, b *CodecDef) int {
	return a.Prio - b.Prio
}

// MediaRegisterCodec — C: media_register_codec (media_codec.c:159) —
// LIST_INSERT_SORTED by ascending prio.
func MediaRegisterCodec(cd *CodecDef) {
	codecsMutex.Lock()
	defer codecsMutex.Unlock()
	i := 0
	for i < len(registeredCodecs) && codecDefCmp(registeredCodecs[i], cd) <= 0 {
		i++
	}
	registeredCodecs = append(registeredCodecs, nil)
	copy(registeredCodecs[i+1:], registeredCodecs[i:])
	registeredCodecs[i] = cd
}

// MediaCodecStart — C: media_codec_init (media_codec.c:133) — calls each
// registered codec's init() once (from media_init).
func MediaCodecStart() {
	codecsMutex.Lock()
	defer codecsMutex.Unlock()
	for _, cd := range registeredCodecs {
		if cd.Start != nil {
			cd.Start()
		}
	}
}

// MediaCodecCreate creates a media codec.
// C: media_codec_create (media_codec.c:74-127) — allocates mc, iterates
// registeredcodecs calling cd->open(mc, mcp, mp) until one returns 0
// (claims the codec), then sets up the parser ctx when requested and
// retains the format.
//
// Go seam: the C signature's `fw` is `format` and `ctx` is `opaque`
// (mc->fmt_ctx). When no registered codec claims the codec, C frees mc
// and returns NULL — Go does the same. The lavc codec is registered at
// prio 1000 by pkg/media/libav's init() (C: REGISTER_CODEC in libav.c),
// so it claims whatever no earlier codec did when that package is linked.
func MediaCodecCreate(codecID CodecID, parser int, format *MediaFormat, opaque unsafe.Pointer, params *MediaCodecParams, mp *MediaPipe) *MediaCodec {
	mc := &MediaCodec{
		CodecID: codecID,
		MP:      mp,
		FmtCtx:  libav.WrapAVCodecContext(opaque),
		Params:  params,
	}

	if params != nil {
		// C: mc->sar_num = mcp->sar_num; mc->sar_den = mcp->sar_den
		mc.SARNum = params.SARNum
		mc.SARDen = params.SARDen
	}

	codecsMutex.Lock()
	claimed := false
	for _, cd := range registeredCodecs {
		if cd.Open == nil {
			continue
		}
		if cd.Open(mc, params, mp) == 0 {
			claimed = true
			break
		}
	}
	codecsMutex.Unlock()

	if !claimed {
		// C: free(mc); return NULL
		return nil
	}
	mc.Claimed = true

	if parser != 0 {
		// C: assert(fw == NULL); fmt_ctx = avcodec_alloc_context3(codec);
		//    parser_ctx = av_parser_init(codec_id)
		codec := libav.AvcodecFindDecoder(int(codecID))
		if codec != nil {
			c := libav.AvcodecAllocContext3(codec)
			if c != nil {
				mc.FmtCtx = c
			}
			mc.ParserCtx = libav.NewAvParser(int(codecID))
		}
	}

	atomic.StoreInt32(&mc.RefCount, 1)
	mc.Format = format
	if format != nil {
		// C: assert(!parser); atomic_inc(&fw->refcount)
		atomic.AddInt32(&format.RefCount, 1)
	}
	return mc
}
