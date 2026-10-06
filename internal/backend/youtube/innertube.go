// Package youtube — native YouTube backend: browses via the Innertube
// API (youtubei/v1 — the same unauthenticated protocol yt-dlp and
// NewPipe use) and plays through videoparams: URLs handed to the
// canonical videoparams backend. Registered in the BackendSystem as a
// dynamic backend (youtube: prefix) — plugin-shaped (service entry +
// routes + plugin list registration) but compiled in.
// New in Go — upstream Movian C has no YouTube backend.
package youtube

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	facore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/misc"
)

const (
	innertubeEndpoint = "https://www.youtube.com/youtubei/v1"
	uaVR              = "com.google.android.apps.youtube.vr.oculus/1.60.19 (Linux; U; Android 12L) gzip"
	// ANDROID/IOS with current client versions return plain, signed
	// stream URLs and pass the IP bot-check that walls older mobile
	// clients — the same pair yt-dlp relies on.
	uaAndroid = "com.google.android.youtube/20.10.38 (Linux; U; Android 15) gzip"
	uaIOS     = "com.google.ios.youtube/20.10.4 (iPhone16,2; U; CPU iOS 18_3_2 like Mac OS X)"
	// YouTube's TVHTML5 /player gate-checks the User-Agent: only the
	// PlayStation 4 string passes (Cobalt, Tizen, desktop Chrome all
	// get "The page needs to be reloaded").
	uaTV  = "Mozilla/5.0 (PlayStation; PlayStation 4/12.00) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.0 Safari/605.1.15"
	uaWeb = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
)

// innertubeClient is one YouTube client personality. ANDROID_VR
// returns plain, unthrottled stream URLs for most videos as a guest;
// TVHTML5 is the only client that accepts OAuth Bearer auth — that's
// the signed-in path past the bot wall (PlayStation UA required);
// WEB is the guest fallback that may carry signatureCipher'd URLs.
type innertubeClient struct {
	name    string
	version string
	ua      string
	oauth   bool   // attach Bearer when a token exists (TV only)
	params  string // "params" field for /player bodies
	needSTS bool   // /player requires signatureTimestamp
	extra   map[string]any
}

var (
	// ANDROID/IOS — current mobile clients: plain, pre-signed stream
	// URLs (no n-throttle param) and no bot check at current versions.
	// Stale versions are rejected, so keep them current.
	clientAndroid = &innertubeClient{
		name:    "ANDROID",
		version: "20.10.38",
		ua:      uaAndroid,
		extra:   map[string]any{"androidSdkVersion": 35, "osVersion": "15"},
	}
	clientIOS = &innertubeClient{
		name:    "IOS",
		version: "20.10.4",
		ua:      uaIOS,
		extra:   map[string]any{"deviceModel": "iPhone16,2", "osVersion": "18.3.2", "osName": "iPhone"},
	}
	// ANDROID_VR — older VR client, kept as a fallback: plain URLs
	// when not walled, TV-style renderers on browse/search.
	clientVR = &innertubeClient{
		name:    "ANDROID_VR",
		version: "1.60.19",
		ua:      uaVR,
		extra:   map[string]any{"androidSdkVersion": 32},
	}
	// TVHTML5 — the OAuth-capable client (its /player accepts Bearer
	// and, behind auth, returns OK where guests hit the bot wall).
	// Guest calls on it hit LOGIN_REQUIRED, so it's ordered first
	// only when signed in.
	clientTV = &innertubeClient{
		name:    "TVHTML5",
		version: "7.20250319.10.00",
		ua:      uaTV,
		oauth:   true,
		params:  "2AMB",
		needSTS: true,
	}
	// WEB is the guest fallback (may carry signatureCipher'd URLs)
	// and serves classic videoRenderer results.
	clientWeb = &innertubeClient{
		name:    "WEB",
		version: "2.20240801.00.00",
		ua:      uaWeb,
		needSTS: true,
	}
)

// clientChain returns the player fallback order. The current
// ANDROID/IOS mobile clients lead: they return plain, pre-signed,
// unbound stream URLs and are not attestation-gated. TVHTML5 stays
// behind them even when signed in — it is the only OAuth client but
// its URLs are GVS-token bound and 403 on fetch more often than not;
// it remains for auth-required videos where nothing else answers.
func (s *System) clientChain() []*innertubeClient {
	if s.signedIn() {
		return []*innertubeClient{clientAndroid, clientIOS, clientTV, clientVR, clientWeb}
	}
	return []*innertubeClient{clientAndroid, clientIOS, clientVR, clientTV, clientWeb}
}

// browseChain is the fetch order for browse/search: only clients whose
// renderers the walker parses (TV-style and WEB). ANDROID/IOS answer
// player calls fine but return mobile renderers (elementRenderer,
// rich sections) that would come back empty here.
func (s *System) browseChain() []*innertubeClient {
	if s.signedIn() {
		return []*innertubeClient{clientTV, clientVR, clientWeb}
	}
	return []*innertubeClient{clientVR, clientWeb}
}

func (s *System) setVisitorData(v string) {
	if v == "" {
		return
	}
	s.visitorDataMu.Lock()
	defer s.visitorDataMu.Unlock()
	s.visitorData = v
}

func (s *System) getVisitorData() string {
	s.visitorDataMu.RLock()
	defer s.visitorDataMu.RUnlock()
	return s.visitorData
}

// captureVisitorData stores responseContext.visitorData for later
// requests — every Innertube wrapper calls it.
func (s *System) captureVisitorData(m jmap) {
	s.setVisitorData(m.a("responseContext").s("visitorData"))
}

// post sends one Innertube request. body gets the client context
// merged in; the client name+version go in headers (as the app does).
func (s *System) post(c *innertubeClient, endpoint string, body map[string]any) ([]byte, error) {
	if body == nil {
		body = map[string]any{}
	}
	ctx := map[string]any{
		"clientName":    c.name,
		"clientVersion": c.version,
	}
	maps.Copy(ctx, c.extra)
	if v := s.getVisitorData(); v != "" {
		ctx["visitorData"] = v
	}
	if s.prefHL != "" {
		ctx["hl"] = s.prefHL
	}
	if s.prefGL != "" {
		// Region is a country code — YouTube rejects lowercase ("it"
		// → INVALID_ARGUMENT on authed calls).
		ctx["gl"] = strings.ToUpper(s.prefGL)
	}
	body["context"] = map[string]any{"client": ctx}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	var q misc.HtsbufQueue
	q.HtsbufQueueSetup(-1)
	q.Append(raw)

	var result *misc.Buf
	var errbuf [256]byte
	var code int
	args := []any{
		facore.HTTPTagResultPtr, &result,
		facore.HTTPTagErrbuf, errbuf[:],
		facore.HTTPTagResponseCode, &code,
		facore.HTTPTagPostData, &q, "application/json",
		facore.HTTPTagFlags, facore.FaCompression,
		facore.HTTPTagRequestHeader, "User-Agent", c.ua,
		facore.HTTPTagRequestHeader, "X-Youtube-Client-Name", c.name,
		facore.HTTPTagRequestHeader, "X-Youtube-Client-Version", c.version,
	}
	if v := s.getVisitorData(); v != "" {
		args = append(args,
			facore.HTTPTagRequestHeader, "X-Goog-Visitor-Id", v)
	}
	// OAuth Bearer is only valid for TVHTML5 — other clients reject
	// it with INVALID_ARGUMENT, so it is never attached there.
	if c.oauth {
		if b := s.bearerHeader(); b != "" {
			args = append(args,
				facore.HTTPTagRequestHeader, "Authorization", b,
				facore.HTTPTagRequestHeader, "X-Goog-AuthUser", "0")
		}
	}

	if s.fam == nil {
		return nil, fmt.Errorf("innertube %s: fileaccess unavailable", endpoint)
	}
	if s.fam.HTTPReq(innertubeEndpoint+endpoint, args...) != 0 {
		return nil, fmt.Errorf("innertube %s: %s", endpoint,
			errbufString(errbuf[:]))
	}
	data := append([]byte(nil), result.C8()[:result.Len()]...)
	result.Release()
	if code != 200 {
		return nil, fmt.Errorf("innertube %s: HTTP %d", endpoint, code)
	}
	return data, nil
}

// errbufString reads the NUL-terminated C-style errbuf filled by
// HTTP_ERRBUF / FA errbuf seams.
func errbufString(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		return string(b[:i])
	}
	return string(b)
}

// httpGet fetches u through the app fileaccess stack — the same engine
// the demuxer uses (cookies, inspectors, proxy, domain UA). Returns
// the body, the HTTP status code (0 on transport failure) and an error.
func (s *System) httpGet(u string) ([]byte, int, error) {
	if s.fam == nil {
		return nil, 0, fmt.Errorf("fileaccess unavailable")
	}
	var result *misc.Buf
	var errbuf [256]byte
	var code int
	if s.fam.HTTPReq(u,
		facore.HTTPTagResultPtr, &result,
		facore.HTTPTagErrbuf, errbuf[:],
		facore.HTTPTagResponseCode, &code,
		facore.HTTPTagFlags, facore.FaCompression) != 0 {
		return nil, code, fmt.Errorf("%s", errbufString(errbuf[:]))
	}
	data := append([]byte(nil), result.C8()[:result.Len()]...)
	result.Release()
	return data, code, nil
}

// jmap is the nested-map helper over decoded Innertube JSON:
// m.a("b").a("c") walks objects, m.s/m.arr/m.text extract values.
type jmap map[string]any

// unmarshalJSON decodes an Innertube JSON response.
func unmarshalJSON(b []byte) (jmap, error) {
	var m jmap
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// a is the nested-map accessor used everywhere: m.a("b").a("c").
func (m jmap) a(key string) jmap {
	if m == nil {
		return nil
	}
	if v, ok := map[string]any(m)[key].(map[string]any); ok {
		return jmap(v)
	}
	return nil
}

func (m jmap) s(key string) string {
	if m == nil {
		return ""
	}
	s, _ := map[string]any(m)[key].(string)
	return s
}

func (m jmap) arr(key string) []any {
	if m == nil {
		return nil
	}
	a, _ := map[string]any(m)[key].([]any)
	return a
}

// text reads YouTube's runs/simpleText text containers.
func (m jmap) text(key string) string {
	t := m.a(key)
	if t == nil {
		return ""
	}
	if s := t.s("simpleText"); s != "" {
		return s
	}
	var out strings.Builder
	for _, r := range t.arr("runs") {
		if rm, ok := r.(map[string]any); ok {
			if s, ok := rm["text"].(string); ok {
				out.WriteString(s)
			}
		}
	}
	return out.String()
}

// thumbnails returns the best (last, largest) thumbnail URL.
func (m jmap) thumb() string {
	a := m.a("thumbnail").arr("thumbnails")
	if len(a) == 0 {
		return ""
	}
	last, _ := a[len(a)-1].(map[string]any)
	s, _ := last["url"].(string)
	return s
}

// ── Endpoint wrappers ───────────────────────────────────────────────

// player returns the /player response (streamingData, videoDetails,
// microformat, assets) for one video.
func (s *System) player(cl *innertubeClient, videoID string) (jmap, error) {
	pbCtx := map[string]any{
		"html5Preference": "HTML5_PREF_WANTS",
	}
	// signatureTimestamp proves the client matches a current player
	// revision — required by TVHTML5/WEB ("page needs to be reloaded"
	// without it). Mobile clients don't need it.
	if cl.needSTS {
		if sts := s.signatureTimestamp(videoID); sts != "" {
			pbCtx["signatureTimestamp"] = sts
		}
	}
	body := map[string]any{
		"videoId":         videoID,
		"contentCheckOk":  true,
		"racyCheckOk":     true,
		"playbackContext": map[string]any{"contentPlaybackContext": pbCtx},
	}
	if cl.params != "" {
		body["params"] = cl.params
	}
	b, err := s.post(cl, "/player?prettyPrint=false", body)
	if err != nil {
		return nil, err
	}
	m, err := unmarshalJSON(b)
	if err == nil {
		s.captureVisitorData(m)
	}
	return m, err
}

// browse returns the /browse response for a browseId (FEtrending,
// channel UC…, etc.). params selects a tab (e.g. channel Videos).
func (s *System) browse(cl *innertubeClient, browseID, params, continuation string) (jmap, error) {
	body := map[string]any{}
	if continuation != "" {
		body["continuation"] = continuation
	} else {
		body["browseId"] = browseID
		if params != "" {
			body["params"] = params
		}
	}
	b, err := s.post(cl, "/browse?prettyPrint=false", body)
	if err != nil {
		return nil, err
	}
	m, err := unmarshalJSON(b)
	if err == nil {
		s.captureVisitorData(m)
	}
	return m, err
}

// search returns the /search response for a query.
func (s *System) search(cl *innertubeClient, query, params, continuation string) (jmap, error) {
	body := map[string]any{"query": query}
	if continuation != "" {
		body["continuation"] = continuation
	} else if params != "" {
		body["params"] = params
	}
	b, err := s.post(cl, "/search?prettyPrint=false", body)
	if err != nil {
		return nil, err
	}
	m, err := unmarshalJSON(b)
	if err == nil {
		s.captureVisitorData(m)
	}
	return m, err
}

// ── Renderer walking ────────────────────────────────────────────────

// videoItem is one videoRenderer distilled for page rendering.
type videoItem struct {
	ID        string
	Title     string
	Channel   string
	Duration  string
	Views     string
	Published string
	Thumb     string
	Badge     string
}

// walkRenderers recursively finds every "videoRenderer" object in the
// response tree (browse/search nest them under several containers —
// contents, tabs, itemSectionRenderer, shelfRenderer, richItemRenderer…).
func walkRenderers(v any, out *[]videoItem) {
	var m map[string]any
	switch t := v.(type) {
	case jmap:
		m = map[string]any(t)
	case map[string]any:
		m = t
	case []any:
		for _, e := range t {
			walkRenderers(e, out)
		}
		return
	default:
		return
	}
	if vr, ok := m["videoRenderer"].(map[string]any); ok {
		vm := jmap(vr)
		if id := vm.s("videoId"); id != "" {
			*out = append(*out, videoItem{
				ID:        id,
				Title:     vm.text("title"),
				Channel:   vm.text("ownerText"),
				Duration:  vm.text("lengthText"),
				Views:     vm.text("viewCountText"),
				Published: vm.text("publishedTimeText"),
				Thumb:     vm.thumb(),
				Badge:     badgeLabel(vm),
			})
		}
	}
	// TV-style renderers (ANDROID_VR): videoWithContextRenderer on
	// browse/channel pages, compactVideoRenderer on search.
	for _, key := range []string{"videoWithContextRenderer", "compactVideoRenderer"} {
		if vr, ok := m[key].(map[string]any); ok {
			vm := jmap(vr)
			if id := vm.s("videoId"); id != "" {
				*out = append(*out, videoItem{
					ID:        id,
					Title:     firstNonEmpty(vm.text("title"), vm.text("headline")),
					Channel:   vm.text("shortBylineText"),
					Duration:  vm.text("lengthText"),
					Views:     firstNonEmpty(vm.text("viewCountText"), vm.text("shortViewCountText")),
					Published: vm.text("publishedTimeText"),
					Thumb:     vm.thumb(),
					Badge:     badgeLabel(vm),
				})
			}
		}
	}
	// movieCardRenderer — the movies storefront (FEstorefront) feed.
	if vr, ok := m["movieCardRenderer"].(map[string]any); ok {
		vm := jmap(vr)
		id := firstNonEmpty(vm.s("videoId"),
			vm.a("navigationEndpoint").a("watchEndpoint").s("videoId"))
		if id != "" {
			*out = append(*out, videoItem{
				ID:       id,
				Title:    firstNonEmpty(vm.text("title"), vm.text("headline")),
				Channel:  vm.text("shortBylineText"),
				Duration: vm.text("lengthText"),
				Thumb:    vm.thumb(),
				Badge:    badgeLabel(vm),
			})
		}
	}
	// lockupViewModel — the 2024+ WEB format (next/browse feeds).
	if lv, ok := m["lockupViewModel"].(map[string]any); ok {
		if it, ok2 := lockupItem(jmap(lv)); ok2 {
			*out = append(*out, it)
		}
	}
	// tileRenderer — the TVHTML5 browse/search feed format.
	if tr, ok := m["tileRenderer"].(map[string]any); ok {
		if it, ok2 := tileItem(jmap(tr)); ok2 {
			*out = append(*out, it)
		}
	}
	for _, e := range m {
		walkRenderers(e, out)
	}
}

// badgeLabel extracts a purchase badge ("Buy or rent" — localized)
// from a renderer's badges[]; YPC style marks paid/rental items.
func badgeLabel(vm jmap) string {
	for _, b := range vm.arr("badges") {
		bm, _ := b.(map[string]any)
		br := jmap(bm).a("metadataBadgeRenderer")
		if br.s("style") != "BADGE_STYLE_TYPE_YPC" {
			continue
		}
		if l := br.s("label"); l != "" {
			return l
		}
		return "Paid"
	}
	return ""
}

// tileItem distills a TVHTML5 tileRenderer: contentId,
// tileHeaderRenderer (thumbnail + time-status overlay),
// tileMetadataRenderer (title + channel/views/date line items).
func tileItem(tm jmap) (videoItem, bool) {
	if ct := tm.s("contentType"); ct != "" && ct != "TILE_CONTENT_TYPE_VIDEO" {
		return videoItem{}, false
	}
	var it videoItem
	it.ID = firstNonEmpty(tm.s("contentId"),
		tm.a("onSelectCommand").a("watchEndpoint").s("videoId"))
	hdr := tm.a("header").a("tileHeaderRenderer")
	it.Thumb = hdr.thumb()
	var parts []string
	// collectLines parses lineRenderer items: text parts plus
	// purchase badges (YPC — "Buy or rent").
	collectLines := func(meta jmap) {
		for _, ln := range meta.arr("lines") {
			lm, _ := ln.(map[string]any)
			for _, item := range jmap(lm).a("lineRenderer").arr("items") {
				im, _ := item.(map[string]any)
				li := jmap(im).a("lineItemRenderer")
				if br := li.a("badge").a("metadataBadgeRenderer"); br.s("style") == "BADGE_STYLE_TYPE_YPC" {
					it.Badge = firstNonEmpty(br.s("label"), "Paid")
					continue
				}
				if t := li.text("text"); t != "" && t != "•" {
					parts = append(parts, t)
				}
			}
		}
	}
	for _, ov := range hdr.arr("thumbnailOverlays") {
		ovm, _ := ov.(map[string]any)
		om := jmap(ovm)
		if t := om.a("thumbnailOverlayTimeStatusRenderer").text("text"); t != "" {
			it.Duration = t
			continue
		}
		// Movie tiles carry a tileMetadataRenderer overlay with
		// title + lines (genre • year, purchase badges).
		if tm2 := om.a("tileMetadataRenderer"); len(tm2) > 0 {
			if it.Title == "" {
				it.Title = tm2.text("title")
			}
			collectLines(tm2)
		}
	}
	meta := tm.a("metadata").a("tileMetadataRenderer")
	if it.Title == "" {
		it.Title = meta.text("title")
	}
	// lines[]: first line is the channel; later line items are
	// views/date strings separated by "•" items.
	collectLines(meta)
	if len(parts) > 0 {
		it.Channel = parts[0]
	}
	if len(parts) > 1 {
		it.Views = parts[1]
	}
	if len(parts) > 2 {
		it.Published = parts[2]
	}
	return it, it.ID != ""
}

// lockupItem distills a lockupViewModel: watchEndpoint.videoId,
// lockupMetadataViewModel.title.content, thumbnailViewModel sources,
// duration badge and metadata rows (channel/views/date).
func lockupItem(lm jmap) (videoItem, bool) {
	// Skip non-video lockups (playlists, channels, podcasts).
	if ct := lm.s("contentType"); ct != "" && ct != "LOCKUP_CONTENT_TYPE_VIDEO" {
		return videoItem{}, false
	}
	var it videoItem
	// videoId — top-level contentId (LOCKUP_CONTENT_TYPE_VIDEO),
	// else inside the tap command's watchEndpoint.
	it.ID = firstNonEmpty(lm.s("contentId"),
		findStr(map[string]any(lm), "watchEndpoint", "videoId"))
	meta := lm.a("metadata").a("lockupMetadataViewModel")
	it.Title = meta.a("title").s("content")
	// thumbnail — largest of the thumbnailViewModel sources.
	srcs := lm.a("contentImage").a("thumbnailViewModel").a("image").arr("sources")
	if n := len(srcs); n > 0 {
		if last, ok := srcs[n-1].(map[string]any); ok {
			it.Thumb, _ = last["url"].(string)
		}
	}
	// duration — first text badge on the thumbnail overlay.
	it.Duration = findStr(lm, "thumbnailBadgeViewModel", "text")
	// metadata rows → channel + views + published lines.
	cm := meta.a("metadata").a("contentMetadataViewModel")
	var parts []string
	for _, row := range cm.arr("metadataRows") {
		rm, _ := row.(map[string]any)
		for _, p := range jmap(rm).arr("metadataParts") {
			pm, _ := p.(map[string]any)
			if t := jmap(pm).a("text").s("content"); t != "" {
				parts = append(parts, t)
			}
		}
	}
	if len(parts) > 0 {
		it.Channel = parts[0]
	}
	if len(parts) > 1 {
		it.Views = parts[1]
	}
	if len(parts) > 2 {
		it.Published = parts[2]
	}
	return it, it.ID != ""
}

// shelf is one home-feed section (shelfRenderer): a title plus the
// videoItems inside it — maps to one row in the searchresults view.
type shelf struct {
	Title string
	Items []videoItem
}

// walkShelves collects shelfRenderer sections (the home feed's own
// grouping). Video renderers found outside any shelf are gathered in
// `loose` — the caller prepends them as a generic section.
func walkShelves(v any, shelves *[]shelf, loose *[]videoItem) {
	if jm, ok := v.(jmap); ok {
		v = map[string]any(jm)
	}
	m, ok := v.(map[string]any)
	if !ok {
		if a, ok := v.([]any); ok {
			for _, e := range a {
				walkShelves(e, shelves, loose)
			}
		}
		return
	}
	if sr, ok := m["shelfRenderer"].(map[string]any); ok {
		title := jmap(sr).text("title")
		if title == "" {
			// TVHTML5 shelves carry the title inside headerRenderer
			// (shelfHeaderRenderer/avatarLockup), not a top-level
			// "title" field.
			if hr, ok := sr["headerRenderer"]; ok {
				title = findTitle(hr)
			}
		}
		sh := shelf{Title: title}
		var items []videoItem
		walkRenderers(map[string]any(sr), &items)
		sh.Items = items
		*shelves = append(*shelves, sh)
		return // shelf content consumed — don't descend again
	}
	// Non-shelf video renderers at this level are consumed here —
	// never descended into again.
	for _, key := range []string{"videoRenderer",
		"videoWithContextRenderer", "compactVideoRenderer",
		"lockupViewModel", "tileRenderer"} {
		if vr, ok := m[key]; ok {
			var one []videoItem
			walkRenderers(map[string]any{key: vr}, &one)
			if loose != nil {
				*loose = append(*loose, one...)
			}
		}
	}
	for k, e := range m {
		switch k {
		case "videoRenderer", "videoWithContextRenderer",
			"compactVideoRenderer", "lockupViewModel", "tileRenderer":
			continue
		}
		walkShelves(e, shelves, loose)
	}
}

// findStr locates the first `outerKey` object anywhere in the tree and
// returns its `innerKey` string field.
func findStr(v any, outerKey, innerKey string) string {
	if jm, ok := v.(jmap); ok {
		v = map[string]any(jm)
	}
	m, ok := v.(map[string]any)
	if !ok {
		if a, ok := v.([]any); ok {
			for _, e := range a {
				if s := findStr(e, outerKey, innerKey); s != "" {
					return s
				}
			}
		}
		return ""
	}
	if inner, ok := m[outerKey].(map[string]any); ok {
		if s, ok := inner[innerKey].(string); ok && s != "" {
			return s
		}
	}
	for _, e := range m {
		if s := findStr(e, outerKey, innerKey); s != "" {
			return s
		}
	}
	return ""
}

// findTitle locates the first "title" runs/simpleText in the tree —
// used for shelf headers where the title is nested under
// avatarLockupRenderer rather than a plain "title" field.
func findTitle(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		if a, ok := v.([]any); ok {
			for _, e := range a {
				if s := findTitle(e); s != "" {
					return s
				}
			}
		}
		return ""
	}
	if s := jmap(m).text("title"); s != "" {
		return s
	}
	for _, e := range m {
		if s := findTitle(e); s != "" {
			return s
		}
	}
	return ""
}

// continuationToken finds the next-page continuation token, if any.
// Both shapes are recognized: continuationItemRenderer (WEB-style
// lists) and continuations/nextContinuationData on a node that also
// has "contents" (sectionListRenderer etc). Per-shelf continuations —
// horizontalListRenderer carries one for "scroll right" — are skipped:
// they paginate a row, not the feed.
func continuationToken(v any) string {
	if jm, ok := v.(jmap); ok {
		v = map[string]any(jm)
	}
	m, ok := v.(map[string]any)
	if !ok {
		if a, ok := v.([]any); ok {
			for _, e := range a {
				if s := continuationToken(e); s != "" {
					return s
				}
			}
		}
		return ""
	}
	if ci, ok := m["continuationItemRenderer"].(map[string]any); ok {
		return jmap(ci).a("continuationEndpoint").a("continuationCommand").s("token")
	}
	if _, hasContents := m["contents"]; hasContents {
		for _, c := range jmap(m).arr("continuations") {
			cm, _ := c.(map[string]any)
			for _, k := range []string{"nextContinuationData",
				"reloadContinuationData"} {
				if s := jmap(cm).a(k).s("continuation"); s != "" {
					return s
				}
			}
		}
	}
	for k, e := range m {
		switch k {
		case "shelfRenderer", "horizontalListRenderer":
			continue // per-shelf pagination — not the feed continuation
		}
		if s := continuationToken(e); s != "" {
			return s
		}
	}
	return ""
}
