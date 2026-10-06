//go:build rpi

// rpi_video.go — C: src/arch/rpi/rpi_video.c + rpi_video.h — OpenMAX IL
// hardware video decoder codec (OMX.broadcom.video_decode).
package rpi

/*
#cgo CFLAGS: -DOMX_SKIP64BIT -I${SRCDIR}/../../../ffmpeg/rpi/include -I/opt/vc/include -I/opt/vc/include/interface/vmcs_host/linux -I/opt/vc/include/interface/vcos/pthreads
#cgo LDFLAGS: -L/opt/vc/lib -lbcm_host -lvcos -lvchiq_arm -lopenmaxil -lEGL -lGLESv2 -lmmal_core -lmmal_util -lmmal_vc_client -lvchostif -lavcodec -lavformat -lavutil

#include <stdlib.h>
#include <string.h>
#include <OMX_Core.h>
#include <OMX_Component.h>
#include <OMX_Broadcom.h>
#include "omx_shim.h"
#include <libavcodec/avcodec.h>

// Union accessors — cgo maps C unions to byte arrays, so format.video /
// format.image members are reached through these helpers.
static OMX_VIDEO_PORTDEFINITIONTYPE *pd_video(OMX_PARAM_PORTDEFINITIONTYPE *p) { return &p->format.video; }
static OMX_IMAGE_PORTDEFINITIONTYPE *pd_image(OMX_PARAM_PORTDEFINITIONTYPE *p) { return &p->format.image; }
static OMX_AUDIO_PORTDEFINITIONTYPE *pd_audio(OMX_PARAM_PORTDEFINITIONTYPE *p) { return &p->format.audio; }
*/
import "C"

import (
	"fmt"
	"unsafe"

	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/trace"
	decoder "github.com/czz/movian-go/internal/video/decoder"
)

// C: omx_enable_* codec gating flags (rpi_video.c:33-41)
var (
	omxEnableMpg2   bool
	omxEnableVp8    bool
	omxEnableVp6    bool
	omxEnableMjpeg  bool
	omxEnableWvc1   bool
	omxEnableH263   bool
	omxEnableH264   bool
	omxEnableTheora bool
	omxEnableMpeg4  bool
)

// C: rpi_video_codec_t (rpi_video.h:21-32)
type RpiVideoCodec struct {
	Decoder   *OmxComponent // C: rvc_decoder
	LastEpoch int           // C: rvc_last_epoch
	Name      string        // C: rvc_name
	BFrames   int           // C: rvc_b_frames

	H264Parser decoder.H264Parser // C: rvc_h264_parser

	SarNum int // C: rvc_sar_num
	SarDen int // C: rvc_sar_den
}

// omxTicksFromS64 — C: omx_ticks_from_s64: split int64 µs into the two
// 32-bit halves of OMX_TICKS.
func omxTicksFromS64(v int64) C.OMX_TICKS {
	var t C.OMX_TICKS
	t.nLowPart = C.OMX_U32(uint64(v))
	t.nHighPart = C.OMX_U32(uint64(v) >> 32)
	return t
}

// rpiVideoPortSettingsChanged — C: rpi_video_port_settings_changed
// (rpi_video.c:46-114)
func rpiVideoPortSettingsChanged(oc *OmxComponent) {
	mc := oc.Opaque.(*mediacore.MediaCodec)
	rvc := mc.Opaque.(*RpiVideoCodec)
	mp := mc.MP
	fi := &mediacore.FrameInfo{}

	sarNum := 1
	sarDen := 1

	if rvc.SarNum != 0 && rvc.SarDen != 0 {
		sarNum = rvc.SarNum
		sarDen = rvc.SarDen
	} else {
		var pixelAspect C.OMX_CONFIG_POINTTYPE
		pixelAspect.nSize = C.OMX_U32(unsafe.Sizeof(pixelAspect))
		C.omx_init_version_(&pixelAspect.nVersion)
		pixelAspect.nPortIndex = 131
		if C.omx_GetParameter_(oc.Handle, C.OMX_IndexParamBrcmPixelAspectRatio,
			C.OMX_PTR(unsafe.Pointer(&pixelAspect))) == C.OMX_ErrorNone {
			if pixelAspect.nX != 0 {
				sarNum = int(pixelAspect.nX)
			}
			if pixelAspect.nY != 0 {
				sarDen = int(pixelAspect.nY)
			}
		}
	}

	var interlace C.OMX_CONFIG_INTERLACETYPE
	interlace.nSize = C.OMX_U32(unsafe.Sizeof(interlace))
	C.omx_init_version_(&interlace.nVersion)
	interlace.nPortIndex = 131
	if C.omx_GetConfig_(oc.Handle, C.OMX_IndexConfigCommonInterlace,
		C.OMX_PTR(unsafe.Pointer(&interlace))) == C.OMX_ErrorNone {
		switch interlace.eMode {
		case C.OMX_InterlaceFieldsInterleavedUpperFirst:
			fi.TFF = true
			fi.Interlaced = true
			rpiTS.Trace(trace.TRACE_DEBUG, "VideoCore", "Interlaced picture top-field-first")
		case C.OMX_InterlaceFieldsInterleavedLowerFirst:
			fi.Interlaced = true
			rpiTS.Trace(trace.TRACE_DEBUG, "VideoCore", "Interlaced picture bottom-field-first")
		}
	}

	var portImage C.OMX_PARAM_PORTDEFINITIONTYPE
	portImage.nSize = C.OMX_U32(unsafe.Sizeof(portImage))
	C.omx_init_version_(&portImage.nVersion)
	portImage.nPortIndex = 131

	if C.omx_GetParameter_(oc.Handle, C.OMX_IndexParamPortDefinition,
		C.OMX_PTR(unsafe.Pointer(&portImage))) == C.OMX_ErrorNone {
		scan := byte('p')
		if fi.Interlaced {
			scan = 'i'
		}
		codecInfo := fmt.Sprintf("%s %dx%d%c (VideoCore)",
			rvc.Name, int(C.pd_video(&portImage).nFrameWidth),
			int(C.pd_video(&portImage).nFrameHeight), scan)
		mp.Video.PropCodec.SetString(codecInfo)
		rpiTS.Trace(trace.TRACE_DEBUG, "VideoCore",
			"Video decoder output port settings changed to %s (SAR: %d:%d)",
			codecInfo, sarNum, sarDen)

		fi.Width = int(C.pd_video(&portImage).nFrameWidth)
		fi.Height = int(C.pd_video(&portImage).nFrameHeight)
	}
	fi.DARNum = sarNum * fi.Width
	fi.DARDen = sarDen * fi.Height

	mp.Mutex.Lock()
	mb := mediacore.MediaBufAllocLocked(mp, 0)
	mb.DataType = int(mediacore.MBCtrlReconfigure)
	mb.FrameInfo = fi
	mb.Dtor = mediacore.MediaBufDtorFrameInfo
	mb.Codec = mediacore.MediaCodecRef(mc)
	mediacore.MbEnq(mp, mp.Video, mb)
	mp.Mutex.Unlock()
}

// rpiCodecDecodeLocked — C: rpi_codec_decode_locked (rpi_video.c:120-244)
func rpiCodecDecodeLocked(mc *mediacore.MediaCodec, vd any,
	mq *mediacore.MediaQueue, mb *mediacore.MediaBuf) int {
	v := vd.(*decoder.VideoDecoder)
	rvc := mc.Opaque.(*RpiVideoCodec)
	data := mb.Data
	len_ := mb.Size
	isBFrame := false

	oc := rvc.Decoder

	if oc.AvailBytes < len_ {
		oc.NeedBytes = len_
		return 1
	}

	switch mc.CodecID {
	case mediacore.CodecID(C.AV_CODEC_ID_MPEG4):
		if mb.Size <= 7 {
			return 0
		}
		d := mb.Data
		frameType := 0
		if d[0] == 0x00 && d[1] == 0x00 && d[2] == 0x01 && d[3] == 0xb6 {
			frameType = int(d[4]) >> 6
		}
		if frameType == 2 {
			isBFrame = true
		}

	case mediacore.CodecID(C.AV_CODEC_ID_H264):
		rvc.H264Parser.DecodeData(mb.Data)
		if rvc.H264Parser.SliceTypeNOS == decoder.SliceTypeB {
			isBFrame = true
		}
	}

	mbm := &v.Reorder[v.ReorderPtr]
	mediacore.CopyMbmFromMb(mbm, mb)
	mbm.PTS = v.InferPTS(mbm, isBFrame)

	v.ReorderPtr = (v.ReorderPtr + 1) & decoder.VideoDecoderReorderMask

	domark := true

	var q, buf *C.OMX_BUFFERHEADERTYPE
	pq := &q

	for len_ > 0 {
		buf = oc.Avail
		oc.InflightBuffers++
		oc.AvailBytes -= int(buf.nAllocLen)
		oc.Avail = (*C.OMX_BUFFERHEADERTYPE)(buf.pAppPrivate)

		buf.nOffset = 0
		n := int(buf.nAllocLen)
		if len_ < n {
			n = len_
		}
		buf.nFilledLen = C.OMX_U32(n)
		C.memcpy(unsafe.Pointer(buf.pBuffer), unsafe.Pointer(&data[0]), C.size_t(n))
		buf.nFlags = 0

		if v.RenderComponent != nil && domark {
			buf.hMarkTargetComponent = C.OMX_HANDLETYPE(v.RenderComponent.(unsafe.Pointer))
			buf.pMarkData = C.OMX_PTR(unsafe.Pointer(mbm))
			domark = false
		}

		if rvc.LastEpoch != mbm.Epoch {
			buf.nFlags |= C.OMX_BUFFERFLAG_STARTTIME | C.OMX_BUFFERFLAG_DISCONTINUITY
			rvc.LastEpoch = mbm.Epoch
		}

		if len_ <= int(buf.nAllocLen) {
			buf.nFlags |= C.OMX_BUFFERFLAG_ENDOFFRAME
		}

		data = data[int(buf.nFilledLen):]
		len_ -= int(buf.nFilledLen)

		if mbm.PTS != mediacore.PTSUnset {
			buf.nTimeStamp = omxTicksFromS64(mbm.PTS)
		} else {
			buf.nFlags |= C.OMX_BUFFERFLAG_TIME_UNKNOWN
			buf.nTimeStamp = omxTicksFromS64(0)
		}

		if mbm.Flags.Skip {
			buf.nFlags |= C.OMX_BUFFERFLAG_DECODEONLY
		}

		// Enqueue on temporary stack queue
		buf.pAppPrivate = C.OMX_PTR(unsafe.Pointer(*pq))
		*pq = buf
		pq = (**C.OMX_BUFFERHEADERTYPE)(unsafe.Pointer(&buf.pAppPrivate))
	}

	v.MP.Mutex.Unlock()

	for q != nil {
		buf = q
		q = (*C.OMX_BUFFERHEADERTYPE)(buf.pAppPrivate)
		Omxchk(C.omx_EmptyThisBuffer_(rvc.Decoder.Handle, buf), "OMX_EmptyThisBuffer")
	}

	v.MP.Mutex.Lock()
	return 0
}

// rpiCodecFlush — C: rpi_codec_flush (rpi_video.c:250-257)
func rpiCodecFlush(mc *mediacore.MediaCodec, vd any) {
	rvc := mc.Opaque.(*RpiVideoCodec)
	OmxFlushPort(rvc.Decoder, 130)
	OmxFlushPort(rvc.Decoder, 131)
}

// rpiCodecClose — C: rpi_codec_close (rpi_video.c:262-278)
func rpiCodecClose(mc *mediacore.MediaCodec) {
	rvc := mc.Opaque.(*RpiVideoCodec)

	OmxFlushPort(rvc.Decoder, 130)
	OmxFlushPort(rvc.Decoder, 131)

	OmxWaitBuffers(rvc.Decoder)

	OmxSetState(rvc.Decoder, C.OMX_StateIdle)
	OmxReleaseBuffers(rvc.Decoder, 130)
	OmxSetState(rvc.Decoder, C.OMX_StateLoaded)

	OmxComponentDestroy(rvc.Decoder)

	rvc.H264Parser.Fini()
}

// rpiCodecReconfigure — C: rpi_codec_reconfigure (rpi_video.c:283-289)
func rpiCodecReconfigure(mc *mediacore.MediaCodec, fi *mediacore.FrameInfo) {
	mp := mc.MP
	// C: mp->mp_set_video_codec('omx', ...) — 'omx' packs as
	// ('o'<<16)|('m'<<8)|'x' (multi-char literal, sequential).
	mp.SetVideoCodec(mediacore.FourCCOMX, mc,
		mp.VideoFrameOpaque, fi)
}

// rpiCodecCreate — C: rpi_codec_create (rpi_video.c:295-483)
func rpiCodecCreate(mc *mediacore.MediaCodec, mcp *mediacore.MediaCodecParams,
	mp *mediacore.MediaPipe) int {
	var fmt_ C.OMX_VIDEO_CODINGTYPE
	var name string

	switch mc.CodecID {
	case mediacore.CodecID(C.AV_CODEC_ID_H263):
		if !omxEnableH263 {
			return 1
		}
		fmt_ = C.OMX_VIDEO_CodingH263
		name = "h263"

	case mediacore.CodecID(C.AV_CODEC_ID_MPEG4):
		if !omxEnableMpeg4 {
			return 1
		}
		fmt_ = C.OMX_VIDEO_CodingMPEG4
		name = "MPEG-4"

	case mediacore.CodecID(C.AV_CODEC_ID_H264):
		if !omxEnableH264 {
			return 1
		}
		fmt_ = C.OMX_VIDEO_CodingAVC
		name = "h264"

	case mediacore.CodecID(C.AV_CODEC_ID_MPEG1VIDEO),
		mediacore.CodecID(C.AV_CODEC_ID_MPEG2VIDEO):
		if !omxEnableMpg2 {
			return 1
		}
		fmt_ = C.OMX_VIDEO_CodingMPEG2
		name = "MPEG2"

	case mediacore.CodecID(C.AV_CODEC_ID_VP8):
		if !omxEnableVp8 {
			return 1
		}
		fmt_ = C.OMX_VIDEO_CodingVP8
		name = "VP8"

	case mediacore.CodecID(C.AV_CODEC_ID_VP6F),
		mediacore.CodecID(C.AV_CODEC_ID_VP6A):
		if !omxEnableVp6 {
			return 1
		}
		fmt_ = C.OMX_VIDEO_CodingVP6
		name = "VP6"

	case mediacore.CodecID(C.AV_CODEC_ID_MJPEG),
		mediacore.CodecID(C.AV_CODEC_ID_MJPEGB):
		if !omxEnableMjpeg {
			return 1
		}
		fmt_ = C.OMX_VIDEO_CodingMJPEG
		name = "MJPEG"

	case mediacore.CodecID(C.AV_CODEC_ID_VC1),
		mediacore.CodecID(C.AV_CODEC_ID_WMV3):
		if !omxEnableWvc1 {
			return 1
		}
		if mcp == nil || mcp.ExtraDataSize == 0 {
			return 1
		}
		fmt_ = C.OMX_VIDEO_CodingWMV
		name = "VC1"

	case mediacore.CodecID(C.AV_CODEC_ID_THEORA):
		if !omxEnableTheora {
			return 1
		}
		fmt_ = C.OMX_VIDEO_CodingTheora
		name = "Theora"

	default:
		return 1
	}

	rvc := &RpiVideoCodec{}

	d := OmxComponentCreate("OMX.broadcom.video_decode",
		&mp.Mutex, mp.Video.Avail)
	if d == nil {
		return 1
	}

	if mcp != nil {
		rvc.SarNum = mcp.SARNum
		rvc.SarDen = mcp.SARDen
	}

	rvc.Decoder = d
	d.PortSettingsChangedCb = rpiVideoPortSettingsChanged
	d.Opaque = mc

	OmxSetState(d, C.OMX_StateIdle)

	var format C.OMX_VIDEO_PARAM_PORTFORMATTYPE
	format.nSize = C.OMX_U32(unsafe.Sizeof(format))
	C.omx_init_version_(&format.nVersion)
	format.nPortIndex = 130
	format.eCompressionFormat = fmt_

	format.xFramerate = 25 * (1 << 16)
	if mcp != nil && mcp.FrameRateNum != 0 && mcp.FrameRateDen != 0 {
		format.xFramerate = C.OMX_U32(uint64(65536) * uint64(mcp.FrameRateNum) /
			uint64(mcp.FrameRateDen))
	}

	rpiTS.Trace(trace.TRACE_DEBUG, "OMX", "Frame rate set to %2.3f",
		float64(format.xFramerate)/65536.0)

	Omxchk(C.omx_SetParameter_(d.Handle, C.OMX_IndexParamVideoPortFormat,
		C.OMX_PTR(unsafe.Pointer(&format))), "OMX_SetParameter")

	var portParam C.OMX_PARAM_PORTDEFINITIONTYPE
	portParam.nSize = C.OMX_U32(unsafe.Sizeof(portParam))
	C.omx_init_version_(&portParam.nVersion)
	portParam.nPortIndex = 130

	if C.omx_GetParameter_(d.Handle, C.OMX_IndexParamPortDefinition,
		C.OMX_PTR(unsafe.Pointer(&portParam))) == C.OMX_ErrorNone {
		if mcp != nil {
			C.pd_video(&portParam).nFrameWidth = C.OMX_U32(mcp.Width)
			C.pd_video(&portParam).nFrameHeight = C.OMX_U32(mcp.Height)
		}
		C.omx_SetParameter_(d.Handle, C.OMX_IndexParamPortDefinition,
			C.OMX_PTR(unsafe.Pointer(&portParam)))
	}

	var notification C.OMX_CONFIG_REQUESTCALLBACKTYPE
	notification.nSize = C.OMX_U32(unsafe.Sizeof(notification))
	C.omx_init_version_(&notification.nVersion)
	notification.nPortIndex = 131
	notification.nIndex = C.OMX_IndexParamBrcmPixelAspectRatio
	notification.bEnable = C.OMX_TRUE
	C.omx_SetParameter_(d.Handle, C.OMX_IndexParamPortDefinition,
		C.OMX_PTR(unsafe.Pointer(&notification)))

	var ec C.OMX_PARAM_BRCMVIDEODECODEERRORCONCEALMENTTYPE
	ec.nSize = C.OMX_U32(unsafe.Sizeof(ec))
	C.omx_init_version_(&ec.nVersion)
	ec.bStartWithValidFrame = C.OMX_TRUE
	Omxchk(C.omx_SetParameter_(d.Handle,
		C.OMX_IndexParamBrcmVideoDecodeErrorConcealment,
		C.OMX_PTR(unsafe.Pointer(&ec))), "OMX_SetParameter")

	var bt C.OMX_CONFIG_BOOLEANTYPE
	bt.nSize = C.OMX_U32(unsafe.Sizeof(bt))
	C.omx_init_version_(&bt.nVersion)
	bt.bEnabled = C.OMX_TRUE
	Omxchk(C.omx_SetConfig_(d.Handle,
		C.OMX_IndexParamBrcmInterpolateMissingTimestamps,
		C.OMX_PTR(unsafe.Pointer(&bt))), "OMX_SetConfig")

	OmxAllocBuffers(d, 130)
	OmxSetState(d, C.OMX_StateExecuting)

	if mcp != nil && mcp.ExtraDataSize != 0 {
		mp.Mutex.Lock()
		buf := OmxGetBufferLocked(rvc.Decoder)
		mp.Mutex.Unlock()
		buf.nOffset = 0
		buf.nFilledLen = C.OMX_U32(mcp.ExtraDataSize)
		C.memcpy(unsafe.Pointer(buf.pBuffer), unsafe.Pointer(&mcp.ExtraData[0]),
			C.size_t(buf.nFilledLen))
		buf.nFlags = C.OMX_BUFFERFLAG_CODECCONFIG | C.OMX_BUFFERFLAG_ENDOFFRAME
		Omxchk(C.omx_EmptyThisBuffer_(rvc.Decoder.Handle, buf),
			"OMX_EmptyThisBuffer")
	}

	if mc.CodecID == mediacore.CodecID(C.AV_CODEC_ID_H264) {
		var ed []byte
		if mcp != nil {
			ed = mcp.ExtraData
		}
		rvc.H264Parser.Setup(ed)
	}

	OmxEnableBufferMarks(d)

	mc.Opaque = rvc
	mc.Close = rpiCodecClose
	mc.DecodeLocked = rpiCodecDecodeLocked
	mc.Flush = rpiCodecFlush
	mc.Reconfigure = rpiCodecReconfigure
	rvc.Name = name
	return 0
}

// rpiCodecStart — C: rpi_codec_init (rpi_video.c:488-500)
func rpiCodecStart() {
	omxEnableMpg2 = RpiIsCodecEnabled("MPG2")
	omxEnableVp6 = RpiIsCodecEnabled("VP6")
	omxEnableVp8 = RpiIsCodecEnabled("VP8")
	omxEnableMjpeg = RpiIsCodecEnabled("MJPG")
	omxEnableWvc1 = RpiIsCodecEnabled("WVC1")
	omxEnableH263 = RpiIsCodecEnabled("H263")
	omxEnableH264 = RpiIsCodecEnabled("H264")
	omxEnableTheora = RpiIsCodecEnabled("THRA")
	omxEnableMpeg4 = RpiIsCodecEnabled("MPG4")
}

// REGISTER_CODEC(rpi_codec_init, rpi_codec_create, 100)
// (rpi_video.c:502) — registered from RpiInit; the C INITME/REGISTER_
// macros run at static-init time, Go runs them from the platform init.
func init() {
	mediacore.MediaRegisterCodec(&mediacore.CodecDef{
		Start: rpiCodecStart,
		Open:  rpiCodecCreate,
		Prio:  100,
	})
}
