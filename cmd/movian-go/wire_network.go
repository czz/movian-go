package main

import (
	"context"
	"path/filepath"
	"time"

	"github.com/czz/movian-go/internal/nls"

	"github.com/czz/movian-go/internal/gconf"

	"github.com/czz/movian-go/internal/api/airplay"
	"github.com/czz/movian-go/internal/api/httpcontrol"
	"github.com/czz/movian-go/internal/api/lastfm"
	metadataapi "github.com/czz/movian-go/internal/api/metadata"
	"github.com/czz/movian-go/internal/api/mgpp"
	apimgpp "github.com/czz/movian-go/internal/api/mgpp"
	"github.com/czz/movian-go/internal/api/mpris"
	"github.com/czz/movian-go/internal/api/screenshot"
	"github.com/czz/movian-go/internal/app"
	"github.com/czz/movian-go/internal/arch"
	"github.com/czz/movian-go/internal/arch/mgos"
	backendcore "github.com/czz/movian-go/internal/backend/core"
	"github.com/czz/movian-go/internal/backend/youtube"
	"github.com/czz/movian-go/internal/db"
	"github.com/czz/movian-go/internal/ecmascript"
	"github.com/czz/movian-go/internal/htsmsg"
	"github.com/czz/movian-go/internal/i18n"
	mediasettings "github.com/czz/movian-go/internal/media/settings"
	navcore "github.com/czz/movian-go/internal/navigator"
	"github.com/czz/movian-go/internal/networking/ftpserver"
	httpnet "github.com/czz/movian-go/internal/networking/http"
	"github.com/czz/movian-go/internal/networking/ifaddr"
	"github.com/czz/movian-go/internal/plugins"
	propcore "github.com/czz/movian-go/internal/prop"
	prophttp "github.com/czz/movian-go/internal/prop/http"
	"github.com/czz/movian-go/internal/ui"

	// C links ext_subtitles.o/video_overlay.o/sub_ass.o unconditionally;
	// the Go port self-registers its mediacore hooks via init().
	"github.com/czz/movian-go/internal/upgrade"
	"github.com/czz/movian-go/internal/upnp"
	"github.com/czz/movian-go/internal/video"
)

func wireNetwork(ctx *appContext, gc *gconf.T) {
	traceSystem := ctx.traceSystem
	settingsManager := ctx.settingsManager
	bs := ctx.backendSystem
	persistentPath := ctx.persistentPath
	metadataManager := ctx.metadataManager

	// 25. plugins_init(gconf.devplugins) — C: plugins.c:1410-1447
	// Synchronous setup: root props, repo config, dev plugins.
	pluginManager := plugins.NewPluginManager(ctx.usageReporter, ctx.notifMgr, ctx.store)
	pluginManager.SetTraceSystem(traceSystem)
	pluginManager.SetPropPageManager(ctx.propPageManager)
	pluginManager.SetPropManager(ctx.propManager)
	pluginManager.SetRouteRegistrar(ctx.navSystem.GetRouteSystem())
	pluginManager.SetServiceSystem(ctx.serviceSystem)
	pluginManager.SetFileAccessManager(ctx.faManager)
	pluginManager.SetGconf(gc)
	pluginManager.SetPersistentPath(persistentPath)
	// NOTE: C's plugin_props_from_file (fa_scanner.c:233, CONTENT_PLUGIN
	// entries — clicking a plugin .zip in file browse) is not yet ported;
	// the scanner seam exists (scanner.SetPluginPropsFunc) but no
	// PluginManager-side implementation is wired here.
	// C: plugins_setup_root_props wires "Plugin repositories" into
	// general:plugins via setting_get_dir + settings_create_action
	pluginManager.SetSettingsManager(settingsManager)
	pluginManager.SetEventManager(ctx.eventManager)

	// Wire plugin manager to backend system for plugin_search support
	bs.SetPluginManager(&pluginSearchAdapter{pm: pluginManager})
	// C: plugin_probe_for_autoinstall under ENABLE_PLUGINS
	// (fa_probe.c:570, fa_audio.c:160).
	ecmascript.EsSetPluginSelectView(
		func(pluginID, filename string) {
			pluginManager.SelectView(pluginID, filename)
		})
	ctx.faManager.SetPluginProbeForAutoinstall(
		func(fh any, buf []byte, l int, url string) {
			pluginManager.ProbeForAutoInstall(fh, buf, l, url)
		})
	// C: plugin_open_file under ENABLE_PLUGINS (fa_backend.c:240)
	bs.SetPluginOpenFile(func(page *propcore.Prop, url string) {
		plugins.OpenFile(context.Background(), pluginManager, page, url)
	})
	// C: nav_redirect (navigator.c:972) — file_open_file's redirect dispatch.
	bs.SetNavRedirect(navcore.Redirect)
	// C: es_service.c plugin.uninstall → plugin_uninstall (global seam)
	ecmascript.EsSetPluginUninstall(func(id string) {
		_ = pluginManager.UninstallPluginByID(id)
	})
	// C: es_io.c probe() → backend_probe (global seam)
	ecmascript.EsSetBackendSystem(bs)
	// C: keyring_lookup (keyring.c) — FTP/other protocol auth lookups.
	ctx.faManager.SetKeyringLookup(ctx.keyring.Lookup)
	ctx.smbSys.SetKeyringLookup(ctx.keyring.Lookup)
	// C: gconf.plugin_repo — --plugin-repo, consumed inside plugins_init
	if err := plugins.Start(context.Background(), pluginManager, nil); err != nil {
		traceSystem.Error("core", "Failed to initialize plugins: %v", err)
	}

	// Native YouTube backend — Go extension (no C counterpart). Plugin-
	// shaped: youtube: backend routes + home-screen service + entry in
	// the plugin list, but compiled in and always on.
	youtube.NewSystem(traceSystem).Start(bs, ctx.serviceSystem,
		pluginManager, settingsManager, ctx.kvstore)

	// C: plugins_init loads dev plugins inline (plugins.c:1424-1443).
	// Go: parseOpts collects them into gconf.DevPlugins.
	for _, pluginPath := range ctx.gconf.DevPlugins {
		if err := pluginManager.LoadPlugin(pluginPath, "dev", plugins.PluginLoadForce|plugins.PluginLoadByUser); err != nil {
			// C: plugins.c:1437
			traceSystem.Error("plugins", "Unable to load development plugin: %s\n%v", pluginPath, err)
		} else {
			// C: plugins.c:1440
			traceSystem.Info("plugins", "Loaded dev plugin %s", pluginPath)
		}
	}

	pluginManager.UpdatePluginService()

	// 26. generate_device_id() — C: main.c:486 (before swthread spawn)
	deviceID := generateDeviceID(ctx.gconf, ctx.store)
	ctx.gconf.DeviceID = deviceID
	// C: main.c:487
	traceSystem.Debug("SYSTEM", "Hashed device ID: %s", deviceID)

	// C: swthread (main.c:254-310) — background thread that:
	//   1. plugins_load_all() — loads installed plugins from persistent_path/mrp/installed
	//   2. upgrade_init(), usage_start()
	//   3. if !gconf.disable_upgrades: plugins_upgrade_check() retry loop (up to 10 retries)
	//      navigator_can_start() after each retry
	//      upgrade_refresh() retry loop (up to 10 retries)
	//   4. else: navigator_can_start()
	//   5. periodic swrefresh loop (12-hour interval)
	// Go: spawn a background goroutine matching swthread.
	go func() {
		// C: plugins_load_all()
		if err := plugins.LoadAll(context.Background(), pluginManager); err != nil {
			traceSystem.Error("plugins", "Failed to load installed plugins: %v", err)
		}

		// C: upgrade_init() — deps are the C globals: app_shutdown
		//    (install_locked tail, upgrade.c:1146),
		//    gconf.upgrade_path/binary (upgrade.c:845,1252)
		upgradeInstance := upgrade.NewUpgrade(settingsManager, ctx.propManager,
			&upgrade.UpgradeDeps{
				Shutdown:         func(retcode int) { app_shutdown(ctx, retcode) },
				UpgradePath:      ctx.gconf.UpgradePath,
				Binary:           ctx.gconf.Binary,
				Usage:            ctx.usageReporter,
				NM:               ctx.notifMgr,
				FAM:              ctx.faManager,
				EnableBinReplace: &ctx.enableBinReplace,
				EnableOmnigrade:  &ctx.enableOmnigrade,
			})
		upgradeInstance.SetTraceSystem(traceSystem)
		upgradeInstance.UpgradeStart()
		// C: prop_subscribe(global.upgrade.eventSink) inside upgrade_init —
		// split out because Go's sync.Mutex is not recursive.
		upgradeInstance.UpgradeSubscribe()
		// C: be_upgrade's upgrade_open_url calls the global upgrade_refresh()
		ctx.backendSystem.SetUpgradeMgr(upgradeInstance)

		// C: usage_start() — usage.c:172-178. The Reporter reads gconf.*
		// values from ctx.usageCfg at send time; fill them now.
		ctx.usageCfg.DeviceID = deviceID
		ctx.usageCfg.OSInfo = ctx.gconf.OSInfo
		// C: main.c:595 — --show-usage-events
		ctx.usageCfg.ShowEvents = ctx.gconf.ShowUsageEvents
		ctx.usageCfg.Track = upgradeInstance.UpgradeGetTrack
		ctx.usageReporter.Start()

		// C: main.c:265-288 — if(!gconf.disable_upgrades) { ... } else { navigator_can_start(); }
		if !ctx.gconf.DisableUpgrades {
			// C: for(int i = 0; i < 10; i++) { if(!plugins_upgrade_check()) break; navigator_can_start(); sleep(i+i); }
			for i := 0; i < 10; i++ {
				if _, err := plugins.UpgradeCheck(context.Background(), pluginManager); err == nil {
					break
				}
				ctx.navSystem.SetCanStart()
				// C: main.c:271 — prints i+1, sleeps i+i
				traceSystem.Debug("plugins", "Failed to update repo, retrying in %d seconds", i+1)
				time.Sleep(time.Duration(i+i) * time.Second)
			}
			// C: navigator_can_start()
			ctx.navSystem.SetCanStart()

			// C: for(int i = 0; i < 10; i++) { if(!upgrade_refresh()) break; sleep(i+1); }
			for i := 0; i < 10; i++ {
				if err := upgradeInstance.UpgradeRefresh(); err == nil {
					break
				}
				time.Sleep(time.Duration(i+1) * time.Second)
				// C: main.c:278 — trace fires after the sleep
				traceSystem.Debug("upgrade", "Failed to check for app upgrade, retrying in %d seconds", i+1)
			}
		} else {
			// C: navigator_can_start()
			ctx.navSystem.SetCanStart()
		}

		// C: main.c:290-313 — periodic swrefresh loop (12-hour interval)
		if !ctx.gconf.DisableUpgrades {
			ticker := time.NewTicker(12 * time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
				case <-swrefreshKick:
					// C: swrefresh() (main.c:321-329) — an external
					// poke (rpi UI restart) sets gconf.swrefresh.
				}
				// C: if(!timeout) plugins_upgrade_check();
				plugins.UpgradeCheck(context.Background(), pluginManager)
				// C: upgrade_refresh();
				upgradeInstance.UpgradeRefresh()
			}
		}
	}()

	// 27. Background threads — C: main.c:492-493
	//   hts_thread_create_detached("geothread", geothread, ...) — no-op on Linux
	//   hts_thread_create_detached("swinst", swthread, ...) — handled above (step 25)
	// geothread is a no-op (geoip_init does nothing on Linux), so it's not spawned.
	// swthread is spawned in step 25 (after plugins_init).

	// 28. i18n_init()
	// C: set_timezone → callout_update_clock_props (i18n.c:82)
	i18nInstance := i18n.NewI18N(settingsManager, ctx.propManager)
	i18nInstance.SetGconf(ctx.gconf)
	i18nInstance.SetTraceSystem(traceSystem)
	i18nInstance.SetUpdateClockProps(ctx.calloutSystem.UpdateClockProps)
	i18nInstance.SetFileAccessManager(ctx.faManager)
	i18nInstance.Start()
	// C: es_subtitles.c → i18n_subtitle_lang (global i18n seam)
	ecmascript.EsSetI18N(i18nInstance)
	// C: i18n_audio_score / i18n_subtitle_score are global; media_track
	// uses them for track scoring.
	ctx.mediaSystem.Core().LangScorer = i18nInstance
	// C: gconf.lang seam — i18n_set_language writes gconf.lang which
	// usage.c:138 reads as metrics._locale.
	ctx.usageCfg.Lang = i18nInstance.CurrentLang

	// 29. video_settings_init() — SOFT
	// C: video_settings.c:30 — the canonical setting_create calls are in
	// mediasettings.VideoSettingsStart (pkg/video cannot import
	// settings/core — cycle via glw/view).
	videoSettings := video.NewVideoSettings(ctx.propManager)
	ctx.mediaSystem.Core().SetVideoSettings(videoSettings)
	ctx.mediaSettingsSys = mediasettings.NewSystem(ctx.gconf)
	ctx.mediaSettingsSys.VideoSettingsStart(settingsManager, videoSettings)

	// C: mp_settings_init is wired via mp_set_url (media.c:667) —
	// the ENABLE_MEDIA_SETTINGS seam. gconf.setting_av_volume /
	// gconf.setting_av_sync are assigned inside audio_init.
	if ctx.runControl != nil {
		canStandby, _, _, _, _, _ := ctx.runControl.GetCapabilities()
		ctx.gconf.CanStandby = canStandby
	}
	ctx.mediaSettingsSys.Start(settingsManager, ctx.mediaSystem.Core())

	// 30. init_group(InitGroupIPC)
	ctx.initGroupSystem.InitGroup(arch.InitGroupIPC)
	// C: INITME(INIT_GROUP_IPC, devevent_start, NULL, 0) — ipc/devevent.c
	ctx.ipc.DeveventStart(ctx.eventManager)
	// C: INITME(INIT_GROUP_IPC, lirc_open, NULL, 0) — ipc/lirc.c
	ctx.ipc.LircOpen(ctx.eventManager)
	// C: INITME(INIT_GROUP_IPC, stdin_start, NULL, 0) — ipc/stdin.c
	ctx.ipc.StdinStart(ctx.eventManager, ctx.gconf.ListenOnStdin)
	// C: INITME(INIT_GROUP_IPC, libcec_init, libcec_fini, 10) — ipc/libcec.c
	//    (CONFIG_LIBCEC — only enabled on RPi builds; no-op otherwise)
	ctx.ipc.LibcecStart(ctx.eventManager, settingsManager, ctx.taskSystem)
	// C: INITME(INIT_GROUP_IPC, clipboard_init, NULL, 10) — ui/clipboard.c
	ctx.clipboard = ui.NewClipboard(ctx.propManager, ctx.eventManager, ctx.faManager, ctx.notifMgr, ctx.taskSystem, traceSystem)
	ui.SetWebpopupTrace(traceSystem)
	ui.SetWebpopupFAM(ctx.faManager)
	ui.SetWebpopupMainloopWake(arch.WakeMainloop)
	// C: linux_webpopup_check() slot in mainloop (linux_main.c:110).
	arch.WebpopupCheck = ui.WpMainCheck
	arch.WebpopupActive = ui.WpActive

	// 31. sd_init()
	// C: #if STOS && ENABLE_AVAHI — set_system_name calls
	//    avahi_update_hostname (settings.c:1327-1330). Under !mgos the
	//    function still exists but its flag is never consumed.
	settingsManager.SetAvahiUpdateHostname(ctx.sdSys.AvahiUpdateHostname)
	ctx.sdSys.Start(ctx.serviceSystem, settingsManager, ctx.propManager, traceSystem)

	// C: INITME(INIT_GROUP_API, tmdb_init / tvdb_init / lastfm_init, NULL, 0)
	// — each registers a metadata source (which adds its dir + "Enabled"
	// toggle under Settings → Metadata) plus its own settings entries.
	// Registered here (before the group fires) because they need
	// settingsManager/backendSystem, created after group registration.
	ctx.initGroupSystem.RegisterInitHelper(&arch.InitHelper{
		Group: arch.InitGroupAPI,
		Prio:  0,
		Start: func() {
			metadataapi.NewTMDBMetadata(ctx.propManager, bs, settingsManager, ctx.metadataManager)
		},
	})
	ctx.initGroupSystem.RegisterInitHelper(&arch.InitHelper{
		Group: arch.InitGroupAPI,
		Prio:  0,
		Start: func() {
			metadataapi.NewTVDBMetadata(ctx.propManager, bs, settingsManager, ctx.metadataManager)
		},
	})
	ctx.initGroupSystem.RegisterInitHelper(&arch.InitHelper{
		Group: arch.InitGroupAPI,
		Prio:  0,
		Start: func() {
			// C: lastfm_init — metadata_add_source("lastfm", ...) only;
			// the handler registers the source. db/key are unused here.
			h := lastfm.NewLastfmHandler(nil, lastfm.LastfmAPIKey, "", traceSystem, ctx.metadataManager)
			h.Start()
			// C: lastfm_load_albuminfo — invoked from mlp with the
			// pooled metadb connection.
			ctx.metadataManager.SetLastfmLoadAlbumInfo(func(dbc *db.DB, album, artist string) {
				_ = lastfm.NewLastfmHandler(dbc, lastfm.LastfmAPIKey, "", traceSystem, ctx.metadataManager).LoadAlbumInfo(album, artist)
			})
		},
	})

	// 32. init_group(InitGroupAPI)
	ctx.initGroupSystem.InitGroup(arch.InitGroupAPI)

	// Start HTTP control server (like C http_server_start)
	ctx.httpServer = httpnet.NewHTTPServer()
	// C: INITME(INIT_GROUP_API, torrent_stats_init) — /api/torrents
	// endpoint (group ran above; SetHTTPServer re-runs it now that the
	// server exists).
	ctx.btg.SetHTTPServer(ctx.httpServer)
	// C: INITME(INIT_GROUP_API, ecmascript_stats_init) — /api/ecmascript/
	// stats + /api/ecmascript/gc (es_stats.c:146-156; group ran above).
	ecmascript.EsSetHTTPServer(ctx.httpServer)
	ecmascript.EcmascriptStatsStart()

	// C: INITME(INIT_GROUP_API, prop_http_init) — /api/prop endpoint
	prophttp.SetDeps(ctx.propManager, ctx.httpServer, ctx.eventManager)
	prophttp.PropHTTPStart()
	hcHandler := httpcontrol.NewHTTPControlHandler(
		ctx.eventManager, // C: eventMgr for hc_action dispatch
		filepath.Join(persistentPath, "cache"),
		app.AppDataRoot(),
		"movian",
		"9.0.0",
		"movian",
		"movian",
	)
	hcHandler.SetPluginInstaller(pluginManager)

	// Wire /api/open?url=... to the navigator (mirrors C's event_to_ui → nav_eventsink → nav_open0)
	if ctx.navSystem != nil {
		navSys := ctx.navSystem
		hcHandler.SetOpenURLFunc(func(url string) {
			// C: nav_open() → event_dispatch(openurl) → the CURRENT
			// navigator (navigators/current), i.e. the UI-visible one.
			navSys.Open(url, "")
		})
	}

	// C: httpcontrol_init (httpcontrol.c:678-695) — register all canonical HTTP endpoints
	hcHandler.Register(ctx.httpServer)

	// MPRIS2 D-Bus endpoint — documented extension (upstream C has no
	// D-Bus support). Lets the DE route global media keys and show a
	// player widget. Non-fatal when no session bus exists.
	ctx.mprisServer = mpris.NewServer(ctx.eventManager, ctx.propManager, traceSystem)

	// C: http_path_add("/api/screenshot", NULL, hc_screenshot, 0) (screenshot.c:298)
	ctx.screenshotHandler = screenshot.NewScreenshotHandler(ctx.eventManager, persistentPath, "7c79b311d4797ed", traceSystem)
	ctx.httpServer.HTTPPathAdd("/api/screenshot", nil, func(hc *httpnet.HTTPConnection, remain string, opaque any, method httpnet.HTTPCmd) int {
		return ctx.screenshotHandler.Screenshot(hc, remain, opaque, method)
	}, false)

	// GAP-002: Register /api/stpp WebSocket endpoint
	// C: stpp.c:ws_init calls http_add_websocket("/api/stpp", ...)
	// with stpp_init, stpp_input, stpp_fini callbacks.
	// Go: We register the same endpoint with equivalent callbacks.
	// "stpp" stays for backwards compatibility; "/api/mgpp" is an alias.
	for _, wsPath := range []string{"/api/stpp", "/api/mgpp"} {
		ctx.httpServer.HTTPAddWebSocket(wsPath, nil,
			// wsSetup — called when a WebSocket client connects to /api/stpp
			// C: stpp_init creates a stpp_t, sets it as connection opaque,
			// and sets $global.stpp.remoteControlled = 1.
			func(hc *httpnet.HTTPConnection, pathOpaque any) int {
				apiMgpp := apimgpp.NewMgpp(ctx.propManager, traceSystem)
				apiMgpp.SetCourier(ctx.asyncIO.Courier())
				apiMgpp.Conn = hc
				hc.HTTPSetOpaque(apiMgpp)

				// C: prop_add_int(p, 1) — remoteControlled is a counter of
				// active controllee connections, not a boolean.
				gp := ctx.propManager.GetGlobal()
				if gp != nil {
					mgppProp := gp.GetChild("stpp")
					if mgppProp != nil {
						rcProp := mgppProp.GetChild("remoteControlled")
						if rcProp != nil {
							rcProp.AddInt(1)
						}
					}
				}
				return 0
			},
			// wsData — called when a WebSocket frame is received
			// C: stpp_input dispatches text frames to stpp_json and binary to stpp_binary
			func(hc *httpnet.HTTPConnection, opcode int, data []byte, dataLen int, connectionOpaque any) int {
				apiMgpp, ok := connectionOpaque.(*apimgpp.Mgpp)
				if !ok || apiMgpp == nil {
					return -1
				}

				switch opcode {
				case 1: // Text frame — JSON
					msg, err := htsmsg.DeserializeJSON(string(data[:dataLen]))
					if err == nil && msg != nil {
						_ = apimgpp.MgppHandleJSON(apiMgpp, msg)
					}
				case 2: // Binary frame
					_ = apimgpp.MgppHandleBinary(apiMgpp, nil, data[:dataLen])
				}
				return 0
			},
			// wsFini — called when the WebSocket disconnects
			// C: stpp_fini cleans up the stpp_t
			func(hc *httpnet.HTTPConnection, connectionOpaque any) {
				if apiMgpp, ok := connectionOpaque.(*apimgpp.Mgpp); ok && apiMgpp != nil {
					apimgpp.MgppFini(apiMgpp)
				}

				// C: prop_add_int(p, -1) — decrement connection counter
				gp := ctx.propManager.GetGlobal()
				if gp != nil {
					mgppProp := gp.GetChild("stpp")
					if mgppProp != nil {
						rcProp := mgppProp.GetChild("remoteControlled")
						if rcProp != nil {
							rcProp.AddInt(-1)
						}
					}
				}
			},
			// wsRemoved — called when the path is removed
			nil,
		)
	}

	listenErr := ctx.httpServer.Listen(42000)

	// C: #if STOS — asyncio_listen("http-server", 80, http_accept, NULL, 1)
	//    (http_server.c:1176-1178): extra listener on port 80, between the
	//    main listen and the fd check.
	if mgos.Enabled {
		if err := ctx.httpServer.ListenAdditional(80); err != nil {
			traceSystem.Error("HTTP", "HTTP port 80 listen failed: %v", err)
		}
	}

	if listenErr != nil {
		traceSystem.Error("HTTP", "Failed to start HTTP control server: %v", listenErr)
	} else {
		// C: http_server.c:1180-1185 — upnp_init after the server binds
		if !ctx.gconf.DisableUPnP {
			if ctx.netIfMgr == nil {
				ctx.netIfMgr = ifaddr.NewNetIfAddrManager(ctx.asyncIO)
			}
			upnpSys := upnp.NewSystem(42000, ctx.httpServer, ctx.serviceSystem,
				settingsManager, ctx.propManager, ctx.backendSystem,
				ctx.eventManager, ctx.playqueue,
				metadataManager, ctx.netIfMgr,
				ctx.calloutSystem, ctx.store, ctx.kvstore, traceSystem)
			upnpSys.SetGconf(ctx.gconf)
			// C: INITME(INIT_GROUP_ASYNCIO, NULL, ssdp_shutdown, 10)
			ctx.initGroupSystem.RegisterInitHelper(&arch.InitHelper{
				Group: arch.InitGroupAsyncIO,
				Prio:  10,
				Fini:  upnpSys.SsdpShutdown,
			})
		}
	}

	// C: stpp_init (stpp.c:1831-1836) — INIT_GROUP_API.
	// stpp_discover_init registers the "Allow remote control" setting
	// (which announces the controllee role), subscribes systemname and
	// opens per-interface discovery sockets.
	if ctx.mgppClient == nil {
		if ctx.netIfMgr == nil {
			ctx.netIfMgr = ifaddr.NewNetIfAddrManager(ctx.asyncIO)
		}
		ctx.mgppClient = mgpp.NewMGPPClient(ctx.propManager, settingsManager,
			ctx.serviceSystem, nil, ctx.netIfMgr)
		ctx.mgppClient.SetHTTPServerPort(42000)
		ctx.mgppClient.StartDiscovery()
		ctx.mgppClient.CreateSettings(settingsManager)
		mgpp.StartNetworking(ctx.netIfMgr, ctx.propManager)
		// C: BE_REGISTER(stpp) (stpp.c:1375)
		ctx.backendSystem.Register(&backendcore.Backend{
			CanHandle: ctx.mgppClient.BeMgppCanHandle,
			Open:      ctx.mgppClient.BeMgppOpen,
		})
		ctx.initGroupSystem.RegisterInitHelper(&arch.InitHelper{
			Group: arch.InitGroupAPI,
			Fini:  ctx.mgppClient.DiscoverFini,
		})
	}

	// Register AirPlay handler (mirrors C's airplay_init)
	airplayHandler := airplay.NewAirplayHandler(ctx.eventManager)
	airplayHandler.Register(ctx.httpServer)

	// Register I18N HTTP server for translation uploads
	i18nInstance.SetHTTPServer(ctx.httpServer)

	// 33. asyncio_start()
	// C: asyncio_thread runs init_group(INIT_GROUP_ASYNCIO) inside the
	// event loop (asyncio_posix.c:665) and net_refresh_network_status()
	// on network changes; shutdown_hook_add registers asyncio_shutdown.
	ctx.asyncIO.SetInitGroupFunc(ctx.initGroupSystem.InitGroup)
	ctx.asyncIO.SetFiniGroupFunc(ctx.initGroupSystem.FiniGroup)
	ctx.asyncIO.SetNetRefreshFunc(func() {
		mgpp.NetRefreshNetworkStatus(ctx.propManager)
	})
	ctx.asyncIO.SetShutdownHookAdd(func(
		fn func(opaque any, retcode int),
		opaque any, early int) {
		arch.ShutdownHookAdd(fn, opaque, early)
	})
	ctx.asyncIO.Start()

	// C: INITME(INIT_GROUP_ASYNCIO, ftp_server_init, NULL, 0) —
	// creates the "FTP server" settings and the asyncio listener.
	ctx.ftpServer = &ftpserver.Server{}
	ctx.ftpServer.SetGconf(ctx.gconf)
	ctx.initGroupSystem.RegisterInitHelper(&arch.InitHelper{
		Group: arch.InitGroupAsyncIO,
		Prio:  0,
		Start: func() {
			ctx.ftpServer.SetUsageEvent(func(key string, count int) {
				ctx.usageReporter.Event(key, count)
			})
			// C: smb.c calls usage_event("SMB connect") on auth'd connects
			ctx.smbSys.SetCalloutSystem(ctx.calloutSystem)
			ctx.smbSys.SetUsageEvent(func(key string, count int) {
				ctx.usageReporter.Event(key, count)
			})
			ctx.smbSys.SetGettext(nls.GetRString)
			ctx.ftpServer.FTPServerStart(settingsManager, ctx.asyncIO,
				ctx.faManager)
		},
	})

	// C: init_group(INIT_GROUP_ASYNCIO, ...) — runs INSIDE asyncio_thread
	// via asyncio.InitGroupHook (wired above); nothing to do here.

	// Initialize pending action channel for HTTP /api/input/action/ endpoint

}
