package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/czz/movian-go/internal/app"
	"github.com/czz/movian-go/internal/backend"
	backendcore "github.com/czz/movian-go/internal/backend/core"
	"github.com/czz/movian-go/internal/blobcache"
	"github.com/czz/movian-go/internal/db"
	"github.com/czz/movian-go/internal/db/kvstore"
	"github.com/czz/movian-go/internal/drm/clearkey"
	"github.com/czz/movian-go/internal/drm/widevine"
	"github.com/czz/movian-go/internal/ecmascript"
	"github.com/czz/movian-go/internal/keyring"
	"github.com/czz/movian-go/internal/metadata"
	"github.com/czz/movian-go/internal/notifications"
	"github.com/czz/movian-go/internal/service"
	"github.com/czz/movian-go/internal/subtitles"

	// C links ext_subtitles.o/video_overlay.o/sub_ass.o unconditionally;
	// the Go port self-registers its mediacore hooks via init().
	subtitlesext "github.com/czz/movian-go/internal/subtitles/ext"
)

func wireStorage(ctx *appContext) {
	traceSystem := ctx.traceSystem
	settingsManager := ctx.settingsManager
	persistentPath := ctx.persistentPath

	// 9. db_init() — C: db_init (db_support.c:719-734) sets
	// sqlite3_temp_directory = gconf.cache_path, configures the log
	// callback, and calls sqlite3_initialize(). No return value.
	db.DBStart(ctx.gconf.CachePath)

	// 10. blobcache_init() — SOFT
	// C: blobcache lives under gconf.cache_path (blobcache_file.c:131,178)
	blobcacheSystem := blobcache.NewBlobcacheSystem(ctx.gconf.CachePath)
	// C: blobcache_init registers "Clear cached files" under
	// setting_get_dir("general:resets") and calls notify_add on clear.
	blobcacheSystem.SetSettingsMgr(settingsManager)
	blobcacheSystem.SetNotifyAdd(func(notifyType int, icon string, delay int, format string, args ...any) {
		if ctx.notifMgr != nil {
			ctx.notifMgr.NotifyAdd(nil, notifications.NotifyType(notifyType), icon, delay, format, args...)
		}
	})
	if err := blobcacheSystem.Start(); err != nil {
		traceSystem.Error("core", "Failed to initialize blobcache: %v", err)
	}
	// Wire the cache into its consumers (C: implicit blobcache global).
	ctx.faManager.SetBlobCache(blobcacheSystem.Cache())
	ecmascript.EsSetBlobCache(blobcacheSystem.Cache())
	// C: main.c:425
	traceSystem.Debug("core", "Persistent path: %s", persistentPath)

	// 11. kvstore_init() — C-equivalent
	// C: kvstore_init() opens persistent/kvstore/kvstore.db, creates pool,
	// runs schema migration. On failure: kvstore_pool = NULL (no-op).
	ctx.kvstore = kvstore.NewKVStore(persistentPath, app.AppDataRoot(), ctx.calloutSystem, ctx.faManager)
	ctx.kvstore.SetGconf(ctx.gconf)
	// Wire kvstore into every owner created before this point
	// (C: kvstore_* are globals; consumers read them lazily, so late
	// injection is equivalent).
	settingsManager.SetKVStore(ctx.kvstore)
	ecmascript.EsSetKVStore(ctx.kvstore)
	ecmascript.EsSetTaskSystem(ctx.taskSystem)

	// DRM key systems — Go extension (upstream Movian has no DRM).
	// ClearKey is always available; Widevine L3 registers only when
	// the user dropped device credentials at <persistent>/drm/
	// device.wvd — nothing is shipped or generated.
	clearkey.New()
	if !strings.Contains(persistentPath, "://") {
		wvdPath := filepath.Join(persistentPath, "drm", "device.wvd")
		if _, err := widevine.Open(wvdPath); err == nil {
			traceSystem.Info("DRM", "Widevine L3 CDM loaded (%s)", wvdPath)
		} else if !os.IsNotExist(err) {
			traceSystem.Error("DRM", "Widevine device load failed: %v", err)
		}
	}

	// 12. C main.c:438-444 — metadata_init() [= mlp_init +
	//     metadata_sources_init], metadb_init(), decoration_init().
	//     metadata_sources_init needs the settings manager (it calls
	//     settings_add_dir), so SetSettingsManager must run first.
	ctx.metadataManager = metadata.NewMetadataManager(ctx.propManager, ctx.notifMgr)
	metadataManager := ctx.metadataManager
	ctx.indexer.SetIndexerMetadataManager(metadataManager)
	metadataManager.SetSettingsManager(settingsManager)
	metadataManager.SetKVStore(ctx.kvstore)
	metadataManager.SetFAM(ctx.faManager)
	metadataManager.SetTraceSystem(traceSystem)
	metadataManager.MetadataStart() // C: mlp_init
	metadataManager.MetadataSourcesStart()
	if err := ctx.metadataManager.Start(persistentPath, app.AppDataRoot()); err != nil {
		traceSystem.Error("core", "Failed to initialize metadb: %v", err)
	}
	metadata.DecorationStart(ctx.propManager, settingsManager)

	// 13. subtitles_init() — C: subtitles.c:873-887. The settings-layer
	// calls C compiles inside subtitles.c (settings_add_dir,
	// setting_create, setting_destroy, _p, prop_create_root) are
	// C: subtitles.c calls settings.c (settings_add_dir, setting_create,
	// setting_destroy, _p) and prop_create_root directly — Go injects the
	// manager instances via SetDeps. subtitles_probe stays a hook:
	// ext_subtitles.c (pkg/subtitles/ext) imports pkg/subtitles, so the
	// reverse import would cycle.
	ctx.subSys = subtitles.NewSystem(settingsManager, ctx.propManager, ctx.store)
	ctx.subSys.SetTextSystem(ctx.textSys)
	ctx.subSys.Hooks.SubtitlesProbe = func(u string) string {
		return subtitlesext.SubtitlesProbe(ctx.faManager, u)
	}

	ctx.subSys.SubtitlesStart()
	ctx.metadataManager.SetSubtitleSystem(ctx.subSys)
	ctx.esSys = ecmascript.NewSystem()
	ecmascript.EsSetSystem(ctx.esSys)
	ecmascript.EsSetSubtitleSystem(ctx.subSys)

	// 14. keyring_init()
	ctx.keyring = keyring.New(ctx.propManager, ctx.notifMgr, settingsManager, ctx.store)

	// Frontend-specific: canonical path (x11) wires pkg/ui/glw seams
	// whose deps already exist (prop manager, settings manager, fa
	// manager, kvstore). No-op in non-x11 builds (no GLW backend).
	glwFrontendEarlyStart(ctx, settingsManager)

	// C: the service system and backend system objects must exist before
	// INIT_GROUP_GRAPHICS fires — fontstash_init (INITME) registers its
	// backend via BE_REGISTER(fontstash) and adds the "Fonts" concat
	// source to settings_look_and_feel before glw_settings_init runs.
	// The Start() calls still happen at their canonical spots below
	// (service_init/backend_init after media_init).
	ctx.serviceSystem = service.NewServiceSystem(ctx.propManager)
	ctx.serviceSystem.SetTraceSystem(traceSystem)
	ctx.backendRegistry = backend.NewBackendRegistry()
	bs := backendcore.NewBackendSystem(ctx.propManager, ctx.serviceSystem, traceSystem, ctx.usageReporter)
	bs.SetGconf(ctx.gconf)
	bs.SetKeyring(ctx.keyring)
	ecmascript.EsSetKeyring(ctx.keyring)
	ecmascript.EsSetNotifMgr(ctx.notifMgr)
	ecmascript.EsSetMetadataManager(ctx.metadataManager)
	ctx.backendSystem = bs
	bs.SetPlaybackInfoFn(videoPlaybackInfoInvoke)
}
