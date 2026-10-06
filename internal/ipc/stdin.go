//go:build linux || darwin

package ipc

// C: src/ipc/stdin.c — keyboard input via stdin (terminal escape sequences).

import (
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/czz/movian-go/internal/arch"
	eventpkg "github.com/czz/movian-go/internal/event"
)

// C: const static struct { const uint8_t *codes; int action; int fkey; } map[]
var stdinMap = []struct {
	codes  []byte
	action eventpkg.ActionType
	fkey   int
}{
	{[]byte{0x1b, 0x5b, 0x41, 0x00}, eventpkg.ACTION_UP, 0},
	{[]byte{0x1b, 0x5b, 0x42, 0x00}, eventpkg.ACTION_DOWN, 0},
	{[]byte{0x1b, 0x5b, 0x43, 0x00}, eventpkg.ACTION_RIGHT, 0},
	{[]byte{0x1b, 0x5b, 0x44, 0x00}, eventpkg.ACTION_LEFT, 0},
	{[]byte{0x1b, 0x4f, 0x50, 0x00}, 0, 0x1},
	{[]byte{0x1b, 0x4f, 0x51, 0x00}, 0, 0x2},
	{[]byte{0x1b, 0x4f, 0x52, 0x00}, 0, 0x3},
	{[]byte{0x1b, 0x4f, 0x53, 0x00}, 0, 0x4},
	{[]byte{0x1b, 0x5b, 0x31, 0x35, 0x7e, 0x00}, 0, 0x5},
	{[]byte{0x1b, 0x5b, 0x31, 0x37, 0x7e, 0x00}, 0, 0x6},
	{[]byte{0x1b, 0x5b, 0x31, 0x38, 0x7e, 0x00}, 0, 0x7},
	{[]byte{0x1b, 0x5b, 0x31, 0x39, 0x7e, 0x00}, 0, 0x8},
	{[]byte{0x1b, 0x5b, 0x32, 0x30, 0x7e, 0x00}, 0, 0x9},
	{[]byte{0x1b, 0x5b, 0x32, 0x31, 0x7e, 0x00}, 0, 0xa},
	{[]byte{0x1b, 0x5b, 0x32, 0x32, 0x7e, 0x00}, 0, 0xb},
	{[]byte{0x1b, 0x5b, 0x32, 0x33, 0x7e, 0x00}, 0, 0xc},
	{[]byte{0x1b, 0x5b, 0x35, 0x7e, 0x00}, eventpkg.ACTION_PAGE_UP, 0},
	{[]byte{0x1b, 0x5b, 0x36, 0x7e, 0x00}, eventpkg.ACTION_PAGE_DOWN, 0},
	{[]byte{0x1b, 0x4f, 0x48, 0x00}, eventpkg.ACTION_TOP, 0},
	{[]byte{0x1b, 0x4f, 0x46, 0x00}, eventpkg.ACTION_BOTTOM, 0},
}

// stdinIPC — C: stdin eventmanager + saved termios (stdin.c statics).
// Allocated by IPC.StdinStart; owned by the thread + shutdown hook.
type stdinIPC struct {
	ipc    *IPC
	em     *eventpkg.EventManager
	termio *unix.Termios // C: static struct termios
}

// C: static void *stdin_thread(void *aux)
func (s *stdinIPC) stdinThread() {
	var c [1]byte
	var buffer [64]byte
	bufferptr := 0
	escaped := 0

	for {
		var e *eventpkg.Event

		var r int
		if escaped != 0 {
			fds := []unix.PollFd{{Fd: 0, Events: unix.POLLIN}}
			if n, _ := unix.Poll(fds, 100); n == 1 {
				nr, _ := unix.Read(0, c[:])
				r = nr
			} else {
				r = 0
			}
		} else {
			nr, _ := unix.Read(0, c[:])
			r = nr
		}

		if r == 1 {
			if bufferptr == len(buffer)-1 {
				bufferptr = 0
			}
			buffer[bufferptr] = c[0]
			bufferptr++
		}
		escaped = 0

		switch buffer[0] {
		case 8, 0x7f:
			e = &s.em.CreateActionMulti([]eventpkg.ActionType{
				eventpkg.ACTION_BS, eventpkg.ACTION_NAV_BACK}).Event
			bufferptr = 0

		case 10:
			e = &s.em.CreateActionMulti([]eventpkg.ActionType{
				eventpkg.ACTION_ACTIVATE, eventpkg.ACTION_ENTER}).Event
			bufferptr = 0

		case 9:
			e = &s.em.CreateAction(eventpkg.ACTION_FOCUS_NEXT).Event
			bufferptr = 0

		case 0x1b:
			if r == 0 {
				if bufferptr == 1 {
					e = &s.em.CreateAction(eventpkg.ACTION_CANCEL).Event
				}
				bufferptr = 0
			} else {
				escaped = 1
				buffer[bufferptr] = 0

				for _, m := range stdinMap {
					if cstrEqual(m.codes, buffer[:bufferptr+1]) {
						if m.action != 0 {
							e = &s.em.CreateAction(m.action).Event
						} else if m.fkey != 0 {
							e = s.em.FromFkey(uint(m.fkey), 0)
						}
						break
					}
				}
			}

		default:
			if buffer[0] >= 32 && buffer[0] <= 0x7e {
				// C: case 32 ... 0x7e — printable ASCII → EVENT_UNICODE
				bufferptr = 0
				e = &s.em.CreateInt(eventpkg.EVENT_UNICODE, int(buffer[0])).Event
			} else {
				bufferptr = 0
			}
		}

		if e == nil {
			continue
		}
		e.Flags |= eventpkg.EventKeypress
		s.em.ToUI(e)
	}
}

// cstrEqual compares NUL-terminated C strings (C: strcmp).
func cstrEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// C: static void stdin_shutdown_early(void *opaque, int exitcode)
func stdinShutdownEarly(opaque any, exitCode int) {
	s := opaque.(*stdinIPC)
	if s.termio != nil {
		unix.IoctlSetTermios(0, stdinTermSet, s.termio)
	}
}

// StdinStart — C: static void stdin_start(void), INITME(INIT_GROUP_IPC).
func (i *IPC) StdinStart(em *eventpkg.EventManager, listenOnStdin bool) {
	if !listenOnStdin {
		return
	}

	s := &stdinIPC{ipc: i, em: em}

	// C: if(!isatty(0)) return;
	tio, err := unix.IoctlGetTermios(0, stdinTermGet)
	if err != nil {
		return
	}
	s.termio = tio

	// C: termio2.c_lflag &= ~(ECHO | ICANON); tcsetattr(0, TCSANOW, &termio2)
	tio2 := *tio
	tio2.Lflag &^= unix.ECHO | unix.ICANON
	if err := unix.IoctlSetTermios(0, stdinTermSet, &tio2); err != nil {
		return
	}

	arch.ShutdownHookAdd(stdinShutdownEarly, s, 0)

	// C: hts_thread_create_detached("stdin", stdin_thread, NULL, ...)
	go s.stdinThread()
}

// ensure unsafe import is referenced (C parity for raw byte buffers)
var _ = unsafe.Sizeof(byte(0))
