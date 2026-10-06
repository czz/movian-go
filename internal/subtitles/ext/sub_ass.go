// Package ext is the canonical port of src/subtitles/{video_overlay.c,
// sub_ass.c, ext_subtitles.c} — the parts that need media_pipe_t plus
// text_parse/fileaccess.
//
// Go layering note: in C these files both use and are used by media_pipe_t.
// Go requires an acyclic import graph, so the VideoOverlay type and the
// mp-coupled queue ops live in pkg/media/core while everything needing
// pkg/text lives here (ext → {mediacore, subtitles, text, fileaccess}).
// ext's RegisterMediaHooks installs MediaSystem.SubtitlesLoad/SubtitlesDestroy,
// which is how mp_load_ext_sub (media_track.c) reaches subtitles_load.
package ext

import (
	"slices"
	"strings"

	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/misc"
	"github.com/czz/movian-go/internal/subtitles"
	"github.com/czz/movian-go/internal/text"
)

// ---------------------------------------------------------------------------
// sub_ass.c
// ---------------------------------------------------------------------------

// assGetTS — C: ass_get_ts
func assGetTS(buf string) int64 {
	if len(buf) != 10 {
		return mediacore.PTSUnset
	}
	return 1000 * (int64(buf[0]-'0')*3600000 +
		int64(buf[2]-'0')*600000 +
		int64(buf[3]-'0')*60000 +
		int64(buf[5]-'0')*10000 +
		int64(buf[6]-'0')*1000 +
		int64(buf[8]-'0')*100 +
		int64(buf[9]-'0')*10)
}

// textAppend — C: text_append (TEXT_APPEND_STEP handled by Go slices)
func textAppend(ptr []uint32, uc uint32) []uint32 {
	return append(ptr, uc)
}

// assStyle — C: ass_style_t
type assStyle struct {
	name           string
	fontname       string
	primaryColor   uint32
	secondaryColor uint32
	outlineColor   uint32
	backColor      uint32

	fontsize    int
	bold        int
	italic      int
	underline   int
	strikeout   int
	scalex      int
	scaley      int
	spacing     int
	angle       int
	borderStyle uint
	outline     uint
	shadow      uint
	alignment   uint

	marginLeft     int
	marginRight    int
	marginVertical int

	encoding int
}

// assStyleDefault — C: ass_style_default
var assStyleDefault = assStyle{
	primaryColor:   0xffffff,
	outlineColor:   0x000000,
	shadow:         1,
	outline:        1,
	bold:           1,
	alignment:      1,
	marginLeft:     20,
	marginRight:    20,
	marginVertical: 20,
	fontsize:       48,
}

// C: enum adc_section
const (
	adcSectionNone = iota
	adcSectionScriptInfo
	adcSectionV4Styles
	adcSectionEvents
)

// assDecoderCtx — C: ass_decoder_ctx_t
type assDecoderCtx struct {
	section         int
	styles          []*assStyle // C: LIST_HEAD, INSERT_HEAD order
	styleFormat     string
	eventFormat     string
	resx            int
	resy            int
	opaque          any
	dialogueHandler func(adc *assDecoderCtx, s string)
	sys             *subtitles.System // C: subtitles.c statics
	shadow          uint32
	outline         uint32
}

// adcSetup — C: adc_init
func adcSetup(adc *assDecoderCtx) {
	adc.shadow = text.TR_CODE_SHADOW_US
	adc.outline = text.TR_CODE_OUTLINE_US
}

// adcCleanup — C: adc_cleanup (GC reclaims; clear references)
func adcCleanup(adc *assDecoderCtx) {
	adc.styles = nil
	adc.eventFormat = ""
}

// getToken — C: gettoken (comma-separated field, trims spaces, 127-char cap)
func getToken(src *string) string {
	s := *src
	for len(s) > 0 && s[0] == 32 {
		s = s[1:]
	}
	var b strings.Builder
	for len(s) > 0 && s[0] != 0 && s[0] != ',' && b.Len() < 127 {
		b.WriteByte(s[0])
		s = s[1:]
	}
	if len(s) > 0 && s[0] == ',' {
		s = s[1:]
	}
	*src = s
	return strings.TrimRight(b.String(), " ")
}

// assParseColor — C: ass_parse_color
func assParseColor(str string) uint32 {
	l := 0
	var rgba uint32
	if len(str) > 0 && str[0] == '&' {
		str = str[1:]
	}
	if len(str) > 0 && (str[0] == 'h' || str[0] == 'H') {
		str = str[1:]
	}
	for l < len(str) && misc.Hexnibble(str[l]) != -1 {
		l++
	}
	if l > 8 {
		l = 8
	}
	for i := l; i >= 1; i-- {
		rgba |= uint32(misc.Hexnibble(str[l-i])) << uint32((i-1)*4)
	}
	return rgba
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func minUint(a, b uint) uint {
	if a < b {
		return a
	}
	return b
}

// assParseV4Style — C: ass_parse_v4style
func assParseV4Style(adc *assDecoderCtx, str string) {
	fmtStr := adc.styleFormat
	if fmtStr == "" {
		return
	}

	as := &assStyle{}
	as.primaryColor = 0x00ffffff
	as.outlineColor = 0x00000000

	for len(fmtStr) > 0 && len(str) > 0 {
		key := getToken(&fmtStr)
		val := getToken(&str)

		switch strings.ToLower(key) {
		case "name":
			as.name = val
		case "alignment":
			as.alignment = uint(misc.Atoi(val))
			if as.alignment < 1 || as.alignment > 9 {
				as.alignment = 1
			}
		case "marginl":
			as.marginLeft = misc.Atoi(val)
		case "marginr":
			as.marginRight = misc.Atoi(val)
		case "marginv":
			as.marginVertical = misc.Atoi(val)
		case "bold":
			as.bold = btoi(misc.Atoi(val) != 0)
		case "italic":
			as.italic = btoi(misc.Atoi(val) != 0)
		case "primarycolour":
			as.primaryColor = assParseColor(val)
		case "secondarycolour":
			as.secondaryColor = assParseColor(val)
		case "outlinecolour":
			as.outlineColor = assParseColor(val)
		case "backcolour":
			as.backColor = assParseColor(val)
		case "outline":
			as.outline = minUint(uint(misc.Atoi(val)), 4)
		case "shadow":
			as.shadow = minUint(uint(misc.Atoi(val)), 4)
		case "fontsize":
			as.fontsize = misc.Atoi(val)
		case "fontname":
			as.fontname = val
		case "encoding":
			as.encoding = misc.Atoi(val)
		}
	}
	// C: LIST_INSERT_HEAD(&adc->adc_styles, as, as_link)
	adc.styles = slices.Insert(adc.styles, 0, as)
}

// myStrBegins — C: mystrbegins (remainder + found flag)
func myStrBegins(str, prefix string) (string, bool) {
	if strings.HasPrefix(str, prefix) {
		return str[len(prefix):], true
	}
	return "", false
}

// assDecodeLine — C: ass_decode_line
func assDecodeLine(adc *assDecoderCtx, str string) int {
	if str == "[Script Info]" {
		adc.section = adcSectionScriptInfo
		return 0
	}
	if str == "[V4+ Styles]" {
		adc.section = adcSectionV4Styles
		return 0
	}
	if str == "[Events]" {
		adc.section = adcSectionEvents
		return 0
	}
	if len(str) > 0 && str[0] == '[' {
		// Unknown section, better stay out of it
		adc.section = adcSectionNone
		return 0
	}

	switch adc.section {
	case adcSectionScriptInfo:
		if s, ok := myStrBegins(str, "PlayResX:"); ok {
			adc.resx = misc.Atoi(s)
			break
		}
		if s, ok := myStrBegins(str, "PlayResY:"); ok {
			adc.resy = misc.Atoi(s)
			break
		}
		if s, ok := myStrBegins(str, "ScaledBorderAndShadow:"); ok {
			if misc.Atoi(s) > 0 || strings.EqualFold(strings.TrimSpace(s), "yes") {
				// C sets adc_shadow twice (verbatim — likely a C typo
				// for outline; kept for parity)
				adc.shadow = text.TR_CODE_SHADOW_US
				adc.shadow = text.TR_CODE_SHADOW_US
			}
			break
		}

	case adcSectionV4Styles:
		if s, ok := myStrBegins(str, "Format:"); ok {
			adc.styleFormat = s
			break
		}
		if s, ok := myStrBegins(str, "Style:"); ok {
			assParseV4Style(adc, s)
			break
		}

	case adcSectionEvents:
		if s, ok := myStrBegins(str, "Format:"); ok {
			adc.eventFormat = s
			break
		}
		if s, ok := myStrBegins(str, "Dialogue:"); ok {
			if adc.dialogueHandler == nil {
				return 1
			}
			adc.dialogueHandler(adc, s)
			break
		}
	}
	return 0
}

// assDecodeLines — C: ass_decode_lines
func assDecodeLines(adc *assDecoderCtx, s string) {
	i := 0
	for i < len(s) {
		l := 0
		for i+l < len(s) && s[i+l] != '\r' && s[i+l] != '\n' {
			l++
		}
		line := s[i : i+l]
		i += l
		for i < len(s) && (s[i] == '\r' || s[i] == '\n') {
			i++
		}
		if assDecodeLine(adc, line) != 0 {
			break
		}
	}
}

// adcFindStyle — C: adc_find_style
func adcFindStyle(adc *assDecoderCtx, name string) *assStyle {
	if len(name) > 0 && name[0] == '*' {
		name = name[1:]
	}
	for _, as := range adc.styles {
		if strings.EqualFold(as.name, name) {
			return as
		}
	}
	return &assStyleDefault
}

// assDialogue — C: ass_dialoge_t
type assDialogue struct {
	text []uint32 // C: ad_text

	fadein  int
	fadeout int

	x int16
	y int16

	alignment   int8
	absolutePos int8

	notSupported bool
}

// adTxtAppend — C: ad_txt_append
func adTxtAppend(ad *assDialogue, v uint32) {
	ad.text = textAppend(ad.text, v)
}

// isD — C: isd
func isD(c byte) bool {
	return c >= '0' && c <= '9'
}

// sscanfPair — parses `prefix(%d,%d)` at head of s.
// C: sscanf(str, "fad(%d,%d)", &v1, &v2) / sscanf(str, "pos(%d,%d)", ...)
func sscanfPair(s string, prefix string) (int, int, bool) {
	if !strings.HasPrefix(s, prefix+"(") {
		return 0, 0, false
	}
	rest := s[len(prefix)+1:]
	v1, n1 := strtolS(rest)
	if n1 == 0 {
		return 0, 0, false
	}
	rest = rest[n1:]
	if len(rest) == 0 || rest[0] != ',' {
		return 0, 0, false
	}
	v2, n2 := strtolS(rest[1:])
	if n2 == 0 {
		return 0, 0, false
	}
	rest = rest[1+n2:]
	if len(rest) == 0 || rest[0] != ')' {
		return 0, 0, false
	}
	return v1, v2, true
}

// strtolS — C: strtol(s, &end, 10); returns value and bytes consumed
// (0 when no digits — caller checks).
func strtolS(s string) (int, int) {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' ||
		s[i] == '\r' || s[i] == '\v' || s[i] == '\f') {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	v := 0
	digits := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		v = v*10 + int(s[i]-'0')
		i++
		digits++
	}
	if digits == 0 {
		return 0, 0
	}
	if neg {
		v = -v
	}
	return v, i
}

// assHandleOverride — C: ass_handle_override
func assHandleOverride(adc *assDecoderCtx, ad *assDialogue, src string, fontdomain int) {
	if len(src) > 1000 {
		return
	}
	str := src

	for {
		idx := strings.IndexByte(str, '\\')
		if idx == -1 {
			return
		}
		str = str[idx+1:]
	next:
		if len(str) == 0 {
			// C falls through to strchr() which returns NULL → loop ends
			return
		}
		switch {
		case str[0] == 'i' && len(str) > 1 && isD(str[1]):
			if str[1] == '1' {
				adTxtAppend(ad, text.TR_CODE_ITALIC_ON)
			} else {
				adTxtAppend(ad, text.TR_CODE_ITALIC_OFF)
			}
		case str[0] == 'b' && len(str) > 1 && isD(str[1]):
			if str[1] == '1' {
				adTxtAppend(ad, text.TR_CODE_BOLD_ON)
			} else {
				adTxtAppend(ad, text.TR_CODE_BOLD_OFF)
			}
		default:
			if v1, v2, ok := sscanfPair(str, "fad"); ok {
				ad.fadein = v1 * 1000
				ad.fadeout = v2 * 1000
			} else if v1, v2, ok := sscanfPair(str, "pos"); ok {
				ad.x = int16(v1)
				ad.y = int16(v2)
				ad.absolutePos = 1
			} else if len(str) >= 4 && (str[:4] == "fscx" || str[:4] == "fscy") {
				v1 := misc.Atoi(str[4:])
				if v1 > 0 {
					adTxtAppend(ad, text.TR_CODE_SIZE_PX+uint32(v1&0xff))
				}
			} else if len(str) > 2 && str[0] == 'f' && str[1] == 's' && isD(str[2]) {
				v1 := misc.Atoi(str[2:])
				if v1 > 3 {
					adTxtAppend(ad, text.TR_CODE_SIZE_PX+uint32(v1&0xff))
				}
			} else if len(str) > 1 && ((str[0] == 'c' && str[1] == '&') ||
				(str[0] == '1' && str[1] == 'c')) {
				adTxtAppend(ad, text.TR_CODE_COLOR|assParseColor(str[2:]))
			} else if len(str) > 1 && str[0] == '3' && str[1] == 'c' {
				adTxtAppend(ad, text.TR_CODE_OUTLINE_COLOR|assParseColor(str[2:]))
			} else if len(str) > 1 && str[0] == '4' && str[1] == 'c' {
				adTxtAppend(ad, text.TR_CODE_SHADOW_COLOR|assParseColor(str[2:]))
			} else if len(str) > 1 && str[0] == 'f' && str[1] == 'n' {
				str = str[2:]
				cmd := strings.IndexByte(str, '\\')
				name := str
				if cmd != -1 {
					name = str[:cmd]
				}
				adTxtAppend(ad, text.TR_CODE_FONT_FAMILY|
					uint32(adc.sys.TextSys.FreetypeFamilyId(name, fontdomain)))
				if cmd == -1 {
					return
				}
				str = str[cmd+1:]
				goto next
			} else if len(str) > 1 && str[0] == 'a' && str[1] == 'n' {
				// Alignment
				ad.alignment = int8(misc.Atoi(str[2:]))
			} else if len(str) > 1 && str[0] == 't' && str[1] == '(' {
				// ignore Animated transform
				if j := strings.IndexByte(str, ')'); j != -1 {
					str = str[j:]
				} else {
					str = ""
				}
			} else if len(str) > 1 && str[0] == 'p' && isD(str[1]) {
				// ignore Drawing tags
				ad.notSupported = true
				return
			}
		}
	}
}

// adDialogueDecode — C: ad_dialogue_decode
func adDialogueDecode(adc *assDecoderCtx, line string,
	fontdomain int) *mediacore.VideoOverlay {
	fmtStr := adc.eventFormat
	as := &assStyleDefault
	layer := 0
	start := mediacore.PTSUnset
	end := mediacore.PTSUnset
	str := ""

	ad := &assDialogue{}

	if fmtStr == "" {
		return nil
	}

	for len(fmtStr) > 0 && len(line) > 0 && line[0] != '\n' && line[0] != '\r' {
		key := getToken(&fmtStr)
		if strings.EqualFold(key, "text") {
			// C: d = mystrdupa(line); d[strcspn(d,"\n\r")] = 0
			if i := strings.IndexAny(line, "\n\r"); i != -1 {
				str = line[:i]
			} else {
				str = line
			}
			break
		}

		val := getToken(&line)

		switch strings.ToLower(key) {
		case "layer":
			layer = misc.Atoi(val)
		case "start":
			start = assGetTS(val)
		case "end":
			end = assGetTS(val)
		case "style":
			as = adcFindStyle(adc, val)
		}
	}

	if start == mediacore.PTSUnset || end == mediacore.PTSUnset || str == "" {
		return nil
	}

	if as.bold != 0 {
		adTxtAppend(ad, text.TR_CODE_BOLD_ON)
	}
	if as.italic != 0 {
		adTxtAppend(ad, text.TR_CODE_ITALIC_ON)
	}

	if ts := adc.sys.TextSys; ts != nil && ts.FontSubs() != "" {
		adTxtAppend(ad, text.TR_CODE_FONT_FAMILY|
			uint32(ts.FreetypeFamilyId(ts.FontSubs(), fontdomain)))
	} else if as.fontname != "" {
		adTxtAppend(ad, text.TR_CODE_FONT_FAMILY|
			uint32(adc.sys.TextSys.FreetypeFamilyId(as.fontname, fontdomain)))
	}

	if as == &assStyleDefault || adc.sys.SubSettings().StyleOverride() != 0 {
		adTxtAppend(ad, text.TR_CODE_COLOR|
			uint32(adc.sys.SubSettings().Color()))
		adTxtAppend(ad, text.TR_CODE_OUTLINE_COLOR|
			uint32(adc.sys.SubSettings().OutlineColor()))
		adTxtAppend(ad, text.TR_CODE_SHADOW_COLOR|
			uint32(adc.sys.SubSettings().ShadowColor()))

		adTxtAppend(ad, adc.shadow|
			uint32(adc.sys.SubSettings().ShadowDisplacement()))
		adTxtAppend(ad, adc.outline|
			uint32(adc.sys.SubSettings().OutlineSize()))
	} else {
		var alpha uint32
		alpha = 255 - (as.primaryColor >> 24)

		adTxtAppend(ad, text.TR_CODE_SIZE_PX|uint32(as.fontsize))

		adTxtAppend(ad, text.TR_CODE_COLOR|(as.primaryColor&0xffffff))
		adTxtAppend(ad, text.TR_CODE_ALPHA|alpha)

		alpha = 255 - (as.outlineColor >> 24)
		adTxtAppend(ad, text.TR_CODE_OUTLINE_COLOR|(as.outlineColor&0xffffff))
		adTxtAppend(ad, text.TR_CODE_OUTLINE_ALPHA|alpha)

		alpha = 255 - (as.backColor >> 24)
		adTxtAppend(ad, text.TR_CODE_SHADOW_COLOR|(as.backColor&0xffffff))
		adTxtAppend(ad, text.TR_CODE_SHADOW_ALPHA|alpha)

		if as.shadow != 0 {
			adTxtAppend(ad, adc.shadow|uint32(as.shadow&0xff))
		}
		if as.outline != 0 {
			adTxtAppend(ad, adc.outline|uint32(as.outline&0xff))
		}
	}

	// C: while((c = utf8_get(&str)) != 0)
	sb := []byte(str)
	for {
		c := misc.Utf8Get(&sb)
		if c == 0 {
			break
		}
		if c == '\\' && len(sb) > 0 && (sb[0] == 'n' || sb[0] == 'N') {
			sb = sb[1:]
			adTxtAppend(ad, '\n')
			continue
		}
		if c == '\\' && len(sb) > 0 && sb[0] == 'h' {
			// hard space
			sb = sb[1:]
			adTxtAppend(ad, ' ')
			continue
		}
		if c == '{' {
			e := -1
			for i := range len(sb) {
				if sb[i] == '}' {
					e = i
					break
				}
			}
			if e == -1 {
				break
			}
			assHandleOverride(adc, ad, string(sb[:e]), fontdomain)
			if ad.notSupported {
				return nil
			}
			sb = sb[e+1:]
			continue
		}
		adTxtAppend(ad, uint32(c))
	}

	vo := &mediacore.VideoOverlay{Type: mediacore.VOText}

	vo.Text = make([]uint32, len(ad.text))
	copy(vo.Text, ad.text)
	vo.TextLength = len(ad.text)

	vo.Start = start
	vo.Stop = end
	vo.FadeIn = ad.fadein
	vo.FadeOut = ad.fadeout

	vo.X = ad.x
	vo.Y = ad.y
	vo.AbsPos = ad.absolutePos != 0

	if ad.alignment != 0 {
		vo.Alignment = int(ad.alignment)
	} else {
		vo.Alignment = int(as.alignment)
	}

	vo.PaddingLeft = int16(as.marginLeft)
	vo.PaddingRight = int16(as.marginRight)

	switch vo.Alignment {
	case misc.LAYOUT_ALIGN_TOP, misc.LAYOUT_ALIGN_TOP_LEFT,
		misc.LAYOUT_ALIGN_TOP_RIGHT:
		vo.PaddingTop = int16(as.marginVertical)
	case misc.LAYOUT_ALIGN_BOTTOM, misc.LAYOUT_ALIGN_BOTTOM_LEFT,
		misc.LAYOUT_ALIGN_BOTTOM_RIGHT:
		vo.PaddingBottom = int16(as.marginVertical)
	}

	switch {
	case adc.resx == 0 && adc.resy == 0:
		vo.CanvasWidth = 384
		vo.CanvasHeight = 288
	case (adc.resx == 1280 && adc.resy == 0) ||
		(adc.resx == 0 && adc.resy == 1024):
		vo.CanvasWidth = 1280
		vo.CanvasHeight = 1024
	case adc.resx != 0 && adc.resy != 0:
		vo.CanvasWidth = int16(adc.resx)
		vo.CanvasHeight = int16(adc.resy)
	case adc.resx != 0:
		vo.CanvasWidth = int16(adc.resx)
		vo.CanvasHeight = int16(adc.resx * 3 / 4)
	case adc.resy != 0:
		vo.CanvasWidth = int16(adc.resy * 4 / 3)
		vo.CanvasHeight = int16(adc.resy)
	}

	vo.Layer = layer
	return vo
}

// SubAssRender — C: sub_ass_render
func SubAssRender(mp *mediacore.MediaPipe, src string,
	header []byte, fontdomain int) {
	if !strings.HasPrefix(src, "Dialogue:") {
		return
	}
	src = src[len("Dialogue:"):]

	adc := &assDecoderCtx{sys: subtitles.SystemOf(mp)}
	adcSetup(adc)

	// Headers
	assDecodeLines(adc, string(header))

	// Dialogue
	vo := adDialogueDecode(adc, src, fontdomain)
	if vo != nil {
		mediacore.VideoOverlayEnqueue(mp, vo)
	}

	adcCleanup(adc)
}

// loadSSADialogue — C: load_ssa_dialogue
func loadSSADialogue(adc *assDecoderCtx, str string) {
	vo := adDialogueDecode(adc, str, 0)
	if vo == nil {
		return
	}
	es := adc.opaque.(*subtitles.ExtSubtitles)
	// C: TAILQ_INSERT_TAIL(&es->es_entries, vo, vo_link)
	es.Entries = append(es.Entries, vo)
}

// LoadSSA — C: load_ssa (declared in ext_subtitles.h)
func LoadSSA(sys *subtitles.System, url string, buf string) *subtitles.ExtSubtitles {
	es := &subtitles.ExtSubtitles{Sys: sys}
	adc := &assDecoderCtx{sys: es.Sys}

	adcSetup(adc)
	adc.dialogueHandler = loadSSADialogue
	adc.opaque = es

	assDecodeLines(adc, buf)
	adcCleanup(adc)
	return es
}
