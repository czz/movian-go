package main

import (
	"fmt"

	"github.com/czz/movian-go/internal/arch"
	"github.com/czz/movian-go/internal/arch/mgos"
	backendcore "github.com/czz/movian-go/internal/backend/core"
	"github.com/czz/movian-go/internal/backend/dvd"
	"github.com/czz/movian-go/internal/backend/hls"
	"github.com/czz/movian-go/internal/backend/htsp"
	"github.com/czz/movian-go/internal/backend/icecast"
	"github.com/czz/movian-go/internal/backend/playlist/playqueue"
	"github.com/czz/movian-go/internal/backend/rtmp"
	"github.com/czz/movian-go/internal/ecmascript"
	"github.com/czz/movian-go/internal/event"
	"github.com/czz/movian-go/internal/fileaccess/scanner"
	fileaccsettings "github.com/czz/movian-go/internal/fileaccess/settings"
	"github.com/czz/movian-go/internal/htsmsg"
	"github.com/czz/movian-go/internal/libav"
	"github.com/czz/movian-go/internal/media"
	mediacore "github.com/czz/movian-go/internal/media/core"
	medialibav "github.com/czz/movian-go/internal/media/libav"
	"github.com/czz/movian-go/internal/metadata"
	navcore "github.com/czz/movian-go/internal/navigator"
	propcore "github.com/czz/movian-go/internal/prop"
	propproxy "github.com/czz/movian-go/internal/prop/proxy"
	"github.com/czz/movian-go/internal/service"
	"github.com/czz/movian-go/internal/settings"
	subtitlesext "github.com/czz/movian-go/internal/subtitles/ext"
	// C links ext_subtitles.o/video_overlay.o/sub_ass.o unconditionally;
	// the Go port self-registers its mediacore hooks via init().
)

func wireMedia(ctx *appContext) {
	traceSystem := ctx.traceSystem
	settingsManager := ctx.settingsManager
	bs := ctx.backendSystem

	// C: INITME(INIT_GROUP_GRAPHICS, freetype_init) — loads the default
	// LiberationSans face from <dataroot>/res/fonts/ before fontstash.
	ctx.initGroupSystem.RegisterInitHelper(&arch.InitHelper{
		Group: arch.InitGroupGraphics,
		Prio:  0,
		Start: ctx.textSys.FreetypeStart,
	})
	// C: fa_video.c calls freetype_get_context / freetype_load_dynamic_font_buf
	// directly — pkg/text registers its ops on the BackendSystem.
	bs.SetFreetypeOps(ctx.textSys.FreetypeOps())
	// C: INITME(INIT_GROUP_GRAPHICS, fontstash_init) + BE_REGISTER(fontstash)
	ctx.initGroupSystem.RegisterInitHelper(&arch.InitHelper{
		Group: arch.InitGroupGraphics,
		Prio:  0,
		Start: func() {
			ctx.textSys.FontstashStart(ctx.propManager, ctx.faManager, settingsManager, bs,
				htsmsg.NewStore(""), ctx.notifMgr)
			// C: fa_scanner.c:236 calls fontstash_props_from_title
			// directly for CONTENT_FONT entries (file-scope in C).
			scanner.SetFontPropsFunc(ctx.textSys.FontstashPropsFromTitle)
		},
	})

	// 16. init_group(InitGroupGraphics)
	ctx.initGroupSystem.InitGroup(arch.InitGroupGraphics)

	// C: main.c:430-432 — init_group(INIT_GROUP_GRAPHICS) then
	// glw_settings_init() before media_init. Canonical path (x11) runs
	// pkg/ui/glw GlwSettingsStart here.
	glwFrontendSettingsStart(ctx)

	// C: av_register_all() + libav system (main.c:424) — required before
	// any fa_libav_* usage (audio/video demux producers).
	// C: REGISTER_CODEC constructors ran pre-main; explicit calls.
	mediacore.RegisterBuiltinCodecs()
	medialibav.RegisterLibavCodecs()

	libavSys := libav.NewLibAVSystem()
	libav.SetGlobalLibAVSystem(libavSys)
	if err := libav.SetupLibAV(); err != nil {
		traceSystem.Error("core", "Failed to initialize libav: %v", err)
	}

	// 18. media_init() — SOFT
	ctx.mediaSystem = media.NewMediaSystem(ctx.propManager)
	if err := ctx.mediaSystem.Start(); err != nil {
		traceSystem.Error("core", "Failed to initialize media: %v", err)
	}
	// C: ext_subtitles.o/video_overlay.o/sub_ass.o hooks linked
	// unconditionally — explicit install on the media system (Core()
	// exists only after Start() runs mediacore.MediaStart).
	subtitlesext.RegisterMediaHooks(ctx.mediaSystem.Core())
	ctx.playback = media.NewPlaybackPipeline(ctx.propManager, ctx.mediaSystem.Core())
	ctx.playback.SetTraceSystem(traceSystem)
	ctx.playback.SetKVStore(ctx.kvstore)
	ctx.playback.SetFAM(ctx.faManager)

	// Create playqueue
	ctx.playqueue = playqueue.NewPlayQueue(ctx.propManager, ctx.usageReporter, ctx.faManager, ctx.mediaSystem.Core())
	ctx.playqueue.SetTraceSystem(traceSystem)
	// C: fa_scanner reaches the process-global playqueue — inject it into
	// the backend system so the scanner page callback can find it.
	bs.SetMediaSystem(ctx.mediaSystem.Core())
	bs.SetSubtitleSystem(ctx.subSys)
	bs.SetIndexer(ctx.indexer)
	bs.SetPlayQueue(ctx.playqueue)
	bs.SetKVStore(ctx.kvstore)
	bs.SetCalloutSystem(ctx.calloutSystem)
	bs.SetPropPageManager(ctx.propPageManager)
	bs.SetMetadataManager(ctx.metadataManager)

	// Connect playqueue to playback pipeline (mirrors C's playqueue_mp → video_player_idle)
	// This starts the player thread that watches for startme=true entries
	// and calls PlaybackPipeline.Open/Play to start media playback
	ctx.playqueue.SetPlayback(ctx.playback)

	// C: media_global_eventsink calls playqueue_event_handler for
	// EVENT_PLAYTRACK / non-primary events (playqueue imports
	// mediacore, so the reverse is a hook).
	ctx.mediaSystem.Core().PlayqueueEvent = func(e *event.Event) {
		ctx.playqueue.EventHandler(e)
	}

	// Set video page opener callback (mirrors C's backend_open_video)
	// When the playqueue starts playback, it opens a video player page
	// in the navigator with model.type="video" and source=url.
	// The video.view skin creates a glw_video_t widget that receives
	// decoded frames from the PlaybackPipeline via DeliverCallback.
	ctx.playqueue.SetOpenVideoPageFunc(func(url string) {
		if ctx.navSystem != nil {
			ctx.navSystem.Open("video:"+url, "")
		}
	})

	// Create EventManager and stopChan early — needed by playqueue and navigator
	// event consumers. Both consumers are started later in the init sequence.
	ctx.stopChan = make(chan struct{})
	ctx.eventManager = event.NewEventManager(ctx.propManager)

	// Wire the STPP prop-proxy client seams (C: implicit globals —
	// gconf.running_instance, the global prop context, event_mgr).
	propproxy.SetPropManager(ctx.propManager)
	propproxy.SetEventManager(ctx.eventManager)
	// C: event_dispatch() global — master-volume ACTION_VOLUME_* path.
	ctx.mediaSystem.Core().EventDispatch = ctx.eventManager.Dispatch
	propproxy.SetRunningInstance(ctx.gconf.RunningInstance[:])

	// Start playqueue event consumer for media events
	ctx.playqueue.StartEventConsumer(ctx.eventManager, ctx.stopChan)

	// 19. service_init()
	// (object allocated earlier — see INIT_GROUP_GRAPHICS registration)
	ctx.serviceSystem.Start()

	// C: INITME(INIT_GROUP_IPC, stos_automount_start,
	//    stos_automount_stop, 0) (stos_automount.c:478) — mgos automount
	//    watches /mgos/fsinfo and mounts block devices into /mgos/media.
	mgos.RegisterInit(ctx.initGroupSystem, ctx.serviceSystem)

	// C: gconf.settings_sd — the settings seam used by
	// service_create_managed (service.c:326-381). Injected via hooks
	// because pkg/service cannot import settings/core (import cycle).
	ctx.serviceSystem.SetSettingsHooks(&service.ServiceSettingsHooks{
		SD: settingsManager.SD,
		P:  settingsManager.P,
		AddDir: func(parent, title *propcore.Prop, subtype, icon string,
			url string) *propcore.Prop {
			return settingsManager.AddDir(parent, title, subtype, icon, nil, url)
		},
		CreateBoundBool: func(parent, title *propcore.Prop, value int,
			writeProp *propcore.Prop, store, key string) any {
			return settingsManager.SettingCreate(settings.SettingBool, parent,
				settings.SettingsInitialUpdate,
				settings.SettingTagTitle, title,
				settings.SettingTagValue, value,
				settings.SettingTagWriteProp, writeProp,
				settings.SettingTagStore, store, key)
		},
		CreateBoundString: func(parent, title *propcore.Prop, value string,
			writeProp *propcore.Prop, store, key string) any {
			return settingsManager.SettingCreate(settings.SettingString, parent,
				settings.SettingsInitialUpdate,
				settings.SettingTagTitle, title,
				settings.SettingTagValue, value,
				settings.SettingTagWriteProp, writeProp,
				settings.SettingTagStore, store, key)
		},
		SetBool: func(s any, v int) {
			if st, ok := s.(*settings.Setting); ok {
				settingsManager.SettingSet(st, settings.SettingBool, v)
			}
		},
		Destroy: func(s any) {
			if st, ok := s.(*settings.Setting); ok {
				settingsManager.Destroy(st)
			}
		},
	})

	// C: seturl → backend_canhandle + be->be_normalize (service.c:197-209)
	// and service_probe_loop → backend_probe (service.c:519). Injected
	// because pkg/service cannot import backend/core (import cycle).
	ctx.serviceSystem.SetBackendHooks(&service.ServiceBackendHooks{
		Probe: func(url string, timeoutMs int) (int, error) {
			res, perr := bs.Probe(url, timeoutMs)
			return int(res), perr
		},
		NormalizeURL: bs.Normalize,
	})

	// C: add_xdg_paths() (linux_main.c:187) — runs in main() after the
	// service system exists; creates MUSIC/PICTURES/VIDEOS services.
	platformMainStart(ctx)

	// 20. backend_init() — SOFT
	// (ctx.backendRegistry + bs allocated earlier — see INIT_GROUP_GRAPHICS)

	// Initialize the backend system (registers page:, settings:, search:, prop: backends)
	// In C, backends are registered via INITIALIZER() constructors before main().
	// In Go, we must call Start() explicitly.
	bs.Start()

	// Wire the settings manager to the backend system so the settings: backend
	// can link page.model to the global settingsModel (matching C's
	// prop_set(page, "model", PROP_SET_LINK, settings_model)).
	bs.SetSettingsMgr(settingsManager)

	// fileaccess_init — C: backend_init → be_init (fap_init hooks +
	// indexer/imageloader/settings tail). Protocol registration and the
	// default FAM already ran before settings_init (FAP_REGISTER phase).
	faManager := ctx.faManager
	// C: fileaccess_init settings tail (fileaccess.c:1441-1469) +
	// fa_indexer_init / fa_imageloader_init (fileaccess.c:1434-1441).
	faManager.SetSettingsStart(func() {
		fileaccsettings.FileaccessSettingsStart(settingsManager)
	})
	faManager.SetIndexerStart(ctx.indexer.FAIndexerStart)
	faManager.SetImageloaderStart(bs.FAImageloaderStart)
	faManager.SetDAVXMLDeserializer(htsmsg.DAVXMLDeserializeBridge)
	faManager.SetHTTPCookieBridge(ctx.store, htsmsg.HTTPCookieStoreSave,
		htsmsg.HTTPCookieStoreLoad)
	faManager.SetMessagePopup(ctx.notifMgr.MessagePopup)
	if err := faManager.Start(); err != nil {
		traceSystem.Error("core", "Failed to initialize file access manager: %v", err)
	}
	bs.SetFileAccessManager(faManager)

	// Set playqueue callbacks on fileaccess backend
	// C: playqueue_play(url, meta, 0) and playqueue_open(page) are called
	// from file_open_file when CONTENT_AUDIO is detected.
	// Go: We use callbacks to bridge backend/core → playqueue without import cycles.
	if ctx.playqueue != nil {
		pq := ctx.playqueue
		bs.SetPlayQueueCallbacks(
			func(url string, page *propcore.Prop, meta *propcore.Prop) {
				pq.Play(url, meta, false)
			},
			func(page *propcore.Prop) {
				_ = pq.Open(page)
			},
		)
		// C: player_thread calls backend_play_audio (playqueue.c:1145).
		pq.SetPlayAudioFunc(func(url string, mp *mediacore.MediaPipe,
			paused bool, mimetype string) (any, error) {
			return bs.PlayAudio(url, mp, paused, mimetype)
		})
	}

	if err := ctx.backendRegistry.BackendStart(ctx.propManager); err != nil {
		traceSystem.Error("core", "Failed to initialize backend: %v", err)
	}

	// Register additional backends that can't be registered from core
	// due to import cycles (they import core, so core can't import them)
	ctx.btg.RegisterBittorrentBackend(bs, ctx.propManager,
		ctx.eventManager)
	hls.RegisterHLSBackend(bs)
	htsp.RegisterHTSPBackend(bs)
	ctx.rtmpBackend = rtmp.NewBackend()
	ctx.rtmpBackend.RegisterRTMPBackend(bs)
	icecast.RegisterIcecastBackend(bs)
	dvd.RegisterDVDBackend(bs, ctx.notifMgr)
	// Register playqueue backend (mirrors C's BE_REGISTER(playqueue))
	// Done inline to avoid import cycle (playqueue → backend/core → fileaccess → playqueue)
	if ctx.playqueue != nil {
		pqBe := &backendcore.Backend{
			Prefix: "playqueue:",
			Flags:  0,
		}
		pqBe.CanHandle = playqueue.CanHandlePlayQueue
		pq := ctx.playqueue
		// C: be_playqueue_open — usage_page_open + playqueue_open
		pqBe.Open = func(page any, url string, sync bool) error {
			pageP, _ := page.(*propcore.Prop)
			return pq.OpenPage(pageP, url, sync)
		}
		bs.Register(pqBe)

	}
	// C: BE_REGISTER(ecmascript) — be_ecmascript with
	// BACKEND_OPEN_CHECKS_URI + be_open=ecmascript_openuri +
	// be_search=ecmascript_search (ecmascript.c:1108-1113).
	bs.Register(&backendcore.Backend{
		Flags: backendcore.BackendOpenChecksURI,
		Open: func(page any, url string, sync bool) error {
			p, ok := page.(*propcore.Prop)
			if !ok {
				return fmt.Errorf("ecmascript backend: non-prop page")
			}
			syncInt := 0
			if sync {
				syncInt = 1
			}
			if ecmascript.EcmascriptOpenuri(p, url, syncInt) != 0 {
				return fmt.Errorf("ecmascript: no route for %s", url)
			}
			return nil
		},
		Search: func(model any, query string, loading any) {
			m, _ := model.(*propcore.Prop)
			l, _ := loading.(*propcore.Prop)
			if m == nil {
				return
			}
			// Run the JS searcher hooks off the dispatch thread:
			// EsHookInvoke executes each plugin's searcher synchronously
			// and one slow/hung plugin would otherwise block the whole
			// backend_search loop (like locatedb_search's own thread).
			m.Retain()
			if l != nil {
				l.Retain()
			}
			go func() {
				defer m.Release()
				defer func() {
					if l != nil {
						l.Release()
					}
				}()
				ecmascript.EcmascriptSearch(m, query, l)
			}()
		},
	})

	// C: backend_init (backend.c:121-124) — after all BE_REGISTER
	// registrations, calls be_init on every backend. This is what
	// creates the "Search" settings dir (be_locatedb.be_init →
	// locatedb_init → search_get_settings) and runs htsp/rtmp/dvd init.
	bs.StartBackends()

	// C: BE_REGISTER(library) — be_library with be_canhandle=library_canhandle
	// + be_open=library_open (browsemdb.c:572-578).
	bs.Register(&backendcore.Backend{
		CanHandle: metadata.LibraryCanHandle,
		Open: func(page any, url string, sync bool) error {
			p, ok := page.(*propcore.Prop)
			if !ok {
				return fmt.Errorf("library backend: non-prop page")
			}
			ctx.metadataManager.LibraryOpen(ctx.propManager, p, url, sync,
				func(pm *propcore.PropManager, page *propcore.Prop, msg string) {
					navcore.OpenError(pm, page, msg)
				})
			return nil
		},
	})

	// C: ecmascript seams — prop manager, event manager, backend_prop_make.
	// Wired once here; the canonical pkg/ecmascript layer reads them.
	ecmascript.EsSetPropDeps(ctx.propManager, ctx.eventManager,
		bs.GetPropPageManager())

	// C: video_playback_create's implicit globals — the backend list, the
	// prop system, video_settings, and app_shutdown. Wired once here.
	bs.SetVideoSettings(func() (int, int) {
		vs := ctx.mediaSystem.Core().VS
		return vs.ResumeMode, vs.ContinuousPlayback
	})
	bs.SetAppShutdown(func(retcode int) {
		app_shutdown(ctx, retcode)
	})

}
