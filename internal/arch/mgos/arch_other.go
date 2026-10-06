//go:build mgos && !rpi

package mgos

import "runtime"

// ArchName — C: archname = PLATFORM (upgrade.c:1260). Fallback for
// non-rpi mgos builds.
var ArchName = runtime.GOARCH
