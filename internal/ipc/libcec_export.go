//go:build libcec && (!linux || libceccgo)

package ipc

/*
#include <libcec/cecc.h>
*/
import "C"

import "unsafe"

// Callback exports invoked from the libcec trampolines in libcec.go.
// The v7 ICECCallbacks signatures are pointer-based and void-returning;
// the C-side int returns of the canonical functions are dropped by the
// trampolines.

//export goCecLogMessage
func goCecLogMessage(lib unsafe.Pointer, message *C.cec_log_message) {
	logMessage(lib, message)
}

//export goCecKeyPress
func goCecKeyPress(aux unsafe.Pointer, key *C.cec_keypress) {
	keypress(aux, key)
}

//export goCecSourceActivated
func goCecSourceActivated(aux unsafe.Pointer, la C.cec_logical_address,
	on C.uint8_t) {
	sourceActivated(aux, la, on)
}

//export goCecCommand
func goCecCommand(aux unsafe.Pointer, cmd *C.cec_command) {
	handleCecCommand(aux, cmd)
}
