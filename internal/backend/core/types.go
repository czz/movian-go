package core

// Split from backend.go — Fase 4 pure-move refactor.

import (
	"errors"
	imagepkg "github.com/czz/movian-go/internal/image"
	propcore "github.com/czz/movian-go/internal/prop"
)

type ProbeResult int

const (
	ProbeOK ProbeResult = iota
	ProbeAuth
	ProbeNoHandler
	ProbeFail
)

// Video flags
const (
	VideoPrimary        = 0x1
	VideoNoAudio        = 0x2
	VideoNoFSScan       = 0x4
	VideoSetTitle       = 0x8
	VideoNoSubtitleScan = 0x10
)

// Backend flags
const (
	BackendOpenChecksURI = 0x1
	BackendDynamic       = 0x2
)

// ImageMeta represents image loading metadata.
// C: image_meta_t (image/image.h:34-57)
type ImageMeta struct {
	ReqAspect            float64
	ReqWidth             int
	ReqHeight            int
	MaxWidth             int
	MaxHeight            int
	CanMono              bool
	NoDecoding           bool
	Bit32Swizzle         bool
	WantThumb            bool
	IntensityAnalysis    bool
	PrimaryColorAnalysis bool
	ForceLocal           bool
	CornerSelection      uint8
	CornerRadius         uint16
	Shadow               uint16
	Margin               uint16
	Opaque               any
	Incremental          func(any, *imagepkg.Pixmap)
}

// NotModifiedImage is a sentinel value returned by image loaders to indicate
// the image has not been modified (HTTP 304). C uses NOT_MODIFIED = (void*)-1.
// It is distinct from nil (which means "no image" / error).
var NotModifiedImage = errors.New("not modified")

// Wired by cmd/movian-go init; nil-safe methods make unwired calls no-ops.

// SetPlaybackInfoFn — C: the vpi_handlers LIST_HEAD global.
const (
	BackendVideoPrimary        = 0x1  // BACKEND_VIDEO_PRIMARY
	BackendVideoNoAudio        = 0x2  // BACKEND_VIDEO_NO_AUDIO
	BackendVideoNoFSScan       = 0x4  // BACKEND_VIDEO_NO_FS_SCAN
	BackendVideoSetTitle       = 0x8  // BACKEND_VIDEO_SET_TITLE
	BackendVideoNoSubtitleScan = 0x10 // BACKEND_VIDEO_NO_SUBTITLE_SCAN
)

type VideoArgs struct {
	Flags                int
	Priority             int
	ResumeMode           int
	Mimetype             string
	CanonicalURL         string
	Title                string
	IMDB                 string
	Season               int
	Episode              int
	Year                 int
	HashValid            bool
	Filesize             int64
	OpenSubHash          uint64
	SubDBHash            [16]byte
	ParentURL            string
	ParentTitle          string
	Origin               any
	LoadRequestTimestamp int64
}

// Backend represents a media backend
type Backend struct {
	Refcount int32
	Flags    int

	Start func() error
	Fini  func()

	CanHandle func(url string) int

	Open  func(page any, url string, sync bool) error
	Open2 func(page any, url string, sync bool, opaque any) error

	PlayVideo func(url string, mediaPipe any,
		videoQueue any, vsourceList any, va *VideoArgs) (any, error)

	PlayAudio func(url string, mediaPipe any, paused bool,
		mimetype string, opaque any) (any, error)

	Imageloader func(url string, imageMeta any,
		cacheControl *int, cancellable any, backend *Backend) (any, error)

	Normalize func(url string, dst []byte) int

	Probe func(url string, timeoutMs int) (ProbeResult, error)

	Search func(model any, query string, loading any)

	ResolveItem func(url string, item any) error

	// Members for BACKEND_DYNAMIC instances
	Destroy func(backend *Backend)

	Opaque any

	Prefix string
}

// proxyBackendAdapter wraps a Backend to implement propcore.ProxyBackend interface
type proxyBackendAdapter struct {
	backend *Backend
}

// Destroy implements propcore.ProxyBackend interface
func (pba *proxyBackendAdapter) Destroy() {
	if pba != nil && pba.backend != nil && pba.backend.Destroy != nil {
		pba.backend.Destroy(pba.backend)
	}
}

// Imageloader implements propcore.ProxyBackend interface
func (pba *proxyBackendAdapter) Imageloader(url string, imageMeta any,
	cacheControl *int, cancellable any) (any, error) {
	if pba != nil && pba.backend != nil && pba.backend.Imageloader != nil {
		return pba.backend.Imageloader(url, imageMeta, cacheControl, cancellable, pba.backend)
	}
	return nil, nil
}

// NewProxyBackendAdapter creates a proxy backend adapter
func NewProxyBackendAdapter(be *Backend) *proxyBackendAdapter {
	return &proxyBackendAdapter{backend: be}
}

// BackendSystem manages all backends and related state
type LoadingImage struct {
	url     string
	image   any // image_t * in C
	waiters int
	done    bool
}

// NewBackendSystem creates a new BackendSystem instance
type FreetypeOps interface {
	GetContext() int
	LoadDynamicFontBuf(data []byte, fontDomain int, source string) any
	UnloadFont(h any)
}

// SetHLSPlayer wires hls_play_extm3u (C: direct call fa_video.c:635).
type PluginSearchProvider interface {
	GetPlugins() []PluginSearchEntry
	// LoadAllRepos — C: plugins_upgrade_check (plugins.c:1274-1293) —
	// refreshes repo listings; invoked when plugin:repo pages open.
	LoadAllRepos() error
}

// PluginSearchEntry represents a plugin for search purposes.
type PluginSearchEntry struct {
	Title     string
	RepoModel *propcore.Prop
}

// GetFileAccessManager returns the file access manager
