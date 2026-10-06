// Canonical port of src/fileaccess/fa_fs.c — the filesystem protocol.
// Includes the split-file machinery (foo.001/foo.002/... opened and
// read as one concatenated file), fs_normalize (realpath), xattrs
// (Linux user.* namespace), ftruncate (single-part only), and the
// /dev/stdout+/dev/stderr write-open special case.
package fileaccess

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// fsPart — C: part_t (fa_fs.c:41)
type fsPart struct {
	fd   *os.File
	size int64
}

// fsHandle — C: fs_handle_t (fa_fs.c:47)
type fsHandle struct {
	partCount int
	totalSize int64 // Only valid if partCount != 1
	readPos   int64
	parts     []fsPart
}

// fsURLsprintf — C: fs_urlsnprintf (fa_fs.c:60)
func fsURLsprintf(prefix, base, fname string) string {
	blen := len(base)
	if base == "/" {
		base = ""
		blen = 0
	}
	sep := "/"
	if blen > 0 && base[blen-1] == '/' {
		sep = ""
	}
	return prefix + base + sep + fname
}

// isSplittedFileName — C: is_splitted_file_name (fa_fs.c:73).
// "foobar.00x" → x; 0 otherwise.
func isSplittedFileName(s string) int {
	if len(s) < 4 {
		return 0
	}
	s = s[len(s)-4:]
	if s[0] == '.' &&
		s[1] >= '0' && s[1] <= '9' &&
		s[2] >= '0' && s[2] <= '9' &&
		s[3] >= '0' && s[3] <= '9' {
		n, _ := strconv.Atoi(s[1:])
		return n
	}
	return 0
}

// fileExists — C: file_exists (fa_fs.c:89)
func fileExists(fn string) bool {
	st, err := os.Stat(fn)
	return err == nil && !st.IsDir()
}

// splitPieceName — C: get_split_piece_name (fa_fs.c:97)
func splitPieceName(fn string, num int) string {
	return fmt.Sprintf("%s.%03d", fn, num+1)
}

// splitPieceCount — C: get_split_piece_count (fa_fs.c:110)
func splitPieceCount(fn string) int {
	count := 0
	for fileExists(splitPieceName(fn, count)) {
		count++
	}
	return count
}

// fsOpen — C: fs_open (fa_fs.c:186). On open failure, falls back to
// the concatenated split pieces (url.001, url.002, ...).
func fsOpen(p *FSProtocol, path, url string, extra *OpenExtra) (*Handle, error) {
	var fh *fsHandle

	// C: /dev/stdout and /dev/stderr open for writing directly
	if path == "/dev/stdout" || path == "/dev/stderr" {
		fd, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			return nil, err
		}
		fh = &fsHandle{partCount: 1, parts: []fsPart{{fd: fd}}}
		return fsHandleToHandle(p, fh, url), nil
	}

	if extra != nil && extra.Flags&FaWrite != 0 {
		openFlags := os.O_RDWR | os.O_CREATE
		if extra.Flags&FaAppend == 0 {
			openFlags |= os.O_TRUNC
		}
		fd, err := os.OpenFile(path, openFlags, 0666)
		if err != nil {
			return nil, err
		}
		if extra.Flags&FaAppend != 0 {
			fd.Seek(0, io.SeekEnd)
		}
		fh = &fsHandle{partCount: 1, parts: []fsPart{{fd: fd}}}
		h := fsHandleToHandle(p, fh, url)
		h.writer = fsWriteOnly{fd}
		return h, nil
	}

	fd, err := os.Open(path)
	if err != nil {
		c := splitPieceCount(path)
		if c == 0 {
			return nil, err
		}
		fh = &fsHandle{partCount: c, parts: make([]fsPart, c)}
		for i := range c {
			pfd, perr := os.Open(splitPieceName(path, i))
			if perr != nil {
				fh.Close()
				return nil, perr
			}
			st, serr := pfd.Stat()
			if serr != nil {
				fh.Close()
				return nil, serr
			}
			fh.parts[i] = fsPart{fd: pfd, size: st.Size()}
			fh.totalSize += st.Size()
		}
		return fsHandleToHandle(p, fh, url), nil
	}

	fh = &fsHandle{partCount: 1, parts: []fsPart{{fd: fd}}}
	h := fsHandleToHandle(p, fh, url)
	if info, err := fd.Stat(); err == nil {
		h.size = info.Size()
	}
	return h, nil
}

// fsHandleToHandle wraps an fsHandle in a Handle; for multi-part
// handles the reader/seeker go through fsRead/fsSeek.
func fsHandleToHandle(p *FSProtocol, fh *fsHandle, url string) *Handle {
	h := &Handle{proto: p, url: url}
	if fh.partCount == 1 {
		f := fh.parts[0].fd
		h.reader = f
		h.seeker = f
	} else {
		h.reader = fh
		h.seeker = fh
	}
	// C: fs_fsize — single part stats the fd; multi returns total_size
	if fh.partCount == 1 {
		h.sizer = func() int64 {
			st, err := fh.parts[0].fd.Stat()
			if err != nil {
				return -1
			}
			return st.Size()
		}
	} else {
		h.sizer = func() int64 { return fh.totalSize }
	}
	return h
}

// fsWriteOnly — the fd lifetime is owned by the handle's reader;
// closing the writer side must not close the fd.
type fsWriteOnly struct{ *os.File }

func (fsWriteOnly) Close() error { return nil }

// getCurrentReadPieceNum — C: get_current_read_piece_num (fa_fs.c:271)
func (fh *fsHandle) getCurrentReadPieceNum() int {
	size := fh.parts[0].size
	i := 0
	for i = 0; i < fh.partCount; i++ {
		if fh.readPos <= size {
			break
		}
		if i+1 < fh.partCount {
			size += fh.parts[i+1].size
		}
	}
	if i > fh.partCount-1 {
		i = fh.partCount - 1
	}
	return i
}

// Read — C: fs_read (fa_fs.c:287). Multi-part: reads the current
// piece; on a short read, continues once into the next piece.
func (fh *fsHandle) Read(buf []byte) (int, error) {
	if fh.partCount == 1 {
		return fh.parts[0].fd.Read(buf)
	}
	pn := fh.getCurrentReadPieceNum()
	rsize, err := fh.parts[pn].fd.Read(buf)
	if rsize < len(buf) && pn < fh.partCount-1 {
		fh.parts[pn+1].fd.Seek(0, io.SeekStart)
		n2, _ := fh.parts[pn+1].fd.Read(buf[rsize:])
		rsize += n2
	}
	fh.readPos += int64(rsize)
	return rsize, err
}

// Write — C: fs_write (fa_fs.c:308). Multi-part writes return 0.
func (fh *fsHandle) Write(buf []byte) (int, error) {
	if fh.partCount == 1 {
		return fh.parts[0].fd.Write(buf)
	}
	return 0, nil
}

// Seek — C: fs_seek (fa_fs.c:320). Multi-part: maps the logical
// position onto a per-piece lseek. SEEK_END is total_size - pos
// (canonical quirk preserved).
func (fh *fsHandle) Seek(pos int64, whence int) (int64, error) {
	if fh.partCount == 1 {
		return fh.parts[0].fd.Seek(pos, whence)
	}
	actPos := fh.readPos
	switch whence {
	case io.SeekStart:
		actPos = pos
	case io.SeekCurrent:
		actPos += pos
	case io.SeekEnd:
		actPos = fh.totalSize - pos
	}
	fh.readPos = actPos
	pn := fh.getCurrentReadPieceNum()
	for i := range pn {
		actPos -= fh.parts[i].size
	}
	return fh.parts[pn].fd.Seek(actPos, io.SeekStart)
}

// Close — C: fs_close (fa_fs.c:171)
func (fh *fsHandle) Close() error {
	for i := range fh.partCount {
		if fh.parts[i].fd != nil {
			fh.parts[i].fd.Close()
		}
	}
	return nil
}

// Ftruncate — C: fs_ftruncate (fa_fs.c:855). Single-part only.
func (fh *fsHandle) Ftruncate(newsize int64) int {
	if fh.partCount == 1 {
		if err := fh.parts[0].fd.Truncate(newsize); err == nil {
			return FAP_OK
		}
	}
	return FAP_ERROR
}

// fsStat — C: fs_stat (fa_fs.c:376). On stat failure, falls back to
// summing the split pieces.
func fsStat(path string) (*FileStat, error) {
	fi, err := os.Stat(path)
	if err != nil {
		pieceNum := splitPieceCount(path)
		if pieceNum == 0 {
			// C: returns FAP_ERROR regardless of errno — opaque error
			// (not wrapped) so fapErrCode maps to FAP_ERROR.
			return nil, errors.New(err.Error())
		}
		st := &FileStat{}
		for i := range pieceNum {
			pi, perr := os.Stat(splitPieceName(path, i))
			if perr != nil {
				return nil, errors.New(perr.Error())
			}
			st.Size += pi.Size()
			st.MTime = pi.ModTime()
			st.Type = ContentFile
		}
		return st, nil
	}
	st := &FileStat{Size: fi.Size(), MTime: fi.ModTime()}
	if fi.IsDir() {
		st.Type = ContentDir
	} else {
		st.Type = ContentFile
	}
	return st, nil
}

// fsUnlink — C: fs_unlink (fa_fs.c:429). On unlink failure, removes
// all split pieces.
func fsUnlink(path string) error {
	if err := os.Remove(path); err != nil {
		pieceNum := splitPieceCount(path)
		if pieceNum == 0 {
			return err
		}
		for i := range pieceNum {
			if uerr := os.Remove(splitPieceName(path, i)); uerr != nil {
				return uerr
			}
		}
	}
	return nil
}

// fsMakedir — C: fs_makedir (fa_fs.c:460) — mkdir(url, 0770) with
// fa_err_code_t mapping.
func fsMakedir(path string) int {
	if err := os.Mkdir(path, 0770); err != nil {
		if pe, ok := errors.AsType[*os.PathError](err); ok {
			switch pe.Err {
			case syscall.ENOENT:
				return FAP_NOENT
			case syscall.EPERM:
				return FAP_PERMISSION_DENIED
			case syscall.EEXIST:
				return FAP_EXIST
			}
		}
		return FAP_ERROR
	}
	return 0
}

// fsNormalize — C: fs_normalize (fa_fs.c:702, ENABLE_REALPATH).
// realpath() + "file://" prefix.
func fsNormalize(url string) (string, error) {
	r, err := filepath.EvalSymlinks(url)
	if err != nil {
		return "", err
	}
	return "file://" + r, nil
}
