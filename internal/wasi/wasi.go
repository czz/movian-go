package wasi

// Package wasi — WASI preview1 host implementation backed by the
// real filesystem, shared by the wasm2go modules (ftwasm, dvdwasm).
//
// The wasm modules need real file access: fontconfig reads /etc/fonts
// config files, scans font directories and writes its cache; the DVD
// libs stat() and open() the disc path directly (svfs covers only the
// media files). A single preopen of "/" covers the guest's absolute
// paths; the resolve step forbids ".." escapes above the preopen root.
//
// Ctx satisfies the generated Xwasi_snapshot_preview1 interfaces.

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

/* WASI preview1 errno subset */
const (
	errnoSuccess    = 0
	errnoAcces      = 2
	errnoBadf       = 8
	errnoExist      = 20
	errnoInval      = 28
	errnoIo         = 29
	errnoIsdir      = 31
	errnoLoop       = 32
	errnoNoent      = 44
	errnoNosys      = 52
	errnoNotdir     = 54
	errnoNotempty   = 55
	errnoNotcapable = 76
	errnoNotsup     = 58
)

/* filetype */
const (
	ftUnknown   = 0
	ftBlockDev  = 1
	ftCharDev   = 2
	ftDirectory = 3
	ftRegular   = 4
	ftSymlink   = 7
)

/* path_open oflags */
const (
	ofCreat = 1 << iota
	ofDir
	ofExcl
	ofTrunc
)

/* lookupflags */
const lookupSymlinkFollow = 1

/* rights base bits actually consulted */
const rightFDWrite = 1 << 6

/* seek whence */
const (
	whSet = 0
	whCur = 1
	whEnd = 2
)

// wasiErr maps host errors to preview1 errnos.
func wasiErr(err error) int32 {
	switch {
	case err == nil:
		return errnoSuccess
	case errors.Is(err, fs.ErrNotExist):
		return errnoNoent
	case errors.Is(err, fs.ErrExist):
		return errnoExist
	case errors.Is(err, fs.ErrPermission):
		return errnoAcces
	case errors.Is(err, syscall.ENOTDIR):
		return errnoNotdir
	case errors.Is(err, syscall.ENOTEMPTY):
		return errnoNotempty
	case errors.Is(err, syscall.ELOOP):
		return errnoLoop
	default:
		return errnoIo
	}
}

// wasiFD — an open descriptor on the host side.
type wasiFD struct {
	f      *os.File // regular file (nil for dirs)
	isDir  bool
	dirEnt []fs.DirEntry // snapshot for fd_readdir
}

// wasiFS — fd table + preopens + environ, one per Module instance.
type wasiFS struct {
	fds   map[int32]*wasiFD
	next  int32
	pre   map[int32]string // preopen fd -> host root dir
	pname map[int32]string // preopen fd -> guest path
	env   []byte           // environ blob: k=v\0... (snapshot at init)
}

func newWASIFS() *wasiFS {
	w := &wasiFS{
		fds: map[int32]*wasiFD{
			0: {f: nil}, // stdin
			1: {f: nil}, // stdout
			2: {f: nil}, // stderr
		},
		next:  4,
		pre:   map[int32]string{3: "/"},
		pname: map[int32]string{3: "/"},
	}
	for _, kv := range os.Environ() {
		w.env = append(w.env, kv...)
		w.env = append(w.env, 0)
	}
	return w
}

func (w *wasiFS) alloc(d *wasiFD) int32 {
	fd := w.next
	w.next++
	w.fds[fd] = d
	return fd
}

/* ------------------------- memory helpers ------------------------- */

// Ctx — WASI host state for one module instance. The memory accessor
// is bound by SetMem before the first import call.
type Ctx struct {
	fs    *wasiFS
	memFn func() []byte
}

// NewCtx allocates the fd table, preopens and environ snapshot.
func NewCtx() *Ctx { return &Ctx{fs: newWASIFS()} }

// SetMem wires the memory accessor; f must always return the module's
// *current* linear memory (it may grow).
func (w *Ctx) SetMem(f func() []byte) { w.memFn = f }

func (w *Ctx) mem() []byte { return w.memFn() }

func (w *Ctx) u32(addr int32) uint32 {
	return binary.LittleEndian.Uint32(w.mem()[addr:])
}
func (w *Ctx) w32v(addr int32, v uint32) {
	binary.LittleEndian.PutUint32(w.mem()[addr:], v)
}
func (w *Ctx) w64v(addr int32, v uint64) {
	binary.LittleEndian.PutUint64(w.mem()[addr:], v)
}
func (w *Ctx) gstr(addr, n int32) string {
	return string(w.mem()[addr : addr+n])
}

/* --------------------------- environ ------------------------------ */

func (w *Ctx) Xenviron_sizes_get(pc, pb int32) int32 {
	n := int32(0)
	for _, b := range w.fs.env {
		if b == 0 {
			n++
		}
	}
	w.w32v(pc, uint32(n))
	w.w32v(pb, uint32(len(w.fs.env)))
	return errnoSuccess
}

func (w *Ctx) Xenviron_get(penvp, pbuf int32) int32 {
	mem := w.mem()
	buf := pbuf
	start := 0
	for i, b := range w.fs.env {
		if b != 0 {
			continue
		}
		w.w32v(penvp, uint32(buf))
		penvp += 4
		n := i - start + 1
		copy(mem[buf:], w.fs.env[start:start+n])
		buf += int32(n)
		start = i + 1
	}
	return errnoSuccess
}

/* --------------------------- clock/rand --------------------------- */

func (w *Ctx) Xclock_time_get(_ int32, _ int64, out int32) int32 {
	w.w64v(out, uint64(time.Now().UnixNano()))
	return errnoSuccess
}

func (w *Ctx) Xrandom_get(buf, n int32) int32 {
	if _, err := rand.Read(w.mem()[buf : buf+n]); err != nil {
		return errnoIo
	}
	return errnoSuccess
}

func (w *Ctx) Xsched_yield() int32 { runtime.Gosched(); return errnoSuccess }

/* ----------------------------- fds -------------------------------- */

func filetypeOf(m fs.FileMode) int32 {
	switch {
	case m.IsDir():
		return ftDirectory
	case m&fs.ModeSymlink != 0:
		return ftSymlink
	case m&fs.ModeCharDevice != 0:
		return ftCharDev
	case m&fs.ModeDevice != 0:
		return ftBlockDev
	default:
		return ftRegular
	}
}

// writeFilestat — 64B __wasi_filestat_t.
func (w *Ctx) writeFilestat(addr int32, st fs.FileInfo, ino uint64) {
	w.w64v(addr, 0)     // dev
	w.w64v(addr+8, ino) // ino
	mem := w.mem()
	mem[addr+16] = byte(filetypeOf(st.Mode()))
	w.w64v(addr+24, 1) // nlink
	w.w64v(addr+32, uint64(st.Size()))
	w.w64v(addr+40, uint64(st.ModTime().UnixNano())) // atim≈mtim
	w.w64v(addr+48, uint64(st.ModTime().UnixNano()))
	w.w64v(addr+56, uint64(st.ModTime().UnixNano()))
}

func (w *Ctx) Xfd_prestat_get(fd, out int32) int32 {
	name, ok := w.fs.pname[fd]
	if !ok {
		return errnoBadf
	}
	w.mem()[out] = 0 // __wasi_preopentype_t::dir
	w.w32v(out+4, uint32(len(name)))
	return errnoSuccess
}

func (w *Ctx) Xfd_prestat_dir_name(fd, buf, _ int32) int32 {
	name, ok := w.fs.pname[fd]
	if !ok {
		return errnoBadf
	}
	copy(w.mem()[buf:], name)
	return errnoSuccess
}

func (w *Ctx) Xfd_fdstat_get(fd, out int32) int32 {
	d, ok := w.fs.fds[fd]
	_, isPre := w.fs.pre[fd]
	if !ok && !isPre {
		return errnoBadf
	}
	t := ftRegular
	if isPre || (ok && d.isDir) {
		t = ftDirectory
	}
	mem := w.mem()
	mem[out] = byte(t)
	binary.LittleEndian.PutUint16(mem[out+2:], 0) // fs_flags
	w.w64v(out+8, ^uint64(0))                     // rights base
	w.w64v(out+16, ^uint64(0))                    // rights inheriting
	return errnoSuccess
}

func (w *Ctx) Xfd_fdstat_set_flags(_, _ int32) int32 { return errnoSuccess }

func (w *Ctx) Xfd_filestat_get(fd, out int32) int32 {
	if _, isPre := w.fs.pre[fd]; isPre {
		st, err := os.Stat(w.fs.pre[fd])
		if err != nil {
			return wasiErr(err)
		}
		w.writeFilestat(out, st, 0)
		return errnoSuccess
	}
	d, ok := w.fs.fds[fd]
	if !ok {
		return errnoBadf
	}
	if d.f == nil {
		return errnoBadf
	}
	st, err := d.f.Stat()
	if err != nil {
		return wasiErr(err)
	}
	w.writeFilestat(out, st, 0)
	return errnoSuccess
}

func (w *Ctx) Xfd_close(fd int32) int32 {
	if d, ok := w.fs.fds[fd]; ok {
		if d.f != nil {
			d.f.Close()
		}
		delete(w.fs.fds, fd)
		return errnoSuccess
	}
	if _, ok := w.fs.pre[fd]; ok {
		return errnoSuccess // preopens stay open
	}
	return errnoBadf
}

// readv — gather reads across iovecs.
func (w *Ctx) Xfd_read(fd, iovp, iovn, nread int32) int32 {
	d, ok := w.fs.fds[fd]
	if !ok || d.f == nil {
		if fd == 0 { // stdin: EOF
			w.w32v(nread, 0)
			return errnoSuccess
		}
		return errnoBadf
	}
	mem := w.mem()
	var total int32
	for i := int32(0); i < iovn; i++ {
		bp := int32(w.u32(iovp + i*8))
		bl := int32(w.u32(iovp + i*8 + 4))
		if bl == 0 {
			continue
		}
		n, err := d.f.Read(mem[bp : bp+bl])
		total += int32(n)
		if err != nil {
			if err == io.EOF {
				break
			}
			return wasiErr(err)
		}
		if int32(n) < bl {
			break
		}
	}
	w.w32v(nread, uint32(total))
	return errnoSuccess
}

// writev — scatter writes; stdio is swallowed (log/assert path).
func (w *Ctx) Xfd_write(fd, iovp, iovn, nwritten int32) int32 {
	d, ok := w.fs.fds[fd]
	if !ok {
		return errnoBadf
	}
	var total int32
	if d.f == nil { // stdin/stdout/stderr
		if fd == 2 {
			for i := int32(0); i < iovn; i++ {
				bp := int32(w.u32(iovp + i*8))
				bl := int32(w.u32(iovp + i*8 + 4))
				os.Stderr.Write(w.mem()[bp : bp+bl])
				total += bl
			}
		} else {
			for i := int32(0); i < iovn; i++ {
				total += int32(w.u32(iovp + i*8 + 4))
			}
		}
		w.w32v(nwritten, uint32(total))
		return errnoSuccess
	}
	mem := w.mem()
	for i := int32(0); i < iovn; i++ {
		bp := int32(w.u32(iovp + i*8))
		bl := int32(w.u32(iovp + i*8 + 4))
		if bl == 0 {
			continue
		}
		n, err := d.f.Write(mem[bp : bp+bl])
		total += int32(n)
		if err != nil {
			w.w32v(nwritten, uint32(total))
			return wasiErr(err)
		}
	}
	w.w32v(nwritten, uint32(total))
	return errnoSuccess
}

func (w *Ctx) Xfd_seek(fd int32, off int64, whence, out int32) int32 {
	d, ok := w.fs.fds[fd]
	if !ok || d.f == nil {
		return errnoBadf
	}
	var w2 int
	switch whence {
	case whSet:
		w2 = io.SeekStart
	case whCur:
		w2 = io.SeekCurrent
	case whEnd:
		w2 = io.SeekEnd
	default:
		return errnoInval
	}
	n, err := d.f.Seek(off, w2)
	if err != nil {
		return wasiErr(err)
	}
	w.w64v(out, uint64(n))
	return errnoSuccess
}

/* --------------------------- directories -------------------------- */

// fd_readdir: cookie = index into the snapshot taken at open.
func (w *Ctx) Xfd_readdir(fd, buf, buflen int32, cookie int64, used int32) int32 {
	d, ok := w.fs.fds[fd]
	if !ok || !d.isDir {
		return errnoBadf
	}
	mem := w.mem()
	p := buf
	end := buf + buflen
	i := int64(cookie)
	for i < int64(len(d.dirEnt)) {
		e := d.dirEnt[i]
		name := e.Name()
		if p+24 > end {
			break
		}
		// Per preview1, bufused < buflen means end-of-directory, so a
		// partially-written entry (header + truncated name) is the
		// required way to fill the tail: d_namlen keeps the true
		// length and wasi-libc re-reads the entry via the cookie.
		n := int32(len(name))
		if n > end-p-24 {
			n = end - p - 24
		}
		w.w64v(p, uint64(i+1))
		w.w64v(p+8, 0)
		w.w32v(p+16, uint32(len(name)))
		t := int32(ftUnknown)
		if st, err := e.Info(); err == nil {
			t = filetypeOf(st.Mode())
		}
		mem[p+20] = byte(t)
		copy(mem[p+24:p+24+n], name)
		p += 24 + n
		if n < int32(len(name)) {
			break
		}
		i++
	}
	if i < int64(len(d.dirEnt)) {
		p = end
	}
	w.w32v(used, uint32(p-buf))
	return errnoSuccess
}

/* ----------------------------- paths ------------------------------ */

// resolve maps (dirfd, guest-relative-path) to a host path under the
// descriptor's root. ".." escapes above the root are rejected.
// Windows drive-letter paths (C:/...) bypass the preopen — they are
// already absolute on a Windows host.
func (w *Ctx) resolve(fd int32, path string) (string, int32) {
	root, isPre := w.fs.pre[fd]
	if !isPre {
		d, ok := w.fs.fds[fd]
		if !ok || !d.isDir || d.f == nil {
			return "", errnoBadf
		}
		root = d.f.Name()
	}
	if len(path) >= 3 && path[1] == ':' &&
		(path[2] == '/' || path[2] == '\\') &&
		((path[0] >= 'A' && path[0] <= 'Z') ||
			(path[0] >= 'a' && path[0] <= 'z')) {
		return filepath.FromSlash(path), 0
	}
	if filepath.IsAbs(path) {
		path = strings.TrimPrefix(path, "/")
	}
	clean := filepath.Clean(filepath.FromSlash(path))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", errnoNotcapable
	}
	return filepath.Join(root, clean), 0
}

func (w *Ctx) Xpath_open(dirfd, _ /*dirflags*/ int32, pp, plen,
	oflags int32, rightsBase, _ /*rightsInh*/ int64,
	fdflags, outfd int32) int32 {
	gp := w.gstr(pp, plen)
	host, err2 := w.resolve(dirfd, gp)
	if err2 != 0 {
		return err2
	}
	st, statErr := os.Stat(host)
	isDir := statErr == nil && st.IsDir()

	if oflags&ofDir != 0 {
		if statErr != nil {
			return wasiErr(statErr)
		}
		if !isDir {
			return errnoNotdir
		}
		f, err := os.Open(host)
		if err != nil {
			return wasiErr(err)
		}
		ents, _ := f.ReadDir(-1)
		fd := w.fs.alloc(&wasiFD{f: f, isDir: true, dirEnt: ents})
		w.w32v(outfd, uint32(fd))
		return errnoSuccess
	}

	mode := os.O_RDONLY
	if rightsBase&rightFDWrite != 0 {
		mode = os.O_RDWR
	}
	if oflags&ofCreat != 0 {
		mode |= os.O_CREATE
	}
	if oflags&ofExcl != 0 {
		mode |= os.O_EXCL
	}
	if oflags&ofTrunc != 0 {
		mode |= os.O_TRUNC
	}
	f, err := os.OpenFile(host, mode, 0o644)
	if err != nil {
		return wasiErr(err)
	}
	fd := w.fs.alloc(&wasiFD{f: f})
	w.w32v(outfd, uint32(fd))
	return errnoSuccess
}

func (w *Ctx) Xpath_filestat_get(dirfd, flags, pp, plen, out int32) int32 {
	host, err2 := w.resolve(dirfd, w.gstr(pp, plen))
	if err2 != 0 {
		return err2
	}
	var st fs.FileInfo
	var err error
	if flags&lookupSymlinkFollow != 0 {
		st, err = os.Stat(host)
	} else {
		st, err = os.Lstat(host)
	}
	if err != nil {
		return wasiErr(err)
	}
	w.writeFilestat(out, st, 0)
	return errnoSuccess
}

func (w *Ctx) Xpath_create_directory(dirfd, pp, plen int32) int32 {
	host, err2 := w.resolve(dirfd, w.gstr(pp, plen))
	if err2 != 0 {
		return err2
	}
	if err := os.Mkdir(host, 0o755); err != nil {
		return wasiErr(err)
	}
	return errnoSuccess
}

func (w *Ctx) Xpath_unlink_file(dirfd, pp, plen int32) int32 {
	host, err2 := w.resolve(dirfd, w.gstr(pp, plen))
	if err2 != 0 {
		return err2
	}
	if err := os.Remove(host); err != nil {
		return wasiErr(err)
	}
	return errnoSuccess
}

func (w *Ctx) Xpath_remove_directory(dirfd, pp, plen int32) int32 {
	host, err2 := w.resolve(dirfd, w.gstr(pp, plen))
	if err2 != 0 {
		return err2
	}
	if err := os.Remove(host); err != nil {
		return wasiErr(err)
	}
	return errnoSuccess
}

func (w *Ctx) Xpath_rename(ofd, opp, oplen, nfd, npp, nplen int32) int32 {
	old, err2 := w.resolve(ofd, w.gstr(opp, oplen))
	if err2 != 0 {
		return err2
	}
	nw, err2 := w.resolve(nfd, w.gstr(npp, nplen))
	if err2 != 0 {
		return err2
	}
	if err := os.Rename(old, nw); err != nil {
		return wasiErr(err)
	}
	return errnoSuccess
}

func (w *Ctx) Xpath_link(ofd, _ int32, opp, oplen, nfd, npp, nplen int32) int32 {
	old, err2 := w.resolve(ofd, w.gstr(opp, oplen))
	if err2 != 0 {
		return err2
	}
	nw, err2 := w.resolve(nfd, w.gstr(npp, nplen))
	if err2 != 0 {
		return err2
	}
	if err := os.Link(old, nw); err != nil {
		return wasiErr(err)
	}
	return errnoSuccess
}

func (w *Ctx) Xpath_symlink(opp, oplen, dirfd, npp, nplen int32) int32 {
	nw, err2 := w.resolve(dirfd, w.gstr(npp, nplen))
	if err2 != 0 {
		return err2
	}
	if err := os.Symlink(w.gstr(opp, oplen), nw); err != nil {
		return wasiErr(err)
	}
	return errnoSuccess
}

func (w *Ctx) Xpath_readlink(dirfd, pp, plen, buf, blen, out int32) int32 {
	host, err2 := w.resolve(dirfd, w.gstr(pp, plen))
	if err2 != 0 {
		return err2
	}
	t, err := os.Readlink(host)
	if err != nil {
		return wasiErr(err)
	}
	n := len(t)
	if n > int(blen) {
		n = int(blen)
	}
	copy(w.mem()[buf:], t[:n])
	w.w32v(out, uint32(n))
	return errnoSuccess
}

/* ------------------------------ misc ------------------------------ */

func (w *Ctx) Xproc_exit(code int32) {
	panic("wasi proc_exit")
}
