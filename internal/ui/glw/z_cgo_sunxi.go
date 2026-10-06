//go:build sunxi

package glw

// Shared vendored CedarX/VE header path for the sunxi backend —
// previously duplicated between glw_sunxi.go and
// glw_video_sunxi.go.

/*
#cgo CFLAGS: -I${SRCDIR}/../../arch/sunxi/include
*/
import "C"
