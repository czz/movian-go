//go:build android

package main

// Platform early init — android does this inside coreInit
// (android_jni.go: device_id = md5hex(android_id + android_serialno),
// gconf.concurrency = runtime.NumCPU). The desktop path is a no-op here.

// platformState — no platform singleton on android.
type platformState struct{}

func platformEarlyStart(ctx *appContext) {}

// platformProcessMonitorStart — no android process monitor (upstream
// has none).
func platformProcessMonitorStart(ctx *appContext) {}

// platformMainStart — no android per-user media-dir services.
func platformMainStart(ctx *appContext) {}
