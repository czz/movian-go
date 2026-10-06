//go:build !android && !rpi

package glw

// C: src/ui/glw/glw_opengl_ogl.c — canonical 1:1 port.
// Desktop-OpenGL backend context: render-to-texture (FBO) support,
// screenshot readback and context init/fini. Requires a current GL
// context — all entry points are called from glw_x11.c's window code
// in C; the Go port wires them the same way when glw_x11 lands.

/*
#define GL_GLEXT_PROTOTYPES
#ifdef __ANDROID__
#include <GLES2/gl2.h>
#include <GLES2/gl2ext.h>
#elif defined(__APPLE__)
#include <OpenGL/gl.h>
#else
#include <GL/gl.h>
#endif
#ifdef __APPLE__
#include <OpenGL/glext.h>
#else
#include <GL/glext.h>
#endif

#if defined(_WIN32)
#include "glw_win32_gl.h"
#endif
#include <stdlib.h>
*/
import "C"

import (
	"unsafe"

	imagepkg "github.com/czz/movian-go/internal/image"
	tracepkg "github.com/czz/movian-go/internal/trace"
)

// C: glw_rtt_init (glw_opengl_ogl.c:28-59)
func glwRttSetup(gr *glwRoot, grtt *glwRtt, width, height, alpha int) {
	m := C.GLenum(C.GL_TEXTURE_2D)

	grtt.grttWidth = width
	grtt.grttHeight = height

	C.glGenTextures(1, (*C.GLuint)(unsafe.Pointer(&grtt.grttTexture.Textures[0])))

	C.glBindTexture(m, C.GLuint(grtt.grttTexture.Textures[0]))
	C.glTexParameteri(m, C.GL_TEXTURE_MIN_FILTER, C.GL_LINEAR)
	C.glTexParameteri(m, C.GL_TEXTURE_MAG_FILTER, C.GL_LINEAR)
	C.glTexParameteri(m, C.GL_TEXTURE_WRAP_S, C.GL_CLAMP_TO_EDGE)
	C.glTexParameteri(m, C.GL_TEXTURE_WRAP_T, C.GL_CLAMP_TO_EDGE)

	var mode C.GLint
	if alpha != 0 {
		mode = C.GL_RGBA
	} else {
		mode = C.GL_RGB
	}

	C.glPixelStorei(C.GL_UNPACK_ALIGNMENT, 1)
	C.glPixelStorei(C.GL_UNPACK_ROW_LENGTH, 0)

	C.glTexImage2D(m, 0, mode, C.GLsizei(width), C.GLsizei(height), 0,
		C.GLenum(mode), C.GL_UNSIGNED_BYTE, nil)
	C.glPixelStorei(C.GL_UNPACK_ALIGNMENT, pixmapRowAlign)
	C.glGenFramebuffersEXT(1, (*C.GLuint)(unsafe.Pointer(&grtt.grttFramebuffer)))
	C.glBindFramebufferEXT(C.GL_FRAMEBUFFER_EXT, C.GLuint(grtt.grttFramebuffer))
	C.glFramebufferTexture2DEXT(C.GL_FRAMEBUFFER_EXT,
		C.GL_COLOR_ATTACHMENT0_EXT,
		m, C.GLuint(grtt.grttTexture.Textures[0]), 0)

	C.glBindFramebufferEXT(C.GL_FRAMEBUFFER_EXT, 0)
}

// C: glw_rtt_enter (glw_opengl_ogl.c:66-80)
// NOTE: the canonical C contains an unconditional abort() at line 78 —
// RTT rendering crashes the C build before ever reaching
// glw_rctx_init. The abort is preserved verbatim: any caller
// (glw_bloom) aborts identically.
func glwRttEnter(gr *glwRoot, grtt *glwRtt, rc *glwRctx) {
	/* Save viewport */
	C.glGetIntegerv(C.GL_VIEWPORT, (*C.GLint)(unsafe.Pointer(&grtt.grttViewport[0])))

	C.glBindTexture(C.GL_TEXTURE_2D, 0)
	C.glBindFramebufferEXT(C.GL_FRAMEBUFFER_EXT, C.GLuint(grtt.grttFramebuffer))

	C.glViewport(0, 0, C.GLsizei(grtt.grttWidth), C.GLsizei(grtt.grttHeight))

	C.glClear(C.GL_COLOR_BUFFER_BIT)

	C.abort() // C: abort() — canonical bug preserved
	glwRctxSetup(rc, grtt.grttWidth, grtt.grttHeight, 0, nil)
}

// C: glw_rtt_restore (glw_opengl_ogl.c:87-96)
func glwRttRestore(gr *glwRoot, grtt *glwRtt) {
	C.glBindFramebufferEXT(C.GL_FRAMEBUFFER_EXT, 0)

	/* Restore viewport */
	C.glViewport(C.GLint(grtt.grttViewport[0]),
		C.GLint(grtt.grttViewport[1]),
		C.GLsizei(grtt.grttViewport[2]),
		C.GLsizei(grtt.grttViewport[3]))
}

// C: glw_rtt_destroy (glw_opengl_ogl.c:103-107)
func glwRttDestroy(gr *glwRoot, grtt *glwRtt) {
	t := grtt.grttTexture.Textures[0]
	C.glDeleteTextures(1, (*C.GLuint)(unsafe.Pointer(&t)))
	grtt.grttTexture.Textures[0] = t
	fb := grtt.grttFramebuffer
	C.glDeleteFramebuffersEXT(1, (*C.GLuint)(unsafe.Pointer(&fb)))
}

// C: opengl_read_pixels (glw_opengl_ogl.c:114-122) — screenshot readback.
func openglReadPixels(gr *glwRoot) *imagepkg.Pixmap {
	pm := imagepkg.PixmapCreate(gr.grWidth, gr.grHeight, imagepkg.PixmapBGR32, 0)

	C.glReadPixels(0, 0, C.GLsizei(gr.grWidth), C.GLsizei(gr.grHeight),
		C.GL_BGRA, C.GL_UNSIGNED_BYTE, unsafe.Pointer(&pm.Data[0]))
	pm.Flags |= imagepkg.PixmapVflip
	return pm
}

// C: glw_opengl_init_context (glw_opengl_ogl.c:129-157)
func glwOpenglSetupContext(gr *glwRoot) int {
	var tu C.GLint

	C.glEnable(C.GL_BLEND)
	C.glBlendFunc(C.GL_SRC_ALPHA, C.GL_ONE_MINUS_SRC_ALPHA)
	C.glEnable(C.GL_CULL_FACE)

	C.glPixelStorei(C.GL_UNPACK_ALIGNMENT, pixmapRowAlign)
	C.glPixelStorei(C.GL_PACK_ALIGNMENT, pixmapRowAlign)

	C.glGetIntegerv(C.GL_MAX_TEXTURE_IMAGE_UNITS, &tu)
	if tu < 6 {
		glwDeps.ts.Trace(tracepkg.TRACE_ERROR, "GLW",
			"Insufficient number of texture image units %d < 6 "+
				"for GLW video rendering widget.", int(tu))
		return -1
	}
	glwDeps.ts.Trace(tracepkg.TRACE_DEBUG, "GLW",
		"%d texture image units available", int(tu))

	vendor := C.GoString((*C.char)(unsafe.Pointer(C.glGetString(C.GL_VENDOR))))
	renderer := C.GoString((*C.char)(unsafe.Pointer(C.glGetString(C.GL_RENDERER))))
	glwDeps.ts.Trace(tracepkg.TRACE_INFO, "GLW",
		"OpenGL Renderer: '%s' by '%s'", renderer, vendor)

	gr.grBrReadPixels = openglReadPixels

	return glwOpenglShadersSetup(gr)
}

// C: glw_opengl_fini_context (glw_opengl_ogl.c:164-167)
func glwOpenglFiniContext(gr *glwRoot) {
	glwOpenglShadersFini(gr)
}
