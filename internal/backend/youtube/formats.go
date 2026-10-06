// formats.go — streamingData → a single playable stream URL.
// Preference order:
//  1. hlsManifestUrl — emitted with the hls: scheme so the canonical
//     movian HLS demuxer owns it: master variants get real ABR via
//     bandwidth estimation and EXT-X-MEDIA audio renditions keep
//     demuxed VODs audible. A bare https URL would fall to FFmpeg
//     (the fa_video sniff misses STREAM-INF past its 1023-byte window)
//     which downloads every variant and plays the first.
//  2. Best muxed ("progressive") format — single mp4 with audio+video
//     (itag 22 = 720p, 18 = 360p); always FFmpeg-playable.
//  3. Best adaptive video-only format — degraded (no audio) but never
//     leaves the user with nothing.
//
// URLs carrying signatureCipher are deciphered via base.js (decipher.go).
package youtube

import (
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/czz/movian-go/internal/misc"
)

// format is one entry of streamingData.formats / adaptiveFormats.
type format struct {
	itag             float64
	url              string
	mimeType         string
	bitrate          float64
	height           float64
	audioQuality     string
	qualityLabel     string
	signatureCipher  string
	approxDurationMs string
}

// parseFormats collects formats from both lists of streamingData.
func parseFormats(sd jmap) []format {
	var out []format
	for _, list := range []string{"formats", "adaptiveFormats"} {
		for _, e := range sd.arr(list) {
			em, ok := e.(map[string]any)
			if !ok {
				continue
			}
			m := jmap(em)
			out = append(out, format{
				itag:             num(m, "itag"),
				url:              m.s("url"),
				mimeType:         m.s("mimeType"),
				bitrate:          num(m, "bitrate"),
				height:           num(m, "height"),
				audioQuality:     m.s("audioQuality"),
				qualityLabel:     m.s("qualityLabel"),
				signatureCipher:  firstNonEmpty(m.s("signatureCipher"), m.s("cipher")),
				approxDurationMs: m.s("approxDurationMs"),
			})
		}
	}
	return out
}

func num(m jmap, key string) float64 {
	f, _ := map[string]any(m)[key].(float64)
	return f
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// muxed reports whether the format carries both audio and video.
// Muxed formats have a qualityLabel (video) AND audioQuality.
func (f format) muxed() bool {
	return f.qualityLabel != "" && f.audioQuality != ""
}

// pickStream selects the playable URL from a /player response.
// maxH < 0 applies the video-quality pref, 0 is uncapped, >0 caps the
// resolution. Returns (streamURL, mimetype, kind) where kind is "hls",
// "muxed" or "video" (video-only, silent — worst case). Empty
// streamURL = nothing playable. The response's assets.js path is
// staged for decipher work.
func (s *System) pickStream(resp jmap, maxH int) (string, string, string) {
	return s.pickStream0(resp, maxH, true)
}

func (s *System) pickStream0(resp jmap, maxH int, allowHLS bool) (string, string, string) {
	jsURL := resp.a("assets").s("js")
	if jsURL == "" {
		// TV player responses carry no assets — the embed page names
		// the current base.js instead.
		if vid := resp.a("videoDetails").s("videoId"); vid != "" {
			jsURL, _ = s.playerScript(vid)
		}
	}
	sd := resp.a("streamingData")
	if sd == nil {
		return "", "", ""
	}
	if hls := sd.s("hlsManifestUrl"); allowHLS && hls != "" {
		return hls, "application/vnd.apple.mpegurl", "hls"
	}
	formats := parseFormats(sd)
	// Best muxed first, best adaptive video as degraded fallback.
	// capH (0 = auto) caps the resolution: prefer the highest
	// bitrate at-or-below the cap; if every format exceeds it, take the
	// lowest-resolution one.
	capH := maxH
	if capH < 0 {
		capH = s.maxHeight()
	}
	var muxed, video *format
	better := func(best, cand *format) *format {
		if best == nil {
			return cand
		}
		inB := capH <= 0 || best.height <= float64(capH)
		inC := capH <= 0 || cand.height <= float64(capH)
		switch {
		case inC && !inB:
			return cand
		case inC && inB && cand.bitrate > best.bitrate:
			return cand
		case !inC && !inB && cand.height < best.height:
			return cand
		}
		return best
	}
	for i := range formats {
		f := &formats[i]
		if f.muxed() {
			muxed = better(muxed, f)
		} else if f.qualityLabel != "" {
			video = better(video, f)
		}
	}
	if muxed != nil {
		return s.streamURL(muxed, jsURL), "video/mp4", "muxed"
	}
	if video != nil {
		return s.streamURL(video, jsURL), "video/mp4", "video"
	}
	return "", "", ""
}

// streamHeights — the distinct heights the picker can actually
// deliver for this /player response, sorted descending; used to fill
// the OSD quality menu (each entry maps to a pickStream maxH cap).
// pickStream0 always prefers muxed formats when any exist, so the
// heights of video-only adaptive formats are unreachable then and
// must not be advertised.
func streamHeights(resp jmap) []int {
	sd := resp.a("streamingData")
	if sd == nil {
		return nil
	}
	formats := parseFormats(sd)
	muxedAny := false
	for i := range formats {
		if formats[i].muxed() {
			muxedAny = true
			break
		}
	}
	seen := map[int]bool{}
	var out []int
	for i := range formats {
		f := &formats[i]
		if f.qualityLabel == "" || (muxedAny && !f.muxed()) {
			continue
		}
		h := int(f.height)
		if h > 0 && !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	slices.SortFunc(out, func(a, b int) int { return b - a })
	return out
}

// streamURL resolves a format to a playable URL: plain `url` field
// wins; signatureCipher'd URLs are deciphered via base.js. The n
// throttling parameter is transformed when required.
func (s *System) streamURL(f *format, jsURL string) string {
	var u string
	if f.url != "" {
		u = f.url
	} else if f.signatureCipher != "" {
		if d, err := s.decipherURL(f.signatureCipher, jsURL); err == nil {
			u = d
		}
	}
	if u == "" {
		return ""
	}
	return s.applyNThrottling(u, jsURL)
}

// decipherURL expands "s=…&sp=…&url=…" ciphers into a playable URL:
// base.js sig-transform on s, then url-encoded into the `sp` param.
func (s *System) decipherURL(sc, jsURL string) (string, error) {
	q, err := url.ParseQuery(sc)
	if err != nil {
		return "", err
	}
	sig := q.Get("s")
	sp := q.Get("sp")
	if sp == "" {
		sp = "signature"
	}
	base := q.Get("url")
	if sig == "" || base == "" {
		return "", nil
	}
	dec, err := s.decipherSig(sig, jsURL)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	v := u.Query()
	v.Set(sp, dec)
	u.RawQuery = v.Encode()
	return u.String(), nil
}

// applyNThrottling rewrites the n= parameter of a googlevideo URL when
// the player requires it (throttled URLs stall otherwise).
func (s *System) applyNThrottling(u, jsURL string) string {
	pu, err := url.Parse(u)
	if err != nil {
		return u
	}
	n := pu.Query().Get("n")
	if n == "" {
		return u
	}
	if nn, err := s.nTransform(n, jsURL); err == nil && nn != "" {
		v := pu.Query()
		v.Set("n", nn)
		pu.RawQuery = v.Encode()
		return pu.String()
	}
	return u
}

// hlsVariantInfo — one variant of a master playlist: the height from
// its #EXT-X-STREAM-INF RESOLUTION attribute (0 when absent), whether
// its CODECS list carries an avc1 entry (the only video codec the
// canonical HLS TS demuxer accepts — non-avc1 variants are flagged
// audio-only by hls_ext_x_stream_inf and can never play), plus the
// resolved variant URL.
type hlsVariantInfo struct {
	height int
	avc    bool
	url    string
}

// probeHLS verifies an hlsManifestUrl is actually fetchable before
// committing to it: for live broadcasts it is the only playable URL,
// but some clients (authed TVHTML5 above all — its URLs are GVS-token
// bound) hand back manifest trees whose variant playlists 403 on a
// plain fetch, which used to surface only as a demuxer error
// mid-open. Fetches the master, then its first AND last referenced
// resources — the demuxer's initial pick is the last variant
// (demuxerSelectVariantSimple walks Backward at bw=0) while the probe
// used to test only the first, so a partially dead manifest could
// slip through. Resolution uses the same UrlResolveRelativeFromBase
// and fileaccess HTTP stack the demuxer uses.
//
// Returns the variant list extracted from #EXT-X-STREAM-INF tags —
// lives expose quality only this way (no adaptiveFormats), so the OSD
// quality menu and the ?h= cap ride on it — and the raw master body so
// a height-capped playback can be served a filtered master.
func (s *System) probeHLS(manifestURL string) ([]hlsVariantInfo, []byte, bool) {
	body, code, err := s.httpGet(manifestURL)
	if err != nil || code != 200 {
		return nil, nil, false
	}
	var first, last string
	var variants []hlsVariantInfo
	streamInf := "" // pending #EXT-X-STREAM-INF tag
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			streamInf = line
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		if first == "" {
			first = line
		}
		last = line
		if streamInf != "" {
			variants = append(variants, hlsVariantInfo{
				height: hlsStreamInfHeight(streamInf),
				avc:    hlsStreamInfAVC(streamInf),
				url:    misc.UrlResolveRelativeFromBase(manifestURL, line),
			})
			streamInf = ""
		}
	}
	if first == "" {
		return nil, nil, false // no resource lines — not a usable playlist
	}
	for _, l := range [2]string{first, last} {
		if !s.httpStatusOK(misc.UrlResolveRelativeFromBase(
			manifestURL, l)) {
			return nil, nil, false
		}
	}
	return variants, body, true
}

// hlsStreamInfHeight extracts the height from a #EXT-X-STREAM-INF
// attribute list (RESOLUTION=WxH). 0 when absent/unparseable.
func hlsStreamInfHeight(tag string) int {
	for _, attr := range strings.Split(tag, ",") {
		if v, ok := strings.CutPrefix(attr, "RESOLUTION="); ok {
			if _, h, ok2 := strings.Cut(v, "x"); ok2 {
				if n, err := strconv.Atoi(h); err == nil {
					return n
				}
			}
		}
	}
	return 0
}

// hlsStreamInfAVC reports whether the tag's CODECS attribute lists an
// avc1 codec — C hls_ext_x_stream_inf marks variants without avc1 as
// audio-only, so only avc1 variants can ever render video.
func hlsStreamInfAVC(tag string) bool {
	for _, attr := range strings.Split(tag, ",") {
		if v, ok := strings.CutPrefix(attr, "CODECS=\""); ok {
			return strings.Contains(v, "avc1")
		}
	}
	return false
}

// hlsVariantHeights — distinct heights the canonical demuxer can
// actually play (avc1 variants only — anything else is flagged
// audio-only at parse), descending; feeds the OSD quality menu.
func hlsVariantHeights(variants []hlsVariantInfo) []int {
	seen := map[int]bool{}
	var out []int
	for _, v := range variants {
		if v.height > 0 && v.avc && !seen[v.height] {
			seen[v.height] = true
			out = append(out, v.height)
		}
	}
	slices.SortFunc(out, func(a, b int) int { return b - a })
	return out
}

// hlsFilterMaster rewrites a master playlist dropping every
// #EXT-X-STREAM-INF variant whose RESOLUTION height exceeds capH —
// an explicit ?h= quality cap must keep the master shape (a bare
// variant playlist carries no EXT-X-MEDIA audio renditions, so it
// would play muted). Variants without RESOLUTION are kept — their
// height cannot disprove the cap. Returns nil when nothing survives
// (cap below every listed variant — impossible via the OSD menu,
// which only offers real heights, but reachable through hand-edited
// URLs).
func hlsFilterMaster(body []byte, capH int) []byte {
	var out strings.Builder
	lines := strings.Split(string(body), "\n")
	kept := 0
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if !strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			out.WriteString(line)
			out.WriteByte('\n')
			continue
		}
		h := hlsStreamInfHeight(line)
		// The variant URL is the next non-comment line — consume it
		// together with the tag so dropping never leaves orphans.
		if i+1 < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i+1]), "#") {
			if h <= 0 || h <= capH {
				out.WriteString(line)
				out.WriteByte('\n')
				out.WriteString(lines[i+1])
				out.WriteByte('\n')
				kept++
			}
			i++
			continue
		}
		if h <= 0 || h <= capH {
			out.WriteString(line)
			out.WriteByte('\n')
			kept++
		}
	}
	if kept == 0 {
		return nil
	}
	return []byte(out.String())
}

// httpStatusOK reports whether a GET on u succeeds through the app
// HTTP stack (redirects followed; non-2xx is an error there).
func (s *System) httpStatusOK(u string) bool {
	_, code, err := s.httpGet(u)
	return err == nil && code >= 200 && code < 400
}
