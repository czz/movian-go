package metadata

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/czz/movian-go/internal/db/kvstore"
	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"

	"github.com/czz/movian-go/internal/db"
	"github.com/czz/movian-go/internal/notifications"
	propcore "github.com/czz/movian-go/internal/prop"
	settingscore "github.com/czz/movian-go/internal/settings"
	"github.com/czz/movian-go/internal/subtitles"
	"github.com/czz/movian-go/internal/trace"
)

// MetadataManager manages all metadata operations
type MetadataManager struct {
	initialized bool

	// kvstore — C: the global kvstore_* functions (kvstore.c).
	kvstore *kvstore.KVStore

	// fam — C: the implicit global fa context (load_nfo, DBUpgradeSchema).
	fam *fileaccesscore.FileAccessManager

	// ts — C: trace() global on trace.c file statics.
	ts *trace.TraceSystem

	// subSys — C: subtitles.c statics (subtitles_embedded_score /
	// autosel read by metadataStreamMakeProp). Injected.
	subSys *subtitles.System

	// lastfmLoadAlbumInfo — C: lastfm_load_albuminfo direct call from
	// mlp.c (import-cycle seam: api/lastfm imports metadata).
	lastfmLoadAlbumInfo func(dbc *db.DB, album, artist string)

	// Database pool
	dbPool  atomic.Pointer[db.DBPool]
	dbOnce  sync.Once
	dbMutex sync.Mutex

	// Lazy loading queue
	mlpQueue           []*MetadataLazyProp
	mlpMutex           sync.Mutex
	mlpCond            *sync.Cond
	metadataNumThreads int

	// C: metadata_sources_mutex + metadata_sources[] +
	// metadata_sources_settings[] (metadata_sources.c:45-48) —
	// priority-ordered source queue + root settings prop per type.
	msMu       sync.Mutex
	msSources  [MetadataTypeNum][]*MetadataSource
	msSettings [MetadataTypeNum]*propcore.Prop

	// Play info hash
	mipMutex sync.Mutex
	mipHash  [311][]*metadbItemProp

	// Video metadata cache
	videoCacheMutex sync.RWMutex
	videoCache      map[int64]*Metadata

	pm *propcore.PropManager

	// Settings manager (C: settings subsystem, for items_clear action)
	settingsMgr *settingscore.SettingsManager

	// C: notify_add / message_popup globals — injected
	nm *notifications.NotificationManager

	// Decoration courier for async metadata processing
	decorationCourier any
}

// NewMetadataManager creates a new metadata manager
// SetKVStore injects the kvstore handle (C: global kvstore_* funcs).
// SetSubtitleSystem injects the subtitles subsystem (C: subtitles.c
// statics reached by metadataStreamMakeProp).
func (mm *MetadataManager) SetSubtitleSystem(s *subtitles.System) { mm.subSys = s }

func (mm *MetadataManager) SetKVStore(kvs *kvstore.KVStore) { mm.kvstore = kvs }

// SetFAM injects the file access manager (C: implicit global fa context).
func (mm *MetadataManager) SetFAM(fam *fileaccesscore.FileAccessManager) { mm.fam = fam }

// SetTraceSystem injects the trace system (C: trace() global).
func (mm *MetadataManager) SetTraceSystem(ts *trace.TraceSystem) { mm.ts = ts }

// SetLastfmLoadAlbumInfo wires lastfm_load_albuminfo (C: direct call
// from mlp.c — provider api/lastfm imports this package: cycle seam).
func (mm *MetadataManager) SetLastfmLoadAlbumInfo(
	fn func(dbc *db.DB, album, artist string)) {
	mm.lastfmLoadAlbumInfo = fn
}

func NewMetadataManager(pm *propcore.PropManager,
	nm *notifications.NotificationManager) *MetadataManager {
	mm := &MetadataManager{
		nm:         nm,
		mlpQueue:   make([]*MetadataLazyProp, 0),
		videoCache: make(map[int64]*Metadata),
		pm:         pm,
	}
	mm.mlpCond = sync.NewCond(&mm.mlpMutex)
	return mm
}

// SetSettingsManager sets the settings manager used to register the
// "Clear all metadata" action (C: metadb_init's settings_create_action)
func (mm *MetadataManager) SetSettingsManager(sm *settingscore.SettingsManager) {
	mm.settingsMgr = sm
}

// Start initializes the metadata database
func (mm *MetadataManager) Start(persistentPath string, dataRoot string) error {
	var setupErr error
	mm.dbOnce.Do(func() {
		// C: metadb_init (metadb.c:107-113) — snprintf("%s/metadb",
		// gconf.persistent_path) + fa_makedir + db_open on the fa URL.
		// Paths stay in URL form (persistent:///…); os.MkdirAll would
		// mangle "persistent://" into "persistent:".
		// C: fa_makedir(buf) — return value ignored upstream;
		// db_open fails later on a real error anyway.
		metadbDir := persistentPath + "/metadb"
		fileaccesscore.FAMakedir(mm.fam, metadbDir)

		metadbPath := metadbDir + "/meta.db"
		schemaDir := dataRoot + "/res/metadb"
		kvstorePath := persistentPath + "/kvstore/kvstore.db"

		// C: metadb_pool = db_pool_create(dbpath, 2)
		mm.dbPool.Store(db.DBPoolCreate(metadbPath, 2, mm.ts))

		// Upgrade schema
		dbConn := db.DBPoolGet(mm.dbPool.Load())
		if dbConn == nil {
			setupErr = fmt.Errorf("failed to get db connection")
			return
		}

		// C: db_upgrade_schema(db, "%s/res/metadb", "metadb", "kvstore", kvpath)
		rc := db.DBUpgradeSchema(dbConn, schemaDir, "metadb", mm.fam,
			"kvstore", kvstorePath)
		db.DBPoolPut(mm.dbPool.Load(), dbConn)

		if rc != 0 {
			db.DBPoolClose(mm.dbPool.Swap(nil))
			setupErr = fmt.Errorf("metadb schema upgrade failed")
		} else if mm.settingsMgr != nil {
			// C: settings_create_action(setting_get_dir("general:resets"),
			//     _p("Clear all metadata"), items_clear, NULL, 0, NULL)
			dir := mm.settingsMgr.SettingGetDir("general:resets")
			if dir != nil {
				mm.settingsMgr.CreateActionProp(dir,
					"Clear all metadata", "", mm.ItemsClear, nil, 0)
			}
		}
	})

	return setupErr
}

// SetDBPool installs a database pool directly (used by tests that can't
// run the full Start schema-upgrade path).
func (mm *MetadataManager) SetDBPool(pool *db.DBPool) {
	mm.dbPool.Store(pool)
}

// Fini closes the metadata database — C: metadb_fini → db_pool_close
func (mm *MetadataManager) Fini() {
	if pool := mm.dbPool.Swap(nil); pool != nil {
		db.DBPoolClose(pool)
	}
}

// Get gets a database connection from the pool — C: metadb_get
func (mm *MetadataManager) Get() *db.DB {
	pool := mm.dbPool.Load()
	if pool == nil {
		return nil
	}
	return db.DBPoolGet(pool)
}

// Close returns a database connection to the pool — C: metadb_close
func (mm *MetadataManager) Close(dbc *db.DB) {
	pool := mm.dbPool.Load()
	if pool == nil || dbc == nil {
		return
	}
	db.DBPoolPut(pool, dbc)
}

func (ms *MetadataStr) Get(key string) string {
	return ms.strings[key]
}

func (mdb *MetaDB) Get(id int64) *Metadata {
	mdb.mutex.RLock()
	defer mdb.mutex.RUnlock()
	return mdb.items[id]
}
