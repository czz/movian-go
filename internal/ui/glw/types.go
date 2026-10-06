package glw

// C: src/ui/glw/glw.h — canonical 1:1 port.
// Every constant, enum, struct, inline helper and macro from the header.

import (
	"sync"
	"unsafe"

	archpkg "github.com/czz/movian-go/internal/arch"
	backendcore "github.com/czz/movian-go/internal/backend/core"
	eventpkg "github.com/czz/movian-go/internal/event"
	imagepkg "github.com/czz/movian-go/internal/image"
	miscpkg "github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
	settingscore "github.com/czz/movian-go/internal/settings"
)

// C: typedef struct glw_cached_view (glw_view.c:101-112)
type GlwCachedView struct {
	gcvLinkNext *GlwCachedView  // C: LIST_ENTRY gcv_link
	gcvLinkPrev **GlwCachedView //
	gcvSof      *Token          // C: gcv_sof
	gcvURL      *miscpkg.Rstr   // C: gcv_url
	gcvAlturl   *miscpkg.Rstr   // C: gcv_alturl

	gcvError     string // C: gcv_error
	gcvErrorFile string // C: gcv_error_file
	gcvErrorLine int    // C: gcv_error_line
	gcvRefcount  int    // C: gcv_refcount
	gcvLoaded    int    // C: gcv_loaded
}

// C: typedef struct glw_view_load_request (glw_view.c:233-240)
type GlwViewLoadRequest struct {
	linkNext *GlwViewLoadRequest  // C: TAILQ_ENTRY link
	linkPrev **GlwViewLoadRequest //
	url      *miscpkg.Rstr        // C: url
	alturl   *miscpkg.Rstr        // C: alturl
	w        *Glw                 // C: w
	scope    *glwScope            // C: scope
	gcv      *GlwCachedView       // C: gcv
}

// GlwTextBitmap — C: glw_text_bitmap_t (glw_text_bitmap.c:48-118)
type GlwTextBitmap struct {
	w Glw // C: struct glw w

	// C: TAILQ_ENTRY(glw_text_bitmap) gtb_workq_link
	gtbWorkqLinkNext *GlwTextBitmap
	gtbWorkqLinkPrev **GlwTextBitmap
	// C: LIST_ENTRY(glw_text_bitmap) gtb_global_link
	gtbGlobalLinkNext *GlwTextBitmap
	gtbGlobalLinkPrev **GlwTextBitmap

	gtbImage *imagepkg.Image // C: image_t *gtb_image

	gtbCaption     string                 // C: char *gtb_caption
	gtbCaptionSet  bool                   // Go: gtb_caption NULL vs strdup("")
	gtbFont        *miscpkg.Rstr          // C: rstr_t *gtb_font
	gtbSub         *propcore.Subscription // C: prop_sub_t *gtb_sub
	gtbP           *propcore.Prop         // C: prop_t *gtb_p
	gtbDescription string                 // C: char *gtb_description

	gtbCaptionType int // C: prop_str_type_t gtb_caption_type

	gtbTexture GlwBackendTexture // C: glw_backend_texture_t gtb_texture

	gtbTextRenderer       glwRenderer // C: glw_renderer_t gtb_text_renderer
	gtbCursorRenderer     glwRenderer // C: glw_renderer_t gtb_cursor_renderer
	gtbBackgroundRenderer glwRenderer // C: glw_renderer_t gtb_background_renderer

	gtbUcBuffer        []uint32 // C: uint32_t *gtb_uc_buffer
	gtbCursorAlpha     float32  // C: gtb_cursor_alpha
	gtbBackgroundAlpha float32  // C: gtb_background_alpha
	gtbSizeScale       float32  // C: gtb_size_scale
	gtbColor           glwRgb   // C: gtb_color
	gtbBackgroundColor glwRgb   // C: gtb_background_color

	gtbFlags int // C: gtb_flags (GTB_*)

	gtbState int // C: gtb_state (GTB_IDLE..GTB_VALID)

	gtbSavedWidth  int16 // C: gtb_saved_width
	gtbSavedHeight int16 // C: gtb_saved_height

	gtbEditPtr int16 // C: gtb_edit_ptr

	gtbPadding [4]int16 // C: gtb_padding[4]

	gtbUcLen       int16 // C: gtb_uc_len
	gtbUcSize      int16 // C: gtb_uc_size
	gtbMaxlines    int16 // C: gtb_maxlines
	gtbDefaultSize int16 // C: gtb_default_size
	gtbMaxWidth    int16 // C: gtb_max_width

	gtbMargin int16 // C: gtb_margin

	gtbPendingUpdates uint8 // C: gtb_pending_updates

	gtbFrozen          bool // C: gtb_frozen : 1
	gtbPaintCursor     bool // C: gtb_paint_cursor : 1
	gtbUpdateCursor    bool // C: gtb_update_cursor : 1
	gtbNeedLayout      bool // C: gtb_need_layout : 1
	gtbDeferredRealize bool // C: gtb_deferred_realize : 1
	gtbCaptionDirty    bool // C: gtb_caption_dirty : 1
}

// C: glw_backend_root_t (glw_opengl.h:100-142) — OpenGL backend state.
// C LIST_HEAD gbr_programs → slice. gbr_vdpau_dev + gbr_glVDPAU*NV proc
// pointers are gone with VDPAU itself.
type glwBackendRootT struct {
	gbrCurrent          *glwProgram   // C: gbr_current
	gbrUseStencilBuffer int           // C: gbr_use_stencil_buffer
	gbrPrograms         []*glwProgram // C: gbr_programs (LIST_HEAD)

	// Video renderer programs
	gbrYuv2rgb1f *glwProgram // C: gbr_yuv2rgb_1f
	gbrYuv2rgb2f *glwProgram // C: gbr_yuv2rgb_2f
	gbrRgb2rgb1f *glwProgram // C: gbr_rgb2rgb_1f
	gbrRgb2rgb2f *glwProgram // C: gbr_rgb2rgb_2f
	gbrYc2rgb1f  *glwProgram // C: gbr_yc2rgb_1f
	gbrYc2rgb2f  *glwProgram // C: gbr_yc2rgb_2f

	// NV12 dmabuf-imported video (VAAPI-EGL zero-copy) — extension, no
	// C counterpart. yc2rgb variants sampling UV from .r/.g (GR88)
	// instead of .r/.a (LUMINANCE_ALPHA).
	gbrNv121f *glwProgram
	gbrNv122f *glwProgram

	// UI renderer programs
	gbrRendererTex            *glwProgram // C: gbr_renderer_tex
	gbrRendererTexStencil     *glwProgram // C: gbr_renderer_tex_stencil
	gbrRendererTexBlur        *glwProgram // C: gbr_renderer_tex_blur
	gbrRendererTexStencilBlur *glwProgram // C: gbr_renderer_tex_stencil_blur
	gbrRendererFlat           *glwProgram // C: gbr_renderer_flat
	gbrRendererFlatStencil    *glwProgram // C: gbr_renderer_flat_stencil

	gbrVbo uint32 // C: GLuint gbr_vbo
}

// C: typedef struct glw_rect { int x1, x2, y1, y2; } glw_rect_t;
// glwRecStateT — C: static hts_mutex_t glw_rec_mutex + glw_recs list
// (glw_rec.c) — folded into glwDeps; declared untagged so glw.go can
// embed it regardless of the `glwrec` build tag.
type glwRecStateT struct {
	mu   sync.Mutex
	recs glwRecList
}

// ---------------------------------------------------------------------------
// C: Helpers (glw.h:128-135)
func glwLerp(a, y0, y1 float32) float32 { return y0 + a*(y1-y0) } // GLW_LERP

func glwMin(a, b float32) float32 { // GLW_MIN
	if a < b {
		return a
	}
	return b
}

func glwMax(a, b float32) float32 { // GLW_MAX
	if a > b {
		return a
	}
	return b
}

func glwDeg2rad(a float32) float32 { return a * 3.141592653589793 * 2.0 / 360.0 } // GLW_DEG2RAD

func glwRescale(x, min, max float32) float32 { return (x - min) / (max - min) } // GLW_RESCALE

func glwClamp(x, min, max float32) float32 { return glwMin(glwMax(x, min), max) } // GLW_CLAMP

// ---------------------------------------------------------------------------
// C: Flags for dynamic evaluation of view statements (glw.h:139-146)
const (
	GLW_VIEW_EVAL_LAYOUT     = 0x1   // GLW_VIEW_EVAL_LAYOUT
	GLW_VIEW_EVAL_ACTIVE     = 0x2   // GLW_VIEW_EVAL_ACTIVE
	GLW_VIEW_EVAL_FHP_CHANGE = 0x4   // GLW_VIEW_EVAL_FHP_CHANGE
	GLW_VIEW_EVAL_OTHER      = 0x8   // GLW_VIEW_EVAL_OTHER
	GLW_VIEW_EVAL_EM         = 0x10  // GLW_VIEW_EVAL_EM
	GLW_VIEW_EVAL_PROP       = 0x100 // GLW_VIEW_EVAL_PROP
	GLW_VIEW_EVAL_KEEP       = 0x200 // GLW_VIEW_EVAL_KEEP
)

// ---------------------------------------------------------------------------
// C: Attributes (glw.h:150-203)
type glwAttribute int

const (
	glwAttribEnd              glwAttribute = iota // GLW_ATTRIB_END
	glwAttribValue                                // GLW_ATTRIB_VALUE
	glwAttribArgs                                 // GLW_ATTRIB_ARGS
	glwAttribPropSelf                             // GLW_ATTRIB_PROP_SELF
	glwAttribPropItemModel                        // GLW_ATTRIB_PROP_ITEM_MODEL
	glwAttribPropParentModel                      // GLW_ATTRIB_PROP_PARENT_MODEL
	glwAttribAngle                                // GLW_ATTRIB_ANGLE
	glwAttribMode                                 // GLW_ATTRIB_MODE
	glwAttribTime                                 // GLW_ATTRIB_TIME
	glwAttribTransitionTime                       // GLW_ATTRIB_TRANSITION_TIME
	glwAttribIntStep                              // GLW_ATTRIB_INT_STEP
	glwAttribIntMin                               // GLW_ATTRIB_INT_MIN
	glwAttribIntMax                               // GLW_ATTRIB_INT_MAX
	glwAttribTransitionEffect                     // GLW_ATTRIB_TRANSITION_EFFECT
	glwAttribExpansion                            // GLW_ATTRIB_EXPANSION
	glwAttribChildAspect                          // GLW_ATTRIB_CHILD_ASPECT
	glwAttribChildTilesX                          // GLW_ATTRIB_CHILD_TILES_X
	glwAttribChildTilesY                          // GLW_ATTRIB_CHILD_TILES_Y
	glwAttribAlphaEdges                           // GLW_ATTRIB_ALPHA_EDGES
	glwAttribPriority                             // GLW_ATTRIB_PRIORITY
	glwAttribFill                                 // GLW_ATTRIB_FILL
	glwAttribSpacing                              // GLW_ATTRIB_SPACING
	glwAttribXSpacing                             // GLW_ATTRIB_X_SPACING
	glwAttribYSpacing                             // GLW_ATTRIB_Y_SPACING
	glwAttribSaturation                           // GLW_ATTRIB_SATURATION
	glwAttribCenter                               // GLW_ATTRIB_CENTER
	glwAttribAlphaFallOff                         // GLW_ATTRIB_ALPHA_FALLOFF
	glwAttribBlurFallOff                          // GLW_ATTRIB_BLUR_FALLOFF
	glwAttribRadius                               // GLW_ATTRIB_RADIUS
	glwAttribAudioVolume                          // GLW_ATTRIB_AUDIO_VOLUME
	glwAttribAspect                               // GLW_ATTRIB_ASPECT
	glwAttribChildScale                           // GLW_ATTRIB_CHILD_SCALE
	glwAttribParentURL                            // GLW_ATTRIB_PARENT_URL
	glwAttribAlphaSelf                            // GLW_ATTRIB_ALPHA_SELF
	glwAttribSizeScale                            // GLW_ATTRIB_SIZE_SCALE
	glwAttribSize                                 // GLW_ATTRIB_SIZE
	glwAttribMaxWidth                             // GLW_ATTRIB_MAX_WIDTH
	glwAttribMaxLines                             // GLW_ATTRIB_MAX_LINES
	glwAttribRGB                                  // GLW_ATTRIB_RGB
	glwAttribColor1                               // GLW_ATTRIB_COLOR1
	glwAttribColor2                               // GLW_ATTRIB_COLOR2
	glwAttribScaling                              // GLW_ATTRIB_SCALING
	glwAttribTranslation                          // GLW_ATTRIB_TRANSLATION
	glwAttribRotation                             // GLW_ATTRIB_ROTATION
	glwAttribPlane                                // GLW_ATTRIB_PLANE
	glwAttribBorder                               // GLW_ATTRIB_BORDER
	glwAttribPadding                              // GLW_ATTRIB_PADDING
	glwAttribFont                                 // GLW_ATTRIB_FONT
	glwAttribTentativeValue                       // GLW_ATTRIB_TENTATIVE_VALUE
	glwAttribBackgroundColor                      // GLW_ATTRIB_BACKGROUND_COLOR
	glwAttribBackgroundAlpha                      // GLW_ATTRIB_BACKGROUND_ALPHA
	glwAttribNum                                  // GLW_ATTRIB_num
)

// ---------------------------------------------------------------------------
// C: Text flags (glw.h:208-215)
const (
	gtbPassword        = 0x1   // GTB_PASSWORD
	gtbEllipsize       = 0x2   // GTB_ELLIPSIZE
	gtbBold            = 0x4   // GTB_BOLD
	gtbItalic          = 0x8   // GTB_ITALIC
	gtbOutline         = 0x10  // GTB_OUTLINE
	gtbPermanentCursor = 0x20  // GTB_PERMANENT_CURSOR
	gtbOskPassword     = 0x40  // GTB_OSK_PASSWORD
	gtbFileRequest     = 0x80  // GTB_FILE_REQUEST
	gtbDirRequest      = 0x100 // GTB_DIR_REQUEST
)

// ---------------------------------------------------------------------------
// C: Image flags (glw.h:221-238)
const (
	glwImageCornerTopleft     = 0x1     // GLW_IMAGE_CORNER_TOPLEFT
	glwImageCornerTopright    = 0x2     // GLW_IMAGE_CORNER_TOPRIGHT
	glwImageCornerBottomleft  = 0x4     // GLW_IMAGE_CORNER_BOTTOMLEFT
	glwImageCornerBottomright = 0x8     // GLW_IMAGE_CORNER_BOTTOMRIGHT
	glwImageTexOverlap        = 0xff    // GLW_IMAGE_TEX_OVERLAP
	glwImageFixedSize         = 0x100   // GLW_IMAGE_FIXED_SIZE
	glwImageBevelLeft         = 0x200   // GLW_IMAGE_BEVEL_LEFT
	glwImageBevelTop          = 0x400   // GLW_IMAGE_BEVEL_TOP
	glwImageBevelRight        = 0x800   // GLW_IMAGE_BEVEL_RIGHT
	glwImageBevelBottom       = 0x1000  // GLW_IMAGE_BEVEL_BOTTOM
	glwImageSetAspect         = 0x2000  // GLW_IMAGE_SET_ASPECT
	glwImageAdditive          = 0x4000  // GLW_IMAGE_ADDITIVE
	glwImageBorderOnly        = 0x8000  // GLW_IMAGE_BORDER_ONLY
	glwImageBorderLeft        = 0x10000 // GLW_IMAGE_BORDER_LEFT
	glwImageBorderRight       = 0x20000 // GLW_IMAGE_BORDER_RIGHT
)

// ---------------------------------------------------------------------------
// C: glw_pointer_event_type_t (glw.h:247-264)
type glwPointerEventType int

const (
	glwPointerLeftPress glwPointerEventType = iota // GLW_POINTER_LEFT_PRESS
	glwPointerLeftRelease
	glwPointerRightPress
	glwPointerRightRelease
	glwPointerTouchStart
	glwPointerTouchMove
	glwPointerTouchEnd
	glwPointerTouchCancel
	glwPointerMotionUpdate  // GLW_POINTER_MOTION_UPDATE
	glwPointerMotionRefresh // GLW_POINTER_MOTION_REFRESH
	glwPointerFocusMotion   // GLW_POINTER_FOCUS_MOTION
	glwPointerFineScroll    // GLW_POINTER_FINE_SCROLL
	glwPointerScroll        // GLW_POINTER_SCROLL
	glwPointerGone          // GLW_POINTER_GONE
)

// C: typedef struct glw_pointer_event { ... } glw_pointer_event_t; (glw.h:266-275)
type glwPointerEventT struct {
	screenX float32
	screenY float32
	localX  float32
	localY  float32
	deltaX  float32
	deltaY  float32
	typ     glwPointerEventType // C: type
	ts      int64
}

// ---------------------------------------------------------------------------
// C: prop_root_t (prop.h:155-158) — { prop_t *p; const char *name; }
type propRootT = propcore.PropRoot

// ---------------------------------------------------------------------------
// C: typedef struct glw_scope (glw.h:283-301)
const (
	glwRootSelf       = 0 // GLW_ROOT_SELF
	glwRootParent     = 1 // GLW_ROOT_PARENT
	glwRootView       = 2 // GLW_ROOT_VIEW
	glwRootArgs       = 3 // GLW_ROOT_ARGS
	glwRootClone      = 4 // GLW_ROOT_CLONE
	glwRootCore       = 5 // GLW_ROOT_CORE
	glwRootParentview = 6 // GLW_ROOT_PARENTVIEW
	glwRootStatic     = 7 // GLW_ROOT_static
)

type glwScope struct {
	gsRefcount int
	gsNumRoots int

	gsBackend *backendcore.Backend // C: struct backend *gs_backend
	gsEvent   *eventpkg.Event      // C: struct event *gs_event

	gsRoots [glwRootStatic]propRootT
}

// ---------------------------------------------------------------------------
// C: glw_signal_t (glw.h:315-394)
type glwSignal int

const (
	glwSignalNone                        glwSignal = iota // GLW_SIGNAL_NONE
	glwSignalDestroy                                      // GLW_SIGNAL_DESTROY
	glwSignalActive                                       // GLW_SIGNAL_ACTIVE
	glwSignalInactive                                     // GLW_SIGNAL_INACTIVE
	glwSignalChildCreated                                 // GLW_SIGNAL_CHILD_CREATED
	glwSignalChildDestroyed                               // GLW_SIGNAL_CHILD_DESTROYED
	glwSignalChildMoved                                   // GLW_SIGNAL_CHILD_MOVED
	glwSignalFocusChildInteractive                        // GLW_SIGNAL_FOCUS_CHILD_INTERACTIVE
	glwSignalFocusChildAutomatic                          // GLW_SIGNAL_FOCUS_CHILD_AUTOMATIC
	glwSignalSliderMetrics                                // GLW_SIGNAL_SLIDER_METRICS
	glwSignalScroll                                       // GLW_SIGNAL_SCROLL
	glwSignalFhpPathChanged                               // GLW_SIGNAL_FHP_PATH_CHANGED
	glwSignalChildConstraintsChanged                      // GLW_SIGNAL_CHILD_CONSTRAINTS_CHANGED
	glwSignalChildHidden                                  // GLW_SIGNAL_CHILD_HIDDEN
	glwSignalChildUnhidden                                // GLW_SIGNAL_CHILD_UNHIDDEN
	glwSignalCanScrollChanged                             // GLW_SIGNAL_CAN_SCROLL_CHANGED
	glwSignalFullwindowConstraintChanged                  // GLW_SIGNAL_FULLWINDOW_CONSTRAINT_CHANGED
	glwSignalStatusChanged                                // GLW_SIGNAL_STATUS_CHANGED
	glwSignalReselectChanged                              // GLW_SIGNAL_RESELECT_CHANGED
	glwSignalMove                                         // GLW_SIGNAL_MOVE
	glwSignalWrapCheck                                    // GLW_SIGNAL_WRAP_CHECK
	glwSignalNum                                          // GLW_SIGNAL_num
)

// C: typedef struct { float knob_size; float position; } glw_slider_metrics_t;
type glwSliderMetrics struct {
	knobSize float32
	position float32
}

// C: typedef struct { float value; } glw_scroll_t;
type glwScroll struct {
	value float32
}

// C: typedef struct { int steps; int did_move; } glw_move_op_t;
type glwMoveOp struct {
	steps   int
	didMove int
}

// C: typedef int (glw_callback_t)(struct glw *w, void *opaque,
//
//	glw_signal_t signal, void *value);
type glwCallback func(w *Glw, opaque any, signal glwSignal, value any) int

// C: glw_orientation_t (glw.h:416-420)
type glwOrientation int

const (
	glwOrientationNone glwOrientation = iota
	glwOrientationHorizontal
	glwOrientationVertical
)

// C: glw_widget_status_t (glw.h:422-427)
type glwWidgetStatus int

const (
	glwStatusIdle glwWidgetStatus = iota
	glwStatusLoading
	glwStatusLoaded
	glwStatusError
)

// ---------------------------------------------------------------------------
// C: glw_class_t — the widget class vtable (glw.h:435-765)
const (
	glwNavigationSearchBoundary = 0x1 // GLW_NAVIGATION_SEARCH_BOUNDARY
	glwCanHideChilds            = 0x2 // GLW_CAN_HIDE_CHILDS
	glwUnconstrained            = 0x4 // GLW_UNCONSTRAINED
	glwDrivePagination          = 0x8 // GLW_DRIVE_PAGINATION
)

// gc_set_* return values (glw.h:479-482)
const (
	glwSetNotResponding    = -1 // GLW_SET_NOT_RESPONDING
	glwSetNoChange         = 0  // GLW_SET_NO_CHANGE
	glwSetRerenderRequired = 1  // GLW_SET_RERENDER_REQUIRED
	glwSetLayoutOnly       = 2  // GLW_SET_LAYOUT_ONLY
)

type glwClass struct {
	gcName           string // C: gc_name
	gcName2          string // C: gc_name2
	gcInstanceSize   int    // C: gc_instance_size
	gcParentDataSize int    // C: gc_parent_data_size
	// Go addition: C zero-allocates gc_parent_data_size trailing bytes per
	// child; Go allocates a typed item via this ctor instead (same
	// semantics — fresh zeroed item per child of this parent class).
	gcNewParentData func() any
	gcFlags         int // C: gc_flags

	// gcNew allocates the class instance and returns its embedded Glw.
	// C: glw_create() does calloc(1, gc_instance_size + parent_data_size) —
	// every C class struct embeds `glw_t w` first, so the allocation IS the
	// widget and class code recovers the subtype by casting. Go reproduces
	// the identical layout by allocating the class struct (Glw embedded
	// first); gc_instance_size is kept for canonical parity.
	gcNew func(parent *Glw) *Glw

	gcCtor func(w *Glw) // C: gc_ctor

	gcSetInt     func(w *Glw, a glwAttribute, value int, gs *GlwStyle) int           // C: gc_set_int
	gcSetFloat   func(w *Glw, a glwAttribute, value float32, gs *GlwStyle) int       // C: gc_set_float
	gcSetEm      func(w *Glw, a glwAttribute, value float32) int                     // C: gc_set_em
	gcSetRstr    func(w *Glw, a glwAttribute, value *miscpkg.Rstr, gs *GlwStyle) int // C: gc_set_rstr
	gcSetProp    func(w *Glw, a glwAttribute, p *propcore.Prop) int                  // C: gc_set_prop
	gcBindToId   func(w *Glw, id string) int                                         // C: gc_bind_to_id
	gcSetFloat3  func(w *Glw, a glwAttribute, vector []float32, gs *GlwStyle) int    // C: gc_set_float3
	gcSetFloat4  func(w *Glw, a glwAttribute, vector []float32) int                  // C: gc_set_float4
	gcSetInt16_4 func(w *Glw, a glwAttribute, v []int16, gs *GlwStyle) int           // C: gc_set_int16_4

	gcSetIntUnresolved   func(w *Glw, a string, value int, gs *GlwStyle) int           // C: gc_set_int_unresolved
	gcSetFloatUnresolved func(w *Glw, a string, value float32, gs *GlwStyle) int       // C: gc_set_float_unresolved
	gcSetRstrUnresolved  func(w *Glw, a string, value *miscpkg.Rstr, gs *GlwStyle) int // C: gc_set_rstr_unresolved

	gcRender             func(w *Glw, rc *glwRctx)               // C: gc_render
	gcLayout             func(w *Glw, rc *glwRctx)               // C: gc_layout
	gcRetireChild        func(w *Glw, c *Glw)                    // C: gc_retire_child
	gcDtor               func(w *Glw)                            // C: gc_dtor
	gcNewframe           func(w *Glw, flags int)                 // C: gc_newframe
	gcStatus             func(w *Glw) glwWidgetStatus            // C: gc_status
	gcPrimaryColor       func(w *Glw, rgb *float32) int          // C: gc_primary_color
	gcSuggestFocus       func(w *Glw, c *Glw)                    // C: gc_suggest_focus
	gcSendEvent          func(w *Glw, e *eventpkg.Event) int     // C: gc_send_event
	gcBubbleEvent        func(w *Glw, e *eventpkg.Event) int     // C: gc_bubble_event
	gcPointerEventFilter func(w *Glw, gpe *glwPointerEventT) int // C: gc_pointer_event_filter
	gcPointerEvent       func(w *Glw, gpe *glwPointerEventT) int // C: gc_pointer_event

	gcSignalHandler glwCallback // C: gc_signal_handler

	gcGetText func(w *Glw) string // C: gc_get_text

	gcDefaultAlignment int // C: gc_default_alignment

	gcSelectChild    func(w *Glw, c *Glw, origin *propcore.Prop) int // C: gc_select_child
	gcDetachControl  func(w *Glw, on int)                            // C: gc_detach_control
	gcCanSelectChild func(w *Glw, next int) int                      // C: gc_can_select_child
	gcGetRctx        func(w *Glw, rc *glwRctx)                       // C: gc_get_rctx

	gcModImageFlags   func(w *Glw, set int, clr int, gs *GlwStyle) // C: gc_mod_image_flags
	gcModVideoFlags   func(w *Glw, set int, clr int)               // C: gc_mod_video_flags
	gcModTextFlags    func(w *Glw, set int, clr int, gs *GlwStyle) // C: gc_mod_text_flags
	gcModFlags2       func(w *Glw, set int, clr int)               // C: gc_mod_flags2
	gcModFlags2Always func(w *Glw, set int, clr int, gs *GlwStyle) // C: gc_mod_flags2_always

	gcSetCaption func(w *Glw, str string, typ int) // C: gc_set_caption
	gcUpdateText func(w *Glw, str string)          // C: gc_update_text
	gcSetFs      func(w *Glw, str *miscpkg.Rstr)   // C: gc_set_fs

	gcBindToProperty func(w *Glw, scope *glwScope, pname []string) // C: gc_bind_to_property

	gcSetSource  func(w *Glw, url *miscpkg.Rstr, flags int, gs *GlwStyle) // C: gc_set_source
	gcSetAlt     func(w *Glw, url *miscpkg.Rstr)                          // C: gc_set_alt
	gcSetSources func(w *Glw, urls []*miscpkg.Rstr)                       // C: gc_set_sources
	gcSetHow     func(w *Glw, how string)                                 // C: gc_set_how
	gcSetDesc    func(w *Glw, desc string)                                // C: gc_set_desc

	gcSetFocusWeight func(w *Glw, f float32, gs *GlwStyle) // C: gc_set_focus_weight
	gcSetAlpha       func(w *Glw, f float32, gs *GlwStyle) // C: gc_set_alpha
	gcSetBlur        func(w *Glw, f float32, gs *GlwStyle) // C: gc_set_blur
	gcSetWeight      func(w *Glw, f float32, gs *GlwStyle) // C: gc_set_weight
	gcSetWidth       func(w *Glw, v int, gs *GlwStyle)     // C: gc_set_width
	gcSetHeight      func(w *Glw, v int, gs *GlwStyle)     // C: gc_set_height
	gcSetAlign       func(w *Glw, v int, gs *GlwStyle)     // C: gc_set_align
	gcSetHidden      func(w *Glw, v int, gs *GlwStyle)     // C: gc_set_hidden
	gcSetMargin      func(w *Glw, v []int16, gs *GlwStyle) // C: gc_set_margin

	gcFreeze func(w *Glw) // C: gc_freeze
	gcThaw   func(w *Glw) // C: gc_thaw

	gcGetIdentity      func(w *Glw, tmp []byte) string // C: gc_get_identity
	gcFindVisibleChild func(w *Glw) *Glw               // C: gc_find_visible_child

	// C: LIST_ENTRY(glw_class) gc_link;
	gcLinkNext *glwClass
	gcLinkPrev **glwClass
}

// ---------------------------------------------------------------------------
// C: glw_root_t — GLW root context (glw.h:779-1058)
// backdrop — C: typedef struct backdrop (rpi_main.c:283-289).
// Declared here (not in the rpi-tagged file) so glwRoot can hold it.
type backdrop struct {
	url      *miscpkg.Rstr // C: rstr_t *url
	alpha    float32       // C: float alpha
	mark     bool          // C: int mark
	refcount int32         // C: atomic_t refcount
}

type glwRoot struct {
	grPropUi   *propcore.Prop // C: gr_prop_ui
	grPropNav  *propcore.Prop // C: gr_prop_nav
	grPropCore *propcore.Prop // C: gr_prop_core

	grPropDispatcher func(pc *propcore.Courier, timeout int) // C: gr_prop_dispatcher
	grPropMaxtime    int                                     // C: gr_prop_maxtime

	grRefcount int32 // C: atomic_t gr_refcount

	grKeyboardMode        int                    // C: gr_keyboard_mode
	grSkinScaleAdjustment int                    // C: gr_skin_scale_adjustment
	grReduceCpu           int                    // C: gr_reduce_cpu
	grEvsub               *propcore.Subscription // C: gr_evsub
	grScalesub            *propcore.Subscription // C: gr_scalesub

	grTokenPool        *miscpkg.Pool // C: gr_token_pool
	grClonePool        *miscpkg.Pool // C: gr_clone_pool
	grStyleBindingPool *miscpkg.Pool // C: gr_style_binding_pool
	grGemIdTally       int           // C: gr_gem_id_tally
	grFrames           int           // C: gr_frames

	grStartFlags int  // C: gr_init_flags
	grUniverse   *Glw // C: gr_universe

	grViews glwCachedViewList // C: LIST_HEAD(, glw_cached_view) gr_views

	grSkin string // C: char *gr_skin

	grThread  *archpkg.Thread   // C: gr_thread
	grMutex   sync.Mutex        // C: hts_mutex_t gr_mutex
	grCourier *propcore.Courier // C: gr_courier

	// C: rpi_main.c backdrop statics — per-root here (cond signals on
	// gr_mutex; the loader thread takes gr as aux).
	bdRun     bool        // C: static backdrop_loader_run
	bdCond    *sync.Cond  // C: static backdrop_loader_cond
	bdList    []*backdrop // C: static backdrop_list
	bdCurrent *backdrop   // C: static backdrop_current
	bdPending *backdrop   // C: static backdrop_pending

	grAllStyles glwStyleList // C: gr_all_styles

	grStyleTally int // C: gr_style_tally

	grDestroyerQueue glwQueue // C: gr_destroyer_queue

	grFrameduration int     // C: gr_frameduration
	grFramerate     float32 // C: gr_framerate

	grActiveList      glwHead // C: gr_active_list
	grActiveFlushList glwHead // C: gr_active_flush_list
	grActiveDummyList glwHead // C: gr_active_dummy_list
	grEveryFrameList  glwHead // C: gr_every_frame_list

	grWidth  int // C: gr_width
	grHeight int // C: gr_height

	grPropWidth  *propcore.Prop // C: gr_prop_width
	grPropHeight *propcore.Prop // C: gr_prop_height
	grPropAspect *propcore.Prop // C: gr_prop_aspect

	grMouseX     float32 // C: gr_mouse_x
	grMouseY     float32 // C: gr_mouse_y
	grMouseValid int     // C: gr_mouse_valid

	grVideoDecoders    glwVideoList // C: gr_video_decoders
	grUiStart          int64        // C: gr_ui_start
	grFrameStart       int64        // C: gr_frame_start
	grFrameStartAvtime int64        // C: gr_frame_start_avtime
	grIsFullscreen     int          // C: gr_is_fullscreen

	grFramerateAvg [16]int64 // C: gr_framerate_avg[16]

	grTimeUsec uint64  // C: gr_time_usec
	grTimeSec  float64 // C: gr_time_sec

	grNeedRefresh      int   // C: gr_need_refresh
	grScheduledRefresh int64 // C: gr_scheduled_refresh

	// Screensaver / User activity (glw.h:854-862)
	grLastActivityAt         int64                  // C: gr_last_activity_at
	grScreensaverResetAt     int64                  // C: gr_screensaver_reset_at
	grScreensaverForceEnable int                    // C: gr_screensaver_force_enable
	grScreensaverActive      *propcore.Prop         // C: gr_screensaver_active
	grInhibitScreensaver     int                    // C: gr_inhibit_screensaver
	grDisableScreensaverSub  *propcore.Subscription // C: gr_disable_screensaver_sub

	// View loader (glw.h:868-872)
	grViewLoaderThread *archpkg.Thread         // C: gr_view_loader_thread
	grViewLoaderRun    int                     // C: gr_view_loader_run
	grViewLoaderCond   sync.Cond               // C: hts_cond_t gr_view_loader_cond
	grViewLoadRequests glwViewLoadRequestQueue // C: gr_view_load_requests
	grViewEvalRequests glwViewLoadRequestQueue // C: gr_view_eval_requests

	// Font renderer (glw.h:878-886)
	grGtbs              glwTextBitmapList  // C: LIST_HEAD(, glw_text_bitmap) gr_gtbs
	grGtbRenderQueue    glwTextBitmapQueue // C: TAILQ_HEAD gr_gtb_render_queue
	grGtbDimQueue       glwTextBitmapQueue // C: TAILQ_HEAD gr_gtb_dim_queue
	grGtbWorkCond       sync.Cond          // C: gr_gtb_work_cond
	grFontThread        *archpkg.Thread    // C: gr_font_thread
	grFontThreadRunning int                // C: gr_font_thread_running

	grDefaultFont *miscpkg.Rstr // C: gr_default_font
	grFontDomain  int           // C: gr_font_domain

	// Image/Texture loader (glw.h:891-917)
	grTexThreadsRunning int                                // C: gr_tex_threads_running
	grTexThreads        [glwTextureThreads]*archpkg.Thread // C: gr_tex_threads[6]
	grIcons             glwImageList                       // C: LIST_HEAD(, glw_image) gr_icons
	grTexLoadCond       sync.Cond                          // C: gr_tex_load_cond
	grTexLoadQueue      [lqNum]glwLoadableTextureQueue     // C: gr_tex_load_queue[LQ_num]
	grTexActiveList     glwLoadableTextureList             // C: gr_tex_active_list
	grTexFlushList      glwLoadableTextureList             // C: gr_tex_flush_list
	grTexRelQueue       glwLoadableTextureQueue            // C: gr_tex_rel_queue
	grTexStash          [2]struct {                        // C: gr_tex_stash[2]
		q     glwLoadableTextureQueue
		size  int
		limit int
	}
	grTexList glwLoadableTextureList // C: gr_tex_list

	// Root focus leader (glw.h:922-948)
	grPointerGrab       *Glw // C: gr_pointer_grab
	grPointerHover      *Glw // C: gr_pointer_hover
	grPointerPress      *Glw // C: gr_pointer_press
	grPointerGrabScroll *Glw // C: gr_pointer_grab_scroll

	grPointerPressTime int64 // C: gr_pointer_press_time

	grTouchMode   int     // C: gr_touch_mode
	grTouchStartX float32 // C: gr_touch_start_x
	grTouchStartY float32 // C: gr_touch_start_y
	grTouchMoveX  float32 // C: gr_touch_move_x
	grTouchMoveY  float32 // C: gr_touch_move_y
	grTouchEndX   float32 // C: gr_touch_end_x
	grTouchEndY   float32 // C: gr_touch_end_y

	grCurrentFocus           *Glw           // C: gr_current_focus
	grLastFocus              *Glw           // C: gr_last_focus
	grDelayedFocusLeave      int            // C: gr_delayed_focus_leave
	grLastFocusedInteractive *propcore.Prop // C: gr_last_focused_interactive
	grPointerVisible         *propcore.Prop // C: gr_pointer_visible
	grFocusWork              int            // C: gr_focus_work

	grCurrentCursor      *Glw                                   // C: gr_current_cursor
	grCursorFocusTracker func(w *Glw, rc *glwRctx, cursor *Glw) // C: gr_cursor_focus_tracker

	grPendingFocus *miscpkg.Rstr // C: gr_pending_focus

	// Backend specifics (glw.h:953-955)
	grBe               glwBackendRootT                    // C: gr_be
	grBeRenderUnlocked func(gr *glwRoot)                  // C: gr_be_render_unlocked
	grBrReadPixels     func(gr *glwRoot) *imagepkg.Pixmap // C: gr_br_read_pixels

	// Settings (glw.h:959-974)
	grUnderscanV           int                   // C: gr_underscan_v
	grUnderscanH           int                   // C: gr_underscan_h
	grCurrentSize          int                   // C: gr_current_size
	grUserUnderscanChanged int                   // C: gr_user_underscan_changed
	grUserUnderscanH       int                   // C: gr_user_underscan_h
	grUserUnderscanV       int                   // C: gr_user_underscan_v
	grBaseUnderscanV       int                   // C: gr_base_underscan_v
	grBaseUnderscanH       int                   // C: gr_base_underscan_h
	grSettingScreensaver   *settingscore.Setting // C: gr_setting_screensaver

	// Rendering (glw.h:980-1028)
	grClip             [numClipplanes]Vec4    // C: gr_clip
	grClipAlphaOut     [numClipplanes]float32 // C: gr_clip_alpha_out
	grClipSharpnessOut [numClipplanes]float32 // C: gr_clip_sharpness_out
	grActiveClippers   int                    // C: gr_active_clippers
	grNeedSwClip       byte                   // C: gr_need_sw_clip
	// C: NUM_STENCILERS == 0 and NUM_FADERS == 0 — the guarded fields
	// (gr_stencil_*, gr_fader_*) do not exist in this build config.

	grNumRenderJobs      int              // C: gr_num_render_jobs
	grRenderJobsCapacity int              // C: gr_render_jobs_capacity
	grRenderJobs         []GlwRenderJob   // C: gr_render_jobs
	grRenderOrder        []GlwRenderOrder // C: gr_render_order

	grVertexBuffer         []float32 // C: gr_vertex_buffer
	grVertexBufferCapacity int       // C: gr_vertex_buffer_capacity
	grVertexOffset         int       // C: gr_vertex_offset

	grIndexBuffer         []uint16 // C: gr_index_buffer
	grIndexBufferCapacity int      // C: gr_index_buffer_capacity
	grIndexOffset         int      // C: gr_index_offset

	grBlendmode int // C: gr_blendmode
	grFrontface int // C: gr_frontface

	grVtmpBuffer   []float32 // C: gr_vtmp_buffer
	grVtmpCur      int       // C: gr_vtmp_cur
	grVtmpCapacity int       // C: gr_vtmp_capacity

	grRandom int // C: gr_random

	grZmax int // C: gr_zmax

	// On Screen Keyboard (glw.h:1032-1040)
	grOpenOsk    func(gr *glwRoot, title string, str string, w *Glw, password int) // C: gr_open_osk
	grOskWidget  *Glw                                                              // C: gr_osk_widget
	grOskTextSub *propcore.Subscription                                            // C: gr_osk_text_sub
	grOskEvSub   *propcore.Subscription                                            // C: gr_osk_ev_sub
	grOskRevert  string                                                            // C: gr_osk_revert

	// Backdrop render helper (glw.h:1044-1049)
	grCanExternalize int                      // C: gr_can_externalize
	grExternalizeCnt int                      // C: gr_externalize_cnt
	grExternalized   [glwMaxExternalized]*Glw // C: gr_externalized[4]

	grPrivate any // C: void *gr_private
	grWindow  any // C: void *gr_window

	grLeftPressed uint8 // C: gr_left_pressed

	grRec *GlwRec // C: gr_rec
}

// C: #define GLW_MAX_EXTERNALIZED 4
const glwMaxExternalized = 4

// ---------------------------------------------------------------------------
// C: glw_signal_handler_t (glw.h:1110-1115)
type glwSignalHandler struct {
	gshLinkNext    *glwSignalHandler // C: LIST_ENTRY gsh_link
	gshLinkPrev    **glwSignalHandler
	gshFunc        glwCallback // C: gsh_func
	gshOpaque      any         // C: gsh_opaque
	gshDeferRemove int16       // C: gsh_defer_remove
}

// ---------------------------------------------------------------------------
// C: glw_styleset_t (glw.h:1123-1127)
type glwStyleset struct {
	gssRefcount  int         // C: gss_refcount
	gssNumstyles int         // C: gss_numstyles
	gssStyles    []*GlwStyle // C: gss_styles[0] (flexible array)
}

// C: glw_style_binding_t (glw.h:1136-1142)
type glwStyleBinding struct {
	gsbStyleLinkNext  *glwStyleBinding // C: LIST_ENTRY gsb_style_link
	gsbStyleLinkPrev  **glwStyleBinding
	gsbWidgetLinkNext *glwStyleBinding // C: LIST_ENTRY gsb_widget_link
	gsbWidgetLinkPrev **glwStyleBinding
	gsbWidget         *Glw      // C: gsb_widget
	gsbStyle          *GlwStyle // C: gsb_style
	gsbMark           int       // C: gsb_mark
}

// ---------------------------------------------------------------------------
// C: typedef struct glw — the widget (glw.h:1148-1278)
type Glw struct {
	glwClass           *glwClass      // C: glw_class
	glwRoot            *glwRoot       // C: glw_root
	glwScope           *glwScope      // C: glw_scope
	glwOriginatingProp *propcore.Prop // C: glw_originating_prop

	// C: LIST_ENTRY(glw) glw_active_link;
	glwActiveLinkNext *Glw
	glwActiveLinkPrev **Glw

	// C: LIST_ENTRY(glw) glw_every_frame_link;
	glwEveryFrameLinkNext *Glw
	glwEveryFrameLinkPrev **Glw

	glwSignalHandlers glwSignalHandlerList // C: glw_signal_handlers

	glwParent         *Glw // C: glw_parent
	glwParentLinkNext *Glw // C: TAILQ_ENTRY glw_parent_link
	glwParentLinkPrev **Glw
	glwChilds         glwQueue // C: glw_childs

	// C: parent_data — trailing bytes after the child struct sized by
	// parent->gc_parent_data_size (glw_parent_data(w, type) cast). Go:
	// typed item allocated by parent class's gcNewParentData.
	glwParentData any

	// C: TAILQ_ENTRY(glw) glw_render_link;
	glwRenderLinkNext *Glw
	glwRenderLinkPrev **Glw

	glwSelected *Glw // C: glw_selected
	glwFocused  *Glw // C: glw_focused

	glwIdRstr *miscpkg.Rstr // C: glw_id_rstr

	glwEventMaps glwEventMapList // C: glw_event_maps

	glwPropSubscriptions glwPropSubSlist // C: glw_prop_subscriptions

	glwDynamicExpressions *Token // C: glw_dynamic_expressions

	glwMatrix *Mtx // C: glw_matrix

	glwClone *glwClone // C: glw_clone

	// Styling
	glwStyles        *glwStyleset        // C: glw_styles
	glwStyleBindings glwStyleBindingList // C: glw_style_bindings

	glwFile *miscpkg.Rstr // C: glw_file
	glwLine int           // C: glw_line

	glwRefcnt int // C: glw_refcnt

	// Layout contraints
	glwReqSizeX  int16   // C: glw_req_size_x
	glwReqSizeY  int16   // C: glw_req_size_y
	glwReqWeight float32 // C: glw_req_weight

	glwMargin [4]int16 // C: glw_margin[4]

	glwFlags int // C: glw_flags

	glwFlags2 int // C: glw_flags2

	glwAlpha     float32 // C: glw_alpha
	glwSharpness float32 // C: glw_sharpness

	glwFocusWeight float32 // C: glw_focus_weight

	glwZoffset int16 // C: glw_zoffset

	glwAlignment uint8 // C: glw_alignment

	glwDynamicEval uint8 // C: glw_dynamic_eval (GLW_VIEW_EVAL_ flags)
}

// C: glw_flags bits (glw.h:1202-1236)
const (
	glwConstraintX = 0x1 // GLW_CONSTRAINT_X
	glwConstraintY = 0x2 // GLW_CONSTRAINT_Y
	glwConstraintW = 0x4 // GLW_CONSTRAINT_W
	glwConstraintD = 0x8 // GLW_CONSTRAINT_D

	glwConstraintFlags = glwConstraintX | glwConstraintY | glwConstraintW | glwConstraintD // GLW_CONSTRAINT_FLAGS

	glwActive        = 0x10 // GLW_ACTIVE
	glwFloatingFocus = 0x20 // GLW_FLOATING_FOCUS
	glwFocusBlocked  = 0x40 // GLW_FOCUS_BLOCKED
	glwUpdateMetrics = 0x80 // GLW_UPDATE_METRICS

	glwInFocusPath   = 0x100 // GLW_IN_FOCUS_PATH
	glwInPressedPath = 0x200 // GLW_IN_PRESSED_PATH
	glwInHoverPath   = 0x400 // GLW_IN_HOVER_PATH
	glwDestroying    = 0x800 // GLW_DESTROYING

	glwHidden    = 0x1000 // GLW_HIDDEN
	glwRetired   = 0x2000 // GLW_RETIRED
	glwCanScroll = 0x4000 // GLW_CAN_SCROLL
	glwMark      = 0x8000 // GLW_MARK

	glwHaveMargins = 0x10000 // GLW_HAVE_MARGINS
	glwPreloaded   = 0x20000 // GLW_PRELOADED

	glwClipped          = 0x1000000 // GLW_CLIPPED
	glwFhpSpillToChilds = 0x4000000 // GLW_FHP_SPILL_TO_CHILDS

	glwConstraintConfW = 0x10000000  // GLW_CONSTRAINT_CONF_W
	glwConstraintConfX = 0x20000000  // GLW_CONSTRAINT_CONF_X
	glwConstraintConfY = 0x40000000  // GLW_CONSTRAINT_CONF_Y
	glwConstraintConfD = -2147483648 // GLW_CONSTRAINT_CONF_D — (int)0x80000000 in C;
	// written negative so the constant fits int on 32-bit targets (android/arm)
)

// C: glw_flags2 bits (glw.h:1241-1265)
const (
	glw2ConstraintIgnoreX     = glwConstraintX // GLW2_CONSTRAINT_IGNORE_X
	glw2ConstraintIgnoreY     = glwConstraintY // GLW2_CONSTRAINT_IGNORE_Y
	glw2ConstraintIgnoreW     = glwConstraintW // GLW2_CONSTRAINT_IGNORE_W
	glw2ConstraintIgnoreD     = glwConstraintD // GLW2_CONSTRAINT_IGNORE_D
	glw2Enabled               = 0x10           // GLW2_ENABLED
	glw2AlwaysGrabKnob        = 0x40           // GLW2_ALWAYS_GRAB_KNOB
	glw2Autohide              = 0x80           // GLW2_AUTOHIDE
	glw2Shadow                = 0x100          // GLW2_SHADOW
	glw2Autofade              = 0x200          // GLW2_AUTOFADE
	glw2ExpediteSubscriptions = 0x400          // GLW2_EXPEDITE_SUBSCRIPTIONS
	glw2NoInitialTrans        = 0x2000         // GLW2_NO_INITIAL_TRANS
	glw2FocusOnClick          = 0x4000         // GLW2_FOCUS_ON_CLICK
	glw2Autorefocusable       = 0x8000         // GLW2_AUTOREFOCUSABLE
	glw2NavFocusable          = 0x10000        // GLW2_NAV_FOCUSABLE
	glw2Homogenous            = 0x20000        // GLW2_HOMOGENOUS
	glw2Debug                 = 0x40000        // GLW2_DEBUG
	glw2NavWrap               = 0x200000       // GLW2_NAV_WRAP
	glw2AutoFocusLimit        = 0x400000       // GLW2_AUTO_FOCUS_LIMIT
	glw2Cursor                = 0x800000       // GLW2_CURSOR
	glw2PositionalNavigation  = 0x1000000      // GLW2_POSITIONAL_NAVIGATION
	glw2Clickable             = 0x2000000      // GLW2_CLICKABLE
	glw2FhpSpill              = 0x4000000      // GLW2_FHP_SPILL
	glw2SelectOnFocus         = 0x8000000      // GLW2_SELECT_ON_FOCUS
	glw2SelectOnHover         = 0x10000000     // GLW2_SELECT_ON_HOVER
)

// ---------------------------------------------------------------------------
// C: inline helpers (glw.h:1280-1294)
func glwFilterConstraints(w *Glw) int { // glw_filter_constraints
	return (w.glwFlags &^ w.glwFlags2) & glwConstraintFlags
}

func glwReqWidth(w *Glw) int { // glw_req_width
	return int(w.glwReqSizeX) + int(w.glwMargin[0]) + int(w.glwMargin[2])
}

func glwReqHeight(w *Glw) int { // glw_req_height
	return int(w.glwReqSizeY) + int(w.glwMargin[1]) + int(w.glwMargin[3])
}

// C: (glw.h:1297-1299)
const (
	glwFlagKeyboardMode = 0x1 // GLW_INIT_KEYBOARD_MODE
	glwFlagOverscan     = 0x2 // GLW_INIT_OVERSCAN
	glwFlagInFullscreen = 0x4 // GLW_INIT_IN_FULLSCREEN
)

// C: (glw.h:1324-1325) — GLW_REINITIALIZE_VDPAU removed with VDPAU.
const (
	glwNoFramerateUpdate = 0x2 // GLW_NO_FRAMERATE_UPDATE
)

// C: macros (glw.h:1369-1378)
func glwIsFocusable(w *Glw) bool { return w.glwFocusWeight > 0 } // glw_is_focusable

func glwIsFocusableOrClickable(w *Glw) bool { // glw_is_focusable_or_clickable
	return w.glwFocusWeight > 0 || w.glwFlags2&glw2Clickable != 0
}

func glwIsFocused(w *Glw) bool { return w.glwFlags&glwInFocusPath != 0 } // glw_is_focused

func glwIsHovered(w *Glw) bool { return w.glwFlags&glwInHoverPath != 0 } // glw_is_hovered

func glwIsPressed(w *Glw) bool { return w.glwFlags&glwInPressedPath != 0 } // glw_is_pressed

// C: (glw.h:1382-1385)
const (
	glwFocusSetAutomatic   = 0 // GLW_FOCUS_SET_AUTOMATIC
	glwFocusSetAutomaticFf = 1 // GLW_FOCUS_SET_AUTOMATIC_FF
	glwFocusSetInteractive = 2 // GLW_FOCUS_SET_INTERACTIVE
	glwFocusSetSuggested   = 3 // GLW_FOCUS_SET_SUGGESTED
)

// ---------------------------------------------------------------------------
// C: glw_clip_boundary_t (glw.h:1420-1425)
type glwClipBoundary int

const (
	glwClipLeft glwClipBoundary = iota
	glwClipTop
	glwClipRight
	glwClipBottom
)

// ---------------------------------------------------------------------------
// C: glw_transition_type_t (glw.h:1466-1474)
type glwTransitionType int

// C: (glw.h:1698-1701)
const (
	glwRefreshFlagLayout = 0x1 // GLW_REFRESH_FLAG_LAYOUT
	glwRefreshFlagRender = 0x2 // GLW_REFRESH_FLAG_RENDER
	glwRefreshLayoutOnly = 2   // GLW_REFRESH_LAYOUT_ONLY
)

// C: static __inline void glw_need_refresh(glw_root_t *gr, int how)
// (glw.h:1713-1720, non-GLW_TRACK_REFRESH variant)
func glwNeedRefresh(gr *glwRoot, how int) {
	flags := glwRefreshFlagLayout
	if how != glwRefreshLayoutOnly {
		flags |= glwRefreshFlagRender
	}
	gr.grNeedRefresh |= flags
}

// C: static __inline void glw_schedule_refresh(glw_root_t *gr, int64_t when)
func glwScheduleRefresh(gr *glwRoot, when int64) {
	if when < gr.grScheduledRefresh { // C: MIN(gr->gr_scheduled_refresh, when)
		gr.grScheduledRefresh = when
	}
}

// C: static __inline int glw_send_event2 (glw.h:1501-1505)
func glwSendEvent2(w *Glw, e *eventpkg.Event) bool {
	gc := w.glwClass
	return gc.gcSendEvent != nil && gc.gcSendEvent(w, e) != 0
}

// C: static __inline int glw_send_pointer_event (glw.h:1508-1512)
func glwSendPointerEvent(w *Glw, gpe *glwPointerEventT) bool {
	gc := w.glwClass
	return gc.gcPointerEvent != nil && gc.gcPointerEvent(w, gpe) != 0
}

// C: static __inline int glw_bubble_event2 (glw.h:1515-1519)
func glwBubbleEvent2(w *Glw, e *eventpkg.Event) bool {
	gc := w.glwClass
	return gc.gcBubbleEvent != nil && gc.gcBubbleEvent(w, e) != 0
}

// C: static inline int glw_debug (glw.h:1572-1575)
func glwDebug(w *Glw) int {
	return w.glwFlags2 & glw2Debug
}

// C: static inline void glw_zinc (glw.h:1580-1584)
func glwZinc(rc *glwRctx) {
	rc.rcZindex++
	if rc.rcZindex > int16(*rc.rcZmax) {
		*rc.rcZmax = int(rc.rcZindex)
	}
}

// C: #define glw_ref(w) ((w)->glw_refcnt++)
func glwRef(w *Glw) int {
	w.glwRefcnt++
	return w.glwRefcnt
}

// C: #define glw_lock(gr) / glw_unlock(gr)
func glwLock(gr *glwRoot) { gr.grMutex.Lock() }

func glwUnlock(gr *glwRoot) { gr.grMutex.Unlock() }

// C: LAYOUT_ALIGN_* (misc/layout.h:38-47)
const (
	layoutAlignBottomLeft  = 1
	layoutAlignBottom      = 2
	layoutAlignBottomRight = 3
	layoutAlignLeft        = 4
	layoutAlignCenter      = 5
	layoutAlignRight       = 6
	layoutAlignTopLeft     = 7
	layoutAlignTop         = 8
	layoutAlignTopRight    = 9
	layoutAlignJustified   = 10
)

// C: source flags (glw.h:673)
const glwSourceFlagAlwaysLocal = 0x1 // GLW_SOURCE_FLAG_ALWAYS_LOCAL

// BSD queue.h TAILQ_PREV / TAILQ_LAST both resolve via the same trick:
//
//	*(((struct headname *)slot)->tqh_last)
//
// where `slot` is elm->tqe_prev for TAILQ_PREV or head->tqh_last for
// TAILQ_LAST. tqh_last is the second member of the fake head, i.e. the
// memory word right after `slot`, which is always a **Glw.
func glwTAILQPrevSlot(slot **Glw) *Glw {
	// C: *(((struct headname *)slot)->tqh_last) — reinterpret the slot
	// address as a fake TAILQ head {tqh_first, tqh_last} and dereference
	// tqh_last once.
	h := (*struct {
		first *Glw
		last  **Glw
	})(unsafe.Pointer(slot))
	if h.last == nil {
		return nil
	}
	return *h.last
}

// C: TAILQ_PREV(w, glw_queue, glw_parent_link)
func glwTAILQPrev(w *Glw) *Glw { return glwTAILQPrevSlot(w.glwParentLinkPrev) }

// C: TAILQ_LAST(&w->glw_childs, glw_queue)
func glwTAILQLast(w *Glw) *Glw { return glwTAILQPrevSlot(w.glwChilds.tqhLast) }

// Hooks wired by cmd/movian-go for symbols outside this package —
// mirrors gu.go's NavSpawnFn/AppShutdownFn convention.
var (
	// GlwX11NavSpawnFn — C: nav_spawn()
	GlwX11NavSpawnFn func() *propcore.Prop
)

// hexInt — C: snprintf("X11+0x%x", keycode)
func hexInt(v uint) string {
	const digits = "0123456789abcdef"
	if v == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = digits[v&0xf]
		v >>= 4
	}
	return string(b[i:])
}
