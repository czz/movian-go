//go:build connman || linux

package connman

import propcore "github.com/czz/movian-go/internal/prop"

// ConnmanService is the seam between the app and the connman/gio
// D-Bus network manager. connman.Start is the only entry point —
// this interface names the contract so the gio/glib-bound backend
// can be swapped for a pure-Go D-Bus or a stub.
//
// contract: Start spawns the connman worker goroutine that owns the
// D-Bus session and publishes network state onto the given prop
// root; callers never hold a handle — shutdown is process lifetime.
type ConnmanService interface {
	// Start — begin watching system network state under prop root.
	Start(propMgr *propcore.PropManager, args ...any) int
}
