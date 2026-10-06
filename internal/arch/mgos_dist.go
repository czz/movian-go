//go:build linux && mgos

package arch

import (
	"os"
	"strings"
)

// mgosGetDist — C: stos_get_dist (posix.c:87-101). Reads /mgosversion
// (C: /stosversion) and returns "MGOS <version>" (C: "STOS <version>").
// Empty string when the file is absent — caller falls back to
// linux_get_dist like C's `if(dist == NULL) dist = linux_get_dist()`.
func mgosGetDist() string {
	data, err := os.ReadFile("/mgosversion")
	if err != nil {
		return ""
	}
	// C: fgets reads one line; strchr(buf,'\n') truncates it
	if i := strings.IndexByte(string(data), '\n'); i >= 0 {
		data = data[:i]
	}
	if len(data) == 0 {
		return ""
	}
	return "MGOS " + string(data)
}
