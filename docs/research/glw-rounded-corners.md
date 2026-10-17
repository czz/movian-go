# GLW rounded poster corners: Movian vs Movian Go

## Scope and evidence

Compared the user-provided Movian and Movian Go screenshots, both HDRezka views outside this checkout, the Go GLW/image path, and C sources. The HDRezka views are not part of this repository.

C references:

- **Fork (primary):** [`Buksa/movian`](https://github.com/Buksa/movian) at commit `0faa94a0d39697b139c07ef2380c0e3755a2b4cc`. Paths below marked "fork" refer to this revision.
- **Upstream permalinks:** [`andoma/movian` commit `65c22dd274d78e7e6274820ac0f6cd093cb0c3a6`](https://github.com/andoma/movian/commit/65c22dd274d78e7e6274820ac0f6cd093cb0c3a6) (2019-01-13). The headers and C files linked below are identical between this commit and the fork revision, except `src/ui/glw/glw_image.c`, whose constructor the permalink links at its upstream position. The fork sets the same four corner flags there.

The original view `movian-plugin-HDRezka/views/grid_video_minimal.view:141-146` sets `cornerRadius: 0.6em` on the poster. The newer `grid_video_content_minimal.view` sets `cornerRadius: 0.8em` on both the poster and a `backdrop` via `CardRadius()` (lines 43-45, 100-119).

## Current contract

Movian C and Movian Go use the same bit values for corner selection and texture-only flags.

| Flag | Movian C | Movian Go |
| --- | --- | --- |
| Image corner selection | `GLW_IMAGE_CORNER_*`: `0x1`, `0x2`, `0x4`, `0x8` ([glw.h](https://github.com/andoma/movian/blob/65c22dd274d78e7e6274820ac0f6cd093cb0c3a6/src/ui/glw/glw.h#L221-L227)) | `glwImageCorner*`: `0x1`, `0x2`, `0x4`, `0x8` ([types.go](../../internal/ui/glw/types.go#L263-L266)) |
| Texture corner selection | `GLW_TEX_CORNER_*` alias `GLW_IMAGE_CORNER_*` ([glw_texture.h](https://github.com/andoma/movian/blob/65c22dd274d78e7e6274820ac0f6cd093cb0c3a6/src/ui/glw/glw_texture.h#L26-L29)) | `glwTexCorner*` alias `glwImageCorner*` ([glw_texture_loader.go](../../internal/ui/glw/glw_texture_loader.go#L50-L53)) |
| Texture-only flags | repeat / intensity / primary-color: `0x80000000`, `0x40000000`, `0x20000000` ([glw_texture.h](https://github.com/andoma/movian/blob/65c22dd274d78e7e6274820ac0f6cd093cb0c3a6/src/ui/glw/glw_texture.h#L31-L33)) | Same bits; repeat is the signed `int` `-0x80000000` ([glw_texture_loader.go](../../internal/ui/glw/glw_texture_loader.go#L44-L48)) |
| Pixmap corner mask | `PIXMAP_CORNER_*`: `0x1`, `0x2`, `0x4`, `0x8` ([pixmap.h](https://github.com/andoma/movian/blob/65c22dd274d78e7e6274820ac0f6cd093cb0c3a6/src/image/pixmap.h#L95-L102)) | `PixmapCorner*` ([pixmap.go](../../internal/image/pixmap.go#L47-L50)) |

The Go corner aliases and the image flags are the same values, so the loader's corner mask and the view's corner attributes select the same bits that `PixmapRoundedCorners` consumes.

## Data path

1. The view's `cornerRadius: 0.6em` maps to `glwAttribRadius` ([attribute table](../../internal/ui/glw/glw_view_attrib.go#L399)). `setNumberEm` converts the em value to pixels and falls through to the integer setter ([number evaluation](../../internal/ui/glw/glw_view_attrib_num.go#L136-L162)); `glwImageSetInt` stores the radius in `giRadius` ([image setter](../../internal/ui/glw/glw_image.go#L1092-L1111)).
2. `glwImageCtor` enables all four texture-corner flags ([Go constructor](../../internal/ui/glw/glw_image.go#L815-L824)); `glwImageTexLoad` passes the radius and overlapping flags to `glwTexCreate` ([Go texture request](../../internal/ui/glw/glw_image.go#L527-L546)).
3. The loader forwards the radius and masks `CornerSelection` from the same flag word ([Go loader metadata](../../internal/ui/glw/glw_texture_loader.go#L313-L325)). The backend forwards both fields to `image.ImageMeta` ([image backend](../../internal/backend/core/image.go#L299-L318)).
4. With a nonzero radius, `imagePostprocessPixmap` invokes `PixmapRoundedCorners` ([postprocess](../../internal/image/image.go#L274-L293)). That routine tests only the low `0x1`–`0x8` pixmap bits ([corner algorithm](../../internal/image/pixmap.go#L254-L336)).

The fork confirms the same path at `0faa94a`: `src/ui/glw/glw_image.c:686-697,1020-1026` sets the low corner aliases and keeps repeat and analysis flags separate; `src/ui/glw/glw_texture_loader.c:298-303` forwards the selected low bits; `src/image/image.c:280-298` rounds the pixmap when the radius is nonzero. Upstream permalinks: [C image constructor](https://github.com/andoma/movian/blob/65c22dd274d78e7e6274820ac0f6cd093cb0c3a6/src/ui/glw/glw_image.c#L1005-L1016), [C texture-loader metadata](https://github.com/andoma/movian/blob/65c22dd274d78e7e6274820ac0f6cd093cb0c3a6/src/ui/glw/glw_texture_loader.c#L285-L303), [C image postprocess](https://github.com/andoma/movian/blob/65c22dd274d78e7e6274820ac0f6cd093cb0c3a6/src/image/image.c#L281-L290), and [C pixel rounding](https://github.com/andoma/movian/blob/65c22dd274d78e7e6274820ac0f6cd093cb0c3a6/src/image/pixmap.c#L234-L293).

## Corner attributes

The view attributes `cornerTopLeft`, `cornerTopRight`, `cornerBottomLeft`, and `cornerBottomRight` set the `GLW_IMAGE_CORNER_*` bits through `modImgFlags`. Those bits do not overlap the texture-only flags, so setting or clearing a corner leaves repeat and analysis flags untouched. Sources: [view corner attributes](../../internal/ui/glw/glw_view_attrib.go#L340-L343), [image flag mutation](../../internal/ui/glw/glw_image.go#L907-L919), [texture repeat use](../../internal/ui/glw/glw_texture_opengl.go#L70-L72), and [metadata flag use](../../internal/ui/glw/glw_texture_loader.go#L321-L325).

## Regression coverage

`internal/ui/glw/glw_texture_loader_test.go` covers:

- `TestGLWTextureFlagsMatchCBitAssignments`: the repeat, intensity, primary-color, and corner values against literal C bit values.
- `TestGLWImageCornerFlagsSetAndClear`: setting and clearing corner bits without changing other image flags.
- `TestGLWCornerViewAttributesSetAndClearImageCorners`: all four corner view attributes, through the attribute table, set and clear the matching image corner bit.
- `TestGLWTextureLoaderPassesCornerSelectionToPixels`: the texture loader passes the selected corners through the backend image decoder, and the decoded pixmap has alpha only at the selected corners. Corner selection does not request repeat or image analysis.

These tests verify the core image path, not full UI focus/hover rendering.

## Separate view-level overlay issue

The newer view comments document a second, conditional failure: a rectangular quad drawn over a rounded poster can cover its transparent corner pixels during focus/hover. In the original `grid_video_minimal.view`, the quad sits in a bottom-aligned information layer whose alpha follows focus/hover (`container_y` and `widget(quad)` at lines 148-156). It can square the lower edge and corners it covers while active. It cannot explain square top corners or every inactive poster being square in Go.

The newer `grid_video_content_minimal.view` addresses that compositing issue by using a rounded `backdrop` for the full-card gradient and applying the same `CardRadius()` to the poster and backdrop (lines 39-45, 100-119). The core corner-flag fix is the change that restores the rounded poster in Go. The older quad remains a separate lower-edge focus/hover risk in the plugin view, which this repository does not change.

Sources: `movian-plugin-HDRezka/views/grid_video_minimal.view:141-156`; `movian-plugin-HDRezka/views/grid_video_content_minimal.view:10-33,39-45,100-119`.
