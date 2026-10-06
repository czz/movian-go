//go:build rpi

// rpi_main_export.go — Go bodies of the C-entry callbacks declared in
// rpi_main.go's preamble (tv_service_callback, my_vcos_log).
// Declaration-only preamble: cgo forbids C definitions alongside //export.
package rpi

/*
#cgo CFLAGS: -DOMX_SKIP64BIT -I/opt/vc/include -I/opt/vc/include/interface/vmcs_host/linux -I/opt/vc/include/interface/vcos/pthreads

#include <stdint.h>
#include <interface/vcos/vcos_logging.h>
*/
import "C"

import (
	"unsafe"

	"github.com/czz/movian-go/internal/trace"
)

//export goTvServiceCallback
func goTvServiceCallback(callbackData unsafe.Pointer, reason C.uint32_t,
	param1, param2 C.uint32_t) {
	tvServiceCallback(uint32(reason), uint32(param1), uint32(param2))
}

// goVcosLog — C: my_vcos_log (rpi_main.c:878-890)
//
//export goVcosLog
func goVcosLog(name *C.char, level C.int, msg *C.char) {
	var stlevel int
	switch C.VCOS_LOG_LEVEL_T(level) {
	case C.VCOS_LOG_ERROR:
		stlevel = trace.TRACE_ERROR
	case C.VCOS_LOG_WARN:
		stlevel = trace.TRACE_ERROR
	default:
		stlevel = trace.TRACE_DEBUG
	}
	rpiTS.Trace(stlevel, C.GoString(name), "%s", C.GoString(msg))
}
