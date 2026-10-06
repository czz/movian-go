package asyncio

// C: src/networking/asyncio_posix.c — canonical 1:1 port.
// Core dispatch loop: single goroutine, task queue, timer list,
// worker registry, prop courier, network-change monitor, suspend/resume.
//
// Phase 5 — the poll()/wakeup-pipe machinery is gone: every socket is
// Go-native (netpoller), so nothing remains for poll() to wait on.
// The loop is now a channel-driven dispatcher running the SAME code on
// the SAME single goroutine: queued tasks (incl. worker wakeups and
// courier polls) and timer/af.timeout delivery — C's asyncio_dopoll
// body preserved where the semantics map.

import (
	"crypto/tls"
	"slices"
	"sync"
	"time"

	archpkg "github.com/czz/movian-go/internal/arch"
	netcore "github.com/czz/movian-go/internal/networking/core"
	propcore "github.com/czz/movian-go/internal/prop"
	tracepkg "github.com/czz/movian-go/internal/trace"
)

// ---------------------------------------------------------------------------
// C: static globals (asyncio_posix.c:57-89)

// C: asyncio_verify_thread — assert(hts_thread_current() == asyncio_thread_id).
// Go cannot compare goroutine ids portably; all loop-thread-only entry
// points carry the C assert as a comment instead.

// aioState — C: asyncio_worker_mutex + asyncio_worker_list +
// asyncio_task_mutex + asyncio_task_queue + asyncio_courier +
// static int64_t async_now + asyncio_timers + worker id generator +
// netifchange list. aio.taskWake replaces the C wakeup pipe.
// AsyncIO — C: the asyncio_posix.c file statics (worker list, task
// queue, async_now, timer list, netifchange list, fd list, trace()).
// Owned by the app context (ctx.asyncIO); consumers receive it by
// injection or reach it via the AsyncIOFD/Timer/DNSReq backpointers.
type AsyncIO struct {
	workerMu sync.Mutex
	workers  []*asyncioWorker
	taskMu   sync.Mutex
	tasks    []*asyncioTask
	courier  *propcore.Courier

	now          int64    // C: static int64_t async_now
	timers       []*Timer // C: asyncio_timer_list, sorted by expire
	workerSeq    int      // C: static int generator
	netifchanges []netifchange
	taskWake     chan struct{}

	initGroup       func(group int) // C: init_group direct call
	finiGroup       func(group int) // C: fini_group direct call
	netRefresh      func()          // C: net_refresh_network_status
	shutdownHookAdd func(f func(opaque any, retcode int), opaque any, early int)

	ts *tracepkg.TraceSystem // C: trace() global

	fdsMu    sync.Mutex
	fdsFirst *AsyncIOFD // C: asyncio_fd_list LIST_HEAD

	// C: static hts_mutex_t asyncio_dns_mutex + pending/completed queues
	dns struct {
		mu          sync.Mutex
		worker      int
		pending     []*DNSReq
		completed   []*DNSReq
		resolverRun bool
	}
}

// NewAsyncIO — C: asyncio static initializers (worker_seq=1, wakeup
// pipe). Called by the composition root at init.
func NewAsyncIO() *AsyncIO {
	return &AsyncIO{workerSeq: 1, taskWake: make(chan struct{}, 1)}
}

// SetTraceSystem injects the trace system (C: trace() global).
func (aio *AsyncIO) SetTraceSystem(ts *tracepkg.TraceSystem) { aio.ts = ts }

// C: typedef struct asyncio_worker (asyncio_posix.c:95-100)
type asyncioWorker struct {
	fn      func()
	id      int
	pending int
}

// C: typedef struct asyncio_task (asyncio_posix.c:156-160)
type asyncioTask struct {
	fn  func(aux any)
	aux any
}

// C: typedef struct netifchange (asyncio_posix.c:1505-1508)
type netifchange struct {
	cb func(ni []netcore.NetIF)
}

// Lifecycle seams — C resolves these as direct cross-module calls from
// asyncio_posix.c (init_group/fini_group/net_refresh_network_status/
// shutdown_hook_add). Providers live in packages that import asyncio —
// wired once at init.
// SetInitGroupFunc wires init_group(INIT_GROUP_ASYNCIO) — runs group
// helpers on the asyncio thread (asyncio_posix.c:665).
func (aio *AsyncIO) SetInitGroupFunc(fn func(group int)) { aio.initGroup = fn }

// SetFiniGroupFunc wires fini_group(INIT_GROUP_ASYNCIO) — asyncio_do_shutdown.
func (aio *AsyncIO) SetFiniGroupFunc(fn func(group int)) { aio.finiGroup = fn }

// SetNetRefreshFunc wires net_refresh_network_status (asyncio_posix.c:1547).
func (aio *AsyncIO) SetNetRefreshFunc(fn func()) { aio.netRefresh = fn }

// SetShutdownHookAdd wires shutdown_hook_add(asyncio_shutdown, NULL, 1).
func (aio *AsyncIO) SetShutdownHookAdd(fn func(f func(opaque any, retcode int),
	opaque any, early int)) {
	aio.shutdownHookAdd = fn
}

// ---------------------------------------------------------------------------
// C: int64_t async_current_time (asyncio_posix.c:168-172)
func (aio *AsyncIO) CurrentTime() int64 { return aio.now }

// C: static void asyncio_wakeup (asyncio_posix.c:237-245) — the C wakeup
// pipe is replaced by aio.taskWake: posting signals the dispatcher, which
// drains the task queue on the loop goroutine — same effect, no fd.
func (aio *AsyncIO) wake() {
	select {
	case aio.taskWake <- struct{}{}:
	default:
	}
}

// C: static void asyncio_courier_notify (asyncio_posix.c:250-254)
func (aio *AsyncIO) asyncioCourierNotify(opaque any) {
	aio.RunTask(func(aux any) {
		if aio.courier != nil {
			aio.courier.Poll()
		}
	}, nil)
}

// C: void asyncio_wakeup_worker (asyncio_posix.c:260-264)
func (aio *AsyncIO) WakeupWorker(id int) {
	var aw *asyncioWorker
	aio.workerMu.Lock()
	for _, w := range aio.workers {
		if w.id == id {
			aw = w
			break
		}
	}
	aio.workerMu.Unlock()

	if aw != nil {
		fn := aw.fn
		aio.RunTask(func(aux any) { fn() }, nil)
	}
}

// ---------------------------------------------------------------------------
// C: static void asyncio_dopoll (asyncio_posix.c:370-484).
// Same iteration shape as C: fire expired timers, deliver af timeouts,
// compute the earliest deadline, sleep until a wake or the deadline.
// The C revents pass is gone — nothing is polled anymore.
func (aio *AsyncIO) asyncioDoPoll() {
	for len(aio.timers) > 0 && aio.timers[0].expire <= aio.now {
		at := aio.timers[0]
		aio.timers = aio.timers[1:]
		at.expire = 0
		at.fn(at.opaque)
	}

	timeout := int64(0x7fffffff) // C: INT32_MAX

	for af := aio.fdsFirst; af != nil; af = af.linkNext {
		if af.timeout != 0 {
			if af.timeout <= aio.now {
				af.timeout = 0
				af.callback(af, af.opaque, AsyncIOTimeout, 0)
				continue
			}
			if to := af.timeout - aio.now; to < timeout {
				timeout = to
			}
		}
	}

	if len(aio.timers) > 0 {
		if to := aio.timers[0].expire - aio.now; to < timeout {
			timeout = to
		}
	}

	if timeout == 0x7fffffff {
		// C: timeout = -1 — block until a wake
		<-aio.taskWake
	} else if timeout <= 0 {
		// Deadline already passed (can happen if a timer callback
		// consumed time) — don't sleep, just drain pending tasks.
		select {
		case <-aio.taskWake:
		default:
		}
	} else {
		t := time.NewTimer(time.Duration(timeout) * time.Microsecond)
		select {
		case <-aio.taskWake:
		case <-t.C:
		}
		t.Stop()
	}

	aio.now = archpkg.GetTS()

	// Drain the task queue — C: asyncio_handle_pipe byte 1 (task queue),
	// byte 0 (courier) and byte N (worker) are all queued via RunTask
	// now, so they drain here in posting order.
	aio.taskMu.Lock()
	atq := aio.tasks
	aio.tasks = nil
	aio.taskMu.Unlock()

	for _, at := range atq {
		at.fn(at.aux)
	}
}

// ---------------------------------------------------------------------------
// C: static int asyncio_handle_pipe (asyncio_posix.c:584-625) — removed:
// courier polls, task-queue drains and worker wakeups are all RunTask
// dispatches now, executed by the select loop above.

// C: int asyncio_add_worker (asyncio_posix.c:631-646)

func (aio *AsyncIO) AddWorker(fn func()) int {
	aw := &asyncioWorker{fn: fn}

	aio.workerMu.Lock()
	aio.workerSeq++
	aw.id = aio.workerSeq
	aio.workers = slices.Insert(aio.workers, 0, aw)
	aio.workerMu.Unlock()
	return aw.id
}

// ---------------------------------------------------------------------------
// C: static void *asyncio_thread (asyncio_posix.c:653-672)
func (aio *AsyncIO) asyncioThread() {
	// C: asyncio_thread_id = hts_thread_current()

	aio.courier = propcore.NewCourierNotify("asyncio",
		aio.asyncioCourierNotify, nil)

	aio.now = archpkg.GetTS()

	if aio.initGroup != nil {
		aio.initGroup(archpkg.InitGroupAsyncIO) // C: init_group(...)
	}

	aio.TrigNetworkChange() // C: asyncio_trig_network_change()

	for {
		aio.asyncioDoPoll()
	}
}

// C: static void asyncio_do_shutdown (asyncio_posix.c:676-679)
func (aio *AsyncIO) asyncioDoShutdown(aux any) {
	if aio.finiGroup != nil {
		aio.finiGroup(archpkg.InitGroupAsyncIO)
	}
}

// C: static void asyncio_shutdown (asyncio_posix.c:685-690)
func (aio *AsyncIO) asyncioShutdown(opaque any, retcode int) {
	aio.ts.Trace(tracepkg.TRACE_DEBUG, "ASYNCIO", "Shutdown")
	aio.RunTask(aio.asyncioDoShutdown, nil)
}

// ---------------------------------------------------------------------------
// C: void asyncio_init_early (asyncio_posix.c:696-711)
func (aio *AsyncIO) StartEarly() {
	sysSetup() // windows: WSAStartup; unix: no-op

	aio.dns.worker = aio.AddWorker(aio.adrDeliverCb)
}

// C: void asyncio_start (asyncio_posix.c:716-723)
func (aio *AsyncIO) Start() {
	go aio.asyncioThread() // C: hts_thread_create_detached("asyncio", ...)

	if aio.shutdownHookAdd != nil {
		aio.shutdownHookAdd(aio.asyncioShutdown, nil, 1)
	}
}

// C: void asyncio_run_task (asyncio_posix.c:728-741)
func (aio *AsyncIO) RunTask(fn func(aux any), aux any) {
	at := &asyncioTask{fn: fn, aux: aux}

	aio.taskMu.Lock()
	doSignal := len(aio.tasks) == 0
	aio.tasks = append(aio.tasks, at)
	aio.taskMu.Unlock()
	if doSignal {
		aio.wake()
	}
}

// ---------------------------------------------------------------------------
// Network interface changes (asyncio_posix.c:1502-1608)

// C: void asyncio_register_for_network_changes (asyncio_posix.c:1513-1523)
func (aio *AsyncIO) RegisterForNetworkChanges(cb func(ni []netcore.NetIF)) {
	// C: asyncio_verify_thread()
	nic := netifchange{cb: cb}
	aio.netifchanges = slices.Insert(aio.netifchanges, 0, nic)
	ni, _ := netcore.NetGetInterfaces() // C: net_get_interfaces + free
	nic.cb(ni)
}

// C: static void asyncio_do_network_change (asyncio_posix.c:1529-1538)
func (aio *AsyncIO) asyncioDoNetworkChange(aux any) {
	ni, _ := netcore.NetGetInterfaces()
	for _, nic := range aio.netifchanges {
		nic.cb(ni)
	}
}

// C: void asyncio_trig_network_change (asyncio_posix.c:1544-1549)
func (aio *AsyncIO) TrigNetworkChange() {
	if aio.netRefresh != nil {
		aio.netRefresh() // C: net_refresh_network_status()
	}
	aio.RunTask(aio.asyncioDoNetworkChange, nil)
}

// C: static void asyncio_do_suspend (asyncio_posix.c:1555-1574)
func (aio *AsyncIO) asyncioDoSuspend(aux any) {
	for _, nic := range aio.netifchanges {
		nic.cb(nil)
	}

	for af := aio.fdsFirst; af != nil; af = af.linkNext {
		if af.resume == nil {
			continue // Socket can't be resumed, skip
		}

		if c := af.udpConn.Load(); c != nil {
			// Go-native UDP socket — close kills the recv goroutine;
			// resume rebinds via asyncioUDPResume
			af.suspended = true
			af.udpConn.Store(nil)
			c.Close()
			continue
		}
		if af.listener != nil {
			// Go-native TCP listener — close kills the accept
			// goroutine; resume rebinds via asyncioTCPResume
			af.suspended = true
			af.listener.Close()
			af.listener = nil
			continue
		}
	}
}

// C: static void asyncio_do_resume (asyncio_posix.c:1579-1589)
func (aio *AsyncIO) asyncioDoResume(aux any) {
	for af := aio.fdsFirst; af != nil; af = af.linkNext {
		if af.suspended {
			af.resume(af)
		}
	}
	aio.asyncioDoNetworkChange(nil)
}

// C: void asyncio_suspend (asyncio_posix.c:1595-1599)
func (aio *AsyncIO) Suspend() { aio.RunTask(aio.asyncioDoSuspend, nil) }

// C: void asyncio_resume (asyncio_posix.c:1604-1608)
func (aio *AsyncIO) Resume() { aio.RunTask(aio.asyncioDoResume, nil) }

// Courier — C: asyncio_courier global.
func (aio *AsyncIO) Courier() *propcore.Courier { return aio.courier }

// errBindFailed — Go error wrapper for NULL-returning C functions
type bindError string

func (e bindError) Error() string { return string(e) }

const errBindFailed = bindError("bind failed")

// Stop — façade retained for the app-context shutdown path.
func (aio *AsyncIO) Stop() {}

// SSLCreateClient — C: asyncio_ssl_create_client
func (aio *AsyncIO) SSLCreateClient() *tls.Config {
	return SSLCreateClient().(*tls.Config)
}

// SSLCreateServer — C: asyncio_ssl_init server ctx.
func (aio *AsyncIO) SSLCreateServer(privateKeyFile, certFile string) (any,
	error) {
	return SSLCreateServer(privateKeyFile, certFile)
}

// SSLFree — C: SSL_CTX_free.
func (aio *AsyncIO) SSLFree(ctx any) { SSLFree(ctx) }
