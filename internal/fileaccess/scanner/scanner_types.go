package scanner

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/czz/movian-go/internal/db"

	"github.com/czz/movian-go/internal/backend/playlist/playqueue"
	"github.com/czz/movian-go/internal/db/kvstore"
	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/metadata"
	"github.com/czz/movian-go/internal/notifications"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/trace"
)

// scannerTypeMap — C: static const prop_nf_sort_strmap_t typemap[]
// (fa_scanner.c:824-828): { "directory", 0 }, { NULL, 1 }
var scannerTypeMap = []propcore.PropNFSortStrmap{
	{Str: "directory", Val: 0},
	{Str: "", Val: 1}, // C NULL terminator carries the default value
}

// TypeMapEntry represents a mapping from content type string to sort value
type TypeMapEntry struct {
	Type  string
	Value int
}

// TypeMap represents a mapping for sorting content types
type TypeMap struct {
	entries []TypeMapEntry
}

// NewTypeMap creates a new TypeMap with default directory-first mapping
func NewTypeMap() *TypeMap {
	return &TypeMap{
		entries: []TypeMapEntry{
			{Type: "directory", Value: 0},
		},
	}
}

// GetSortValue returns the sort value for a given type string
func (tm *TypeMap) GetSortValue(typeStr string) int {
	for _, entry := range tm.entries {
		if entry.Type == typeStr {
			return entry.Value
		}
	}
	return 1 // default value for non-matching types
}

// TypeToString converts content type integer to string
func (tm *TypeMap) TypeToString(typ int) string {
	switch typ {
	case ContentDir:
		return "directory"
	case ContentFile:
		return "file"
	case ContentAudio:
		return "audio"
	case ContentVideo:
		return "video"
	case ContentImage:
		return "image"
	case ContentPlaylist:
		return "playlist"
	case ContentArchive:
		return "archive"
	case ContentShare:
		return "share"
	case ContentFont:
		return "font"
	default:
		return "unknown"
	}
}

// C: fa_scanner.c calls fontstash_props_from_title (line 236) and
// plugin_props_from_file (line 233) directly — file-scope singletons
// in C. Go can't import text/plugins here (fileaccess→scanner and
// text→fileaccess would cycle), so they're injected once at wire time.
var fontPropsFromTitle func(p *propcore.Prop, url, title string)
var pluginPropsFromFile func(p *propcore.Prop, zipfile string)

// SetFontPropsFunc injects fontstash_props_from_title (C: direct call).
func SetFontPropsFunc(fn func(p *propcore.Prop, url, title string)) {
	fontPropsFromTitle = fn
}

// SetPluginPropsFunc injects plugin_props_from_file (C: direct call,
// #if ENABLE_PLUGINS — always enabled in this port).
func SetPluginPropsFunc(fn func(p *propcore.Prop, zipfile string)) {
	pluginPropsFromFile = fn
}

// Scanner represents a file scanner
type Scanner struct {
	ctx           context.Context
	refcount      atomic.Int32
	url           string
	mtime         time.Time
	playme        string
	nodes         *propcore.Prop
	loading       *propcore.Prop
	model         *propcore.Prop
	directClose   *propcore.Prop
	running       atomic.Int32
	fd            *Dir
	ref           *fileaccesscore.Handle // C: fa_handle_t *s_ref (fa_reference)
	metadb        *db.DB                 // metadata database connection
	pnf           *PropNF
	title         string
	db            *metadata.DecoBrowse // C: deco_browse_t *s_db
	pc            *propcore.Courier    // C: prop_courier_t *s_pc
	dbg           int
	pm            *propcore.PropManager
	fam           *fileaccesscore.FileAccessManager // C: implicit global fa context
	ms            *mediacore.MediaSystem            // C: media.c globals (buffer_hungry)
	metadataProbe *MetadataProbe
	notifMgr      *notifications.NotificationManager
	typemap       *TypeMap
	metadataMgr   *metadata.MetadataManager
	ts            *trace.TraceSystem
	pq            *playqueue.PlayQueue // C: the global playqueue — injected
	kvstore       *kvstore.KVStore     // C: global kvstore_* funcs — injected
	ix            *Indexer             // C: fa_indexer.c statics — injected
}

// ScannerProbeStatus represents probe status
type ScannerProbeStatus int

const (
	FDEProbedNone ScannerProbeStatus = iota
	FDEProbedFilename
	FDEProbedContents
)

// Content type constants — C CONTENT_* values (metadata.h), persisted in SQLite.
const (
	ContentUnknown  = int(metadata.ContentUnknown)
	ContentDir      = int(metadata.ContentDir)
	ContentFile     = int(metadata.ContentFile)
	ContentAudio    = int(metadata.ContentAudio)
	ContentVideo    = int(metadata.ContentVideo)
	ContentPlaylist = int(metadata.ContentPlaylist)
	ContentDVD      = int(metadata.ContentDVD)
	ContentImage    = int(metadata.ContentImage)
	ContentAlbum    = int(metadata.ContentAlbum)
	ContentPlugin   = int(metadata.ContentPlugin)
	ContentFont     = int(metadata.ContentFont)
	ContentShare    = int(metadata.ContentShare)
	ContentArchive  = int(metadata.ContentArchive)
	ContentDocument = int(metadata.ContentDocument)
)

// DirEntry represents a directory entry
type DirEntry struct {
	Name     string
	Filename string
	URL      string
	Type     int
	Size     int64
	MTime    time.Time
	StatDone bool
	Probed   ScannerProbeStatus
	Marked   bool
	Stat     FileInfo
	md       *metadata.Metadata // C: metadata_t *fde_md
	// C: int fde_ignore_cache — set by rescan when mtime changed so the
	// deep probe bypasses the metadb cache.
	IgnoreCache bool
}

// Dir represents a directory
type Dir struct {
	Entries map[string]*DirEntry
	Count   int
}

// DirAlloc allocates a new directory structure
// Returns a pointer to an initialized Dir with empty Entries map and Count set to 0
func DirAlloc() *Dir {
	return &Dir{
		Entries: make(map[string]*DirEntry),
		Count:   0,
	}
}

// FileInfo represents file information
type FileInfo struct {
	Name     string
	Size     int64
	Type     int
	MTime    time.Time
	Modified time.Time // Alias for MTime for compatibility
	URL      string
}

// FileStat represents file statistics (alias for FileInfo for compatibility)
type FileStat = FileInfo

// ScannerEntry represents a scanner entry
type ScannerEntry struct {
	url           string
	filename      string
	typ           int
	metadata      *propcore.Prop
	probestatus   ScannerProbeStatus
	prop          *propcore.Prop
	statdone      bool
	stat          FileInfo
	md            *metadata.Metadata
	ignoreCache   bool
	boundToMetadb bool
}

// MetadataProbe probes file metadata
type MetadataProbe struct {
	fam *fileaccesscore.FileAccessManager // C: implicit global fa context
}

// NewMetadataProbe creates a new metadata probe
func NewMetadataProbe(fam *fileaccesscore.FileAccessManager) *MetadataProbe {
	return &MetadataProbe{fam: fam}
}

// ProbeDir probes a directory for metadata (C: fa_probe_dir)
func (mp *MetadataProbe) ProbeDir(url string) *metadata.Metadata {
	return FAProbeDir(mp.fam, url)
}

// ProbeMetadata probes a file for metadata (C: fa_probe_metadata —
// header signatures, ISO, then libav format probing).
func (mp *MetadataProbe) ProbeMetadata(url string, filename string, stats any) *metadata.Metadata {
	md, _ := FAProbeMetadata(mp.fam,
		url, filename, stats)
	return md
}

// FAUnlink unlinks (deletes) a file at the given URL
// Parameters:
//   - ctx: Context for cancellation and timeout
//   - url: The URL of the file to delete (e.g., "file:///path/to/file")
//
// Returns error if deletion failed
func FAUnlink(ctx context.Context, url string) error {
	// Check context cancellation
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// Handle file:// protocol
	if after, ok := strings.CutPrefix(url, "file://"); ok {
		filePath := after
		return os.Remove(filePath)
	}

	// For other protocols, return error for now
	return errors.New("FAUnlink: unsupported URL scheme for " + url)
}

// PropNFPredicateType represents the comparison type for a PropNF predicate
type PropNFPredicateType int

const (
	PropNFPredEq     PropNFPredicateType = iota // integer equality
	PropNFPredStrEq                             // string equality
	PropNFPredNeq                               // C: PROP_NF_CMP_NEQ (int)
	PropNFPredStrNeq                            // C: PROP_NF_CMP_NEQ (string)
)

// PropNFPredicateMode represents the filter mode
type PropNFPredicateMode int

const (
	PropNFModeExclude PropNFPredicateMode = iota // exclude matching nodes
	PropNFModeInclude                            // C: PROP_NF_MODE_INCLUDE
)

// PropNFPredicate represents a filter predicate
type PropNFPredicate struct {
	ID     int // C: pnp->pnp_id (per-nf tally)
	Key    string
	Type   PropNFPredicateType
	IntVal int
	StrVal string
	Mode   PropNFPredicateMode
}

// PropNF wraps the canonical propcore.PropNF with the scanner's
// legacy sort/predicate helpers. The implementation itself is the
// 1:1 port of src/prop/prop_nodefilter.c in pkg/prop/core.
type PropNF struct {
	*propcore.PropNF
	sortKeys [4]string // last sort path per slot (for SetSortOrder re-sort)
	descs    [4]bool
}

// NewPropNF creates a new property node filter.
// C: prop_nf_create(target, source, NULL, PROP_NF_AUTODESTROY)
// (prop_nodefilter.c:1328-1349). Note the C argument order: (dst, src).
func NewPropNF(pm *propcore.PropManager, source, target *propcore.Prop) *PropNF {
	inner := propcore.PropNFCreate(target, source, nil, propcore.PropNFAutoDestroy)
	if inner == nil {
		return nil
	}
	return &PropNF{PropNF: inner}
}

// NewPropNFWithFlags creates a PropNF with explicit AUTODESTROY control.
// C: prop_nf_create(target, source, NULL, flags)
func NewPropNFWithFlags(pm *propcore.PropManager, source, target *propcore.Prop, autoDestroy bool) *PropNF {
	var flags int
	if autoDestroy {
		flags |= propcore.PropNFAutoDestroy
	}
	inner := propcore.PropNFCreate(target, source, nil, flags)
	if inner == nil {
		return nil
	}
	return &PropNF{PropNF: inner}
}

// SetSortKey sets a sort key (path) at the given index.
// C: prop_nf_sort(pnf, key, desc, index, NULL, 0)
func (pnf *PropNF) SetSortKey(index int, key string) {
	if index < 0 || index > 3 {
		return
	}
	pnf.sortKeys[index] = key
	propcore.PropNFSort(pnf.PropNF, key, pnf.descs[index], uint(index), nil, false)
}

// SetSortOrder sets sort direction: order < 0 → descending, else ascending.
// (Legacy wrapper convention; canonical callers use prop_nf_sort directly.)
func (pnf *PropNF) SetSortOrder(index int, order int) {
	if index < 0 || index > 3 {
		return
	}
	pnf.descs[index] = order < 0
	propcore.PropNFSort(pnf.PropNF, pnf.sortKeys[index], pnf.descs[index], uint(index), nil, false)
}

// AddPredicate adds an int predicate. C: prop_nf_pred_int_add.
func (pnf *PropNF) AddPredicate(key string, predType PropNFPredicateType, intVal int, mode PropNFPredicateMode) int {
	cf := propcore.PropNFCmpEq
	if predType == PropNFPredNeq || predType == PropNFPredStrNeq {
		cf = propcore.PropNFCmpNeq
	}
	md := propcore.PropNFModeExclude
	if mode == PropNFModeInclude {
		md = propcore.PropNFModeInclude
	}
	if predType == PropNFPredStrEq || predType == PropNFPredStrNeq {
		return propcore.PropNFPredStrAdd(pnf.PropNF, key, cf, "", nil, md)
	}
	return propcore.PropNFPredIntAdd(pnf.PropNF, key, cf, intVal, nil, md)
}

// ContentTypeFromFilename determines content type from filename
func ContentTypeFromFilename(filename string) int {
	// C: contenttype_from_filename (metadata.c:439-451) — looks up the
	// extension in postfixtab (metadata.c:382-438); unmatched →
	// CONTENT_FILE.
	str := filename
	i := strings.LastIndexByte(str, '.')
	if i < 0 {
		return ContentFile
	}
	ext := strings.ToLower(str[i+1:])
	switch ext {
	case "iso":
		return ContentDVD
	case "jpeg", "jpg", "png", "gif", "svg", "webp":
		// webp is a Go extension — upstream postfixtab lacks it.
		return ContentImage
	case "mp3", "m4a", "flac", "aac", "wma", "ogg", "spc", "wav":
		return ContentAudio
	case "mkv", "avi", "mov", "m4v", "ts", "mpg", "wmv", "mp4", "mts":
		return ContentVideo
	case "sid":
		return ContentAlbum
	case "ttf", "otf":
		return ContentFont
	case "pdf":
		return ContentDocument
	case "nfo", "gz", "txt", "srt", "smi", "ass", "ssa", "idx", "sub",
		"exe", "tmp", "db", "pkg", "elf", "self":
		return ContentUnknown
	}
	return ContentFile
}
