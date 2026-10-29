//go:build android

package core

// Live TV settings page — "settings:livetv". Lets the user pick the
// channel-list source and a JSON channel map on disk. Exists because
// some vendor boxes publish a broken or package-locked TvProvider
// while their inputs still tune raw feeder URIs; a file carrying
// name/number/inputId/uri per channel sidesteps the provider entirely.
// The file reuses the discovered-channels sidecar schema that
// Tv.java merges (<externalFilesDir>/tv_channels.json):
//
//	{"channels": [{"id","inputId","name","number","uri"}...], "epgUrl": "..."}
//
// Values persist in the "tvinput:store" kvstore page (same domain as
// the EPG URL sidecar), so the runtime read path never touches the
// settings store.
import (
	"encoding/json"
	"strings"

	"github.com/czz/movian-go/internal/arch"
	"github.com/czz/movian-go/internal/db/kvstore"
	facore "github.com/czz/movian-go/internal/fileaccess"
	propcore "github.com/czz/movian-go/internal/prop"
	settingscore "github.com/czz/movian-go/internal/settings"
)

// Channel-list source ids — values of the "chanSrc" kvstore key.
const (
	tvSrcAuto     = "auto"
	tvSrcProvider = "provider"
	tvSrcFile     = "file"
	tvSrcMerge    = "merge"
)

func (bs *BackendSystem) tvKvGet(key string) string {
	if bs.kvstore == nil {
		return ""
	}
	return bs.kvstore.UrlOptGetString("tvinput:store",
		kvstore.DomainPlugin, key)
}

func (bs *BackendSystem) tvKvSet(key, val string) {
	if bs.kvstore != nil {
		bs.kvstore.UrlOptSet("tvinput:store", kvstore.DomainPlugin,
			key, kvstore.SetString, val)
	}
}

// tvChannelSource — configured channel-list source, "auto" (provider +
// Java sidecar fallback, the pre-setting behavior) when unset.
func (bs *BackendSystem) tvChannelSource() string {
	switch bs.tvKvGet("chanSrc") {
	case tvSrcProvider, tvSrcFile, tvSrcMerge:
		return bs.tvKvGet("chanSrc")
	}
	return tvSrcAuto
}

// tvLoadChannelsFromFile — parse the JSON channel map the user picked
// in settings:livetv. Entries without inputId tune through the first
// non-passthrough input; missing ids get synthetic negative keys so
// they can't collide with TvProvider rowids.
func (bs *BackendSystem) tvLoadChannelsFromFile() []tvChannel {
	u := bs.tvKvGet("chanList")
	if u == "" || bs.fileAccessManager == nil {
		return nil
	}
	buf, err := facore.FALoad(bs.fileAccessManager, u, nil, nil, 0)
	if err != nil {
		return nil
	}
	var doc struct {
		Channels []tvChannel `json:"channels"`
		EPGURL   string      `json:"epgUrl"`
	}
	if json.Unmarshal(buf.Data, &doc) != nil ||
		len(doc.Channels) == 0 {
		return nil
	}
	if doc.EPGURL != "" {
		bs.tvKvSet("epgUrl", doc.EPGURL)
		tvGuideURL.Store(doc.EPGURL) // same priming as the Java sidecar
	}
	defInput := ""
	for _, in := range bs.tvLoadInputs() {
		if !in.Passthrough {
			defInput = in.ID
			break
		}
	}
	for i := range doc.Channels {
		if doc.Channels[i].InputID == "" {
			doc.Channels[i].InputID = defInput
		}
		if doc.Channels[i].ID == 0 {
			doc.Channels[i].ID = int64(-(i + 1))
		}
		doc.Channels[i].URI = tvNormalizeFileURI(doc.Channels[i].URI)
	}
	sortTvChannels(doc.Channels)
	return doc.Channels
}

// tvNormalizeFileURI — a bare absolute path in the channel file is a
// media source; prefix it with file://. Anything already carrying a
// scheme is left untouched — TIF-style URIs (dvb://, content://, …)
// keep the TvView path, media URLs (file://, http://, …) play through
// movian's own pipeline. No vendor conventions hardcoded here: the
// channel file carries whatever URI its box can serve.
func tvNormalizeFileURI(uri string) string {
	if strings.HasPrefix(uri, "/") {
		return "file://" + uri
	}
	return uri
}

// tvIsMediaURI — true when the channel URI is directly playable by
// movian's own pipeline (file/http/…), as opposed to a TIF tune URI
// (dvb://, content://, vendor schemes) that needs TvView.
func tvIsMediaURI(uri string) bool {
	switch {
	case strings.HasPrefix(uri, "/"):
		return true
	case strings.HasPrefix(uri, "file://"), strings.HasPrefix(uri, "http://"),
		strings.HasPrefix(uri, "https://"), strings.HasPrefix(uri, "rtsp://"),
		strings.HasPrefix(uri, "rtmp://"), strings.HasPrefix(uri, "smb://"),
		strings.HasPrefix(uri, "nfs://"), strings.HasPrefix(uri, "hls://"):
		return true
	}
	return false
}

// mergeTvChannels — file channels first (they are the curated list),
// then provider channels not already covered. Dedup by uri when set,
// else by name.
func mergeTvChannels(file, provider []tvChannel) []tvChannel {
	uris := make(map[string]bool, len(file))
	names := make(map[string]bool, len(file))
	for _, ch := range file {
		if ch.URI != "" {
			uris[ch.URI] = true
		}
		names[ch.Name] = true
	}
	out := make([]tvChannel, 0, len(file)+len(provider))
	out = append(out, file...)
	for _, ch := range provider {
		if ch.URI != "" && uris[ch.URI] {
			continue
		}
		if names[ch.Name] {
			continue
		}
		out = append(out, ch)
	}
	return out
}

// tvSettingsInit — build the "Live TV" settings directory: channel
// list source multiopt, the channel file row + file picker, and a
// shortcut to the input's own setup activity. Called only when the
// device exposes TvInputServices (same gate as the tvinput: backend).
func (bs *BackendSystem) tvSettingsInit() {
	sm := bs.settingsMgr
	if sm == nil {
		return
	}
	pm := bs.propManager
	dir := sm.AddDir(nil, sm.P("Live TV"), "",
		"skin://icons/ic_tv_48px.svg",
		sm.P("Channel list source and tuner setup"), "settings:livetv")

	// Channel list source — persisted straight into the tvinput
	// kvstore page by the change callback.
	sm.SettingCreate(settingscore.SettingMultiOpt, dir,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Channel list"),
		settingscore.SettingTagValue, bs.tvChannelSource(),
		settingscore.SettingTagOption, tvSrcAuto, sm.P("Automatic"),
		settingscore.SettingTagOption, tvSrcProvider, sm.P("TV provider"),
		settingscore.SettingTagOption, tvSrcFile, sm.P("Channel list file"),
		settingscore.SettingTagOption, tvSrcMerge,
		sm.P("File + TV provider"),
		settingscore.SettingTagCallback,
		func(opaque any, value any) {
			if s, ok := value.(string); ok {
				bs.tvKvSet("chanSrc", s)
			}
		}, nil,
	)

	// Single action row — title plus the current file as its
	// description. Built by hand (like the tvchannels page items) so
	// the subtitle can be bound live to fileProp.
	fileProp := pm.CreateRootEx("", false)
	fileProp.Retain()
	pm.SetStringEx(fileProp, nil, bs.tvKvGet("chanList"),
		propcore.StringUTF8)
	fileProp.Subscribe(func(o any, et propcore.EventType,
		args ...any) {
		if (et != propcore.EventSetCString &&
			et != propcore.EventSetRString) || len(args) == 0 {
			return
		}
		if s, ok := args[0].(string); ok {
			bs.tvKvSet("chanList", s)
		}
	}, nil, propcore.SubNoInitialUpdate)

	nodes := pm.CreateEx(dir, "nodes", nil, false, false)
	item := pm.CreateEx(nodes, "", nil, false, false)
	pm.SetStringEx(pm.CreateEx(item, "type", nil, false, false),
		nil, "action", propcore.StringUTF8)
	pm.SetIntEx(pm.CreateEx(item, "enabled", nil, false, false), nil, 1)
	im := pm.CreateEx(item, "metadata", nil, false, false)
	pm.Link(sm.P("Channel list file…"),
		pm.CreateEx(im, "title", nil, false, false), nil, false, false)
	pm.Link(fileProp,
		pm.CreateEx(im, "shortdesc", nil, false, false), nil, false, false)
	es := pm.CreateEx(item, "eventSink", nil, false, false)
	es.Subscribe(func(o any, et propcore.EventType, args ...any) {
		if et != propcore.EventExtEvent {
			return
		}
		// The picker's scandir is non-interactive and can't ask for
		// storage permission itself — request it up front or es://
		// opens on a dead "access rejected" page.
		arch.AndroidGetPermission(
			"android.permission.READ_EXTERNAL_STORAGE", true)
		cur := pm.GetString(fileProp, "")
		// Root the picker on the sdcard: / itself is SELinux-denied
		// on vendor boxes (open / → EACCES), while /storage and
		// below scan fine. The parent chain still surfaces /storage
		// (real SDs too) — / errors harmlessly at the tail.
		start := "file:///storage/emulated/0"
		if cur != "" {
			if parent, err := facore.FAParent(cur); err == nil &&
				parent != "" {
				start = parent
			}
		}
		facore.FilepickerPickToPropAt(bs.fileAccessManager,
			"Select channel list file", fileProp,
			cur, facore.FilepickerFiles, start)
	}, nil, propcore.SubNoInitialUpdate)

	// Same setup shortcut the empty channel list offers.
	sm.CreateActionProp(dir, "Open tuner setup…", "setup",
		func(opaque any, value any) {
			for _, in := range bs.tvLoadInputs() {
				if in.Setup {
					arch.AndroidTvSetup(in.ID)
					return
				}
			}
		}, nil, 0)

	sm.CreateInfo(dir, "",
		sm.P("The channel list file uses the JSON map schema: "+
			"{\"channels\":[{\"id\",\"name\",\"number\",\"inputId\","+
			"\"uri\",\"video\"}],\"epgUrl\":\"…\"}. \"id\" is the "+
			"vendor input's internal channel id and tunes through "+
			"the TvView; \"uri\" is the raw vendor feeder (e.g. "+
			"/mnt/dtvfs/live/dvb:…) used when no id is present; a "+
			"missing \"inputId\" falls back to the first "+
			"non-passthrough input."))
}
