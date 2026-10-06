package metadata

// Canonical 1:1 port of src/metadata/mlp.c
//
// Lazy-loaded metadata: artist pictures, album art and video info are
// bound to props; a subscription monitor on each bound prop enqueues the
// item on the mlp queue only when an interested (non-monitor) subscriber
// appears. Worker threads (max 4) drain the queue and run the per-class
// load callback.
//
// C file-statics mapped onto the MetadataManager:
//   metadata_mutex          -> mm.mlpMutex
//   metadata_loading_cond   -> mm.mlpCond
//   metadata_num_threads    -> mm.metadataNumThreads
//   mlpqueue                -> mm.mlpQueue

import (
	"fmt"
	"slices"

	dbpkg "github.com/czz/movian-go/internal/db"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/trace"
)

// metadataTrace — C: METADATA_TRACE (metadata.h:24-27). Gated on
// gconf.enable_metadata_debug.
func (mm *MetadataManager) metadataTrace(format string, args ...any) {
	if mm.ts.GetEnableMetadataDebug() {
		mm.ts.Trace(trace.TRACE_DEBUG, "METADATA", format, args...)
	}
}

// MetadataLazyClass — C: metadata_lazy_class_t (mlp.c:56-63)
type MetadataLazyClass struct {
	Load      func(db *dbpkg.DB, mlp *MetadataLazyProp) // mlc_load
	Kill      func(mlp *MetadataLazyProp)               // mlc_kill
	Dtor      func(mlp *MetadataLazyProp)               // mlc_dtor
	AllocSize int                                       // mlc_alloc_size
}

// MetadataLazyProp — C: metadata_lazy_prop_t (mlp.c:68-78).
// Embedded as the first member of the concrete structs so a
// *MetadataLazyProp can be cast back with unsafe.Pointer (C: the
// metadata_lazy_prop_t is the first struct member too).
type MetadataLazyProp struct {
	Class *MetadataLazyClass // mlp_class
	mm    *MetadataManager   // owning manager — C resolves it via the singleton

	ReqItems uint64 // mlp_req_items
	RefCount int16  // mlp_refcount
	Zombie   bool   // mlp_zombie
	Queued   bool   // mlp_queued
	Loading  bool   // mlp_loading
}

// mlpAlloc — C: mlp_alloc (mlp.c:86-92). In C it callocs
// class->mlc_alloc_size and returns the embedded header; in Go the
// caller allocates the concrete struct and we initialise its MLP header.
func mlpAlloc(class *MetadataLazyClass) *MetadataLazyProp {
	return &MetadataLazyProp{
		Class:    class,
		RefCount: 1,
	}
}

// mlpEnqueue — C: mlp_enqueue (mlp.c:99-107).
// Caller holds mm.mlpMutex.
func (mm *MetadataManager) mlpEnqueue(mlp *MetadataLazyProp) {
	if mlp.Zombie || mlp.Queued {
		return
	}
	mm.mlpQueue = append(mm.mlpQueue, mlp)
	mlp.Queued = true
	mm.metadataThreadsStart()
}

// mlpUnqueue — C: mlp_unqueue (mlp.c:113-120).
// Caller holds mm.mlpMutex.
func (mm *MetadataManager) mlpUnqueue(mlp *MetadataLazyProp) {
	if !mlp.Queued {
		return
	}
	for i, p := range mm.mlpQueue {
		if p == mlp {
			mm.mlpQueue = slices.Delete(mm.mlpQueue, i, i+1)
			break
		}
	}
	mlp.Queued = false
}

// mlpRelease — C: mlp_release (mlp.c:126-137).
// Caller holds mm.mlpMutex.
func (mm *MetadataManager) mlpRelease(mlp *MetadataLazyProp) {
	mlp.RefCount--
	if mlp.RefCount > 0 {
		return
	}
	mm.mlpUnqueue(mlp)
	if mlp.Class != nil && mlp.Class.Dtor != nil {
		mlp.Class.Dtor(mlp)
	}
	// C: free(mlp) — GC
}

// mlpRetain — C: mlp_retain (mlp.c:143-146).
// Caller holds mm.mlpMutex.
func (mm *MetadataManager) mlpRetain(mlp *MetadataLazyProp) {
	mlp.RefCount++
}

// mlpDestroy — C: mlp_destroy (mlp.c:152-162).
// Caller holds mm.mlpMutex.
func (mm *MetadataManager) mlpDestroy(mlp *MetadataLazyProp) {
	if !mlp.Zombie {
		mlp.Zombie = true
		if mlp.Class != nil && mlp.Class.Kill != nil {
			mlp.Class.Kill(mlp)
		}
	}
	mm.mlpRelease(mlp)
}

// mlpSubCb — C: mlp_sub_cb (mlp.c:176-200). Runs under metadata_mutex
// (SubMutex). args[len-1] is the subscription's user_int — the
// METADATA_PROP_* bit (Go constants are already shifted bit values, so
// the C `id = 1 << id` is folded into the constant definition).
func (mm *MetadataManager) mlpSubCb(mlp *MetadataLazyProp, event propcore.EventType, args ...any) {
	switch event {
	case propcore.EventSubscriptionMonitorActive:
		var id uint64
		if len(args) > 0 {
			if v, ok := args[len(args)-1].(int); ok {
				id = uint64(v)
			}
		}
		if id != 0 && mlp.ReqItems&id == 0 {
			mlp.ReqItems |= id
			mm.mlpEnqueue(mlp)
		}
	case propcore.EventDestroyed:
		mm.mlpDestroy(mlp)
	}
}

// metadataThread — C: metadata_thread (mlp.c:1992-2024)
func (mm *MetadataManager) metadataThread() {
	var dbc *dbpkg.DB

	mm.mlpMutex.Lock()

	for {
		if len(mm.mlpQueue) == 0 {
			break
		}
		mlp := mm.mlpQueue[0]

		if dbc == nil {
			dbc = mm.Get()
		}

		mm.mlpQueue = mm.mlpQueue[1:]
		mlp.Queued = false
		if !mlp.Zombie && mlp.Class != nil && mlp.Class.Load != nil {
			mlp.Class.Load(dbc, mlp)
		}
	}

	mm.metadataNumThreads--

	mm.mlpMutex.Unlock()

	if dbc != nil {
		mm.Close(dbc)
	}
}

// metadataThreadsStart — C: metadata_threads_start (mlp.c:2030-2038).
// Caller holds mm.mlpMutex.
func (mm *MetadataManager) metadataThreadsStart() {
	if mm.metadataNumThreads >= 4 {
		return
	}
	mm.metadataNumThreads++
	go mm.metadataThread()
}

var _ = fmt.Sprintf // sprintf used via fmt.Sprintf in buildInfoText
