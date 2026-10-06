//go:build libcec && (!linux || libceccgo)

package ipc

/*
#cgo LDFLAGS: -lcec
#include <stdlib.h>
#include <stdio.h>
#include <string.h>
#include <libcec/cecc.h>

extern void goCecLogMessage(void *lib, const cec_log_message *message);
extern void goCecKeyPress(void *aux, const cec_keypress *key);
extern void goCecSourceActivated(void *aux, cec_logical_address la, uint8_t on);
extern void goCecCommand(void *aux, const cec_command *cmd);

// Callback trampolines — the installed libcec (v7) ICECCallbacks uses
// pointer-based void-returning callbacks; the movian C code was written
// against the older CBCec* by-value API. The trampolines adapt the
// signatures; the Go bodies keep the canonical C semantics.
static void logMessageTramp(void *cbparam, const cec_log_message *message) {
	goCecLogMessage(cbparam, message);
}

static void keyPressTramp(void *cbparam, const cec_keypress *key) {
	goCecKeyPress(cbparam, key);
}

static void sourceActivatedTramp(void *cbparam, cec_logical_address la,
	uint8_t on) {
	goCecSourceActivated(cbparam, la, on);
}

static void commandReceivedTramp(void *cbparam, const cec_command *cmd) {
	goCecCommand(cbparam, cmd);
}

// C: static ICECCallbacks g_callbacks
static ICECCallbacks g_callbacks = {
	.logMessage      = logMessageTramp,
	.keyPress        = keyPressTramp,
	.commandReceived = commandReceivedTramp,
	.sourceActivated = sourceActivatedTramp,
};

static ICECCallbacks *getCallbacks(void) {
	return &g_callbacks;
}

// v7: libcec_find_adapters gained iBufSize/strDevicePath args.
static int8_t findAdapters(libcec_connection_t conn, cec_adapter *ca) {
	return libcec_find_adapters(conn, ca, 1, NULL);
}

static void setDeviceName(libcec_configuration *c, const char *name) {
	snprintf(c->strDeviceName, sizeof(c->strDeviceName), "%s", name);
}

static void setCallbackParam(libcec_configuration *c, void *p) {
	c->callbackParam = p;
}
*/
import "C"

import (
	"runtime/cgo"
	"unsafe"

	"github.com/czz/movian-go/internal/app"
	eventpkg "github.com/czz/movian-go/internal/event"
	"github.com/czz/movian-go/internal/htsmsg"
	propcore "github.com/czz/movian-go/internal/prop"
	settings "github.com/czz/movian-go/internal/settings"
	"github.com/czz/movian-go/internal/task"
	"github.com/czz/movian-go/internal/trace"
	"github.com/czz/movian-go/internal/video"
)

// cecIPC — C: static libcec_connection_t conn + cec_config +
// longpress_select + event/settings managers (libcec.c statics).
// Allocated by IPC.LibcecStart; the C callbacks recover it via
// config.callbackParam (cgo.Handle).
type cecIPC struct {
	ipc             *IPC
	conn            C.libcec_connection_t
	config          C.libcec_configuration
	longpressSelect int // C: static int longpress_select
	em              *eventpkg.EventManager
	tasks           *task.TaskSystem
	sm              *settings.SettingsManager
	handle          cgo.Handle
}

// cecFromAux recovers the cecIPC passed through cbparam (C: the global
// cec_state — parametric here).
func cecFromAux(aux unsafe.Pointer) *cecIPC {
	return cgo.Handle(aux).Value().(*cecIPC)
}

// LibcecEnabled — C: CONFIG_LIBCEC (enabled only on platforms whose
// configure script turns it on, e.g. RPi). Gates the "cecdebug" dev
// bool in init_dev_settings.
const LibcecEnabled = true

/**
 * C: log_message — v7 callback is void; the int return is dropped by
 * the trampoline.
 */
func logMessage(lib unsafe.Pointer, message *C.cec_log_message) int {
	cec := cecFromAux(lib)
	var level int
	switch message.level {
	case C.CEC_LOG_ERROR:
		level = trace.TRACE_ERROR
	case C.CEC_LOG_WARNING, C.CEC_LOG_NOTICE:
		level = trace.TRACE_INFO
	default:
		if !cec.ipc.gcfg.EnableCecDebug.Load() {
			return 1
		}
		level = trace.TRACE_DEBUG
	}
	cec.ipc.ts.Trace(level, "CEC", "%s", C.GoString(message.message))
	return 1
}

// C: #define AVEC(x...) (const action_type_t []){x, ACTION_NONE}
// The trailing ACTION_NONE terminator is implicit in the Go slice length.
var btnToAction = map[C.cec_user_control_code][]eventpkg.ActionType{
	C.CEC_USER_CONTROL_CODE_SELECT:       {eventpkg.ACTION_ACTIVATE},
	C.CEC_USER_CONTROL_CODE_LEFT:         {eventpkg.ACTION_LEFT},
	C.CEC_USER_CONTROL_CODE_UP:           {eventpkg.ACTION_UP},
	C.CEC_USER_CONTROL_CODE_RIGHT:        {eventpkg.ACTION_RIGHT},
	C.CEC_USER_CONTROL_CODE_DOWN:         {eventpkg.ACTION_DOWN},
	C.CEC_USER_CONTROL_CODE_EXIT:         {eventpkg.ACTION_NAV_BACK},
	C.CEC_USER_CONTROL_CODE_DOT:          {eventpkg.ACTION_ITEMMENU},
	C.CEC_USER_CONTROL_CODE_ROOT_MENU:    {eventpkg.ACTION_MENU},
	C.CEC_USER_CONTROL_CODE_PLAY:         {eventpkg.ACTION_PLAYPAUSE},
	C.CEC_USER_CONTROL_CODE_STOP:         {eventpkg.ACTION_STOP},
	C.CEC_USER_CONTROL_CODE_PAUSE:        {eventpkg.ACTION_PAUSE},
	C.CEC_USER_CONTROL_CODE_RECORD:       {eventpkg.ACTION_RECORD},
	C.CEC_USER_CONTROL_CODE_REWIND:       {eventpkg.ACTION_SEEK_BACKWARD},
	C.CEC_USER_CONTROL_CODE_FAST_FORWARD: {eventpkg.ACTION_SEEK_FORWARD},
	C.CEC_USER_CONTROL_CODE_FORWARD:      {eventpkg.ACTION_SKIP_FORWARD},
	C.CEC_USER_CONTROL_CODE_BACKWARD:     {eventpkg.ACTION_SKIP_BACKWARD},
	C.CEC_USER_CONTROL_CODE_CHANNEL_UP:   {eventpkg.ACTION_NEXT_CHANNEL},
	C.CEC_USER_CONTROL_CODE_CHANNEL_DOWN: {eventpkg.ACTION_PREV_CHANNEL},
	C.CEC_USER_CONTROL_CODE_F1_BLUE:      {eventpkg.ACTION_SYSINFO},
	C.CEC_USER_CONTROL_CODE_F4_YELLOW:    {eventpkg.ACTION_SHOW_MEDIA_STATS},
	C.CEC_USER_CONTROL_CODE_SUB_PICTURE:  {eventpkg.ACTION_CYCLE_SUBTITLE},
}

/**
 * C: keypress
 */
func keypress(aux unsafe.Pointer, kp *C.cec_keypress) int {
	cec := cecFromAux(aux)
	var e *eventpkg.Event

	if cec.ipc.gcfg.EnableCecDebug.Load() {
		cec.ipc.ts.Trace(trace.TRACE_DEBUG, "CEC",
			"Got keypress code=0x%x duration=0x%x",
			uint(kp.keycode), uint(kp.duration))
	}

	if cec.longpressSelect != 0 {
		if kp.keycode == C.CEC_USER_CONTROL_CODE_SELECT {
			if kp.duration == 0 {
				return 0
			}
			if kp.duration < 500 {
				e = cec.em.CreateAction(eventpkg.ACTION_ACTIVATE).AsEvent()
			} else {
				e = cec.em.CreateAction(eventpkg.ACTION_ITEMMENU).AsEvent()
			}
		}
	}

	if e == nil {
		var avec []eventpkg.ActionType
		if kp.duration == 0 || kp.keycode == cec.config.comboKey {
			avec = btnToAction[kp.keycode]
		}

		if avec != nil {
			e = cec.em.CreateActionMulti(avec).AsEvent()
		}
	}

	if e != nil {
		e.Flags |= eventpkg.EventKeypress // C: e->e_flags |= EVENT_KEYPRESS
		cec.em.ToUI(e)
	}
	return 1
}

/**
 * C: source_activated
 */
func sourceActivated(aux unsafe.Pointer, la C.cec_logical_address, on C.uint8_t) {
	cec := cecFromAux(aux)
	state := "inactive"
	if on != 0 {
		state = "active"
	}
	cec.ipc.ts.Trace(trace.TRACE_INFO, "CEC", "Logical address %d is %s",
		int(la), state)
}

/**
 * C: handle_cec_command
 */
func handleCecCommand(aux unsafe.Pointer, cmd *C.cec_command) int {
	cec := cecFromAux(aux)
	switch cmd.opcode {
	case C.CEC_OPCODE_STANDBY:
		if cmd.initiator == C.CECDEVICE_TV {
			cec.ipc.ts.Trace(trace.TRACE_INFO, "CEC", "TV STANDBY")
		}
	}
	return 1
}

/**
 * C: set_activate_source
 */
func setActivateSource(opaque any, value any) {
	cec := opaque.(*cecIPC)
	v := 0
	if i, ok := value.(int); ok {
		v = i
	}
	cec.config.bActivateSource = C.uint8_t(v)
	C.libcec_set_configuration(cec.conn, &cec.config)
}

/**
 * C: set_stop_combo_mode
 */
func setStopComboMode(opaque any, value any) {
	cec := opaque.(*cecIPC)
	v := 0
	if i, ok := value.(int); ok {
		v = i
	}
	if v != 0 {
		cec.config.comboKey = C.CEC_USER_CONTROL_CODE_STOP
	} else {
		cec.config.comboKey = 0xfe
	}
	C.libcec_set_configuration(cec.conn, &cec.config)
}

/**
 * C: set_longpress_select
 */
func setLongpressSelect(opaque any, value any) {
	cec := opaque.(*cecIPC)
	if i, ok := value.(int); ok {
		cec.longpressSelect = i
	}
}

/**
 * C: libcec_init_thread
 */
func (cec *cecIPC) libcecStartThread() {
	set := cec.sm.SettingGetDir("settings:tv")

	C.libcec_clear_configuration(&cec.config)
	cec.config.callbacks = C.getCallbacks()
	cec.handle = cgo.NewHandle(cec)
	C.setCallbackParam(&cec.config, unsafe.Pointer(cec.handle))
	// C: snprintf(cec_config.strDeviceName, ..., "%s", APPNAMEUSER)
	devName := C.CString(app.AppNameUser)
	C.setDeviceName(&cec.config, devName)
	C.free(unsafe.Pointer(devName))
	cec.config.deviceTypes.types[0] = C.CEC_DEVICE_TYPE_RECORDING_DEVICE

	cec.conn = C.libcec_initialise(&cec.config)
	if cec.conn == nil {
		cec.ipc.ts.Trace(trace.TRACE_ERROR, "CEC", "Unable to init libcec")
		return
	}

	C.libcec_init_video_standalone(cec.conn)

	var ca C.cec_adapter
	numAdapters := int(C.findAdapters(cec.conn, &ca))
	if numAdapters < 1 {
		C.libcec_destroy(cec.conn)
		cec.conn = nil
		cec.ipc.ts.Trace(trace.TRACE_ERROR, "CEC", "No adapters found")
		return
	}
	cec.ipc.ts.Trace(trace.TRACE_DEBUG, "CEC", "Using adapter %s on %s",
		C.GoString(&ca.comm[0]), C.GoString(&ca.path[0]))

	cec.sm.SettingCreate(settings.SettingBool, set,
		settings.SettingsInitialUpdate,
		settings.SettingTagTitle, cec.sm.P("Switch TV input source"),
		settings.SettingTagValue, 1,
		settings.SettingTagCallback, setActivateSource, cec,
		settings.SettingTagStore, "cec", "controlinput")

	cec.sm.SettingCreate(settings.SettingBool, set,
		settings.SettingsInitialUpdate,
		settings.SettingTagTitle, cec.sm.P("Use STOP key for combo input"),
		settings.SettingTagValue, 1,
		settings.SettingTagCallback, setStopComboMode, cec,
		settings.SettingTagStore, "cec", "stopcombo")

	cec.sm.SettingCreate(settings.SettingBool, set,
		settings.SettingsInitialUpdate,
		settings.SettingTagTitle, cec.sm.P("Longpress SELECT for item menu"),
		settings.SettingTagValue, 1,
		settings.SettingTagCallback, setLongpressSelect, cec,
		settings.SettingTagStore, "cec", "longpress_select")

	if C.libcec_open(cec.conn, &ca.comm[0], 5000) == 0 {
		cec.ipc.ts.Trace(trace.TRACE_ERROR, "CEC",
			"Unable to open connection to %s", C.GoString(&ca.comm[0]))
		C.libcec_destroy(cec.conn)
		cec.conn = nil
		return
	}
}

/**
 * C: libcec_init
 */
func (i *IPC) libcecStart(em *eventpkg.EventManager, sm *settings.SettingsManager, tasks *task.TaskSystem) {
	i.cec = &cecIPC{ipc: i, em: em, sm: sm, tasks: tasks}
	// C: hts_thread_create_detached("cec", libcec_init_thread, NULL,
	//                             THREAD_PRIO_BGTASK)
	go i.cec.libcecStartThread()
}

/**
 * C: libcec_fini
 */
func (i *IPC) libcecFini() {
}

/**
 * C: activate_self
 */
func activateSelf(aux any) {
	cec := aux.(*cecIPC)
	C.libcec_set_active_source(cec.conn, C.CEC_DEVICE_TYPE_RESERVED)
}

// libcecVpi — C: libcec_vpi (libcec.c:387-395). C registers it via
// VPI_REGISTER; in Go cmd/movian-go init appends LibcecVpiHandler to the
// fan-out when CONFIG_LIBCEC is on.
func (i *IPC) libcecVpi(op video.VPIOp, info *htsmsg.HTSMsg, p, origin *propcore.Prop) {
	if i.cec == nil || i.cec.conn == nil {
		return
	}

	if op == video.VPIStart {
		i.cec.tasks.Run(activateSelf, i.cec)
	}
}

// initPlatform — libcec builds wire the VPI handler (C: VPI_REGISTER).
func (i *IPC) initPlatform() { i.LibcecVpiHandler = i.libcecVpi }

// LibcecStart — C: INITME(INIT_GROUP_IPC, libcec_init, libcec_fini, 10).
// The C globals (event manager, settings manager) are injected here.
func (i *IPC) LibcecStart(em *eventpkg.EventManager, sm *settings.SettingsManager, tasks *task.TaskSystem) {
	i.libcecStart(em, sm, tasks)
}

// LibcecFini — C: libcec_fini (INIT_GROUP_IPC fini slot).
func (i *IPC) LibcecFini() {
	i.libcecFini()
}
