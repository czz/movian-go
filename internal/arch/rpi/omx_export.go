//go:build rpi

// omx_export.go — Go bodies of the OMX callbacks (C: oc_event_handler,
// oc_empty_buffer_done, oc_fill_buffer_done in omx.c). Split into a
// declaration-only preamble file: cgo forbids C definitions in the
// preamble of files using //export.
package rpi

/*
#cgo CFLAGS: -DOMX_SKIP64BIT -I/opt/vc/include -I/opt/vc/include/interface/vmcs_host/linux -I/opt/vc/include/interface/vcos/pthreads

#include <OMX_Core.h>
#include <OMX_Component.h>
#include <OMX_Broadcom.h>
*/
import "C"

import (
	"runtime/cgo"
	"unsafe"

	"github.com/czz/movian-go/internal/trace"
)

// ocEventHandler — C: oc_event_handler (omx.c:32-96)
//
//export goOmxEventHandler
func goOmxEventHandler(component C.OMX_HANDLETYPE, opaque C.OMX_PTR,
	event C.OMX_EVENTTYPE, data1, data2 C.OMX_U32, eventdata C.OMX_PTR) C.OMX_ERRORTYPE {
	oc := cgo.Handle(opaque).Value().(*OmxComponent)

	switch event {
	case C.OMX_EventCmdComplete:
		goto complete

	case C.OMX_EventError:
		switch C.OMX_ERRORTYPE(data1) {
		case C.OMX_ErrorPortUnpopulated:
		case C.OMX_ErrorSameState:
			goto complete
		case C.OMX_ErrorStreamCorrupt:
			rpiTS.Trace(trace.TRACE_INFO, "OMX", "%s: Corrupt stream", oc.Name)
			oc.Mtx.Lock()
			oc.StreamCorrupt = 1
			if oc.AvailCond != nil {
				oc.AvailCond.Signal()
			}
			oc.Mtx.Unlock()
		default:
			rpiTS.Trace(trace.TRACE_ERROR, "OMX", "%s: ERROR 0x%x\n", oc.Name, int(data1))
		}
		goto ret

	case C.OMX_EventPortSettingsChanged:
		if oc.PortSettingsChangedCb != nil {
			oc.PortSettingsChangedCb(oc)
		}
		goto ret

	case C.OMX_EventMark:
		if oc.EventMarkCb != nil {
			oc.EventMarkCb(oc, unsafe.Pointer(eventdata))
		}
		goto ret
	}
	goto ret

complete:
	oc.Mtx.Lock()
	oc.CmdDone = 1
	oc.EventCond.Broadcast()
	oc.Mtx.Unlock()

ret:
	return 0
}

// ocEmptyBufferDone — C: oc_empty_buffer_done (omx.c:103-119)
//
//export goOmxEmptyDone
func goOmxEmptyDone(hComponent C.OMX_HANDLETYPE, opaque C.OMX_PTR,
	buf *C.OMX_BUFFERHEADERTYPE) C.OMX_ERRORTYPE {
	oc := cgo.Handle(opaque).Value().(*OmxComponent)
	oc.Mtx.Lock()
	oc.InflightBuffers--
	buf.pAppPrivate = C.OMX_PTR(unsafe.Pointer(oc.Avail))
	oc.Avail = buf
	oc.AvailBytes += int(buf.nAllocLen)
	if oc.AvailCond != nil && oc.AvailBytes >= oc.NeedBytes {
		oc.AvailCond.Signal()
	}
	oc.Mtx.Unlock()
	return 0
}

// ocFillBufferDone — C: oc_fill_buffer_done (omx.c:126-138)
//
//export goOmxFillDone
func goOmxFillDone(hComponent C.OMX_HANDLETYPE, opaque C.OMX_PTR,
	buf *C.OMX_BUFFERHEADERTYPE) C.OMX_ERRORTYPE {
	oc := cgo.Handle(opaque).Value().(*OmxComponent)
	oc.Mtx.Lock()
	oc.Filled = buf
	if oc.AvailCond != nil {
		oc.AvailCond.Signal()
	}
	oc.Mtx.Unlock()
	return 0
}
