// Canonical port of src/fileaccess/fa_audio.c — be_file_playaudio:
// the file backend's audio demux/feed loop. Opens the URL via
// fileaccess, hands the handle to libav via AVIO, finds the first audio
// stream, creates the codec, and pumps MB_AUDIO buffers into
// mp->mp_audio until EOF or a stop/seek/skip event.

package core

import (
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/czz/movian-go/internal/event"
	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/libav"
	mediacore "github.com/czz/movian-go/internal/media/core"
	medialibav "github.com/czz/movian-go/internal/media/libav"
)

// playinfoAudioPlayThreshold — C: PLAYINFO_AUDIO_PLAY_THRESHOLD
// (playinfo.h:23) — 10 seconds in microseconds.
const playinfoAudioPlayThreshold = 10 * 1000000

// mbSpecialEOF — C: MB_SPECIAL_EOF ((void *)-1) sentinel marking the
// drain-queues state inside the demux loop.
var mbSpecialEOF = &mediacore.MediaBuf{}

// rescale — C: rescale (fa_audio.c:104-113) — av_rescale_q from the
// stream's time_base to AV_TIME_BASE_Q (microseconds).
func faAudioRescale(f *medialibav.AVFormatCtx, ts int64, si int) int64 {
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

// seekflush — C: seekflush (fa_audio.c:117-126) — mp_flush + free any
// in-flight buffer.
func faAudioSeekflush(mp *mediacore.MediaPipe, mbp **mediacore.MediaBuf) {
	mediacore.MpFlush(mp)
	if *mbp != nil && *mbp != mbSpecialEOF {
		mediacore.MediaBufFreeUnlocked(mp, *mbp)
	}
	*mbp = nil
}

// audioPlayZipfile — C: audio_play_zipfile (fa_audio.c:46-84) — loads
// the whole file, registers it as a memfile, scans it as a zip://
// archive, and plays each entry until one succeeds.
func (bs *BackendSystem) audioPlayZipfile(fh *fileaccesscore.Handle,
	mp *mediacore.MediaPipe, hold bool) (any, error) {
	var e any

	// C: buf_t *b = fa_load_and_close(fh)
	b := fileaccesscore.LoadAndClose(fh)
	if b == nil {
		return nil, errors.New("Load error")
	}

	fam := bs.fileAccessManager
	bm := fam.GetBundleManager()
	id := bm.MemFileRegister(b.Data)
	defer bm.MemFileUnregister(id)

	url := fmt.Sprintf("zip://memfile://%d", id)
	fd, _ := fileaccesscore.FAScanDir(fam, url)
	if fd != nil {
		for _, fde := range fd.Entries {
			var zerr error
			e, zerr = bs.beFilePlayaudio(fde.URL, mp, hold, "", nil)
			_ = zerr // C: errbuf overwritten by the "no audio file" msg
			if e != nil {
				fileaccesscore.DirFree(fd)
				return e, nil
			}
		}
		fileaccesscore.DirFree(fd)
		return nil, errors.New("No audio file found in ZIP archive")
	}
	return e, errors.New("Unable to parse ZIP archive")
}

// beFilePlayaudio — C: be_file_playaudio (fa_audio.c:123-365).
// Returns the event that terminated playback (e.g. PLAYQUEUE_JUMP), or
// nil on error (errbuf filled) / EVENT_EOF.
func (bs *BackendSystem) beFilePlayaudio(url string, mp *mediacore.MediaPipe,
	hold bool, mimetype string, opaque any) (any, error) {
	var mb *mediacore.MediaBuf
	var e *mediacore.MediaEvent
	registeredPlay := false

	// C: mp->mp_seek_base = 0
	mp.SeekBase = 0

	// C: fh = fa_open_ex(url, errbuf, errlen, FA_BUFFERED_SMALL, NULL)
	fh, err := fileaccesscore.FAOpenEx(bs.fileAccessManager, url,
		fileaccesscore.FaBufferedSmall, nil)
	if fh == nil {
		return nil, err
	}

	// C: psiz = fa_read(fh, pb, sizeof(pb)); if(psiz < 128) too small
	pb := make([]byte, 4096)
	psiz, _ := fileaccesscore.FARead(fh, pb)
	if psiz < 128 {
		fileaccesscore.FAClose(fh)
		return nil, errors.New("File too small")
	}

	// C: ZIP magic PK\x03\x04 → audio_play_zipfile
	if pb[0] == 0x50 && pb[1] == 0x4b && pb[2] == 0x03 && pb[3] == 0x04 {
		return bs.audioPlayZipfile(fh, mp, hold)
	}

	// C: #if ENABLE_PLUGINS → plugin_probe_for_autoinstall(fh, pb, psiz,
	//   url) (fa_audio.c:160). The plugin manager being set is the Go
	//   build's "ENABLE_PLUGINS".
	if prober, ok := bs.pluginManager.(interface {
		ProbeForAutoInstall(any, []byte, int, string) bool
	}); ok {
		prober.ProbeForAutoInstall(fh, pb, psiz, url)
	}
	// C: #if ENABLE_VMIR → np_fa_probe redirect (fa_audio.c:163-182) —
	// the native-plugin subsystem is not ported (build-conditional in C).

	// C: AVIOContext *avio = fa_libav_reopen(fh, 0)
	libavSys := libav.GetGlobalLibAVSystem()
	avio, err := libav.FALibavReopen(libavSys, fh, false)
	if avio == nil {
		fileaccesscore.FAClose(fh)
		return nil, err
	}

	// C: fctx = fa_libav_open_format(avio, url, errbuf, errlen,
	//   mimetype, FA_LIBAV_OPEN_STRATEGY_AUDIO)
	fctxLibav, err := libav.FALibavOpenFormat(avio, url,
		mimetype, libav.FaLibavOpenStrategyAudio)
	if fctxLibav == nil {
		libav.FALibavClose(libavSys, avio)
		return nil, err
	}
	_ = err
	defer libav.FALibavCloseFormat(libavSys, fctxLibav, false)

	// Non-owning view for stream/packet helpers
	fctx := medialibav.WrapFormatCtx(fctxLibav)

	// C: usage_event("Play audio", 1, USAGE_SEG("format", name))
	bs.usage.Event("Play audio", 1, "format", fctx.GetFormatName())

	// C: mp_configure(mp, MP_CAN_SEEK|MP_CAN_PAUSE, MP_BUFFER_SHALLOW,
	//   fctx->duration, "tracks")
	mediacore.MpConfigure(mp, bs.propManager,
		mediacore.MPCanSeek|mediacore.MPCanPause,
		mediacore.MPBufferShallow, fctx.GetDuration(), "tracks")

	// C: mp->mp_audio.mq_stream = -1; mp->mp_video.mq_stream = -1
	mp.Audio.Stream = -1
	mp.Video.Stream = -1

	// C: fw = media_format_create(fctx)
	fw := mediacore.MediaFormatCreate(fctxLibav)

	// C: find first AVMEDIA_TYPE_AUDIO stream → media_codec_create
	var cw *mediacore.MediaCodec
	for i := range fctx.GetNumStreams() {
		si := fctx.GetStreamInfo(i)
		if si == nil || si.CodecType != libav.AvmediaTypeAudio {
			continue
		}
		// C: media_codec_create(ctx->codec_id, 0, fw, ctx, NULL, mp)
		cw = mediacore.MediaCodecCreate(mediacore.CodecID(si.CodecID), 0,
			fw, fctx.CodecCtxFromStream(i).CPtr(), nil, mp)
		mp.Audio.Stream = i
		break
	}

	if cw == nil {
		mediacore.MediaFormatDeref(fw)
		return nil, errors.New("Unable to open codec")
	}
	defer mediacore.MediaCodecDeref(cw)
	defer mediacore.MediaFormatDeref(fw)

	// C: mp_become_primary(mp); mq = &mp->mp_audio
	mediacore.MpBecomePrimary(mp, bs.propManager)
	mq := mp.Audio

	// C: prop_set(mp->mp_prop_root, "format", PROP_SET_STRING,
	//   fctx->iformat->long_name)
	if bs.propManager != nil && mp.PropRoot != nil {
		fp := bs.propManager.Find(mp.PropRoot, "format")
		if fp == nil {
			fp = bs.propManager.CreateEx(mp.PropRoot, "format", nil, false, true)
		}
		if fp != nil {
			fp.SetString(fctx.GetFormatLongName())
		}
	}

	for {
		// C: fetch a new packet when mb == NULL
		if mb == nil {
			atomic.StoreInt32(&mp.EOF, 0)
			pktPtr, rerr := fctx.ReadPacketRaw()
			if rerr != nil {
				// C: non-EOF av_read_frame error — drain queues for
				// a navigation event, else report EOF
				for {
					e = mediacore.MpWaitForEmptyQueues(mp)
					if e == nil {
						break
					}
					if faEventIsType(e, event.EVENT_PLAYQUEUE_JUMP) ||
						faEventIsAction(e, event.ACTION_SKIP_BACKWARD) ||
						faEventIsAction(e, event.ACTION_SKIP_FORWARD) ||
						faEventIsAction(e, event.ACTION_STOP) {
						mediacore.MpFlush(mp)
						break
					}
					e = nil // event_release
				}
				if e == nil {
					e = &mediacore.MediaEvent{
						Type: int(event.EVENT_EOF),
						Data: &event.Event{Type: event.EVENT_EOF},
					}
				}
				goto out
			}
			if pktPtr == nil {
				// C: AVERROR_EOF || AVERROR(EIO) → MB_SPECIAL_EOF
				mb = mbSpecialEOF
				atomic.StoreInt32(&mp.EOF, 1)
				continue
			}

			si := packetStreamIndex(pktPtr)
			if si != mp.Audio.Stream {
				libav.AvPacketFree(pktPtr)
				continue
			}

			// C: mb = media_buf_from_avpkt_unlocked(mp, &pkt)
			mb = mediacore.MediaBufFromAVPkt(mp, pktPtr)
			mb.DataType = int(mediacore.MBAudio)

			pts, dts, dur, _ := packetFields(pktPtr)
			mb.PTS = faAudioRescale(fctx, pts, si)
			mb.DTS = faAudioRescale(fctx, dts, si)
			mb.Duration = faAudioRescale(fctx, dur, si)

			// C: mb->mb_cw = media_codec_ref(cw)
			mb.Codec = mediacore.MediaCodecRef(cw)
			mb.Stream = si

			if mb.PTS != mediacore.PTSUnset {
				offset := fctx.GetStartTime()
				if offset == mediacore.PTSUnset {
					offset = 0
				}
				mb.UserTime = mb.PTS + offset
				mb.DriveClock = 1
			}
			libav.AvPacketFree(pktPtr)
		}

		// C: try to send the buffer / catch an event
		if mb == mbSpecialEOF {
			// C: EOF — drain queues
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

		if faEventIsType(e, event.EVENT_PLAYQUEUE_JUMP) {
			mediacore.MpFlush(mp)
			break
		} else if faEventIsType(e, event.EVENT_CURRENT_TIME) {
			// C: playinfo_register_play past the 10s threshold
			if ets, ok := event.ConcreteOf(e.Data).(*event.EventTs); ok {
				if !registeredPlay && ets.Ts > playinfoAudioPlayThreshold {
					registeredPlay = true
					bs.metadata.PlayInfoRegisterPlay(url, 1)
				}
			}
		} else if faEventIsType(e, event.EVENT_SEEK) {
			if ets, ok := event.ConcreteOf(e.Data).(*event.EventTs); ok {
				var ts int64
				startTime := fctx.GetStartTime()
				if startTime != mediacore.PTSUnset {
					ts = max(ets.Ts+startTime, startTime)
				} else {
					ts = max(ets.Ts, 0)
				}
				fctx.SeekFrame(ts)
				faAudioSeekflush(mp, &mb)
			}
		} else if faEventIsAction(e, event.ACTION_SKIP_BACKWARD) {
			// C: if(mp->mp_seek_base < 1500000) goto skip
			if mp.SeekBase < 1500000 {
				mediacore.MpFlush(mp)
				break
			}
			var z int64
			if st := fctx.GetStartTime(); st != mediacore.PTSUnset {
				z = st
			}
			fctx.SeekFrame(z)
			faAudioSeekflush(mp, &mb)
		} else if faEventIsAction(e, event.ACTION_SKIP_FORWARD) ||
			faEventIsAction(e, event.ACTION_STOP) {
			// C: skip: mp_flush(mp); break
			mediacore.MpFlush(mp)
			break
		}
		e = nil // C: event_release(e)
	}

out:
	// C: if(mb != NULL && mb != MB_SPECIAL_EOF) media_buf_free_unlocked
	if mb != nil && mb != mbSpecialEOF {
		mediacore.MediaBufFreeUnlocked(mp, mb)
	}
	return e, nil
}

// mediaEventData unwraps the *event.Event (or subtype) carried by a
// MediaEvent (C: the event_t itself).
func mediaEventData(me *mediacore.MediaEvent) *event.Event {
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

// faEventIsType — C: event_is_type.
func faEventIsType(me *mediacore.MediaEvent, t event.EventType) bool {
	return me != nil && me.Type == int(t)
}

// faEventIsAction — C: event_is_action.
func faEventIsAction(me *mediacore.MediaEvent, at event.ActionType) bool {
	ev := mediaEventData(me)
	return ev != nil && event.IsAction(ev, at)
}

// packetStreamIndex / packetFields — AVPacket accessors used by the
// demux loop (the packet is freed after MediaBufFromAVPkt refs it).
func packetStreamIndex(pkt *libav.AVPacket) int {
	_, _, _, si, _, _ := libav.AvPacketFieldsPtr(pkt.CPtr())
	return si
}

func packetFields(pkt *libav.AVPacket) (pts, dts, duration int64, si int) {
	var size int
	var data []byte
	pts, dts, duration, si, size, data = libav.AvPacketFieldsPtr(pkt.CPtr())
	_ = size
	_ = data
	return
}
