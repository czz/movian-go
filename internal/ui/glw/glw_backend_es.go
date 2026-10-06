//go:build android || rpi

package glw

// enableGLWBackendOpenGL — C: ENABLE_GLW_BACKEND_OPENGL is 0 under
// CONFIG_GLW_BACKEND_OPENGL_ES (Makefile:477) — no fixed pipeline.
const enableGLWBackendOpenGL = false
