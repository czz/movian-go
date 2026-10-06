//go:build !windows

/*
 *  Copyright (C) 2007-2015 Lonelycoder AB
 *  Canonical 1:1 Go port of src/backend/dvd/dvd.c
 */

package dvd

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"github.com/czz/movian-go/internal/backend/dvd/dvdlib"
	"github.com/czz/movian-go/internal/event"
	"github.com/czz/movian-go/internal/libav"
	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/misc"
	"github.com/czz/movian-go/internal/nls"
	"github.com/czz/movian-go/internal/notifications"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/trace"
	"github.com/czz/movian-go/internal/usage"
)

const (
	packStartCode         = 0x000001ba
	systemHeaderStartCode = 0x000001bb
	sequenceEndCode       = 0x000001b7
	packetStartCodeMask   = 0xffffff00
	packetStartCodePrefix = 0x00000100
	iso11172EndCode       = 0x000001b9
	programStreamMap      = 0x1bc
	privateStream1        = 0x1bd
	paddingStream         = 0x1be
	privateStream2        = 0x1bf
)

// getu32 — C: getu32(b, l) macro
func getu32(b *[]byte, l *int) uint32 {
	x := uint32((*b)[0])<<24 | uint32((*b)[1])<<16 |
		uint32((*b)[2])<<8 | uint32((*b)[3])
	*b = (*b)[4:]
	*l -= 4
	return x
}

// getu16 — C: getu16(b, l) macro
func getu16(b *[]byte, l *int) uint16 {
	x := uint16((*b)[0])<<8 | uint16((*b)[1])
	*b = (*b)[2:]
	*l -= 2
	return x
}

// getu8 — C: getu8(b, l) macro
func getu8(b *[]byte, l *int) uint8 {
	x := (*b)[0]
	*b = (*b)[1:]
	*l--
	return x
}

// getpts — C: getpts(b, l) macro
func getpts(b *[]byte, l *int) int64 {
	pts := int64((getu8(b, l)>>1)&0x07) << 30
	pts |= int64(getu16(b, l)>>1) << 15
	pts |= int64(getu16(b, l) >> 1)
	return pts
}

// mpegTc — C: const static AVRational mpeg_tc = {1, 90000}
var mpegTc = libav.AVRational{Num: 1, Den: 90000}

// avTimeBaseQ — C: AV_TIME_BASE_Q
var avTimeBaseQ = libav.AVRational{Num: 1, Den: 1000000}

// C: the svfs_ops faops vtable lives in the dvdlib package (svfs_ops
// trampolines → Go fa_* bridge); passed to dvdnav_open via DvdnavOpen(vfs).

// dvdPlayer — C: typedef struct dvd_player
type dvdPlayer struct {
	dvdnav *dvdlib.Dvdnav
	mp     *mediacore.MediaPipe
	pm     *propcore.PropManager

	audioTrack   int // C: dp_audio_track
	audioTrackVM int // C: dp_audio_track_vm

	spuTrack   int // C: dp_spu_track
	spuTrackVM int // C: dp_spu_track_vm

	aspectOverride int // C: dp_aspect_override

	video *mediacore.MediaCodec // C: dp_video
	audio *mediacore.MediaCodec // C: dp_audio
	spu   *mediacore.MediaCodec // C: dp_spu

	buf [dvdlib.DvdVideoLbLen]byte // C: dp_buf

	pci dvdlib.PCI // C: dp_pci

	endPtm int // C: dp_end_ptm

	hold int // C: dp_hold

	audioProps [8]*propcore.Prop  // C: dp_audio_props
	spuProps   [32]*propcore.Prop // C: dp_spu_props

	vwidth  int // C: dp_vwidth
	vheight int // C: dp_vheight
}

const (
	dpAudioDisable  = -1 // C: DP_AUDIO_DISABLE
	dpAudioFollowVM = -2 // C: DP_AUDIO_FOLLOW_VM
	dpSpuDisable    = -1 // C: DP_SPU_DISABLE
	dpSpuFollowVM   = -2 // C: DP_SPU_FOLLOW_VM
)

// dvdInMenu — C: dvd_in_menu(dp)
func dvdInMenu(dp *dvdPlayer) int {
	return dp.pci.HliSS()
}

// C: static event_t *dvd_process_event(dvd_player_t *dp, event_t *e);
// (forward declaration — defined below)

// dvdReleaseCodecs — C: dvd_release_codecs
func dvdReleaseCodecs(dp *dvdPlayer) {
	if dp.video != nil {
		mediacore.MediaCodecDeref(dp.video)
		dp.video = nil
	}
	if dp.audio != nil {
		mediacore.MediaCodecDeref(dp.audio)
		dp.audio = nil
	}
	if dp.spu != nil {
		mediacore.MediaCodecDeref(dp.spu)
		dp.spu = nil
	}
}

// dvdVideoPush — C: dvd_video_push
func dvdVideoPush(dp *dvdPlayer) {
	cw := dp.video
	mp := dp.mp

	if cw == nil {
		return
	}

	ctx := cw.FmtCtx

	mb := mediacore.MediaBufAllocUnlocked(mp, 0)
	mb.Codec = mediacore.MediaCodecRef(cw)
	mb.AspectOverride = dp.aspectOverride
	mb.DisableDeinterlacer = true
	mb.DataType = int(mediacore.MBVideo)
	// C: ctx->ticks_per_frame * 1000000LL * av_q2d(ctx->time_base)
	// The bundled FFmpeg has no ticks_per_frame (removed in FFmpeg 5);
	// ctx->framerate carries the same frame-period information.
	mb.Duration = int64(codecFramePeriodUs(ctx))
	mb.PTS = mediacore.PTSUnset
	mb.DTS = mediacore.PTSUnset

	mediacore.MbEnqueueAlways(mp, mp.Video, mb)
}

// codecFramePeriodUs — C: ctx->ticks_per_frame * 1000000LL *
// av_q2d(ctx->time_base). FFmpeg ≥5 removed ticks_per_frame; the equivalent
// frame period is av_q2d(av_inv_q(ctx->framerate)), falling back to
// av_q2d(ctx->time_base) (ticks_per_frame == 1) when unset.
func codecFramePeriodUs(ctx *libav.AVCodecContext) float64 {
	fr := libav.AvCodecCtxFramerate(ctx.CPtr())
	if fr.Num > 0 && fr.Den > 0 {
		return float64(fr.Den) / float64(fr.Num) * 1000000.0
	}
	return libav.AvQ2d(libav.AvCodecCtxTimeBase(ctx.CPtr())) * 1000000.0
}

// dvdMediaEnqueue0 — C: dvd_media_enqueue0
func dvdMediaEnqueue0(dp *dvdPlayer, mq *mediacore.MediaQueue,
	mb *mediacore.MediaBuf, dts, pts int64) *mediacore.MediaEvent {
	var e *mediacore.MediaEvent

	mb.DisableDeinterlacer = true
	mb.DTS = dts
	mb.PTS = pts

	if mb.DataType == int(mediacore.MBVideo) {
		mb.UserTime = libav.AvRescaleQ(dp.dvdnav.GetCurrentTime(),
			mpegTc, avTimeBaseQ)
		mb.DriveClock = 1
	}

	for {
		e = mediacore.MbEnqueueWithEvents(dp.mp, mq, mb)
		if e == nil {
			mb = nil
			break
		}

		e = dvdProcessEvent(dp, e)
		if e != nil {
			break
		}
	}

	if mb != nil {
		mediacore.MediaBufFreeUnlocked(dp.mp, mb)
	}

	return e
}

// dvdMediaEnqueue — C: dvd_media_enqueue
func dvdMediaEnqueue(dp *dvdPlayer, mq *mediacore.MediaQueue,
	cw *mediacore.MediaCodec, dataType int, data []byte,
	dts, pts int64) *mediacore.MediaEvent {
	mb := mediacore.MediaBufAllocUnlocked(dp.mp, len(data))

	ctx := cw.FmtCtx

	mb.Codec = mediacore.MediaCodecRef(cw)
	mb.DataType = dataType
	// C: 2000000LL * av_q2d(ctx->time_base)
	mb.Duration = int64(2000000.0 *
		libav.AvQ2d(libav.AvCodecCtxTimeBase(ctx.CPtr())))
	mb.AspectOverride = dp.aspectOverride
	copy(mb.Data, data)
	return dvdMediaEnqueue0(dp, mq, mb, dts, pts)
}

// lpcmFreqTab — C: static const int lpcm_freq_tab[4]
var lpcmFreqTab = [4]int{48000, 96000, 44100, 32000}

// dvdLpcm — C: dvd_lpcm
func dvdLpcm(dp *dvdPlayer, buf []byte, dts, pts int64) *mediacore.MediaEvent {
	if len(buf) < 3 {
		return nil
	}

	channels := 1 + int(buf[1]&0x7)
	freq := lpcmFreqTab[(buf[1]>>4)&3]
	bps := 16 + int((buf[1]>>6)&3)*3

	if bps != 16 {
		return nil
	}

	buf = buf[3:]
	length := len(buf)

	frames := length / channels / (bps >> 3)
	mb := mediacore.MediaBufAllocUnlocked(dp.mp, length)

	mb.DataType = int(mediacore.MBAudio)
	mb.Duration = int64(1000000 * frames / freq)
	mb.Channels = uint8(channels)
	mb.Rate = freq

	// C: little-endian host — byteswap each 16-bit sample
	src := buf
	for i := range length / 2 {
		mb.Data[i*2] = src[1]
		mb.Data[i*2+1] = src[0]
		src = src[2:]
	}
	return dvdMediaEnqueue0(dp, dp.mp.Audio, mb, dts, pts)
}

// dvdPes — C: dvd_pes
func dvdPes(dp *dvdPlayer, sc uint32, buf []byte, length int) *mediacore.MediaEvent {
	mp := dp.mp
	var mq *mediacore.MediaQueue
	var flags, hlen, x uint8
	dts, pts := mediacore.PTSUnset, mediacore.PTSUnset
	var dataType int
	var track int
	var cw *mediacore.MediaCodec
	var cwp **mediacore.MediaCodec
	var codecID mediacore.CodecID
	var e *mediacore.MediaEvent
	mcp := &mediacore.MediaCodecParams{}

	x = getu8(&buf, &length)
	flags = getu8(&buf, &length)
	hlen = getu8(&buf, &length)

	if length < int(hlen) {
		return nil
	}

	if (x & 0xc0) != 0x80 {
		// no MPEG 2 PES
		return nil
	}

	if (flags & 0xc0) == 0xc0 {
		if hlen < 10 {
			return nil
		}

		pts = getpts(&buf, &length)
		dts = getpts(&buf, &length)

		hlen -= 10
	} else if (flags & 0xc0) == 0x80 {
		if hlen < 5 {
			return nil
		}

		pts = getpts(&buf, &length)
		dts = pts
		hlen -= 5
	}

	buf = buf[hlen:]
	length -= int(hlen)

	if sc == privateStream1 {
		if length < 1 {
			return nil
		}

		sc = uint32(getu8(&buf, &length))
		if sc >= 0x80 && sc <= 0xbf {
			// audio, skip header
			if length < 3 {
				return nil
			}
			buf = buf[3:]
			length -= 3
		}
	}

	if sc > 0x1ff {
		return nil
	}

	if dts != mediacore.PTSUnset {
		dts = libav.AvRescaleQ(dts, mpegTc, avTimeBaseQ)
	}

	if pts != mediacore.PTSUnset {
		pts = libav.AvRescaleQ(pts, mpegTc, avTimeBaseQ)
	}

	if sc >= 0x1e0 && sc <= 0x1ef {
		codecID = mediacore.CodecIDMPEG2Video
		dataType = int(mediacore.MBVideo)
		cwp = &dp.video
		mq = mp.Video

		mcp.Width = dp.vwidth
		mcp.Height = dp.vheight

	} else if (sc >= 0x80 && sc <= 0xaf) || (sc >= 0x1c0 && sc <= 0x1df) {

		if dp.audioTrack == dpAudioDisable {
			return nil
		}

		if dp.audioTrack == dpAudioFollowVM {
			track = dp.audioTrackVM
		} else {
			track = dp.audioTrack
		}

		if (int(sc) & 7) != track {
			return nil
		}

		dataType = int(mediacore.MBAudio)

		switch {
		case sc >= 0x80 && sc <= 0x87:
			codecID = mediacore.CodecIDAC3

		case sc >= 0x88 && sc <= 0x9f:
			codecID = mediacore.CodecIDDTS

		case sc >= 0xa0 && sc <= 0xaf:
			return dvdLpcm(dp, buf, dts, pts)

		case sc >= 0x1c0 && sc <= 0x1df:
			codecID = mediacore.CodecIDMP2

		default:
			return nil
		}
		cwp = &dp.audio
		mq = mp.Audio

	} else if sc >= 0x20 && sc <= 0x3f {

		if dp.spuTrack == dpSpuDisable {
			return nil
		}

		if dp.spuTrack == dpSpuFollowVM {
			track = dp.spuTrackVM
		} else {
			track = dp.spuTrack
		}

		if (int(sc) & 31) != track {
			return nil
		}

		codecID = mediacore.CodecIDDVDSubtitle
		dataType = int(mediacore.MBDVDSPU)

		cwp = &dp.spu
		mq = mp.Video

	} else {
		return nil
	}

	cw = *cwp

	if cw == nil || cw.CodecID != codecID {
		if cw != nil {
			mediacore.MediaCodecDeref(cw)
		}

		cw = mediacore.MediaCodecCreate(codecID, 1, nil, nil, mcp, mp)
		*cwp = cw
		if cw == nil {
			return nil
		}
	}

	ctx := cw.FmtCtx

	if cw.ParserCtx == nil {
		// No parser available
		return dvdMediaEnqueue(dp, mq, cw, dataType, buf, dts, pts)
	}

	for length > 0 {
		out, rlen := cw.ParserCtx.Parse2(ctx, buf,
			pts, dts)
		if len(out) > 0 {
			e = dvdMediaEnqueue(dp, mq, cw, dataType, out,
				cw.ParserCtx.Dts(),
				cw.ParserCtx.Pts())
			if e != nil {
				return e
			}
		}
		pts = mediacore.PTSUnset
		dts = mediacore.PTSUnset
		buf = buf[rlen:]
		length -= rlen
	}
	return nil
}

// dvdBlock — C: dvd_block
func dvdBlock(dp *dvdPlayer, buf []byte, length int) *mediacore.MediaEvent {
	var startcode uint32
	var pesLen uint16
	var e *mediacore.MediaEvent

	if buf[13]&7 != 0 {
		return nil // Stuffing is not supported
	}

	buf = buf[14:]
	length -= 14

	for length > 0 {

		if length < 4 {
			break
		}

		startcode = getu32(&buf, &length)
		pesLen = getu16(&buf, &length)

		if pesLen < 3 {
			break
		}

		switch {
		case startcode == paddingStream,
			startcode == privateStream1,
			startcode == privateStream2,
			startcode >= 0x1c0 && startcode <= 0x1df,
			startcode >= 0x1e0 && startcode <= 0x1ef:
			e = dvdPes(dp, startcode, buf, int(pesLen))
			if e != nil {
				return e
			}
			length -= int(pesLen)
			buf = buf[pesLen:]
		default:
		}
	}
	return nil
}

// dvdSetupStreams — C: dvd_init_streams
func dvdSetupStreams(dp *dvdPlayer, mp *mediacore.MediaPipe) {
	pm := dp.pm

	pm.DestroyChilds(mp.PropAudioTracks)

	mediacore.MpAddTrack(pm, mp.PropAudioTracks, "Auto", "audio:auto",
		"", "", "", "", nls.GetProp("DVD"), 50, 1)

	pm.DestroyChilds(mp.PropSubtitleTracks)

	mediacore.MpAddTrackOff(pm, mp.PropSubtitleTracks, "sub:off")
	mediacore.MpAddTrack(pm, mp.PropSubtitleTracks, "Auto", "sub:auto",
		"", "", "", "", nls.GetProp("DVD"), 50, 1)
}

// dvdSetAudioStream — C: dvd_set_audio_stream
func dvdSetAudioStream(dp *dvdPlayer, id string, user int) {
	if id == "off" {
		dp.audioTrack = dpAudioDisable
	} else if id == "auto" {
		dp.audioTrack = dpAudioFollowVM
	} else {
		dp.audioTrack = misc.Atoi(id)
	}

	dp.pm.SetStringEx(dp.mp.PropAudioTrackCurrent, nil,
		fmt.Sprintf("audio:%s", id), propcore.StringUTF8)
	dp.pm.SetIntEx(dp.mp.PropAudioTrackCurrentManual, nil, user)
}

// dvdSetSpuStream — C: dvd_set_spu_stream
func dvdSetSpuStream(dp *dvdPlayer, id string, manual int) {
	if id == "off" {
		dp.spuTrack = dpSpuDisable
	} else if id == "auto" {
		dp.spuTrack = dpSpuFollowVM
	} else {
		dp.spuTrack = misc.Atoi(id)
	}
	dp.pm.SetStringEx(dp.mp.PropSubtitleTrackCurrent, nil,
		fmt.Sprintf("sub:%s", id), propcore.StringUTF8)
	dp.pm.SetIntEx(dp.mp.PropSubtitleTrackCurrentManual, nil, manual)
}

// dvdlang — C: dvdlang
func dvdlang(code uint16) *misc.IsolangT {
	buf := []byte{byte(code >> 8), byte(code), 0}
	return misc.IsolangFind(string(buf[:2]))
}

// dvdUpdateStreams — C: dvd_update_streams
func dvdUpdateStreams(dp *dvdPlayer) {
	var lang uint16
	var p *propcore.Prop
	mp := dp.mp
	pm := dp.pm

	for i := range 8 {

		if dp.dvdnav.GetAudioLogicalStream(i) == -1 {

			// Not present

			if dp.audioProps[i] != nil {
				pm.Destroy(dp.audioProps[i])
				dp.audioProps[i] = nil
			}

		} else {

			p = dp.audioProps[i]
			if p == nil {
				p = pm.CreateRoot("")
				dp.audioProps[i] = p
				if pm.SetParentEx(p, mp.PropAudioTracks,
					nil, "") != 0 {
					panic("prop_set_parent failed")
				}
			}

			pm.SetStringEx(pm.CreateEx(p, "url", nil, false,
				true), nil, fmt.Sprintf("audio:%d", i),
				propcore.StringUTF8)

			channels := dp.dvdnav.AudioStreamChannels(i)

			var chtxt string
			switch channels {
			case 1:
				chtxt = "Mono"
			case 2:
				chtxt = "Stereo"
			case 6:
				chtxt = "5.1"
			default:
				chtxt = ""
			}

			if chtxt != "" {
				pm.SetVEx(nil, p, "title", chtxt)
			} else {
				pm.SetVEx(nil, p, "title", nil)
			}

			var format string
			switch dp.dvdnav.AudioStreamFormat(i) {
			case dvdlib.DvdAudioFormatAC3:
				format = "AC3"
			case dvdlib.DvdAudioFormatMPEG:
				format = "MPEG"
			case dvdlib.DvdAudioFormatLPCM:
				format = "PCM"
			case dvdlib.DvdAudioFormatDTS:
				format = "DTS"
			case dvdlib.DvdAudioFormatSDDS:
				format = "SDDS"
			default:
				format = "???"
			}

			pm.SetStringEx(pm.CreateEx(p, "format", nil, false,
				true), nil, format, propcore.StringUTF8)

			if l := dp.dvdnav.AudioStreamToLang(i); l != 0xffff {
				lang = uint16(l)

				if il := dvdlang(lang); il != nil {
					pm.SetVEx(nil, p, "language", il.Fullname)
					pm.SetVEx(nil, p, "isolang", il.Iso639_2)
				}
			}
		}
	}

	for i := range 32 {

		if dp.dvdnav.GetSpuLogicalStream(i) == -1 {

			// Not present

			if dp.spuProps[i] != nil {
				pm.Destroy(dp.spuProps[i])
				dp.spuProps[i] = nil
			}

		} else {

			p = dp.spuProps[i]
			if p == nil {
				p = pm.CreateRoot("")
				dp.spuProps[i] = p
				if pm.SetParentEx(p, mp.PropSubtitleTracks,
					nil, "") != 0 {
					panic("prop_set_parent failed")
				}
			}

			pm.Link(nls.GetProp("DVD"), pm.CreateEx(p, "source",
				nil, false, true), nil, false, false)

			if l := dp.dvdnav.SpuStreamToLang(i); l != 0xffff {
				lang = uint16(l)
				// C bug preserved: prop_set(..., PROP_SET_STRING,
				// dvdlang(lang)) passes isolang_t* as a char*;
				// iso639_2 is the first field so C reads the
				// 3-letter code.
				if il := dvdlang(lang); il != nil {
					pm.SetVEx(nil, p, "language", il.Iso639_2)
				}
			}

			pm.SetStringEx(pm.CreateEx(p, "url", nil, false,
				true), nil, fmt.Sprintf("sub:%d", i),
				propcore.StringUTF8)
			pm.SetVEx(nil, p, "basescore", 32-i)
		}
	}
}

// updateChapter — C: update_chapter
func updateChapter(dp *dvdPlayer, mp *mediacore.MediaPipe) {
	titles := dp.dvdnav.GetNumberOfTitles()
	title, part, _ := dp.dvdnav.CurrentTitleInfo()
	parts := dp.dvdnav.GetNumberOfParts(title)

	dp.pm.SetVEx(nil, mp.PropRoot, "currenttitle", title)
	dp.pm.SetVEx(nil, mp.PropMetadata, "titles", titles)
	dp.pm.SetVEx(nil, mp.PropRoot, "currentchapter", part)
	dp.pm.SetVEx(nil, mp.PropMetadata, "chapters", parts)
}

// updateDuration — C: update_duration
func updateDuration(dp *dvdPlayer, mp *mediacore.MediaPipe) {
	title, _, _ := dp.dvdnav.CurrentTitleInfo()

	if parts, _, totdur := dp.dvdnav.DescribeTitleChapters(title); parts > 0 {
		totdur = libav.AvRescaleQ(totdur, mpegTc, avTimeBaseQ)
		dp.pm.SetVEx(nil, mp.PropMetadata, "duration",
			totdur/1000000)
	} else {
		dp.pm.SetVoidEx(dp.pm.CreateEx(mp.PropMetadata, "duration",
			nil, false, true), nil)
	}
}

// DvdPlay — C: dvd_play
func DvdPlay(u *usage.Reporter, nm *notifications.NotificationManager,
	url string, mp *mediacore.MediaPipe,
	vfs int) (*mediacore.MediaEvent, error) {
	var dp *dvdPlayer
	var result, ev, length, t int
	var block []byte
	var e *mediacore.MediaEvent
	var title string

	u.Event("Play video", 1, "format", "DVD")

	mp.FAM.TraceSystem().Trace(trace.TRACE_DEBUG, "DVD", "Starting playback of %s", url)

	pm := mp.PropRoot.Manager()
	pm.SetStringEx(pm.CreateEx(mp.PropMetadata, "format", nil, false,
		true), nil, "DVD", propcore.StringUTF8)

restart:
	dp = &dvdPlayer{}

	dp.mp = mp
	dp.pm = pm

	mp.Video.Stream = 0
	mp.Audio.Stream = 0

	var st int
	dp.dvdnav, st = dvdlib.DvdnavOpen(url, vfs != 0)
	if st != dvdlib.StatusOk {
		return nil, errors.New("dvdnav: Unable to open DVD")
	}
	dp.dvdnav.SetReadaheadFlag(1)
	dp.dvdnav.SetPGCPositioningFlag(1)

	mediacore.MpBecomePrimary(mp, pm)

	// Might wanna use deep buffering but it requires some modification
	// to buffer draining code

	mediacore.MpConfigure(mp, pm,
		mediacore.MPCanPause|mediacore.MPCanEject,
		mediacore.MPBufferShallow, 0, "dvd")

	pm.SetIntEx(mp.PropCanSkipForward, nil, 1)
	pm.SetIntEx(mp.PropCanSkipBackward, nil, 1)

	dvdSetupStreams(dp, mp)

	if ts, s := dp.dvdnav.GetTitleString(); s == dvdlib.StatusOk {
		title = ts
		s := ""

		if title != "" {
			s = makeNiceTitle(title)
		} else {
			// C: s0 = mystrdupa(url); x = strrchr(s0, '/');
			// if(x && x[1] == 0) *x = 0;  (strip trailing '/')
			s0 := url
			if idx := strings.LastIndexByte(s0, '/'); idx != -1 &&
				idx == len(s0)-1 {
				s0 = s0[:idx]
			}
			if idx := strings.LastIndexByte(s0, '/'); idx != -1 {
				s = makeNiceTitle(s0[idx+1:])
			}
		}
		pm.SetStringEx(pm.CreateEx(mp.PropMetadata, "title", nil,
			false, true), nil, s, propcore.StringUTF8)
	}

	// DVD main loop
	for e == nil {
		block, ev, length, result = dp.dvdnav.GetNextCacheBlock()
		if result == dvdlib.StatusErr {
			if nm != nil {
				nm.NotifyAdd(nil, notifications.NotifyError, "", 5,
					"%s", nls.GetRString("DVD read error, restarting disc"))
			}
			dvdReleaseCodecs(dp)
			dp.dvdnav.DvdnavClose()
			goto restart
		}

		switch ev {
		case dvdlib.BlockOK:
			e = dvdBlock(dp, block,
				length)

		case dvdlib.Nop:

		case dvdlib.SpuStreamChange:
			// C: dvdnav_spu_stream_change_event_t.physical_wide
			dp.spuTrackVM = int(binary.LittleEndian.Uint32(
				block)) & 0x1f

		case dvdlib.AudioStreamChange:
			// C: dvdnav_audio_stream_change_event_t.physical
			dp.audioTrackVM = int(binary.LittleEndian.Uint32(
				block)) & 0x7

		case dvdlib.VtsChange:
			mediacore.MpSendCmd(mp, mp.Video,
				int(mediacore.MBDVDResetSPU))
			dvdVideoPush(dp)
			dvdReleaseCodecs(dp)
			dvdSetAudioStream(dp, "auto", 0)
			dvdSetSpuStream(dp, "auto", 0)
			if dp.dvdnav.GetVideoAspect() != 0 {
				dp.aspectOverride = 2
			} else {
				dp.aspectOverride = 1
			}
			dp.vwidth, dp.vheight = dp.dvdnav.GetVideoResolution()
			mediacore.MpBumpEpoch(mp)
			dvdUpdateStreams(dp)
			updateDuration(dp, mp)

		case dvdlib.NavPacket:
			pci := dp.dvdnav.GetCurrentNavPCI()
			if pci != nil {
				if dp.endPtm != pci.VobuSPtm() {
					mediacore.MpBumpEpoch(mp)
				}
				dp.endPtm = pci.VobuEPtm()
			}

			updateChapter(dp, mp)

			mediacore.MpSendCmdData(mp, mp.Video,
				int(mediacore.MBDVDPCI), pci.PCIBytes())

		case dvdlib.Highlight:
			// C: dvdnav_highlight_event_t.buttonN (offset 20)
			buttonN := binary.LittleEndian.Uint32(
				block[20:])
			mediacore.MpSendCmdU32(mp, mp.Video,
				int(mediacore.MBCtrlDVDHilite), buttonN)

		case dvdlib.CellChange:
			mediacore.MpSendCmd(mp, mp.Video,
				int(mediacore.MBDVDResetSPU))

		case dvdlib.SpuClutChange:
			data := make([]byte, 64)
			copy(data, block)
			mediacore.MpSendCmdData(mp, mp.Video,
				int(mediacore.MBDVDCLUT), data)

		case dvdlib.Wait:
			e = mediacore.MpWaitForEmptyQueues(mp)
			if e == nil {
				dp.dvdnav.WaitSkip()
			} else {
				e = dvdProcessEvent(dp, e)
			}

		case dvdlib.HopChannel:

		case dvdlib.StillFrame:
			dvdVideoPush(dp)
			t = int(binary.LittleEndian.Uint32(
				block))

			if t == 255 {
				e = mediacore.MpDequeueEvent(mp)
			} else {
				e = mediacore.MpDequeueEventDeadline(mp, t*1000)
				if e == nil {
					dp.dvdnav.StillSkip()
					dp.dvdnav.FreeCacheBlock(block)
					continue
				}
			}

			e = dvdProcessEvent(dp, e)

		default:
			fmt.Printf("Unknown event %d\n", ev)
			panic("unknown dvdnav event")
		}
		dp.dvdnav.FreeCacheBlock(block)
	}

	mediacore.MpShutdown(mp)

	dvdReleaseCodecs(dp)
	dp.dvdnav.DvdnavClose()
	return e, nil
}

// dvdProcessEvent — C: dvd_process_event
func dvdProcessEvent(dp *dvdPlayer, e *mediacore.MediaEvent) *mediacore.MediaEvent {
	pci := &dp.pci
	mp := dp.mp

	if e.Type == int(event.EVENT_EXIT) ||
		e.Type == int(event.EVENT_PLAY_URL) {
		return e
	}

	if e.Type == int(event.EVENT_SELECT_AUDIO_TRACK) {
		if est, ok := event.ConcreteOf(e.Data).(*event.EventSelectTrack); ok {
			if strings.HasPrefix(est.ID, "audio:") {
				dvdSetAudioStream(dp,
					est.ID[len("audio:"):], btoi(est.Manual))
			}
		}

	} else if e.Type == int(event.EVENT_SELECT_SUBTITLE_TRACK) {
		if est, ok := event.ConcreteOf(e.Data).(*event.EventSelectTrack); ok {
			if strings.HasPrefix(est.ID, "sub:") {
				dvdSetSpuStream(dp,
					est.ID[len("sub:"):], btoi(est.Manual))
			}
		}

	} else if meIsAction(e, event.ACTION_ACTIVATE) {

		dp.dvdnav.ButtonActivate(pci)

	} else if meIsAction(e, event.ACTION_UP) {

		dp.dvdnav.UpperButtonSelect(pci)

	} else if meIsAction(e, event.ACTION_DOWN) {

		dp.dvdnav.LowerButtonSelect(pci)

	} else if meIsAction(e, event.ACTION_LEFT) {

		dp.dvdnav.LeftButtonSelect(pci)

	} else if meIsAction(e, event.ACTION_RIGHT) {

		dp.dvdnav.RightButtonSelect(pci)

	} else if e.Type == int(event.EVENT_DVD_PCI) {

		if payload, ok := e.Data.([]byte); ok {
			dp.pci = *dvdlib.PCIFromBytes(payload)
		}

	} else if e.Type == int(event.EVENT_DVD_SELECT_BUTTON) {

		if payload, ok := e.Data.([]byte); ok && len(payload) > 0 {
			dp.dvdnav.ButtonSelect(pci, int(payload[0]))
		}

	} else if e.Type == int(event.EVENT_DVD_ACTIVATE_BUTTON) {

		if payload, ok := e.Data.([]byte); ok && len(payload) > 0 {
			dp.dvdnav.ButtonSelectAndActivate(pci, int(payload[0]))
		}

	} else if meIsAction(e, event.ACTION_SKIP_BACKWARD) {

		mediacore.MpFlush(mp)
		dp.dvdnav.PrevPgSearch()

	} else if meIsAction(e, event.ACTION_SKIP_FORWARD) {

		mediacore.MpFlush(mp)
		dp.dvdnav.NextPgSearch()

	} else if meIsAction(e, event.ACTION_EJECT) {
		return e
	}

	// C: event_release(e) — Go GC handles it
	return nil
}

// makeNiceTitle — C: make_nice_title
func makeNiceTitle(t string) string {
	ret := make([]byte, 0, len(t))
	uc := true

	for i := range len(t) {
		c := t[i]
		if c == '_' || c == ' ' {
			ret = append(ret, ' ')
			uc = true
		} else if uc {
			ret = append(ret, toUpperByte(c))
			uc = false
		} else {
			ret = append(ret, toLowerByte(c))
		}
	}
	return string(ret)
}

// toUpperByte — C: toupper (ASCII)
func toUpperByte(c byte) byte {
	if c >= 'a' && c <= 'z' {
		return c - 32
	}
	return c
}

// toLowerByte — C: tolower (ASCII)
func toLowerByte(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 32
	}
	return c
}

// meIsAction — C: event_is_action (htsp.go precedent)
func meIsAction(e *mediacore.MediaEvent, at event.ActionType) bool {
	if e == nil {
		return false
	}
	if c, ok := e.Data.(interface{ IsAction(event.ActionType) bool }); ok {
		return c.IsAction(at)
	}
	return false
}

// btoi — bool → int (C int semantics)
func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
