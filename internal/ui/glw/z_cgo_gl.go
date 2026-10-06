package glw

// Canonical OpenGL link directives shared by every glw file that
// used to carry its own copy (glw_opengl_ogl.go,
// glw_opengl_shaders.go, glw_opengl_es.go, glw_texture_opengl.go,
// glw_video_opengl.go). cgo aggregates #cgo lines package-wide, so
// one copy keeps the flag set identical.

/*
#cgo linux,!android,!arm LDFLAGS: -lGL
#cgo android LDFLAGS: -lGLESv2
#cgo darwin LDFLAGS: -framework OpenGL
#cgo windows LDFLAGS: -lopengl32
*/
import "C"
