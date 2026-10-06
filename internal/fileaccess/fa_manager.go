package fileaccess

import (
	"errors"
	nethttp "net/http"
	"os"
	"slices"
	"sync"

	"github.com/czz/movian-go/internal/asyncio"

	"github.com/czz/movian-go/internal/callout"
	"github.com/czz/movian-go/internal/task"

	"github.com/czz/movian-go/internal/blobcache"

	"github.com/czz/movian-go/internal/gconf"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/trace"
)

// SetDataRoot sets the data root directory for dataroot:// protocol.
// Mirrors C's app_dataroot() — used by fa_resolve_proto for "dataroot://" URLs.
func (fam *FileAccessManager) SetDataRoot(root string) {
	fam.dataRoot.mu.Lock()
	fam.dataRoot.dir = root
	fam.dataRoot.mu.Unlock()
}

// getDataRoot returns the data root directory.
// Equivalent to C's app_dataroot().
// Priority: SetDataRoot() value > MOVIANGO_DATAROOT env var > "./"
func (fam *FileAccessManager) getDataRoot() string {
	fam.dataRoot.mu.Lock()
	if fam.dataRoot.dir != "" {
		dir := fam.dataRoot.dir
		fam.dataRoot.mu.Unlock()
		return dir
	}
	fam.dataRoot.mu.Unlock()
	if env := os.Getenv("MOVIANGO_DATAROOT"); env != "" {
		return env
	}
	return "./"
}

// FileAccessManager manages file access protocols and configuration
type FileAccessManager struct {
	pm               *propcore.PropManager
	protocols        []Protocol             // Registered protocol handlers
	fsProto          *FSProtocol            // Filesystem protocol instance
	httpProto        *HTTPProtocol          // HTTP protocol instance
	ftpProto         *FTPProtocol           // FTP protocol instance
	smbProto         *SMBProtocol           // SMB protocol instance
	memProto         *MemoryProtocol        // Memory protocol instance
	zipProto         *ZIPProtocol           // ZIP protocol instance
	dataProto        *DataProtocol          // Data URI protocol instance
	vfsProto         *VFSProtocol           // Virtual filesystem protocol instance
	torrentfileProto *TorrentFileProtocol   // Torrent file protocol instance
	bundleManager    *BundleManager         // Bundle manager for bundle and memfile protocols
	bundleProto      *BundleProtocol        // Bundle protocol instance
	memfileProto     *MemfileProtocol       // Memfile protocol instance
	protoStarted     bool                   // RegisterProtocols already ran
	usage            usageEventer           // C: usage_event (usage.c) — injected
	blobCache        *blobcache.BlobCache   // C: the implicit blobcache — injected
	store            any                    // C: the global htsmsg_store — injected; `any` because htsmsg imports this package
	calloutSystem    *callout.CalloutSystem // C: callout_* globals — injected
	gconf            *gconf.T               // C: gconf_t — injected
	tasks            *task.TaskSystem       // C: task_run globals — injected
	tracer           *trace.TraceSystem     // C: trace() global fn on file statics — injected

	// Link-time seams — C resolves these as direct cross-package calls
	// (fileaccess_init → fa_indexer_init/fa_imageloader_init/settings,
	// fa_ftp → keyring_lookup, fa_unlink_recursive → message_popup).
	// Providers import this package, so the wiring is inverted.
	indexerStart     func(fam *FileAccessManager, store any)
	imageloaderStart func(fam *FileAccessManager)
	settingsSetup    func()
	keyringLookup    func(id string, username, password, domain *string,
		rememberMe *int, source, reason string, flags int) int
	messagePopup func(msg string, flags int, extra []string) int

	// pluginProbeForAutoinstall — C: plugin_probe_for_autoinstall under
	// ENABLE_PLUGINS (fa_probe.c:570). Provider: plugin manager.
	pluginProbeForAutoinstall func(fh any, buf []byte, l int, url string)

	// C: fileaccess.c globals — fileaccess_protocols list + its mutex +
	// native_proto + fa_protocol_data. Per-Manager here.
	protocolList   []*FAProtocol
	protocolMutex  sync.Mutex
	nativeProto    *FAProtocol
	faProtocolData *FAProtocol // C: fa_protocol_data

	// dataRoot — C: app_dataroot() static result for dataroot://
	dataRoot struct {
		mu  sync.Mutex
		dir string
	}

	// ---- C: fa_http.c file statics ----
	httpConns struct {
		mu        sync.Mutex
		cond      *sync.Cond
		parked    []*httpConnection
		active    []*httpConnection
		numParked int
		connTally int32
		fileTally int32
		davXML    func(buf []byte) (*DAVXMLMap, error) // C: htsmsg_xml_deserialize_buf
	}
	httpRedirects struct {
		list []httpRedirect
		mu   sync.Mutex
	}
	httpCookies struct {
		list         []*httpCookie
		mu           sync.Mutex
		persistTimer callout.Callout
		// C: htsmsg_store_save/load("httpcookies") bridge (pkg/htsmsg
		// imports this package — cycle seam).
		cookieStore any
		cookieSave  func(store any, records []HTTPCookieRecord)
		cookieLoad  func(store any) []HTTPCookieRecord
	}
	// Go extension (fa_http2.go): shared *net/http.Client per TLS
	// verify mode — lazily built, connection reuse via Transport.
	http2Mu      sync.Mutex
	http2Clients map[bool]*nethttp.Client
	// Go extension: QUIC/HTTP3 transports + per-host capability cache
	// (h3Pool is declared in fa_http2.go).
	h3      h3Pool
	httpDUA struct { // C: static ua_for_domain list
		sync.Mutex
		m map[string]string
	}
	httpAuthMu     sync.Mutex              // C: http_auth_caches mutex
	httpAuthCaches []*httpAuthCache        // C: http_auth_caches
	httpInspectors []*HTTPRequestInspector // C: http_request_inspectors
	httpStartOnce  sync.Once               // C: http_init once

	// ---- C: asyncio_http.c file statics ----
	asyncioHTTP struct {
		mu        sync.Mutex
		aio       *asyncio.AsyncIO
		worker    int
		completed []*AsyncioHTTPReq // LIST_INSERT_HEAD order
	}

	// ---- C: fa_ftp.c / ftpparse.c file statics ----
	ftp struct {
		mu      sync.Mutex
		conns   []*ftpConnection
		idTally int32
	}
	ftpTimebase struct { // C: ftpparse.c statics (base/now/currentYear)
		base        int64
		currentYear int64
		now         int64
	}

	// ---- C: fa_buffer.c file statics ----
	bf struct {
		mu            sync.Mutex // C: buffered_global_mutex
		parked        *bufferedFile
		parkedCallout callout.Callout
	}

	// ---- C: fa_rar.c / fa_zip.c file statics ----
	rar struct {
		mu       sync.Mutex
		archives []*rarArchive
	}
	zip struct {
		mu       sync.Mutex    // C: zip_global_mutex
		archives []*zipArchive // C: LIST_HEAD, insert-head order
	}
}

// NewFileAccessManager creates a new file access manager
func NewFileAccessManager(pm *propcore.PropManager, u usageEventer) *FileAccessManager {
	fam := &FileAccessManager{
		protocols:        make([]Protocol, 0),
		fsProto:          &FSProtocol{},
		httpProto:        &HTTPProtocol{},
		ftpProto:         &FTPProtocol{usage: u},
		smbProto:         &SMBProtocol{},
		memProto:         &MemoryProtocol{files: make(map[int][]byte), nextID: 1},
		dataProto:        &DataProtocol{},
		torrentfileProto: NewTorrentFileProtocol(),
		bundleManager:    NewBundleManager(),
		pm:               pm,
		usage:            u,
	}
	fam.httpProto.fam = fam
	fam.ftpProto.fam = fam
	fam.httpConns.cond = sync.NewCond(&fam.httpConns.mu)
	fam.httpDUA.m = map[string]string{}
	fam.zipProto = NewZIPProtocol(fam)
	fam.vfsProto = NewVFSProtocol(fam, pm)
	return fam
}

// SetBlobCache injects the blob cache (C: the implicit global that
// fa_load and the imageloader consult). Called once at init.
func (fam *FileAccessManager) SetBlobCache(bc *blobcache.BlobCache) {
	fam.blobCache = bc
}

// SetStore injects the htsmsg store (C: the global htsmsg_store the
// indexer persists its roots to). `any` because htsmsg imports this
// package. Called once at init.
func (fam *FileAccessManager) SetStore(st any) { fam.store = st }

// SetCalloutSystem injects the callout system (C: callout_arm/
// callout_disarm globals used by the connection keepalive, cookie jar
// and parked-buffer reaper). Also pushed into the file-scope states —
// they are the C file-scope statics of fa_http/fa_buffer; in C they
// reach the global callout list, here fam is the owner that injects it.
// Called once at init.
func (fam *FileAccessManager) SetCalloutSystem(cs *callout.CalloutSystem) {
	fam.calloutSystem = cs
}

// SetTaskSystem injects the task system (C: task_run globals, task.c).
func (fam *FileAccessManager) SetTaskSystem(ts *task.TaskSystem) {
	fam.tasks = ts
}

// SetTraceSystem injects the trace system (C: trace.c file statics
// reached by the global TRACE() macro). Also wires the per-protocol
// file-static states that emit trace lines.
// SetGconf injects the process gconf (C: gconf_t — owned by main) and
// propagates it into the protocol subsystem state like the other deps.
func (fam *FileAccessManager) SetGconf(g *gconf.T) {
	fam.gconf = g
}

// Gconf — C: gconf_t as read by fileaccess code.
func (fam *FileAccessManager) Gconf() *gconf.T {
	if fam == nil || fam.gconf == nil {
		return gconf.New()
	}
	return fam.gconf
}

func (fam *FileAccessManager) SetTraceSystem(ts *trace.TraceSystem) {
	fam.tracer = ts
}

// TraceSystem returns the injected trace system (nil until wired).
// Nil-receiver safe — unwired paths simply emit no trace output.
func (fam *FileAccessManager) TraceSystem() *trace.TraceSystem {
	if fam == nil {
		return nil
	}
	return fam.tracer
}

// CalloutSystem returns the injected callout system (nil until wired).
func (fam *FileAccessManager) CalloutSystem() *callout.CalloutSystem {
	if fam == nil {
		return nil
	}
	return fam.calloutSystem
}

// Store returns the injected store (nil until wired).
func (fam *FileAccessManager) Store() any { return fam.store }

// BlobCache returns the injected blob cache (nil until wired).
// Nil-receiver safe: FALoad can be reached with fam == nil (C: the
// blobcache global was simply nil until blobcache_init).
func (fam *FileAccessManager) BlobCache() *blobcache.BlobCache {
	if fam == nil {
		return nil
	}
	return fam.blobCache
}

// RegisterMemory stores data under a new mem:// id and returns the id;
// the file stays registered for the process lifetime (mem:// files are
// process-scoped scratch space — e.g. synthesized playlists). C has no
// equivalent API — its mem:// files are opened by id for blobs created
// elsewhere; this is the Go-side registration entry point.
// Nil-receiver safe: returns -1 when no manager exists.
func (fam *FileAccessManager) RegisterMemory(data []byte) int {
	if fam == nil {
		return -1
	}
	return fam.memProto.Register(data)
}

// Start initializes the file access manager and registers all protocols
// RegisterProtocols performs the static protocol-registration phase —
// C's FAP_REGISTER() sections plus the fa_protocol_register loop of
// fileaccess_init. It does NOT run per-protocol fap_init hooks
// (http_init, vfs_init, indexer/imageloader/settings tail), so it is
// safe to call while arbitrary locks are held — e.g. from
// persistent_load under htsmsg_store's mutex. Start() calls this and
// then the fap_init phase.
func (fam *FileAccessManager) RegisterProtocols() {
	if fam == nil || fam.protoStarted {
		return
	}
	fam.protoStarted = true
	fam.bundleProto = &BundleProtocol{bm: fam.bundleManager}
	fam.memfileProto = &MemfileProtocol{bm: fam.bundleManager}

	fam.RegisterProtocol(fam.fsProto)
	fam.RegisterProtocol(fam.httpProto)
	fam.RegisterProtocol(fam.ftpProto)
	fam.RegisterProtocol(fam.smbProto)
	fam.RegisterProtocol(fam.memProto)
	fam.RegisterProtocol(fam.zipProto)
	fam.RegisterProtocol(fam.dataProto)
	fam.RegisterProtocol(fam.vfsProto)
	fam.RegisterProtocol(fam.bundleProto)
	fam.RegisterProtocol(fam.memfileProto)
	fam.RegisterProtocol(fam.torrentfileProto)
	// C: FAP_REGISTER(rar) — fa_protocol_rar (fa_rar.c:861)
	fam.RegisterProtocol(&RARProtocol{})

	// C: FAP_REGISTER(es/cache/persistent/file) — android_fs.c; no-op
	// off android.
	registerAndroidProtocols(fam)
	// C: FAP_REGISTER(webdav) / FAP_REGISTER(webdavs) — fa_http.c
	fam.RegisterProtocol(&WebDAVProtocol{ssl: false, fam: fam})
	fam.RegisterProtocol(&WebDAVProtocol{ssl: true, fam: fam})

	// C: fileaccess_init → fa_protocol_register(&fa_protocol_fs), …
	// (fileaccess.c) — every built-in protocol lands in the global
	// fileaccess_all_protocols list that fa_resolve_proto consults.
	// Go: the global FAProtocol registry gates FAOpenEx/FAResolveProto;
	// the actual open dispatches through fam.protocols.
	for _, p := range fam.protocols {
		fam.registerGlobalFAProtocol(p.Name())
	}
	// C: fa_protocol_https is a separate FAP_REGISTER (fa_http.c:2742) —
	// the fam-level HTTPProtocol handles both schemes under one Name().
	fam.registerGlobalFAProtocol("https")
	// C: fa_protocol_fs carries real ops (fap_stat/unlink/rmdir/…) —
	// populate the "file" gate with the fs adapters so resolved faps
	// behave like C's fs protocol. On android the four android_fs.c
	// gates are populated instead (fa_android.go).
	populateFSGate(fam)
	// C: fa_protocol_rar carries fap_stat/fap_get_parts/fap_reference —
	// populate the "rar" gate likewise.
	if fp := fam.lookupFAProtocol("rar"); fp != nil {
		populateRARFAProtocol(fp, fam)
	}
	// C: fa_protocol_zip carries fap_stat/fap_reference/fap_unreference
	// (fa_zip.c:865) — populate the "zip" gate likewise.
	if fp := fam.lookupFAProtocol("zip"); fp != nil {
		populateZIPFAProtocol(fp, fam)
	}
	// C: ftp_init → ftpparse_init (fa_ftp.c:886)
	fam.FTPParseStart()
	// C: http/https/ftp/smb set fap_flags = FAP_INCLUDE_PROTO_IN_URL |
	// FAP_ALLOW_CACHE — enables the fa_buffered_open wrapper for
	// FA_BUFFERED_* opens on those protocols.
	for _, name := range []string{"http", "https", "ftp", "smb"} {
		if fp := fam.lookupFAProtocol(name); fp != nil {
			fp.Flags = FAPIncludeProtoInURL | FAPAllowCache
			fp.IncludeProto = true
		}
	}
	// C: fa_protocol_http/https/webdav/webdavs (fa_http.c:2403-2743) —
	// populate the gates with the canonical fap ops; webdav gets
	// fap_scan=dav_scandir + fap_stat=dav_stat.
	for _, name := range []string{"http", "https"} {
		if fp := fam.lookupFAProtocol(name); fp != nil {
			populateHTTPFAProtocol(fp, false)
		}
	}
	for _, name := range []string{"webdav", "webdavs"} {
		if fp := fam.lookupFAProtocol(name); fp != nil {
			populateHTTPFAProtocol(fp, true)
		}
	}
	// C: fa_protocol_smb (fa_nativesmb.c:2921) — populate the "smb" gate
	// with fap_stat/unlink/rmdir/xattr/no_parking.
	if fp := fam.lookupFAProtocol("smb"); fp != nil {
		populateSMBFAProtocol(fp)
	}
	// C: fa_protocol_data is extern-referenced by fa_resolve_proto.
	fam.SetDataProtocol(&FAProtocol{Name: "data"})
	// C: bare paths (no scheme) resolve to fa_protocol_fs.
	fam.SetNativeProtocol(&FAProtocol{
		Name:  "native",
		Write: true, // C: fap_write != NULL
		// C: fa_protocol_fs.fap_normalize = fs_normalize (ENABLE_REALPATH)
		Normalize: func(fap *FAProtocol, filename string) (string, error) {
			return fsNormalize(filename)
		},
		Stat: func(fap *FAProtocol, filename string, flags int) (*FileStat, error) {
			// C: fs_stat returns FAP_ERROR regardless of errno — opaque
			// error (not wrapped) so fapErrCode maps to FAP_ERROR.
			fi, err := os.Stat(filename)
			if err != nil {
				return nil, errors.New(err.Error())
			}
			st := &FileStat{Size: fi.Size(), MTime: fi.ModTime()}
			if fi.IsDir() {
				st.Type = ContentDir
			} else {
				st.Type = ContentFile
			}
			return st, nil
		},
		Makedirs: func(proto *FAProtocol, filename string) error {
			return os.MkdirAll(filename, 0755)
		},
		Unlink: func(proto *FAProtocol, filename string) error {
			return os.Remove(filename)
		},
		Rmdir: func(proto *FAProtocol, filename string) error {
			return os.Remove(filename)
		},
		Rename: func(proto *FAProtocol, oldFilename, newFilename string) error {
			return os.Rename(oldFilename, newFilename)
		},
	})

}

// Start completes fileaccess_init — C's per-protocol fap_init hooks and
// the fileaccess_init tail (indexer, imageloader, settings). The static
// protocol registration runs in RegisterProtocols and is idempotent.
func (fam *FileAccessManager) Start() error {
	if fam == nil {
		return nil
	}
	fam.RegisterProtocols()

	// C: fa_protocol_http.fap_init = http_init — cookie store load.
	fam.httpStart()

	// C: fap_init runs per protocol at fileaccess_init —
	// fa_protocol_vfs.fap_init = vfs_init subscribes global.services.all.
	fam.vfsProto.Setup()

	// C: fileaccess_init → fa_indexer_init (fileaccess.c:1441) under
	// ENABLE_METADATA. Go: fa_indexer lives in pkg/fileaccess/scanner which
	// imports this package — wired via hook to avoid the cycle.
	if fam.indexerStart != nil {
		fam.indexerStart(fam, fam.store)
	}
	// C: fileaccess_init → fa_imageloader_init (fileaccess.c:1434)
	if fam.imageloaderStart != nil {
		fam.imageloaderStart(fam)
	}

	// C: fileaccess_init settings tail (fileaccess.c:1441-1469) —
	// setting_get_dir("general:filebrowse") + the four bool settings.
	if fam.settingsSetup != nil {
		fam.settingsSetup()
	}

	return nil
}

// SetIndexerStart wires fa_indexer_init (C: called from fileaccess_init).
// Provider: pkg/fileaccess/scanner (imports this package — inverted seam).
func (fam *FileAccessManager) SetIndexerStart(fn func(fam *FileAccessManager, store any)) {
	fam.indexerStart = fn
}

// SetImageloaderStart wires fa_imageloader_init (C: fileaccess.c:1434).
// Provider: pkg/backend/core (owns the be_file imageloader).
func (fam *FileAccessManager) SetImageloaderStart(fn func(fam *FileAccessManager)) {
	fam.imageloaderStart = fn
}

// SetPluginProbeForAutoinstall wires plugin_probe_for_autoinstall
// (C: ENABLE_PLUGINS — fa_probe.c:570). Provider: ctx.pluginManager.
func (fam *FileAccessManager) SetPluginProbeForAutoinstall(
	fn func(fh any, buf []byte, l int, url string)) {
	fam.pluginProbeForAutoinstall = fn
}

// PluginProbeForAutoinstall returns the plugin probe hook (nil if no
// plugin manager wired — C: ENABLE_PLUGINS compile gate).
func (fam *FileAccessManager) PluginProbeForAutoinstall() func(
	fh any, buf []byte, l int, url string) {
	return fam.pluginProbeForAutoinstall
}

// SetSettingsStart wires the settings tail of fileaccess_init
// (C: fileaccess.c:1441-1469). Provider: pkg/fileaccess/settings.
func (fam *FileAccessManager) SetSettingsStart(fn func()) { fam.settingsSetup = fn }

// SetKeyringLookup wires keyring_lookup (C: keyring.c direct call from
// fa_ftp.c auth). Provider: ctx.keyring.
func (fam *FileAccessManager) SetKeyringLookup(fn func(id string, username, password, domain *string,
	rememberMe *int, source, reason string, flags int) int) {
	fam.keyringLookup = fn
}

// SetMessagePopup wires message_popup (C: notifications.c direct call
// from fileaccess delete-verify).
func (fam *FileAccessManager) SetMessagePopup(fn func(msg string, flags int, extra []string) int) {
	fam.messagePopup = fn
}

// SetHTTPCookieBridge wires cookie_persist/load_cookies
// (C: htsmsg_store_save/load("httpcookies")). Provider: pkg/htsmsg
// (imports this package — cycle seam). `store` is the htsmsg.Store as
// `any` for the same reason.
func (fam *FileAccessManager) SetHTTPCookieBridge(store any,
	save func(store any, records []HTTPCookieRecord),
	load func(store any) []HTTPCookieRecord) {
	fam.httpCookies.cookieStore = store
	fam.httpCookies.cookieSave = save
	fam.httpCookies.cookieLoad = load
}

// SetDAVXMLDeserializer wires htsmsg_xml_deserialize_buf for DAV
// PROPFIND replies (C: direct call from fa_http.c). Provider:
// pkg/htsmsg (imports this package — inverted seam).
func (fam *FileAccessManager) SetDAVXMLDeserializer(fn func(buf []byte) (*DAVXMLMap, error)) {
	fam.httpConns.davXML = fn
}

// GetBundleManager returns the bundle manager
func (fam *FileAccessManager) GetBundleManager() *BundleManager {
	if fam == nil {
		return nil
	}
	return fam.bundleManager
}

// RegisterProtocol registers a protocol handler for file access
func (fam *FileAccessManager) RegisterProtocol(proto Protocol) {
	if fam == nil {
		return
	}
	fam.protocols = append(fam.protocols, proto)
}

// UnregisterProtocol removes a protocol from the fam dispatch list —
// the fam-level counterpart of UnregisterFAProtocol for adapter Protocols.
func (fam *FileAccessManager) UnregisterProtocol(proto Protocol) {
	if fam == nil {
		return
	}
	for i, p := range fam.protocols {
		if p == proto {
			fam.protocols = slices.Delete(fam.protocols, i, i+1)
			return
		}
	}
}

// FindProtocol finds a protocol that can handle the given URL
func (fam *FileAccessManager) FindProtocol(url string) Protocol {
	if fam == nil {
		return nil
	}
	for _, proto := range fam.protocols {
		if proto.CanHandle(url) {
			return proto
		}
	}
	return nil
}

// SetNativeProtocol sets the native filesystem protocol
func (fam *FileAccessManager) SetNativeProtocol(proto *FAProtocol) {
	proto.fam = fam
	fam.protocolMutex.Lock()
	defer fam.protocolMutex.Unlock()

	fam.nativeProto = proto
}

// SetDataProtocol sets the data: protocol
// C: fa_protocol_data — extern in fa_resolve_proto (fileaccess.c:104-108)
func (fam *FileAccessManager) SetDataProtocol(proto *FAProtocol) {
	proto.fam = fam
	fam.protocolMutex.Lock()
	defer fam.protocolMutex.Unlock()

	fam.faProtocolData = proto
}

// GetNativeProtocol returns the native filesystem protocol
func (fam *FileAccessManager) GetNativeProtocol() *FAProtocol {
	fam.protocolMutex.Lock()
	defer fam.protocolMutex.Unlock()

	return fam.nativeProto
}
