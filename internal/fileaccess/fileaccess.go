package fileaccess

import (
	"errors"
	"strings"
)

// dataRoot — C: app_dataroot() result cached per-manager for
// dataroot:// resolution. Lives on FileAccessManager (fam.dataRoot).

// Content type constants.
// C: contenttype_t (metadata.h:63-78) — values are ABI-visible via
// fde_type/fs_type and content2type; they must match metadata.h exactly.
const (
	ContentUnknown  = 0  // CONTENT_UNKNOWN
	ContentDir      = 1  // CONTENT_DIR
	ContentFile     = 2  // CONTENT_FILE
	ContentArchive  = 3  // CONTENT_ARCHIVE
	ContentAudio    = 4  // CONTENT_AUDIO
	ContentVideo    = 5  // CONTENT_VIDEO
	ContentPlaylist = 6  // CONTENT_PLAYLIST
	ContentDVD      = 7  // CONTENT_DVD
	ContentImage    = 8  // CONTENT_IMAGE
	ContentAlbum    = 9  // CONTENT_ALBUM
	ContentPlugin   = 10 // CONTENT_PLUGIN
	ContentFont     = 11 // CONTENT_FONT
	ContentShare    = 12 // CONTENT_SHARE
	ContentDocument = 13 // CONTENT_DOCUMENT
)

// File access flags
const (
	FaDebug              = 0x1
	FaNoDebug            = 0x2
	FaStreaming          = 0x4
	FaCache              = 0x8
	FaBufferedSmall      = 0x10
	FaBufferedBig        = 0x20
	FaDisableAuth        = 0x40
	FaCompression        = 0x80
	FaNofollow           = 0x100
	FaWrite              = 0x400
	FaAppend             = 0x800
	FaImportant          = 0x1000
	FaNoRetries          = 0x2000
	FaNoParking          = 0x4000
	FaBufferedNoPrefetch = 0x8000
	FaContentOnError     = 0x10000
	FaNonInteractive     = 0x20000
	FaNoCookies          = 0x40000
	FaSSLVerify          = 0x80000
)

// Normalize normalizes a URL by standardizing path separators and removing duplicates
func Normalize(url string) string {
	// Convert backslashes to forward slashes
	url = strings.ReplaceAll(url, "\\", "/")

	// Preserve the scheme (e.g. "file://", "http://") — only collapse
	// duplicate slashes in the path portion, not in the scheme separator.
	// C's fa_normalize preserves the scheme prefix.
	schemeEnd := -1
	if idx := strings.Index(url, "://"); idx >= 0 {
		schemeEnd = idx + 3 // keep the "://" intact (3 chars: ':', '/', '/')
	}

	var scheme, path string
	if schemeEnd >= 0 && schemeEnd <= len(url) {
		scheme = url[:schemeEnd]
		path = url[schemeEnd:]
	} else {
		path = url
	}

	// Remove duplicate slashes in the path portion only
	for strings.Contains(path, "//") {
		path = strings.ReplaceAll(path, "//", "/")
	}

	// Remove trailing slash (except for root)
	if len(path) > 1 && strings.HasSuffix(path, "/") {
		path = path[:len(path)-1]
	}

	return scheme + path
}

// Parent returns the parent URL.
// C: fa_parent (fileaccess.c:1810-1847) — splits proto://path, strips
// the last component (collapsing a trailing slash), emits "proto://p/".
func Parent(url string) (string, error) {
	proto := "file"
	parent := url
	if before, after, ok := strings.Cut(url, "://"); ok {
		proto = before
		parent = after
	}

	if parent != "/" {
		x := strings.LastIndex(parent, "/")
		if x >= 0 {
			trailing := x == len(parent)-1
			parent = parent[:x]
			if trailing {
				// C: if(x[1] == 0) — '/' was the last char; strip the
				// previous component as well.
				if x = strings.LastIndex(parent, "/"); x >= 0 {
					parent = parent[:x]
				}
			}
			return proto + "://" + parent + "/", nil
		}
	}
	return "", errors.New("no parent")
}

// PathJoin joins path components.
// C: fa_pathjoin (fileaccess.c:1883-1891) — strips leading "./" from
// p2, inserts '/' only when p1 doesn't already end with one.
func PathJoin(p1, p2 string) string {
	for strings.HasPrefix(p2, "./") {
		p2 = p2[2:]
	}
	l1 := len(p1)
	sep := ""
	if l1 == 0 || p1[l1-1] != '/' {
		sep = "/"
	}
	return p1 + sep + p2
}

// URLGetLastComponent returns the last component (filename) of a URL.
// C: fa_url_get_last_component (fileaccess.c:1946-1962) — resolves the
// protocol for fap_get_last_component, else scans the raw URL.
func (fam *FileAccessManager) URLGetLastComponent(url string) string {
	fap, filename, err := fam.FAResolveProto(url)
	if err != nil {
		fap = nil
		filename = ""
	}
	s := faURLGetLastComponentI(fap, filename, url)
	fapRelease(fap)
	return s
}

// faURLGetLastComponentI — C: fa_url_get_last_component_i
// (fileaccess.c:1897-1940). Strips a trailing '/' and '|', then takes
// the text after the last '/'. Uses fap_get_last_component when the
// protocol provides one.
func faURLGetLastComponentI(fap *FAProtocol, filename, url string) string {
	if filename != "" && fap != nil && fap.GetLastComponent != nil {
		var dst [1024]byte
		fap.GetLastComponent(fap, filename, dst[:])
		return strings.TrimRight(string(dst[:]), "\x00")
	}

	e := len(url)
	if e > 0 && url[e-1] == '/' {
		e--
	}
	if e > 0 && url[e-1] == '|' {
		e--
	}
	if e == 0 {
		return ""
	}
	b := e
	for b > 0 {
		b--
		if url[b] == '/' {
			b++
			break
		}
	}
	return url[b:e]
}

// SanitizeFilename replaces characters invalid in filenames with '_'.
// C: fa_sanitize_filename (fileaccess.c:2090-2109) — bytes 1-31, '/',
// '\\', ':', '?', '"', '|', '<', '>', and 128-255 become '_'.
// NOTE: '*' is NOT replaced (C leaves it).
func SanitizeFilename(filename string) string {
	b := []byte(filename)
	for i, c := range b {
		switch {
		case c >= 1 && c <= 31, c >= 128:
			b[i] = '_'
		case c == '/', c == '\\', c == ':', c == '?',
			c == '"', c == '|', c == '<', c == '>':
			b[i] = '_'
		}
	}
	return string(b)
}

// Makedirs creates directories recursively
// C: fa_makedirs (fileaccess.c:1179-1197) — resolves protocol, calls
// fa_makedir_p (which recurses to parents on FAP_NOENT), releases.
func Makedirs(fam *FileAccessManager, url string) error {
	proto, filename, err := fam.FAResolveProto(url)
	if err != nil {
		// C: resolve failure → return -1 with errbuf set
		return err
	}
	defer fapRelease(proto)
	r := faMakedirP(fam, proto, url, filename)
	if r != FAP_OK {
		return errors.New(FAErrCodeStr(r))
	}
	return nil
}

// Makedir creates a directory (single-shot, non-recursive).
// C: fa_makedir (fileaccess.c:1201-1219) — resolves protocol, calls
// fap_makedir once, releases. Returns FAP_NOT_SUPPORTED if the
// protocol has no fap_makedir. Distinct from Makedirs (fa_makedirs)
// which recurses to parents on FAP_NOENT.
func Makedir(fam *FileAccessManager, url string) error {
	proto, filename, err := fam.FAResolveProto(url)
	if err != nil {
		// C: fa_resolve_proto failure → FAP_NOENT
		return errors.New(FAErrCodeStr(FAP_NOENT))
	}
	defer fapRelease(proto)
	r := fapMakedirOnce(fam, proto, url, filename)
	if r != FAP_OK {
		return errors.New(FAErrCodeStr(r))
	}
	return nil
}

// Unlink deletes a file
// C: fa_unlink (fileaccess.c:1228-1246) — resolves protocol, calls
// fap_unlink, releases protocol. Works on ANY protocol with fap_unlink.
func Unlink(fam *FileAccessManager, url string) error {
	proto, filename, err := fam.FAResolveProto(url)
	if err == nil {
		defer fapRelease(proto)
		if proto.Unlink != nil {
			return proto.Unlink(proto, filename)
		}
	}
	if fam != nil {
		p := fam.FindProtocol(url)
		if p != nil {
			if fsProto, ok := p.(*FSProtocol); ok {
				return fsProto.Unlink(url)
			}
		}
	}
	return errors.New("No unlink support in filesystem")
}

// Rmdir removes a directory
// C: fa_rmdir (fileaccess.c:1253-1271) — resolves protocol, calls
// fap_rmdir, releases protocol. Works on ANY protocol with fap_rmdir.
func Rmdir(fam *FileAccessManager, url string) error {
	proto, filename, err := fam.FAResolveProto(url)
	if err == nil {
		defer fapRelease(proto)
		if proto.Rmdir != nil {
			return proto.Rmdir(proto, filename)
		}
	}
	if fam != nil {
		p := fam.FindProtocol(url)
		if p != nil {
			if fsProto, ok := p.(*FSProtocol); ok {
				return fsProto.Rmdir(url)
			}
		}
	}
	return errors.New("No rmdir support in filesystem")
}

// Rename renames a file or directory
// C: fa_rename (fileaccess.c:1278-) — resolves both protocols, checks
// same protocol, calls fap_rename, releases both. Works on ANY protocol.
func Rename(fam *FileAccessManager, oldURL, newURL string) error {
	oldProto, oldFilename, err := fam.FAResolveProto(oldURL)
	if err == nil {
		newProto, newFilename, err2 := fam.FAResolveProto(newURL)
		if err2 == nil {
			defer fapRelease(oldProto)
			defer fapRelease(newProto)
			if oldProto == newProto && oldProto.Rename != nil {
				return oldProto.Rename(oldProto, oldFilename, newFilename)
			}
		} else {
			fapRelease(oldProto)
		}
	}
	// Fallback to FileAccessManager protocol system
	if fam != nil {
		oldP := fam.FindProtocol(oldURL)
		newP := fam.FindProtocol(newURL)
		if oldP != nil && newP != nil && oldP == newP {
			if fsProto, ok := oldP.(*FSProtocol); ok {
				return fsProto.Rename(oldURL, newURL)
			}
		}
	}
	return errors.New("No rename support in filesystem")
}

// Copy copies a file from source to destination.
// C: fa_copy (fileaccess.c:1121-1128) — delegates to the canonical
// faCopy/fa_copy_from_fh path (fileaccess_ops.go).
func Copy(fam *FileAccessManager, to, from string) error {
	return faCopy(fam, to, from)
}

// FSInfo returns filesystem information for the given URL
// C: fa_fsinfo (fileaccess.c:1372-) — resolves protocol, calls
// fap_fsinfo, releases protocol. Works on ANY protocol with fap_fsinfo.
func FSInfo(fam *FileAccessManager, url string) (*FileSystemInfo, error) {
	proto, filename, err := fam.FAResolveProto(url)
	if err == nil {
		defer fapRelease(proto)
		if proto.FSInfo != nil {
			return proto.FSInfo(proto, filename)
		}
	}
	// Fallback to FileAccessManager protocol system
	if fam != nil {
		p := fam.FindProtocol(url)
		if p != nil {
			if fsProto, ok := p.(*FSProtocol); ok {
				return fsProto.FSInfo(url)
			}
		}
	}
	return nil, errors.New("No fsinfo support in filesystem")
}
