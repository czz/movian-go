package text

import (
	backendcore "github.com/czz/movian-go/internal/backend/core"
	miscpkg "github.com/czz/movian-go/internal/misc"
)

// freetypeOps implements backendcore.FreetypeOps over the canonical
// freetype functions — C: fa_video.c calls freetype_get_context /
// freetype_load_dynamic_font_buf / freetype_unload_font directly.
type freetypeOps struct{ sys *System }

// FreetypeOps returns the adapter registered on the BackendSystem
// (pkg/text imports backendcore, so the provider wires itself).
func (sys *System) FreetypeOps() backendcore.FreetypeOps { return freetypeOps{sys} }

func (o freetypeOps) GetContext() int { return o.sys.FreetypeGetContext() }

func (o freetypeOps) LoadDynamicFontBuf(data []byte, fontDomain int, source string) any {
	b := miscpkg.BufCreateAndCopy(len(data), data)
	defer b.Release()
	f, err := o.sys.FreetypeLoadDynamicFontBuf(b, fontDomain)
	if err != nil {
		return nil
	}
	return f
}

func (o freetypeOps) UnloadFont(h any) {
	if f, ok := h.(*faceT); ok {
		o.sys.FreetypeUnloadFont(f)
	}
}
