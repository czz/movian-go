// Canonical port of src/backend/hls/hls.c — HLS playlist/variant/segment
// management and the dual-demuxer playback loop. hls.h structures are
// reproduced as Go types; all 55 C functions are mapped in file order.
//
// Sentinel pointers (void*)-1/-2/-3 are modelled by mbRef/segRef codes.
package hls

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/czz/movian-go/internal/arch"
	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/trace"
)

func pm(h *hls) *propcore.PropManager {
	if h == nil || h.MP == nil || h.MP.PropRoot == nil {
		return nil
	}
	return h.MP.PropRoot.Manager()
}

func hlsFreeMbp(mp *mediacore.MediaPipe, mbp *mbRef) {
	mb := mbp.mb
	if mbp.mb == nil && mbp.code == 0 {
		return
	}
	if mbp.code == 0 {
		mediacore.MediaBufFreeUnlocked(mp, mb)
	}
	*mbp = mbRef{}
}

// lpLine converts an lp_get result to a Go string. lp_get returns the
// remaining buffer with an embedded NUL terminator (C char* semantics),
// so the line ends at the first NUL, not at the slice end.
func lpLine(s []byte) string {
	if i := bytes.IndexByte(s, 0); i >= 0 {
		s = s[:i]
	}
	return string(s)
}

// getAttrib parses "key=value" / "key=\"quoted\"" attribute lists like C's
// get_attrib. Returns the remainder after the terminating ','.
func getAttrib(v string) (key, value, rest string, ok bool) {
	key = strings.TrimLeft(v, " ")
	i := strings.IndexByte(key, '=')
	if i < 0 {
		return "", "", "", false
	}
	key, v = key[:i], key[i+1:]
	for len(v) > 0 && v[0] < 33 {
		v = v[1:]
	}
	if len(v) > 0 && v[0] == '"' {
		v = v[1:]
		value = v
		j := 0
		// C: while(*v && *v != '"' && v[-1] != '\\') v++;
		for j < len(value) && value[j] != '"' && (j == 0 || value[j-1] != '\\') {
			j++
		}
		if j < len(value) {
			v = value[j+1:]
			value = value[:j]
		} else {
			v = value[j:]
			value = value[:j]
		}
	} else {
		value = v
	}
	j := 0
	for j < len(v) && v[j] != ',' {
		j++
	}
	if j < len(v) {
		rest = v[j+1:]
	} else {
		rest = ""
	}
	if v == value && j < len(v) {
		value = v[:j]
	}
	return key, value, rest, true
}

func discontinuitySeqGet(h *hls, seq int) *hlsDiscontinuitySegment {
	for _, hds := range h.DiscontinuitySegments {
		if hds.Seq == seq {
			hds.Refcount++
			return hds
		}
	}
	hds := &hlsDiscontinuitySegment{Refcount: 1, Seq: seq}
	// C: LIST_INSERT_HEAD
	h.DiscontinuitySegments = slices.Insert(h.DiscontinuitySegments, 0, hds)
	if seq != 0 {
		hds.Offset = mediacore.PTSUnset
	}
	return hds
}

func discontinuitySeqRelease(h *hls, hds *hlsDiscontinuitySegment) {
	hds.Refcount--
	if hds.Refcount != 0 {
		return
	}
	for i, x := range h.DiscontinuitySegments {
		if x == hds {
			h.DiscontinuitySegments = append(
				h.DiscontinuitySegments[:i],
				h.DiscontinuitySegments[i+1:]...)
			return
		}
	}
}

func segmentDestroy(hs *hlsSegment) {
	hv := hs.Variant
	discontinuitySeqRelease(hv.Demuxer.HLS, hs.DiscontinuitySegment)

	if hs.FH != nil {
		hs.FH.Close()
	}
	for i, x := range hv.Segments {
		if x == hs {
			hv.Segments = slices.Delete(hv.Segments, i, i+1)
			break
		}
	}
	if hs == hv.SegmentSearch {
		hv.SegmentSearch = nil
	}
}

func variantDestroy(hv *hlsVariant) {
	hlsVariantClose(hv)
	for len(hv.Segments) > 0 {
		segmentDestroy(hv.Segments[0])
	}
}

func variantsDestroy(q *[]*hlsVariant) {
	for len(*q) > 0 {
		hv := (*q)[0]
		*q = (*q)[1:]
		variantDestroy(hv)
	}
}

func variantCreate(hd *hlsDemuxer) *hlsVariant {
	hv := &hlsVariant{
		StartTimeOffset: mediacore.PTSUnset,
		Demuxer:         hd,
		FirstSeq:        -1,
		LastSeq:         -1,
	}
	return hv
}

func hvAddSegment(hv *hlsVariant, url string) *hlsSegment {
	hs := &hlsSegment{
		TSOffset:   mediacore.PTSUnset,
		URL:        misc.UrlResolveRelativeFromBase(hv.URL, url),
		Variant:    hv,
		ByteOffset: -1,
		ByteSize:   -1,
	}
	hv.Segments = append(hv.Segments, hs)
	return hs
}

func hvParseKey(hvp *hlsVariantParser, baseurl, V string) {
	v := V
	hvp.Crypto = HLSCryptoNone
	hvp.ExplicitIV = 0

	for len(v) > 0 {
		key, value, rest, ok := getAttrib(v)
		if !ok {
			break
		}
		v = rest
		switch key {
		case "METHOD":
			if value == "AES-128" {
				hvp.Crypto = HLSCryptoAES128
			}
		case "URI":
			hvp.KeyURL = misc.UrlResolveRelativeFromBase(baseurl, value)
		case "IV":
			if strings.HasPrefix(value, "0x") ||
				strings.HasPrefix(value, "0X") {
				hvp.ExplicitIV = 1
				misc.Hex2bin(hvp.IV[:], 16, value[2:])
			}
		}
	}
}

func hvParseStart(hv *hlsVariant, V string) {
	v := V
	for len(v) > 0 {
		key, value, rest, ok := getAttrib(v)
		if !ok {
			break
		}
		v = rest
		if key == "TIME-OFFSET" {
			if d := myStr2double(value); d > 0 {
				hv.StartTimeOffset = int64(d * 1000000)
			} else {
				hv.StartTimeOffset = 0
			}
		}
	}
}

func hlsBadVariant(hv *hlsVariant, err hlsError) {
	hd := hv.Demuxer
	h := hd.HLS
	now := arch.GetTS()

	h.LastError = err

	hlsTrace(h, "Unable to demux variant %s -- %s",
		hv.Name, hlserrstr[err])
	hv.CorruptCounter++
	if hv.CorruptTimer < now-hlsCorruptionPeriodUs {
		hv.CorruptionsLast = 1
		hv.CorruptTimer = now
	} else {
		hv.CorruptionsLast++
	}

	hlsVariantClose(hv)
	time.Sleep(100 * time.Millisecond)
	hd.Req = hlsDemuxerSelectVariant(hd, now, 0)
	hd.LastSwitch = now
}

func hlsVariantUpdate(hv *hlsVariant, mp *mediacore.MediaPipe) hlsError {
	if hv.Frozen {
		return hlsErrorOK
	}
	h := hv.Demuxer.HLS

	b, lerr := fileaccesscore.FALoad2(mp.FAM, hv.URL,
		&fileaccesscore.FALoadArgs{
			Flags:       fileaccesscore.FaCompression,
			Cancellable: h.MP.Cancellable,
		})

	if b == nil {
		if misc.CancellableIsCancelled(h.MP.Cancellable) == 0 {
			msg := "load failed"
			if lerr != nil {
				msg = lerr.Error()
			}
			// Log url length + tail so signature truncation is
			// diagnosable from the log line (googlevideo signed
			// URLs carry sig/lsig at the end of the path).
			tail := hv.URL
			if len(tail) > 80 {
				tail = "…" + tail[len(tail)-80:]
			}
			h.ts.Trace(trace.TRACE_ERROR, "HLS",
				"Unable to open variant (len=%d tail=%q) %s -- %s",
				len(hv.URL), tail, hv.URL, msg)
		}
		return hlsErrorVariantNotFound
	}

	hv.Loaded = time.Now().Unix()

	var duration float64
	byteOffset := -1
	byteSize := -1
	seq := 1
	items := 0
	changed := false
	var hvp hlsVariantParser
	firstSeq := -1
	discontinuitySeq := -1

	lp := b.Data
	for {
		s := misc.LpGet(&lp)
		if s == nil {
			break
		}
		line := lpLine(s)
		if v, ok := mystrbegins(line, "#EXTINF:"); ok {
			duration = myStr2double(v)
		} else if _, ok := mystrbegins(line, "#EXT-X-ENDLIST"); ok {
			hv.Frozen = true
		} else if v, ok := mystrbegins(line, "#EXT-X-TARGETDURATION"); ok {
			hv.TargetDuration = misc.Atoi(v)
		} else if v, ok := mystrbegins(line, "#EXT-X-KEY:"); ok {
			hvParseKey(&hvp, hv.URL, v)
		} else if v, ok := mystrbegins(line, "#EXT-X-MEDIA-SEQUENCE:"); ok {
			seq = misc.Atoi(v)
			if firstSeq == -1 {
				firstSeq = seq
			}
		} else if v, ok := mystrbegins(line, "#EXT-X-DISCONTINUITY-SEQUENCE:"); ok {
			discontinuitySeq = misc.Atoi(v)
		} else if line == "#EXT-X-DISCONTINUITY" {
			if discontinuitySeq == -1 {
				if hs := hvFindSegmentBySeq(hv, seq-1); hs != nil {
					discontinuitySeq = hs.DiscontinuitySegment.Seq
				} else {
					discontinuitySeq = 0
				}
			}
			discontinuitySeq++
		} else if v, ok := mystrbegins(line, "#EXT-X-START:"); ok {
			hvParseStart(hv, v)
		} else if v, ok := mystrbegins(line, "#EXT-X-BYTERANGE:"); ok {
			byteSize = misc.Atoi(v)
			if _, after, ok := strings.Cut(v, "@"); ok {
				byteOffset = misc.Atoi(after)
			}
		} else if len(line) > 0 && line[0] != '#' {
			items++

			if seq > hv.LastSeq {
				if discontinuitySeq == -1 {
					if hs := hvFindSegmentBySeq(hv, seq-1); hs != nil {
						discontinuitySeq = hs.DiscontinuitySegment.Seq
					} else {
						discontinuitySeq = 0
					}
				}
				if hv.FirstSeq == -1 {
					hv.FirstSeq = seq
				}
				hs := hvAddSegment(hv, line)
				hs.ByteOffset = byteOffset
				hs.ByteSize = byteSize
				hs.TimeOffset = hv.Duration
				hs.Duration = int64(duration * 1000000)
				hs.Crypto = hvp.Crypto
				hs.KeyURL = hvp.KeyURL
				hs.DiscontinuitySegment =
					discontinuitySeqGet(h, discontinuitySeq)

				hv.Duration += hs.Duration

				if hvp.ExplicitIV != 0 {
					copy(hs.IV[:], hvp.IV[:])
				} else {
					for i := range 12 {
						hs.IV[i] = 0
					}
					hs.IV[12] = byte(seq >> 24)
					hs.IV[13] = byte(seq >> 16)
					hs.IV[14] = byte(seq >> 8)
					hs.IV[15] = byte(seq)
				}

				if hv.TargetDuration == 0 {
					hv.TargetDuration = int(duration)
				}

				changed = true
				hs.Seq = seq
				duration = 0
				byteOffset = -1
				byteSize = -1
				hv.LastSeq = hs.Seq
			}
			seq++
		}
	}

	if firstSeq == -1 {
		firstSeq = 1
	}

	for len(hv.Segments) > 0 {
		hs := hv.Segments[0]
		if hs.Seq >= firstSeq || hs.FH != nil || hv.CurrentSeg == hs {
			break
		}
		segmentDestroy(hs)
		changed = true
	}

	if len(hv.Segments) == 0 {
		return hlsErrorVariantEmpty
	}

	if changed {
		first := hv.Segments[0].Seq
		last := hv.Segments[len(hv.Segments)-1].Seq
		hlsTrace(h, "Loaded segments %d ... %d (%d)", first, last,
			last-first)
	}

	if hv.Frozen && hv.Duration != 0 {
		h.Duration = hv.Duration
	}

	return hlsErrorOK
}

// variantCmp — C: variant_cmp returns b->hv_bitrate - a->hv_bitrate
// (descending bitrate order for LIST_INSERT_SORTED).
func variantCmp(a, b *hlsVariant) int {
	return b.Bitrate - a.Bitrate
}

func checkIfBitrateExist(hd *hlsDemuxer, bitrate int) bool {
	for _, hv := range hd.Variants {
		if hv.Bitrate == bitrate {
			return true
		}
	}
	return false
}

func hlsAddVariant(h *hls, url string, hv *hlsVariant,
	hd *hlsDemuxer, name string) *hlsVariant {
	if hv == nil {
		hv = variantCreate(hd)
	}

	if hv.Bitrate != 0 && checkIfBitrateExist(hd, hv.Bitrate) {
		hlsTrace(h, "Skipping duplicate bitrate %d via %s",
			hv.Bitrate, url)
		variantDestroy(hv)
		return nil
	}

	if name != "" {
		hv.Name = truncateName(name)
	} else {
		hv.Name = truncateName(fmt.Sprintf("bitrate %d", hv.Bitrate))
	}

	hv.URL = misc.UrlResolveRelativeFromBase(h.BaseURL, url)

	// C: TAILQ_INSERT_SORTED(&hd->hd_variants, hv, hv_link, variant_cmp)
	// — insert before first e where variantCmp(hv, e) <= 0.
	pos := len(hd.Variants)
	for i, e := range hd.Variants {
		if variantCmp(hv, e) <= 0 {
			pos = i
			break
		}
	}
	hd.Variants = append(hd.Variants, nil)
	copy(hd.Variants[pos+1:], hd.Variants[pos:])
	hd.Variants[pos] = hv
	return hv
}

// truncateName — C: snprintf(hv->hv_name, sizeof(hv->hv_name) = 32, ...)
// truncates to 31 chars + NUL.
func truncateName(s string) string {
	if len(s) > 31 {
		return s[:31]
	}
	return s
}

func hlsExtXMedia(h *hls, V string) {
	v := V
	var typ, group, name, lang, uri string
	def := false
	autosel := false

	for len(v) > 0 {
		key, value, rest, ok := getAttrib(v)
		if !ok {
			break
		}
		v = rest
		switch key {
		case "TYPE":
			typ = value
		case "GROUP-ID":
			group = value
		case "NAME":
			name = value
		case "LANGUAGE":
			lang = value
		case "AUTOSELECT":
			autosel = value == "YES"
		case "DEFAULT":
			def = value == "YES"
		case "URI":
			uri = value
		}
	}
	if uri == "" || typ == "" {
		return
	}

	opt := func(s string) string {
		if s == "" {
			return "<unset>"
		}
		return s
	}
	hlsTrace(h, "Secondary stream %s "+
		"(type=%s group=%s name=%s language=%s autoselect=%s default=%s)",
		uri, typ, opt(group), opt(name), opt(lang),
		map[bool]string{true: "YES", false: "NO"}[autosel],
		map[bool]string{true: "YES", false: "NO"}[def])

	if typ == "AUDIO" {
		nm := name
		if nm == "" {
			nm = "audio"
		}
		hv := hlsAddVariant(h, uri, nil, &h.Audio, nm)
		if hv != nil {
			hv.AudioStream = hlsGetAudioTrack(h, 0, hv.URL,
				lang, "", misc.BoolToInt(autosel))
		}
	}
}

// AVC_h264_codecs — C: AVC_h264_codecs (hls.c:2059-2080).
var avcH264Codecs = []struct {
	name    string
	profile int
	level   int
}{
	{"avc1.42001e", 66, 30},
	{"avc1.66.30", 66, 30},
	{"avc1.42001f", 66, 31},
	{"avc1.4d001e", 77, 30},
	{"avc1.77.30", 77, 30},
	{"avc1.4d001f", 77, 31},
	{"avc1.4d0028", 77, 40},
	{"avc1.64001f", 100, 31},
	{"avc1.640028", 100, 40},
	{"avc1.640029", 100, 41},
}

func hlsExtXStreamInf(h *hls, V string, hvp **hlsVariant,
	hd *hlsDemuxer) {
	v := V

	if *hvp != nil {
		*hvp = nil // C: free(*hvp) — variant is unattached, GC'd
	}

	hv := variantCreate(hd)
	*hvp = hv

	for len(v) > 0 {
		key, value, rest, ok := getAttrib(v)
		if !ok {
			break
		}
		v = rest
		switch key {
		case "BANDWIDTH":
			hv.Bitrate = misc.Atoi(value)
		case "AUDIO":
			hv.AudioGroup = value
		case "SUBS":
			hv.SubsGroup = value
		case "PROGRAM-ID":
			hv.Program = misc.Atoi(value)
		case "CODECS":
			if !strings.Contains(value, "avc1") {
				hv.AudioOnly = true
			}
			for _, c := range avcH264Codecs {
				if strings.Contains(value, c.name) {
					hv.H264Profile = c.profile
					hv.H264Level = c.level
					break
				}
			}
		case "RESOLUTION":
			if _, after, ok := strings.Cut(value, "x"); ok {
				hv.Width = misc.Atoi(value)
				hv.Height = misc.Atoi(after)
			}
		}
	}
}
