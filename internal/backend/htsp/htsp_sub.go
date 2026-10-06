// Package htsp is a 1:1 port of src/backend/htsp/htsp.c.
//
// Every C function, struct and global maps to the Go counterpart below,
// in the same order. C references are given per function.
package htsp

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	backendcore "github.com/czz/movian-go/internal/backend/core"
	"github.com/czz/movian-go/internal/event"
	"github.com/czz/movian-go/internal/htsmsg"
	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/trace"
)

// C: set_channel (htsp.c:1598-1621)
func setChannel(hc *htspConnection, hs *htspSubscription, chid int,
	name *string) {
	pm := hc.sys.pm
	hc.metaMutex.Lock()

	if ch := htspChannelGet(hc, chid, 1); ch != nil {
		m := hs.mp.PropMetadata

		hc.sys.ts.Trace(trace.TRACE_DEBUG, "HTSP",
			"Subscribing to channel %s", ch.title)

		pm.Link(ch.propTitle,
			pm.CreateEx(m, "title", nil, false, false),
			nil, false, false)
		pm.Link(ch.propIcon,
			pm.CreateEx(m, "icon", nil, false, false),
			nil, false, false)
		pm.Link(ch.propChannelNumber,
			pm.CreateEx(m, "channelNumber", nil, false, false),
			nil, false, false)
		pm.Link(ch.propEvents,
			pm.CreateEx(m, "events", nil, false, false),
			nil, false, false)

		*name = ch.title // C: mystrset(name, ch->ch_title)
	} else {
		*name = "" // C: mystrset(name, NULL)
	}

	hc.metaMutex.Unlock()
}

// C: zap_channel (htsp.c:1626-1698)
func zapChannel(hc *htspConnection, hs *htspSubscription,
	reverse int, name *string,
	vq *backendcore.VideoQueue) (int, error) {
	pm := hc.sys.pm

	if hs.origin == nil {
		return 0, nil
	}

	next := backendcore.VideoQueueFindNext(vq, hs.origin, reverse != 0, true)
	if next == nil {
		return 0, nil
	}

	// C: rstr_t *next_url = prop_get_string(next, "url", NULL)
	nextURL := ""
	if up := next.ResolvePath([]string{"url"}, true); up != nil {
		nextURL = up.GetString()
	}
	_, after, ok := strings.Cut(nextURL, "/channel/")
	if !ok {
		pm.RefDec(next)
		return 0, nil
	}

	newch := misc.Atoi(after)

	// Stop current

	m := htsmsg.NewMap()

	m.AddStr("method", "unsubscribe")
	m.AddU32("subscriptionId", hs.sid)

	m = htspReqreply(hc, m)
	if m == nil {
		return -1, errors.New("Connection with server lost")
	}
	m.Release()

	hc.subscriptionMutex.Lock()
	hs.sid = uint32(hc.sidGenerator.Add(1))

	mediacore.MpFlush(hs.mp)
	hc.subscriptionMutex.Unlock()

	m = htsmsg.NewMap()

	m.AddStr("method", "subscribe")
	m.AddU32("channelId", uint32(newch))
	m.AddU32("subscriptionId", hs.sid)

	m = htspReqreply(hc, m)
	if m == nil {
		pm.RefDec(next)
		return -1, errors.New("Connection with server lost")
	}

	if err, ok := htsmsgGetStr(m, "error"); ok {
		serr := fmt.Errorf("From server: %s", err)
		m.Release()
		pm.RefDec(next)
		return -1, serr
	}

	pm.RefDec(hs.origin)
	hs.origin = next

	pm.SuggestFocus(hs.origin)

	m.Release()
	setChannel(hc, hs, newch, name)
	return 0, nil
}

// C: htsp_subscriber (htsp.c:1723-1903)
func htspSubscriber(hc *htspConnection, hs *htspSubscription,
	primary int, priority int,
	vq *backendcore.VideoQueue, url string) (*mediacore.MediaEvent, error) {
	pm := hc.sys.pm
	mp := hs.mp
	name := ""
	var mpFlags mediacore.MediaPipeFlags

	_, after, ok := strings.Cut(url, "/channel/")
	if !ok {
		return nil, nil
	}
	chid := misc.Atoi(after)

	m := htsmsg.NewMap()

	m.AddStr("method", "subscribe")
	m.AddU32("channelId", uint32(chid))
	m.AddU32("subscriptionId", hs.sid)
	m.AddU32("weight", uint32(prioToWeight(priority)))
	m.AddU32("timeshiftPeriod", 3600)

	m = htspReqreply(hc, m)
	if m == nil {
		return nil, errors.New("Connection with server lost")
	}

	if err, ok := htsmsgGetStr(m, "error"); ok {
		serr := fmt.Errorf("From server: %s", err)
		m.Release()
		return nil, serr
	}

	if m.GetU32OrDefault("timeshiftPeriod", 0) != 0 {
		mpFlags |= mediacore.MPCanPause
	}

	m.Release()

	pm.SetStringEx(mp.PropPlayStatus, nil, "play", propcore.StringUTF8)

	// With a set mq_stream mp_configure things that we don't use
	// audio at all which might screw up A/V sync on some platforms (rpi)
	mp.Audio.Stream = 0
	mediacore.MpConfigure(mp, pm, mpFlags, mediacore.MPBufferDeep, 0, "tv")

	if primary != 0 {
		mediacore.MpBecomePrimary(mp, pm)
	} else {
		mediacore.MpSetupAudio(mp)
	}

	setChannel(hc, hs, chid, &name)

	var e *mediacore.MediaEvent
	for {
		e = mediacore.MpDequeueEvent(mp)

		if meIsType(e, event.EVENT_SEEK) {
			ets, _ := event.ConcreteOf(e.Data).(*event.EventTs)

			hc.sys.ts.Trace(trace.TRACE_DEBUG, "HTSP", "%s: Seek to %d",
				name, ets.Ts)

			m = htsmsg.NewMap()

			m.AddStr("method", "subscriptionSkip")
			m.AddU32("subscriptionId", hs.sid)
			m.AddU32("absolute", 1)
			m.AddS64("time", ets.Ts)

			m = htspReqreply(hc, m)
			if m == nil {
				return nil, errors.New("Connection with server lost")
			}

			if err, ok := htsmsgGetStr(m, "error"); ok {
				serr := fmt.Errorf("From server: %s", err)
				m.Release()
				return nil, serr
			}

			m.Release()

		} else if mpFlags&mediacore.MPCanPause != 0 &&
			meIsType(e, event.EVENT_HOLD) {

			ei, _ := event.ConcreteOf(e.Data).(*event.EventInt)
			hold := ei.Val

			hc.sys.ts.Trace(trace.TRACE_DEBUG, "HTSP", "%s: Hold set to %d",
				name, hold)

			m = htsmsg.NewMap()

			m.AddStr("method", "subscriptionSpeed")
			m.AddU32("subscriptionId", hs.sid)
			speed := 0
			if hold == 0 {
				speed = 100 // C: 100 * !hold
			}
			m.AddU32("speed", uint32(speed))

			m = htspReqreply(hc, m)
			if m == nil {
				return nil, errors.New("Connection with server lost")
			}

			if err, ok := htsmsgGetStr(m, "error"); ok {
				serr := fmt.Errorf("From server: %s", err)
				m.Release()
				return nil, serr
			}

			m.Release()

		} else if meIsType(e, event.EVENT_SELECT_SUBTITLE_TRACK) {
			est, _ := event.ConcreteOf(e.Data).(*event.EventSelectTrack)

			if strings.HasPrefix(est.ID, "sub:") {
				manual := 0
				if est.Manual {
					manual = 1
				}
				hc.sys.htspSetSubtitles(mp, est.ID[len("sub:"):], manual)
			}

		} else if meIsType(e, event.EVENT_SELECT_AUDIO_TRACK) {
			est, _ := event.ConcreteOf(e.Data).(*event.EventSelectTrack)

			if strings.HasPrefix(est.ID, "audio:") {
				manual := 0
				if est.Manual {
					manual = 1
				}
				hc.sys.htspSetAudio(mp, est.ID[len("audio:"):], manual)
			}

		} else if meIsType(e, event.EVENT_PLAYBACK_PRIORITY) {
			ei, _ := event.ConcreteOf(e.Data).(*event.EventInt)

			hc.sys.ts.Trace(trace.TRACE_DEBUG, "HTSP",
				"%s: Changed priority to %d", name, ei.Val)

			m = htsmsg.NewMap()

			m.AddStr("method", "subscriptionChangeWeight")
			m.AddU32("subscriptionId", hs.sid)
			m.AddU32("weight", uint32(prioToWeight(ei.Val)))

			m = htspReqreply(hc, m)
			if m == nil {
				return nil, errors.New("Connection with server lost")
			}

			if err, ok := htsmsgGetStr(m, "error"); ok {
				serr := fmt.Errorf("From server: %s", err)
				m.Release()
				return nil, serr
			}

			m.Release()

		} else if meIsAction(e, event.ACTION_PREV_CHANNEL) ||
			meIsAction(e, event.ACTION_SKIP_BACKWARD) {

			if zr, zerr := zapChannel(hc, hs, 1, &name, vq); zr != 0 {
				return nil, zerr
			}

		} else if meIsAction(e, event.ACTION_NEXT_CHANNEL) ||
			meIsAction(e, event.ACTION_SKIP_FORWARD) {

			if zr, zerr := zapChannel(hc, hs, 0, &name, vq); zr != 0 {
				return nil, zerr
			}

		} else if meIsType(e, event.EVENT_EXIT) ||
			meIsType(e, event.EVENT_PLAY_URL) {
			break
		}

		e = nil // C: event_release(e)
	}

	pm.SetStringEx(mp.PropPlayStatus, nil, "stop", propcore.StringUTF8)

	m = htsmsg.NewMap()

	m.AddStr("method", "unsubscribe")
	m.AddU32("subscriptionId", hs.sid)

	m = htspReqreply(hc, m)
	if m != nil {
		m.Release()
	}

	return e, nil
}

// C: htsp_free_streams (htsp.c:1909-1920)
func htspFreeStreams(hs *htspSubscription) {
	for len(hs.streams) > 0 {
		hss := hs.streams[0]
		hs.streams = hs.streams[1:]
		if hss.cw != nil {
			mediacore.MediaCodecDeref(hss.cw)
		}
	}
}

// C: htsp_find_subscription_by_msg (htsp.c:2167-2183)
//
// Leaves 'hc_subscription_mutex' locked if we successfully find a
// subscription — the caller MUST unlock.
func htspFindSubscriptionByMsg(hc *htspConnection,
	m *htsmsg.HTSMsg) *htspSubscription {
	sid, err := m.GetU32("subscriptionId")
	if err != nil {
		return nil
	}

	hc.subscriptionMutex.Lock()
	for _, hs := range hc.subscriptions {
		if hs.sid == sid {
			return hs
		}
	}

	hc.subscriptionMutex.Unlock()
	return nil
}

// C: htsp_mux_input (htsp.c:2189-2256) — transport input
func htspMuxInput(hc *htspConnection, m *htsmsg.HTSMsg) {
	stream, err := m.GetU32("stream")
	if err != nil {
		return
	}
	bin, err := m.GetBin("payload")
	if err != nil {
		return
	}

	hs := htspFindSubscriptionByMsg(hc, m)
	if hs == nil {
		return
	}

	mp := hs.mp

	if int(stream) == mp.Audio.Stream || int(stream) == mp.Video.Stream ||
		int(stream) == mp.Video.Stream2 {

		var hss *htspSubscriptionStream
		for _, s := range hs.streams {
			if s.index == int(stream) {
				hss = s
				break
			}
		}

		if hss != nil {
			mb := mediacore.MediaBufAllocUnlocked(mp, len(bin))
			mb.DataType = hss.dataType
			mb.Stream = hss.index

			if u32, err := m.GetU32("duration"); err != nil {
				mb.Duration = 0
			} else {
				mb.Duration = int64(u32)
			}

			if v, err := m.GetS64("dts"); err != nil {
				mb.DTS = mediacore.PTSUnset
			} else {
				mb.DTS = v
			}

			if v, err := m.GetS64("pts"); err != nil {
				mb.PTS = mediacore.PTSUnset
			} else {
				mb.PTS = v
			}

			var timeshift int64
			if v, err := m.GetS64("timeshift"); err != nil {
				timeshift = 0
			} else {
				timeshift = v
			}
			_ = timeshift

			if hss.cw != nil {
				mb.Codec = mediacore.MediaCodecRef(hss.cw)
			}

			copy(mb.Data, bin)

			mb.Size = len(bin)

			if mb.DataType == mediacore.MbSubtitle {
				mb.FontContext = 0
			}

			auxtype := -1
			if mb.DataType == mediacore.MbSubtitle {
				auxtype = mb.DataType
			}
			if mediacore.MbEnqueueNoBlock(mp, hss.mq, mb, auxtype) != 0 {
				mediacore.MediaBufFreeUnlocked(mp, mb)
			}
		}
	}
	hc.subscriptionMutex.Unlock()
}

// C: htsp_subscriptionStart (htsp.c:2262-2460)
func htspSubscriptionStart(hc *htspConnection, m *htsmsg.HTSMsg) {
	pm := hc.sys.pm

	vstream := -1 // Initial video stream
	astream := -1 // Initial audio stream

	hs := htspFindSubscriptionByMsg(hc, m)
	if hs == nil {
		return
	}

	mp := hs.mp

	pm.SetVEx(nil, mp.PropRoot, "loading", 0)

	hc.sys.ts.Trace(trace.TRACE_DEBUG, "HTSP", "Got start notitification")
	mp.Audio.Stream = -1
	mp.Video.Stream2 = -1

	mediacore.MpReset(mp, pm)

	if sourceinfo := m.GetMap("sourceinfo"); sourceinfo != nil {
		service, ok := htsmsgGetStr(sourceinfo, "service")
		if !ok {
			service = "<?>" // C: ?: "<?>"
		}
		mux, ok := htsmsgGetStr(sourceinfo, "mux")
		if !ok {
			mux = "<?>" // C: ?: "<?>"
		}
		format := snprintf(256, "HTSP TV \"%s\" from \"%s\"",
			service, mux)

		pm.SetStringEx(
			pm.CreateEx(mp.PropMetadata, "format", nil, false, false),
			nil, format, propcore.StringUTF8)
	}

	// Parse each stream component and add it as a stream at our end
	if streams := m.GetList("streams"); streams != nil {
		for _, f := range streams.GetFields() {
			sub := f.GetMap()
			if sub == nil {
				continue
			}

			typ, ok := htsmsgGetStr(sub, "type")
			if !ok {
				continue
			}

			idx, err := sub.GetU32("index")
			if err != nil {
				continue
			}

			mcp := mediacore.MediaCodecParams{}

			lang, langOK := htsmsgGetStr(sub, "language")

			var codecID mediacore.CodecID
			var mediaType mediacore.MediaType
			var nicename string

			switch typ {
			case "AC3":
				codecID = mediacore.CodecIDAC3
				mediaType = mediacore.MediaTypeAudio
				nicename = "AC3"
			case "EAC3":
				codecID = mediacore.CodecIDEAC3
				mediaType = mediacore.MediaTypeAudio
				nicename = "EAC3"
			case "AAC":
				codecID = mediacore.CodecIDAAC
				mediaType = mediacore.MediaTypeAudio
				nicename = "AAC"
			case "MPEG2AUDIO":
				codecID = mediacore.CodecIDMP2
				mediaType = mediacore.MediaTypeAudio
				nicename = "MPEG"
			case "MPEG2VIDEO":
				codecID = mediacore.CodecIDMPEG2Video
				mediaType = mediacore.MediaTypeVideo
				nicename = "MPEG-2"
			case "H264":
				codecID = mediacore.CodecIDH264
				mediaType = mediacore.MediaTypeVideo
				nicename = "H264"
				mcp.CheatForSpeed = true
				mcp.BrokenAudPlacement = true
			case "DVBSUB":
				codecID = mediacore.CodecIDDVBSubtitle
				mediaType = mediacore.MediaTypeSubtitle
				nicename = "Bitmap"

				var compositionID, ancillaryID uint32

				v, err := sub.GetU32("composition_id")
				if err != nil {
					compositionID = 0
					hc.sys.ts.Trace(trace.TRACE_ERROR, "HTSP",
						"Subtitle stream #%d missing composition id",
						idx)
				} else {
					compositionID = v
				}

				v, err = sub.GetU32("ancillary_id")
				if err != nil {
					ancillaryID = 0
					hc.sys.ts.Trace(trace.TRACE_ERROR, "HTSP",
						"Subtitle stream #%d missing ancillary id",
						idx)
				} else {
					ancillaryID = v
				}

				buf4 := make([]byte, 4)
				buf4[0] = byte(compositionID >> 8)
				buf4[1] = byte(compositionID)
				buf4[2] = byte(ancillaryID >> 8)
				buf4[3] = byte(ancillaryID)

				mcp.ExtraData = buf4
				mcp.ExtraDataSize = 4 // C: mcp.extradata_size = 4
				mcp.ExtradataSize = 4
			case "TEXTSUB":
				codecID = -1
				mediaType = mediacore.MediaTypeSubtitle
				nicename = "Text"
			default:
				continue
			}

			if v, err := sub.GetU32("width"); err == nil {
				mcp.Width = int(v)
			}
			if v, err := sub.GetU32("height"); err == nil {
				mcp.Height = int(v)
			}

			// Try to create the codec
			var cw *mediacore.MediaCodec
			if codecID != -1 {
				cw = mediacore.MediaCodecCreate(codecID, 0, nil, nil,
					&mcp, mp)
				if cw == nil {
					hc.sys.ts.Trace(trace.TRACE_ERROR, "HTSP",
						"Unable to create codec for %s (#%d)",
						nicename, idx)
					continue // We should print something i guess ..
				}
			}

			hss := &htspSubscriptionStream{
				index: int(idx),
				cw:    cw,
			}

			var title string
			if !langOK {
				title = snprintf(64, "Stream %d", idx)
			} else {
				title = lang
			}

			switch mediaType {
			case mediacore.MediaTypeVideo:
				hss.mq = mp.Video
				hss.dataType = mediacore.MbVideo

				if vstream == -1 {
					vstream = int(idx)
				}

			case mediacore.MediaTypeSubtitle:
				hss.mq = mp.Video
				hss.dataType = mediacore.MbSubtitle

				url := snprintf(16, "sub:%d", idx)
				mediacore.MpAddTrack(pm, mp.PropSubtitleTracks,
					"", url, nicename, "", lang, "HTSP", nil, 0, 1)

			case mediacore.MediaTypeAudio:
				hss.mq = mp.Audio
				hss.dataType = mediacore.MbAudio

				if astream == -1 {
					astream = int(idx)
				}

				url := snprintf(16, "audio:%d", idx)
				mediacore.MpAddTrack(pm, mp.PropAudioTracks,
					"", url, nicename, "", lang, "HTSP", nil, 0, 1)
			}

			hc.sys.ts.Trace(trace.TRACE_DEBUG, "HTSP", "Stream #%d: %s %s",
				idx, nicename, title)
			hs.streams = slices.Insert(hs.streams, 0, hss)
		}
	}
	mp.Video.Stream = vstream

	hc.subscriptionMutex.Unlock()
}

// C: htsp_subscriptionStop (htsp.c:2466-2477)
func htspSubscriptionStop(hc *htspConnection, m *htsmsg.HTSMsg) {
	hs := htspFindSubscriptionByMsg(hc, m)
	if hs == nil {
		return
	}
	hc.sys.ts.Trace(trace.TRACE_DEBUG, "HTSP", "Subscription stopped")

	htspFreeStreams(hs)
	hc.subscriptionMutex.Unlock()
}

// C: htsp_subscriptionStatus (htsp.c:2483-2498)
func htspSubscriptionStatus(hc *htspConnection, m *htsmsg.HTSMsg) {
	status, ok := htsmsgGetStr(m, "status")

	hs := htspFindSubscriptionByMsg(hc, m)
	if hs == nil {
		return
	}

	propSetStringOrVoid(hc.sys.pm,
		hc.sys.pm.CreateEx(hs.mp.PropRoot, "error", nil, false, false),
		status, ok)

	if ok {
		hc.sys.ts.Trace(trace.TRACE_ERROR, "HTSP", "%s", status)
	}

	hc.subscriptionMutex.Unlock()
}

// C: htsp_queueStatus (htsp.c:2504-2538)
func htspQueueStatus(hc *htspConnection, m *htsmsg.HTSMsg) {
	pm := hc.sys.pm

	hs := htspFindSubscriptionByMsg(hc, m)
	if hs == nil {
		return
	}

	mp := hs.mp

	drops := 0
	if u32, err := m.GetU32("Bdrops"); err == nil {
		drops += int(u32)
	}
	if u32, err := m.GetU32("Pdrops"); err == nil {
		drops += int(u32)
	}
	if u32, err := m.GetU32("Idrops"); err == nil {
		drops += int(u32)
	}

	pm.SetIntEx(pm.CreateEx(mp.PropRoot, "isRemote", nil, false, false),
		nil, 1)

	r := pm.CreateEx(mp.PropRoot, "remote", nil, false, false)
	pm.SetIntEx(pm.CreateEx(r, "drops", nil, false, false), nil, drops)

	pm.SetIntEx(pm.CreateEx(r, "qlen", nil, false, false), nil,
		int(m.GetU32OrDefault("packets", 0)))

	pm.SetIntEx(pm.CreateEx(r, "qbytes", nil, false, false), nil,
		int(m.GetU32OrDefault("bytes", 0)))

	hc.subscriptionMutex.Unlock()
}

// C: htsp_signalStatus (htsp.c:2543-2546)
func htspSignalStatus(hc *htspConnection, m *htsmsg.HTSMsg) {
}
