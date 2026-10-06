//go:build android || rpi

package glw

// C: src/ui/glw/glw_opengl_es.c — canonical 1:1 port.
// OpenGL ES 2 backend context: init + fini + (empty) rtt hooks.

/*
#include <GLES2/gl2.h>
#include <GLES2/gl2ext.h>
*/
import "C"

import (
	"unsafe"

	tracepkg "github.com/czz/movian-go/internal/trace"
)

// C: glw_opengl_init_context (glw_opengl_es.c:26-42). No
// GL_MAX_TEXTURE_IMAGE_UNITS check — ES version skips it and goes
// straight to shader init.
func glwOpenglSetupContext(gr *glwRoot) int {
	C.glEnable(C.GL_BLEND)
	C.glBlendFuncSeparate(C.GL_SRC_ALPHA, C.GL_ONE_MINUS_SRC_ALPHA,
		C.GL_ONE, C.GL_ONE)
	C.glEnable(C.GL_CULL_FACE)

	C.glPixelStorei(C.GL_UNPACK_ALIGNMENT, pixmapRowAlign)

	vendor := C.GoString((*C.char)(unsafe.Pointer(C.glGetString(C.GL_VENDOR))))
	renderer := C.GoString((*C.char)(unsafe.Pointer(C.glGetString(C.GL_RENDERER))))
	glwDeps.ts.Trace(tracepkg.TRACE_INFO, "GLW",
		"OpenGLES Renderer: '%s' by '%s'", renderer, vendor)

	return glwOpenglShadersSetup(gr)
}

// C: glw_opengl_fini_context (glw_opengl_es.c:48-51)
func glwOpenglFiniContext(gr *glwRoot) {
	glwOpenglShadersFini(gr)
}

// C: glw_rtt_init (glw_opengl_es.c:58-61) — empty on ES.
func glwRttSetup(gr *glwRoot, grtt *glwRtt, width, height, alpha int) {
}

// C: glw_rtt_enter (glw_opengl_es.c:68-70) — empty on ES.
func glwRttEnter(gr *glwRoot, grtt *glwRtt, rc *glwRctx) {
}

// C: glw_rtt_restore (glw_opengl_es.c:77-80) — empty on ES.
func glwRttRestore(gr *glwRoot, grtt *glwRtt) {
}

// C: glw_rtt_destroy (glw_opengl_es.c:87-89) — empty on ES.
func glwRttDestroy(gr *glwRoot, grtt *glwRtt) {
}
