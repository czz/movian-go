// Canonical port of the video_playback_info (VPI) hooks from
// src/video/video_playback.c:1001-1044 — handlers invoked on video
// start/stop with an info htsmsg built from video_args_t.
//
// C wires the handlers through a linker-section registry (VPI_REGISTER +
// LIST_HEAD). The Go port keeps no package state: the fan-out is composed
// explicitly in cmd/movian-go init (vpiHandlers) and injected into the
// fileaccess backend via prop.SetPlaybackInfoFn.

package video

import (
	"github.com/czz/movian-go/internal/htsmsg"
	"github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
)

// VPIOp — C: vpi_op_t (video_playback.h:50-53)
type VPIOp int

const (
	VPIStart VPIOp = iota // VPI_START
	VPIStop               // VPI_STOP
)

// VPIHandler — C: video_playback_info_handler_t.invoke
type VPIHandler func(op VPIOp, info *htsmsg.HTSMsg, mpRoot, origin *propcore.Prop)

// VPIArgs — the video_args_t fields consumed by
// video_playback_info_create (C: video_playback.c:1001-1025).
type VPIArgs struct {
	CanonicalURL string
	Title        string
	Mimetype     string
	IMDB         string
	Season       int
	Episode      int
	Year         int
}

// VideoPlaybackInfoCreate — C: video_playback_info_create
// (video_playback.c:1001-1025).
func VideoPlaybackInfoCreate(va *VPIArgs) *htsmsg.HTSMsg {
	vpi := htsmsg.NewMap()

	// C: rstr_t *id = get_random_string()
	id := misc.GetRandomString()
	vpi.AddStr("id", misc.RstrGet(id))
	misc.RstrRelease(id)

	vpi.AddStr("canonical_url", va.CanonicalURL)
	if va.Title != "" {
		vpi.AddStr("title", va.Title)
	}
	if va.Mimetype != "" {
		vpi.AddStr("mimetype", va.Mimetype)
	}
	if va.IMDB != "" {
		vpi.AddStr("imdbid", va.IMDB)
	}
	if va.Season > 0 {
		vpi.AddU32("season", uint32(va.Season))
	}
	if va.Episode > 0 {
		vpi.AddU32("episode", uint32(va.Episode))
	}
	if va.Year > 0 {
		vpi.AddU32("year", uint32(va.Year))
	}
	return vpi
}
