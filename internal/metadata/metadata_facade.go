package metadata

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sync"

	"github.com/czz/movian-go/internal/db"
	propcore "github.com/czz/movian-go/internal/prop"
)

// MetadataSourceFuncs — C: metadata_source_funcs_t (metadata.h:244-261).
// Query callbacks supplied by a metadata provider (tmdb, tvdb, ...).
// All return an int64: >= 0 is a videoitem id (or 0 for "no error"),
// METADATA_PERMANENT_ERROR / METADATA_TEMPORARY_ERROR / METADATA_DEADLOCK.
type MetadataSourceFuncs struct {
	QueryByTitleAndYear func(db *db.DB, itemURL, title string, year int,
		duration int, qtype int, initiator string) int64
	QueryByIMDBID func(db *db.DB, itemURL, imdbID string, qtype int,
		initiator string) int64
	QueryByID      func(db *db.DB, itemURL, id string, initiator string) int64
	QueryByEpisode func(db *db.DB, itemURL, title string, season int,
		episode int, qtype int, initiator string) int64
}

// MetadataSource — C: metadata_source_t (metadata_sources.h:26-42)
type MetadataSource struct {
	Name          string               // ms_name
	Description   string               // ms_description
	Prio          int                  // ms_prio
	ID            int                  // ms_id (datasource row id)
	Enabled       int                  // ms_enabled
	Type          MetadataType         // ms_type
	Funcs         *MetadataSourceFuncs // ms_funcs
	Settings      *propcore.Prop       // ms_settings
	CfgID         int64                // ms_cfgid
	PartialProps  uint64               // ms_partial_props
	CompleteProps uint64               // ms_complete_props
	mm            *MetadataManager     // owning manager — C resolves via singleton
}

// MetadataStr represents metadata string utilities
type MetadataStr struct {
	strings map[string]string
}

func NewMetadataStr() *MetadataStr {
	return &MetadataStr{
		strings: make(map[string]string),
	}
}

func (ms *MetadataStr) Set(key, value string) {
	ms.strings[key] = value
}

func (ms *MetadataStr) GetString(key string) string {
	return ms.strings[key]
}

func (ms *MetadataStr) GetInt(key string) int {
	val := ms.strings[key]
	if val == "" {
		return 0
	}
	return 0
}

// MetaDB represents metadata database with JSON storage
type MetaDB struct {
	items   map[int64]*Metadata
	dbPath  string
	mutex   sync.RWMutex
	artists map[string]int64
	albums  map[string]int64
	sources []*MetadataSource
}

func NewMetaDB(dbPath string) *MetaDB {
	return &MetaDB{
		items:   make(map[int64]*Metadata),
		dbPath:  dbPath,
		artists: make(map[string]int64),
		albums:  make(map[string]int64),
		sources: make([]*MetadataSource, 0),
	}
}

func (mdb *MetaDB) Open() error {
	mdb.mutex.Lock()
	defer mdb.mutex.Unlock()

	// Create directory if needed
	dir := filepath.Dir(mdb.dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	// Load existing data
	if _, err := os.Stat(mdb.dbPath); err == nil {
		data, err := os.ReadFile(mdb.dbPath)
		if err != nil {
			return err
		}
		var dbData struct {
			Items   map[int64]*Metadata
			Artists map[string]int64
			Albums  map[string]int64
			Sources []*MetadataSource
		}
		if err := json.Unmarshal(data, &dbData); err == nil {
			mdb.items = dbData.Items
			mdb.artists = dbData.Artists
			mdb.albums = dbData.Albums
			mdb.sources = dbData.Sources
		}
	}

	return nil
}

func (mdb *MetaDB) Save() error {
	mdb.mutex.RLock()
	defer mdb.mutex.RUnlock()

	dbData := struct {
		Items   map[int64]*Metadata
		Artists map[string]int64
		Albums  map[string]int64
		Sources []*MetadataSource
	}{
		Items:   mdb.items,
		Artists: mdb.artists,
		Albums:  mdb.albums,
		Sources: mdb.sources,
	}

	data, err := json.MarshalIndent(dbData, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(mdb.dbPath, data, 0644)
}

func (mdb *MetaDB) Add(id int64, md *Metadata) {
	mdb.mutex.Lock()
	defer mdb.mutex.Unlock()
	mdb.items[id] = md
}

func (mdb *MetaDB) GetByURL(url string) *Metadata {
	mdb.mutex.RLock()
	defer mdb.mutex.RUnlock()
	for _, md := range mdb.items {
		if md.Redirect == url {
			return md
		}
	}
	return nil
}

func (mdb *MetaDB) Remove(id int64) {
	mdb.mutex.Lock()
	defer mdb.mutex.Unlock()
	delete(mdb.items, id)
}

func (mdb *MetaDB) GetAll() map[int64]*Metadata {
	mdb.mutex.RLock()
	defer mdb.mutex.RUnlock()
	result := make(map[int64]*Metadata)
	maps.Copy(result, mdb.items)
	return result
}

func (mdb *MetaDB) ArtistGetByTitle(title string, dsID int, extID string) int64 {
	mdb.mutex.Lock()
	defer mdb.mutex.Unlock()

	key := fmt.Sprintf("%s:%d:%s", title, dsID, extID)
	if id, ok := mdb.artists[key]; ok {
		return id
	}

	// Create new artist ID
	newID := int64(len(mdb.artists) + 1)
	mdb.artists[key] = newID
	return newID
}

func (mdb *MetaDB) AlbumGetByTitle(album string, artistID int64, dsID int, extID string) int64 {
	mdb.mutex.Lock()
	defer mdb.mutex.Unlock()

	key := fmt.Sprintf("%s:%d:%d:%s", album, artistID, dsID, extID)
	if id, ok := mdb.albums[key]; ok {
		return id
	}

	// Create new album ID
	newID := int64(len(mdb.albums) + 1)
	mdb.albums[key] = newID
	return newID
}

func (mdb *MetaDB) InsertAlbumArt(albumID int64, url string, width, height int) {
	// Store album art in metadata
	mdb.mutex.Lock()
	defer mdb.mutex.Unlock()
	// Implementation would add to album metadata
}

func (mdb *MetaDB) AddSource(source *MetadataSource) {
	mdb.mutex.Lock()
	defer mdb.mutex.Unlock()
	mdb.sources = append(mdb.sources, source)
}

func (mdb *MetaDB) GetSources() []*MetadataSource {
	mdb.mutex.RLock()
	defer mdb.mutex.RUnlock()
	return mdb.sources
}

func (mdb *MetaDB) ClearAll() error {
	mdb.mutex.Lock()
	defer mdb.mutex.Unlock()
	mdb.items = make(map[int64]*Metadata)
	mdb.artists = make(map[string]int64)
	mdb.albums = make(map[string]int64)
	mdb.sources = make([]*MetadataSource, 0)
	return os.Remove(mdb.dbPath)
}

// MLP represents metadata lookup provider
type MLP struct {
	sources []*MetadataSource
}

func NewMLP() *MLP {
	return &MLP{
		sources: make([]*MetadataSource, 0),
	}
}

func (mlp *MLP) AddSource(source *MetadataSource) {
	mlp.sources = append(mlp.sources, source)
}

func (mlp *MLP) GetSources() []*MetadataSource {
	return mlp.sources
}

func (mlp *MLP) Query(query string) *Metadata {
	// Query metadata from sources
	return nil
}

// PlayInfo represents playback information
type PlayInfo struct {
	url      string
	title    string
	duration float32
	position float32
	canSeek  bool
	canPause bool
	canSkip  bool
}

func NewPlayInfo() *PlayInfo {
	return &PlayInfo{
		url:      "",
		title:    "",
		duration: 0,
		position: 0,
		canSeek:  false,
		canPause: false,
		canSkip:  false,
	}
}

func (pi *PlayInfo) SetDuration(duration float32) {
	pi.duration = duration
}

func (pi *PlayInfo) SetPosition(position float32) {
	pi.position = position
}

func (pi *PlayInfo) SetCanSeek(canSeek bool) {
	pi.canSeek = canSeek
}

func (pi *PlayInfo) SetCanPause(canPause bool) {
	pi.canPause = canPause
}

func (pi *PlayInfo) SetCanSkip(canSkip bool) {
	pi.canSkip = canSkip
}

func (pi *PlayInfo) GetDuration() float32 {
	return pi.duration
}

func (pi *PlayInfo) GetPosition() float32 {
	return pi.position
}

func (pi *PlayInfo) CanSeek() bool {
	return pi.canSeek
}

func (pi *PlayInfo) CanPause() bool {
	return pi.canPause
}

func (pi *PlayInfo) CanSkip() bool {
	return pi.canSkip
}
