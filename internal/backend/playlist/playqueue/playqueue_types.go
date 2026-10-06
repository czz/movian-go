// Package playqueue is a strict C-to-Go port of src/playqueue.c.
//
// C's playqueue is a process-global singleton (static lists, one
// media_pipe, one player thread). In Go, the PlayQueue struct carries the
// C file-scope globals as fields; NewPlayQueue performs playqueue_init.
package playqueue

import (
	"sync"
	"sync/atomic"

	"github.com/czz/movian-go/internal/trace"

	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	mediacore "github.com/czz/movian-go/internal/media/core"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/usage"
)

// PlayqueueURL — C: PLAYQUEUE_URL "playqueue:"
const PlayqueueURL = "playqueue:"

// C: playqueue.h flags
const (
	PQNoSkip = 0x1 // C: PQ_NO_SKIP
	PQPaused = 0x2 // C: PQ_PAUSED
)

// PlayQueueEntry — C: playqueue_entry_t
type PlayQueueEntry struct {
	refcount atomic.Int32

	// Read-only members
	url     string         // C: pqe_url
	psource *propcore.Prop // C: pqe_psource
	node    *propcore.Prop // C: pqe_node
	propURL *propcore.Prop // C: pqe_prop_url

	enq      bool // C: pqe_enq — enqueued (not from source list)
	linked   bool // C: pqe_linked — globally linked (playqueue_mutex)
	playable bool // C: pqe_playable
	startme  int  // C: pqe_startme — 1 + start_paused

	// Points back into node prop from source siblings.
	// A ref is held on this prop when not nil.
	originator *propcore.Prop // C: pqe_originator

	// C: pqe_urlsub — subscribes to source.url
	urlsub *pqNamedChildSub
	// C: pqe_typesub — subscribes to source.type
	typesub *pqNamedChildSub

	// Index in queue — C: pqe_index
	index int
}

// PlayAudioFunc is the C backend_play_audio seam.
// C: backend_play_audio(url, mp, errbuf, errlen, paused, mimetype) —
// blocks until playback ends; returns the terminating event or nil on
// error. Go returns any (*event.Event or *mediacore.MediaEvent).
type PlayAudioFunc func(url string, mp *mediacore.MediaPipe, paused bool, mimetype string) (any, error)

// PlayQueue — C: playqueue.c file-scope globals.
type PlayQueue struct {
	usage *usage.Reporter    // C: usage_page_open global (usage.c)
	mu    sync.Mutex         // C: playqueue_mutex
	ts    *trace.TraceSystem // C: trace() global — injected

	pm  *propcore.PropManager
	fam *fileaccesscore.FileAccessManager // C: implicit global fa context

	mp *mediacore.MediaPipe // C: playqueue_mp

	// C: playqueue_entries / playqueue_shuffled_entries /
	// playqueue_source_entries (TAILQ heads)
	entries       []*PlayQueueEntry
	shuffled      []*PlayQueueEntry
	sourceEntries []*PlayQueueEntry
	length        int // C: playqueue_length

	current *PlayQueueEntry // C: pqe_current

	root        *propcore.Prop // C: playqueue_root
	nodes       *propcore.Prop // C: playqueue_nodes
	model       *propcore.Prop // C: playqueue_model
	sourceSub   *pqNamedChildSub
	startme     *propcore.Prop // C: playqueue_startme
	startPaused bool           // C: playqueue_start_paused

	shuffleMode int // C: playqueue_shuffle_mode
	repeatMode  int // C: playqueue_repeat_mode
	shuffleLfg  int // C: shuffle_lfg

	// Go seam for C's backend_play_audio (avoids backend→playqueue
	// import cycle; wired by init.go to BackendSystem.PlayAudio).
	playAudio PlayAudioFunc

	// Go extras (test seams)
	playback      PlaybackInterface
	openVideoPage OpenVideoPageFunc

	playerStop    chan struct{}
	playerWg      sync.WaitGroup
	playerStarted bool

	// Serial callback dispatch — emulates C's PROP_TAG_MUTEX(&playqueue_mutex)
	// + prop_mutex serialization. Go delivers prop notifications unlocked,
	// so callbacks that fire re-entrantly (e.g. initial updates from
	// Subscribe while a queue operation holds pq.mu) are queued and drained
	// after the outermost dispatched section completes, instead of
	// deadlocking on a non-recursive mutex.
	dqMu   sync.Mutex
	dqBusy bool
	dq     []func()
}

// PlaybackInterface — Go seam retained for callers that drive playback
// directly (used when no PlayAudioFunc is wired).
type PlaybackInterface interface {
	Open(url string) error
	Play() error
	Stop() error
	Pause() error
	Resume() error
	SeekToPosition(positionSec int64) error
}

// OpenVideoPageFunc is called when the playqueue starts video playback.
type OpenVideoPageFunc func(url string)

// pqNamedChildSub — owns the parent child-watch plus the armed leaf value
// subscription of a watchNamedChild path. C has a single prop_sub_t for
// the whole PROP_TAG_NAME path; the Go split must destroy both halves
// together or the leaf stays armed forever (leaks the pq lockmgr ref).
type pqNamedChildSub struct {
	mu     sync.Mutex
	parent *propcore.Subscription
	leaf   *propcore.Subscription
}
