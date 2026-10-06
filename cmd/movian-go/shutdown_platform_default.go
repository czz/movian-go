//go:build !linux || android

package main

// platformFini — no platform singleton teardown outside desktop linux
// (android has no connman: its platformState is empty).
func platformFini(ctx *appContext) {}
