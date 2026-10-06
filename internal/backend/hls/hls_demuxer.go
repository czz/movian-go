// Canonical port of src/backend/hls/hls.c — HLS playlist/variant/segment
// management and the dual-demuxer playback loop. hls.h structures are
// reproduced as Go types; all 55 C functions are mapped in file order.
//
// Sentinel pointers (void*)-1/-2/-3 are modelled by mbRef/segRef codes.
package hls

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/czz/movian-go/internal/arch"
	backendcore "github.com/czz/movian-go/internal/backend/core"
	"github.com/czz/movian-go/internal/event"
	"github.com/czz/movian-go/internal/gconf"
	"github.com/czz/movian-go/internal/libav"
	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/metadata"
	"github.com/czz/movian-go/internal/misc"
	"github.com/czz/movian-go/internal/nls"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/subtitles"
	"github.com/czz/movian-go/internal/trace"
	"github.com/czz/movian-go/internal/usage"
)

func hlsDumpDemuxer(hd *hlsDemuxer, h *hls) {
	for _, hv := range hd.Variants {
		hlsTrace(h, "  %s", hv.URL)
		hlsTrace(h, "    bitrate:    %d", hv.Bitrate)

		if hv.AudioOnly {
			hlsTrace(h, "    Audio only")
			continue
		}
		if hv.Initial {
			hlsTrace(h, "    Initial")
			continue
		}

		var txt string
		switch hv.H264Profile {
		case 66:
			txt = "h264 Baseline"
		case 77:
			txt = "h264 Main"
		default:
			txt = "<unknown>"
		}
		hlsTrace(h,
			"    Video resolution: %d x %d  Profile: %s  Level: %d.%d",
			hv.Width, hv.Height, txt,
			hv.H264Level/10, hv.H264Level%10)
	}
}

func hlsDump(h *hls) {
	hlsTrace(h, "Base URL: %s", h.BaseURL)
	hlsTrace(h, "Primary/Adaptive variants")
	hlsDumpDemuxer(&h.Primary, h)
	hlsTrace(h, "Audio variants")
	hlsDumpDemuxer(&h.Audio, h)
}

func checkAudioOnly(hd *hlsDemuxer) {
	streams := 0
	audioOnly := 0
	for _, hv := range hd.Variants {
		streams++
		if hv.AudioOnly {
			audioOnly++
		}
	}
	if streams == audioOnly {
		// Most likely not _all_ variants are audio only, clear the flag
		for _, hv := range hd.Variants {
			hv.AudioOnly = false
		}
	}
}

// mpWaitBackpressureTimeout — C: hts_cond_wait_timeout(&mp->mp_backpressure,
// &mp->mp_mutex, ms) — returns true if the wait timed out.
func mpWaitBackpressureTimeout(mp *mediacore.MediaPipe, ms int) bool {
	bp := mp.Backpressure
	if bp == nil {
		return true
	}
	deadline := time.Now().Add(time.Duration(ms) * time.Millisecond)
	t := time.AfterFunc(time.Until(deadline), func() {
		mp.Mutex.Lock()
		bp.Broadcast()
		mp.Mutex.Unlock()
	})
	bp.Wait()
	t.Stop()
	return !time.Now().Before(deadline)
}

func hlsSelectDefaultVariant(hd *hlsDemuxer) *hlsVariant {
	for _, hv := range hd.Variants {
		if hv.Initial {
			return hv
		}
	}
	var best *hlsVariant
	for _, hv := range slices.Backward(hd.Variants) {

		if hv.AudioOnly {
			continue
		}
		if best == nil || best.CorruptCounter > hv.CorruptCounter {
			best = hv
		}
	}
	return best
}

func demuxerSelectVariantSimple(hd *hlsDemuxer, now int64,
	bw int) *hlsVariant {
	var best *hlsVariant
	lcc := int32(0x7FFFFFFF)

	for _, hv := range hd.Variants {
		if hv.AudioOnly {
			continue
		}
		if hv.CorruptTimer < now-hlsCorruptionPeriodUs {
			hv.CorruptionsLast = 0
		}
		if int32(hv.CorruptCounter) < lcc {
			lcc = int32(hv.CorruptCounter)
		}
	}

	for _, hv := range hd.Variants {
		if hv.AudioOnly || hv.CorruptionsLast >= 3 ||
			int32(hv.CorruptCounter) != lcc || hv.Bitrate >= bw {
			continue
		}
		if best == nil {
			best = hv
		}
	}
	if best != nil {
		return best
	}

	for _, hv := range slices.Backward(hd.Variants) {

		if hv.AudioOnly || hv.CorruptionsLast >= 3 {
			continue
		}
		return hv
	}
	hd.NoFunctionalStreams = true
	return nil
}

func hlsDemuxerSelectVariant(hd *hlsDemuxer, now int64,
	bw int) *hlsVariant {
	// C: if(0) return demuxer_select_variant_random(hd);
	return demuxerSelectVariantSimple(hd, now, bw)
}

func hlsCheckBWSwitch(hd *hlsDemuxer) {
	h := hd.HLS
	mp := h.MP

	if hd.BWUpdated == 0 {
		return
	}

	if hd == &h.Primary {
		if m := pm(h); m != nil {
			m.SetVEx(nil, mp.PropIO, "bitrate", hd.BW/1000)
			m.SetVEx(nil, mp.PropIO, "bitrateValid", 1)
		}
	}

	now := arch.GetTS()
	if hd.LastSwitch+1000000 > now {
		return
	}

	hd.BWUpdated = 0
	hv := hlsDemuxerSelectVariant(hd, now, hd.BW)

	if hv == nil || hv == hd.Current {
		return
	}

	if hv.Bitrate > hd.Current.Bitrate {
		// Stepping up only with more than 10s worth of buffer
		if mp.BufferDelay < 10000000 {
			hd.LastSwitch = now
			return
		}
	}

	hd.LastSwitch = now
	hd.Req = hv

	hlsFreeMbp(mp, &hd.Mb)
}

func hlsEventCallback(mp *mediacore.MediaPipe, aux any,
	e *mediacore.MediaEvent) int {
	h := aux.(*hls)

	if meIsType(e, event.EVENT_CURRENT_TIME) {
		if ets, ok := event.ConcreteOf(e.Data).(*event.EventTs); ok && ets != nil {
			if int64(ets.Epoch) == mp.Epoch {
				sec := int(ets.Ts / 1000000)
				h.LastTimestampPresented = ets.Ts

				// Update restartpos every 5 seconds
				if mp.Flags&mediacore.MPCanSeek != 0 &&
					(sec < h.RestartposLast ||
						sec >= h.RestartposLast+5) {
					h.RestartposLast = sec
					// playinfo_set_restartpos call is commented
					// out in canonical C (hls.c:1164)
				}
			}
		}
	} else if meIsType(e, event.EVENT_PLAYBACK_PRIORITY) {
		if ei, ok := event.ConcreteOf(e.Data).(*event.EventInt); ok && ei != nil {
			h.PlaybackPriority = ei.Val
		}
	} else if meIsType(e, event.EVENT_SELECT_AUDIO_TRACK) {
		if est, ok := event.ConcreteOf(e.Data).(*event.EventSelectTrack); ok && est != nil {
			if id, ok2 := mystrbegins(est.ID, "hls:"); ok2 {
				streamid := misc.Atoi(id)
				if streamid == 0 {
					panic("streamid == 0")
				}
				h.Audio.PendingStream = streamid
			}
		}
	} else if meIsAction(e, event.ACTION_SKIP_FORWARD) ||
		meIsAction(e, event.ACTION_SKIP_BACKWARD) ||
		meIsType(e, event.EVENT_EXIT) ||
		meIsType(e, event.EVENT_PLAY_URL) ||
		meIsType(e, event.EVENT_SEEK) {

		misc.CancellableCancel(h.Primary.Cancellable)
		misc.CancellableCancel(h.Audio.Cancellable)
		return 0 // Continue processing
	}
	return 1
}

// extractPsNal — C: extract_ps_nal (hls.c:1200-1216). nal is the NAL
// payload starting right after the startcode (C's data arg); withSC is
// the same region including the 3-byte startcode (C's data-3 copy).
func extractPsNal(hv *hlsVariant, nal, withSC []byte) {
	if len(nal) == 0 {
		return
	}
	nalType := nal[0] & 0x1f
	if nalType == 7 || nalType == 8 {
		hv.VideoHeaders = append(hv.VideoHeaders, withSC...)
	}
}

func extractPs(hv *hlsVariant, mb *mediacore.MediaBuf) {
	d := mb.Data[:mb.Size]
	p := -1 // index of NAL data start (after a startcode), C's `p`
	i := 0
	for len(d)-i > 3 {
		if !(d[i] == 0 && d[i+1] == 0 && d[i+2] == 1) {
			i++
			continue
		}
		if p >= 0 {
			extractPsNal(hv, d[p:i], d[p-3:i])
		}
		i += 3
		p = i
	}
	if p >= 0 {
		extractPsNal(hv, d[p:], d[p-3:])
	}
}

func dropEarlyAudioPackets(mp *mediacore.MediaPipe, dts int64) {
	mq := mp.Audio
	for len(mq.DataQueue) > 0 {
		mb := mq.DataQueue[0]
		mq.DataQueue = mq.DataQueue[1:]
		mediacore.MediaBufFreeLocked(mp, mb)
	}
}

func enqueueBuffer(mp *mediacore.MediaPipe, mq *mediacore.MediaQueue,
	mb *mediacore.MediaBuf, h *hls,
	hv *hlsVariant) *mediacore.MediaEvent {
	isVideo := mb.DataType == int(mediacore.MBVideo)

	mp.Mutex.Lock()

	mediacore.MpUpdateBufferDelay(mp)

	for {
		if len(mp.EventQueue) > 0 {
			e := mp.EventQueue[0]
			mp.EventQueue = mp.EventQueue[1:]
			mp.Mutex.Unlock()
			return e
		}
		if mp.BufferCurrent+mediacore.MbBufferedSize(mb) < mp.BufferLimit &&
			mp.BufferDelay < 60000000 {
			break
		}
		h.Blocked++
		if mp.Backpressure != nil {
			mp.Backpressure.Wait()
		}
	}

	if mq.SeekTarget != mediacore.PTSUnset {
		ts := mb.UserTime
		if ts < mq.SeekTarget {
			mb.Flags.Skip = true
		} else {
			mq.SeekTarget = mediacore.PTSUnset
		}
	}

	flush := false

	if mq.DemuxerFlags&HLSQueueMerge != 0 {
		if mq.LastDeqDTS != mediacore.PTSUnset &&
			mb.DTS < mq.LastDeqDTS {
			// This frame has already been dequeued — drop packet and
			// restart keyframe search
			if isVideo {
				extractPs(hv, mb)
			}
			mediacore.MediaBufFreeLocked(mp, mb)
			mq.DemuxerFlags &^= HLSQueueKeyframeSeen
			mp.Mutex.Unlock()
			return nil
		}

		// Find first queued buf with DTS >= mb.DTS
		bi := -1
		for i, b := range mq.DataQueue {
			if b.DTS != mediacore.PTSUnset && b.DTS >= mb.DTS {
				bi = i
				break
			}
		}
		if bi >= 0 {
			var kept []*mediacore.MediaBuf
			for i, b := range mq.DataQueue {
				if i >= bi &&
					(b.DataType == int(mediacore.MBAudio) ||
						b.DataType == int(mediacore.MBVideo)) {
					mq.PacketsCurrent--
					mp.BufferCurrent -= mediacore.MbBufferedSize(b)
					mediacore.MediaBufFreeLocked(mp, b)
					continue
				}
				kept = append(kept, b)
			}
			mq.DataQueue = kept
		}
		mq.DemuxerFlags &^= HLSQueueMerge
		flush = true
		if !mb.Flags.Keyframe {
			panic("mb_keyframe != 1 on queue merge")
		}
		hlsTrace(h, "%s queue merged at DTS %d",
			map[bool]string{true: "Video", false: "Audio"}[isVideo],
			mb.DTS)
	}

	if isVideo && mq.DemuxerFlags&HLSQueueKeyframeSeen == 0 &&
		len(mq.DataQueue) == 0 {
		if mb.DTS == mediacore.PTSUnset {
			panic("mb_dts unset")
		}
		dropEarlyAudioPackets(mp, mb.DTS)
	}

	if mb.Flags.Keyframe {
		mq.DemuxerFlags |= HLSQueueKeyframeSeen
	}

	if mp.HoldFlags&mediacore.MPHoldSync != 0 &&
		mp.Video.DemuxerFlags&HLSQueueKeyframeSeen != 0 &&
		mp.Audio.DemuxerFlags&HLSQueueKeyframeSeen != 0 {
		mp.HoldFlags &^= mediacore.MPHoldSync
		mediacore.MpSetPlaystatusByHoldLocked(mp, "")
	}

	if isVideo && len(hv.VideoHeaders) > 0 {
		mbx := mediacore.MediaBufAllocLocked(mp,
			len(hv.VideoHeaders)+mb.Size)
		copy(mbx.Data, hv.VideoHeaders)
		copy(mbx.Data[len(hv.VideoHeaders):], mb.Data[:mb.Size])

		mbx.Flags.Keyframe = mb.Flags.Keyframe
		mbx.DataType = int(mediacore.MBVideo)
		mbx.Codec = mediacore.MediaCodecRef(mb.Codec)
		mbx.Flags.Skip = mb.Flags.Skip
		mbx.UserTime = mb.UserTime

		mbx.Flags.Flush = flush
		mediacore.MbEnq(mp, mq, mbx)

		flush = false
		hv.VideoHeaders = nil

		mediacore.MediaBufFreeLocked(mp, mb)

		mp.Mutex.Unlock()
		return nil
	}

	mb.Flags.Flush = flush
	mediacore.MbEnq(mp, mq, mb)
	h.LastEnqueuedSeq = mb.Sequence

	if !isVideo && mq.Stream != h.Audio.CurrentStream {
		hlsTrace(h, "Telling audio decoder to switch from stream %d to %d",
			mq.Stream, h.Audio.CurrentStream)
		mq.Stream = h.Audio.CurrentStream
	}
	mp.Mutex.Unlock()
	return nil
}

func hlsDemuxerGet(hd *hlsDemuxer) mbRef {
	if hd.Mb.mb == nil && hd.Mb.code == 0 {
		hd.Mb = hlsTsDemuxerRead(hd)
	}
	return hd.Mb
}

func hlsDemuxerGetAudio(h *hls) mbRef {
	hd := &h.Audio

	if hd.CurrentStream != hd.PendingStream {
		hd.CurrentStream = hd.PendingStream

		var hv *hlsVariant
		for _, x := range hd.Variants {
			if x.AudioStream == hd.CurrentStream {
				hv = x
				break
			}
		}

		name := "<muxed in primary>"
		if hv != nil {
			name = hv.Name
		}
		hlsTrace(h,
			"Audio demuxer checking stream switch to streamid: %d -> %s",
			hd.CurrentStream, name)

		if hd.Current != nil {
			hlsVariantClose(hd.Current)
		}

		if hd.Current != nil && hv == nil {
			// Switching to an audio stream in primary mux requires a
			// full resync
			hlsTrace(h,
				"Force primary stream to resynchronize due to audio switch")
			h.Primary.Req = h.Primary.Current
		} else {
			mp := h.MP
			mp.Audio.DemuxerFlags |= HLSQueueMerge
			mp.Audio.DemuxerFlags &^= HLSQueueKeyframeSeen
			hlsTrace(h, "Audio queue merge started")
		}

		hd.Current = hv
		hlsFreeMbp(h.MP, &hd.Mb)
	}

	return hlsDemuxerGet(hd)
}

func hlsDemuxerSeek(mp *mediacore.MediaPipe, hd *hlsDemuxer, pos int64) {
	hd.SeekToSegment = pos

	if hd.Current != nil && hd.Current.DemuxerFlush != nil {
		hd.Current.DemuxerFlush(hd.Current)
	}

	hlsFreeMbp(mp, &hd.Mb)
}

func getMediaBuf(h *hls) (*hlsDemuxer, int) {
	mp := h.MP

again:
	b2 := hlsDemuxerGetAudio(h)
	if b2.mb == nil && b2.code == 0 {
		return nil, 0
	}
	b1 := hlsDemuxerGet(&h.Primary)
	if b1.mb == nil && b1.code == 0 {
		return nil, 0
	}

	if b1.code == codeEOF && b2.code == codeEOF {
		// All demuxers are EOF
		mp.EOF = 1
		return nil, codeEOF
	}

	mp.EOF = 0
	var hd *hlsDemuxer

	if b1.code == codeDIS {
		if b2.code == codeEOF {
			h.Primary.Discontinuity = 0
			h.Primary.Mb = mbRef{}
			goto again
		}
		if b2.code == codeDIS {
			h.Primary.Discontinuity = 0
			h.Audio.Discontinuity = 0
			h.Primary.Mb = mbRef{}
			h.Audio.Mb = mbRef{}
			goto again
		}
		hd = &h.Audio
	} else if b2.code == codeDIS {
		if b1.code == codeEOF {
			h.Audio.Discontinuity = 0
			h.Audio.Mb = mbRef{}
			goto again
		}
		hd = &h.Primary
	} else if b2.code == codeEOF {
		hd = &h.Primary
	} else if b1.code == codeEOF {
		hd = &h.Audio
	} else if b1.mb.DTS == mediacore.PTSUnset {
		hd = &h.Primary
	} else if b2.mb.DTS == mediacore.PTSUnset {
		hd = &h.Audio
	} else if b1.mb.DTS < b2.mb.DTS {
		hd = &h.Primary
	} else {
		hd = &h.Audio
	}

	return hd, 0
}

func clearTSOffsets(hd *hlsDemuxer) {
	hd.LastDTS = mediacore.PTSUnset
	for _, hv := range hd.Variants {
		for _, hs := range hv.Segments {
			hs.TSOffset = mediacore.PTSUnset
		}
	}
}

func clearHDSOffset(h *hls) {
	for _, hds := range h.DiscontinuitySegments {
		if hds.Seq != 0 {
			hds.Offset = mediacore.PTSUnset
		}
	}
}

func hlsSeek(h *hls, ts int64) {
	mp := h.MP

	hlsTrace(h, "Seeking to %d", ts)

	hlsDemuxerSeek(mp, &h.Primary, ts)
	hlsDemuxerSeek(mp, &h.Audio, ts)
	clearHDSOffset(h)
	clearTSOffsets(&h.Primary)
	clearTSOffsets(&h.Audio)
	misc.CancellableReset(h.Primary.Cancellable)
	misc.CancellableReset(h.Audio.Cancellable)

	mp.Video.SeekTarget = ts
	mp.Audio.SeekTarget = ts
	mediacore.MpFlush(mp)

	mp.Video.DemuxerFlags &^= HLSQueueKeyframeSeen
	mp.Audio.DemuxerFlags &^= HLSQueueKeyframeSeen
}

func hlsPlay(h *hls, mp *mediacore.MediaPipe,
	va *backendcore.VideoArgs) (*mediacore.MediaEvent, error) {
	var e *mediacore.MediaEvent
	canonicalURL := va.CanonicalURL
	var ss *subtitles.SubScanner
	loading := true

	h.RestartposLast = -1
	h.LastTimestampPresented = mediacore.PTSUnset
	h.SubScanningDone = false
	h.EnqueuedSomething = false

	h.PlaybackPriority = va.Priority

	mp.Video.Stream = 0
	mp.Audio.Stream = 1

	if va.Flags&backendcore.BackendVideoNoAudio == 0 {
		mediacore.MpBecomePrimary(mp, pm(h))
	}

	mediacore.MpHold(mp, pm(h), mediacore.MPHoldSync, "")

	mediacore.MpConfigure(mp, pm(h), mediacore.MPCanPause,
		mediacore.MPBufferDeep, 0, "video")

	mp.Video.SeekTarget = mediacore.PTSUnset
	mp.Audio.SeekTarget = mediacore.PTSUnset
	mp.SeekBase = 0

	mediacore.MpEventSetCallback(mp, hlsEventCallback, h)

	start := h.mm.PlayInfoGetRestartPos(canonicalURL,
		va.Title, va.ResumeMode) * 1000
	if start != 0 {
		h.ts.Trace(trace.TRACE_DEBUG, "HLS",
			"Attempting to resume from %.2f seconds",
			float64(start)/1000000.0)
		mp.SeekBase = start
		hlsSeek(h, start)
	}

	h.Primary.Current = hlsSelectDefaultVariant(&h.Primary)
	h.Audio.Current = hlsSelectDefaultVariant(&h.Audio)

	h.Audio.PendingStream = 1

	if va.Flags&backendcore.BackendVideoNoSubtitleScan != 0 {
		h.SubScanningDone = true
	}

	for {
		if h.Primary.NoFunctionalStreams ||
			h.Audio.NoFunctionalStreams {
			if h.LastError != hlsErrorOK {
				return nil, fmt.Errorf("No playable streams -- %s",
					hlserrstr[h.LastError])
			}
			return nil, errors.New("No playable streams")
		}
		if e == nil {
			e = mediacore.MpDequeueEventDeadline(mp, 0)
		}

		if e != nil {
			if meIsAction(e, event.ACTION_SKIP_FORWARD) ||
				meIsAction(e, event.ACTION_SKIP_BACKWARD) ||
				meIsType(e, event.EVENT_EXIT) ||
				meIsType(e, event.EVENT_PLAY_URL) {
				break
			}

			if meIsType(e, event.EVENT_SEEK) {
				if ets, ok := event.ConcreteOf(e.Data).(*event.EventTs); ok && ets != nil {
					hlsSeek(h, ets.Ts)
				}
			}
			e = nil // C: event_release(e)
		}

		if !h.SubScanningDone && h.Duration != 0 {
			h.SubScanningDone = true
			ss = h.subSys.SubScannerCreate(h.BaseURL,
				mp.PropSubtitleTracks,
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
				}, int(h.Duration/1000000), mp.FAM)
		}

		hd, code := getMediaBuf(h)
		if code == 0 && hd == nil {
			continue
		}

		if code == codeEOF {
			mp.Mutex.Lock()
			isEmpty := len(mp.Audio.DataQueue) == 0 &&
				len(mp.Video.DataQueue) == 0
			mp.Mutex.Unlock()

			if !isEmpty {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			e = &mediacore.MediaEvent{
				Type: int(event.EVENT_EOF),
				Data: &event.Event{Type: event.EVENT_EOF},
			}
			break
		}

		mb := hd.Mb.mb

		var mq *mediacore.MediaQueue
		if mb.DataType == int(mediacore.MBVideo) {
			mq = mp.Video
		} else {
			mq = mp.Audio
		}

		e = enqueueBuffer(mp, mq, mb, h, hd.Current)
		if e == nil {
			hd.Mb = mbRef{}
			if loading {
				if m := pm(h); m != nil {
					m.SetVEx(nil, mp.PropRoot, "loading", 0)
				}
				loading = false
			}
		}
	}

	if mp.Flags&mediacore.MPCanSeek != 0 {
		// Compute stop position (in percentage of video length)
		var spp int64
		if mp.Duration != 0 {
			spp = mp.SeekBase * 100 / mp.Duration
		}

		if spp >= int64(mp.Sys.VS.PlayedThreshold) ||
			meIsType(e, event.EVENT_EOF) {
			h.mm.PlayInfoSetRestartPos(canonicalURL, -1, false)
			h.mm.PlayInfoRegisterPlay(canonicalURL, 1)
			h.ts.Trace(trace.TRACE_DEBUG, "Video",
				"Playback reached %d%%%s, counting as played (%s)",
				spp,
				map[bool]string{true: ", EOF detected",
					false: ""}[meIsType(e, event.EVENT_EOF)],
				canonicalURL)
		} else if h.LastTimestampPresented != mediacore.PTSUnset {
			h.mm.PlayInfoSetRestartPos(canonicalURL,
				h.LastTimestampPresented/1000, false)
		}
	}

	mediacore.MpEventSetCallback(mp, nil, nil)

	mediacore.MpShutdown(mp)

	subtitles.SubScannerDestroy(ss)

	return e, nil
}

func hlsGetAudioTrack(h *hls, pid int, muxID, language, fmt_ string,
	autosel int) int {
	var hat *hlsAudioTrack
	for _, x := range h.AudioTracks {
		if muxID == "" && x.MuxID == "" && x.Pid == pid {
			hat = x
			break
		}
		if muxID != "" && x.MuxID != "" && x.MuxID == muxID {
			hat = x
			break
		}
	}

	if hat != nil {
		if m := pm(h); m != nil && hat.Trackprop != nil {
			m.SetVEx(nil, hat.Trackprop, "format", fmt_)
		}
	} else {
		newid := 1
		if len(h.AudioTracks) > 0 {
			newid = h.AudioTracks[0].StreamID + 1
		}
		hat = &hlsAudioTrack{
			StreamID: newid,
			Pid:      pid,
			MuxID:    muxID,
		}
		h.AudioTracks = slices.Insert(h.AudioTracks, 0, hat)
		trackuri := fmt.Sprintf("hls:%d", newid)

		var s *propcore.Prop
		score := 1000
		if muxID != "" {
			s = nls.GetProp("Supplementary")
		} else {
			s = nls.GetProp("Primary")
			score += 10
		}

		if m := pm(h); m != nil {
			hat.Trackprop = mediacore.MpAddTrackR(m,
				h.MP.PropAudioTracks,
				"", trackuri, fmt_, fmt_, language,
				"", s, score, autosel)
		}
	}
	return hat.StreamID
}

func hlsFreeAudioTracks(h *hls) {
	m := pm(h)
	for _, hat := range h.AudioTracks {
		if m != nil && hat.Trackprop != nil {
			m.RefDec(hat.Trackprop)
		}
	}
	h.AudioTracks = nil
}

func hlsDemuxerSetup(hd *hlsDemuxer, h *hls, typ string) {
	hd.HLS = h
	hd.Type = typ
	hd.SeekToSegment = mediacore.PTSUnset
	hd.LastDTS = mediacore.PTSUnset
	hd.Cancellable = misc.CancellableCreate()
}

func hlsDemuxerClose(mp *mediacore.MediaPipe, hd *hlsDemuxer) {
	variantsDestroy(&hd.Variants)
	if hd.AudioCodec != nil {
		mediacore.MediaCodecDeref(hd.AudioCodec)
	}
	hlsFreeMbp(mp, &hd.Mb)
	misc.CancellableRelease(hd.Cancellable)
}

// hlsPlayExtm3u — C: hls_play_extm3u (hls.c:2166).
// Signature matches backendcore.HLSPlayExtm3uHook.
func hlsPlayExtm3u(u *usage.Reporter, mm *metadata.MetadataManager, content []byte, url string, mp *mediacore.MediaPipe,
	vq, vsl any,
	va0 *backendcore.VideoArgs, ts *trace.TraceSystem, g *gconf.T) (any, error) {

	buf := content
	if !strings.HasPrefix(string(buf), "#EXTM3U") {
		return nil, errors.New("Not an m3u file")
	}

	// C: usage_event("Play video", 1, USAGE_SEG("format", "HLS"))
	u.Event("Play video", 1, "format", "HLS")

	if mp.PropRoot != nil {
		if m := mp.PropRoot.Manager(); m != nil {
			m.SetVEx(nil, mp.PropRoot, "loading", 1)
		}
	}

	mp.Audio.DemuxerFlags = 0
	mp.Video.DemuxerFlags = 0

	h := &hls{mm: mm, ts: ts}
	if b, ok := mp.Sys.Owner.(interface {
		SubSys() *subtitles.System
	}); ok {
		h.subSys = b.SubSys()
	}
	hlsDemuxerSetup(&h.Primary, h, "primary")
	hlsDemuxerSetup(&h.Audio, h, "audio")
	h.MP = mp
	h.BaseURL = url
	h.CodecH264 = mediacore.MediaCodecCreate(
		mediacore.CodecID(libav.AVCodecIDH264), 1, nil, nil, nil, mp)
	h.Debug = false
	if g != nil && g.EnableHLSDebug.Load() {
		h.Debug = true
	}

	if strings.Contains(string(buf), "#EXT-X-STREAM-INF:") {
		var hv *hlsVariant
		firstVariant := true

		lp := buf
		for {
			s := misc.LpGet(&lp)
			if s == nil {
				break
			}
			line := lpLine(s)
			if v, ok := mystrbegins(line, "#EXT-X-MEDIA:"); ok {
				hlsExtXMedia(h, v)
			} else if v, ok := mystrbegins(line, "#EXT-X-STREAM-INF:"); ok {
				hlsExtXStreamInf(h, v, &hv, &h.Primary)
			} else if len(line) > 0 && line[0] != '#' {
				if hv != nil {
					hv.Initial = firstVariant
					firstVariant = false
					hlsAddVariant(h, line, hv, &h.Primary, "")
					hv = nil
				}
			}
		}

		if hv != nil {
			variantDestroy(hv)
		}
	} else {
		hlsAddVariant(h, h.BaseURL, nil, &h.Primary, "single")
	}

	checkAudioOnly(&h.Primary)

	hlsDump(h)

	e, perr := hlsPlay(h, mp, va0)

	hlsDemuxerClose(mp, &h.Primary)
	hlsDemuxerClose(mp, &h.Audio)

	mediacore.MediaCodecDeref(h.CodecH264)

	hlsFreeAudioTracks(h)
	if len(h.DiscontinuitySegments) != 0 {
		panic("discontinuity segments leaked")
	}
	hlsTrace(h, "HLS player done")

	return e, perr
}
