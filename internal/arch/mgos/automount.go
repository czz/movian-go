//go:build linux && mgos

package mgos

// automount.go — canonical port of src/arch/stos/stos_automount.c.
//
// The OS (blkid helper) writes one udev-style info file per block device
// into FsinfoDir, named by the filesystem UUID. We watch the directory
// with inotify: IN_CLOSE_WRITE → parse + mount, IN_DELETE → unmount.
// Mounted filesystems appear in the navigator as "usb" services.

import (
	"errors"
	"github.com/czz/movian-go/internal/misc"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/czz/movian-go/internal/service"
	"github.com/czz/movian-go/internal/trace"
	"golang.org/x/sys/unix"
)

// C: typedef struct fsinfo { LIST_ENTRY ... } fsinfo_t (stos_automount.c:41)
type fsinfo struct {
	uuid       string // C: fi_uuid
	devname    string // C: fi_devname
	fstype     string // C: fi_type ("" = NULL)
	label      string // C: fi_label
	mountpoint string // C: fi_mountpoint
	status     int    // C: fi_status
	svc        *service.Service
}

// C: FI_STATUS_* (stos_automount.c:55-57)
const (
	fiStatusUnmounted = 0
	fiStatusMounted   = 1
	fiStatusMountFail = 2
)

// C: static struct fsinfo_list fsinfos (stos_automount.c:59)
var fsinfos []*fsinfo

// mgosTS — C: trace() global reached by automount/mgos file statics.
// Injected by the composition root via SetTraceSystem.
var mgosTS struct{ ts *trace.TraceSystem }

// SetTraceSystem injects the trace system (C: trace() global).
func SetTraceSystem(ts *trace.TraceSystem) { mgosTS.ts = ts }

// cleanup — C: cleanup() (stos_automount.c:62-77). blkid quotes values
// containing spaces as 'foo bar'; strip the quotes.
func cleanup(s string) string {
	if !strings.HasPrefix(s, "'") {
		return s
	}
	r := s[1:]
	if i := strings.IndexByte(r, '\''); i >= 0 {
		r = r[:i]
	}
	return r
}

// tryMountFs — C: try_mount_fs (stos_automount.c:83-132).
func tryMountFs(fi *fsinfo) int {
	os.Mkdir(MediaDir, 0755)

	mpoint := filepath.Join(MediaDir, fi.label)
	dupcnt := 1

	for {
		err := os.Mkdir(mpoint, 0755)
		if err == nil {
			break
		}
		if os.IsExist(err) {
			dupcnt++
			mpoint = filepath.Join(MediaDir, fi.label+itoa(dupcnt))
			continue
		}
		mgosTS.ts.Trace(trace.TRACE_ERROR, "Automount",
			"Failed to create mountpoint %s -- %s", mpoint, err)
		fi.status = fiStatusMountFail
		return -1
	}

	// C: MS_NOSUID | MS_NODEV | MS_NOATIME | MS_SYNCHRONOUS
	mountflags := uintptr(unix.MS_NOSUID | unix.MS_NODEV |
		unix.MS_NOATIME | unix.MS_SYNCHRONOUS)

	if err := unix.Mount(fi.devname, mpoint, fi.fstype, mountflags, ""); err != nil {
		if errors.Is(err, syscall.EBUSY) {
			// Assume it's already mounted OK
			mgosTS.ts.Trace(trace.TRACE_DEBUG, "Automount",
				"%s (%s) already mounted", fi.devname, mpoint)
		} else {
			mgosTS.ts.Trace(trace.TRACE_ERROR, "Automount",
				"Failed to mount %s at %s -- %s", fi.devname, mpoint, err)
			fi.status = fiStatusMountFail
			return -1
		}
	} else {
		mgosTS.ts.Trace(trace.TRACE_DEBUG, "Automount",
			"Mounted %s at %s", fi.devname, mpoint)
	}

	fi.mountpoint = mpoint
	fi.status = fiStatusMounted
	return 0
}

// itoa — C: snprintf("%d", dupcnt)
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [12]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// tryMount — C: try_mount (stos_automount.c:139-162).
func tryMount() {
	for _, fi := range fsinfos {
		if fi.status != fiStatusUnmounted {
			continue
		}
		if tryMountFs(fi) != 0 {
			continue
		}
		url := "file://" + fi.mountpoint
		// C: service_create_managed(uuid, label, url, "usb", NULL,
		//                          0, 1, SVC_ORIGIN_MEDIA)
		fi.svc = mgosDeps.ss.ServiceCreateManaged(
			fi.uuid, fi.label, url, "usb", "",
			false, true, service.SvcOriginMedia)
	}
}

// parseInfofile — C: parse_infofile (stos_automount.c:168-226).
// Parses DEVNAME=/ID_FS_UUID=/ID_FS_TYPE=/ID_FS_LABEL= lines; registers a
// new fsinfo when uuid+devname are present and the uuid is unknown.
// Returns nil if the fs already exists (C frees the strings and returns
// NULL) or fields are missing.
func parseInfofile(buf string) *fsinfo {
	var uuid, devname, fstype, label string
	uuidSet, devnameSet, labelSet := false, false, false

	// C: LINEPARSE(s, buf) — iterate lines; each match strdup's the
	//    value, so "key absent" (NULL) differs from "key empty" ("").
	for _, line := range strings.Split(buf, "\n") {
		if v, ok := strings.CutPrefix(line, "DEVNAME="); ok {
			devname, devnameSet = cleanup(v), true
		} else if v, ok := strings.CutPrefix(line, "ID_FS_UUID="); ok {
			uuid, uuidSet = cleanup(v), true
		} else if v, ok := strings.CutPrefix(line, "ID_FS_TYPE="); ok {
			fstype = cleanup(v)
		} else if v, ok := strings.CutPrefix(line, "ID_FS_LABEL="); ok {
			label, labelSet = cleanup(v), true
		}
	}

	// C: if(uuid != NULL && devname != NULL)
	if !uuidSet || !devnameSet {
		return nil
	}

	for _, fi := range fsinfos {
		if fi.uuid == uuid {
			return nil
		}
	}

	// C: if(label == NULL) label = strdup("USB Drive")
	if !labelSet {
		label = "USB Drive"
	}

	fi := &fsinfo{
		uuid:    uuid,
		devname: devname,
		fstype:  fstype,
		label:   label,
	}
	// C: LIST_INSERT_HEAD(&fsinfos, fi, fi_link)
	fsinfos = slices.Insert(fsinfos, 0, fi)

	lbl := fi.label
	if lbl == "" {
		lbl = "<noname>"
	}
	typ := fi.fstype
	if typ == "" {
		typ = "<unknown type>"
	}
	mgosTS.ts.Trace(trace.TRACE_DEBUG, "Automount",
		"Added filesystem %s at %s (%s) [%s]", lbl, devname, typ, uuid)

	return fi
}

// addFs — C: add_fs (stos_automount.c:232-273).
func addFs(path, infofile string) {
	fullname := filepath.Join(path, infofile)
	data, err := os.ReadFile(fullname)
	if err != nil {
		mgosTS.ts.Trace(trace.TRACE_ERROR, "Automount",
			"Unable to open %s -- %s", infofile, err)
		return
	}
	parseInfofile(string(data))
}

// removeFs — C: remove_fs (stos_automount.c:278-311).
func removeFs(uuid string) {
	var fi *fsinfo
	idx := -1
	for i, f := range fsinfos {
		if f.uuid == uuid {
			fi = f
			idx = i
			break
		}
	}
	if fi == nil {
		return
	}

	if fi.status == fiStatusMounted {
		mgosDeps.ss.ServiceDestroy(fi.svc)
		err := unix.Unmount(fi.mountpoint, unix.MNT_DETACH)
		errstr := "OK"
		if err != nil {
			errstr = err.Error()
		}
		mgosTS.ts.Trace(trace.TRACE_DEBUG, "Automount",
			"Unmounted %s -- %s", fi.mountpoint, errstr)
		os.Remove(fi.mountpoint)
	}

	lbl := fi.label
	if lbl == "" {
		lbl = "<noname>"
	}
	typ := fi.fstype
	if typ == "" {
		typ = "<unknown type>"
	}
	mgosTS.ts.Trace(trace.TRACE_DEBUG, "Automount",
		"Removed filesystem %s at %s (%s) [%s]",
		lbl, fi.devname, typ, fi.uuid)

	fsinfos = slices.Delete(fsinfos, idx, idx+1)
}

// scanCurrent — C: scan_current (stos_automount.c:317-336).
func scanCurrent(path string) {
	namelist, err := os.ReadDir(path)
	if err != nil {
		mgosTS.ts.Trace(trace.TRACE_ERROR, "Automount",
			"Unable to scan %s -- %s", path, err)
		return
	}
	for _, e := range namelist {
		if e.Name()[0] != '.' {
			addFs(path, e.Name())
		}
	}
}

// unmountAll — C: unmount_all (stos_automount.c:342-371).
func unmountAll(path string) {
	namelist, err := os.ReadDir(path)
	if err != nil {
		mgosTS.ts.Trace(trace.TRACE_ERROR, "Automount",
			"Unable to scan %s -- %s", path, err)
		return
	}
	for _, e := range namelist {
		if e.Name()[0] == '.' {
			continue
		}
		newpath := filepath.Join(path, e.Name())
		mgosTS.ts.Trace(trace.TRACE_DEBUG, "Automount", "Unmounting %s", newpath)

		if err := unix.Unmount(newpath, unix.MNT_DETACH); err != nil {
			mgosTS.ts.Trace(trace.TRACE_ERROR, "Automount",
				"Unable to unmount %s -- %s", newpath, err)
		}
		if err := syscall.Rmdir(newpath); err != nil {
			mgosTS.ts.Trace(trace.TRACE_ERROR, "Automount",
				"Unable to remove %s -- %s", newpath, err)
		}
	}
}

// C: static hts_thread_t automount_tid / automount_pipe[2].
// Pre-set to -1 so stop is a clean no-op if start never ran (in C the
// zero-init would close fd 0 — unreachable in practice because fini
// only fires after init).
var automountPipe = [2]int{-1, -1}
var automountDone chan struct{}

const inotifyEventSize = 16 // sizeof(struct inotify_event)

// automountThread — C: automount_thread (stos_automount.c:380-450).
func automountThread() {
	defer close(automountDone)

	os.Mkdir(FsinfoDir, 0755)
	os.Mkdir(MediaDir, 0755)

	unmountAll(MediaDir)

	fd, err := unix.InotifyInit()
	if err != nil {
		return
	}
	defer unix.Close(fd)

	if _, err := unix.InotifyAddWatch(fd, FsinfoDir,
		unix.IN_ONLYDIR|unix.IN_CLOSE_WRITE|unix.IN_DELETE); err != nil {
		mgosTS.ts.Trace(trace.TRACE_DEBUG, "Automount",
			"Unable to watch %s -- %s", FsinfoDir, err)
		return
	}

	fds := []unix.PollFd{
		{Fd: int32(fd), Events: unix.POLLIN},
		{Fd: int32(automountPipe[0]), Events: unix.POLLERR},
	}

	scanCurrent(FsinfoDir)

	buf := make([]byte, 1024)
	for {
		tryMount()
		if _, err := unix.Poll(fds, -1); err != nil {
			break
		}
		if fds[1].Revents&(unix.POLLERR|unix.POLLHUP) != 0 {
			break
		}
		if fds[0].Revents&(unix.POLLERR|unix.POLLHUP) != 0 {
			break
		}

		n, err := unix.Read(fd, buf)
		if err != nil {
			break
		}
		off := 0
		for n > inotifyEventSize {
			mask := uint32(buf[off+4]) | uint32(buf[off+5])<<8 |
				uint32(buf[off+6])<<16 | uint32(buf[off+7])<<24
			elen := int(uint32(buf[off+12]) | uint32(buf[off+13])<<8 |
				uint32(buf[off+14])<<16 | uint32(buf[off+15])<<24)

			if elen == 0 {
				break
			}

			mgosTS.ts.Trace(trace.TRACE_DEBUG, "Automount", "Got notification")

			name := misc.CStr(buf[off+inotifyEventSize : off+inotifyEventSize+elen])

			if mask&unix.IN_DELETE != 0 {
				removeFs(name)
			}
			if mask&unix.IN_CLOSE_WRITE != 0 {
				addFs(FsinfoDir, name)
			}

			off += inotifyEventSize + elen
			n -= inotifyEventSize + elen
		}
	}
	mgosTS.ts.Trace(trace.TRACE_DEBUG, "Automount", "Stopping automounter")
	unix.Close(automountPipe[0])
}

// automountStart — C: stos_automount_start (stos_automount.c:453-464).
func automountStart() {
	if err := unix.Pipe(automountPipe[:]); err != nil {
		automountPipe[1] = -1
		return
	}
	automountDone = make(chan struct{})
	// C: hts_thread_create_joinable("automounter", ...)
	go automountThread()
}

// automountStop — C: stos_automount_stop (stos_automount.c:467-476).
func automountStop() {
	if automountPipe[1] == -1 {
		return
	}
	unix.Close(automountPipe[1])
	<-automountDone // C: hts_thread_join(&automount_tid)
	unmountAll(MediaDir)
}
