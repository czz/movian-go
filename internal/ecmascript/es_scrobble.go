// Canonical 1:1 port of src/ecmascript/es_scrobble.c — videoscrobble hook
// invoked from video playback info events.
package ecmascript

import (
	"github.com/czz/movian-go/internal/gaftape"
	htsmsg "github.com/czz/movian-go/internal/htsmsg"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/video"
)

// ---------------------------------------------------------------------------
// video_scrobble_aux_t — C: es_scrobble.c:26-31
// ---------------------------------------------------------------------------

// C: video_scrobble_aux_t
type videoScrobbleAux struct {
	op     video.VPIOp
	info   *htsmsg.HTSMsg
	p      *propcore.Prop
	origin *propcore.Prop
}

// videoScrobblePushArgs — C: video_scrobble_push_args (es_scrobble.c:37-59)
func videoScrobblePushArgs(gaf *gaftape.Context, opaque any) int {
	vsa := opaque.(*videoScrobbleAux)

	var op string
	switch vsa.op {
	case video.VPIStart:
		op = "start"
	case video.VPIStop:
		op = "stop"
	default:
		return 0
	}

	gaf.PushString(op)

	gaf.PushObject()
	for _, f := range vsa.info.GetFields() {
		EsPushHtsmsgField(gaf, f)
		gaf.PutPropString(-2, f.GetName())
	}

	esStpropPush(gaf, vsa.p)
	esStpropPush(gaf, vsa.origin)
	return 4
}

// scrobbleVideoTask — C: scrobble_video_task (es_scrobble.c:63-72)
func scrobbleVideoTask(aux any) {
	vsa := aux.(*videoScrobbleAux)
	EsHookInvoke("videoscrobble", videoScrobblePushArgs, vsa)
	vsa.info.Release()
	esEnv.propPM.RefDec(vsa.p)
	esEnv.propPM.RefDec(vsa.origin)
}

// ScrobbleVideo — C: es_scrobble_video (es_scrobble.c:78-88),
// registered via VPI_REGISTER; the Go wiring lives in cmd/movian-go init.
func ScrobbleVideo(op video.VPIOp, info *htsmsg.HTSMsg,
	p, origin *propcore.Prop) {
	vsa := &videoScrobbleAux{}
	vsa.op = op
	vsa.info = info.Copy()
	vsa.p = esEnv.propPM.RefInc(p)
	vsa.origin = esEnv.propPM.Follow(origin)

	esEnv.tasks.Run(scrobbleVideoTask, vsa)
}
