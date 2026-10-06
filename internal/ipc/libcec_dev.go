//go:build linux && libcec && !libceccgo

package ipc

// libcec_dev.go — pure-Go replacement for the libcec backend, using the
// kernel CEC UAPI (/dev/cec*) instead of -lcec. Selected by the `libcec`
// tag on linux; -tags "libcec libceccgo" selects the canonical cgo
// libcec backend (libcec.go).
//
// Mapping vs libcec.go / libcec.c:
//   libcec_find_adapters        → scan /dev/cec0..7, first adapter with
//                                 CEC_CAP_TRANSMIT|CEC_CAP_LOG_ADDRS
//   libcec_initialise + open    → open(2) + CEC_ADAP_S_LOG_ADDRS
//   libcec_init_video_standalone→ nothing: the kernel framework handles
//                                 the CEC housekeeping replies itself
//   ICECCallbacks.logMessage    → own TRACE lines (kernel has no logs)
//   ICECCallbacks.keyPress      → CEC_MSG_USER_CONTROL_PRESSED (0x44) /
//                                 USER_CONTROL_RELEASED (0x45), duration
//                                 measured here like libcec does
//   ICECCallbacks.commandReceived→ CEC_MSG_* opcode dispatch
//   ICECCallbacks.sourceActivated→ synthesized from ACTIVE_SOURCE (0x82)
//                                 / INACTIVE_SOURCE (0x9d) broadcasts
//   libcec_set_active_source    → CEC_TRANSMIT ACTIVE_SOURCE broadcast
//   cec_config.bActivateSource  → send ACTIVE_SOURCE after claiming the
//                                 logical address (kernel equivalent of
//                                 libcec's activate-on-open)
//   cec_config.comboKey         → local logic only (same as cgo variant:
//                                 keypress() reads cec.comboKey)
//
// Adapter coverage: /dev/cec* covers both SoC-integrated CEC and
// Pulse-Eight USB dongles on kernels >= ~4.12 (pulse8-cec driver). The
// legacy Pulse-Eight serial protocol (kernel without pulse8-cec) is not
// implemented — use the libceccgo rollback for that case.

import (
	"fmt"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/czz/movian-go/internal/app"
	eventpkg "github.com/czz/movian-go/internal/event"
	"github.com/czz/movian-go/internal/htsmsg"
	propcore "github.com/czz/movian-go/internal/prop"
	settings "github.com/czz/movian-go/internal/settings"
	"github.com/czz/movian-go/internal/task"
	"github.com/czz/movian-go/internal/trace"
	"github.com/czz/movian-go/internal/video"
)

/* Kernel CEC UAPI — linux/cec.h, sizes match the C structs. */

type cecMsg struct { // C: struct cec_msg (56 bytes)
	txTs, rxTs                      uint64
	len, timeout, sequence, flags   uint32
	msg                             [16]uint8
	reply, rxStatus, txStatus       uint8
	txArbLost, txNack, txLow, txErr uint8
	_                               [1]byte
}

type cecLogAddrs struct { // C: struct cec_log_addrs (92 bytes)
	logAddr           [4]uint8
	logAddrMask       uint16
	cecVersion        uint8
	numLogAddrs       uint8
	vendorID          uint32
	flags             uint32
	osdName           [15]uint8
	primaryDeviceType [4]uint8
	logAddrType       [4]uint8
	allDeviceTypes    [4]uint8
	features          [4][12]uint8
}

type cecCaps struct { // C: struct cec_caps (76 bytes)
	driver, name                    [32]uint8
	availableLogAddrs, capabilities uint32
	version                         uint32
}

type cecEvent struct { // C: struct cec_event (80 bytes)
	ts    uint64
	event uint32
	flags uint32
	raw   [16]uint32 // state_change: phys_addr u16, mask u16
}

// ioctl numbers — _IOWR/_IOR('a', nr, size).
func cecIOC(dir, nr, size uintptr) uintptr {
	return dir<<30 | size<<16 | uintptr('a')<<8 | nr
}

var (
	iocCecAdapGCaps     = cecIOC(3, 0, 76)
	iocCecAdapGPhysAddr = cecIOC(2, 1, 2)
	iocCecAdapGLogAddrs = cecIOC(2, 3, 92)
	iocCecAdapSLogAddrs = cecIOC(3, 4, 92)
	iocCecTransmit      = cecIOC(3, 5, 56)
	iocCecReceive       = cecIOC(3, 6, 56)
	iocCecDQEvent       = cecIOC(3, 7, 80)
)

func cecIoctl(fd int, req uintptr, arg unsafe.Pointer) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), req,
		uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}

// CEC constants (linux/cec.h)
const (
	cecCapTransmit = 1 << 2
	cecCapLogAddrs = 1 << 1
	cecCapPhysAddr = 1 << 0

	cecLogAddrBroadcast = 15
	cecLogAddrTV        = 0

	cecPrimDevTypeRecord = 1 // CEC_OP_PRIM_DEVTYPE_RECORD
	cecLogAddrTypeRecord = 1 // CEC_LOG_ADDR_TYPE_RECORD
	cecVersion14         = 5 // CEC_OP_CEC_VERSION_1_4
	cecPhysAddrInvalid   = 0xffff

	cecEventStateChange = 1
	cecEventLostMsgs    = 2

	cecMsgUserControlPressed  = 0x44
	cecMsgUserControlReleased = 0x45
	cecMsgStandby             = 0x36
	cecMsgActiveSource        = 0x82
	cecMsgInactiveSource      = 0x9d
)

// CEC user control codes — same values as cec_user_control_code
// (cec.h), which are the standard HDMI CEC UI command codes.
const (
	cecUICSelect      = 0x00
	cecUICUp          = 0x01
	cecUICDown        = 0x02
	cecUICLeft        = 0x03
	cecUICRight       = 0x04
	cecUICRootMenu    = 0x09
	cecUICExit        = 0x0d
	cecUICDot         = 0x2a
	cecUICChannelUp   = 0x30
	cecUICChannelDown = 0x31
	cecUICPlay        = 0x44
	cecUICStop        = 0x45
	cecUICPause       = 0x46
	cecUICRecord      = 0x47
	cecUICRewind      = 0x48
	cecUICFastForward = 0x49
	cecUICForward     = 0x4b
	cecUICBackward    = 0x4c
	cecUICSubPicture  = 0x51
	cecUICF1Blue      = 0x71
	cecUICF4Yellow    = 0x74
)

// cecIPC — C: static conn + cec_config + longpress_select +
// event/settings managers (libcec.c statics). Here conn is the kernel
// /dev/cec fd; config fields map to the log_addrs/request state.
type cecIPC struct {
	ipc             *IPC
	mu              sync.Mutex // guards fd, myLogAddr, physAddr (task/settings goroutines)
	fd              int        // C: conn
	bActivateSource int        // C: config.bActivateSource
	comboKey        uint8      // C: config.comboKey
	longpressSelect int        // C: static int longpress_select
	myLogAddr       uint8
	physAddr        uint16
	lastKey         uint8 // rxLoop only
	lastPressNs     int64 // rxLoop only
	em              *eventpkg.EventManager
	tasks           *task.TaskSystem
	sm              *settings.SettingsManager
}

// LibcecEnabled — C: CONFIG_LIBCEC.
const LibcecEnabled = true

// cecLog — C: log_message callback.
func (cec *cecIPC) cecLog(level int, format string, args ...any) {
	if level == trace.TRACE_DEBUG && !cec.ipc.gcfg.EnableCecDebug.Load() {
		return
	}
	cec.ipc.ts.Trace(level, "CEC", "%s", fmt.Sprintf(format, args...))
}

// C: #define AVEC(x...) (const action_type_t []){x, ACTION_NONE}
var btnToAction = map[uint8][]eventpkg.ActionType{
	cecUICSelect:      {eventpkg.ACTION_ACTIVATE},
	cecUICLeft:        {eventpkg.ACTION_LEFT},
	cecUICUp:          {eventpkg.ACTION_UP},
	cecUICRight:       {eventpkg.ACTION_RIGHT},
	cecUICDown:        {eventpkg.ACTION_DOWN},
	cecUICExit:        {eventpkg.ACTION_NAV_BACK},
	cecUICDot:         {eventpkg.ACTION_ITEMMENU},
	cecUICRootMenu:    {eventpkg.ACTION_MENU},
	cecUICPlay:        {eventpkg.ACTION_PLAYPAUSE},
	cecUICStop:        {eventpkg.ACTION_STOP},
	cecUICPause:       {eventpkg.ACTION_PAUSE},
	cecUICRecord:      {eventpkg.ACTION_RECORD},
	cecUICRewind:      {eventpkg.ACTION_SEEK_BACKWARD},
	cecUICFastForward: {eventpkg.ACTION_SEEK_FORWARD},
	cecUICForward:     {eventpkg.ACTION_SKIP_FORWARD},
	cecUICBackward:    {eventpkg.ACTION_SKIP_BACKWARD},
	cecUICChannelUp:   {eventpkg.ACTION_NEXT_CHANNEL},
	cecUICChannelDown: {eventpkg.ACTION_PREV_CHANNEL},
	cecUICF1Blue:      {eventpkg.ACTION_SYSINFO},
	cecUICF4Yellow:    {eventpkg.ACTION_SHOW_MEDIA_STATS},
	cecUICSubPicture:  {eventpkg.ACTION_CYCLE_SUBTITLE},
}

/**
 * C: keypress — identical semantics to the cgo variant: duration==0 on
 * press, ms on release.
 */
func (cec *cecIPC) keypress(keycode uint8, duration uint32) {
	var e *eventpkg.Event

	if cec.ipc.gcfg.EnableCecDebug.Load() {
		cec.ipc.ts.Trace(trace.TRACE_DEBUG, "CEC",
			"Got keypress code=0x%x duration=0x%x",
			uint(keycode), uint(duration))
	}

	if cec.longpressSelect != 0 {
		if keycode == cecUICSelect {
			if duration == 0 {
				return
			}
			if duration < 500 {
				e = cec.em.CreateAction(eventpkg.ACTION_ACTIVATE).AsEvent()
			} else {
				e = cec.em.CreateAction(eventpkg.ACTION_ITEMMENU).AsEvent()
			}
		}
	}

	if e == nil {
		var avec []eventpkg.ActionType
		if duration == 0 || keycode == cec.comboKey {
			avec = btnToAction[keycode]
		}

		if avec != nil {
			e = cec.em.CreateActionMulti(avec).AsEvent()
		}
	}

	if e != nil {
		e.Flags |= eventpkg.EventKeypress // C: e->e_flags |= EVENT_KEYPRESS
		cec.em.ToUI(e)
	}
}

/**
 * C: source_activated
 */
func (cec *cecIPC) sourceActivated(la uint8, on uint8) {
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
func (cec *cecIPC) handleCecCommand(initiator, opcode uint8) {
	if opcode == cecMsgStandby && initiator == cecLogAddrTV {
		cec.ipc.ts.Trace(trace.TRACE_INFO, "CEC", "TV STANDBY")
	}
}

// rxLoop — C: libcec's internal receive thread; kernel CEC_RECEIVE.
func (cec *cecIPC) rxLoop() {
	var m cecMsg
	for {
		m = cecMsg{}
		if err := cecIoctl(cec.fd, iocCecReceive, unsafe.Pointer(&m)); err != nil {
			cec.cecLog(trace.TRACE_ERROR, "CEC_RECEIVE failed: %v", err)
			return
		}
		cec.handleRxMsg(&m)
	}
}

// handleRxMsg — C: the switch inside libcec's dispatch on a received
// message. Separated so unit tests can drive it without a device.
func (cec *cecIPC) handleRxMsg(m *cecMsg) {
	if m.len < 2 || m.rxStatus == 0 {
		return // tx result / invalid
	}
	initiator := m.msg[0] >> 4
	opcode := m.msg[1]
	switch opcode {
	case cecMsgUserControlPressed:
		if m.len >= 3 {
			cec.lastKey = m.msg[2]
			cec.lastPressNs = time.Now().UnixNano()
			cec.keypress(cec.lastKey, 0)
		}
	case cecMsgUserControlReleased:
		dur := uint32((time.Now().UnixNano() - cec.lastPressNs) / 1e6)
		cec.keypress(cec.lastKey, dur)
	case cecMsgActiveSource:
		cec.sourceActivated(initiator, 1)
	case cecMsgInactiveSource:
		cec.sourceActivated(initiator, 0)
	default:
		cec.handleCecCommand(initiator, opcode)
	}
}

// evLoop — kernel CEC_DQEVENT for state changes / lost msgs.
func (cec *cecIPC) evLoop() {
	var ev cecEvent
	for {
		ev = cecEvent{}
		if err := cecIoctl(cec.fd, iocCecDQEvent, unsafe.Pointer(&ev)); err != nil {
			return
		}
		switch ev.event {
		case cecEventStateChange:
			cec.mu.Lock()
			cec.physAddr = uint16(ev.raw[0])
			cec.mu.Unlock()
			cec.cecLog(trace.TRACE_DEBUG,
				"state change: phys_addr=%x mask=%x",
				uint16(ev.raw[0]), uint16(ev.raw[0]>>16))
		case cecEventLostMsgs:
			cec.cecLog(trace.TRACE_INFO, "lost %d msgs", ev.raw[0])
		}
	}
}

// transmit — C: libcec_transmit.
func (cec *cecIPC) transmit(initiator, dest uint8, operands ...uint8) error {
	var m cecMsg
	m.msg[0] = initiator<<4 | dest
	copy(m.msg[1:], operands)
	m.len = uint32(1 + len(operands))
	return cecIoctl(cec.getFD(), iocCecTransmit, unsafe.Pointer(&m))
}

// getFD — fd read under mu (written by findAdapter/libcecFini on other
// goroutines).
func (cec *cecIPC) getFD() int {
	cec.mu.Lock()
	defer cec.mu.Unlock()
	return cec.fd
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
	cec.bActivateSource = v
	if v != 0 {
		cec.transmitActiveSource()
	}
}

// transmitActiveSource — C: libcec_set_active_source(RESERVED):
// broadcast ACTIVE_SOURCE with our physical address.
func (cec *cecIPC) transmitActiveSource() {
	cec.mu.Lock()
	la := cec.myLogAddr
	phys := cec.physAddr
	cec.mu.Unlock()
	if la == 0xff {
		return
	}
	var b [2]byte
	if cecIoctl(cec.getFD(), iocCecAdapGPhysAddr,
		unsafe.Pointer(&phys)) == nil {
		cec.mu.Lock()
		cec.physAddr = phys
		cec.mu.Unlock()
	}
	b[0] = byte(phys >> 8)
	b[1] = byte(phys)
	if err := cec.transmit(la, cecLogAddrBroadcast,
		cecMsgActiveSource, b[0], b[1]); err != nil {
		cec.cecLog(trace.TRACE_ERROR, "ACTIVE_SOURCE failed: %v", err)
	}
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
		cec.comboKey = cecUICStop
	} else {
		cec.comboKey = 0xfe
	}
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

// findAdapter — C: libcec_find_adapters: first /dev/cec* with transmit+
// log_addrs caps.
func (cec *cecIPC) findAdapter() (string, cecCaps, bool) {
	for i := 0; i < 8; i++ {
		path := fmt.Sprintf("/dev/cec%d", i)
		fd, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC, 0)
		if err != nil {
			continue
		}
		var caps cecCaps
		err = cecIoctl(fd, iocCecAdapGCaps, unsafe.Pointer(&caps))
		if err == nil && caps.capabilities&cecCapTransmit != 0 &&
			caps.capabilities&cecCapLogAddrs != 0 {
			cec.mu.Lock()
			cec.fd = fd
			cec.mu.Unlock()
			return path, caps, true
		}
		unix.Close(fd)
	}
	return "", cecCaps{}, false
}

/**
 * C: libcec_init_thread
 */
func (cec *cecIPC) libcecStartThread() {
	set := cec.sm.SettingGetDir("settings:tv")

	path, caps, ok := cec.findAdapter()
	if !ok {
		cec.ipc.ts.Trace(trace.TRACE_ERROR, "CEC", "No adapters found")
		return
	}
	cec.ipc.ts.Trace(trace.TRACE_DEBUG, "CEC", "Using adapter %s on %s",
		cstr(caps.name[:]), path)

	var la cecLogAddrs
	la.cecVersion = cecVersion14
	la.numLogAddrs = 1
	copy(la.osdName[:], app.AppNameUser) // C: strDeviceName=APPNAMEUSER
	la.primaryDeviceType[0] = cecPrimDevTypeRecord
	la.logAddrType[0] = cecLogAddrTypeRecord
	la.allDeviceTypes[0] = cecPrimDevTypeRecord
	// C: deviceTypes.types[0] = CEC_DEVICE_TYPE_RECORDING_DEVICE
	fd := cec.getFD()
	if err := cecIoctl(fd, iocCecAdapSLogAddrs,
		unsafe.Pointer(&la)); err != nil {
		cec.ipc.ts.Trace(trace.TRACE_ERROR, "CEC",
			"Unable to open connection to %s: %v", path, err)
		cec.mu.Lock()
		unix.Close(cec.fd)
		cec.fd = -1
		cec.mu.Unlock()
		return
	}
	cec.mu.Lock()
	cec.myLogAddr = la.logAddr[0]
	cec.physAddr = cecPhysAddrInvalid
	cec.mu.Unlock()
	var phys uint16
	if cecIoctl(fd, iocCecAdapGPhysAddr, unsafe.Pointer(&phys)) == nil {
		cec.mu.Lock()
		cec.physAddr = phys
		cec.mu.Unlock()
	}

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

	// C: libcec's internal threads; kernel msg/event queues here.
	go cec.rxLoop()
	go cec.evLoop()
}

func cstr(b []uint8) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

/**
 * C: libcec_init
 */
func (i *IPC) libcecStart(em *eventpkg.EventManager, sm *settings.SettingsManager, tasks *task.TaskSystem) {
	i.cec = &cecIPC{ipc: i, em: em, sm: sm, tasks: tasks, fd: -1,
		myLogAddr: 0xff}
	// C: hts_thread_create_detached("cec", libcec_init_thread, ...)
	go i.cec.libcecStartThread()
}

/**
 * C: libcec_fini
 */
func (i *IPC) libcecFini() {
	if i.cec != nil {
		i.cec.mu.Lock()
		if i.cec.fd >= 0 {
			unix.Close(i.cec.fd)
			i.cec.fd = -1
		}
		i.cec.mu.Unlock()
	}
}

/**
 * C: activate_self
 */
func activateSelf(aux any) {
	cec := aux.(*cecIPC)
	cec.transmitActiveSource()
}

// libcecVpi — C: libcec_vpi (libcec.c:387-395, VPI_REGISTER).
func (i *IPC) libcecVpi(op video.VPIOp, info *htsmsg.HTSMsg, p, origin *propcore.Prop) {
	if i.cec == nil || i.cec.getFD() < 0 {
		return
	}

	if op == video.VPIStart {
		i.cec.tasks.Run(activateSelf, i.cec)
	}
}

// initPlatform — libcec builds wire the VPI handler (C: VPI_REGISTER).
func (i *IPC) initPlatform() { i.LibcecVpiHandler = i.libcecVpi }

// LibcecStart — C: INITME(INIT_GROUP_IPC, libcec_init, libcec_fini, 10).
func (i *IPC) LibcecStart(em *eventpkg.EventManager, sm *settings.SettingsManager, tasks *task.TaskSystem) {
	i.libcecStart(em, sm, tasks)
}

// LibcecFini — C: libcec_fini (INIT_GROUP_IPC fini slot).
func (i *IPC) LibcecFini() {
	i.libcecFini()
}
