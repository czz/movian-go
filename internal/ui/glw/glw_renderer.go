package glw

import (
	"slices"
	"unsafe"
)

// glw_renderer.c — vertex tessellation, clip-plane evaluation and render
// job accumulation. 1:1 port of src/ui/glw/glw_renderer.c.
//
// Build config: NUM_CLIPPLANES=6, NUM_FADERS=0, NUM_STENCILERS=0 — the
// fader/stenciler paths are #if'd out in C and omitted here (glw.h:52-55).

const vertexSize = 12 // VERTEX_SIZE (glw_renderer.h:32)

// Vertex layout (glw_renderer.h:34-46):
//   [0..2] x,y,z   [3] s (sharpness)   [4..7] r,g,b,a
//   [8..9] s0,t0 (Texture0)   [10..11] s1,t1 (Texture1)

// C: GLW_DRAW_* (glw_opengl.h:150-152 — GL enum values)
const (
	glwDrawTriangles = 0x0004 // GL_TRIANGLES
	glwDrawLines     = 0x0001 // GL_LINES
	glwDrawLineLoop  = 0x0002 // GL_LINE_LOOP
)

// C: render flags (glw_renderer.h:23-29)
const (
	glwRenderBlurAttribute = 0x1 // GLW_RENDER_BLUR_ATTRIBUTE
	glwRenderOpaque        = 0x2 // GLW_RENDER_OPAQUE
	glwRenderDebug         = 0x4 // GLW_RENDER_DEBUG
)

// C: static const glw_rgb_t white = {1,1,1} (glw_renderer.c:23)
var white = glwRgb{r: 1, g: 1, b: 1}

// glwRendererCaches — C: GLW_RENDERER_CACHES
const glwRendererCaches = 2

// C: glw_renderer_cache_t (glw_renderer.h:55-75)
type glwRendererCache struct {
	grcMtx            Mtx
	grcActiveClippers uint16
	grcActiveFaders   uint16
	grcClip           [numClipplanes]Vec4
	// NUM_STENCILERS/NUM_FADERS == 0 — stencil/fader fields omitted
	grcBlurred     bool
	grcNumVertices uint16
	grcVertices    []float32
}

// C: glw_renderer_t (glw_renderer.h:80-96)
type glwRenderer struct {
	grVertices        []float32
	grIndices         []uint16
	grCache           [glwRendererCaches]*glwRendererCache
	grNumVertices     uint16
	grNumTriangles    uint16
	grFramecmp        uint8
	grCacheptr        uint8
	grStaticIndices   bool
	grDirty           bool
	grColorAttributes bool
}

// glwVtmpResize — C: glw_vtmp_resize (glw_renderer.c:30-37)
func glwVtmpResize(gr *glwRoot, numFloat int) {
	if gr.grVtmpCapacity >= numFloat {
		return
	}
	gr.grVtmpCapacity = numFloat * 10
	nb := make([]float32, gr.grVtmpCapacity)
	copy(nb, gr.grVtmpBuffer)
	gr.grVtmpBuffer = nb
}

// glwRendererSetup — C: glw_renderer_init (glw_renderer.c:43-69)
func glwRendererSetup(gr *glwRenderer, numVertices, numTriangles int, indices []uint16) {
	gr.grVertices = make([]float32, vertexSize*numVertices)
	gr.grNumVertices = uint16(numVertices)

	gr.grStaticIndices = indices != nil
	if gr.grStaticIndices {
		gr.grIndices = indices
	} else {
		gr.grIndices = make([]uint16, numTriangles*3)
	}

	gr.grNumTriangles = uint16(numTriangles)

	for i := range numVertices {
		gr.grVertices[i*vertexSize+3] = 1
		gr.grVertices[i*vertexSize+4] = 1
		gr.grVertices[i*vertexSize+5] = 1
		gr.grVertices[i*vertexSize+6] = 1
		gr.grVertices[i*vertexSize+7] = 1
	}
	gr.grDirty = true
	gr.grColorAttributes = false
}

// C: static uint16_t quadvertices[6] (glw_renderer.c:85-88)
var quadvertices = [6]uint16{0, 1, 2, 0, 2, 3}

// glwRendererSetupQuad — C: glw_renderer_init_quad (glw_renderer.c:94-98)
func glwRendererSetupQuad(gr *glwRenderer) {
	glwRendererSetup(gr, 4, 2, quadvertices[:])
}

// glwRendererSetupTriangle — C: glw_renderer_init_triangle (glw_renderer.c:104-108)
func glwRendererSetupTriangle(gr *glwRenderer) {
	glwRendererSetup(gr, 3, 1, quadvertices[:])
}

// glwRendererFree — C: glw_renderer_free (glw_renderer.c:114-131)
// Go: slices are GC'd; reset to nil for structural parity.
func glwRendererFree(gr *glwRenderer) {
	gr.grVertices = nil
	if !gr.grStaticIndices {
		gr.grIndices = nil
	}
	for i := range glwRendererCaches {
		if gr.grCache[i] != nil {
			gr.grCache[i].grcVertices = nil
			gr.grCache[i] = nil
		}
	}
}

// glwRendererStarted — C: glw_renderer_initialized (glw_renderer.c:137-141)
func glwRendererStarted(gr *glwRenderer) bool {
	return gr.grVertices != nil
}

// glwRendererVtxPos — C: glw_renderer_vtx_pos (glw_renderer.c:147-154)
func glwRendererVtxPos(gr *glwRenderer, vertex int, x, y, z float32) {
	gr.grVertices[vertex*vertexSize+0] = x
	gr.grVertices[vertex*vertexSize+1] = y
	gr.grVertices[vertex*vertexSize+2] = z
	gr.grDirty = true
}

// glwRendererVtxSt — C: glw_renderer_vtx_st (glw_renderer.c:160-166)
func glwRendererVtxSt(gr *glwRenderer, vertex int, s, t float32) {
	gr.grVertices[vertex*vertexSize+8] = s
	gr.grVertices[vertex*vertexSize+9] = t
	gr.grDirty = true
}

// glwRendererVtxSt2 — C: glw_renderer_vtx_st2 (glw_renderer.c:173-179)
func glwRendererVtxSt2(gr *glwRenderer, vertex int, s, t float32) {
	gr.grVertices[vertex*vertexSize+10] = s
	gr.grVertices[vertex*vertexSize+11] = t
	gr.grDirty = true
}

// glwRendererVtxCol — C: glw_renderer_vtx_col (glw_renderer.c:186-195)
func glwRendererVtxCol(gr *glwRenderer, vertex int, r, g, b, a float32) {
	gr.grVertices[vertex*vertexSize+4] = r
	gr.grVertices[vertex*vertexSize+5] = g
	gr.grVertices[vertex*vertexSize+6] = b
	gr.grVertices[vertex*vertexSize+7] = a
	gr.grDirty = true
	gr.grColorAttributes = true
}

// glwRendererVtxColReset — C: glw_renderer_vtx_col_reset (glw_renderer.c:202-220)
func glwRendererVtxColReset(gr *glwRenderer) {
	if !gr.grColorAttributes {
		return
	}
	for i := range int(gr.grNumVertices) {
		gr.grVertices[i*vertexSize+3] = 1
		gr.grVertices[i*vertexSize+4] = 1
		gr.grVertices[i*vertexSize+5] = 1
		gr.grVertices[i*vertexSize+6] = 1
		gr.grVertices[i*vertexSize+7] = 1
	}
	gr.grDirty = true
	gr.grColorAttributes = false
}

// vec4At — C: glw_vec4_get(p) aliasing a float array element.
// Vec4 is [4]float32 — the cast preserves C's zero-copy view semantics.
func vec4At(f []float32, off int) *Vec4 {
	return (*Vec4)(unsafe.Pointer(&f[off]))
}

// glwVec4MulC3 — C: glw_vec4_mul_c3(v, s) → v[3] *= s (glw_math_c.h:175)
// Component 3 is alpha for color vecs, sharpness (pos.w) for position vecs.
func glwVec4MulC3(v *Vec4, s float32) {
	v[3] *= s
}

// emitTriangle — C: emit_triangle (glw_renderer.c:227-248)
func emitTriangle(gr *glwRoot, v1, v2, v3, c1, c2, c3, t1, t2, t3 *Vec4) {
	glwVtmpResize(gr, (gr.grVtmpCur+3)*vertexSize+4)

	f := gr.grVtmpBuffer[gr.grVtmpCur*vertexSize:]

	glwVec4Store(f, v1)
	glwVec4Store(f[4:], c1)
	glwVec4Store(f[8:], t1)

	glwVec4Store(f[vertexSize:], v2)
	glwVec4Store(f[vertexSize+4:], c2)
	glwVec4Store(f[vertexSize+8:], t2)

	glwVec4Store(f[vertexSize*2:], v3)
	glwVec4Store(f[vertexSize*2+4:], c3)
	glwVec4Store(f[vertexSize*2+8:], t3)

	gr.grVtmpCur += 3
}

// clipOut — C: clip_out (glw_renderer.c:324-357)
func clipOut(gr *glwRoot, grc *glwRendererCache,
	v1, v2, v3, c1, c2, c3, t1, t2, t3 *Vec4, plane int) {
	alphaOut := gr.grClipAlphaOut[plane-1]
	sharpnessOut := gr.grClipSharpnessOut[plane-1]
	if alphaOut < glwAlphaEpsilon {
		return
	}

	nv1, nv2, nv3 := *v1, *v2, *v3
	nc1, nc2, nc3 := *c1, *c2, *c3

	if sharpnessOut < 1 {
		grc.grcBlurred = true
		glwVec4MulC3(&nv1, sharpnessOut)
		glwVec4MulC3(&nv2, sharpnessOut)
		glwVec4MulC3(&nv3, sharpnessOut)
	}

	glwVec4MulC3(&nc1, alphaOut)
	glwVec4MulC3(&nc2, alphaOut)
	glwVec4MulC3(&nc3, alphaOut)

	clipper(gr, grc, &nv1, &nv2, &nv3, &nc1, &nc2, &nc3, t1, t2, t3, plane)
}

// clipper — C: clipper (glw_renderer.c:363-508)
// Sutherland-Hodgman triangle clipping against the active clip planes.
func clipper(gr *glwRoot, grc *glwRendererCache,
	v1, v2, v3, c1, c2, c3, t1, t2, t3 *Vec4, plane int) {
	for {
		if plane == numClipplanes {
			// NUM_FADERS == 0 — emit directly
			emitTriangle(gr, v1, v2, v3, c1, c2, c3, t1, t2, t3)
			return
		}
		if grc.grcActiveClippers&(1<<uint(plane)) != 0 {
			break
		}
		plane++
	}

	d1 := glwVec34Dot((*Vec3)(unsafe.Pointer(v1)), &grc.grcClip[plane])
	d2 := glwVec34Dot((*Vec3)(unsafe.Pointer(v2)), &grc.grcClip[plane])
	d3 := glwVec34Dot((*Vec3)(unsafe.Pointer(v3)), &grc.grcClip[plane])

	plane++

	var s12, s13, s23 float32
	var v12, v13, v23 Vec4
	var c12, c13, c23 Vec4
	var t12, t13, t23 Vec4

	if d1 >= 0 {
		if d2 >= 0 {
			if d3 >= 0 {
				clipper(gr, grc, v1, v2, v3, c1, c2, c3, t1, t2, t3, plane)
			} else {
				s13 = d1 / (d1 - d3)
				s23 = d2 / (d2 - d3)

				glwVec4Lerp(&v13, s13, v1, v3)
				glwVec4Lerp(&v23, s23, v2, v3)
				glwVec4Lerp(&c13, s13, c1, c3)
				glwVec4Lerp(&c23, s23, c2, c3)
				glwVec4Lerp(&t13, s13, t1, t3)
				glwVec4Lerp(&t23, s23, t2, t3)

				clipper(gr, grc, v1, v2, &v23, c1, c2, &c23, t1, t2, &t23, plane)
				clipper(gr, grc, v1, &v23, &v13, c1, &c23, &c13, t1, &t23, &t13, plane)

				clipOut(gr, grc, &v23, v3, &v13, &c23, c3, &c13, &t23, t3, &t13, plane)
			}
		} else {
			s12 = d1 / (d1 - d2)
			glwVec4Lerp(&v12, s12, v1, v2)
			glwVec4Lerp(&c12, s12, c1, c2)
			glwVec4Lerp(&t12, s12, t1, t2)

			if d3 >= 0 {
				s23 = d2 / (d2 - d3)
				glwVec4Lerp(&v23, s23, v2, v3)
				glwVec4Lerp(&c23, s23, c2, c3)
				glwVec4Lerp(&t23, s23, t2, t3)

				clipper(gr, grc, v1, &v12, &v23, c1, &c12, &c23, t1, &t12, &t23, plane)
				clipper(gr, grc, v1, &v23, v3, c1, &c23, c3, t1, &t23, t3, plane)

				clipOut(gr, grc, &v12, v2, &v23, &c12, c2, &c23, &t12, t2, &t23, plane)
			} else {
				s13 = d1 / (d1 - d3)
				glwVec4Lerp(&v13, s13, v1, v3)
				glwVec4Lerp(&c13, s13, c1, c3)
				glwVec4Lerp(&t13, s13, t1, t3)

				clipper(gr, grc, v1, &v12, &v13, c1, &c12, &c13, t1, &t12, &t13, plane)

				clipOut(gr, grc, &v12, v2, v3, &c12, c2, c3, &t12, t2, t3, plane)
				clipOut(gr, grc, &v12, v3, &v13, &c12, c3, &c13, &t12, t3, &t13, plane)
			}
		}
	} else if d2 >= 0 {
		s12 = d1 / (d1 - d2)
		glwVec4Lerp(&v12, s12, v1, v2)
		glwVec4Lerp(&c12, s12, c1, c2)
		glwVec4Lerp(&t12, s12, t1, t2)

		if d3 >= 0 {
			s13 = d1 / (d1 - d3)
			glwVec4Lerp(&v13, s13, v1, v3)
			glwVec4Lerp(&c13, s13, c1, c3)
			glwVec4Lerp(&t13, s13, t1, t3)

			clipper(gr, grc, &v12, v2, v3, &c12, c2, c3, &t12, t2, t3, plane)
			clipper(gr, grc, &v12, v3, &v13, &c12, c3, &c13, &t12, t3, &t13, plane)

			clipOut(gr, grc, v1, &v12, &v13, c1, &c12, &c13, t1, &t12, &t13, plane)
		} else {
			s23 = d2 / (d2 - d3)
			glwVec4Lerp(&v23, s23, v2, v3)
			glwVec4Lerp(&c23, s23, c2, c3)
			glwVec4Lerp(&t23, s23, t2, t3)

			clipper(gr, grc, &v12, v2, &v23, &c12, c2, &c23, &t12, t2, &t23, plane)

			clipOut(gr, grc, v1, &v12, &v23, c1, &c12, &c23, t1, &t12, &t23, plane)
			clipOut(gr, grc, v1, &v23, v3, c1, &c23, c3, t1, &t23, t3, plane)
		}
	} else if d3 >= 0 {
		s13 = d1 / (d1 - d3)
		s23 = d2 / (d2 - d3)

		glwVec4Lerp(&v13, s13, v1, v3)
		glwVec4Lerp(&v23, s23, v2, v3)
		glwVec4Lerp(&c13, s13, c1, c3)
		glwVec4Lerp(&c23, s23, c2, c3)
		glwVec4Lerp(&t13, s13, t1, t3)
		glwVec4Lerp(&t23, s23, t2, t3)

		clipper(gr, grc, &v13, &v23, v3, &c13, &c23, c3, &t13, &t23, t3, plane)

		clipOut(gr, grc, v1, v2, &v23, c1, c2, &c23, t1, t2, &t23, plane)
		clipOut(gr, grc, v1, &v23, &v13, c1, &c23, &c13, t1, &t23, &t13, plane)
	} else {
		clipOut(gr, grc, v1, v2, v3, c1, c2, c3, t1, t2, t3, plane)
	}
}

// glwRendererTesselate — C: glw_renderer_tesselate (glw_renderer.c:779-859)
func glwRendererTesselate(gr *glwRenderer, root *glwRoot, rc *glwRctx, grc *glwRendererCache) {
	ip := 0 // index into gr.grIndices
	a := gr.grVertices
	var pmtx PMtx

	root.grVtmpCur = 0

	grc.grcMtx = rc.rcMtx
	grc.grcActiveClippers = uint16(root.grActiveClippers)
	grc.grcBlurred = false

	for i := range numClipplanes {
		if (1<<uint(i))&root.grActiveClippers != 0 {
			grc.grcClip[i] = root.grClip[i]
		}
	}

	glwPmtxMulPrepare(&pmtx, &rc.rcMtx)

	for range int(gr.grNumTriangles) {
		v1 := int(gr.grIndices[ip])
		ip++
		v2 := int(gr.grIndices[ip])
		ip++
		v3 := int(gr.grIndices[ip])
		ip++

		var vv1, vv2, vv3 Vec4

		glwPmtxMulVec4I(&vv1, &pmtx, vec4At(a, v1*vertexSize))
		glwPmtxMulVec4I(&vv2, &pmtx, vec4At(a, v2*vertexSize))
		glwPmtxMulVec4I(&vv3, &pmtx, vec4At(a, v3*vertexSize))

		// NUM_STENCILERS == 0 — straight to clipper
		clipper(root, grc, &vv1, &vv2, &vv3,
			vec4At(a, v1*vertexSize+4),
			vec4At(a, v2*vertexSize+4),
			vec4At(a, v3*vertexSize+4),
			vec4At(a, v1*vertexSize+8),
			vec4At(a, v2*vertexSize+8),
			vec4At(a, v3*vertexSize+8),
			0)
	}

	size := root.grVtmpCur * vertexSize

	if root.grVtmpCur != int(grc.grcNumVertices) {
		grc.grcNumVertices = uint16(root.grVtmpCur)
		grc.grcVertices = make([]float32, size)
	}
	if size > 0 {
		copy(grc.grcVertices, root.grVtmpBuffer[:size])
	}
}

// glwRendererClippersCmp — C: glw_renderer_clippers_cmp (glw_renderer.c:878-890)
func glwRendererClippersCmp(grc *glwRendererCache, root *glwRoot) int {
	if grc.grcActiveClippers != uint16(root.grActiveClippers) {
		return 1
	}
	for i := range numClipplanes {
		if (1<<uint(i))&root.grActiveClippers != 0 {
			if grc.grcClip[i] != root.grClip[i] {
				return 1
			}
		}
	}
	return 0
}

// glwRendererGetCache — C: glw_renderer_get_cache (glw_renderer.c:946-962)
func glwRendererGetCache(root *glwRoot, gr *glwRenderer) *glwRendererCache {
	var idx int
	if uint8(root.grFrames&0xff) != gr.grFramecmp {
		gr.grCacheptr = 0
		gr.grFramecmp = uint8(root.grFrames & 0xff)
	} else {
		gr.grCacheptr = (gr.grCacheptr + 1) & (glwRendererCaches - 1)
	}
	idx = int(gr.grCacheptr)

	if gr.grCache[idx] == nil {
		gr.grCache[idx] = &glwRendererCache{}
	}
	return gr.grCache[idx]
}

// addJob — C: add_job (glw_renderer.c:967-1108)
func addJob(gr *glwRoot, m *Mtx,
	t0, t1 *GlwBackendTexture,
	rgbMul, rgbOff *glwRgb,
	alpha, blur float32,
	vertices []float32, numVertices int,
	indices []uint16, numIndices int,
	flags int,
	gpa *GlwProgramArgs,
	rc *glwRctx,
	primitiveType int16,
	zoffset int) {

	if gr.grNumRenderJobs >= gr.grRenderJobsCapacity {
		// Need more space
		oldCapacity := gr.grRenderJobsCapacity
		gr.grRenderJobsCapacity = 100 + gr.grRenderJobsCapacity*2

		nj := make([]GlwRenderJob, gr.grRenderJobsCapacity)
		copy(nj, gr.grRenderJobs)
		gr.grRenderJobs = nj

		// C adjusts ro->job pointers after realloc — Go indices need
		// no fixup (GlwRenderOrder.JobIdx stays valid).
		no := make([]GlwRenderOrder, gr.grRenderJobsCapacity)
		copy(no, gr.grRenderOrder)
		gr.grRenderOrder = no
		_ = oldCapacity
	}

	rj := &gr.grRenderJobs[gr.grNumRenderJobs]
	ro := &gr.grRenderOrder[gr.grNumRenderJobs]

	if m == nil {
		rj.Eyespace = 1
	} else {
		rj.Eyespace = 0
		rj.M = *m
	}

	rj.Width = rc.rcWidth
	rj.Height = rc.rcHeight

	rj.Gpa = gpa
	rj.T0 = t0
	rj.T1 = t1
	rj.PrimitiveType = primitiveType
	ro.Zindex = int16(glwClamp(float32(int(rc.rcZindex)+zoffset), -32768, 32767))
	ro.JobIdx = gr.grNumRenderJobs

	switch gr.grBlendmode {
	case glwBlendNormal:
		rj.RgbMul = *rgbMul
		rj.Alpha = alpha
	case glwBlendAdditive:
		rj.RgbMul.r = rgbMul.r * alpha
		rj.RgbMul.g = rgbMul.g * alpha
		rj.RgbMul.b = rgbMul.b * alpha
		rj.Alpha = 1
	}

	if rgbOff != nil {
		rj.RgbOff = *rgbOff
	} else {
		rj.RgbOff.r = 0
		rj.RgbOff.g = 0
		rj.RgbOff.b = 0
	}

	rj.Blur = blur
	rj.Blendmode = int8(gr.grBlendmode)
	rj.Frontface = int8(gr.grFrontface)
	rj.Flags = int8(flags)

	// -------- Copy indices ---------------
	if indices == nil {
		numIndices = numVertices
	}

	if gr.grIndexOffset+numIndices > gr.grIndexBufferCapacity {
		gr.grIndexBufferCapacity = 100 + numIndices + gr.grIndexBufferCapacity*2
		nb := make([]uint16, gr.grIndexBufferCapacity)
		copy(nb, gr.grIndexBuffer)
		gr.grIndexBuffer = nb
	}

	idst := gr.grIndexBuffer[gr.grIndexOffset:]

	if indices == nil {
		for i := range numIndices {
			idst[i] = uint16(i + gr.grVertexOffset)
		}
	} else {
		for i := range numIndices {
			idst[i] = indices[i] + uint16(gr.grVertexOffset)
		}
	}

	rj.IndexOffset = gr.grIndexOffset
	rj.NumIndices = int16(numIndices)
	gr.grIndexOffset += numIndices

	// -------- Copy vertices ---------------
	if gr.grVertexOffset+numVertices > gr.grVertexBufferCapacity {
		gr.grVertexBufferCapacity = 100 + numVertices + gr.grVertexBufferCapacity*2
		nb := make([]float32, vertexSize*gr.grVertexBufferCapacity)
		copy(nb, gr.grVertexBuffer)
		gr.grVertexBuffer = nb
	}

	vdst := gr.grVertexBuffer[gr.grVertexOffset*vertexSize:]
	copy(vdst, vertices[:numVertices*vertexSize])

	rj.VertexOffset = gr.grVertexOffset
	rj.NumVertices = int16(numVertices)
	gr.grVertexOffset += numVertices

	gr.grNumRenderJobs++
}

// glwRendererDraw — C: glw_renderer_draw (glw_renderer.c:1117-1170)
func glwRendererDraw(gr *glwRenderer, root *glwRoot,
	rc *glwRctx,
	tex, tex2 *GlwBackendTexture,
	rgbMul, rgbOff *glwRgb,
	alpha, blur float32,
	gpa *GlwProgramArgs) {
	if rgbMul == nil {
		rgbMul = &white
	}

	flags := 0

	if root.grNeedSwClip != 0 {
		grc := glwRendererGetCache(root, gr)

		if gr.grDirty ||
			grc.grcMtx != rc.rcMtx ||
			glwRendererClippersCmp(grc, root) != 0 {
			glwRendererTesselate(gr, root, rc, grc)
		}

		if grc.grcBlurred {
			flags |= glwRenderBlurAttribute
		}

		addJob(root, nil, tex, tex2,
			rgbMul, rgbOff, alpha, blur,
			grc.grcVertices, int(grc.grcNumVertices),
			nil, 0, flags, gpa, rc, glwDrawTriangles, int(rc.rcZindex))
	} else {
		addJob(root, &rc.rcMtx, tex, tex2, rgbMul, rgbOff, alpha, blur,
			gr.grVertices, int(gr.grNumVertices),
			gr.grIndices, int(gr.grNumTriangles)*3,
			flags, gpa, rc, glwDrawTriangles, int(rc.rcZindex))
	}
	gr.grDirty = false
}

// C: box_vertices[4][12] (glw_renderer.c:1173-1178) — flat, contiguous
// like the C 2-D array so the whole 4*VERTEX_SIZE block can be copied.
var boxVertices = [4 * 12]float32{
	-1, -1, 0, 0, 1, 1, 1, 1,
	1, -1, 0, 0, 1, 1, 1, 1,
	1, 1, 0, 0, 1, 1, 1, 1,
	-1, 1, 0, 0, 1, 1, 1, 1,
}

// glwWirebox — C: glw_wirebox (glw_renderer.c:1185-1192)
func glwWirebox(root *glwRoot, rc *glwRctx) {
	addJob(root, &rc.rcMtx, nil, nil, &white, nil, 1, 0,
		boxVertices[:], 4,
		nil, 0,
		0, nil, rc, glwDrawLineLoop, 32767)
}

// glwLine — C: glw_line (glw_renderer.c:1198-1211)
func glwLine(root *glwRoot, rc *glwRctx,
	x1, y1, x2, y2 float32,
	r, g, b, alpha float32) {
	lineVertices := [2 * 12]float32{
		x1, y1, 0, 0, r, g, b, alpha,
		x2, y2, 0, 0, r, g, b, alpha,
	}

	addJob(root, &rc.rcMtx, nil, nil, &white, nil, 1, 0,
		lineVertices[:], 2,
		nil, 0,
		0, nil, rc, glwDrawLines, 32767)
}

// C: clip_planes[4][3] (glw_renderer.c:1215-1220)
var clipPlanes = [4][3]float32{
	glwClipTop:    {0.0, -1.0, 0.0},
	glwClipBottom: {0.0, 1.0, 0.0},
	glwClipLeft:   {1.0, 0.0, 0.0},
	glwClipRight:  {-1.0, 0.0, 0.0},
}

// glwClipEnable — C: glw_clip_enable (glw_renderer.c:1229-1257)
func glwClipEnable(gr *glwRoot, rc *glwRctx, how glwClipBoundary,
	distance, alphaOut, sharpnessOut float32) int {
	i := 0
	for ; i < numClipplanes; i++ {
		if gr.grActiveClippers&(1<<uint(i)) == 0 {
			break
		}
	}
	if i == numClipplanes {
		return -1
	}

	v4 := glwVec4Make(clipPlanes[how][0],
		clipPlanes[how][1],
		clipPlanes[how][2],
		1-(distance*2))

	var inv Mtx
	if glwMtxInvert(&inv, &rc.rcMtx) == 0 {
		return -1
	}

	glwMtxTransMulVec4(&gr.grClip[i], &inv, &v4)
	gr.grNeedSwClip = 1

	gr.grActiveClippers |= 1 << uint(i)
	gr.grClipAlphaOut[i] = alphaOut
	gr.grClipSharpnessOut[i] = sharpnessOut
	return i
}

// glwClipDisable — C: glw_clip_disable (glw_renderer.c:1266-1274)
func glwClipDisable(gr *glwRoot, which int) {
	if which == -1 {
		return
	}
	gr.grActiveClippers &^= 1 << uint(which)
	gr.grNeedSwClip = byte(gr.grActiveClippers)
}

// glwFrontface — C: glw_frontface (glw_renderer.c:1382-1386)
func glwFrontface(gr *glwRoot, how int) {
	gr.grFrontface = how
}

// glwBlendmode — C: glw_blendmode (glw_renderer.c:1393-1397)
func glwBlendmode(gr *glwRoot, mode int) {
	gr.grBlendmode = mode
}

// renderOrderCmp — C: render_order_cmp (glw_renderer.c:1403-1421)
// Sorts by zindex, then by t0 texture pointer to minimize texture switches.
func renderOrderCmp(gr *glwRoot, oa, ob GlwRenderOrder) int {
	if oa.Zindex != ob.Zindex {
		return int(oa.Zindex) - int(ob.Zindex)
	}

	aj := gr.grRenderJobs[oa.JobIdx].T0
	bj := gr.grRenderJobs[ob.JobIdx].T0

	// C compares t0 pointers — Go: order by pointer address
	ap := uintptr(unsafe.Pointer(aj))
	bp := uintptr(unsafe.Pointer(bj))
	if ap < bp {
		return -1
	}
	if ap > bp {
		return 1
	}
	return 0
}

// glwRendererRender — C: glw_renderer_render (glw_renderer.c:1428-1439)
func glwRendererRender(gr *glwRoot) {
	// Sort items to render in order:
	//   Front to back, try to minimize texture switches
	slices.SortStableFunc(gr.grRenderOrder[:gr.grNumRenderJobs], func(a, b GlwRenderOrder) int { return renderOrderCmp(gr, a, b) })

	gr.grBeRenderUnlocked(gr)
}
