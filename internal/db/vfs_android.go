//go:build android

package db

import (
	"strings"

	"github.com/czz/movian-go/internal/arch"
)

// dbFSPath — C: the SQLITE_VFS in src/db/vfs.c (CONFIG_SQLITE_VFS,
// SQLITE_OS_OTHER) routes every sqlite file open through fa_open, so
// fa URLs like persistent:///x resolve through the registered fa
// protocols (android_fs.c's urlToPath → settings/cache/sdcard dirs).
// modernc's sqlite opens host paths directly — translate the fa scheme
// URLs here, at the sqlite boundary.
func dbFSPath(path string) string {
	for _, m := range []struct{ scheme, dir string }{
		{"persistent", arch.AndroidFSSettingsPath},
		{"cache", arch.AndroidFSCachePath},
		{"es", arch.AndroidFSSdcardPath},
	} {
		if strings.HasPrefix(path, m.scheme+"://") {
			rest := strings.TrimPrefix(path, m.scheme+"://")
			if rest == "" || rest[0] != '/' {
				return m.dir + "/" + rest
			}
			return m.dir + rest
		}
	}
	return strings.TrimPrefix(path, "file://")
}
