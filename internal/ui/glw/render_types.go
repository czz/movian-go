package glw

// C: src/ui/glw/glw.h — canonical 1:1 port.
// Every constant, enum, struct, inline helper and macro from the header.

import ()

// C: glw_render_job_t (glw_renderer.h:112-135)
type GlwRenderJob struct {
	M             Mtx
	T0            *GlwBackendTexture
	T1            *GlwBackendTexture
	Gpa           *GlwProgramArgs
	RgbMul        glwRgb
	RgbOff        glwRgb
	Alpha         float32
	Blur          float32
	VertexOffset  int
	IndexOffset   int
	NumVertices   int16
	NumIndices    int16
	Width         int16
	Height        int16
	PrimitiveType int16
	Blendmode     int8
	Flags         int8
	Eyespace      uint8 // C: eyespace : 1
	Frontface     int8
	Opaque        int8
}

// C: glw_render_order_t (glw_renderer.h:138-142)
// Go: job is an index into grRenderJobs, not a pointer — C's realloc
// fixup (glw_renderer.c:991-995) is equivalent: the job identity is
// preserved across slice growth without stale pointers.
type GlwRenderOrder struct {
	JobIdx int
	Zindex int16
}

// C: glw_program_t (glw_opengl.h:71-98) — GLSL shader program.
// C LIST_ENTRY gp_link → membership in gbr.gbrPrograms slice.
type glwProgram struct {
	gpTitle   string // C: gp_title
	gpProgram uint32 // C: GLuint gp_program

	// Attributes
	gpAttributePosition int32 // C: gp_attribute_position
	gpAttributeTexcoord int32 // C: gp_attribute_texcoord
	gpAttributeColor    int32 // C: gp_attribute_color

	// Uniforms
	gpUniformModelview   int32 // C: gp_uniform_modelview
	gpUniformColor       int32 // C: gp_uniform_color
	gpUniformColormtx    int32 // C: gp_uniform_colormtx
	gpUniformBlend       int32 // C: gp_uniform_blend
	gpUniformColorOffset int32 // C: gp_uniform_color_offset
	gpUniformBlur        int32 // C: gp_uniform_blur
	gpUniformTime        int32 // C: gp_uniform_time
	gpUniformResolution  int32 // C: gp_uniform_resolution

	gpUniformT [6]int32 // C: gp_uniform_t[6]

	gpCurrentColorOffset glwRgb  // C: gp_current_color_offset
	gpCurrentColorMul    glwRgb  // C: gp_current_color_mul
	gpCurrentAlpha       float32 // C: gp_current_alpha

	gpIdentityMvm int8 // C: gp_identity_mvm
}

// C: glw_program_args_t (glw_renderer.h:99-105)
type GlwProgramArgs struct {
	GpaProg         *glwProgram
	GpaAux          any
	GpaLoadUniforms func(gr *glwRoot, prog *glwProgram, aux any, rj *GlwRenderJob)
	GpaLoadTexture  func(gr *glwRoot, prog *glwProgram, aux any, t *GlwBackendTexture, num int)
}

// ---------------------------------------------------------------------------
// C: (glw.h:48-57)
const (
	numClipplanes = 6 // NUM_CLIPPLANES
	numFaders     = 0 // NUM_FADERS
	numStencilers = 0 // NUM_STENCILERS

	glwCursorAutohideTime = 3000000 // GLW_CURSOR_AUTOHIDE_TIME

	glwAlphaEpsilon = 1.0 / 256.0 // GLW_ALPHA_EPSILON
)

// C: typedef struct glw_vertex { float x, y, z; } glw_vertex_t;
type glwVertex struct {
	x, y, z float32
}

// C: typedef struct glw_rgb { float r, g, b; } glw_rgb_t;
type glwRgb struct {
	r, g, b float32
}

// C: static inline int glw_rgb_cmp(...) (glw.h:83-86)
func glwRgbCmp(x, y *glwRgb) bool {
	return x.r == y.r && x.g == y.g && x.b == y.b
}

// C: static inline void glw_rgb_cpy(...) (glw.h:91-96)
func glwRgbCpy(x, y *glwRgb) {
	x.r = y.r
	x.g = y.g
	x.b = y.b
}

type glwRect struct {
	x1, x2 int
	y1, y2 int
}

// ---------------------------------------------------------------------------
// C: Math mode (glw.h:118-124)
type Vec4 [4]float32

type Vec3 [3]float32

// C: typedef struct { Vec4 r[4]; } Mtx;
type Mtx struct {
	r [4]Vec4
}

// ---------------------------------------------------------------------------
// C: glw_rctx_t — render context (glw.h:1064-1096)
type glwRctx struct {
	rcZmax *int // C: rc_zmax

	rcMtx Mtx // C: rc_mtx

	rcAlpha     float32 // C: rc_alpha
	rcSharpness float32 // C: rc_sharpness

	rcWidth  int16 // C: rc_width
	rcHeight int16 // C: rc_height

	rcZindex int16 // C: rc_zindex

	rcLayer uint8 // C: rc_layer

	// C bitfields — kept as separate uint8 fields
	rcInhibitShadows     uint8 // C: rc_inhibit_shadows : 1
	rcInhibitMatrixStore uint8 // C: rc_inhibit_matrix_store : 1
	rcOverscanning       uint8 // C: rc_overscanning : 1
	rcInvisible          uint8 // C: rc_invisible : 1
	rcPreloaded          uint8 // C: rc_preloaded : 1
	rcSegwayed           uint8 // C: rc_segwayed : 1
}

const (
	glwTransNone            glwTransitionType = iota // GLW_TRANS_NONE
	glwTransBlend                                    // GLW_TRANS_BLEND
	glwTransFlipHorizontal                           // GLW_TRANS_FLIP_HORIZONTAL
	glwTransFlipVertical                             // GLW_TRANS_FLIP_VERTICAL
	glwTransSlideHorizontal                          // GLW_TRANS_SLIDE_HORIZONTAL
	glwTransSlideVertical                            // GLW_TRANS_SLIDE_VERTICAL
	glwTransNum                                      // GLW_TRANS_num
)

// ---------------------------------------------------------------------------
// C: glw_gf_ctrl_t (glw.h:1590-1594)
type glwGfCtrl struct {
	linkNext *glwGfCtrl
	linkPrev **glwGfCtrl
	flush    func(opaque any)
	opaque   any
}

// C: blend modes (glw.h:1636-1637)
const (
	glwBlendNormal   = 0 // GLW_BLEND_NORMAL
	glwBlendAdditive = 1 // GLW_BLEND_ADDITIVE
)

// C: frontface (glw.h:1643-1644)
const (
	glwCw  = 0 // GLW_CW
	glwCcw = 1 // GLW_CCW
)
