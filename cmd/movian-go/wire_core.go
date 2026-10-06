package main

import (
	"log"
	"os"
	"runtime"

	medialibav "github.com/czz/movian-go/internal/media/libav"

	"github.com/czz/movian-go/internal/gconf"

	"github.com/czz/movian-go/internal/app"
	"github.com/czz/movian-go/internal/arch"
	"github.com/czz/movian-go/internal/asyncio"
	"github.com/czz/movian-go/internal/backend/bittorrent"
	"github.com/czz/movian-go/internal/callout"
	"github.com/czz/movian-go/internal/db"
	"github.com/czz/movian-go/internal/ecmascript"
	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/fileaccess/smb"
	"github.com/czz/movian-go/internal/htsmsg"
	"github.com/czz/movian-go/internal/ipc"
	"github.com/czz/movian-go/internal/networking/tcpcon"
	"github.com/czz/movian-go/internal/notifications"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/settings"
	devsettings "github.com/czz/movian-go/internal/settings/dev" // init_dev_settings hook (C: settings_init tail)
	"github.com/czz/movian-go/internal/version"
	decoderpkg "github.com/czz/movian-go/internal/video/decoder"

	// C links ext_subtitles.o/video_overlay.o/sub_ass.o unconditionally;
	// the Go port self-registers its mediacore hooks via init().
	"github.com/czz/movian-go/internal/trace"
	"github.com/czz/movian-go/internal/unicode"
)

func wireCore(ctx *appContext, gc *gconf.T, cleanupOnFail func()) {
	// 1. unicode_init() — CRITICAL (C: main.c:382)
	unicodeSystem := unicode.NewUnicodeSystem()
	if err := unicodeSystem.Start(); err != nil {
		log.Printf("[MAIN] CRITICAL: Failed to initialize unicode: %v\n", err)
		cleanupOnFail()
		os.Exit(1)
	}

	// 2. prop_init() + init_global_info() — CRITICAL
	ctx.propManager = propcore.NewPropManager()
	ctx.propManager.SetGconf(ctx.gconf)
	ctx.propManager.Start()
	ctx.propManager.SetStore(ctx.store)
	ctx.propManager.SetReorderBridge(htsmsg.PropReorderStoreSave,
		htsmsg.PropReorderStoreLoad)
	// C: init_global_info (main.c:145-155) — name/version/fullversion/copyright
	appProp := ctx.propManager.CreateEx(ctx.propManager.GetGlobal(), "app", nil, false, true)
	ctx.propManager.CreateEx(appProp, "name", nil, false, true).SetString(app.AppNameUser)
	ctx.propManager.CreateEx(appProp, "version", nil, false, true).SetString(version.AppVersion())
	ctx.propManager.CreateEx(appProp, "fullversion", nil, false, true).SetString(version.AppVersion())
	ctx.propManager.CreateEx(appProp, "copyright", nil, false, true).SetString("© 2006 - 2018 Lonelycoder AB")

	// 3. callout_init()
	ctx.calloutSystem = callout.NewCalloutSystem(ctx.propManager)
	ctx.usageReporter.SetCalloutSystem(ctx.calloutSystem)
	ctx.usageReporter.SetTaskSystem(ctx.taskSystem)

	// 4. asyncio_init_early() + init_group(InitGroupNet)
	ctx.asyncIO = asyncio.NewAsyncIO()
	ctx.asyncIO.StartEarly()
	ctx.btg = bittorrent.NewBtGlobal(ctx.asyncIO)
	ctx.btg.SetGconf(gc)
	ecmascript.EsSetAsyncIO(ctx.asyncIO)
	// C: gconf.bypass_ecmascript_acl / gconf.load_ecmascript — parsed by
	// parse_opts, consumed by es_fs / ecmascript_init (main.c:660-666)

	ctx.initGroupSystem = arch.NewInitGroupSystem()
	ctx.nmbManager = smb.NewNMBManager(ctx.smbSys)
	// C: INITME(INIT_GROUP_NET, nmb_resolver_init, NULL, 0) — installs the
	// resolver worker and wires nmb_resolve into tcp_connect.
	ctx.initGroupSystem.RegisterInitHelper(&arch.InitHelper{
		Group: arch.InitGroupNet,
		Prio:  0,
		Start: func() {
			ctx.nmbManager.NMBResolverStart(ctx.asyncIO, ctx.taskSystem)
			tcpcon.SetNMBResolver(ctx.nmbManager.NMBResolve)
		},
	})
	// C: INITME(INIT_GROUP_ASYNCIO, nmb_init, NULL, 0) — UDP socket,
	// MSBROWSE/flush timers, first broadcast query.
	ctx.initGroupSystem.RegisterInitHelper(&arch.InitHelper{
		Group: arch.InitGroupAsyncIO,
		Prio:  0,
		Start: func() { ctx.nmbManager.NMBStart(ctx.serviceSystem) },
	})
	// C: INITME(INIT_GROUP_ASYNCIO, asyncio_http_init, NULL, 0) —
	// registers the completed-request delivery worker.
	ctx.initGroupSystem.RegisterInitHelper(&arch.InitHelper{
		Group: arch.InitGroupAsyncIO,
		Prio:  0,
		Start: func() { ctx.faManager.AsyncioHTTPStart(ctx.asyncIO) },
	})
	// C: INITME(INIT_GROUP_API, ecmascript_init, ecmascript_fini, 0) —
	// loads gconf.load_ecmascript into a "cmdline" context; fini destroys
	// all contexts' permanent resources.
	ctx.initGroupSystem.RegisterInitHelper(&arch.InitHelper{
		Group: arch.InitGroupAPI,
		Prio:  0,
		Start: func() { ecmascript.EcmascriptStart(ctx.usageReporter) },
		Fini:  ecmascript.EcmascriptFini,
	})
	// C: INITME(INIT_GROUP_API, usage_init, usage_fini, 0) — usage_init is
	// empty in C; usage_fini disarms the callout and sends end_session.
	ctx.initGroupSystem.RegisterInitHelper(&arch.InitHelper{
		Group: arch.InitGroupAPI,
		Prio:  0,
		Fini:  ctx.usageReporter.Fini,
	})
	// C: INITME(INIT_GROUP_API, linux_process_monitor_init, NULL, 0) —
	// creates the "system" prop root (cpuinfo/mem) and starts the
	// 1s cpu_monitor_do/meminfo_do timer.
	ctx.initGroupSystem.RegisterInitHelper(&arch.InitHelper{
		Group: arch.InitGroupAPI,
		Prio:  0,
		Start: func() {
			platformProcessMonitorStart(ctx)
		},
	})
	// C: INITME(INIT_GROUP_NET, torrent_early_init) +
	// INITME(INIT_GROUP_ASYNCIO, torrent_asyncio_init / tracker_init /
	// tracker_udp_init) + INITME(INIT_GROUP_API, torrent_stats_init).
	// Must register BEFORE InitGroupNet fires (below) — torrent_early_init
	// sets peer limits and installs the pendings/boot/metainfo workers.
	ctx.btg.RegisterBittorrentStart(ctx.initGroupSystem)

	ctx.initGroupSystem.InitGroup(arch.InitGroupNet)

	// 5. trace_init() — C: trace.c uses gconf.cache_path for the rotating
	// log dir and APPNAME for the file prefix (trace.c:309-330)
	traceSystem := trace.NewTraceSystem(ctx.propManager)
	// C: tracev() calls trace_arch() for every line (trace.c) —
	// android writes to __android_log_print. On desktop stderr is
	// already emitted by the traceLevel-gated branch of Tracev; the
	// desktop TraceArch would re-enter TraceSystem.Debug and deadlock
	// on traceMutex, so the platform logger stays android-only.
	if runtime.GOOS == "android" {
		traceSystem.SetPlatformLogger(arch.TraceArch)
	}
	traceSystem.Start(ctx.gconf.CachePath, "movian")
	// C: trace.c:306-307 — trace_init defaults gconf.trace_level to
	// TRACE_INFO; -d set TRACE_DEBUG earlier (main.c:579). Without the
	// default, level 0 would filter out every non-EMERG trace line.
	if ctx.gconf.TraceLevel == 0 {
		ctx.gconf.TraceLevel = trace.TRACE_INFO
	}
	traceSystem.SetLevel(ctx.gconf.TraceLevel)
	ctx.traceSystem = traceSystem
	// C: trace() is a process-wide file static in trace.c — every
	// subsystem that owned a C static trace path gets the injected
	// instance.
	ctx.btg.BtSetTraceSystem(traceSystem)
	ctx.textSys.FreetypeSetTraceSystem(traceSystem)
	ecmascript.EsSetTraceSystem(traceSystem)
	ctx.smbSys.SetTraceSystem(traceSystem)
	ctx.indexer.SetIndexerTraceSystem(traceSystem)
	db.SetTraceSystem(traceSystem)
	arch.SetTraceSystem(traceSystem)
	ctx.asyncIO.SetTraceSystem(traceSystem)
	decoderpkg.SetTraceSystem(traceSystem)
	medialibav.SetTraceSystem(traceSystem)
	ctx.ipc = ipc.New(gc, traceSystem)
	// C: trace.c:344-349 — startup banner inside trace_init
	osInfo := ctx.gconf.OSInfo
	if osInfo == "" {
		osInfo = "<unknown>"
	}
	// C: main.c:797 — TRACE("SYSTEM", APPNAMEUSER" %s starting ...")
	traceSystem.Info("SYSTEM", "%s %s starting. %d CPU cores. Systemtype:%s OS:%s",
		app.AppNameUser, version.AppVersion(), ctx.gconf.Concurrency,
		arch.GetSystemType(), osInfo)

	// 6. prop_init_late()
	ctx.propManager.StartPropertySystemLate()

	// C: FAP_REGISTER() sections statically register all protocols at
	// program start — persistent_load (htsmsg_store) resolves file://
	// through them during settings_init, long before fileaccess_init's
	// fap_init hooks run at backend_init. The Go equivalent: register
	// protocols + install the default FAM now, run fap_init later in
	// faManager.Start(). Doing the full Start() lazily under the htsmsg
	// store mutex deadlocks (http_init → load_cookies → same store).
	ctx.faManager = fileaccesscore.NewFileAccessManager(ctx.propManager, ctx.usageReporter)
	ctx.faManager.SetDataRoot(app.AppDataRoot())
	ctx.faManager.SetGconf(ctx.gconf)
	ctx.faManager.RegisterProtocols()
	// C: filebundle constructors (mkbundle-generated objects) register
	// before main() — the Go equivalent runs right after the fam is
	// created so fonts/shaders/schemas resolve bundle:// from the start.
	registerFileBundles(ctx.faManager.GetBundleManager())
	ctx.faManager.SetStore(ctx.store)
	ctx.faManager.SetCalloutSystem(ctx.calloutSystem)
	ctx.faManager.SetTaskSystem(ctx.taskSystem)
	ctx.faManager.SetTraceSystem(traceSystem)
	ctx.store.SetFAM(ctx.faManager)
	ctx.usageReporter.SetFileAccessManager(ctx.faManager)
	ctx.btg.BtSetFAM(ctx.faManager)
	ctx.textSys.FreetypeSetFAM(ctx.faManager)
	ecmascript.EsSetFAM(ctx.faManager)

	// C: usage_event / usage_page_open are global functions (usage.c)
	// reached by every subsystem — ctx.usageReporter is injected into each
	// consumer at construction.

	// C: gconf.enable_bin_replace / gconf.enable_omnigrade (main.h:234-235)
	// — gconf_t fields shared between init_dev_settings (write) and
	// upgrade (read). The ints are owned here; both sides get pointers.
	// C: VPI_REGISTER(libcec_vpi) — only under CONFIG_LIBCEC
	if ctx.ipc.LibcecVpiHandler != nil {
		vpiHandlers = append(vpiHandlers, ctx.ipc.LibcecVpiHandler)
	}
	// C: video_playback_info_invoke walks the vpi_handlers list

	// 7. settings_init()
	settingsManager := settings.NewSettingsManager(ctx.propManager)
	settingsManager.SetGconf(ctx.gconf)
	settingsManager.SetTraceSystem(traceSystem)
	ctx.settingsManager = settingsManager
	settingsManager.SetStore(ctx.store)
	settingsManager.SetPropPageManager(ctx.propPageManager)
	settingsManager.SetDevSettingsStart(func(sm *settings.SettingsManager,
		dev *propcore.Prop) {
		devsettings.Start(sm, dev, &devsettings.Deps{
			BinReplace: &ctx.enableBinReplace,
			Omnigrade:  &ctx.enableOmnigrade,
			UsageCfg:   &ctx.usageCfg,
			TS:         traceSystem,
			CecDebug:   ctx.ipc.SetEnableCecDebug,
			Gconf:      ctx.gconf,
		})
	})
	settingsManager.Start()
	// C: torrent_settings_init (INIT_GROUP_ASYNCIO) uses the global
	// settings context — injected here (blobcache.SetSettingsMgr pattern).
	ctx.btg.SetSettingsManager(settingsManager, ctx.propManager)

	// 8. notifications_init()
	ctx.notifMgr = notifications.NewNotificationManager(ctx.propManager, ctx.store, ctx.calloutSystem, traceSystem)

	// Surface the data-dir migrations performed by bootstrap
	// (movian-go rebrand) — trace already logged them early.
	for _, m := range ctx.dataMigrations {
		switch {
		case m.err != nil:
			ctx.notifMgr.NotifyAdd(nil, notifications.NotifyWarning, "", 10,
				"Could not migrate %s directory %s to %s (%v) — the previous location is still in use",
				m.what, m.oldPath, m.newPath, m.err)
		case m.leftover:
			ctx.notifMgr.NotifyAdd(nil, notifications.NotifyInfo, "", 10,
				"%s directory is now %s — the old %s still exists and can be removed",
				m.what, m.newPath, m.oldPath)
		default:
			ctx.notifMgr.NotifyAdd(nil, notifications.NotifyInfo, "", 10,
				"%s directory migrated to %s", m.what, m.newPath)
		}
	}
	// C: main.c:404-406
	traceSystem.Debug("core", "Loading resources from %s", app.AppDataRoot())
	traceSystem.Debug("core", "Cache path: %s", ctx.gconf.CachePath)
}
