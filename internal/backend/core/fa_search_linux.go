//go:build linux

package core

// registerFASearchBackends — C gates BE_REGISTER(locatedb) behind
// CONFIG_LOCATEDB (configure.linux) and BE_REGISTER(spotlight) behind
// CONFIG_SPOTLIGHT (configure.osx). On Linux only locatedb exists.
func (bs *BackendSystem) registerFASearchBackends() {
	// C: be_locatedb.be_search = locatedb_search — runs
	// `locate -i -L -q -b 'query'` in a thread, parses results, and adds
	// matching files as nodes to model.nodes.
	bs.registerLocatedbSearchBackend()
}
