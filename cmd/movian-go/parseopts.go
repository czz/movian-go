package main

// parseopts.go — Go port of C's parse_opts (src/main.c:523-717).
//
// C semantic contract:
//
//	parse_opts iterates argv with a while loop. Each iteration checks
//	argv[0] against known flags. If a flag matches, it (and its value
//	if applicable) is consumed. If no flag matches, the loop breaks —
//	remaining args are positional. After the loop, if any args remain,
//	argv[0] (the FIRST positional) becomes gconf.initial_url, stored
//	as the raw string from argv (no file:// conversion, no Abs()).
//
// This file reproduces that exact semantics in Go.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/czz/movian-go/internal/arch/mgos"
	"github.com/czz/movian-go/internal/gconf"
	"github.com/czz/movian-go/internal/version"
)

// parseOpts mirrors C's parse_opts (main.c:523-717).
// Returns GConf with parsed values.
// `args` is os.Args (including program name at [0]).
func parseOpts(args []string, g *gconf.T) {
	// C: gconf.binary = argv[0] (linux_main.c:145) — set before parsing
	g.ShellFD = -1
	g.Binary = args[0]

	// C: posix.c:137-155 — posix_init runs BEFORE parse_opts and seeds
	// gconf.cache_path/gconf.persistent_path (STOS fixed paths, else
	// HOME-based). --cache/--persistent below override these, and --help
	// prints them. Windows/android seed their own paths elsewhere.
	if mgos.Enabled {
		g.CachePath = mgos.CachePath
		g.PersistentPath = mgos.PersistentPath
	} else if home := os.Getenv("HOME"); home != "" &&
		runtime.GOOS != "windows" && runtime.GOOS != "android" {
		// C: cache uses APPNAME; upstream keeps the legacy
		// ".hts/showtime" persistent dir. Movian Go uses
		// ".movian-go"; bootstrap migrates the legacy dirs.
		g.CachePath = filepath.Join(home, ".cache", "movian-go")
		g.PersistentPath = filepath.Join(home, ".movian-go")
	}

	// C: argv++; argc--;  (skip program name)
	i := 1

	// C: while(argc > 0) { ... }
	for i < len(args) {
		arg := args[i]

		// C: if(!strcmp(argv[0], "-h") || !strcmp(argv[0], "--help"))
		if arg == "-h" || arg == "--help" {
			fmt.Printf("Movian Go %s\n"+
				"Copyright (C) 2006-2018 Lonelycoder AB\n"+
				"\n"+
				"Usage: %s [options] [<url>]\n"+
				"\n"+
				"  Options:\n"+
				"   -h, --help          - This help text.\n"+
				"   -d                  - Enable debug output.\n"+
				"   --no-ui             - Start without UI.\n"+
				"   --fullscreen        - Start in fullscreen mode.\n"+
				"   --libav-log         - Print libav log messages.\n"+
				"   --with-standby      - Enable system standby.\n"+
				"   --with-poweroff     - Enable system power-off.\n"+
				"   -s <path>           - Non-default settings path.\n"+
				"   --platform <p>      - Display server: x11 or wayland (GLFW).\n"+
				"   -L <ip:host>        - Send log messages to remote <ip:host>.\n"+
				"   --syslog            - Send log messages to syslog.\n"+
				"   -v <view>           - Use specific view for <url>.\n"+
				"   --cache <path>      - Set path for cache [%s].\n"+
				"   --persistent <path> - Set path for persistent stuff [%s].\n"+
				"   --disable-upnp      - Disable UPNP/DLNA stack.\n"+
				"   --disable-sd        - Disable service discovery (mDNS, etc).\n"+
				"   -p                  - Path to plugin directory to load\n"+
				"                         Intended for plugin development\n"+
				"   --plugin-repo       - URL to plugin repository\n"+
				"                         Intended for plugin development\n"+
				"   --proxy <host:port> - Use SOCKS 4/5 proxy for http requests.\n"+
				"   --ecmascript <path> - Load javascript file (alias: -j)\n"+
				"   --skin <skin>       - Select skin (for GLW ui)\n"+
				"\n"+
				"  URL is any URL-type supported, "+
				"e.g., \"file:///...\"\n"+
				"\n",
				version.AppVersion(),
				args[0],
				g.CachePath,
				g.PersistentPath)
			os.Exit(0)
		}

		// C: else if(!strcmp(argv[0], "-d"))
		if arg == "-d" {
			g.TraceLevel = 5 // TRACE_DEBUG
			i += 1
			continue
		}
		if arg == "--libav-log" {
			g.LibavLog = true
			i += 1
			continue
		}
		if arg == "--debug-glw" {
			g.DebugGLW = true
			i += 1
			continue
		}
		if arg == "--no-ui" {
			g.NoUI = true
			i += 1
			continue
		}
		if arg == "--fullscreen" {
			g.Fullscreen = true
			i += 1
			continue
		}
		if arg == "--syslog" {
			g.Syslog = true
			i += 1
			continue
		}
		// C: main.c:590-597 — --pointer-is-touch / --show-usage-events
		if arg == "--pointer-is-touch" {
			g.ConvertPointerToTouch = 1
			i += 1
			continue
		}
		if arg == "--show-usage-events" {
			g.ShowUsageEvents = 1
			i += 1
			continue
		}
		if arg == "--disable-upnp" {
			g.DisableUPnP = true
			i += 1
			continue
		}
		if arg == "--disable-sd" {
			g.DisableSD = true
			i += 1
			continue
		}
		if arg == "--disable-upgrades" {
			g.DisableUpgrades = true
			i += 1
			continue
		}
		// C: main.c:628-650 — runcontrol capability flags
		if arg == "--with-standby" {
			g.CanStandby = true
			i += 1
			continue
		}
		if arg == "--with-poweroff" {
			g.CanPoweroff = true
			i += 1
			continue
		}
		if arg == "--with-logout" {
			g.CanLogout = true
			i += 1
			continue
		}
		if arg == "--with-openshell" {
			g.CanOpenShell = true
			i += 1
			continue
		}
		if arg == "--without-exit" {
			g.CanNotExit = true
			i += 1
			continue
		}
		if arg == "--with-restart" {
			g.CanRestart = true
			i += 1
			continue
		}
		// C: else if(!strcmp(argv[0], "--stdin")) (main.c:578)
		if arg == "--stdin" {
			g.ListenOnStdin = true
			i += 1
			continue
		}

		// Two-arg flags: --flag <value>
		// C: else if(!strcmp(argv[0], "--skin") && argc > 1)
		if arg == "--skin" && i+1 < len(args) {
			g.Skin = args[i+1]
			i += 2
			continue
		}
		if arg == "-p" && i+1 < len(args) {
			g.DevPlugins = append(g.DevPlugins, args[i+1])
			i += 2
			continue
		}
		// C: else if(!strcmp(argv[0], "--plugin-repo") && argc > 1) (main.c:656)
		if arg == "--plugin-repo" && i+1 < len(args) {
			g.PluginRepo = args[i+1]
			i += 2
			continue
		}
		// C: else if(!strcmp(argv[0], "--bypass-ecmascript-acl")) (main.c:660)
		if arg == "--bypass-ecmascript-acl" {
			g.BypassEcmascriptACL = true
			i += 1
			continue
		}
		// C: else if(!strcmp(argv[0], "--ecmascript") && argc > 1) (main.c:663)
		// C's usage text documents "-j <path>" but only parses
		// --ecmascript; we accept both.
		if (arg == "--ecmascript" || arg == "-j") && i+1 < len(args) {
			g.LoadEcmascript = args[i+1]
			i += 2
			continue
		}
		// C: --vmir-bitcode (main.c:667) → gconf.load_np — VMIR/np is not
		// ported; the flag is intentionally absent.
		if arg == "-v" && i+1 < len(args) {
			g.InitialView = args[i+1]
			i += 2
			continue
		}
		if arg == "--cache" && i+1 < len(args) {
			g.CachePath = args[i+1]
			i += 2
			continue
		}
		if arg == "--persistent" && i+1 < len(args) {
			g.PersistentPath = args[i+1]
			i += 2
			continue
		}
		// C: --ui <ui> (main.c:681) — removed: GLFW is the only UI
		// frontend. --platform replaces it to pin the display server
		// (extension: upstream C had no such flag — X11 vs Wayland was
		// a compile-time choice).
		if arg == "--platform" && i+1 < len(args) {
			switch strings.ToLower(args[i+1]) {
			case "x11", "wayland":
				g.Platform = strings.ToLower(args[i+1])
			default:
				fmt.Fprintf(os.Stderr,
					"Invalid --platform value %q (expected x11 or wayland)\n",
					args[i+1])
			}
			i += 2
			continue
		}
		// C: else if (!strcmp(argv[0], "--upgrade-path") && argc > 1) (main.c:686)
		if arg == "--upgrade-path" && i+1 < len(args) {
			g.UpgradePath = args[i+1]
			i += 2
			continue
		}
		if arg == "--showtime-shell-fd" && i+1 < len(args) {
			g.ShellFD, _ = strconv.Atoi(args[i+1])
			i += 2
			continue
		}
		if arg == "--proxy" && i+1 < len(args) {
			// C: char *x = mystrdupa(argv[1]); char *pstr = strchr(x, ':');
			proxy := args[i+1]
			if idx := strings.Index(proxy, ":"); idx >= 0 {
				g.ProxyHost = proxy[:idx]
				g.ProxyPort, _ = strconv.Atoi(proxy[idx+1:])
			} else {
				g.ProxyHost = proxy
				g.ProxyPort = 1080
			}
			i += 2
			continue
		}

		// C: #ifdef __APPLE__ ... -psn ... (macOS process serial number)
		if strings.HasPrefix(arg, "-psn") {
			i += 1
			continue
		}

		// Go-specific extensions (not in C):
		// -headless: used for CI/testing to start HTTP input endpoint
		if arg == "-headless" || arg == "--headless" {
			i += 1
			continue
		}

		// C: else break;
		// Unknown arg → stop parsing. Remaining args are positional.
		break
	}

	// C: if(argc > 0) gconf.initial_url = argv[0];
	if i < len(args) {
		g.InitialURL = args[i]
	}

}
