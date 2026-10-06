// Canonical port of src/fileaccess/fa_video.c — be_file_playvideo /
// be_file_playvideo_fh / video_player_loop: the file backend's video
// demux/feed loop. Opens the URL via fileaccess, hands it to libav via
// AVIO, selects default streams, creates codecs, builds seek/chapter
// indexes, and pumps MB_VIDEO/MB_AUDIO/MB_SUBTITLE buffers into
// mp->mp_video / mp->mp_audio until EOF or a stop/seek/skip event.

package core

import (
	"crypto/md5"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/czz/movian-go/internal/event"
	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/fileaccess/scanner"
	"github.com/czz/movian-go/internal/htsmsg"
	"github.com/czz/movian-go/internal/libav"
	mediacore "github.com/czz/movian-go/internal/media/core"
	medialibav "github.com/czz/movian-go/internal/media/libav"
	"github.com/czz/movian-go/internal/metadata"
	"github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/subtitles"
	"github.com/czz/movian-go/internal/trace"
	videopkg "github.com/czz/movian-go/internal/video"
)

// seekItemT — C: seek_item_t (fa_video.c:52-56)
type seekItemT struct {
	prop  *propcore.Prop // C: si_prop
	start int            // C: si_start (seconds)
}

// seekIndexT — C: seek_index_t (fa_video.c:49-62)
type seekIndexT struct {
	root    *propcore.Prop // C: si_root
	nItems  int            // C: si_nitems
	current *seekItemT     // C: si_current
	items   []seekItemT    // C: si_items[0]
}

// faVideoRescale — C: rescale (fa_video.c:88-96) — av_rescale_q from the
// stream's time_base to AV_TIME_BASE_Q.
func faVideoRescale(f *medialibav.AVFormatCtx, ts int64, si int) int64 {
	if ts == mediacore.PTSUnset {
		return mediacore.PTSUnset
	}
	si2 := f.GetStreamInfo(si)
	if si2 == nil || si2.TimeBaseDen == 0 {
		return mediacore.PTSUnset
	}
	return libav.AVRescaleQ(ts,
		libav.AVRational{Num: si2.TimeBaseNum, Den: si2.TimeBaseDen},
		libav.AVRational{Num: 1, Den: 1000000})
}

// videoSeek — C: video_seek (fa_video.c:102-128).
func (bs *BackendSystem) videoSeek(fctx *medialibav.AVFormatCtx,
	mp *mediacore.MediaPipe, mbp **mediacore.MediaBuf, pos int64, txt string) {
	duration := fctx.GetDuration()
	if pos > duration {
		pos = duration
	}
	if pos < 0 {
		pos = 0
	}
	pos += fctx.GetStartTime()

	if err := fctx.SeekFrame(pos); err != nil {
		bs.traceSystem.Trace(trace.TRACE_ERROR, "Video", "Seek failed")
	}

	mp.Video.SeekTarget = pos
	mp.Audio.SeekTarget = pos

	mediacore.MpFlush(mp)

	if *mbp != nil && *mbp != mbSpecialEOF {
		mediacore.MediaBufFreeUnlocked(mp, *mbp)
	}
	*mbp = nil

	pos -= fctx.GetStartTime()

	// C: prop_set(mp->mp_prop_root, "seektime", PROP_SET_FLOAT,
	//   pos / 1000000.0)
	mpSetRootPropFloat(bs.propManager, mp, "seektime",
		float64(pos)/1000000.0)
}

// updateSeekIndex — C: update_seek_index (fa_video.c:135-150).
func updateSeekIndex(si *seekIndexT, sec int) {
	if si == nil || si.nItems == 0 {
		return
	}
	j := 0
	for i := range si.nItems {
		j = i
		if si.items[i].start > sec {
			break
		}
	}
	if si.current != &si.items[j] {
		si.current = &si.items[j]
		// C: prop_suggest_focus(si->si_current->si_prop)
		if pm := si.current.prop.Manager(); pm != nil {
			pm.SuggestFocus(si.current.prop)
		}
	}
}

// mpSetRootProp* — C: prop_set(mp->mp_prop_root, name, ...) helpers.
func mpSetRootPropInt(pm *propcore.PropManager, mp *mediacore.MediaPipe,
	name string, v int) {
	if pm == nil || mp.PropRoot == nil {
		return
	}
	p := pm.Find(mp.PropRoot, name)
	if p == nil {
		p = pm.CreateEx(mp.PropRoot, name, nil, false, true)
	}
	if p != nil {
		p.SetInt(v)
	}
}

func mpSetRootPropFloat(pm *propcore.PropManager, mp *mediacore.MediaPipe,
	name string, v float64) {
	if pm == nil || mp.PropRoot == nil {
		return
	}
	p := pm.Find(mp.PropRoot, name)
	if p == nil {
		p = pm.CreateEx(mp.PropRoot, name, nil, false, true)
	}
	if p != nil {
		p.SetFloat(float32(v))
	}
}

func mpSetRootPropString(pm *propcore.PropManager, mp *mediacore.MediaPipe,
	name string, v string) {
	if pm == nil || mp.PropRoot == nil {
		return
	}
	p := pm.Find(mp.PropRoot, name)
	if p == nil {
		p = pm.CreateEx(mp.PropRoot, name, nil, false, true)
	}
	if p != nil {
		p.SetString(v)
	}
}

func setChildPropString(p *propcore.Prop, name, v string) {
	if p == nil {
		return
	}
	if c := p.FindChild(name); c != nil {
		c.SetString(v)
		return
	}
	if pm := p.Manager(); pm != nil {
		if c := pm.CreateEx(p, name, nil, false, true); c != nil {
			c.SetString(v)
		}
	}
}

func setChildPropFloat(p *propcore.Prop, name string, v float64) {
	if p == nil {
		return
	}
	if c := p.FindChild(name); c != nil {
		c.SetFloat(float32(v))
		return
	}
	if pm := p.Manager(); pm != nil {
		if c := pm.CreateEx(p, name, nil, false, true); c != nil {
			c.SetFloat(float32(v))
		}
	}
}

// attachment — C: attachment_t (fa_video.c:66-70)
type attachmentT struct {
	dtor   func(any)
	opaque any
}

// attachmentAddDtor — C: attachment_add_dtor (fa_video.c:950-958)
func attachmentAddDtor(alist *[]*attachmentT, fn func(any), opaque any) {
	*alist = append(*alist, &attachmentT{dtor: fn, opaque: opaque})
}

// attachmentLoadSlice — C: attachment_load_slice (fa_video.c:965-983)
func (bs *BackendSystem) attachmentLoadSlice(alist *[]*attachmentT,
	url string, offset int64, size int, fontDomain int, source string) {
	if size < 20 {
		return
	}
	if bs.freetype == nil {
		return
	}
	fh, err := fileaccesscore.FAOpenEx(bs.fileAccessManager, url, 0, nil)
	if fh == nil {
		bs.traceSystem.Trace(trace.TRACE_ERROR, "Video",
			"Unable to open attachement -- %v", err)
		return
	}
	sfh := fileaccesscore.FASliceOpen(fh, offset, int64(size))
	data := make([]byte, size)
	n, _ := io.ReadFull(sfh, data)
	fileaccesscore.FAClose(sfh)
	if n <= 0 {
		return
	}
	// C: freetype_load_dynamic_font_fh(fh, url, font_domain, NULL, 0)
	h := bs.freetype.LoadDynamicFontBuf(data[:n], fontDomain, source)
	if h != nil {
		attachmentAddDtor(alist, bs.freetype.UnloadFont, h)
	}
}

// attachmentLoadBuf — C: attachment_load_buf (fa_video.c:989-999)
func (bs *BackendSystem) attachmentLoadBuf(alist *[]*attachmentT, b []byte, fontDomain int,
	source string) {
	if len(b) < 20 || bs.freetype == nil {
		return
	}
	h := bs.freetype.LoadDynamicFontBuf(b, fontDomain, source)
	if h != nil {
		attachmentAddDtor(alist, bs.freetype.UnloadFont, h)
	}
}

// attachmentUnloadAll — C: attachment_unload_all (fa_video.c:1006-1014)
func attachmentUnloadAll(alist []*attachmentT) {
	for _, a := range alist {
		a.dtor(a.opaque)
	}
}

// computeHash — C: compute_hash (fa_video.c:1024-1082) — opensubtitles.org
// + subdb hashes over the first+last 64KiB of the file.
func computeHash(fh *fileaccesscore.Handle, va *VideoArgs) {
	size, err := fileaccesscore.FSize(fh)
	if err != nil || size < 65536 {
		return
	}
	hash := uint64(size)

	if _, err := fileaccesscore.FASeek(fh, 0, io.SeekStart); err != nil {
		return
	}

	mem := make([]byte, 65536)
	h := md5.New()

	if _, err := io.ReadFull(fh, mem); err != nil {
		return
	}
	h.Write(mem)
	for i := range 8192 {
		hash += binaryLE64(mem[i*8:])
	}

	var subdb [16]byte
	if _, err := fileaccesscore.FASeekLazy(fh, -65536, io.SeekEnd); err != nil {
		copy(subdb[:], h.Sum(nil)) // C: md5_final then early return
		copy(va.SubDBHash[:], subdb[:])
		return
	}
	if _, err := io.ReadFull(fh, mem); err != nil {
		copy(subdb[:], h.Sum(nil))
		copy(va.SubDBHash[:], subdb[:])
		return
	}
	h.Write(mem)
	for i := range 8192 {
		hash += binaryLE64(mem[i*8:])
	}
	copy(va.SubDBHash[:], h.Sum(nil))
	va.OpenSubHash = hash
	va.HashValid = true
}

func binaryLE64(b []byte) uint64 {
	// C: little-endian host → hash += mem[i] (native u64 loads)
	return uint64(b[0]) | uint64(b[1])<<8 | uint64(b[2])<<16 |
		uint64(b[3])<<24 | uint64(b[4])<<32 | uint64(b[5])<<40 |
		uint64(b[6])<<48 | uint64(b[7])<<56
}

// videoPlayerLoop — C: video_player_loop (fa_video.c:160-381).
func (bs *BackendSystem) videoPlayerLoop(fctx *medialibav.AVFormatCtx,
	cwvec []*mediacore.MediaCodec, mp *mediacore.MediaPipe, flags int,
	canonicalURL string, freetypeContext int,
	sidx, cidx *seekIndexT, fh *fileaccesscore.Handle,
	resumeMode int, title string, vpi *htsmsg.HTSMsg,
	origin *propcore.Prop) *mediacore.MediaEvent {

	var mb *mediacore.MediaBuf
	var mq *mediacore.MediaQueue
	var e *mediacore.MediaEvent

	lastsec := -1
	restartposLast := -1
	lastTimestampPresented := mediacore.PTSUnset

	// C: mp->mp_seek_base = 0; seektargets = AV_NOPTS_VALUE
	mp.SeekBase = 0
	mp.Video.SeekTarget = mediacore.PTSUnset
	mp.Audio.SeekTarget = mediacore.PTSUnset

	var start int64
	if mp.Flags&mediacore.MPCanSeek != 0 {
		// C: playinfo_get_restartpos(...) * 1000
		start = bs.metadata.PlayInfoGetRestartPos(canonicalURL, title,
			resumeMode) * 1000
		if start != 0 {
			bs.traceSystem.Trace(trace.TRACE_DEBUG, "VIDEO",
				"Attempting to resume from %.2f seconds",
				float64(start)/1000000.0)
			mp.SeekBase = start
			bs.videoSeek(fctx, mp, &mb, start, "restart position")
		}
	}

	// C: prop_set_float_ex(mp->mp_prop_currenttime, mp->mp_sub_currenttime,
	//   start / 1000000.0)
	if bs.propManager != nil && mp.PropCurrentTime != nil {
		bs.propManager.SetFloatEx(mp.PropCurrentTime, mp.SubCurrentTime,
			float32(start)/1000000.0)
	}

	duration := fctx.GetDuration()
	if duration != mediacore.PTSUnset {
		// C: htsmsg_add_dbl(vpi, "duration", ...)
		vpi.AddDbl("duration", float64(duration)/1000000.0)
	}

	// C: video_playback_info_invoke(VPI_START, vpi, mp->mp_prop_root, origin)
	if bs.playbackInfoFn != nil {
		bs.playbackInfoFn(videopkg.VPIStart, vpi, mp.PropRoot, origin)
	}

	offset := fctx.GetStartTime()
	if offset == mediacore.PTSUnset {
		offset = 0
	}

	for {
		// C: need a new packet?
		if mb == nil {
			atomic.StoreInt32(&mp.EOF, 0)

			// C: fa_deadline(fh, mp->mp_buffer_delay != INT32_MAX ?
			//   mp->mp_buffer_delay / 3 : 0) — C reads the field
			// unlocked; Go takes mp.Mutex to stay race-clean.
			mp.Mutex.Lock()
			bufferDelay := mp.BufferDelay
			mp.Mutex.Unlock()
			if bufferDelay != math.MaxInt32 {
				fileaccesscore.FADeadline(fh, int(bufferDelay/3))
			} else {
				fileaccesscore.FADeadline(fh, 0)
			}

			pktPtr, rerr := fctx.ReadPacketRaw()
			if rerr != nil {
				// C: av_strerror + TRACE "Playback reached EOF" — any
				// non-EAGAIN av_read_frame error enters the EOF state.
				bs.traceSystem.Trace(trace.TRACE_DEBUG, "Video",
					"Playback reached EOF: %v", rerr)
				mb = mbSpecialEOF
				atomic.StoreInt32(&mp.EOF, 1)
				continue
			}
			if pktPtr == nil {
				// C: EOF
				mb = mbSpecialEOF
				atomic.StoreInt32(&mp.EOF, 1)
				continue
			}

			si := packetStreamIndex(pktPtr)
			if si >= len(cwvec) {
				libav.AvPacketFree(pktPtr)
				continue
			}

			streamInfo := fctx.GetStreamInfo(si)
			pts, dts, dur, _ := packetFields(pktPtr)

			mp.Mutex.Lock()
			curVideo := mp.Video.Stream
			mp.Mutex.Unlock()
			if si == curVideo {
				// C: current video stream
				mb = mediacore.MediaBufFromAVPkt(mp, pktPtr)
				mb.DataType = int(mediacore.MBVideo)
				mq = mp.Video

				num, den := fctx.GetStreamAvgFrameRate(si)
				if num != 0 {
					mb.Duration = 1000000 * int64(den) / int64(num)
				} else {
					mb.Duration = faVideoRescale(fctx, dur, si)
				}
				mp.Framerate = mediacore.AVRational{Num: num, Den: den}

			} else if streamInfo != nil &&
				streamInfo.CodecType == medialibav.AVMediaTypeAudio {

				mb = mediacore.MediaBufFromAVPkt(mp, pktPtr)
				mb.DataType = int(mediacore.MBAudio)
				mq = mp.Audio

			} else if streamInfo != nil &&
				streamInfo.CodecType == medialibav.AVMediaTypeSubtitle {

				// C: int duration = pkt.convergence_duration ?:
				//   pkt.duration (FFmpeg ≥7: merged into pkt.duration)
				duration2 := medialibav.PacketConvergenceDuration(pktPtr)
				if duration2 == 0 {
					duration2 = dur
				}

				mb = mediacore.MediaBufFromAVPkt(mp, pktPtr)
				mb.CodecID = mediacore.CodecID(streamInfo.CodecID)
				mb.FontContext = freetypeContext
				mb.DataType = int(mediacore.MBSubtitle)
				mq = mp.Video

				mb.Duration = faVideoRescale(fctx, duration2, si)

			} else {
				// C: bad: — not a consumed stream
				libav.AvPacketFree(pktPtr)
				continue
			}

			mb.PTS = faVideoRescale(fctx, pts, si)
			mb.DTS = faVideoRescale(fctx, dts, si)

			if mq.SeekTarget != mediacore.PTSUnset &&
				mb.DataType != int(mediacore.MBSubtitle) {
				ts := mb.PTS
				if ts == mediacore.PTSUnset {
					ts = mb.DTS
				}
				if ts < mq.SeekTarget {
					mb.Flags.Skip = true
				} else {
					mq.SeekTarget = mediacore.PTSUnset
				}
			}

			// C: media_discontinuity_debug — dead code in C
			//   (returns immediately); no-op here.

			if cwvec[si] != nil {
				mb.Codec = mediacore.MediaCodecRef(cwvec[si])
			}
			mb.Stream = si

			if mb.DataType == int(mediacore.MBVideo) {
				mb.DriveClock = 2
				mb.UserTime = offset
			}

			mb.Flags.Keyframe = medialibav.PacketFlags(pktPtr)&libav.AVPktFlagKey != 0
			libav.AvPacketFree(pktPtr)
		}

		// C: try to send the buffer; event caught instead
		if mb == mbSpecialEOF {
			// C: wait for queues to drain
			e = mediacore.MpWaitForEmptyQueues(mp)
			if e == nil {
				e = &mediacore.MediaEvent{
					Type: int(event.EVENT_EOF),
					Data: &event.Event{Type: event.EVENT_EOF},
				}
				break
			}
		} else {
			e = mediacore.MbEnqueueWithEvents(mp, mq, mb)
			if e == nil {
				mb = nil // enqueue succeeded
				continue
			}
		}

		if faEventIsType(e, event.EVENT_CURRENT_TIME) {
			if ets, ok := event.ConcreteOf(e.Data).(*event.EventTs); ok {
				if int64(ets.Epoch) == mp.Epoch &&
					mp.Flags&mediacore.MPCanSeek != 0 {
					sec := int(ets.Ts / 1000000)
					lastTimestampPresented = ets.Ts

					// C: update restartpos every 5 seconds
					if sec < restartposLast || sec >= restartposLast+5 {
						restartposLast = sec
						bs.metadata.PlayInfoSetRestartPos(canonicalURL,
							ets.Ts/1000, true)
					}
					if sec != lastsec {
						lastsec = sec
						updateSeekIndex(sidx, sec)
						updateSeekIndex(cidx, sec)
					}
				}
			}
		} else if faEventIsType(e, event.EVENT_SEEK) {
			if ets, ok := event.ConcreteOf(e.Data).(*event.EventTs); ok {
				bs.videoSeek(fctx, mp, &mb, ets.Ts, "direct")
			}
		} else if faEventIsAction(e, event.ACTION_SKIP_FORWARD) ||
			faEventIsAction(e, event.ACTION_SKIP_BACKWARD) ||
			faEventIsType(e, event.EVENT_EXIT) ||
			faEventIsType(e, event.EVENT_PLAY_URL) {
			break
		}
		e = nil // C: event_release(e)
	}

	if mb != nil && mb != mbSpecialEOF {
		mediacore.MediaBufFreeUnlocked(mp, mb)
	}

	if duration != mediacore.PTSUnset {
		// C: stop position in percent of video length
		var spp int64
		if duration > 0 {
			spp = mp.SeekBase * 100 / duration
		}
		if spp >= int64(mp.Sys.VS.PlayedThreshold) ||
			faEventIsType(e, event.EVENT_EOF) {
			bs.metadata.PlayInfoSetRestartPos(canonicalURL, -1, false)
			bs.metadata.PlayInfoRegisterPlay(canonicalURL, 1)
			bs.traceSystem.Trace(trace.TRACE_DEBUG, "Video",
				"Playback reached %d%%, counting as played (%s)",
				spp, canonicalURL)
		} else if lastTimestampPresented != mediacore.PTSUnset {
			vpi.AddDbl("stopposition",
				float64(lastTimestampPresented)/1000000.0)
			bs.metadata.PlayInfoSetRestartPos(canonicalURL,
				lastTimestampPresented/1000, false)
		}
	} else {
		// C: wipe bad db records (#2503)
		bs.metadata.PlayInfoSetRestartPos(canonicalURL, -1, false)
	}

	return e
}

// buildIndex — C: build_index (fa_video.c:386-444) — minute-by-minute
// thumbnail seek index under mp_prop_root.seekindex.positions.
func (bs *BackendSystem) buildIndex(mp *mediacore.MediaPipe,
	fctx *medialibav.AVFormatCtx, url string) *seekIndexT {
	duration := fctx.GetDuration()
	if duration == mediacore.PTSUnset || bs.propManager == nil ||
		mp.PropRoot == nil {
		return nil
	}

	items := 1 + int(duration/60000000)

	si := &seekIndexT{nItems: items, items: make([]seekItemT, items)}
	si.root = bs.propManager.CreateEx(mp.PropRoot, "seekindex", nil, false, true)
	parent := bs.propManager.CreateEx(si.root, "positions", nil, false, true)

	if avail := bs.propManager.CreateEx(si.root, "available", nil, false, true); avail != nil {
		avail.SetInt(1)
	}

	for i := range items {
		item := &si.items[i]
		p := bs.propManager.CreateRoot("")

		setChildPropString(p, "image", fmt.Sprintf("%s#%d", url, i*60))
		setChildPropFloat(p, "timestamp", float64(i*60))

		item.prop = p
		item.start = i * 60
		p.SetParent(parent)
	}
	return si
}

// buildChapters — C: build_chapters (fa_video.c:450-498).
func (bs *BackendSystem) buildChapters(mp *mediacore.MediaPipe,
	fctx *medialibav.AVFormatCtx, url string) *seekIndexT {
	items := fctx.GetNumChapters()
	if items == 0 || bs.propManager == nil || mp.PropRoot == nil {
		return nil
	}
	bs.traceSystem.Trace(trace.TRACE_DEBUG, "Video", "%d chapters", items)

	si := &seekIndexT{nItems: items, items: make([]seekItemT, items)}
	si.root = bs.propManager.CreateEx(mp.PropRoot, "chapterindex", nil, false, true)
	parent := bs.propManager.CreateEx(si.root, "positions", nil, false, true)

	if avail := bs.propManager.CreateEx(si.root, "available", nil, false, true); avail != nil {
		avail.SetInt(1)
	}

	for i := range items {
		avc := fctx.GetChapter(i)
		if avc == nil {
			continue
		}
		item := &si.items[i]
		p := bs.propManager.CreateRoot("")

		item.start = int(avc.StartUS / 1000000)

		setChildPropString(p, "image",
			fmt.Sprintf("%s#%d", url, item.start))
		setChildPropFloat(p, "timestamp", float64(item.start))
		setChildPropFloat(p, "end", float64(avc.EndUS)/1000000.0)

		// C: av_dict_get(avc->metadata, "title") + utf8_verify
		if avc.Title != "" && misc.Utf8Verify(avc.Title) != 0 {
			setChildPropString(p, "title", avc.Title)
		}

		item.prop = p
		p.SetParent(parent)
	}
	return si
}

// seekIndexDestroy — C: seek_index_destroy (fa_video.c:504-510)
func seekIndexDestroy(pm *propcore.PropManager, si *seekIndexT) {
	if si == nil {
		return
	}
	if pm != nil && si.root != nil {
		pm.Destroy(si.root)
	}
}

// BeFilePlayvideo — C: be_file_playvideo (fa_video.c:516-649).
func (bs *BackendSystem) BeFilePlayvideo(url string, mp *mediacore.MediaPipe,
	vq, vsl any, va0 *VideoArgs) (any, error) {
	var title string
	va := *va0

	// C: mp_set_url(mp, va0->canonical_url, va0->parent_url,
	//   va0->parent_title)
	mediacore.MpSetURL(mp, va0.CanonicalURL, va0.ParentURL, va0.ParentTitle)

	// C: prop_set(mp->mp_prop_root, "loading", PROP_SET_INT, 1)
	mpSetRootPropInt(bs.propManager, mp, "loading", 1)

	if va.Flags&BackendVideoNoAudio == 0 {
		mediacore.MpBecomePrimary(mp, bs.propManager)
	}

	// C: prop_set(mp->mp_prop_root, "type", PROP_SET_STRING, "video")
	mpSetRootPropString(bs.propManager, mp, "type", "video")

	// C: #if ENABLE_DVD && ENABLE_METADATA — DVD directory check
	if va.Mimetype == "" {
		fs, err := fileaccesscore.Stat(bs.fileAccessManager, url)
		if err != nil {
			return nil, err
		}
		if fs.Type == fileaccesscore.ContentDir {
			md := scanner.FAProbeDir(bs.fileAccessManager, url)
			isDVD := md != nil && md.ContentType == metadata.ContentDVD
			if md != nil {
				md.Destroy()
			}
			if isDVD {
				return bs.playDVD(url, mp)
			}
			return nil, nil
		}
	}

	// C: fa_open_ex(url, ..., FA_BUFFERED_BIG, &foe)
	foe := &fileaccesscore.OpenExtra{
		Stats:       mp.PropIO,
		Cancellable: mp.Cancellable,
	}
	fh, err := fileaccesscore.FAOpenEx(bs.fileAccessManager, url,
		fileaccesscore.FaBufferedBig, foe)
	if fh == nil {
		return nil, err
	}

	if va.Flags&BackendVideoSetTitle != 0 {
		// C: fa_url_get_last_component + strip extension
		tmp := bs.fileAccessManager.FAURLGetLastComponent(va.CanonicalURL)
		if x := strings.LastIndexByte(tmp, '.'); x >= 0 {
			tmp = tmp[:x]
		}
		title = tmp
		va.Title = title
		if mp.PropMetadata != nil {
			setChildPropString(mp.PropMetadata, "title", title)
		}
	}

	// C: #if ENABLE_METADATA — ISO/DVD probe
	if va.Mimetype == "" && scanner.FAProbeISOIsDVD(fh) {
		fileaccesscore.FAClose(fh)
		return bs.playDVD(url, mp)
	}

	// C: #if ENABLE_HLS — HLS playlist sniff
	if _, err := fileaccesscore.FASeek(fh, 0, io.SeekStart); err == nil {
		buf := make([]byte, 1024)
		// C: fa_read(fh, buf, sizeof(buf) - 1) — max 1023 so the
		// "buffer full → read the rest" check below can trigger.
		l, _ := fileaccesscore.FARead(fh, buf[:len(buf)-1])
		if l > 10 && scanner.IsHLSPlaylist(buf[:l]) {
			hlslist := slices.Clone(buf[:l])
			if l == len(buf)-1 {
				// C: fa_read_to_htsbuf(&hq, fh, 100000)
				rest, rerr := faReadAll(fh, 100000)
				if rerr != nil {
					fileaccesscore.FAClose(fh)
					return nil, errors.New("Unable to read HLS playlist file")
				}
				hlslist = append(hlslist, rest...)
			}
			fileaccesscore.FAClose(fh)
			if bs.hlsPlayExtm3u == nil {
				return nil, errors.New("HLS support not available")
			}
			return bs.hlsPlayExtm3u(hlslist, url, mp,
				vq, vsl, &va)
		}
	}

	return bs.beFilePlayvideoFH(url, mp, vq, fh, &va)
}

// playDVD — C: the `isdvd:` label (fa_video.c:604-608).
func (bs *BackendSystem) playDVD(url string,
	mp *mediacore.MediaPipe) (any, error) {
	mpSetRootPropInt(bs.propManager, mp, "loading", 0)
	if bs.dvdPlay == nil {
		return nil, errors.New("DVD playback is not supported")
	}
	return bs.dvdPlay(url, mp, 1)
}

// faReadAll — C: fa_read_to_htsbuf — read until EOF or maxlen.
func faReadAll(fh *fileaccesscore.Handle, maxlen int) ([]byte, error) {
	var out []byte
	buf := make([]byte, 8192)
	for len(out) < maxlen {
		n, err := fileaccesscore.FARead(fh, buf)
		if n > 0 {
			out = append(out, buf[:n]...)
		}
		if err != nil || n <= 0 {
			break
		}
	}
	if len(out) >= maxlen {
		return out, fmt.Errorf("truncated")
	}
	return out, nil
}

// BeFilePlayvideoFH — C: be_file_playvideo_fh — exported wrapper for
// backends in other packages (HTSP DVR playback uses this path).
func (bs *BackendSystem) BeFilePlayvideoFH(url string, mp *mediacore.MediaPipe,
	vq any, fh *fileaccesscore.Handle,
	va0 *VideoArgs) (any, error) {
	return bs.beFilePlayvideoFH(url, mp, vq, fh, va0)
}

// BePlayvideoFctx — exported wrapper around bePlayvideoFctx for backends
// that open the AVFormatContext directly on a URL without a fileaccess
// handle (rtmp-ffmpeg experiment — passes fh=nil).
func (bs *BackendSystem) BePlayvideoFctx(url string, mp *mediacore.MediaPipe,
	fh *fileaccesscore.Handle, va0 *VideoArgs,
	fctxLibav *libav.AVFormatContext) (any, error) {
	return bs.bePlayvideoFctx(url, mp, fh, va0, fctxLibav)
}

// beFilePlayvideoFH — C: be_file_playvideo_fh (fa_video.c:654-934).
func (bs *BackendSystem) beFilePlayvideoFH(url string, mp *mediacore.MediaPipe,
	vq any, fh *fileaccesscore.Handle,
	va0 *VideoArgs) (any, error) {
	va := *va0

	if va.Flags&BackendVideoNoSubtitleScan == 0 {
		computeHash(fh, &va)
		if !va.HashValid {
			bs.traceSystem.Trace(trace.TRACE_DEBUG, "Video",
				"Unable to compute opensub hash, stream probably not seekable")
		}
	}

	libavSys := libav.GetGlobalLibAVSystem()
	strategy := libav.FALibavGetStrategyForFile(fh)
	avio, err := libav.FALibavReopen(libavSys, fh, false)
	if avio == nil {
		return nil, err
	}
	va.Filesize = libav.AvioSize(avio)

	fctxLibav, err := libav.FALibavOpenFormat(avio, url,
		va.Mimetype, strategy)
	if fctxLibav == nil {
		libav.FALibavClose(libavSys, avio)
		return nil, err
	}
	defer libav.FALibavCloseFormat(libavSys, fctxLibav, false)
	return bs.bePlayvideoFctx(url, mp, fh, &va, fctxLibav)
}

// bePlayvideoFctx — C: be_file_playvideo_fh tail (fa_video.c:~704-934).
// Everything after avformat open: metadata → proptree, subtitle scan,
// codec vector (cwvec) creation, index/chapter builders and the
// video_player_loop itself. fh may be nil when the AVFormatContext was
// opened directly on a URL (rtmp-ffmpeg experiment) — FADeadline and the
// subtitle/hash guards are nil-safe or skipped via va.Flags.
func (bs *BackendSystem) bePlayvideoFctx(url string, mp *mediacore.MediaPipe,
	fh *fileaccesscore.Handle, va0 *VideoArgs,
	fctxLibav *libav.AVFormatContext) (any, error) {
	var ss *subtitles.SubScanner
	va := *va0
	fctx := medialibav.WrapFormatCtx(fctxLibav)

	bs.usage.Event("Play video", 1, "format", fctx.GetFormatName())

	mp.Mutex.Lock() // C: mp_mutex — mp_*_stream writers run under mp_lockmgr
	mp.Audio.Stream = -1
	mp.Video.Stream = -1
	mp.Video.Stream2 = -1
	mp.Mutex.Unlock()

	bs.traceSystem.Trace(trace.TRACE_DEBUG, "Video", "Starting playback of %s (%s)",
		url, fctx.GetFormatName())

	// C: #if ENABLE_METADATA — fa_metadata_from_fctx → proptree
	var md *metadata.Metadata
	if md2 := scanner.FAMetadataFromFctx(fctx); md2 != nil {
		bs.metadata.MetadataToProptree(md2, mp.PropMetadata, false)
		md2.Destroy()
	}

	// C: overwrite with db data when dsid != 1
	md = bs.metadata.MetadataGetVideoData(url)
	if md != nil && md.DSID != 1 {
		bs.metadata.MetadataToProptree(md, mp.PropMetadata, false)

		if md.Parent != nil &&
			md.Parent.Type == metadata.MetadataTypeSeason &&
			md.Parent.Parent != nil &&
			md.Parent.Parent.Type == metadata.MetadataTypeSeries {
			va.Episode = int(md.Idx)
			va.Season = int(md.Parent.Idx)
			if md.Parent.Parent.Title != "" {
				va.Title = md.Parent.Parent.Title
			}
		} else {
			if md.Title != "" {
				va.Title = md.Title
			}
			if md.IMDBID != "" {
				va.IMDB = md.IMDBID
			}
		}
	}

	if va.Flags&BackendVideoNoSubtitleScan == 0 {
		duration := fctx.GetDuration()
		if duration != mediacore.PTSUnset || va.HashValid {
			dur := 0
			if duration != mediacore.PTSUnset {
				dur = int(duration / 1000000)
			}
			// C: sub_scanner_create(url, mp_prop_subtitle_tracks, &va, dur)
			ss = bs.SubSys().SubScannerCreate(url, mp.PropSubtitleTracks,
				&subtitles.SubScannerArgs{
					Flags:       va.Flags,
					Title:       va.Title,
					IMDB:        va.IMDB,
					Filesize:    va.Filesize,
					Year:        va.Year,
					Season:      va.Season,
					Episode:     va.Episode,
					HashValid:   va.HashValid,
					OpenSubHash: va.OpenSubHash,
					SubDBHash:   va.SubDBHash,
				}, dur, mp.FAM)
		}
	}

	// C: vpi = video_playback_info_create(&va)
	vpi := videopkg.VideoPlaybackInfoCreate(&videopkg.VPIArgs{
		CanonicalURL: va.CanonicalURL,
		Title:        va.Title,
		Mimetype:     va.Mimetype,
		IMDB:         va.IMDB,
		Season:       va.Season,
		Episode:      va.Episode,
		Year:         va.Year,
	})

	// C: cwvec = alloca(nb_streams * sizeof(void *)) — codec vector
	cwvecSize := fctx.GetNumStreams()
	cwvec := make([]*mediacore.MediaCodec, cwvecSize)
	fw := mediacore.MediaFormatCreate(fctxLibav)

	freetypeContext := 0
	if bs.freetype != nil {
		freetypeContext = bs.freetype.GetContext()
	}
	var alist []*attachmentT

	for i := range cwvecSize {
		var mcp mediacore.MediaCodecParams
		si := fctx.GetStreamInfo(i)
		if si == nil {
			continue
		}
		ctxType := si.CodecType

		switch ctxType {
		case medialibav.AVMediaTypeVideo:
			mcp.Width = si.Width
			mcp.Height = si.Height
			mcp.Profile = fctx.GetStreamProfile(i)
			mcp.Level = fctx.GetStreamLevel(i)
			mcp.SARNum, mcp.SARDen = fctx.GetStreamSampleAspectRatio(i)
			mcp.FrameRateNum, mcp.FrameRateDen = fctx.GetStreamAvgFrameRate(i)
			// C: if(!mcp.frame_rate_num || !mcp.frame_rate_num) — the
			// upstream bug checks num twice; equivalent to num == 0.
			if mcp.FrameRateNum == 0 {
				// C: ctx->time_base.den / ctx->time_base.num
				tbn, tbd := fctx.GetStreamCodecTimeBase(i)
				mcp.FrameRateNum = tbd
				mcp.FrameRateDen = tbn
			}

		case medialibav.AVMediaTypeAudio:
			if va.Flags&BackendVideoNoAudio != 0 {
				continue
			}
			if si.CodecID == libav.AVCodecIDDts {
				fctx.SetStreamChannels(i, 0)
			}

		case libav.AvmediaTypeAttachment:
			fn := fctx.GetStreamMetadata(i, "filename")
			if fn == "" {
				fn = "<unknown>"
			}
			// C: #else branch — extradata carries the attachment
			if ed := fctx.GetStreamExtradata(i); len(ed) > 0 {
				bs.attachmentLoadBuf(&alist, ed, freetypeContext, fn)
			}

		default:
		}

		if ctxType == medialibav.AVMediaTypeVideo {
			mp.Mutex.Lock()
			sel := mp.Video.Stream != -1
			mp.Mutex.Unlock()
			if sel {
				continue
			}
		}

		mcp.ExtraData = fctx.GetStreamExtradata(i)
		mcp.ExtraDataSize = len(mcp.ExtraData)

		// C: cwvec[i] = media_codec_create(ctx->codec_id, 0, fw, ctx,
		//   &mcp, mp)
		cwvec[i] = mediacore.MediaCodecCreate(mediacore.CodecID(si.CodecID),
			0, fw, fctx.CodecCtxFromStream(i).CPtr(), &mcp, mp)

		if cwvec[i] != nil {
			switch ctxType {
			case medialibav.AVMediaTypeVideo:
				mp.Mutex.Lock()
				sel := mp.Video.Stream == -1
				if sel {
					mp.Video.Stream = i
				}
				mp.Mutex.Unlock()
				if sel {
					if mcp.FrameRateNum != 0 && mcp.FrameRateDen != 0 {
						vpi.AddDbl("framerate",
							float64(mcp.FrameRateNum)/float64(mcp.FrameRateDen))
					}
					if mcp.Width != 0 {
						vpi.AddU32("width", uint32(mcp.Width))
					}
					if mcp.Height != 0 {
						vpi.AddU32("height", uint32(mcp.Height))
					}
				}
			case medialibav.AVMediaTypeAudio:
				mp.Mutex.Lock()
				sel := mp.Audio.Stream == -1
				if sel {
					mp.Audio.Stream = i
				}
				mp.Mutex.Unlock()
				if sel {
					// C: prop_set_stringf(audio_track_current,
					//   "libav:%d", i) + manual = 0
					if mp.PropAudioTrackCurrent != nil {
						mp.PropAudioTrackCurrent.SetString(
							fmt.Sprintf("libav:%d", i))
					}
					if mp.PropAudioTrackCurrentManual != nil {
						mp.PropAudioTrackCurrentManual.SetInt(0)
					}
				}
			}
		}
	}

	flags := mediacore.MPCanPause
	if fctx.GetDuration() != mediacore.PTSUnset {
		flags |= mediacore.MPCanSeek
	}

	// C: mp_configure(mp, flags, MP_BUFFER_DEEP, fctx->duration, "video")
	mediacore.MpConfigure(mp, bs.propManager, flags,
		mediacore.MPBufferDeep, fctx.GetDuration(), "video")

	ncodec := 0
	for _, cw := range cwvec {
		if cw != nil {
			ncodec++
		}
	}

	si := bs.buildIndex(mp, fctx, url)
	ci := bs.buildChapters(mp, fctx, url)

	bs.metadata.PlayInfoRegisterPlay(va.CanonicalURL, 0)
	mpSetRootPropInt(bs.propManager, mp, "loading", 0)

	var origin *propcore.Prop
	if p, ok := va.Origin.(*propcore.Prop); ok {
		origin = p
	}

	e := bs.videoPlayerLoop(fctx, cwvec, mp, va.Flags,
		va.CanonicalURL, freetypeContext, si, ci, fh, va.ResumeMode,
		va.Title, vpi, origin)

	if bs.playbackInfoFn != nil {
		bs.playbackInfoFn(videopkg.VPIStop, vpi, mp.PropRoot, origin)
	}
	vpi.Release()

	seekIndexDestroy(bs.propManager, si)
	seekIndexDestroy(bs.propManager, ci)

	bs.traceSystem.Trace(trace.TRACE_DEBUG, "Video", "Stopped playback of %s", url)

	mediacore.MpShutdown(mp)

	for i := range cwvecSize {
		if cwvec[i] != nil {
			mediacore.MediaCodecDeref(cwvec[i])
		}
	}

	attachmentUnloadAll(alist)

	mediacore.MediaFormatDeref(fw)

	if ss != nil {
		subtitles.SubScannerDestroy(ss)
	}

	if md != nil {
		md.Destroy()
	}

	return e, nil
}
