package core

// Canonical port of src/media/media_event.c — media pipe event dispatch,
// queue seek, and the currenttime-prop seek path.

import (
	"slices"

	"github.com/czz/movian-go/internal/event"
	"github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
	videosettings "github.com/czz/movian-go/internal/video"
)

// MPSkipLimit — C: MP_SKIP_LIMIT (media.h:30) — µs that must have elapsed
// before a skip backward restarts the track instead.
const MPSkipLimit = 3000000

// totalSeekTimeInSeconds — C: TOTAL_SEEK_TIME_IN_SECONDS (media_event.c:251)
const totalSeekTimeInSeconds = 2

// updateEpochInQueue — C: update_epoch_in_queue (media_event.c:25)
func updateEpochInQueue(q []*MediaBuf, epoch int) {
	for _, mb := range q {
		mb.Epoch = epoch
	}
}

// mpSeekInQueues — C: mp_seek_in_queues (media_event.c:36-117)
// Drops queued audio packets and pre-keyframe video packets when the
// queues already contain data past the seek target, marks packets
// between the keyframe and target as skip, bumps the epoch, and injects
// flush controls. Returns 0 when the seek was satisfied from the queues.
// Caller holds mp.Mutex.
func mpSeekInQueues(mp *MediaPipe, userTime int64) int {
	rval := 1

	var abuf *MediaBuf
	for _, mb := range mp.Audio.DataQueue {
		if mb.UserTime != PTSUnset && mb.UserTime >= userTime {
			abuf = mb
			break
		}
	}

	if abuf != nil {
		var vbuf, vk *MediaBuf
		for _, mb := range mp.Video.DataQueue {
			if mb.Flags.Keyframe {
				vk = mb
			}
			if mb.PTS != PTSUnset && mb.UserTime >= userTime {
				vbuf = mb
				break
			}
		}

		if vbuf != nil && vk != nil {
			adrop, vdrop, vskip := 0, 0, 0

			for len(mp.Audio.DataQueue) > 0 {
				mb := mp.Audio.DataQueue[0]
				if mb == abuf {
					break
				}
				mp.Audio.DataQueue = mp.Audio.DataQueue[1:]
				mp.Audio.PacketsCurrent--
				mp.BufferCurrent -= MbBufferedSize(mb)
				MediaBufFreeLocked(mp, mb)
				adrop++
			}
			MqUpdateStats(mp, mp.Audio, 1)

			for len(mp.Video.DataQueue) > 0 {
				mb := mp.Video.DataQueue[0]
				if mb == vk {
					break
				}
				mp.Video.DataQueue = mp.Video.DataQueue[1:]
				mp.Video.PacketsCurrent--
				mp.BufferCurrent -= MbBufferedSize(mb)
				MediaBufFreeLocked(mp, mb)
				vdrop++
			}
			MqUpdateStats(mp, mp.Video, 1)

			// Mark packets between the keyframe and target as skip
			vkIdx := indexOfBuf(mp.Video.DataQueue, vk)
			vbufIdx := indexOfBuf(mp.Video.DataQueue, vbuf)
			for i := vkIdx; i >= 0 && i < vbufIdx; i++ {
				mp.Video.DataQueue[i].Flags.Skip = true
				vskip++
			}
			rval = 0

			epoch := int(mp.Epoch)
			updateEpochInQueue(mp.Audio.DataQueue, epoch)
			updateEpochInQueue(mp.Video.DataQueue, epoch)
			updateEpochInQueue(mp.Video.AuxQueue, epoch)

			mb := MediaBufAllocLocked(mp, 0)
			mb.DataType = int(MBCtrlFlush)
			mb.Data32 = 0
			MbEnq(mp, mp.Video, mb)

			mb = MediaBufAllocLocked(mp, 0)
			mb.DataType = int(MBCtrlFlush)
			mb.Data32 = 0
			MbEnq(mp, mp.Audio, mb)

			mpCheckUnderrun(mp)
		}
	}
	return rval
}

// MpDirectSeek — C: mp_direct_seek (media_event.c:123-163).
// Caller holds mp.Mutex.
func MpDirectSeek(mp *MediaPipe, ts int64) {
	if mp.Flags&MPCanSeek == 0 {
		return
	}

	if ts < 0 {
		ts = 0
	}

	// C: prop_set_float_ex(mp->mp_prop_currenttime, mp->mp_sub_currenttime,
	//                      ts / 1000000.0)
	if mp.PropCurrentTime != nil {
		if pm := mp.PropCurrentTime.Manager(); pm != nil {
			pm.SetFloatEx(mp.PropCurrentTime, mp.SubCurrentTime,
				float32(ts)/1000000.0)
		}
	}

	mp.Epoch++
	mp.SeekBase = ts

	if mp.SeekInitiate != nil {
		mp.SeekInitiate(mp)
	}

	// C: prop_set(mp->mp_prop_root, "seektime", PROP_SET_FLOAT, ts / 1000000.0)
	if mp.PropRoot != nil {
		if pm := mp.PropRoot.Manager(); pm != nil {
			pm.SetVEx(nil, mp.PropRoot, "seektime", float32(ts)/1000000.0)
		}
	}

	if mpSeekInQueues(mp, ts) == 0 {
		return
	}

	// C: if a seek event is already enqueued, update it in place
	for _, e := range mp.EventQueue {
		if e.Type != int(event.EVENT_SEEK) {
			continue
		}
		if ets, ok := event.ConcreteOf(e.Data).(*event.EventTs); ok {
			ets.Ts = ts
		}
		return
	}

	ets := &event.EventTs{Ts: ts, Epoch: int(mp.Epoch)}
	ets.Event.SetConcrete(ets)
	ets.Type = event.EVENT_SEEK
	MpEventDispatch(mp, &MediaEvent{Type: int(event.EVENT_SEEK), Data: ets})
}

// MpSeekByPropchange — C: mp_seek_by_propchange (media_event.c:170-193) —
// prop callback on mp_prop_currenttime translating value changes into
// direct seeks. Runs under mp.Mutex (PROP_TAG_MUTEX).
func MpSeekByPropchange(opaque any, ev propcore.EventType, args ...any) {
	mp, ok := opaque.(*MediaPipe)
	if !ok || mp == nil {
		return
	}

	var t int64
	switch ev {
	case propcore.EventSetInt:
		if len(args) == 0 {
			return
		}
		switch v := args[0].(type) {
		case int:
			t = int64(v) * 1000000
		case int64:
			t = v * 1000000
		case int32:
			t = int64(v) * 1000000
		default:
			return
		}
	case propcore.EventSetFloat:
		if len(args) == 0 {
			return
		}
		switch v := args[0].(type) {
		case float64:
			t = int64(v * 1000000.0)
		case float32:
			t = int64(float64(v) * 1000000.0)
		default:
			return
		}
	default:
		return
	}

	MpDirectSeek(mp, t)
}

// MpEventDispatch — C: mp_event_dispatch (media_event.c:198-208) —
// gives the pipe's event callback first refusal; unhandled events go to
// mp_eq and wake producers blocked on backpressure.
// Caller holds mp.Mutex.
func MpEventDispatch(mp *MediaPipe, e *MediaEvent) {
	if mp.HandleEvent == nil ||
		mp.HandleEvent(mp, mp.HandleEventOpaque, e) == 0 {
		mp.EventQueue = append(mp.EventQueue, e)
		backpressureLocked(mp).Signal()
	}
	// C: else event_release(e) — Go GC handles it
}

// eventTypeOf returns the event type id for dedup — matches C's
// e->e_type for both MediaEvent ids and wrapped *event.Event types.
func eventTypeOf(e *MediaEvent) int {
	return e.Type
}

// actionUpdateHoldByEvent — C: action_update_hold_by_event (event.c:386)
func actionUpdateHoldByEvent(hold MediaHoldFlags, e *event.Event) bool {
	if e.IsAction(event.ACTION_PLAYPAUSE) {
		return hold == 0
	}
	if e.IsAction(event.ACTION_PAUSE) {
		return true
	}
	return false
}

// actionCarrier is satisfied by *event.Event and all embedded subtypes
// (C: event_t* covers every event_* via first-member embedding).
type actionCarrier interface {
	IsAction(event.ActionType) bool
}

// eventIsAction checks an ACTION_* against a MediaEvent's wrapped event.
// C: event_is_action (event.h:435-439)
func eventIsAction(e *MediaEvent, at event.ActionType) bool {
	ev, ok := e.Data.(actionCarrier)
	return ok && ev != nil && ev.IsAction(at)
}

// eventBase extracts the embedded *event.Event from any event subtype.
func eventBase(e *MediaEvent) *event.Event {
	if be, ok := e.Data.(interface{ AsEvent() *event.Event }); ok {
		return be.AsEvent()
	}
	return nil
}

// MpEnqueueEventLocked — C: mp_enqueue_event_locked (media_event.c:213-355)
// Caller holds mp.Mutex.
func MpEnqueueEventLocked(mp *MediaPipe, e *MediaEvent) {
	dedupEvent := false

	switch e.Type {
	case int(event.EVENT_SELECT_AUDIO_TRACK):
		est, _ := event.ConcreteOf(e.Data).(*event.EventSelectTrack)
		if est == nil {
			return
		}
		// C: mp_track_mgr_select_track runs synchronously under mp_mutex
		if mp.AudioTrackMgr.selectTrackLocked(est.ID, est.Manual) != 0 {
			return
		}
		dedupEvent = true

	case int(event.EVENT_SELECT_SUBTITLE_TRACK):
		est, _ := event.ConcreteOf(e.Data).(*event.EventSelectTrack)
		if est == nil {
			return
		}
		if mp.SubtitleTrackMgr.selectTrackLocked(est.ID, est.Manual) != 0 {
			return
		}
		dedupEvent = true

	case int(event.EVENT_DELTA_SEEK_REL):
		// We want to seek thru the entire feature in 3 seconds
		ei3, _ := event.ConcreteOf(e.Data).(*event.EventInt3)
		if ei3 == nil {
			return
		}
		pre := int64(ei3.Val1)
		sign := int64(ei3.Val2)
		rate := int64(ei3.Val3)
		if rate == 0 {
			return
		}
		d := pre * pre * mp.Duration /
			(rate * totalSeekTimeInSeconds * 255 * 255)
		mp.SeekBase += d * sign
		MpDirectSeek(mp, mp.SeekBase)
		return

	case int(event.EVENT_PLAYQUEUE_JUMP):
		dedupEvent = true
	}

	if dedupEvent {
		for i, e2 := range mp.EventQueue {
			if eventTypeOf(e2) == e.Type {
				mp.EventQueue = slices.Delete(mp.EventQueue, i, i+1)
				break
			}
		}
	}

	ev := eventBase(e)

	switch {
	case eventIsAction(e, event.ACTION_PLAYPAUSE),
		eventIsAction(e, event.ACTION_PLAY),
		eventIsAction(e, event.ACTION_PAUSE):

		if actionUpdateHoldByEvent(mp.HoldFlags&MPHoldPause, ev) {
			mp.HoldFlags |= MPHoldPause
		} else {
			mp.HoldFlags &^= MPHoldPause
		}
		mpSetPlaystatusByHoldLocked(mp, "")

	case e.Type == int(event.EVENT_INTERNAL_PAUSE):
		var payload string
		if ep := event.BaseEvent(e.Data); ep != nil {
			payload = ep.Payload
		}
		mp.HoldFlags |= MPHoldPause
		mpSetPlaystatusByHoldLocked(mp, payload)

	case eventIsAction(e, event.ACTION_SEEK_BACKWARD):
		MpDirectSeek(mp, mp.SeekBase-1000000*
			int64(mp.Sys.VS.SeekBackStep))

	case eventIsAction(e, event.ACTION_SEEK_FORWARD):
		MpDirectSeek(mp, mp.SeekBase+1000000*
			int64(mp.Sys.VS.SeekFwdStep))

	case eventIsAction(e, event.ACTION_SHUFFLE):
		if mp.PropShuffle != nil {
			mp.PropShuffle.SetInt(1 - mp.PropShuffle.GetInt())
		}

	case eventIsAction(e, event.ACTION_REPEAT):
		if mp.PropRepeat != nil {
			mp.PropRepeat.SetInt(1 - mp.PropRepeat.GetInt())
		}

	case eventIsAction(e, event.ACTION_CYCLE_AUDIO):
		MpTrackMgrNextTrack(mp.AudioTrackMgr)

	case eventIsAction(e, event.ACTION_CYCLE_SUBTITLE):
		MpTrackMgrNextTrack(mp.SubtitleTrackMgr)

	case eventIsAction(e, event.ACTION_VOLUME_UP),
		eventIsAction(e, event.ACTION_VOLUME_DOWN):

		switch mp.Sys.VS.DpadUpDownMode {
		case videosettings.VideoDpadMasterVolume:
			// C: event_dispatch(e) — forward to the global event system.
			// Go: the composition root wires ms.EventDispatch to the
			// global event manager (mediacore sits below it in the DAG).
			if mp.Sys.EventDispatch != nil && ev != nil {
				mp.Sys.EventDispatch(ev)
			}
		case videosettings.VideoDpadPerFileVolume:
			if mp.VolSetting == nil {
				break
			}
			delta := -1
			if eventIsAction(e, event.ACTION_VOLUME_UP) {
				delta = 1
			}
			mp.VolSetting.AddInt(delta)
		}

	default:
		// Forward event to player

		if eventIsAction(e, event.ACTION_SKIP_BACKWARD) &&
			mp.SeekBase >= MPSkipLimit &&
			mp.Flags&MPCanSeek != 0 {
			// Convert skip previous to track restart
			MpDirectSeek(mp, 0)
			return
		}

		if eventIsAction(e, event.ACTION_STOP) ||
			eventIsAction(e, event.ACTION_EJECT) {
			if mp.PropPlayStatus != nil {
				mp.PropPlayStatus.SetString("stop")
			}
		}

		if e.Type == int(event.EVENT_PLAYQUEUE_JUMP) ||
			e.Type == int(event.EVENT_EXIT) ||
			eventIsAction(e, event.ACTION_STOP) ||
			eventIsAction(e, event.ACTION_SKIP_FORWARD) ||
			eventIsAction(e, event.ACTION_SKIP_BACKWARD) {
			// C: always non-NULL post-mp_create; guard for pipes
			// built outside mp_create (tests, stubs).
			if mp.Cancellable != nil {
				misc.CancellableCancel(mp.Cancellable)
			}
		}

		// C: atomic_inc(&e->e_refcount); mp_event_dispatch(mp, e)
		MpEventDispatch(mp, e)
	}
}

// MpEnqueueEvent is the canonical locking wrapper — see lifecycle.go.

// MediaHooks.EventDispatch — Go seam for C's global event_dispatch()
// (mp_enqueue_event_locked's ACTION_VOLUME_UP/DOWN master-volume path).
// Registered by the app init once the global event manager exists.

// MediaEventsink — C: media_eventsink (media_event.c:371) — per-mp
// eventSink subscription callback; runs under mp.Mutex.
func MediaEventsink(opaque any, e *MediaEvent) {
	mp, ok := opaque.(*MediaPipe)
	if !ok || mp == nil {
		return
	}
	MpEnqueueEventLocked(mp, e)
}

// MpEventSetCallback — C: mp_event_set_callback (media_event.c:379)
func MpEventSetCallback(mp *MediaPipe,
	cb func(mp *MediaPipe, opaque any, e *MediaEvent) int,
	opaque any) {
	mp.Mutex.Lock()
	defer mp.Mutex.Unlock()
	mp.HandleEvent = cb
	mp.HandleEventOpaque = opaque
}

// boolToInt — C: ternary-to-int for prop_set_int flag writes.
