package libav

/*
#include <libavcodec/avcodec.h>
#include <string.h>
*/
import "C"

import (
	"fmt"
	"unsafe"

	"github.com/czz/movian-go/internal/libav"
	"github.com/czz/movian-go/internal/media/buffer"
	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/misc/avgtime"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/video/decoder"
)

// vdFrame returns the decoder's persistent AVFrame (C: vd->vd_frame,
// allocated once in vd_thread). Lazily allocated here for decoder
// instances driven outside vd_thread (the synchronous playback path);
// av_frame_unref clears the previous frame's buffers before reuse.
func vdFrame(vd *VideoDecoder) *libav.AVFrame {
	if vd == nil {
		return nil
	}
	if vd.Frame == nil {
		vd.Frame = libav.AvFrameAlloc()
	}
	if vd.Frame == nil {
		return nil
	}
	avFrameUnref(vd.Frame)
	return vd.Frame
}

// Color space constants
const (
	ColorSpaceBT709    = 0
	ColorSpaceBT601    = 1
	ColorSpaceSMPTE240 = 2
)

// FFmpeg color space constants
const (
	Avcolspcbt709     = 1
	Avcolspcbt470bg   = 5
	Avcolspcsmpte170m = 6
	Avcolspcsmpte240m = 7
)

// Pixel format constants
const (
	AVPixFmtYuv420p = 0
)

// AV_NOPTS_VALUE represents no PTS value
// C: AV_NOPTS_VALUE = PTS_UNSET = INT64_C(0x8000000000000000)
const AV_NOPTS_VALUE = int64(-9223372036854775808)

// libavColorspaceTbl maps FFmpeg color space to Movian color space
var libavColorspaceTbl = map[int]int{
	Avcolspcbt709:     ColorSpaceBT709,
	Avcolspcbt470bg:   ColorSpaceBT601,
	Avcolspcsmpte170m: ColorSpaceBT601,
	Avcolspcsmpte240m: ColorSpaceSMPTE240,
}

// vdValidDuration checks if duration is valid
func vdValidDuration(t int64) bool {
	return t > 10000 && t < 1000000
}

// VideoDecoder is the canonical C video_decoder_t — a type alias for
// pkg/video/decoder.VideoDecoder so both the decoder thread and the lavc
// decode path share the same state (C uses a single video_decoder_t).
type VideoDecoder = decoder.VideoDecoder

// libavDecodeVideoCb — C: libav_decode_video (libav.c:327) installed as
// mc->decode. Adapts the canonical void signature (vd as any).
func libavDecodeVideoCb(mc *mediacore.MediaCodec, vdi any, mq *mediacore.MediaQueue, mb *mediacore.MediaBuf, reqsize int) {
	vd, _ := vdi.(*VideoDecoder)
	LibAVDecodeVideo(mc, vd, mq, mb, reqsize)
}

// libavVideoFlushCb — C: libav_video_flush (libav.c:262) installed as
// mc->flush.
func libavVideoFlushCb(mc *mediacore.MediaCodec, vdi any) {
	vd, _ := vdi.(*VideoDecoder)
	LibAVVideoFlush(mc, vd)
}

// mediaCodecCreateLavcOpen — C: media_codec_create_lavc (libav.c:401)
// as a codec_def open() callback.
func mediaCodecCreateLavcOpen(mc *mediacore.MediaCodec, mcp *mediacore.MediaCodecParams, mp *mediacore.MediaPipe) int {
	if err := MediaCodecCreateLavc(mc, mcp, mp); err != nil {
		return -1
	}
	return 0
}

// C: REGISTER_CODEC(NULL, media_codec_create_lavc, 1000) — libav.c:457.
// Lowest preference (highest prio value): the catch-all fallback claimed
// when no hardware/specialized codec claims the codec_id.
func registerLibavVideoCodec() {
	mediacore.MediaRegisterCodec(&mediacore.CodecDef{
		Open: mediaCodecCreateLavcOpen,
		Prio: 1000,
	})
}

// VideoDecoderTestGetCodecPixFmt returns ctx->pix_fmt — used by
// integration tests to verify a hw pix_fmt (e.g. LavcPixFmtVAAPI)
// was negotiated by get_format.
func VideoDecoderTestGetCodecPixFmt(vd *VideoDecoder, mc *mediacore.MediaCodec) int {
	if mc == nil || mc.Ctx == nil {
		return -1
	}
	return avCodecCtxPixFmt(mc.Ctx)
}

// VideoDecoderTestGetCodecFlags returns the flags from the codec context.
// Used by integration tests to verify AV_CODEC_FLAG_COPY_OPAQUE is set.
func VideoDecoderTestGetCodecFlags(vd *VideoDecoder, mc *mediacore.MediaCodec) int {
	if mc == nil || mc.Ctx == nil {
		return 0
	}
	return avCodecContextGetFlags(mc.Ctx)
}

// TestGetPacketOpaque returns the opaque field from an AVPacket as uintptr.
// Used by integration tests to verify opaque is set before send.
func TestGetPacketOpaque(pkt *libav.AVPacket) uintptr {
	return avPacketGetOpaque(pkt)
}

// TestGetFrameOpaque returns the opaque field from an AVFrame as uintptr.
// Used by integration tests to verify frame opaque propagation.
func TestGetFrameOpaque(frame *libav.AVFrame) uintptr {
	return avFrameGetOpaque(frame)
}

// TestPacketAlloc allocates an AVPacket for test use.
func TestPacketAlloc() *libav.AVPacket {
	return avPacketAlloc()
}

// TestPacketFree frees an AVPacket allocated by TestPacketAlloc.
func TestPacketFree(pkt *libav.AVPacket) {
	avPacketFree(pkt)
}

// TestPacketSetOpaque sets the opaque field on an AVPacket.
func TestPacketSetOpaque(pkt *libav.AVPacket, value uintptr) {
	avPacketSetOpaque(pkt, value)
}

// TestNewPacket creates a packet from raw data (for test use).
func TestNewPacket(pkt *libav.AVPacket, data unsafe.Pointer, size int) int {
	return avNewPacket(pkt, data, size)
}

// TestPacketSetPTS sets the PTS field on an AVPacket.
func TestPacketSetPTS(pkt *libav.AVPacket, pts int64) {
	avPacketSetPTS(pkt, pts)
}

// TestSendPacket sends a packet to the decoder.
func TestSendPacket(ctx *libav.AVCodecContext, pkt *libav.AVPacket) int {
	return avcodecSendPacket(ctx, pkt)
}

// TestReceiveFrame receives a frame from the decoder.
func TestReceiveFrame(ctx *libav.AVCodecContext, frame *libav.AVFrame) int {
	return avcodecReceiveFrame(ctx, frame)
}

// TestPictureAllocStruct allocates an AVFrame struct.
func TestPictureAllocStruct() *libav.AVFrame {
	return avPictureAllocStruct()
}

// TestPictureFree frees an AVFrame struct.
func TestPictureFree(frame *libav.AVFrame) {
	avPictureFree(frame)
}

// TestFrameUnref unrefs an AVFrame.
func TestFrameUnref(frame *libav.AVFrame) {
	avFrameUnref(frame)
}

// TestFlushBuffers flushes the decoder buffers.
func TestFlushBuffers(ctx *libav.AVCodecContext) {
	avcodecFlushBuffers(ctx)
}

// AvgTimeStart — C: avgtime_start. Keeps the historical package-local
// name; delegates to the canonical avgtime implementation.
func AvgTimeStart(a *avgtime.AvgTime) {
	a.StartTiming()
}

// AvgTimeStop — C: avgtime_stop. Returns the rolling average in µs.
func AvgTimeStop(a *avgtime.AvgTime, avgProp, peakProp *propcore.Prop) int {
	return a.Stop(avgProp, peakProp)
}

// videoDecoderInferPTS — C: video_decoder_infer_pts. Delegates to the
// canonical implementation in pkg/video/decoder (shared video_decoder_t).
func videoDecoderInferPTS(mbm *mediacore.MediaBufMeta, vd *VideoDecoder, isBFrame bool) int64 {
	return vd.InferPTS(mbm, isBFrame)
}

// videoDeliverFrame delivers a decoded frame to the video pipeline.
// Canonical route: mp.VideoFrameDeliver (C: mp->mp_video_frame_deliver).
// The vd.DeliverCallback fallback serves the synchronous playback path
// where no media-pipe deliver hook is installed.
func videoDeliverFrame(vd *VideoDecoder, fi *mediacore.FrameInfo) error {
	if vd == nil || vd.MP == nil {
		return nil
	}

	if vd.MP.VideoFrameDeliver != nil {
		if vd.DeliverFrame(fi) != 0 {
			return fmt.Errorf("video frame delivery failed")
		}
		return nil
	}

	if fi.DriveClock != 0 {
		videoDecoderSetCurrentTime(vd, fi.UserTime, fi.Epoch, fi.PTS, fi.DriveClock)
	}

	if vd.DeliverCallback != nil {
		return vd.DeliverCallback(fi)
	}
	return nil
}

// videoDecoderSetCurrentTime — C: video_decoder_set_current_time.
// Delegates to the canonical implementation (FPS tracking, current
// time, subtitle timestamp state).
func videoDecoderSetCurrentTime(vd *VideoDecoder, userTime int64, epoch int, pts int64, driveMode int) {
	vd.SetCurrentTime(userTime, epoch, pts, driveMode)
}

// LibAVDeliverFrame delivers a decoded frame with aspect ratio, PTS, duration calculation
// This is a high-level function that orchestrates frame delivery
func LibAVDeliverFrame(vd *VideoDecoder, mp *mediacore.MediaPipe, mq *mediacore.MediaQueue, ctx *libav.AVCodecContext, frame *libav.AVFrame, mbm *mediacore.MediaBufMeta, decodeTime int, mc *mediacore.MediaCodec) error {
	// VAAPI extension (no C counterpart): a hw frame carries a
	// VASurfaceID in data[3], not CPU planes. When the GL backend can
	// import dma-bufs (video.VaapiEGLZeroCopy — EGL context +
	// EGL_EXT_image_dma_buf_import) the frame is delivered as-is and
	// the 'VAAE' engine exports it via vaExportSurfaceHandle. Otherwise
	// pull it into a software frame (av_hwframe_transfer_data → NV12)
	// and continue exactly as for software decode; av_frame_free also
	// unrefs the buffers.
	pixFmt := avFrameFormat(frame)
	vaapiZeroCopy := pixFmt == LavcPixFmtVAAPI &&
		mp.Sys.VS != nil && mp.Sys.VS.VaapiEGL.ZeroCopy()
	if (pixFmt == LavcPixFmtVAAPI || pixFmt == LavcPixFmtCUDA ||
		pixFmt == LavcPixFmtD3D11VA || pixFmt == LavcPixFmtDXVA2VLD) &&
		!vaapiZeroCopy {
		// NVDEC extension: CUDA frames reach the same transfer
		// path — NVDEC never goes zero-copy here because the
		// renderer runs on a different GPU (Optimus/Intel).
		sw := AvFrameAllocRaw()
		ret := -1
		if sw != nil {
			ret = HwframeTransfer(sw, frame)
		}
		if ret < 0 {
			lavTS.Error("HWACCEL", "hwframe transfer failed ret=%d — frame dropped", ret)
			AvFrameFreeRaw(sw)
			return nil
		}
		defer AvFrameFreeRaw(sw)
		frame = sw
	}

	// Compute aspect ratio
	var darNum, darDen int
	switch mbm.AspectOverride {
	case 0:
		darNum = avFrameWidth(frame)
		darDen = avFrameHeight(frame)

		sarNum := avFrameSARNum(frame)
		sarDen := avFrameSARDen(frame)
		if sarNum != 0 {
			darNum *= sarNum
			darDen *= sarDen
		} else if mc != nil && mc.SARNum != 0 {
			darNum *= int(mc.SARNum)
			darDen *= int(mc.SARDen)
		}
	case 1:
		darNum = 4
		darDen = 3
	case 2:
		darNum = 16
		darDen = 9
	}

	// Infer PTS
	isBFrame := avFramePictType(frame) == AV_PICTURE_TYPE_B
	pts := videoDecoderInferPTS(mbm, vd, isBFrame)

	// Get duration
	duration := mbm.Duration
	if !vdValidDuration(duration) {
		duration = vd.EstimatedDuration
	}

	// Use estimated PTS if no PTS available
	if pts == AV_NOPTS_VALUE && vd.NextPTS != AV_NOPTS_VALUE {
		pts = vd.NextPTS
	}

	// Estimate duration from PTS difference
	if pts != AV_NOPTS_VALUE && vd.PrevPTS != AV_NOPTS_VALUE && vd.PrevPTSCnt > 0 {
		t := (pts - vd.PrevPTS) / int64(vd.PrevPTSCnt)
		if vdValidDuration(t) {
			vd.EstimatedDuration = t
			if duration == 0 {
				duration = t
			}
		}
	}

	// Add repeat pict duration
	repeatPict := avFrameRepeatPict(frame)
	duration += int64(repeatPict) * duration / 2

	// Update PTS tracking
	if pts != AV_NOPTS_VALUE {
		vd.PrevPTS = pts
		vd.PrevPTSCnt = 0
	}
	vd.PrevPTSCnt++

	// Drop frame with zero duration
	if duration == 0 {
		return nil
	}

	// Set too slow property
	if mq != nil && mq.PropTooSlow != nil {
		tooSlow := int64(decodeTime) > duration
		if tooSlow {
			mq.PropTooSlow.SetInt(1)
		} else {
			mq.PropTooSlow.SetInt(0)
		}
	}

	// Update next PTS
	if pts != AV_NOPTS_VALUE {
		vd.NextPTS = pts + duration
	} else {
		vd.NextPTS = AV_NOPTS_VALUE
	}

	// Update interlacing flag
	interlacedFrame := avFrameInterlacedFrame(frame) != 0
	if !mbm.DisableDeinterlacer {
		vd.Interlaced = vd.Interlaced || interlacedFrame
	}

	// Build FrameInfo
	// Capture frame->opaque BEFORE AVFrame is unreffed/cleared.
	// The opaque value identifies the original packet in the reorder buffer.
	// 0 = not set (fallback), N = Reorder[N-1].
	opaqueVal := avFrameGetOpaque(frame)

	fi := mediacore.FrameInfo{
		Width:      avFrameWidth(frame),
		Height:     avFrameHeight(frame),
		DARNum:     darNum,
		DARDen:     darDen,
		PTS:        pts,
		Duration:   int64(duration),
		Epoch:      mbm.Epoch,
		UserTime:   mbm.UserTime,
		DriveClock: int(mbm.DriveClock),
		Interlaced: vd.Interlaced,
		TFF:        avFrameTopFieldFirst(frame) != 0,
		Prescaled:  false,
		ColorSpace: -1, // C: COLOR_SPACE_UNSET=0 → default (height-based); Go 0-based constants use -1 as unset (see vtb_darwin.go)
		Opaque:     opaqueVal,
		Type:       mediacore.FourCCLAVC, // C: 'LAVC'
	}

	// Map color space
	colorSpace := avFrameColorSpace(frame)
	if cs, ok := libavColorspaceTbl[colorSpace]; ok {
		fi.ColorSpace = cs
	}

	// C: libav.c:178-200 — deliver the frame as-is first; engines return
	// 1 if they need YUV420P conversion (0 = consumed, -1 = fail).
	// Chroma plane height is ceil(h >> vshift) of the SOURCE format —
	// not always (h+1)/2: a 422/444 frame's chroma is taller and a
	// 420-sized buffer would be over-read by the engine.
	_, srcVShift := PixFmtChromaSubSample(avFrameFormat(frame))
	for i := range 3 {
		dataPtr := avFrameData(frame, i)
		pitch := avFrameLinesize(frame, i)
		if dataPtr != nil && pitch > 0 {
			height := fi.Height
			if i > 0 {
				height = (height + (1 << srcVShift) - 1) >> srcVShift
			}
			size := pitch * height
			// Copy frame data to Go-managed slice (safe from avFrameUnref)
			fi.Data[i] = make([]byte, size)
			copy(fi.Data[i], unsafe.Slice((*byte)(dataPtr), size))
		}
		fi.Pitch[i] = pitch
	}
	fi.PixFmt = avFrameFormat(frame)
	// C: fi.fi_avframe = frame (libav.c:194) — hardware engines keep
	// the AVFrame to read data[3]; software engines consume the
	// copied planes and don't need it. The pointer is only valid for
	// the duration of VideoDeliverFrame. The 'VAAE' engine needs it
	// (VASurfaceID → vaExportSurfaceHandle); VDPAU is removed.
	if vaapiZeroCopy {
		fi.AVFrame = frame
	}

	r := vd.DeliverFrame(&fi)
	if r == -1 && vaapiZeroCopy {
		// The VAAE engine could not consume the frame (dmabuf export
		// or surface allocation failed) — fall back to the software
		// transfer path so playback survives a driver without
		// vaExportSurfaceHandle support.
		sw := AvFrameAllocRaw()
		if sw != nil && HwframeTransfer(sw, frame) == 0 {
			defer AvFrameFreeRaw(sw)
			frame = sw
			fi.AVFrame = nil
			_, srcVShift := PixFmtChromaSubSample(avFrameFormat(frame))
			for i := range 3 {
				dataPtr := avFrameData(frame, i)
				pitch := avFrameLinesize(frame, i)
				if dataPtr != nil && pitch > 0 {
					height := fi.Height
					if i > 0 {
						height = (height + (1 << srcVShift) - 1) >> srcVShift
					}
					size := pitch * height
					fi.Data[i] = make([]byte, size)
					copy(fi.Data[i], unsafe.Slice((*byte)(dataPtr), size))
				}
				fi.Pitch[i] = pitch
			}
			fi.PixFmt = avFrameFormat(frame)
			r = vd.DeliverFrame(&fi)
		} else {
			AvFrameFreeRaw(sw)
		}
	}
	if r != 1 {
		return nil
	}

	// r == 1 — need conversion to YUV420P at the frame's own dimensions
	// (C: libav.c:207-250). vd.ConvertWidth/Height/PixFmt track the
	// SOURCE frame geometry, as in C.
	srcFmt := avFrameFormat(frame)
	if pixFmtIsHwaccel(srcFmt) {
		// A hardware frame has no CPU-readable data[0..2] planes —
		// feeding it to sws_scale emits FFmpeg's "bad src image
		// pointers" and yields a black picture. Transfer to a
		// software frame first (e.g. the 'VAAE' engine refused the
		// surface after the zero-copy fallback, or a backend frame
		// reached delivery without transfer).
		sw := AvFrameAllocRaw()
		if sw == nil || HwframeTransfer(sw, frame) != 0 {
			AvFrameFreeRaw(sw)
			return nil
		}
		defer AvFrameFreeRaw(sw)
		frame = sw
		srcFmt = avFrameFormat(frame)
	}
	vd.SWS = libav.SwsGetCachedContext(
		vd.SWS,
		fi.Width, fi.Height, srcFmt,
		fi.Width, fi.Height, LavcPixFmtYUV420P,
		0, // flags
	)
	if vd.SWS == nil {
		lavTS.Error("libav", "Video: Unable to convert from fmt %d to yuv420p", srcFmt)
		return nil
	}

	if vd.ConvertWidth != fi.Width ||
		vd.ConvertHeight != fi.Height ||
		vd.ConvertPixFmt != srcFmt {

		if vd.Convert == nil {
			// FFmpeg 7: AVPicture removed; a heap AVFrame plays its role
			vd.Convert = libav.AvFrameAlloc()
			if vd.Convert == nil {
				return nil
			}
		}
		// C: avpicture_free(&vd->vd_convert) — unrefs buffers before realloc
		avPictureFree(vd.Convert)
		vd.ConvertWidth = fi.Width
		vd.ConvertHeight = fi.Height
		vd.ConvertPixFmt = srcFmt

		if ret := avPictureAlloc(vd.Convert, LavcPixFmtYUV420P,
			fi.Width, fi.Height); ret < 0 {
			return nil
		}
	}

	// Prepare source data pointers — C passes frame->data / frame->linesize,
	// which are [8]-element arrays in AVFrame. swscale indexes them via
	// desc->comp[i].plane which can reach index 3 (alpha/4-plane formats);
	// a shorter array would be an out-of-bounds read on the Go stack.
	// The stride arrays MUST be int32 (C int): passing Go int (64-bit)
	// makes swscale read every odd index as the high 32 bits (zero) of
	// the preceding stride, which fails its check_image_pointers.
	srcData := [8]unsafe.Pointer{}
	srcStride := [8]int32{}
	for i := range 8 {
		srcData[i] = avFrameData(frame, i)
		srcStride[i] = int32(avFrameLinesize(frame, i))
	}

	// Get destination data from AVPicture
	dstData := [8]unsafe.Pointer{}
	dstStride := [8]int32{}
	for i := range 8 {
		dstData[i] = avPictureData(vd.Convert, i)
		dstStride[i] = int32(avPictureLinesize(vd.Convert, i))
	}

	libav.SwsScale(
		vd.SWS,
		unsafe.Pointer(&srcData[0]), unsafe.Pointer(&srcStride[0]),
		0, fi.Height,
		unsafe.Pointer(&dstData[0]), unsafe.Pointer(&dstStride[0]),
	)

	// Set converted frame data — COPY to Go-allocated memory
	for i := range 3 {
		dataPtr := dstData[i]
		pitch := int(dstStride[i])
		if dataPtr != nil && pitch > 0 {
			height := fi.Height
			if i > 0 {
				height = (height + 1) / 2 // Chroma planes are half height for YUV420
			}
			size := pitch * height
			fi.Data[i] = make([]byte, size)
			copy(fi.Data[i], unsafe.Slice((*byte)(dataPtr), size))
		}
		fi.Pitch[i] = pitch
	}
	fi.PixFmt = LavcPixFmtYUV420P
	fi.AVFrame = nil // Converted frame, not original AVFrame

	// Deliver converted frame to video pipeline
	videoDeliverFrame(vd, &fi)

	return nil
}

// LibAVVideoFlush flushes the video decoder
func LibAVVideoFlush(mc *mediacore.MediaCodec, vd *VideoDecoder) error {
	if mc == nil || mc.Ctx == nil {
		return nil
	}

	// Send NULL packet to flush decoder
	avcodecSendPacket(mc.Ctx, nil)

	// C: AVFrame *frame = vd->vd_frame — persistent decoder frame
	frame := vdFrame(vd)
	if frame == nil {
		return nil
	}

	// Allocate frame data buffers (see comment in Decode function)
	if mc.Ctx != nil {
		w := avCodecCtxWidth(mc.Ctx)
		h := avCodecCtxHeight(mc.Ctx)
		pixFmt := avCodecCtxPixFmt(mc.Ctx)
		if w > 0 && h > 0 && !pixFmtIsHwaccel(pixFmt) {
			avPictureAlloc(frame, pixFmt, w, h)
		}
	}

	for {
		ret := avcodecReceiveFrame(mc.Ctx, frame)
		if ret < 0 {
			break
		}
		avFrameUnref(frame)
	}

	// Flush buffers
	avcodecFlushBuffers(mc.Ctx)

	return nil
}

// LibAVVideoEOF handles EOF by draining the decoder
func LibAVVideoEOF(mc *mediacore.MediaCodec, vd *VideoDecoder, mq *mediacore.MediaQueue) error {
	if mc == nil || mc.Ctx == nil {
		return nil
	}

	// Send NULL packet to drain decoder
	avcodecSendPacket(mc.Ctx, nil)

	// C: AVFrame *frame = vd->vd_frame — persistent decoder frame
	frame := vdFrame(vd)
	if frame == nil {
		return nil
	}

	// Allocate frame data buffers (see comment in Decode function)
	if mc.Ctx != nil {
		w := avCodecCtxWidth(mc.Ctx)
		h := avCodecCtxHeight(mc.Ctx)
		pixFmt := avCodecCtxPixFmt(mc.Ctx)
		if w > 0 && h > 0 && !pixFmtIsHwaccel(pixFmt) {
			avPictureAlloc(frame, pixFmt, w, h)
		}
	}

	for {
		// Start decode time measurement
		AvgTimeStart(&vd.DecodeTime)

		ret := avcodecReceiveFrame(mc.Ctx, frame)
		if ret < 0 {
			break
		}

		// Stop decode time measurement — avgtime_stop returns the
		// rolling average in µs (C: t = avgtime_stop(...) → decode_time)
		decodeTime := AvgTimeStop(&vd.DecodeTime, mq.PropDecodeAvg, mq.PropDecodePeak)

		// Get mbm from reorder buffer using frame opaque — same
		// (index+1) convention as the decode path (COPY_OPAQUE).
		var mbm *mediacore.MediaBufMeta
		var mbmCopy mediacore.MediaBufMeta
		if opaque := avFrameGetOpaque(frame); opaque != 0 {
			mbm = &vd.Reorder[int(opaque-1)&255] // VIDEO_DECODER_REORDER_MASK = 255
		} else {
			mbm = &mbmCopy
		}

		// Deliver frame if not skipped
		//
		if !mbm.Flags.Skip {
			// Deliver frame using LibAVDeliverFrame — decode_time in µs
			LibAVDeliverFrame(vd, vd.MP, mq, mc.Ctx, frame, mbm, decodeTime, mc)
		}

		avFrameUnref(frame)
	}

	// Flush buffers
	avcodecFlushBuffers(mc.Ctx)

	return nil
}

// LibAVDecodeVideo decodes a video frame
func LibAVDecodeVideo(mc *mediacore.MediaCodec, vd *VideoDecoder, mq *mediacore.MediaQueue, mb *mediacore.MediaBuf, reqsize int) error {
	if mc == nil || mc.Ctx == nil {
		return nil
	}

	// C: libav.c:336-337 — a flush-tagged buffer drains the decoder
	// before the new packet is fed (HLS queue-merge boundary).
	if mb != nil && mb.Flags.Flush {
		LibAVVideoEOF(mc, vd, mq)
	}

	// C: libav.c:346 — while seeking, drop non-reference frames at
	// codec level (avcodec handles the actual discard).
	if mb != nil && mb.Flags.Skip {
		CctxSetSkipFrame(mc.Ctx, AVDiscardNonRef())
	} else {
		CctxSetSkipFrame(mc.Ctx, AVDiscardDefault())
	}

	// Allocate AVPacket (FFmpeg 7: av_packet_alloc allocates and initializes)
	pkt := avPacketAlloc()
	if pkt == nil {
		return nil
	}
	defer avPacketFree(pkt)

	// Populate reorder buffer and set opaque for packet->frame association.
	// We store (ReorderPtr + 1) as the opaque value so that NULL (0) means
	// "not set" and 1 means "index 0" -- distinguishing the two cases.
	// This mirrors C's reordered_opaque mechanism using AV_CODEC_FLAG_COPY_OPAQUE.
	var opaqueVal uintptr
	if mb != nil {
		buffer.CopyMetaFromMB(&vd.Reorder[vd.ReorderPtr], mb)
		opaqueVal = uintptr(vd.ReorderPtr + 1)
		vd.ReorderPtr = (vd.ReorderPtr + 1) & 255
	}

	// C: avcodec_decode_video2(ctx, frame, &got_pic, &mb->mb_pkt) — the
	// media_buf's AVPacket IS the payload (mb_data/mb_size are macros on
	// mb_pkt). AVPacket-backed bufs carry the packet in mb.Pkt.
	if mb != nil && mb.Pkt != nil {
		p := mb.Pkt
		// Go extension (no C counterpart): cenc-encrypted packets are
		// decrypted in place through the codec's DRM decryptor before
		// avcodec_send_packet. Clear packets are a no-op.
		if err := DRMDecryptPacket(mc, p); err != nil {
			return err
		}
		avPacketSetOpaque(p, opaqueVal)
		ret := avcodecSendPacket(mc.Ctx, p)
		if ret < 0 {
			return nil
		}
	} else if mb != nil && mb.Data != nil && len(mb.Data) > 0 {
		ret := avNewPacket(pkt, unsafe.Pointer(&mb.Data[0]), len(mb.Data))
		if ret < 0 {
			return nil
		}

		// Set packet fields from MediaBuf
		avPacketSetPTS(pkt, mb.PTS)
		avPacketSetDTS(pkt, mb.DTS)
		avPacketSetDuration(pkt, int64(mb.Duration))
		// Convert MediaBufFlags struct to int flags
		flags := 0
		if mb.Flags.Keyframe {
			flags |= 0x0001 // AVPktFlagKey
		}
		avPacketSetFlags(pkt, flags)

		avPacketSetOpaque(pkt, opaqueVal)

		// Apply bitstream filter if present (h264_mp4toannexb for MP4/AVCC)
		// This mirrors C's use of av_bitstream_filter_filter() in fa_libav.c
		if bsf := vd.BSF; bsf != nil {
			// Send packet to BSF
			err := bsf.SendPacket(pkt)
			if err != nil {
				return nil
			}
			// Receive filtered packet(s) and send each to decoder.
			bsfRecvCount := 0
			for {
				filteredPkt := avPacketAlloc()
				if filteredPkt == nil {
					break
				}
				err := bsf.ReceivePacket(filteredPkt)
				if err != nil {
					avPacketFree(filteredPkt)
					break
				}
				// Set opaque on filtered packet (BSF may not copy it)
				avPacketSetOpaque(filteredPkt, opaqueVal)
				ret = avcodecSendPacket(mc.Ctx, filteredPkt)
				avPacketFree(filteredPkt)
				bsfRecvCount++
				if ret < 0 {
					break
				}
			}
		} else {
			// Send packet directly to decoder (Annex B or non-H.264)
			ret = avcodecSendPacket(mc.Ctx, pkt)
			if ret < 0 {
				return nil
			}
		}
	}

	// C: AVFrame *frame = vd->vd_frame — persistent decoder frame
	frame := vdFrame(vd)
	if frame == nil {
		return nil
	}

	// CRITICAL: Allocate frame data buffers before calling avcodec_receive_frame.
	// In FFmpeg 7+ (send/receive API), the decoder does NOT automatically
	// allocate buffers via get_buffer2 callback like FFmpeg 4.x's
	// avcodec_decode_video2() did. The caller must set format/width/height
	// and call av_frame_get_buffer() first.
	// Without this, frame->data[] remains NULL and sws_scale() fails with
	// "bad src image pointers".
	// C: src/libav.c:436 sets refcounted_frames=1 and get_buffer2 callback
	// which handles this internally. Go uses the send/receive API which
	// requires explicit buffer allocation.
	if mc.Ctx != nil {
		w := avCodecCtxWidth(mc.Ctx)
		h := avCodecCtxHeight(mc.Ctx)
		pixFmt := avCodecCtxPixFmt(mc.Ctx)
		if w > 0 && h > 0 && !pixFmtIsHwaccel(pixFmt) {
			ret := avPictureAlloc(frame, pixFmt, w, h)
			if ret < 0 {
				return nil
			}
		}
	}

	for {
		// C: avgtime_start/stop around avcodec_decode_video2 — in the
		// send/receive API the decode work happens inside receive_frame
		// (send_packet only queues), so the measurement wraps it.
		AvgTimeStart(&vd.DecodeTime)

		ret := avcodecReceiveFrame(mc.Ctx, frame)
		if ret < 0 {
			break
		}

		decodeTime := AvgTimeStop(&vd.DecodeTime, mq.PropDecodeAvg, mq.PropDecodePeak)

		// Recover metadata from reorder buffer using frame's opaque field.
		// The decoder propagates AVPacket.opaque to AVFrame.opaque when
		// AV_CODEC_FLAG_COPY_OPAQUE is set. The opaque identifies the
		// ORIGINAL packet (not the current one), which is correct for
		// B-frame reordering.
		// We store (index + 1) as opaque, so NULL means "not set".
		var mbm mediacore.MediaBufMeta
		opaque := avFrameGetOpaque(frame)
		if opaque != 0 {
			// Opaque is present -- look up metadata from reorder buffer
			idx := int(opaque) - 1
			mbm = vd.Reorder[idx&255]
		} else if mb != nil {
			// Opaque not set (codec doesn't support COPY_OPAQUE or frame
			// from drain) -- fall back to current packet metadata
			buffer.CopyMetaFromMB(&mbm, mb)
		}

		// C: libav.c:360-361 — skipped frames are decoded but never
		// delivered (seek fast-forward drops them here).
		if !mbm.Flags.Skip {
			// Deliver frame — C: libav_deliver_frame(..., t) with t in µs
			LibAVDeliverFrame(vd, vd.MP, mq, mc.Ctx, frame, &mbm, decodeTime, mc)
		}

		avFrameUnref(frame)
	}

	// C: mp_set_mq_meta(mq, ctx->codec, ctx) (libav.c:354) — runs after
	// decode even when no frame was produced (got_pic==0); codec ctx
	// fields are updated by receive_frame, not send_packet.
	MqSetMeta(mq, mc.Ctx)

	return nil
}
