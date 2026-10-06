//go:build !linux && !darwin

package core

// registerFASearchBackends — no fa_locatedb (CONFIG_LOCATEDB is
// linux-only) and no fa_spotlight (CONFIG_SPOTLIGHT is osx-only) exist
// on other platforms in C.
func (bs *BackendSystem) registerFASearchBackends() {}
