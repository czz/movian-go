//go:build darwin

package core

// registerFASearchBackends — C gates BE_REGISTER(locatedb) behind
// CONFIG_LOCATEDB (configure.linux) and BE_REGISTER(spotlight) behind
// CONFIG_SPOTLIGHT (configure.osx). On macOS only spotlight exists.
func (bs *BackendSystem) registerFASearchBackends() {
	bs.registerSpotlightSearchBackend()
}
