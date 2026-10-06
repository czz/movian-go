package main

import (
	"log"
	"os"
	"path/filepath"
	"runtime"

	uiglw "github.com/czz/movian-go/internal/ui/glw"

	"github.com/czz/movian-go/internal/gconf"

	"github.com/czz/movian-go/internal/arch"
	backendprop "github.com/czz/movian-go/internal/backend/prop"
	"github.com/czz/movian-go/internal/ecmascript"
	"github.com/czz/movian-go/internal/fileaccess/scanner"
	"github.com/czz/movian-go/internal/fileaccess/smb"
	"github.com/czz/movian-go/internal/htsmsg"
	"github.com/czz/movian-go/internal/sd"
	"github.com/czz/movian-go/internal/task"
	"github.com/czz/movian-go/internal/ui"

	// C links ext_subtitles.o/video_overlay.o/sub_ass.o unconditionally;
	// the Go port self-registers its mediacore hooks via init().
	textpkg "github.com/czz/movian-go/internal/text"
	"github.com/czz/movian-go/internal/usage"
)

func bootstrapPhase(ctx *appContext, gc *gconf.T) {
	// C: gconf_t gconf — owned by main; injected into every consumer
	// package (private cfg pointer per package, C-parity reads).
	arch.SetGconf(gc)
	ecmascript.SetGconf(gc)
	// C: ES module/native-class constructors ran pre-main; explicit call.
	ecmascript.RegisterBuiltinModules()
	ctx.smbSys = smb.NewSystem()
	ctx.smbSys.SetGconf(gc)

	ctx.sdSys = sd.NewSystem()
	ctx.sdSys.SetGconf(gc)
	ctx.indexer = scanner.NewIndexer()
	ctx.indexer.SetGconf(gc)
	ctx.textSys = textpkg.NewSystem()
	ctx.textSys.FontstashSetGconf(gc)
	// C: GLW_REGISTER_CLASS constructors ran pre-main; explicit call.
	uiglw.RegisterBuiltinClasses()
	uiglw.SetGconf(gc)
	uiglw.SetTextSystem(ctx.textSys)

	// C: the usage.c singleton — created once, injected into every
	// subsystem that calls usage_event()/usage_page_open().
	ctx.usageReporter = usage.New(&ctx.usageCfg)

	// C: linux_init() (linux_misc.c:156-161) — runs in main() after
	// parse_opts, before main_init: get_device_id() (eth0 MAC-MD5 →
	// gconf.device_id), linux_trap_init(), gconf.concurrency =
	// get_system_concurrency() (sched_getaffinity count → NumCPU).
	// Platform early init (device id → gconf.device_id, trap init,
	// gconf.concurrency) — per-OS implementations in init_*.go:
	// linux → linux_init (linux_misc.c:156-161), darwin → osx_app.m
	// main(), windows → modeled on the osx branch (MachineGuid),
	// android → set inside coreInit (android.c).
	platformEarlyStart(ctx)

	projectRoot, err := os.Getwd()
	if err != nil {
		exe, exeErr := os.Executable()
		if exeErr != nil {
			projectRoot = "."
		} else {
			projectRoot = filepath.Dir(exe)
		}
	}

	// If the current working directory doesn't contain the skins directory,
	// fall back to the executable's directory. This allows the app to be run
	// from any directory while still finding its data files (skins, fonts, etc.).
	// C: app_dataroot() returns "./" in wd.c, but C builds with
	// SHOWTIME_DATADIR as a compile-time absolute path. Go: detect at runtime.
	if _, statErr := os.Stat(filepath.Join(projectRoot, "glwskins", "flat")); os.IsNotExist(statErr) {
		if exe, exeErr := os.Executable(); exeErr == nil {
			exeDir := filepath.Dir(exe)
			if _, statErr2 := os.Stat(filepath.Join(exeDir, "glwskins", "flat")); statErr2 == nil {
				projectRoot = exeDir
			}
		}
	}
	// C: posix.c:148-155 — persistent path defaults to
	// $HOME/.movian-go (upstream keeps the legacy ".hts/showtime").
	// Cache path defaults to $HOME/.cache/movian-go (APPNAME).
	// Override with --persistent command line option.
	// Android: coreInit sets gconf.persistent_path = "persistent://"
	// (android.c:325) — a fileaccess URL, not a filesystem path.
	// --persistent/--cache (or the seeded posix defaults) win, like C's
	// parse_opts overriding posix_init; platform fallbacks only apply
	// when nothing was set (windows/android don't seed HOME paths).
	persistentPath := ctx.gconf.PersistentPath
	if persistentPath == "" {
		if runtime.GOOS == "android" {
			persistentPath = "persistent://"
		} else if runtime.GOOS == "windows" {
			// No upstream C counterpart; %APPDATA%\MovianGo is the
			// Windows equivalent of $HOME/.movian-go.
			if appdata := os.Getenv("APPDATA"); appdata != "" {
				persistentPath = filepath.Join(appdata, "MovianGo")
			} else {
				persistentPath = filepath.Join(projectRoot, "persistent")
			}
			// Windows cache belongs in %LOCALAPPDATA% — %APPDATA% is
			// Roaming and gets synced with domain profiles; a large
			// blobcache/sqlite there would roam pointlessly.
			if ctx.gconf.CachePath == "" {
				if local := os.Getenv("LOCALAPPDATA"); local != "" {
					ctx.gconf.CachePath = filepath.Join(local, "MovianGo", "cache")
				}
			}
		} else {
			// Defensive: no HOME, no flag (dev mode)
			persistentPath = filepath.Join(projectRoot, "persistent")
		}
	}
	// Movian Go: default data dirs were renamed (legacy
	// $HOME/.hts/showtime → $HOME/.movian-go, $HOME/.cache/movian →
	// $HOME/.cache/movian-go, %APPDATA%\Movian → %APPDATA%\MovianGo).
	// When the configured path is still the default and only the
	// legacy dir exists, it is renamed; every outcome is logged and
	// surfaced to the user via a popup notification once the
	// notification manager exists (ctx.dataMigrations).
	persistentPath = ctx.migrateDataDirs(persistentPath)
	// gconf.cache_path must never be empty (sqlite3_temp_directory,
	// blobcache — blobcache_file.c:131). Windows/android seed it via
	// platform init; fall back to a dir under the persistent path.
	if ctx.gconf.CachePath == "" {
		ctx.gconf.CachePath = filepath.Join(persistentPath, "cache")
	}
	ctx.persistentPath = persistentPath
	if runtime.GOOS != "android" {
		if err := os.MkdirAll(filepath.Join(persistentPath, "kvstore"), 0755); err != nil {
			log.Printf("[MAIN] Warning: failed to create kvstore dir: %v\n", err)
		}
		if err := os.MkdirAll(filepath.Join(persistentPath, "metadb"), 0755); err != nil {
			log.Printf("[MAIN] Warning: failed to create metadb dir: %v\n", err)
		}
	}
	// C: gconf.persistent_path — used by persistent_load/write/remove
	// (htsmsg/persistent_file.c) and htsmsg_store.c
	// C: gconf.persistent_path — plugin storage root used by
	// ecmascript_plugin_load ("<persistent_path>/plugins/<id>",
	// plugins.c:817/1492).
	ctx.gconf.PersistentPath = persistentPath
	// Webview cookie jar (webkit2gtk) persisted under the app data dir —
	// challenge/login sessions survive restarts like the C-era cookies.
	ui.WebpopupSetStorage(filepath.Join(persistentPath, "webkit"))
	// C: htsmsg_store_init(persistent_path) — the store is injected into
	// each consumer (settings writeback, bookmarks, keyring, indexer…).
	// C: INITIALIZER(taskinit) — the task pool's statics exist before
	// main() runs; create it first so every consumer can be wired.
	ctx.taskSystem = task.NewTaskSystem()

	// C: backend_prop.c static proppages — exists before settings_init;
	// init owns it and injects into settings/backend/plugins.
	ctx.propPageManager = backendprop.NewPropPageManager()

	ctx.store = htsmsg.NewStore("")
	ctx.store.SetGconf(ctx.gconf)

	// C: app_dataroot() — dev build (wd.c) returns "./", installed build
	// (datadir.c) returns compile-time SHOWTIME_DATADIR (= app.DataDir
	// injected by `make install-build DATADIR=...`).
}

// dataMigration records a legacy → new data-dir rename (or its
// failure) so a popup notification can be shown once the notification
// manager is wired (notifications_init happens after bootstrap).
type dataMigration struct {
	what     string // "data" / "cache"
	oldPath  string
	newPath  string
	err      error // rename failed → old dir kept in use (persistent) or new empty dir (cache)
	leftover bool  // new dir already existed; old one left untouched
}

// migrateDataDirs renames legacy data dirs to their movian-go
// counterparts when the configured paths are still the defaults
// (--persistent/--cache overrides are untouched). Returns the
// effective persistent path.
func (ctx *appContext) migrateDataDirs(persistentPath string) string {
	var oldPersist, defPersist, oldCache, defCache string
	switch runtime.GOOS {
	case "android":
		return persistentPath
	case "windows":
		appdata, local := os.Getenv("APPDATA"), os.Getenv("LOCALAPPDATA")
		if appdata != "" {
			defPersist = filepath.Join(appdata, "MovianGo")
			oldPersist = filepath.Join(appdata, "Movian")
		}
		if local != "" {
			defCache = filepath.Join(local, "MovianGo", "cache")
			oldCache = filepath.Join(local, "Movian", "cache")
		}
	default:
		home := os.Getenv("HOME")
		if home == "" {
			return persistentPath
		}
		defPersist = filepath.Join(home, ".movian-go")
		oldPersist = filepath.Join(home, ".hts", "showtime")
		defCache = filepath.Join(home, ".cache", "movian-go")
		oldCache = filepath.Join(home, ".cache", "movian")
	}

	if persistentPath == defPersist && defPersist != "" {
		persistentPath = ctx.migrateOneDir("data", oldPersist, defPersist, true)
	}
	if ctx.gconf.CachePath == defCache && defCache != "" {
		ctx.migrateOneDir("cache", oldCache, defCache, false)
	}
	return persistentPath
}

// migrateOneDir renames old→new when new does not exist and old does.
// keepOldOnErr controls the failure fallback: persistent data keeps
// using the old dir; a cache can be rebuilt at the new location.
// Returns the path to actually use.
func (ctx *appContext) migrateOneDir(what, old, def string, keepOldOnErr bool) string {
	if _, err := os.Stat(def); err == nil {
		if _, err := os.Stat(old); err == nil {
			ctx.dataMigrations = append(ctx.dataMigrations,
				dataMigration{what, old, def, nil, true})
			log.Printf("[MAIN] %s directory %s is in use; legacy %s still exists and was left untouched",
				what, def, old)
		}
		return def
	}
	if _, err := os.Stat(old); err != nil {
		return def
	}
	if err := os.Rename(old, def); err != nil {
		ctx.dataMigrations = append(ctx.dataMigrations,
			dataMigration{what, old, def, err, false})
		log.Printf("[MAIN] Failed to migrate %s directory %s -> %s: %v", what, old, def, err)
		if keepOldOnErr {
			return old
		}
		return def
	}
	ctx.dataMigrations = append(ctx.dataMigrations,
		dataMigration{what, old, def, nil, false})
	log.Printf("[MAIN] Migrated %s directory %s -> %s", what, old, def)
	// Tidy up the now-empty parent (e.g. $HOME/.hts) — fails silently
	// if non-empty, which is fine.
	os.Remove(filepath.Dir(old))
	return def
}
