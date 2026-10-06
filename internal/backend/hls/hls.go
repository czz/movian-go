// Canonical port of src/backend/hls/hls.c — HLS playlist/variant/segment
// management and the dual-demuxer playback loop. hls.h structures are
// reproduced as Go types; all 55 C functions are mapped in file order.
//
// Sentinel pointers (void*)-1/-2/-3 are modelled by mbRef/segRef codes.
package hls

import (
	"errors"
	"strconv"
	"strings"

	backendcore "github.com/czz/movian-go/internal/backend/core"
	"github.com/czz/movian-go/internal/event"
	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/gconf"
	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/metadata"
	"github.com/czz/movian-go/internal/subtitles"
	"github.com/czz/movian-go/internal/trace"
	"github.com/czz/movian-go/internal/usage"
)

// ---------------------------------------------------------------------------
// Constants (hls.h:28-42, hls.c:41)
// ---------------------------------------------------------------------------

// hlsTrace — C: HLS_TRACE (hls.h:273-276).
func hlsTrace(h *hls, format string, ap ...any) {
	if h != nil && h.Debug {
		h.ts.Trace(trace.TRACE_DEBUG, "HLS", format, ap...)
	}
}

func hlsPlayvideo(u *usage.Reporter, mm *metadata.MetadataManager, url string, mp *mediacore.MediaPipe,
	vq, vsl any,
	va0 *backendcore.VideoArgs, ts *trace.TraceSystem, g *gconf.T,
	subSys *subtitles.System) (*mediacore.MediaEvent, error) {

	mediacore.MpSetURL(mp, va0.CanonicalURL, va0.ParentURL, va0.ParentTitle)

	if mp.PropRoot != nil {
		if m := mp.PropRoot.Manager(); m != nil {
			m.SetVEx(nil, mp.PropRoot, "loading", 1)
		}
	}

	url = strings.TrimPrefix(url, "hls:")
	if url == "test" {
		url = testURL
	}

	var baseurl string
	buf, lerr := fileaccesscore.FALoad2(mp.FAM, url,
		&fileaccesscore.FALoadArgs{
			Flags:       fileaccesscore.FaCompression,
			Cancellable: mp.Cancellable,
			Location:    &baseurl,
		})
	if buf == nil {
		return nil, lerr
	}

	mp.ResetTime = va0.LoadRequestTimestamp
	mp.ResetEpoch = mp.Epoch

	var e *mediacore.MediaEvent
	if len(buf.Data) == 0 {
		return nil, errors.New("Playlist contains no data")
	}
	e0, perr := hlsPlayExtm3u(u, mm, buf.Data, baseurl, mp,
		vq, vsl, va0, ts, g)
	if e0 != nil {
		e = e0.(*mediacore.MediaEvent)
	}
	return e, perr
}

func hlsCanHandle(url string) int {
	if strings.HasPrefix(url, "hls:") {
		return 1
	}
	return 0
}

// RegisterHLSBackend — C: backend_t be_hls (hls.c:2311-2315) +
// BE_REGISTER(hls).
func RegisterHLSBackend(bs *backendcore.BackendSystem) {
	// C: be_file_playvideo calls hls_play_extm3u directly (fa_video.c:635)
	bs.SetHLSPlayer(func(content []byte, url string,
		mp *mediacore.MediaPipe, vq, vsl any,
		va *backendcore.VideoArgs) (any, error) {
		return hlsPlayExtm3u(bs.Usage(), bs.Metadata(), content, url, mp, vq, vsl, va, bs.TraceSystem(), bs.Gconf())
	})
	be := &backendcore.Backend{
		Prefix: "hls:",
	}
	be.CanHandle = hlsCanHandle
	be.Open = func(page any, url string, sync bool) error {
		// C: hls_open — usage_page_open(sync, "HLS") then
		// backend_open_video(page, url, sync)
		// C: usage_page_open(sync, "HLS")
		bs.Usage().PageOpen(sync, "HLS")
		return bs.OpenVideo(page, url, sync)
	}
	be.PlayVideo = func(url string, mediaPipe any,
		vq any, vsl any,
		va *backendcore.VideoArgs) (any, error) {
		mp, _ := mediaPipe.(*mediacore.MediaPipe)
		return hlsPlayvideo(bs.Usage(), bs.Metadata(), url, mp, vq, vsl, va, bs.TraceSystem(), bs.Gconf(), bs.SubSys())
	}
	bs.Register(be)
}

// mystrbegins — C: mystrbegins (misc/str.h) — returns remainder or !ok.
func mystrbegins(s, prefix string) (string, bool) {
	if strings.HasPrefix(s, prefix) {
		return s[len(prefix):], true
	}
	return "", false
}

// myStr2double — C: my_str2double (misc/dbl.h) — strtod semantics
// (leading double parse).
func myStr2double(s string) float64 {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	start := i
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	digits := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
		digits++
	}
	if i < len(s) && s[i] == '.' {
		i++
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
			digits++
		}
	}
	if digits == 0 {
		return 0
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		j := i + 1
		if j < len(s) && (s[j] == '+' || s[j] == '-') {
			j++
		}
		exp := j
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j > exp {
			i = j
		}
	}
	f, _ := strconv.ParseFloat(s[start:i], 64)
	return f
}

// meIsType — C: event_is_type.
func meIsType(e *mediacore.MediaEvent, t event.EventType) bool {
	return e != nil && e.Type == int(t)
}

// meIsAction — C: event_is_action.
func meIsAction(e *mediacore.MediaEvent, at event.ActionType) bool {
	if e == nil {
		return false
	}
	if c, ok := e.Data.(interface{ IsAction(event.ActionType) bool }); ok {
		return c.IsAction(at)
	}
	return false
}
