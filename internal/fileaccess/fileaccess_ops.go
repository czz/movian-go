package fileaccess

// Canonical port of the remaining fileaccess.c operations:
// fa_get_parts, fa_findfile, fa_notify_start/stop, fa_rscan,
// fa_unlink_recursive, fa_copy_from_fh, fa_makedir_p/fa_makedir,
// fa_set_xattr/fa_get_xattr, fa_check_url, fa_get_title,
// fa_read_to_htsbuf, fa_err_code_str and the full tagged fa_load.

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/czz/movian-go/internal/blobcache"
	"github.com/czz/movian-go/internal/misc"
	"github.com/czz/movian-go/internal/nls"
)

// fa_err_code_t (fileaccess.h:176-183)
const (
	FAP_OK                = 0
	FAP_ERROR             = -1
	FAP_NEED_AUTH         = -2
	FAP_NOT_SUPPORTED     = -3
	FAP_PERMISSION_DENIED = -4
	FAP_NOENT             = -5
	FAP_EXIST             = -6
)

// BACKEND_PROBE_* (backend.h:43-47) — fa_check_url result codes.
// Declared here because backend/core imports this package (not the
// other way around).
const (
	BackendProbeOK        = 0
	BackendProbeAuth      = 1
	BackendProbeNoHandler = 2
	BackendProbeFail      = 3
)

// fa_notify_op_t (fileaccess.h:165-169)
const (
	FANotifyAdd = iota
	FANotifyDel
	FANotifyDirChange
)

// FANotifyFunc — C: fa_notify change callback
// (fileaccess.h:259-262): void (*)(void *opaque, fa_notify_op_t op,
// const char *filename, const char *url, int type).
type FANotifyFunc func(opaque any, op int, filename, url string, typ int)

// FALoadCB — C: fa_load_cb_t (fileaccess.h:38):
// void (*)(void *opaque, int loaded, int total).
type FALoadCB func(opaque any, loaded, total int)

// Cache-control sentinels — C: BYPASS_CACHE ((int*)-1) and
// DISABLE_CACHE ((int*)-2) in main.h:54-55. Go encodes them as the
// pointed-to value.
const (
	BypassCacheVal  = -1 // C: BYPASS_CACHE
	DisableCacheVal = -2 // C: DISABLE_CACHE
)

// FA_CACHE_INFO_* (fileaccess.h:329-331)
const (
	FACacheInfoFromCache            = 1
	FACacheInfoFromCacheNotModified = 2
	FACacheInfoExpiredFromCache     = 3
)

// faLoadCacheStash — C: FA_LOAD_CACHE_STASH (fileaccess.c:1491)
const faLoadCacheStash = "fa-load"

const (
	messagePopupOK     = 0x1000 // C: MESSAGE_POPUP_OK
	messagePopupCancel = 0x2000 // C: MESSAGE_POPUP_CANCEL
)

// FAErrCodeStr — C: fa_err_code_str (fileaccess.c:2112-2127)
func FAErrCodeStr(errcode int) string {
	switch errcode {
	case FAP_OK:
		return "OK"
	case FAP_ERROR:
		return "Error"
	case FAP_NEED_AUTH:
		return "Authentication needed"
	case FAP_NOT_SUPPORTED:
		return "Operation not supported"
	case FAP_PERMISSION_DENIED:
		return "Permission denied"
	case FAP_NOENT:
		return "No such entry"
	case FAP_EXIST:
		return "Item already exist"
	default:
		return "Unmapped errorcode"
	}
}

// fapError carries a C fa_err_code_t (and the errbuf message) through a
// Go error value so fapErrCode recovers the exact code at the boundary.
type fapError struct {
	code int
	msg  string
}

func (e fapError) Error() string {
	if e.msg != "" {
		return e.msg
	}
	return FAErrCodeStr(e.code)
}

// fapCodeError wraps a fa_err_code_t as an error.
func fapCodeError(code int) error { return fapError{code: code} }

// fapErrCode maps a Go error to the C fa_err_code_t domain so protocol
// methods that return error can feed canonical code paths.
func fapErrCode(err error) int {
	if err == nil {
		return FAP_OK
	}
	if fe, ok := errors.AsType[fapError](err); ok {
		return fe.code
	}
	switch {
	case errors.Is(err, ErrNotSupported):
		return FAP_NOT_SUPPORTED
	case errors.Is(err, ErrAuthRequired):
		return FAP_NEED_AUTH
	case errors.Is(err, os.ErrNotExist):
		return FAP_NOENT
	case errors.Is(err, os.ErrPermission):
		return FAP_PERMISSION_DENIED
	case errors.Is(err, os.ErrExist):
		return FAP_EXIST
	default:
		return FAP_ERROR
	}
}

// fapStat — C: fap->fap_stat. Prefers the protocol's own Stat hook
// (resolved filename); falls back to the fam-level Stat on the full URL
// for protocols registered only in the fam Protocol list.
func fapStat(fam *FileAccessManager, fap *FAProtocol, url, filename string,
	flags int) (*FileStat, error) {
	if fap != nil && fap.Stat != nil {
		return fap.Stat(fap, filename, flags)
	}
	if fam != nil {
		return StatEx(fam, url, flags)
	}
	return nil, ErrNotSupported
}

// FAScandir — C: fa_scandir(url, errbuf, flags) → fa_dir_t*.
// Exported counterpart of faScandir for package-external callers.
func FAScandir(fam *FileAccessManager, url string, flags int) (*Dir, error) {
	return faScandir(fam, url, flags)
}

// faScandir — C: fa_scandir(url, errbuf, flags) → fa_dir_t*.
func faScandir(fam *FileAccessManager, url string, flags int) (*Dir, error) {
	return ScanDirEx(fam, url, flags)
}

// FAGetParts — C: fa_get_parts (fileaccess.c:495-517). Returns the
// directory filled by fap_get_parts (archive member listing).
func (fam *FileAccessManager) FAGetParts(url string) (*Dir, error) {
	fap, filename, err := fam.FAResolveProto(url)
	if err != nil {
		return nil, err
	}
	defer fapRelease(fap)

	if fap.GetParts == nil {
		return nil, errors.New("Protocol does not implement part scanning")
	}
	fd := &Dir{Entries: make(map[string]*DirEntry)}
	if err := fap.GetParts(fap, fd, filename); err != nil {
		fd.Free()
		return nil, err
	}
	return fd, nil
}

// FAFindfile — C: fa_findfile (fileaccess.c:524-546). Case-insensitive
// lookup of file in dir path. Returns (fullpath, 0) on hit,
// ("", -1) on miss, ("", -2) if scandir failed.
func FAFindfile(fam *FileAccessManager, path, file string) (string, int) {
	fd, _ := faScandir(fam, path, 0)
	if fd == nil {
		return "", -2
	}
	for _, fde := range fd.Entries {
		if strings.EqualFold(fde.Filename, file) {
			sep := ""
			if len(path) == 0 || path[len(path)-1] != '/' {
				sep = "/"
			}
			full := path + sep + fde.Filename
			fd.Free()
			return full, 0
		}
	}
	fd.Free()
	return "", -1
}

// FANotifyStart — C: fa_notify_start (fileaccess.c:582-605). Returns a
// notify handle or nil if the protocol has no fap_notify_start.
func (fam *FileAccessManager) FANotifyStart(url string, opaque any, change FANotifyFunc) *Handle {
	fap, filename, err := fam.FAResolveProto(url)
	if err != nil {
		return nil
	}
	defer fapRelease(fap)

	if fap.NotifyStart == nil {
		return nil
	}
	return fap.NotifyStart(fap, filename, opaque, change)
}

// FANotifyStop — C: fa_notify_stop (fileaccess.c:612-615)
func FANotifyStop(fh *Handle) {
	if fh != nil && fh.fap != nil && fh.fap.NotifyStop != nil {
		fh.fap.NotifyStop(fh)
	}
}

// FARscan — C: fa_rscan (fileaccess.c:794-814). Recursively invokes fn
// on every non-directory entry, then once on url itself as CONTENT_DIR.
func FARscan(fam *FileAccessManager, url string,
	fn func(aux any, fde *DirEntry), aux any) {
	fd, _ := faScandir(fam, url, 0)
	if fd == nil {
		return
	}
	for _, fde := range fd.Entries {
		if fde.Type == ContentDir {
			FARscan(fam, fde.URL, fn, aux)
		} else {
			fn(aux, fde)
		}
	}
	fd.Free()

	// C: fde_create(NULL, url, url, CONTENT_DIR); fn(aux, fde); free
	fde := &DirEntry{Filename: url, URL: url, Type: ContentDir}
	fn(aux, fde)
}

// delscanItem — C: delscan_item_t (fileaccess.c:819-825)
type delscanItem struct {
	url string
	typ int
}

// C: delitem_add / delitem_addr (fileaccess.c:830-852)
func delitemAdd(diq *[]*delscanItem, url string, typ int) {
	*diq = append(*diq, &delscanItem{url: url, typ: typ})
}

func delitemAddFde(aux any, fde *DirEntry) {
	diq := aux.(*[]*delscanItem)
	delitemAdd(diq, fde.URL, fde.Type)
}

// unlinkItems — C: unlink_items (fileaccess.c:859-905). Runs
// fap_rmdir / fap_unlink on each queued URL in order.
func unlinkItems(fam *FileAccessManager, diq []*delscanItem) {
	for _, di := range diq {
		fap, filename, err := fam.FAResolveProto(di.url)
		if err != nil {
			continue
		}
		var r error
		if di.typ == ContentDir {
			if fap.Rmdir != nil {
				r = fap.Rmdir(fap, filename)
			} else {
				r = Rmdir(fam, di.url)
			}
		} else {
			if fap.Unlink != nil {
				r = fap.Unlink(fap, filename)
			} else {
				r = Unlink(fam, di.url)
			}
		}
		_ = r // C: TRACE(TRACE_ERROR, ...) on failure
		fapRelease(fap)
	}
}

// verifyDelete — C: verify_delete (fileaccess.c:921-960). Builds the
// confirmation text via _pl/_ and asks via message_popup.
func verifyDelete(fam *FileAccessManager, diq []*delscanItem) int {
	var files, dirs, parts int
	for _, di := range diq {
		switch di.typ {
		case ContentDir:
			dirs++
		case ContentFile:
			files++
		case 1000: // C: archive part marker
			parts++
		}
	}

	ftxt := nls.GetRStringP("%d files", "%d file", files)
	dtxt := nls.GetRStringP("%d directories", "%d directory", dirs)
	ptxt := nls.GetRStringP("%d archive parts", "%d archive part", parts)
	del := nls.GetRString("Are you sure you want to delete:")

	var sb strings.Builder
	sb.WriteString(del)
	sb.WriteString("\n")
	if files != 0 {
		fmt.Fprintf(&sb, ftxt+"\n", files)
	}
	if dirs != 0 {
		fmt.Fprintf(&sb, dtxt+"\n", dirs)
	}
	if parts != 0 {
		fmt.Fprintf(&sb, ptxt+"\n", parts)
	}

	if fam.messagePopup == nil {
		return 0
	}
	return fam.messagePopup(sb.String(),
		messagePopupCancel|messagePopupOK, nil)
}

// FAUnlinkRecursive — C: fa_unlink_recursive (fileaccess.c:966-1064).
// Deletes a file (or .rar part set) or a directory tree. verify!=0 asks
// the user first. Returns the number of items deleted, or -1 + error.
func FAUnlinkRecursive(fam *FileAccessManager, url string,
	verify int) (int, error) {
	fap, filename, err := fam.FAResolveProto(url)
	if err != nil {
		return -1, err
	}
	defer fapRelease(fap)

	var diq []*delscanItem
	fail := func(msg string) (int, error) {
		return -1, errors.New(msg)
	}

	st, serr := fapStat(fam, fap, url, filename, 0)
	if serr != nil {
		return -1, serr
	}

	if st.Type == ContentFile {
		if fap.Unlink == nil {
			return fail("Deleting not supported for this file system")
		}

		// C: rar archives delete all their parts (type marker 1000)
		if idx := strings.LastIndex(url, "."); idx >= 0 &&
			strings.EqualFold(url[idx:], ".rar") {
			fd, gperr := fam.FAGetParts("rar://" + url)
			if gperr != nil {
				return -1, gperr
			}
			for _, fde := range fd.Entries {
				delitemAdd(&diq, fde.URL, 1000)
			}
			fd.Free()
		} else {
			delitemAdd(&diq, url, ContentFile)
		}
	} else if st.Type == ContentDir {
		if fap.Unlink == nil || fap.Rmdir == nil {
			return fail("Deleting not supported for this file system")
		}
		FARscan(fam, url, delitemAddFde, &diq)
	} else {
		return fail("Can't delete this type")
	}

	if verify != 0 && verifyDelete(fam, diq) != messagePopupOK {
		return fail("Canceled by user")
	}

	unlinkItems(fam, diq)
	return len(diq), nil // C: free_items count
}

// FACopyFromFh — C: fa_copy_from_fh (fileaccess.c:1071-1114). Streams
// src into a newly created `to`, creating parent dirs. On failure the
// partial destination is unlinked via its own fap_unlink.
func FACopyFromFh(fam *FileAccessManager, to string, src *Handle) error {
	tmp := make([]byte, 8192)

	parent, perr := FAParent(to)
	if perr != nil {
		FAClose(src)
		return errors.New("Unable to figure out parent dir for dest")
	}

	if err := Makedirs(fam, parent); err != nil {
		FAClose(src)
		return err
	}

	dst, err := FAOpenEx(fam, to, FaWrite, nil)
	if err != nil || dst == nil {
		FAClose(src)
		if err != nil {
			return err
		}
		return errors.New("open failed")
	}

	r := 0
	var cerr error
	for {
		r = faRead(src, tmp)
		if r <= 0 {
			break
		}
		w, werr := FAWrite(dst, tmp[:r])
		if werr != nil || w != r {
			cerr = errors.New("Write error")
			r = -2
			break
		}
	}
	if r == -1 {
		cerr = errors.New("Read error")
	}

	FAClose(dst)
	FAClose(src)

	if r < 0 {
		// C: if(r < 0 && dfap->fap_unlink != NULL) fap_unlink(dfap, to, ...)
		if dfap, dfilename, derr := fam.FAResolveProto(to); derr == nil {
			if dfap.Unlink != nil {
				dfap.Unlink(dfap, dfilename)
			} else {
				Unlink(fam, to)
			}
			fapRelease(dfap)
		}
		if cerr != nil {
			return cerr
		}
		return fmt.Errorf("copy failed: %d", r)
	}
	return nil
}

// faCopy — C: fa_copy (fileaccess.c:1121-1128)
func faCopy(fam *FileAccessManager, to, from string) error {
	src, err := FAOpenEx(fam, from, 0, nil)
	if src == nil {
		return err
	}
	return FACopyFromFh(fam, to, src)
}

// fapMakedirOnce — single fap_makedir attempt (C: fap->fap_makedir).
// Falls back to the fam-level leaf mkdir for fam-only protocols.
func fapMakedirOnce(fam *FileAccessManager, fap *FAProtocol, url, path string) int {
	if fap != nil && fap.Makedirs != nil {
		return fapErrCode(fap.Makedirs(fap, path))
	}
	if fam != nil {
		if p := fam.FindProtocol(url); p != nil {
			if fsProto, ok := p.(*FSProtocol); ok {
				return fapErrCode(fsProto.Makedirs(url))
			}
		}
	}
	return FAP_NOT_SUPPORTED
}

// faMakedirP — C: fa_makedir_p (fileaccess.c:1135-1176). Creates a dir;
// on FAP_NOENT recurses to the parent first.
func faMakedirP(fam *FileAccessManager, fap *FAProtocol, url, path string) int {
	st, _ := fapStat(fam, fap, url, path, 0)
	if st != nil && st.Type == ContentDir {
		return FAP_OK // Dir already there
	}

	r := fapMakedirOnce(fam, fap, url, path)
	if r == FAP_OK {
		return FAP_OK
	}
	if r == FAP_NOENT {
		// Strip last path component and create the parent first
		// (C: alloca + strip to last '/' in fap_makedir_p)
		i := len(path) - 1
		for ; i >= 0; i-- {
			if path[i] == '/' {
				break
			}
		}
		// C checks !i (i==0); i<0 means no '/' at all — C would index
		// path[-1] (UB upstream never hit), Go would panic. NOENT both.
		if i <= 0 {
			return FAP_NOENT
		}
		parent := path[:i]
		parentURL := url[:len(url)-len(path)] + parent
		if r = faMakedirP(fam, fap, parentURL, parent); r != FAP_OK {
			return r
		}
		r = fapMakedirOnce(fam, fap, url, path) // Try again
	}
	return r
}

// FAMakedir — C: fa_makedir (fileaccess.c:1201-1219). Returns
// fa_err_code_t.
func FAMakedir(fam *FileAccessManager, url string) int {
	fap, filename, err := fam.FAResolveProto(url)
	if err != nil {
		return FAP_NOENT
	}
	defer fapRelease(fap)

	if fap.Makedirs == nil && fam == nil {
		return FAP_NOT_SUPPORTED
	}
	return fapMakedirOnce(fam, fap, url, filename)
}

// FASetXattr — C: fa_set_xattr (fileaccess.c:1325-1343)
func (fam *FileAccessManager) FASetXattr(url, name string, data []byte) int {
	fap, filename, err := fam.FAResolveProto(url)
	if err != nil {
		return FAP_NOT_SUPPORTED
	}
	defer fapRelease(fap)
	if fap.SetXattr == nil {
		return FAP_NOT_SUPPORTED
	}
	return fap.SetXattr(fap, filename, name, data)
}

// FAGetXattr — C: fa_get_xattr (fileaccess.c:1349-1367)
func (fam *FileAccessManager) FAGetXattr(url, name string) ([]byte, int) {
	fap, filename, err := fam.FAResolveProto(url)
	if err != nil {
		return nil, FAP_NOT_SUPPORTED
	}
	defer fapRelease(fap)
	if fap.GetXattr == nil {
		return nil, FAP_NOT_SUPPORTED
	}
	return fap.GetXattr(fap, filename, name)
}

// FACheckURL — C: fa_check_url (fileaccess.c:1850-1876). Returns a
// BACKEND_PROBE_* code; the error carries the C errbuf message.
func FACheckURL(fam *FileAccessManager, url string,
	timeoutMs int) (int, error) {
	fap, filename, err := fam.FAResolveProto(url)
	if err != nil {
		return BackendProbeNoHandler, err
	}
	defer fapRelease(fap)

	_, serr := fapStat(fam, fap, url, filename, FaNonInteractive)
	if serr == nil {
		return BackendProbeOK, nil
	}
	if fapErrCode(serr) == FAP_NEED_AUTH {
		return BackendProbeAuth, errors.New("Authentication required")
	}
	return BackendProbeFail, serr
}

// FAGetTitle — C: fa_get_title (fileaccess.c:1967-1994). Protocol title
// via fap_title, else last URL component.
func (fam *FileAccessManager) FAGetTitle(url string) string {
	fap, filename, err := fam.FAResolveProto(url)
	if err != nil {
		fap = nil
		filename = ""
	}
	defer fapRelease(fap)

	if filename != "" && fap != nil && fap.Title != nil {
		if t := fap.Title(fap, filename); t != "" {
			return t
		}
	}
	return faURLGetLastComponentI(fap, filename, url)
}

// FAReadToHtsbuf — C: fa_read_to_htsbuf (fileaccess.c:2061-2081).
// Streams fh into hq in 4096-byte prealloc chunks. Returns 0 on clean
// EOF, -1 on error or maxbytes exhausted mid-file.
func FAReadToHtsbuf(hq *misc.HtsbufQueue, fh *Handle, maxbytes int) int {
	const chunksize = 4096
	for maxbytes > 0 {
		buf := make([]byte, chunksize)
		l := faRead(fh, buf)
		if l < 0 {
			return -1
		}
		if l > 0 {
			hq.AppendPrealloc(buf[:l])
		}
		if l != chunksize {
			return 0
		}
		maxbytes -= l
	}
	return -1
}

// FALoadArgs — the tagged-varargs of fa_load (fileaccess.h:291-314)
// as an option struct. Fields correspond to FA_LOAD_TAG_*.
type FALoadArgs struct {
	CacheControl *int        // FA_LOAD_CACHE_CONTROL (*-1=BYPASS, *-2=DISABLE)
	Flags        int         // FA_LOAD_FLAGS
	ProgressCB   FALoadCB    // FA_LOAD_PROGRESS_CALLBACK
	ProgressArg  any         //   cb opaque
	Cancellable  any         // FA_LOAD_CANCELLABLE (*misc.Cancellable)
	QueryArgs    [][2]string // FA_LOAD_QUERY_ARG / _ARGVEC merged
	MinExpire    int         // FA_LOAD_MIN_EXPIRE
	ReqHeaders   any         // FA_LOAD_REQUEST_HEADERS (http_header_list)
	RespHeaders  any         // FA_LOAD_RESPONSE_HEADERS (filled by proto)
	Location     *string     // FA_LOAD_LOCATION (final URL after redirects)
	CacheInfo    *int        // FA_LOAD_CACHE_INFO (FA_CACHE_INFO_*)
	ProtocolCode *int        // FA_LOAD_PROTOCOL_CODE (HTTP status)
	NoFallback   bool        // FA_LOAD_NO_FALLBACK
	CacheEvict   bool        // FA_LOAD_CACHE_EVICT
}

// faLoad — C: fa_load (fileaccess.c:1498-1783). Full blobcache +
// fap_load path with the open/fsize/read fallback.
func faLoad(fam *FileAccessManager, url string, a *FALoadArgs) (*Buffer, error) {
	// C: fa_load always ran against the implicit global context — a nil
	// fam is a wiring error (the global always exists in C).
	if fam == nil {
		return nil, errors.New("faLoad: nil file access manager")
	}
	if a == nil {
		a = &FALoadArgs{}
	}
	cacheControl := a.CacheControl
	bypass := cacheControl != nil && *cacheControl == BypassCacheVal
	disable := cacheControl != nil && *cacheControl == DisableCacheVal

	if a.ProtocolCode != nil {
		*a.ProtocolCode = 0
	}
	protocolCode := 0
	if a.ProtocolCode == nil {
		a.ProtocolCode = &protocolCode
	}

	// FA_LOAD_QUERY_ARG* — rebuild the URL with escaped query args
	if len(a.QueryArgs) > 0 {
		var q misc.HtsbufQueue
		q.Append([]byte(url))
		prefix := byte('?')
		if strings.Contains(url, "?") {
			prefix = '&'
		}
		for _, kv := range a.QueryArgs {
			// C: SIMPLEQ_FOREACH(la, &qargs) — every queued pair is
			// emitted (FA_LOAD_TAG_QUERY_ARG queues only non-NULL
			// key/value; Go strings can't be NULL so presence in
			// the slice is the equivalent).
			q.Append([]byte{prefix})
			q.AppendAndEscapeURL(kv[0])
			q.Append([]byte("="))
			q.AppendAndEscapeURL(kv[1])
			prefix = '&'
		}
		url = q.String()
	}

	bc := fam.BlobCache()

	if a.CacheEvict {
		if bc != nil {
			bc.Evict(url, faLoadCacheStash)
		}
		return nil, nil
	}

	if a.Location != nil {
		*a.Location = url
	}

	fap, filename, err := fam.FAResolveProto(url)
	if err != nil {
		return nil, err
	}
	defer fapRelease(fap)

	if fap.Load != nil {
		var buf *blobcache.Buf
		var etag string
		var mtime time.Time
		var isExpired bool

		if !bypass && !disable && bc != nil {
			buf = bc.Get(url, faLoadCacheStash, 1, &isExpired, &etag, &mtime)
			if buf != nil {
				if cacheControl != nil {
					// ONLY_CACHED: caller handles expiry
					*cacheControl = misc.BoolToInt(isExpired)
					if a.CacheInfo != nil {
						*a.CacheInfo = FACacheInfoExpiredFromCache
					}
					return bufToBuffer(buf), nil
				}
				if !isExpired {
					if a.CacheInfo != nil {
						*a.CacheInfo = FACacheInfoFromCache
					}
					return bufToBuffer(buf), nil
				}
			} else if cacheControl != nil {
				return nil, errors.New("Not cached")
			}
		}

		if bypass && bc != nil {
			bc.GetMeta(url, faLoadCacheStash, &etag, &mtime)
		}

		maxAge := 0
		data2, lerr := fap.Load(fap, filename, &etag, &mtime,
			&maxAge, a.Flags, a.ProgressCB, a.ProgressArg, a.Cancellable,
			a.ReqHeaders, a.RespHeaders, a.Location, a.ProtocolCode)

		if errors.Is(lerr, ErrLoadNotModified) {
			// C: data2 == NOT_MODIFIED
			if bypass {
				return nil, ErrLoadNotModified
			}
			if a.CacheInfo != nil {
				*a.CacheInfo = FACacheInfoFromCacheNotModified
			}
			return bufToBuffer(buf), nil
		}

		if data2 == nil && buf != nil {
			// Reload failed — serve the stale cached entry unless the
			// failure was a definitive 4xx (C: fileaccess.c:1707-1720)
			if *a.ProtocolCode == 401 || *a.ProtocolCode == 403 ||
				*a.ProtocolCode == 400 {
				return nil, lerr
			}
			if a.CacheInfo != nil {
				*a.CacheInfo = FACacheInfoExpiredFromCache
			}
			return bufToBuffer(buf), nil
		}
		if data2 == nil {
			return nil, lerr
		}

		// C: max_age = MAX(min_expire, max_age)
		if a.MinExpire > maxAge {
			maxAge = a.MinExpire
		}

		noChange := 0
		if bc != nil && !disable && *a.ProtocolCode < 300 &&
			(cacheControl != nil || maxAge != 0 || etag != "" || !mtime.IsZero()) {
			bcFlags := 0
			if a.Flags&FaImportant != 0 {
				bcFlags |= blobcache.ImportantItem
			}
			noChange = bc.Put(url, faLoadCacheStash,
				&blobcache.Buf{Ptr: data2.Data, Size: data2.Size,
					ContentType: data2.ContentType},
				maxAge, etag, mtime, bcFlags)
		}

		if bypass && noChange != 0 {
			return nil, ErrLoadNotModified
		}
		return data2, nil
	}

	if a.NoFallback {
		return nil, ErrLoadNoMethod
	}

	// C: fallback — fap_open + fa_fsize + fa_read
	fh, err := FAOpenEx(fam, url, 0, nil)
	if fh == nil {
		return nil, err
	}

	size, serr := FSize(fh)
	if serr != nil || size == -1 {
		FAClose(fh)
		return nil, errors.New("Unable to load file from non-seekable fs")
	}

	data := make([]byte, size+1)
	r := faRead(fh, data[:size])
	FAClose(fh)

	if int64(r) != size {
		return nil, errors.New("Short read")
	}
	return &Buffer{Data: data[:size], Size: int(size)}, nil
}

func bufToBuffer(b *blobcache.Buf) *Buffer {
	if b == nil {
		return nil
	}
	return &Buffer{Data: b.Ptr[:b.Size], Size: b.Size,
		ContentType: b.ContentType}
}
