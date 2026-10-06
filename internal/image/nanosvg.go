package image

// src/image/nanosvg.c binds the vendored ext/nanosvg library, which
// is not present in this tree. The canonical fallback is upstream's
// own in-tree SVG decoder (svg.c: svg_decode → vector component →
// image_rasterize_ft downstream) — it produces the same image_t the
// caller of nanosvg_decode expects.

import misc "github.com/czz/movian-go/internal/misc"

// NanosvgDecode — C: nanosvg_decode (nanosvg.c:31-86). The ext
// library being absent, this maps to svg_decode.
func NanosvgDecode(buf *misc.Buf, meta *ImageMeta) (*Image, error) {
	return SvgDecode(buf, meta)
}
