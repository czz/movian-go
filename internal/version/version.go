package version

import (
	"fmt"
	"runtime/debug"
	"strings"
)

// appversion is the application version string.
// C: version.c:24-27 — appversion = VERSIONOVERRIDE or VERSION_GIT, a
// global const fixed at build time. Same here: set once at link time via
// ldflags -X github.com/czz/movian-go/internal/version.appversion=... and never
// mutated at runtime. When empty (plain `go build` / `go install`), the
// version is derived from the binary's embedded build info instead.
var appversion string

// AppVersion returns the application version string.
// C: extern const char *appversion (version.c:24)
//
// The injected string is sanity-checked the same way ParseVersionInt reads
// it: it must carry a full major.minor.commit prefix ("5.0.568.g449bf.dirty"
// qualifies — the parser stops at the first non-digit). Garbage injected by
// a broken Makefile/git-describe falls back to the binary's build info
// instead of surfacing in the UI and the upgrade check.
func AppVersion() string {
	if isVersionString(appversion) {
		return appversion
	}
	return buildInfoVersion()
}

// isVersionString reports whether s parses as [v]major.minor.commit — the
// shape ParseVersionInt consumes. All three fields must be present so bare
// hashes like "449bf16" (git describe --always without tags) are rejected.
func isVersionString(s string) bool {
	var major, minor, commit int
	n, err := fmt.Sscanf(strings.TrimPrefix(s, "v"), "%d.%d.%d", &major, &minor, &commit)
	return err == nil && n == 3
}

// buildInfoVersion derives a version from the Go binary's embedded build
// info — the Go-idiomatic fallback when no -X injection was performed:
//
//	go install pkg@vX.Y.Z  → bi.Main.Version ("v5.0.568")
//	go build inside a repo → "dev-<vcs.revision>[.dirty]"
//	otherwise              → "0.0.0-dev"
func buildInfoVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "0.0.0-dev"
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	var rev, dirty string
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value
		}
	}
	if rev == "" {
		return "0.0.0-dev"
	}
	if len(rev) > 9 {
		rev = rev[:9]
	}
	v := "dev-" + rev
	if dirty == "true" {
		v += ".dirty"
	}
	return v
}

// ParseVersionInt parses a version string "major.minor.commit" into an integer.
// C: version.c:32-44 — parse_version_int
func ParseVersionInt(str string) uint32 {
	var major, minor, commit int
	fmt.Sscanf(str, "%d.%d.%d", &major, &minor, &commit)
	return uint32(major)*10000000 + uint32(minor)*100000 + uint32(commit)
}

// AppGetVersionInt returns the application version as an integer.
// C: version.c:46-50 — app_get_version_int
func AppGetVersionInt() uint32 {
	return ParseVersionInt(AppVersion())
}
