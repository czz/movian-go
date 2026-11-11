// webpopup_cookiewait.go — the completion rules of a popup that waits for
// a cookie. No build tags and no cgo: the CEF session only wires it.
package ui

import (
	"sync"
	"time"
)

const (
	// WebpopupCookieFound — the awaited cookie is present (popup wait).
	WebpopupCookieFound = 3
	// WebpopupCookieTimeout — the wait ran out before the cookie appeared.
	WebpopupCookieTimeout = 4

	// DefaultCookieWaitTimeout bounds a wait that gave no timeout. The
	// maximum matches the Android popup session limit (PopupRequest).
	DefaultCookieWaitTimeout = 5 * time.Minute
	MaxCookieWaitTimeout     = 30 * time.Minute

	// cookieWaitPollEvery re-checks the jar between page loads: a cookie
	// can be set by a script without any navigation.
	cookieWaitPollEvery = 2 * time.Second
)

// WebpopupCookieWait — ask a popup to finish once a nonempty cookie Name is
// available for URL. WebpopupCreate verifies URL is the opener's exact HTTPS
// origin; otherwise the popup does not start (WebpopupLoadError).
type WebpopupCookieWait struct {
	URL     string
	Name    string
	Timeout time.Duration // 0 = DefaultCookieWaitTimeout; capped at Max

	target cookieURL // set by WebpopupCreate
	// port is the non-default port of the validated URL ("" for 443). The
	// target's host is canonical and carries no port; the owner context needs
	// the port to name the origin the popup ended on.
	port string
}

// cookieWaitTimeout clamps a requested wait to (0, Max]; <=0 → default.
func cookieWaitTimeout(d time.Duration) time.Duration {
	switch {
	case d <= 0:
		return DefaultCookieWaitTimeout
	case d > MaxCookieWaitTimeout:
		return MaxCookieWaitTimeout
	}
	return d
}

// cookieWaiter — the first-result arbiter of one popup session. Every
// terminal source — cookie found, timeout, trap URL, user close, load error —
// resolves through it: the first Resolve or Fire fixes the result for good,
// disarms the timers, and every later source gets that result back. Safe
// for concurrent use.
type cookieWaiter struct {
	mu      sync.Mutex
	done    bool
	result  int
	timeout *time.Timer
	poll    *time.Timer
	onFire  func(code int)
	check   func()
	every   time.Duration
}

// startCookieWaiter arms the timeout and the poll. check is called (on a
// timer goroutine) every `every` until the waiter is resolved; it must only
// queue work. onFire(code) runs for a won Fire only (found, timeout); it
// must queue the session finish, which resolves again to the same code.
func startCookieWaiter(timeout, every time.Duration, check func(),
	onFire func(code int)) *cookieWaiter {
	w := &cookieWaiter{onFire: onFire, check: check, every: every}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.timeout = time.AfterFunc(cookieWaitTimeout(timeout), func() {
		w.Fire(WebpopupCookieTimeout)
	})
	if check != nil && every > 0 {
		w.poll = time.AfterFunc(every, w.tick)
	}
	return w
}

func (w *cookieWaiter) tick() {
	w.mu.Lock()
	if w.done {
		w.mu.Unlock()
		return
	}
	w.poll.Reset(w.every)
	w.mu.Unlock()
	w.check()
}

// resolveLocked fixes the result as code if none is fixed yet and reports
// whether it did. w.mu is held.
func (w *cookieWaiter) resolveLocked(code int) bool {
	if w.done {
		return false
	}
	w.done = true
	w.result = code
	w.timeout.Stop()
	if w.poll != nil {
		w.poll.Stop()
	}
	return true
}

// Resolve claims the result with code unless one is already fixed, and
// returns the fixed result: code if this call won, else the earlier winner's.
func (w *cookieWaiter) Resolve(code int) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.resolveLocked(code)
	return w.result
}

// Fire resolves with code and, if it won, runs onFire once. It reports
// whether it won.
func (w *cookieWaiter) Fire(code int) bool {
	w.mu.Lock()
	won := w.resolveLocked(code)
	w.mu.Unlock()
	if won {
		w.onFire(code)
	}
	return won
}

// cookieRead — one WebpopupCookieList call as the platform hook sees it:
// the result channel and the way for the caller to abandon the read. If the
// caller gives up (timeout) the platform detaches the read, so a late CEF
// callback does nothing and no state outlives the call.
type cookieRead struct {
	done      chan []WebpopupCookie // buffered (1)
	owner     string                // requesting plugin ("" = none)
	name      string                // wanted cookie name (Android only)
	mu        sync.Mutex
	abandoned bool
	cancel    func()
}

func newCookieRead() *cookieRead {
	return &cookieRead{done: make(chan []WebpopupCookie, 1)}
}

// Attach registers the platform's cancel function. It reports false when
// the read was abandoned before the platform started it: do not start it.
func (r *cookieRead) Attach(cancel func()) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.abandoned {
		return false
	}
	r.cancel = cancel
	return true
}

// Abandon is the caller giving up. It calls the cancel function once, if
// one is attached; idempotent.
func (r *cookieRead) Abandon() {
	r.mu.Lock()
	r.abandoned = true
	cancel := r.cancel
	r.cancel = nil
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
