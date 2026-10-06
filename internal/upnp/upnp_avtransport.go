package upnp

import (
	"fmt"
	"strings"

	"github.com/czz/movian-go/internal/api/soap"
	"github.com/czz/movian-go/internal/event"
	"github.com/czz/movian-go/internal/htsmsg"
	httpnet "github.com/czz/movian-go/internal/networking/http"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/trace"
)

// currentPlaystate — C: current_playstate (upnp_avtransport.c:55-65)
func (s *System) currentPlaystate() string {
	if !s.currentPlaystatusSet {
		return "NO_MEDIA_PRESENT"
	} else if s.currentPlaystatus == "play" {
		return "PLAYING"
	} else if s.currentPlaystatus == "pause" {
		return "PAUSED_PLAYBACK"
	}
	return "NO_MEDIA_PRESENT"
}

// currentPlayMode — C: current_playMode (upnp_avtransport.c:71-77)
func (s *System) currentPlayMode() string {
	if s.currentShuffle != 0 {
		return "SHUFFLE"
	} else if s.currentRepeat != 0 {
		return "REPEAT_ALL"
	}
	return "NORMAL"
}

// currentTransportActions — C: current_transportActions (upnp_avtransport.c:84-93)
func (s *System) currentTransportActions() string {
	str := "Play"
	if s.currentCanSkipBackward != 0 {
		str += ",Previous"
	}
	if s.currentCanSkipForward != 0 {
		str += ",Next"
	}
	if s.currentCanSeek != 0 {
		str += ",Seek"
	}
	if s.currentCanPause != 0 {
		str += ",Pause"
	}
	if s.currentCanStop != 0 {
		str += ",Stop"
	}
	return str
}

// avtStop — C: avt_Stop (upnp_avtransport.c:100-104)
func (uls *UPNPLocalService) avtStop(hc *httpnet.HTTPConnection, args *htsmsg.HTSMsg,
	myhost string, myport int) *htsmsg.HTSMsg {
	s := uls.sys
	if s.eventManager != nil {
		s.eventManager.Dispatch(
			s.eventManager.CreateAction(event.ACTION_STOP).AsEvent())
	}
	return nil
}

// avtPause — C: avt_Pause (upnp_avtransport.c:110-114)
func (uls *UPNPLocalService) avtPause(hc *httpnet.HTTPConnection, args *htsmsg.HTSMsg,
	myhost string, myport int) *htsmsg.HTSMsg {
	s := uls.sys
	if s.eventManager != nil {
		s.eventManager.Dispatch(
			s.eventManager.CreateAction(event.ACTION_PAUSE).AsEvent())
	}
	return nil
}

// avtSeek — C: avt_Seek (upnp_avtransport.c:122-125)
func (uls *UPNPLocalService) avtSeek(hc *httpnet.HTTPConnection, args *htsmsg.HTSMsg,
	myhost string, myport int) *htsmsg.HTSMsg {
	return nil
}

// avtPlay — C: avt_Play (upnp_avtransport.c:131-135)
func (uls *UPNPLocalService) avtPlay(hc *httpnet.HTTPConnection, args *htsmsg.HTSMsg,
	myhost string, myport int) *htsmsg.HTSMsg {
	s := uls.sys
	if s.eventManager != nil {
		s.eventManager.Dispatch(
			s.eventManager.CreateAction(event.ACTION_PLAY).AsEvent())
	}
	return nil
}

// avtNext — C: avt_Next (upnp_avtransport.c:140-144)
func (uls *UPNPLocalService) avtNext(hc *httpnet.HTTPConnection, args *htsmsg.HTSMsg,
	myhost string, myport int) *htsmsg.HTSMsg {
	s := uls.sys
	if s.eventManager != nil {
		s.eventManager.Dispatch(
			s.eventManager.CreateAction(event.ACTION_SKIP_FORWARD).AsEvent())
	}
	return nil
}

// avtPrevious — C: avt_Previous (upnp_avtransport.c:150-154)
func (uls *UPNPLocalService) avtPrevious(hc *httpnet.HTTPConnection, args *htsmsg.HTSMsg,
	myhost string, myport int) *htsmsg.HTSMsg {
	s := uls.sys
	if s.eventManager != nil {
		s.eventManager.Dispatch(
			s.eventManager.CreateAction(event.ACTION_SKIP_BACKWARD).AsEvent())
	}
	return nil
}

// playWithContext resolves the item's parent listing and loads the
// playqueue with the context model. C: play_with_context
// (upnp_avtransport.c:161-205)
func (s *System) playWithContext(uri string, meta *htsmsg.HTSMsg) int {
	parentid := meta.GetStrMulti("DIDL-Lite", "item", "parentID")
	if parentid == "" {
		return 1
	}

	id := meta.GetStrMulti("DIDL-Lite", "item", "id")
	if id == "" {
		return 1
	}

	s.upnpTrace("Playing %s (id: %s, parent: %s)", uri, id, parentid)

	s.mu.Lock()

	us := s.serviceGuess(uri)

	if us != nil {
		s.upnpTrace("Using controlpoint %s", us.controlURL)

		model := s.propManager.CreateRootEx("", true)
		nodes := s.propManager.CreateEx(model, "nodes", nil, false, false)
		var t *propcore.Prop

		// C: upnp_browse_children runs a blocking HTTP fetch under
		// upnp_lock (upnp_avtransport.c:207) — the lock guards us
		// (and upnp_devices) against concurrent device teardown.
		// Same invariant as upnp_add_device's introspect_device.
		if s.browseChildren(us.controlURL, parentid, nodes, id, &t) != 0 ||
			t == nil {

			s.propManager.Destroy(model)

		} else {
			if s.playQueue != nil {
				s.playQueue.LoadWithSource(t, model, PQPaused)
			}
			s.mu.Unlock()
			return 0
		}
	}

	s.mu.Unlock()
	return 1
}

// avtSetAVTransportURI — C: avt_SetAVTransportURI
// (upnp_avtransport.c:212-243)
func (uls *UPNPLocalService) avtSetAVTransportURI(hc *httpnet.HTTPConnection, args *htsmsg.HTSMsg,
	myhost string, myport int) *htsmsg.HTSMsg {
	s := uls.sys
	uri := args.GetStr("CurrentURI")
	metaxml := args.GetStr("CurrentURIMetaData")

	if uri == "" {
		return nil
	}

	if metaxml == "" {
		if s.playQueue != nil {
			s.playQueue.Play(uri, s.propManager.CreateRootEx("", true), true)
		}
		return nil
	}

	meta, err := htsmsg.DeserializeXML(metaxml)
	if meta == nil {
		uls.sys.ts.Trace(trace.TRACE_ERROR, "UPNP",
			"SetAVTransportURI: Unable to parse metadata -- %v", err)
		return nil
	}

	if s.playWithContext(uri, meta) != 0 {
		// Failed to play from context
		// TODO: Fix metadata here
		if s.playQueue != nil {
			s.playQueue.Play(uri, s.propManager.CreateRootEx("", true), true)
		}
	}
	meta.Release()
	return nil
}

// buildDIDL builds the DIDL-Lite metadata XML for the current track.
// C: build_didl (upnp_avtransport.c:250-318)
func (s *System) buildDIDL(myhost string, myport int) string {
	var hq strings.Builder

	hq.WriteString(
		"<DIDL-Lite xmlns=\"urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/\" " +
			"xmlns:dc=\"http://purl.org/dc/elements/1.1/\" " +
			"xmlns:dlna=\"urn:schemas-dlna-org:metadata-1-0\" " +
			"xmlns:pv=\"http://www.pv.com/pvns/\" " +
			"xmlns:upnp=\"urn:schemas-upnp-org:metadata-1-0/upnp/\">" +
			"<item id=\"101\" parentID=\"100\" restricted=\"0\">" +
			"<upnp:class xmlns:upnp=\"urn:schemas-upnp-org:metadata-1-0/upnp/\">object.item.audioItem.musicTrack</upnp:class>")

	if s.currentTitle != "" {
		hq.WriteString("<dc:title xmlns:dc=\"http://purl.org/dc/elements/1.1/\">")
		hq.WriteString(soap.EscapeXML(s.currentTitle))
		hq.WriteString("</dc:title>")
	}

	if s.currentArtist != "" {
		hq.WriteString("<upnp:artist xmlns:upnp=\"urn:schemas-upnp-org:metadata-1-0/upnp/\">")
		hq.WriteString(soap.EscapeXML(s.currentArtist))
		hq.WriteString("</upnp:artist>")
	}

	if s.currentAlbum != "" {
		hq.WriteString("<upnp:album xmlns:upnp=\"urn:schemas-upnp-org:metadata-1-0/upnp/\">")
		hq.WriteString(soap.EscapeXML(s.currentAlbum))
		hq.WriteString("</upnp:album>")
	}

	if s.currentAlbumArt != "" {
		var arturl string

		if !strings.HasPrefix(s.currentAlbumArt, "http://") {
			arturl = fmt.Sprintf("http://%s:%d/api/image/%s",
				myhost, myport, s.currentAlbumArt)
		} else {
			arturl = s.currentAlbumArt
		}

		hq.WriteString("<upnp:albumArtURI xmlns:upnp=\"urn:schemas-upnp-org:metadata-1-0/upnp/\">")
		hq.WriteString(soap.EscapeXML(arturl))
		hq.WriteString("</upnp:albumArtURI>")
	}

	hq.WriteString("</item></DIDL-Lite>")

	return hq.String()
}

// fmtTime formats seconds as h:mm:ss.
// C: fmttime (upnp_avtransport.c:325-328)
func fmtTime(t int) string {
	return fmt.Sprintf("%d:%02d:%02d", t/3600, (t/60)%60, t%60)
}

// avtGetPositionInfo — C: avt_GetPositionInfo (upnp_avtransport.c:335-361)
func (uls *UPNPLocalService) avtGetPositionInfo(hc *httpnet.HTTPConnection, args *htsmsg.HTSMsg,
	myhost string, myport int) *htsmsg.HTSMsg {
	s := uls.sys
	out := htsmsg.NewMap()

	s.mu.Lock()

	didl := s.buildDIDL(hc.HTTPGetMyHost(), hc.HTTPGetMyPort())

	out.AddU32("Track", uint32(s.currentTrack))

	out.AddStr("TrackDuration", fmtTime(s.currentTrackDuration))

	out.AddStr("TrackMetaData", didl)
	out.AddStr("TrackURI", s.currentURL)

	tbuf := fmtTime(s.currentTrackTime)
	out.AddStr("RelTime", tbuf)
	out.AddStr("AbsTime", tbuf) //"NOT_IMPLEMENTED"
	out.AddU32("RelCount", 2147483647)
	out.AddU32("AbsCount", 2147483647)

	s.mu.Unlock()
	return out
}

// avtGetTransportInfo — C: avt_GetTransportInfo (upnp_avtransport.c:368-380)
func (uls *UPNPLocalService) avtGetTransportInfo(hc *httpnet.HTTPConnection, args *htsmsg.HTSMsg,
	myhost string, myport int) *htsmsg.HTSMsg {
	s := uls.sys
	out := htsmsg.NewMap()

	s.mu.Lock()
	out.AddStr("CurrentTransportState", s.currentPlaystate())
	out.AddStr("CurrentTransportStatus", "OK")
	out.AddStr("CurrentSpeed", "1")

	s.mu.Unlock()
	return out
}

// avtGetTransportSettings — C: avt_GetTransportSettings
// (upnp_avtransport.c:387-395)
func (uls *UPNPLocalService) avtGetTransportSettings(hc *httpnet.HTTPConnection, args *htsmsg.HTSMsg,
	myhost string, myport int) *htsmsg.HTSMsg {
	s := uls.sys
	out := htsmsg.NewMap()

	s.mu.Lock()
	out.AddStr("PlayMode", s.currentPlayMode())
	s.mu.Unlock()
	return out
}

// currentMediaCategory — C: current_mediaCategory (upnp_avtransport.c:402-409)
func (s *System) currentMediaCategory() string {
	if !s.currentPlaystatusSet {
		return "NO_MEDIA"
	}
	if s.currentType == "tracks" {
		return "TRACK_AWARE"
	}
	return "TRACK_UNAWARE"
}

// avtGetMediaInfoCommon — C: avt_GetMediaInfo_common
// (upnp_avtransport.c:415-437)
func (uls *UPNPLocalService) avtGetMediaInfoCommon(hc *httpnet.HTTPConnection, args *htsmsg.HTSMsg,
	myhost string, myport int, ext int) *htsmsg.HTSMsg {
	s := uls.sys
	out := htsmsg.NewMap()

	s.mu.Lock()
	if ext != 0 {
		out.AddStr("CurrentType", s.currentMediaCategory())
	}
	out.AddU32("NrTracks", uint32(s.currentTotalTracks))
	out.AddStr("MediaDuration", fmtTime(s.currentTrackDuration))
	out.AddStr("CurrentURI", s.currentURL)

	meta := s.buildDIDL(myhost, myport)
	out.AddStr("CurrentURIMetaData", meta)

	playMedium := "NONE"
	if s.currentPlaystatusSet {
		playMedium = "NETWORK"
	}
	out.AddStr("PlayMedium", playMedium)
	s.mu.Unlock()
	return out
}

// avtGetMediaInfoExt — C: avt_GetMediaInfo_Ext (upnp_avtransport.c:444-448)
func (uls *UPNPLocalService) avtGetMediaInfoExt(hc *httpnet.HTTPConnection, args *htsmsg.HTSMsg,
	myhost string, myport int) *htsmsg.HTSMsg {
	return uls.avtGetMediaInfoCommon(hc, args, myhost, myport, 1)
}

// avtGetMediaInfo — C: avt_GetMediaInfo (upnp_avtransport.c:454-458)
func (uls *UPNPLocalService) avtGetMediaInfo(hc *httpnet.HTTPConnection, args *htsmsg.HTSMsg,
	myhost string, myport int) *htsmsg.HTSMsg {
	return uls.avtGetMediaInfoCommon(hc, args, myhost, myport, 0)
}

// avtGetDeviceCapabilities — C: avt_GetDeviceCapabilities
// (upnp_avtransport.c:464-470)
func (uls *UPNPLocalService) avtGetDeviceCapabilities(hc *httpnet.HTTPConnection, args *htsmsg.HTSMsg,
	myhost string, myport int) *htsmsg.HTSMsg {
	out := uls.avtGetMediaInfo(hc, args, myhost, myport)
	out.AddStr("PlayMedia", "NETWORK")
	return out
}

// avtGetCurrentTransportActions — C: avt_GetCurrentTransportActions
// (upnp_avtransport.c:476-483)
func (uls *UPNPLocalService) avtGetCurrentTransportActions(hc *httpnet.HTTPConnection, args *htsmsg.HTSMsg,
	myhost string, myport int) *htsmsg.HTSMsg {
	s := uls.sys
	out := uls.avtGetMediaInfo(hc, args, myhost, myport)
	out.AddStr("Actions", s.currentTransportActions())
	return out
}

// avtGenerateProps — C: avt_generate_props (upnp_avtransport.c:490-555)
func avtGenerateProps(uls *UPNPLocalService, myhost string, myport int) *htsmsg.HTSMsg {
	s := uls.sys
	var xml strings.Builder

	xml.WriteString("<Event xmlns=\"urn:schemas-upnp-org:metadata-1-0/RCS/\">" +
		"<InstanceID val=\"0\">")

	eventEncodeStr(&xml, "TransportState", s.currentPlaystate())

	eventEncodeStr(&xml, "CurrentMediaCategory", s.currentMediaCategory())

	// PlaybackStorageMedium

	medium := "NONE"
	if s.currentPlaystatusSet {
	}
	eventEncodeStr(&xml, "PlaybackStorageMedium", medium)

	eventEncodeStr(&xml, "CurrentPlayMode", s.currentPlayMode())

	eventEncodeStr(&xml, "CurrentTransportActions", s.currentTransportActions())

	eventEncodeInt(&xml, "NumberOfTracks", s.currentTotalTracks)
	eventEncodeInt(&xml, "CurrentTrack", s.currentTrack)
	eventEncodeStr(&xml, "AVTransportURI", s.currentURL)
	eventEncodeInt(&xml, "TransportPlaySpeed", 1)

	// Metadata

	meta := s.buildDIDL(myhost, myport)
	eventEncodeStr(&xml, "AVTransportURIMetaData", meta)
	eventEncodeStr(&xml, "CurrentTrackMetaData", meta)

	eventEncodeStr(&xml, "CurrentTrackDuration", fmtTime(s.currentTrackDuration))
	eventEncodeStr(&xml, "CurrentMediaDuration", fmtTime(s.currentTrackDuration))

	eventEncodeStr(&xml, "PossibleRecordQualityModes", "")
	eventEncodeStr(&xml, "TransportStatus", "OK")
	eventEncodeStr(&xml, "DRMState", "UNKNOWN")
	eventEncodeStr(&xml, "RecordMediumWriteStatus", "")
	eventEncodeStr(&xml, "RecordStorageMedium", "")
	eventEncodeStr(&xml, "PossibleRecordStorageMedia", "")
	eventEncodeStr(&xml, "NextAVTransportURI", "")
	eventEncodeStr(&xml, "NextAVTransportURIMetaData", "")
	eventEncodeStr(&xml, "CurrentRecordQualityMode", "")
	eventEncodeStr(&xml, "PossiblePlaybackStorageMedia", "NETWORK")

	xml.WriteString("</InstanceID></Event>")

	r := htsmsg.NewMap()
	r.AddStr("LastChange", xml.String())
	return r
}

// setCurrentStr — C: set_current_str (upnp_avtransport.c:589-594)
func (s *System) setCurrentStr(opaque any, str string) {
	s.mu.Lock()
	p := opaque.(*string)
	*p = str
	// playstatus NULL tracking: opaque is &s.currentPlaystatus → track set
	if p == &s.currentPlaystatus {
		s.currentPlaystatusSet = str != ""
	}
	s.avt.scheduleNotify()
	s.mu.Unlock()
}

// setCurrentInt — C: set_current_int (upnp_avtransport.c:600-604)
func (s *System) setCurrentInt(opaque any, v int) {
	s.mu.Lock()
	*(opaque.(*int)) = v
	s.avt.scheduleNotify()
	s.mu.Unlock()
}

// setCurrentIntPassive — C: set_current_int_passive (upnp_avtransport.c:610-613)
func (s *System) setCurrentIntPassive(opaque any, v int) {
	s.mu.Lock()
	*(opaque.(*int)) = v
	s.mu.Unlock()
}

// subscribeCurrentStr subscribes to global.media.current.<name> delivering
// string updates. C: prop_subscribe(PROP_TAG_NAME(...),
// PROP_TAG_CALLBACK_STRING, cb, opaque, PROP_TAG_MUTEX, &upnp_lock)
func (s *System) subscribeCurrentStr(fn func(any, string), opaque any,
	names ...string) {
	p := s.propManager.GetGlobal()
	if p == nil {
		return
	}
	target := s.propManager.CreateMultiPath(p, names...)
	target.Subscribe(func(o any, et propcore.EventType, args ...any) {
		switch et {
		case propcore.EventSetRString, propcore.EventSetCString:
			if s, ok := args[0].(string); ok {
				fn(o, s)
				return
			}
			fn(o, "")
		case propcore.EventSetVoid:
			fn(o, "")
		case propcore.EventSetURI:
			if s, ok := args[0].(string); ok {
				fn(o, s)
			}
		}
	}, opaque)
}

// subscribeCurrentInt — C: PROP_TAG_CALLBACK_INT variant
func (s *System) subscribeCurrentInt(fn func(any, int), opaque any,
	names ...string) {
	p := s.propManager.GetGlobal()
	if p == nil {
		return
	}
	target := s.propManager.CreateMultiPath(p, names...)
	target.Subscribe(func(o any, et propcore.EventType, args ...any) {
		switch et {
		case propcore.EventSetInt:
			switch v := args[0].(type) {
			case int:
				fn(o, v)
			case int64:
				fn(o, int(v))
			default:
				fn(o, 0)
			}
		case propcore.EventSetVoid:
			fn(o, 0)
		}
	}, opaque)
}

// upnpAVTransportStart subscribes to global.media.current.* to track
// playback state. C: upnp_avtransport_init (upnp_avtransport.c:621-768)
func (s *System) upnpAVTransportStart() {
	s.avt.methods = []UPNPServiceMethod{
		{"Stop", s.avt.avtStop},
		{"SetAVTransportURI", s.avt.avtSetAVTransportURI},
		{"Play", s.avt.avtPlay},
		{"Pause", s.avt.avtPause},
		{"Seek", s.avt.avtSeek},
		{"Next", s.avt.avtNext},
		{"Previous", s.avt.avtPrevious},
		{"GetPositionInfo", s.avt.avtGetPositionInfo},
		{"GetTransportInfo", s.avt.avtGetTransportInfo},
		{"GetTransportSettings", s.avt.avtGetTransportSettings},
		{"GetMediaInfo", s.avt.avtGetMediaInfo},
		{"GetMediaInfo_Ext", s.avt.avtGetMediaInfoExt},
		{"GetDeviceCapabilities", s.avt.avtGetDeviceCapabilities},
		{"GetCurrentTransportActions", s.avt.avtGetCurrentTransportActions},
	}

	s.subscribeCurrentStr(s.setCurrentStr, &s.currentURL,
		"media", "current", "url")

	s.subscribeCurrentStr(s.setCurrentStr, &s.currentPlaystatus,
		"media", "current", "playstatus")

	s.subscribeCurrentStr(s.setCurrentStr, &s.currentType,
		"media", "current", "type")

	s.subscribeCurrentStr(s.setCurrentStr, &s.currentTitle,
		"media", "current", "metadata", "title")

	s.subscribeCurrentStr(s.setCurrentStr, &s.currentAlbum,
		"media", "current", "metadata", "album")

	s.subscribeCurrentStr(s.setCurrentStr, &s.currentAlbumArt,
		"media", "current", "metadata", "album_art")

	s.subscribeCurrentStr(s.setCurrentStr, &s.currentArtist,
		"media", "current", "metadata", "artist")

	s.subscribeCurrentInt(s.setCurrentInt, &s.currentTrackDuration,
		"media", "current", "metadata", "duration")

	s.subscribeCurrentInt(s.setCurrentInt, &s.currentTrack,
		"media", "current", "currentTrack")

	s.subscribeCurrentInt(s.setCurrentInt, &s.currentTotalTracks,
		"media", "current", "totalTracks")

	s.subscribeCurrentInt(s.setCurrentIntPassive, &s.currentTrackTime,
		"media", "current", "currenttime")

	s.subscribeCurrentInt(s.setCurrentInt, &s.currentShuffle,
		"media", "current", "shuffle")

	s.subscribeCurrentInt(s.setCurrentInt, &s.currentRepeat,
		"media", "current", "repeat")

	s.subscribeCurrentInt(s.setCurrentInt, &s.currentCanSkipBackward,
		"media", "current", "canSkipBackward")

	s.subscribeCurrentInt(s.setCurrentInt, &s.currentCanSkipForward,
		"media", "current", "canSkipForward")

	s.subscribeCurrentInt(s.setCurrentInt, &s.currentCanSeek,
		"media", "current", "canSeek")

	s.subscribeCurrentInt(s.setCurrentInt, &s.currentCanPause,
		"media", "current", "canPause")

	s.subscribeCurrentInt(s.setCurrentInt, &s.currentCanStop,
		"media", "current", "canStop")
}
