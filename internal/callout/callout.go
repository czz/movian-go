package callout

import (
	"cmp"
	"slices"
	"sync"
	"time"

	propcore "github.com/czz/movian-go/internal/prop"
)

// Callout represents a timed callback
type Callout struct {
	callback    func(*Callout, any)
	opaque      any
	delta       int64
	deadline    int64
	armedByFile string
	armedByLine int
	lockmgr     LockManager
}

// LockManager manages locking for managed callouts
type LockManager interface {
	Lock(opaque any)
	Unlock(opaque any)
	Retain(opaque any)
	Release(opaque any)
}

// CalloutSystem manages timed callbacks
type CalloutSystem struct {
	mutex       sync.Mutex
	cond        *sync.Cond
	callouts    []*Callout
	initialized bool
	pm          *propcore.PropManager

	// Global clock
	calloutClock *Callout
	propClock    *propcore.Prop
}

// NewCalloutSystem creates a new callout system
func NewCalloutSystem(pm *propcore.PropManager) *CalloutSystem {
	cs := &CalloutSystem{
		callouts:    make([]*Callout, 0),
		pm:          pm,
		initialized: true,
	}
	// cond MUST share cs.mutex so that Wait() releases/acquires the same mutex
	// that Lock()/Unlock() use elsewhere. Previous code used a separate anonymous
	// mutex → race: Wait() unlocked the anon mutex while Fini() locked cs.mutex,
	// causing "unlock of unlocked mutex" fatal under -race.
	cs.cond = sync.NewCond(&cs.mutex)

	// Start callout loop in background
	go cs.loop()

	// Create global clock property
	cs.propClock = pm.Create("clock")
	cs.calloutClock = &Callout{}

	// Arm clock callout
	cs.Arm(cs.calloutClock, func(c *Callout, opaque any) {
		cs.updateClockProps()
	}, nil, 1)

	return cs
}

// Fini shuts down the callout system
func (cs *CalloutSystem) Fini() {
	cs.mutex.Lock()
	defer cs.mutex.Unlock()

	// Disarm all callouts
	for _, c := range cs.callouts {
		if c.callback != nil {
			c.callback = nil
		}
	}
	cs.callouts = nil
	cs.initialized = false

	// Signal stop
	cs.cond.Broadcast()
}

// loop is the background goroutine that executes callouts
func (cs *CalloutSystem) loop() {
	for {
		cs.mutex.Lock()
		if !cs.initialized {
			cs.mutex.Unlock()
			return
		}
		now := time.Now().UnixNano() / 1000 // microseconds

		// Execute all expired callouts
		for len(cs.callouts) > 0 && cs.callouts[0].deadline <= now {
			c := cs.callouts[0]
			cc := c.callback
			lm := c.lockmgr
			opaque := c.opaque

			// Remove from list
			cs.callouts = cs.callouts[1:]
			c.callback = nil

			cs.mutex.Unlock()

			if lm != nil {
				lm.Lock(opaque)
			}

			cc(c, opaque)

			if lm != nil {
				lm.Unlock(opaque)
				lm.Release(opaque)
			}

			cs.mutex.Lock()

			ts := time.Now().UnixNano() / 1000
			now = ts
		}

		// Wait for next callout or indefinitely if no callouts
		if len(cs.callouts) > 0 {
			timeout := (cs.callouts[0].deadline - now + 999) / 1000 // convert to milliseconds
			if timeout > 0 {
				// Simple sleep-based timeout
				cs.mutex.Unlock()
				time.Sleep(time.Duration(timeout) * time.Millisecond)
				cs.mutex.Lock()
			}
		} else {
			// cond.Wait() releases the lock, allowing Fini() to acquire it,
			// set initialized=false, and Broadcast. After Wakeup we must
			// re-check initialized BEFORE unlocking to avoid double-unlock
			// race with Fini's deferred Unlock.
			cs.cond.Wait()
			if !cs.initialized {
				cs.mutex.Unlock()
				return
			}
		}
		cs.mutex.Unlock()
	}
}

// Arm arms a callout with a callback after delta seconds
func (cs *CalloutSystem) Arm(c *Callout, callback func(*Callout, any), opaque any, delta int) {
	cs.ArmHires(c, callback, opaque, int64(delta)*1000000)
}

// ArmHires arms a callout with a callback after delta microseconds
func (cs *CalloutSystem) ArmHires(c *Callout, callback func(*Callout, any), opaque any, delta int64) {
	cs.arm0(c, callback, opaque, delta, nil, "", 0)
}

// ArmManaged arms a managed callout with lock manager
func (cs *CalloutSystem) ArmManaged(c *Callout, callback func(*Callout, any), opaque any, delta int64, lm LockManager, file string, line int) {
	cs.arm0(c, callback, opaque, delta, lm, file, line)
}

// arm0 is the internal arm function
func (cs *CalloutSystem) arm0(c *Callout, callback func(*Callout, any), opaque any, delta int64, lm LockManager, file string, line int) {
	cs.mutex.Lock()
	defer cs.mutex.Unlock()

	if c == nil {
		c = &Callout{}
	} else {
		if c.callback != nil {
			// Remove from list if already armed
			cs.removeCallout(c)
		} else if lm != nil {
			lm.Retain(opaque)
		}
	}

	c.callback = callback
	c.opaque = opaque
	c.delta = delta
	c.deadline = time.Now().UnixNano()/1000 + delta
	c.armedByFile = file
	c.armedByLine = line
	c.lockmgr = lm

	cs.insertCallout(c)
	cs.cond.Signal()
}

// Rearm changes the deadline of an already armed callout
func (cs *CalloutSystem) Rearm(c *Callout, delta int64) {
	cs.mutex.Lock()
	defer cs.mutex.Unlock()

	if c.callback != nil {
		c.deadline += delta - c.delta
		c.delta = delta
		cs.removeCallout(c)
		cs.insertCallout(c)
	}
}

// Disarm disarms a callout
func (cs *CalloutSystem) Disarm(c *Callout) {
	cs.mutex.Lock()
	defer cs.mutex.Unlock()

	if c.callback != nil {
		lm := c.lockmgr
		cs.removeCallout(c)
		c.callback = nil
		if lm != nil {
			lm.Release(c.opaque)
		}
	}
}

// removeCallout removes a callout from the sorted list
func (cs *CalloutSystem) removeCallout(c *Callout) {
	if i := slices.Index(cs.callouts, c); i >= 0 {
		cs.callouts = slices.Delete(cs.callouts, i, i+1)
	}
}

// insertCallout inserts a callout in the sorted list
func (cs *CalloutSystem) insertCallout(c *Callout) {
	i, _ := slices.BinarySearchFunc(cs.callouts, c.deadline,
		func(e *Callout, t int64) int { return cmp.Compare(e.deadline, t) })
	cs.callouts = slices.Insert(cs.callouts, i, c)
}

// getOrCreateChild gets a child property or creates it if it doesn't exist
func getOrCreateChild(pm *propcore.PropManager, parent *propcore.Prop, name string) *propcore.Prop {
	child := parent.GetChild(name)
	if child == nil {
		child = pm.CreateEx(parent, name, nil, false, true)
	}
	return child
}

// UpdateClockProps — C: callout_update_clock_props (callout.c) is called
// directly by i18n's set_timezone under #ifdef STOS. Exported wrapper.
func (cs *CalloutSystem) UpdateClockProps() {
	cs.updateClockProps()
}

// updateClockProps updates the global clock properties
func (cs *CalloutSystem) updateClockProps() {
	cs.Arm(cs.calloutClock, func(c *Callout, opaque any) {
		cs.updateClockProps()
	}, nil, 1)

	now := time.Now()
	unixTime := now.Unix()

	// Create or get child properties using helper
	valid := getOrCreateChild(cs.pm, cs.propClock, "valid")
	localTime := getOrCreateChild(cs.pm, cs.propClock, "localtimeofday")
	unixtime := getOrCreateChild(cs.pm, cs.propClock, "unixtime")
	hourProp := getOrCreateChild(cs.pm, cs.propClock, "hour")
	minuteProp := getOrCreateChild(cs.pm, cs.propClock, "minute")
	dayMinuteProp := getOrCreateChild(cs.pm, cs.propClock, "dayminute")

	if unixTime < 1000000000 {
		valid.SetInt(0)
		localTime.SetString("--:--")
		return
	}

	valid.SetInt(1)
	unixtime.SetInt(int(unixTime))

	hour := now.Hour()
	minute := now.Minute()
	dayMinute := hour*60 + minute

	hourProp.SetInt(hour)
	minuteProp.SetInt(minute)
	dayMinuteProp.SetInt(dayMinute)

	// Format time as HH:MM
	localTime.SetString(now.Format("15:04"))
}
