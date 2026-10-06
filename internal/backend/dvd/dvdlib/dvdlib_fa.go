/*
 * Go side of the svfs_ops trampolines + TRACE sink for bundled ext/dvd —
 * implements the w2g imports of the wasm2go dvd module (internal/dvdwasm).
 * Was the //export'd goFa/goDvdTrace set of the cgo build; same semantics.
 */

package dvdlib

import (
	"os"
	"sync"
	"sync/atomic"

	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
)

// dvdlibFAM — C: the implicit global fa context reached by the
// libdvdnav file callbacks. Wired by RegisterDVDBackend (init-time).
var dvdlibFAM *fileaccesscore.FileAccessManager

// SetFAM wires the file access manager for the fa_* callbacks.
func SetFAM(fam *fileaccesscore.FileAccessManager) { dvdlibFAM = fam }

// faHandle — wasm-visible file handle table (replaces cgo.Handle).
var (
	faMu  sync.Mutex
	faTab = map[uint64]*fileaccesscore.Handle{}
	faSeq uint64
)

func faNew(f *fileaccesscore.Handle) uint64 {
	h := atomic.AddUint64(&faSeq, 1)
	faMu.Lock()
	faTab[h] = f
	faMu.Unlock()
	return h
}

func faGet(h uint64) *fileaccesscore.Handle {
	faMu.Lock()
	defer faMu.Unlock()
	return faTab[h]
}

func faDel(h uint64) {
	faMu.Lock()
	delete(faTab, h)
	faMu.Unlock()
}

// faHost implements dvdwasm.Host.
type faHost struct{}

// FaOpen — C: dvd_fa_open → fa_open_ex(url, NULL, 0, FA_BUFFERED_BIG, NULL)
var dbg = os.Getenv("DVD_FA_DEBUG") != ""

func (faHost) FaOpen(url string) uint64 {
	fam := dvdlibFAM
	if fam == nil {
		return 0
	}
	fh, err := fileaccesscore.OpenEx(fam, url, nil,
		fileaccesscore.FaBufferedBig)
	if dbg {
		println("FaOpen", url, "err", err != nil, "nil", fh == nil)
	}
	if err != nil || fh == nil {
		return 0
	}
	return faNew(fh)
}

// FaClose — C: fa_close
func (faHost) FaClose(fh uint64) {
	if f := faGet(fh); f != nil {
		faDel(fh)
		f.Close()
	}
}

// FaRead — C: fa_read
func (faHost) FaRead(fh uint64, buf []byte) int {
	f := faGet(fh)
	if f == nil {
		if dbg {
			println("FaRead bad fh", fh)
		}
		return -1
	}
	n, err := f.Read(buf)
	if dbg {
		println("FaRead", fh, len(buf), "->", n)
	}
	if err != nil && n == 0 {
		return -1
	}
	return n
}

// FaSeek — C: dvd_fa_seek → fa_seek(fh, pos, whence)
func (faHost) FaSeek(fh uint64, pos int64, whence int) int64 {
	f := faGet(fh)
	if f == nil {
		return -1
	}
	n, err := f.Seek(pos, whence)
	if dbg {
		println("FaSeek", fh, pos, whence, "->", n)
	}
	if err != nil {
		return -1
	}
	return n
}

// FaStat — C: dvd_fa_stat → fa_stat(url, &fs, NULL, 0); -1 on fail/size==-1
func (faHost) FaStat(url string) (int64, bool, int64, bool) {
	fam := dvdlibFAM
	if fam == nil {
		return 0, false, 0, false
	}
	fs, err := fileaccesscore.Stat(fam, url)
	if err != nil || fs == nil || fs.Size == -1 {
		return 0, false, 0, false
	}
	return fs.Size, fs.Type == fileaccesscore.ContentDir,
		fs.MTime.Unix(), true
}

// FaFindfile — C: fa_findfile
func (faHost) FaFindfile(path, file string) (string, bool) {
	fam := dvdlibFAM
	if fam == nil {
		return "", false
	}
	full, r := fileaccesscore.FAFindfile(fam, path, file)
	return full, r == 0
}

// DvdTrace — C: goDvdTrace
func (faHost) DvdTrace(level int, subsys, msg string) {
	if fam := dvdlibFAM; fam != nil {
		fam.TraceSystem().Trace(level, subsys, "%s", msg)
	}
}
