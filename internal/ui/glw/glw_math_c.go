package glw

import (
	"math"
	"unsafe"
)

// C: src/ui/glw/glw_math_c.h — canonical 1:1 port (scalar C math path).

// C: typedef struct { Vec4 c[4]; } PMtx;
type PMtx struct {
	c [4]Vec4
}

// C: static __inline void glw_pmtx_mul_prepare(PMtx *dst, const Mtx *src)
// Transpose a matrix so it's faster to vectorize
func glwPmtxMulPrepare(dst *PMtx, src *Mtx) {
	for i := range 4 {
		dst.c[i][0] = src.r[0][i]
		dst.c[i][1] = src.r[1][i]
		dst.c[i][2] = src.r[2][i]
		dst.c[i][3] = src.r[3][i]
	}
}

// C: static __inline void glw_pmtx_mul_vec3(Vec3 dst, const PMtx *m, const Vec3 a)
func glwPmtxMulVec3(dst *Vec3, m *PMtx, a *Vec3) {
	dst[0] =
		m.c[0][0]*a[0] + m.c[0][1]*a[1] + m.c[0][2]*a[2] + m.c[0][3]
	dst[1] =
		m.c[1][0]*a[0] + m.c[1][1]*a[1] + m.c[1][2]*a[2] + m.c[1][3]
	dst[2] =
		m.c[2][0]*a[0] + m.c[2][1]*a[1] + m.c[2][2]*a[2] + m.c[2][3]
}

// C: static __inline void glw_pmtx_mul_vec4_i(Vec4 dst, const PMtx *m, const Vec4 a)
func glwPmtxMulVec4I(dst *Vec4, m *PMtx, a *Vec4) {
	dst[0] =
		m.c[0][0]*a[0] + m.c[0][1]*a[1] + m.c[0][2]*a[2] + m.c[0][3]
	dst[1] =
		m.c[1][0]*a[0] + m.c[1][1]*a[1] + m.c[1][2]*a[2] + m.c[1][3]
	dst[2] =
		m.c[2][0]*a[0] + m.c[2][1]*a[1] + m.c[2][2]*a[2] + m.c[2][3]
	dst[3] = a[3]
}

// C: static __inline void glw_pmtx_mul_vec4(Vec4 dst, const PMtx *m, const Vec4 a)
func glwPmtxMulVec4(dst *Vec4, m *PMtx, a *Vec4) {
	dst[0] =
		m.c[0][0]*a[0] + m.c[0][1]*a[1] + m.c[0][2]*a[2] + m.c[0][3]*a[3]
	dst[1] =
		m.c[1][0]*a[0] + m.c[1][1]*a[1] + m.c[1][2]*a[2] + m.c[1][3]*a[3]
	dst[2] =
		m.c[2][0]*a[0] + m.c[2][1]*a[1] + m.c[2][2]*a[2] + m.c[2][3]*a[3]
	dst[3] =
		m.c[3][0]*a[0] + m.c[3][1]*a[1] + m.c[3][2]*a[2] + m.c[3][3]*a[3]
}

// C: static __inline float glw_vec34_dot(const Vec3 A, const Vec4 B)
func glwVec34Dot(A *Vec3, B *Vec4) float32 {
	return A[0]*B[0] + A[1]*B[1] + A[2]*B[2] + B[3]
}

// C: static __inline void glw_vec4_lerp(Vec4 dst, float s, const Vec4 a, const Vec4 b)
func glwVec4Lerp(dst *Vec4, s float32, a *Vec4, b *Vec4) {
	dst[0] = a[0] + s*(b[0]-a[0])
	dst[1] = a[1] + s*(b[1]-a[1])
	dst[2] = a[2] + s*(b[2]-a[2])
	dst[3] = a[3] + s*(b[3]-a[3])
}

// C: static __inline void glw_vec4_store(float *p, const Vec4 v)
func glwVec4Store(p []float32, v *Vec4) {
	p[0] = v[0]
	p[1] = v[1]
	p[2] = v[2]
	p[3] = v[3]
}

// C: #define glw_vec4_get(p) (p)
func glwVec4Get(p *Vec4) *Vec4 { return p }

// C: #define glw_vec3_make(x,y,z) ((const float[3]){x,y,z})
func glwVec3Make(x, y, z float32) Vec3 { return Vec3{x, y, z} }

// C: #define glw_vec4_make(x,y,z,w) ((const float[4]){x,y,z,w})
func glwVec4Make(x, y, z, w float32) Vec4 { return Vec4{x, y, z, w} }

// C: static __inline void glw_vec3_copy(Vec3 dst, const Vec3 src)
func glwVec3Copy(dst *Vec3, src *Vec3) {
	dst[0] = src[0]
	dst[1] = src[1]
	dst[2] = src[2]
}

// C: static __inline void glw_vec4_copy(Vec4 dst, const Vec4 src)
func glwVec4Copy(dst *Vec4, src *Vec4) {
	dst[0] = src[0]
	dst[1] = src[1]
	dst[2] = src[2]
	dst[3] = src[3]
}

// C: static __inline void glw_vec3_addmul(Vec3 dst, const Vec3 a, const Vec3 b, float s)
func glwVec3Addmul(dst *Vec3, a *Vec3, b *Vec3, s float32) {
	dst[0] = a[0] + b[0]*s
	dst[1] = a[1] + b[1]*s
	dst[2] = a[2] + b[2]*s
}

// C: static __inline void glw_vec3_sub(Vec3 dst, const Vec3 a, const Vec3 b)
func glwVec3Sub(dst *Vec3, a *Vec3, b *Vec3) {
	dst[0] = a[0] - b[0]
	dst[1] = a[1] - b[1]
	dst[2] = a[2] - b[2]
}

// C: static __inline void glw_vec3_cross(Vec3 dst, const Vec3 a, const Vec3 b)
func glwVec3Cross(dst *Vec3, a *Vec3, b *Vec3) {
	dst[0] = (a[1] * b[2]) - (a[2] * b[1])
	dst[1] = (a[2] * b[0]) - (a[0] * b[2])
	dst[2] = (a[0] * b[1]) - (a[1] * b[0])
}

// C: static __inline float glw_vec3_dot(const Vec3 a, const Vec3 b)
func glwVec3Dot(a *Vec3, b *Vec3) float32 {
	return a[0]*b[0] + a[1]*b[1] + a[2]*b[2]
}

// C: static __inline void glw_mtx_trans_mul_vec4(Vec4 dst, const Mtx *m, const Vec4 v)
func glwMtxTransMulVec4(dst *Vec4, m *Mtx, v *Vec4) {
	dst[0] = m.r[0][0]*v[0] + m.r[0][1]*v[1] + m.r[0][2]*v[2] + m.r[0][3]*v[3]
	dst[1] = m.r[1][0]*v[0] + m.r[1][1]*v[1] + m.r[1][2]*v[2] + m.r[1][3]*v[3]
	dst[2] = m.r[2][0]*v[0] + m.r[2][1]*v[1] + m.r[2][2]*v[2] + m.r[2][3]*v[3]
	dst[3] = m.r[3][0]*v[0] + m.r[3][1]*v[1] + m.r[3][2]*v[2] + m.r[3][3]*v[3]
}

// C: extern int glw_mtx_invert(Mtx *dst, const Mtx *src) — glw.c

// C: #define glw_vec2_extract(v, i) v[i]
// C: #define glw_vec3_extract(v, i) v[i]
// C: #define glw_vec4_extract(v, i) v[i]
// (plain indexing in Go)

// C: #define glw_vec4_mul_c0(v, s) v[0] *= (s) ... c3 — plain indexing in Go
// C: #define glw_vec4_set(v, i, s) v[i] = (s) — plain indexing in Go

// C: #define glw_mtx_get(m) (const float *)(&(m)) — &m.r[0][0] in Go

// C: static __inline void glw_mtx_copy(Mtx *dst, const Mtx *src)
func glwMtxCopy(dst *Mtx, src *Mtx) {
	*dst = *src
}

// C: void glw_mtx_mul(Mtx *dst, const Mtx *a, const Mtx *b) — glw.c

// C: static __inline void glw_Translatef(glw_rctx_t *rc, float x, float y, float z)
func glwTranslatef(rc *glwRctx, x, y, z float32) {
	m := &rc.rcMtx

	m.r[3][0] += m.r[0][0]*x + m.r[1][0]*y + m.r[2][0]*z
	m.r[3][1] += m.r[0][1]*x + m.r[1][1]*y + m.r[2][1]*z
	m.r[3][2] += m.r[0][2]*x + m.r[1][2]*y + m.r[2][2]*z
}

// C: static __inline void glw_Scalef(glw_rctx_t *rc, float x, float y, float z)
func glwScalef(rc *glwRctx, x, y, z float32) {
	m := &rc.rcMtx

	m.r[0][0] *= x
	m.r[1][0] *= y
	m.r[2][0] *= z
	m.r[0][1] *= x
	m.r[1][1] *= y
	m.r[2][1] *= z
	m.r[0][2] *= x
	m.r[1][2] *= y
	m.r[2][2] *= z
}

// C: void glw_Rotatef(glw_rctx_t *rc, float a, float x, float y, float z) — glw.c
// C: void glw_LoadIdentity(glw_rctx_t *rc) — glw.c

// C: static __inline void glw_LerpMatrix(Mtx *out, float v, const Mtx *a, const Mtx *b)
func glwLerpMatrix(out *Mtx, v float32, a *Mtx, b *Mtx) {
	for i := range 4 {
		glwVec4Lerp(&out.r[i], v, &a.r[i], &b.r[i])
	}
}

// ===========================================================================
// C: src/ui/glw/glw_math.c + src/ui/glw/glw_math_c.c
// (glw_math.c selects the scalar path when ENABLE_GLW_MATH_SSE is off;
// the Go port implements the C scalar path)

// C: int glw_mtx_invert(Mtx *dst_, const Mtx *src_) (glw_math_c.c:23-65)
//
// C accesses the matrix as a flat float[16]; Go does the same via unsafe.
func glwMtxInvert(dst_ *Mtx, src_ *Mtx) int {
	dst := (*[16]float32)(unsafe.Pointer(dst_))
	src := (*[16]float32)(unsafe.Pointer(src_))

	det :=
		src[0]*src[5]*src[10] +
			src[4]*src[9]*src[2] +
			src[8]*src[1]*src[6] -
			src[2]*src[5]*src[8] -
			src[1]*src[4]*src[10] -
			src[0]*src[6]*src[9]

	if det == 0 {
		return 0
	}

	det = 1.0 / det

	dst[0] = (src[5]*src[10] - src[6]*src[9]) * det
	dst[4] = -(src[4]*src[10] - src[6]*src[8]) * det
	dst[8] = (src[4]*src[9] - src[5]*src[8]) * det

	dst[1] = -(src[1]*src[10] - src[2]*src[9]) * det
	dst[5] = (src[0]*src[10] - src[2]*src[8]) * det
	dst[9] = -(src[0]*src[9] - src[1]*src[8]) * det

	dst[2] = (src[1]*src[6] - src[2]*src[5]) * det
	dst[6] = -(src[0]*src[6] - src[2]*src[4]) * det
	dst[10] = (src[0]*src[5] - src[1]*src[4]) * det

	dst[12] = -dst[0]*src[12] - dst[4]*src[13] - dst[8]*src[14]
	dst[13] = -dst[1]*src[12] - dst[5]*src[13] - dst[9]*src[14]
	dst[14] = -dst[2]*src[12] - dst[6]*src[13] - dst[10]*src[14]

	dst[3] = 0
	dst[7] = 0
	dst[11] = 0
	dst[15] = 1
	return 1
}

// C: void glw_Rotatef(glw_rctx_t *rc, float a, float x, float y, float z)
// (glw_math_c.c:72-125)
func glwRotatef(rc *glwRctx, a, x, y, z float32) {
	s := float32(math.Sin(float64(glwDeg2rad(a))))
	c := float32(math.Cos(float64(glwDeg2rad(a))))
	t := float32(1.0) - c
	n := float32(1.0) / float32(math.Sqrt(float64(x*x+y*y+z*z)))
	var m [16]float32
	o := (*[16]float32)(unsafe.Pointer(&rc.rcMtx))
	var p [16]float32

	x *= n
	y *= n
	z *= n

	m[0] = t*x*x + c
	m[4] = t*x*y - s*z
	m[8] = t*x*z + s*y
	m[12] = 0

	m[1] = t*y*x + s*z
	m[5] = t*y*y + c
	m[9] = t*y*z - s*x
	m[13] = 0

	m[2] = t*z*x - s*y
	m[6] = t*z*y + s*x
	m[10] = t*z*z + c
	m[14] = 0

	p[0] = o[0]*m[0] + o[4]*m[1] + o[8]*m[2]
	p[4] = o[0]*m[4] + o[4]*m[5] + o[8]*m[6]
	p[8] = o[0]*m[8] + o[4]*m[9] + o[8]*m[10]
	p[12] = o[0]*m[12] + o[4]*m[13] + o[8]*m[14] + o[12]

	p[1] = o[1]*m[0] + o[5]*m[1] + o[9]*m[2]
	p[5] = o[1]*m[4] + o[5]*m[5] + o[9]*m[6]
	p[9] = o[1]*m[8] + o[5]*m[9] + o[9]*m[10]
	p[13] = o[1]*m[12] + o[5]*m[13] + o[9]*m[14] + o[13]

	p[2] = o[2]*m[0] + o[6]*m[1] + o[10]*m[2]
	p[6] = o[2]*m[4] + o[6]*m[5] + o[10]*m[6]
	p[10] = o[2]*m[8] + o[6]*m[9] + o[10]*m[10]
	p[14] = o[2]*m[12] + o[6]*m[13] + o[10]*m[14] + o[14]

	p[3] = 0
	p[7] = 0
	p[11] = 0
	p[15] = 1

	*o = p // C: memcpy(o, p, sizeof(float) * 16)
}

// C: void glw_LoadIdentity(glw_rctx_t *rc) (glw_math_c.c:131-139)
func glwLoadIdentity(rc *glwRctx) {
	rc.rcMtx = Mtx{} // C: memset(&rc->rc_mtx, 0, sizeof(Mtx))

	rc.rcMtx.r[0][0] = 1
	rc.rcMtx.r[1][1] = 1
	rc.rcMtx.r[2][2] = 1
	rc.rcMtx.r[3][3] = 1
}

// C: void glw_mtx_mul(Mtx *dst_, const Mtx *a_, const Mtx *b_)
// (glw_math_c.c:142-170)
func glwMtxMul(dst_ *Mtx, a_ *Mtx, b_ *Mtx) {
	dst := (*[16]float32)(unsafe.Pointer(dst_))
	a := (*[16]float32)(unsafe.Pointer(a_))
	b := (*[16]float32)(unsafe.Pointer(b_))

	dst[0] = a[0]*b[0] + a[4]*b[1] + a[8]*b[2] + a[12]*b[3]
	dst[1] = a[1]*b[0] + a[5]*b[1] + a[9]*b[2] + a[13]*b[3]
	dst[2] = a[2]*b[0] + a[6]*b[1] + a[10]*b[2] + a[14]*b[3]
	dst[3] = a[3]*b[0] + a[7]*b[1] + a[11]*b[2] + a[15]*b[3]

	dst[4] = a[0]*b[4] + a[4]*b[5] + a[8]*b[6] + a[12]*b[7]
	dst[5] = a[1]*b[4] + a[5]*b[5] + a[9]*b[6] + a[13]*b[7]
	dst[6] = a[2]*b[4] + a[6]*b[5] + a[10]*b[6] + a[14]*b[7]
	dst[7] = a[3]*b[4] + a[7]*b[5] + a[11]*b[6] + a[15]*b[7]

	dst[8] = a[0]*b[8] + a[4]*b[9] + a[8]*b[10] + a[12]*b[11]
	dst[9] = a[1]*b[8] + a[5]*b[9] + a[9]*b[10] + a[13]*b[11]
	dst[10] = a[2]*b[8] + a[6]*b[9] + a[10]*b[10] + a[14]*b[11]
	dst[11] = a[3]*b[8] + a[7]*b[9] + a[11]*b[10] + a[15]*b[11]

	dst[12] = a[0]*b[12] + a[4]*b[13] + a[8]*b[14] + a[12]*b[15]
	dst[13] = a[1]*b[12] + a[5]*b[13] + a[9]*b[14] + a[13]*b[15]
	dst[14] = a[2]*b[12] + a[6]*b[13] + a[10]*b[14] + a[14]*b[15]
	dst[15] = a[3]*b[12] + a[7]*b[13] + a[11]*b[14] + a[15]*b[15]
}
