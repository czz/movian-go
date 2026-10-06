package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/fileaccess/scanner"
	imagepkg "github.com/czz/movian-go/internal/image"
	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/metadata"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/trace"
)

// registerFileaccessBackend registers the fileaccess backend
func (bs *BackendSystem) registerFileaccessBackend() {
	// Create fileaccess backend with video open callback and scanner page callback
	fb := fileaccesscore.NewFileBackend(func(page *propcore.Prop, url string, sync bool) error {
		return bs.openVideoDirect(page, url, sync)
	}, func(url string, urlMtime time.Time, model *propcore.Prop,
		playme string, directClose *propcore.Prop, title string,
		pm *propcore.PropManager, ts *trace.TraceSystem) {
		scanner.FAScannerPage(url, urlMtime, model, playme, directClose,
			title, pm, ts, bs.fileAccessManager, bs.mediaSys, bs.indexer, bs.playQueue, bs.kvstore, bs.metadata, bs.Gconf())
	}, bs.traceSystem)

	// Store reference for later callback setup (e.g., playqueue callbacks)
	bs.fileBackend = fb

	be := &Backend{
		Prefix: "file:",
		// C: be_file (fa_backend.c:309-320) sets no be_flags — only
		// be_prop and be_ecmascript carry BACKEND_OPEN_CHECKS_URI.
	}
	// C: &be_file — needed by file_open_file's "newbe != &be_file" check.
	bs.fileBE = be
	be.CanHandle = func(url string) int {
		// C: be_file_canhandle → fa_can_handle(url, NULL, 0)
		// (fa_backend.c:46-48) — fa_resolve_proto-based.
		if bs.fileAccessManager.FACanHandle(url) {
			return 1
		}
		return 0
	}
	be.Open = func(page any, url string, sync bool) error {
		// C: be_file_open (fa_backend.c:264-290) — always returns 0;
		// fa_stat failure shows nav_open_error inside.
		beFileaccessOpen(bs.propManager, page, url, sync, fb, bs)
		return nil
	}
	// C: .be_normalize = fa_normalize (fileaccess.c:208-221)
	// C semantics: resolve proto, fap_normalize or -1, write dst, return r.
	be.Normalize = func(url string, dst []byte) int {
		proto, filename, err := bs.fileAccessManager.FAResolveProto(url)
		if err != nil {
			return -1
		}
		if proto.Normalize == nil {
			return -1
		}
		result, err := proto.Normalize(proto, filename)
		if err != nil {
			return -1
		}
		copy(dst, result) // zero-padded dst provides C's NUL
		return 0
	}
	// C: .be_imageloader = fa_imageloader (fa_imageloader.c:185)
	be.Imageloader = func(url string, imageMeta any,
		cacheControl *int, cancellable any, backend *Backend) (any, error) {
		// C passes image_meta_t* directly; the Go port splits it into
		// backend ImageMeta (callers) and imagepkg.ImageMeta (image pkg).
		var im *imagepkg.ImageMeta
		switch m := imageMeta.(type) {
		case *ImageMeta:
			im = &imagepkg.ImageMeta{
				ReqAspect:            m.ReqAspect,
				ReqWidth:             m.ReqWidth,
				ReqHeight:            m.ReqHeight,
				MaxWidth:             m.MaxWidth,
				MaxHeight:            m.MaxHeight,
				CanMono:              m.CanMono,
				NoDecoding:           m.NoDecoding,
				Bit32Swizzle:         m.Bit32Swizzle,
				WantThumb:            m.WantThumb,
				IntensityAnalysis:    m.IntensityAnalysis,
				PrimaryColorAnalysis: m.PrimaryColorAnalysis,
				ForceLocalLoad:       m.ForceLocal,
				CornerSelection:      m.CornerSelection,
				CornerRadius:         m.CornerRadius,
				Shadow:               m.Shadow,
				Margin:               m.Margin,
				Opaque:               m.Opaque,
				Incremental:          m.Incremental,
			}
		case *imagepkg.ImageMeta:
			im = m
		}
		return bs.il.FAImageloader(bs.fileAccessManager, url, im,
			cacheControl, cancellable, backend)
	}
	// C: .be_playaudio = be_file_playaudio (fa_audio.c)
	be.PlayAudio = func(url string, mediaPipe any,
		paused bool, mimetype string, opaque any) (any, error) {
		mp, _ := mediaPipe.(*mediacore.MediaPipe)
		return bs.beFilePlayaudio(url, mp, paused, mimetype, opaque)
	}
	// C: .be_playvideo = be_file_playvideo (fa_video.c)
	be.PlayVideo = func(url string, mediaPipe any,
		vq any, vsl any, va *VideoArgs) (any, error) {
		mp, _ := mediaPipe.(*mediacore.MediaPipe)
		return bs.BeFilePlayvideo(url, mp, vq, vsl, va)
	}
	// P2-NEW-1: C's be_file (fileaccess.c) does NOT have .be_search.
	// Search is handled by separate backends: locatedb, spotlight, plugins.
	// The .Search handler was incorrectly wired to the file:// backend.
	// It is now handled by the locatedb backend (registerLocatedbBackend).
	bs.Register(be)
}

// SetPlayQueueCallbacks sets the playqueue play and open callbacks on the
// fileaccess backend. This must be called after the PlayQueue is created
// (in init.go) to enable audio file → playqueue integration.
//
// C: playqueue_play and playqueue_open are global functions called from
// file_open_file when CONTENT_AUDIO is detected.
// Go: We use callbacks to avoid import cycles
// (backend/core → fileaccess/core → playqueue → backend/core).
func (bs *BackendSystem) SetPlayQueueCallbacks(playCB fileaccesscore.PlayQueuePlayCallback, openCB fileaccesscore.PlayQueueOpenCallback) {
	if bs.fileBackend != nil {
		bs.fileBackend.SetPlayQueueCallbacks(playCB, openCB)
	}
}

// beFileaccessOpen opens a fileaccess URL.
//
// Reproduces C's be_file_open (src/fileaccess/fa_backend.c:264):
//  1. Create model, model.loading, model.loadingStatus, model.io
//  2. Set model.loading = 1
//  3. Stat the URL
//  4. If ContentDir  → fileOpenDir  → fileOpenBrowse (scanner)
//     If ContentShare → fileOpenBrowse (scanner)
//     If regular file → fileOpenFile (content-type dispatch)
func beFileaccessOpen(pm *propcore.PropManager, page any, url string, sync bool, fb *fileaccesscore.FileBackend, bs *BackendSystem) error {
	propRoot, ok := page.(*propcore.Prop)
	if !ok {
		return fmt.Errorf("page is not a propcore.Prop")
	}

	fam := bs.GetFileAccessManager()

	// Create model property (C: prop_create_r(page, "model"))
	modelProp := pm.CreateEx(propRoot, "model", nil, false, false)

	// Create loading property and set to 1 (C: prop_set_int(loading, 1))
	loadingProp := pm.CreateEx(modelProp, "loading", nil, false, false)
	pm.SetIntEx(loadingProp, nil, 1)

	// C: loading_status = prop_create_r(model, "loadingStatus")
	loadingStatusProp := pm.CreateEx(modelProp, "loadingStatus", nil, false, false)

	// C: io = prop_create_r(model, "io")
	ioProp := pm.CreateEx(modelProp, "io", nil, false, false)

	// Stat the URL (C: fa_stat(url, &fs, ...))
	stat, err := fileaccesscore.Stat(fam, url)
	if err != nil {
		// C: nav_open_error(page, errbuf)
		openErrorf(pm, propRoot, "Unable to stat file: %v", err)
		return nil
	}

	// C: fa_backend.c:277-285 — three-way dispatch with usage_page_open
	// per branch; the else covers FILE plus every other content type.
	switch stat.Type {
	case fileaccesscore.ContentDir:
		bs.usage.PageOpen(sync, "Directory")
		// C: file_open_dir(page, url, fs.fs_mtime, model)
		fileOpenDir(pm, fb, propRoot, url, stat.MTime, modelProp)
	case fileaccesscore.ContentShare:
		bs.usage.PageOpen(sync, "Share")
		// C: file_open_browse(page, url, fs.fs_mtime, model)
		fileOpenBrowse(pm, fb, propRoot, url, stat.MTime, modelProp)
	default:
		bs.usage.PageOpen(sync, "File")
		// C: file_open_file(page, url, &fs, model, loading, io, loading_status)
		fileOpenFile(pm, fb, bs, propRoot, url, stat, modelProp,
			loadingProp, ioProp, loadingStatusProp)
	}

	return nil
}

// fileOpenDir opens a directory URL.
//
// Reproduces C's file_open_dir (fa_backend.c:95):
//
//	Probes the directory content type, then dispatches:
//	- CONTENT_DVD               → openVideo (via videoOpenCB)
//	- CONTENT_DIR/SHARE/ARCHIVE → fileOpenBrowse
//
// C's fa_probe_dir (fa_probe.c:634-655) checks for VIDEO_TS or video_ts
// subdirectory. If found, returns CONTENT_DVD → backend_open_video.
// Go must do the same — the existing Go isDVDDirectory in dvd_backend.go
// is MORE restrictive (requires VIDEO_TS.IFO) and is not called from
// this path.
func fileOpenDir(pm *propcore.PropManager, fb *fileaccesscore.FileBackend, page *propcore.Prop, url string, mtime time.Time, model *propcore.Prop) {
	// C: metadata_t *md = fa_probe_dir(url)
	// C: fa_probe_dir checks for VIDEO_TS and video_ts subdirectories
	if probeDirIsDVD(url) {
		// C: case CONTENT_DVD: backend_open_video(page, url, 0)
		if fb.VideoOpenCB() != nil {
			fb.VideoOpenCB()(page, url, false)
		}
		return
	}

	// C: case CONTENT_DIR/SHARE/ARCHIVE: file_open_browse(page, url, mtime, model)
	fileOpenBrowse(pm, fb, page, url, mtime, model)
}

// probeDirIsDVD checks if a directory URL contains a VIDEO_TS or video_ts
// subdirectory, matching C's fa_probe_dir (fa_probe.c:634-655).
//
// C checks:
//  1. url + "VIDEO_TS" → fa_stat → if CONTENT_DIR → DVD
//  2. url + "video_ts" → fa_stat → if CONTENT_DIR → DVD
//
// C does NOT check for VIDEO_TS.IFO (that's Go's isDVDDirectory which
// is more restrictive and used only by the dvd: backend).
//
// We use os.Stat directly since we're dealing with file:// URLs.
// For non-file:// URLs, we fall back to the FileAccessManager if available.
func probeDirIsDVD(url string) bool {
	path := strings.TrimPrefix(url, "file://")

	// C: fa_pathjoin(path, sizeof(path), url, "VIDEO_TS")
	// C: fa_stat(path, &fs, NULL, 0) == 0 && fs.fs_type == CONTENT_DIR
	videoTSPath := filepath.Join(path, "VIDEO_TS")
	if info, err := os.Stat(videoTSPath); err == nil && info.IsDir() {
		return true
	}

	// C: fa_pathjoin(path, sizeof(path), url, "video_ts")
	// C: fa_stat(path, &fs, NULL, 0) == 0 && fs.fs_type == CONTENT_DIR
	videoTSLowerPath := filepath.Join(path, "video_ts")
	if info, err := os.Stat(videoTSLowerPath); err == nil && info.IsDir() {
		return true
	}

	return false
}

// fileOpenBrowse opens a directory for browsing with the scanner.
//
// Reproduces C's file_open_browse (fa_backend.c:67):
//  1. model.type = "directory" (STRING CHILD)
//  2. model.metadata.title = title from URL
//  3. page.parent = parent URL
//  4. Call fa_scanner_page(url, mtime, model, ...)
func fileOpenBrowse(pm *propcore.PropManager, fb *fileaccesscore.FileBackend, page *propcore.Prop, url string, mtime time.Time, model *propcore.Prop) {
	// C: prop_set(model, "type", PROP_SET_STRING, "directory")
	typeProp := pm.CreateEx(model, "type", nil, false, false)
	pm.SetStringEx(typeProp, nil, "directory", propcore.StringUTF8)

	// C: prop_setv(model, "metadata", "title", NULL, PROP_SET_RSTRING, title)
	title := titleFromURL(url)
	metadataProp := pm.CreateEx(model, "metadata", nil, false, false)
	titleProp := pm.CreateEx(metadataProp, "title", nil, false, false)
	pm.SetStringEx(titleProp, nil, title, propcore.StringUTF8)

	// C: if(!fa_parent(parent, sizeof(parent), url)) prop_set(page, "parent", PROP_SET_STRING, parent)
	if parent, err := fileaccesscore.Parent(url); err == nil && parent != "" && parent != url {
		parentProp := pm.CreateEx(page, "parent", nil, false, false)
		pm.SetStringEx(parentProp, nil, parent, propcore.StringUTF8)
	}

	// C: prop_t *dc = prop_create_r(page, "directClose")
	directClose := pm.CreateEx(page, "directClose", nil, false, false)

	// C: fa_scanner_page(url, mtime, model, NULL, dc, title)
	// The scanner creates model.source, model.nodes, model.loading, model.filter, etc.
	if fb != nil && fb.ScannerPageCB() != nil {
		fb.ScannerPageCB()(url, mtime, model, "", directClose, title, pm, fb.TraceSystem())
	}
}

// fileOpenFile opens a regular file, dispatching by content type.
//
// Reproduces C's file_open_file (fa_backend.c:175):
//  1. Probe metadata for the file
//  2. Dispatch by content type:
//     - ARCHIVE/ALBUM/PLAYLIST → fileOpenBrowse
//     - AUDIO                  → fileOpenAudio (open parent dir, play track)
//     - VIDEO/DVD              → openVideo
//     - IMAGE                  → fileOpenImage
//     - default                → openError
func fileOpenFile(pm *propcore.PropManager, fb *fileaccesscore.FileBackend, bs *BackendSystem, page *propcore.Prop, url string, stat *fileaccesscore.FileStat, model *propcore.Prop, loading *propcore.Prop, io *propcore.Prop, loadingStatus *propcore.Prop) {
	var md *metadata.Metadata

	// C: db = metadb_get(); md = metadb_metadata_get(db, url, fs->fs_mtime)
	if db := bs.metadata.Get(); db != nil {
		md = bs.metadata.MetadbMetadataGet(db, url, stat.MTime.Unix())
		bs.metadata.Close(db)
	}

	if md == nil {
		// C: prop_link(_p("Checking file contents"), loading_status)
		var linkProp *propcore.Prop
		if loadingStatus != nil {
			linkProp = pm.CreateRootEx("", false)
			pm.SetStringEx(linkProp, nil, "Checking file contents", propcore.StringUTF8)
			pm.Link(linkProp, loadingStatus, nil, false, false)
		}
		// C: md = fa_probe_metadata(url, errbuf, sizeof(errbuf), NULL, io)
		var perr error
		md, perr = scanner.FAProbeMetadata(bs.fileAccessManager, url, "", io)
		if linkProp != nil {
			pm.Unlink(linkProp)
		}
		if md == nil {
			errStr := ""
			if perr != nil {
				errStr = perr.Error()
			}
			openErrorf(pm, page, "Unable to open file: %s", errStr)
			return
		}
	}

	// C: if(md->md_redirect != NULL) { url = md->md_redirect; ... }
	if md.Redirect != "" {
		url = md.Redirect
		// C: backend_t *newbe = backend_resolve(url)
		newbe := bs.Resolve(url)
		if newbe == nil {
			openErrorf(pm, page, "Invalid URL from redirect")
			return
		}
		if newbe != bs.fileBE {
			// C: nav_redirect(page, url) — EVENT_REDIRECT via eventSink.
			if bs.navRedirect != nil {
				bs.navRedirect(pm, page, url)
			}
			bs.Release(newbe)
			return
		}
		bs.Release(newbe)
	}

	// C: meta = prop_create_root("metadata"); metadata_to_proptree(md, meta, 0)
	meta := pm.CreateRootEx("metadata", false)
	bs.metadata.MetadataToProptree(md, meta, false)

	switch md.ContentType {
	case metadata.ContentArchive, metadata.ContentAlbum,
		metadata.ContentPlaylist:
		// C: file_open_browse(page, url, fs.fs_mtime, model)
		fileOpenBrowse(pm, fb, page, url, stat.MTime, model)

	case metadata.ContentAudio:
		// C: if(!file_open_audio(page, url, model)) break;
		// file_open_audio returns 0 on success — it starts
		// fa_scanner_page(parent, ..., playme=url) which plays the file
		// through the scanner's tryplay() once it appears in the listing.
		// playqueue_play/playqueue_open below are the FALLBACK for when
		// the parent dir can't be scanned (C returns 1 there).
		if fileOpenAudio(pm, fb, bs.fileAccessManager, page, url, model) {
			break
		}
		// C: prop_set_int(loading, 0)
		pm.SetIntEx(loading, nil, 0)
		// C: playqueue_play(url, meta, 0); playqueue_open(page)
		if fb.PlayQueuePlayCB() != nil {
			fb.PlayQueuePlayCB()(url, page, meta)
		}
		if fb.PlayQueueOpenCB() != nil {
			fb.PlayQueueOpenCB()(page)
		}
		meta = nil

	case metadata.ContentVideo, metadata.ContentDVD:
		// C: backend_open_video(page, url, 0)
		fb.VideoOpenCB()(page, url, false)

	case metadata.ContentImage:
		// C: file_open_image(model, meta); prop_set_int(loading, 0)
		fileOpenImage(pm, model, meta)
		pm.SetIntEx(loading, nil, 0)
		meta = nil

	case metadata.ContentPlugin:
		// C: #if ENABLE_PLUGINS → plugin_open_file(page, url)
		pm.SetIntEx(loading, nil, 0)
		if bs.pluginOpenFile != nil {
			bs.pluginOpenFile(page, url)
		}

	default:
		// C: nav_open_errorf(page, _("Can't handle content type %d"), ...)
		openErrorf(pm, page, "Can't handle content type %d", md.ContentType)
	}
	// C: prop_destroy(meta); metadata_destroy(md)
	if meta != nil {
		pm.Destroy(meta)
	}
	md.Destroy()
}

// fileOpenImage opens an image file.
//
// Reproduces C's file_open_image (fa_backend.c:124-132):
//  1. model.type = "image"
//  2. prop_set_parent(meta, model) — meta lands as model.metadata
func fileOpenImage(pm *propcore.PropManager, model *propcore.Prop, meta *propcore.Prop) {
	// C: prop_set(model, "type", PROP_SET_STRING, "image")
	typeProp := pm.CreateEx(model, "type", nil, false, false)
	pm.SetStringEx(typeProp, nil, "image", propcore.StringUTF8)

	// C: if(prop_set_parent(meta, model)) prop_destroy(meta)
	if pm.SetParentEx(meta, model, nil, "metadata") != 0 {
		pm.Destroy(meta)
	}
}

// fileOpenAudio opens an audio file by opening its parent directory.
//
// Reproduces C's file_open_audio (fa_backend.c:140):
//  1. Get parent directory URL
//  2. model.type = "directory" (STRING CHILD)
//  3. model.metadata.title = parent dir title
//  4. page.parent = grandparent
//  5. Call scanner on parent directory with playme=url
//
// fileOpenAudio opens an audio file by opening the parent directory.
//
// Reproduces C's file_open_audio (fa_backend.c:140):
//  1. Get parent URL — if fail, return 1 (error)
//  2. Stat parent — if fail, return 1 (error)
//  3. Set model.type = "directory"
//  4. Set model.metadata.title from parent URL
//
// 5. Set page.parent to grandparent
// 6. Call scanner on parent with playme=url
//
// Returns true on success, false on failure.
// C: returns 0 on success, 1 on failure (inverted from Go bool convention)
func fileOpenAudio(pm *propcore.PropManager, fb *fileaccesscore.FileBackend, fam *fileaccesscore.FileAccessManager, page *propcore.Prop, url string, model *propcore.Prop) bool {
	// C: if(fa_parent(parent, sizeof(parent), url)) return 1
	// Failure leaves the model untouched — the caller clears loading
	// and falls back to playqueue_play/playqueue_open.
	parentURL, err := fileaccesscore.Parent(url)
	if err != nil || parentURL == "" || parentURL == url {
		return false
	}

	// C: if(fa_stat(parent, &fs, ...)) return 1
	parentStat, err := fileaccesscore.Stat(fam, parentURL)
	if err != nil {
		return false
	}

	// C: prop_set(model, "type", PROP_SET_STRING, "directory")
	typeProp := pm.CreateEx(model, "type", nil, false, false)
	pm.SetStringEx(typeProp, nil, "directory", propcore.StringUTF8)

	// C: prop_setv(model, "metadata", "title", NULL, PROP_SET_RSTRING, title)
	title := titleFromURL(parentURL)
	metadataProp := pm.CreateEx(model, "metadata", nil, false, false)
	titleProp := pm.CreateEx(metadataProp, "title", nil, false, false)
	pm.SetStringEx(titleProp, nil, title, propcore.StringUTF8)

	// C: if(!fa_parent(parent2, sizeof(parent2), parent)) prop_set(page, "parent", ...)
	if grandparent, err := fileaccesscore.Parent(parentURL); err == nil && grandparent != "" && grandparent != parentURL {
		parentProp := pm.CreateEx(page, "parent", nil, false, false)
		pm.SetStringEx(parentProp, nil, grandparent, propcore.StringUTF8)
	}

	// C: fa_scanner_page(parent, fs.fs_mtime, model, url, dc, title)
	// The scanner is called on the parent directory with playme=url
	directClose := pm.CreateEx(page, "directClose", nil, false, false)
	if fb != nil && fb.ScannerPageCB() != nil {
		fb.ScannerPageCB()(parentURL, parentStat.MTime, model, url, directClose, title, pm, fb.TraceSystem())
	}
	return true
}

// openVideoDirect sets up the property tree for a video page.
//
// Reproduces C's backend_open_video (backend.c:523):
//  1. page.directClose = 1
//  2. page.source = url
//  3. model.type = "video" (STRING CHILD)
//  4. model.loading = 0
func (bs *BackendSystem) openVideoDirect(page *propcore.Prop, url string, sync bool) error {
	if page == nil {
		return nil
	}

	// C: prop_set(page, "directClose", PROP_SET_INT, 1)
	dcProp := bs.propManager.CreateEx(page, "directClose", nil, false, false)
	bs.propManager.SetIntEx(dcProp, nil, 1)

	// C: prop_set(page, "source", PROP_SET_STRING, url)
	sourceProp := bs.propManager.CreateEx(page, "source", nil, false, false)
	bs.propManager.SetStringEx(sourceProp, nil, url, propcore.StringUTF8)

	// C: prop_t *m = prop_create_r(page, "model")
	modelProp := bs.propManager.CreateEx(page, "model", nil, false, false)

	// C: prop_set(m, "type", PROP_SET_STRING, "video")
	typeProp := bs.propManager.CreateEx(modelProp, "type", nil, false, false)
	bs.propManager.SetStringEx(typeProp, nil, "video", propcore.StringUTF8)

	// C: prop_set(m, "loading", PROP_SET_INT, 0)
	loadingProp := bs.propManager.CreateEx(modelProp, "loading", nil, false, false)
	bs.propManager.SetIntEx(loadingProp, nil, 0)

	return nil
}

// OpenVideo opens a video URL. This is the public API matching C's backend_open_video.
// It sets up the property tree for video playback (model.type="video", model.loading=0).
func (bs *BackendSystem) OpenVideo(page any, url string, sync bool) error {
	propRoot, ok := page.(*propcore.Prop)
	if !ok {
		return fmt.Errorf("page is not a propcore.Prop")
	}
	return bs.openVideoDirect(propRoot, url, sync)
}

// openErrorf creates an error page.
// Uses navigator.OpenErrorf semantics: model.type="openerror", model.error=msg
func openErrorf(pm *propcore.PropManager, page *propcore.Prop, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	modelProp := pm.CreateEx(page, "model", nil, false, false)
	typeProp := pm.CreateEx(modelProp, "type", nil, false, false)
	pm.SetStringEx(typeProp, nil, "openerror", propcore.StringUTF8)
	loadingProp := pm.CreateEx(modelProp, "loading", nil, false, false)
	pm.SetIntEx(loadingProp, nil, 0)
	errorProp := pm.CreateEx(modelProp, "error", nil, false, false)
	pm.SetStringEx(errorProp, nil, msg, propcore.StringUTF8)
	dcProp := pm.CreateEx(page, "directClose", nil, false, false)
	pm.SetIntEx(dcProp, nil, 1)
}

// titleFromURL extracts the last component of a URL as a human-readable title.
// Matches C's title_from_url / fa_get_title.
func titleFromURL(url string) string {
	if url == "" {
		return ""
	}
	url = strings.TrimSuffix(url, "/")
	return filepath.Base(url)
}
