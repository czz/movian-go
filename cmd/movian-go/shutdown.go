package main

import (
	"os"
	"sync"

	"github.com/czz/movian-go/internal/arch"
	"github.com/czz/movian-go/internal/notifications"
)

var shutdownOnce sync.Once

// app_shutdown triggers the full shutdown sequence.
// Uses sync.Once to ensure it only runs once even if called from
// both the runcontrol callback and the end of main().
func app_shutdown(ctx *appContext, exitCode int) {
	shutdownOnce.Do(func() {
		doShutdown(ctx, exitCode)
	})
}

// doShutdown performs the actual shutdown sequence.
func doShutdown(ctx *appContext, exitCode int) {
	ts := ctx.traceSystem
	// C: app_shutdown (main.c:815)
	ts.Debug("core", "Shutdown requested, returncode = %d", exitCode)

	// C: shutdown_hook_run(1) — main.c:811 (early hooks, e.g. termios restore)
	arch.ShutdownHookRun(1, exitCode)

	// Destroy popups property
	ctx.propManager.DestroyByName(ctx.propManager.GetGlobal(), "popups")

	// Stop HTTP control server
	if ctx.httpServer != nil {
		ctx.httpServer.Close()
	}

	// Release the MPRIS bus name (Close is nil-safe when no session bus)
	if ctx.mprisServer != nil {
		ctx.mprisServer.Close()
	}

	// Platform teardown — linux: stops the connman worker (nil-safe;
	// C leaves the thread alive until exit, we let it return cleanly)
	platformFini(ctx)

	// Finalize API group
	ctx.initGroupSystem.FiniGroup(arch.InitGroupAPI)
	// C: main.c:846
	ts.Debug("core", "API group finished")

	// Finalize IPC group
	ctx.initGroupSystem.FiniGroup(arch.InitGroupIPC)
	// C: main.c:848
	ts.Debug("core", "IPC group finished")

	// C: there is no sd_fini — the avahi poll thread is left running
	// until process exit, exactly like the C build.

	// C: audio_fini (audio.c:153-156) is a no-op — nothing to finalize.

	// Stop playback pipeline
	if ctx.playback != nil {
		ctx.playback.Stop()
	}

	// Save and finalize playqueue
	if ctx.playqueue != nil {
		ctx.playqueue.Fini()
		// C: main.c:852 (ENABLE_PLAYQUEUE)
		ts.Debug("core", "Playqueue finished")
	}

	// Stop event consumer goroutines BEFORE finalizing navigator
	// to prevent races between consumer goroutines and Fini()
	if ctx.stopChan != nil {
		close(ctx.stopChan)
	}

	// Wait for event consumer goroutines to fully exit before destroying
	// the navigator. Without this, a consumer could still be inside
	// processNavEvent() (e.g. calling Open()) when Fini() destroys the
	// navigator's internal state.
	ctx.navSystem.WaitConsumers()

	// Finalize navigator
	ctx.navSystem.Fini()
	// C: main.c:854
	ts.Debug("core", "Navigator finished")

	// Finalize backend
	ctx.backendRegistry.BackendFini(ctx.propManager)
	// C: main.c:855
	ts.Debug("core", "Backend finished")

	// C: shutdown_hook_run(0) — main.c:827 (slow shutdown hooks)
	arch.ShutdownHookRun(0, exitCode)
	// C: main.c:856
	ts.Debug("core", "Slow shutdown hooks finished")

	// Finalize blobcache
	// C: main.c:857
	ts.Debug("core", "Blobcache finished")

	// Finalize metadata
	ctx.metadataManager.Fini()
	// C: main.c:859
	ts.Debug("core", "Metadb finished")

	// Finalize kvstore — C: kvstore_fini() → db_pool_close (main.c:835)
	ctx.kvstore.Fini()

	// Finalize notifications
	notifications.NotificationsFini(ctx.notifMgr)

	// C: asyncio_shutdown → asyncio_do_shutdown → fini_group(INIT_GROUP_ASYNCIO)
	//    (asyncio_posix.c:678) — runs ssdp_shutdown (byebye) et al.
	ctx.initGroupSystem.FiniGroup(arch.InitGroupAsyncIO)
	ctx.asyncIO.Stop()

	// Finalize callout system
	ctx.calloutSystem.Fini()

	// C: main.c:860 — APPNAMEUSER" terminated normally"
	ts.Debug("core", "Movian Go terminated normally")
	os.Exit(exitCode)
}
