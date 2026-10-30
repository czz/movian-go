//go:build android

package core

// TV Input Framework backend — "tvinput:" URLs. New in Go: upstream C
// has no TIF support (Tvheadend via HTSP was its answer). Channels come
// from the system TvProvider — whatever TvInputService published them
// (OEM tuner, IPTV, streaming inputs). Playback happens in a TvView
// owned by GLWActivity: the surface renders below the translucent GLW
// layer, exactly like the video renderer.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/czz/movian-go/internal/epg"

	"github.com/czz/movian-go/internal/arch"
	"github.com/czz/movian-go/internal/db/kvstore"
	"github.com/czz/movian-go/internal/event"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/trace"
)

// tvOpenError — inline nav_open_error equivalent (navigator can't be
// imported from backend/core — import cycle).
func tvOpenError(pm *propcore.PropManager, root *propcore.Prop,
	msg string) error {
	model := pm.CreateEx(root, "model", nil, false, false)
	pm.RefInc(model)
	pm.SetStringEx(pm.CreateEx(model, "type", nil, false, false),
		nil, "openerror", propcore.StringUTF8)
	pm.SetIntEx(pm.CreateEx(model, "loading", nil, false, false),
		nil, 0)
	pm.SetStringEx(pm.CreateEx(model, "error", nil, false, false),
		nil, msg, propcore.StringUTF8)
	pm.SetIntEx(pm.CreateEx(root, "directClose", nil, false, false),
		nil, 1)
	pm.RefDec(model)
	return nil
}

// tvInput — one TvInputService from Tv.listInputs() (JSON).
type tvInput struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Passthrough bool   `json:"passthrough"`
	Setup       bool   `json:"setup"`
}

// tvChannel — one browsable TvProvider channel from Tv.listChannels().
// URI is optional: when set (discovered vendor channel maps, e.g. TIM
// box /mnt/dtvfs/live/dvb:<triplet>) it tunes the raw feeder URI and
// the rowid is only a list key — it goes stale on rescan, the URI
// survives because the triplet is the vendor's stable service id.
type tvChannel struct {
	ID      int64  `json:"id"`
	InputID string `json:"inputId"`
	Name    string `json:"name"`
	Number  string `json:"number"`
	URI     string `json:"uri,omitempty"`
}

// tvLoadChannels fetches the channel list from the configured source:
// "auto"/"provider" → the system TvProvider (Java side, incl. its
// discovered-channels sidecar); "file" → only the JSON channel map
// picked in settings:livetv; "merge" → file entries followed by the
// provider's, deduplicated.
func (bs *BackendSystem) tvLoadChannels() []tvChannel {
	src := bs.tvChannelSource()
	if src != tvSrcAuto && src != tvSrcProvider {
		if chans := bs.tvLoadChannelsFromFile(); chans != nil {
			if src == tvSrcFile {
				return chans
			}
			return mergeTvChannels(chans, bs.tvLoadProviderChannels())
		}
		bs.traceSystem.Trace(trace.TRACE_ERROR, "TV",
			"channel list file %q unreadable or empty", bs.tvKvGet("chanList"))
		if src == tvSrcMerge {
			return bs.tvLoadProviderChannels()
		}
		return nil
	}
	return bs.tvLoadProviderChannels()
}

// tvLoadProviderChannels — TvProvider channels via the Java side.
func (bs *BackendSystem) tvLoadProviderChannels() []tvChannel {
	raw := arch.AndroidTvListChannels()
	var chans []tvChannel
	if err := json.Unmarshal([]byte(raw), &chans); err != nil {
		bs.traceSystem.Trace(trace.TRACE_ERROR, "TV",
			"listChannels failed: %v raw=%q", err, raw)
		return nil
	}
	sortTvChannels(chans)
	return chans
}

// sortTvChannels — Logical Channel Numbering: order by numeric
// "number" when the source publishes it (TvContract
// COLUMN_DISPLAY_NUMBER, the sidecar, or the channel list file).
// Unnumbered entries keep their discovery order at the tail — a
// stable sort keeps zap indices consistent everywhere.
func sortTvChannels(chans []tvChannel) {
	sort.SliceStable(chans, func(a, b int) bool {
		na, ea := strconv.Atoi(chans[a].Number)
		nb, eb := strconv.Atoi(chans[b].Number)
		if ea == nil && eb == nil {
			return na < nb
		}
		return ea == nil
	})
}

// tvLoadInputs fetches the input list from the Java side.
func (bs *BackendSystem) tvLoadInputs() []tvInput {
	raw := arch.AndroidTvListInputs()
	var inputs []tvInput
	if err := json.Unmarshal([]byte(raw), &inputs); err != nil {
		bs.traceSystem.Trace(trace.TRACE_ERROR, "TV",
			"listInputs failed: %v raw=%q", err, raw)
		return nil
	}
	return inputs
}

// tvOpen — backend Open for "tvinput:" URLs:
//
//	tvinput:                    → channel list page
//	tvinput:setup               → first input's setup activity (channel scan)
//	tvinput:play/<input>/<id>   → tune TvView to the channel, minimal OSD page
func (bs *BackendSystem) tvOpen(page any, url0 string, sync bool) error {
	pm := bs.propManager
	propRoot, ok := page.(*propcore.Prop)
	if !ok {
		return fmt.Errorf("tvinput: backend cannot handle non-prop page")
	}
	path := strings.TrimPrefix(url0, "tvinput:")

	switch {
	case strings.HasPrefix(path, "play/"):
		return bs.tvPlayPage(propRoot, path[len("play/"):])

	case strings.HasPrefix(path, "setup"):
		// Optional explicit input id after "setup/".
		inputID := ""
		if rest := strings.TrimPrefix(path, "setup/"); rest != path {
			if un, err := url.PathUnescape(rest); err == nil {
				inputID = un
			}
		}
		if inputID == "" {
			for _, in := range bs.tvLoadInputs() {
				if in.Setup {
					inputID = in.ID
					break
				}
			}
		}
		if inputID == "" || !arch.AndroidTvSetup(inputID) {
			return tvOpenError(pm, propRoot,
				"No TV input provides a setup activity")
		}
		// Like androidsettings: the system activity is now in front;
		// self-close so BACK lands on the channel list.
		pm.SetIntEx(pm.CreateEx(propRoot, "close", nil, false, false),
			nil, 1)
		return nil
	}

	return bs.tvChannelsPage(propRoot)
}

// ---- shared TV session state ----------------------------------------
// The channel list page (PiP) and the play page (fullscreen) share one
// TvView session. tvTunedKey tracks what's tuned so page hand-offs
// don't retune; tvGuide caches the XMLTV guide across page opens.
var (
	tvTunedKey      string
	tvGuideMu       sync.Mutex
	tvGuide         *epg.Guide
	tvGuideFetching atomic.Bool
	tvGuideURL      atomic.Value // stores string — cross-goroutine EPG URL
)

// Keep in sync with glwskins/flat/pages/tvchannels.view: the left
// column is tvListWem wide; the PiP occupies the remaining width for
// the top tvVidHem ems.
const (
	tvListWem = 18
	tvVidHem  = 21
)

// tvEPGURL — XMLTV source: kvstore override, primed by the channels
// sidecar's "epg" field.
func (bs *BackendSystem) tvEPGURL() string {
	if u, _ := tvGuideURL.Load().(string); u != "" {
		return u
	}
	u := ""
	if bs.kvstore != nil {
		u = bs.kvstore.UrlOptGetString("tvinput:store",
			kvstore.DomainPlugin, "epgUrl")
	}
	if v := arch.AndroidTvEpgURL(); v != "" {
		u = v
		if bs.kvstore != nil {
			bs.kvstore.UrlOptSet("tvinput:store", kvstore.DomainPlugin,
				"epgUrl", kvstore.SetString, u)
		}
	}
	tvGuideURL.Store(u)
	return u
}

// tvGuideGet — returns the cached guide or kicks off the fetch once.
// onLoaded (may be nil) runs when the async fetch lands.
func (bs *BackendSystem) tvGuideGet(chans []tvChannel,
	onLoaded func(*epg.Guide)) *epg.Guide {
	tvGuideMu.Lock()
	g := tvGuide
	tvGuideMu.Unlock()
	if g != nil || bs.tvEPGURL() == "" ||
		!tvGuideFetching.CompareAndSwap(false, true) {
		return g
	}
	go func() {
		defer tvGuideFetching.Store(false)
		names := make([]string, len(chans))
		for i := range chans {
			names[i] = chans[i].Name
		}
		ctx, cancel := context.WithTimeout(context.Background(),
			60*time.Second)
		defer cancel()
		u := bs.tvEPGURL()
		g, err := epg.FetchXMLTV(ctx, u, epg.KeepForNames(names))
		if err != nil {
			bs.traceSystem.Trace(trace.TRACE_ERROR, "TV",
				"epg fetch %s: %v", u, err)
			return
		}
		tvGuideMu.Lock()
		tvGuide = g
		tvGuideMu.Unlock()
		bs.traceSystem.Trace(trace.TRACE_DEBUG, "TV",
			"epg guide loaded: %s", u)
		if onLoaded != nil {
			onLoaded(g)
		}
	}()
	return nil
}

// tvFillChannelEvents — fill each channel's metadata.events triple
// (list/current/next) from the guide for [now, now+24h].
func tvFillChannelEvents(pm *propcore.PropManager, ev []chanEvProps,
	chans []tvChannel, g *epg.Guide) {
	if g == nil {
		return
	}
	now := time.Now()
	end := now.Add(24 * time.Hour)
	for i := range chans {
		if i >= len(ev) || ev[i].list == nil {
			continue
		}
		tvSetEvents(pm, ev[i].list, ev[i].cur, ev[i].next,
			g.EventsFor(chans[i].Name, now, end), now)
	}
}

// tvTuneChannel — tune, deduplicated by channel key across both pages.
func (bs *BackendSystem) tvTuneChannel(ch tvChannel) {
	// Media-URI channels without a provider rowid play through
	// movian's own pipeline (their item URL is the media URL
	// itself) — nothing to feed the TvView. Rowid channels must
	// continue to the real tune below.
	if ch.ID <= 0 && bs.tvIsFeederURI(ch.URI) {
		return
	}
	// The TvView session renders outside the media-pipe system, so no
	// MpBecomePrimary runs to stop a previous primary (radio/audio
	// file). Stop it explicitly, exactly like a pipe-based playback
	// becoming primary would.
	if bs.mediaSys != nil {
		bs.mediaSys.MediaStopPrimary(bs.propManager)
	}
	key := ch.InputID + "|" + ch.URI
	if ch.URI == "" {
		key = fmt.Sprintf("%s|%d", ch.InputID, ch.ID)
	}
	if tvTunedKey == key && arch.AndroidTvIsTuned() {
		return
	}
	// Prefer the provider rowid when real (file-loaded channels get
	// negative synthetic ids): it is the tune path that has always
	// worked on vendor boxes — a raw uri is only the fallback.
	if ch.ID > 0 && ch.InputID != "" {
		arch.AndroidTvTune(ch.InputID, ch.ID)
	} else if ch.URI != "" {
		arch.AndroidTvTuneUri(ch.InputID, ch.URI)
	} else {
		arch.AndroidTvTune(ch.InputID, ch.ID)
	}
	tvTunedKey = key
}

// tvRelease — stop the session and restore the fullscreen rect.
func (bs *BackendSystem) tvRelease() {
	arch.AndroidTvUntune()
	tvTunedKey = ""
	arch.AndroidTvSetVideoRect(0, 0, 0, 0)
}

// tvPageModelType — the model.type of a sibling nav page prop.
func tvPageModelType(pm *propcore.PropManager, p *propcore.Prop) string {
	if p == nil {
		return ""
	}
	m := pm.Find(p, "model")
	if m == nil {
		return ""
	}
	return pm.GetString(pm.Find(m, "type"), "")
}

// tvPiPRect — PiP rect in screen pixels for the channel list page.
func tvPiPRect(pm *propcore.PropManager) (int, int, int, int) {
	ui := pm.Ensure(pm.GetGlobal(), "userinterfaces", "ui")
	if ui == nil {
		return 0, 0, 0, 0
	}
	em := pm.GetInt(pm.Find(ui, "size"), 30)
	w := pm.GetInt(pm.Find(ui, "width"), 0)
	return tvListWem * em, 0, w, tvVidHem * em
}

// tvChannelsPage — the "Live TV" root: browsable channels as list
// items; when the provider is empty a single setup row points at the
// input's scan activity.
func (bs *BackendSystem) tvChannelsPage(propRoot *propcore.Prop) error {
	pm := bs.propManager
	model := pm.CreateEx(propRoot, "model", nil, false, false)
	pm.SetStringEx(pm.CreateEx(model, "type", nil, false, false),
		nil, "tvchannels", propcore.StringUTF8)
	meta := pm.CreateEx(model, "metadata", nil, false, false)
	pm.SetStringEx(pm.CreateEx(meta, "title", nil, false, false),
		nil, "Live TV", propcore.StringUTF8)
	nodes := pm.CreateEx(model, "nodes", nil, false, false)
	pm.SetIntEx(pm.CreateEx(model, "loading", nil, false, false),
		nil, 0)

	chans := bs.tvLoadChannels()
	bs.traceSystem.Trace(trace.TRACE_DEBUG, "TV",
		"channels page: %d channels", len(chans))

	if len(chans) == 0 {
		// Empty provider — offer the input's own setup (e.g. the OEM
		// DVB-T scan) instead of a dead page.
		arch.AndroidTvSetVideoRect(0, 0, 0, 0)
		item := pm.CreateEx(nil, "", nil, false, false)
		pm.SetStringEx(pm.CreateEx(item, "type", nil, false, false),
			nil, "androidapp", propcore.StringUTF8)
		im := pm.CreateEx(item, "metadata", nil, false, false)
		pm.SetStringEx(pm.CreateEx(im, "title", nil, false, false),
			nil, "Set up TV channels…", propcore.StringUTF8)
		pm.SetStringEx(pm.CreateEx(im, "icon", nil, false, false),
			nil, "skin://icons/ic_tv_48px.svg", propcore.StringUTF8)
		es := pm.CreateEx(item, "eventSink", nil, false, false)
		es.Subscribe(func(o any, et propcore.EventType, args ...any) {
			if et != propcore.EventExtEvent {
				return
			}
			for _, in := range bs.tvLoadInputs() {
				if in.Setup {
					arch.AndroidTvSetup(in.ID)
					return
				}
			}
		}, nil, propcore.SubNoInitialUpdate)
		pm.SetParentEx(item, nodes, nil, "")
		return nil
	}

	// focusIdx — debounced focused index written by the view
	// (set() + delay() on focusedIndex); drives the live preview.
	focusProp := pm.CreateEx(model, "focusIdx", nil, false, false)

	chEv := tvChannelNodes(bs, pm, nodes, chans)
	if g := bs.tvGuideGet(chans, func(g *epg.Guide) {
		tvFillChannelEvents(pm, chEv, chans, g)
	}); g != nil {
		tvFillChannelEvents(pm, chEv, chans, g)
	}

	pip := func() {
		l, t, r, b := tvPiPRect(pm)
		arch.AndroidTvSetVideoRect(l, t, r, b)
	}
	pip()
	if !arch.AndroidTvIsTuned() {
		bs.tvTuneChannel(chans[0])
	}

	preview := -1
	focusProp.Subscribe(func(o any, et propcore.EventType,
		args ...any) {
		if et != propcore.EventSetFloat || len(args) == 0 {
			return
		}
		v, ok := args[0].(float32)
		if !ok {
			return
		}
		i := int(v + 0.5)
		if i < 0 || i >= len(chans) || i == preview {
			return
		}
		preview = i
		bs.tvTuneChannel(chans[i])
	}, nil, propcore.SubNoInitialUpdate)

	active := true
	var selSub *propcore.Subscription
	if parent := propRoot.GetParent(); parent != nil {
		selSub = parent.Subscribe(func(o any,
			et propcore.EventType, args ...any) {
			if et != propcore.EventSelectChild || len(args) == 0 {
				return
			}
			sel, _ := args[0].(*propcore.Prop)
			if sel == propRoot {
				active = true
				pip()
				if !arch.AndroidTvIsTuned() && preview >= 0 {
					bs.tvTuneChannel(chans[preview])
				}
			} else {
				active = false
				// tvplay keeps the session for the fullscreen
				// hand-off; any other destination stops it.
				if tvPageModelType(pm, sel) != "tvplay" {
					bs.tvRelease()
				}
			}
		}, nil, propcore.SubNoInitialUpdate)
	}

	propRoot.Subscribe(func(o any, et propcore.EventType, args ...any) {
		if et == propcore.EventDestroyed {
			if selSub != nil {
				selSub.Unsubscribe()
			}
			if active {
				bs.tvRelease()
			}
		}
	}, nil, propcore.SubNoInitialUpdate|propcore.SubFlagTrackDestroy)
	return nil
}

// chanEvProps — the metadata.events triple on each channel node;
// the play page fills these so the EPG panel can follow the list
// focus without a round-trip.
type chanEvProps struct {
	list, cur, next *propcore.Prop
}

// tvChannelNodes — one browsable item per channel, shared by the
// channel-list page and the play page OSD cloner. Returns the
// per-channel metadata.events props so the play page can fill them.
func tvChannelNodes(bs *BackendSystem, pm *propcore.PropManager, nodes *propcore.Prop,
	chans []tvChannel) []chanEvProps {
	ev := make([]chanEvProps, len(chans))
	for i, ch := range chans {
		item := pm.CreateEx(nil, "", nil, false, false)
		pm.SetStringEx(pm.CreateEx(item, "type", nil, false, false),
			nil, "video", propcore.StringUTF8)
		// zapIdx — list position, used by the play-page OSD to retune
		// in place via eventSink ("tune:<idx>") instead of navOpen.
		pm.SetIntEx(pm.CreateEx(item, "zapIdx", nil, false, false),
			nil, i)
		pm.SetStringEx(pm.CreateEx(item, "url", nil, false, false),
			nil, bs.tvPlayURL(ch), propcore.StringUTF8)
		im := pm.CreateEx(item, "metadata", nil, false, false)
		num := ch.Number
		if num == "" {
			num = strconv.Itoa(i + 1)
		}
		title := num + ". " + ch.Name
		pm.SetStringEx(pm.CreateEx(im, "title", nil, false, false),
			nil, title, propcore.StringUTF8)
		pm.SetStringEx(pm.CreateEx(im, "icon", nil, false, false),
			nil, "skin://icons/ic_tv_48px.svg", propcore.StringUTF8)
		e := pm.CreateEx(im, "events", nil, false, false)
		ev[i].list = pm.CreateEx(e, "list", nil, false, false)
		ev[i].cur = pm.CreateEx(e, "current", nil, false, false)
		ev[i].next = pm.CreateEx(e, "next", nil, false, false)
		pm.SetParentEx(item, nodes, nil, "")
	}
	return ev
}

// tvPlayURL — canonical play URL for a channel. Input id goes in
// unescaped: tvPlayPage matches it as a literal prefix, and a decoded
// '%2F' inside it would be indistinguishable from the path separator.
func (bs *BackendSystem) tvPlayURL(ch tvChannel) string {
	// Media URIs (file:///mnt/dtvfs/…, http://…) play through
	// movian's own pipeline: wrap in video: so the video backend
	// opens them directly — a raw file:// URL would just hit the
	// file browser (the dvb node has no extension to sniff).
	// Only when the channel lacks a real provider rowid: a positive
	// id always wins the TvView tune path (proven on vendor boxes).
	if ch.ID <= 0 && bs.tvIsFeederURI(ch.URI) {
		return "video:" + ch.URI
	}
	tail := fmt.Sprintf("%d", ch.ID)
	if ch.URI != "" {
		tail = url.PathEscape(ch.URI)
	}
	return "tvinput:play/" + ch.InputID + "/" + tail
}

// tvPlayPage — tunes the TvView and builds the minimal channel page.
// The TvView surface renders under the translucent GLW layer; the page
// exists so BACK has somewhere to return to (and carries the channel
// title OSD). Untune happens when the page is destroyed.
func (bs *BackendSystem) tvPlayPage(propRoot *propcore.Prop, spec string) error {
	pm := bs.propManager

	// spec = <inputId>/<channelId|rawURI>. Both sides can contain
	// '/' (input ids look like "pkg/.Class", raw feeder URIs like
	// /mnt/dtvfs/live/dvb:<triplet>) and the nav layer may or may not
	// have path-unescaped once — so don't split on slashes at all:
	// match the literal input id as prefix from the known input list.
	inputID, tail := "", ""
	for _, in := range bs.tvLoadInputs() {
		if strings.HasPrefix(spec, in.ID+"/") {
			inputID, tail = in.ID, spec[len(in.ID)+1:]
			break
		}
	}
	if inputID == "" {
		return fmt.Errorf("tvinput: unknown input in play URL: %s", spec)
	}
	idPart, err := url.PathUnescape(tail)
	if err != nil {
		return err
	}
	// DVB-triplet form ("<onid>.<tsid>.<sid>" hex) — vendor inputs accept
	// dvb:// feeder URIs directly, bypassing TvProvider rowids. Other
	// non-numeric forms (e.g. /mnt/dtvfs/live/dvb:<triplet>, scheme:...)
	// pass through as raw URIs.
	rawURI := ""
	var chanID int64
	if matched, _ := regexp.MatchString(`^[0-9a-fA-F]+\.[0-9a-fA-F]+\.[0-9a-fA-F]+$`, idPart); matched {
		rawURI = "dvb://" + idPart
	} else if strings.ContainsAny(idPart, "/:") {
		rawURI = idPart
	} else if _, err := fmt.Sscanf(idPart, "%d", &chanID); err != nil {
		return err
	}

	// Channel title + current index for the OSD header and zapping.
	chans := bs.tvLoadChannels()
	cur := -1
	for i, ch := range chans {
		if (chanID != 0 && ch.ID == chanID) ||
			(rawURI != "" && ch.URI == rawURI) {
			cur = i
			break
		}
	}
	title := ""
	if cur >= 0 {
		title = chans[cur].Name
	}

	model := pm.CreateEx(propRoot, "model", nil, false, false)
	pm.SetStringEx(pm.CreateEx(model, "type", nil, false, false),
		nil, "tvplay", propcore.StringUTF8)
	meta := pm.CreateEx(model, "metadata", nil, false, false)
	titleProp := pm.CreateEx(meta, "title", nil, false, false)
	pm.SetStringEx(titleProp, nil, title, propcore.StringUTF8)
	pm.SetIntEx(pm.CreateEx(model, "loading", nil, false, false),
		nil, 0)

	// EPG — canonical Movian events model (same shape the HTSP backend
	// publishes: list of event nodes + current/next links). Filled
	// from TvContract.Programs where readable, else an XMLTV guide.
	eventsProp := pm.CreateEx(meta, "events", nil, false, false)
	eventsList := pm.CreateEx(eventsProp, "list", nil, false, false)
	eventsCur := pm.CreateEx(eventsProp, "current", nil, false, false)
	eventsNext := pm.CreateEx(eventsProp, "next", nil, false, false)

	// Media OSD state: paused flag, timeshift position/window (seconds —
	// the seekbar binds tsPos with tsBuf as its max), the zap index for
	// the current-channel check mark, and the audio/subtitle track node
	// lists the OSD sidebar clones from.
	pausedProp := pm.CreateEx(model, "paused", nil, false, false)
	pm.SetIntEx(pausedProp, nil, 0)
	tsPosProp := pm.CreateEx(model, "tsPos", nil, false, false)
	tsBehindProp := pm.CreateEx(model, "tsBehind", nil, false, false)
	tsBufProp := pm.CreateEx(model, "tsBuf", nil, false, false)
	tsTextProp := pm.CreateEx(model, "tsText", nil, false, false)
	pm.SetStringEx(tsTextProp, nil, "LIVE", propcore.StringUTF8)
	curIdxProp := pm.CreateEx(model, "curIdx", nil, false, false)
	pm.SetIntEx(curIdxProp, nil, cur)
	audioTracksProp := pm.CreateEx(model, "audioTracks", nil, false, false)
	subTracksProp := pm.CreateEx(model, "subTracks", nil, false, false)

	// Media info — TvView bypasses Movian's media pipeline, so
	// global.media.current stays void and the mediainfo window shows
	// "No media loaded". Fill it with what the session does report:
	// play URL + channel title as Source, video resolution/encoding/
	// framerate and audio encoding/language from TvTrackInfo, plus
	// io.infoNodes rows (input, channel, timeshift). Cleared on page
	// destroy so normal playback re-owns the node.
	miURL, miFormat, miVCodec, miFPS, miACodec, miInfo :=
		(*propcore.Prop)(nil), (*propcore.Prop)(nil),
		(*propcore.Prop)(nil), (*propcore.Prop)(nil),
		(*propcore.Prop)(nil), (*propcore.Prop)(nil)
	if global := pm.GetGlobal(); global != nil {
		if media := pm.Find(global, "media"); media != nil {
			if curP := pm.Find(media, "current"); curP != nil {
				miURL = pm.CreateEx(curP, "url", nil, false, false)
				im := pm.CreateEx(curP, "metadata", nil, false, false)
				miFormat = pm.CreateEx(im, "format", nil, false, false)
				iv := pm.CreateEx(curP, "video", nil, false, false)
				miVCodec = pm.CreateEx(iv, "codec", nil, false, false)
				miFPS = pm.CreateEx(curP, "fps", nil, false, false)
				ia := pm.CreateEx(curP, "audio", nil, false, false)
				miACodec = pm.CreateEx(ia, "codec", nil, false, false)
				io := pm.CreateEx(curP, "io", nil, false, false)
				miInfo = pm.CreateEx(io, "infoNodes", nil, false, false)
				// TvView reports none of these — void any stale
				// values left by a previous file playback so
				// mediainfo shows only what the session offers.
				ivoid := func(p *propcore.Prop, name string) {
					pm.SetVoidEx(pm.CreateEx(p, name, nil,
						false, false), nil)
				}
				ivoid(iv, "bitrate")
				ivoid(iv, "dqlen")
				ivoid(ia, "bitrate")
				ivoid(ia, "dqlen")
				ivoid(curP, "avdiff")
				ivoid(curP, "avdiffError")
				if buf := pm.CreateEx(curP, "buffer", nil, false,
					false); buf != nil {
					ivoid(buf, "current")
					ivoid(buf, "limit")
					ivoid(buf, "delay")
				}
			}
		}
	}
	miClear := func() {
		for _, q := range []*propcore.Prop{miURL, miFormat, miVCodec,
			miFPS, miACodec} {
			if q != nil {
				pm.SetVoidEx(q, nil)
			}
		}
		if miInfo != nil {
			pm.DestroyChilds(miInfo)
		}
	}
	miAddInfo := func(title, info string) {
		if miInfo == nil {
			return
		}
		n := pm.CreateEx(nil, "", nil, false, false)
		pm.SetStringEx(pm.CreateEx(n, "title", nil, false, false),
			nil, title, propcore.StringUTF8)
		pm.SetStringEx(pm.CreateEx(n, "info", nil, false, false),
			nil, info, propcore.StringUTF8)
		pm.SetParentEx(n, miInfo, nil, "")
	}

	miSetChannel := func(ch tvChannel) {
		pm.SetStringEx(miURL, nil, bs.tvPlayURL(ch),
			propcore.StringUTF8)
		pm.SetStringEx(miFormat, nil, "Live TV",
			propcore.StringUTF8)
		pm.DestroyChilds(miInfo)
		miAddInfo("Channel", ch.Name)
		miAddInfo("Input", ch.InputID)
	}

	// Channel list for the OSD cloner (zap by pick).
	chEv := tvChannelNodes(bs, pm, pm.CreateEx(model, "channels", nil,
		false, false), chans)

	bs.traceSystem.Trace(trace.TRACE_DEBUG, "TV",
		"tune input=%s channel=%d uri=%q (%s)", inputID, chanID, rawURI, title)
	arch.AndroidTvSetVideoRect(0, 0, 0, 0) // PiP -> fullscreen
	tvCh := tvChannel{InputID: inputID, URI: rawURI, ID: chanID,
		Name: title}
	tvTunedKey = "" // force the tune — key dedup is for zaps
	bs.tvTuneChannel(tvCh)
	if miURL != nil && cur >= 0 {
		miSetChannel(chans[cur])
	}

	// ---- timeshift + tracks ----
	var (
		paused         bool
		tsStart, tsCur int64 // wall-clock ms
		tsPosMs        int64 // atomic: last polled position in buffer
		tracks         []tvTrack
		lastTracksRaw  string
	)

	// rebuildTrackNodes — refresh the track lists the OSD sidebar
	// clones from. Each node carries trackId/title/sel; subtitles get
	// an extra leading "Off" row (empty id deselects on the session).
	rebuildTrackNodes := func() {
		pm.DestroyChilds(audioTracksProp)
		pm.DestroyChilds(subTracksProp)
		subOn := false
		for _, t := range tracks {
			if t.Type == tvTrackTypeSubtitle && t.Selected {
				subOn = true
			}
		}
		add := func(parent *propcore.Prop, id, title string, sel bool) {
			n := pm.CreateEx(nil, "", nil, false, false)
			pm.SetStringEx(pm.CreateEx(n, "trackId", nil, false, false),
				nil, id, propcore.StringUTF8)
			pm.SetStringEx(pm.CreateEx(n, "title", nil, false, false),
				nil, title, propcore.StringUTF8)
			s := 0
			if sel {
				s = 1
			}
			pm.SetIntEx(pm.CreateEx(n, "sel", nil, false, false), nil, s)
			pm.SetParentEx(n, parent, nil, "")
		}
		add(subTracksProp, "", "Off", !subOn)
		for _, t := range tracks {
			switch t.Type {
			case tvTrackTypeAudio:
				add(audioTracksProp, t.ID, tvTrackTitle(t), t.Selected)
			case tvTrackTypeSubtitle:
				add(subTracksProp, t.ID, tvTrackTitle(t), t.Selected)
			}
		}
	}

	// refreshTracks — re-read the session track list; rebuilds the
	// sidebar nodes only when the JSON actually changed.
	refreshTracks := func() {
		raw := arch.AndroidTvTracks()
		if raw == "" || raw == lastTracksRaw {
			return
		}
		lastTracksRaw = raw
		var parsed []tvTrack
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			return
		}
		tracks = parsed
		rebuildTrackNodes()

		// Stream stats for the mediainfo window — video size/encoding/
		// framerate and the selected audio encoding + language.
		var v *tvTrack
		var au *tvTrack
		for i := range tracks {
			t := &tracks[i]
			if t.Type == tvTrackTypeVideo && (v == nil || t.Selected) {
				v = t
			}
			if t.Type == tvTrackTypeAudio && (au == nil || t.Selected) {
				au = t
			}
		}
		if v != nil {
			codec := v.Enc
			if codec == "" {
				codec = "video"
			}
			if v.W > 0 && v.H > 0 {
				codec = fmt.Sprintf("%s %dx%d", codec, v.W, v.H)
			}
			pm.SetStringEx(miVCodec, nil, codec, propcore.StringUTF8)
			if v.FPS > 0 {
				pm.SetFloatEx(miFPS, nil, float32(v.FPS))
			}
		}
		if au != nil {
			codec := strings.TrimSpace(au.Enc + " " + au.Lang)
			if codec == "" {
				codec = "audio"
			}
			if au.Ch > 0 {
				codec = fmt.Sprintf("%s %dch", codec, au.Ch)
			}
			pm.SetStringEx(miACodec, nil, codec, propcore.StringUTF8)
		}
	}

	// selectTrack — user pick from the sidebar ("" deselects, used by
	// the subtitle "Off" row).
	selectTrack := func(typ int, id string) {
		arch.AndroidTvSelectTrack(typ, id)
		for i := range tracks {
			if tracks[i].Type == typ {
				tracks[i].Selected = tracks[i].ID == id
			}
		}
		rebuildTrackNodes()
	}

	tsPoll := func() {
		s := arch.AndroidTvTimeshift()
		if s == "" {
			return
		}
		var st, cu int64
		if n, _ := fmt.Sscanf(s, "%d;%d", &st, &cu); n != 2 {
			return
		}
		atomic.StoreInt64(&tsStart, st)
		atomic.StoreInt64(&tsCur, cu)
		now := time.Now().UnixMilli()
		behind := now - cu
		if behind < 0 {
			behind = 0
		}
		buf := cu - st
		if buf < 0 {
			buf = 0
		}
		win := now - st
		if win < buf {
			win = buf
		}
		atomic.StoreInt64(&tsPosMs, buf)
		pm.SetFloatEx(tsPosProp, nil, float32(buf)/1000)
		pm.SetFloatEx(tsBehindProp, nil, float32(behind)/1000)
		pm.SetFloatEx(tsBufProp, nil, float32(win)/1000)
		if behind < 3000 {
			pm.SetStringEx(tsTextProp, nil,
				fmt.Sprintf("LIVE · buffer %d:%02d",
					buf/60000, buf/1000%60), propcore.StringUTF8)
		} else {
			pm.SetStringEx(tsTextProp, nil,
				fmt.Sprintf("-%d:%02d · buffer %d:%02d",
					behind/60000, behind/1000%60,
					buf/60000, buf/1000%60), propcore.StringUTF8)
		}
	}

	// Zap: the view delivers "Channel+"/"Channel-" (remote CH buttons or
	// left/right) and "tune:<listIndex>" (OSD pick). Retune in place —
	// no page churn, TvView keeps its session, banner just updates.
	var trackTicks int32
	var updateEPG func(ch tvChannel)
	var updateChannelEPGs func()
	tuneIdx := func(i int) {
		if i < 0 || i >= len(chans) {
			return
		}
		cur = i
		ch := chans[cur]
		bs.traceSystem.Trace(trace.TRACE_DEBUG, "TV",
			"zap -> %s (%s)", ch.Name, ch.URI)
		bs.tvTuneChannel(ch)
		pm.SetStringEx(titleProp, nil, ch.Name, propcore.StringUTF8)
		pm.SetIntEx(curIdxProp, nil, cur)
		if miURL != nil {
			miSetChannel(ch)
		}
		// Tracks change with the service — re-arm the refresh window;
		// the session reports them asynchronously.
		atomic.StoreInt32(&trackTicks, 0)
		paused = false
		pm.SetIntEx(pausedProp, nil, 0)
		updateEPG(ch)
	}

	// EPG: TvContract.Programs when the provider is readable (rare —
	// needs privileged perms for other inputs' data), else the shared
	// XMLTV guide (tvGuideGet caches it across page opens).
	onGuide := func(g *epg.Guide) {
		if cur >= 0 {
			updateEPG(chans[cur])
		}
		updateChannelEPGs()
	}
	bs.tvGuideGet(chans, onGuide)
	updateEPG = func(ch tvChannel) {
		now := time.Now()
		var evs []epg.Event
		if ch.ID != 0 {
			if raw := arch.AndroidTvPrograms(ch.ID); raw != "" &&
				raw != "[]" {
				var rows []struct {
					Title       string `json:"title"`
					Description string `json:"desc"`
					Start       int64  `json:"start"`
					Stop        int64  `json:"stop"`
				}
				if json.Unmarshal([]byte(raw), &rows) == nil {
					ms, day := now.UnixMilli(),
						now.Add(24*time.Hour).UnixMilli()
					for i := range rows {
						if rows[i].Stop <= ms || rows[i].Start > day {
							continue
						}
						evs = append(evs, epg.Event{
							Title: rows[i].Title,
							Desc:  rows[i].Description,
							Start: rows[i].Start / 1000,
							Stop:  rows[i].Stop / 1000,
						})
					}
				}
			}
		}
		if len(evs) == 0 {
			if g := bs.tvGuideGet(chans, onGuide); g != nil {
				evs = g.EventsFor(ch.Name, now,
					now.Add(24*time.Hour))
			}
		}
		tvSetEvents(pm, eventsList, eventsCur, eventsNext, evs, now)
	}
	// updateChannelEPGs — per-channel rolling schedules (now → +24h)
	// for the list-focus EPG panel (guide-only; TvProvider programs
	// only cover the channel the session is tuned to anyway).
	updateChannelEPGs = func() {
		tvFillChannelEvents(pm, chEv, chans,
			bs.tvGuideGet(chans, onGuide))
	}
	if cur >= 0 {
		updateEPG(chans[cur])
	}
	// Fill the per-channel lists right away — when the guide is
	// already cached (e.g. fetched by the channel list page) onGuide
	// never fires and the OSD EPG panel would stay empty for a minute.
	updateChannelEPGs()

	// Poll timeshift positions once a second; refresh the track list a
	// few times after each tune (the session reports tracks async) and
	// the now/next events every minute.
	done := make(chan struct{})
	var epgTicks int32
	go func() {
		for {
			select {
			case <-done:
				return
			case <-time.After(time.Second):
				tsPoll()
				if atomic.AddInt32(&trackTicks, 1) <= 15 {
					refreshTracks()
				}
				if atomic.AddInt32(&epgTicks, 1) >= 60 {
					atomic.StoreInt32(&epgTicks, 0)
					if cur >= 0 {
						updateEPG(chans[cur])
					}
					updateChannelEPGs()
				}
			}
		}
	}()

	// Seekbar drag: the view binds the slider to model.tsPos, so user
	// seeks land here as float sets. The poll writes the same prop —
	// ignore sets matching the last polled position to avoid loops.
	tsPosProp.Subscribe(func(o any, et propcore.EventType, args ...any) {
		if et != propcore.EventSetFloat || len(args) == 0 {
			return
		}
		v, ok := args[0].(float32)
		if !ok {
			return
		}
		ms := int64(v * 1000)
		diff := ms - atomic.LoadInt64(&tsPosMs)
		if diff < 0 {
			diff = -diff
		}
		start := atomic.LoadInt64(&tsStart)
		if diff <= 1500 || start <= 0 {
			return
		}
		arch.AndroidTvSeekTo(start + ms)
	}, nil, propcore.SubNoInitialUpdate)

	es := pm.CreateEx(model, "eventSink", nil, false, false)
	es.Subscribe(func(o any, et propcore.EventType, args ...any) {
		if et != propcore.EventExtEvent || len(args) == 0 {
			return
		}
		e := event.ExtArg(args[0])
		if e == nil {
			return
		}
		switch {
		case e.IsAction(event.ACTION_NEXT_CHANNEL):
			if len(chans) > 0 {
				tuneIdx((cur + 1) % len(chans))
			}
		case e.IsAction(event.ACTION_PREV_CHANNEL):
			if len(chans) > 0 {
				tuneIdx((cur - 1 + len(chans)) % len(chans))
			}
		case e.IsAction(event.ACTION_PLAYPAUSE):
			paused = !paused
			v := 0
			if paused {
				v = 1
			}
			pm.SetIntEx(pausedProp, nil, v)
			arch.AndroidTvPause(paused)
		case e.IsAction(event.ACTION_SEEK_BACKWARD):
			tsPoll()
			start, cur := atomic.LoadInt64(&tsStart),
				atomic.LoadInt64(&tsCur)
			if cur-start > 5000 {
				t := cur - 30000
				if t < start {
					t = start
				}
				arch.AndroidTvSeekTo(t)
			}
		case e.IsAction(event.ACTION_SEEK_FORWARD):
			tsPoll()
			t := atomic.LoadInt64(&tsCur) + 30000
			if now := time.Now().UnixMilli(); t > now {
				t = now
			}
			arch.AndroidTvSeekTo(t)
		case e.Type == event.EVENT_DYNAMIC_ACTION:
			if ep, ok := e.Concrete().(*event.EventPayload); ok {
				switch {
				case strings.HasPrefix(ep.Payload, "selaudio:"):
					selectTrack(tvTrackTypeAudio, ep.Payload[9:])
				case strings.HasPrefix(ep.Payload, "selsub:"):
					selectTrack(tvTrackTypeSubtitle, ep.Payload[7:])
				case strings.HasPrefix(ep.Payload, "tune:"):
					if idx, err := strconv.Atoi(
						ep.Payload[5:]); err == nil {
						tuneIdx(idx)
					}
				}
			}
		}
	}, nil, propcore.SubNoInitialUpdate)

	// BACK does NOT destroy the page — navBack only selects the
	// previous history entry, keeping propRoot alive for Forward.
	// So watch the pages container's selection: when the selected
	// child is no longer this page, release the TvView session;
	// when it comes back (Forward), retune the current channel.
	var selSub *propcore.Subscription
	stopped := false
	if parent := propRoot.GetParent(); parent != nil {
		selSub = parent.Subscribe(func(o any,
			et propcore.EventType, args ...any) {
			if et != propcore.EventSelectChild ||
				len(args) == 0 {
				return
			}
			sel, _ := args[0].(*propcore.Prop)
			if sel == propRoot {
				arch.AndroidTvSetVideoRect(0, 0, 0, 0)
				if stopped && cur >= 0 {
					stopped = false
					tuneIdx(cur)
				}
			} else if tvPageModelType(pm, sel) == "tvchannels" {
				// Back to the channel list: it takes over the
				// session as PiP — no untune, no flicker.
			} else if !stopped {
				stopped = true
				arch.AndroidTvUntune()
				tvTunedKey = ""
				miClear()
			}
		}, nil, propcore.SubNoInitialUpdate)
	}

	// Untune when the page is destroyed (nav close/reload).
	propRoot.Subscribe(func(o any, et propcore.EventType, args ...any) {
		if et == propcore.EventDestroyed {
			close(done)
			if selSub != nil {
				selSub.Unsubscribe()
			}
			bs.tvRelease()
			miClear()
		}
	}, nil, propcore.SubNoInitialUpdate|propcore.SubFlagTrackDestroy)

	return nil
}

// tvTrack — one TvTrackInfo row from Tv.tracks() (JSON).
type tvTrack struct {
	Type     int     `json:"type"` // TvTrackInfo: 0 video, 1 audio, 2 subtitle
	ID       string  `json:"id"`
	Lang     string  `json:"lang"`
	Desc     string  `json:"desc"`
	Selected bool    `json:"selected"`
	Enc      string  `json:"enc"`
	W        int     `json:"w"`
	H        int     `json:"h"`
	FPS      float64 `json:"fps"`
	Ch       int     `json:"ch"`
	SR       int     `json:"sr"`
}

const (
	tvTrackTypeVideo    = 0
	tvTrackTypeAudio    = 1
	tvTrackTypeSubtitle = 2
)

// tvLoadTracks — audio/subtitle tracks reported by the tuned session.
func tvLoadTracks() []tvTrack {
	raw := arch.AndroidTvTracks()
	if raw == "" {
		return nil
	}
	var tracks []tvTrack
	if err := json.Unmarshal([]byte(raw), &tracks); err != nil {
		return nil
	}
	return tracks
}

// tvTrackTitle — display label for one track in the OSD sidebar:
// language, then description, then raw id.
func tvTrackTitle(t tvTrack) string {
	for _, s := range []string{t.Lang, t.Desc, t.ID} {
		if s != "" {
			return s
		}
	}
	return "?"
}
