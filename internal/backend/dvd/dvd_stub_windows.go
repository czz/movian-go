//go:build windows

package dvd

// Windows has no DVD backend — the vendored dvdlib (libdvdread/dvdnav/
// dvdcss) is POSIX-only (linux/cdrom.h ioctls, pthread types clash with
// mingw winpthreads). Upstream never shipped a win32 build. The backend
// simply registers nothing.

import (
	backendcore "github.com/czz/movian-go/internal/backend/core"
	"github.com/czz/movian-go/internal/notifications"
)

// RegisterDVDBackend — no-op on windows.
func RegisterDVDBackend(bs *backendcore.BackendSystem,
	nm *notifications.NotificationManager) {
}
