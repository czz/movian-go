package main

import (
	"log"
	"os"

	"github.com/czz/movian-go/internal/audio"
	audiocore "github.com/czz/movian-go/internal/audio/core"
	mediacore "github.com/czz/movian-go/internal/media/core"
	navcore "github.com/czz/movian-go/internal/navigator"
	propcore "github.com/czz/movian-go/internal/prop"
	// C links ext_subtitles.o/video_overlay.o/sub_ass.o unconditionally;
	// the Go port self-registers its mediacore hooks via init().
)

func wireUI(ctx *appContext, cleanupOnFail func()) {
	traceSystem := ctx.traceSystem
	settingsManager := ctx.settingsManager
	bs := ctx.backendSystem

	// 21. nav_init() — CRITICAL
	ctx.navSystem = navcore.NewNavigatorSystem(ctx.propManager, ctx.serviceSystem)
	ctx.navSystem.SetGconf(ctx.gconf)
	ctx.navSystem.SetTraceSystem(traceSystem)
	ctx.navSystem.SetStore(ctx.store)
	ctx.navSystem.SetKVStore(ctx.kvstore)
	ctx.navSystem.SetNotificationManager(ctx.notifMgr)
	ctx.navSystem.SetBackendSystem(bs)
	// C: bookmarks_init (nav_init head) needs gconf.settings context for the
	// "Bookmarks" settings dir (settings:bookmarks).
	ctx.navSystem.SetSettingsMgr(settingsManager)
	ctx.navSystem.StartNavigator(ctx.eventManager)

	// Wire NAV events through SendExtEvent → navCourier → ProcessNavCourier
	// C: prop_send_ext_event(nav.eventSink, e) in event_dispatch
	ctx.eventManager.SetNavEventSinkProp(ctx.navSystem.NavEventSinkProp())

	// Start navigator event consumer (EventManager and stopChan were created earlier)
	ctx.navSystem.StartEventConsumer(ctx.eventManager, ctx.stopChan)

	// 22. glw_init() — CRITICAL
	// Frontend-specific glw init moved before init_group(GRAPHICS)
	// above (canonical ordering: glw_settings_init follows the group).

	// Create missing $global subtrees (matching C's initialization)
	// These are created by various C subsystems at startup
	gp := ctx.propManager.GetGlobal()

	// $global.net — network interface info (C: src/networking/net_common.c)
	netProp := ctx.propManager.CreateEx(gp, "net", nil, false, false)
	ctx.propManager.CreateEx(netProp, "interfaces", nil, false, false)
	netConnProp := ctx.propManager.CreateEx(netProp, "connectivity", nil, false, false)
	ctx.propManager.SetIntEx(netConnProp, nil, 1)

	// $global.fonts — font configuration (C: src/text/fontstash.c)
	fontsProp := ctx.propManager.CreateEx(gp, "fonts", nil, false, false)
	ctx.propManager.CreateEx(fontsProp, "main", nil, false, false)
	ctx.propManager.CreateEx(fontsProp, "condensed", nil, false, false)
	ctx.propManager.CreateEx(fontsProp, "subs", nil, false, false)
	ctx.propManager.CreateEx(fontsProp, "installed", nil, false, false)

	// $global.glw — GLW settings (C: src/ui/glw/glw_settings.c + plugins.c:1893-1894)
	glwProp := ctx.propManager.CreateEx(gp, "glw", nil, false, false)
	ctx.propManager.CreateEx(glwProp, "background", nil, false, false)
	screensaverProp := ctx.propManager.CreateEx(glwProp, "screensaver", nil, false, false)
	imageDurProp := ctx.propManager.CreateEx(screensaverProp, "imageDuration", nil, false, false)
	ctx.propManager.SetIntEx(imageDurProp, nil, 15)
	ctx.propManager.CreateEx(screensaverProp, "items", nil, false, false)
	ctx.propManager.CreateEx(glwProp, "osk", nil, false, false)
	// $global.glw.views.standard.* — standard view types for multiopt selection.
	// C: plugins.c:1913-1927 add_view_type() creates views.<type>.<class>
	// with an unnamed child prop (key="default"). Used by directory.view
	// multiopt and osk.view keyboard selection via vectorize().
	viewsProp := ctx.propManager.CreateEx(glwProp, "views", nil, false, false)
	standardViewsProp := ctx.propManager.CreateEx(viewsProp, "standard", nil, false, false)
	for _, class := range []string{
		"background", "loading", "screensaver", "home", "osk",
		"tracks", "album", "albums", "artist", "tvchannels",
		"images", "movies", "directory",
	} {
		classProp := ctx.propManager.CreateEx(standardViewsProp, class, nil, false, false)
		// C: add_view_type creates an unnamed child prop with key="default"
		// This is what vectorize() iterates over to produce the option list.
		ctx.propManager.CreateEx(classProp, "", nil, false, false)
	}

	// $global.bookmarks — bookmark system (C: src/navigator.c bookmarks_init)
	bookmarksProp := ctx.propManager.CreateEx(gp, "bookmarks", nil, false, false)
	ctx.propManager.CreateEx(bookmarksProp, "queries", nil, false, false)
	ctx.propManager.CreateEx(bookmarksProp, "eventSink", nil, false, false)

	// $global.i18n — internationalization (C: src/api/tvdb.c, src/api/tmdb.c)
	i18nProp := ctx.propManager.CreateEx(gp, "i18n", nil, false, false)
	iso639Prop := ctx.propManager.CreateEx(i18nProp, "iso639_1", nil, false, false)
	ctx.propManager.SetStringEx(iso639Prop, nil, "en", propcore.StringUTF8)

	// $global.runcontrol — run control capabilities (C: src/runcontrol.c)
	rcProp := ctx.propManager.CreateEx(gp, "runcontrol", nil, false, false)
	ctx.propManager.CreateEx(rcProp, "canStandby", nil, false, false)
	ctx.propManager.CreateEx(rcProp, "canPowerOff", nil, false, false)
	ctx.propManager.CreateEx(rcProp, "canLogout", nil, false, false)
	ctx.propManager.CreateEx(rcProp, "canOpenShell", nil, false, false)
	ctx.propManager.CreateEx(rcProp, "canRestart", nil, false, false)
	canExitProp := ctx.propManager.CreateEx(rcProp, "canExit", nil, false, false)
	ctx.propManager.SetIntEx(canExitProp, nil, 1)
	ctx.propManager.CreateEx(rcProp, "sleepTimer", nil, false, false)
	ctx.propManager.CreateEx(rcProp, "sleepTime", nil, false, false)
	ctx.propManager.CreateEx(rcProp, "sleepTimeMax", nil, false, false)

	// $global.eventSink — global event sink (C: used by runcontrol)
	ctx.propManager.CreateEx(gp, "eventSink", nil, false, false)

	// $global.itemhooks — item hook system (C: plugin item hooks)
	ctx.propManager.CreateEx(gp, "itemhooks", nil, false, false)

	// $global.location — geographic location (C: src/main.c)
	locationProp := ctx.propManager.CreateEx(gp, "location", nil, false, false)
	ccProp := ctx.propManager.CreateEx(locationProp, "cc", nil, false, false)
	ctx.propManager.SetStringEx(ccProp, nil, "US", propcore.StringUTF8)

	// $global.stpp — STPP remote control (C: src/api/stpp.c)
	mgppProp := ctx.propManager.CreateEx(gp, "stpp", nil, false, false)
	ctx.propManager.CreateEx(mgppProp, "remoteControlled", nil, false, false)

	// $global.news — news/notifications (C: src/notifications.c)
	ctx.propManager.CreateEx(gp, "news", nil, false, false)

	defaultNav := ctx.navSystem.DefaultNavigator()
	if defaultNav == nil {
		log.Printf("[MAIN] CRITICAL: Default navigator is nil")
		cleanupOnFail()
		os.Exit(1)
	}
	// Frontend-specific late init: consolidated path binds PropNav and
	// runs GLWInit+GlwSettingsStart+GLFW backend; canonical path (x11)
	// resolves nav via GlwX11NavSpawnFn inside glw_x11_start.
	glwFrontendLateStart(ctx, defaultNav, settingsManager)

	// 24. audio_init() — SOFT
	ctx.audioManager = audio.NewAudioManager(ctx.propManager, ctx.store)
	ctx.audioManager.SetTraceSystem(traceSystem)
	ctx.audioManager.SetFAM(ctx.faManager)
	ctx.audioManager.SetGconf(ctx.gconf)
	ctx.audioManager.SetSettingsManager(settingsManager)
	if err := ctx.audioManager.AudioStart(); err != nil {
		traceSystem.Error("core", "Failed to initialize audio: %v", err)
	}

	// C: audio_decoder_create / audio_decoder_destroy — wired into the
	// media pipe via mp_init_audio (media.c:490) called from
	// mp_become_primary.
	ctx.mediaSystem.Core().AudioDecoderCreate = func(mp *mediacore.MediaPipe) any {
		ad, err := ctx.audioManager.AudioDecoderCreate(mp)
		if err != nil {
			return nil
		}
		return ad
	}
	ctx.mediaSystem.Core().AudioDecoderDestroy = func(v any) {
		if ad, ok := v.(*audiocore.AudioDecoder); ok {
			ctx.audioManager.AudioDecoderDestroy(ad)
		}
	}

	// Register video: route (mirrors C's backend_open_video)
	// When a video: URL is opened, set model.type="video" and source=url
	// so the video.view skin creates a glw_video_t widget.
	//
	// IMPORTANT: This route must NOT call playback.Open()/Play().
	// C's backend_open_video (src/backend/backend.c:523-533) only sets props:
	//   directClose=1, source=url, model.type="video", model.loading=0
	// Playback is started separately by the playqueue playerThread,
	// which calls pb.Open(url) + pb.Play() before invoking openVideoPage(url).
	// Calling playback.Open() here would fail with "playback already active"
	// and cause OpenErrorf to set model.type="openerror", loading the wrong
	// view (openerror.view instead of video.view), breaking OSD/playdeck.
	ctx.navSystem.GetRouteSystem().CreateRoute("video:", func(page any, url string, matches []string) {
		pm := ctx.propManager
		propRoot, ok := page.(*propcore.Prop)
		if !ok || propRoot == nil {
			return
		}
		// Set model.type = "video" (triggers pages/video.view)
		modelProp := pm.CreateEx(propRoot, "model", nil, false, false)
		typeProp := pm.CreateEx(modelProp, "type", nil, false, false)
		pm.SetStringEx(typeProp, nil, "video", propcore.StringUTF8)
		// Set source = url (the video widget reads this)
		sourceProp := pm.CreateEx(propRoot, "source", nil, false, false)
		// Strip "video:" prefix from the URL for the source
		sourceURL := url
		if len(sourceURL) > 6 && sourceURL[:6] == "video:" {
			sourceURL = sourceURL[6:]
		}
		pm.SetStringEx(sourceProp, nil, sourceURL, propcore.StringUTF8)
		// Set directClose=1 (like C's backend_open_video)
		dcProp := pm.CreateEx(propRoot, "directClose", nil, false, false)
		pm.SetIntEx(dcProp, nil, 1)
		// Set loading=0
		loadingProp := pm.CreateEx(modelProp, "loading", nil, false, false)
		pm.SetIntEx(loadingProp, nil, 0)
	})

}
