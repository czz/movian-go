//go:build rpi

// rpi_main.go — C: src/arch/rpi/rpi_main.c — platform globals,
// revision detection, TV service init, vc_gencmd queries, monitor
// timer, and the main() platform sequence (wired from cmd/movian-go).
package rpi

/*
#cgo CFLAGS: -DOMX_SKIP64BIT -I/opt/vc/include -I/opt/vc/include/interface/vmcs_host/linux -I/opt/vc/include/interface/vcos/pthreads
#cgo LDFLAGS: -L/opt/vc/lib -lbcm_host -lvcos -lvchiq_arm -lopenmaxil -lEGL -lGLESv2 -lmmal_core -lmmal_util -lmmal_vc_client -lvchostif

#include <stdlib.h>
#include <string.h>
#include <stdio.h>
#include <stdarg.h>
#include <bcm_host.h>
#include <interface/vmcs_host/vc_tvservice.h>
#include <interface/vmcs_host/vc_vchi_gencmd.h>
#include "vc_gencmd_shim.h"
#include <interface/vcos/vcos.h>

extern void goTvServiceCallback(void *callback_data, uint32_t reason,
				uint32_t param1, uint32_t param2);
extern void goVcosLog(const char *name, int level, const char *msg);

// C: vcos_set_vlog_impl(my_vcos_log) — shim formats fmt+va_list in C
// (va_list can't cross the cgo boundary) then calls the Go body.
static void vcos_log_shim(const VCOS_LOG_CAT_T *cat, VCOS_LOG_LEVEL_T level,
			  const char *fmt, va_list args) {
	char buf[512];
	vsnprintf(buf, sizeof(buf), fmt, args);
	buf[sizeof(buf) - 1] = 0;
	goVcosLog(cat->name, (int)level, buf);
}

static void install_vcos_log(void) {
	vcos_set_vlog_impl(vcos_log_shim);
}

// C: main() fpscr asm (rpi_main.c:900-902) — ARM VFP flush-to-zero.
// Guarded: the asm only exists on ARM; desktop compile-checks skip it.
static void rpi_set_fpscr_fz(void) {
#if defined(__arm__)
	asm volatile("vmrs r0, fpscr\n"
		     "orr r0, $(1 << 24)\n"
		     "vmsr fpscr, r0" : : : "r0");
#endif
}

static void register_tv_callback(void) {
	vc_tv_register_callback(goTvServiceCallback, NULL);
}
*/
import "C"

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"github.com/czz/movian-go/internal/callout"
	"github.com/czz/movian-go/internal/gconf"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/trace"
)

// C: globals from rpi.h / rpi_main.c
const (
	DisplayStatusOff = 0 // DISPLAY_STATUS_OFF
	DisplayStatusOn  = 1 // DISPLAY_STATUS_ON

	RunmodeExit    = 0 // RUNMODE_EXIT
	RunmodeRunning = 1 // RUNMODE_RUNNING
	RunmodeStandby = 2 // RUNMODE_STANDBY
)

var (
	// DispmanDisplay — C: DISPMANX_DISPLAY_HANDLE_T dispman_display
	// (rpi_main.c:59)
	DispmanDisplay uint32 // C: DISPMANX_DISPLAY_HANDLE_T (uint32)

	// DisplayStatus — C: int display_status = DISPLAY_STATUS_ON
	DisplayStatus = DisplayStatusOn

	// CecWeAreNotActive — C: int cec_we_are_not_active
	CecWeAreNotActive int

	// RestartUI — C: int restart_ui (set by rpi_tv VPI hook)
	RestartUI int

	// Runmode — C: static int runmode
	Runmode int

	// Ctrlc — C: static int ctrlc
	Ctrlc int
)

// C: static struct strtab rpirevisions[] (rpi_main.c:81-101)
var rpiRevisions = []struct {
	name string
	rev  int
}{
	{"-1 Model B r1.0 256MB", 2},
	{"-1 Model B r1.0 256MB + ECN0001", 3},
	{"-1 Model B r2.0 256MB", 4},
	{"-1 Model B r2.0 256MB", 5},
	{"-1 Model B r2.0 256MB", 6},
	{"-1 Model A 256MB", 7},
	{"-1 Model A 256MB", 8},
	{"-1 Model A 256MB", 9},
	{"-1 Model B r2.0 512MB", 0xd},
	{"-1 Model B r2.0 512MB", 0xe},
	{"-1 Model B r2.0 512MB", 0xf},
	{"-1 Model B+ 512MB", 0x10},
	{"-1 Compute Module 512MB", 0x11},
	{"-1 Model A+ 512MB", 0x12},
	{"-2 Model B (Sony) 1GB", 0xa01041},
	{"-2 Model B (Embest) 1GB", 0xa21041},
	{"-Zero (Sony) 512MB", 0x900092},
	{"-3 Model B (Sony) 1GB", 0xa02082},
	{"-3 Model B (Sony) 1GB", 0xa22082},
}

// val2str — C: val2str over rpirevisions (misc/strtab.h)
func val2str(v int) string {
	for _, e := range rpiRevisions {
		if e.rev == v {
			return e.name
		}
	}
	return ""
}

// RpiGetRevision — C: rpi_get_revision (rpi_main.c:103-137)
// deviceType — C: gconf.device_type (rpi_main.c writes the gconf global
// during rpi_get_revision; the Go reporter reads it from usage.Config).
var deviceType string

// DeviceType — the detected board name, consumed by usage.Config.
func DeviceType() string { return deviceType }

func RpiGetRevision(gc *gconf.T) {
	fp, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return
	}
	defer fp.Close()

	sc := bufio.NewScanner(fp)
	for sc.Scan() {
		buf := sc.Text()
		if !strings.HasPrefix(buf, "Revision") {
			continue
		}
		i := strings.IndexByte(buf, ':')
		if i < 0 {
			continue
		}
		x := strings.TrimSpace(buf[i+1:])
		rev64, err := strconv.ParseInt(x, 16, 64)
		if err != nil {
			continue
		}
		rev := int(rev64) & 0xffffff

		if rev < 0xa {
			// Limit max video buffer size to 64 for 256MB systems RAM
			gc.MaxVideoBufferSize = 64
		}

		model := val2str(rev)
		if model == "" {
			deviceType = fmt.Sprintf(
				"Raspberry Pi unknown revision 0x%x", rev)
		} else {
			deviceType = "Raspberry Pi" + model
		}
		return
	}
}

// KillFramebuffer — C: kill_framebuffer (rpi_main.c:778-788).
// Turning off TV output seems to kill framebuffer.
func KillFramebuffer() {
	var st syscall.Stat_t
	if syscall.Stat("/dev/fb0", &st) != nil {
		return // No frame buffer
	}
	C.vc_tv_power_off()
	C.vc_tv_hdmi_power_on_preferred()
}

// tvUpdateState — C: tv_update_state (rpi_main.c:795-839)
func tvUpdateState() {
	var state C.TV_DISPLAY_STATE_T
	if C.vc_tv_get_display_state(&state) != 0 {
		rpiTS.Trace(trace.TRACE_DEBUG, "TV", "Failed to get TV state")
		return
	}
	rpiTS.Trace(trace.TRACE_DEBUG, "TV", "Statemask: 0x%08x",
		uint32(state.state))

	hdmiLive := (uint32(state.state) & 0xf) == 0xa

	if !hdmiLive {
		rpiTS.Trace(trace.TRACE_DEBUG, "TV", "HDMI not active")
		return
	}

	rpiTS.Trace(trace.TRACE_DEBUG, "TV",
		"HDMI current output: %dx%d @ %dfps",
		uint32(C.tv_hdmi_(&state).width),
		uint32(C.tv_hdmi_(&state).height),
		uint32(C.tv_hdmi_(&state).frame_rate))

	var preferGroup C.HDMI_RES_GROUP_T
	var preferMode C.uint32_t

	numModes := int(C.vc_tv_hdmi_get_supported_modes_new(
		C.HDMI_RES_GROUP_CEA, nil, 0, &preferGroup, &preferMode))

	rpiTS.Trace(trace.TRACE_DEBUG, "TV", "number of modes: %d", numModes)

	modes := make([]C.TV_SUPPORTED_MODE_NEW_T, numModes)

	numModes = int(C.vc_tv_hdmi_get_supported_modes_new(
		C.HDMI_RES_GROUP_CEA, &modes[0], C.uint32_t(numModes),
		&preferGroup, &preferMode))

	for i := 0; i < numModes; i++ {
		m := &modes[i]
		rpiTS.Trace(trace.TRACE_DEBUG, "TV",
			"Supported mode %dx%d @ %d FPS",
			uint32(C.tvmode_width_(m)), uint32(C.tvmode_height_(m)), uint32(C.tvmode_frame_rate_(m)))
	}
}

// tvServiceCallback — C: tv_service_callback (rpi_main.c:845-861)
func tvServiceCallback(reason, param1, param2 uint32) {
	rpiTS.Trace(trace.TRACE_DEBUG, "TV", "State change 0x%08x 0x%08x 0x%08x",
		reason, param1, param2)
	// C: #if 0'd display_status update — kept out canonically
	tvUpdateState()
}

// TvStart — C: tv_init (rpi_main.c:867-872)
func TvStart() {
	C.register_tv_callback()
	tvUpdateState()
}

// StosStopSplash — C: stos_stop_splash (rpi_main.c:748-772)
func StosStopSplash() {
	const runfile = "/var/run/stos-splash.pid"
	data, err := os.ReadFile(runfile)
	if err != nil {
		return
	}
	var val int
	if _, err := fmt.Sscanf(string(data), "%d", &val); err != nil {
		return
	}
	rpiTS.Trace(trace.TRACE_DEBUG, "STOS",
		"Asking stos-splash (pid: %d) to stop", val)
	syscall.Kill(val, syscall.SIGINT)

	for i := 0; i < 100; i++ {
		var st syscall.Stat_t
		if syscall.Stat(runfile, &st) != nil {
			rpiTS.Trace(trace.TRACE_DEBUG, "STOS", "stos-splash is gone")
			return
		}
		syscallNanosleep(10 * 1000 * 1000)
	}
	rpiTS.Trace(trace.TRACE_ERROR, "STOS", "stos-splash fails to terminate")
}

// syscallNanosleep — C: usleep
func syscallNanosleep(ns int64) {
	var ts syscall.Timespec
	ts.Sec = int32(ns / 1e9)
	ts.Nsec = int32(ns % 1e9)
	syscall.Nanosleep(&ts, nil)
}

// RpiIsCodecEnabled — C: rpi_is_codec_enabled (rpi_main.c:985-994)
func RpiIsCodecEnabled(id string) bool {
	query := fmt.Sprintf("codec_enabled %s", id)
	buf := make([]C.char, 64)
	cq := C.CString(query)
	C.vc_gencmd0(&buf[0], C.int(len(buf)), cq)
	C.free(unsafe.Pointer(cq))
	s := C.GoString(&buf[0])
	rpiTS.Trace(trace.TRACE_INFO, "VideoCore", "%s", s)
	return strings.Contains(s, "=enabled")
}

// InstallVcosLog — C: vcos_set_vlog_impl(my_vcos_log) (rpi_main.c:917)
func InstallVcosLog() {
	C.install_vcos_log()
}

// BcmHostStart — C: bcm_host_init (rpi_main.c:913)
func BcmHostStart() {
	C.bcm_host_init()
}

// SetFpscrFlushToZero — C: the fpscr asm at rpi_main.c:900-902.
func SetFpscrFlushToZero() {
	C.rpi_set_fpscr_fz()
}

// monitor timer — C: static callout_t timer (rpi_main.c:997)
var rpiMonitorTimer callout.Callout

// rpiCalloutSystem — C: the callout_* globals. Wired by the app init
// (this is the rpi-target equivalent of ctx.calloutSystem).
var rpiCalloutSystem *callout.CalloutSystem

// rpiMonitorTimercb — C: rpi_monitor_timercb (rpi_main.c:1002-1015)
func rpiMonitorTimercb(c *callout.Callout, aux any) {
	if cs := rpiCalloutSystem; cs != nil {
		cs.Arm(&rpiMonitorTimer, rpiMonitorTimercb, aux, 10)
	}

	p := aux.(*propcore.Prop)
	tempprop := pmCreate(p, "temp")

	var buf [64]C.char
	cm := C.CString("measure_temp")
	C.vc_gencmd0(&buf[0], C.int(len(buf)), cm)
	C.free(unsafe.Pointer(cm))
	s := C.GoString(&buf[0])
	if x, ok := strings.CutPrefix(s, "temp="); ok {
		if v, err := strconv.Atoi(strings.TrimSpace(x)); err == nil {
			pmSetInt(tempprop, v)
		}
	}
}

var rpiPM *propcore.PropManager

// rpiTS — C: trace() global reached by rpi/omx/tv file statics.
var rpiTS *trace.TraceSystem

// pmCreate — C: prop_create(parent, name)
func pmCreate(parent *propcore.Prop, name string) *propcore.Prop {
	return rpiPM.CreateEx(parent, name, nil, false, false)
}

// pmSetInt — C: prop_set(p, "cpu", PROP_SET_INT, v)
func pmSetInt(p *propcore.Prop, v int) {
	rpiPM.SetVEx(nil, p, "cpu", v)
}

// RpiMonitorStart — C: rpi_monitor_init + INITME(INIT_GROUP_API)
// (rpi_main.c:1021-1030)
func RpiMonitorStart(pm *propcore.PropManager,
	cs *callout.CalloutSystem, ts *trace.TraceSystem) {
	rpiPM = pm
	rpiCalloutSystem = cs
	rpiTS = ts
	p := pm.CreateEx(pm.GetGlobal(), "system", nil, false, false)
	rpiMonitorTimercb(nil, p)
}

// ArchStopReq — C: arch_stop_req (rpi_main.c:978-982)
func ArchStopReq() int {
	Runmode = RunmodeExit
	return 0
}
