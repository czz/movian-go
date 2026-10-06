//go:build linux && !android

package ipc

// C: src/ipc/devevent.c — Linux /dev/input/event* (evdev) input devices.

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"

	eventpkg "github.com/czz/movian-go/internal/event"
)

// C: typedef struct de_dev
type deDev struct {
	present bool // C: dd_present
	fd      int  // C: dd_fd
	qual    int  // C: dd_qual
	typ     int  // C: dd_type
}

const (
	qualLeftShift  = 0x1
	qualRightShift = 0x2
	qualLeftAlt    = 0x4
	qualRightAlt   = 0x8
)

const (
	ddTypeUnknown      = iota // C: DD_TYPE_UNKNOWN
	ddTypeFullKeyboard        // C: DD_TYPE_FULL_KEYBOARD
	ddTypeDpad                // C: DD_TYPE_DPAD
)

const deMaxdevs = 16 // C: DE_MAXDEVS

// C: <linux/input.h> key codes (input-event-codes.h) — x/sys/unix does not
// export them; values are the stable kernel input ABI.
const (
	keySpace        = 57
	keyEnter        = 28
	keyPageup       = 104
	keyPagedown     = 109
	keyBackspace    = 14
	keyEsc          = 1
	keyF1           = 59
	keyF10          = 68
	keyLeftshift    = 42
	keyRightshift   = 54
	keyLeftalt      = 56
	keyRightalt     = 100
	keyUp           = 103
	keyDown         = 108
	keyRight        = 106
	keyLeft         = 105
	keyTab          = 15
	keyMute         = 113
	keyVolumeup     = 115
	keyVolumedown   = 114
	keyHomepage     = 172
	keyPlaypause    = 164
	keyPrevioussong = 165
	keyNextsong     = 163
	keyStopcd       = 166
	keyRecord       = 167
	keySleep        = 142
	keyBack         = 158
	keyCompose      = 127
	keyMedia        = 226
	keyMenu         = 139
	keyProg1        = 148
	keyProg2        = 149
	keyProg3        = 202

	btnRight         = 0x111
	btnLeft          = 0x110
	btnTop2          = 0x113
	btnBase          = 0x126
	btnPinkie        = 0x115
	btnBase2         = 0x117
	btnTop           = 0x112
	btnBase5         = 0x12a
	btnBase6         = 0x12b
	btnDead          = 0x12f
	btnThumb         = 0x11e
	btnThumb2        = 0x11f
	btnBase4         = 0x119
	btnBase3         = 0x118
	btnTriggerHappy1 = 0x2c0
	btnTrigger       = 0x120

	relWheel = 0x08 // C: REL_WHEEL
)

// C: struct input_event (linux/input.h) — 24 bytes on 64-bit Linux
// (timeval=16 + type=2 + code=2 + value=4).
type inputEvent struct {
	Time  unix.Timeval
	Type  uint16
	Code  uint16
	Value int32
}

// C: typedef struct devevent — includes the C static eventmanager
// (devevent.c devevent_state) so the thread and readers are fully
// parametric.
type devevent struct {
	lastScan int64                  // C: de_last_scan
	devs     [deMaxdevs]deDev       // C: de_devs
	fds      [deMaxdevs]unix.PollFd // C: de_fds
	nfds     int                    // C: de_nfds
	ipc      *IPC                   // injected deps (trace)
	em       *eventpkg.EventManager // C: static eventmanager
}

// eviocgName computes EVIOCGNAME(len): _IOC(_IOC_READ, 'E', 0x06, len)
func eviocgName(length int) uint {
	return (2 << 30) | (uint(length)&0x1FFF)<<16 | ('E' << 8) | 0x06
}

// C: static void update_fds(devevent_t *de)
func (de *devevent) updateFds() {
	j := 0
	for i := range deMaxdevs {
		dd := &de.devs[i]
		if !dd.present {
			continue
		}
		de.fds[j].Events = unix.POLLIN | unix.POLLERR
		de.fds[j].Fd = int32(dd.fd)
		j++
	}
	de.nfds = j
}

// C: static void rescan(devevent_t *de)
func (de *devevent) rescan() {
	for i := range deMaxdevs {
		dd := &de.devs[i]
		path := fmt.Sprintf("/dev/input/event%d", i)

		if dd.present {
			if unix.Access(path, unix.R_OK|unix.W_OK) == nil {
				continue
			}
			de.ipc.ts.Info("DE", "Device %s disconnected", path)
			unix.Close(dd.fd)
			dd.present = false
		} else {
			fd, err := unix.Open(path, unix.O_RDWR, 0)
			if err != nil {
				continue
			}
			dd.fd = fd

			name := "???"
			var namebuf [80]byte
			if _, _, errno := unix.Syscall(unix.SYS_IOCTL,
				uintptr(fd), uintptr(eviocgName(len(namebuf)-1)),
				uintptr(unsafe.Pointer(&namebuf[0]))); errno == 0 {
				for j, b := range namebuf {
					if b == 0 {
						name = string(namebuf[:j])
						break
					}
				}
			}

			de.ipc.ts.Debug("DE", "Found %s on %s", name, path)
			dd.present = true
			dd.typ = ddTypeUnknown
		}
	}
	de.updateFds()
}

// C: static de_dev_t *dd_from_fd(devevent_t *de, int fd)
func (de *devevent) ddFromFd(fd int) *deDev {
	for i := range deMaxdevs {
		if de.devs[i].present && de.devs[i].fd == fd {
			return &de.devs[i]
		}
	}
	return nil
}

// C: static const wchar_t keymap[128]
var keymap = []rune(
	"  12345678" +
		"90-=  qwer" +
		"tyuiopå   " +
		"asdfghjklö" +
		"ä   zxcvbn" +
		"m,.-")

// C: static const wchar_t keymap_shift[128]
var keymapShift = []rune(
	"  !\"#¤%&/(" +
		")=?   QWER" +
		"TYUIOPÅ   " +
		"ASDFGHJKLÖ" +
		"Ä   ZXCVBN" +
		"M;:_")

// C: static int key_to_action[][3]
var keyToAction = [][3]eventpkg.ActionType{
	{keyUp, eventpkg.ACTION_UP, eventpkg.ACTION_MOVE_UP},
	{keyDown, eventpkg.ACTION_DOWN, eventpkg.ACTION_MOVE_DOWN},
	{keyRight, eventpkg.ACTION_RIGHT, eventpkg.ACTION_MOVE_RIGHT},
	{keyLeft, eventpkg.ACTION_LEFT, eventpkg.ACTION_MOVE_LEFT},
	{keyTab, eventpkg.ACTION_FOCUS_NEXT, eventpkg.ACTION_FOCUS_PREV},
	{keyMute, eventpkg.ACTION_VOLUME_MUTE_TOGGLE, 0},
	{keyVolumeup, eventpkg.ACTION_VOLUME_UP, 0},
	{keyVolumedown, eventpkg.ACTION_VOLUME_DOWN, 0},
	{keyHomepage, eventpkg.ACTION_HOME, 0},
	{keyPlaypause, eventpkg.ACTION_PLAYPAUSE, 0},
	{keyPrevioussong, eventpkg.ACTION_SKIP_BACKWARD, 0},
	{keyNextsong, eventpkg.ACTION_SKIP_FORWARD, 0},
	{keyStopcd, eventpkg.ACTION_STOP, 0},
	{keyRecord, eventpkg.ACTION_RECORD, 0},
	{keySleep, eventpkg.ACTION_STANDBY, 0},
	{keyBack, eventpkg.ACTION_NAV_BACK, 0},
	{btnRight, eventpkg.ACTION_NAV_BACK, 0},
	{btnLeft, eventpkg.ACTION_ITEMMENU, 0},
	{keyCompose, eventpkg.ACTION_MENU, 0},

	// These should be configurable
	{keyMedia, eventpkg.ACTION_MENU, 0},
	{keyMenu, eventpkg.ACTION_ITEMMENU, 0},
	{keyProg1, eventpkg.ACTION_LOGWINDOW, 0},
	{keyProg2, eventpkg.ACTION_SHOW_MEDIA_STATS, 0},
	{keyProg3, eventpkg.ACTION_SYSINFO, 0},

	// Ps3 controller
	{btnTop2, eventpkg.ACTION_UP, eventpkg.ACTION_MOVE_UP},
	{btnBase, eventpkg.ACTION_DOWN, eventpkg.ACTION_MOVE_DOWN},
	{btnPinkie, eventpkg.ACTION_RIGHT, eventpkg.ACTION_MOVE_RIGHT},
	{btnBase2, eventpkg.ACTION_LEFT, eventpkg.ACTION_MOVE_LEFT},
	{btnTop, eventpkg.ACTION_RECORD, 0},
	{btnBase5, eventpkg.ACTION_SKIP_BACKWARD, 0},
	{btnBase6, eventpkg.ACTION_SKIP_FORWARD, 0},
	{btnDead, eventpkg.ACTION_ITEMMENU, 0},
	{btnThumb, eventpkg.ACTION_SYSINFO, 0},
	{btnThumb2, eventpkg.ACTION_SHOW_MEDIA_STATS, 0},
	{btnBase4, eventpkg.ACTION_VOLUME_UP, 0},
	{btnBase3, eventpkg.ACTION_VOLUME_DOWN, 0},
	{btnTriggerHappy1, eventpkg.ACTION_HOME, 0},
	{btnTrigger, eventpkg.ACTION_STOP, 0},
	{300, eventpkg.ACTION_MENU, 0},
	{302, eventpkg.ACTION_ACTIVATE, 0},
}

// C: static void doqual(de_dev_t *dd, const struct input_event *ie, int code, int qual)
func doqual(dd *deDev, ie *inputEvent, code uint16, qual int) {
	if ie.Code == code {
		if ie.Value != 0 {
			dd.qual |= qual
		} else {
			dd.qual &^= qual
		}
	}
}

// C: static int dd_read(de_dev_t *dd)
func (de *devevent) ddRead(dd *deDev) int {
	var ie inputEvent

	buf := make([]byte, int(unsafe.Sizeof(ie)))
	n, err := unix.Read(dd.fd, buf)
	if err != nil || n != len(buf) {
		return 1
	}
	ie = *(*inputEvent)(unsafe.Pointer(&buf[0]))

	if ie.Type == unix.EV_REL {
		if ie.Code == relWheel {
			action := eventpkg.ACTION_UP
			if ie.Value < 0 {
				action = eventpkg.ACTION_DOWN
			}
			cnt := int(ie.Value)
			if cnt < 0 {
				cnt = -cnt
			}
			if cnt > 4 {
				cnt = 4
			}
			for ; cnt > 0; cnt-- {
				e := &de.em.CreateAction(eventpkg.ActionType(action)).Event
				e.Flags |= eventpkg.EventKeypress
				de.em.ToUI(e)
			}
		}
		return 0
	}

	if ie.Type != unix.EV_KEY {
		return 0
	}

	doqual(dd, &ie, keyLeftshift, qualLeftShift)
	doqual(dd, &ie, keyRightshift, qualRightShift)

	doqual(dd, &ie, keyLeftalt, qualLeftAlt)
	doqual(dd, &ie, keyRightalt, qualRightAlt)

	if ie.Value == 0 {
		return 0 // release
	}

	alt := dd.qual&(qualLeftAlt|qualRightAlt) != 0
	shift := 0
	if dd.qual&(qualLeftShift|qualRightShift) != 0 {
		shift = 1
	}

	var e *eventpkg.Event

	for i := 0; keyToAction[i][0] != 0; i++ {
		if eventpkg.ActionType(ie.Code) == keyToAction[i][0] {
			ev := &de.em.CreateAction(keyToAction[i][1+shift]).Event
			ev.Flags |= eventpkg.EventKeypress
			de.em.ToUI(ev)
			return 0
		}
	}

	switch ie.Code {
	case keySpace:
		e = &de.em.CreateInt(eventpkg.EVENT_UNICODE, 32).Event

	case keyEnter:
		if dd.typ == ddTypeFullKeyboard {
			e = &de.em.CreateActionMulti([]eventpkg.ActionType{
				eventpkg.ACTION_ACTIVATE, eventpkg.ACTION_ENTER}).Event
		} else {
			e = &de.em.CreateActionMulti([]eventpkg.ActionType{
				eventpkg.ACTION_ACTIVATE}).Event
		}

	case keyPageup:
		e = &de.em.CreateActionMulti([]eventpkg.ActionType{
			eventpkg.ACTION_PAGE_UP, eventpkg.ACTION_PREV_CHANNEL,
			eventpkg.ACTION_SKIP_BACKWARD}).Event

	case keyPagedown:
		e = &de.em.CreateActionMulti([]eventpkg.ActionType{
			eventpkg.ACTION_PAGE_DOWN, eventpkg.ACTION_NEXT_CHANNEL,
			eventpkg.ACTION_SKIP_FORWARD}).Event

	case keyBackspace:
		e = &de.em.CreateActionMulti([]eventpkg.ActionType{
			eventpkg.ACTION_BS, eventpkg.ACTION_NAV_BACK}).Event

	case keyEsc:
		e = &de.em.CreateActionMulti([]eventpkg.ActionType{
			eventpkg.ACTION_CANCEL, eventpkg.ACTION_NAV_BACK}).Event

	case 301: // PS3 controller
		e = &de.em.CreateActionMulti([]eventpkg.ActionType{
			eventpkg.ACTION_BS, eventpkg.ACTION_NAV_BACK}).Event

	default:
		if ie.Code >= keyF1 && ie.Code <= keyF10 {
			// C: case KEY_F1 ... KEY_F10
			e = de.em.FromFkey(uint(1+ie.Code-keyF1), uint(shift))
			break
		}

		if ie.Code < 128 {
			if dd.typ == ddTypeUnknown {
				dd.typ = ddTypeFullKeyboard
			}

			if alt {
				switch keymap[ie.Code] {
				case 'l':
					e = &de.em.CreateAction(eventpkg.ACTION_LOGWINDOW).Event
				case 'm':
					e = &de.em.CreateAction(eventpkg.ACTION_SHOW_MEDIA_STATS).Event
				case 's':
					e = &de.em.CreateAction(eventpkg.ACTION_SYSINFO).Event
				}
			} else {
				var uc rune
				if shift != 0 {
					uc = keymapShift[ie.Code]
				} else {
					uc = keymap[ie.Code]
				}
				if uc > ' ' {
					e = &de.em.CreateInt(eventpkg.EVENT_UNICODE, int(uc)).Event
				}
			}
		}
		if e == nil {
			de.ipc.ts.Debug("DE", "Unmapped key %d (0x%x)", ie.Code, ie.Code)
		}
	}

	if e != nil {
		e.Flags |= eventpkg.EventKeypress
		de.em.ToUI(e)
	}

	return 0
}

// C: static void dd_stop(devevent_t *de, de_dev_t *dd)
func (de *devevent) ddStop(dd *deDev) {
	unix.Close(dd.fd)
	dd.present = false
	de.updateFds()
}

// C: static void *devevent_thread(void *aux)
func (de *devevent) deveventThread() {
	de.rescan()

	for {
		n, _ := unix.Poll(de.fds[:de.nfds], 1000)
		if n == 0 {
			de.rescan()
			continue
		}

		for i := 0; i < de.nfds && n > 0; i++ {
			if de.fds[i].Revents == 0 {
				continue
			}

			dd := de.ddFromFd(int(de.fds[i].Fd))
			if dd == nil {
				continue
			}

			if de.fds[i].Revents&unix.POLLIN != 0 {
				if de.ddRead(dd) != 0 {
					de.ddStop(dd)
				}
			}

			if de.fds[i].Revents&unix.POLLERR != 0 {
				de.ddStop(dd)
			}
		}
	}
}

// DeveventStart — C: static void devevent_start(void), INITME(INIT_GROUP_IPC).
func (i *IPC) DeveventStart(em *eventpkg.EventManager) {
	de := &devevent{ipc: i, em: em}
	// C: hts_thread_create_detached("devevent", devevent_thread, NULL, ...)
	go de.deveventThread()
}
