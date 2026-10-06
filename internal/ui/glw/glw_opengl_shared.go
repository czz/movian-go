package glw

// Shared OpenGL/ES backend types — C: glw_opengl.h.

// pixmapRowAlign — C: PIXMAP_ROW_ALIGN (pixmap.h:21-25). 16 on __PPC__,
// 8 elsewhere; matches imagepkg.PixmapRowAlign.
const pixmapRowAlign = 8

// C: glw_rtt_t (glw_opengl.h:176-194) — render-to-texture target.
type glwRtt struct {
	grttFramebuffer uint32            // C: GLuint grtt_framebuffer
	grttTexture     GlwBackendTexture // C: glw_backend_texture_t grtt_texture
	grttWidth       int               // C: grtt_width
	grttHeight      int               // C: grtt_height
	grttViewport    [4]int32          // C: GLint grtt_viewport[4] (saved)
}

// C: glw_rtt_texture(grtt) — glw_opengl.h:219
func glwRttTexture(rtt *glwRtt) *GlwBackendTexture { return &rtt.grttTexture }
