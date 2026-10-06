//go:build linux && !android

package main

// platformFini — linux platform teardown. Upstream C has no connman
// stop (the thread runs until process exit); the Go port owns a
// per-instance *System, so Stop() lets connmanThread close the bus
// connection and return before the rest of shutdown proceeds.
func platformFini(ctx *appContext) {
	ctx.platform.connman.Stop()
}
