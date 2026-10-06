//go:build !android && !rpi

package glw

// enableGLWBackendOpenGL — C: ENABLE_GLW_BACKEND_OPENGL
// (Makefile:471, CONFIG_GLW_BACKEND_OPENGL). True on the desktop GL
// backend: enables the fixed-function fallback paths that GLES2 lacks.
const enableGLWBackendOpenGL = true
