package misc

import (
	"sync"
	"sync/atomic"
)

// Port of src/misc/lockmgr.c + lockmgr.h

// C: typedef enum lockmgr_op_t
const (
	LOCKMGR_UNLOCK  = iota // C: LOCKMGR_UNLOCK
	LOCKMGR_LOCK           // C: LOCKMGR_LOCK
	LOCKMGR_TRY            // C: LOCKMGR_TRY
	LOCKMGR_RETAIN         // C: LOCKMGR_RETAIN
	LOCKMGR_RELEASE        // C: LOCKMGR_RELEASE
)

// C: typedef struct lockmgr
type Lockmgr struct {
	lmMutex    sync.Mutex         // C: hts_mutex_t lm_mutex
	lmRelease  func(ptr *Lockmgr) // C: void (*lm_release)(void *ptr)
	lmRefcount atomic.Int32       // C: atomic_t lm_refcount
}

// C: int lockmgr_handler(void *ptr, lockmgr_op_t op)
func LockmgrHandler(ptr *Lockmgr, op int) int {
	lm := ptr

	switch op {
	case LOCKMGR_UNLOCK:
		lm.lmMutex.Unlock()
		return 0
	case LOCKMGR_LOCK:
		lm.lmMutex.Lock()
		return 0
	case LOCKMGR_TRY:
		if lm.lmMutex.TryLock() { // C: hts_mutex_trylock returns 0 on success
			return 0
		}
		return 1
	case LOCKMGR_RETAIN:
		lm.lmRefcount.Add(1)
		return 0
	case LOCKMGR_RELEASE:
		lm.lmRelease(ptr)
		return 0
	}
	panic("lockmgr_handler: invalid op") // C: abort()
}

// C: void lockmgr_init(lockmgr_t *lm, void (*release)(void *aux))
func LockmgrSetup(lm *Lockmgr, release func(ptr *Lockmgr)) {
	lm.lmRelease = release
	lm.lmRefcount.Store(1)
}

// C: int lockmgr_release(lockmgr_t *lm)
func LockmgrRelease(lm *Lockmgr) int {
	if lm.lmRefcount.Add(-1) != 0 {
		return 1
	}
	// C: hts_mutex_destroy(&lm->lm_mutex)
	return 0
}
