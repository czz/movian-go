//go:build !android && !windows && !bundle

package app

import (
	"os"
	"path/filepath"
)

// AppDataRoot returns the application data root directory.
// C: app_dataroot() — two link-time variants:
//
//	support/dataroot/datadir.c (installed build, $(PROG).datadir):
//	  returns SHOWTIME_DATADIR (compile-time, e.g. /usr/share/showtime).
//	  Go equivalent: `make install-build DATADIR=...` injects DataDir
//	  via -X github.com/czz/movian-go/internal/app.DataDir.
//	support/dataroot/wd.c (dev build, $(PROG)):
//	  returns "./" — resources are loaded from the working directory.
//
// Go extension: when neither is set and "./res" does not exist, fall
// back to the directory containing the executable (if it has res/) —
// equivalent to a datadir.c install sitting next to the binary. This
// makes dataroot:// resolve fonts/skins/JS modules regardless of CWD.
func AppDataRoot() string {
	if DataDir != "" {
		return DataDir
	}
	if _, err := os.Stat("./res"); err == nil {
		return "./"
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		if _, err := os.Stat(filepath.Join(dir, "res")); err == nil {
			return dir
		}
	}
	return "./"
}
