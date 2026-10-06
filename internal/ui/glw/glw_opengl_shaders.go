package glw

// C: src/ui/glw/glw_opengl_shaders.c — canonical 1:1 port.
// GLSL shader program compile/link + the render job dispatcher
// (render_unlocked → gr_be_render_unlocked) for the desktop OpenGL
// backend. Requires a current GL context on the calling thread.

/*
#define GL_GLEXT_PROTOTYPES
#if defined(__ANDROID__) || defined(MOVIAN_GLES2)
#include <GLES2/gl2.h>
#include <GLES2/gl2ext.h>
#elif defined(__APPLE__)
#include <OpenGL/gl.h>
#include <OpenGL/glext.h>
#else
#include <GL/gl.h>
#include <GL/glext.h>
#endif

#if defined(_WIN32)
#include "glw_win32_gl.h"
#endif
#include <stdlib.h>
#include <stdint.h>

// Fixed-function pipeline helpers — C gates these call sites with
// #if ENABLE_GLW_BACKEND_OPENGL (glw_opengl_shaders.c:214,681). GLES2
// has no fixed pipeline so they compile to no-ops under __ANDROID__;
// the Go side additionally gates them behind enableGLWBackendOpenGL.
static void ml_glLoadMatrix(const GLfloat *m) {
#if !defined(__ANDROID__) && !defined(MOVIAN_GLES2)
    glLoadMatrixf(m);
#endif
}
static void ml_glBeginQuads(void) {
#if !defined(__ANDROID__) && !defined(MOVIAN_GLES2)
    glBegin(GL_QUADS);
#endif
}
static void ml_glEnd(void) {
#if !defined(__ANDROID__) && !defined(MOVIAN_GLES2)
    glEnd();
#endif
}
static void ml_glTexCoord2i(GLint a, GLint b) {
#if !defined(__ANDROID__) && !defined(MOVIAN_GLES2)
    glTexCoord2i(a, b);
#endif
}
static void ml_glVertex3i(GLint a, GLint b, GLint c) {
#if !defined(__ANDROID__) && !defined(MOVIAN_GLES2)
    glVertex3i(a, b, c);
#endif
}
static void ml_glMatrixMode(GLenum m) {
#if !defined(__ANDROID__) && !defined(MOVIAN_GLES2)
    glMatrixMode(m);
#endif
}

// GL_PROJECTION/GL_MODELVIEW don't exist in GLES2 — the mode matrix
// calls are dead code there anyway (enableGLWBackendOpenGL == false).
#if defined(__ANDROID__) || defined(MOVIAN_GLES2)
#define ML_GL_PROJECTION 0
#define ML_GL_MODELVIEW  0
#else
#define ML_GL_PROJECTION GL_PROJECTION
#define ML_GL_MODELVIEW  GL_MODELVIEW
#endif

// GL vertex attribute offsets are byte offsets into the bound VBO, not
// real pointers — C passes (const GLvoid*)NULL + n. The helper keeps the
// (void*)n cast inside C so no fake pointer is ever stored in a
// pointer-typed Go stack slot (a stack copy/shrink would treat it as an
// invalid pointer and abort).
static void glw_vertex_attrib_pointer(GLuint idx, GLint size, GLsizei stride, intptr_t off) {
    glVertexAttribPointer(idx, size, GL_FLOAT, 0, stride, (void *)off);
}
*/
import "C"

import (
	"fmt"
	"slices"
	"unsafe"

	archpkg "github.com/czz/movian-go/internal/arch"
	facore "github.com/czz/movian-go/internal/fileaccess"
	tracepkg "github.com/czz/movian-go/internal/trace"
)

// C: projection[16] (glw_opengl_shaders.c:28-33) — ENABLE_GLW_BACKEND_OPENGL
var projection = [16]float32{
	2.414213, 0.000000, 0.000000, 0.000000,
	0.000000, 2.414213, 0.000000, 0.000000,
	0.000000, 0.000000, 1.033898, -1.000000,
	0.000000, 0.000000, 2.033898, 0.000000,
}

// C: render_state_t (glw_opengl_shaders.c:36-41)
type renderStateT struct {
	t0              *GlwBackendTexture // C: t0
	t1              *GlwBackendTexture // C: t1
	texloadSkips    int                // C: texload_skips
	programSwitches int                // C: program_switches
}

// glwMtxGet — C: glw_mtx_get(m) (glw_math_c.h:179) = (const float*)(&m)
func glwMtxGet(m *Mtx) *C.GLfloat {
	return (*C.GLfloat)(unsafe.Pointer(&m.r[0]))
}

// C: use_program (glw_opengl_shaders.c:47-55)
func useProgram(gbr *glwBackendRootT, gp *glwProgram, rs *renderStateT) {
	if gbr.gbrCurrent == gp {
		return
	}

	gbr.gbrCurrent = gp
	if gp != nil {
		C.glUseProgram(C.GLuint(gp.gpProgram))
	} else {
		C.glUseProgram(0)
	}
	rs.programSwitches++
}

// C: load_program (glw_opengl_shaders.c:62-158)
func loadProgram(gr *glwRoot,
	t0 *GlwBackendTexture,
	t1 *GlwBackendTexture,
	blur float32, flags int,
	gpa *GlwProgramArgs,
	rs *renderStateT,
	rj *GlwRenderJob) *glwProgram {

	var gp *glwProgram
	gbr := &gr.grBe

	if gpa != nil {

		rs.t0 = nil
		rs.t1 = nil

		if t1 != nil {

			if gpa.GpaLoadTexture != nil {
				// Program has specialized code to load textures, run it
				gpa.GpaLoadTexture(gr, gpa.GpaProg, gpa.GpaAux, t1, 1)
			} else {
				C.glActiveTexture(C.GL_TEXTURE1)
				C.glBindTexture(C.GL_TEXTURE_2D, C.GLuint(t1.Textures[0]))
				C.glActiveTexture(C.GL_TEXTURE0)
			}
		}

		if t0 != nil {

			if gpa.GpaLoadTexture != nil {
				// Program has specialized code to load textures, run it
				gpa.GpaLoadTexture(gr, gpa.GpaProg, gpa.GpaAux, t0, 0)
			} else {
				C.glBindTexture(C.GL_TEXTURE_2D, C.GLuint(t0.Textures[0]))
			}
		}

		useProgram(gbr, gpa.GpaProg, rs)

		if gpa.GpaLoadUniforms != nil {
			gpa.GpaLoadUniforms(gr, gpa.GpaProg, gpa.GpaAux, rj)
		}

		return gpa.GpaProg
	}

	if t0 == nil {

		if t1 != nil {
			gp = gbr.gbrRendererFlatStencil

			if rs.t0 != t1 {
				C.glBindTexture(C.GL_TEXTURE_2D, C.GLuint(t1.Textures[0]))
				rs.t0 = t1
			} else {
				rs.texloadSkips++
			}

		} else {
			gp = gbr.gbrRendererFlat
		}

	} else {

		doblur := blur > 0.05 || flags&glwRenderBlurAttribute != 0

		if t1 != nil {

			if doblur {
				gp = gbr.gbrRendererTexStencilBlur
			} else {
				gp = gbr.gbrRendererTexStencil
			}

			if rs.t1 != t1 {
				C.glActiveTexture(C.GL_TEXTURE1)
				C.glBindTexture(C.GL_TEXTURE_2D, C.GLuint(t1.Textures[0]))
				C.glActiveTexture(C.GL_TEXTURE0)
				rs.t1 = t1
			} else {
				rs.texloadSkips++
			}

		} else if doblur {
			gp = gbr.gbrRendererTexBlur
		} else {
			gp = gbr.gbrRendererTex
		}

		if rs.t0 != t0 {
			C.glBindTexture(C.GL_TEXTURE_2D, C.GLuint(t0.Textures[0]))
			rs.t0 = t0
		} else {
			rs.texloadSkips++
		}
	}
	useProgram(gbr, gp, rs)
	return gp
}

// C: render_unlocked (glw_opengl_shaders.c:164-360)
func renderUnlocked(gr *glwRoot) {
	gbr := &gr.grBe
	var rs renderStateT
	ts := archpkg.GetTS()
	uniCalls := 0
	savedCalls := 0
	currentBlendmode := glwBlendNormal

	C.glBlendFuncSeparate(C.GL_SRC_ALPHA, C.GL_ONE_MINUS_SRC_ALPHA,
		C.GL_ONE_MINUS_DST_ALPHA, C.GL_ONE)

	C.glBindBuffer(C.GL_ARRAY_BUFFER, C.GLuint(gbr.gbrVbo))
	var vptr unsafe.Pointer
	if len(gr.grVertexBuffer) > 0 {
		vptr = unsafe.Pointer(&gr.grVertexBuffer[0])
	}
	C.glBufferData(C.GL_ARRAY_BUFFER,
		C.GLsizeiptr(4*vertexSize*gr.grVertexOffset),
		vptr, C.GL_STATIC_DRAW)

	currentFrontface := glwCcw
	C.glFrontFace(C.GL_CCW)

	C.glw_vertex_attrib_pointer(0, 4, C.GLsizei(4*vertexSize), 0)
	C.glw_vertex_attrib_pointer(1, 4, C.GLsizei(4*vertexSize), 16)
	C.glw_vertex_attrib_pointer(2, 4, C.GLsizei(4*vertexSize), 32)

	for j := range gr.grNumRenderJobs {
		ro := &gr.grRenderOrder[j]
		rj := &gr.grRenderJobs[ro.JobIdx]

		if rj.NumVertices == 0 {
			continue
		}

		t0 := rj.T0

		gp := loadProgram(gr, t0, rj.T1, rj.Blur, int(rj.Flags), rj.Gpa, &rs, rj)

		if gbr.gbrUseStencilBuffer != 0 {
			C.glStencilFunc(C.GL_GEQUAL, C.GLint(ro.Zindex), 0xFF)
		}

		if gp == nil {

			// ENABLE_GLW_BACKEND_OPENGL fixed-function fallback —
			// compiled out under the ES backend (enableGLWBackendOpenGL).
			if enableGLWBackendOpenGL {
				if rj.Eyespace != 0 {
					C.ml_glLoadMatrix((*C.GLfloat)(unsafe.Pointer(&glwIdentitymtx[0])))
				} else {
					C.ml_glLoadMatrix(glwMtxGet(&rj.M))
				}

				C.ml_glBeginQuads()
				if t0 != nil {
					C.ml_glTexCoord2i(0, C.GLint(t0.Height))
				}
				C.ml_glVertex3i(-1, -1, 0)

				if t0 != nil {
					C.ml_glTexCoord2i(C.GLint(t0.Width), C.GLint(t0.Height))
				}
				C.ml_glVertex3i(1, -1, 0)

				if t0 != nil {
					C.ml_glTexCoord2i(C.GLint(t0.Width), 0)
				}
				C.ml_glVertex3i(1, 1, 0)

				if t0 != nil {
					C.ml_glTexCoord2i(0, 0)
				}
				C.ml_glVertex3i(-1, 1, 0)

				C.ml_glEnd()

				C.glDisable(C.GLenum(t0.Gltype))
			}
			continue

		}

		if !glwRgbCmp(&gp.gpCurrentColorOffset, &rj.RgbOff) {
			glwRgbCpy(&gp.gpCurrentColorOffset, &rj.RgbOff)
			C.glUniform4f(C.GLint(gp.gpUniformColorOffset),
				C.GLfloat(rj.RgbOff.r), C.GLfloat(rj.RgbOff.g),
				C.GLfloat(rj.RgbOff.b), 0)
			uniCalls++
		} else {
			savedCalls++
		}

		if !glwRgbCmp(&gp.gpCurrentColorMul, &rj.RgbMul) ||
			gp.gpCurrentAlpha != rj.Alpha {
			glwRgbCpy(&gp.gpCurrentColorMul, &rj.RgbMul)
			gp.gpCurrentAlpha = rj.Alpha
			C.glUniform4f(C.GLint(gbr.gbrCurrent.gpUniformColor),
				C.GLfloat(rj.RgbMul.r), C.GLfloat(rj.RgbMul.g),
				C.GLfloat(rj.RgbMul.b), C.GLfloat(rj.Alpha))
			uniCalls++
		} else {
			savedCalls++
		}

		if gp.gpUniformTime != -1 {
			C.glUniform1f(C.GLint(gp.gpUniformTime), C.GLfloat(gr.grTimeSec))
			uniCalls++
		}

		if gp.gpUniformResolution != -1 {
			C.glUniform3f(C.GLint(gp.gpUniformResolution),
				C.GLfloat(rj.Width), C.GLfloat(rj.Height), 1)
			uniCalls++
		}

		if gp.gpUniformBlur != -1 && t0 != nil {
			C.glUniform3f(C.GLint(gp.gpUniformBlur), C.GLfloat(rj.Blur),
				C.GLfloat(1.5/float32(t0.Width)),
				C.GLfloat(1.5/float32(t0.Height)))
			uniCalls++
		}

		if rj.Eyespace != 0 {

			if gp.gpIdentityMvm == 0 {
				C.glUniformMatrix4fv(C.GLint(gp.gpUniformModelview), 1, 0,
					(*C.GLfloat)(unsafe.Pointer(&glwIdentitymtx[0])))
				gp.gpIdentityMvm = 1
				uniCalls++
			} else {
				savedCalls++
			}
		} else {
			gp.gpIdentityMvm = 0
			C.glUniformMatrix4fv(C.GLint(gp.gpUniformModelview), 1, 0,
				glwMtxGet(&rj.M))
			uniCalls++
		}

		if currentBlendmode != int(rj.Blendmode) {
			currentBlendmode = int(rj.Blendmode)
			switch currentBlendmode {
			case glwBlendNormal:
				C.glBlendFuncSeparate(C.GL_SRC_ALPHA, C.GL_ONE_MINUS_SRC_ALPHA,
					C.GL_ONE_MINUS_DST_ALPHA, C.GL_ONE)

			case glwBlendAdditive:
				C.glBlendFuncSeparate(C.GL_SRC_COLOR, C.GL_ONE,
					C.GL_ONE_MINUS_DST_ALPHA, C.GL_ONE)
			}
		}

		if currentFrontface != int(rj.Frontface) {
			currentFrontface = int(rj.Frontface)
			if currentFrontface == glwCw {
				C.glFrontFace(C.GL_CW)
			} else {
				C.glFrontFace(C.GL_CCW)
			}
		}

		var iptr unsafe.Pointer
		if rj.NumIndices > 0 && rj.IndexOffset < len(gr.grIndexBuffer) {
			iptr = unsafe.Pointer(&gr.grIndexBuffer[rj.IndexOffset])
		}
		C.glDrawElements(C.GLenum(rj.PrimitiveType),
			C.GLsizei(rj.NumIndices),
			C.GL_UNSIGNED_SHORT, iptr)
	}

	if currentBlendmode != glwBlendNormal {
		C.glBlendFuncSeparate(C.GL_SRC_COLOR, C.GL_ONE,
			C.GL_ONE_MINUS_DST_ALPHA, C.GL_ONE)
	}
	tse := archpkg.GetTS()
	ts = tse - ts

	// C: static int hold++ / if(hold < 20) return / #if 0 performance
	// dump (glw_opengl_shaders.c:333-359) — the dump is compiled out in
	// C, so hold is dead state there too; both omitted.
}

// shaderPath — C: SHADERPATH (glw_opengl_shaders.c:518-519)
func shaderPath(filename string) string {
	return fmt.Sprintf("dataroot://res/shaders/glsl/%s", filename)
}

// C: glw_compile_shader (glw_opengl_shaders.c:366-399)
func glwCompileShader(path string, stype int, gr *glwRoot) uint32 {
	b, err := facore.FALoad2(glwDeps.fam, path, nil)
	if b == nil || err != nil {
		msg := "load failed"
		if err != nil {
			msg = err.Error()
		}
		glwDeps.ts.Trace(tracepkg.TRACE_ERROR, "glw",
			"Unable to load shader %s -- %s", path, msg)
		return 0
	}

	// C: buf_make_writable + buf_str — the C string is NUL-terminated
	// (fa_load allocates size+1, mem[size]=0). glShaderSource with
	// length=NULL requires the terminating NUL.
	csrc := C.CBytes(append(b.Data, 0))
	defer C.free(csrc)

	s := C.glCreateShader(C.GLenum(stype))
	psrc := (*C.GLchar)(csrc)
	C.glShaderSource(s, 1, &psrc, nil)

	C.glCompileShader(s)
	var v, lglen C.GLint
	var log [4096]C.char
	C.glGetShaderInfoLog(s, C.GLsizei(len(log)), &lglen,
		(*C.GLchar)(unsafe.Pointer(&log[0])))
	C.glGetShaderiv(s, C.GL_COMPILE_STATUS, &v)

	if v == 0 {
		glwDeps.ts.Trace(tracepkg.TRACE_ERROR, "GLW",
			"Unable to compile shader %s", path)
		glwDeps.ts.Trace(tracepkg.TRACE_ERROR, "GLW",
			"%s", C.GoString((*C.char)(unsafe.Pointer(&log[0]))))
		return 0
	}
	return uint32(s)
}

// C: glw_link_program (glw_opengl_shaders.c:405-494)
func glwLinkProgram(gbr *glwBackendRootT, title string,
	vs, fs uint32) *glwProgram {
	var v C.GLint

	p := C.glCreateProgram()
	C.glAttachShader(p, C.GLuint(vs))
	C.glAttachShader(p, C.GLuint(fs))

	aPosition := C.CString("a_position")
	C.glBindAttribLocation(p, 0, aPosition)
	C.free(unsafe.Pointer(aPosition))
	aColor := C.CString("a_color")
	C.glBindAttribLocation(p, 1, aColor)
	C.free(unsafe.Pointer(aColor))
	aTexcoord := C.CString("a_texcoord")
	C.glBindAttribLocation(p, 2, aTexcoord)
	C.free(unsafe.Pointer(aTexcoord))

	C.glLinkProgram(p)

	var lglen C.GLsizei
	var log [4096]C.char
	C.glGetProgramInfoLog(p, C.GLsizei(len(log)), &lglen,
		(*C.GLchar)(unsafe.Pointer(&log[0])))

	C.glGetProgramiv(p, C.GL_LINK_STATUS, &v)
	if v == 0 {
		glwDeps.ts.Trace(tracepkg.TRACE_ERROR, "GLW",
			"Unable to link shader %s", title)
		glwDeps.ts.Trace(tracepkg.TRACE_ERROR, "GLW",
			"%s", C.GoString((*C.char)(unsafe.Pointer(&log[0]))))
		return nil
	}

	gp := &glwProgram{}

	gp.gpTitle = title
	gp.gpProgram = uint32(p)

	C.glUseProgram(p)
	gbr.gbrCurrent = gp

	gp.gpAttributePosition = 0
	gp.gpAttributeColor = 1
	gp.gpAttributeTexcoord = 2

	gp.gpUniformModelview = int32(glwGetUniformLocation(p, "u_modelview"))
	gp.gpUniformColor = int32(glwGetUniformLocation(p, "u_color"))
	gp.gpUniformColormtx = int32(glwGetUniformLocation(p, "u_colormtx"))
	gp.gpUniformBlend = int32(glwGetUniformLocation(p, "u_blend"))
	gp.gpUniformColorOffset = int32(glwGetUniformLocation(p, "u_color_offset"))
	gp.gpUniformBlur = int32(glwGetUniformLocation(p, "u_blur"))

	gp.gpUniformTime = int32(glwGetUniformLocation(p, "iGlobalTime"))
	gp.gpUniformResolution = int32(glwGetUniformLocation(p, "iResolution"))

	for i := range 6 {
		gp.gpUniformT[i] = int32(glwGetUniformLocation(p, fmt.Sprintf("u_t%d", i)))

		if gp.gpUniformT[i] == -1 {
			gp.gpUniformT[i] = int32(glwGetUniformLocation(p, fmt.Sprintf("iChannel%d", i)))
		}

		if gp.gpUniformT[i] != -1 {
			C.glUniform1i(C.GLint(gp.gpUniformT[i]), C.GLint(i))
		}
	}

	// C: LIST_INSERT_HEAD(&gbr->gbr_programs, gp, gp_link)
	gbr.gbrPrograms = slices.Insert(gbr.gbrPrograms, 0, gp)

	return gp
}

// glwGetUniformLocation — CString wrapper for glGetUniformLocation
func glwGetUniformLocation(p C.GLuint, name string) C.GLint {
	cs := C.CString(name)
	loc := C.glGetUniformLocation(p, cs)
	C.free(unsafe.Pointer(cs))
	return loc
}

// C: glw_program_set_modelview (glw_opengl_shaders.c:500-505)
func glwProgramSetModelview(gbr *glwBackendRootT, rc *glwRctx) {
	var m *C.GLfloat
	if rc != nil {
		m = glwMtxGet(&rc.rcMtx)
	} else {
		m = (*C.GLfloat)(unsafe.Pointer(&glwIdentitymtx[0]))
	}
	C.glUniformMatrix4fv(C.GLint(gbr.gbrCurrent.gpUniformModelview), 1, 0, m)
}

// C: glw_program_set_uniform_color (glw_opengl_shaders.c:510-515)
func glwProgramSetUniformColor(gbr *glwBackendRootT,
	r, g, b, a float32) {
	C.glUniform4f(C.GLint(gbr.gbrCurrent.gpUniformColor),
		C.GLfloat(r), C.GLfloat(g), C.GLfloat(b), C.GLfloat(a))
}

// C: glw_make_program (glw_opengl_shaders.c:524-548)
func glwMakeProgram(gr *glwRoot, vertexShader string,
	fragmentShader string) *glwProgram {

	vs := glwCompileShader(
		orDefault(vertexShader, shaderPath("v1.glsl")),
		C.GL_VERTEX_SHADER, gr)
	if vs == 0 {
		return nil
	}
	fs := glwCompileShader(fragmentShader, C.GL_FRAGMENT_SHADER, gr)
	if fs == 0 {
		s := C.GLuint(vs)
		C.glDeleteShader(s)
		return nil
	}

	p := glwLinkProgram(&gr.grBe, "user shader", vs, fs)
	C.glDeleteShader(C.GLuint(vs))
	C.glDeleteShader(C.GLuint(fs))

	return p
}

// orDefault — C: GNU ?: operator (vertex_shader ?: path)
func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// C: glw_destroy_program (glw_opengl_shaders.c:555-564)
func glwDestroyProgram(gr *glwRoot, gp *glwProgram) {
	if gp == nil {
		return
	}
	// C: LIST_REMOVE(gp, gp_link)
	gbr := &gr.grBe
	for i, p := range gbr.gbrPrograms {
		if p == gp {
			gbr.gbrPrograms = append(
				gbr.gbrPrograms[:i], gbr.gbrPrograms[i+1:]...)
			break
		}
	}
	// C: free(gp->gp_title) — Go GC
	C.glDeleteProgram(C.GLuint(gp.gpProgram))
}

// C: glw_opengl_shaders_init (glw_opengl_shaders.c:570-688)
func glwOpenglShadersSetup(gr *glwRoot) int {
	gbr := &gr.grBe

	vs := glwCompileShader(shaderPath("v1.glsl"), C.GL_VERTEX_SHADER, gr)

	fs := glwCompileShader(shaderPath("f_tex.glsl"), C.GL_FRAGMENT_SHADER, gr)
	gbr.gbrRendererTex = glwLinkProgram(gbr, "Texture", vs, fs)
	C.glDeleteShader(C.GLuint(fs))

	fs = glwCompileShader(shaderPath("f_tex_stencil.glsl"), C.GL_FRAGMENT_SHADER, gr)
	gbr.gbrRendererTexStencil = glwLinkProgram(gbr, "TextureStencil", vs, fs)
	C.glDeleteShader(C.GLuint(fs))

	fs = glwCompileShader(shaderPath("f_tex_blur.glsl"), C.GL_FRAGMENT_SHADER, gr)
	gbr.gbrRendererTexBlur = glwLinkProgram(gbr, "TextureBlur", vs, fs)
	C.glDeleteShader(C.GLuint(fs))

	fs = glwCompileShader(shaderPath("f_tex_stencil_blur.glsl"), C.GL_FRAGMENT_SHADER, gr)
	gbr.gbrRendererTexStencilBlur = glwLinkProgram(gbr, "TextureStencilBlur", vs, fs)
	C.glDeleteShader(C.GLuint(fs))

	fs = glwCompileShader(shaderPath("f_flat.glsl"), C.GL_FRAGMENT_SHADER, gr)
	gbr.gbrRendererFlat = glwLinkProgram(gbr, "Flat", vs, fs)
	C.glDeleteShader(C.GLuint(fs))

	fs = glwCompileShader(shaderPath("f_flat_stencil.glsl"), C.GL_FRAGMENT_SHADER, gr)
	gbr.gbrRendererFlatStencil = glwLinkProgram(gbr, "FlatStencil", vs, fs)
	C.glDeleteShader(C.GLuint(fs))

	C.glDeleteShader(C.GLuint(vs))

	// yuv2rgb Video renderer

	vs = glwCompileShader(shaderPath("yuv2rgb_v.glsl"), C.GL_VERTEX_SHADER, gr)

	fs = glwCompileShader(shaderPath("yuv2rgb_1f_norm.glsl"), C.GL_FRAGMENT_SHADER, gr)
	gbr.gbrYuv2rgb1f = glwLinkProgram(gbr, "yuv2rgb_1f_norm", vs, fs)
	C.glDeleteShader(C.GLuint(fs))

	fs = glwCompileShader(shaderPath("yuv2rgb_2f_norm.glsl"), C.GL_FRAGMENT_SHADER, gr)
	gbr.gbrYuv2rgb2f = glwLinkProgram(gbr, "yuv2rgb_2f_norm", vs, fs)
	C.glDeleteShader(C.GLuint(fs))
	C.glDeleteShader(C.GLuint(vs))

	// rgb2rgb Video renderer

	vs = glwCompileShader(shaderPath("rgb2rgb_v.glsl"), C.GL_VERTEX_SHADER, gr)

	fs = glwCompileShader(shaderPath("rgb2rgb_1f_norm.glsl"), C.GL_FRAGMENT_SHADER, gr)
	gbr.gbrRgb2rgb1f = glwLinkProgram(gbr, "rgb2rgb_1f_norm", vs, fs)
	C.glDeleteShader(C.GLuint(fs))

	fs = glwCompileShader(shaderPath("rgb2rgb_2f_norm.glsl"), C.GL_FRAGMENT_SHADER, gr)
	gbr.gbrRgb2rgb2f = glwLinkProgram(gbr, "rgb2rgb_2f_norm", vs, fs)
	C.glDeleteShader(C.GLuint(fs))
	C.glDeleteShader(C.GLuint(vs))

	// yc2rgb Video renderer

	vs = glwCompileShader(shaderPath("yc2rgb_v.glsl"), C.GL_VERTEX_SHADER, gr)

	fs = glwCompileShader(shaderPath("yc2rgb_1f_norm.glsl"), C.GL_FRAGMENT_SHADER, gr)
	gbr.gbrYc2rgb1f = glwLinkProgram(gbr, "yc2rgb_1f_norm", vs, fs)
	C.glDeleteShader(C.GLuint(fs))

	fs = glwCompileShader(shaderPath("yc2rgb_2f_norm.glsl"), C.GL_FRAGMENT_SHADER, gr)
	gbr.gbrYc2rgb2f = glwLinkProgram(gbr, "yc2rgb_2f_norm", vs, fs)
	C.glDeleteShader(C.GLuint(fs))
	C.glDeleteShader(C.GLuint(vs))

	// nv12 Video renderer (VAAPI-EGL dmabuf import) — extension, no C
	// counterpart. Same vertex shader as yc2rgb; fragment shaders
	// sample the UV plane from .r/.g (GR88) instead of .r/.a.

	vs = glwCompileShader(shaderPath("yc2rgb_v.glsl"), C.GL_VERTEX_SHADER, gr)

	fs = glwCompileShader(shaderPath("nv12_1f_norm.glsl"), C.GL_FRAGMENT_SHADER, gr)
	gbr.gbrNv121f = glwLinkProgram(gbr, "nv12_1f_norm", vs, fs)
	C.glDeleteShader(C.GLuint(fs))

	fs = glwCompileShader(shaderPath("nv12_2f_norm.glsl"), C.GL_FRAGMENT_SHADER, gr)
	gbr.gbrNv122f = glwLinkProgram(gbr, "nv12_2f_norm", vs, fs)
	C.glDeleteShader(C.GLuint(fs))
	C.glDeleteShader(C.GLuint(vs))

	// --------

	gr.grBeRenderUnlocked = renderUnlocked
	C.glGenBuffers(1, (*C.GLuint)(unsafe.Pointer(&gbr.gbrVbo)))

	C.glEnableVertexAttribArray(0)
	C.glEnableVertexAttribArray(1)
	C.glEnableVertexAttribArray(2)

	gr.grPropUi.CreateString("rendermode", "OpenGL VP/FP shaders")

	// ENABLE_GLW_BACKEND_OPENGL fixed-function projection
	if enableGLWBackendOpenGL {
		C.ml_glMatrixMode(C.ML_GL_PROJECTION)
		C.ml_glLoadMatrix((*C.GLfloat)(unsafe.Pointer(&projection[0])))
		C.ml_glMatrixMode(C.ML_GL_MODELVIEW)
	}

	return 0
}

// C: glw_opengl_shaders_fini (glw_opengl_shaders.c:694-702)
func glwOpenglShadersFini(gr *glwRoot) {
	gbr := &gr.grBe

	for len(gbr.gbrPrograms) > 0 {
		glwDestroyProgram(gr, gbr.gbrPrograms[0])
	}
}

// C: stencilquad (glw_opengl_shaders.c:707-715)
var stencilquad = [6][vertexSize]float32{
	{-1, -1, 0, 0, 1, 1, 1, 1, 0, 0, 0, 0},
	{1, -1, 0, 0, 1, 1, 1, 1, 0, 0, 0, 0},
	{1, 1, 0, 0, 1, 1, 1, 1, 0, 0, 0, 0},

	{-1, -1, 0, 0, 1, 1, 1, 1, 0, 0, 0, 0},
	{1, 1, 0, 0, 1, 1, 1, 1, 0, 0, 0, 0},
	{-1, 1, 0, 0, 1, 1, 1, 1, 0, 0, 0, 0},
}

// C: glw_stencil_quad (glw_opengl_shaders.c:721-755)
func glwStencilQuad(gr *glwRoot, rc *glwRctx) {
	gbr := &gr.grBe
	gp := gbr.gbrRendererFlat

	C.glStencilFunc(C.GL_NEVER, C.GLint(rc.rcZindex), 0xFF)
	C.glStencilOp(C.GL_REPLACE, C.GL_KEEP, C.GL_KEEP)
	C.glStencilMask(0xff)

	C.glUseProgram(C.GLuint(gp.gpProgram))

	C.glUniformMatrix4fv(C.GLint(gp.gpUniformModelview), 1, 0,
		glwMtxGet(&rc.rcMtx))

	C.glBindBuffer(C.GL_ARRAY_BUFFER, 0)

	C.glFrontFace(C.GL_CCW)

	C.glVertexAttribPointer(0, 4, C.GL_FLOAT, 0,
		C.GLsizei(4*vertexSize),
		unsafe.Pointer(&stencilquad[0][0]))

	C.glVertexAttribPointer(1, 4, C.GL_FLOAT, 0,
		C.GLsizei(4*vertexSize),
		unsafe.Pointer(&stencilquad[0][4]))

	C.glVertexAttribPointer(2, 4, C.GL_FLOAT, 0,
		C.GLsizei(4*vertexSize),
		unsafe.Pointer(&stencilquad[0][8]))

	C.glDrawArrays(C.GL_TRIANGLES, 0, 6)

	C.glUseProgram(0)
	gbr.gbrCurrent = nil
}
