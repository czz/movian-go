package prop

import (
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// ExtEvent is the interface for external event objects carried by
// EventExtEvent notifications. It is implemented by *event.Event.
// Using an interface avoids an import cycle between pkg/prop/core
// and pkg/event.
//
// C: event_t * (prop_i.h: hpn_ext_event)
type ExtEvent interface {
	// AddRef increments the reference count.
	// C: atomic_inc(&e->e_refcount)
	AddRef()
	Release()
}

// DispatchMode represents the dispatch mode for a subscription.
// C: hps_dispatch_mode (prop_i.h:524-527)
type DispatchMode uint8

const (
	// DispatchModeCourier enqueues notifications on a courier for async dispatch.
	// C: PROP_SUB_DISPATCH_MODE_COURIER
	DispatchModeCourier DispatchMode = 0

	// DispatchModeDirect invokes callbacks synchronously (C: PROP_SUB_INTERNAL).
	// This is the default for most internal subscriptions.
	DispatchModeDirect DispatchMode = 1

	// DispatchModeGlobal enqueues on a global dispatch queue.
	// C: PROP_SUB_DISPATCH_MODE_GLOBAL
	DispatchModeGlobal DispatchMode = 2

	// DispatchModeGroup enqueues on a shared group dispatch queue.
	// C: PROP_SUB_DISPATCH_MODE_GROUP
	DispatchModeGroup DispatchMode = 3
)

// LockmgrOp is the lock-manager operation.
// C: lockmgr_op_t (src/misc/lockmgr.h)
type LockmgrOp int

const (
	LockmgrUnlock  LockmgrOp = iota // C: LOCKMGR_UNLOCK
	LockmgrLock                     // C: LOCKMGR_LOCK
	LockmgrTry                      // C: LOCKMGR_TRY
	LockmgrRetain                   // C: LOCKMGR_RETAIN
	LockmgrRelease                  // C: LOCKMGR_RELEASE
)

// Lockmgr wraps a lock-manager function, giving it a stable identity.
// C compares hps_lockmgr function pointers when piggybacking contended
// subscriptions in the global dispatcher; the *Lockmgr pointer plays
// the role of the C function-pointer identity.
// C: lockmgr_fn_t (src/misc/lockmgr.h)
type Lockmgr struct {
	// Fn performs a lock operation on ptr. Returns non-zero when
	// LockmgrTry fails to acquire the lock (C: hts_mutex_trylock).
	Fn func(ptr any, op LockmgrOp) int
}

// proplockmgr is the default lockmanager for normal mutexes.
// C: proplockmgr (prop_core.c:197)
var proplockmgr = &Lockmgr{Fn: func(ptr any, op LockmgrOp) int {
	switch op {
	case LockmgrUnlock:
		ptr.(*sync.Mutex).Unlock()
	case LockmgrLock:
		ptr.(*sync.Mutex).Lock()
	case LockmgrTry:
		if !ptr.(*sync.Mutex).TryLock() {
			return 1
		}
	}
	return 0
}}

// propSubDispatch is a per-subscription (GLOBAL) or shared (GROUP)
// notification queue drained by the global dispatch workers.
// C: prop_sub_dispatch_t (prop_i.h:393)
type propSubDispatch struct {
	// notifications is the FIFO of pending notifications.
	// C: TAILQ psd_notifications
	notifications []*Notification
	// waitQueue holds psd's that piggybacked onto this one because
	// their head notification contends on the same hps_lock.
	// C: TAILQ psd_wait_queue
	waitQueue []*propSubDispatch
	// refCount is only used for GROUP subscriptions: the group
	// itself holds one ref plus one per member subscription.
	// C: psd_refcount
	refCount int
}

const (
	// C: PROP_GLOBAL_DISPATCH_IDLE_THREADS (prop_core.c:57)
	globalDispatchIdleThreads = 4
	// C: PROP_GLOBAL_DISPATCH_MAX_THREADS (prop_core.c:58)
	globalDispatchMaxThreads = 8
)

// Global dispatch state. All fields are protected by globalDispatchMutex,
// which plays the role C assigns to prop_mutex for the dispatch queues.
// C: prop_core.c:60-64
var (
	globalDispatchMutex       sync.Mutex
	globalDispatchCond        = sync.NewCond(&globalDispatchMutex)
	globalDispatchQueue       []*propSubDispatch // C: prop_global_dispatch_queue
	globalDispatchDispatching []*propSubDispatch // C: prop_global_dispatch_dispatching_queue
	globalDispatchAvail       int                // C: prop_global_dispatch_avail (idle workers)
	globalDispatchRunning     int                // C: prop_global_dispatch_running (total workers)
)

// removePsd removes psd from a queue slice.
// C: TAILQ_REMOVE(q, psd, psd_link)
func removePsd(q []*propSubDispatch, psd *propSubDispatch) []*propSubDispatch {
	for i, e := range q {
		if e == psd {
			return slices.Delete(q, i, i+1)
		}
	}
	return q
}

// propGlobalDispatchThread is the global dispatch worker.
// C: prop_global_dispatch_thread (prop_core.c:1060)
//
// Each iteration pops the head psd, tries to dispatch its head
// notification with LOCKMGR_TRY. On contention it either piggybacks
// onto another in-flight psd holding the same lock, or blocks with
// LOCKMGR_LOCK. Drained psd's are requeued tail for round-robin.
func propGlobalDispatchThread() {
	globalDispatchMutex.Lock()
	for {
		var psd *propSubDispatch
		if len(globalDispatchQueue) > 0 {
			psd = globalDispatchQueue[0]
		}
		if psd == nil {
			if globalDispatchAvail == globalDispatchIdleThreads {
				break
			}
			globalDispatchAvail++
			globalDispatchCond.Wait()
			globalDispatchAvail--
			continue
		}

		globalDispatchQueue = globalDispatchQueue[1:]
		globalDispatchDispatching = append(globalDispatchDispatching, psd)

		n := psd.notifications[0]

		globalDispatchMutex.Unlock()
		r := dispatchOne(n, LockmgrTry)
		globalDispatchMutex.Lock()

		globalDispatchDispatching = removePsd(globalDispatchDispatching, psd)

		if r != 0 {
			// Failed to acquire lock — check if any other psd currently
			// dispatching holds the same (lockmgr, lock); if so,
			// piggyback onto its wait queue.
			var s *propSubDispatch
			for _, other := range globalDispatchDispatching {
				if other == psd {
					continue // C: assert(s != psd)
				}
				n2 := other.notifications[0]
				if n2.sub.active.Load() && n.sub.active.Load() &&
					n2.sub.hpsLockmgr == n.sub.hpsLockmgr &&
					n2.sub.hpsLock == n.sub.hpsLock {
					other.waitQueue = append(other.waitQueue, psd)
					s = other
					break
				}
			}

			if s == nil {
				// No contending psd — something else holds the lock.
				// Wait for it with a blocking lock.
				globalDispatchDispatching = append(globalDispatchDispatching, psd)
				globalDispatchMutex.Unlock()
				dispatchOne(n, LockmgrLock)
				globalDispatchMutex.Lock()
				globalDispatchDispatching = removePsd(globalDispatchDispatching, psd)
			} else {
				continue
			}
		}

		psd.notifications = psd.notifications[1:]

		// Promote the first waiting psd back to the main queue and
		// merge the rest of our wait queue onto it (C: TAILQ_MERGE).
		if len(psd.waitQueue) > 0 {
			s := psd.waitQueue[0]
			psd.waitQueue = psd.waitQueue[1:]
			globalDispatchQueue = append(globalDispatchQueue, s)
			s.waitQueue = append(s.waitQueue, psd.waitQueue...)
			psd.waitQueue = nil
		}

		if len(psd.notifications) == 0 {
			if n.sub.dispatchMode == DispatchModeGlobal {
				n.sub.hpsDispatch = nil
				// C: pool_put(psd_pool, psd) — Go GC
			}
		} else {
			// Round-robin: requeue at tail
			globalDispatchQueue = append(globalDispatchQueue, psd)
		}

		subRefDec(n.sub) // C: prop_sub_ref_dec_locked + pool_put(notify_pool, n)
	}
	globalDispatchRunning--
	globalDispatchMutex.Unlock()
}

// propGlobalDispatchWakeup wakes an idle worker or spawns a new one.
// C: prop_global_dispatch_wakeup (prop_core.c:1164)
// Called with globalDispatchMutex held.
func propGlobalDispatchWakeup() {
	if globalDispatchAvail > 0 {
		globalDispatchCond.Signal()
	} else if globalDispatchRunning < globalDispatchMaxThreads {
		globalDispatchRunning++
		go propGlobalDispatchThread()
	}
}

// GlobalDispatchBarrier blocks until the global dispatch queue is
// drained and no worker is mid-dispatch. A psd stays on
// globalDispatchDispatching for the whole duration of dispatchOne, so
// observing both lists empty under globalDispatchMutex means every
// earlier dispatch has re-acquired the mutex — which establishes
// happens-before between dispatch-thread writes and subsequent reads
// by the caller. Go-only test/sync utility (C has no equivalent — its
// UI thread model never needs it).
func GlobalDispatchBarrier() {
	for {
		globalDispatchMutex.Lock()
		quiescent := len(globalDispatchQueue) == 0 &&
			len(globalDispatchDispatching) == 0
		globalDispatchMutex.Unlock()
		if quiescent {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

// courierEnqueue routes a notification to the subscription's dispatch
// target according to its dispatch mode.
// C: courier_enqueue0 / prop_courier_enqueue (prop_core.c:1198)
func courierEnqueue(s *Subscription, n *Notification) {
	switch s.dispatchMode {
	case DispatchModeCourier:
		s.courier.Enqueue(n) // C: TAILQ_INSERT_TAIL + courier_notify

	case DispatchModeGlobal:
		globalDispatchMutex.Lock()
		psd := s.hpsDispatch
		if psd == nil {
			psd = &propSubDispatch{}
			s.hpsDispatch = psd
			globalDispatchQueue = append(globalDispatchQueue, psd)
			propGlobalDispatchWakeup()
		}
		psd.notifications = append(psd.notifications, n)
		globalDispatchMutex.Unlock()

	case DispatchModeGroup:
		psd := s.hpsDispatch
		globalDispatchMutex.Lock()
		if len(psd.notifications) == 0 {
			globalDispatchQueue = append(globalDispatchQueue, psd)
			propGlobalDispatchWakeup()
		}
		psd.notifications = append(psd.notifications, n)
		globalDispatchMutex.Unlock()
	}
}

// notifyInvoke invokes the subscription callback for a notification and
// releases the notification's payload references.
// C: notify_invoke (prop_core.c:767)
//
// Upstream quirk documented, not reproduced: for PROP_DESTROYED and
// PROP_REQ_DELETE the non-trampoline branch passes n->hpn_flags (stale
// pool memory — prop_notify_destroyed never initializes it) as the event
// argument instead of n->hpn_event (prop_core.c:858). The trampoline
// branch passes hpn_event correctly, which is what every real consumer
// relies on; Go delivers the real event type in both cases.
func notifyInvoke(s *Subscription, n *Notification) {
	// C: cb(opaque, event, ..., s->hps_user_int) as last arg
	// EXCEPT for PROP_VALUE_PROP which has NO user_int (C special case).
	if s.callback != nil {
		if n.noUserInt {
			// C: cb(opaque, PROP_VALUE_PROP, prop) — NO user_int
			s.callback(s.opaque, n.event, n.prop)
		} else {
			// C: cb(opaque, event, ..., user_int)
			s.callback(s.opaque, n.event, append(n.args, s.userInt)...)
		}
	}
	// C: notify_invoke releases payload refs inside its switch
	// (rstr_release / prop_ref_dec / event_release).
	freeNotificationPayload(n)
}

// dispatchOne dispatches a single notification, honouring the
// subscription's lock manager. Returns 1 if the lock could not be
// acquired with LockmgrTry (the notification stays queued).
// C: prop_dispatch_one (prop_core.c:919)
func dispatchOne(n *Notification, lockmode LockmgrOp) int {
	s := n.sub
	// C: assert((s->hps_flags & PROP_SUB_INTERNAL) == 0)

	if s.hpsLock != nil {
		if s.hpsLockmgr.Fn(s.hpsLock, lockmode) != 0 {
			// C: assert(lockmode == LOCKMGR_TRY)
			return 1
		}
	}

	if !s.active.Load() {
		// C: hps_zombie — free payload, skip callback
		if s.hpsLock != nil {
			s.hpsLockmgr.Fn(s.hpsLock, LockmgrUnlock)
		}
		freeNotificationPayload(n)
		return 0
	}

	notifyInvoke(s, n)

	if s.hpsLock != nil {
		s.hpsLockmgr.Fn(s.hpsLock, LockmgrUnlock)
	}
	return 0
}

// DispatchGroup is a shared dispatch queue (C: prop_sub_dispatch_t used
// as a PROP_TAG_DISPATCH_GROUP). Member subscriptions' notifications are
// drained by the global dispatch workers in shared FIFO order.
// C: prop_dispatch_group_create / prop_dispatch_group_destroy (prop_core.c:5851)
type DispatchGroup = propSubDispatch

// NewDispatchGroup creates a dispatch group (C: prop_dispatch_group_create).
func NewDispatchGroup() *DispatchGroup {
	return &propSubDispatch{refCount: 1}
}

// DispatchGroupDestroy releases the group's own reference.
// C: prop_dispatch_group_destroy → prop_psd_release
func DispatchGroupDestroy(g *DispatchGroup) {
	propPsdRelease(g)
}

// propPsdRelease decrements a psd refcount (C: prop_psd_release).
func propPsdRelease(psd *propSubDispatch) {
	psd.refCount--
	// C: if(psd->psd_refcount == 0) pool_put(psd_pool, psd) — Go GC
}

// Notification represents a queued property notification.
// C: prop_notify_t (prop_i.h:82-122)
//
// A notification holds:
//   - a reference on the subscription (hps_refcount) to keep the sub alive
//   - a reference on the prop (hp_refcount) for child/value events to keep
//     the prop's memory alive while the notification is pending
//   - a snapshot of the value (string, int, float, etc.) so the callback
//     sees a consistent value even if the prop changes before dispatch
type Notification struct {
	sub       *Subscription
	event     EventType
	prop      *Prop    // for ADD_CHILD, DEL_CHILD, SET_PROP, VALUE_PROP, etc. (holds refcount)
	extEvent  ExtEvent // for EventExtEvent: owns one event reference (C: hpn_ext_event)
	args      []any    // snapshot of event-specific args (non-owning)
	expedite  bool
	noUserInt bool // EventValueProp: C does NOT pass user_int (special case)
}

// Courier is a notification queue with FIFO ordering and async dispatch.
// C: prop_courier_t (prop_i.h:48-75)
//
// The courier has two queues:
//   - queueExp: expedited notifications (PROP_SUB_EXPEDITE)
//   - queueNor: normal notifications
//
// Expedited notifications are dispatched before normal ones.
// The courier can be polled (non-blocking) or waited (blocking).
type Courier struct {
	mu       sync.Mutex
	cond     *sync.Cond
	queueExp []*Notification
	queueNor []*Notification
	refcount int32
	name     string
	running  bool
	stopped  bool
	woken    bool
	threadMu sync.Mutex
	// notifyFn is called when a notification is enqueued and no cond is available.
	// C: pc_notify callback (prop_courier_create_notify)
	notifyFn     func(opaque any)
	notifyOpaque any

	// entryLock is the lock acquired while dispatching this courier's
	// notifications (C: pc_entry_lock — set by prop_courier_create_thread).
	// nil for passive/waitable/notify couriers (C: NULL entrymutex).
	entryLock any

	// entryLockmgr is the lock manager for entryLock (C: pc_lockmgr).
	// nil → the subscription's own lockmgr applies (C: pc->pc_lockmgr ?: lockmgr).
	entryLockmgr *Lockmgr
}

// NewCourier creates a new courier (C: prop_courier_create).
func NewCourier(name string) *Courier {
	c := &Courier{
		refcount: 1,
		name:     name,
	}
	c.cond = sync.NewCond(&c.mu)
	return c
}

// NewCourierNotify creates a courier with a notify callback (C: prop_courier_create_notify).
// When a notification is enqueued, notifyFn is called instead of signaling a cond.
// This is used for integrating with external event loops (glib, JNI, asyncio).
func NewCourierNotify(name string, notifyFn func(opaque any), opaque any) *Courier {
	c := &Courier{
		refcount:     1,
		name:         name,
		notifyFn:     notifyFn,
		notifyOpaque: opaque,
	}
	// No cond — notifyFn is used for wakeup
	return c
}

// Retain increments the courier refcount (C: pc_refcount++).
func (c *Courier) Retain() {
	atomic.AddInt32(&c.refcount, 1)
}

// GetRefcount returns the current refcount (for testing/diagnostics).
func (c *Courier) GetRefcount() int32 {
	return atomic.LoadInt32(&c.refcount)
}

// Release decrements the courier refcount. When it hits 0, the courier
// is destroyed (C: prop_courier_destroy).
func (c *Courier) Release() {
	if atomic.AddInt32(&c.refcount, -1) == 0 {
		c.Destroy()
	}
}

// Destroy cleans up the courier, draining any pending notifications.
// C: prop_courier_destroy (prop_core.c:5335)
func (c *Courier) Destroy() {
	c.mu.Lock()
	c.stopped = true
	// Drain pending notifications, freeing payloads
	for _, n := range c.queueExp {
		freeNotification(n)
	}
	for _, n := range c.queueNor {
		freeNotification(n)
	}
	c.queueExp = nil
	c.queueNor = nil
	c.mu.Unlock()
	c.cond.Broadcast()
}

// Enqueue adds a notification to the courier queue.
// C: courier_enqueue0 (prop_core.c:1195) + courier_notify (prop_core.c:1182)
func (c *Courier) Enqueue(n *Notification) {
	c.mu.Lock()
	if c.stopped {
		// Courier destroyed but a stale subscription still points at
		// it (C keeps hps_dispatch too — freed-pointer UAF upstream).
		// Drop the notification instead of growing a dead queue.
		c.mu.Unlock()
		freeNotification(n)
		return
	}
	if n.expedite {
		c.queueExp = append(c.queueExp, n)
	} else {
		c.queueNor = append(c.queueNor, n)
	}
	c.mu.Unlock()
	// C: courier_notify(pc) — if has_cond: signal; else if notify: call notify
	if c.notifyFn != nil {
		c.notifyFn(c.notifyOpaque)
	} else {
		c.cond.Signal()
	}
}

// Poll drains all pending notifications and dispatches them.
// C: prop_courier_poll (prop_core.c:5383)
//
// Expedited notifications are dispatched first, then normal ones.
// Returns the number of notifications dispatched.
func (c *Courier) Poll() int {
	c.mu.Lock()
	exp := c.queueExp
	nor := c.queueNor
	c.queueExp = nil
	c.queueNor = nil
	c.mu.Unlock()

	count := 0
	for _, n := range exp {
		dispatchNotification(n)
		count++
	}
	for _, n := range nor {
		dispatchNotification(n)
		count++
	}
	return count
}

// PollOne dequeues and dispatches a single notification (expedited first).
// Returns false if the queue was empty.
// C: one iteration of the prop_global_dispatch_thread loop body —
// TAILQ_REMOVE(psd_notifications head) + prop_dispatch_one (prop_core.c:1078-1086).
// Group-dispatch consumers wrap each PollOne in their lockmgr lock/unlock,
// matching C's per-notification hps_lockmgr acquire/release.
func (c *Courier) PollOne() bool {
	c.mu.Lock()
	var n *Notification
	if len(c.queueExp) > 0 {
		n = c.queueExp[0]
		c.queueExp = c.queueExp[1:]
	} else if len(c.queueNor) > 0 {
		n = c.queueNor[0]
		c.queueNor = c.queueNor[1:]
	}
	c.mu.Unlock()
	if n == nil {
		return false
	}
	dispatchNotification(n)
	return true
}

// WaitReady blocks until at least one notification is pending or the
// courier is stopped. Returns true if a notification is ready for PollOne.
// C: the cond-wait portion of the dispatch thread loop (prop_core.c:1070-1076).
func (c *Courier) WaitReady(timeoutMs int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if timeoutMs <= 0 {
		for len(c.queueExp) == 0 && len(c.queueNor) == 0 && !c.stopped && !c.woken {
			c.cond.Wait()
		}
	} else {
		deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
		done := make(chan struct{})
		go func() {
			timer := time.NewTimer(time.Duration(timeoutMs) * time.Millisecond)
			select {
			case <-timer.C:
				c.mu.Lock()
				c.cond.Broadcast()
				c.mu.Unlock()
			case <-done:
				timer.Stop()
			}
		}()
		for len(c.queueExp) == 0 && len(c.queueNor) == 0 && !c.stopped && !c.woken {
			if time.Until(deadline) <= 0 {
				break
			}
			c.cond.Wait()
		}
		close(done)
	}
	c.woken = false
	return len(c.queueExp) > 0 || len(c.queueNor) > 0
}

// Stop marks the courier stopped and wakes any waiters.
// C: courier destruction wakes hts_cond_waiters (prop_courier_destroy).
func (c *Courier) Stop() {
	c.mu.Lock()
	c.stopped = true
	c.mu.Unlock()
	c.cond.Broadcast()
}

// Wake nudges a thread parked in Wait/WaitTimed to return (with an
// empty drain) so its caller can re-check loop conditions — the
// C g_main_context_wakeup that poked the mainloop so non-prop work
// (e.g. linux_webpopup_check jobs) could be drained. The flag is
// consumed by the next wait exit so it is not lost if no one is
// currently parked.
func (c *Courier) Wake() {
	c.mu.Lock()
	c.woken = true
	c.cond.Broadcast()
	c.mu.Unlock()
}

// Wait blocks until at least one notification is available, then drains
// and dispatches all pending notifications.
// C: prop_courier_wait_and_dispatch (prop_core.c:5304)
func (c *Courier) Wait() {
	c.mu.Lock()
	for len(c.queueExp) == 0 && len(c.queueNor) == 0 &&
		!c.stopped && !c.woken {
		c.cond.Wait()
	}
	c.woken = false
	exp := c.queueExp
	nor := c.queueNor
	c.queueExp = nil
	c.queueNor = nil
	c.mu.Unlock()

	for _, n := range exp {
		dispatchNotification(n)
	}
	for _, n := range nor {
		dispatchNotification(n)
	}
}

// WaitTimed blocks until at least one notification is available or timeout
// elapses, then drains and dispatches all pending notifications.
// C: prop_courier_wait (prop_core.c:5281)
// timeout is in milliseconds. 0 = infinite wait.
func (c *Courier) WaitTimed(timeoutMs int) bool {
	c.mu.Lock()
	if len(c.queueExp) == 0 && len(c.queueNor) == 0 && !c.stopped && !c.woken {
		if timeoutMs <= 0 {
			for len(c.queueExp) == 0 && len(c.queueNor) == 0 && !c.stopped && !c.woken {
				c.cond.Wait()
			}
		} else {
			// Use a timeout goroutine to wake up the cond; the loop
			// re-checks the deadline on every broadcast so a bare
			// wake can't park us again past the timeout.
			done := make(chan struct{})
			go func() {
				timer := time.NewTimer(time.Duration(timeoutMs) * time.Millisecond)
				select {
				case <-timer.C:
					c.mu.Lock()
					c.cond.Broadcast()
					c.mu.Unlock()
				case <-done:
					timer.Stop()
				}
			}()
			deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
			for len(c.queueExp) == 0 && len(c.queueNor) == 0 && !c.stopped &&
				!c.woken && time.Now().Before(deadline) {
				c.cond.Wait()
			}
			close(done)
		}
	}
	c.woken = false
	exp := c.queueExp
	nor := c.queueNor
	c.queueExp = nil
	c.queueNor = nil
	c.mu.Unlock()

	for _, n := range exp {
		dispatchNotification(n)
	}
	for _, n := range nor {
		dispatchNotification(n)
	}
	return len(exp)+len(nor) > 0
}

// PendingCount returns the total number of pending notifications.
func (c *Courier) PendingCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.queueExp) + len(c.queueNor)
}

// PollTimed drains pending notifications with a time budget.
// C: prop_courier_poll_timed (prop_core.c:5398)
// maxtimeMs = -1 means drain all (same as Poll).
// Returns the number of notifications dispatched.
func (c *Courier) PollTimed(maxtimeMs int) int {
	if maxtimeMs < 0 {
		return c.Poll()
	}
	c.mu.Lock()
	exp := c.queueExp
	nor := c.queueNor
	c.queueExp = nil
	c.queueNor = nil
	c.mu.Unlock()

	deadline := time.Now().Add(time.Duration(maxtimeMs) * time.Millisecond)
	count := 0
	for _, n := range exp {
		dispatchNotification(n)
		count++
		if time.Now().After(deadline) {
			// Re-enqueue remaining normal notifications
			if len(nor) > 0 {
				c.mu.Lock()
				c.queueNor = append(nor, c.queueNor...)
				c.mu.Unlock()
			}
			return count
		}
	}
	for _, n := range nor {
		dispatchNotification(n)
		count++
		if time.Now().After(deadline) {
			break
		}
	}
	return count
}

// dispatchNotification dispatches a single notification to its subscription's
// callback. After dispatch, the notification's payload is freed and the
// subscription's refcount is decremented.
// C: prop_notify_dispatch → prop_dispatch_one(n, LOCKMGR_LOCK) + free
// (prop_core.c:954)
func dispatchNotification(n *Notification) {
	if n == nil || n.sub == nil {
		return
	}
	dispatchOne(n, LockmgrLock)
	freeNotification(n)
}

// freeNotificationPayload releases the notification's payload references
// (extEvent + prop), without touching the subscription refcount.
// C: prop_notify_free_payload (prop_core.c:899)
func freeNotificationPayload(n *Notification) {
	// Release extEvent reference if held (C: event_release(n->hpn_ext_event))
	// This must happen for ALL notifications, including zombie ones,
	// to prevent event leaks when subscribers unsubscribe before Poll.
	if n.extEvent != nil {
		n.extEvent.Release()
		n.extEvent = nil
	}
	// Release prop memory ref if held (C: prop_ref_dec_locked(n->hpn_prop))
	if n.prop != nil {
		atomic.AddInt32(&n.prop.refCount, -1)
		n.prop = nil
	}
}

// freeNotification releases the notification's payload references and
// decrements the subscription's refcount.
// C: prop_notify_free — payload + prop_sub_ref_dec_locked + pool_put
func freeNotification(n *Notification) {
	if n == nil {
		return
	}
	freeNotificationPayload(n)
	// Decrement subscription refcount (C: prop_sub_ref_dec_locked)
	if n.sub != nil {
		subRefDec(n.sub)
		n.sub = nil
	}
	n.args = nil
}

// subRefDec decrements the subscription's refcount. When it hits 0,
// the subscription is freed: the lock manager gets LOCKMGR_RELEASE and
// a GROUP psd loses its member reference (C: prop_sub_ref_dec_locked,
// prop_core.c:461).
func subRefDec(s *Subscription) {
	if s == nil {
		return
	}
	newRef := atomic.AddInt32(&s.refCount, -1)
	if newRef <= 0 {
		// C: prop_ref_dec_locked(s->hps_origin) + pot refs freed
		// (prop_sub_ref_dec_locked → free path)
		releaseSubOriginRefs(s)
		// C: s->hps_lockmgr(s->hps_lock, LOCKMGR_RELEASE)
		if s.hpsLockmgr != nil {
			s.hpsLockmgr.Fn(s.hpsLock, LockmgrRelease)
		}
		// C: if(GROUP) prop_psd_release(s->hps_dispatch)
		if s.dispatchMode == DispatchModeGroup && s.hpsDispatch != nil {
			propPsdRelease(s.hpsDispatch)
		}
	}
}

// subRefInc increments the subscription's refcount.
// C: atomic_inc(&s->hps_refcount) in prop_get_notify
func subRefInc(s *Subscription) {
	if s == nil {
		return
	}
	atomic.AddInt32(&s.refCount, 1)
}

// notifySub dispatches a notification to a single subscriber.
// If the subscription has a courier, the notification is enqueued for async
// dispatch. Otherwise, the callback is invoked synchronously.
//
// C: prop_build_notify_value / prop_build_notify_child (prop_core.c:1265, 1564)
//   - If PROP_SUB_INTERNAL or direct mode: invoke callback directly
//   - If courier mode: create prop_notify_t, enqueue on courier
//
// For courier mode, this function:
//  1. Creates a Notification with a snapshot of the event data
//  2. Increments sub.refCount (keeps sub alive while notification pending)
//  3. If the event carries a prop (ADD_CHILD, DEL_CHILD, etc.), increments
//     prop.refCount (keeps prop memory alive while notification pending)
//  4. Enqueues the notification on the courier
func notifySub(sub *Subscription, eventType EventType, prop *Prop, args ...any) {
	if sub == nil || !sub.active.Load() {
		return
	}

	// Extract extEvent for EventExtEvent (C: n->hpn_ext_event = e)
	// The caller (SendExtEvent) is responsible for AddRef.
	// notifySub transfers ownership to the Notification; it does NOT
	// do a second AddRef.
	var extEv ExtEvent
	if eventType == EventExtEvent && len(args) > 0 {
		if e, ok := args[0].(ExtEvent); ok {
			extEv = e
		}
	}

	// C: non-INTERNAL subscriptions go through courier_enqueue0, which
	// routes by hps_dispatch_mode (COURIER → pc queue, GLOBAL → per-sub
	// psd, GROUP → shared psd).
	if !sub.internal &&
		(sub.dispatchMode == DispatchModeCourier && sub.courier != nil ||
			sub.dispatchMode == DispatchModeGlobal ||
			sub.dispatchMode == DispatchModeGroup && sub.hpsDispatch != nil) {
		// FIX F: Always copy args to ensure slice isolation between
		// notifications. Previously when prop==nil, fullArgs=args
		// shared the backing array across all subscriber notifications.
		var fullArgs []any
		if prop != nil {
			fullArgs = make([]any, 0, len(args)+1)
			fullArgs = append(fullArgs, prop)
			fullArgs = append(fullArgs, args...)
		} else {
			fullArgs = make([]any, len(args))
			copy(fullArgs, args)
		}
		n := &Notification{
			sub:      sub,
			event:    eventType,
			args:     fullArgs,
			expedite: sub.expedite, // C: PROP_SUB_EXPEDITE flag
		}
		// FIX C: Transfer event ownership to Notification
		// The Notification now owns the event reference (from SendExtEvent's AddRef).
		// freeNotification will Release it after the callback returns.
		if extEv != nil {
			n.extEvent = extEv
		}
		// If the event carries a prop, hold a memory ref on it
		// C: atomic_inc(&p->hp_refcount) in prop_build_notify_child
		if prop != nil {
			atomic.AddInt32(&prop.refCount, 1)
			n.prop = prop
		}
		// Increment sub refcount (C: atomic_inc(&s->hps_refcount))
		subRefInc(sub)
		// C: prop_courier_enqueue → courier_enqueue0
		courierEnqueue(sub, n)
		return
	}

	// Direct mode: invoke callback synchronously
	// C: PROP_SUB_INTERNAL path in prop_build_notify_value / prop_build_notify_child
	// C: passes s->hps_user_int as last arg in all events
	if sub.callback != nil {
		// FIX E: For EventExtEvent, the framework owns the event reference.
		// Release it after the callback returns (matching freeNotification
		// behavior in courier mode). The callback receives a BORROWED ref
		// and must NOT call Release.
		if extEv != nil {
			defer extEv.Release()
		}
		// Build full args: [prop?, ...args, userInt]
		var fullArgs []any
		if prop != nil {
			fullArgs = make([]any, 0, len(args)+2)
			fullArgs = append(fullArgs, prop)
		} else {
			fullArgs = make([]any, 0, len(args)+1)
		}
		fullArgs = append(fullArgs, args...)
		// C: cb(s->hps_opaque, event, ..., s->hps_user_int)
		fullArgs = append(fullArgs, sub.userInt)
		sub.callback(sub.opaque, eventType, fullArgs...)
	}
}

// notifyVoidSub dispatches a SET_VOID notification matching C's prop_notify_void.
// C (prop_core.c:1468-1489):
//
//	Direct (INTERNAL): cb(opaque, PROP_SET_VOID, value_prop, user_int)
//	  — passes value_prop as arg (non-refcounted)
//	Courier: prop_get_notify(s) + n->hpn_event = PROP_SET_VOID + enqueue
//	  — n->hpn_prop NOT set (no prop ref)
//	  — notify_invoke SET_VOID: cb(opaque, PROP_SET_VOID, user_int) — no prop
func notifyVoidSub(sub *Subscription, prop *Prop) {
	if sub == nil || !sub.active.Load() {
		return
	}

	// C: PROP_SUB_IGNORE_VOID — skip notification when prop is VOID
	// C: prop_core.c:1270, 1470
	if sub.ignoreVoid {
		return
	}

	if !sub.internal &&
		(sub.dispatchMode == DispatchModeCourier && sub.courier != nil ||
			sub.dispatchMode == DispatchModeGlobal ||
			sub.dispatchMode == DispatchModeGroup && sub.hpsDispatch != nil) {
		// Queued mode: enqueue SET_VOID without prop ref
		// C: n = prop_get_notify(s); n->hpn_event = PROP_SET_VOID; enqueue
		// n->hpn_prop is NOT set (no prop ref acquired)
		n := &Notification{
			sub:      sub,
			event:    EventSetVoid,
			args:     nil, // C: notify_invoke SET_VOID passes only user_int
			expedite: sub.expedite,
		}
		// No prop ref (n.prop stays nil)
		subRefInc(sub)
		courierEnqueue(sub, n)
		return
	}

	// Direct mode: callback with prop as arg
	// C: cb(opaque, PROP_SET_VOID, value_prop, user_int)
	if sub.callback != nil {
		sub.callback(sub.opaque, EventSetVoid, prop, sub.userInt)
	}
}

// notifySubs dispatches a notification to all active value subscribers of a prop.
// This is the central function used by all prop mutations to notify subscribers.
//
// C: prop_notify_value / prop_notify_child / prop_notify_simple
// All these iterate hp_value_subscriptions (the value sub list).
func notifySubs(p *Prop, eventType EventType, prop *Prop, args ...any) {
	if p == nil {
		return
	}
	p.mu.RLock()
	// C: LIST_FOREACH(s, &p->hp_value_subscriptions, hps_value_prop_link)
	subsCopy := make([]*Subscription, len(p.valueSubs))
	copy(subsCopy, p.valueSubs)
	p.mu.RUnlock()

	for _, sub := range subsCopy {
		notifySub(sub, eventType, prop, args...)
	}
}
