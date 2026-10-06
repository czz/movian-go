//go:build android

package glw

// Android log/runtime libs shared by glw_android.go and
// glw_video_android.go (which used to repeat
// "-lGLESv2 -llog -landroid"; -lGLESv2 lives in z_cgo_gl.go).

/*
#cgo LDFLAGS: -llog -landroid
*/
import "C"
