package navigator

import (
	"runtime/debug"

	"github.com/czz/movian-go/internal/event"
	propcore "github.com/czz/movian-go/internal/prop"
)

// SendEvent dispatches an action event through the shared EventManager.
// C: event_create_action + event_dispatch (global event system).
func (n *Navigator) SendEvent(action event.ActionType) {
	if n == nil || n.ns == nil || n.ns.eventMgr == nil {
		return
	}
	evt := n.ns.eventMgr.CreateAction(action)
	if evt != nil {
		n.ns.eventMgr.Dispatch(evt.AsEvent())
	}
}

// StartEventConsumer starts a goroutine that consumes events from the
// EventManager's eventSink channel. This drains the global event sink
// for non-NAV events (NAV events are routed via SendExtEvent to the
// nav courier, drained by ProcessNavCourier in the render thread).
//
// The goroutine stops when stopChan is closed.
func (ns *NavigatorSystem) StartEventConsumer(em *event.EventManager, stopChan <-chan struct{}) {
	eventSink := em.GetEventSink()

	ns.wg.Go(func() {
		for {
			select {
			case <-stopChan:
				return
			case e, ok := <-eventSink:
				if !ok {
					return
				}
				func() {
					defer func() {
						if r := recover(); r != nil {
							ns.ts.Debug("NAV", "panic in eventSink consumer: %v\n%s", r, debug.Stack())
						}
						e.Release()
					}()
				}()
			}
		}
	})
}

// WaitConsumers blocks until all event consumer goroutines have exited.
// Must be called after closing stopChan and before Fini() to ensure
// no consumer is still inside processNavEvent() when the navigator is destroyed.
func (ns *NavigatorSystem) WaitConsumers() {
	ns.wg.Wait()
}

// navEventsinkCallback is the Go equivalent of C's nav_eventsink
// (navigator.c:916-945). It is subscribed to nav.propEventSink via
// SendExtEvent and receives EventExtEvent notifications.
//
// Ownership contract (Phase 0/1):
//
//	The callback receives a BORROWED event reference. It must NOT
//	call e.Release(). The framework releases the event via
//	freeNotification after the callback returns.
//
// C: nav_eventsink(void *opaque, event_t *e)
//
//	— called with nav_mutex held (GLOBAL dispatch mode)
//
// Go: called in render thread via navCourier.Poll() (COURIER mode)
func (ns *NavigatorSystem) navEventsinkCallback(opaque any, eventType propcore.EventType, args ...any) {
	if eventType != propcore.EventExtEvent {
		return
	}
	nav, ok := opaque.(*Navigator)
	if !ok || nav == nil {
		return
	}
	// Extract the event from args. In courier mode, dispatchNotification
	// appends userInt as the last arg. The event is args[0] (since prop
	// is nil for SendExtEvent).
	// args layout: [event, userInt]
	if len(args) < 1 {
		return
	}
	e, ok := args[0].(*event.Event)
	if !ok {
		return
	}
	// e is BORROWED — do NOT release. Framework releases via freeNotification.
	func() {
		defer func() {
			if r := recover(); r != nil {
				ns.ts.Debug("NAV", "panic in navEventsinkCallback: %v\n%s", r, debug.Stack())
			}
		}()
		ns.processNavEvent(nav, e)
	}()
}

// ProcessNavCourier drains the nav courier and dispatches all pending
// EventExtEvent notifications. This is the Go equivalent of C's
// render loop. Must be called in the render thread before widget tree
// construction.
//
// C: prop_courier_poll_timed in glw_prepare_frame (but C uses GLOBAL
// dispatch for nav_eventsink, not the render-thread courier).
// Go: IMPLEMENTATION DIFFERENCE — we drain in the render thread
// instead of a global dispatch thread pool.
func (ns *NavigatorSystem) ProcessNavCourier() {
	if ns.navCourier == nil {
		return
	}
	ns.navCourier.Poll()
}

// NavCourier returns the navigator event courier.
// C: nav_eventsink runs on the GLOBAL prop dispatch (no courier) —
// in Go it is courier-bound and must be pumped by a UI thread.
// Exposed so headless builds can pump it from a worker goroutine.
func (ns *NavigatorSystem) NavCourier() *propcore.Courier {
	return ns.navCourier
}

// processNavEvent handles a navigation event from the nav courier.
// Equivalent to C's nav_eventsink (src/navigator.c:916-945) — it
// operates on the navigator passed as the subscription opaque, not on
// the default navigator.
func (ns *NavigatorSystem) processNavEvent(nav *Navigator, e *event.Event) {
	if e == nil || nav == nil {
		return
	}

	switch {
	case e.IsAction(event.ACTION_NAV_BACK):
		navBack(nav)

	case e.IsAction(event.ACTION_NAV_FWD):
		navFwd(nav)

	case e.IsAction(event.ACTION_HOME):
		// C: nav_open0(nav, NAV_HOME, NULL, NULL, NULL, NULL, NULL)
		navOpen0(nav, NavHome, "", nil, nil, "", "")

	case e.IsAction(event.ACTION_PLAYQUEUE):
		navOpen0(nav, "playqueue:", "", nil, nil, "", "")

	case e.IsAction(event.ACTION_RELOAD_DATA):
		navReloadCurrent(nav)

	case e.Type == event.EVENT_OPENURL:
		// C: nav_eventsink (navigator.c:936-941):
		//   ou = (event_openurl_t *)e;
		//   if(ou->url != NULL)
		//     nav_open0(nav, ou->url, ou->view, ...);
		//   else
		//     TRACE("Tried to open NULL URL");
		// C: if(ou->url != NULL) — Go strings have no NULL; a void
		// navOpen() argument produces an empty URL, which is the
		// equivalent of NULL here.
		if e.OpenURL != nil && e.OpenURL.URL != "" {
			ns.ts.Debug("NAV", "openurl event: %s", e.OpenURL.URL)
			var im, pm *propcore.Prop
			if p, ok := e.OpenURL.ItemModel.(*propcore.Prop); ok {
				im = p
			}
			if p, ok := e.OpenURL.ParentModel.(*propcore.Prop); ok {
				pm = p
			}
			navOpen0(nav, e.OpenURL.URL, e.OpenURL.View, im, pm,
				e.OpenURL.How, e.OpenURL.ParentURL)
		}
	}
}
