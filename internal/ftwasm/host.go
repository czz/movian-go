// Package ftwasm — host side of the wasm2go-generated FreeType
// module (freetype_gen.go). The module itself is FreeType 2.13.3
// compiled to wasm32 by wasi-sdk (build.sh + ftglue.c regenerate it).
//
// The module imports three namespaces, all implemented by hostCtx:
//
//	w2g: face_read / face_close / gray_spans (wired to the consumer
//	     package via Host) and throw_longjmp (a panic — see SjLj below)
//	env: invoke_* wrappers — the SjLj catch boundaries
//	wasi_snapshot_preview1: benign syscall stubs (FreeType never
//	     reaches real syscalls; malloc/mem*/math are pure-compute)
//
// SjLj emulation: FreeType uses setjmp/longjmp (raster cell overflow
// in ftgrays, table validation in sfnt) compiled with
// -mllvm -enable-emscripten-sjlj. emscripten_longjmp is a Go panic;
// every invoke_* boundary defers catchLongjmp which restores the
// wasm stack pointer — the caller then routes via __wasm_setjmp_test.
package ftwasm

import (
	"github.com/czz/movian-go/internal/wasi"
	"sync"
)

// Host — consumer-provided callbacks for the module's w2g imports.
// Implementations mirror freetype.c's face_read/face_close and
// rasterizer_ft.c's gray_spans dispatch.
type Host interface {
	// FaceRead — C: face_read (count==0 means seek to offset).
	FaceRead(fh int64, offset, buffer, count int32) int32
	// FaceClose — C: face_close.
	FaceClose(fh int64)
	// GraySpans — C: params.gray_spans.
	GraySpans(y, count, spans int32, user int64)
}

// Mu serializes module calls + memory reads: the module has a single
// linear memory and the glue uses scratch cells (bbox/kerning vec),
// so compound operations must not interleave. The C library is not
// thread-safe either — this matches upstream expectations.
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
	w := &hostCtx{h: h, Ctx: wasi.NewCtx()}
	m := New(w, w, w)
	w.mod = m
	w.SetMem(func() []byte { return Mem(w.mod) })
	m.X_initialize()
	return m
}

func (w *hostCtx) Init(m any) { w.mod = m.(*Module) }

/* ---------------------------- w2g ---------------------------- */

func (w *hostCtx) Xface_read(fh int64, off, buf, count int32) int32 {
	return w.h.FaceRead(fh, off, buf, count)
}
func (w *hostCtx) Xface_close(fh int64) { w.h.FaceClose(fh) }
func (w *hostCtx) Xgray_spans(y, count, spans int32, user int64) {
	w.h.GraySpans(y, count, spans, user)
}
func (w *hostCtx) Xthrow_longjmp() { panic("emscripten_longjmp") }

/* ---------------------------- env ------------------------------ */

// catchLongjmp — the "catch" half of emulated SjLj: a panic raised by
// Xthrow_longjmp unwinds Go frames to the nearest invoke boundary;
// restoring the wasm stack pointer discards abandoned C frames.
func (w *hostCtx) catchLongjmp(sp int32) {
	if r := recover(); r == "emscripten_longjmp" {
		*w.mod.X__stack_pointer() = sp
	} else if r != nil {
		panic(r)
	}
}

func (w *hostCtx) table() []any { return *w.mod.X__indirect_function_table() }

func (w *hostCtx) Xinvoke_v(idx int32) {
	m := w.mod
	defer w.catchLongjmp(*m.X__stack_pointer())
	w.table()[idx].(func())()
}
func (w *hostCtx) Xinvoke_vi(idx, a int32) {
	m := w.mod
	defer w.catchLongjmp(*m.X__stack_pointer())
	w.table()[idx].(func(int32))(a)
}
func (w *hostCtx) Xinvoke_vii(idx, a, b int32) {
	m := w.mod
	defer w.catchLongjmp(*m.X__stack_pointer())
	w.table()[idx].(func(int32, int32))(a, b)
}
func (w *hostCtx) Xinvoke_viii(idx, a, b, c int32) {
	m := w.mod
	defer w.catchLongjmp(*m.X__stack_pointer())
	w.table()[idx].(func(int32, int32, int32))(a, b, c)
}
func (w *hostCtx) Xinvoke_viiii(idx, a, b, c, d int32) {
	m := w.mod
	defer w.catchLongjmp(*m.X__stack_pointer())
	w.table()[idx].(func(int32, int32, int32, int32))(a, b, c, d)
}
func (w *hostCtx) Xinvoke_ii(idx, a int32) int32 {
	m := w.mod
	defer w.catchLongjmp(*m.X__stack_pointer())
	return w.table()[idx].(func(int32) int32)(a)
}
func (w *hostCtx) Xinvoke_iiiiiii(idx, a, b, c, d, e, f int32) int32 {
	m := w.mod
	defer w.catchLongjmp(*m.X__stack_pointer())
	return w.table()[idx].(func(int32, int32, int32, int32,
		int32, int32) int32)(a, b, c, d, e, f)
}
func (w *hostCtx) Xinvoke_iiiiiiiiii(idx, a, b, c, d, e, f, g, h,
	i int32) int32 {
	m := w.mod
	defer w.catchLongjmp(*m.X__stack_pointer())
	return w.table()[idx].(func(int32, int32, int32, int32, int32,
		int32, int32, int32, int32) int32)(a, b, c, d, e, f, g, h, i)
}
func (w *hostCtx) Xinvoke_iii(idx, a, b int32) int32 {
	m := w.mod
	defer w.catchLongjmp(*m.X__stack_pointer())
	return w.table()[idx].(func(int32, int32) int32)(a, b)
}
func (w *hostCtx) Xinvoke_iiii(idx, a, b, c int32) int32 {
	m := w.mod
	defer w.catchLongjmp(*m.X__stack_pointer())
	return w.table()[idx].(func(int32, int32, int32) int32)(a, b, c)
}
func (w *hostCtx) Xinvoke_iiiii(idx, a, b, c, d int32) int32 {
	m := w.mod
	defer w.catchLongjmp(*m.X__stack_pointer())
	return w.table()[idx].(func(int32, int32, int32, int32) int32)(a, b, c, d)
}

/* --------------------- wasi_snapshot_preview1 -------------------- */
/* Real implementation lives in fs.go (fontconfig does actual file   */
/* I/O: /etc/fonts config, font dir scanning, cache writes).         */
