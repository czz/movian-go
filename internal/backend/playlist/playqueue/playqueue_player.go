// Package playqueue is a strict C-to-Go port of src/playqueue.c.
//
// C's playqueue is a process-global singleton (static lists, one
// media_pipe, one player thread). In Go, the PlayQueue struct carries the
// C file-scope globals as fields; NewPlayQueue performs playqueue_init.
package playqueue

import (
	"github.com/czz/movian-go/internal/event"
	mediacore "github.com/czz/movian-go/internal/media/core"
	propcore "github.com/czz/movian-go/internal/prop"
)

// enqueue — C: playqueue_enqueue.
func (pq *PlayQueue) enqueue(track *propcore.Prop) {
	pq.dispatch(func() {
		pq.enqueueLocked(track)
	})
}

func (pq *PlayQueue) enqueueLocked(track *propcore.Prop) {
	urlP := track.GetChild("url")
	if urlP == nil {
		return
	}
	url := urlP.GetString()
	if url == "" {
		return
	}

	pq.mu.Lock()
	defer pq.mu.Unlock()
	pqe := &PlayQueueEntry{url: url}
	pqe.node = pq.pm.CreateRoot("")
	pqe.enq = true
	pqe.refcount.Store(1)
	pqe.linked = true
	pqe.playable = true

	// C: prop_link_ex(prop_create(track,"metadata"),
	//                 prop_create(pqe->pqe_node,"metadata"), NULL,
	//                 PROP_LINK_XREFED, 0)
	srcMeta := pq.pm.CreateEx(track, "metadata", nil, false, false)
	dstMeta := pq.pm.CreateEx(pqe.node, "metadata", nil, false, false)
	if srcMeta != nil && dstMeta != nil {
		pq.pm.LinkHard(srcMeta, dstMeta, nil, propcore.LinkXrefed)
	}

	if up := pq.pm.CreateEx(pqe.node, "url", nil, false, false); up != nil {
		up.SetString(url)
		pqe.propURL = up
	}
	if tp := pq.pm.CreateEx(pqe.node, "type", nil, false, false); tp != nil {
		tp.SetString("audio")
	}

	doplay := pq.current == nil

	var before *PlayQueueEntry
	if pq.current != nil {
		// C: before = TAILQ_NEXT(pqe_current, pqe_linear_link)
		for i, x := range pq.entries {
			if x == pq.current && i+1 < len(pq.entries) {
				before = pq.entries[i+1]
				break
			}
		}
	}
	// Skip past any previously enqueued entries
	for before != nil && before.enq {
		for i, x := range pq.entries {
			if x == before && i+1 < len(pq.entries) {
				before = pq.entries[i+1]
			} else if x == before {
				before = nil
			}
		}
	}

	if before == nil {
		pq.entries = append(pq.entries, pqe)
		pq.pm.SetParentEx(pqe.node, pq.nodes, nil, "")
	} else {
		pq.entries = insertBeforePQE(pq.entries, pqe, before)
		pq.pm.SetParentEx(pqe.node, pq.nodes,
			&propcore.SetParentOpaque{Before: before.node}, "")
	}
	pq.pqRenumber(pqe)
	pq.pqeInsertShuffled(pqe)
	pq.updatePQMeta()

	if doplay {
		pq.pqePlay(pqe, event.EVENT_PLAYQUEUE_JUMP)
	}
}

// Play — C: playqueue_play. Clears the queue, enqueues one track, plays it.
// metadata may be a *propcore.Prop (adopted as the node's metadata child)
// or nil.
func (pq *PlayQueue) Play(url string, metadata any, paused bool) {
	pq.dispatch(func() {
		pq.mu.Lock()
		defer pq.mu.Unlock()
		pq.playLocked(url, metadata, paused)
	})
}

// playLocked — C: playqueue_play body, under pq.mu.
func (pq *PlayQueue) playLocked(url string, metadata any, paused bool) {
	pqe := &PlayQueueEntry{url: url}
	pqe.node = pq.pm.CreateRoot("")
	pqe.refcount.Store(1)
	pqe.linked = true
	pqe.playable = true

	if mp, ok := metadata.(*propcore.Prop); ok && mp != nil {
		pq.pm.SetParentEx(mp, pqe.node, nil, "metadata")
	}
	pqe.originator = pq.pm.RefInc(pqe.node)
	if up := pq.pm.CreateEx(pqe.node, "url", nil, false, false); up != nil {
		up.SetString(url)
	}
	if tp := pq.pm.CreateEx(pqe.node, "type", nil, false, false); tp != nil {
		tp.SetString("audio")
	}

	// Clear out the current playqueue
	pq.playqueueClear()

	// Enqueue our new entry
	pq.entries = append(pq.entries, pqe)
	pq.pqRenumber(pqe)
	pq.pqeInsertShuffled(pqe)
	pq.updatePQMeta()
	pq.pm.SetParentEx(pqe.node, pq.nodes, nil, "")

	// Tick player to play it
	how := event.EVENT_PLAYQUEUE_JUMP
	if paused {
		how = event.EVENT_PLAYQUEUE_JUMP_AND_PAUSE
	}
	pq.pqePlay(pqe, how)
}

// playqueueSetShuffle — C: playqueue_set_shuffle. Runs under pq.mu.
func (pq *PlayQueue) playqueueSetShuffle(v int) {
	pq.shuffleMode = v
	pq.updatePQMeta()
}

// playqueueSetRepeat — C: playqueue_set_repeat. Runs under pq.mu.
func (pq *PlayQueue) playqueueSetRepeat(v int) {
	pq.repeatMode = v
	pq.updatePQMeta()
}

// pqEventsink — C: pq_eventsink. Runs unlocked (C: no mutex on this sub —
// the callbacks take playqueue_mutex themselves).
func (pq *PlayQueue) pqEventsink(_ any, ev propcore.EventType, args ...any) {
	if ev != propcore.EventExtEvent {
		return
	}
	if len(args) == 0 {
		return
	}
	var track, source *propcore.Prop
	mode := 0
	switch ep := args[0].(type) {
	case *event.EventPlayTrack:
		// C: event_playtrack_t — track/source/mode fields
		track, _ = ep.Track.(*propcore.Prop)
		source, _ = ep.Source.(*propcore.Prop)
		mode = ep.Mode
	case *event.Event:
		if ep.Type != event.EVENT_PLAYTRACK {
			return
		}
		track, source = playtrackFields(ep)
		mode = playtrackMode(ep)
	default:
		return
	}
	if track == nil {
		return
	}
	if source == nil {
		pq.enqueue(track)
	} else {
		flags := 0
		if mode != 0 {
			flags = PQNoSkip
		}
		pq.LoadWithSource(track, source, flags)
	}
}

// playqueueAdvance0 — C: playqueue_advance0. Caller must hold pq.mu.
func (pq *PlayQueue) playqueueAdvance0(pqe *PlayQueueEntry, reverse bool) *PlayQueueEntry {
	cur := pqe
	for {
		if pqe.linked {
			if pq.shuffleMode != 0 {
				if reverse {
					pqe = prevPQE(pq.shuffled, pqe)
					if pq.repeatMode != 0 && pqe == nil {
						pqe = lastPQE(pq.shuffled)
					}
				} else {
					pqe = nextPQE(pq.shuffled, pqe)
					if pq.repeatMode != 0 && pqe == nil {
						pqe = firstPQE(pq.shuffled)
					}
				}
			} else {
				if reverse {
					pqe = prevPQE(pq.entries, pqe)
					if pq.repeatMode != 0 && pqe == nil {
						pqe = lastPQE(pq.entries)
					}
				} else {
					pqe = nextPQE(pq.entries, pqe)
					if pq.repeatMode != 0 && pqe == nil {
						pqe = firstPQE(pq.entries)
					}
				}
			}
		} else {
			pqe = firstPQE(pq.entries)
		}
		if pqe == nil || pqe == cur || pqe.playable {
			break
		}
	}
	return pqe
}

// updatePQMeta — C: update_pq_meta. Caller must hold pq.mu.
func (pq *PlayQueue) updatePQMeta() {
	mp := pq.mp
	if mp == nil || mp.PropRoot == nil {
		return
	}
	pqe := pq.current

	canNext := 0
	canPrev := 0
	if pqe != nil {
		if pq.playqueueAdvance0(pqe, false) != nil {
			canNext = 1
		}
		if pq.playqueueAdvance0(pqe, true) != nil {
			canPrev = 1
		}
	}

	if mp.PropCanSkipForward != nil {
		mp.PropCanSkipForward.SetInt(canNext)
	}
	if mp.PropCanSkipBackward != nil {
		mp.PropCanSkipBackward.SetInt(canPrev)
	}

	if t := pq.pm.CreateEx(mp.PropRoot, "totalTracks", nil, false, false); t != nil {
		t.SetInt(pq.length)
	}
	ct := pq.pm.CreateEx(mp.PropRoot, "currentTrack", nil, false, false)
	if ct != nil {
		if pqe != nil {
			ct.SetInt(pqe.index)
		} else {
			ct.SetVoid()
		}
	}
}

// playqueueAdvance — C: playqueue_advance.
func (pq *PlayQueue) playqueueAdvance(pqe *PlayQueueEntry, reverse bool) *PlayQueueEntry {
	var nxt *PlayQueueEntry
	pq.dispatch(func() {
		pq.mu.Lock()
		nxt = pq.playqueueAdvance0(pqe, reverse)
		if nxt != nil {
			pqeRef(nxt)
		}
		pq.updatePQMeta()
		pq.pqeUnref(pqe)
		pq.mu.Unlock()
	})
	return nxt
}

// StartPlayerThread — C: hts_thread_create_detached("audioplayer", ...).
func (pq *PlayQueue) StartPlayerThread() {
	pq.mu.Lock()
	if pq.playerStarted {
		pq.mu.Unlock()
		return
	}
	pq.playerStarted = true
	pq.mu.Unlock()
	pq.playerWg.Add(1)
	go pq.playerThread()
}

// StopPlayerThread stops the player thread (Go teardown; C's thread is
// detached and runs for the process lifetime).
func (pq *PlayQueue) StopPlayerThread() {
	pq.mu.Lock()
	started := pq.playerStarted
	pq.mu.Unlock()
	if !started {
		return
	}
	select {
	case <-pq.playerStop:
	default:
		close(pq.playerStop)
	}
	// Wake the dequeue wait with a quit sentinel.
	if pq.mp != nil {
		mediacore.MpEnqueueEvent(pq.mp, &mediacore.MediaEvent{Type: -1})
	}
	pq.playerWg.Wait()
}

// playerThread — C: player_thread (playqueue.c:1032).
func (pq *PlayQueue) playerThread() {
	defer pq.playerWg.Done()
	mp := pq.mp
	var pqe *PlayQueueEntry
	startpaused := false
	errStr := "" // C: char errbuf[512] — persists across iterations

	for {
		select {
		case <-pq.playerStop:
			return
		default:
		}

		for pqe == nil {
			// Got nothing to play, enter STOP mode
			pq.dispatch(func() {
				pq.mu.Lock()
				pq.current = nil
				pq.updatePQMeta()
				pq.mu.Unlock()
			})

			// Drain queues
			e := mediacore.MpWaitForEmptyQueues(mp)
			if e != nil {
				// Got event while waiting for drain
				mediacore.MpFlush(mp)
			} else {
				// Nothing and media queues empty
				if pq.root != nil {
					if a := pq.pm.CreateEx(pq.root, "active", nil, false, false); a != nil {
						a.SetInt(0)
					}
				}
				mediacore.MpSetURL(mp, "", "", "")
				mediacore.MpShutdown(mp)
				if mp != nil && mp.PropMetadata != nil {
					pq.pm.Unlink(mp.PropMetadata)
				}
				// ... and wait for an event
				e = mediacore.MpDequeueEvent(mp)
			}

			if e == nil {
				continue
			}
			if e.Type == -1 { // stop sentinel
				return
			}
			ev := normalizeEvent(e)

			if e.Type == int(event.EVENT_PLAYQUEUE_JUMP) ||
				e.Type == int(event.EVENT_PLAYQUEUE_JUMP_AND_PAUSE) {
				if p, ok := e.Data.(*PlayQueueEntry); ok {
					pqe = p
					e.Data = nil // ref consumed by pqe
				}
				startpaused = e.Type == int(event.EVENT_PLAYQUEUE_JUMP_AND_PAUSE)
			} else if ev != nil &&
				(ev.IsAction(event.ACTION_PLAY) || ev.IsAction(event.ACTION_PLAYPAUSE)) {
				pq.dispatch(func() {
					pq.mu.Lock()
					pqe = firstPQE(pq.entries)
					if pqe != nil {
						pqeRef(pqe)
					}
					pq.mu.Unlock()
				})
			}
			pq.releaseEventEntry(e)
		}

		if pqe.url == "" {
			pqe = pq.playqueueAdvance(pqe, false)
			continue
		}

		if pq.root != nil {
			if a := pq.pm.CreateEx(pq.root, "active", nil, false, false); a != nil {
				a.SetInt(1)
			}
		}
		mediacore.MpReset(mp, pq.pm)

		// C: prop_get_by_name(PNVEC("self","metadata"), 1, NAMED_ROOT node "self")
		// The '1' is incref — caller holds an extra ref dropped by prop_ref_dec.
		sm := pq.pm.CreateEx(pqe.node, "metadata", nil, false, false)
		if sm != nil {
			pq.pm.RefInc(sm)
			if mp.PropMetadata != nil {
				pq.pm.LinkHard(sm, mp.PropMetadata, nil, propcore.LinkXrefed)
			}
		}
		mp.PropMetadataSource = sm

		m := pq.pm.CreateEx(pqe.node, "media", nil, false, false)
		if m != nil {
			pq.pm.RefInc(m)
			pq.pm.Link(mp.PropRoot, m, nil, false, false)
		}

		pq.dispatch(func() {
			pq.mu.Lock()
			mediacore.MpSetURL(mp, pqe.url, "", "")
			pq.current = pqe
			pq.updatePQMeta()

			if pq.playqueueAdvance0(pqe, false) == nil && pq.sourceSub != nil {
				pq.pm.WantMoreChilds(pq.sourceSub.parent)
			}
			pq.mu.Unlock()
		})

		p := pq.pm.CreateEx(pqe.node, "playing", nil, false, false)
		if p != nil {
			pq.pm.RefInc(p) // C: prop_get_by_name(..., incref=1)
			p.SetInt(1)
		}

		if startpaused {
			mediacore.MpHold(mp, pq.pm, mediacore.MPHoldPause, "")
		} else {
			mediacore.MpUnhold(mp, pq.pm, mediacore.MPHoldPause)
		}

		// C: e = backend_play_audio(pqe->pqe_url, mp, errbuf, ...)
		var re any
		if pq.playAudio != nil {
			var perr error
			re, perr = pq.playAudio(pqe.url, mp, startpaused, "")
			if perr != nil {
				errStr = perr.Error()
			}
		} else if pq.playback != nil {
			// Legacy Go seam: drive the playback pipeline directly.
			if err := pq.playback.Open(pqe.url); err == nil {
				_ = pq.playback.Play()
				re = &event.Event{Type: event.EVENT_EOF}
			} else {
				re = nil
			}
		}
		if sm != nil {
			pq.pm.RefDec(sm)
		}
		startpaused = false

		if p != nil {
			p.SetInt(0)
			pq.pm.RefDec(p)
		}

		// Unlink $self.media
		if m != nil {
			pq.pm.Unlink(m)
			pq.pm.RefDec(m)
		}

		pq.dispatch(func() {
			pq.mu.Lock()
			pq.current = nil
			pq.mu.Unlock()
		})

		if f := pq.pm.CreateEx(mp.PropRoot, "format", nil, false, false); f != nil {
			f.SetVoid()
		}

		e := normalizeEventAny(re)
		if e == nil {
			pq.ts.Error("PLAYQUEUE", "Unable to play %s -- %s", pqe.url, errStr)
			pqe = pq.playqueueAdvance(pqe, false)
			continue
		}

		switch {
		case e.IsAction(event.ACTION_SKIP_BACKWARD):
			pqe = pq.playqueueAdvance(pqe, true)
		case e.IsAction(event.ACTION_SKIP_FORWARD) || e.Type == event.EVENT_EOF:
			pqe = pq.playqueueAdvance(pqe, false)
		case e.IsAction(event.ACTION_STOP) || e.IsAction(event.ACTION_EJECT):
			pq.pqeUnref(pqe)
			pqe = nil
		case e.Type == event.EVENT_PLAYQUEUE_JUMP ||
			e.Type == event.EVENT_PLAYQUEUE_JUMP_AND_PAUSE:
			pq.pqeUnref(pqe)
			pqe = nil
			if me, ok := re.(*mediacore.MediaEvent); ok {
				if np, ok2 := me.Data.(*PlayQueueEntry); ok2 {
					pqe = np
					me.Data = nil
				}
			}
			startpaused = e.Type == event.EVENT_PLAYQUEUE_JUMP_AND_PAUSE
		default:
			// C: abort()
			pq.ts.Debug("PLAYQUEUE", "Unhandled event type %d", e.Type)
			pq.pqeUnref(pqe)
			pqe = nil
		}
	}
}

// EventHandler — C: playqueue_event_handler.
func (pq *PlayQueue) EventHandler(ev any) {
	e := event.BaseEvent(ev)
	if e == nil {
		return
	}
	if e.IsAction(event.ACTION_PLAY) || e.IsAction(event.ACTION_PLAYPAUSE) {
		mediacore.MpEnqueueEvent(pq.mp, &mediacore.MediaEvent{
			Type: int(event.EVENT_ACTION_VECTOR), Data: e.Concrete(),
		})
	}
}

// DispatchStop — C: event_dispatch(event_create_action(ACTION_STOP)) in
// playqueue_fini. Delivered to the mp queue so the player thread exits
// playback.
func (pq *PlayQueue) DispatchStop() {
	if pq.mp == nil {
		return
	}
	mediacore.MpEnqueueEvent(pq.mp, &mediacore.MediaEvent{
		Type: int(event.EVENT_ACTION_VECTOR),
		Data: &event.Event{Type: event.EVENT_ACTION_VECTOR,
			Actions: []event.ActionType{event.ACTION_STOP}},
	})
}

// StartEventConsumer consumes event-manager events (Go wiring for the
// parts of C's event_dispatch flow that don't go through the prop
// eventSink).
func (pq *PlayQueue) StartEventConsumer(em *event.EventManager, stopChan <-chan struct{}) {
	if em == nil {
		return
	}
	go func() {
		sink := em.GetEventSink()
		for {
			select {
			case <-stopChan:
				return
			case e, ok := <-sink:
				if !ok {
					return
				}
				pq.EventHandler(e)
			}
		}
	}()
}

// normalizeEvent unwraps a MediaEvent's Data into an *event.Event when
// possible (ACTION_* checks need the real event).
func normalizeEvent(me *mediacore.MediaEvent) *event.Event {
	if me == nil {
		return nil
	}
	return event.BaseEvent(me.Data)
}

// normalizeEventAny converts a backend_play_audio return value to an
// *event.Event (accepting *event.Event directly or MediaEvent-wrapped).
func normalizeEventAny(re any) *event.Event {
	switch x := re.(type) {
	case *event.Event:
		return x
	case *mediacore.MediaEvent:
		if e := event.BaseEvent(x.Data); e != nil {
			return e
		}
		// MediaEvent carrying an event type directly
		return &event.Event{Type: event.EventType(x.Type)}
	case interface{ AsEvent() *event.Event }:
		return x.AsEvent()
	case nil:
		return nil
	default:
		return nil
	}
}

// propEventInt extracts an int value from a prop value event.
func propEventInt(ev propcore.EventType, args []any) int {
	switch ev {
	case propcore.EventSetInt:
		if len(args) > 0 {
			switch v := args[0].(type) {
			case int:
				return v
			case int64:
				return int(v)
			}
		}
	case propcore.EventSetFloat:
		if len(args) > 0 {
			switch f := args[0].(type) {
			case float32:
				return int(f)
			case float64:
				return int(f)
			}
		}
	}
	return 0
}

// playtrackFields extracts (track, source) from an EVENT_PLAYTRACK event.
// The event arrives flattened to *event.Event; the Track/Source props are
// carried on the base Event's PlayTrack field (C: event_playtrack_t cast
// from event_t).
func playtrackFields(e *event.Event) (track, source *propcore.Prop) {
	if e.PlayTrack == nil {
		return nil, nil
	}
	t, _ := e.PlayTrack.Track.(*propcore.Prop)
	s, _ := e.PlayTrack.Source.(*propcore.Prop)
	return t, s
}

// playtrackMode extracts the mode field from an EVENT_PLAYTRACK event.
func playtrackMode(e *event.Event) int {
	if e.PlayTrack == nil {
		return 0
	}
	return e.PlayTrack.Mode
}
