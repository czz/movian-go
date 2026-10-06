// Package dvdwasm — host side of the wasm2go-generated DVD module
// (dvd_gen.go). The module is the vendored ext/dvd bundle
// (libdvdcss + libdvdread + libdvdnav) compiled to wasm32 by wasi-sdk
// (internal/backend/dvd/dvdlib/wasm/build.sh + dvglue.c regenerate it).
//
// Imports:
//
//	w2g: fa_open/close/read/seek/stat/findfile (the svfs_ops bridge,
//	     wired to the fileaccess layer via Host) and dvd_trace
//	     (the libs' TRACE sink → Go)
//	wasi_snapshot_preview1: real-fs WASI host from internal/wasi —
//	     libdvdread stats/opens the disc path itself, while all media
//	     I/O still goes through the svfs fa_* bridge (as in C).
//
// No SjLj: the DVD libs use no setjmp/longjmp, so no invoke_* host
// wrappers are generated.
package dvdwasm

import (
	"encoding/binary"
	"sync"

	"github.com/czz/movian-go/internal/wasi"
)

// Host — consumer-provided callbacks for the module's w2g imports.
// Mirrors dvdlib_fa.go's faHost (svfs_ops semantics).
type Host interface {
	// FaOpen — C: goFaOpen. Returns 0 on failure.
	FaOpen(url string) uint64
	// FaClose — C: goFaClose.
	FaClose(fh uint64)
	// FaRead — C: goFaRead. Returns bytes read, <0 on error.
	FaRead(fh uint64, buf []byte) int
	// FaSeek — C: goFaSeek. Returns new position, <0 on error.
	FaSeek(fh uint64, pos int64, whence int) int64
	// FaStat — C: goFaStat. ok=false on failure.
	FaStat(url string) (size int64, isdir bool, mtime int64, ok bool)
	// FaFindfile — C: goFaFindfile. Returns the resolved path.
	FaFindfile(path, file string) (string, bool)
	// DvdTrace — C: goDvdTrace.
	DvdTrace(level int, subsys, msg string)
}

// Dbg — verbose w2g import trace (tests).
var Dbg bool

// Mu serializes module calls + memory reads: single linear memory and
// the dvglue scratch cell — matches upstream's non-thread-safe lib.
var Mu sync.Mutex

// Mem returns the module's linear memory. Always refetch after a call
// that may allocate — memory.grow may reallocate the backing array.
func Mem(m *Module) []byte { return *m.Xmemory().Slice() }

type hostCtx struct {
	*wasi.Ctx
	mod *Module
	h   Host
}

// NewModule instantiates the generated module and runs the reactor
// initializer (wasi-libc crt).
func NewModule(h Host) *Module {
	w := &hostCtx{Ctx: wasi.NewCtx(), h: h}
	m := New(w, w)
	w.mod = m
	w.SetMem(func() []byte { return Mem(m) })
	m.X_initialize()
	return m
}

func (w *hostCtx) Init(m any) { w.mod = m.(*Module) }

func (w *hostCtx) cstr(p int32) string {
	mem := Mem(w.mod)
	end := p
	for end < int32(len(mem)) && mem[end] != 0 {
		end++
	}
	return string(mem[p:end])
}

/* ---------------------------- w2g ---------------------------- */

func (w *hostCtx) Xfa_open(url int32) int64 {
	r := int64(w.h.FaOpen(w.cstr(url)))
	if Dbg {
		println("w2g fa_open", w.cstr(url), "->", r)
	}
	return r
}
func (w *hostCtx) Xfa_close(fh int64) { w.h.FaClose(uint64(fh)) }
func (w *hostCtx) Xfa_read(fh int64, buf, size int32) int32 {
	r := int32(w.h.FaRead(uint64(fh), Mem(w.mod)[buf:buf+size]))
	if Dbg {
		println("w2g fa_read", fh, size, "->", r)
	}
	return r
}
func (w *hostCtx) Xfa_seek(fh, pos int64, whence int32) int64 {
	r := w.h.FaSeek(uint64(fh), pos, int(whence))
	if Dbg {
		println("w2g fa_seek", fh, pos, whence, "->", r)
	}
	return r
}

// out points to dvglue's stat scratch {i64 size, i32 isdir, i64 mtime}
func (w *hostCtx) Xfa_stat(url, out int32) int32 {
	size, isdir, mtime, ok := w.h.FaStat(w.cstr(url))
	if !ok {
		return -1
	}
	m := Mem(w.mod)
	binary.LittleEndian.PutUint64(m[out:], uint64(size))
	var d int32
	if isdir {
		d = 1
	}
	binary.LittleEndian.PutUint32(m[out+8:], uint32(d))
	binary.LittleEndian.PutUint64(m[out+12:], uint64(mtime))
	return 0
}

func (w *hostCtx) Xfa_findfile(path, file, fullpath, length int32) int32 {
	full, ok := w.h.FaFindfile(w.cstr(path), w.cstr(file))
	if !ok {
		return -1
	}
	m := Mem(w.mod)
	b := []byte(full)
	if len(b) >= int(length) {
		b = b[:length-1]
	}
	copy(m[fullpath:], b)
	m[fullpath+int32(len(b))] = 0
	return 0
}

func (w *hostCtx) Xdvd_trace(level, subsys, msg int32) {
	w.h.DvdTrace(int(level), w.cstr(subsys), w.cstr(msg))
}
