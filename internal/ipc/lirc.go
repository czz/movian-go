//go:build linux || darwin

package ipc

// C: src/ipc/lirc.c — LIRC remote control daemon socket input.

import (
	"fmt"
	"strings"

	"golang.org/x/sys/unix"

	eventpkg "github.com/czz/movian-go/internal/event"
	"github.com/czz/movian-go/internal/misc"
)

// lircIPC — C: static int lirc_fd + implicit eventmanager (lirc.c
// statics). Allocated by IPC.LircOpen; owned by the lirc thread.
type lircIPC struct {
	ipc *IPC
	fd  int // C: static int lirc_fd
	em  *eventpkg.EventManager
}

// C: static const struct { const char *name; uint16_t action1;
//
//	uint16_t action2; } lircmap[]
var lircmap = []struct {
	name    string
	action1 eventpkg.ActionType
	action2 eventpkg.ActionType
}{
	{"Up", eventpkg.ACTION_UP, 0},
	{"Down", eventpkg.ACTION_DOWN, 0},
	{"Left", eventpkg.ACTION_LEFT, 0},
	{"Right", eventpkg.ACTION_RIGHT, 0},
	{"Enter", eventpkg.ACTION_ACTIVATE, eventpkg.ACTION_ENTER},
	{"Back", eventpkg.ACTION_BS, eventpkg.ACTION_NAV_BACK},
	{"Backspace", eventpkg.ACTION_BS, eventpkg.ACTION_NAV_BACK},
	{"Tab", eventpkg.ACTION_FOCUS_NEXT, 0},
	{"ShiftTab", eventpkg.ACTION_FOCUS_PREV, 0},
}

// C: static void *lirc_thread(void *aux)
func (li *lircIPC) lircThread() {
	var buf [200]byte
	fd := li.fd

	var q misc.HtsbufQueue
	q.HtsbufQueueSetup(0)

	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}

	for {
		r, _ := unix.Poll(fds, -1)
		if r > 0 {
			nr, err := unix.Read(fd, buf[:])
			if nr < 1 || err != nil {
				li.ipc.ts.Error("lircd", "Read error: %v", err)
				break
			}
			q.Append(buf[:nr])
		}

		for {
			l := q.Find(0xa)
			if l == -1 {
				break
			}

			if l >= len(buf)-1 {
				li.ipc.ts.Error("lircd", "Command buffer size exceeded")
				goto out
			}

			q.Read(buf[:], l)
			buf[l] = 0

			for l > 0 && buf[l-1] < 32 {
				l--
				buf[l] = 0
			}
			q.Drop(1) // Drop the \n

			line := string(buf[:l])

			// C: sscanf(buf, "%"PRIx64" %x %s", &ircode, &repeat, keyname)
			var ircode uint64
			var repeat uint32
			var keyname string
			n, _ := fmt.Sscanf(line, "%x %x %s", &ircode, &repeat, &keyname)
			if n != 3 {
				li.ipc.ts.Error("lircd", "Invalid LIRC input: %q", line)
				continue
			}

			var e *eventpkg.Event
			if len(keyname) == 1 {
				// ASCII input — C: keyname[0] && keyname[1] == 0
				e = &li.em.CreateInt(eventpkg.EVENT_UNICODE, int(keyname[0])).Event
			} else {
				for _, m := range lircmap {
					if strings.EqualFold(keyname, m.name) {
						av := []eventpkg.ActionType{m.action1, m.action2}
						if av[1] != 0 {
							e = &li.em.CreateActionMulti(av[:2]).Event
						} else {
							e = &li.em.CreateActionMulti(av[:1]).Event
						}
						break
					}
				}
			}
			if e == nil {
				e = &li.em.CreateStr(eventpkg.EVENT_KEYDESC, "IR+"+keyname).Event
			}
			e.Flags |= eventpkg.EventKeypress
			li.em.ToUI(e)
		}
	}
out:
	unix.Close(fd)
	q.Flush()
}

// C: static int lirc_open_socket(const char *path)
func lircOpenSocket(path string) int {
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return -1
	}

	sun := &unix.SockaddrUnix{Name: path}
	if err := unix.Connect(fd, sun); err != nil {
		unix.Close(fd)
		return -1
	}
	return fd
}

// LircOpen — C: static void lirc_open(void), INITME(INIT_GROUP_IPC).
func (i *IPC) LircOpen(em *eventpkg.EventManager) {
	fd := lircOpenSocket("/var/run/lirc/lircd")
	if fd == -1 {
		fd = lircOpenSocket("/dev/lircd") // Old path
	}
	if fd == -1 {
		return
	}

	li := &lircIPC{ipc: i, fd: fd, em: em}
	// C: hts_thread_create_detached("lirc", lirc_thread, NULL, ...)
	go li.lircThread()
}
