// Package playqueue is a strict C-to-Go port of src/playqueue.c.
//
// C's playqueue is a process-global singleton (static lists, one
// media_pipe, one player thread). In Go, the PlayQueue struct carries the
// C file-scope globals as fields; NewPlayQueue performs playqueue_init.
package playqueue

import (
	"errors"
	"strings"
	"time"

	"github.com/czz/movian-go/internal/trace"

	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	mediacore "github.com/czz/movian-go/internal/media/core"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/usage"
)

// SetTraceSystem injects the trace system (C: trace() global).
func (pq *PlayQueue) SetTraceSystem(ts *trace.TraceSystem) { pq.ts = ts }

// dispatch runs fn serialized against all other dispatched sections. If a
// dispatched section is already running on any goroutine, fn is queued and
// executed when the outer section drains — preserving C's ordering where
// nested prop notifications run after the current transition under the
// global prop_mutex.
func (pq *PlayQueue) dispatch(fn func()) {
	pq.dqMu.Lock()
	if pq.dqBusy {
		pq.dq = append(pq.dq, fn)
		pq.dqMu.Unlock()
		return
	}
	pq.dqBusy = true
	pq.dqMu.Unlock()
	fn()
	for {
		pq.dqMu.Lock()
		if len(pq.dq) == 0 {
			pq.dqBusy = false
			pq.dqMu.Unlock()
			return
		}
		q := pq.dq
		pq.dq = nil
		pq.dqMu.Unlock()
		for _, f := range q {
			f()
		}
	}
}

// Fini — C: playqueue_fini.
func (pq *PlayQueue) Fini() {
	pq.DispatchStop()
	pq.dispatch(func() {
		pq.mu.Lock()
		pq.playqueueClear()
		pq.mu.Unlock()
	})
	pq.StopPlayerThread()
}

// NewPlayQueue — C: playqueue_init.
func NewPlayQueue(pm *propcore.PropManager, u *usage.Reporter, fam *fileaccesscore.FileAccessManager, ms *mediacore.MediaSystem) *PlayQueue {
	pq := &PlayQueue{
		usage:      u,
		pm:         pm,
		fam:        fam,
		shuffleLfg: int(time.Now().Unix()),
		playerStop: make(chan struct{}),
	}

	pq.mp = mediacore.MpCreate(ms, pm, "playqueue", mediacore.MPPrimaable)
	if pq.mp != nil {
		pq.mp.FAM = fam
	}

	if pq.mp != nil {
		if pq.mp.PropCanShuffle != nil {
			pq.mp.PropCanShuffle.SetInt(1)
		}
		if pq.mp.PropCanRepeat != nil {
			pq.mp.PropCanRepeat.SetInt(1)
		}
		if pq.mp.PropRoot != nil {
			// C: PROP_TAG_NAME("self","shuffle") CALLBACK_INT on mp root
			pq.watchNamedChild(pq.mp.PropRoot, "shuffle",
				func(ev propcore.EventType, a []any) {
					pq.playqueueSetShuffle(propEventInt(ev, a))
				})
			pq.watchNamedChild(pq.mp.PropRoot, "repeat",
				func(ev propcore.EventType, a []any) {
					pq.playqueueSetRepeat(propEventInt(ev, a))
				})
		}
	}

	// C: playqueue_root = prop_create(prop_get_global(), "playqueue")
	//    playqueue_nodes = prop_create(playqueue_root, "nodes")
	if g := pm.GetGlobal(); g != nil {
		pq.root = pq.pm.CreateEx(g, "playqueue", nil, false, false)
	} else {
		pq.root = pq.pm.CreateRoot("playqueue")
	}
	pq.nodes = pq.pm.CreateEx(pq.root, "nodes", nil, false, false)

	// C: hts_thread_create_detached("audioplayer", player_thread, ...)
	pq.StartPlayerThread()

	// C: prop_subscribe(PROP_TAG_NAME("playqueue","eventSink"), CALLBACK,
	// pq_eventsink, PROP_TAG_ROOT, playqueue_root)
	sink := pq.pm.CreateEx(pq.root, "eventSink", nil, false, false)
	if sink != nil {
		sink.Subscribe(pq.pqEventsink, nil, propcore.SubNoInitialUpdate)
	}

	return pq
}

// Open — C: playqueue_open.
func (pq *PlayQueue) Open(page any) error {
	pageP, _ := page.(*propcore.Prop)
	if pageP == nil {
		return errors.New("playqueue: page is not a prop")
	}

	model := pq.pm.CreateEx(pageP, "model", nil, false, false)
	if tp := pq.pm.CreateEx(model, "type", nil, false, false); tp != nil {
		tp.SetString("directory")
	}
	meta := pq.pm.CreateEx(model, "metadata", nil, false, false)
	if tp := pq.pm.CreateEx(meta, "title", nil, false, false); tp != nil {
		tp.SetString("Playqueue")
	}
	nodes := pq.pm.CreateEx(model, "nodes", nil, false, false)
	if nodes != nil && pq.nodes != nil {
		pq.pm.Link(pq.nodes, nodes, nil, false, false)
	}
	return nil
}

// OpenPage — C: be_playqueue_open.
func (pq *PlayQueue) OpenPage(page *propcore.Prop, url string, sync bool) error {
	pq.usage.PageOpen(sync, "Playqueue")
	return pq.Open(page)
}

// CanHandlePlayQueue — C: be_playqueue_canhandle.
func CanHandlePlayQueue(url string) int {
	if strings.HasPrefix(url, PlayqueueURL) {
		return 1
	}
	return 0
}

// SetPlayAudioFunc wires the C backend_play_audio seam.
func (pq *PlayQueue) SetPlayAudioFunc(fn PlayAudioFunc) {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	pq.playAudio = fn
}

// SetPlayback sets the legacy playback seam (used when no PlayAudioFunc).
func (pq *PlayQueue) SetPlayback(pb PlaybackInterface) {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	pq.playback = pb
}

// SetOpenVideoPageFunc sets the video-page opener callback (Go extra).
func (pq *PlayQueue) SetOpenVideoPageFunc(fn OpenVideoPageFunc) {
	pq.openVideoPage = fn
}
