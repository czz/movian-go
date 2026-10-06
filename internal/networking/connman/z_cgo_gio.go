//go:build linux && rpi && connman && connmancgo

package connman

// Shared gio-2.0 pkg-config directive — previously duplicated in
// connman.go and prop_gvariant.go (same package-level union).

/*
#cgo pkg-config: gio-2.0
#include "csrc/connman_glue.c"
*/
import "C"
