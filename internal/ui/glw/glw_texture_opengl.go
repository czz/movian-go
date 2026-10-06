package glw

// C: src/ui/glw/glw_texture_opengl.c — canonical 1:1 port.

/*
#if defined(__ANDROID__) || defined(MOVIAN_GLES2)
#include <GLES2/gl2.h>
#include <GLES2/gl2ext.h>
#ifndef GL_BGRA
#define GL_BGRA GL_BGRA_EXT
#endif
#elif defined(__APPLE__)
#include <OpenGL/gl.h>
#elif defined(_WIN32)
#include <GL/gl.h>
#include <GL/glext.h> // windows gl.h is 1.1-only — GL 1.2+ consts live here
#else
#include <GL/gl.h>
#endif
#include <string.h>
*/
import "C"

import (
	"unsafe"

	imagepkg "github.com/czz/movian-go/internal/image"
)

// C: glw_tex_backend_free_render_resources (glw_texture_opengl.c:31-39)
func glwTexBackendFreeRenderResources(gr *glwRoot, glt *GlwLoadableTexture) {
	if glt.gltTexture.Textures[0] != 0 {
		t := glt.gltTexture.Textures[0]
		C.glDeleteTextures(1, (*C.GLuint)(unsafe.Pointer(&t)))
		glt.gltTexture.Textures[0] = 0
	}
}

// C: glw_tex_backend_free_loader_resources (glw_texture_opengl.c:45-52)
func glwTexBackendFreeLoaderResources(glt *GlwLoadableTexture) {
	if glt.gltPixmap != nil {
		imagepkg.PixmapRelease(glt.gltPixmap)
		glt.gltPixmap = nil
	}
}

// C: glw_tex_backend_layout (glw_texture_opengl.c:57-112)
func glwTexBackendLayout(gr *glwRoot, glt *GlwLoadableTexture) {
	m := C.GLenum(C.GL_TEXTURE_2D)

	if glt.gltPixmap == nil {
		return
	}

	if glt.gltTexture.Textures[0] == 0 {
		t := glt.gltTexture.Textures[0]
		C.glGenTextures(1, (*C.GLuint)(unsafe.Pointer(&t)))
		glt.gltTexture.Textures[0] = t
	}

	C.glBindTexture(m, C.GLuint(glt.gltTexture.Textures[0]))
	glt.gltTexture.Width = int32(glt.gltXs)
	glt.gltTexture.Height = int32(glt.gltYs)
	glt.gltTexture.Opaque = int8(glt.gltOpaque)

	C.glTexParameteri(m, C.GL_TEXTURE_MAG_FILTER, C.GL_LINEAR)
	C.glTexParameteri(m, C.GL_TEXTURE_MIN_FILTER, C.GL_LINEAR)

	wrapmode := C.GLint(C.GL_CLAMP_TO_EDGE)
	if glt.gltFlags&glwTexRepeat != 0 {
		wrapmode = C.GL_REPEAT
	}
	C.glTexParameteri(m, C.GL_TEXTURE_WRAP_S, wrapmode)
	C.glTexParameteri(m, C.GL_TEXTURE_WRAP_T, wrapmode)

	var p unsafe.Pointer
	if len(glt.gltPixmap.Data) > 0 {
		p = unsafe.Pointer(&glt.gltPixmap.Data[0])
	}

	if glt.gltTexWidth != 0 && glt.gltTexHeight != 0 {
		C.glTexImage2D(m, 0, C.GLint(glt.gltInternalFormat),
			C.GLsizei(glt.gltTexWidth), C.GLsizei(glt.gltTexHeight),
			0, C.GLenum(glt.gltFormat), C.GL_UNSIGNED_BYTE, nil)

		C.glTexSubImage2D(m, 0, 0, 0,
			C.GLsizei(glt.gltXs), C.GLsizei(glt.gltYs),
			C.GLenum(glt.gltFormat), C.GL_UNSIGNED_BYTE, p)

		glt.gltS = float32(glt.gltXs) / float32(glt.gltTexWidth)
		glt.gltT = float32(glt.gltYs) / float32(glt.gltTexHeight)
	} else {
		glt.gltS = 1
		glt.gltT = 1

		C.glTexImage2D(m, 0, C.GLint(glt.gltInternalFormat),
			C.GLsizei(glt.gltXs), C.GLsizei(glt.gltYs),
			0, C.GLenum(glt.gltFormat), C.GL_UNSIGNED_BYTE, p)
	}

	C.glBindTexture(m, 0)

	glwTexBackendFreeLoaderResources(glt)
}

// C: glw_tex_backend_load (glw_texture_opengl.c:115-165)
func glwTexBackendLoad(gr *glwRoot, glt *GlwLoadableTexture, pm *imagepkg.Pixmap) int {
	var size int

	switch pm.Type {
	default:
		return 0

	case imagepkg.PixmapRGB24:
		glt.gltFormat = C.GL_RGB
		glt.gltInternalFormat = C.GL_RGB
		size = pm.Width * pm.Height * 4

	case imagepkg.PixmapBGR32, imagepkg.PixmapRGBA:
		glt.gltFormat = C.GL_RGBA
		glt.gltInternalFormat = C.GL_RGBA
		size = pm.Width * pm.Height * 4

	case imagepkg.PixmapBGRA:
		glt.gltFormat = C.GL_BGRA
		glt.gltInternalFormat = C.GL_RGBA
		size = pm.Width * pm.Height * 4

	case imagepkg.PixmapIA:
		glt.gltFormat = C.GL_LUMINANCE_ALPHA
		glt.gltInternalFormat = C.GL_LUMINANCE_ALPHA
		size = pm.Width * pm.Height * 2

	case imagepkg.PixmapI:
		glt.gltFormat = C.GL_LUMINANCE
		glt.gltInternalFormat = C.GL_LUMINANCE
		size = pm.Width * pm.Height
	}

	if glt.gltPixmap != nil {
		imagepkg.PixmapRelease(glt.gltPixmap)
	}
	glt.gltPixmap = imagepkg.PixmapDup(pm)

	return size
}

// C: glw_tex_upload (glw_texture_opengl.c:170-220)
func glwTexUpload(gr *glwRoot, tex *GlwBackendTexture, pm *imagepkg.Pixmap, flags int) {
	var format, intFormat C.GLenum
	m := C.GLenum(C.GL_TEXTURE_2D)

	if tex.Textures[0] == 0 {
		C.glGenTextures(1, (*C.GLuint)(unsafe.Pointer(&tex.Textures[0])))
		C.glBindTexture(m, C.GLuint(tex.Textures[0]))
		C.glTexParameteri(m, C.GL_TEXTURE_MAG_FILTER, C.GL_LINEAR)
		C.glTexParameteri(m, C.GL_TEXTURE_MIN_FILTER, C.GL_LINEAR)

		m2 := C.GLint(C.GL_CLAMP_TO_EDGE)
		if flags&glwTexRepeat != 0 {
			m2 = C.GL_REPEAT
		}
		C.glTexParameteri(m, C.GL_TEXTURE_WRAP_S, m2)
		C.glTexParameteri(m, C.GL_TEXTURE_WRAP_T, m2)
	} else {
		C.glBindTexture(m, C.GLuint(tex.Textures[0]))
	}

	switch pm.Type {
	case imagepkg.PixmapBGR32, imagepkg.PixmapRGBA:
		format = C.GL_RGBA
		intFormat = C.GL_RGBA
	case imagepkg.PixmapBGRA:
		format = C.GL_BGRA
		intFormat = C.GL_RGBA
	case imagepkg.PixmapRGB24:
		format = C.GL_RGB
		intFormat = C.GL_RGB
	case imagepkg.PixmapIA:
		format = C.GL_LUMINANCE_ALPHA
		intFormat = C.GL_LUMINANCE_ALPHA
	default:
		return
	}

	tex.Width = int32(pm.Width)
	tex.Height = int32(pm.Height)
	if pm.Flags&imagepkg.PixmapOpaque != 0 {
		tex.Opaque = 1
	} else {
		tex.Opaque = 0
	}

	var p unsafe.Pointer
	if len(pm.Data) > 0 {
		p = unsafe.Pointer(&pm.Data[0])
	}
	C.glTexImage2D(m, 0, C.GLint(intFormat), C.GLsizei(pm.Width), C.GLsizei(pm.Height),
		0, format, C.GL_UNSIGNED_BYTE, p)
}

// C: glw_tex_destroy (glw_texture_opengl.c:225-232)
func glwTexDestroy(gr *glwRoot, tex *GlwBackendTexture) {
	if tex.Textures[0] != 0 {
		C.glDeleteTextures(1, (*C.GLuint)(unsafe.Pointer(&tex.Textures[0])))
		tex.Textures[0] = 0
	}
}
