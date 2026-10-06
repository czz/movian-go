// Package playqueue is a strict C-to-Go port of src/playqueue.c.
//
// C's playqueue is a process-global singleton (static lists, one
// media_pipe, one player thread). In Go, the PlayQueue struct carries the
// C file-scope globals as fields; NewPlayQueue performs playqueue_init.
package playqueue

import (
	"slices"

	"github.com/czz/movian-go/internal/event"
	mediacore "github.com/czz/movian-go/internal/media/core"
)

// pqeRef — C: pqe_ref
func pqeRef(pqe *PlayQueueEntry) {
	pqe.refcount.Add(1)
}

// pqeUnref — C: pqe_unref
func (pq *PlayQueue) pqeUnref(pqe *PlayQueueEntry) {
	if pqe.refcount.Add(-1) != 0 {
		return
	}
	// C asserts: pqe_current != pqe, !linked, originator == NULL,
	// urlsub == NULL, typesub == NULL
	if pqe.originator != nil {
		pq.pm.RefDec(pqe.originator)
		pqe.originator = nil
	}
	if pqe.node != nil {
		pq.pm.Destroy(pqe.node)
	}
}

// pqeEventCreatex — C: pqe_event_createx. Returns a MediaEvent whose Data
// holds the pqe (with a reference taken).
func pqeEventCreatex(pqe *PlayQueueEntry, et event.EventType) *mediacore.MediaEvent {
	pqeRef(pqe)
	return &mediacore.MediaEvent{Type: int(et), Data: pqe}
}

// pqePlay — C: pqe_play — enqueue a JUMP event on the media pipe.
func (pq *PlayQueue) pqePlay(pqe *PlayQueueEntry, how event.EventType) {
	e := pqeEventCreatex(pqe, how)
	mediacore.MpEnqueueEvent(pq.mp, e)
	// C: event_release(e) — the queue holds the only ref; the entry ref
	// is consumed by the receiver (dtor semantics).
}

// releaseEventEntry — C: pqe_event_dtor / the pe_pqe ref drop done at
// consume sites in player_thread.
func (pq *PlayQueue) releaseEventEntry(e *mediacore.MediaEvent) {
	if e == nil {
		return
	}
	if pqe, ok := e.Data.(*PlayQueueEntry); ok {
		e.Data = nil
		pq.pqeUnref(pqe)
	}
}

func removePQE(list []*PlayQueueEntry, pqe *PlayQueueEntry) []*PlayQueueEntry {
	for i, x := range list {
		if x == pqe {
			return slices.Delete(list, i, i+1)
		}
	}
	return list
}

func insertBeforePQE(list []*PlayQueueEntry, pqe, before *PlayQueueEntry) []*PlayQueueEntry {
	for i, x := range list {
		if x == before {
			out := make([]*PlayQueueEntry, 0, len(list)+1)
			out = append(out, list[:i]...)
			out = append(out, pqe)
			out = append(out, list[i:]...)
			return out
		}
	}
	return append(list, pqe)
}

func insertPQE(list []*PlayQueueEntry, pqe *PlayQueueEntry, pos int) []*PlayQueueEntry {
	if pos >= len(list) {
		return append(list, pqe)
	}
	out := make([]*PlayQueueEntry, 0, len(list)+1)
	out = append(out, list[:pos]...)
	out = append(out, pqe)
	out = append(out, list[pos:]...)
	return out
}

func nextPQE(list []*PlayQueueEntry, pqe *PlayQueueEntry) *PlayQueueEntry {
	for i, x := range list {
		if x == pqe {
			if i+1 < len(list) {
				return list[i+1]
			}
			return nil
		}
	}
	return nil
}

func prevPQE(list []*PlayQueueEntry, pqe *PlayQueueEntry) *PlayQueueEntry {
	for i, x := range list {
		if x == pqe {
			if i > 0 {
				return list[i-1]
			}
			return nil
		}
	}
	return nil
}

func firstPQE(list []*PlayQueueEntry) *PlayQueueEntry {
	if len(list) > 0 {
		return list[0]
	}
	return nil
}

func lastPQE(list []*PlayQueueEntry) *PlayQueueEntry {
	if len(list) > 0 {
		return list[len(list)-1]
	}
	return nil
}
