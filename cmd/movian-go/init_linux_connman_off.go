//go:build linux && !android

package main

// init_linux_connman_off.go — no-op on desktop linux: upstream C
// calls connman_init only in rpi_main.c (init_linux_rpi.go does the
// same via connman.Start under the rpi+connman tags).
func platformConnmanStart(ctx *appContext) {}
