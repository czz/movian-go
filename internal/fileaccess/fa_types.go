package fileaccess

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/czz/movian-go/internal/gconf"
	httpnet "github.com/czz/movian-go/internal/networking/http"
)

// FileSystemInfo represents filesystem information including total size and available space
type FileSystemInfo struct {
	Size  uint64 // Total filesystem size in bytes
	Avail uint64 // Available space in bytes
}

// FileStat represents file statistics including size, type, and modification time
type FileStat struct {
	Size  int64     // File size in bytes
	Type  int       // Content type (ContentDir, ContentFile, etc.)
	MTime time.Time // Modification time
}

// DirEntry represents a single entry in a directory listing
type DirEntry struct {
	Filename string   // Name of the file/directory
	URL      string   // Full URL to the file/directory
	Type     int      // Content type
	Stat     FileStat // File statistics
	StatDone bool     // True if Stat has been populated
	Probed   int      // Probe level: 0=none, 1=filename, 2=contents
	Marked   bool     // Whether the entry is marked
	Md       any      // C: metadata_t *fde_md
}

// Handle represents a file handle for reading, writing, and seeking
type Handle struct {
	fam      *FileAccessManager // C: implicit global fa context
	proto    Protocol           // Protocol handler for this file
	fap      *FAProtocol        // C: fh_proto (for fa_reference/fa_unreference)
	reader   io.ReadCloser      // Reader for reading data
	writer   io.WriteCloser     // Writer for writing data
	seeker   io.Seeker          // Seeker for seeking within file
	url      string             // URL of the file
	size     int64              // File size in bytes
	position int64              // Current position in file
	closed   bool               // Whether the handle is closed
	sizer    func() int64       // C: fap_fsize — lazy size resolver (buffered files)

}

// Reader exposes the underlying reader — the exported counterpart of the
// package-internal fhOpaque helper (fa_buffer.go), used by fap-level hooks
// (Deadline, Unreference) that need to recover protocol-private state.
func (h *Handle) Reader() io.ReadCloser { return h.reader }

// OpenExtra contains extra options for opening files
type OpenExtra struct {
	RequestHeaders  map[string]string // HTTP request headers
	ResponseHeaders map[string]string // HTTP response headers
	// C: foe_request_headers / foe_response_headers are http_header_list_t * —
	// these fields carry the canonical header list for callers that can use it.
	// ResponseHeadersList is updated live by the response parser.
	RequestHeadersList  *httpnet.HTTPHeaderList
	ResponseHeadersList *httpnet.HTTPHeaderList
	Stats               any // Statistics tracking
	Cancellable         any // Cancellation handle
	OpenTimeout         int // Open timeout in milliseconds
	ProtocolError       int // Protocol-specific error code
	Flags               int // C: flags param of fa_open_ex (FA_WRITE etc.)
}

// Protocol is the interface for file access protocol handlers
type Protocol interface {
	Open(url string, extra *OpenExtra) (*Handle, error)    // Opens a file
	Stat(url string) (*FileStat, error)                    // Gets file statistics
	ScanDir(ctx context.Context, url string) (*Dir, error) // Scans a directory
	CanHandle(url string) bool                             // Checks if protocol can handle URL
	Name() string                                          // Returns protocol name
}

// Ftruncater is an optional interface for protocols that support ftruncate
type Ftruncater interface {
	Ftruncate(fh *Handle, size int64) error
}

// FSProtocol is the filesystem protocol implementation for local file access
type FSProtocol struct{}

// HTTPProtocol is the HTTP protocol implementation for web resources
type HTTPProtocol struct {
	fam *FileAccessManager // C: implicit global fa context
}

// MemoryProtocol is the memory-backed file protocol for in-memory files
type MemoryProtocol struct {
	mu sync.Mutex // Guards files/nextID (register on resolver
	// goroutines, open on playback threads)
	files  map[int][]byte // Map of file ID to file data
	nextID int            // Next file ID to assign
}

// usageEventer — C: usage_event (usage.c). Declared as a minimal
// interface because pkg/usage imports this package (HTTPReq for the
// tracker POST); *usage.Reporter satisfies it.
type usageEventer interface {
	Event(key string, count int, seg ...string)
}

// TorrentfileProto exposes the torrentfile protocol instance — the
// bittorrent backend injects its implementation here (C: link seam).
func (fam *FileAccessManager) TorrentfileProto() *TorrentFileProtocol {
	return fam.torrentfileProto
}

// VFSProto exposes the vfs protocol instance. C: extern fa_protocol_t
// fa_protocol_vfs — referenced directly by ftp_server.c's helpers.
func (fam *FileAccessManager) VFSProto() *VFSProtocol {
	return fam.vfsProto
}

// FAAllowDelete — C: gconf.fa_allow_delete
func FAAllowDelete(g *gconf.T) bool { return g != nil && g.FAAllowDelete.Load() }

// FAKVStoreAsXattr — C: gconf.fa_kvstore_as_xattr (read by kvstore.c)
func FAKVStoreAsXattr(g *gconf.T) bool { return g != nil && g.FAKVStoreAsXattr.Load() }

// ShowFilenameExtensions — C: gconf.show_filename_extensions
func ShowFilenameExtensions(g *gconf.T) bool { return g != nil && g.ShowFilenameExtensions.Load() }

// FABrowseArchives — C: gconf.fa_browse_archives
func FABrowseArchives(g *gconf.T) bool { return g != nil && g.FABrowseArchives.Load() }

// ErrLoadNoMethod — C: NO_LOAD_METHOD sentinel from fa_load when the
// resolved protocol has no fap_load and FA_LOAD_NO_FALLBACK is set.
var ErrLoadNoMethod = errors.New("fa_load: no load method")

// ErrLoadNotModified — C: NOT_MODIFIED sentinel from fa_load
// (cache-control conditional hit, e.g. HTTP 304).
var ErrLoadNotModified = errors.New("fa_load: not modified")

// ErrNotSupported — C: FAP_NOT_SUPPORTED. Protocol methods return this
// when C's fap has no corresponding op (fap_stat/fap_scandir == NULL).
var ErrNotSupported = errors.New("operation not supported")

// ErrAuthRequired — C: FAP_NEED_AUTH.
var ErrAuthRequired = errors.New("authentication required")

// FAProtocol represents a file access protocol
// C: fa_protocol_t (fa_proto.h) — reference counted, atomic refcount.
type FAProtocol struct {
	fam          *FileAccessManager                                       // Go owner of C's implicit global fa context
	Name         string                                                   // Protocol name
	RefCount     int32                                                    // C: fap_refcount (atomic)
	Opaque       any                                                      // C: fap_opaque
	IncludeProto bool                                                     // C: FAP_INCLUDE_PROTO_IN_URL flag
	Flags        int                                                      // C: fap_flags (FAP_ALLOW_CACHE etc.)
	Redirect     func(proto *FAProtocol, filename string) string          // C: fap_redirect
	Normalize    func(proto *FAProtocol, filename string) (string, error) // C: fap_normalize
	MatchProto   func(protoName string) bool                              // C: fap_match_proto — custom protocol matcher
	Write        bool                                                     // C: fap_write != NULL
	Park         func(fh *Handle)                                         // C: fap_park
	NoParking    func(fh *Handle) bool                                    // C: fap_no_parking
	Deadline     func(fh *Handle, deadline int)                           // C: fap_set_read_timeout
	Ftruncate    func(fh *Handle, size int64) error                       // C: fap_ftruncate
	Fini         func(proto *FAProtocol)                                  // C: fap_fini — cleanup on refcount=0

	// C: fap_reference / fap_unreference — hold a mount reference on a URL
	// (fileaccess.c:549-574). NULL for protocols without reference support.
	Reference   func(fap *FAProtocol, filename string) *Handle // C: fap_reference
	Unreference func(fh *Handle)                               // C: fap_unreference

	// FS operations — C: fap_makedir, fap_unlink, fap_rmdir, fap_rename, fap_fsinfo
	// These are NULL if the protocol doesn't support the operation (C checks for NULL).
	Makedirs func(proto *FAProtocol, filename string) error                    // C: fap_makedir
	Unlink   func(proto *FAProtocol, filename string) error                    // C: fap_unlink
	Rmdir    func(proto *FAProtocol, filename string) error                    // C: fap_rmdir
	Rename   func(proto *FAProtocol, oldFilename, newFilename string) error    // C: fap_rename
	FSInfo   func(proto *FAProtocol, filename string) (*FileSystemInfo, error) // C: fap_fsinfo

	// C: fap_stat (fa_proto.h) — stat a resolved filename; returns
	// fa_err_code_t (FAP_OK=0, FAP_NOENT, FAP_NEED_AUTH, ...).
	Stat func(fap *FAProtocol, filename string, flags int) (*FileStat, error)

	// C: fap_get_parts (fa_proto.h) — fill fd with archive parts; 0 ok.
	GetParts func(fap *FAProtocol, fd *Dir, filename string) error

	// C: fap_notify_start / fap_notify_stop (fa_proto.h)
	NotifyStart func(fap *FAProtocol, filename string, opaque any, change FANotifyFunc) *Handle
	NotifyStop  func(fh *Handle)

	// C: fap_get_last_component (fa_proto.h) — protocol-specific basename.
	GetLastComponent func(fap *FAProtocol, filename string, dst []byte)

	// C: fap_title (fa_proto.h) — protocol-specific title (rstr_t* → string;
	// "" means no title, fall back to last component like C's NULL rstr).
	Title func(fap *FAProtocol, filename string) string

	// C: fap_set_xattr / fap_get_xattr (fa_proto.h) — fa_err_code_t results.
	SetXattr func(fap *FAProtocol, filename, name string, data []byte) int
	GetXattr func(fap *FAProtocol, filename, name string) ([]byte, int)

	// C: fap_load (fa_proto.h) — protocol-native whole-file load with
	// cache revalidation. Returns ErrLoadNotModified for NOT_MODIFIED.
	Load func(fap *FAProtocol, filename string,
		etag *string, mtime *time.Time, maxAge *int, flags int,
		cb FALoadCB, opaque, c any,
		reqHeaders, respHeaders any, location *string,
		protocolCode *int) (*Buffer, error)
}
