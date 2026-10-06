package fileaccess

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/czz/movian-go/internal/libav"
)

// FAResolveProto resolves a protocol from a URL
// C: fa_resolve_proto (fileaccess.c:89-167) — extracts protocol name,
// handles data: specially, iterates protocol list with fap_match_proto,
// retains the protocol (refcount++), handles redirect.
func (fam *FileAccessManager) FAResolveProto(url string) (*FAProtocol, string, error) {
	if fam == nil { // unwired port — C would deref the global
		return nil, "", ErrNotSupported
	}
	var buf [256]byte
	n := 0

	// C: while(*url != ':' && *url>31 && n < sizeof(buf) - 1) buf[n++] = *url++;
	for i := 0; i < len(url) && url[i] > 31 && url[i] != ':' && n < len(buf)-1; i++ {
		buf[n] = url[i]
		n++
	}
	buf[n] = 0
	protoName := string(buf[:n])

	// C: if(!strcmp(buf, "data") && url[0] == ':') { *p = &fa_protocol_data; return strdup(url + 1); }
	// (fileaccess.c:104-108) — special handling for data: protocol
	if protoName == "data" && len(url) > n && url[n] == ':' {
		if fam.faProtocolData != nil {
			return fam.faProtocolData, url[n+1:], nil
		}
		return nil, "", errors.New("data: protocol not registered")
	}

	// C: if(url[0] != ':') { /* No protocol specified, assume a plain
	//    file */ if(native_fap == NULL || (url0[0] != '/' &&
	//    native_fap->fap_stat(native_fap, url0, &fs, 0, NULL, 0))) {
	//    snprintf(errbuf, errsize, "File not found"); return NULL; } }
	// isWindowsDrivePath: upstream never shipped win32, so "X:\..." has
	// no C counterpart — treat it as a plain file like CanHandle does,
	// or "C" gets looked up as a protocol name.
	if len(url) < n+1 || url[n] != ':' || isWindowsDrivePath(url) {
		// C: native_fap == NULL || (url0[0] != '/' && fap_stat(...) != 0)
		if fam.nativeProto == nil {
			return nil, "", errors.New("File not found")
		}
		// A drive path is absolute — skip the stat gate C applies to
		// relative names, or makedirs of a not-yet-existing dir fails.
		if len(url) == 0 || (url[0] != '/' && !isWindowsDrivePath(url)) {
			if fam.nativeProto.Stat == nil {
				return nil, "", errors.New("File not found")
			}
			if _, serr := fam.nativeProto.Stat(fam.nativeProto, url, 0); serr != nil {
				return nil, "", errors.New("File not found")
			}
		}
		return fam.nativeProto, url, nil
	}

	// C: url += 3; (skip "://")
	filename := url[n+1:]

	// C: if(!strcmp("dataroot", buf)) { ... fa_resolve_proto(buf, p, errbuf, errsize); }
	// Handle dataroot protocol
	if protoName == "dataroot" {
		dataroot := fam.getDataRoot()
		newURL := dataroot + "/" + filename
		return fam.FAResolveProto(newURL)
	}

	// C: hts_mutex_lock(&fap_mutex); LIST_FOREACH(fap, &fileaccess_all_protocols, fap_link) { ... }
	fam.protocolMutex.Lock()
	defer fam.protocolMutex.Unlock()

	for _, proto := range fam.protocolList {
		// C: if(fap->fap_match_proto != NULL) { if(fap->fap_match_proto(buf)) continue; }
		//    else { if(strcmp(fap->fap_name, buf)) continue; }
		if proto.MatchProto != nil {
			if !proto.MatchProto(protoName) {
				continue
			}
		} else {
			if proto.Name != protoName {
				continue
			}
		}

		// C: fap_retain(fap);
		fapRetain(proto)

		// C: const char *fname = fap->fap_flags & FAP_INCLUDE_PROTO_IN_URL ? url0 : url;
		var fname string
		if proto.IncludeProto {
			fname = url
		} else {
			fname = filename
			// C: url += 3 already done above (skip "://")
			if len(fname) >= 2 && fname[0] == '/' && fname[1] == '/' {
				fname = fname[2:]
			}
		}

		// C: if(fap->fap_redirect != NULL) { rstr_t *newurl = fap->fap_redirect(fap, fname); ... }
		if proto.Redirect != nil {
			newURL := proto.Redirect(proto, fname)
			if newURL != "" {
				fapRelease(proto)
				return fam.FAResolveProto(newURL)
			}
		}

		return proto, fname, nil
	}

	return nil, "", fmt.Errorf("protocol %s not supported", protoName)
}

// FACanHandle checks if a URL can be handled by any registered protocol
// C: fa_can_handle (fileaccess.c:173-183) — resolves, releases, frees, returns 1/0.
func (fam *FileAccessManager) FACanHandle(url string) bool {
	proto, _, err := fam.FAResolveProto(url)
	if err != nil {
		return false
	}
	// C: fap_release(fap); free(filename);
	fapRelease(proto)
	return true
}

// FAAbsolutePath converts a relative path to an absolute path based on a reference location
func FAAbsolutePath(filename string, at string) string {
	if strings.Contains(filename, ":") || filename == "" || filename[0] == '/' {
		return filename
	}

	if strings.HasPrefix(filename, "./") {
		return filename
	}

	lastSlash := strings.LastIndex(at, "/")
	if lastSlash < 0 {
		return filename
	}

	return at[:lastSlash+1] + filename
}

// FANormalize normalizes a URL using protocol-specific normalization if available
// C: fa_normalize (fileaccess.c:208-221) — resolves, calls fap_normalize,
// releases protocol, frees filename.
func (fam *FileAccessManager) FANormalize(url string) (string, error) {
	proto, filename, err := fam.FAResolveProto(url)
	if err != nil {
		return "", err
	}

	// C: r = fap->fap_normalize ? fap->fap_normalize(fap, filename, dst, dstlen) : -1;
	if proto.Normalize == nil {
		fapRelease(proto)
		return "", errors.New("normalize not supported")
	}
	result, err := proto.Normalize(proto, filename)
	fapRelease(proto)
	if err != nil || result == "" {
		// C: r == -1 → caller keeps the original URL
		return "", errors.New("normalize failed")
	}
	return result, nil
}

// FAOpenEx opens a file with extended options and flags
// C: fa_open_ex (fileaccess.c:228-253) — resolves protocol, checks write
// support, calls fap_open, then fap_release(fap).
func FAOpenEx(fam *FileAccessManager, url string, flags int, extra *OpenExtra) (*Handle, error) {
	// C: if(!(flags & FA_WRITE)) {
	//     if(flags & (FA_BUFFERED_SMALL | FA_BUFFERED_BIG))
	//       return fa_buffered_open(url, errbuf, errsize, flags, foe); }
	if flags&FaWrite == 0 &&
		flags&(FaBufferedSmall|FaBufferedBig) != 0 {
		return FABufferedOpen(fam, url, flags, extra)
	}

	// C: if((filename = fa_resolve_proto(url, &fap, errbuf, errsize)) == NULL) return NULL;
	proto, _, err := fam.FAResolveProto(url)
	if err != nil {
		return nil, err
	}

	// C: if(flags & FA_WRITE && fap->fap_write == NULL) { snprintf(errbuf, errsize, "FS does not support writing"); fh = NULL; }
	// Check write support
	if flags&FaWrite != 0 {
		if proto != nil && !proto.Write {
			fapRelease(proto)
			return nil, errors.New("FS does not support writing")
		}
	}

	// C: fap_release(fap); — release the ref from fa_resolve_proto
	fapRelease(proto)

	return OpenEx(fam, url, extra, flags)
}

// FAOpenResolver opens a file with protocol resolver
// C: similar to fa_open_ex but used for resolver-based opening.
func FAOpenResolver(fam *FileAccessManager, url string, flags int, extra *OpenExtra) (*Handle, error) {
	proto, _, err := fam.FAResolveProto(url)
	if err != nil {
		return nil, err
	}
	// C: fap_release(fap); — release the ref from fa_resolve_proto
	fapRelease(proto)

	return OpenEx(fam, url, extra, flags)
}

// FAClose closes a file handle
func FAClose(fh *Handle) error {
	if fh == nil {
		return nil
	}
	return Close(fh)
}

// FACloseWithPark closes a file handle with optional parking for reuse
// C: fa_close_with_park (fileaccess.c:305) — park dispatches to
// fh->fh_proto->fap_park (only fa_protocol_buffered implements it).
func FACloseWithPark(fh *Handle, park bool) error {
	if fh == nil {
		return nil
	}

	if park && fh.fap != nil && fh.fap.Park != nil {
		fh.fap.Park(fh)
		return nil
	}

	return Close(fh)
}

// OpenSubURL opens a related URL (HLS/DASH segment, sidecar) through
// the same fileaccess manager — used by libav's io_open callback so
// nested FFmpeg opens go through fa (Go TLS for https) instead of
// FFmpeg's own URL layer.
func (h *Handle) OpenSubURL(url string) (libav.FileAccessReader, error) {
	fh, err := FAOpenEx(h.fam, url, 0, nil)
	if fh == nil {
		return nil, err
	}
	return fh, nil
}

// FARead reads data from a file handle
func FARead(fh *Handle, buf []byte) (int, error) {
	return Read(fh, buf)
}

// FADeadline sets a deadline for file operations
// C: fa_set_read_timeout (fileaccess.c) — dispatches on fh->fh_proto's
// fap_set_read_timeout (no re-resolve: buffered handles must hit
// fab_set_read_timeout, which propagates to the inner protocol).
func FADeadline(fh *Handle, deadline int) {
	if fh == nil {
		return
	}
	if fh.fap != nil && fh.fap.Deadline != nil {
		fh.fap.Deadline(fh, deadline)
	}
}

// FAPark parks a file handle for potential reuse
func FAPark(fh *Handle) {
	FACloseWithPark(fh, true)
}

// FAWrite writes data to a file handle
func FAWrite(fh *Handle, buf []byte) (int, error) {
	return Write(fh, buf)
}

// FASeek seeks to a position in a file
func FASeek(fh *Handle, offset int64, whence int) (int64, error) {
	return Seek(fh, offset, whence)
}

// FASeekLazy seeks without discarding cached/readahead data.
// C: fa_seek_lazy (fileaccess.c) — like fa_seek but keeps buffers when the
// target is inside the readahead window. Go's Handle has no readahead
// cache to preserve, so it delegates to Seek.
func FASeekLazy(fh *Handle, offset int64, whence int) (int64, error) {
	return Seek(fh, offset, whence)
}

// sliceReadSeekCloser — C: slice_t (fa_slice.c) — confines reads/seeks to
// [start, start+size) of the parent handle. Closing the slice closes the
// parent (C: slice_close → s_src->fh_proto->fap_close).
type sliceReadSeekCloser struct {
	parent *Handle
	start  int64
	size   int64
	pos    int64
}

func (s *sliceReadSeekCloser) Read(buf []byte) (int, error) {
	// C: if(s->s_fpos + size > s->s_size) size = s->s_size - s->s_fpos;
	//    if(size <= 0) return 0;
	size := len(buf)
	if s.pos+int64(size) > s.size {
		size = int(s.size - s.pos)
	}
	if size <= 0 {
		return 0, io.EOF
	}
	buf = buf[:size]
	p := s.pos + s.start
	// C: if(fa_seek(s->s_src, p, SEEK_SET) != p) return -1;
	v, err := s.parent.seeker.Seek(p, io.SeekStart)
	if err != nil || v != p {
		return -1, errors.New("slice: source seek failed")
	}
	n, err := s.parent.reader.Read(buf)
	if n >= 0 {
		s.pos += int64(n)
	}
	return n, err
}

func (s *sliceReadSeekCloser) Seek(offset int64, whence int) (int64, error) {
	var np int64
	switch whence {
	case io.SeekStart:
		np = offset
	case io.SeekCurrent:
		np = s.pos + offset
	case io.SeekEnd:
		np = s.size + offset
	default:
		return -1, errors.New("invalid whence")
	}
	if np < 0 {
		return -1, errors.New("negative position")
	}
	s.pos = np
	return np, nil
}

func (s *sliceReadSeekCloser) Close() error {
	return FAClose(s.parent)
}

// faProtocolSlice — C: fa_protocol_slice (fa_slice.c:127). Slice handles
// dispatch read/seek/close through the slice impl (Handle.reader/seeker);
// the gate carries the name for fh_proto parity.
var faProtocolSlice = &FAProtocol{Name: "slice"}

// FAScanDir scans a directory
func FAScanDir(fam *FileAccessManager, url string) (*Dir, error) {
	return ScanDir(fam, url)
}

// FAParent returns the parent directory of a URL
func FAParent(url string) (string, error) {
	return Parent(url)
}

// FAURLGetLastComponent returns the last component (filename) of a URL
func (fam *FileAccessManager) FAURLGetLastComponent(url string) string {
	return fam.URLGetLastComponent(url)
}

// FASanitizeFilename removes invalid characters from a filename
func FASanitizeFilename(filename string) string {
	return SanitizeFilename(filename)
}

// FAPathjoin — C: fa_pathjoin (fileaccess.c:1879).
// Joins a directory URL and a filename, stripping leading "./" from the
// second component and inserting "/" only when the first lacks it.
func FAPathjoin(p1, p2 string) string {
	for len(p2) >= 2 && p2[:2] == "./" {
		p2 = p2[2:]
	}
	sep := ""
	if len(p1) == 0 || p1[len(p1)-1] != '/' {
		sep = "/"
	}
	return p1 + sep + p2
}

// FAPathJoin joins path components with a forward slash
func FAPathJoin(p1, p2 string) string {
	return PathJoin(p1, p2)
}

// FAMakedirs creates directories recursively
func FAMakedirs(fam *FileAccessManager, url string) error {
	return Makedirs(fam, url)
}

// FAUnlink deletes a file
func FAUnlink(fam *FileAccessManager, url string) error {
	return Unlink(fam, url)
}

// FARmdir removes a directory
func FARmdir(fam *FileAccessManager, url string) error {
	return Rmdir(fam, url)
}

// FARename renames a file or directory
func FARename(fam *FileAccessManager, oldURL, newURL string) error {
	return Rename(fam, oldURL, newURL)
}

// FACopy copies a file from source to destination.
// C: fa_copy (fileaccess.c:1121-1128) — canonical int code mapped to error.
func FACopy(fam *FileAccessManager, to, from string) error {
	return faCopy(fam, to, from)
}

// FAFSInfo returns filesystem information for the given URL
func FAFSInfo(fam *FileAccessManager, url string) (*FileSystemInfo, error) {
	return FSInfo(fam, url)
}
