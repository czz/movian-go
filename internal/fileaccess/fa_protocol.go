package fileaccess

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"sync/atomic"
)

// fapRetain increments the protocol reference count.
// C: fap_retain (fileaccess.c:81) — atomic_inc(&fap->fap_refcount)
func fapRetain(fap *FAProtocol) {
	if fap == nil {
		return
	}
	atomic.AddInt32(&fap.RefCount, 1)
}

// fapRelease decrements the protocol reference count.
// C: fap_release (fileaccess.c:65-74) — if atomic_dec returns 0, call fap_fini.
func fapRelease(fap *FAProtocol) {
	if fap == nil {
		return
	}
	if fap.Fini == nil {
		return
	}
	if atomic.AddInt32(&fap.RefCount, -1) != 0 {
		return
	}
	fap.Fini(fap)
}

// FAProtocolRelease is the exported form of fap_release for callers outside
// this package (e.g. ecmascript fap) that hold a resolve-time reference.
// C: fap_release (fileaccess.c:65-74)
func FAProtocolRelease(fap *FAProtocol) {
	fapRelease(fap)
}

// FAReference holds a reference on a URL to keep its filesystem mounted.
// C: fa_reference (fileaccess.c:549-563)
func (fam *FileAccessManager) FAReference(url string) *Handle {
	fap, filename, err := fam.FAResolveProto(url)
	if err != nil {
		return nil
	}
	var fh *Handle
	if fap.Reference != nil {
		fh = fap.Reference(fap, filename)
		if fh != nil {
			fh.fap = fap
		}
	}
	fapRelease(fap)
	return fh
}

// FAUnreference releases a reference held by FAReference.
// C: fa_unreference (fileaccess.c:569-574)
func FAUnreference(fh *Handle) {
	if fh != nil && fh.fap != nil && fh.fap.Unreference != nil {
		fh.fap.Unreference(fh)
	}
}

// RegisterFAProtocol registers a file access protocol for URL resolution
func (fam *FileAccessManager) RegisterFAProtocol(proto *FAProtocol) {
	fam.protocolMutex.Lock()
	defer fam.protocolMutex.Unlock()

	proto.fam = fam
	fam.protocolList = append(fam.protocolList, proto)
}

// RegisterDynamicFAProtocol — C: fileaccess_register_dynamic
// (fileaccess.c:1404-1412). Refcount=1, inserted at HEAD.
func (fam *FileAccessManager) RegisterDynamicFAProtocol(fap *FAProtocol) {
	fap.fam = fam
	atomic.StoreInt32(&fap.RefCount, 1)
	fam.protocolMutex.Lock()
	defer fam.protocolMutex.Unlock()

	fam.protocolList = slices.Insert(fam.protocolList, 0, fap)
}

// UnregisterDynamicFAProtocol — C: fileaccess_unregister_dynamic
// (fileaccess.c:1418-1425). Removes from the list and releases the
// registration reference (fires fap_fini when the count hits zero).
func (fam *FileAccessManager) UnregisterDynamicFAProtocol(fap *FAProtocol) {
	fam.protocolMutex.Lock()
	for i, p := range fam.protocolList {
		if p == fap {
			fam.protocolList = slices.Delete(fam.protocolList, i, i+1)
			break
		}
	}
	fam.protocolMutex.Unlock()
	fapRelease(fap)
}

// lookupGlobalFAProtocol finds a registered protocol by name.
func (fam *FileAccessManager) lookupFAProtocol(name string) *FAProtocol {
	if fam == nil { // unwired — C would consult the global list
		return nil
	}
	fam.protocolMutex.Lock()
	defer fam.protocolMutex.Unlock()
	for _, p := range fam.protocolList {
		if p.Name == name {
			return p
		}
	}
	return nil
}

// populateFSFAProtocol fills the "file" FAProtocol with the real fs ops
// (C: fa_protocol_fs in fa_fs.c — fap_stat/fap_unlink/fap_rmdir/
// fap_rename/fap_makedir/fap_fsinfo).
func (fam *FileAccessManager) populateFSFAProtocol(fp *FAProtocol) {
	fs := &FSProtocol{}
	fp.Write = true // C: fap_write != NULL
	// C: fs_stat (fa_fs.c:376) — split-piece fallback included.
	fp.Stat = func(fap *FAProtocol, filename string, flags int) (*FileStat, error) {
		return fsStat(filename)
	}
	// C: fs_makedir (fa_fs.c:460) — mkdir(0770) + fa_err_code mapping.
	// Sentinel errors let fapErrCode recover the canonical code.
	fp.Makedirs = func(fap *FAProtocol, filename string) error {
		switch fsMakedir(filename) {
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
	// C: fs_unlink (fa_fs.c:429) — split-piece fallback included.
	fp.Unlink = func(fap *FAProtocol, filename string) error {
		return fsUnlink(filename)
	}
	fp.Rmdir = func(fap *FAProtocol, filename string) error {
		return os.Remove(filename)
	}
	fp.Rename = func(fap *FAProtocol, oldFilename, newFilename string) error {
		return os.Rename(oldFilename, newFilename)
	}
	fp.FSInfo = func(fap *FAProtocol, filename string) (*FileSystemInfo, error) {
		return fs.FSInfo("file://" + filename)
	}
	// C: fs_normalize (fa_fs.c:702, ENABLE_REALPATH) — realpath + file://
	fp.Normalize = func(fap *FAProtocol, filename string) (string, error) {
		return fsNormalize(filename)
	}
	// C: fs_set_xattr / fs_get_xattr (fa_fs.c:719/760, HAVE_XATTR)
	fp.SetXattr = func(fap *FAProtocol, filename, name string, data []byte) int {
		return fsSetXattr(filename, name, data)
	}
	fp.GetXattr = func(fap *FAProtocol, filename, name string) ([]byte, int) {
		return fsGetXattr(filename, name)
	}
	// C: fs_ftruncate (fa_fs.c:855) — single-part handles only.
	fp.Ftruncate = func(fh *Handle, size int64) error {
		switch r := fh.reader.(type) {
		case *fsHandle:
			if r.Ftruncate(size) == FAP_OK {
				return nil
			}
		case *os.File:
			if r.Truncate(size) == nil {
				return nil
			}
		}
		return fmt.Errorf("ftruncate failed")
	}
}

// registerGlobalFAProtocol adds a bare FAProtocol gate for a fam-registered
// protocol name if not already present (C: fa_protocol_register is called
// once per protocol at fileaccess_init; fam.Start may run per-test).
func (fam *FileAccessManager) registerGlobalFAProtocol(name string) {
	fam.protocolMutex.Lock()
	defer fam.protocolMutex.Unlock()
	for _, p := range fam.protocolList {
		if p.Name == name {
			return
		}
	}
	fam.protocolList = append(fam.protocolList, &FAProtocol{fam: fam, Name: name})
}
