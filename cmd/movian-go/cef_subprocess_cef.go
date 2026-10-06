//go:build webpopup && cef

package main

// cef_subprocess_cef.go — CEF spawns renderer/GPU/utility workers
// by re-exec'ing this binary with --type=...; the cgo constructor
// in ui (cef_ctor) normally intercepts them before the Go runtime
// starts — this is a backstop for environments where .init_array
// ordering differs (cef_execute_process is idempotent for the
// browser process: returns -1).
import (
	"os"

	"github.com/czz/movian-go/internal/ui"
)

func cefEarlyCheck() {
	if code := ui.CefExecuteProcess(); code >= 0 {
		os.Exit(code)
	}
}
