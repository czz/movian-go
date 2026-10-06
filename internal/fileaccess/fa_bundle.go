package fileaccess

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// MemFile represents a file in memory
type MemFile struct {
	data []byte
	size int
	id   int
}

// FileBundle represents a bundle of files
type FileBundle struct {
	prefix  string
	entries []*FileBundleEntry
	next    *FileBundle
}

// FileBundleEntry represents an entry in a file bundle
type FileBundleEntry struct {
	filename string
	data     []byte
	size     int
}

// BundleHandle represents a handle to a bundle file
type BundleHandle struct {
	ptr  []byte
	size int
	pos  int
}

// BundleManager manages file bundles and memory files
type BundleManager struct {
	bundleMutex sync.Mutex
	fileBundles *FileBundle
	memFiles    []*MemFile
	tally       int
}

// NewBundleManager creates a new bundle manager
func NewBundleManager() *BundleManager {
	return &BundleManager{
		fileBundles: nil,
		memFiles:    make([]*MemFile, 0),
		tally:       0,
	}
}

// BundleOpen opens a file from a bundle
func (bm *BundleManager) BundleOpen(url string) (*Handle, error) {
	if bm == nil {
		return nil, errors.New("bundle manager is nil")
	}
	bm.bundleMutex.Lock()
	defer bm.bundleMutex.Unlock()

	fbe := bm.bundleResolve(url)
	if fbe == nil {
		return nil, errors.New("file not found")
	}

	bh := &BundleHandle{
		ptr:  fbe.data,
		size: fbe.size,
		pos:  0,
	}

	return &Handle{
		proto:    &BundleProtocol{bm: bm},
		reader:   bh,
		writer:   nil,
		seeker:   bh,
		url:      url,
		size:     int64(fbe.size),
		position: 0,
		closed:   false,
	}, nil
}

// BundleRegister registers a file bundle
func (bm *BundleManager) BundleRegister(fb *FileBundle) {
	if bm == nil {
		return
	}
	bm.bundleMutex.Lock()
	defer bm.bundleMutex.Unlock()

	fb.next = bm.fileBundles
	bm.fileBundles = fb
}

// BundleRegisterFS registers every file found under root in fsys as a
// bundle entry — Go equivalent of a support/mkbundle-generated
// filebundle plus its filebundle_register() constructor. Filenames are
// stored relative to root and prefix is the BUNDLES entry verbatim
// (mkbundle -p $<), so register e.g. ("glwskins/flat", "glwskins/flat", fsys).
func (bm *BundleManager) BundleRegisterFS(prefix, root string, fsys fs.FS) error {
	if bm == nil {
		return errors.New("bundle manager is nil")
	}
	fb := &FileBundle{prefix: prefix}
	err := fs.WalkDir(fsys, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(path, root)
		name = strings.TrimPrefix(name, "/")
		fb.entries = append(fb.entries, &FileBundleEntry{
			filename: name,
			data:     data,
			size:     len(data),
		})
		return nil
	})
	if err != nil {
		return err
	}
	bm.BundleRegister(fb)
	return nil
}

// bundleResolve resolves a file URL in the bundle (internal)
// C: resolve_file (fa_bundle.c:60-84). C receives the post-scheme
// filename from fap dispatch; our callers hand the full "bundle://" URL —
// strip the scheme, then apply C's while(*url == '/') url++ so that
// prefixes are stored verbatim like mkbundle's -p argument.
func (bm *BundleManager) bundleResolve(url string) *FileBundleEntry {
	if bm == nil {
		return nil
	}
	url = strings.TrimPrefix(url, "bundle://")
	// C: while(*url == '/') url++;
	for len(url) > 0 && url[0] == '/' {
		url = url[1:]
	}

	// Search through bundles
	for fb := bm.fileBundles; fb != nil; fb = fb.next {
		if len(url) < len(fb.prefix) || url[:len(fb.prefix)] != fb.prefix {
			continue
		}

		u := url[len(fb.prefix):]
		if len(u) == 0 || u[0] != '/' {
			continue
		}
		u = u[1:]

		for _, fbe := range fb.entries {
			if fbe.filename == u {
				return fbe
			}
		}
	}

	return nil
}

// MemFileRegister registers a file in memory and returns its ID
func (bm *BundleManager) MemFileRegister(data []byte) int {
	if bm == nil {
		return 0
	}
	bm.bundleMutex.Lock()
	defer bm.bundleMutex.Unlock()

	bm.tally++
	mf := &MemFile{
		data: data,
		size: len(data),
		id:   bm.tally,
	}
	bm.memFiles = append(bm.memFiles, mf)
	return bm.tally
}

// MemFileUnregister removes a file from memory
func (bm *BundleManager) MemFileUnregister(id int) {
	if bm == nil {
		return
	}
	bm.bundleMutex.Lock()
	defer bm.bundleMutex.Unlock()

	for i, mf := range bm.memFiles {
		if mf.id == id {
			bm.memFiles = slices.Delete(bm.memFiles, i, i+1)
			return
		}
	}
}

// MemFileGet gets a file from memory by ID
func (bm *BundleManager) MemFileGet(id int) *MemFile {
	if bm == nil {
		return nil
	}
	bm.bundleMutex.Lock()
	defer bm.bundleMutex.Unlock()

	for _, mf := range bm.memFiles {
		if mf.id == id {
			return mf
		}
	}
	return nil
}

// MemFileMake creates a handle from memory
func (bm *BundleManager) MemFileMake(data []byte) *Handle {
	if bm == nil {
		return nil
	}
	bh := &BundleHandle{
		ptr:  data,
		size: len(data),
		pos:  0,
	}

	return &Handle{
		proto:    &MemfileProtocol{bm: bm},
		reader:   bh,
		writer:   nil,
		seeker:   bh,
		url:      fmt.Sprintf("memfile://%d", bm.tally),
		size:     int64(len(data)),
		position: 0,
		closed:   false,
	}
}

// Read implements io.Reader
func (bh *BundleHandle) Read(p []byte) (int, error) {
	if len(p) < 1 {
		return len(p), nil
	}

	if bh.pos+len(p) > bh.size {
		p = p[:bh.size-bh.pos]
	}

	copy(p, bh.ptr[bh.pos:])
	bh.pos += len(p)

	if len(p) == 0 {
		return 0, nil
	}

	return len(p), nil
}

// Seek implements io.Seeker
func (bh *BundleHandle) Seek(offset int64, whence int) (int64, error) {
	var np int

	switch whence {
	case 0: // SEEK_SET
		np = int(offset)
	case 1: // SEEK_CUR
		np = bh.pos + int(offset)
	case 2: // SEEK_END
		np = bh.size + int(offset)
	default:
		return 0, errors.New("invalid whence")
	}

	if np < 0 {
		return 0, errors.New("invalid position")
	}

	bh.pos = np
	return int64(np), nil
}

// Close implements io.Closer
func (bh *BundleHandle) Close() error {
	return nil
}

// BundleProtocol is the bundle protocol implementation
type BundleProtocol struct {
	bm *BundleManager
}

// Name returns the protocol name
func (bp *BundleProtocol) Name() string {
	return "bundle"
}

// Open opens a file from the bundle
func (bp *BundleProtocol) Open(url string, extra *OpenExtra) (*Handle, error) {
	return bp.bm.BundleOpen(url)
}

// Stat returns file information
func (bp *BundleProtocol) Stat(url string) (*FileStat, error) {
	fbe := bp.bm.bundleResolve(url)
	if fbe != nil {
		return &FileStat{
			Size: int64(fbe.size),
			Type: ContentFile,
		}, nil
	}

	// Check if it's a directory
	if bp.bm.bundleScanDir(url, nil) == nil {
		return &FileStat{
			Size: 0,
			Type: ContentDir,
		}, nil
	}

	return nil, errors.New("file not found")
}

// ScanDir scans a directory in the bundle
func (bp *BundleProtocol) ScanDir(ctx context.Context, url string) (*Dir, error) {
	d := &Dir{
		Entries: make(map[string]*DirEntry),
		Count:   0,
	}
	err := bp.bm.bundleScanDir(url, d)
	return d, err
}

// CanHandle checks if the protocol can handle the URL
func (bp *BundleProtocol) CanHandle(url string) bool {
	return strings.HasPrefix(url, "bundle://")
}

// bundleScanDir scans a directory in the bundle (internal)
// C: b_scandir (fa_bundle.c) — same post-scheme normalization as
// bundleResolve.
func (bm *BundleManager) bundleScanDir(url string, fd *Dir) error {
	if bm == nil {
		return errors.New("bundle manager is nil")
	}
	url = strings.TrimPrefix(url, "bundle://")
	// Strip leading slashes
	for len(url) > 0 && url[0] == '/' {
		url = url[1:]
	}

	// Root directory - list all bundle prefixes
	if url == "" {
		if fd != nil {
			for fb := bm.fileBundles; fb != nil; fb = fb.next {
				prefix := fb.prefix
				if idx := strings.Index(prefix, "/"); idx != -1 {
					prefix = prefix[:idx]
				}
				fullURL := "bundle://" + prefix
				fd.Entries[prefix] = &DirEntry{
					Filename: prefix,
					URL:      fullURL,
					Type:     ContentDir,
					StatDone: true,
				}
				fd.Count++
			}
		}
		return nil
	}

	// Remove trailing slashes
	url = strings.TrimRight(url, "/")

	// C: b_scandir (fa_bundle.c:189-309) — three match cases per bundle:
	//   url is a strict ancestor of fb->prefix → emit next prefix
	//     segment as a dir (e.g. "glwskins" → "flat" from
	//     prefix "glwskins/flat");
	//   url == fb->prefix (or the strncmp quirk) → list entries;
	//   url deeper than fb->prefix → list matching subdir entries.
	add := func(name, fullURL string, typ int) {
		if fd == nil || name == "" {
			return
		}
		if _, exists := fd.Entries[name]; !exists {
			fd.Entries[name] = &DirEntry{
				Filename: name,
				URL:      fullURL,
				Type:     typ,
				StatDone: true,
			}
			fd.Count++
		}
	}

	ok := false
	for fb := bm.fileBundles; fb != nil; fb = fb.next {
		if strings.HasPrefix(fb.prefix, url) {
			// C: !strncmp(url, fb->prefix, strlen(url))
			if len(fb.prefix) > len(url) && fb.prefix[len(url)] == '/' {
				// Strict ancestor: next prefix segment as dir.
				rest := fb.prefix[len(url)+1:]
				seg := rest
				if idx := strings.Index(rest, "/"); idx != -1 {
					seg = rest[:idx]
				}
				add(seg, "bundle://"+fb.prefix[:len(url)+1]+seg,
					ContentDir)
				ok = true
				continue
			}
			// url == fb.prefix, or the C strncmp quirk (url is a
			// partial prefix, e.g. "glwsk" vs "glwskins/flat") —
			// both list the bundle's entries upstream.
			ok = true
			for _, fbe := range fb.entries {
				name := fbe.filename
				typ := ContentFile
				if idx := strings.Index(name, "/"); idx != -1 {
					name = name[:idx]
					typ = ContentDir
				}
				add(name, "bundle://"+fb.prefix+"/"+name, typ)
			}
			continue
		}
		if strings.HasPrefix(url, fb.prefix+"/") {
			// C: !strncmp(url, fb->prefix, strlen(fb->prefix)) &&
			//    url[len(prefix)] == '/' — deeper subdirectory.
			u := url[len(fb.prefix)+1:]
			for _, fbe := range fb.entries {
				if !strings.HasPrefix(fbe.filename, u+"/") {
					continue
				}
				ok = true
				u2 := fbe.filename[len(u)+1:]
				name := u2
				typ := ContentFile
				fullURL := "bundle://" + fb.prefix + "/" + fbe.filename
				if idx := strings.Index(u2, "/"); idx != -1 {
					name = u2[:idx]
					typ = ContentDir
					fullURL = "bundle://" + fb.prefix + "/" + u + "/" + name
				}
				add(name, fullURL, typ)
			}
		}
	}

	if !ok {
		return errors.New("no such directory")
	}
	return nil
}

// MemfileProtocol is the memfile protocol implementation
type MemfileProtocol struct {
	bm *BundleManager
}

// Name returns the protocol name
func (mp *MemfileProtocol) Name() string {
	return "memfile"
}

// Open opens a file from memory
func (mp *MemfileProtocol) Open(url string, extra *OpenExtra) (*Handle, error) {
	// Parse ID from URL (memfile://123)
	idStr := strings.TrimPrefix(url, "memfile://")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return nil, errors.New("invalid memfile URL")
	}

	mf := mp.bm.MemFileGet(id)
	if mf == nil {
		return nil, errors.New("no such file")
	}

	bh := &BundleHandle{
		ptr:  mf.data,
		size: mf.size,
		pos:  0,
	}

	return &Handle{
		proto:    mp,
		reader:   bh,
		writer:   nil,
		seeker:   bh,
		url:      url,
		size:     int64(mf.size),
		position: 0,
		closed:   false,
	}, nil
}

// Stat returns file information
func (mp *MemfileProtocol) Stat(url string) (*FileStat, error) {
	idStr := strings.TrimPrefix(url, "memfile://")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return nil, errors.New("invalid memfile URL")
	}

	mf := mp.bm.MemFileGet(id)
	if mf == nil {
		return nil, errors.New("no such file")
	}

	return &FileStat{
		Size: int64(mf.size),
		Type: ContentFile,
	}, nil
}

// ScanDir scans a directory (not supported for memfile)
func (mp *MemfileProtocol) ScanDir(ctx context.Context, url string) (*Dir, error) {
	return nil, errors.New("not supported")
}

// CanHandle checks if the protocol can handle the URL
func (mp *MemfileProtocol) CanHandle(url string) bool {
	return strings.HasPrefix(url, "memfile://")
}
