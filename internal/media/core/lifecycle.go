package core

import (
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/czz/movian-go/internal/event"
	"github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
	videosettings "github.com/czz/movian-go/internal/video"
)

// mediaGlobals holds the global media pipe list and primary pointer.
// C: media_pipelines (LIST_HEAD), num_media_pipelines, media_primary (media.c)
// MediaSystem — the media.c subsystem instance. In C these fields are
// file-scope globals (media_mutex, media_pipelines, num_media_pipelines,
// media_primary, media_prop_root/sources/current, spk0, video_settings,
// media_buffer_hungry); the Go port groups them into one parametric
// system created by MediaStart. Every MediaPipe carries a Sys
// backpointer so mp_* functions keep their C signatures.
type MediaSystem struct {
	Mutex        sync.Mutex   // C: media_mutex
	pipelines    []*MediaPipe // C: media_pipelines
	numPipelines int          // C: num_media_pipelines
	primary      *MediaPipe   // C: media_primary

	// C: media_prop_root, media_prop_sources, media_prop_current
	// (media.c:55-57) — set by MediaStart.
	PropRoot    *propcore.Prop
	PropSources *propcore.Prop
	PropCurrent *propcore.Prop

	// BufferHungry — C: static atomic_t media_buffer_hungry (media.c) —
	// buffer-starvation counter read by the scanner.
	BufferHungry atomic.Int32

	// PropLockmgr — C: PROP_TAG_LOCKMGR, mp_lockmgr — the lockmgr
	// adapter for media-pipe prop subscriptions.
	PropLockmgr *propcore.Lockmgr

	// VS — C: the ambient `struct video_settings video_settings`
	// (video_settings.c:28). Never nil.
	VS *videosettings.VideoSettings

	// C: uint8_t HTS_JOIN(sp, k0)[321] (media.c:71) — byte 4 written in
	// media_init. Dead buffer retained for canonical completeness.
	spk0 [321]byte

	// Owner — the owning *backendcore.BackendSystem (opaque: mediacore
	// sits below backendcore in the import DAG). Set by
	// bs.SetMediaSystem.
	Owner any

	// PlayqueueEvent — C: playqueue_event_handler (media.c) hook,
	// called from the global eventsink. Per-instance (was MediaHooks).
	PlayqueueEvent func(e *event.Event)

	// MpSettings hooks — C: mp_settings_init / mp_settings_clear
	// (media_settings.c) — per-instance (were MediaHooks).
	MpSettingsSetup func(mp *MediaPipe, url, dirURL, parentTitle string)
	MpSettingsClear func(mp *MediaPipe)

	// AudioDecoder hooks — C: audio_decoder_create/destroy
	// (audio_decoder.c) — per-instance (were MediaHooks).
	AudioDecoderCreate  func(mp *MediaPipe) any
	AudioDecoderDestroy func(ad any)

	// EventDispatch — C: global event_dispatch() reached from
	// mp_enqueue_event_locked's master-volume path (media_event.c).
	// Per-instance (was MediaHooks; previously never wired).
	EventDispatch func(e *event.Event)

	// Subtitle/overlay hooks — C: subtitles_load / subtitles_destroy /
	// video_overlay_decode / sub_ass_render (ext_subtitles.c,
	// video_overlay.c) — per-instance. SubtitlesDestroy is captured by
	// extSubDtor at buffer creation (mb_data dtor has no mp back-pointer).
	SubtitlesLoad      func(mp *MediaPipe, url string, dcs misc.CharsetDefaultSrc) any
	SubtitlesDestroy   func(es any)
	VideoOverlayDecode func(mp *MediaPipe, mb *MediaBuf)
	SubAssRender       func(mp *MediaPipe, src string, header []byte, fontdomain int)

	// LangScorer — C: global i18n state (i18n.c) reached from track
	// scoring and subtitle load. Per-instance (was MediaHooks).
	LangScorer LangScorer

	// MediaPipeSetupExtra/MediaPipeFiniExtra — C: media_pipe_init_extra
	// / media_pipe_fini_extra (media.c:289-290, 386-387) — platform
	// seam: arch/rpi's OmxStart registers these on the instance handed
	// by the composition root (before/around first MpCreate).
	MediaPipeSetupExtra func(mp *MediaPipe)
	MediaPipeFiniExtra  func(mp *MediaPipe)
}

// MediaHooks — Go seam: all cross-package dependency hooks for
// mediacore (subtitles, settings, video overlay, playqueue, event
// dispatch, audio decoder, pipe extras) now live as per-instance fields
// on MediaSystem — the global facade is gone (Fase 6.3 complete).

// MediaHooks.PlayqueueEvent — C: playqueue_event_handler called from
// media_global_eventsink. Wired by init (playqueue imports mediacore;
// the reverse would create an import cycle).

// SetVideoSettings — publishes the video settings instance (init-time).
// A nil argument restores a zero-value instance.
func (ms *MediaSystem) SetVideoSettings(vs *videosettings.VideoSettings) {
	if vs == nil {
		vs = &videosettings.VideoSettings{}
	}
	ms.VS = vs
}

// NewMediaSystem creates a MediaSystem with zero-value C defaults
// (no prop tree — equivalent to a media.c before media_init).
func NewMediaSystem() *MediaSystem {
	return &MediaSystem{
		PropLockmgr: &propcore.Lockmgr{Fn: func(ptr any, op propcore.LockmgrOp) int {
			return MpLockmgr(ptr, int(op))
		}},
		VS: &videosettings.VideoSettings{},
	}
}

// MediaStart — C: media_init (media.c:77-94) — codec init, media prop
// root (media/sources/current), and the global eventsink subscription.
func MediaStart(pm *propcore.PropManager) *MediaSystem {
	ms := NewMediaSystem()
	if pm == nil {
		return ms
	}
	// C: media_codec_init()
	MediaCodecStart()

	// C: hts_mutex_init(&media_mutex) — zero-valued sync.Mutex.

	global := pm.GetGlobal()
	ms.PropRoot = pm.CreateEx(global, "media", nil, false, true)
	ms.PropSources = pm.CreateEx(ms.PropRoot, "sources", nil, false, true)
	ms.PropCurrent = pm.CreateEx(ms.PropRoot, "current", nil, false, true)
	ms.spk0[4] = 0x78 // C: HTS_JOIN(sp, k0)[4] = 0x78

	// C: prop_subscribe(0, PROP_TAG_NAME("media", "eventSink"),
	//   PROP_TAG_CALLBACK, media_global_eventsink, NULL,
	//   PROP_TAG_MUTEX, &media_mutex, PROP_TAG_ROOT, media_prop_root)
	sink := pm.CreateEx(ms.PropRoot, "eventSink", nil, false, false)
	if sink != nil {
		sink.Subscribe(ms.globalEventsink, nil,
			propcore.SubMutex{Ptr: &ms.Mutex})
	}
	return ms
}

// mediaGlobalEventsink — C: media_global_eventsink (media.c:801-833) —
// routes EVENT_PLAYTRACK to the playqueue; other events go to the
// primary pipeline's event queue (or the playqueue when none).
func (ms *MediaSystem) globalEventsink(_ any, ev propcore.EventType, args ...any) {
	if ev != propcore.EventExtEvent || len(args) == 0 {
		return
	}
	e, ok := args[0].(*event.Event)
	if !ok || e == nil {
		return
	}

	// C: runs under media_mutex (PROP_TAG_MUTEX) — held by the dispatcher.
	primary := ms.primary

	if e.Type == event.EVENT_PLAYTRACK {
		// C: playqueue_event_handler(e)
		if ms.PlayqueueEvent != nil {
			ms.PlayqueueEvent(e)
		}
	} else if primary != nil {
		// C: mp_enqueue_event(media_primary, e)
		MpEnqueueEvent(primary, &MediaEvent{Type: int(e.Type), Data: e.Concrete()})
	} else if ms.PlayqueueEvent != nil {
		// C: playqueue_event_handler(e)
		ms.PlayqueueEvent(e)
	}
}

// MpCreate creates a new media pipe.
// C: mp_create (media.c:101-293) — allocates, initializes, registers globally,
// creates all property nodes, sets up queues and track managers.
func MpCreate(ms *MediaSystem, pm *propcore.PropManager, name string, flags MediaPipeFlags) *MediaPipe {
	mp := &MediaPipe{
		Name:        name,
		Flags:       flags,
		RefCount:    1,               // C: atomic_set(&mp->mp_refcount, 1)
		Epoch:       1,               // C: mp->mp_epoch = 1
		Satisfied:   -1,              // C: mp->mp_satisfied = -1
		BufferLimit: 1 * 1024 * 1024, // C: mp->mp_buffer_limit = 1 * 1024 * 1024
		VolUI:       1.0,             // C: mp->mp_vol_ui = 1.0f
		// C: mp->mp_mb_pool = pool_create("packet headers", sizeof(media_buf_t), POOL_ZERO_MEM)
		MbPool: &MediaBufPool{},
	}

	// C: hts_cond_init(&mp->mp_backpressure, &mp->mp_mutex)
	mp.Backpressure = sync.NewCond(&mp.Mutex)

	// C: mp->mp_cancellable = cancellable_create() (media.c:107)
	mp.Cancellable = misc.CancellableCreate()

	// C: hts_mutex_init(&mp->mp_clock_mutex) — zero-valued sync.Mutex.

	// C: LIST_INSERT_HEAD(&media_pipelines, mp, mp_global_link)
	mp.Sys = ms
	ms.Mutex.Lock()
	ms.pipelines = append(ms.pipelines, mp)
	ms.numPipelines++
	ms.Mutex.Unlock()

	// C: TAILQ_INIT(&mp->mp_eq)
	mp.EventQueue = make([]*MediaEvent, 0)
	mp.EventCond = sync.NewCond(&mp.Mutex)

	// Initialize media queues
	mp.Video = &MediaQueue{MP: mp}
	mp.Audio = &MediaQueue{MP: mp}
	mp.VideoQueue = mp.Video
	mp.AudioQueue = mp.Audio
	// C: mq_init sets up mq_avail etc. — must run even without a
	// prop manager (PropVideo/PropAudio nil → MqSetup skips prop children).
	MqSetup(mp.Video, nil, mp)
	MqSetup(mp.Audio, nil, mp)

	// Create property tree
	if pm != nil {
		// C: mp->mp_prop_root = prop_create(media_prop_sources, NULL)
		// — anonymous child of media_prop_sources (the name is a debug
		// name on the mp struct, not the prop).
		parent := ms.PropSources
		if parent == nil {
			parent = pm.GetGlobal() // MediaStart not run (tests)
		}
		mp.PropRoot = pm.CreateEx(parent, "", nil, false, true)

		// C: mp->mp_prop_metadata = prop_create(mp->mp_prop_root, "metadata")
		mp.PropMetadata = pm.CreateEx(mp.PropRoot, "metadata", nil, false, true)
		// C: mp->mp_prop_primary = prop_create(mp->mp_prop_root, "primary")
		mp.PropPrimary = pm.CreateEx(mp.PropRoot, "primary", nil, false, true)
		// C: mp->mp_prop_io = prop_create(mp->mp_prop_root, "io")
		mp.PropIO = pm.CreateEx(mp.PropRoot, "io", nil, false, true)
		// C: mp->mp_prop_notifications = prop_create(mp->mp_prop_root, "notifications")
		mp.PropNotifications = pm.CreateEx(mp.PropRoot, "notifications", nil, false, true)
		// C: mp->mp_prop_url = prop_create(mp->mp_prop_root, "url")
		mp.PropURL = pm.CreateEx(mp.PropRoot, "url", nil, false, true)

		// C: mp->mp_setting_root = prop_create(mp->mp_prop_root, "settings")
		mp.SettingRoot = pm.CreateEx(mp.PropRoot, "settings", nil, false, true)

		// Video props
		// C: mp->mp_prop_video = prop_create(mp->mp_prop_root, "video")
		mp.PropVideo = pm.CreateEx(mp.PropRoot, "video", nil, false, true)
		// C: mq_init(&mp->mp_video, mp->mp_prop_video, &mp->mp_mutex, mp)
		// — attach queue props now that PropVideo exists (Avail already set).
		MqSetup(mp.Video, mp.PropVideo, mp)
		mp.SettingVideoRoot = pm.CreateEx(mp.PropVideo, "settings", nil, false, true)

		// Audio props
		// C: mp->mp_prop_audio = prop_create(mp->mp_prop_root, "audio")
		mp.PropAudio = pm.CreateEx(mp.PropRoot, "audio", nil, false, true)
		// C: mq_init(&mp->mp_audio, mp->mp_prop_audio, &mp->mp_mutex, mp)
		MqSetup(mp.Audio, mp.PropAudio, mp)
		mp.SettingAudioRoot = pm.CreateEx(mp.PropAudio, "settings", nil, false, true)
		mp.PropAudioTrackCurrent = pm.CreateEx(mp.PropAudio, "current", nil, false, true)
		mp.PropAudioTrackCurrentManual = pm.CreateEx(mp.PropAudio, "manual", nil, false, true)
		mp.PropAudioTracks = pm.CreateEx(mp.PropMetadata, "audiostreams", nil, false, true)
		// C: prop_linkselected_create(mp->mp_prop_audio_tracks,
		//                           mp->mp_prop_audio, "active", NULL)
		pm.LinkselectedCreate(mp.PropAudioTracks, mp.PropAudio, "active", "")
		// C: prop_set_string(mp->mp_prop_audio_track_current, "audio:off")
		pm.SetStringEx(mp.PropAudioTrackCurrent, nil, "audio:off", propcore.StringUTF8)

		// C: mp_track_mgr_init(mp, &mp->mp_audio_track_mgr,
		//     mp->mp_prop_audio_tracks, MEDIA_TRACK_MANAGER_AUDIO,
		//     mp->mp_prop_audio_track_current,
		//     prop_create(mp->mp_prop_audio, "sorted"))
		mp.AudioTrackMgr = &MediaTrackMgr{}
		MpTrackMgrSetup(mp, mp.AudioTrackMgr, mp.PropAudioTracks,
			MediaTrackManagerAudio, mp.PropAudioTrackCurrent,
			pm.CreateEx(mp.PropAudio, "sorted", nil, false, true))

		// Subtitle props
		// C: p = prop_create(mp->mp_prop_root, "subtitle")
		mp.PropSubtitle = pm.CreateEx(mp.PropRoot, "subtitle", nil, false, true)
		mp.SettingSubtitleRoot = pm.CreateEx(mp.PropSubtitle, "settings", nil, false, true)
		mp.PropSubtitleTrackCurrent = pm.CreateEx(mp.PropSubtitle, "current", nil, false, true)
		mp.PropSubtitleTrackCurrentManual = pm.CreateEx(mp.PropSubtitle, "manual", nil, false, true)
		mp.PropSubtitleTracks = pm.CreateEx(mp.PropMetadata, "subtitlestreams", nil, false, true)
		// C: prop_linkselected_create(mp->mp_prop_subtitle_tracks, p, "active", NULL)
		pm.LinkselectedCreate(mp.PropSubtitleTracks, mp.PropSubtitle, "active", "")
		// C: prop_set_string(mp->mp_prop_subtitle_track_current, "sub:off")
		pm.SetStringEx(mp.PropSubtitleTrackCurrent, nil, "sub:off", propcore.StringUTF8)
		// C: mp_add_track_off(mp->mp_prop_subtitle_tracks, "sub:off")
		MpAddTrackOff(pm, mp.PropSubtitleTracks, "sub:off")

		// C: mp_track_mgr_init(mp, &mp->mp_subtitle_track_mgr,
		//     mp->mp_prop_subtitle_tracks, MEDIA_TRACK_MANAGER_SUBTITLES,
		//     mp->mp_prop_subtitle_track_current, prop_create(p, "sorted"))
		mp.SubtitleTrackMgr = &MediaTrackMgr{}
		MpTrackMgrSetup(mp, mp.SubtitleTrackMgr, mp.PropSubtitleTracks,
			MediaTrackManagerSubtitles, mp.PropSubtitleTrackCurrent,
			pm.CreateEx(mp.PropSubtitle, "sorted", nil, false, true))

		// Buffer props
		// C: p = prop_create(mp->mp_prop_root, "buffer")
		bufferProp := pm.CreateEx(mp.PropRoot, "buffer", nil, false, true)
		mp.PropBufferCurrent = pm.CreateEx(bufferProp, "current", nil, false, true)
		pm.SetIntEx(mp.PropBufferCurrent, nil, 0)
		mp.PropBufferLimit = pm.CreateEx(bufferProp, "limit", nil, false, true)
		pm.SetIntEx(mp.PropBufferLimit, nil, mp.BufferLimit)
		mp.PropBufferDelay = pm.CreateEx(bufferProp, "delay", nil, false, true)

		// Playstatus props
		// C: mp->mp_prop_playstatus = prop_create(mp->mp_prop_root, "playstatus")
		mp.PropPlayStatus = pm.CreateEx(mp.PropRoot, "playstatus", nil, false, true)
		// C: mp->mp_prop_pausereason = prop_create(mp->mp_prop_root, "pausereason")
		mp.PropPauseReason = pm.CreateEx(mp.PropRoot, "pausereason", nil, false, true)
		// C: mp->mp_prop_currenttime = prop_create(mp->mp_prop_root, "currenttime")
		mp.PropCurrentTime = pm.CreateEx(mp.PropRoot, "currenttime", nil, false, true)
		// C: prop_set_float_clipping_range(mp->mp_prop_currenttime, 0, 10e6)
		// C: mp->mp_prop_fps = prop_create(mp->mp_prop_root, "fps")
		mp.PropFPS = pm.CreateEx(mp.PropRoot, "fps", nil, false, true)

		// C: mp->mp_prop_avdelta = prop_create(mp->mp_prop_root, "avdelta")
		mp.PropAVDelta = pm.CreateEx(mp.PropRoot, "avdelta", nil, false, true)
		pm.SetFloatEx(mp.PropAVDelta, nil, 0)
		// C: mp->mp_prop_svdelta = prop_create(mp->mp_prop_root, "svdelta")
		mp.PropSVDelta = pm.CreateEx(mp.PropRoot, "svdelta", nil, false, true)
		pm.SetFloatEx(mp.PropSVDelta, nil, 0)

		// C: mp->mp_prop_shuffle = prop_create(mp->mp_prop_root, "shuffle")
		mp.PropShuffle = pm.CreateEx(mp.PropRoot, "shuffle", nil, false, true)
		pm.SetIntEx(mp.PropShuffle, nil, 0)
		// C: mp->mp_prop_repeat = prop_create(mp->mp_prop_root, "repeat")
		mp.PropRepeat = pm.CreateEx(mp.PropRoot, "repeat", nil, false, true)
		pm.SetIntEx(mp.PropRepeat, nil, 0)

		// C: mp->mp_prop_avdiff = prop_create(mp->mp_prop_root, "avdiff")
		mp.PropAVDiff = pm.CreateEx(mp.PropRoot, "avdiff", nil, false, true)
		// C: mp->mp_prop_avdiff_error = prop_create(mp->mp_prop_root, "avdiffError")
		mp.PropAVDiffError = pm.CreateEx(mp.PropRoot, "avdiffError", nil, false, true)

		// Capability props
		// C: mp->mp_prop_canSkipBackward = prop_create(mp->mp_prop_root, "canSkipBackward")
		mp.PropCanSkipBackward = pm.CreateEx(mp.PropRoot, "canSkipBackward", nil, false, true)
		// C: mp->mp_prop_canSkipForward = prop_create(mp->mp_prop_root, "canSkipForward")
		mp.PropCanSkipForward = pm.CreateEx(mp.PropRoot, "canSkipForward", nil, false, true)
		// C: mp->mp_prop_canSeek = prop_create(mp->mp_prop_root, "canSeek")
		mp.PropCanSeek = pm.CreateEx(mp.PropRoot, "canSeek", nil, false, true)
		// C: mp->mp_prop_canPause = prop_create(mp->mp_prop_root, "canPause")
		mp.PropCanPause = pm.CreateEx(mp.PropRoot, "canPause", nil, false, true)
		// C: mp->mp_prop_canEject = prop_create(mp->mp_prop_root, "canEject")
		mp.PropCanEject = pm.CreateEx(mp.PropRoot, "canEject", nil, false, true)
		// C: mp->mp_prop_canShuffle = prop_create(mp->mp_prop_root, "canShuffle")
		mp.PropCanShuffle = pm.CreateEx(mp.PropRoot, "canShuffle", nil, false, true)
		// C: mp->mp_prop_canRepeat = prop_create(mp->mp_prop_root, "canRepeat")
		mp.PropCanRepeat = pm.CreateEx(mp.PropRoot, "canRepeat", nil, false, true)
		// C: prop_set_int(prop_create(mp->mp_prop_root, "canStop"), 1)
		pm.SetIntEx(pm.CreateEx(mp.PropRoot, "canStop", nil, false, true), nil, 1)

		// C: mp->mp_prop_ctrl = prop_create(mp->mp_prop_root, "ctrl")
		mp.PropCtrl = pm.CreateEx(mp.PropRoot, "ctrl", nil, false, true)
		// C: mp->mp_prop_model = prop_create(mp->mp_prop_root, "model")
		mp.PropModel = pm.CreateEx(mp.PropRoot, "model", nil, false, true)

		// C: mp->mp_sub_currenttime = prop_subscribe(
		//   PROP_SUB_NO_INITIAL_UPDATE,
		//   PROP_TAG_CALLBACK, mp_seek_by_propchange, mp,
		//   PROP_TAG_ROOT, mp->mp_prop_currenttime, NULL) (media.c:271)
		// User-driven currenttime changes become direct seeks; the sub is
		// passed as skipme when the decoder writes currenttime back.
		if mp.PropCurrentTime != nil {
			// C: PROP_TAG_LOCKMGR mp_lockmgr + PROP_TAG_MUTEX mp — the
			// dispatcher holds mp_mutex around the callback.
			mp.SubCurrentTime = mp.PropCurrentTime.Subscribe(
				func(o any, ev propcore.EventType, a ...any) {
					MpSeekByPropchange(o, ev, a...)
				}, mp, propcore.SubNoInitialUpdate,
				propcore.SubMutex{Ptr: mp},
				propcore.SubLockmgr{L: mp.Sys.PropLockmgr})
		}

		// C: mp->mp_sub_eventsink = prop_subscribe(0,
		//   PROP_TAG_NAME("media", "eventSink"),
		//   PROP_TAG_CALLBACK_EVENT, media_eventsink, mp,
		//   PROP_TAG_LOCKMGR, mp_lockmgr, PROP_TAG_MUTEX, mp,
		//   PROP_TAG_NAMED_ROOT, mp->mp_prop_root, "media")
		// NAMED_ROOT resolves "media" → mp_prop_root; "eventSink" is its
		// child. media_eventsink = mp_enqueue_event_locked(mp, e) — runs
		// with mp_mutex held by the dispatcher.
		sink := pm.CreateEx(mp.PropRoot, "eventSink", nil, false, false)
		if sink != nil {
			mp.SubEventSink = sink.Subscribe(func(_ any, ev propcore.EventType, args ...any) {
				if ev != propcore.EventExtEvent || len(args) == 0 {
					return
				}
				if e, ok := args[0].(*event.Event); ok && e != nil {
					MpEnqueueEventLocked(mp, &MediaEvent{Type: int(e.Type), Data: e.Concrete()})
				}
			}, mp, propcore.SubNoInitialUpdate,
				propcore.SubMutex{Ptr: mp},
				propcore.SubLockmgr{L: mp.Sys.PropLockmgr})
		}
	}

	// C: if(media_pipe_init_extra != NULL) media_pipe_init_extra(mp)
	// (media.c:289-290)
	if ms.MediaPipeSetupExtra != nil {
		ms.MediaPipeSetupExtra(mp)
	}

	return mp
}

// MpRetain increments the media pipe reference count.
// C: mp_retain — atomic_inc(&mp->mp_refcount)
func MpRetain(mp *MediaPipe) *MediaPipe {
	if mp == nil {
		return nil
	}
	atomic.AddInt32(&mp.RefCount, 1)
	return mp
}

// MpRelease decrements the media pipe reference count.
// C: mp_release (media.c:374-423) — on refcount=0, drains event queue,
// flushes/destroys queues, destroys props.
func MpRelease(mp *MediaPipe) {
	if mp == nil {
		return
	}
	rc := atomic.AddInt32(&mp.RefCount, -1)
	if rc != 0 {
		return
	}

	// C: assert(mp->mp_audio_decoder == NULL)
	// C: assert(mp != media_primary)

	// C: if(media_pipe_fini_extra != NULL) media_pipe_fini_extra(mp)
	// (media.c:386-387)
	if mp.Sys.MediaPipeFiniExtra != nil {
		mp.Sys.MediaPipeFiniExtra(mp)
	}

	// C: drain mp_eq — mp_release runs WITHOUT mp_mutex held
	// (media.c:374-417); prop_mutex may be held by the caller, so the
	// final prop teardown is deferred via task anyway.
	mp.EventQueue = make([]*MediaEvent, 0)

	// C: mq_flush(mp, &mp->mp_audio, 1); mq_flush(mp, &mp->mp_video, 1)
	// — full flush runs each buffer's dtor (prop_ref_dec, packet unref,
	// subtitle destroy). Simply dropping the slices leaks those.
	if mp.Audio != nil {
		MqFlush(mp, mp.Audio, true)
	}
	if mp.Video != nil {
		MqFlush(mp, mp.Video, true)
	}

	// C: video_overlay_flush_locked(mp, 0); dvdspu_destroy_all(mp)
	// (media.c:403-404) — teardown; overlay mutex held across both.
	mp.OverlayMutex.Lock()
	VideoOverlayFlushLocked(mp, false)
	DvdspuDestroyAll(mp)
	mp.OverlayMutex.Unlock()

	// C: if(mp->mp_satisfied == 0) atomic_dec(&media_buffer_hungry)
	if mp.Satisfied == 0 {
		mp.Sys.BufferHungry.Add(-1)
	}

	// C: cancellable_release(mp->mp_cancellable)
	misc.CancellableRelease(mp.Cancellable)
	mp.Cancellable = nil

	// C: task_run(mp_final_release, mp) — prop_destroy(mp->mp_prop_root)
	// deferred because prop_mutex may be held by the caller. Go props
	// don't share one global mutex, but destroy may still be re-entered
	// from a subscription — dispatch asynchronously to be safe.
	root := mp.PropRoot
	mp.PropRoot = nil
	if root != nil {
		if pm := root.Manager(); pm != nil {
			go pm.Destroy(root)
		}
	}
}

// MpDestroy destroys a media pipe.
// C: mp_destroy (media.c:329-356) — removes from global list, unbecomes
// primary, unsubscribes, destroys track managers, calls mp_release.
func MpDestroy(mp *MediaPipe) {
	if mp == nil {
		return
	}

	// C: LIST_REMOVE(mp, mp_global_link); num_media_pipelines--
	mp.Sys.Mutex.Lock()
	for i, p := range mp.Sys.pipelines {
		if p == mp {
			mp.Sys.pipelines = slices.Delete(mp.Sys.pipelines, i, i+1)
			mp.Sys.numPipelines--
			break
		}
	}
	mp.Sys.Mutex.Unlock()

	// C: mp_unbecome_primary(mp)
	MpUnbecomePrimary(mp)

	// C: assert(mp->mp_sub_currenttime != NULL) (media.c:338)

	// C: hts_mutex_lock(&mp->mp_mutex) (media.c:340) — the unsubscribes,
	// settings_clear and both mp_track_mgr_destroy run under mp_mutex.
	mp.Mutex.Lock()

	// C: prop_unsubscribe(mp->mp_sub_currenttime) (media.c:342)
	// C: prop_unsubscribe(mp->mp_sub_eventsink) (media.c:343)
	// NOT optional cleanup: each sub holds an mp ref via
	// LOCKMGR_RETAIN; skipping unsubscribe leaves mp->mp_refcount > 0
	// forever, so mp_release never drains queues and a pipe that was
	// mp_satisfied==0 leaks one media_buffer_hungry tick (scanner stalls).
	if mp.SubCurrentTime != nil {
		mp.SubCurrentTime.Unsubscribe()
		mp.SubCurrentTime = nil
	}
	if mp.SubEventSink != nil {
		mp.SubEventSink.Unsubscribe()
		mp.SubEventSink = nil
	}

	// C: #if ENABLE_MEDIA_SETTINGS → mp_settings_clear(mp) (media.c:346)
	if mp.Sys.MpSettingsClear != nil {
		mp.Sys.MpSettingsClear(mp)
	}

	// C: mp_track_mgr_destroy(&mp->mp_audio_track_mgr)
	// C: mp_track_mgr_destroy(&mp->mp_subtitle_track_mgr)
	MpTrackMgrDestroy(mp.AudioTrackMgr)
	MpTrackMgrDestroy(mp.SubtitleTrackMgr)
	// Go: track managers are GC'd.

	// C: hts_mutex_unlock(&mp->mp_mutex) (media.c:352)
	mp.Mutex.Unlock()

	// C: mp_release(mp)
	MpRelease(mp)
}

// MpBecomePrimary makes this media pipe the primary pipe.
// C: mp_become_primary (media.c:500-526) — calls mp_init_audio, asserts
// MP_PRIMABLE, stops previous primary, sets media_primary, prop_select,
// prop_link to media_prop_current, sets primary=1.
func MpBecomePrimary(mp *MediaPipe, pm *propcore.PropManager) {
	if mp == nil {
		return
	}

	// C: mp_init_audio(mp)
	MpSetupAudio(mp)

	// C: if(media_primary == mp) return
	if mp.Sys.primary == mp {
		return
	}

	mp.Sys.Mutex.Lock()
	defer mp.Sys.Mutex.Unlock()

	// C: assert(mp->mp_flags & MP_PRIMABLE)
	if mp.Flags&MPPrimaable == 0 {
		panic("mp_become_primary: MP_PRIMABLE not set")
	}

	// C: if(media_primary != NULL) { prop_set_int(primary, 0); enqueue ACTION_STOP }
	if mp.Sys.primary != nil {
		if pm != nil && mp.Sys.primary.PropPrimary != nil {
			pm.SetIntEx(mp.Sys.primary.PropPrimary, nil, 0)
		}
		// C: event_t *e = event_create_action(ACTION_STOP); mp_enqueue_event(media_primary, e)
		stop := &event.Event{
			Type:    event.EVENT_ACTION_VECTOR,
			Actions: []event.ActionType{event.ACTION_STOP},
		}
		// C: mp_enqueue_event locks mp_mutex internally.
		MpEnqueueEvent(mp.Sys.primary, &MediaEvent{
			Type: int(event.EVENT_ACTION_VECTOR), Data: stop,
		})
	}

	// C: media_primary = mp_retain(mp)
	mp.Sys.primary = MpRetain(mp)

	// C: prop_select(mp->mp_prop_root) — select the pipe's root among
	// media_prop_sources' children.
	if pm != nil && mp.PropRoot != nil {
		pm.SelectChildProp(mp.PropRoot, nil)
	}
	// C: prop_link(mp->mp_prop_root, media_prop_current) — "current"
	// mirrors the primary pipe's root.
	if mp.PropRoot != nil && mp.Sys.PropCurrent != nil {
		mp.PropRoot.Link(mp.Sys.PropCurrent)
	}
	// C: prop_set_int(mp->mp_prop_primary, 1)
	if pm != nil && mp.PropPrimary != nil {
		pm.SetIntEx(mp.PropPrimary, nil, 1)
	}
}

// MpUnbecomePrimary removes this media pipe from primary status.
// C: mp_unbecome_primary (media.c:532-549) — asserts MP_PRIMABLE, if
// media_primary == mp, sets primary=0, unlinks, clears media_primary.
func MpUnbecomePrimary(mp *MediaPipe) {
	if mp == nil {
		return
	}

	mp.Sys.Mutex.Lock()
	defer mp.Sys.Mutex.Unlock()

	// C: assert(mp->mp_flags & MP_PRIMABLE)
	if mp.Flags&MPPrimaable == 0 {
		return
	}

	// C: if(media_primary == mp) { prop_set_int(primary, 0);
	// prop_unlink(media_prop_current); media_primary = NULL;
	// mp_release(mp); prop_unselect(media_prop_sources) }
	if mp.Sys.primary == mp {
		if mp.PropPrimary != nil {
			mp.PropPrimary.SetInt(0)
		}
		if mp.Sys.PropCurrent != nil {
			mp.Sys.PropCurrent.Unlink()
		}
		mp.Sys.primary = nil
		MpRelease(mp) // mp could be free'd here
		if mp.Sys.PropSources != nil && mp.Sys.PropSources.Manager() != nil {
			mp.Sys.PropSources.Manager().UnselectChild(mp.Sys.PropSources)
		}
	}
}

// MpSetCurrentTime sets the current playback time.
// C: mp_set_current_time (media.c:575-597) — returns if PTS_UNSET,
// subtracts delta, checks epoch, sets prop_currenttime as float,
// enqueues EVENT_CURRENT_TIME, updates mp_seek_base.
func MpSetCurrentTime(mp *MediaPipe, pm *propcore.PropManager, ts int64, epoch int, delta int64) {
	if mp == nil {
		return
	}
	// C: if(ts == PTS_UNSET) return
	if ts == PTSUnset {
		return
	}

	// C: ts -= delta
	ts -= delta

	mp.Mutex.Lock()
	defer mp.Mutex.Unlock()

	// C: if(epoch == mp->mp_epoch)
	if int(epoch) == int(mp.Epoch) {
		// C: prop_set_float_ex(mp->mp_prop_currenttime, mp->mp_sub_currenttime, ts / 1000000.0)
		if pm != nil && mp.PropCurrentTime != nil {
			pm.SetFloatEx(mp.PropCurrentTime, mp.SubCurrentTime, float32(ts)/1000000.0)
		}

		// C: event_ts_t *ets = event_create(EVENT_CURRENT_TIME, sizeof(event_ts_t))
		// C: ets->ts = ts; ets->epoch = epoch
		// C: mp->mp_seek_base = ts
		// C: mp_enqueue_event_locked(mp, &ets->h)
		mp.SeekBase = ts
		ets := &event.EventTs{Ts: ts, Epoch: int(epoch)}
		ets.Event.SetConcrete(ets)
		ets.Type = event.EVENT_CURRENT_TIME
		MpEnqueueEventLocked(mp, &MediaEvent{
			Type: int(event.EVENT_CURRENT_TIME),
			Data: ets,
		})
	}
}

// MpHold holds (pauses) the media pipe with the given flag.
// C: mp_hold (media.c:641-647) — ORs flag into mp_hold_flags, calls
// mp_set_playstatus_by_hold_locked.
func MpHold(mp *MediaPipe, pm *propcore.PropManager, flag MediaHoldFlags, msg string) {
	if mp == nil {
		return
	}
	mp.Mutex.Lock()
	defer mp.Mutex.Unlock()
	mp.HoldFlags |= flag
	mpSetPlaystatusByHoldLocked(mp, msg)
}

// MpUnhold releases a hold flag from the media pipe.
// C: mp_unhold (media.c:654-660) — clears flag from mp_hold_flags,
// calls mp_set_playstatus_by_hold_locked.
func MpUnhold(mp *MediaPipe, pm *propcore.PropManager, flag MediaHoldFlags) {
	if mp == nil {
		return
	}
	mp.Mutex.Lock()
	defer mp.Mutex.Unlock()
	mp.HoldFlags &^= flag
	mpSetPlaystatusByHoldLocked(mp, "")
}

// mpSetPlaystatusByHoldLocked updates playstatus based on hold flags.
// C: mp_set_playstatus_by_hold_locked (media.c:604-634) — toggles
// mp_hold_gate, sends MB_CTRL_PAUSE/MB_CTRL_PLAY, sets pausereason
// and playstatus props, fires EVENT_HOLD, optionally flushes.
func mpSetPlaystatusByHoldLocked(mp *MediaPipe, msg string) {
	mpSetPlaystatusByHoldLockedImpl(mp, msg)
}

// MpSetPlaystatusByHoldLocked — exported variant for demuxers
// (backend/hls) that manage mp_hold_flags directly.
func MpSetPlaystatusByHoldLocked(mp *MediaPipe, msg string) {
	mpSetPlaystatusByHoldLockedImpl(mp, msg)
}

func mpSetPlaystatusByHoldLockedImpl(mp *MediaPipe, msg string) {
	// C: int hold = !!mp->mp_hold_flags
	hold := 0
	if mp.HoldFlags != 0 {
		hold = 1
	}

	// C: if(hold == mp->mp_hold_gate) return
	if hold == mp.HoldGate {
		return
	}

	mp.HoldGate = hold

	// C: int cmd = hold ? MB_CTRL_PAUSE : MB_CTRL_PLAY
	cmd := int(MBCtrlPause)
	if hold == 0 {
		cmd = int(MBCtrlPlay)
	}

	// C: if(mp->mp_flags & MP_VIDEO) mp_send_cmd_locked(mp, &mp->mp_video, cmd)
	// C: mp_send_cmd_locked(mp, &mp->mp_audio, cmd)
	if mp.Flags&MPVideo != 0 && mp.Video != nil {
		MpSendCmdLocked(mp, mp.Video, cmd)
	}
	if mp.Audio != nil {
		MpSendCmdLocked(mp, mp.Audio, cmd)
	}

	// C: if(!hold) prop_set_void(mp->mp_prop_pausereason)
	// C: else prop_set_string(mp->mp_prop_pausereason, msg ?: "Paused by user")
	if mp.PropPauseReason != nil {
		if hold == 0 {
			mp.PropPauseReason.SetVoid()
		} else {
			if msg == "" {
				msg = "Paused by user"
			}
			mp.PropPauseReason.SetString(msg)
		}
	}

	// C: prop_set_string(mp->mp_prop_playstatus, hold ? "pause" : "play")
	if mp.PropPlayStatus != nil {
		if hold != 0 {
			mp.PropPlayStatus.SetString("pause")
		} else {
			mp.PropPlayStatus.SetString("play")
		}
	}

	// C: mp_event_dispatch(mp, event_create_int(EVENT_HOLD, hold))
	MpEventDispatch(mp, &MediaEvent{
		Type: int(event.EVENT_HOLD),
		Data: &event.EventInt{
			Event: event.Event{Type: event.EVENT_HOLD},
			Val:   hold,
		},
	})

	// C: if(mp->mp_flags & MP_FLUSH_ON_HOLD) mp_flush_locked(mp, 0)
	if mp.Flags&MPFlushOnHold != 0 {
		MpFlushLocked(mp, 0)
	}

	// C: if(mp->mp_hold_changed != NULL) mp->mp_hold_changed(mp)
	// (media.c:632-633)
	if mp.HoldChanged != nil {
		mp.HoldChanged(mp)
	}
}

// MpSetDuration — C: mp_set_duration (media.c:683-699). Caller holds
// mp.Mutex in C (mp_configure); Go prop writes are lock-free so the lock
// is not strictly needed, but callers follow the C convention.
func MpSetDuration(mp *MediaPipe, duration int64) {
	if mp == nil {
		return
	}
	if duration == PTSUnset { // C: AV_NOPTS_VALUE
		mp.Duration = 0
		if mp.PropMetadata != nil {
			if p := mp.PropMetadata.Manager().CreateEx(mp.PropMetadata,
				"duration", nil, false, false); p != nil {
				p.SetVoid()
			}
		}
		return
	}
	mp.Duration = duration

	d := float32(duration) / 1000000.0
	if mp.PropMetadata != nil {
		if p := mp.PropMetadata.Manager().CreateEx(mp.PropMetadata,
			"duration", nil, false, false); p != nil {
			p.SetFloat(d)
		}
	}
	// C: if(duration && mp->mp_prop_metadata_source)
	if duration != 0 && mp.PropMetadataSource != nil {
		if p := mp.PropMetadataSource.Manager().CreateEx(mp.PropMetadataSource,
			"duration", nil, false, false); p != nil {
			p.SetFloat(d)
		}
	}
}

// MpSetClrFlagsLocked — C: mp_set_clr_flags_locked (media.c:703-714).
// Caller holds mp.Mutex.
func MpSetClrFlagsLocked(mp *MediaPipe, set, clr MediaPipeFlags) {
	mp.Flags &^= clr
	mp.Flags |= set

	if mp.PropCanSeek != nil {
		mp.PropCanSeek.SetInt(misc.BoolToInt(mp.Flags&MPCanSeek != 0))
	}
	if mp.PropCanPause != nil {
		mp.PropCanPause.SetInt(misc.BoolToInt(mp.Flags&MPCanPause != 0))
	}
	if mp.PropCanEject != nil {
		mp.PropCanEject.SetInt(misc.BoolToInt(mp.Flags&MPCanEject != 0))
	}
}

// MpSetClrFlags — C: mp_set_clr_flags (media.c:718-724).
func MpSetClrFlags(mp *MediaPipe, set, clr MediaPipeFlags) {
	if mp == nil {
		return
	}
	mp.Mutex.Lock()
	defer mp.Mutex.Unlock()
	MpSetClrFlagsLocked(mp, set, clr)
}

// MpConfigure configures the media pipe.
// C: mp_configure (media.c:729-775) — sets playstatus="play", resets
// framerate, sets/clears flags, sets buffer_limit based on buffer_size,
// sets type prop, calls mp_set_duration, mp_clock_setup.
func MpConfigure(mp *MediaPipe, pm *propcore.PropManager, flags MediaPipeFlags,
	bufferSize int, duration int64, typeStr string) {
	if mp == nil {
		return
	}
	mp.Mutex.Lock()
	defer mp.Mutex.Unlock()

	// C: prop_set_string(mp->mp_prop_playstatus, "play")
	if pm != nil && mp.PropPlayStatus != nil {
		pm.SetStringEx(mp.PropPlayStatus, nil, "play", propcore.StringUTF8)
	}

	// C: mp->mp_framerate.num = 0; mp->mp_framerate.den = 1
	mp.Framerate = AVRational{Num: 0, Den: 1}

	// C: mp->mp_max_realtime_delay = INT32_MAX
	mp.MaxRealtimeDelay = 0x7FFFFFFF

	// C: mp_set_clr_flags_locked(mp, flags, MP_PRE_BUFFERING | MP_FLUSH_ON_HOLD |
	//   MP_ALWAYS_SATISFIED | MP_CAN_SEEK | MP_CAN_PAUSE | MP_CAN_EJECT)
	MpSetClrFlagsLocked(mp, flags, MPPreBuffering|MPFlushOnHold|
		MPAlwaysSatisfied|MPCanSeek|MPCanPause|MPCanEject)

	// C: prop_set(mp->mp_prop_root, "type", PROP_SET_STRING, type)
	if pm != nil && mp.PropRoot != nil && typeStr != "" {
		typeProp := pm.CreateEx(mp.PropRoot, "type", nil, false, true)
		pm.SetStringEx(typeProp, nil, typeStr, propcore.StringUTF8)
	}

	// C: switch(buffer_size) { case MP_BUFFER_NONE: ...; case MP_BUFFER_SHALLOW: ...; case MP_BUFFER_DEEP: ... }
	switch bufferSize {
	case MPBufferNone:
		mp.BufferLimit = 0
	case MPBufferShallow:
		mp.BufferLimit = 1 * 1024 * 1024
	case MPBufferDeep:
		// C: mp->mp_buffer_limit = video_settings.video_buffer_size * 1024 * 1024
		mp.BufferLimit = mp.Sys.VS.VideoBufferSize * 1024 * 1024
	}

	// C: prop_set_int(mp->mp_prop_buffer_limit, mp->mp_buffer_limit)
	if pm != nil && mp.PropBufferLimit != nil {
		pm.SetIntEx(mp.PropBufferLimit, nil, mp.BufferLimit)
	}

	// C: mp_set_duration(mp, duration)
	MpSetDuration(mp, duration)

	// C: if(mp->mp_clock_setup != NULL)
	//      mp->mp_clock_setup(mp, mp->mp_audio.mq_stream != -1)
	if mp.ClockSetup != nil && mp.Audio != nil {
		mp.ClockSetup(mp, mp.Audio.Stream != -1)
	}

	// C: if(mp->mp_flags & MP_PRE_BUFFERING) mp_check_underrun(mp)
	if mp.Flags&MPPreBuffering != 0 {
		mpCheckUnderrun(mp)
	}
}

// MediaGlobalHold holds or releases all media pipes globally.
// C: media_global_hold (media.c:842-871) — snapshots all pipelines,
// retains each, iterates MP_VIDEO pipes, calls mp_hold/mp_unhold.
func (ms *MediaSystem) MediaGlobalHold(pm *propcore.PropManager, on bool, flag MediaHoldFlags) {
	// C: hts_mutex_lock(&media_mutex); count = num_media_pipelines
	ms.Mutex.Lock()
	count := ms.numPipelines
	pipes := make([]*MediaPipe, count)
	copy(pipes, ms.pipelines)
	// C: LIST_FOREACH(mp, &media_pipelines, mp_global_link) mpv[i++] = mp_retain(mp)
	for _, mp := range pipes {
		MpRetain(mp)
	}
	ms.Mutex.Unlock()

	// C: for(i = 0; i < count; i++) { if(!(mp->mp_flags & MP_VIDEO)) continue; ... }
	for _, mp := range pipes {
		if mp.Flags&MPVideo == 0 {
			MpRelease(mp)
			continue
		}

		if on {
			MpHold(mp, pm, flag, "")
		} else {
			MpUnhold(mp, pm, flag)
		}
		MpRelease(mp)
	}
}

// GetMediaPrimary returns the current primary media pipe.
// C: media_primary (global variable)
func (ms *MediaSystem) Primary() *MediaPipe {
	ms.Mutex.Lock()
	defer ms.Mutex.Unlock()
	return ms.primary
}

// GetMediaPipelines returns a snapshot of all media pipelines.
// C: media_pipelines (global list)
func (ms *MediaSystem) Pipelines() []*MediaPipe {
	ms.Mutex.Lock()
	defer ms.Mutex.Unlock()
	result := make([]*MediaPipe, len(ms.pipelines))
	copy(result, ms.pipelines)
	return result
}

// MpReset resets the media pipe for a new playback.
// C: mp_reset (media.c:300-322) — unholds pre-buffering/stream/sync,
// resets cancellable, clears startLatency/io bitrate/infoNodes,
// destroys audio/subtitle track children, resets track current/manual,
// adds "sub:off" track.
func MpReset(mp *MediaPipe, pm *propcore.PropManager) {
	if mp == nil || pm == nil {
		return
	}

	// C: mp_unhold(mp, MP_HOLD_PRE_BUFFERING | MP_HOLD_STREAM | MP_HOLD_SYNC)
	MpUnhold(mp, pm, MPHoldPreBuffering|MPHoldStream|MPHoldSync)

	// C: cancellable_reset(mp->mp_cancellable)
	misc.CancellableReset(mp.Cancellable)

	// C: prop_set(mp->mp_prop_root, "startLatency", PROP_SET_VOID)
	if mp.PropRoot != nil {
		if p := pm.CreateMulti(mp.PropRoot, "startLatency"); p != nil {
			p.SetVoid()
		}
	}

	// C: prop_set(mp->mp_prop_io, "bitrate", PROP_SET_VOID)
	// C: prop_set(mp->mp_prop_io, "bitrateValid", PROP_SET_VOID)
	if mp.PropIO != nil {
		if p := pm.CreateMulti(mp.PropIO, "bitrate"); p != nil {
			p.SetVoid()
		}
		if p := pm.CreateMulti(mp.PropIO, "bitrateValid"); p != nil {
			p.SetVoid()
		}
		// C: p = prop_create(mp->mp_prop_io, "infoNodes");
		//    prop_destroy_childs(p) (media.c:309-310)
		if p := pm.CreateEx(mp.PropIO, "infoNodes", nil, false, false); p != nil {
			pm.DestroyChilds(p)
		}
	}

	// C: prop_destroy_childs(mp->mp_prop_audio_tracks)
	// C: prop_destroy_childs(mp->mp_prop_subtitle_tracks)
	if mp.PropAudioTracks != nil {
		pm.DestroyChilds(mp.PropAudioTracks)
	}
	if mp.PropSubtitleTracks != nil {
		pm.DestroyChilds(mp.PropSubtitleTracks)
	}

	// C: prop_set_void(mp->mp_prop_audio_track_current)
	// C: prop_set_int(mp->mp_prop_audio_track_current_manual, 0)
	if mp.PropAudioTrackCurrent != nil {
		mp.PropAudioTrackCurrent.SetVoid()
	}
	if mp.PropAudioTrackCurrentManual != nil {
		mp.PropAudioTrackCurrentManual.SetInt(0)
	}

	// C: mp_add_track_off(mp->mp_prop_subtitle_tracks, "sub:off")
	// C: prop_set_string(mp->mp_prop_subtitle_track_current, "sub:off")
	// C: prop_set_int(mp->mp_prop_subtitle_track_current_manual, 0)
	MpAddTrackOff(pm, mp.PropSubtitleTracks, "sub:off")
	if mp.PropSubtitleTrackCurrent != nil {
		mp.PropSubtitleTrackCurrent.SetString("sub:off")
	}
	// C: prop_set_int(mp->mp_prop_subtitle_track_current_manual, 0)
	if mp.PropSubtitleTrackCurrentManual != nil {
		mp.PropSubtitleTrackCurrentManual.SetInt(0)
	}
}

// MpBumpEpoch increments the media pipe epoch.
// C: mp_bump_epoch (media.h) — mp->mp_epoch++
// Used by video_player_idle after play_video to invalidate stale events.
func MpBumpEpoch(mp *MediaPipe) {
	if mp == nil {
		return
	}
	mp.Mutex.Lock()
	defer mp.Mutex.Unlock()
	mp.Epoch++
}

// MpShutdown shuts down the media pipe playback.
// C: mp_shutdown (media.c:556-569) — flush(final=1), unbecome primary,
// destroy the audio decoder.
func MpShutdown(mp *MediaPipe) {
	if mp == nil {
		return
	}
	mp.Mutex.Lock()
	MpFlushLocked(mp, 1)
	mp.Mutex.Unlock()

	MpUnbecomePrimary(mp)

	if mp.AudioDecoder != nil {
		if mp.Sys.AudioDecoderDestroy != nil {
			mp.Sys.AudioDecoderDestroy(mp.AudioDecoder)
		}
		mp.AudioDecoder = nil
	}
}

// MpSetupAudio — C: mp_init_audio (media.c:490-495).
func MpSetupAudio(mp *MediaPipe) {
	if mp.AudioDecoder == nil && mp.Sys.AudioDecoderCreate != nil {
		mp.AudioDecoder = mp.Sys.AudioDecoderCreate(mp)
	}
}

// MpLockmgr — C: mp_lockmgr (media.c:782-806) — the media-pipe instance
// of the lockmgr pattern used by PROP_TAG_LOCKMGR subscriptions.
func MpLockmgr(ptr any, op int) int {
	mp, ok := ptr.(*MediaPipe)
	if !ok || mp == nil {
		panic("mp_lockmgr: bad ptr")
	}
	switch op {
	case misc.LOCKMGR_UNLOCK:
		mp.Mutex.Unlock()
		return 0
	case misc.LOCKMGR_LOCK:
		mp.Mutex.Lock()
		return 0
	case misc.LOCKMGR_TRY:
		if mp.Mutex.TryLock() {
			return 0 // C: hts_mutex_trylock returns 0 on success
		}
		return 1
	case misc.LOCKMGR_RETAIN:
		atomic.AddInt32(&mp.RefCount, 1)
		return 0
	case misc.LOCKMGR_RELEASE:
		MpRelease(mp)
		return 0
	}
	panic("mp_lockmgr: invalid op") // C: abort()
}

// MpDequeueEvent dequeues the next event from the media pipe event queue.
// C: mp_dequeue_event (media.c:442-455) — blocks on mp_backpressure.
func MpDequeueEvent(mp *MediaPipe) *MediaEvent {
	if mp == nil {
		return nil
	}
	mp.Mutex.Lock()
	defer mp.Mutex.Unlock()

	bp := backpressureLocked(mp)
	for len(mp.EventQueue) == 0 {
		bp.Wait() // C: hts_cond_wait(&mp->mp_backpressure, ...)
	}

	e := mp.EventQueue[0]
	mp.EventQueue = mp.EventQueue[1:]
	return e
}

// MpDequeueEventDeadline — C: mp_dequeue_event_deadline (media.c:460-485).
// timeoutMs == 0 → nonblocking peek; otherwise waits up to timeoutMs.
func MpDequeueEventDeadline(mp *MediaPipe, timeoutMs int) *MediaEvent {
	if mp == nil {
		return nil
	}
	mp.Mutex.Lock()
	defer mp.Mutex.Unlock()

	if timeoutMs != 0 {
		// C: ts = arch_get_ts() + timeout * 1000;
		// hts_cond_wait_timeout_abs(&mp->mp_backpressure, &mp->mp_mutex, ts)
		bp := backpressureLocked(mp)
		deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
		for len(mp.EventQueue) == 0 {
			d := time.Until(deadline)
			if d <= 0 {
				break
			}
			t := time.AfterFunc(d, func() {
				mp.Mutex.Lock()
				bp.Broadcast()
				mp.Mutex.Unlock()
			})
			bp.Wait()
			t.Stop()
		}
	}

	if len(mp.EventQueue) == 0 {
		return nil
	}
	e := mp.EventQueue[0]
	mp.EventQueue = mp.EventQueue[1:]
	return e
}

// MpEnqueueEvent enqueues an event to the media pipe event queue.
// C: mp_enqueue_event (media_event.c:362) — locks mp_mutex and runs the
// full mp_enqueue_event_locked processing (track-select handling, dedup,
// playpause hold toggling, seek/cancel side effects).
func MpEnqueueEvent(mp *MediaPipe, e *MediaEvent) {
	if mp == nil || e == nil {
		return
	}
	mp.Mutex.Lock()
	defer mp.Mutex.Unlock()
	MpEnqueueEventLocked(mp, e)
}

// MpAddTrack adds a track to a track list.
// C: mp_add_track (media_track.c:120-136) — mp_add_track_ex + prop_ref_dec.
func MpAddTrack(pm *propcore.PropManager, parent *propcore.Prop,
	title, url, format, longFormat, isolang, source string,
	sourcep *propcore.Prop, basescore, autosel int) {
	p := MpAddTrackEx(pm, parent, title, url, format, longFormat, isolang,
		source, sourcep, basescore, autosel)
	if p != nil {
		pm.RefDec(p)
	}
}

// MpAddTrackOff adds an "off" track to a track list.
// C: mp_add_track_off (media_track.c:142-149) —
// mp_add_track_ex(prop, "Off", url, NULL,...,NULL, 1000000, 1).
func MpAddTrackOff(pm *propcore.PropManager, parent *propcore.Prop, url string) {
	p := MpAddTrackEx(pm, parent, "Off", url, "", "", "", "", nil, 1000000, 1)
	if p != nil {
		pm.RefDec(p)
	}
}

// MediaHooks.MpSettingsSetup — C: mp_settings_init (media_settings.c:196) seam,
// called under mp_mutex by MpSetURL (#if ENABLE_MEDIA_SETTINGS).

// MediaHooks.MpSettingsClear — C: mp_settings_clear (media_settings.c:72),
// called by mp_release (media.c:346).

// MediaHooks.VideoPlaybackCreate — C: video_playback_create (video_playback.c:981)
// seam. The implementation lives in the backend layer which cannot be
// imported by widget code without an import cycle.

// MediaHooks.VideoPlaybackDestroy — C: video_playback_destroy
// (video_playback.c:993) seam.

// MpSetURL sets the URL property on the media pipe.
// C: mp_set_url (media.c:667-677) — prop url + mp_settings_init under
// mp_mutex.
func MpSetURL(mp *MediaPipe, url, parentURL, parentTitle string) {
	if mp == nil {
		return
	}
	if mp.PropURL != nil {
		if url == "" {
			mp.PropURL.SetVoid()
		} else {
			mp.PropURL.SetString(url)
		}
	}
	// C: #if ENABLE_MEDIA_SETTINGS → mp_settings_init under mp_mutex
	if mp.Sys.MpSettingsSetup != nil {
		mp.Mutex.Lock()
		mp.Sys.MpSettingsSetup(mp, url, parentURL, parentTitle)
		mp.Mutex.Unlock()
	}
}
