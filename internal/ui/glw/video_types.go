package glw

// C: src/ui/glw/glw.h — canonical 1:1 port.
// Every constant, enum, struct, inline helper and macro from the header.

import (
	"sync"
	"unsafe"

	"github.com/czz/movian-go/internal/media"
	mediacore "github.com/czz/movian-go/internal/media/core"
	medialibav "github.com/czz/movian-go/internal/media/libav"
	miscpkg "github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/video/decoder"
)

// C: typedef struct glw_video_reap_task (glw_video_common.h:36-40)
type glwVideoReapTask struct {
	linkNext *glwVideoReapTask  // C: LIST_ENTRY link
	linkPrev **glwVideoReapTask //
	fn       func(gv *GlwVideo, ptr *glwVideoReapTask)
	// C callers allocate sizeof(derived) and cast t to their own
	// task struct — Go engines attach extra state via aux instead.
	aux any
}

// C: typedef struct glw_video_surface (glw_video_common.h:44-90)
type glwVideoSurface struct {
	gvsLinkNext *glwVideoSurface  // C: TAILQ_ENTRY gvs_link
	gvsLinkPrev **glwVideoSurface //

	gvsDuration int
	gvsPts      int64 // C: uint64_t gvs_pts; PTS_UNSET = INT64_MIN sentinel
	gvsEpoch    int

	gvsData [3]unsafe.Pointer

	gvsWidth  [3]int
	gvsHeight [3]int

	gvsInterlaced int
	gvsYshift     int

	gvsID     int
	gvsOpaque unsafe.Pointer
	gvsFormat int

	gvsTexture  GlwBackendTexture
	gvsUploaded int

	// CONFIG_GLW_BACKEND_OPENGL
	gvsPbo  [3]uint32 // C: GLuint gvs_pbo[3]
	gvsSize [3]int

	// C: #if ENABLE_VDPAU (glw_video_common.h) — gvs_vdpau_surface /
	// gvs_gl_surface removed with VDPAU.
	gvsMapped int // C: int gvs_mapped

	gvsFrame    unsafe.Pointer // C: struct AVFrame *gvs_frame
	gvsTexWidth float32        // C: gvs_tex_width

	// VAAPI-EGL zero-copy state — extension, no C counterpart.
	// vaExportSurfaceHandle(DRM_PRIME_2, COMPOSED_LAYERS) result; fds
	// are owned until the EGLImage is created, EGLImage+texture until
	// the surface is released.
	gvsVaapiState   int // 0 none, 1 fds pending import, 2 imported
	gvsVaapiDesc    medialibav.VaapiDmabufDesc
	gvsVaapiImages  [4]unsafe.Pointer // EGLImageKHR ([0] only for NV12)
	gvsVaapiFdOwned [4]int32          // fds still to close (post-import)

	// vaapiImportFails counts consecutive eglCreateImageKHR failures —
	// persistent failure flips ZeroCopy off so delivery falls back to
	// the hw→sw transfer path. Per-surface (was a file global).
	vaapiImportFails int

	gvsRefAux     unsafe.Pointer
	gvsRefRelease func(aux unsafe.Pointer)
}

const glwVideoMaxSurfaces = 10 // C: GLW_VIDEO_MAX_SURFACES

// C: typedef struct glw_video (glw_video_common.h:94-249)
type GlwVideo struct {
	w Glw

	gvQuad glwRenderer    // C: gv_quad
	gvGpa  GlwProgramArgs // C: gv_gpa

	gvWidth  int // C: gv_width
	gvHeight int // C: gv_height

	gvDarNum int // C: gv_dar_num
	gvDarDen int // C: gv_dar_den

	gvVheight int // C: gv_vheight

	gvRwidth  int // C: gv_rwidth
	gvRheight int // C: gv_rheight

	gvFwidth  int // C: gv_fwidth
	gvFheight int // C: gv_fheight

	gvCurrentURL string // C: gv_current_url (char*)
	gvPendingURL string // C: gv_pending_url (char*)

	gvHow        string        // C: gv_how (char*)
	gvParentURLX *miscpkg.Rstr // C: gv_parent_url_x

	gvFlags       int            // C: gv_flags
	gvPriority    int            // C: gv_priority
	gvItemModel   *propcore.Prop // C: gv_item_model
	gvParentModel *propcore.Prop // C: gv_parent_model
	gvFreezed     int8           // C: char gv_freezed

	// AV Diff handling
	gvAvdiffUpdateThres int                // C: gv_avdiff_update_thres
	gvAvfilter          media.KalmanFilter // C: kalman_t gv_avfilter
	gvAvdiffX           float32            // C: gv_avdiff_x
	// C: int gv_avdiff — int64 aclock-pts truncated to C int (32-bit);
	// the abs() check below sees the wrapped value, same as C.
	gvAvdiff int32

	gvVd *decoder.VideoDecoder // C: gv_vd
	gvMp *mediacore.MediaPipe  // C: gv_mp

	gvSa    *glwVideoSurface // C: gv_sa
	gvSb    *glwVideoSurface // C: gv_sb
	gvBlend float32          // C: gv_blend

	gvGlobalLinkNext *GlwVideo  // C: LIST_ENTRY gv_global_link
	gvGlobalLinkPrev **GlwVideo //

	gvOverlays                  glwVideoOverlayList // C: gv_overlays
	gvBottomOverlayDisplacement int                 // C: gv_bottom_overlay_displacement

	gvCmatrixCur        [16]float32 // C: gv_cmatrix_cur
	gvCmatrixTgt        [16]float32 // C: gv_cmatrix_tgt
	gvPlanes            int         // C: gv_planes
	gvTexInternalFormat int         // C: gv_tex_internal_format
	gvTexFormat         int         // C: gv_tex_format
	gvTexType           int         // C: gv_tex_type
	gvTexBytesPerPixel  int         // C: gv_tex_bytes_per_pixel

	gvSurfaceMutex sync.Mutex // C: hts_mutex_t gv_surface_mutex

	gvReaps glwVideoReapTaskList // C: gv_reaps

	gvEngine    *glwVideoEngine // C: gv_engine
	gvNeedStart int             // C: gv_need_init
	gvStartCond *sync.Cond      // C: hts_cond_t gv_init_cond (on gv_surface_mutex)

	gvSurfaces [glwVideoMaxSurfaces]glwVideoSurface // C: gv_surfaces

	// Frames that need to be prepared before they can be used
	gvParkedQueue glwVideoSurfaceQueue // C: gv_parked_queue

	// Frames available for decoder; gv_avail_queue_cond signaled on push
	gvAvailQueue     glwVideoSurfaceQueue // C: gv_avail_queue
	gvAvailQueueCond *sync.Cond           // C: gv_avail_queue_cond

	// Frames currently being displayed
	gvDisplayingQueue glwVideoSurfaceQueue // C: gv_displaying_queue

	// Freshly decoded surfaces are enqueued here
	gvDecodedQueue glwVideoSurfaceQueue // C: gv_decoded_queue

	gvNextpts      int64 // C: gv_nextpts
	gvNextptsEpoch int   // C: gv_nextpts_epoch

	gvAux unsafe.Pointer // C: gv_aux

	// Settings subscriptions (ENABLE_MEDIA_SETTINGS — always on here)
	gvVoScalingSub       *propcore.Subscription // C: gv_vo_scaling_sub
	gvVoDisplaceYSub     *propcore.Subscription // C: gv_vo_displace_y_sub
	gvVoDisplaceXSub     *propcore.Subscription // C: gv_vo_displace_x_sub
	gvVoOnVideoSub       *propcore.Subscription // C: gv_vo_on_video_sub
	gvVzoomSub           *propcore.Subscription // C: gv_vzoom_sub
	gvPanHorizontalSub   *propcore.Subscription // C: gv_pan_horizontal_sub
	gvPanVerticalSub     *propcore.Subscription // C: gv_pan_vertical_sub
	gvScaleHorizontalSub *propcore.Subscription // C: gv_scale_horizontal_sub
	gvScaleVerticalSub   *propcore.Subscription // C: gv_scale_vertical_sub
	gvHstretchSub        *propcore.Subscription // C: gv_hstretch_sub
	gvFstretchSub        *propcore.Subscription // C: gv_fstretch_sub
	gvVinterpolateSub    *propcore.Subscription // C: gv_vinterpolate_sub

	gvVoScaling       float32 // C: gv_vo_scaling
	gvVoDisplaceY     int     // C: gv_vo_displace_y
	gvVoDisplaceX     int     // C: gv_vo_displace_x
	gvVoOnVideo       int     // C: gv_vo_on_video
	gvVzoom           int     // C: gv_vzoom
	gvPanHorizontal   int     // C: gv_pan_horizontal
	gvPanVertical     int     // C: gv_pan_vertical
	gvScaleHorizontal int     // C: gv_scale_horizontal
	gvScaleVertical   int     // C: gv_scale_vertical
	gvHstretch        int     // C: gv_hstretch
	gvFstretch        int     // C: gv_fstretch
	gvVinterpolate    int     // C: gv_vinterpolate

	// DVD SPU stuff
	gvSpuInMenu int // C: gv_spu_in_menu

	// 2D coordinates on screen
	gvRect glwRect // C: gv_rect

	gvInvisible int // C: gv_invisible

	// Log suppression
	gvLoggedPixfmt int // C: gv_logged_pixfmt
}

// C: typedef struct glw_video_overlay (glw_video_overlay.c:33-80)
type GlwVideoOverlay struct {
	linkNext *GlwVideoOverlay  // C: LIST_ENTRY gvo_link
	linkPrev **GlwVideoOverlay //

	gvoType int // C: enum { GVO_DVDSPU, GVO_BITMAP, GVO_TEXT } gvo_type

	// GVO_DVDSPU and GVO_BITMAP
	gvoTexture  GlwBackendTexture // C: gvo_texture
	gvoRenderer glwRenderer       // C: gvo_renderer

	gvoStart         int64 // C: gvo_start
	gvoStop          int64 // C: gvo_stop
	gvoStopEstimated int   // C: gvo_stop_estimated

	gvoFadein  int // C: gvo_fadein
	gvoFadeout int // C: gvo_fadeout

	gvoAlignment int // C: gvo_alignment

	gvoPaddingLeft   int // C: gvo_padding_left
	gvoPaddingTop    int // C: gvo_padding_top
	gvoPaddingRight  int // C: gvo_padding_right
	gvoPaddingBottom int // C: gvo_padding_bottom

	gvoWidth  int // C: gvo_width
	gvoHeight int // C: gvo_height

	gvoAlpha float32 // C: gvo_alpha

	gvoWidget *Glw // C: gvo_widget

	gvoCanvasWidth  int // C: gvo_canvas_width
	gvoCanvasHeight int // C: gvo_canvas_height

	// If set the overlay should be aligned to actual video frame
	gvoVideoframeAlign int // C: gvo_videoframe_align
	gvoLayer           int // C: gvo_layer

	gvoX      int // C: gvo_x
	gvoY      int // C: gvo_y
	gvoAbspos int // C: gvo_abspos
}

// C: enum { GVO_DVDSPU, GVO_BITMAP, GVO_TEXT } (glw_video_overlay.c:37-41)
const (
	gvoDVDSPU = iota // C: GVO_DVDSPU
	gvoBitmap        // C: GVO_BITMAP
	gvoText          // C: GVO_TEXT
)

// C: typedef struct glw_video_engine (glw_video_common.h:254-281)
type glwVideoEngine struct {
	gveType            mediacore.FourCC // C: gve_type (4-char tag)
	gveStartOnUIThread int              // C: gve_init_on_ui_thread

	gveDeliver      func(fi *mediacore.FrameInfo, gv *GlwVideo, gve *glwVideoEngine) int
	gveSetCodec     func(mc *mediacore.MediaCodec, gv *GlwVideo, fi *mediacore.FrameInfo, gve *glwVideoEngine) int
	gveBlackout     func(gv *GlwVideo)
	gveRender       func(gv *GlwVideo, rc *glwRctx)
	gveNewframe     func(gv *GlwVideo, vd *decoder.VideoDecoder, flags int) int64
	gveReset        func(gv *GlwVideo)
	gveStart        func(gv *GlwVideo) int
	gveSurfaceSetup func(gv *GlwVideo, gvs *glwVideoSurface) int // C: returns 0/-1; failure → get_surface NULL

	linkNext *glwVideoEngine  // C: LIST_ENTRY gve_link
	linkPrev **glwVideoEngine //
}

// C: typedef gv_surface_pixmap_release_t (glw_video_common.h:290-292)
type gvSurfacePixmapReleaseFunc func(gv *GlwVideo, gvs *glwVideoSurface,
	fq *glwVideoSurfaceQueue)

// ---------------------------------------------------------------------------
// C: Video flags (glw.h:243-244)
const (
	glwVideoPrimary = 0x1 // GLW_VIDEO_PRIMARY
	glwVideoNoAudio = 0x2 // GLW_VIDEO_NO_AUDIO
)
