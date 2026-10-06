package misc

import (
	"sync"
	"sync/atomic"
)

// Port of src/misc/cancellable.c

// C: struct cancellable — void *opaque maps to any. The C static
// cancellable_mutex becomes per-object (it only ever guards the
// single cancellable's fields).
type Cancellable struct {
	mu        sync.Mutex       // C: static HTS_MUTEX_DECL(cancellable_mutex)
	refcount  atomic.Int32     // C: atomic_t refcount
	cancelled bool             // C: int cancelled
	cancel    func(opaque any) // C: void (*cancel)(void *opaque)
	opaque    any              // C: void *opaque
}

// C: cancellable_t *cancellable_bind(cancellable_t *c, void (*fn)(void *opaque), void *opaque)
func CancellableBind(c *Cancellable, fn func(opaque any), opaque any) *Cancellable {
	c.refcount.Add(1)
	c.mu.Lock()
	c.cancel = fn
	c.opaque = opaque

	if c.cancelled {
		fn(opaque)
	}

	c.mu.Unlock()
	return c
}

// C: void cancellable_unbind(cancellable_t *c, void *opaque)
func CancellableUnbind(c *Cancellable, opaque any) {
	if c == nil {
		return
	}

	c.mu.Lock()

	if c.opaque == opaque {
		c.cancel = nil
		c.opaque = nil
	}

	c.mu.Unlock()
	CancellableRelease(c)
}

// C: void cancellable_cancel_locked(cancellable_t *c)
func CancellableCancelLocked(c *Cancellable) {
	c.cancelled = true
	if c.cancel != nil {
		c.cancel(c.opaque)
	}
}

// C: void cancellable_cancel(cancellable_t *c)
func CancellableCancel(c *Cancellable) {
	c.mu.Lock()
	CancellableCancelLocked(c)
	c.mu.Unlock()
}

// C: void cancellable_reset(cancellable_t *c)
func CancellableReset(c *Cancellable) {
	c.mu.Lock()
	c.cancelled = false
	c.cancel = nil
	c.mu.Unlock()
}

// C: int cancellable_is_cancelled(const cancellable_t *c)
func CancellableIsCancelled(c *Cancellable) int {
	if c == nil || !c.cancelled {
		return 0
	}
	return 1
}

// C: cancellable_t *cancellable_create(void)
func CancellableCreate() *Cancellable {
	c := &Cancellable{}
	c.refcount.Store(1) // C: atomic_set(&c->refcount, 1)
	return c
}

// C: void cancellable_release(cancellable_t *c)
func CancellableRelease(c *Cancellable) {
	if c == nil {
		return
	}

	if c.refcount.Add(-1) != 0 {
		return
	}
	// C: free(c)
}

// C: cancellable_t *cancellable_retain(cancellable_t *c)
func CancellableRetain(c *Cancellable) *Cancellable {
	if c != nil {
		c.refcount.Add(1)
	}
	return c
}
