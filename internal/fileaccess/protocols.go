package fileaccess

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"
)

// FSProtocol implements the filesystem protocol
func (p *FSProtocol) Name() string {
	return "file"
}

func (p *FSProtocol) CanHandle(url string) bool {
	// Only handle file:// URLs and plain file paths (no scheme prefix)
	// URLs like "slideshow:", "settings:", "page:home" have a scheme prefix
	// and should NOT be handled by the filesystem protocol
	if strings.HasPrefix(url, "file://") {
		return true
	}
	// Windows absolute path — C's fa_resolve_proto looks for "://", so
	// "C:\..." / "C:/..." always fell through to the fs handler. The
	// single-colon scheme check above must not eat drive letters.
	if isWindowsDrivePath(url) {
		return true
	}
	// Check if it looks like a URL scheme (e.g. "http:", "slideshow:")
	if idx := strings.Index(url, ":"); idx > 0 {
		// Check if it's a scheme (letter followed by colon)
		scheme := url[:idx]
		if isAlphaScheme(scheme) {
			return false
		}
	}
	return !strings.Contains(url, "://")
}

// isWindowsDrivePath reports whether url is a windows absolute path
// ("X:\" or "X:/"), on windows only — linux keeps the plain
// single-colon scheme detection.
func isWindowsDrivePath(url string) bool {
	return runtime.GOOS == "windows" && len(url) > 2 && url[1] == ':' &&
		(url[2] == '\\' || url[2] == '/') && isAlphaScheme(url[:1])
}

// isAlphaScheme checks if a string is a valid URL scheme (starts with letter, followed by alphanumeric)
func isAlphaScheme(s string) bool {
	if len(s) == 0 {
		return false
	}
	if !((s[0] >= 'a' && s[0] <= 'z') || (s[0] >= 'A' && s[0] <= 'Z')) {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func (p *FSProtocol) Open(url string, extra *OpenExtra) (*Handle, error) {
	// C: fs_open (fa_fs.c:186) — includes /dev/stdout+stderr,
	// FA_WRITE/FA_APPEND, and the split-piece (.001/.002/…) fallback.
	return fsOpen(p, p.urlToPath(url), url, extra)
}

func (p *FSProtocol) Stat(url string) (*FileStat, error) {
	// C: fs_stat (fa_fs.c:376) — split-piece fallback on ENOENT.
	return fsStat(p.urlToPath(url))
}

func (p *FSProtocol) ScanDir(ctx context.Context, url string) (*Dir, error) {
	path := p.urlToPath(url)

	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}

	dir := DirAlloc()

	for _, entry := range entries {
		name := entry.Name()
		// C: fs_scandir (fa_fs.c:117) stats each entry via
		// fs_urlsnprintf("", url, d_name) — non-file/dir skipped.
		statPath := fsURLsprintf("", path, name)
		st, serr := os.Stat(statPath)
		if serr != nil {
			continue
		}
		var fileType int
		if st.IsDir() {
			fileType = ContentDir
		} else if st.Mode().IsRegular() {
			fileType = ContentFile
			// C: split pieces — only .001 is listed, with the
			// ".00x" suffix stripped to the base name.
			if n := isSplittedFileName(name); n > 0 {
				if n != 1 {
					continue
				}
				name = name[:len(name)-4]
			}
		} else {
			continue
		}
		DirAdd(dir, fsURLsprintf("file://", path, name), name, fileType)
	}

	return dir, nil
}

func (p *FSProtocol) Makedirs(url string) error {
	path := p.urlToPath(url)
	return os.MkdirAll(path, 0755)
}

func (p *FSProtocol) Unlink(url string) error {
	// C: fs_unlink (fa_fs.c:429) — split-piece fallback.
	return fsUnlink(p.urlToPath(url))
}

func (p *FSProtocol) Rmdir(url string) error {
	path := p.urlToPath(url)
	return os.Remove(path)
}

func (p *FSProtocol) Rename(oldURL, newURL string) error {
	oldPath := p.urlToPath(oldURL)
	newPath := p.urlToPath(newURL)
	return os.Rename(oldPath, newPath)
}

func (p *FSProtocol) urlToPath(url string) string {
	// C: fap_* receive the URL with the "file://" scheme already
	// stripped by fa_resolve_proto; the path is used verbatim.
	if strings.HasPrefix(url, "file://") {
		path := url[7:]
		// file:///C:/x → C:\x — the third slash belongs to the
		// URL authority separator, not the drive path.
		if runtime.GOOS == "windows" && isWindowsDrivePath(path[1:]) {
			return path[1:]
		}
		return path
	}
	return url
}

// HTTPProtocol — C: fa_protocol_http / fa_protocol_https (fa_http.c:2403).
// Delegates to the canonical machinery in fa_http.go (connection pool,
// cookies, auth, redirects, chunked/gzip reads).
func (p *HTTPProtocol) Name() string {
	return "http"
}

func (p *HTTPProtocol) CanHandle(url string) bool {
	return strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://")
}

func (p *HTTPProtocol) Open(url string, extra *OpenExtra) (*Handle, error) {
	flags := 0
	if extra != nil {
		flags = extra.Flags
	}
	fap := p.fam.lookupFAProtocol("http")
	https := strings.HasPrefix(url, "https://")
	if https {
		if p2 := p.fam.lookupFAProtocol("https"); p2 != nil {
			fap = p2
		}
	}
	var h *Handle
	var herr error
	if https && p.fam.Gconf().FAHTTP2.Load() {
		// Go extension: net/http → HTTP/2 via ALPN (fa_http2.go)
		h, herr = h2Open(fap, url, flags, extra)
	} else {
		h, herr = httpOpen(fap, url, flags, extra)
	}
	if h == nil {
		return nil, herr
	}
	h.proto = p
	return h, nil
}

func (p *HTTPProtocol) Stat(url string) (*FileStat, error) {
	fap := p.fam.lookupFAProtocol("http")
	https := strings.HasPrefix(url, "https://")
	if https {
		if p2 := p.fam.lookupFAProtocol("https"); p2 != nil {
			fap = p2
		}
	}
	if https && p.fam.Gconf().FAHTTP2.Load() {
		return h2Stat(fap, url, 0)
	}
	return httpStat(fap, url, 0)
}

func (p *HTTPProtocol) ScanDir(ctx context.Context, url string) (*Dir, error) {
	return nil, errors.New("HTTP does not support directory scanning")
}

// FTPProtocol implements the FTP protocol
// FTPProtocol — C: fa_protocol_ftp (fa_ftp.c:897). Delegates to the
// canonical machinery in fa_ftp.go (pooled control connections +
// per-file ftpFile + PASV data channels).
type FTPProtocol struct {
	fam   *FileAccessManager // C: implicit global fa context
	usage usageEventer       // C: usage_event (fa_ftp.c:281)
}

func (p *FTPProtocol) Name() string {
	return "ftp"
}

func (p *FTPProtocol) CanHandle(url string) bool {
	return strings.HasPrefix(url, "ftp://")
}

func (p *FTPProtocol) Open(url string, extra *OpenExtra) (*Handle, error) {
	// C: ftp_open (fa_ftp.c:578) — receives the full ftp:// URL
	// (FAP_INCLUDE_PROTO_IN_URL).
	ff, err := p.fam.ftpOpen(url, p.usage)
	if err != nil {
		return nil, err
	}
	return &Handle{
		proto:  p,
		reader: ff,
		seeker: ff,
		url:    url,
		size:   ff.size,
		sizer:  ff.Size,
	}, nil
}

func (p *FTPProtocol) Stat(url string) (*FileStat, error) {
	// C: ftp_stat (fa_ftp.c:765)
	st, err := p.fam.ftpStat(url, 0, p.usage)
	if err != nil {
		return nil, err
	}
	return st, nil
}

func (p *FTPProtocol) ScanDir(ctx context.Context, url string) (*Dir, error) {
	// C: ftp_scandir (fa_ftp.c:561)
	fd := DirAlloc()
	if err := p.fam.ftpScandir(url, fd, p.usage); err != nil {
		DirFree(fd)
		return nil, err
	}
	return fd, nil
}

func (p *FTPProtocol) urlToPath(url string) string {
	// Extract path from ftp:// URL
	if strings.HasPrefix(url, "ftp://") {
		// Remove ftp://host:port prefix
		parts := strings.SplitN(url, "/", 4)
		if len(parts) >= 4 {
			return "/" + parts[3]
		}
		return "/"
	}
	return url
}

// SMBProtocol implements the SMB protocol
// SMBProtocol — C: fa_protocol_smb (fa_nativesmb.c:2921). Delegates to the
// canonical machinery in pkg/fileaccess/smb via hooks (that package
// imports fileaccess/core, so core can't import it — the seams are wired
// from smb's init()).
type SMBProtocol struct{}

// SMBFile is the raw native-SMB file object (C: smb_file_t) returned by
// SMBOpenHook — fills the Handle's reader/seeker/sizer slots.
type SMBFile interface {
	io.Reader
	io.Seeker
	io.Closer
	Size() int64 // C: fap_fsize = smb_fsize
}

// SMBOps — the smb provider's operation table (C: fa_protocol_smb's
// fap_* vtable). Wired from pkg/fileaccess/smb's init() — core can't
// import smb (cycle), so the dependency is inverted like io.Reader
// consumers: the interface is declared here and implemented there.
type SMBOps interface {
	Open(url string, extra *OpenExtra) (SMBFile, error)
	Scandir(url string, flags int) (*Dir, error)
	Stat(url string) (*FileStat, error)
	StatFAP(fap *FAProtocol, url string, flags int) (*FileStat, error)
	Unlink(fap *FAProtocol, url string) error
	Rmdir(fap *FAProtocol, url string) error
	SetXattr(fap *FAProtocol, url, name string, data []byte) int
	GetXattr(fap *FAProtocol, url, name string) ([]byte, int)
	NoParking(fh *Handle) bool
}

var smbOps SMBOps

// SetSMBOps wires the smb provider's operation table (C: FAP_REGISTER).
func SetSMBOps(ops SMBOps) { smbOps = ops }

func (p *SMBProtocol) Name() string {
	return "smb"
}

func (p *SMBProtocol) CanHandle(url string) bool {
	return strings.HasPrefix(url, "smb://")
}

func (p *SMBProtocol) Open(url string, extra *OpenExtra) (*Handle, error) {
	// C: fap_open = smb_open
	if smbOps == nil {
		return nil, errors.New("SMB protocol not available")
	}
	sf, err := smbOps.Open(url, extra)
	if err != nil {
		return nil, err
	}
	return &Handle{
		proto:  p,
		reader: sf,
		seeker: sf,
		url:    url,
		size:   sf.Size(),
		sizer:  sf.Size,
	}, nil
}

func (p *SMBProtocol) Stat(url string) (*FileStat, error) {
	// C: fap_stat = smb_stat
	if smbOps == nil {
		return nil, errors.New("SMB protocol not available")
	}
	return smbOps.Stat(url)
}

func (p *SMBProtocol) ScanDir(ctx context.Context, url string) (*Dir, error) {
	// C: fap_scan = smb_scandir
	if smbOps == nil {
		return nil, errors.New("SMB protocol not available")
	}
	return smbOps.Scandir(url, FAFlagsFromCtx(ctx))
}

// populateSMBFAProtocol fills the bare "smb" gate with the canonical
// fap ops (C: fa_protocol_smb's fap_stat/unlink/rmdir/xattr/no_parking).
func populateSMBFAProtocol(fp *FAProtocol) {
	fp.Stat = func(fap *FAProtocol, filename string, flags int) (*FileStat, error) {
		if smbOps == nil {
			return nil, ErrNotSupported
		}
		return smbOps.StatFAP(fap, filename, flags)
	}
	fp.Unlink = func(fap *FAProtocol, filename string) error {
		if smbOps == nil {
			return errors.New("not supported")
		}
		return smbOps.Unlink(fap, filename)
	}
	fp.Rmdir = func(fap *FAProtocol, filename string) error {
		if smbOps == nil {
			return errors.New("not supported")
		}
		return smbOps.Rmdir(fap, filename)
	}
	fp.SetXattr = func(fap *FAProtocol, filename, name string, data []byte) int {
		if smbOps == nil {
			return FAP_NOT_SUPPORTED
		}
		return smbOps.SetXattr(fap, filename, name, data)
	}
	fp.GetXattr = func(fap *FAProtocol, filename, name string) ([]byte, int) {
		if smbOps == nil {
			return nil, FAP_NOT_SUPPORTED
		}
		return smbOps.GetXattr(fap, filename, name)
	}
	fp.NoParking = func(fh *Handle) bool {
		// C: fap_no_parking = smb_no_parking → always 1
		return smbOps == nil || smbOps.NoParking(fh)
	}
}

// MemoryProtocol implements the memory-backed file protocol
func (p *MemoryProtocol) Name() string {
	return "mem"
}

func (p *MemoryProtocol) CanHandle(url string) bool {
	return strings.HasPrefix(url, "mem://")
}

func (p *MemoryProtocol) Open(url string, extra *OpenExtra) (*Handle, error) {
	// Parse ID from URL
	parts := strings.Split(url, "/")
	if len(parts) < 3 {
		return nil, errors.New("invalid memory URL")
	}

	var id int
	_, err := fmt.Sscanf(parts[2], "%d", &id)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	data, ok := p.files[id]
	p.mu.Unlock()
	if !ok {
		return nil, errors.New("memory file not found")
	}

	reader := &memoryReader{
		data: data,
		pos:  0,
	}

	return &Handle{
		proto:    p,
		reader:   reader,
		seeker:   reader,
		url:      url,
		size:     int64(len(data)),
		position: 0,
		closed:   false,
	}, nil
}

func (p *MemoryProtocol) Stat(url string) (*FileStat, error) {
	parts := strings.Split(url, "/")
	if len(parts) < 3 {
		return nil, errors.New("invalid memory URL")
	}

	var id int
	_, err := fmt.Sscanf(parts[2], "%d", &id)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	data, ok := p.files[id]
	p.mu.Unlock()
	if !ok {
		return nil, errors.New("memory file not found")
	}

	return &FileStat{
		Size:  int64(len(data)),
		Type:  ContentFile,
		MTime: time.Now(),
	}, nil
}

func (p *MemoryProtocol) ScanDir(ctx context.Context, url string) (*Dir, error) {
	return nil, errors.New("memory does not support directory scanning")
}

// RegisterMemoryFile registers a memory file and returns its ID
func (p *MemoryProtocol) Register(data []byte) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	id := p.nextID
	p.files[id] = data
	p.nextID++
	return id
}

// UnregisterMemoryFile removes a memory file
func (p *MemoryProtocol) Unregister(id int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.files, id)
}

// memoryReader implements io.ReadCloser and io.Seeker for memory files
type memoryReader struct {
	data []byte
	pos  int
}

func (r *memoryReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}

	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

func (r *memoryReader) Close() error {
	return nil
}

func (r *memoryReader) Seek(offset int64, whence int) (int64, error) {
	var newPos int

	switch whence {
	case io.SeekStart:
		newPos = int(offset)
	case io.SeekCurrent:
		newPos = r.pos + int(offset)
	case io.SeekEnd:
		newPos = len(r.data) + int(offset)
	default:
		return 0, errors.New("invalid whence")
	}

	if newPos < 0 || newPos > len(r.data) {
		return 0, errors.New("seek out of bounds")
	}

	r.pos = newPos
	return int64(newPos), nil
}
