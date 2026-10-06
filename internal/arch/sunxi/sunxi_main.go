//go:build sunxi

// sunxi_main.go — C: src/arch/sunxi/sunxi_main.c — platform globals,
// signal/shutdown plumbing, arch_exit/arch_stop_req. The EGL/GLW
// ui_init/ui_run live in pkg/ui/glw/glw_sunxi.go (they need
// package-internal glw access); the main() sequencing is wired in
// cmd/movian-go (init_linux_sunxi.go / ui_glw_sunxi.go).
package sunxi

import (
	"fmt"
	"os"
)

// C: static int ctrlc / static int running (sunxi_main.c:40-41)
var (
	Ctrlc   int32
	Running int32 = 1
)

// Doexit — C: doexit (sunxi_main.c:325-332) — SIGTERM/SIGINT handler.
func Doexit() {
	fmt.Printf("ctrl:%d\n", Ctrlc)
	if Ctrlc != 0 {
		os.Exit(0)
	}
	Ctrlc = 1
}

// ArchStopReq — C: arch_stop_req (sunxi_main.c:400-405)
func ArchStopReq() int {
	Running = 0
	return 0
}
