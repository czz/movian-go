package main

import (
	"os"
	"runtime"

	"github.com/czz/movian-go/internal/gconf"

	"github.com/czz/movian-go/internal/arch"
	"github.com/czz/movian-go/internal/runcontrol"
)

// swrefreshKick — C: gconf.swrefresh (main.c:291-329). A send kicks the
// swthread into an immediate plugin/upgrade check (used by the rpi
// frontend's swrefresh() call on every UI (re)start).
var swrefreshKick = make(chan struct{}, 1)

func main() {
	runtime.LockOSThread()

	// CEF subprocesses re-exec this binary with --type=...; the
	// check must run before anything else (cef tag only — no-op
	// otherwise).
	cefEarlyCheck()

	// Parse CLI options — mirrors C's parse_opts (src/main.c:523-717).
	// Sets gconf.skin, gconf.initial_url, gconf.initial_view, etc.
	// --skin <value> consumes 2 args; first positional arg becomes
	// initial_url (raw string, no conversion). The posix_init path
	// defaults are seeded inside parseOpts so --help prints them and
	// --cache/--persistent override them, like C.
	gc := gconf.New()
	parseOpts(os.Args, gc)

	// C: posix_init (arch/posix/posix.c:110) — runs before parse_opts and
	// sets gconf.os_info (uname/linux_get_dist → PRETTY_NAME), srand,
	// setlocale, SIGPIPE ignore, rlimits. Go: parseOpts returns a fresh
	// gconf, so the os_info fill lands here (parse_opts never reads it).
	// SIGPIPE/locale are N/A in Go; C's 512MB RLIMIT_AS/DATA cap is
	// deliberately skipped — Go's runtime reserves large virtual address
	// space and would crash under it.
	gc.OSInfo = arch.GetOSInfo()

	// C: posix_init tail — if(gconf.trace_to_syslog) openlog(APPNAME,...)
	if gc.Syslog {
		arch.OpenSyslog("MovianGo")
	}

	ctx := newAppContext(gc)

	// Dispatch initial_url — mirrors C's navigator.c:278-291.
	// C: after nav_open0(nav, NAV_HOME, ...), if gconf.initial_url != NULL,
	//    wait for navigator_can_start, then dispatch
	//    event_create_openurl(.url = gconf.initial_url, .view = gconf.initial_view)
	//    via prop_send_ext_event(eventsink, e).
	// Go: StartNavigator (called inside newAppContext) opens page:home.
	//     DispatchInitialURL blocks until SetCanStart() is called (after
	//     plugin init), then dispatches via OpenURLWithView (event sink path).
	// The URL is used AS-IS (raw string from argv), matching C semantics.
	if gc.InitialURL != "" {
		go ctx.navSystem.DispatchInitialURL(gc.InitialURL, gc.InitialView)
	}

	// 34. runcontrol_init()
	shutdownCallback := func(exitCode int) {
		app_shutdown(ctx, exitCode)
	}
	// C: runcontrol_init() — can_* come from gconf (main.c:628-650,
	// set by --with-*/--without-exit options).
	ctx.runControl = runcontrol.NewRunControl(ctx.propManager,
		ctx.settingsManager, ctx.calloutSystem,
		gc.CanStandby, gc.CanPoweroff, gc.CanLogout,
		gc.CanOpenShell, gc.CanRestart, !gc.CanNotExit,
		gc.ShellFD, shutdownCallback)

	// ── UI main loop ──────────────────────────────────────────────────
	// NOTE: the GTK2 (ui_gu) and X11/GLX (glw_x11) frontends were
	// removed — GLFW is the only GLW UI, so renderLoop needs no --ui
	// switch (the flag itself is gone). Wayland vs X11 is a runtime
	// choice inside GLFW (--platform / GLFW_PLATFORM).
	renderLoop(ctx)

	// Shutdown is handled by the runControl shutdown callback.
	// app_shutdown is NOT called directly here to avoid double-fini (BUG-003).
	ctx.runControl.Shutdown(0)
}
