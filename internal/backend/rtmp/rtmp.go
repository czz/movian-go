/*
 * Canonical port of src/backend/rtmp/rtmp.c — backend registration shell.
 *
 * be_play_video lives in rtmp_ffmpeg.go and uses FFmpeg's native rtmp://
 * protocol + live_flv demuxer. The vendored ext/rtmpdump/librtmp
 * transport (rtmplib subpackage) was removed after live testing showed
 * its rtmpdump-era handshake fails against modern RTMP servers
 * (mediamtx: connection never completes), while the FFmpeg path plays,
 * resumes and shuts down through the shared fa_video tail
 * (BePlayvideoFctx). rtmp_t's AMF packet parsing, RTMP_SendSeek and the
 * manual event loop no longer exist because libavformat owns the
 * transport; RTMPE/SWF verification was already disabled upstream
 * (NO_CRYPTO) and FFmpeg's RTMPS/RTMPE are disabled in the vendored
 * build, so reachable schemes are unchanged.
 *
 * Registration follows the htsp/bittorrent precedent: the package
 * imports backend/core, so RegisterRTMPBackend is called externally
 * from cmd/movian-go/wire_media.go to avoid an import cycle.
 */

package rtmp

import (
	"errors"
	"strings"
	"time"

	backendcore "github.com/czz/movian-go/internal/backend/core"
	mediacore "github.com/czz/movian-go/internal/media/core"
	medialibav "github.com/czz/movian-go/internal/media/libav"
	"github.com/czz/movian-go/internal/trace"
)

// Backend — C: rtmp.c file statics (be_rtmp), owned by main.
type Backend struct {
	ts *trace.TraceSystem // C: trace() global — injected from bs
}

// NewBackend creates the rtmp backend instance — C: rtmp.c file
// statics, owned by main.
func NewBackend() *Backend { return &Backend{} }

// ---------------------------------------------------------------------------
// rtmp_canhandle (rtmp.c:78-83)
// ---------------------------------------------------------------------------

func rtmpCanHandle(url string) int {
	if strings.HasPrefix(url, "rtmp://") || strings.HasPrefix(url, "rtmpe://") {
		return 1
	}
	return 0
}

// ---------------------------------------------------------------------------
// rtmp_probe (rtmp.c:806-839) — FFmpeg variant
// ---------------------------------------------------------------------------

// rtmpProbe opens the stream with avformat_open_input; ProbeOK when the
// RTMP handshake and stream headers succeed, ProbeFail otherwise. The
// caller's timeoutMs bounds the wait; OpenFormat's 30 s AVIOInterruptCB
// is the outer bound that eventually releases a goroutine stuck on a
// dead host.
func (b *Backend) rtmpProbe(url string, timeoutMs int) (backendcore.ProbeResult, error) {
	type probeRes struct {
		f   *medialibav.AVFormatCtx
		err error
	}
	ch := make(chan probeRes, 1)
	go func() {
		f, err := medialibav.OpenFormat(url)
		ch <- probeRes{f, err}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			return backendcore.ProbeFail, r.err
		}
		if r.f != nil {
			r.f.Close()
		}
		return backendcore.ProbeOK, nil
	case <-time.After(time.Duration(timeoutMs) * time.Millisecond):
		return backendcore.ProbeFail, errors.New("RTMP probe timed out")
	}
}

// ---------------------------------------------------------------------------
// rtmp_open (rtmp.c:846-850) — be_open
// ---------------------------------------------------------------------------

func (b *Backend) rtmpOpen(bs *backendcore.BackendSystem, page any, url string,
	sync bool) error {
	// C: usage_page_open(sync, "RTMP")
	bs.Usage().PageOpen(sync, "RTMP")
	return bs.OpenVideo(page, url, sync)
}

// ---------------------------------------------------------------------------
// be_rtmp (rtmp.c:856-864) — BE_REGISTER(rtmp)
// ---------------------------------------------------------------------------

// RegisterRTMPBackend registers the RTMP backend.
// C: static backend_t be_rtmp = { .be_canhandle = rtmp_canhandle,
// .be_open = rtmp_open, .be_play_video = rtmp_playvideo,
// .be_probe = rtmp_probe }; BE_REGISTER(rtmp);
// (be_init only installed the librtmp log handler — dropped with
// rtmplib.)
func (b *Backend) RegisterRTMPBackend(bs *backendcore.BackendSystem) {
	be := &backendcore.Backend{}
	be.CanHandle = rtmpCanHandle
	be.Open = func(page any, url string, sync bool) error {
		return b.rtmpOpen(bs, page, url, sync)
	}
	be.PlayVideo = func(url string, mediaPipe any,
		vq any, vsl any, va *backendcore.VideoArgs) (any, error) {
		mp, _ := mediaPipe.(*mediacore.MediaPipe)
		b.ts = bs.TraceSystem()
		return b.rtmpPlayvideoAV(bs, url, mp, va)
	}
	be.Probe = func(url string, timeoutMs int) (backendcore.ProbeResult, error) {
		return b.rtmpProbe(url, timeoutMs)
	}
	bs.Register(be)
}
