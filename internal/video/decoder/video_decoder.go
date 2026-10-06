package decoder

import (
	"encoding/binary"
	"slices"

	"github.com/czz/movian-go/internal/event"
	"github.com/czz/movian-go/internal/libav"
	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/misc/avgtime"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/subtitles"
	tracepkg "github.com/czz/movian-go/internal/trace"
)

// C: VIDEO_DECODER_REORDER_SIZE / _MASK (video_decoder.h:27-28)
const (
	VideoDecoderReorderSize = 256
	VideoDecoderReorderMask = VideoDecoderReorderSize - 1
)

// C: VD_FRAME_SIZE_LEN / VD_FRAME_SIZE_MASK (video_decoder.h:85-86)
const (
	FrameSizeLen  = 16
	FrameSizeMask = FrameSizeLen - 1
	// PTSUnset — C: PTS_UNSET = 0x8000000000000000 (media.h:78)
	PTSUnset = mediacore.PTSUnset
)

// VideoDecoder represents a video decoder.
// C: video_decoder_t (video_decoder.h:37-124)
// decoderTS — C: trace() global reached by decoder file statics
// (h264_parser.c dump helpers, vtb.c logging).
var decoderTS struct{ ts *tracepkg.TraceSystem }

// SetTraceSystem injects the trace system (C: trace() global).
func SetTraceSystem(ts *tracepkg.TraceSystem) { decoderTS.ts = ts }

type VideoDecoder struct {
	// C: vd_decoder_thread — the joinable thread handle; Go: done chan
	done chan struct{}

	Hold bool // C: vd_hold

	MP *mediacore.MediaPipe // C: vd_mp

	NextPTS           int64 // C: vd_nextpts
	PrevPTS           int64 // C: vd_prevpts
	PrevPTSCnt        int   // C: vd_prevpts_cnt
	EstimatedDuration int64 // C: vd_estimated_duration

	Frame *libav.AVFrame // C: vd_frame (av_frame_alloc in vd_thread)

	// Temporary picture — C: vd_sws / vd_convert{,_width,_height,_pixfmt}
	// (FFmpeg 7: AVPicture is gone; Convert is a heap AVFrame)
	SWS           *libav.SwsContext // C: struct SwsContext *vd_sws
	Convert       *libav.AVFrame    // C: AVPicture vd_convert (by value in C)
	ConvertWidth  int
	ConvertHeight int
	ConvertPixFmt int

	// Stats — C: vd_decode_time / vd_upload_time (avgtime_t, embedded)
	DecodeTime avgtime.AvgTime
	uploadTime avgtime.AvgTime

	// Deinterlacing — C: vd_interlaced
	Interlaced bool

	// DVD / SPU — C: vd_dvd_clut / vd_pci / vd_spu_curbut / vd_spu_repaint
	dvdClut    [16]uint32
	PCI        []byte // C: pci_t vd_pci (raw copy of mb_data)
	SPUCurBut  int    // C: int vd_spu_curbut — DVD button index (mb_data32)
	SPURepaint bool   // C: int vd_spu_repaint — 0/1 flag only

	// Video overlay and subtitles — C: vd_subpts_user_time / vd_subpts_ts /
	// vd_ext_subtitles
	subPTSUserTime int64
	subPTSTS       int64
	extSubtitles   *subtitles.ExtSubtitles

	// Bitrate computation — C: vd_frame_size[] / vd_frame_size_ptr
	frameSize    [FrameSizeLen]int
	frameSizePtr int

	// Framerate — C: vd_fps_ptr / vd_fps_array[16] / vd_fps_delta / vd_fps
	fpsPtr   int
	fpsArray [16]int64
	fpsDelta int64
	fps      float32

	RenderComponent any // C: void *vd_render_component (opaque hand-back)

	// Reordering — C: vd_reorder_ptr / vd_reorder[256] / vd_reorder_current /
	// vd_seen_bframe. C stores metas by value.
	ReorderPtr     int
	Reorder        [VideoDecoderReorderSize]mediacore.MediaBufMeta
	ReorderCurrent *mediacore.MediaBufMeta
	seenBFrame     int // C: vd_seen_bframe (countdown, 0 = not seen)

	// C: vd_debug_discont_in / vd_debug_discont_out
	debugDiscontIn  mediacore.MediaDiscontinuityAux
	debugDiscontOut mediacore.MediaDiscontinuityAux

	// Go-side plumbing (no C counterpart — C routes frames through
	// mp->mp_video_frame_deliver and the bitstream filter lives in the
	// codec wrapper): DeliverCallback routes decoded frames to the GL
	// engine; BSF is the avcc→annexb bitstream filter.
	DeliverCallback mediacore.VideoFrameDeliver
	BSF             *libav.BSFContext
}

// setupTimings — C: vd_init_timings (video_decoder.c:47-58)
func (vd *VideoDecoder) setupTimings() {
	// C: vd_prevpts/vd_nextpts = AV_NOPTS_VALUE (= PTS_UNSET)
	vd.PrevPTS = libav.AVNoPTSValue
	vd.NextPTS = libav.AVNoPTSValue
	vd.EstimatedDuration = 0

	// C: for(i=0; i<VD_FRAME_SIZE_LEN; i++) vd_fps_array[i] = PTS_UNSET —
	// upstream quirk: the fps ring init borrows the frame-size constant.
	// Preserved 1:1 (do not "fix" to len(vd.fpsArray)).
	for i := range FrameSizeLen {
		vd.fpsArray[i] = mediacore.PTSUnset
	}

	vd.fpsDelta = 0
}

// InferPTS — C: video_decoder_infer_pts (video_decoder.c:64-80)
func (vd *VideoDecoder) InferPTS(mbm *mediacore.MediaBufMeta, isBFrame bool) int64 {
	if vd == nil || mbm == nil {
		return PTSUnset
	}
	if isBFrame {
		vd.seenBFrame = 100
	}

	if vd.seenBFrame != 0 {
		vd.seenBFrame--
	}

	if mbm.PTS == mediacore.PTSUnset && mbm.DTS != mediacore.PTSUnset &&
		(vd.seenBFrame == 0 || isBFrame) {
		return mbm.DTS
	}

	return mbm.PTS
}

// SetCurrentTime — C: video_decoder_set_current_time
// (video_decoder.c:86-136)
func (vd *VideoDecoder) SetCurrentTime(userTime int64, epoch int, pts int64, driveMode int) {
	if vd == nil {
		return
	}
	mp := vd.MP

	lastPTS := vd.fpsArray[vd.fpsPtr]
	vd.fpsArray[vd.fpsPtr] = pts

	vd.fpsPtr = (vd.fpsPtr + 1) & (len(vd.fpsArray) - 1) // C: ARRAYSIZE(vd_fps_array) - 1

	if pts != mediacore.PTSUnset && lastPTS != mediacore.PTSUnset {
		delta := pts - lastPTS

		if delta < 10000000 && delta > 10000 {
			if vd.fpsDelta != 0 {
				vd.fpsDelta += (delta - vd.fpsDelta) >> 4
			} else {
				vd.fpsDelta = delta
			}
			// C: 100000000LL * VD_FRAME_SIZE_LEN / vd_fps_delta — upstream
			// quirk: fps window uses the frame-size constant, not
			// ARRAYSIZE(vd_fps_array). Preserved 1:1 (do not "fix").
			tmp := 100000000 * int64(FrameSizeLen) / vd.fpsDelta
			fps := float32(tmp) / 100.0

			if fps != vd.fps {
				vd.fps = fps
				// C: prop_set_float(vd->vd_mp->mp_prop_fps, vd->vd_fps)
				if mp != nil && mp.PropFPS != nil {
					mp.PropFPS.SetFloat(fps)
				}
			}
		}
	}

	if driveMode == 2 {
		if pts == mediacore.PTSUnset || userTime == mediacore.PTSUnset {
			return
		}
		userTime = pts - userTime
	} else {
		if userTime == mediacore.PTSUnset {
			return
		}
	}

	if mp != nil {
		// C: mp_set_current_time(vd->vd_mp, user_time, epoch, 0)
		var pmgr *propcore.PropManager
		if mp.PropRoot != nil {
			pmgr = mp.PropRoot.Manager()
		}
		mediacore.MpSetCurrentTime(mp, pmgr, userTime, epoch, 0)
	}

	if pts == mediacore.PTSUnset {
		return
	}

	// C: vd->vd_mp->mp_svdelta (vd_mp always set in C; nil-guard for Go)
	var svdelta int64
	if mp != nil {
		svdelta = mp.SVDelta
	}
	vd.subPTSUserTime = userTime - svdelta
	vd.subPTSTS = pts - svdelta

	pts -= svdelta

	if vd.extSubtitles != nil && mp != nil {
		// C: subtitles_pick(vd->vd_ext_subtitles, vd->vd_subpts_user_time, pts, vd->vd_mp)
		subtitles.SubtitlesPick(vd.extSubtitles, vd.subPTSUserTime, pts, mp)
	}
}

// DeliverFrame — C: video_deliver_frame (video_decoder.c:142-153)
func (vd *VideoDecoder) DeliverFrame(info *mediacore.FrameInfo) int {
	if vd == nil || info == nil {
		return -1
	}
	mp := vd.MP
	if mp == nil || mp.VideoFrameDeliver == nil {
		return -1
	}

	r := mp.VideoFrameDeliver(info, mp.VideoFrameOpaque)

	if info.DriveClock != 0 && r == 0 {
		vd.SetCurrentTime(info.UserTime, info.Epoch,
			info.PTS, info.DriveClock)
	}

	return r
}

// updateVBitrate — C: update_vbitrate (video_decoder.c:159-182)
func updateVBitrate(mp *mediacore.MediaPipe, mq *mediacore.MediaQueue,
	mb *mediacore.MediaBuf, vd *VideoDecoder) {
	size := mb.Size
	duration := mb.Duration
	if duration == 0 {
		duration = vd.EstimatedDuration
	}
	vd.frameSize[vd.frameSizePtr] = size
	vd.frameSizePtr = (vd.frameSizePtr + 1) & FrameSizeMask

	if duration == 0 || (vd.frameSizePtr&7) != 0 {
		return
	}

	var sum int64
	for i := range FrameSizeLen {
		sum += int64(vd.frameSize[i])
	}

	sum = 8000000 * sum / FrameSizeLen / duration
	if mq.PropBitrate != nil {
		mq.PropBitrate.SetInt(int(sum / 1000))
	}
}

// mediaDiscontinuityDebug — C: media_discontinuity_debug (media.c:895) —
// dead code upstream (`return;` as first statement). Kept for parity.
func mediaDiscontinuityDebug(aux *mediacore.MediaDiscontinuityAux,
	dts, pts int64, epoch int, skip int, prefix string) {
	return
}

// decoderThread — C: vd_thread (video_decoder.c:187-497)
func (vd *VideoDecoder) decoderThread() {
	mp := vd.MP
	mq := mp.Video // C: &mp->mp_video
	var mb *mediacore.MediaBuf
	var cur *mediacore.MediaBuf
	var mcCurrent *mediacore.MediaCodec
	run := true
	reqsize := -1
	restart := false

	var mbm *mediacore.MediaBufMeta

	// C: vd->vd_frame = av_frame_alloc()
	vd.Frame = libav.AvFrameAlloc()

	mp.Mutex.Lock()

	for run {
		if mbm != vd.ReorderCurrent {
			mbm = vd.ReorderCurrent
			mp.Mutex.Unlock()

			vd.EstimatedDuration = mbm.Duration

			if mbm.DriveClock != 0 {
				vd.SetCurrentTime(mbm.UserTime,
					mbm.Epoch, mbm.PTS, mbm.DriveClock)
			}
			mp.Mutex.Lock()
			continue
		}

		var ctrl, data, aux *mediacore.MediaBuf
		if len(mq.CtrlQueue) > 0 {
			ctrl = mq.CtrlQueue[0]
		}
		if len(mq.DataQueue) > 0 {
			data = mq.DataQueue[0]
		}
		if len(mq.AuxQueue) > 0 {
			aux = mq.AuxQueue[0]
		}

		retry := false
		if ctrl != nil {
			mq.CtrlQueue = mq.CtrlQueue[1:]
			mb = ctrl
		} else if aux != nil && aux.PTS < vd.subPTSTS+1000000 {
			if vd.Hold {
				mq.Avail.Wait()
				continue
			}
			mq.AuxQueue = mq.AuxQueue[1:]
			mb = aux
		} else if cur != nil {
			if vd.Hold {
				mq.Avail.Wait()
				continue
			}
			mb = cur
			retry = true // C: goto retry_current
		} else if data != nil {
			if vd.Hold {
				mq.Avail.Wait()
				continue
			}
			mq.DataQueue = mq.DataQueue[1:]
			mediacore.MpCheckUnderrun(mp)
			mb = data
			if mb.DTS != mediacore.PTSUnset {
				mq.LastDeqDTS = mb.DTS
			}
		} else {
			mq.Avail.Wait()
			continue
		}

		if !retry {
			mq.PacketsCurrent--
			mp.BufferCurrent -= mediacore.MbBufferedSize(mb)
			mediacore.MqUpdateStats(mp, mq, 1)

			mp.Backpressure.Signal()
		}

		// retry_current:
		mc := mb.Codec

		if mediacore.MediaBufDataType(mb.DataType) == mediacore.MBVideo &&
			mc != nil && mc.DecodeLocked != nil {

			if mc != mcCurrent {
				mp.Mutex.Unlock()
				if mcCurrent != nil {
					mediacore.MediaCodecDeref(mcCurrent)
				}
				mcCurrent = mediacore.MediaCodecRef(mc)
				if mq.PropTooSlow != nil {
					mq.PropTooSlow.SetInt(0)
				}
				mp.Mutex.Lock()
			}

			mq.NoDataInterest = true
			if mc.DecodeLocked(mc, vd, mq, mb) != 0 {
				cur = mb
				mq.Avail.Wait()
				continue
			}
			mq.NoDataInterest = false
			cur = nil

			updateVBitrate(mp, mq, mb, vd)
			mediacore.MediaBufFreeLocked(mp, mb)
			continue
		}

		mp.Mutex.Unlock()

		switch mediacore.MediaBufDataType(mb.DataType) {
		case mediacore.MBCtrlExit:
			run = false

		case mediacore.MBCtrlPause:
			vd.Hold = true

		case mediacore.MBCtrlPlay:
			vd.Hold = false

		case mediacore.MBCtrlFlush:
			if cur != nil {
				mediacore.MediaBufFreeUnlocked(mp, cur)
				mq.NoDataInterest = false
				cur = nil
			}
			vd.setupTimings()
			vd.Interlaced = false

			mp.OverlayMutex.Lock()
			mediacore.VideoOverlayFlushLocked(mp, true)
			mediacore.DvdspuFlushLocked(mp)
			mp.OverlayMutex.Unlock()

			if mp.VideoFrameDeliver != nil {
				mp.VideoFrameDeliver(nil, mp.VideoFrameOpaque)
			}

			if mcCurrent != nil && mcCurrent.Flush != nil {
				mcCurrent.Flush(mcCurrent, vd)
			}

			if mb.Data32 != 0 {
				// Final flush, sent from mp_shutdown(). Release the
				// codec so the last reference (mc_current) closes it.
				if mcCurrent != nil {
					mediacore.MediaCodecDeref(mcCurrent)
					mcCurrent = nil
				}
				if mp.SetVideoCodec != nil {
					mp.SetVideoCodec(mediacore.FourCCNone, nil,
						mp.VideoFrameOpaque, nil)
				}
				if mp.VideoFrameDeliver != nil {
					mp.VideoFrameDeliver(nil, mp.VideoFrameOpaque)
				}
			}

			if mp.SeekVideoDone != nil {
				mp.SeekVideoDone(mp)
			}

		case mediacore.MBVideo:
			if mc != mcCurrent {
				if mcCurrent != nil {
					mediacore.MediaCodecDeref(mcCurrent)
					if mp.SetVideoCodec != nil {
						mp.SetVideoCodec(mediacore.FourCCNone, nil,
							mp.VideoFrameOpaque, nil)
					}
				}
				mcCurrent = mediacore.MediaCodecRef(mc)
				if mq.PropTooSlow != nil {
					mq.PropTooSlow.SetInt(0)
				}
			}

			if restart {
				if mc != nil && mc.Restart != nil {
					mc.Restart(mc)
				}
				restart = false
			}

			skip := 0
			if mb.Flags.Skip {
				skip = 1
			}
			mediaDiscontinuityDebug(&vd.debugDiscontIn,
				mb.DTS, mb.PTS, int(mp.Epoch), skip, "VDEC")

			if mc != nil && mc.Decode != nil {
				mc.Decode(mc, vd, mq, mb, reqsize)
			}
			updateVBitrate(mp, mq, mb, vd)
			reqsize = -1

		case mediacore.MBCtrlReqOutputSize:
			reqsize = int(mb.Data32)

		case mediacore.MBCtrlRestart:
			restart = true

		case mediacore.MBCtrlReconfigure:
			if mb.Codec != nil && mb.Codec.Reconfigure != nil {
				mb.Codec.Reconfigure(mc, mb.FrameInfo)
			}

		case mediacore.MBDVDResetSPU:
			mp.OverlayMutex.Lock()
			vd.SPUCurBut = 1
			mediacore.DvdspuFlushLocked(mp)
			mp.OverlayMutex.Unlock()

		case mediacore.MBCtrlDVDHilite:
			vd.SPUCurBut = int(mb.Data32)
			vd.SPURepaint = true

		case mediacore.MBDVDPCI:
			vd.PCI = append(vd.PCI[:0], mb.Data...)
			vd.SPURepaint = true
			// C: event_create(EVENT_DVD_PCI, ...) + mp_enqueue_event
			mp.EventQueue = append(mp.EventQueue, &mediacore.MediaEvent{
				Type: int(event.EVENT_DVD_PCI),
				Data: slices.Clone(mb.Data),
			})
			mp.EventCond.Signal()

		case mediacore.MBDVDCLUT:
			mediacore.DvdspuDecodeClut(vd.dvdClut[:], bytesToU32(mb.Data, 16))

		case mediacore.MBDVDSPU:
			mediacore.DvdspuEnqueue(mp, mb.Data, mb.Size,
				vd.dvdClut[:], 0, 0, mb.PTS)

		case mediacore.MBCtrlDVDSPU2:
			// C: clut at data[0:64], w/h at uint32[16]/[17],
			// SPU packet at data+72.
			clut := bytesToU32(mb.Data, 16)
			w := int(binary.NativeEndian.Uint32(mb.Data[64:]))
			h := int(binary.NativeEndian.Uint32(mb.Data[68:]))
			mediacore.DvdspuEnqueue(mp, mb.Data[72:], mb.Size-72,
				clut, w, h, mb.PTS)

		case mediacore.MBSubtitle:
			// C: if(vd->vd_ext_subtitles == NULL &&
			//        mb->mb_stream == mq->mq_stream2)
			if vd.extSubtitles == nil && mb.Stream == mq.Stream2 &&
				mp.Sys.VideoOverlayDecode != nil {
				mp.Sys.VideoOverlayDecode(mp, mb)
			}

		case mediacore.MBCtrlFlushSubtitles:
			mp.OverlayMutex.Lock()
			mediacore.VideoOverlayFlushLocked(mp, true)
			mp.OverlayMutex.Unlock()

		case mediacore.MBCtrlExtSubtitle:
			if vd.extSubtitles != nil {
				// C: subtitles_destroy(vd->vd_ext_subtitles)
				subtitles.SubtitlesDestroy(vd.extSubtitles)
			}

			// Steal subtitle from the media_buf
			vd.extSubtitles, _ = mb.DataOpaque.(*subtitles.ExtSubtitles)
			mb.DataOpaque = nil
			mp.OverlayMutex.Lock()
			mediacore.VideoOverlayFlushLocked(mp, true)
			mp.OverlayMutex.Unlock()

		default:
			// C: abort()
			panic("video_decoder: unhandled mb_data_type")
		}

		mp.Mutex.Lock()
		mediacore.MediaBufFreeLocked(mp, mb)
	}

	if cur != nil {
		mediacore.MediaBufFreeLocked(mp, cur)
	}

	mq.NoDataInterest = false

	mp.Mutex.Unlock()

	if mcCurrent != nil {
		mediacore.MediaCodecDeref(mcCurrent)
		if mp.SetVideoCodec != nil {
			mp.SetVideoCodec(mediacore.FourCCNone, nil,
				mp.VideoFrameOpaque, nil)
		}
	}

	if vd.extSubtitles != nil {
		subtitles.SubtitlesDestroy(vd.extSubtitles)
		vd.extSubtitles = nil
	}

	// C: av_frame_free(&vd->vd_frame)
	libav.AvFrameFree(vd.Frame)
	vd.Frame = nil

	close(vd.done)
}

// NewVideoDecoder — C: video_decoder_create (video_decoder.c:502-516)
func NewVideoDecoder(mp *mediacore.MediaPipe) *VideoDecoder {
	vd := &VideoDecoder{
		MP:   mp,
		done: make(chan struct{}),
	}

	// C: vd->vd_mp = mp_retain(mp)
	if mp != nil {
		mediacore.MpRetain(mp)
	}

	// C: vd_init_timings(vd)
	vd.setupTimings()

	// C: hts_thread_create_joinable("video decoder", &vd->vd_decoder_thread,
	//   vd_thread, vd, THREAD_PRIO_VIDEO)
	if mp != nil {
		go vd.decoderThread()
	} else {
		close(vd.done)
	}

	return vd
}

// Stop — C: video_decoder_stop (video_decoder.c:522-532)
func (vd *VideoDecoder) Stop() {
	if vd == nil || vd.MP == nil {
		return
	}
	mp := vd.MP

	// C: mp_send_cmd(mp, &mp->mp_video, MB_CTRL_EXIT)
	mediacore.MpSendCmd(mp, mp.Video, int(mediacore.MBCtrlExit))

	// C: hts_thread_join(&vd->vd_decoder_thread)
	<-vd.done

	// C: mp_release(vd->vd_mp); vd->vd_mp = NULL
	mediacore.MpRelease(mp)
	vd.MP = nil
}

// Destroy — C: video_decoder_destroy (video_decoder.c:538-544).
// Contract: must run after Stop() — C requires the decoder thread to be
// joined before the decoder is torn down; destroy does not stop it.
// The MP!=nil self-stop below is a Go-side safety net enforcing that
// invariant (a missed Stop would leak the goroutine and free sws/convert
// under the running thread). Callers must still sequence Stop→Destroy
// from a single teardown path — never from the decoder thread itself
// (self-join deadlock, same as C hts_thread_join).
func (vd *VideoDecoder) Destroy() {
	if vd == nil {
		return
	}
	if vd.MP != nil {
		vd.Stop()
	}
	// C: sws_freeContext(vd->vd_sws)
	libav.SwsFreeContext(vd.SWS)
	vd.SWS = nil
	// C: avpicture_free(&vd->vd_convert)
	libav.AvFrameFree(vd.Convert)
	vd.Convert = nil
}

// bytesToU32 — C: (const uint32_t *)mb->mb_data — host-native endian words
// (the buffer format is an internal native uint32 array, same as C).
func bytesToU32(data []byte, n int) []uint32 {
	out := make([]uint32, n)
	for i := 0; i < n && i*4+4 <= len(data); i++ {
		out[i] = binary.NativeEndian.Uint32(data[i*4:])
	}
	return out
}
