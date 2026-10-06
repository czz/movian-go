package text

// fc* seam — wasm2go fontconfig backend: FcInitLoadConfig /
// FcConfigBuildFonts / pattern ops run inside the same wasm module as
// FreeType (internal/ftwasm, real WASI filesystem — /etc/fonts, font
// dirs and the fontconfig cache are the host's own files).
//
// Handles are wasm32 pointers carried in uintptr/unsafe.Pointer.

import (
	"unsafe"

	"github.com/czz/movian-go/internal/ftwasm"
)

type fcPattern = unsafe.Pointer
type fcFontSet = unsafe.Pointer
type fcCharSet = unsafe.Pointer

// fcwPtr packs a wasm32 pointer into unsafe.Pointer (vet-clean
// uintptr->Pointer via the double-cast, same trick as ftu/rtu).
func fcwPtr(u uint32) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&u))
}
func fcwOff(p unsafe.Pointer) int32 {
	return int32(uintptr(p))
}

// fcwModule returns the shared module, creating it if the FreeType
// side has not been initialized yet (C: fontconfig_init needs no
// ft_lib — fontconfig keeps its own internal FT_Library).
func fcwModule() *ftwasm.Module {
	if ftwMod == nil {
		var lib ftLibrary
		ftInitFreeType(&lib)
	}
	return ftwMod
}

// fcwCStr copies s into the wasm heap as a NUL-terminated string and
// returns the pointer; the caller frees with Xftw_free.
func fcwCStr(m *ftwasm.Module, s string) int32 {
	p := m.Xftw_alloc(int64(len(s) + 1))
	mem := ftwasm.Mem(m)
	copy(mem[p:], s)
	mem[p+int32(len(s))] = 0
	return p
}

// C: FcInitLoadConfig + FcConfigBuildFonts (fcglue: fcw_init packs
// (err<<32)|conf).
func fcInit() unsafe.Pointer {
	m := fcwModule() // outside Mu: lazy ftwMod init takes the lock
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	r := m.Xfcw_init()
	if int32(r>>32) != 0 {
		return nil
	}
	return fcwPtr(uint32(r))
}

func fcPatternCreate() fcPattern {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	return fcPattern(fcwPtr(uint32(fcwModule().Xfcw_pat_create())))
}
func fcPatternDestroy(p fcPattern) {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	fcwModule().Xfcw_pat_destroy(fcwOff(p))
}

func fcPatternAddString(p fcPattern, sel int, s string) {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	m := fcwModule()
	cs := fcwCStr(m, s)
	m.Xfcw_pat_add_str(fcwOff(p), int32(sel), cs)
	m.Xftw_free(cs)
}
func fcPatternAddBool(p fcPattern, sel, v int) {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	fcwModule().Xfcw_pat_add_bool(fcwOff(p), int32(sel), int32(v))
}
func fcPatternAddInteger(p fcPattern, sel, v int) {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	fcwModule().Xfcw_pat_add_int(fcwOff(p), int32(sel), int32(v))
}

func fcPatternGetBoolVal(p fcPattern, sel, n int) int {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	r := fcwModule().Xfcw_pat_get_bool(fcwOff(p), int32(sel), int32(n))
	return int(uint32(r))
}
func fcPatternGetCharSet(p fcPattern, sel, n int) (fcCharSet, int) {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	r := fcwModule().Xfcw_pat_get_charset(fcwOff(p), int32(sel), int32(n))
	return fcCharSet(fcwPtr(uint32(r))), int(int32(r >> 32))
}
func fcPatternGetString(p fcPattern, sel, n int) (string, int) {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	m := fcwModule()
	r := m.Xfcw_pat_get_string(fcwOff(p), int32(sel), int32(n))
	s := ""
	if sp := int32(r); sp != 0 {
		s = ftwCStr(sp)
	}
	return s, int(int32(r >> 32))
}

func fcCharSetHasChar(cs fcCharSet, uc uint32) int {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	return int(fcwModule().Xfcw_charset_has_char(fcwOff(cs), int32(uc)))
}

func fcDefaultSubstitute(p fcPattern) {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	fcwModule().Xfcw_default_substitute(fcwOff(p))
}
func fcConfigSubstitute(conf unsafe.Pointer, p fcPattern, kind int) int {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	return int(fcwModule().Xfcw_config_substitute(
		fcwOff(conf), fcwOff(p), int32(kind)))
}

// C: FcFontSort(config, pat, FcTrue, NULL, &result)
func fcFontSort(conf unsafe.Pointer, p fcPattern, trim int) (fcFontSet, int) {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	r := fcwModule().Xfcw_font_sort(fcwOff(conf), fcwOff(p),
		int32(trim))
	return fcFontSet(fcwPtr(uint32(r))), int(int32(r >> 32))
}
func fcFontSetNFont(fs fcFontSet) int {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	return int(fcwModule().Xfcw_fs_nfont(fcwOff(fs)))
}
func fcFontSetFont(fs fcFontSet, i int) fcPattern {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	return fcPattern(fcwPtr(uint32(fcwModule().Xfcw_fs_font(fcwOff(fs), int32(i)))))
}
func fcFontSetDestroy(fs fcFontSet) {
	ftwasm.Mu.Lock()
	defer ftwasm.Mu.Unlock()
	fcwModule().Xfcw_fs_destroy(fcwOff(fs))
}
