package navigator

import (
	backendcore "github.com/czz/movian-go/internal/backend/core"
	"github.com/czz/movian-go/internal/event"
	propcore "github.com/czz/movian-go/internal/prop"
)

// NavigatorProvider defines the interface for navigation system management.
// Consumers should depend on this interface, not the concrete NavigatorSystem.
type NavigatorProvider interface {
	// StartNavigator initializes the navigation system with the shared EventManager.
	// C has a single global event system; Go uses a single EventManager instance.
	StartNavigator(em *event.EventManager)

	// SetBackendSystem injects the backend system
	SetBackendSystem(bs *backendcore.BackendSystem)

	// DefaultNavigator returns the default navigator instance
	DefaultNavigator() *Navigator

	// GetDefaultNavigator returns the default navigator instance
	GetDefaultNavigator() *Navigator

	// SetDefaultNavigator sets the default navigator instance
	SetDefaultNavigator(nav *Navigator)

	// Spawn creates a new navigation context
	Spawn(pm *propcore.PropManager) *Navigator

	// Open opens a URL with a specific view
	Open(url string, view string)

	// GetCurrentURL returns the current URL
	GetCurrentURL() string

	// GetCurrentView returns the current view
	GetCurrentView() string

	// CanGoBack checks if we can go back in history
	CanGoBack() bool

	// CanGoForward checks if we can go forward in history
	CanGoForward() bool

	// GoBack goes back in history
	GoBack() bool

	// GoForward goes forward in history
	GoForward() bool

	// GetHistory returns the navigation history
	GetHistory() []HistoryEntry

	// GetHistoryPosition returns the current position in history
	GetHistoryPosition() int

	// ClearHistory clears the navigation history
	ClearHistory()

	// Fini finalizes the navigation system
	Fini()
}

// Compile-time assertion that NavigatorSystem implements NavigatorProvider
var _ NavigatorProvider = (*NavigatorSystem)(nil)
