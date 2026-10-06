package glw

// C: src/ui/glw/glw.h — canonical 1:1 port.
// Every constant, enum, struct, inline helper and macro from the header.

import (
	backendcore "github.com/czz/movian-go/internal/backend/core"
	imagepkg "github.com/czz/movian-go/internal/image"
	miscpkg "github.com/czz/movian-go/internal/misc"
)

// Forward-declared element types — fields land when their C file is ported.
// C: struct glw_loadable_texture; struct glw_video; struct glw_cached_view;
//
//	struct glw_view_load_request; struct glw_text_bitmap; struct glw_rec;
//	struct glw_render_job; struct glw_render_order; struct glw_backend_texture;
//
// C: glw_loadable_texture_t (glw_texture.h:36-102)
type GlwLoadableTexture struct {
	gltGlobalLinkNext *GlwLoadableTexture // C: LIST_ENTRY glt_global_link
	gltGlobalLinkPrev **GlwLoadableTexture
	gltFlushLinkNext  *GlwLoadableTexture // C: LIST_ENTRY glt_flush_link
	gltFlushLinkPrev  **GlwLoadableTexture
	gltWorkLinkNext   *GlwLoadableTexture // C: TAILQ_ENTRY glt_work_link
	gltWorkLinkPrevP  **GlwLoadableTexture
	gltQ              *glwLoadableTextureQueue // C: struct glw_loadable_texture_queue *glt_q

	gltFlags       int // C: glt_flags — GLW_TEX_*
	gltSourceFlags int // C: glt_source_flags — GLW_SOURCE_FLAGS_*

	gltState uint8 // C: enum GLT_STATE_*

	gltRefcnt uint

	gltAspect    float32
	gltReqAspect float32

	gltTexture GlwBackendTexture

	// C: struct fa_resolver *glt_far — fa_resolver removed upstream, not ported
	gltURL *miscpkg.Rstr

	gltPixmap *imagepkg.Pixmap

	gltCancellable *miscpkg.Cancellable

	gltReqXs int16
	gltReqYs int16
	gltXs    int16
	gltYs    int16

	gltOrientation uint8
	gltStash       uint8
	gltOriginType  uint8
	gltOpaque      uint8

	gltFormat         int
	gltInternalFormat int

	gltS, gltT   float32
	gltTexWidth  int16
	gltTexHeight int16
	gltRadius    int16
	gltMargin    int16
	gltShadow    int16

	gltSize int

	gltIntensity float32

	gltPrimaryColor [3]float32

	gltBackend *backendcore.Backend
	gltGr      *glwRoot
}

// C: glw_backend_texture_t (glw.h:104-110)
// C: glw_backend_texture_t (glw_opengl.h:157-163)
type GlwBackendTexture struct {
	Textures [3]uint32 // C: GLuint textures[3]
	Gltype   int       // C: gltype — GL_TEXTURE_2D etc.
	Width    int32     // C: uint16_t width
	Height   int32     // C: uint16_t height
	Opaque   int8      // C: uint8_t opaque
	Aspect   float32   // Go-only: texture aspect (see glw_texture_loader)
}

// C: #define GLW_TEXTURE_THREADS 6
const glwTextureThreads = 6

// C: LQ_* queue indices (glw.h:898-903)
const (
	lqSkin      = 0 // LQ_SKIN
	lqTentative = 1 // LQ_TENTATIVE
	lqThumbs    = 2 // LQ_THUMBS
	lqOther     = 3 // LQ_OTHER
	lqRefresh   = 4 // LQ_REFRESH
	lqNum       = 5 // LQ_num
)

// GlwImage — C: glw_image_t (glw_image.c:24-108)
type GlwImage struct {
	w Glw // C: glw_t w

	giAlphaSelf float32 // C: gi_alpha_self
	giAngle     float32 // C: gi_angle
	giAspect    float32 // C: gi_aspect

	giPendingUrl      *miscpkg.Rstr // C: rstr_t *gi_pending_url
	giPendingUrlFlags int           // C: gi_pending_url_flags

	giCurrent *GlwLoadableTexture // C: gi_current
	giPending *GlwLoadableTexture // C: gi_pending

	giBorder  [4]int16 // C: gi_border[4]
	giPadding [4]int16 // C: gi_padding[4]

	// C: gi_box_ is (gi_border_ + gi_padding_)
	giBoxLeft   int16
	giBoxRight  int16
	giBoxTop    int16
	giBoxBottom int16

	giBitmapFlags int // C: gi_bitmap_flags (GLW_IMAGE_*)

	giRescaleHold uint8 // C: gi_rescale_hold
	giMode        uint8 // C: gi_mode (GI_MODE_*)

	giAlphaEdge uint8 // C: gi_alpha_edge

	giWidgetStatus glwWidgetStatus // C: gi_widget_status

	giIsReady          bool // C: gi_is_ready : 1
	giUpdate           bool // C: gi_update : 1
	giNeedReload       bool // C: gi_need_reload : 1
	giLoadingNewUrl    bool // C: gi_loading_new_url : 1
	giRecompile        bool // C: gi_recompile : 1
	giExternalized     bool // C: gi_externalized : 1
	giSaturated        bool // C: gi_saturated : 1
	giWantPrimaryColor bool // C: gi_want_primary_color : 1

	giFixedSize int16 // C: gi_fixed_size
	giRadius    int16 // C: gi_radius
	giShadow    int16 // C: gi_shadow

	giGr glwRenderer // C: gi_gr

	giLastWidth  int16 // C: gi_last_width
	giLastHeight int16 // C: gi_last_height

	giColor  glwRgb // C: gi_color
	giColMul glwRgb // C: gi_col_mul
	giColOff glwRgb // C: gi_col_off

	giSizeScale   float32 // C: gi_size_scale
	giSaturation  float32 // C: gi_saturation
	giAutofade    float32 // C: gi_autofade
	giChildAspect float32 // C: gi_child_aspect

	// C: LIST_ENTRY(glw_image) gi_link
	giLinkNext *GlwImage
	giLinkPrev **GlwImage

	giSources []*miscpkg.Rstr // C: rstr_t **gi_sources (NULL-terminated)

	giSwitchCnt int // C: gi_switch_cnt
	giSwitchTgt int // C: gi_switch_tgt

	giMaxIntensity float32 // C: gi_max_intensity

	giGpa GlwProgramArgs // C: gi_gpa

	giFs *miscpkg.Rstr // C: rstr_t *gi_fs
}
