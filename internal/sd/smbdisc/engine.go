// Package smbdisc finds the SMB servers on the LAN and lists each one
// once on Local network.
//
// Go extension: an engine merges what its sources see into the SMB host
// entries of its host table, by host address and then by name. Its sources are
// WS-Discovery, mDNS and NetBIOS host discovery (nmb.go), which reports
// names only. The Buksa/movian movian6 fork has a WS-Discovery client
// (src/sd/wsd.c) and browses _smb._tcp through Avahi (src/sd/avahi.c);
// both create their services beside those of nmb.c with no merge.
//
// Discovery is probe-and-reply: each round multicasts a WS-Discovery
// Probe and an mDNS query from ephemeral ports on every LAN interface
// and reads the unicast replies, so the engine binds no well-known
// port, joins no multicast group and runs the same on every platform.
// IPv4 only.
package smbdisc

import (
	"context"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/czz/movian-go/internal/sd/smbdisc/lan"
	"github.com/czz/movian-go/internal/sd/smbdisc/wsd"
	"github.com/czz/movian-go/internal/service"
	"github.com/czz/movian-go/internal/trace"
)

const (
	roundInterval = 20 * time.Second // between Probes
	collectWindow = 3 * time.Second  // replies are read this long after a Probe
	smbPort       = 445
	traceSubsys   = "SMBDISC"
)

// Clock is the engine's time source; tests drive a fake one.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time                         { return time.Now() }
func (systemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Config holds the engine's dependencies.
type Config struct {
	Services *service.ServiceSystem
	Trace    *trace.TraceSystem
	// WSDDest receives the WS-Discovery Probes; nil means the multicast
	// group 239.255.255.250:3702.
	WSDDest *net.UDPAddr
	// MDNSDest receives the mDNS queries; nil means the multicast group
	// 224.0.0.251:5353.
	MDNSDest *net.UDPAddr
	// Interfaces lists the interfaces the Probes and mDNS queries go
	// out through; nil means every LAN interface (lan.Interfaces). The
	// engine reads it again each round.
	Interfaces func() ([]lan.Interface, error)
	// HTTP fetches WS-Transfer metadata; nil means a client without a
	// proxy. Redirects are never followed.
	HTTP *http.Client
	// Clock times rounds and aging; nil means the system clock.
	Clock Clock
	// NetworkChanges subscribes notify to the app's network-change
	// notification and returns a function that unsubscribes it, which
	// the engine calls as it stops; nil means rounds run on the clock
	// only. notify never blocks.
	NetworkChanges func(notify func()) (unsubscribe func())
}

// Engine runs the sources and keeps the host table, which owns every SMB
// host entry on Local network.
//
// mu is held across calls into the service registry, so the lock order
// is mu, then the registry's mutex; the registry never calls back into
// the engine.
type Engine struct {
	ts    *trace.TraceSystem
	mdns  *net.UDPAddr // where the mDNS queries go
	ifs   func() ([]lan.Interface, error)
	wsd   *wsd.Client
	clock Clock
	wg    sync.WaitGroup
	ctx   context.Context    // the engine's; NetBIOS reports check it
	stop  context.CancelFunc // ends the engine's context; Stop calls it with mu held

	// changed holds a network change no round has answered yet; it is
	// nil when the engine has no network-change subscription.
	changed     chan struct{}
	unsubscribe func() // ends that subscription; nil without one

	// viewers counts the open Local network pages (Watch); rounds run on
	// the clock only while one is open. opened holds a page opening no
	// round has answered yet.
	viewers int // guarded by mu
	opened  chan struct{}
	sent    atomic.Int64 // Probes and queries that went out; a round that sent none ages nothing

	mu    sync.Mutex
	table *hostTable // guarded by mu
}

// sight takes a sighting into the host table. Once the engine's context
// is done it does nothing; it checks the context with mu held, as Stop
// cancels it, so a sighting still in flight as the engine stops either
// is published before Stop cancels or lists no host.
func (e *Engine) sight(o sighting) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ctx.Err() != nil {
		return
	}
	e.table.sight(o, e.clock.Now())
}

// Start runs discovery until ctx is done or Stop is called. No round
// starts until Watch says a page is current; Start itself does not
// block.
func Start(ctx context.Context, cfg Config) *Engine {
	e := &Engine{
		ts:     cfg.Trace,
		mdns:   cfg.MDNSDest,
		ifs:    cfg.Interfaces,
		clock:  cfg.Clock,
		table:  newHostTable(registryListing{cfg.Services}, cfg.Trace),
		opened: make(chan struct{}, 1),
	}
	if e.mdns == nil {
		e.mdns = mdnsGroup
	}
	if e.ifs == nil {
		e.ifs = lan.Interfaces
	}
	if e.clock == nil {
		e.clock = systemClock{}
	}
	e.wsd = &wsd.Client{Dest: cfg.WSDDest, HTTP: cfg.HTTP, Trace: cfg.Trace, Now: e.clock.Now}

	e.ctx, e.stop = context.WithCancel(ctx)
	if cfg.NetworkChanges != nil {
		e.changed = make(chan struct{}, 1)
		e.unsubscribe = cfg.NetworkChanges(e.networkChanged)
	}
	e.wg.Add(1)
	go e.run(e.ctx, e.clock.Now())
	return e
}

// Stop stops the engine and waits until it has. It cancels the
// engine's context with mu held, and sight checks the context with mu
// held, so a sighting being published as Stop is called is published
// first, and none is published after.
//
// C: movian6 registers wsd_init with no fini (src/sd/wsd.c), so its
// WS-Discovery client runs until the process exits.
// Go extension: shutdown stops the engine, so it changes no service
// once the rest of shutdown runs.
func (e *Engine) Stop() {
	e.mu.Lock()
	e.stop()
	e.mu.Unlock()
	e.Wait()
}

// Wait blocks until the engine has stopped after the context given to
// Start is done. A sighting being published as that context ends may
// still be published; Stop rules that out.
func (e *Engine) Wait() {
	e.wg.Wait()
	e.wsd.Wait()
}

// Watch tells the engine the Local network page is the current page,
// which starts a round at once and keeps rounds running every
// roundInterval until the returned release is called; release is
// idempotent. With no page current the engine probes at no time, not at
// start and not on a network change, as a phone would otherwise wake its
// radio and, in the background, find its network blocked.
//
// Go extension: movian6 probes every 20 s for as long as it runs.
func (e *Engine) Watch() (release func()) {
	if e == nil {
		return func() {}
	}
	e.mu.Lock()
	e.viewers++
	e.mu.Unlock()
	select {
	case e.opened <- struct{}{}:
	default: // a round is due already
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			e.mu.Lock()
			e.viewers--
			e.mu.Unlock()
		})
	}
}

func (e *Engine) watched() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.viewers > 0
}

// run starts a round when a page opens or the network changes while one
// is open, and every roundInterval while it stays open; it ages the
// hosts once each round's replies are in. Once ctx ends it unsubscribes
// from network changes.
func (e *Engine) run(ctx context.Context, next time.Time) {
	defer e.wg.Done()
	if e.unsubscribe != nil {
		defer e.unsubscribe()
	}
	for {
		var ok bool
		if next, ok = e.awaitRound(ctx, next); !ok {
			return
		}
		select {
		case <-e.opened: // this round answers it
		default:
		}
		sentBefore := e.sent.Load()
		// The WS-Discovery round hands back how many Probes it sent only as
		// it ends, which is not before the window closes, so the engine
		// waits for it rather than racing it.
		wsdSent := make(chan int, 1)
		e.wg.Add(2)
		go func() {
			defer e.wg.Done()
			wsdSent <- e.wsdRound(ctx, next)
		}()
		go e.mdnsRound(ctx, next)
		if !e.sleepUntil(ctx, next.Add(collectWindow)) {
			return
		}
		select {
		case n := <-wsdSent:
			e.sent.Add(int64(n))
		case <-ctx.Done():
			return
		}
		// A round that could not send, with no interface or a network
		// the system blocks, saw nothing and says nothing about the hosts.
		if e.sent.Load() != sentBefore {
			e.expire()
		}
		next = next.Add(roundInterval)
	}
}

// awaitRound waits for the round due at next, a network change or a
// page opening, and returns when the round starts: at next, or now
// after a change, an opening, or a wake-up past next. False means ctx
// ended. No round is due while no page is open: not at start, on the
// clock or after a change.
//
// C: wsd_send_probe (movian6 src/sd/wsd.c) runs off its 20 s timer
// only; the WS-Discovery client never hears of a network change.
// Go extension: a round whose timer is handled after next, as when the
// process was suspended, starts now and not at next, whose collection
// window has closed; the round after it is due roundInterval later.
// Go extension: a change starts a round at once, through the interfaces
// as they are then, and the round after it is due roundInterval later.
// A change heard while a round collects its replies starts the next
// round as soon as that one is over, as its sockets predate the change.
// Changes heard before that round starts fold into it, so a burst of
// them brings one round, not one per change.
func (e *Engine) awaitRound(ctx context.Context, next time.Time) (time.Time, bool) {
	for {
		var tick <-chan time.Time
		if e.watched() {
			tick = e.clock.After(next.Sub(e.clock.Now()))
		}
		select {
		case <-ctx.Done():
			return time.Time{}, false
		case <-tick:
			if e.watched() {
				// A round that fires late, the process having been
				// suspended, starts now with its whole collection window;
				// the rounds it missed are not replayed.
				if now := e.clock.Now(); now.After(next) {
					next = now
				}
				return next, true
			}
			// The last page closed while this waited.
		case <-e.changed:
			if !e.watched() {
				continue // nobody looks at the list
			}
			e.ts.Debug(traceSubsys, "network changed: round starts now")
			return e.clock.Now(), true
		case <-e.opened:
			if !e.watched() {
				continue // the page closed before the engine heard
			}
			e.ts.Debug(traceSubsys, "Local network opened: round starts now")
			return e.clock.Now(), true
		}
	}
}

// networkChanged is the engine's network-change subscriber. It never
// blocks, so the notifier does not wait on the engine.
func (e *Engine) networkChanged() {
	select {
	case e.changed <- struct{}{}:
	default: // a round is due already
	}
}

// sleepUntil waits for the clock to reach t; false means ctx ended.
func (e *Engine) sleepUntil(ctx context.Context, t time.Time) bool {
	select {
	case <-ctx.Done():
		return false
	case <-e.clock.After(t.Sub(e.clock.Now())):
		return true
	}
}

// expire drops the hosts no source has seen for hostTTL. It runs as a
// round's collection window closes, so a host is gone right after its
// third missed round.
func (e *Engine) expire() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.table.age(e.clock.Now())
}

// wsdRound runs one WS-Discovery round through the client and returns
// how many Probes it sent. It reports the interfaces it cannot list, then
// probes through the ones it can.
func (e *Engine) wsdRound(ctx context.Context, start time.Time) int {
	ifs, err := e.ifs()
	if err != nil {
		e.ts.Debug(traceSubsys, "WS-Discovery: interfaces: %v", err)
	}
	window := e.clock.After(start.Add(collectWindow).Sub(e.clock.Now()))
	return e.wsd.Round(ctx, window, ifs, e.wsdSight)
}

// wsdSight takes a WS-Discovery sighting into the host table.
func (e *Engine) wsdSight(s wsd.Sighting) {
	e.sight(sighting{from: fromWSD, name: s.Name, addr: s.Addr, port: s.Port})
}
