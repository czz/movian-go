package navigator

import (
	"github.com/czz/movian-go/internal/event"
	"github.com/czz/movian-go/internal/mathutil"
)

// CanGoBack checks if we can go back in history
func (ns *NavigatorSystem) CanGoBack() bool {
	nav := ns.currentNavigator()
	if nav == nil {
		return false
	}

	nav.mutex.Lock()
	defer nav.mutex.Unlock()

	// Matching C: can go back if current page has a previous page in history
	np := nav.current
	if np == nil {
		return false
	}
	for i, p := range nav.historyQueue {
		if p == np && i > 0 {
			return true
		}
	}
	return false
}

// CanGoForward checks if we can go forward in history
func (ns *NavigatorSystem) CanGoForward() bool {
	nav := ns.currentNavigator()
	if nav == nil {
		return false
	}

	nav.mutex.Lock()
	defer nav.mutex.Unlock()

	// Matching C: can go forward if current page has a next page in history
	np := nav.current
	if np == nil {
		return false
	}
	for i, p := range nav.historyQueue {
		if p == np && i < len(nav.historyQueue)-1 {
			return true
		}
	}
	return false
}

// GoBack goes back in history on the current navigator.
// C: nav_back(navigator_t *nav) (navigator.c:796-816).
func (ns *NavigatorSystem) GoBack() bool {
	nav := ns.currentNavigator()
	if nav == nil {
		return false
	}
	return navBack(nav)
}

// navBack is the per-navigator back operation.
// C: nav_back (navigator.c:796-816). Caller must NOT hold nav.mutex.
func navBack(nav *Navigator) bool {
	nav.mutex.Lock()
	defer nav.mutex.Unlock()

	np := nav.current
	if np == nil {
		return false
	}

	// Find current index in historyQueue
	currentIdx := -1
	for i, p := range nav.historyQueue {
		if p == np {
			currentIdx = i
			break
		}
	}
	if currentIdx <= 0 {
		// No previous page — dispatch ACTION_SYSTEM_HOME.
		// C: event_t *e = event_create_action(ACTION_SYSTEM_HOME);
		//    prop_t *es = prop_create_r(nav->nav_prop_root, "eventSink");
		//    prop_send_ext_event(es, e); prop_ref_dec(es); event_release(e);
		// Must carry a real *event.Event — eventSink subscribers expect
		// args[0].(*event.Event) (nav_eventsink's PROP_TAG_CALLBACK_EVENT).
		if nav.propEventSink != nil && nav.ns != nil && nav.ns.eventMgr != nil {
			e := nav.ns.eventMgr.CreateAction(event.ACTION_SYSTEM_HOME)
			if e != nil {
				nav.propManager.SendExtEvent(nav.propEventSink, e.AsEvent())
				e.AsEvent().Release()
			}
		}
		return false
	}

	prev := nav.historyQueue[currentIdx-1]

	// Check if we should close the current page
	// C: doclose = np->np_direct_close || gconf.enable_nav_always_close
	doclose := np.directClose || nav.ns.gcfg().EnableNavAlwaysClose.Load()

	// Select the previous page (this links prev.propRoot to currentpage)
	navSelect(nav, prev, nil)

	if doclose {
		navCloseNoLock(np, true)
	}

	return true
}

// GoForward goes forward in history on the current navigator.
// C: nav_fwd(navigator_t *nav) (navigator.c:819-828).
func (ns *NavigatorSystem) GoForward() bool {
	nav := ns.currentNavigator()
	if nav == nil {
		return false
	}
	return navFwd(nav)
}

// navFwd is the per-navigator forward operation.
// C: nav_fwd (navigator.c:819-828). Caller must NOT hold nav.mutex.
func navFwd(nav *Navigator) bool {
	nav.mutex.Lock()
	defer nav.mutex.Unlock()

	np := nav.current
	if np == nil {
		return false
	}

	// Find current index in historyQueue
	currentIdx := -1
	for i, p := range nav.historyQueue {
		if p == np {
			currentIdx = i
			break
		}
	}
	if currentIdx < 0 || currentIdx >= len(nav.historyQueue)-1 {
		return false
	}

	next := nav.historyQueue[currentIdx+1]
	navSelect(nav, next, nil)

	return true
}

// GetHistory returns the navigation history
func (ns *NavigatorSystem) GetHistory() []HistoryEntry {
	nav := ns.currentNavigator()
	if nav == nil {
		return nil
	}

	nav.mutex.Lock()
	defer nav.mutex.Unlock()

	result := make([]HistoryEntry, len(nav.history))
	copy(result, nav.history)
	return result
}

// GetHistoryPosition returns the current position in history
func (ns *NavigatorSystem) GetHistoryPosition() int {
	nav := ns.currentNavigator()
	if nav == nil {
		return -1
	}

	nav.mutex.Lock()
	defer nav.mutex.Unlock()

	return nav.historyPos
}

// ClearHistory clears the navigation history
func (ns *NavigatorSystem) ClearHistory() {
	nav := ns.currentNavigator()
	if nav == nil {
		return
	}

	nav.mutex.Lock()
	defer nav.mutex.Unlock()

	nav.history = make([]HistoryEntry, 0)
	nav.historyPos = -1
	navUpdateCangoLocked(nav)
}

// navUpdateCango updates canGoBack, canGoForward, canGoHome properties
// navUpdateCangoLocked updates can-go-back/fwd/home props. Caller must hold nav.mutex.
func navUpdateCangoLocked(nav *Navigator) {
	np := nav.current
	if np == nil {
		nav.propManager.SetIntEx(nav.propCanGoBack, nil, 0)
		nav.propManager.SetIntEx(nav.propCanGoFwd, nil, 0)
		nav.propManager.SetIntEx(nav.propCanGoHome, nil, 1)
		return
	}
	canGoBack := false
	for i, p := range nav.historyQueue {
		if p == np && i > 0 {
			canGoBack = true
			break
		}
	}
	nav.propManager.SetIntEx(nav.propCanGoBack, nil, mathutil.BoolToInt(canGoBack))
	canGoFwd := false
	for i, p := range nav.historyQueue {
		if p == np && i < len(nav.historyQueue)-1 {
			canGoFwd = true
			break
		}
	}
	nav.propManager.SetIntEx(nav.propCanGoFwd, nil, mathutil.BoolToInt(canGoFwd))
	nav.propManager.SetIntEx(nav.propCanGoHome, nil, mathutil.BoolToInt(np.url != NavHome))
}
