//go:build android

package fileaccess

// Canonical port of src/arch/android/android_fs.c — the four
// filesystem protocols (es, cache, persistent, file) that resolve
// scheme URLs to Android paths, with the runtime permission gate for
// external storage.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/czz/movian-go/internal/arch"
)

// AndroidFSProtocol — C: fa_protocol_es/cache/persistent/file
// (android_fs.c:371-444). One Go type parameterised by scheme name,
// matching C's four identical vtables that differ only in fap_name.
type AndroidFSProtocol struct {
	scheme string             // C: fap_name
	fam    *FileAccessManager // Go: owning fam (C: global fa registry)
}

// androidURLToPath — C: android_url_to_path (android_fs.c:52-82).
// url is the filename with the "<scheme>://" prefix already stripped.
// Returns (path, fa_err_code_t).
//
// The base dirs from JNI carry no trailing '/', and URLs after the
// scheme strip carry no leading one (es://DCIM → "DCIM"), so the join
// must supply the separator — otherwise es://DCIM resolves to
// /storage/emulated/0DCIM.
func (p *AndroidFSProtocol) urlToPath(url string, flags int) (string, int) {
	join := func(base string) string {
		if url == "" || url[0] != '/' {
			return base + "/" + url
		}
		return base + url
	}
	switch p.scheme {
	case "persistent":
		return join(arch.AndroidFSSettingsPath), 0
	case "cache":
		return join(arch.AndroidFSCachePath), 0
	case "file":
		return url, 0
	default:
		// C: external storage — runtime permission check
		// (android_fs.c:64-76).
		perm := "android.permission.READ_EXTERNAL_STORAGE"
		if flags&FaWrite != 0 {
			perm = "android.permission.WRITE_EXTERNAL_STORAGE"
		}
		if arch.AndroidGetPermission(perm, flags&FaNonInteractive == 0) == 0 {
			return "", FAP_PERMISSION_DENIED
		}
		return join(arch.AndroidFSSdcardPath), 0
	}
}

func (p *AndroidFSProtocol) Name() string { return p.scheme }

func (p *AndroidFSProtocol) CanHandle(url string) bool {
	return strings.HasPrefix(url, p.scheme+"://")
}

// stripScheme — fam-level Protocol methods receive the full URL; the
// C fap_* ops see it after fa_resolve_proto has removed "scheme://".
func (p *AndroidFSProtocol) stripScheme(url string) string {
	return strings.TrimPrefix(url, p.scheme+"://")
}

// Open — C: fs_open (android_fs.c:152-190).
func (p *AndroidFSProtocol) Open(url string, extra *OpenExtra) (*Handle, error) {
	flags := 0
	if extra != nil {
		flags = extra.Flags
	}
	path, err := p.urlToPath(p.stripScheme(url), flags)
	if err != 0 {
		return nil, fmt.Errorf("Access rejected by user")
	}

	var fd *os.File
	var oerr error
	if flags&FaWrite != 0 {
		openFlags := os.O_RDWR | os.O_CREATE
		if flags&FaAppend == 0 {
			openFlags |= os.O_TRUNC
		}
		fd, oerr = os.OpenFile(path, openFlags, 0666)
		if oerr == nil && flags&FaAppend != 0 {
			fd.Seek(0, 2) // SEEK_END
		}
	} else {
		fd, oerr = os.Open(path)
	}
	if oerr != nil {
		return nil, oerr
	}

	// C: fs_handle_t { fa_handle_t h; int fd; } — os.File carries fd.
	return NewHandle(p.fam, p, url, fd, fd, fd, func() int64 {
		st, err := fd.Stat()
		if err != nil {
			return -1
		}
		return st.Size()
	}), nil
}

// Stat — C: fs_stat (android_fs.c:240-262).
func (p *AndroidFSProtocol) Stat(url string) (*FileStat, error) {
	return p.stat(p.stripScheme(url), 0)
}

// stat — the fap_stat body shared by Protocol.Stat and the FAProtocol
// gate (C: fs_stat).
func (p *AndroidFSProtocol) stat(filename string, flags int) (*FileStat, error) {
	path, err := p.urlToPath(filename, flags)
	if err != 0 {
		if err == FAP_PERMISSION_DENIED {
			return nil, os.ErrPermission
		}
		return nil, fapCodeError(err)
	}
	fi, serr := os.Stat(path)
	if serr != nil {
		return nil, errors.New(serr.Error())
	}
	fs := &FileStat{Size: fi.Size(), MTime: fi.ModTime()}
	if fi.IsDir() {
		fs.Type = ContentDir
	} else {
		fs.Type = ContentFile
	}
	return fs, nil
}

// ScanDir — C: fs_scandir (android_fs.c:85-134).
func (p *AndroidFSProtocol) ScanDir(ctx context.Context, url string) (*Dir, error) {
	filename := p.stripScheme(url)
	path, err := p.urlToPath(filename, 0)
	if err != 0 {
		return nil, fmt.Errorf("access rejected")
	}
	entries, rerr := os.ReadDir(path)
	if rerr != nil {
		return nil, rerr
	}
	sep := ""
	if len(url) == 0 || url[len(url)-1] != '/' {
		sep = "/"
	}
	dir := DirAlloc()
	for _, e := range entries {
		name := e.Name()
		st, serr := os.Stat(path + "/" + name)
		if serr != nil {
			continue
		}
		var typ int
		if st.IsDir() {
			typ = ContentDir
		} else if st.Mode().IsRegular() {
			typ = ContentFile
		} else {
			continue
		}
		DirAdd(dir, fmt.Sprintf("%s://%s%s%s", p.scheme, filename, sep, name),
			name, typ)
	}
	return dir, nil
}

// makedir — C: fs_makedir (android_fs.c:311-331). Single-level
// mkdir(0770) with fa_err_code mapping.
func (p *AndroidFSProtocol) makedir(filename string) int {
	path, err := p.urlToPath(filename, FaWrite)
	if err != 0 {
		return err
	}
	if e := syscall.Mkdir(path, 0770); e != nil {
		switch e {
		case syscall.ENOENT:
			return FAP_NOENT
		case syscall.EPERM:
			return FAP_PERMISSION_DENIED
		case syscall.EEXIST:
			return FAP_EXIST
		default:
			return FAP_ERROR
		}
	}
	return 0
}

// populateAndroidFAProtocol — the FAProtocol gate ops for an android
// fs scheme: the fap_* vtable entries (stat/makedir/unlink/rmdir/
// rename/ftruncate/fsinfo) resolved through android_url_to_path.
func populateAndroidFAProtocol(fp *FAProtocol, p *AndroidFSProtocol) {
	fp.Write = true // C: fap_write != NULL
	fp.Stat = func(fap *FAProtocol, filename string, flags int) (*FileStat, error) {
		return p.stat(filename, flags)
	}
	fp.Makedirs = func(fap *FAProtocol, filename string) error {
		switch p.makedir(filename) {
		case 0:
			return nil
		case FAP_NOENT:
			return os.ErrNotExist
		case FAP_PERMISSION_DENIED:
			return os.ErrPermission
		case FAP_EXIST:
			return os.ErrExist
		default:
			return errors.New("makedir failed")
		}
	}
	// C: fs_unlink (android_fs.c:288-304)
	fp.Unlink = func(fap *FAProtocol, filename string) error {
		path, err := p.urlToPath(filename, FaWrite)
		if err != 0 {
			return fmt.Errorf("access rejected")
		}
		return os.Remove(path)
	}
	// C: fs_rmdir (android_fs.c:267-282)
	fp.Rmdir = func(fap *FAProtocol, filename string) error {
		path, err := p.urlToPath(filename, FaWrite)
		if err != 0 {
			return fmt.Errorf("access rejected")
		}
		return syscall.Rmdir(path)
	}
	// C: fs_rename (android_fs.c:337-357)
	fp.Rename = func(fap *FAProtocol, oldFilename, newFilename string) error {
		oldPath, err := p.urlToPath(oldFilename, FaWrite)
		if err != 0 {
			return fmt.Errorf("access rejected")
		}
		newPath, err2 := p.urlToPath(newFilename, FaWrite)
		if err2 != 0 {
			return fmt.Errorf("access rejected")
		}
		return os.Rename(oldPath, newPath)
	}
	// C: fs_ftruncate (android_fs.c:361-368) — os.File handles.
	fp.Ftruncate = func(fh *Handle, size int64) error {
		if f, ok := fh.Reader().(*os.File); ok {
			if err := f.Truncate(size); err == nil {
				return nil
			}
		}
		return fmt.Errorf("ftruncate failed")
	}
	// C: no fsinfo on android_fs.c — statfs is not wired there.
}

// registerAndroidProtocols — C: FAP_REGISTER(es) + FAP_REGISTER(cache)
// + FAP_REGISTER(persistent) + FAP_REGISTER(file)
// (android_fs.c:387,406,425,444). Called from RegisterProtocols.
// The fap gates are populated later by populateFSGate, after
// registerGlobalFAProtocol has created them.
func registerAndroidProtocols(fam *FileAccessManager) {
	for _, scheme := range []string{"es", "cache", "persistent", "file"} {
		fam.RegisterProtocol(&AndroidFSProtocol{scheme: scheme, fam: fam})
	}
}

// populateFSGate — android: the es/cache/persistent/file fap gates
// carry the android_fs.c ops (permission check + path mapping through
// android_url_to_path), not the fa_fs.c filesystem ops.
func populateFSGate(fam *FileAccessManager) {
	for _, proto := range fam.protocols {
		p, ok := proto.(*AndroidFSProtocol)
		if !ok {
			continue
		}
		if fp := fam.lookupFAProtocol(p.scheme); fp != nil {
			populateAndroidFAProtocol(fp, p)
		}
	}
}
