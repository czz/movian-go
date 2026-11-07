package ui

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fired struct {
	mu    sync.Mutex
	codes []int
}

func (f *fired) add(c int) { f.mu.Lock(); f.codes = append(f.codes, c); f.mu.Unlock() }
func (f *fired) get() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.codes...)
}

func TestCookieWaitTimeoutClamp(t *testing.T) {
	cases := []struct{ in, want time.Duration }{
		{0, DefaultCookieWaitTimeout},
		{-time.Second, DefaultCookieWaitTimeout},
		{time.Second, time.Second},
		{MaxCookieWaitTimeout, MaxCookieWaitTimeout},
		{MaxCookieWaitTimeout + time.Hour, MaxCookieWaitTimeout},
	}
	for _, c := range cases {
		if got := cookieWaitTimeout(c.in); got != c.want {
			t.Errorf("%v: got %v, want %v", c.in, got, c.want)
		}
	}
}

func TestCookieWaiterTimeoutFiresOnce(t *testing.T) {
	var f fired
	w := startCookieWaiter(20*time.Millisecond, 0, nil, f.add)
	time.Sleep(120 * time.Millisecond)
	if got := f.get(); len(got) != 1 || got[0] != WebpopupCookieTimeout {
		t.Fatalf("got %v, want one timeout", got)
	}
	if w.Fire(WebpopupCookieFound) {
		t.Fatal("Fire after the timeout must lose")
	}
	if got := f.get(); len(got) != 1 {
		t.Fatalf("second completion: %v", got)
	}
}

func TestCookieWaiterFoundBeatsTimeout(t *testing.T) {
	var f fired
	w := startCookieWaiter(30*time.Millisecond, 0, nil, f.add)
	if !w.Fire(WebpopupCookieFound) {
		t.Fatal("first Fire must win")
	}
	if w.Fire(WebpopupCookieFound) {
		t.Fatal("second Fire must lose")
	}
	time.Sleep(100 * time.Millisecond)
	if got := f.get(); len(got) != 1 || got[0] != WebpopupCookieFound {
		t.Fatalf("got %v, want one found", got)
	}
}

// A user close, a trap or a load error resolves the waiter: no onFire of
// its own, and no timeout or poll afterwards.
func TestCookieWaiterResolveCancels(t *testing.T) {
	var f fired
	var polls atomic.Int32
	w := startCookieWaiter(30*time.Millisecond, 5*time.Millisecond,
		func() { polls.Add(1) }, f.add)
	if got := w.Resolve(WebpopupClosedByUser); got != WebpopupClosedByUser {
		t.Fatalf("first Resolve = %d", got)
	}
	if got := w.Resolve(WebpopupTrappedURL); got != WebpopupClosedByUser {
		t.Fatalf("second Resolve = %d, want the first result", got)
	}
	n := polls.Load()
	time.Sleep(100 * time.Millisecond)
	if got := f.get(); len(got) != 0 {
		t.Fatalf("stopped waiter fired: %v", got)
	}
	if polls.Load() > n+1 { // a tick already running may finish
		t.Fatalf("polling continued after Stop: %d -> %d", n, polls.Load())
	}
	if w.Fire(WebpopupCookieFound) {
		t.Fatal("Fire after Resolve must lose")
	}
}

func TestCookieWaiterPollsUntilDone(t *testing.T) {
	var f fired
	var polls atomic.Int32
	w := startCookieWaiter(time.Minute, 5*time.Millisecond,
		func() { polls.Add(1) }, f.add)
	deadline := time.Now().Add(2 * time.Second)
	for polls.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if polls.Load() < 3 {
		t.Fatalf("poll ran %d times, want >= 3", polls.Load())
	}
	w.Resolve(WebpopupClosedByUser)
}

// Many racers: exactly one completion. Run with -race.
func TestCookieWaiterConcurrentFireOnce(t *testing.T) {
	for i := 0; i < 50; i++ {
		var f fired
		w := startCookieWaiter(time.Minute, 0, nil, f.add)
		var wg sync.WaitGroup
		var wins atomic.Int32
		for j := 0; j < 8; j++ {
			wg.Add(1)
			go func(code int) {
				defer wg.Done()
				if w.Fire(code) {
					wins.Add(1)
				}
			}(WebpopupCookieFound + j%2)
		}
		wg.Wait()
		if wins.Load() != 1 || len(f.get()) != 1 {
			t.Fatalf("wins=%d completions=%v, want exactly one", wins.Load(), f.get())
		}
	}
}

// First wins across sources: a timeout or found that fired first fixes the
// result even if a close or trap resolves before the queued finish runs.
func TestCookieWaiterFireThenCloseReturnsFired(t *testing.T) {
	for _, fire := range []int{WebpopupCookieFound, WebpopupCookieTimeout} {
		var f fired
		w := startCookieWaiter(time.Minute, 0, nil, f.add)
		if !w.Fire(fire) {
			t.Fatal("Fire must win")
		}
		if got := w.Resolve(WebpopupClosedByUser); got != fire {
			t.Fatalf("close after fire(%d) resolved to %d", fire, got)
		}
		if got := w.Resolve(WebpopupTrappedURL); got != fire {
			t.Fatalf("trap after fire(%d) resolved to %d", fire, got)
		}
		if got := f.get(); len(got) != 1 || got[0] != fire {
			t.Fatalf("onFire = %v", got)
		}
	}
}

// ... and the other way round: close first, then a late timeout/found loses
// and never runs onFire.
func TestCookieWaiterCloseThenFireLoses(t *testing.T) {
	var f fired
	w := startCookieWaiter(time.Minute, 0, nil, f.add)
	w.Resolve(WebpopupClosedByUser)
	if w.Fire(WebpopupCookieTimeout) || w.Fire(WebpopupCookieFound) {
		t.Fatal("Fire after close must lose")
	}
	if got := f.get(); len(got) != 0 {
		t.Fatalf("onFire ran: %v", got)
	}
	if got := w.Resolve(WebpopupTrappedURL); got != WebpopupClosedByUser {
		t.Fatalf("got %d", got)
	}
}

// Many racing Resolves and Fires: one result, every caller sees it.
func TestCookieWaiterResolveRace(t *testing.T) {
	for i := 0; i < 50; i++ {
		var f fired
		w := startCookieWaiter(time.Minute, 0, nil, f.add)
		results := make([]int, 8)
		var wg sync.WaitGroup
		for j := range results {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if j%2 == 0 {
					w.Fire(WebpopupCookieFound)
				}
				results[j] = w.Resolve(WebpopupClosedByUser + j)
			}()
		}
		wg.Wait()
		for _, r := range results {
			if r != results[0] {
				t.Fatalf("callers saw different results: %v", results)
			}
		}
		if n := len(f.get()); n > 1 {
			t.Fatalf("onFire ran %d times", n)
		}
	}
}

func TestCookieReadAbandon(t *testing.T) {
	// abandoned after attach: cancel runs exactly once
	r := newCookieRead()
	var n atomic.Int32
	if !r.Attach(func() { n.Add(1) }) {
		t.Fatal("Attach must succeed")
	}
	r.Abandon()
	r.Abandon()
	if n.Load() != 1 {
		t.Fatalf("cancel ran %d times", n.Load())
	}
	// abandoned before the platform started: Attach refuses
	r = newCookieRead()
	r.Abandon()
	if r.Attach(func() { n.Add(1) }) {
		t.Fatal("Attach after Abandon must refuse")
	}
	if n.Load() != 1 {
		t.Fatal("cancel of a refused Attach must not run")
	}
}
