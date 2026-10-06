package core

// Split from backend.go — Fase 4 pure-move refactor.

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/czz/movian-go/internal/gconf"

	"github.com/czz/movian-go/internal/backend/playlist/playqueue"
	"github.com/czz/movian-go/internal/backend/prop"
	"github.com/czz/movian-go/internal/backend/slideshow"
	"github.com/czz/movian-go/internal/callout"
	"github.com/czz/movian-go/internal/db/kvstore"
	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/fileaccess/scanner"
	"github.com/czz/movian-go/internal/keyring"
	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/metadata"
	"github.com/czz/movian-go/internal/misc"
	"github.com/czz/movian-go/internal/nls"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/service"
	"github.com/czz/movian-go/internal/settings"
	"github.com/czz/movian-go/internal/subtitles"
	"github.com/czz/movian-go/internal/trace"
	"github.com/czz/movian-go/internal/upgrade"
	"github.com/czz/movian-go/internal/usage"
	videopkg "github.com/czz/movian-go/internal/video"
)

func (bs *BackendSystem) SetPlaybackInfoFn(f videopkg.VPIHandler) { bs.playbackInfoFn = f }

// VideoArgs represents arguments for video playback
// C: BACKEND_VIDEO_* flags (backend.h:50-54)
type BackendSystem struct {
	backends      []*Backend
	backendsMutex sync.RWMutex

	// Dynamic backends (separate list for dynamically registered backends)
	dynamicBackends      []*Backend
	dynamicBackendsMutex sync.RWMutex

	// Image loading system
	loadingImages   []*LoadingImage
	cachedImages    []*LoadingImage
	numCachedImages int
	imageloaderMu   sync.Mutex
	// C: hts_cond_t imageloader_cond — broadcast wakes ALL waiters.
	// sync.Cond.Broadcast guarantees all blocked waiters wake up.
	imageDoneCond *sync.Cond

	// Property manager
	propManager *propcore.PropManager

	// Global sources property
	globalSources *propcore.Prop

	// Prop page manager
	propPageManager *prop.PropPageManager

	// playbackInfoFn — C: the vpi_handlers LIST_HEAD global
	// (video_playback.c) — injected.
	playbackInfoFn videopkg.VPIHandler

	// locatedbEnabled — C: static int locatedb_enabled (fa_locatedb.c:43)
	locatedbEnabled int

	// RTMP backend registration is external (pkg/backend/rtmp imports
	// backend/core; rtmp.RegisterRTMPBackend is called from init.go —
	// htsp/bittorrent precedent).

	// Upgrade system — C: global upgrade singleton used by be_upgrade's
	// upgrade_open_url → upgrade_refresh().
	upgradeMgr *upgrade.Upgrade

	// Settings manager
	settingsMgr *settings.SettingsManager

	// gconf — C: gconf_t fields backends read. Owned by main, injected.
	gconf *gconf.T

	// mediaSys — C: the media.c subsystem globals (media_pipelines,
	// media_primary, media_buffer_hungry). Owned by main, injected.
	mediaSys *mediacore.MediaSystem
	il       *imageLoader      // C: fa_imageloader.c file statics
	indexer  *scanner.Indexer  // C: fa_indexer.c file statics
	subSys   *subtitles.System // C: subtitles.c file statics

	// searchSettings caches the "Search" settings dir.
	// C: search_get_settings() static prop_t *p (src/backend/search.c:73)
	searchSettings *propcore.Prop

	// Plugin models (lazily created, matching C's plugin_start_model / plugin_repo_model)
	pluginStartModel *propcore.Prop
	pluginRepoModel  *propcore.Prop

	// Plugin manager (for plugin_search) — uses interface to avoid import cycle
	pluginManager PluginSearchProvider

	// Service system
	serviceSystem *service.ServiceSystem

	// Trace system
	traceSystem *trace.TraceSystem

	// Usage reporter — C: the usage.c global singleton (usage_event/
	// usage_page_open). Injected; backends reach it via bs.Usage().
	usage *usage.Reporter

	// File access manager for file:// URL handling
	fileAccessManager *fileaccesscore.FileAccessManager
	playQueue         *playqueue.PlayQueue      // C: playqueue singleton — injected via SetPlayQueue
	keyring           *keyring.Keyring          // C: implicit global keyring
	kvstore           *kvstore.KVStore          // C: global kvstore_* funcs
	calloutSystem     *callout.CalloutSystem    // C: callout_* globals
	metadata          *metadata.MetadataManager // C: implicit default manager

	// FileBackend instance (for setting playqueue callbacks after creation)
	fileBackend *fileaccesscore.FileBackend

	// Link-time seams — C resolves these as direct cross-module calls
	// (fa_video.c → hls_play_extm3u / dvd_play / freetype_*,
	// video_playback.c → app_shutdown / video_settings,
	// fileaccess_backend.c → plugin_open_file / nav_redirect).
	// In Go the providers import backendcore, so the wiring is inverted:
	// fields on the system set by init/providers.
	hlsPlayExtm3u  func(content []byte, url string, mp *mediacore.MediaPipe, vq, vsl any, va *VideoArgs) (any, error)
	dvdPlay        func(url string, mp *mediacore.MediaPipe, vfs int) (any, error)
	freetype       FreetypeOps
	appShutdown    func(retcode int)
	videoSettings  func() (resumeMode int, continuousPlayback int)
	pluginOpenFile func(page *propcore.Prop, url string)
	navRedirect    func(pm *propcore.PropManager, page *propcore.Prop, url string)
	// fileBE — C: &be_file — the fileaccess Backend itself, for
	// file_open_file's "newbe != &be_file" redirect check.
	fileBE *Backend
}

// LoadingImage represents a loading image (loading_image_t in C)
func NewBackendSystem(pm *propcore.PropManager, ss *service.ServiceSystem, ts *trace.TraceSystem, u *usage.Reporter) *BackendSystem {
	bs := &BackendSystem{
		locatedbEnabled: 1, // C: locatedb_enabled = 1 static init
		usage:           u,
		backends:        make([]*Backend, 0),
		dynamicBackends: make([]*Backend, 0),
		loadingImages:   make([]*LoadingImage, 0),
		cachedImages:    make([]*LoadingImage, 0),
		numCachedImages: 0,
		propManager:     pm,
		serviceSystem:   ss,
		traceSystem:     ts,
		globalSources:   pm.CreateRootEx("sources", false),
		// C: proppages is a file-static list in backend_prop.c —
		// shared process-wide (settings_init calls backend_prop_make
		// before backend_init).
		propPageManager: prop.NewPropPageManager(),
		metadata:        metadata.NewMetadataManager(pm, nil),
		gconf:           gconf.New(),
		// Default media system — init may override via SetMediaSystem.
		mediaSys: mediacore.NewMediaSystem(),
		// C: fa_imageloader.c file statics — owned by the backend
		// system.
		il: newImageLoader(),
	}
	// C: hts_cond_init(&imageloader_cond) — init the broadcast condition.
	bs.imageDoneCond = sync.NewCond(&bs.imageloaderMu)
	return bs
}

// SetUpgradeMgr sets the upgrade system for the backend system
// (C: be_upgrade's upgrade_open_url calls the global upgrade_refresh()).
func (bs *BackendSystem) SetUpgradeMgr(upgradeMgr *upgrade.Upgrade) {
	bs.upgradeMgr = upgradeMgr
}

// GetPropManager returns the PropManager used by this BackendSystem.
// Backends need this to create child props on pages (e.g. model, type, url).
func (bs *BackendSystem) GetPropManager() *propcore.PropManager {
	return bs.propManager
}

// Usage returns the usage reporter — C: usage_event()/usage_page_open()
// global functions. Injected at NewBackendSystem; nil-receiver safe and
// the reporter methods are nil-safe, so callers never need to check.
func (bs *BackendSystem) Usage() *usage.Reporter {
	if bs == nil {
		return nil
	}
	return bs.usage
}

// SetSettingsMgr sets the settings manager for the backend system
// SetGconf injects the process gconf (C: gconf_t — owned by main).
func (bs *BackendSystem) SetGconf(g *gconf.T) { bs.gconf = g }

// Gconf — C: gconf_t gconf as read by backend code.
func (bs *BackendSystem) Gconf() *gconf.T { return bs.gconf }

// SetMediaSystem wires the media system (C's media.c globals).
func (bs *BackendSystem) SetMediaSystem(ms *mediacore.MediaSystem) {
	bs.mediaSys = ms
	ms.Owner = bs
}

// MediaSys returns the wired media system.
func (bs *BackendSystem) MediaSys() *mediacore.MediaSystem { return bs.mediaSys }

// SetIndexer injects the file indexer (C: fa_indexer.c statics).
func (bs *BackendSystem) SetIndexer(ix *scanner.Indexer) { bs.indexer = ix }

// Indexer returns the injected indexer (nil until SetIndexer).
func (bs *BackendSystem) Indexer() *scanner.Indexer { return bs.indexer }

// SetSubtitleSystem injects the subtitles subsystem (C: subtitles.c
// statics).
func (bs *BackendSystem) SetSubtitleSystem(s *subtitles.System) { bs.subSys = s }

// SubSys returns the injected subtitles system (nil until wired).
func (bs *BackendSystem) SubSys() *subtitles.System { return bs.subSys }

// FAImageloaderStart — C: fa_imageloader_init (delegates to the
// backend-owned imageLoader; registered via fam.SetImageloaderStart).
func (bs *BackendSystem) FAImageloaderStart(fam *fileaccesscore.FileAccessManager) {
	bs.il.FAImageloaderStart(fam)
}
func (bs *BackendSystem) SetSettingsMgr(settingsMgr *settings.SettingsManager) {
	bs.settingsMgr = settingsMgr
}

// SetPlayQueue injects the playqueue (C: playqueue process-global).
func (bs *BackendSystem) SetPlayQueue(pq *playqueue.PlayQueue) { bs.playQueue = pq }

// SetKeyring injects the keyring (C: implicit global keyring_lookup).
func (bs *BackendSystem) SetKeyring(kr *keyring.Keyring) { bs.keyring = kr }

// SetKVStore injects the kvstore (C: global kvstore_* funcs reached by
// the fileaccess scanner backend).
func (bs *BackendSystem) SetKVStore(kvs *kvstore.KVStore) { bs.kvstore = kvs }

// SetCalloutSystem injects the callout system (C: callout_* globals —
// slideshow timers, dvd probe, imageloader thumb flush).
func (bs *BackendSystem) SetCalloutSystem(cs *callout.CalloutSystem) {
	bs.calloutSystem = cs
}

// SetPropPageManager injects the shared prop page manager (C: the
// file-static proppages list in backend_prop.c is process-wide —
// settings_init calls backend_prop_make before backend_init, so init
// owns the instance and injects it into every consumer).
func (bs *BackendSystem) SetPropPageManager(ppm *prop.PropPageManager) {
	bs.propPageManager = ppm
}

// SetMetadataManager injects the metadata manager (C: implicit default
// manager — metadb_*/playinfo_* free funcs hit the singleton).
func (bs *BackendSystem) SetMetadataManager(mm *metadata.MetadataManager) {
	bs.metadata = mm
}

// Metadata returns the injected metadata manager.
func (bs *BackendSystem) Metadata() *metadata.MetadataManager { return bs.metadata }

// FileAccessManager returns the injected file access manager.
func (bs *BackendSystem) FileAccessManager() *fileaccesscore.FileAccessManager {
	if bs == nil {
		return nil
	}
	return bs.fileAccessManager
}

// TraceSystem returns the injected trace system (C: trace.c globals).
// Nil-safe — unwired callers emit no trace output.
func (bs *BackendSystem) TraceSystem() *trace.TraceSystem {
	if bs == nil {
		return nil
	}
	return bs.traceSystem
}

// CalloutSystem returns the injected callout system (nil until wired).
func (bs *BackendSystem) CalloutSystem() *callout.CalloutSystem {
	return bs.calloutSystem
}

// Keyring returns the injected keyring (nil until wired).
func (bs *BackendSystem) Keyring() *keyring.Keyring { return bs.keyring }

// FreetypeOps — C: freetype_get_context / freetype_load_dynamic_font_buf /
// freetype_unload_font called directly from fa_video.c (attachment fonts).
// Consumer-side interface: pkg/text implements it (it already imports
// backendcore for the fontstash backend).
func (bs *BackendSystem) SetHLSPlayer(fn func(content []byte, url string, mp *mediacore.MediaPipe, vq, vsl any, va *VideoArgs) (any, error)) {
	bs.hlsPlayExtm3u = fn
}

// HasHLSPlayer reports whether the hls seam is wired (BE_REGISTER(hls)).
func (bs *BackendSystem) HasHLSPlayer() bool { return bs.hlsPlayExtm3u != nil }

// SetDVDPlayer wires dvd_play (C: direct call fa_video.c — ENABLE_DVD).
func (bs *BackendSystem) SetDVDPlayer(fn func(url string, mp *mediacore.MediaPipe, vfs int) (any, error)) {
	bs.dvdPlay = fn
}

// SetFreetypeOps wires the freetype attachment-font ops (C: direct calls).
func (bs *BackendSystem) SetFreetypeOps(ops FreetypeOps) { bs.freetype = ops }

// SetAppShutdown wires app_shutdown (C: main.c:787 direct call).
func (bs *BackendSystem) SetAppShutdown(fn func(retcode int)) { bs.appShutdown = fn }

// SetVideoSettings wires the global video_settings read (C: video_settings.c).
func (bs *BackendSystem) SetVideoSettings(fn func() (resumeMode int, continuousPlayback int)) {
	bs.videoSettings = fn
}

// SetPluginOpenFile wires plugin_open_file (C: ENABLE_PLUGINS direct call).
func (bs *BackendSystem) SetPluginOpenFile(fn func(page *propcore.Prop, url string)) {
	bs.pluginOpenFile = fn
}

// SetNavRedirect wires nav_redirect (C: navigator.c:972 direct call).
func (bs *BackendSystem) SetNavRedirect(fn func(pm *propcore.PropManager, page *propcore.Prop, url string)) {
	bs.navRedirect = fn
}

// SetFileAccessManager sets the file access manager for file:// URL handling
func (bs *BackendSystem) SetFileAccessManager(fam *fileaccesscore.FileAccessManager) {
	bs.fileAccessManager = fam
}

// SetPluginManager sets the plugin manager for plugin_search support
func (bs *BackendSystem) SetPluginManager(pm PluginSearchProvider) {
	bs.pluginManager = pm
}

// PluginSearchProvider provides plugin search functionality.
// Implemented by *plugins.PluginManager. Uses interface to avoid
// import cycle between backend/core and plugins.
func (bs *BackendSystem) GetFileAccessManager() *fileaccesscore.FileAccessManager {
	return bs.fileAccessManager
}

// GetServiceSystem returns the service system (C: global services list)
func (bs *BackendSystem) GetServiceSystem() *service.ServiceSystem {
	return bs.serviceSystem
}

// Start initializes the backend system
func (bs *BackendSystem) Start() {
	// C: backend_init does NOT clear the static backends list —
	// BE_REGISTER backends registered before backend_init (e.g.
	// fontstash via INIT_GROUP_GRAPHICS) must survive. Clearing it
	// silently dropped fontstash: → "No handler for URL".
	bs.loadingImages = make([]*LoadingImage, 0)
	bs.cachedImages = make([]*LoadingImage, 0)
	bs.numCachedImages = 0
	bs.imageDoneCond = sync.NewCond(&bs.imageloaderMu)
	bs.globalSources = bs.propManager.Create("sources")

	// Register the prop backend (handles prop: URLs)
	be := &Backend{
		Prefix: "prop:",
		Flags:  BackendOpenChecksURI,
	}
	be.CanHandle = func(url string) int {
		if len(url) > 5 && url[:5] == "prop:" {
			return 1
		}
		return 0
	}
	be.Open = func(page any, url string, sync bool) error {
		propRoot, ok := page.(*propcore.Prop)
		if !ok {
			// C: be_open returns non-zero for BACKEND_OPEN_CHECKS_URI backends
			// when it can't handle the URL, so the loop continues.
			return fmt.Errorf("prop: backend cannot handle non-prop page")
		}
		bs.propPageManager.PropPagesMu.RLock()
		defer bs.propPageManager.PropPagesMu.RUnlock()

		var pp *prop.PropPage
		for _, p := range bs.propPageManager.PropPages {
			if p.URL == url {
				pp = p
				break
			}
		}

		if pp == nil {
			// C: no matching prop page — return non-zero so loop continues
			return fmt.Errorf("prop: no matching prop page for %s", url)
		}

		pp.Mu.Lock()
		defer pp.Mu.Unlock()

		op := &prop.OpenPage{
			Root: bs.propManager.RefInc(propRoot), // C: prop_ref_inc(page)
			Pp:   pp,
		}

		op.PageSub = propRoot.Subscribe(op.PageCallback, op, propcore.SubFlagTrackDestroy)

		closeProp := propRoot.FindChild("directClose")
		if closeProp == nil {
			closeProp = bs.propManager.CreateEx(propRoot, "directClose", nil, false, false)
		}
		closeProp.SetInt(1)

		modelProp := propRoot.FindChild("model")
		if modelProp == nil {
			modelProp = bs.propManager.CreateEx(propRoot, "model", nil, false, false)
		}
		bs.propManager.Link(pp.Model, modelProp, nil, false, false)

		pp.Pages = append(pp.Pages, op)

		return nil
	}
	bs.Register(be)

	// Register the page backend (handles page: URLs)
	bs.registerPageBackend()

	// Register the settings backend (handles settings: URLs)
	bs.registerSettingsBackend()

	// Register the plugin backend (handles plugin: URLs)
	bs.registerPluginBackend()

	// Register the discovered backend (handles discovered: URLs)
	bs.registerDiscoveredBackend()

	// Register the search backend (handles search: URLs)
	bs.registerSearchBackend()

	// RTMP backend registration is done externally to avoid import cycles

	// Bittorrent backend registration is done externally to avoid import cycles

	// Register the fileaccess backend (handles file: URLs)
	bs.registerFileaccessBackend()

	// C: BE_REGISTER(playlist) — be_playlist (src/backend/playlist.c:122-129)
	bs.registerPlaylistBackend()

	// Register additional backends
	bs.registerSlideshowBackend()

	// C: BE_REGISTER(upgrade) — be_upgrade handles "showtime:upgrade"
	bs.registerUpgradeBackend()

	bs.registerVideoparamsBackend()

	// C: BE_REGISTER(pixmap) — be_pixmap (src/image/pixmap.c:1111-1117)
	bs.registerPixmapBackend()
}

// Fini finalizes the backend system
func (bs *BackendSystem) Fini() {
	bs.backendsMutex.Lock()
	defer bs.backendsMutex.Unlock()

	for _, backend := range bs.backends {
		if backend.Fini != nil {
			backend.Fini()
		}
	}
	bs.backends = nil

	// Cleanup image cache
	bs.imageloaderMu.Lock()
	for _, li := range bs.cachedImages {
		// Release image if needed
		_ = li
	}
	bs.cachedImages = nil
	bs.loadingImages = nil
	bs.numCachedImages = 0
	bs.imageloaderMu.Unlock()
}

// GetGlobalSources returns the global sources property
func (bs *BackendSystem) GetGlobalSources() *propcore.Prop {
	return bs.globalSources
}

// GetPropPageManager returns the prop page manager for creating prop: URLs
func (bs *BackendSystem) GetPropPageManager() *prop.PropPageManager {
	return bs.propPageManager
}

// Retain retains a backend
func (bs *BackendSystem) Retain(backend *Backend) *Backend {
	if backend == nil {
		return nil
	}
	atomic.AddInt32(&backend.Refcount, 1)
	return backend
}

// Release releases a backend
// C: backend_release (backend.c:679-688) — only destroys dynamic backends.
// Non-dynamic backends have no-op release (refcount not decremented).
func (bs *BackendSystem) Release(backend *Backend) {
	if backend == nil {
		return
	}
	// C: if(!(be->be_flags & BACKEND_DYNAMIC)) return;
	if backend.Flags&BackendDynamic == 0 {
		return
	}
	if atomic.AddInt32(&backend.Refcount, -1) == 0 {
		if backend.Destroy != nil {
			backend.Destroy(backend)
		}
	}
}

// Open opens a URL with a backend
// C: backend_open (backend.c:561-614) — 3-phase dispatch:
//  1. Dynamic backends: try be_open2 (set page.url first)
//  2. BACKEND_OPEN_CHECKS_URI backends: try be_open, continue on error
//  3. backend_canhandle: normalize URL, set page.url, call be_open
func (bs *BackendSystem) Register(backend *Backend) {
	if backend == nil {
		return
	}

	bs.backendsMutex.Lock()
	defer bs.backendsMutex.Unlock()

	backend.Refcount = 1
	bs.backends = append(bs.backends, backend)
}

// StartBackends initializes all registered backends.
// C: backend_init (backend.c:121-124) — iterates all backends and calls
// be_init on each. Called once after all backends are registered.
func (bs *BackendSystem) StartBackends() {
	bs.backendsMutex.Lock()
	defer bs.backendsMutex.Unlock()

	for _, backend := range bs.backends {
		if backend.Start != nil {
			backend.Start()
		}
	}
}

// RegisterDynamic registers a dynamic backend
// C: backend_register_dynamic (backend.c:88-98) — only inserts into
// the dynamic backends list with refcount=1 and BackendDynamic flag.
// Does NOT call be_init.
func (bs *BackendSystem) RegisterDynamic(backend *Backend) {
	if backend == nil {
		return
	}

	backend.Flags |= BackendDynamic
	bs.dynamicBackendsMutex.Lock()
	defer bs.dynamicBackendsMutex.Unlock()

	backend.Refcount = 1
	bs.dynamicBackends = append(bs.dynamicBackends, backend)
}

// UnregisterDynamic unregisters a dynamic backend
// C: backend_unregister_dynamic (backend.c:100-105) — only removes from
// the dynamic backends list. Does NOT call backend_release.
func (bs *BackendSystem) UnregisterDynamic(backend *Backend) {
	if backend == nil {
		return
	}

	bs.dynamicBackendsMutex.Lock()
	defer bs.dynamicBackendsMutex.Unlock()

	for i, b := range bs.dynamicBackends {
		if b == backend {
			bs.dynamicBackends = slices.Delete(bs.dynamicBackends, i, i+1)
			break
		}
	}
}

// OpenVideo opens a video URL
// ResolveItem resolves an item
// C: backend_resolve_item (backend.c:643-650) — returns -1 if no handler,
// otherwise returns be_resolve_item result.
func (bs *BackendSystem) registerPageBackend() {
	backend := &Backend{
		Prefix: "page:",
	}
	backend.CanHandle = func(url string) int {
		if len(url) > 5 && url[:5] == "page:" {
			return 1
		}
		return 0
	}
	backend.Open = func(page any, url string, sync bool) error {
		if page == nil {
			return nil
		}

		// Try to convert to propcore.Prop
		propRoot, ok := page.(*propcore.Prop)
		if !ok {
			return nil
		}

		bs.backendPageOpen(propRoot, url)
		return nil
	}
	bs.Register(backend)
}

// backendPageOpen — C: backend_page_open (backend.c:196-210). Shared by
// be_page.be_open and be_upgrade's upgrade_open_url (which calls it with
// "page:upgrade"). Creates model/{type,metadata/title} from the page: URL.
func (bs *BackendSystem) registerUpgradeBackend() {
	backend := &Backend{
		Prefix: "showtime:upgrade",
	}
	backend.CanHandle = func(url string) int {
		// C: upgrade_canhandle — !strcmp(url, "showtime:upgrade")
		if url == "showtime:upgrade" {
			return 1
		}
		return 0
	}
	backend.Open = func(page any, url string, sync bool) error {
		propRoot, _ := page.(*propcore.Prop)
		if propRoot == nil {
			return nil
		}

		if url == "showtime:upgrade" {
			// C: usage_page_open(sync, "Upgrade")
			bs.usage.PageOpen(sync, "Upgrade")
			// C: backend_page_open(page, "page:upgrade", sync)
			bs.backendPageOpen(propRoot, "page:upgrade")
			// C: upgrade_refresh()
			if bs.upgradeMgr != nil {
				bs.upgradeMgr.UpgradeRefresh()
			}
			// C: prop_set(page, "directClose", PROP_SET_INT, 1)
			bs.propManager.SetIntEx(
				bs.propManager.CreateEx(propRoot, "directClose", nil, false, false),
				nil, 1)
		} else {
			// C: nav_open_error(page, "Invalid URI")
			modelProp := bs.propManager.CreateEx(propRoot, "model", nil, false, false)
			typeProp := bs.propManager.CreateEx(modelProp, "type", nil, false, false)
			bs.propManager.SetStringEx(typeProp, nil, "openerror", propcore.StringUTF8)
			errProp := bs.propManager.CreateEx(modelProp, "error", nil, false, false)
			bs.propManager.SetStringEx(errProp, nil, "Invalid URI", propcore.StringUTF8)
		}
		return nil
	}
	bs.Register(backend)
}

// registerSettingsBackend registers the settings backend for handling settings: URLs
func (bs *BackendSystem) registerSettingsBackend() {
	backend := &Backend{
		Prefix: "settings:",
	}
	backend.CanHandle = func(url string) int {
		if len(url) >= 9 && url[:9] == "settings:" {
			return 1
		}
		return 0
	}
	backend.Open = func(page any, url string, sync bool) error {
		// Get the property root for the page
		if page == nil {
			return nil
		}

		// Try to convert to propcore.Prop
		propRoot, ok := page.(*propcore.Prop)
		if !ok {
			return nil
		}

		// C: usage_page_open(sync, "Settings") (settings.c:1454)
		bs.usage.PageOpen(sync, "Settings")

		// C: be_settings_open() does just:
		//   prop_set(page, "model", PROP_SET_LINK, settings_model);
		// This links page.model to the global settings_model, which has
		// type="settings", metadata.title, and nodes (with all settings dirs).
		// The cloner in list.view reads $self.model.nodes and
		// $self.model.metadata.title via GetChildren() which follows the link.

		// Create model property on the page
		modelProp := bs.propManager.CreateEx(propRoot, "model", nil, false, false)

		// Link page.model to the global settingsModel (matching C's PROP_SET_LINK)
		if bs.settingsMgr != nil {
			globalModel := bs.settingsMgr.GetModel()
			if globalModel != nil {
				bs.propManager.Link(globalModel, modelProp, nil, false, false)
				return nil
			}
		}

		// Fallback: if settingsMgr is not available, set model.type="settings"
		// directly so the view loader can find settings.view.
		// C: settings_model has type="settings" (settings.c:1349)
		typeProp := bs.propManager.CreateEx(modelProp, "type", nil, false, false)
		bs.propManager.SetStringEx(typeProp, nil, "settings", propcore.StringUTF8)

		return nil
	}
	bs.Register(backend)
}

// registerPluginBackend registers the plugin backend for handling plugin: URLs.
// Reproduces C's plugin_open_url (src/plugins.c:1698):
//   - "plugin:start" → prop_link(plugin_start_model, page.model)
//   - "plugin:repo"  → prop_link(plugin_repo_model, page.model)
//   - "plugin:repo:categories" / "plugin:repo:<cat>" → open category page
//
// The plugin_start_model is a root prop with type="directory", metadata.title="Plugins",
// and nodes containing a "store" item + installed plugins filter.
func (bs *BackendSystem) registerPluginBackend() {
	backend := &Backend{
		Prefix: "plugin:",
	}
	backend.CanHandle = func(url string) int {
		if len(url) >= 7 && url[:7] == "plugin:" {
			return 1
		}
		return 0
	}
	backend.Open = func(page any, url string, sync bool) error {
		if page == nil {
			return nil
		}
		propRoot, ok := page.(*propcore.Prop)
		if !ok {
			return nil
		}

		modelProp := bs.propManager.CreateEx(propRoot, "model", nil, false, false)

		switch {
		case url == "plugin:start":
			// C: usage_page_open(sync, "Plugins installed") (plugins.c:1703)
			bs.usage.PageOpen(sync, "Plugins installed")
			// C: prop_link(plugin_start_model, model)
			// Create or get the plugin start model and link it to page.model
			startModel := bs.getOrCreatePluginStartModel()
			if startModel != nil {
				bs.propManager.Link(startModel, modelProp, nil, false, false)
			}

		case url == "plugin:repo":
			// C: usage_page_open(sync, "Plugins repo") (plugins.c:1719)
			bs.usage.PageOpen(sync, "Plugins repo")
			// C: prop_link(plugin_repo_model, model) + plugins_upgrade_check()
			repoModel := bs.getOrCreatePluginRepoModel()
			if repoModel != nil {
				bs.propManager.Link(repoModel, modelProp, nil, false, false)
			}
			if bs.pluginManager != nil {
				_ = bs.pluginManager.LoadAllRepos()
			}

		case strings.HasPrefix(url, "plugin:repo:"):
			// C: plugin_open_url — plugin:repo:<x> (plugins.c:1705-1714)
			x := url[len("plugin:repo:"):]
			// C: snprintf(usage, 64, "Plugins %s", x); usage_page_open(...)
			bs.usage.PageOpen(sync, "Plugins "+x)
			if x == "categories" {
				bs.openPluginCategories(modelProp)
			} else {
				bs.openPluginCategoryPage(modelProp, x)
				// C: plugins_upgrade_check() after open_category_page
				if bs.pluginManager != nil {
					_ = bs.pluginManager.LoadAllRepos()
				}
			}

		default:
			// C: nav_open_error(page, "Invalid URI")
			typeProp := bs.propManager.CreateEx(modelProp, "type", nil, false, false)
			bs.propManager.SetStringEx(typeProp, nil, "openerror", propcore.StringUTF8)
			errProp := bs.propManager.CreateEx(modelProp, "error", nil, false, false)
			bs.propManager.SetStringEx(errProp, nil, "Invalid plugin URI", propcore.StringUTF8)
		}

		return nil
	}

	// C: .be_search = plugin_search (src/plugins.c:1776)
	// plugin_search creates a "Plugins" search class under model.nodes,
	// then iterates all plugins and prop_link's those whose title
	// contains the query string (case-insensitive).
	backend.Search = func(model any, query string, loading any) {
		modelProp, ok := model.(*propcore.Prop)
		if !ok || modelProp == nil {
			return
		}
		bs.pluginSearch(modelProp, query)
	}

	bs.Register(backend)
}

// pluginCatTitle — C: plugin_category_set_title_in_model (plugins.c:1087-1125).
// Maps a catnames entry to its nls display title.
func (bs *BackendSystem) registerDiscoveredBackend() {
	backend := &Backend{
		Prefix: "discovered:",
	}
	backend.CanHandle = func(url string) int {
		if url == "discovered:" {
			return 1
		}
		return 0
	}
	backend.Open = func(page any, url string, sync bool) error {
		if page == nil {
			return nil
		}
		propRoot, ok := page.(*propcore.Prop)
		if !ok {
			return nil
		}

		// C: usage_page_open(sync, "Discovered") (service.c:546)
		bs.usage.PageOpen(sync, "Discovered")

		// C: model = prop_create_r(page, "model")
		modelProp := bs.propManager.CreateEx(propRoot, "model", nil, false, false)

		// C: prop_setv(model, "metadata", "title", NULL, PROP_SET_LINK, _p("Local network"))
		metaProp := bs.propManager.CreateEx(modelProp, "metadata", nil, false, false)
		titleProp := bs.propManager.CreateEx(metaProp, "title", nil, false, false)
		bs.propManager.SetStringEx(titleProp, nil, "Local network", propcore.StringUTF8)

		// C: prop_set(model, "type", PROP_SET_STRING, "directory")
		typeProp := bs.propManager.CreateEx(modelProp, "type", nil, false, false)
		bs.propManager.SetStringEx(typeProp, nil, "directory", propcore.StringUTF8)

		// C: nodes = prop_create_r(model, "nodes"); prop_link(discovered_nodes, nodes)
		// The discovered_nodes are maintained by the service discovery system.
		// Create an empty nodes prop for now; the service system will populate it.
		nodesProp := bs.propManager.CreateEx(modelProp, "nodes", nil, false, false)

		// Link discovered nodes from service system if available
		if bs.serviceSystem != nil {
			discoveredNodes := bs.serviceSystem.GetDiscoveredNodesProp()
			if discoveredNodes != nil {
				bs.propManager.Link(discoveredNodes, nodesProp, nil, false, false)
			}
		}

		return nil
	}
	bs.Register(backend)
}

// registerSearchBackend registers the search backend for handling search: URLs
// Reproduces C's search_open (src/backend/search.c:99):
//  1. Call backend_open(page, url, sync) to resolve the inner URL
//  2. Create model.type = "directory"
//  3. Create model.metadata.title = "Search result for: <query>"
//  4. Create model.contents = "searchresults"
//  5. Create page.source with source.nodes
//  6. Create PropNF linking source.nodes → model.nodes with sort + filter
//  7. Call backend_search(source, url, loading) to perform actual search
func (bs *BackendSystem) registerSearchBackend() {
	backend := &Backend{
		Prefix: "search:",
	}
	backend.CanHandle = func(url string) int {
		if len(url) > 7 && url[:7] == "search:" {
			return 1
		}
		return 0
	}
	// C: search_open (src/backend/search.c:92-147)
	backend.Open = func(page any, url0 string, sync bool) error {
		if page == nil {
			return nil
		}

		propRoot, ok := page.(*propcore.Prop)
		if !ok {
			return nil
		}

		// C: url = strchr(url0, ':'); url++
		_, after, ok := strings.Cut(url0, ":")
		if !ok {
			return fmt.Errorf("search_open: no ':' in %s", url0)
		}
		searchQuery := after

		// C: if(!backend_open(page, url, sync)) return 0;
		// If the inner URL opens as a page, search: acts as an alias.
		if bs.Open(propRoot, searchQuery, sync) == nil {
			return nil
		}

		// C: usage_page_open(sync, "Search")
		bs.usage.PageOpen(sync, "Search")

		// C: model = prop_create_r(page, "model")
		modelProp := bs.propManager.CreateEx(propRoot, "model", nil, false, false)

		// C: prop_set(model, "type", PROP_SET_STRING, "directory")
		typeProp := bs.propManager.CreateEx(modelProp, "type", nil, false, false)
		bs.propManager.SetStringEx(typeProp, nil, "directory", propcore.StringUTF8)

		// C: meta = prop_create_r(model, "metadata");
		//    fmt = _("Search result for: %s");
		//    snprintf(title, sizeof(title), rstr_get(fmt), url);
		//    prop_set(meta, "title", PROP_SET_STRING, title)
		metadataProp := bs.propManager.CreateEx(modelProp, "metadata", nil, false, false)
		titleProp := bs.propManager.CreateEx(metadataProp, "title", nil, false, false)
		bs.propManager.SetStringEx(titleProp, nil,
			fmt.Sprintf(nls.GetRString("Search result for: %s"), searchQuery),
			propcore.StringUTF8)

		// C: prop_set(model, "contents", PROP_SET_STRING, "searchresults")
		contentsProp := bs.propManager.CreateEx(modelProp, "contents", nil, false, false)
		bs.propManager.SetStringEx(contentsProp, nil, "searchresults", propcore.StringUTF8)

		// C: source = prop_create_r(page, "source")
		sourceProp := bs.propManager.CreateEx(propRoot, "source", nil, false, false)

		// C: model_nodes = prop_create_r(model, "nodes")
		// C: source_nodes = prop_create_r(source, "nodes")
		modelNodesProp := bs.propManager.CreateEx(modelProp, "nodes", nil, false, false)
		sourceNodesProp := bs.propManager.CreateEx(sourceProp, "nodes", nil, false, false)

		// C: loading = prop_create_r(model, "loading")
		loadingProp := bs.propManager.CreateEx(modelProp, "loading", nil, false, false)
		bs.propManager.SetIntEx(loadingProp, nil, 1) // start loading

		// C: pnf = prop_nf_create(model_nodes, source_nodes, NULL, PROP_NF_AUTODESTROY)
		// C: prop_nf_sort(pnf, "node.metadata.title", 0, 2, NULL, 1)
		// C: prop_nf_pred_int_add(pnf, "node.entries", PROP_NF_CMP_EQ, 0, NULL, PROP_NF_MODE_EXCLUDE)
		// PropNF replicates children from source_nodes to model_nodes with sorting.
		var searchPnf *scanner.PropNF
		if sourceNodesProp != nil && modelNodesProp != nil {
			searchPnf = scanner.NewPropNF(bs.propManager, sourceNodesProp, modelNodesProp)
			// C: prop_nf_sort(pnf, "node.metadata.title", 0, 2, NULL, 1)
			searchPnf.SetSortKey(0, "node.metadata.title")
			searchPnf.SetSortOrder(0, 1) // ascending
			// Exclude empty directories (nodes with 0 children) from search results
			searchPnf.AddPredicate("node.entries", scanner.PropNFPredEq, 0, scanner.PropNFModeExclude)
		}

		// C: backend_search(source, url, loading) — synchronous; async
		// search backends (locatedbSearch) spawn their own threads and
		// clear loading when their search completes. loading=0 is set
		// here only as a fallback when no backends have a Search handler.
		bs.backendsMutex.RLock()
		hasSearchBackend := false
		for _, backend := range bs.backends {
			if backend.Search != nil {
				hasSearchBackend = true
				break
			}
		}
		bs.backendsMutex.RUnlock()

		bs.Search(sourceProp, searchQuery, loadingProp)

		if !hasSearchBackend {
			bs.propManager.SetIntEx(loadingProp, nil, 0)
		}
		// C: prop_nf_release(pnf) — with PROP_NF_AUTODESTROY the PropNF
		// stays alive until the source prop is destroyed.
		if searchPnf != nil {
			searchPnf.Release()
		}

		return nil
	}
	bs.Register(backend)

	// Register OS-specific search backends.
	// C: BE_REGISTER(locatedb) exists only under CONFIG_LOCATEDB
	// (configure.linux); BE_REGISTER(spotlight) only under
	// CONFIG_SPOTLIGHT (configure.osx). registerFASearchBackends is
	// defined per build tag.
	bs.registerFASearchBackends()
}

// registerLocatedbSearchBackend registers a search-only backend that
// uses the Unix `locate` command to find files matching the query.
// Reproduces C's fa_locatedb.c:locatedb_search + fa_searcher.
func (bs *BackendSystem) registerLocatedbSearchBackend() {
	backend := &Backend{
		Prefix: "locatedb-search", // Not a URL handler — only provides Search
	}
	backend.CanHandle = func(url string) int {
		return 0 // This backend doesn't handle URLs, only provides be_search
	}
	backend.Open = func(page any, url string, sync bool) error {
		return nil // Not callable via URL
	}
	// C: be_locatedb.be_init = locatedb_init (fa_locatedb.c:419)
	backend.Start = func() error {
		return bs.locatedbSetup()
	}
	backend.Search = func(model any, query string, loading any) {
		modelProp, ok := model.(*propcore.Prop)
		if !ok || modelProp == nil {
			return
		}
		loadingProp, _ := loading.(*propcore.Prop)
		bs.locatedbSearch(modelProp, query, loadingProp)
	}
	bs.Register(backend)
}

// faSearchT — C: fa_search_t (fa_locatedb.c:45-52)
func (bs *BackendSystem) registerSlideshowBackend() {
	backend := &Backend{
		Prefix: "slideshow:",
	}
	backend.CanHandle = func(url string) int {
		// C: be_slideshow_canhandle — !strncmp(url, "slideshow:", 10)
		return slideshow.CanHandle(url)
	}
	backend.Open = func(page any, url string, sync bool) error {
		propRoot, ok := page.(*propcore.Prop)
		if !ok || propRoot == nil {
			return fmt.Errorf("slideshow: invalid page prop")
		}
		syncFlag := 0
		if sync {
			syncFlag = 1
		}
		if slideshow.Open(propRoot, url, syncFlag, bs.propManager, bs.settingsMgr,
			bs.calloutSystem) != 0 {
			return fmt.Errorf("slideshow: cannot open %s", url)
		}
		return nil
	}
	bs.Register(backend)
}

// registerVideoparamsBackend registers the videoparams backend for handling videoparams: URLs
func (bs *BackendSystem) registerVideoparamsBackend() {
	backend := &Backend{
		Prefix: "videoparams:",
		// C: be_videoparams (backend.c:548-551) sets no be_flags.
	}
	backend.CanHandle = func(url string) int {
		if strings.HasPrefix(url, "videoparams:") {
			return 1
		}
		return 0
	}
	backend.Open = func(page any, url string, sync bool) error {
		// C: .be_open = backend_open_video (backend.c:550)
		return bs.OpenVideo(page, url, sync)
	}
	bs.Register(backend)
}

// GetPrefix returns the backend prefix
func (backend *Backend) GetPrefix() string {
	if backend == nil {
		return ""
	}
	return backend.Prefix
}

// SetPrefix sets the backend prefix
func (backend *Backend) SetPrefix(prefix string) {
	if backend != nil {
		backend.Prefix = prefix
	}
}

// GetFlags returns the backend flags
func (backend *Backend) GetFlags() int {
	if backend == nil {
		return 0
	}
	return backend.Flags
}

// SetFlags sets the backend flags
func (backend *Backend) SetFlags(flags int) {
	if backend != nil {
		backend.Flags = flags
	}
}

// GetOpaque returns the opaque data
func (backend *Backend) GetOpaque() any {
	if backend == nil {
		return nil
	}
	return backend.Opaque
}

// SetOpaque sets the opaque data
func (backend *Backend) SetOpaque(opaque any) {
	if backend != nil {
		backend.Opaque = opaque
	}
}

// IsDynamic returns whether the backend is dynamic
func (backend *Backend) IsDynamic() bool {
	if backend == nil {
		return false
	}
	return backend.Flags&BackendDynamic != 0
}

// BackendRetain retains a backend (C: backend_retain, backend.c:672-677).
func BackendRetain(be *Backend) *Backend {
	if be != nil {
		atomic.AddInt32(&be.Refcount, 1)
	}
	return be
}

// BackendRelease releases a backend (C: backend_release, backend.c:679-688).
// Only dynamic backends are destroyed; the refcount is not decremented
// for non-dynamic backends.
func BackendRelease(be *Backend) {
	if be == nil {
		return
	}
	if be.Flags&BackendDynamic == 0 {
		return
	}
	if atomic.AddInt32(&be.Refcount, -1) == 0 {
		if be.Destroy != nil {
			be.Destroy(be)
		}
	}
}

// registerPlaylistBackend — C: BE_REGISTER(playlist) (backend/playlist.c:122-129).
// be_playlist.canhandle matches the "playlist:" prefix; be_open loads the
// playlist file and populates model.nodes with playable items.
func (bs *BackendSystem) registerPlaylistBackend() {
	backend := &Backend{
		Prefix: "playlist:",
	}
	backend.CanHandle = func(url string) int {
		if strings.HasPrefix(url, "playlist:") {
			return 1
		}
		return 0
	}
	backend.Open = func(page any, url string, sync bool) error {
		propRoot, ok := page.(*propcore.Prop)
		if !ok {
			return fmt.Errorf("playlist: page is not a propcore.Prop")
		}
		bs.playlistOpen(propRoot, url, sync)
		return nil
	}
	bs.Register(backend)
}

// playlistOpen — C: playlist_open (backend/playlist.c:43-112).
// Loads a playlist (M3U/PLS-style), creates model{type=directory,
// metadata.title, nodes[]} and one node per backend-handled entry.
func (bs *BackendSystem) playlistOpen(page *propcore.Prop, url0 string, sync bool) {
	pm := bs.propManager

	// C: url += strlen("playlist:");
	url := url0[len("playlist:"):]

	// C: usage_page_open(sync, "Playlist");
	bs.usage.PageOpen(sync, "Playlist")

	// C: prop_t *model = prop_create_r(page, "model"); — prop_create_r
	// adds an extra ref over prop_create (CreateEx returns ref=1).
	model := pm.RefInc(pm.CreateEx(page, "model", nil, false, false))

	// C: prop_set(model, "type", PROP_SET_STRING, "directory");
	typeProp := pm.CreateEx(model, "type", nil, false, false)
	pm.SetStringEx(typeProp, nil, "directory", propcore.StringUTF8)

	// C: prop_set(model, "loading", PROP_SET_INT, 1);
	loadingProp := pm.CreateEx(model, "loading", nil, false, false)
	pm.SetIntEx(loadingProp, nil, 1)

	// C: prop_t *meta = prop_create_r(model, "metadata");
	//     prop_set(meta, "title", PROP_ADOPT_RSTRING, fa_get_title(url));
	meta := pm.RefInc(pm.CreateEx(model, "metadata", nil, false, false))
	metaTitle := pm.CreateEx(meta, "title", nil, false, false)
	pm.SetStringEx(metaTitle, nil, bs.fileAccessManager.FAGetTitle(url), propcore.StringUTF8)

	// C: buf_t *buf = fa_load(url, FA_LOAD_ERRBUF(errbuf, sizeof(errbuf)), NULL);
	buf, _ := fileaccesscore.FALoad(bs.fileAccessManager, url, nil, nil, 0)

	// C: prop_t *nodes = prop_create_r(model, "nodes");
	nodes := pm.RefInc(pm.CreateEx(model, "nodes", nil, false, false))

	if buf != nil {
		// C: buf = buf_make_writable(buf); — Go Buffer.Data is already mutable.

		// C: double duration = -1; const char *title = NULL;
		duration := -1.0
		var title []byte

		// C: LINEPARSE(s, buf_str(buf)) { ... }
		lp := buf.Data
		for {
			s := misc.LpGet(&lp)
			if s == nil {
				break
			}
			// C string semantics: the line is bounded at the first NUL.
			line := s
			if before, _, ok := bytes.Cut(s, []byte{0}); ok {
				line = before
			}

			if bytes.HasPrefix(line, []byte("#EXTINF:")) {
				// C: v = mystrbegins(s, "#EXTINF:")
				v := line[len("#EXTINF:"):]

				// C: duration = my_str2double(v, NULL);
				duration, _ = misc.MyStr2double(string(v))

				// C: title = strchr(v, ','); if(title != NULL) title++;
				title = nil
				if _, after, ok := bytes.Cut(v, []byte{','}); ok {
					title = after
				}
			} else if len(line) > 0 && line[0] != '#' {
				// C: char *itemurl = url_resolve_relative_from_base(url, s);
				itemurl := misc.UrlResolveRelativeFromBase(url, string(line))

				// C: if(backend_canhandle(itemurl)) {
				if bs.CanHandle(itemurl) != nil {
					// C: prop_t *item = prop_create_root(NULL);
					item := pm.CreateRoot("")

					// C: prop_set(item, "url", PROP_SET_STRING, itemurl);
					itemURL := pm.CreateEx(item, "url", nil, false, false)
					pm.SetStringEx(itemURL, nil, itemurl, propcore.StringUTF8)

					// C: prop_t *metadata = prop_create(item, "metadata");
					metadata := pm.CreateEx(item, "metadata", nil, false, false)

					// C: prop_set(item, "type", PROP_SET_STRING, "file");
					itemType := pm.CreateEx(item, "type", nil, false, false)
					pm.SetStringEx(itemType, nil, "file", propcore.StringUTF8)

					itemTitle := pm.CreateEx(metadata, "title", nil, false, false)
					if title != nil {
						// C: prop_set(metadata, "title", PROP_SET_STRING, title);
						pm.SetStringEx(itemTitle, nil, string(title), propcore.StringUTF8)
					} else {
						// C: prop_set(metadata, "title", PROP_ADOPT_RSTRING,
						//     fa_get_title(itemurl));
						pm.SetStringEx(itemTitle, nil, bs.fileAccessManager.FAGetTitle(itemurl), propcore.StringUTF8)
					}

					// C: if(duration > 0) prop_set(metadata, "duration",
					//     PROP_SET_FLOAT, duration);
					if duration > 0 {
						durationProp := pm.CreateEx(metadata, "duration", nil, false, false)
						pm.SetFloatEx(durationProp, nil, float32(duration))
					}

					// C: if(prop_set_parent(item, nodes)) prop_destroy(item);
					item.SetParent(nodes)
				}
				// C: title = NULL; (duration is NOT reset — canonical quirk)
				title = nil
			}
		}
	} else {
		// C: nav_open_errorf(page, _("Unable to open playlist"));
		openErrorf(pm, page, "Unable to open playlist")
	}

	// C: prop_set(model, "loading", PROP_SET_INT, 0);
	pm.SetIntEx(loadingProp, nil, 0)

	// C: prop_ref_dec(nodes); prop_ref_dec(model); prop_ref_dec(meta);
	pm.RefDec(nodes)
	pm.RefDec(model)
	pm.RefDec(meta)
}
