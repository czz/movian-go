package icecast

// Canonical port of src/backend/icecast/icecast.c — function-for-function
// translation. The Shoutcast/Icecast backend parses PLS/M3U/XSPF playlists,
// picks random live sources with failover, strips ICY metadata inline and
// feeds audio packets into the media pipe.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/czz/movian-go/internal/arch"
	backendcore "github.com/czz/movian-go/internal/backend/core"
	"github.com/czz/movian-go/internal/event"
	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/gconf"
	"github.com/czz/movian-go/internal/htsmsg"
	"github.com/czz/movian-go/internal/libav"
	mediacore "github.com/czz/movian-go/internal/media/core"
	medialibav "github.com/czz/movian-go/internal/media/libav"
	"github.com/czz/movian-go/internal/misc"
	httpnet "github.com/czz/movian-go/internal/networking/http"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/trace"
	"github.com/czz/movian-go/internal/usage"
)

// icecastSource — C: icecast_source_t (icecast.c:37-41). The C TAILQ is a
// slice here; TAILQ_INSERT_TAIL → append preserves ordering.
type icecastSource struct {
	url  string // C: is_url
	dead bool   // C: is_dead
}

// icecastPlayContext — C: icecast_play_context_t (icecast.c:46-64).
type icecastPlayContext struct {
	usage *usage.Reporter

	url  string                 // C: ipc_url
	hold int                    // C: ipc_hold — set if paused
	mp   *mediacore.MediaPipe   // C: ipc_mp
	mf   *mediacore.MediaFormat // C: ipc_mf
	mc   *mediacore.MediaCodec  // C: ipc_mc

	sources  []*icecastSource // C: ipc_sources (TAILQ)
	nsources int              // C: ipc_nsources

	requestHeaders  *httpnet.HTTPHeaderList // C: ipc_request_headers
	responseHeaders *httpnet.HTTPHeaderList // C: ipc_response_headers

	radioInfo *propcore.Prop // C: ipc_radio_info

	// charsetSrc — C: i18n_get_default_charset() (i18n.c), resolved via
	// the injected i18n instance for icymeta title decoding.
	charsetSrc misc.CharsetDefaultSrc

	// gconf — C: gconf_t gconf (enable_icecast_debug). Injected via bs.
	gconf *gconf.T

	streamInfoSet bool // C: ipc_streaminfo_set

	// Go plumbing — in C these are reachable through globals:
	//   fam      — the fileaccess manager (C: fa_* uses the global resolver)
	//   pm       — the prop manager (C: prop_* uses the global prop tree)
	//   libavSys — the libav system (C: fa_libav_* uses a static)
	//   fctx     — C: ipc->ipc_mf->fctx (non-owning view for stream info)
	//   fctxLibav — owning handle closed via FALibavCloseFormat in
	//               closeStream (C: media_format_deref → fa_libav_close_format)
	pm        *propcore.PropManager
	fam       *fileaccesscore.FileAccessManager
	libavSys  *libav.LibAVSystem
	mediaSys  *mediacore.MediaSystem // C: media.c globals — injected
	ts        *trace.TraceSystem     // C: trace() global — injected
	fctx      *medialibav.AVFormatCtx
	fctxLibav *libav.AVFormatContext
}

// mbSpecialEOF — C: MB_SPECIAL_EOF ((void *)-1) (icecast.c:433). The
// sentinel branch is unreachable in icecast.c but kept for parity.
var mbSpecialEOF = &mediacore.MediaBuf{}

// newIcecastPlayContext — C: calloc(1, sizeof(icecast_play_context_t)) plus
// the LIST_INITs of the two header lists (done in stream_radio by use).
func newIcecastPlayContext(u *usage.Reporter, pm *propcore.PropManager,
	fam *fileaccesscore.FileAccessManager, sys *libav.LibAVSystem, ts *trace.TraceSystem,
	dcs misc.CharsetDefaultSrc, g *gconf.T, ms *mediacore.MediaSystem) *icecastPlayContext {
	if g == nil {
		g = gconf.New()
	}
	return &icecastPlayContext{
		usage:           u,
		charsetSrc:      dcs,
		gconf:           g,
		pm:              pm,
		fam:             fam,
		ts:              ts,
		libavSys:        sys,
		mediaSys:        ms,
		requestHeaders:  &httpnet.HTTPHeaderList{},
		responseHeaders: &httpnet.HTTPHeaderList{},
	}
}

// flushSources — C: flush_sources (icecast.c:71-81).
func (ipc *icecastPlayContext) flushSources() {
	ipc.sources = nil
	ipc.nsources = 0
}

// addSource — C: add_source (icecast.c:86-94).
func (ipc *icecastPlayContext) addSource(url string) {
	is := &icecastSource{
		url: misc.UrlResolveRelativeFromBase(ipc.url, url),
	}
	ipc.sources = append(ipc.sources, is)
	ipc.nsources++
}

// parsePLS — C: parse_pls (icecast.c:99-110). buf is mutated by LpGet
// exactly like the C LINEPARSE writes NULs into buf.
func (ipc *icecastPlayContext) parsePLS(buf []byte) {
	ipc.flushSources()

	lp := buf
	for {
		s := misc.LpGet(&lp)
		if s == nil {
			break
		}
		line := s
		if before, _, ok := bytes.Cut(s, []byte{0}); ok {
			line = before
		}
		// C: if(strncasecmp(line, "file", 4)) continue;
		if len(line) < 4 || !strings.EqualFold(string(line[:4]), "file") {
			continue
		}
		// C: if((line = strchr(line + 4, '=')) == NULL) continue;
		i := bytes.IndexByte(line[4:], '=')
		if i < 0 {
			continue
		}
		// C: add_source(ipc, line + 1);
		ipc.addSource(string(line[4+i+1:]))
	}
}

// parseM3U — C: parse_m3u (icecast.c:115-121).
func (ipc *icecastPlayContext) parseM3U(buf []byte) {
	lp := buf
	for {
		s := misc.LpGet(&lp)
		if s == nil {
			break
		}
		// C: if(*line != '#') add_source(ipc, line);
		if s[0] != '#' {
			line := s
			if before, _, ok := bytes.Cut(s, []byte{0}); ok {
				line = before
			}
			ipc.addSource(string(line))
		}
	}
}

// parseXSPF — C: parse_xspf (icecast.c:140-162).
func (ipc *icecastPlayContext) parseXSPF(b *fileaccesscore.Buffer) {
	m, err := htsmsg.DeserializeXMLBuf(b.Data)
	if m == nil {
		errStr := "unknown error"
		if err != nil {
			errStr = err.Error()
		}
		ipc.ts.Trace(trace.TRACE_ERROR, "Radio",
			"Unable to parse XSPF -- %s", errStr)
		return
	}

	// C: htsmsg_get_map_multi(m, "playlist", "trackList", NULL)
	list := m.GetMapMulti("playlist", "trackList")

	if list != nil {
		// C: HTSMSG_FOREACH(f, list)
		for _, f := range list.GetFields() {
			// C: htsmsg_get_map_by_field_if_name(f, "track")
			t := f.GetMapByFieldIfName("track")
			if t == nil {
				continue
			}
			// C: const char *loc = htsmsg_get_str(t, "location");
			if fld := t.FieldFind("location"); fld != nil {
				if loc, ok := fld.FieldGetString(); ok {
					ipc.addSource(loc)
				}
			}
		}
	}
	m.Release()
}

// numDeads — C: num_deads (icecast.c:168-177).
func (ipc *icecastPlayContext) numDeads() int {
	numDead := 0
	for _, is := range ipc.sources {
		if is.dead {
			numDead++
		}
	}
	return numDead
}

// openStream — C: open_stream (icecast.c:180-370). Returns 0 on success,
// -1 on failure.
func (ipc *icecastPlayContext) openStream() int {
	pbuf := make([]byte, 256)
	var fh *fileaccesscore.Handle
	var is *icecastSource
	var url string
	var err error
	var r, n int

	const flags = fileaccesscore.FaStreaming | fileaccesscore.FaNoRetries |
		fileaccesscore.FaBufferedNoPrefetch |
		fileaccesscore.FaBufferedSmall | fileaccesscore.FaNoParking

again:
	numDead := ipc.numDeads()

	// C: assert(num_dead <= ipc->ipc_nsources)
	if numDead > ipc.nsources {
		panic("icecast: num_dead > nsources")
	}

	// C: fa_open_extra_t foe = { .foe_request_headers  = &ipc->ipc_request_headers,
	//                            .foe_response_headers = &ipc->ipc_response_headers,
	//                            .foe_cancellable       = ipc->ipc_mp->mp_cancellable };
	extra := &fileaccesscore.OpenExtra{
		RequestHeadersList:  ipc.requestHeaders,
		ResponseHeadersList: ipc.responseHeaders,
		Cancellable:         ipc.mp.Cancellable,
	}

	if numDead == ipc.nsources {

		// All sources are dead, or we don't have any sources (yet)

		fh, err = fileaccesscore.FAOpenEx(ipc.fam, ipc.url, flags, extra)

		if fh == nil {
			if misc.CancellableIsCancelled(ipc.mp.Cancellable) != 0 {
				return -1
			}
			errStr := ""
			if err != nil {
				errStr = err.Error()
			}
			ipc.ts.Trace(trace.TRACE_ERROR, "Radio", "Unable to open %s -- %s",
				ipc.url, errStr)
			return -1
		}

		ct, ctOK := ipc.responseHeaders.Get("content-type")

		isM3u := false
		isPls := false
		isXspf := false

		if ctOK {
			ipc.ts.Trace(trace.TRACE_DEBUG, "Radio", "%s content-type: %s",
				ipc.url, ct)
		}

		if ctOK && strings.EqualFold(ct, "application/xspf+xml") {
			isXspf = true
			goto load
		}

		n, err = fileaccesscore.Read(fh, pbuf[:len(pbuf)-1])
		r = n
		// C: r = fa_read(...) — returns -1 only on a real error; EOF is
		// just a short/zero count, not a failure.
		if err != nil && !errors.Is(err, io.EOF) {
			r = -1
		}
		if r < 0 {
			ipc.ts.Trace(trace.TRACE_ERROR, "Radio", "Unable to probe %s",
				ipc.url)
			return -1
		}
		pbuf[r] = 0

		if ctOK &&
			(strings.EqualFold(ct, "application/x-mpegurl") ||
				strings.EqualFold(ct, "audio/x-mpegurl")) {
			ipc.ts.Trace(trace.TRACE_DEBUG, "Radio",
				"%s is an .m3u playlist according to content-type", ipc.url)
			isM3u = true
		}

		if r > 5 && bytes.Equal(pbuf[:5], []byte("<?xml")) {
			ipc.ts.Trace(trace.TRACE_DEBUG, "Radio",
				"%s is a XSPF playlist based on content", ipc.url)
			isXspf = true
		}

		if r > 10 && bytes.Equal(pbuf[:10], []byte("[playlist]")) {
			// The URL points to a playlist, parse it
			ipc.ts.Trace(trace.TRACE_DEBUG, "Radio",
				"%s is a .pls playlist based on content", ipc.url)
			isPls = true
		} else if bytes.Contains(pbuf[:r], []byte("http://")) ||
			bytes.Contains(pbuf[:r], []byte("https://")) {
			ipc.ts.Trace(trace.TRACE_DEBUG, "Radio",
				"%s guessed to be an m3u based on content", ipc.url)
			isM3u = true
		}

	load:
		if isPls || isM3u || isXspf {

			// C: buf_t *b = fa_load_and_close(fh)
			b := fileaccesscore.LoadAndClose(fh)
			if b == nil {
				ipc.ts.Trace(trace.TRACE_ERROR, "Radio",
					"Unable to read playlist %s", ipc.url)
				return -1
			}

			ipc.flushSources()

			if isXspf {
				ipc.parseXSPF(b)
			} else if isPls {
				// C: parse_pls(ipc, buf_str(b))
				ipc.parsePLS(b.Data)
			} else {
				ipc.parseM3U(b.Data)
			}
			// C: buf_release(b) — GC

			if ipc.nsources == 0 {
				ipc.ts.Trace(trace.TRACE_ERROR, "Radio",
					"No files found in playlist %s", ipc.url)
				return -1
			}

			goto again
		}
		// C: fa_seek(fh, 0, SEEK_SET)
		fileaccesscore.Seek(fh, 0, io.SeekStart)
		url = ipc.url

	} else {

		// C: int r = rand() % (ipc->ipc_nsources - num_dead);
		r := rand.Intn(ipc.nsources - numDead)
		for _, s := range ipc.sources {
			if s.dead {
				continue
			}
			// C: if(!r--) break;
			if r == 0 {
				is = s
				break
			}
			r--
		}

		// C: assert(is != NULL)
		if is == nil {
			panic("icecast: no source selected")
		}
		url = is.url
		fh, err = fileaccesscore.FAOpenEx(ipc.fam, url, flags, extra)

		if fh == nil {
			is.dead = true
			if misc.CancellableIsCancelled(ipc.mp.Cancellable) != 0 {
				return -1
			}

			errStr := ""
			if err != nil {
				errStr = err.Error()
			}
			ipc.ts.Trace(trace.TRACE_INFO, "Radio",
				"Unable to open %s -- %s, trying another URL",
				ipc.url, errStr)

			numDead = ipc.numDeads()
			if numDead == ipc.nsources {
				return -1
			}

			goto again
		}
	}

	// C: fa_set_read_timeout(fh, 10000)
	fileaccesscore.FADeadline(fh, 10000)

	var reader libav.FileAccessReader = fh

	// C: mx = http_header_get(&ipc->ipc_response_headers, "icy-metaint")
	if mx, ok := ipc.responseHeaders.Get("icy-metaint"); ok {
		stride, _ := strconv.Atoi(mx)
		if stride != 0 {
			reader = icyMetaParser(ipc, fh, stride)
		}
	}

	// C: AVIOContext *avio = fa_libav_reopen(fh, 1)
	avio, err := libav.FALibavReopen(ipc.libavSys, reader, true)

	if avio == nil {
		reader.Close()
		return -1
	}

	ct, _ := ipc.responseHeaders.Get("content-type")

	fctxLibav, err := libav.FALibavOpenFormat(avio, url,
		ct, libav.FaLibavOpenStrategyAudio)
	if fctxLibav == nil {
		if misc.CancellableIsCancelled(ipc.mp.Cancellable) == 0 {
			errStr := ""
			if err != nil {
				errStr = err.Error()
			}
			ipc.ts.Trace(trace.TRACE_ERROR, "Radio", "Unable to open %s -- %s",
				ipc.url, errStr)
		}

		libav.FALibavClose(ipc.libavSys, avio)
		return -1
	}

	ipc.fctxLibav = fctxLibav
	// Non-owning view for stream info / time_base accessors — the C code
	// reads them through ipc->ipc_mf->fctx.
	fctx := medialibav.WrapFormatCtx(fctxLibav)
	ipc.fctx = fctx

	ipc.ts.Trace(trace.TRACE_DEBUG, "Radio", "Starting playback of %s", url)

	// C: mp_configure(ipc->ipc_mp,
	//     MP_CAN_PAUSE | MP_FLUSH_ON_HOLD | MP_ALWAYS_SATISFIED,
	//     MP_BUFFER_SHALLOW, 0, "radio")
	mediacore.MpConfigure(ipc.mp, ipc.pm,
		mediacore.MPCanPause|mediacore.MPFlushOnHold|mediacore.MPAlwaysSatisfied,
		mediacore.MPBufferShallow, 0, "radio")

	ipc.mp.Audio.Stream = -1
	ipc.mp.Video.Stream = -1

	ipc.mf = mediacore.MediaFormatCreate(fctxLibav)
	ipc.mc = nil

	for i := range fctx.GetNumStreams() {
		si := fctx.GetStreamInfo(i)

		if si == nil || si.CodecType != libav.AvmediaTypeAudio {
			continue
		}

		// C: ipc->ipc_mc = media_codec_create(ctx->codec_id, 0, ipc->ipc_mf,
		//     ctx, NULL, ipc->ipc_mp)
		ipc.mc = mediacore.MediaCodecCreate(mediacore.CodecID(si.CodecID), 0,
			ipc.mf, fctx.CodecCtxFromStream(i).CPtr(), nil, ipc.mp)
		ipc.mp.Audio.Stream = i
		break
	}

	if ipc.mc == nil {
		mediacore.MediaFormatDeref(ipc.mf)
		ipc.mf = nil
		ipc.ts.Trace(trace.TRACE_ERROR, "Radio",
			"Unable to open %s -- No audio stream", url)
		return -1
	}

	mediacore.MpBecomePrimary(ipc.mp, ipc.pm)
	ipc.streamInfoSet = false
	return 0
}

// closeStream — C: close_stream (icecast.c:376-387).
func (ipc *icecastPlayContext) closeStream() {
	if ipc.mc != nil {
		mediacore.MediaCodecDeref(ipc.mc)
	}
	if ipc.mf != nil {
		mediacore.MediaFormatDeref(ipc.mf)
	}
	// C: media_format_deref(fw) → fa_libav_close_format(fw->fctx) —
	// closes the AVFormatContext, the AVIOContext and the underlying
	// fa_handle (including the icymeta wrapper chain). The Go MediaFormat
	// is non-owning, so the format close is explicit here.
	if ipc.fctxLibav != nil {
		libav.FALibavCloseFormat(ipc.libavSys, ipc.fctxLibav, false)
		ipc.fctxLibav = nil
		ipc.fctx = nil
	}
	ipc.mc = nil
	ipc.mf = nil
	ipc.flushSources()
}

// rescale — C: rescale (icecast.c:426-431). av_rescale_q from the stream
// time_base to AV_TIME_BASE_Q (microseconds).
func (ipc *icecastPlayContext) rescale(ts int64, si int) int64 {
	if ts == mediacore.PTSUnset {
		return mediacore.PTSUnset
	}
	s := ipc.fctx.GetStreamInfo(si)
	if s == nil || s.TimeBaseDen == 0 {
		return mediacore.PTSUnset
	}
	return libav.AVRescaleQ(ts,
		libav.AVRational{Num: s.TimeBaseNum, Den: s.TimeBaseDen},
		libav.AVRational{Num: 1, Den: 1000000})
}

// eventData unwraps the *event.Event carried by a MediaEvent — C: the
// event_t itself (same helper as fa_audio's mediaEventData).
func eventData(me *mediacore.MediaEvent) *event.Event {
	if me == nil {
		return nil
	}
	if ev, ok := me.Data.(interface{ AsEvent() *event.Event }); ok {
		return ev.AsEvent()
	}
	if ev, ok := me.Data.(*event.Event); ok {
		return ev
	}
	return nil
}

// eventIsType — C: event_is_type.
func eventIsType(me *mediacore.MediaEvent, t event.EventType) bool {
	return me != nil && me.Type == int(t)
}

// eventIsAction — C: event_is_action.
func eventIsAction(me *mediacore.MediaEvent, at event.ActionType) bool {
	ev := eventData(me)
	return ev != nil && event.IsAction(ev, at)
}

// streamRadio — C: stream_radio (icecast.c:439-590). Runs the packet
// pump: opens streams, feeds MB_AUDIO bufs to the audio queue and reacts
// to hold/navigation events. Returns the terminating event (never NULL
// in practice — EOF paths fabricate EVENT_EOF).
func (ipc *icecastPlayContext) streamRadio() *mediacore.MediaEvent {
	var mb *mediacore.MediaBuf
	var e *mediacore.MediaEvent
	mp := ipc.mp
	mq := mp.Audio

	// C: prop_set(mp->mp_prop_root, "format", PROP_SET_STRING, "Shoutcast")
	ipc.pm.SetVEx(nil, mp.PropRoot, "format", "Shoutcast")

	// C: usage_event("Play audio", 1, USAGE_SEG("format", "icecast"))
	ipc.usage.Event("Play audio", 1, "format", "icecast")

	// C: ipc->ipc_radio_info = prop_create(mp->mp_prop_root, "radioinfo")
	ipc.radioInfo = ipc.pm.CreateEx(mp.PropRoot, "radioinfo", nil, false, false)

	// C: TAILQ_INIT(&ipc->ipc_sources)
	ipc.sources = nil
	ipc.nsources = 0

	// C: http_header_add(&ipc->ipc_request_headers, "Icy-MetaData", "1", 1)
	ipc.requestHeaders.Add("Icy-MetaData", "1", true)

	loading := 0

	// C: AVPacket pkt on the function stack — allocated once, its payload
	// freed per use with av_free_packet.
	pkt := libav.AvPacketAlloc()
	defer libav.AvPacketFree(pkt)

	for {

		// Need to fetch a new packet ?
		if mb == nil {

			if ipc.mf == nil {

				if ipc.hold != 0 {
					e = mediacore.MpDequeueEvent(mp)
					goto handleEvent
				}

				ipc.pm.SetVEx(nil, mp.PropRoot, "loading", 1)
				loading = 1

				if ipc.openStream() != 0 {
					e = mediacore.MpDequeueEventDeadline(mp, 1000)
					if e != nil {
						goto handleEvent
					}
					continue
				}
			}

			atomic.StoreInt32(&mp.EOF, 0) // C: mp->mp_eof = 0

			r := libav.AvReadFrameCode(ipc.fctxLibav, pkt)
			if r == libav.AverrorEagain {
				continue
			}

			if r != 0 {
				if r != libav.AverrorEOF {
					msg := make([]byte, 100)
					libav.FALibavErrorToTxt(r, msg, len(msg))
					ipc.ts.Trace(trace.TRACE_ERROR, "Radio",
						"Playback error: %s (%d)", nulString(msg), r)
				}
				ipc.closeStream()

				for {
					e = mediacore.MpWaitForEmptyQueues(mp)
					if e == nil {
						break
					}
					if eventIsType(e, event.EVENT_PLAYQUEUE_JUMP) ||
						eventIsAction(e, event.ACTION_SKIP_BACKWARD) ||
						eventIsAction(e, event.ACTION_SKIP_FORWARD) ||
						eventIsAction(e, event.ACTION_STOP) {
						mediacore.MpFlush(mp)
						break
					}
					// C: event_release(e) — GC
				}

				if e == nil || r == libav.AverrorEOF {
					e = &mediacore.MediaEvent{
						Type: int(event.EVENT_EOF),
						Data: &event.Event{Type: event.EVENT_EOF},
					}
					break
				}
				if e != nil {
					break
				}
				continue
			}

			pts, dts, duration, si, size, data := libav.AvPacketFieldsPtr(pkt.CPtr())

			if si != mp.Audio.Stream {
				// C: av_free_packet(&pkt)
				libav.AvPacketUnref(pkt)
				continue
			}

			mb = mediacore.MediaBufAllocUnlocked(mp, size)
			mb.DataType = int(mediacore.MBAudio)

			mb.PTS = ipc.rescale(pts, si)
			mb.DTS = ipc.rescale(dts, si)
			mb.Duration = ipc.rescale(duration, si)

			// C: mb->mb_cw = media_codec_ref(ipc->ipc_mc)
			mb.Codec = mediacore.MediaCodecRef(ipc.mc)

			// Move the data pointers from libav's packet
			mb.Stream = si

			copy(mb.Data, data)

			if mb.PTS != mediacore.PTSUnset {
				offset := ipc.fctx.GetStartTime()
				if offset == mediacore.PTSUnset {
					offset = 0
				}
				mb.UserTime = mb.PTS + offset
				mb.DriveClock = 1
			}

			// C: av_free_packet(&pkt)
			libav.AvPacketUnref(pkt)
		}

		// Try to send the buffer.  If mb_enqueue() returns something we
		// catched an event instead of enqueueing the buffer. In this case
		// 'mb' will be left untouched.

		if mb == mbSpecialEOF {
			// We have reached EOF, drain queues
			e = mediacore.MpWaitForEmptyQueues(mp)
			if e == nil {
				e = &mediacore.MediaEvent{
					Type: int(event.EVENT_EOF),
					Data: &event.Event{Type: event.EVENT_EOF},
				}
				break
			}

		} else if e = mediacore.MbEnqueueWithEvents(mp, mq, mb); e == nil {
			mb = nil // Enqueue succeeded
			if loading != 0 {
				ipc.pm.SetVEx(nil, mp.PropRoot, "loading", 0)
				loading = 0
			}
			continue
		}

	handleEvent:
		if eventIsType(e, event.EVENT_HOLD) {

			ei, _ := event.ConcreteOf(e.Data).(*event.EventInt)

			ipc.hold = ei.Val
			if ipc.hold != 0 && ipc.mf != nil {
				ipc.closeStream()

				if mb != nil && mb != mbSpecialEOF {
					mediacore.MediaBufFreeUnlocked(mp, mb)
					mb = nil
				}
			}

		} else if eventIsType(e, event.EVENT_PLAYQUEUE_JUMP) ||
			eventIsAction(e, event.ACTION_SKIP_BACKWARD) ||
			eventIsAction(e, event.ACTION_SKIP_FORWARD) ||
			eventIsAction(e, event.ACTION_STOP) {
			mediacore.MpFlush(mp)
			break
		}
		// C: event_release(e) — GC
	}

	// C: prop_set_void(ipc->ipc_radio_info)
	ipc.pm.SetVoidEx(ipc.radioInfo, nil)

	if mb != nil && mb != mbSpecialEOF {
		mediacore.MediaBufFreeUnlocked(mp, mb)
	}

	ipc.closeStream()

	// C: http_headers_free(&ipc->ipc_request_headers);
	//    http_headers_free(&ipc->ipc_response_headers);
	ipc.requestHeaders.Free()
	ipc.responseHeaders.Free()

	return e
}

// icecastThread — C: icecast_thread (icecast.c:596-610). Owns its own
// media pipe created with MP_PRIMABLE.
func icecastThread(aux any) any {
	ipc := aux.(*icecastPlayContext)
	ipc.mp = mediacore.MpCreate(ipc.mediaSys, ipc.pm, "radio", mediacore.MPPrimaable)
	if ipc.mp != nil {
		ipc.mp.FAM = ipc.fam
	}
	if ipc.streamRadio() == nil {
		ipc.ts.Trace(trace.TRACE_ERROR, "Radio", "Error: stream failed")
	}
	mediacore.MpShutdown(ipc.mp)
	mediacore.MpDestroy(ipc.mp)

	// C: free((void *)ipc->ipc_url); free(ipc); — GC
	return nil
}

// icecastOpen — C: icecast_open (icecast.c:615-625), the be_open entry
// point used when navigating to an icecast: page. The page prop is not
// touched — playback starts in a detached thread.
func icecastOpen(bs *backendcore.BackendSystem, url string, sync bool) error {
	ipc := newIcecastPlayContext(bs.Usage(), bs.GetPropManager(),
		bs.GetFileAccessManager(), libav.GetGlobalLibAVSystem(), bs.TraceSystem(),
		bs.MediaSys().DefaultCharsetSrc(), bs.Gconf(), bs.MediaSys())
	// C: ipc->ipc_url = strdup(url + strlen("icecast:"))
	ipc.url = url[len("icecast:"):]
	bs.Usage().PageOpen(sync, "Icecast")
	// C: hts_thread_create_detached("icecast", icecast_thread, ipc,
	//     THREAD_PRIO_MODEL)
	arch.ThreadCreateDetached("icecast", icecastThread, ipc,
		arch.ThreadPrioModel)
	return nil
}

// icecastCanHandle — C: icecast_canhandle (icecast.c:630-634).
// !strncmp(url, "icecast:", 8) — the payload keeps its own scheme, e.g.
// "icecast:http://host/stream.pls" → "http://host/stream.pls".
func icecastCanHandle(url string) int {
	if strings.HasPrefix(url, "icecast:") {
		return 1
	}
	return 0
}

// icecastPlayAudio — C: icecast_play_audio (icecast.c:639-649), the
// be_play_audio entry point driven by the playqueue. Reuses the caller's
// media pipe; the `hold`/`paused` parameter is ignored exactly like C
// (ipc is zero-initialized).
func icecastPlayAudio(bs *backendcore.BackendSystem, url string,
	mediaPipe any, paused bool,
	mimetype string) (any, error) {
	ipc := newIcecastPlayContext(bs.Usage(), bs.GetPropManager(),
		bs.GetFileAccessManager(), libav.GetGlobalLibAVSystem(), bs.TraceSystem(),
		bs.MediaSys().DefaultCharsetSrc(), bs.Gconf(), bs.MediaSys())
	mp, _ := mediaPipe.(*mediacore.MediaPipe)
	if mp == nil {
		return nil, nil
	}
	ipc.url = url[len("icecast:"):]
	ipc.mp = mp
	// C: return stream_radio(&ipc, errbuf, sizeof(errlen)) — the C code
	// passes sizeof(errlen) (=8) instead of errlen; the bug is harmless
	// because stream_radio never writes to errbuf. Preserved for parity.
	return ipc.streamRadio(), nil
}

// RegisterIcecastBackend — C: static backend_t be_icecast + BE_REGISTER
// (icecast.c:654-667). A static backend: no prefix, no flags — resolution
// goes through be_canhandle exactly like C.
func RegisterIcecastBackend(bs *backendcore.BackendSystem) {
	be := &backendcore.Backend{}
	be.CanHandle = icecastCanHandle
	be.Open = func(page any, url string, sync bool) error {
		return icecastOpen(bs, url, sync)
	}
	be.PlayAudio = func(url string, mediaPipe any,
		paused bool, mimetype string, opaque any) (any, error) {
		return icecastPlayAudio(bs, url, mediaPipe, paused, mimetype)
	}
	bs.Register(be)
}

// icyMeta — C: icymeta_t + fa_protocol_icymeta (icecast.c:692-845). A
// fa_handle wrapper that strips inline ICY metadata blocks out of the
// audio stream (server advertises them via the icy-metaint header when
// the client sends Icy-MetaData: 1).
type icyMeta struct {
	src    *fileaccesscore.Handle // C: s_src
	stride int                    // C: stride
	remain int                    // C: remain
	ipc    *icecastPlayContext    // C: ipc
}

// errIcyMetaRead — C: icymeta_read returning -1 on any failure.
var errIcyMetaRead = errors.New("icymeta: read error")

// Read — C: icymeta_read (icecast.c:786-819). Fills buf completely;
// between every `stride` audio bytes sits one length byte, followed by
// length*16 bytes of metadata fed to icymeta_parse. Any short read or
// error is fatal (C returns -1).
func (s *icyMeta) Read(buf []byte) (int, error) {
	offset := 0
	size := len(buf)

	for size > 0 {

		if s.remain == 0 {
			lb := make([]byte, 1)
			if n, _ := fileaccesscore.Read(s.src, lb); n != 1 {
				return 0, errIcyMetaRead
			}

			if lb[0] != 0 {
				mlen := int(lb[0]) * 16
				mbuf := make([]byte, mlen)
				if n, _ := fileaccesscore.Read(s.src, mbuf); n != mlen {
					return 0, errIcyMetaRead
				}
				icyMetaParse(s.ipc, mbuf)
			}
			s.remain = s.stride
		}

		toRead := min(s.remain, size)

		n, _ := fileaccesscore.Read(s.src, buf[offset:offset+toRead])
		if n != toRead {
			return 0, errIcyMetaRead
		}

		offset += n
		size -= n
		s.remain -= n
	}
	return offset, nil
}

// Close — C: icymeta_close (icecast.c:700-705) → fa_close(s->s_src).
func (s *icyMeta) Close() error {
	return s.src.Close()
}

// Seek — C: icymeta_seek (icecast.c:711-714) returns -1.
func (s *icyMeta) Seek(offset int64, whence int) (int64, error) {
	return -1, fmt.Errorf("icymeta: seek not supported")
}

// Size — C: icymeta_fsize (icecast.c:720-723) returns -1.
func (s *icyMeta) Size() int64 {
	return -1
}

// icyMetaParse — C: icymeta_parse (icecast.c:730-777). Extracts
// StreamTitle='...'; from the metadata block, charset-decodes it via
// rstr_from_bytes_len and publishes to the radioinfo prop — first time
// directly, later via the audio queue (mp_send_prop_set_string).
func icyMetaParse(ipc *icecastPlayContext, buf []byte) {
	// C: buf is a NUL-terminated char* — strlen/strstr stop at the first
	// NUL. The Go string keeps embedded NULs, so cut the C view first.
	str := string(buf)
	if i := strings.IndexByte(str, 0); i >= 0 {
		str = str[:i]
	}

	if ipc.gconf.EnableIcecastDebug.Load() {
		// C: hexdump("icymeta", buf, strlen(buf))
		ipc.ts.HexDump("icymeta", []byte(str))
	}

	// C: const char *title = mystrstr(buf, "StreamTitle='") — mystrstr is
	// the case-insensitive strstr.
	title := misc.Mystrstr(str, "StreamTitle='")
	if title == "" {
		return
	}
	title = title[len("StreamTitle='"):]
	end := strings.Index(title, "';")
	if end < 0 {
		return
	}

	tlen := end
	// C: rstr_t *t = rstr_from_bytes_len(title, tlen, how, sizeof(how))
	t, how := misc.RstrFromBytesLen([]byte(title[:tlen]), tlen, ipc.charsetSrc)

	if ipc.gconf.EnableIcecastDebug.Load() {
		ipc.ts.Trace(trace.TRACE_DEBUG, "Radio", "Title decoded as '%s' to '%s'",
			how, misc.RstrGet(t))
	}

	// C: const char *title_tag = strstr(rstr_get(t), "<mus_sng_title>")
	ts := misc.RstrGet(t)
	if i := strings.IndexByte(ts, 0); i >= 0 {
		ts = ts[:i]
	}
	if _, after, ok := strings.Cut(ts, "<mus_sng_title>"); ok {
		tag := after
		if tagEnd := strings.Index(tag, "</mus_sng_title>"); tagEnd >= 0 {
			// C: rstr_t *n = rstr_allocl(title_tag, title_tag_end - title_tag)
			n := misc.RstrAllocl(&tag, tagEnd)
			misc.RstrRelease(t)
			t = n
		}
	}

	if !ipc.streamInfoSet {
		// C: prop_set_rstring(ipc->ipc_radio_info, t)
		ipc.radioInfo.SetString(misc.RstrGet(t))
		ipc.streamInfoSet = true
	} else {
		// C: mp_send_prop_set_string(ipc->ipc_mp, &ipc->ipc_mp->mp_audio,
		//     ipc->ipc_radio_info, rstr_get(t))
		mediacore.MpSendPropSetString(ipc.mp, ipc.mp.Audio,
			ipc.radioInfo, misc.RstrGet(t))
	}
	misc.RstrRelease(t)
}

// icyMetaParser — C: icy_meta_parser (icecast.c:831-845). Wraps src in an
// icymeta reader that gets handed to fa_libav_reopen in place of fh.
func icyMetaParser(ipc *icecastPlayContext, src *fileaccesscore.Handle,
	stride int) *icyMeta {
	return &icyMeta{
		src:    src,
		remain: stride,
		stride: stride,
		ipc:    ipc,
	}
}

// nulString converts a NUL-terminated byte buffer to a Go string —
// C string semantics for errbuf-style outputs.
func nulString(b []byte) string {
	if before, _, ok := bytes.Cut(b, []byte{0}); ok {
		return string(before)
	}
	return string(b)
}

// GconfEnableIcecastDebug — C: gconf.enable_icecast_debug (main.h:251),

// Wired by cmd/movian-go init; nil-safe methods make unwired calls no-ops.

// gcfg.EnableIcecastDebug — C: gconf field (see devsettings binding).

// SetEnableIcecastDebug — C: gconf field written by settings.c dev bool.
