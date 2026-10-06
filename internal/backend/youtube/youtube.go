// youtube.go — the BackendSystem-facing half of the native YouTube
// backend: URL dispatch (youtube:browse|search|channel|video),
// directory-page rendering into page models, the global-search
// "YouTube" class, and the plugin shell (service entry + plugin list
// registration) that makes it look like a plugin in the UI.
package youtube

import (
	"strconv"
	"strings"
	"sync"
	"time"

	backendcore "github.com/czz/movian-go/internal/backend/core"
	"github.com/czz/movian-go/internal/db/kvstore"
	facore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/htsmsg"
	"github.com/czz/movian-go/internal/plugins"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/service"
	settings "github.com/czz/movian-go/internal/settings"
	"github.com/czz/movian-go/internal/trace"
)

// System is the YouTube backend context — owns every mutable state
// (OAuth session, prefs, visitor token, JS caches) plus the injected
// deps. Created once by init; every entry point is a method so the
// package carries no mutable globals.
type System struct {
	// Injected deps (set at construction/Start).
	ts      *trace.TraceSystem
	beSys   *backendcore.BackendSystem
	propMgr *propcore.PropManager
	kvs     *kvstore.KVStore

	// fam — the app fileaccess manager. Every HTTP request (Innertube,
	// OAuth, manifests, player scripts) goes through the app's own
	// stack (fa_http) so cookies, request inspectors and the proxy
	// config apply — and so probe results match what the demuxer gets.
	fam *facore.FileAccessManager

	// oauth — sign-in state machine (oauth.go).
	oauth struct {
		sync.Mutex
		refreshTok string
		accessTok  string
		expiresAt  time.Time
		// Device flow in progress: shown to the user in settings + log.
		userCode  string
		verifyURL string
		signingIn bool
		lastErr   string
	}

	// Preference state (settings_yt.go) — written by setting
	// callbacks / WriteInt, read at request/pick time.
	// prefMaxHeight 0 = auto.
	prefMu        sync.RWMutex
	prefMaxHeight int
	prefSearchOn  int // contributes to Movian's global search
	prefHL        string
	prefGL        string
	statusProp    *propcore.Prop

	// visitorData — YouTube session token replayed as
	// X-Goog-Visitor-Id (innertube.go).
	visitorDataMu sync.RWMutex
	visitorData   string

	// jsCache — base.js player-script cache (decipher.go).
	jsCacheMu sync.Mutex
	jsCache   map[string]string

	// embedJS — videoID → [jsURL, sts] cache (decipher.go).
	embedJSMu   sync.Mutex
	embedJSOnce map[string][2]string
}

// NewSystem creates the YouTube backend context. ts is nil-safe
// (tests pass nil).
func NewSystem(ts *trace.TraceSystem) *System {
	return &System{
		ts:           ts,
		prefSearchOn: 1,
		jsCache:      map[string]string{},
		embedJSOnce:  map[string][2]string{},
	}
}

// Start registers the youtube: backend, creates the home-screen
// service entry, lists the plugin in the plugin manager so it shows
// up under Settings → Plugins like any installed plugin, and adds
// the "YouTube" group to the settings tree (canonical kvstore
// persistence, same mechanism JS plugins use via settings.js).
func (s *System) Start(bs *backendcore.BackendSystem, ss *service.ServiceSystem,
	plugmgr *plugins.PluginManager, sm *settings.SettingsManager,
	kvs *kvstore.KVStore) {
	s.kvs = kvs
	s.beSys = bs
	s.fam = bs.FileAccessManager()
	s.propMgr = bs.GetPropManager()

	be := &backendcore.Backend{Prefix: "youtube:"}
	be.CanHandle = func(url string) int {
		if strings.HasPrefix(url, "youtube:") {
			return 1
		}
		return 0
	}
	be.Open = s.open
	be.Search = s.searchClass
	bs.Register(be)

	if ss != nil {
		ss.ServiceCreate("youtube", "YouTube", "youtube:browse", "other",
			"skin://icons/youtube.svg", false, true, service.SvcOriginApp)
	}
	if plugmgr != nil {
		pl := plugmgr.MakePlugin("youtube", "native")
		pl.Title = "YouTube"
		pl.InstVer = "1.0"
		pl.Loaded = true
		pl.Installed = true
		plugmgr.UpdateState(pl)
	}
	s.setupSettings(sm)
	// Pre-warm an existing OAuth session (stored refresh token →
	// fresh access token) so the first /player call is already
	// authenticated. Synchronous kvstore read — the pool is already
	// initialized at this point in the init sequence; the network
	// refresh it spawns stays async.
	s.oauthLoadRefresh()
}

// ── URL dispatch ────────────────────────────────────────────────────

// open — be_open for youtube: URLs. Errors are reported into the
// page model (openerror) — the return value is ignored in phase 3.
func (s *System) open(page any, url string, sync_ bool) error {
	propRoot, ok := page.(*propcore.Prop)
	if !ok || propRoot == nil {
		return nil
	}
	rest := strings.TrimPrefix(url, "youtube:")
	cmd, arg, _ := strings.Cut(rest, ":")
	switch cmd {
	case "browse":
		s.renderSectionsPage(propRoot, "YouTube", s.fetchHomeSections, true)
	case "tab":
		t := tabByKey(arg)
		s.renderSectionsPage(propRoot, "YouTube — "+t.label,
			s.fetchTab(t), true)
	case "search":
		q, _ := strings.CutPrefix(arg, "")
		s.renderDirPage(propRoot, "YouTube — "+q,
			func() ([]videoItem, error) { return s.fetchSearch(q) })
	case "channel":
		s.renderDirPage(propRoot, "YouTube — Channel",
			func() ([]videoItem, error) {
				// params = the channel "Videos" tab token.
				return s.fetchBrowse(arg, "EgZ2aWRlb3M%3D")
			})
	case "video":
		id, qstr, _ := strings.Cut(arg, "?")
		maxH := -1 // -1 = the video-quality pref
		if v, ok := strings.CutPrefix(qstr, "h="); ok {
			if n, err := strconv.Atoi(v); err == nil {
				maxH = n
			}
		}
		s.resolveVideo(propRoot, id, maxH)
	default:
		s.renderSectionsPage(propRoot, "YouTube", s.fetchHomeSections, true)
	}
	return nil
}

// ── Fetchers ────────────────────────────────────────────────────────

func (s *System) fetchBrowse(browseID, params string) ([]videoItem, error) {
	var lastErr error
	for _, cl := range s.browseChain() {
		resp, err := s.browse(cl, browseID, params, "")
		if err != nil {
			s.ts.Debug("youtube", "browse %s %s: %v", cl.name, browseID, err)
			lastErr = err
			continue
		}
		var items []videoItem
		walkRenderers(resp, &items)
		return items, nil
	}
	return nil, lastErr
}

// fetchHomeSections — the app-style home feed: shelfRenderer sections
// from FEwhat_to_watch. Signed-in feeds can change shape (richGrid
// instead of shelves, or a browse id rejected for the authed client),
// so every client × browse-id pair is tried in order; the first
// non-empty result wins.
func (s *System) fetchHomeSections() ([]shelf, error) {
	for _, cl := range s.browseChain() {
		for _, bid := range []string{"FEwhat_to_watch", "FEtrending"} {
			resp, err := s.browse(cl, bid, "", "")
			if err != nil {
				s.ts.Debug("youtube", "home %s %s: %v", cl.name, bid, err)
				continue
			}
			sh := sectionsFrom(resp)
			if len(sh) == 0 {
				s.ts.Debug("youtube", "home %s %s: no content", cl.name, bid)
				continue
			}
			// The initial feed only carries a couple of shelves; follow
			// continuations like the app does on scroll so the home page
			// is not just 1-2 rows.
			seen := map[string]bool{}
			for i := 0; i < homeContinuationPages; i++ {
				tok := continuationToken(resp)
				if tok == "" || seen[tok] {
					break
				}
				seen[tok] = true
				resp, err = s.browse(cl, bid, "", tok)
				if err != nil || resp == nil {
					s.ts.Debug("youtube", "home %s %s continuation: %v", cl.name, bid, err)
					break
				}
				sh = mergeShelves(sh, sectionsFrom(resp))
			}
			nonEmpty := sh[:0]
			for _, sec := range sh {
				if len(sec.Items) > 0 {
					nonEmpty = append(nonEmpty, sec)
				}
			}
			return nonEmpty, nil
		}
	}
	return nil, errString("empty home feed on all clients")
}

// homeContinuationPages caps how many continuation pages the home
// feed fetches up front — the feed is endless; this just gives the
// page a reasonable initial depth.
const homeContinuationPages = 3

// mergeShelves appends b's sections onto a, merging items into an
// existing shelf when the title repeats (continuation pages reuse
// titles like "Recommended").
func mergeShelves(a, b []shelf) []shelf {
	for _, nb := range b {
		merged := false
		for i := range a {
			if a[i].Title == nb.Title {
				a[i].Items = append(a[i].Items, nb.Items...)
				merged = true
				break
			}
		}
		if !merged {
			a = append(a, nb)
		}
	}
	return a
}

// sectionsFrom distills a browse response into shelf sections.
// shelfRenderer keeps its title+row; videos outside any shelf are
// prepended as a generic "Home" section (or stand alone for flat
// feeds like richGrid).
func sectionsFrom(resp jmap) []shelf {
	var shelves []shelf
	var loose []videoItem
	walkShelves(resp, &shelves, &loose)
	if len(loose) == 0 {
		return shelves
	}
	return append([]shelf{{Title: "Home", Items: loose}}, shelves...)
}

func (s *System) fetchSearch(query string) ([]videoItem, error) {
	var lastErr error
	for _, cl := range s.browseChain() {
		resp, err := s.search(cl, query, "", "")
		if err != nil {
			s.ts.Debug("youtube", "search %s: %v", cl.name, err)
			lastErr = err
			continue
		}
		var items []videoItem
		walkRenderers(resp, &items)
		return items, nil
	}
	return nil, lastErr
}

// ── Top-bar tabs (APK-style category strip) ─────────────────────────

// homeTab is one chip in the page-top category strip. browseID is the
// Innertube feed for the tab (only a handful are accepted, all
// auth-gated feeds verified on TVHTML5); query is the search
// fallback; authOnly hides the tab for guests.
type homeTab struct {
	key      string
	label    string
	browseID string
	query    string
	authOnly bool
}

var homeTabs = []homeTab{
	{"home", "Home", "FEwhat_to_watch", "", false},
	{"subscriptions", "Subscriptions", "FEsubscriptions", "", true},
	{"history", "History", "FEhistory", "", true},
	{"music", "Music", "", "music", false},
	{"sport", "Sport", "", "sport", false},
	{"live", "Live", "", "live stream", false},
	{"gaming", "Gaming", "", "gaming", false},
	{"news", "News", "", "news", false},
	{"movies", "Movies", "FEstorefront", "movies", false},
	{"podcasts", "Podcasts", "", "podcasts", false},
}

func tabByKey(key string) homeTab {
	for _, t := range homeTabs {
		if t.key == key {
			return t
		}
	}
	return homeTab{key: key, label: key, query: key}
}

// visibleTabs returns the tab set for the current session —
// auth-gated feeds (subscriptions/history) are hidden for guests.
func (s *System) visibleTabs() []homeTab {
	out := homeTabs[:0:0]
	for _, t := range homeTabs {
		if t.authOnly && !s.signedIn() {
			continue
		}
		out = append(out, t)
	}
	return out
}

func (t homeTab) url() string {
	if t.key == "home" {
		return "youtube:browse"
	}
	return "youtube:tab:" + t.key
}

// fetchTab resolves one top-bar tab: its Innertube feed first (any
// client that answers), the search fallback after that.
func (s *System) fetchTab(t homeTab) func() ([]shelf, error) {
	return func() ([]shelf, error) {
		if t.key == "home" {
			return s.fetchHomeSections()
		}
		if t.browseID != "" {
			for _, cl := range s.browseChain() {
				resp, err := s.browse(cl, t.browseID, "", "")
				if err != nil {
					s.ts.Debug("youtube", "tab %s %s: %v",
						t.key, cl.name, err)
					continue
				}
				if sh := sectionsFrom(resp); len(sh) > 0 {
					return sh, nil
				}
			}
		}
		if t.query == "" {
			return nil, errString("tab feed unavailable")
		}
		items, err := s.fetchSearch(t.query)
		if err != nil {
			return nil, err
		}
		return []shelf{{Title: t.label, Items: items}}, nil
	}
}

// ── Page rendering ──────────────────────────────────────────────────

// renderDirPage builds a directory model and fills it asynchronously
// (network on a worker goroutine, prop updates when done).
func (s *System) renderDirPage(page *propcore.Prop, title string, fetch func() ([]videoItem, error)) {
	model := s.propMgr.CreateEx(page, "model", nil, false, false)
	s.propMgr.CreateEx(model, "type", nil, false, false).SetString("directory")
	loading := s.propMgr.CreateEx(model, "loading", nil, false, false)
	loading.SetInt(1)
	meta := s.propMgr.CreateEx(model, "metadata", nil, false, false)
	s.propMgr.CreateEx(meta, "title", nil, false, false).SetString(title)

	go func() {
		items, err := fetch()
		if err != nil {
			s.propMgr.CreateEx(model, "type", nil, false, false).SetString("openerror")
			s.propMgr.CreateEx(model, "error", nil, false, false).SetString(err.Error())
			loading.SetInt(0)
			return
		}
		nodes := s.propMgr.CreateEx(model, "nodes", nil, false, false)
		for _, it := range items {
			s.addItemNode(nodes, it)
		}
		loading.SetInt(0)
	}()
}

// renderSectionsPage — the app-style home: model.contents=
// "searchresults" selects the sectioned view (label + horizontal row
// per shelf + a "more" tile that navOpens the section URL). Each
// section is a directory-typed node carrying its own nodes vector —
// the same shape pluginSearch's class dirs use. withTabs prepends a
// "tabbar" section — the APK-style category strip — rendered by
// youtube.view as a horizontal row of chips.
func (s *System) renderSectionsPage(page *propcore.Prop, title string,
	fetch func() ([]shelf, error), withTabs bool) {
	model := s.propMgr.CreateEx(page, "model", nil, false, false)
	s.propMgr.CreateEx(model, "type", nil, false, false).SetString("directory")
	s.propMgr.CreateEx(model, "contents", nil, false, false).SetString("searchresults")
	loading := s.propMgr.CreateEx(model, "loading", nil, false, false)
	loading.SetInt(1)
	meta := s.propMgr.CreateEx(model, "metadata", nil, false, false)
	s.propMgr.CreateEx(meta, "title", nil, false, false).SetString(title)
	// Force the dedicated app-style page view (shelf rows, 5 wide
	// poster tiles with channel line).
	s.propMgr.CreateEx(meta, "glwview", nil, false, false).SetString("youtube.view")

	// nodes is created eagerly so the tab strip renders before the
	// (async) feed fetch completes; CreateEx returns the same node
	// for the shelf appends below.
	nodes := s.propMgr.CreateEx(model, "nodes", nil, false, false)
	if withTabs {
		s.addTabBarNode(nodes)
	}

	go func() {
		shelves, err := fetch()
		if err != nil {
			s.propMgr.CreateEx(model, "type", nil, false, false).SetString("openerror")
			s.propMgr.CreateEx(model, "error", nil, false, false).SetString(err.Error())
			loading.SetInt(0)
			return
		}
		for _, sh := range shelves {
			sec := s.propMgr.CreateRootEx("", true)
			s.propMgr.CreateEx(sec, "type", nil, false, false).SetString("directory")
			// "more" tile → topical search for the shelf title.
			s.propMgr.CreateEx(sec, "url", nil, false, false).SetString(
				"youtube:search:" + sh.Title)
			sm := s.propMgr.CreateEx(sec, "metadata", nil, false, false)
			s.propMgr.CreateEx(sm, "title", nil, false, false).SetString(sh.Title)
			secNodes := s.propMgr.CreateEx(sec, "nodes", nil, false, false)
			for _, it := range sh.Items {
				s.addItemNode(secNodes, it)
			}
			s.propMgr.CreateEx(sec, "entries", nil, false, false).SetInt(len(sh.Items))
			if s.propMgr.SetParentEx(sec, nodes, nil, "") != 0 {
				s.propMgr.Destroy(sec)
			}
		}
		loading.SetInt(0)
	}()
}

// addTabBarNode appends the APK-style category strip as a "tabbar"
// section node — youtube.view renders its children as a horizontal
// row of chips instead of a poster shelf.
func (s *System) addTabBarNode(nodes *propcore.Prop) {
	bar := s.propMgr.CreateRootEx("", true)
	s.propMgr.CreateEx(bar, "type", nil, false, false).SetString("tabbar")
	bm := s.propMgr.CreateEx(bar, "metadata", nil, false, false)
	s.propMgr.CreateEx(bm, "title", nil, false, false).SetString("")
	bn := s.propMgr.CreateEx(bar, "nodes", nil, false, false)
	tabs := s.visibleTabs()
	for _, t := range tabs {
		c := s.propMgr.CreateRootEx("", true)
		s.propMgr.CreateEx(c, "url", nil, false, false).SetString(t.url())
		s.propMgr.CreateEx(c, "type", nil, false, false).SetString("menu")
		m := s.propMgr.CreateEx(c, "metadata", nil, false, false)
		s.propMgr.CreateEx(m, "title", nil, false, false).SetString(t.label)
		if s.propMgr.SetParentEx(c, bn, nil, "") != 0 {
			s.propMgr.Destroy(c)
		}
	}
	s.propMgr.CreateEx(bar, "entries", nil, false, false).SetInt(len(tabs))
	if s.propMgr.SetParentEx(bar, nodes, nil, "") != 0 {
		s.propMgr.Destroy(bar)
	}
}

// addItemNode appends one directory item node (url/type/metadata)
// under the nodes vector — the same shape JS page.appendItem produces.
func (s *System) addItemNode(nodes *propcore.Prop, it videoItem) {
	c := s.propMgr.CreateRootEx("", true)
	s.propMgr.CreateEx(c, "url", nil, false, false).SetString("youtube:video:" + it.ID)
	s.propMgr.CreateEx(c, "type", nil, false, false).SetString("video")
	m := s.propMgr.CreateEx(c, "metadata", nil, false, false)
	s.propMgr.CreateEx(m, "title", nil, false, false).SetString(it.Title)
	if it.Thumb != "" {
		s.propMgr.CreateEx(m, "icon", nil, false, false).SetString(it.Thumb)
	}
	if it.Duration != "" {
		s.propMgr.CreateEx(m, "duration", nil, false, false).SetString(it.Duration)
	}
	var desc []string
	if it.Channel != "" {
		desc = append(desc, it.Channel)
	}
	if it.Views != "" {
		desc = append(desc, it.Views)
	}
	if it.Published != "" {
		desc = append(desc, it.Published)
	}
	if len(desc) > 0 {
		s.propMgr.CreateEx(m, "description", nil, false, false).SetString(strings.Join(desc, " — "))
	}
	if it.Badge != "" {
		s.propMgr.CreateEx(m, "badge", nil, false, false).SetString(it.Badge)
	}
	if s.propMgr.SetParentEx(c, nodes, nil, "") != 0 {
		s.propMgr.Destroy(c)
	}
}

// ── Video resolution ────────────────────────────────────────────────

// resolveVideo runs the player exchange and re-opens the page as a
// videoparams: URL — the canonical video backend takes over from
// there (playqueue, OSD, metadata). maxH caps the resolution the same
// way pickStream's height cap does (-1 = the video-quality pref,
// 0 = uncapped); the OSD quality selector rides on it.
func (s *System) resolveVideo(page *propcore.Prop, videoID string, maxH int) {
	model := s.propMgr.CreateEx(page, "model", nil, false, false)
	s.propMgr.CreateEx(model, "type", nil, false, false).SetString("directory")
	loading := s.propMgr.CreateEx(model, "loading", nil, false, false)
	loading.SetInt(1)
	meta := s.propMgr.CreateEx(model, "metadata", nil, false, false)
	s.propMgr.CreateEx(meta, "title", nil, false, false).SetString("YouTube")

	go func() {
		streamURL, mime, title, heights, err := s.resolveStream(videoID, maxH)
		if err != nil || streamURL == "" {
			s.ts.Error("youtube", "resolve %s failed: %v (stream=%q)",
				videoID, err, streamURL)
			s.propMgr.CreateEx(model, "type", nil, false, false).SetString("openerror")
			msg := "no playable stream"
			if err != nil {
				msg = err.Error()
			}
			s.propMgr.CreateEx(model, "error", nil, false, false).SetString(msg)
			loading.SetInt(0)
			return
		}
		vp := htsmsg.NewMap()
		vp.AddStr("canonicalUrl", "https://www.youtube.com/watch?v="+videoID)
		vp.AddU32("no_fs_scan", 1)
		if title != "" {
			vp.AddStr("title", title)
		}
		src := htsmsg.NewMap()
		src.AddStr("url", streamURL)
		if mime != "" {
			src.AddStr("mimetype", mime)
		}
		sources := htsmsg.NewList()
		sources.AddMsg("", src)
		vp.AddMsg("sources", sources)

		if len(heights) > 0 {
			// Quality menu for the video-page OSD: each entry is a
			// youtube:video: URL carrying its own height cap, so
			// selecting one re-opens through this same path.
			ql := htsmsg.NewList()
			q0 := htsmsg.NewMap()
			q0.AddStr("label", "Auto")
			q0.AddStr("url", "youtube:video:"+videoID)
			ql.AddMsg("", q0)
			for _, h := range heights {
				qe := htsmsg.NewMap()
				qe.AddStr("label", strconv.Itoa(h)+"p")
				qe.AddStr("url", "youtube:video:"+videoID+"?h="+strconv.Itoa(h))
				ql.AddMsg("", qe)
			}
			vp.AddMsg("qualities", ql)
		}

		rstr, err := htsmsg.SerializeJSONToRstr(vp, "videoparams:")
		if err != nil {
			s.propMgr.CreateEx(model, "type", nil, false, false).SetString("openerror")
			s.propMgr.CreateEx(model, "error", nil, false, false).SetString(err.Error())
			loading.SetInt(0)
			return
		}
		s.ts.Debug("youtube", "%s heights=%v mime=%s", videoID, heights, mime)
		// Re-dispatch the page to the videoparams backend.
		if err := s.beSys.Open(page, rstr, false); err != nil {
			s.ts.Error("youtube", "videoparams open failed: %v", err)
		}
	}()
}

// resolveStream queries /player across clients and returns the best
// stream + title. A client answering OK but yielding nothing usable
// (SABR-only streamingData) does not stop the chain; a video-only pick
// (silent playback) is kept as a candidate but later clients still get
// a chance to supply a muxed or HLS stream.
func (s *System) resolveStream(videoID string, maxH int) (streamURL, mime, title string, heights []int, err error) {
	var bestURL, bestMime, bestTitle, bestKind, reason string
	var bestResp jmap
	for _, c := range s.clientChain() {
		resp, perr := s.player(c, videoID)
		if perr != nil {
			err = perr
			s.ts.Debug("youtube", "player %s %s: %v", c.name, videoID, perr)
			continue
		}
		st := resp.a("playabilityStatus").s("status")
		if st != "OK" {
			reason = resp.a("playabilityStatus").s("reason")
			s.ts.Debug("youtube", "player %s %s: status=%s reason=%s",
				c.name, videoID, st, reason)
			continue
		}
		u, m, kind := s.pickStream(resp, maxH)
		if u == "" {
			s.ts.Debug("youtube", "player %s %s: OK but no playable stream",
				c.name, videoID)
			continue
		}
		var hlsVars []hlsVariantInfo
		var hlsBody []byte
		if kind == "hls" {
			var ok bool
			if hlsVars, hlsBody, ok = s.probeHLS(u); !ok {
				// Manifest tree 403s on plain fetch (GVS-token bound
				// URLs — authed TVHTML5) — fall back to this client's
				// muxed/adaptive pick, then to the next client.
				s.ts.Debug("youtube",
					"player %s %s: hls manifest not fetchable",
					c.name, videoID)
				u, m, kind = s.pickStream0(resp, maxH, false)
				if u == "" {
					continue
				}
			}
		}
		vt := resp.a("videoDetails").s("title")
		if len(hlsVars) > 0 {
			// Master playlist — quality comes from the variants:
			// the OSD menu gets their playable (avc1) heights and
			// an explicit ?h= cap (or the max-quality pref) ships a
			// filtered master over mem:// so the canonical demuxer
			// keeps the EXT-X-MEDIA audio renditions — a bare
			// variant playlist would play muted.
			heights := hlsVariantHeights(hlsVars)
			capH := maxH
			if capH < 0 {
				capH = s.maxHeight()
			}
			if capH > 0 {
				if mb := hlsFilterMaster(hlsBody, capH); mb != nil {
					if id := s.fam.RegisterMemory(mb); id >= 0 {
						return "hls:mem://" + strconv.Itoa(id),
							m, vt, heights, nil
					}
				}
			}
			// The bare manifest URL must keep its hls: prefix:
			// without it the fileaccess HLS sniff only scans the
			// first 1023 bytes, and YouTube masters carry multi-KB
			// EXT-X-MEDIA lines that push the first STREAM-INF past
			// the window — the stream then lands in FFmpeg's hls
			// demuxer, which downloads every variant in parallel
			// and plays the first (lowest) one.
			return "hls:" + u, m, vt, heights, nil
		}
		switch kind {
		case "hls":
			// Manifest fetched but without RESOLUTION variants —
			// audio is inside, done.
			return "hls:" + u, m, vt, streamHeights(resp), nil
		case "muxed":
			// Muxed formats cap near 360p/720p and cannot honor the
			// rest of the quality menu — kept as fallback while a
			// later client (IOS) may still offer an HLS manifest
			// covering every height.
			if bestKind != "muxed" {
				bestURL, bestMime, bestTitle, bestKind = u, m, vt, kind
				bestResp = resp
			}
		default: // video-only, silent
			if bestURL == "" {
				bestURL, bestMime, bestTitle, bestKind = u, m, vt, kind
				bestResp = resp
			}
			s.ts.Debug("youtube", "player %s %s: video-only stream, trying next client",
				c.name, videoID)
		}
	}
	if bestURL != "" {
		return bestURL, bestMime, bestTitle, streamHeights(bestResp), nil
	}
	// A playabilityStatus reason is more truthful than the last
	// transport error (e.g. "Sign in to confirm you're not a bot"
	// beats a 429 on a later client).
	if reason != "" {
		return "", "", "", nil, errString(reason)
	}
	if err != nil {
		return "", "", "", nil, err
	}
	return "", "", "", nil, errString("unplayable on all clients")
}

type ytErr string

func (e ytErr) Error() string { return string(e) }
func errString(s string) error {
	return ytErr(s)
}

// ── Global search integration ───────────────────────────────────────

// searchClass — be_search: creates the "YouTube" class directory
// under source.nodes with video results, like C's plugin_search.
// Async like locatedbSearch — the HTTP fetch must not block the
// other backends' searchers; loading is cleared when done.
func (s *System) searchClass(model any, query string, loading any) {
	source, ok := model.(*propcore.Prop)
	if !ok || source == nil || !s.searchOn() {
		return
	}
	source.Retain()
	loadingProp, _ := loading.(*propcore.Prop)
	if loadingProp != nil {
		loadingProp.Retain()
	}
	go func() {
		defer source.Release()
		defer func() {
			if loadingProp != nil {
				loadingProp.SetInt(0)
				loadingProp.Release()
			}
		}()
		items, err := s.fetchSearch(query)
		if err != nil || len(items) == 0 {
			return
		}
		s.searchClassFill(source, items)
	}()
}

// searchClassFill builds the class directory once the results arrive.
func (s *System) searchClassFill(source *propcore.Prop, items []videoItem) {
	classNodes := s.propMgr.CreateEx(source, "nodes", nil, false, false)
	classDir := s.propMgr.CreateRootEx("", false)
	s.propMgr.CreateEx(classDir, "type", nil, false, false).SetString("directory")
	meta := s.propMgr.CreateEx(classDir, "metadata", nil, false, false)
	s.propMgr.CreateEx(meta, "title", nil, false, false).SetString("YouTube")
	s.propMgr.CreateEx(meta, "icon", nil, false, false).SetString("skin://icons/youtube.svg")
	if s.beSys != nil {
		s.propMgr.CreateEx(classDir, "url", nil, false, false).SetString(
			s.beSys.GetPropPageManager().BackendPropMake(s.propMgr, classDir, ""))
	}
	nodes := s.propMgr.CreateEx(classDir, "nodes", nil, false, false)
	for _, it := range items {
		s.addItemNode(nodes, it)
	}
	s.propMgr.CreateEx(classDir, "entries", nil, false, false).SetInt(len(items))
	s.propMgr.SetParentEx(classDir, classNodes, nil, "")
	s.ts.Debug("youtube", "search class: %d items", len(items))
}
