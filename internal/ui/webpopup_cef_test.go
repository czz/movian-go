//go:build webpopup && cef

package ui

import (
	"testing"
	"unsafe"
)

// These tests drive the cookie-query bookkeeping only. They never call
// CEF: no cef_initialize, no browser, no window, no display. (Linking
// needs libcef; nothing from it runs.)

// results maps each test query to the receiving end of its done channel.
var results = map[*cookieQuery]chan []WebpopupCookie{}

func drain(q *cookieQuery) []WebpopupCookie {
	select {
	case c := <-results[q]:
		return c
	default:
		return nil
	}
}

func newTestQuery() *cookieQuery {
	ch := make(chan []WebpopupCookie, 1)
	q := &cookieQuery{url: cookieURL{"https", "site.test", "/"}, done: ch}
	results[q] = ch
	return q
}

func TestCancelCookieQueryBalancesActiveCount(t *testing.T) {
	base := cefActiveN.Load()
	q := newTestQuery()
	key, cancel, ok := startCookieQuery(q)
	if !ok {
		t.Fatal("token allocation failed")
	}
	if got := cefActiveN.Load(); got != base+1 {
		t.Fatalf("active after start = %d, want %d", got, base+1)
	}
	if !recordCookieVisit(key, cookieVisit{name: "a", value: "1", domain: "site.test", path: "/"}) {
		t.Fatal("visit before cancel must be accepted")
	}
	cancel()
	cancel() // idempotent
	if got := cefActiveN.Load(); got != base {
		t.Fatalf("active after cancel = %d, want %d", got, base)
	}
	// Late C callbacks: the visit stops, completion is a no-op, no result.
	if recordCookieVisit(key, cookieVisit{name: "b"}) {
		t.Fatal("visit after cancel must stop")
	}
	cefCookiesDone(key)
	cefCookiesDone(key)
	if got := cefActiveN.Load(); got != base {
		t.Fatalf("active after late done = %d, want %d", got, base)
	}
	if drain(q) != nil {
		t.Fatal("cancelled query delivered a result")
	}
	if q.visits != nil {
		t.Fatal("cancelled query kept its visits")
	}
}

func TestCookieQueryCompletesOnce(t *testing.T) {
	base := cefActiveN.Load()
	q := newTestQuery()
	key, cancel, ok := startCookieQuery(q)
	if !ok {
		t.Fatal("token allocation failed")
	}
	recordCookieVisit(key, cookieVisit{name: "a", value: "1", domain: "site.test", path: "/"})
	cefCookiesDone(key)
	cefCookiesDone(key) // CEF's release after the last cookie
	got := drain(q)
	if len(got) != 1 || got[0].Name != "a" {
		t.Fatalf("result = %+v", got)
	}
	if drain(q) != nil {
		t.Fatal("second result delivered")
	}
	cancel() // after completion: no-op, no second decrement
	if now := cefActiveN.Load(); now != base {
		t.Fatalf("active = %d, want %d", now, base)
	}
}

// A cancel closure must not remove a newer query that got the same token
// address after the first completed.
func TestCancelDoesNotTouchReusedKey(t *testing.T) {
	base := cefActiveN.Load()
	q1 := newTestQuery()
	key, cancel1, ok := startCookieQuery(q1)
	if !ok {
		t.Fatal("token allocation failed")
	}
	cefCookiesDone(key) // q1 completes; the token address is free for reuse
	q2 := newTestQuery()
	cookieQueries.Store(key, q2) // the address, reused
	cefActiveN.Add(1)
	cancel1()
	if v, ok := cookieQueries.Load(key); !ok || v.(*cookieQuery) != q2 {
		t.Fatal("stale cancel removed the newer query")
	}
	if got := cefActiveN.Load(); got != base+1 {
		t.Fatalf("active = %d, want %d", got, base+1)
	}
	cefCookiesDone(key)
	if got := cefActiveN.Load(); got != base {
		t.Fatalf("active = %d, want %d", got, base)
	}
}

func TestBrowserCreateFailureKeepsActiveCount(t *testing.T) {
	old := cefCreateBrowser
	cefCreateBrowser = func(unsafe.Pointer, string) bool { return false }
	defer func() { cefCreateBrowser = old }()

	base := cefActiveN.Load()
	done := make(chan *WebpopupResult, 2)
	s := wpNewSession("https://site.test/", "", nil, &WebpopupResult{}, done)
	if got := cefActiveN.Load(); got != base {
		t.Fatalf("active = %d, want %d", got, base)
	}
	if len(done) != 1 {
		t.Fatalf("completions = %d, want 1", len(done))
	}
	if r := <-done; r.ResultCode != WebpopupLoadError {
		t.Fatalf("result = %d, want WebpopupLoadError", r.ResultCode)
	}
	s.finish(WebpopupLoadError) // a late source must not complete again
	if len(done) != 0 || cefActiveN.Load() != base {
		t.Fatal("second finish completed or changed the count")
	}
}

func TestLateBrowserCreatedAfterFinishIsClosed(t *testing.T) {
	oldCreate, oldClose := cefCreateBrowser, cefCloseBrowser
	defer func() { cefCreateBrowser, cefCloseBrowser = oldCreate, oldClose }()
	var key unsafe.Pointer
	cefCreateBrowser = func(k unsafe.Pointer, _ string) bool { key = k; return true }
	var closed []unsafe.Pointer
	cefCloseBrowser = func(b unsafe.Pointer) { closed = append(closed, b) }

	base := cefActiveN.Load()
	done := make(chan *WebpopupResult, 2)
	// No websurface glue is installed, so the session finishes at once
	// (WebpopupLoadError) while its browser is still being created.
	wpNewSession("https://site.test/", "", nil, &WebpopupResult{}, done)
	if len(done) != 1 || (<-done).ResultCode != WebpopupLoadError {
		t.Fatal("session did not finish once with WebpopupLoadError")
	}
	if key == nil {
		t.Fatal("browser create was not reached")
	}

	late := unsafe.Pointer(new(int))
	cefOnBrowserCreated(late, key) // CEF reports the browser after finish
	if len(closed) != 1 || closed[0] != late {
		t.Fatalf("closed = %v, want exactly the late browser", closed)
	}
	if len(done) != 0 {
		t.Fatal("session completed a second time")
	}
	if got := cefActiveN.Load(); got != base {
		t.Fatalf("active = %d, want %d", got, base)
	}
}

// ---- per-browser session tokens (several browsers alive at once) ----
//
// fakeCEF stands in for libcef. A create builds the real per-browser C
// client and handlers (cl_new) from the token and keeps CEF's reference to
// the client; callbacks are delivered through those real handlers, and the
// browser's end is the release of that client reference.

type fakeCEF struct {
	keys    []unsafe.Pointer                  // token of each create call, in order
	clients map[unsafe.Pointer]unsafe.Pointer // token -> C client
	closed  []unsafe.Pointer                  // browsers passed to cefCloseBrowser
	failing map[int]bool                      // create call numbers (0-based) that fail
}

func (f *fakeCEF) install(t *testing.T, show bool) {
	oldCreate, oldClose, oldFocus := cefCreateBrowser, cefCloseBrowser, cefFocusBrowser
	oldShow, oldHide := wpSurfaceGlueCef.show, wpSurfaceGlueCef.hide
	t.Cleanup(func() {
		cefCreateBrowser, cefCloseBrowser, cefFocusBrowser = oldCreate, oldClose, oldFocus
		wpSurfaceGlueCef.show, wpSurfaceGlueCef.hide = oldShow, oldHide
	})
	cefCreateBrowser = func(k unsafe.Pointer, _ string) bool {
		n := len(f.keys)
		f.keys = append(f.keys, k)
		if f.failing[n] {
			// libcef consumes the client even when creation fails.
			cefTestClientRelease(cefTestClient(k))
			return false
		}
		if f.clients == nil {
			f.clients = map[unsafe.Pointer]unsafe.Pointer{}
		}
		f.clients[k] = cefTestClient(k)
		return true
	}
	cefCloseBrowser = func(b unsafe.Pointer) { f.closed = append(f.closed, b) }
	cefFocusBrowser = func(unsafe.Pointer) {}
	wpSurfaceGlueCef.show = func(any) bool { return show }
	wpSurfaceGlueCef.hide = func() {}
}

// destroyHandlers — CEF dropping its client (and so the handlers) after the
// browser closed.
func (f *fakeCEF) destroyHandlers(k unsafe.Pointer) {
	cefTestClientRelease(f.clients[k])
	delete(f.clients, k)
}

// afterCreated — OnAfterCreated delivered through the browser's own client.
func (f *fakeCEF) afterCreated(k, browser unsafe.Pointer) {
	cefTestAfterCreated(f.clients[k], browser)
}

func newBrowser() unsafe.Pointer { return cefTestBrowser() }

func startSession(t *testing.T) (*cefSession, chan *WebpopupResult) {
	t.Helper()
	done := make(chan *WebpopupResult, 2)
	s := wpNewSession("https://site.test/", "", nil, &WebpopupResult{}, done)
	return s, done
}

func countClosed(f *fakeCEF, b unsafe.Pointer) int {
	n := 0
	for _, c := range f.closed {
		if c == b {
			n++
		}
	}
	return n
}

// Two sessions are created before either OnAfterCreated arrives: each
// browser binds to its own session, none leaks, each closes exactly once.
func TestTwoSessionsBeforeAfterCreated(t *testing.T) {
	f := &fakeCEF{}
	f.install(t, true)
	freed0 := cefTokensFreed()
	a, _ := startSession(t)
	b, _ := startSession(t)
	if len(f.keys) != 2 || f.keys[0] == f.keys[1] {
		t.Fatalf("keys = %v, want two distinct tokens", f.keys)
	}
	brA, brB := newBrowser(), newBrowser()
	f.afterCreated(f.keys[0], brA) // A's callback arrives after B began
	f.afterCreated(f.keys[1], brB)
	if a.browser != brA || b.browser != brB {
		t.Fatalf("browsers bound A=%v B=%v, want %v %v", a.browser, b.browser, brA, brB)
	}
	if len(f.closed) != 0 {
		t.Fatalf("closed %v before any session finished (leak/early close)", f.closed)
	}
	a.finish(WebpopupClosedByUser)
	b.finish(WebpopupClosedByUser)
	if countClosed(f, brA) != 1 || countClosed(f, brB) != 1 || len(f.closed) != 2 {
		t.Fatalf("closed = %v, want each browser once", f.closed)
	}
	f.destroyHandlers(f.keys[0])
	f.destroyHandlers(f.keys[1])
	if got := cefTokensFreed() - freed0; got != 2 {
		t.Fatalf("tokens freed = %d, want 2", got)
	}
}

// B's create fails while A is live: A keeps its callbacks and token, B's
// token is freed on the spot.
func TestCreateFailureWhileOtherBrowserLive(t *testing.T) {
	f := &fakeCEF{failing: map[int]bool{1: true}}
	f.install(t, true)
	freed0 := cefTokensFreed()
	a, _ := startSession(t)
	brA := newBrowser()
	f.afterCreated(f.keys[0], brA)
	b, doneB := startSession(t)
	if r := <-doneB; r.ResultCode != WebpopupLoadError {
		t.Fatalf("B result = %d, want WebpopupLoadError", r.ResultCode)
	}
	if got := cefTokensFreed() - freed0; got != 1 {
		t.Fatalf("tokens freed after B failed = %d, want 1 (B's)", got)
	}
	if cefSessionOf(f.keys[0]) != a || a.finished || a.browser != brA {
		t.Fatal("A lost its session or browser when B's create failed")
	}
	if cefSessionOf(f.keys[1]) != nil || b.key != nil {
		t.Fatal("B's session still reachable")
	}
	if len(f.closed) != 0 {
		t.Fatalf("closed %v, want none yet", f.closed)
	}
	a.finish(WebpopupClosedByUser)
	f.destroyHandlers(f.keys[0])
	if got := cefTokensFreed() - freed0; got != 2 {
		t.Fatalf("tokens freed = %d, want 2", got)
	}
}

// Close order A,B and B,A: a token is freed once, only after the session
// and every handler have let go of it.
func TestCloseOrdersFreeEachTokenOnce(t *testing.T) {
	for _, order := range [][2]int{{0, 1}, {1, 0}} {
		f := &fakeCEF{}
		f.install(t, true)
		freed0 := cefTokensFreed()
		s := make([]*cefSession, 2)
		br := make([]unsafe.Pointer, 2)
		for i := range s {
			s[i], _ = startSession(t)
		}
		for i := range s {
			br[i] = newBrowser()
			f.afterCreated(f.keys[i], br[i])
		}
		first, second := order[0], order[1]
		s[first].finish(WebpopupClosedByUser)
		if got := cefTokensFreed() - freed0; got != 0 {
			t.Fatalf("order %v: freed %d while handlers still hold the token", order, got)
		}
		if cefSessionOf(f.keys[first]) != nil || cefSessionOf(f.keys[second]) != s[second] {
			t.Fatalf("order %v: wrong sessions mapped after first finish", order)
		}
		f.destroyHandlers(f.keys[first])
		if got := cefTokensFreed() - freed0; got != 1 {
			t.Fatalf("order %v: freed %d after first browser ended, want 1", order, got)
		}
		s[second].finish(WebpopupClosedByUser)
		if got := cefTokensFreed() - freed0; got != 1 {
			t.Fatalf("order %v: second token freed before its handlers", order)
		}
		f.destroyHandlers(f.keys[second])
		if got := cefTokensFreed() - freed0; got != 2 {
			t.Fatalf("order %v: freed %d, want 2", order, got)
		}
		for i := range br {
			if countClosed(f, br[i]) != 1 {
				t.Fatalf("order %v: browser %d closed %d times", order, i, countClosed(f, br[i]))
			}
		}
	}
}

// A finishes before its OnAfterCreated while B has started in between:
// A's late browser is closed, and B neither adopts nor loses anything.
func TestLateBrowserWithAnotherSessionInBetween(t *testing.T) {
	f := &fakeCEF{}
	f.install(t, true)
	a, _ := startSession(t)
	a.finish(WebpopupClosedByUser) // before its browser exists
	b, _ := startSession(t)
	brA, brB := newBrowser(), newBrowser()
	f.afterCreated(f.keys[0], brA)
	if countClosed(f, brA) != 1 || len(f.closed) != 1 {
		t.Fatalf("closed = %v, want exactly A's late browser", f.closed)
	}
	if b.browser != nil {
		t.Fatal("B adopted A's browser")
	}
	f.afterCreated(f.keys[1], brB)
	if b.browser != brB || b.finished {
		t.Fatal("B did not get its own browser")
	}
	b.finish(WebpopupClosedByUser)
	f.destroyHandlers(f.keys[0])
	f.destroyHandlers(f.keys[1])
}

// Frames go to the session of the client they came through, also while a
// second session is alive.
func TestPaintRoutedPerClient(t *testing.T) {
	f := &fakeCEF{}
	f.install(t, true)
	a, _ := startSession(t)
	b, _ := startSession(t)
	brA, brB := newBrowser(), newBrowser()
	f.afterCreated(f.keys[0], brA)
	f.afterCreated(f.keys[1], brB)
	cefTestPaint(f.clients[f.keys[0]], brA, []byte{1, 1, 1, 1}, 1, 1)
	cefTestPaint(f.clients[f.keys[1]], brB, []byte{2, 2, 2, 2, 2, 2, 2, 2}, 2, 1)
	if a.fw != 1 || a.frame[0] != 1 || b.fw != 2 || b.frame[0] != 2 {
		t.Fatalf("frames crossed: A %dx%d %v, B %dx%d %v", a.fw, a.fh, a.frame, b.fw, b.fh, b.frame)
	}
	a.finish(WebpopupClosedByUser)
	b.finish(WebpopupClosedByUser)
	f.destroyHandlers(f.keys[0])
	f.destroyHandlers(f.keys[1])
}
