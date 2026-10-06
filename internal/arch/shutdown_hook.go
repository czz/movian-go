package arch

// C: src/main.c — shutdown_hook_t list, shutdown_hook_add, shutdown_hook_run.

import (
	"slices"
	"sync"
)

//	C: typedef struct shutdown_hook {
//	  void (*fn)(void *opaque, int exitcode);
//	  void *opaque;
//	  int early;
//	} shutdown_hook_t;
type ShutdownHook struct {
	Fn     func(opaque any, exitCode int)
	Opaque any
	Early  int
}

var (
	shutdownHooksMu sync.Mutex
	shutdownHooks   []*ShutdownHook // C: static LIST_HEAD(, shutdown_hook) shutdown_hooks
)

// ShutdownHookAdd — C: shutdown_hook_add (main.c:702).
// Returns the hook handle (C returns shutdown_hook_t *).
func ShutdownHookAdd(fn func(opaque any, exitCode int), opaque any, early int) *ShutdownHook {
	sh := &ShutdownHook{Fn: fn, Opaque: opaque, Early: early}
	shutdownHooksMu.Lock()
	// C: LIST_INSERT_HEAD(&shutdown_hooks, sh, link)
	shutdownHooks = slices.Insert(shutdownHooks, 0, sh)
	shutdownHooksMu.Unlock()
	return sh
}

// ShutdownHookRun — C: shutdown_hook_run (main.c:718).
func ShutdownHookRun(early int, exitCode int) {
	shutdownHooksMu.Lock()
	hooks := shutdownHooks
	shutdownHooksMu.Unlock()
	for _, sh := range hooks {
		if sh.Early == early {
			sh.Fn(sh.Opaque, exitCode)
		}
	}
}
