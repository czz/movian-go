//go:build rpi

package glw

// Shared Broadcom SDK (/opt/vc) flags for the RPi glw backend —
// previously duplicated verbatim between glw_rpi.go and
// glw_video_rpi.go. /opt/vc is the canonical VideoCore SDK install
// path (same convention as the original C build), not a checkout
// path: cgo cannot expand Make/env variables.

/*
#cgo CFLAGS: -DMOVIAN_GLES2 -I/opt/vc/include -I/opt/vc/include/interface/vmcs_host/linux -I/opt/vc/include/interface/vcos/pthreads
#cgo LDFLAGS: -L/opt/vc/lib -lbcm_host -lvcos -lvchiq_arm -lopenmaxil -lEGL -lGLESv2 -lmmal_core -lmmal_util -lmmal_vc_client -lvchostif
*/
import "C"
