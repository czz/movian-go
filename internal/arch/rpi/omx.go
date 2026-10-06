//go:build rpi

// Package rpi — canonical port of src/arch/rpi/*.
//
// omx.go — C: src/arch/rpi/omx.c + omx.h — OpenMAX IL component/tunnel/clock
// wrappers. Requires the Broadcom Videocore userland headers/libs
// (/opt/vc) — build with -tags rpi on a Raspberry Pi only.
package rpi

/*
#cgo CFLAGS: -DOMX_SKIP64BIT -I/opt/vc/include -I/opt/vc/include/interface/vmcs_host/linux -I/opt/vc/include/interface/vcos/pthreads
#cgo LDFLAGS: -L/opt/vc/lib -lbcm_host -lvcos -lvchiq_arm -lopenmaxil -lEGL -lGLESv2 -lmmal_core -lmmal_util -lmmal_vc_client -lvchostif

#include <stdlib.h>
#include <string.h>
#include <OMX_Core.h>
#include <OMX_Component.h>
#include <OMX_Broadcom.h>
#include "omx_shim.h"

extern OMX_ERRORTYPE goOmxEventHandler(OMX_HANDLETYPE component, OMX_PTR opaque, OMX_EVENTTYPE event, OMX_U32 data1, OMX_U32 data2, OMX_PTR eventdata);
extern OMX_ERRORTYPE goOmxEmptyDone(OMX_HANDLETYPE hComponent, OMX_PTR opaque, OMX_BUFFERHEADERTYPE *buf);
extern OMX_ERRORTYPE goOmxFillDone(OMX_HANDLETYPE hComponent, OMX_PTR opaque, OMX_BUFFERHEADERTYPE *buf);

static OMX_ERRORTYPE oc_event_handler(OMX_HANDLETYPE component, OMX_PTR opaque, OMX_EVENTTYPE event, OMX_U32 data1, OMX_U32 data2, OMX_PTR eventdata) {
	return goOmxEventHandler(component, opaque, event, data1, data2, eventdata);
}
static OMX_ERRORTYPE oc_empty_buffer_done(OMX_HANDLETYPE hComponent, OMX_PTR opaque, OMX_BUFFERHEADERTYPE *buf) {
	return goOmxEmptyDone(hComponent, opaque, buf);
}
static OMX_ERRORTYPE oc_fill_buffer_done(OMX_HANDLETYPE hComponent, OMX_PTR opaque, OMX_BUFFERHEADERTYPE *buf) {
	return goOmxFillDone(hComponent, opaque, buf);
}

static OMX_CALLBACKTYPE omx_callbacks(void) {
	OMX_CALLBACKTYPE cb;
	cb.EventHandler = oc_event_handler;
	cb.EmptyBufferDone = oc_empty_buffer_done;
	cb.FillBufferDone = oc_fill_buffer_done;
	return cb;
}
*/
import "C"

import (
	"fmt"
	"runtime/cgo"
	"sync"
	"time"
	"unsafe"

	"github.com/czz/movian-go/internal/arch"
	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/trace"
)

// C: omx_component_t (omx.h)
type OmxComponent struct {
	Handle    C.OMX_HANDLETYPE // C: oc_handle
	Name      string           // C: oc_name (strdup'd)
	Mtx       sync.Locker      // C: oc_mtx (hts_mutex_t *)
	AvailCond *sync.Cond       // C: oc_avail_cond (hts_cond_t *)
	EventCond *sync.Cond       // C: oc_event_cond

	Avail      *C.OMX_BUFFERHEADERTYPE // C: oc_avail — chain via pAppPrivate
	AvailBytes int                     // C: oc_avail_bytes
	NeedBytes  int                     // C: oc_need_bytes

	Filled          *C.OMX_BUFFERHEADERTYPE // C: oc_filled
	InflightBuffers int                     // C: oc_inflight_buffers
	CmdDone         int                     // C: oc_cmd_done

	Opaque any // C: oc_opaque

	// C: oc_port_settings_changed_cb / oc_event_mark_cb
	PortSettingsChangedCb func(oc *OmxComponent)
	EventMarkCb           func(oc *OmxComponent, ptr unsafe.Pointer)

	Inport  int // C: oc_inport
	Outport int // C: oc_outport

	StreamCorrupt int // C: oc_stream_corrupt

	// The component is passed to OMX as appData and returned in
	// callbacks; a Go pointer cannot live in C memory, so a
	// runtime/cgo.Handle is used (C: oc as OMX_PTR opaque directly).
	cgoHandle cgo.Handle
}

// C: omx_tunnel_t (omx.h)
type OmxTunnel struct {
	Src     *OmxComponent // C: ot_src
	Srcport int           // C: ot_srcport
	Dst     *OmxComponent // C: ot_dst
	Dstport int           // C: ot_dstport
	Name    string        // C: ot_name
}

// omxchk — C: Omxchk(fn) macro → omxchk0(fn, #fn, __LINE__) (omx.h)
func Omxchk(er C.OMX_ERRORTYPE, fn string) {
	if er == 0 {
		return
	}
	panic(fmt.Sprintf("%s: OMX Error 0x%x\n", fn, int(er)))
}

// condWaitTimeout — C: hts_cond_wait_timeout (posix_threads.c). The
// component mutexes are plain sync.Locker (mp.Mutex), so the
// arch.CondWaitTimeout wrapper (typed to *arch.Mutex) can't be used;
// this is the same timed-cond primitive on a sync.Cond. Returns true
// on timeout.
func condWaitTimeout(c *sync.Cond, timeoutMs int) bool {
	timedOut := false
	done := make(chan struct{})
	go func() {
		timer := time.NewTimer(time.Duration(timeoutMs) * time.Millisecond)
		select {
		case <-timer.C:
			c.L.Lock()
			timedOut = true
			c.Broadcast()
			c.L.Unlock()
		case <-done:
			timer.Stop()
		}
	}()
	c.Wait()
	close(done)
	return timedOut
}

// OmxWaitCommand — C: omx_wait_command (omx.c:145-157)
func OmxWaitCommand(oc *OmxComponent) {
	oc.Mtx.Lock()
	for oc.CmdDone == 0 {
		if condWaitTimeout(oc.EventCond, 250) {
			rpiTS.Trace(trace.TRACE_ERROR, "OMX", "OMX timeout")
			break
		}
	}
	oc.Mtx.Unlock()
}

// OmxSendCommand — C: omx_send_command (omx.c:162-174)
func OmxSendCommand(oc *OmxComponent, cmd C.OMX_COMMANDTYPE, v int, p unsafe.Pointer, wait bool) {
	oc.CmdDone = 0
	Omxchk(C.omx_SendCommand_(oc.Handle, cmd, C.OMX_U32(v), C.OMX_PTR(p)), "OMX_SendCommand")
	if wait {
		OmxWaitCommand(oc)
	}
}

// OmxComponentCreate — C: omx_component_create (omx.c:179-219)
func OmxComponentCreate(name string, mtx sync.Locker, avail *sync.Cond) *OmxComponent {
	oc := &OmxComponent{Mtx: mtx, AvailCond: avail, Name: name}
	oc.EventCond = sync.NewCond(oc.Mtx)
	oc.cgoHandle = cgo.NewHandle(oc)

	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))

	cb := C.omx_callbacks()
	Omxchk(C.OMX_GetHandle(&oc.Handle, cname,
		C.OMX_PTR(unsafe.Pointer(oc.cgoHandle)), &cb), "OMX_GetHandle")

	// Initially disable ports — C: loop over
	// OMX_IndexParam{Audio,Video,Image,Other}Start
	types := []C.OMX_INDEXTYPE{C.OMX_IndexParamAudioInit, C.OMX_IndexParamVideoInit,
		C.OMX_IndexParamImageInit, C.OMX_IndexParamOtherInit}
	for _, t := range types {
		var ports C.OMX_PORT_PARAM_TYPE
		ports.nSize = C.OMX_U32(unsafe.Sizeof(ports))
		C.omx_init_version_(&ports.nVersion)
		Omxchk(C.omx_GetParameter_(oc.Handle, t, C.OMX_PTR(unsafe.Pointer(&ports))), "OMX_GetParameter")
		if ports.nPorts > 0 {
			oc.Inport = int(ports.nStartPortNumber)
			oc.Outport = int(ports.nStartPortNumber) + 1
		}
		for j := C.OMX_U32(0); j < ports.nPorts; j++ {
			OmxSendCommand(oc, C.OMX_CommandPortDisable,
				int(ports.nStartPortNumber+j), nil, true)
		}
	}
	return oc
}

// OmxComponentDestroy — C: omx_component_destroy (omx.c:224-233)
func OmxComponentDestroy(oc *OmxComponent) {
	Omxchk(C.OMX_FreeHandle(oc.Handle), "OMX_FreeHandle")
	oc.cgoHandle.Delete()
}

// OmxSetState — C: omx_set_state (omx.c:238-270)
func OmxSetState(oc *OmxComponent, reqstate C.OMX_STATETYPE) {
	var state C.OMX_STATETYPE
	attempts := 20
	Omxchk(C.omx_GetState_(oc.Handle, &state), "OMX_GetState")

	for {
		oc.CmdDone = 0
		r := C.omx_SendCommand_(oc.Handle, C.OMX_CommandStateSet,
			C.OMX_U32(reqstate), nil)
		if r == C.OMX_ErrorInsufficientResources && attempts != 0 {
			time.Sleep(10 * time.Millisecond) // C: usleep(10000)
			attempts--
			continue
		}
		if r != 0 {
			panic(fmt.Sprintf("OMX Setstate %s from %d to %d error 0x%x",
				oc.Name, int(state), int(reqstate), int(r)))
		}
		if reqstate == C.OMX_StateExecuting {
			OmxWaitCommand(oc)
		}
		return
	}
}

// OmxAllocBuffers — C: omx_alloc_buffers (omx.c:275-306)
func OmxAllocBuffers(oc *OmxComponent, port int) {
	var portdef C.OMX_PARAM_PORTDEFINITIONTYPE
	portdef.nSize = C.OMX_U32(unsafe.Sizeof(portdef))
	C.omx_init_version_(&portdef.nVersion)
	portdef.nPortIndex = C.OMX_U32(port)

	Omxchk(C.omx_GetParameter_(oc.Handle, C.OMX_IndexParamPortDefinition,
		C.OMX_PTR(unsafe.Pointer(&portdef))), "OMX_GetParameter")
	if portdef.bEnabled != C.OMX_FALSE || portdef.nBufferCountActual == 0 ||
		portdef.nBufferSize == 0 {
		// C: exit(3)
		panic("OMX alloc_buffers: port disabled or zero buffers")
	}

	OmxSendCommand(oc, C.OMX_CommandPortEnable, port, nil, false)
	for i := C.OMX_U32(0); i < portdef.nBufferCountActual; i++ {
		var buf *C.OMX_BUFFERHEADERTYPE
		Omxchk(C.omx_AllocateBuffer_(oc.Handle, &buf, C.OMX_U32(port),
			nil, portdef.nBufferSize), "OMX_AllocateBuffer")
		buf.pAppPrivate = C.OMX_PTR(unsafe.Pointer(oc.Avail))
		oc.Avail = buf
		oc.AvailBytes += int(buf.nAllocLen)
	}
	OmxWaitCommand(oc) // Waits for the OMX_CommandPortEnable command
}

// OmxGetBufferLocked — C: omx_get_buffer_locked (omx.c:310-326)
func OmxGetBufferLocked(oc *OmxComponent) *C.OMX_BUFFERHEADERTYPE {
	for oc.Avail == nil {
		if condWaitTimeout(oc.AvailCond, 3000) {
			rpiTS.Trace(trace.TRACE_ERROR, "OMX", "Timeout while waiting for buffer")
			return nil
		}
	}
	buf := oc.Avail
	oc.Avail = (*C.OMX_BUFFERHEADERTYPE)(buf.pAppPrivate)
	oc.AvailBytes -= int(buf.nAllocLen)
	oc.InflightBuffers++
	return buf
}

// OmxGetBuffer — C: omx_get_buffer (omx.c:329-337)
func OmxGetBuffer(oc *OmxComponent) *C.OMX_BUFFERHEADERTYPE {
	oc.Mtx.Lock()
	buf := OmxGetBufferLocked(oc)
	oc.Mtx.Unlock()
	return buf
}

// OmxWaitBuffers — C: omx_wait_buffers (omx.c:342-350)
func OmxWaitBuffers(oc *OmxComponent) {
	oc.Mtx.Lock()
	for oc.InflightBuffers != 0 {
		oc.AvailCond.Wait()
	}
	oc.Mtx.Unlock()
}

// OmxReleaseBuffers — C: omx_release_buffers (omx.c:355-372)
func OmxReleaseBuffers(oc *OmxComponent, port int) {
	for buf := oc.Avail; buf != nil; {
		n := (*C.OMX_BUFFERHEADERTYPE)(buf.pAppPrivate)
		if r := C.omx_FreeBuffer_(oc.Handle, C.OMX_U32(port), buf); r != 0 {
			rpiTS.Trace(trace.TRACE_ERROR, "OMX", "Unable to free buffer")
			// C: exit(1)
			panic("OMX_FreeBuffer failed")
		}
		buf = n
	}
	oc.Avail = nil
	oc.AvailBytes = 0
	oc.NeedBytes = 0
}

// OmxTunnelCreate — C: omx_tunnel_create (omx.c:377-402)
func OmxTunnelCreate(src *OmxComponent, srcport int, dst *OmxComponent,
	dstport int, name string) *OmxTunnel {
	var state C.OMX_STATETYPE
	Omxchk(C.omx_GetState_(src.Handle, &state), "OMX_GetState")
	if state == C.OMX_StateLoaded {
		OmxSetState(src, C.OMX_StateIdle)
	}

	OmxSendCommand(src, C.OMX_CommandPortDisable, srcport, nil, true)
	OmxSendCommand(dst, C.OMX_CommandPortDisable, dstport, nil, true)
	Omxchk(C.omx_SetupTunnel_(src.Handle, C.OMX_U32(srcport), dst.Handle,
		C.OMX_U32(dstport)), "OMX_SetupTunnel")
	OmxSendCommand(src, C.OMX_CommandPortEnable, srcport, nil, false)
	OmxSendCommand(dst, C.OMX_CommandPortEnable, dstport, nil, false)

	Omxchk(C.omx_GetState_(dst.Handle, &state), "OMX_GetState")
	if state == C.OMX_StateLoaded {
		OmxSetState(dst, C.OMX_StateIdle)
	}

	return &OmxTunnel{Src: src, Srcport: srcport, Dst: dst, Dstport: dstport,
		Name: name}
}

// OmxPortEnable — C: omx_port_enable (omx.c:408-411)
func OmxPortEnable(c *OmxComponent, port int) {
	OmxSendCommand(c, C.OMX_CommandPortEnable, port, nil, false)
}

// OmxTunnelDestroy — C: omx_tunnel_destroy (omx.c:417-428)
func OmxTunnelDestroy(ot *OmxTunnel) {
	OmxSendCommand(ot.Src, C.OMX_CommandPortDisable, ot.Srcport, nil, false)
	OmxSendCommand(ot.Dst, C.OMX_CommandPortDisable, ot.Dstport, nil, false)

	OmxWaitCommand(ot.Src)
	OmxWaitCommand(ot.Dst)

	Omxchk(C.omx_SetupTunnel_(ot.Src.Handle, C.OMX_U32(ot.Srcport), nil, 0),
		"OMX_SetupTunnel")
}

// OmxWaitFillBuffer — C: omx_wait_fill_buffer (omx.c:433-442)
func OmxWaitFillBuffer(oc *OmxComponent, buf *C.OMX_BUFFERHEADERTYPE) int {
	oc.Mtx.Lock()
	for oc.Filled == nil {
		oc.AvailCond.Wait()
	}
	oc.Mtx.Unlock()
	return 0
}

// omxTicksToS64 — C: omx_ticks_to_s64 (OMX helper): the OMX tick is a
// 64-bit microsecond value split into two 32-bit halves.
func omxTicksToS64(t C.OMX_TICKS) int64 {
	return int64(uint64(t.nLowPart) | uint64(t.nHighPart)<<32)
}

// OmxGetMediaTime — C: omx_get_media_time (omx.c:447-459)
func OmxGetMediaTime(oc *OmxComponent) int64 {
	var ts C.OMX_TIME_CONFIG_TIMESTAMPTYPE
	ts.nSize = C.OMX_U32(unsafe.Sizeof(ts))
	C.omx_init_version_(&ts.nVersion)
	Omxchk(C.omx_GetConfig_(oc.Handle, C.OMX_IndexConfigTimeCurrentMediaTime,
		C.OMX_PTR(unsafe.Pointer(&ts))), "OMX_GetConfig")
	return omxTicksToS64(ts.nTimestamp)
}

// OmxEnableBufferMarks — C: omx_enable_buffer_marks (omx.c:464-473)
func OmxEnableBufferMarks(oc *OmxComponent) {
	var t C.OMX_CONFIG_BOOLEANTYPE
	t.nSize = C.OMX_U32(unsafe.Sizeof(t))
	C.omx_init_version_(&t.nVersion)
	t.bEnabled = C.OMX_TRUE
	Omxchk(C.omx_SetParameter_(oc.Handle, C.OMX_IndexParamPassBufferMarks,
		C.OMX_PTR(unsafe.Pointer(&t))), "OMX_SetParameter")
}

// OmxFlushPort — C: omx_flush_port (omx.c:478-482)
func OmxFlushPort(oc *OmxComponent, port int) {
	OmxSendCommand(oc, C.OMX_CommandFlush, port, nil, true)
}

// ---------------------------------------------------------------------------
// C: omx_clk_cmd_t / omx_clk_t (omx.c:485-512)
// ---------------------------------------------------------------------------

const (
	omxClkCmdQuit = iota // C: OMX_CLK_QUIT
	omxClkCmdInit
	omxClkCmdPause
	omxClkCmdPlay
	omxClkCmdBeginSeek
	omxClkCmdSeekAudioDone
	omxClkCmdSeekVideoDone
)

// C: omx_clk_cmd_t
type omxClkCmd struct {
	cmd int // C: cmd
	arg int // C: arg
}

// C: omx_clk_t — the mp->mp_extra clock driver.
type OmxClk struct {
	tid            *arch.Thread         // C: tid
	q              []omxClkCmd          // C: omx_clk_cmd_queue q (TAILQ)
	c              *OmxComponent        // C: c — "OMX.broadcom.clock"
	cond           *sync.Cond           // C: cond
	mp             *mediacore.MediaPipe // C: mp
	seekInProgress int                  // C: seek_in_progress
	hasAudio       int                  // C: has_audio
}

// omxClkSetSpeed — C: omx_clk_set_speed (omx.c:517-527)
func omxClkSetSpeed(clk *OmxClk, v int) {
	var scale C.OMX_TIME_CONFIG_SCALETYPE
	scale.nSize = C.OMX_U32(unsafe.Sizeof(scale))
	C.omx_init_version_(&scale.nVersion)
	scale.xScale = C.OMX_S32(v)
	Omxchk(C.omx_SetConfig_(clk.c.Handle, C.OMX_IndexConfigTimeScale,
		C.OMX_PTR(unsafe.Pointer(&scale))), "OMX_SetConfig")
}

// omxClkBeginSeek — C: omx_clk_begin_seek (omx.c:532-542)
func omxClkBeginSeek(clk *OmxClk) {
	var cs C.OMX_TIME_CONFIG_CLOCKSTATETYPE
	cs.nSize = C.OMX_U32(unsafe.Sizeof(cs))
	C.omx_init_version_(&cs.nVersion)
	cs.eState = C.OMX_TIME_ClockStateStopped
	Omxchk(C.omx_SetParameter_(clk.c.Handle, C.OMX_IndexConfigTimeClockState,
		C.OMX_PTR(unsafe.Pointer(&cs))), "OMX_SetParameter")
	clk.seekInProgress = 2
}

// omxClkSeekDone — C: omx_clk_seek_done (omx.c:547-564)
func omxClkSeekDone(clk *OmxClk) {
	if clk.seekInProgress == 0 {
		return
	}
	clk.seekInProgress--
	if clk.seekInProgress != 0 {
		return
	}
	var cstate C.OMX_TIME_CONFIG_CLOCKSTATETYPE
	cstate.nSize = C.OMX_U32(unsafe.Sizeof(cstate))
	C.omx_init_version_(&cstate.nVersion)
	cstate.eState = C.OMX_TIME_ClockStateWaitingForStartTime
	if clk.hasAudio != 0 {
		cstate.nWaitMask = 1
	} else {
		cstate.nWaitMask = 2
	}
	Omxchk(C.omx_SetParameter_(clk.c.Handle, C.OMX_IndexConfigTimeClockState,
		C.OMX_PTR(unsafe.Pointer(&cstate))), "OMX_SetParameter")
}

// omxClkSetup — C: omx_clk_init (omx.c:569-600)
func omxClkSetup(clk *OmxClk, hasAudio int) {
	var cstate C.OMX_TIME_CONFIG_CLOCKSTATETYPE
	clk.hasAudio = hasAudio
	clk.seekInProgress = 0
	cstate.nSize = C.OMX_U32(unsafe.Sizeof(cstate))
	C.omx_init_version_(&cstate.nVersion)
	cstate.eState = C.OMX_TIME_ClockStateStopped
	Omxchk(C.omx_SetParameter_(clk.c.Handle, C.OMX_IndexConfigTimeClockState,
		C.OMX_PTR(unsafe.Pointer(&cstate))), "OMX_SetParameter")

	OmxSetState(clk.c, C.OMX_StateIdle)

	cstate.eState = C.OMX_TIME_ClockStateWaitingForStartTime
	if hasAudio != 0 {
		cstate.nWaitMask = 1
	} else {
		cstate.nWaitMask = 2
	}
	Omxchk(C.omx_SetParameter_(clk.c.Handle, C.OMX_IndexConfigTimeClockState,
		C.OMX_PTR(unsafe.Pointer(&cstate))), "OMX_SetParameter")

	var refClock C.OMX_TIME_CONFIG_ACTIVEREFCLOCKTYPE
	refClock.nSize = C.OMX_U32(unsafe.Sizeof(refClock))
	C.omx_init_version_(&refClock.nVersion)
	if hasAudio != 0 {
		refClock.eClock = C.OMX_TIME_RefClockAudio
	} else {
		refClock.eClock = C.OMX_TIME_RefClockVideo
	}
	Omxchk(C.omx_SetConfig_(clk.c.Handle, C.OMX_IndexConfigTimeActiveRefClock,
		C.OMX_PTR(unsafe.Pointer(&refClock))), "OMX_SetConfig")

	OmxSetState(clk.c, C.OMX_StateExecuting)
}

// omxClkThread — C: omx_clk_thread (omx.c:605-666)
func omxClkThread(clk *OmxClk) {
	run := true

	clk.mp.Mutex.Lock()
	for run {
		for len(clk.q) == 0 {
			clk.cond.Wait()
		}
		cmd := clk.q[0]
		clk.q = clk.q[1:]
		clk.mp.Mutex.Unlock()

		switch cmd.cmd {
		case omxClkCmdQuit:
			run = false
		case omxClkCmdInit:
			omxClkSetup(clk, cmd.arg)
		case omxClkCmdPause:
			omxClkSetSpeed(clk, 0)
		case omxClkCmdPlay:
			omxClkSetSpeed(clk, 1<<16)
		case omxClkCmdBeginSeek:
			omxClkBeginSeek(clk)
		case omxClkCmdSeekAudioDone:
			omxClkSeekDone(clk)
		case omxClkCmdSeekVideoDone:
			omxClkSeekDone(clk)
		}

		clk.mp.Mutex.Lock()
	}
	clk.mp.Mutex.Unlock()
}

// omxClkDo — C: omx_clk_do (omx.c:672-682) — caller holds mp.Mutex
func omxClkDo(clk *OmxClk, op, arg int) {
	clk.q = append(clk.q, omxClkCmd{cmd: op, arg: arg})
	clk.cond.Signal()
}

// omxMpBeginSeek — C: omx_mp_begin_seek (omx.c:688-692)
func omxMpBeginSeek(mp *mediacore.MediaPipe) {
	omxClkDo(mp.Extra.(*OmxClk), omxClkCmdBeginSeek, 0)
}

// omxMpSeekAudioDone — C: omx_mp_seek_audio_done (omx.c:697-703)
func omxMpSeekAudioDone(mp *mediacore.MediaPipe) {
	mp.Mutex.Lock()
	omxClkDo(mp.Extra.(*OmxClk), omxClkCmdSeekAudioDone, 0)
	mp.Mutex.Unlock()
}

// omxMpSeekVideoDone — C: omx_mp_seek_video_done (omx.c:708-714)
func omxMpSeekVideoDone(mp *mediacore.MediaPipe) {
	mp.Mutex.Lock()
	omxClkDo(mp.Extra.(*OmxClk), omxClkCmdSeekVideoDone, 0)
	mp.Mutex.Unlock()
}

// omxMpHoldChanged — C: omx_mp_hold_changed (omx.c:719-724)
func omxMpHoldChanged(mp *mediacore.MediaPipe) {
	op := omxClkCmdPlay
	if mp.HoldGate != 0 {
		op = omxClkCmdPause
	}
	omxClkDo(mp.Extra.(*OmxClk), op, 0)
}

// omxMpClockSetup — C: omx_mp_clock_setup (omx.c:729-733)
func omxMpClockSetup(mp *mediacore.MediaPipe, hasAudio bool) {
	ha := 0
	if hasAudio {
		ha = 1
	}
	omxClkDo(mp.Extra.(*OmxClk), omxClkCmdInit, ha)
}

// omxMpSetup — C: omx_mp_init (omx.c:738-761)
func omxMpSetup(mp *mediacore.MediaPipe) {
	if mp.Flags&mediacore.MPVideo == 0 {
		return
	}

	mp.SeekInitiate = omxMpBeginSeek
	mp.SeekAudioDone = omxMpSeekAudioDone
	mp.SeekVideoDone = omxMpSeekVideoDone
	mp.HoldChanged = omxMpHoldChanged
	mp.ClockSetup = omxMpClockSetup

	clk := &OmxClk{mp: mp}
	clk.c = OmxComponentCreate("OMX.broadcom.clock", &mp.Mutex, nil)
	clk.cond = sync.NewCond(&mp.Mutex)
	mp.Extra = clk

	OmxSetState(clk.c, C.OMX_StateIdle)

	omxClkDo(clk, omxClkCmdInit, 1)

	clk.tid = arch.ThreadCreateJoinable("omxclkctrl",
		func(aux any) any {
			omxClkThread(aux.(*OmxClk))
			return nil
		}, clk, arch.ThreadPrioDemuxer)
}

// omxMpFini — C: omx_mp_fini (omx.c:766-778)
func omxMpFini(mp *mediacore.MediaPipe) {
	if mp.Extra == nil {
		return
	}
	clk := mp.Extra.(*OmxClk)

	mp.Mutex.Lock()
	omxClkDo(clk, omxClkCmdQuit, 0)
	mp.Mutex.Unlock()
	clk.tid.Join()
	OmxComponentDestroy(clk.c)
	mp.Extra = nil
}

// OmxGetClock — C: omx_get_clock (omx.c:783-788)
func OmxGetClock(mp *mediacore.MediaPipe) *OmxComponent {
	if clk, ok := mp.Extra.(*OmxClk); ok && clk != nil {
		return clk.c
	}
	return nil
}

// OmxStart — C: omx_init (omx.c:793-802). Registers the pipe extras on
// the MediaSystem instance handed by the composition root (per-instance
// hooks, was the MediaHooks global).
func OmxStart(ms *mediacore.MediaSystem) {
	C.OMX_Init()

	if ms != nil {
		ms.MediaPipeSetupExtra = omxMpSetup
		ms.MediaPipeFiniExtra = omxMpFini
	}

	rpiPixmapStart()
}
