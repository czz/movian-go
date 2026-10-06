// Package htsp is a 1:1 port of src/backend/htsp/htsp.c.
//
// Every C function, struct and global maps to the Go counterpart below,
// in the same order. C references are given per function.
package htsp

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	backendcore "github.com/czz/movian-go/internal/backend/core"
	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/htsmsg"
	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/misc"
	navcore "github.com/czz/movian-go/internal/navigator"
	"github.com/czz/movian-go/internal/nls"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/trace"
)

// C: be_htsp_open (htsp.c:1500-1552)
func (sys *System) beHtspOpen(bs *backendcore.BackendSystem, page any, url string, sync bool) error {
	pm := sys.pm
	propRoot, ok := page.(*propcore.Prop)
	if !ok {
		return fmt.Errorf("htsp: invalid page prop")
	}

	path := make([]byte, urlMax)
	hc, cerr := sys.htspConnectionFind(url, path)
	if hc == nil {
		msg := ""
		if cerr != nil {
			msg = cerr.Error()
		}
		return navcore.OpenError(pm, propRoot, msg)
	}
	pathStr := misc.CStr(path)

	sys.ts.Trace(trace.TRACE_DEBUG, "HTSP", "Open %s", url)

	if strings.HasPrefix(pathStr, "/dvr/") {
		bs.Usage().PageOpen(sync, "HTSP DVR")
		return bs.OpenVideo(page, url, sync)
	}

	if strings.HasPrefix(pathStr, "/channel/") {
		bs.Usage().PageOpen(sync, "HTSP Channel")
		return bs.OpenVideo(page, url, sync)
	}

	if strings.HasPrefix(pathStr, "/events/") {
		bs.Usage().PageOpen(sync, "HTSP Events")
		sys.makeEventModel(propRoot, hc, pathStr[len("/events/"):])
		return nil
	}

	if pathStr == "/channels" {
		bs.Usage().PageOpen(sync, "HTSP Channels")

		sys.makeModel(propRoot, nls.GetProp("Channels"),
			hc.channelsSorted, "tvchannels")

	} else if pathStr == "/recordings" {
		bs.Usage().PageOpen(sync, "HTSP Recordings")
		sys.makeModel(propRoot, nls.GetProp("Recorded shows"),
			hc.dvrSorted, "")

	} else if strings.HasPrefix(pathStr, "/tag/") {
		bs.Usage().PageOpen(sync, "HTSP Tag")
		model := pm.CreateEx(hc.tagsNodes,
			pathStr[len("/tag/"):], nil, false, false)
		sys.makeModel2(propRoot, model, "tvchannels")

	} else if pathStr == "" {
		bs.Usage().PageOpen(sync, "HTSP Root")

		pm.Link(hc.rootModel,
			pm.CreateEx(propRoot, "model", nil, false, false),
			nil, false, false)

	} else {
		navcore.OpenErrorf(pm, propRoot, "Invalid HTSP URL")
	}
	return nil
}

// C: be_htsp_playdvr (htsp.c:2068-2105)
func (sys *System) beHtspPlaydvr(bs *backendcore.BackendSystem, url string, mp *mediacore.MediaPipe,
	vq *backendcore.VideoQueue, hc *htspConnection,
	remain string, va0 *backendcore.VideoArgs) (*mediacore.MediaEvent, error) {
	pm := sys.pm

	m := htsmsg.NewMap()
	m.AddStr("method", "fileOpen")
	filename := snprintf(64, "dvr/%s", remain)
	m.AddStr("file", filename)

	m = htspReqreply(hc, m)
	if m == nil {
		return nil, errors.New("Unable to open file")
	}

	hf := &htspFile{}

	if v, err := m.GetS64("size"); err == nil {
		hf.fileSize = v
	}
	if v, err := m.GetS32("mtime"); err == nil {
		hf.mtime = int(v)
	}
	if v, err := m.GetS32("id"); err == nil {
		hf.id = int(v)
	}
	hf.hc = hc

	// C: hf->h.fh_proto = &fa_protocol_htsp — Go: build the fam Handle
	// bound to hf's read/seek/close/fsize ops.
	fh := fileaccesscore.NewHandle(bs.GetFileAccessManager(), nil, url, hf, nil, hf,
		func() int64 { return htspFileFsize(hf) })

	va := *va0
	va.Flags |= backendcore.BackendVideoNoSubtitleScan

	pm.SetVEx(nil, mp.PropRoot, "loading", 1)

	mediacore.MpSetURL(mp, va0.CanonicalURL, va0.ParentURL, va0.ParentTitle)

	if va.Flags&backendcore.BackendVideoNoAudio == 0 {
		mediacore.MpBecomePrimary(mp, pm)
	}

	e0, perr := bs.BeFilePlayvideoFH(url, mp, vq, fh, &va)
	return toMediaEvent(e0), perr
}

// C: be_htsp_playvideo (htsp.c:2111-2162)
func (sys *System) beHtspPlayvideo(bs *backendcore.BackendSystem, url string, mp *mediacore.MediaPipe,
	vq *backendcore.VideoQueue, va *backendcore.VideoArgs) (*mediacore.MediaEvent, error) {
	pm := sys.pm
	primary := 0
	if va.Flags&backendcore.BackendVideoPrimary != 0 {
		primary = 1
	}

	mediacore.MpSetURL(mp, va.CanonicalURL, va.ParentURL, va.ParentTitle)

	sys.ts.Trace(trace.TRACE_DEBUG, "HTSP",
		"Starting video playback %s primary=%s, priority=%d",
		url, map[bool]string{true: "yes", false: "no"}[primary != 0],
		va.Priority)

	path := make([]byte, urlMax)
	hc, cerr := sys.htspConnectionFind(url, path)
	if hc == nil {
		return nil, cerr
	}
	pathStr := misc.CStr(path)

	if r, ok := mystrbegins(pathStr, "/dvr/"); ok {
		return sys.beHtspPlaydvr(bs, url, mp, vq, hc, r, va)
	}

	// C: usage_event("Play video", 1, USAGE_SEG("format", "HTSP"))
	bs.Usage().Event("Play video", 1, "format", "HTSP")

	hs := &htspSubscription{}

	hs.sid = uint32(hc.sidGenerator.Add(1))
	hs.mp = mp
	origin, _ := va.Origin.(*propcore.Prop)
	hs.origin = pm.RefInc(origin)

	hc.subscriptionMutex.Lock()
	hc.subscriptions = slices.Insert(hc.subscriptions, 0, hs)
	hc.subscriptionMutex.Unlock()

	e, serr := htspSubscriber(hc, hs, primary, va.Priority, vq, url)

	mediacore.MpShutdown(mp)

	hc.subscriptionMutex.Lock()
	for i, x := range hc.subscriptions {
		if x == hs {
			hc.subscriptions = slices.Delete(hc.subscriptions, i, i+1)
			break
		}
	}
	hc.subscriptionMutex.Unlock()

	htspFreeStreams(hs)
	pm.RefDec(hs.origin)
	return e, serr
}

// C: be_htsp_canhandle (htsp.c:2552-2556)
func beHtspCanhandle(url string) int {
	if strings.HasPrefix(url, "htsp://") {
		return 1
	}
	return 0
}

// C: htsp_init (htsp.c:2562-2567)
func (sys *System) htspStart() int {
	// C: hts_mutex_init(&htsp_global_mutex) — package-level mutex in Go
	// is already initialized.
	return 0
}

// RegisterHTSPBackend registers the HTSP backend.
// C: static backend_t be_htsp = { .be_init = htsp_init,
// .be_canhandle = be_htsp_canhandle, .be_open = be_htsp_open,
// .be_play_video = be_htsp_playvideo }; BE_REGISTER(htsp); (htsp.c:2574-2581)
//
// Go: registration is explicit (bittorrent/hls precedent) because this
// package imports backend/core for VideoArgs/VideoQueue/OpenVideo.
func RegisterHTSPBackend(bs *backendcore.BackendSystem) {
	sys := &System{
		pm: bs.GetPropManager(),
		kr: bs.Keyring(),
		ts: bs.TraceSystem(),
	}

	be := &backendcore.Backend{
		Flags: 0, // C: be_htsp has no BACKEND_OPEN_CHECKS_URI — phase-3 dispatch
	}
	be.Start = func() error {
		sys.htspStart()
		return nil
	}
	be.CanHandle = beHtspCanhandle
	be.Open = func(page any, url string, sync bool) error {
		return sys.beHtspOpen(bs, page, url, sync)
	}
	be.PlayVideo = func(url string, mediaPipe any,
		vq any, vsl any, va *backendcore.VideoArgs) (any, error) {
		mp, _ := mediaPipe.(*mediacore.MediaPipe)
		v, _ := vq.(*backendcore.VideoQueue)
		return sys.beHtspPlayvideo(bs, url, mp, v, va)
	}
	bs.Register(be)
}
