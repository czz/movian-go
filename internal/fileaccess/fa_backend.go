package fileaccess

import (
	"path/filepath"
	"strings"
	"time"

	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/trace"
)

// FAScannerPageCallback is a callback function for scanner page operations
// Used to avoid circular dependency with scanner package
type FAScannerPageCallback func(url string, mtime time.Time, model *propcore.Prop, pageType string, directClose *propcore.Prop, title string, pm *propcore.PropManager, ts *trace.TraceSystem)

// VideoOpenCallback is a callback function to open video URLs
// Used to avoid circular dependency with backend package
type VideoOpenCallback func(page *propcore.Prop, url string, sync bool) error

// PlayQueuePlayCallback is called when an audio file is opened to add it
// to the playqueue and start playback.
//
// C: playqueue_play(url, meta, 0) — clears queue, adds entry, starts playing
// Go: callback receives URL, page prop, and metadata prop
type PlayQueuePlayCallback func(url string, page *propcore.Prop, meta *propcore.Prop)

// PlayQueueOpenCallback opens the playqueue UI on a page.
//
// C: playqueue_open(page) — links playqueue_nodes to page model.nodes
// Go: callback receives page prop
type PlayQueueOpenCallback func(page *propcore.Prop)

// FileBackend represents the file backend (minimal implementation)
type FileBackend struct {
	canHandle       func(url string) bool
	videoOpenCB     VideoOpenCallback
	scannerPageCB   FAScannerPageCallback
	playQueuePlayCB PlayQueuePlayCallback
	playQueueOpenCB PlayQueueOpenCallback
	ts              *trace.TraceSystem
}

// NewFileBackend creates a new FileBackend instance
func NewFileBackend(videoOpenCB VideoOpenCallback, scannerPageCB FAScannerPageCallback, ts *trace.TraceSystem) *FileBackend {
	return &FileBackend{
		canHandle:     backendCanHandle,
		videoOpenCB:   videoOpenCB,
		scannerPageCB: scannerPageCB,
		ts:            ts,
	}
}

// SetVideoOpenCallback sets the video open callback
func (fb *FileBackend) SetVideoOpenCallback(cb VideoOpenCallback) {
	if fb != nil {
		fb.videoOpenCB = cb
	}
}

// VideoOpenCB returns the video open callback
func (fb *FileBackend) VideoOpenCB() VideoOpenCallback {
	if fb == nil {
		return nil
	}
	return fb.videoOpenCB
}

// ScannerPageCB returns the scanner page callback
func (fb *FileBackend) ScannerPageCB() FAScannerPageCallback {
	if fb == nil {
		return nil
	}
	return fb.scannerPageCB
}

// SetPlayQueueCallbacks sets the playqueue play and open callbacks
func (fb *FileBackend) SetPlayQueueCallbacks(playCB PlayQueuePlayCallback, openCB PlayQueueOpenCallback) {
	if fb != nil {
		fb.playQueuePlayCB = playCB
		fb.playQueueOpenCB = openCB
	}
}

// PlayQueuePlayCB returns the playqueue play callback
func (fb *FileBackend) PlayQueuePlayCB() PlayQueuePlayCallback {
	if fb == nil {
		return nil
	}
	return fb.playQueuePlayCB
}

// PlayQueueOpenCB returns the playqueue open callback
func (fb *FileBackend) PlayQueueOpenCB() PlayQueueOpenCallback {
	if fb == nil {
		return nil
	}
	return fb.playQueueOpenCB
}

// TraceSystem returns the trace system
func (fb *FileBackend) TraceSystem() *trace.TraceSystem {
	if fb == nil {
		return nil
	}
	return fb.ts
}

// backendCanHandle checks if URL can be handled by file backend
func backendCanHandle(url string) bool {
	// For now, return true for local file URLs
	return strings.HasPrefix(url, "file://") || !strings.Contains(url, "://")
}

// FileOpenBrowse opens a file for browsing.
//
// Reproduces C's file_open_browse (fa_backend.c:67):
//  1. model.type = "directory" (STRING CHILD, not leaf value)
//  2. model.metadata.title = title from URL
//  3. page.parent = parent URL
//  4. Call scanner page callback (fa_scanner_page)
func FileOpenBrowse(fb *FileBackend, pm *propcore.PropManager, page *propcore.Prop, url string, mtime time.Time, model *propcore.Prop) error {
	// C: prop_set(model, "type", PROP_SET_STRING, "directory")
	// Create model.type as a CHILD property (not setting model's leaf value)
	typeProp := pm.CreateEx(model, "type", nil, false, false)
	pm.SetStringEx(typeProp, nil, "directory", propcore.StringUTF8)

	// C: prop_setv(model, "metadata", "title", NULL, PROP_SET_RSTRING, title)
	title := titleFromURL(url)
	metadataProp := pm.CreateEx(model, "metadata", nil, false, false)
	titleProp := pm.CreateEx(metadataProp, "title", nil, false, false)
	pm.SetStringEx(titleProp, nil, title, propcore.StringUTF8)

	// C: if(!fa_parent(parent, sizeof(parent), url)) prop_set(page, "parent", PROP_SET_STRING, parent)
	parent := getParent(url)
	if parent != "" && parent != url {
		parentProp := pm.CreateEx(page, "parent", nil, false, false)
		pm.SetStringEx(parentProp, nil, parent, propcore.StringUTF8)
	}

	// C: prop_t *dc = prop_create_r(page, "directClose")
	directClose := pm.CreateEx(page, "directClose", nil, false, false)

	// C: fa_scanner_page(url, mtime, model, NULL, dc, title)
	if fb != nil && fb.ScannerPageCB() != nil {
		fb.ScannerPageCB()(url, mtime, model, "", directClose, title, pm, fb.TraceSystem())
	}

	return nil
}

// titleFromURL extracts the last component of URL as title
func titleFromURL(url string) string {
	// Remove trailing slash
	url = strings.TrimSuffix(url, "/")
	// Get last component
	return filepath.Base(url)
}

// getParent extracts parent directory from URL
func getParent(url string) string {
	// Remove trailing slash
	url = strings.TrimSuffix(url, "/")
	// Get parent directory
	parent := filepath.Dir(url)
	if parent == url || parent == "." {
		return ""
	}
	return parent
}

// FileOpenDir opens a directory
func FileOpenDir(pm *propcore.PropManager, fb *FileBackend, page *propcore.Prop, url string, mtime time.Time, model *propcore.Prop) error {
	return FileOpenBrowse(fb, pm, page, url, mtime, model)
}

// FileGetTitle gets title from URL
func FileGetTitle(url string) string {
	// Extract title from URL (last component)
	return titleFromURL(url)
}
