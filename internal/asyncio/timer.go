package asyncio

import "slices"

// C: src/networking/asyncio_posix.c — asyncio_timer (asyncio_posix.c:270-329)

// C: typedef struct asyncio_timer (asyncio.h:76-80)
type Timer struct {
	expire int64 // C: at_expire — 0 if disarmed
	fn     func(opaque any)
	opaque any

	aio *AsyncIO // owning loop (C: global asyncio state)
}

// C: void asyncio_timer_init (asyncio_posix.c:272-279)
// Setup — C: asyncio_timer_init. The timer attaches to the loop passed
// here (Go: aio carries the timer list that C keeps in a global).
func (at *Timer) Setup(aio *AsyncIO, fn func(opaque any), opaque any) {
	at.aio = aio
	at.expire = 0 // C: TAILQ insertion is disarm-equivalent
	at.fn = fn
	at.opaque = opaque
}

// C: void asyncio_timer_arm (asyncio_posix.c:285-302)
func (at *Timer) Arm(expire int64) {
	if at.expire != 0 {
		// C: TAILQ_REMOVE
		for i, t := range at.aio.timers {
			if t == at {
				at.aio.timers = slices.Delete(at.aio.timers, i, i+1)
				break
			}
		}
	}

	at.expire = expire

	// C: TAILQ_FOREACH → TAILQ_INSERT_BEFORE / TAILQ_INSERT_TAIL —
	// list kept sorted by expiry, earliest first
	idx := len(at.aio.timers)
	for i, t := range at.aio.timers {
		if t.expire > expire {
			idx = i
			break
		}
	}
	at.aio.timers = append(at.aio.timers, nil)
	copy(at.aio.timers[idx+1:], at.aio.timers[idx:])
	at.aio.timers[idx] = at
}

// C: void asyncio_timer_arm_delta_sec (asyncio_posix.c:308-311)
func (at *Timer) ArmDeltaSec(delta int) {
	at.Arm(at.aio.now + int64(delta)*1000000)
}

// C: void asyncio_timer_disarm (asyncio_posix.c:317-328)
func (at *Timer) Disarm() {
	if at.expire == 0 {
		return
	}
	for i, t := range at.aio.timers {
		if t == at {
			at.aio.timers = slices.Delete(at.aio.timers, i, i+1)
			break
		}
	}
	at.expire = 0
}

// C: asyncio_timer_is_armed inline — at_expire != 0
func (at *Timer) IsArmed() bool { return at.expire != 0 }
