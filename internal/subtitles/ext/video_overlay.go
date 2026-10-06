package ext

// Canonical port of the text_parse-coupled half of src/subtitles/video_overlay.c
// (video_overlay_render_cleartext, calculate_subtitle_duration,
// video_overlay_decode). The mp-coupled half lives in pkg/media/core —
// see the package comment in sub_ass.go for the layering rationale.

import (
	mediacore "github.com/czz/movian-go/internal/media/core"
	medialibav "github.com/czz/movian-go/internal/media/libav"
	"github.com/czz/movian-go/internal/subtitles"
	"github.com/czz/movian-go/internal/text"
)

// VideoOverlayRenderCleartext — C: video_overlay_render_cleartext
func VideoOverlayRenderCleartext(sys *subtitles.System, txt string, start, stop int64,
	tags int, fontdomain int) *mediacore.VideoOverlay {

	txtLen := len(txt)

	vo := &mediacore.VideoOverlay{}

	if txtLen != 0 {
		// C: uint32_t pfx[6] — subtitle_settings prefix codes
		pfx := []uint32{
			text.TR_CODE_COLOR | uint32(sys.SubSettings().Color()),
			text.TR_CODE_SHADOW | uint32(sys.SubSettings().ShadowDisplacement()),
			text.TR_CODE_SHADOW_COLOR | uint32(sys.SubSettings().ShadowColor()),
			text.TR_CODE_OUTLINE | uint32(sys.SubSettings().OutlineSize()),
			text.TR_CODE_OUTLINE_COLOR | uint32(sys.SubSettings().OutlineColor()),
		}

		// C: if(font_subs[0]) pfx[pfxlen++] = TR_CODE_FONT_FAMILY |
		//     freetype_family_id(font_subs, fontdomain);
		if ts := sys.TextSys; ts != nil && ts.FontSubs() != "" {
			pfx = append(pfx, text.TR_CODE_FONT_FAMILY|
				uint32(ts.FreetypeFamilyId(ts.FontSubs(), fontdomain)))
		}

		uc, ln := sys.TextSys.TextParse(txt, tags, pfx, fontdomain)
		if uc == nil {
			return nil
		}

		vo.Type = mediacore.VOText
		vo.Text = uc
		vo.TextLength = ln
		vo.PaddingLeft = -1 // auto padding
	}

	if stop == mediacore.PTSUnset {
		stop = start + int64(CalculateSubtitleDuration(txtLen))*1000000
		vo.StopEstimated = true
	}

	vo.Start = start
	vo.Stop = stop
	return vo
}

// CalculateSubtitleDuration — C: calculate_subtitle_duration
// Min 2 seconds, max ~7 seconds (74 chars ≈ 2 lines).
func CalculateSubtitleDuration(txtLen int) int {
	return 2 + int((float64(txtLen)/74.0)*5)
}

// mbFontContext — C: mb->mb_font_context (union member; int fontdomain)
func mbFontContext(mb *mediacore.MediaBuf) int {
	if v, ok := mb.FontContext.(int); ok {
		return v
	}
	return 0
}

// VideoOverlayDecode — C: video_overlay_decode
func VideoOverlayDecode(mp *mediacore.MediaPipe, mb *mediacore.MediaBuf) {
	mc := mb.Codec

	if mc == nil {
		offset := 0
		if mb.CodecID == mediacore.CodecIDMOVText {
			if mb.Size < 2 {
				return
			}
			offset = 2
		}
		if mb.Size-offset < 0 {
			return
		}
		str := string(mb.Data[offset : offset+mb.Size-offset])
		// C copies to a NUL-terminated malloc'd buffer; Go string is fine.

		var stop int64
		if mb.Duration != 0 {
			stop = mb.PTS + mb.Duration
		} else {
			stop = mediacore.PTSUnset
		}

		vo := VideoOverlayRenderCleartext(subtitles.SystemOf(mp), str, mb.PTS, stop,
			text.TEXT_PARSE_HTML_TAGS|
				text.TEXT_PARSE_HTML_ENTITIES|
				text.TEXT_PARSE_SLOPPY_TAGS,
			mbFontContext(mb))
		if vo != nil {
			mediacore.VideoOverlayEnqueue(mp, vo)
		}
		return
	}

	if mc.Decode != nil {
		// C: mc->decode(mc, NULL, NULL, mb, 0)
		mc.Decode(mc, nil, nil, mb, 0)
	} else if mc.Ctx != nil {
		// C: video_subtitles_lavc(mp, mb, mc->ctx) under ENABLE_LIBAV
		medialibav.VideoSubtitlesLavc(mp, mb, mc.Ctx)
	}
}
