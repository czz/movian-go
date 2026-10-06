package navigator

import (
	"github.com/czz/movian-go/internal/backend/core"
	propcore "github.com/czz/movian-go/internal/prop"
)

// PropManager returns the property manager used by this navigator.
func (n *Navigator) PropManager() *propcore.PropManager {
	if n == nil {
		return nil
	}
	return n.propManager
}

// Pages returns info about all open pages in this navigator.
func (nav *Navigator) Pages() []PageInfo {
	nav.mutex.Lock()
	defer nav.mutex.Unlock()
	result := make([]PageInfo, 0, len(nav.pages))
	for _, np := range nav.pages {
		result = append(result, PageInfo{URL: np.url, PropRoot: np.propRoot})
	}
	return result
}

// CurrentURL returns the URL of the currently selected page, or "" if none.
func (nav *Navigator) CurrentURL() string {
	nav.mutex.Lock()
	defer nav.mutex.Unlock()
	if nav.current != nil {
		return nav.current.url
	}
	return ""
}

// PropManager returns the property manager used by this navigator system.
func (ns *NavigatorSystem) PropManager() *propcore.PropManager {
	return ns.propMgr
}

// Open opens a URL with a specific view
func (ns *NavigatorSystem) Open(url string, view string) {
	ns.OpenFull(url, view, nil, nil, "", "")
}

// GetCurrentURL returns the current URL
func (ns *NavigatorSystem) GetCurrentURL() string {
	nav := ns.currentNavigator()
	if nav == nil {
		return ""
	}

	nav.mutex.Lock()
	defer nav.mutex.Unlock()

	return nav.currentURL
}

// GetCurrentView returns the current view
func (ns *NavigatorSystem) GetCurrentView() string {
	nav := ns.currentNavigator()
	if nav == nil {
		return ""
	}

	nav.mutex.Lock()
	defer nav.mutex.Unlock()

	return nav.currentView
}

// SetRoot sets the property root for the navigator
func (n *Navigator) SetRoot(root *propcore.Prop) {
	n.mutex.Lock()
	defer n.mutex.Unlock()
	n.root = root
}

// GetRoot returns the property root
func (n *Navigator) GetRoot() *propcore.Prop {
	n.mutex.Lock()
	defer n.mutex.Unlock()
	return n.root
}

// PropRoot returns the internal property root for this navigator
func (n *Navigator) PropRoot() *propcore.Prop {
	if n == nil {
		return nil
	}
	n.mutex.Lock()
	defer n.mutex.Unlock()
	return n.propRoot
}

// BackendSystem returns the backend system used by this navigator.
func (n *Navigator) BackendSystem() *core.BackendSystem {
	if n == nil {
		return nil
	}
	n.mutex.Lock()
	defer n.mutex.Unlock()
	return n.backendSystem
}

// EventSink returns the property that receives navigator events
func (n *Navigator) EventSink() *propcore.Prop {
	if n == nil {
		return nil
	}
	n.mutex.Lock()
	defer n.mutex.Unlock()
	return n.propEventSink
}

// Open opens a URL with a specific view for this navigator
func (n *Navigator) Open(url string, view string) {
	n.mutex.Lock()
	defer n.mutex.Unlock()

	n.currentURL = url
	n.currentView = view

	// Add to history
	entry := HistoryEntry{
		URL:  url,
		View: view,
	}

	// If we're not at the end of history, truncate
	if n.historyPos < len(n.history)-1 {
		n.history = n.history[:n.historyPos+1]
	}

	n.history = append(n.history, entry)
	n.historyPos = len(n.history) - 1

	navUpdateCangoLocked(n)
}
