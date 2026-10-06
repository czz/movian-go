//go:build linux && !android && !rpi && !sunxi

package main

import "github.com/czz/movian-go/internal/arch"

// init_linux_norpi.go — platformArch* hooks are no-ops on generic linux
// (the rpi build defines them in init_linux_rpi.go — C: rpi_main.c).

// platformArchEarlyStart — no-op on non-rpi linux.
func platformArchEarlyStart(ctx *appContext, ls *arch.LinuxSystem) {}

// platformArchMainStart — no-op on non-rpi linux.
func platformArchMainStart(ctx *appContext) {}
