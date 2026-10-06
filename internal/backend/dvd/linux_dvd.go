//go:build !windows

/*
 *  Copyright (C) 2007-2015 Lonelycoder AB
 *  Canonical 1:1 Go port of src/backend/dvd/linux_dvd.c
 */

package dvd

import (
	"errors"
	"strings"

	backendcore "github.com/czz/movian-go/internal/backend/core"
	"github.com/czz/movian-go/internal/backend/dvd/dvdlib"
	"github.com/czz/movian-go/internal/callout"
	"github.com/czz/movian-go/internal/event"
	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/notifications"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/service"
	"github.com/czz/movian-go/internal/trace"
	"github.com/czz/movian-go/internal/usage"
	"golang.org/x/sys/unix"
)

// C: <linux/cdrom.h>
const (
	cdromEject       = 0x5309
	cdromDriveStatus = 0x5326
	cdromDiscStatus  = 0x5327

	cdsNoInfo        = 0
	cdsNoDisc        = 1
	cdsTrayOpen      = 2
	cdsDriveNotReady = 3
	cdsDiscOK        = 4
	cdsAudio         = 100
	cdsData1         = 101
	cdsMixed         = 105
)

// discStatus — C: typedef enum disc_status
type discStatus int

const (
	discNoDrive discStatus = iota
	discNoDisc
	discIsofs
	discAudio
	discUnknownType
)

// discScanner — C: typedef struct disc_scanner
type discScanner struct {
	timer  callout.Callout // C: ds_timer
	status discStatus      // C: ds_status
	title  [40]byte        // C: ds_title

	dev string // C: ds_dev

	svc *service.Service // C: ds_svc

	cs *callout.CalloutSystem // C: callout_* globals — injected
}

// C: static backend_t *backends context for service_create and
// backend_open_video — bound at RegisterDVDBackend.
var discBackendSystem *backendcore.BackendSystem

// setStatus — C: set_status
func setStatus(ds *discScanner, status discStatus, title string) {
	var buf string
	var url string

	if ds.status == status {
		return
	}

	ds.status = status

	if ds.svc != nil {
		discBackendSystem.GetServiceSystem().ServiceDestroy(ds.svc)
		ds.svc = nil
	}

	ss := discBackendSystem.GetServiceSystem()
	if ss == nil {
		return
	}

	switch status {
	case discNoDrive, discNoDisc:

	case discAudio:
		buf = "Audio CD"
		url = "audiocd:" + ds.dev
		ds.svc = ss.ServiceCreate(url, buf, url, "music", "", false,
			true, service.SvcOriginMedia)

	case discIsofs:
		buf = "DVD: " + title
		url = "dvd:" + ds.dev

		ds.svc = ss.ServiceCreate(url, buf, url, "video", "", false,
			true, service.SvcOriginMedia)

	case discUnknownType:
		// #if 0 /* FIXME: Must not pass url as NULL */ — disabled in C
	}
}

// checkDiscType — C: check_disc_type
func checkDiscType(ds *discScanner, fd int) {
	r1, _, _ := unix.Syscall(unix.SYS_IOCTL, uintptr(fd),
		cdromDiscStatus, 0)
	switch int(r1) {
	case cdsAudio, cdsMixed:
		setStatus(ds, discAudio, "")
		return

	case cdsData1:
		unix.Seek(fd, 0x8000, unix.SEEK_SET)
		buf := make([]byte, 2048)
		r, _ := unix.Read(fd, buf)

		if r == 2048 {
			// C: volume descriptor at offset 40, up to 32 chars
			end := 40
			for end < 72 && buf[end] > 32 {
				end++
			}
			setStatus(ds, discIsofs, string(buf[40:end]))
			return
		}
	}

	setStatus(ds, discUnknownType, "")
}

// dvdprobe — C: dvdprobe (callout callback, re-arms itself every 1s)
func dvdprobe(co *callout.Callout, aux any) {
	ds := aux.(*discScanner)

	if cs := ds.cs; cs != nil {
		cs.Arm(&ds.timer, dvdprobe, ds, 1)
	}

	fd, err := unix.Open(ds.dev, unix.O_RDONLY|unix.O_NONBLOCK, 0)

	if fd == -1 || err != nil {
		setStatus(ds, discNoDrive, "")
	} else {
		r1, _, _ := unix.Syscall(unix.SYS_IOCTL, uintptr(fd),
			cdromDriveStatus, 0)
		if int(r1) == cdsDiscOK {
			if ds.svc == nil {
				checkDiscType(ds, fd)
			}
		} else {
			setStatus(ds, discNoDisc, "")
		}
		unix.Close(fd)
	}
}

// beDvdCanhandle — C: be_dvd_canhandle
func beDvdCanhandle(url string) int {
	if strings.HasPrefix(url, "dvd:") {
		return 1
	}
	return 0
}

// beDvdPlay — C: be_dvd_play
func beDvdPlay(u *usage.Reporter, nm *notifications.NotificationManager,
	url string, mp *mediacore.MediaPipe, vq, vsl any,
	va *backendcore.VideoArgs) (*mediacore.MediaEvent, error) {
	var e *mediacore.MediaEvent

	if !strings.HasPrefix(url, "dvd:") {
		return nil, errors.New("dvd: Invalid URL")
	}

	url = url[4:]

	mediacore.MpSetURL(mp, va.CanonicalURL, va.ParentURL, va.ParentTitle)

	var perr error
	e, perr = DvdPlay(u, nm, url, mp, 0)

	if e != nil && meIsAction(e, event.ACTION_EJECT) {

		fd, err := unix.Open(url, unix.O_RDONLY|unix.O_NONBLOCK, 0)
		if err == nil && fd != -1 {
			r1, _, errno := unix.Syscall(unix.SYS_IOCTL,
				uintptr(fd), cdromEject, 0)
			if int(r1) != 0 {
				discBackendSystem.TraceSystem().Trace(trace.TRACE_ERROR, "DVD",
					"Eject of %s failed -- %s", url,
					errno.Error())
			}
			unix.Close(fd)
		} else {
			discBackendSystem.TraceSystem().Trace(trace.TRACE_ERROR, "DVD",
				"Unable to open %s for eject", url)
		}
	}
	return e, perr
}

// beDvdStart — C: be_dvd_init
func beDvdStart() int {
	ds := &discScanner{cs: discBackendSystem.CalloutSystem()}

	ds.dev = "/dev/dvd" // C: strdup("/dev/dvd")
	if cs := ds.cs; cs != nil {
		cs.Arm(&ds.timer, dvdprobe, ds, 0)
	}
	return 0
}

// beDvdOpen — C: be_dvd_open
func beDvdOpen(bs *backendcore.BackendSystem, page *propcore.Prop,
	url string, sync bool) error {
	// C: usage_page_open(sync, "DVD")
	bs.Usage().PageOpen(sync, "DVD")
	return bs.OpenVideo(page, url, sync)
}

// C: static backend_t be_dvd = { .be_canhandle, .be_open,
// .be_play_video, .be_init }; BE_REGISTER(dvd)
func RegisterDVDBackend(bs *backendcore.BackendSystem,
	nm *notifications.NotificationManager) {
	discBackendSystem = bs
	dvdlib.SetFAM(bs.FileAccessManager())

	// C: fa_video.c calls dvd_play(url, mp, errbuf, errlen, 1) directly
	// for ISO-probed files (ENABLE_DVD).
	bs.SetDVDPlayer(func(url string,
		mp *mediacore.MediaPipe, vfs int) (any, error) {
		return DvdPlay(bs.Usage(), nm, url, mp, vfs)
	})

	be := &backendcore.Backend{}

	be.CanHandle = func(url string) int {
		return beDvdCanhandle(url)
	}

	be.Open = func(page any, url string, sync bool) error {
		propRoot, ok := page.(*propcore.Prop)
		if !ok {
			return nil
		}
		return beDvdOpen(bs, propRoot, url, sync)
	}

	be.PlayVideo = func(url string, mediaPipe any,
		vq any, vsl any,
		va *backendcore.VideoArgs) (any, error) {
		mp, _ := mediaPipe.(*mediacore.MediaPipe)
		return beDvdPlay(bs.Usage(), nm, url, mp, vq, vsl, va)
	}

	be.Start = func() error {
		beDvdStart()
		return nil
	}

	bs.Register(be)
}
