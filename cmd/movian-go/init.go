package main

import (
	"github.com/czz/movian-go/internal/gconf"

	"github.com/czz/movian-go/internal/api/mgpp"
	"github.com/czz/movian-go/internal/api/mpris"
	"github.com/czz/movian-go/internal/api/screenshot"
	"github.com/czz/movian-go/internal/arch"
	"github.com/czz/movian-go/internal/asyncio"
	audiocore "github.com/czz/movian-go/internal/audio/core"
	"github.com/czz/movian-go/internal/backend"
	"github.com/czz/movian-go/internal/backend/bittorrent"
	backendcore "github.com/czz/movian-go/internal/backend/core"
	"github.com/czz/movian-go/internal/backend/playlist/playqueue"
	backendprop "github.com/czz/movian-go/internal/backend/prop"
	"github.com/czz/movian-go/internal/backend/rtmp"
	"github.com/czz/movian-go/internal/callout"
	"github.com/czz/movian-go/internal/db/kvstore"
	"github.com/czz/movian-go/internal/ecmascript"
	"github.com/czz/movian-go/internal/event"
	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/fileaccess/scanner"
	"github.com/czz/movian-go/internal/fileaccess/smb"
	"github.com/czz/movian-go/internal/htsmsg"
	"github.com/czz/movian-go/internal/ipc"
	"github.com/czz/movian-go/internal/keyring"
	"github.com/czz/movian-go/internal/media"
	mediasettings "github.com/czz/movian-go/internal/media/settings"
	"github.com/czz/movian-go/internal/metadata"
	navcore "github.com/czz/movian-go/internal/navigator"
	"github.com/czz/movian-go/internal/networking/ftpserver"
	httpnet "github.com/czz/movian-go/internal/networking/http"
	"github.com/czz/movian-go/internal/networking/ifaddr"
	"github.com/czz/movian-go/internal/notifications"
	"github.com/czz/movian-go/internal/plugins"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/runcontrol"
	"github.com/czz/movian-go/internal/sd"
	"github.com/czz/movian-go/internal/service"
	"github.com/czz/movian-go/internal/settings"
	"github.com/czz/movian-go/internal/subtitles"
	"github.com/czz/movian-go/internal/task"
	"github.com/czz/movian-go/internal/ui"

	// C links ext_subtitles.o/video_overlay.o/sub_ass.o unconditionally;
	// the Go port self-registers its mediacore hooks via init().
	textpkg "github.com/czz/movian-go/internal/text"
	"github.com/czz/movian-go/internal/trace"
	"github.com/czz/movian-go/internal/usage"
	"github.com/czz/movian-go/internal/video"
)

// appContext holds all initialized subsystems for cleanup and render access.
type appContext struct {
	gconf             *gconf.T // C: gconf_t gconf — owned by main, injected into consumers
	propManager       *propcore.PropManager
	audioManager      *audiocore.AudioManager
	calloutSystem     *callout.CalloutSystem
	taskSystem        *task.TaskSystem
	propPageManager   *backendprop.PropPageManager
	backendRegistry   *backend.BackendRegistry
	backendSystem     *backendcore.BackendSystem
	faManager         *fileaccesscore.FileAccessManager
	initGroupSystem   *arch.InitGroupSystem
	notifMgr          *notifications.NotificationManager
	navSystem         *navcore.NavigatorSystem
	serviceSystem     *service.ServiceSystem
	asyncIO           *asyncio.AsyncIO
	runControl        *runcontrol.RunControl
	settingsManager   *settings.SettingsManager
	httpServer        *httpnet.HTTPServer
	playback          *media.PlaybackPipeline
	playqueue         *playqueue.PlayQueue
	keyring           *keyring.Keyring
	traceSystem       *trace.TraceSystem
	store             *htsmsg.Store
	kvstore           *kvstore.KVStore
	metadataManager   *metadata.MetadataManager
	eventManager      *event.EventManager
	btg               *bittorrent.BtGlobal  // C: bt_global_t btg — owned by main
	sdSys             *sd.System            // C: sd.c/avahi.c/bonjour.c statics
	clipboard         *ui.Clipboard         // C: ui/clipboard.c statics
	subSys            *subtitles.System     // C: subtitles.c statics
	esSys             *ecmascript.System    // C: ecmascript.c file statics
	textSys           *textpkg.System       // C: freetype.c/fontstash.c statics
	mediaSettingsSys  *mediasettings.System // C: media_settings.c statics
	rtmpBackend       *rtmp.Backend         // C: rtmp.c statics
	platform          platformState         // C: platform singletons (linux.c/darwin.c/…)
	smbSys            *smb.System           // C: smb_global_mutex + cifs conn list
	usageCfg          usage.Config
	usageReporter     *usage.Reporter
	mediaSystem       *media.MediaSystem
	enableBinReplace  int
	enableOmnigrade   int              // C: gconf.* fields usage.c reads at send time
	dataMigrations    []dataMigration  // movian-go rebrand: dir migrations done in bootstrap
	indexer           *scanner.Indexer // C: fa_indexer.c statics
	stopChan          chan struct{}
	screenshotHandler *screenshot.ScreenshotHandler
	persistentPath    string
	netIfMgr          *ifaddr.NetIfAddrManager
	mgppClient        *mgpp.MGPPClient
	nmbManager        *smb.NMBManager
	ftpServer         *ftpserver.Server
	mprisServer       *mpris.Server
	ipc               *ipc.IPC
}

// usageCfg — C: the gconf.* fields the usage.c singleton reads at send
// time. The Reporter keeps a pointer to ctx.usageCfg so values filled later
// in init (device_id, lang, track) and the settings:dev
// "disableanalytics" toggle are read live like gconf.

// vpiHandlers — C: the vpi_handlers LIST_HEAD filled by VPI_REGISTER
// linker-section initializers (video_playback.c:1028). In Go the handler
// set is explicit: listed here and fanned out by the playbackInfoFn hook
// wired below. Platform handlers append themselves (init_linux_rpi.go).
var vpiHandlers = []video.VPIHandler{
	ecmascript.ScrobbleVideo, // C: VPI_REGISTER(es_scrobble_video)
}

// videoPlaybackInfoInvoke — C: video_playback_info_invoke
// (video_playback.c:1038-1044) — calls every registered handler.
func videoPlaybackInfoInvoke(op video.VPIOp, vpi *htsmsg.HTSMsg,
	p, origin *propcore.Prop) {
	for _, h := range vpiHandlers {
		h(op, vpi, p, origin)
	}
}

// newAppContext initializes all subsystems in order and returns an appContext.
// Critical steps (unicode, prop, navigator) cause os.Exit on failure.
// Soft steps (db, blobcache, media, etc.) log a warning and continue.
// On critical failure, cleanupOnFail rolls back already-initialized subsystems.
func newAppContext(gc *gconf.T) *appContext {
	ctx := &appContext{gconf: gc}
	var cleanupFns []func()
	bootstrapPhase(ctx, gc)

	cleanupOnFail := func() {
		for i := len(cleanupFns) - 1; i >= 0; i-- {
			cleanupFns[i]()
		}
	}

	// Same order as the monolithic composition root (see BASELINE.md §5.1).
	wireCore(ctx, gc, cleanupOnFail)
	wireStorage(ctx)
	wireMedia(ctx)
	wireUI(ctx, cleanupOnFail)
	wireNetwork(ctx, gc)
	return ctx
}

// pluginSearchAdapter adapts *plugins.PluginManager to the
// backendcore.PluginSearchProvider interface, converting []*plugins.Plugin
// to []backendcore.PluginSearchEntry. This avoids an import cycle between
// backend/core and plugins.
type pluginSearchAdapter struct {
	pm *plugins.PluginManager
}

func (a *pluginSearchAdapter) GetPlugins() []backendcore.PluginSearchEntry {
	pms := a.pm.GetPlugins()
	result := make([]backendcore.PluginSearchEntry, 0, len(pms))
	for _, p := range pms {
		var repoProp *propcore.Prop
		if p.RepoModel != nil {
			repoProp, _ = p.RepoModel.(*propcore.Prop)
		}
		result = append(result, backendcore.PluginSearchEntry{
			Title:     p.Title,
			RepoModel: repoProp,
		})
	}
	return result
}

// LoadAllRepos — C: plugins_upgrade_check (plugins.c:1274) — refreshes
// repo listings when plugin:repo pages open.
func (a *pluginSearchAdapter) LoadAllRepos() error {
	return a.pm.LoadAllRepos()
}

// ProbeForAutoInstall — C: plugin_probe_for_autoinstall (fa_audio.c:160).
// Delegates to the plugin manager so the fileaccess audio demux can
// detect auto-installable plugin bundles.
func (a *pluginSearchAdapter) ProbeForAutoInstall(fh any, buf []byte, length int, url string) bool {
	return a.pm.ProbeForAutoInstall(fh, buf, length, url)
}
