//go:generate go run generate_cgo.go

package libav

/*
#include <libavcodec/avcodec.h>
#include <libavcodec/bsf.h>
#include <libavformat/avformat.h>
#include <libavutil/pixfmt.h>
#include <libavutil/pixdesc.h>
#include <libavutil/imgutils.h>
#include <libavutil/channel_layout.h>
#include <libavutil/samplefmt.h>
#include <libavutil/opt.h>
#include <libavutil/frame.h>
#include <libswscale/swscale.h>
#include <libswresample/swresample.h>
#include <stdlib.h>
#include <string.h>


static void ml_memcpy(void *dst, const void *src, size_t n) {
    memcpy(dst, src, n);
}

static void *ml_calloc(size_t nmemb, size_t size) {
    return calloc(nmemb, size);
}

// Opaque field accessors — use C casts to avoid Go checkptr issues.
// The opaque value is an integer index+1, not a real pointer.
static void ml_packet_set_opaque(void *pkt, uintptr_t value) {
    ((AVPacket*)pkt)->opaque = (void*)value;
}

static uintptr_t ml_packet_get_opaque(void *pkt) {
    return (uintptr_t)((AVPacket*)pkt)->opaque;
}

static uintptr_t ml_frame_get_opaque(void *frame) {
    return (uintptr_t)((AVFrame*)frame)->opaque;
}

// C: av_frame_clone (glw_video_yuvp.c) — av_frame_clone was removed
// from FFmpeg's public API; alloc+ref is its documented equivalent.
static void *ml_frame_clone(void *frame) {
    AVFrame *dst = av_frame_alloc();
    if (dst != NULL && av_frame_ref(dst, (AVFrame*)frame) < 0) {
        av_frame_free(&dst);
        return NULL;
    }
    return dst;
}

static void ml_frame_data_add(void *frame, int plane, int off) {
    ((AVFrame*)frame)->data[plane] += off;
}

// C: av_pix_fmt_get_chroma_sub_sample (glw_video_common.c:1299).
static void ml_chroma_sub_sample(int pix_fmt, int *hshift, int *vshift) {
    av_pix_fmt_get_chroma_sub_sample((enum AVPixelFormat)pix_fmt,
                                     hshift, vshift);
}

// AV_PIX_FMT_* constants used by video_deliver_lavc's pix_fmt switch
// (glw_video_common.c:1287-1321). Exposed via a function so the Go side
// never hardcodes FFmpeg enum values.
static int ml_pixfmt_const(int which) {
    switch(which) {
    case 0:  return AV_PIX_FMT_YUV420P;
    case 1:  return AV_PIX_FMT_YUV422P;
    case 2:  return AV_PIX_FMT_YUV444P;
    case 3:  return AV_PIX_FMT_YUV410P;
    case 4:  return AV_PIX_FMT_YUV411P;
    case 5:  return AV_PIX_FMT_YUV440P;
    case 6:  return AV_PIX_FMT_YUVJ420P;
    case 7:  return AV_PIX_FMT_YUVJ422P;
    case 8:  return AV_PIX_FMT_YUVJ444P;
    case 9:  return AV_PIX_FMT_YUVJ440P;
    case 11: return AV_PIX_FMT_BGR24;
    case 12: return AV_PIX_FMT_YUV420P10LE;
    case 13: return AV_PIX_FMT_XYZ12LE;
    case 14: return AV_PIX_FMT_VAAPI;
    case 15: return AV_PIX_FMT_CUDA;
    case 16: return AV_PIX_FMT_D3D11;
    case 17: return AV_PIX_FMT_DXVA2_VLD;
    default: return AV_PIX_FMT_NONE;
    }
}

// ml_pixfmt_is_hwaccel — AV_PIX_FMT_FLAG_HWACCEL on the descriptor:
// hardware formats (VAAPI, VDPAU, DRM_PRIME, ...) have no CPU planes,
// so the decode loop must NOT avPictureAlloc on the output frame —
// the decoder manages surfaces through hw_frames_ctx.
static int ml_pixfmt_is_hwaccel(int fmt) {
    const AVPixFmtDescriptor *d = av_pix_fmt_desc_get(fmt);
    return d != NULL && (d->flags & AV_PIX_FMT_FLAG_HWACCEL);
}

static int ml_codec_ctx_get_flags(void *ctx) {
    return ((AVCodecContext*)ctx)->flags;
}

// Forward declaration — defined in format.go's C block
extern int copy_codec_params_to_context(void *ic, int stream_index, void *codec_ctx);
extern int copy_codec_context(void *dst, void *src);
extern void *codec_ctx_from_stream(void *ic, int stream_index);
extern void free_codec_ctx(void *ctx);
extern void log_codecpar(void *ic, int index);

// AVCodec.type accessor (Go can't reach the `type` field — keyword)
static int ml_codec_get_type(const void *codec) {
    return ((const AVCodec*)codec)->type;
}

*/
import "C"
import (
	"fmt"
	"unsafe"

	"github.com/czz/movian-go/internal/trace"

	"github.com/czz/movian-go/internal/libav"
	mediacore "github.com/czz/movian-go/internal/media/core"
)

// lavTS — C: trace() global — injected via SetTraceSystem.
var lavTS *trace.TraceSystem

// SetTraceSystem injects the trace system (C: trace() global).
func SetTraceSystem(ts *trace.TraceSystem) { lavTS = ts }

// MediaCodecCreateLavc creates a LibAV codec with full configuration
// This is the main entry point for creating a LibAV codec
func MediaCodecCreateLavc(mc *mediacore.MediaCodec, mcp *mediacore.MediaCodecParams, mp *mediacore.MediaPipe) error {
	return MediaCodecCreateLavcWithFormat(mc, mcp, mp, nil, -1)
}

// MediaCodecCreateLavcWithFormat creates a lavc codec with format context for proper
// parameter copying before avcodec_open2.
func MediaCodecCreateLavcWithFormat(mc *mediacore.MediaCodec, mcp *mediacore.MediaCodecParams, mp *mediacore.MediaPipe, fmtCtx *libav.AVFormatContext, streamIndex int) error {
	// Find codec
	codec := C.avcodec_find_decoder(C.enum_AVCodecID(mc.CodecID))
	if codec == nil {
		return fmt.Errorf("MediaCodecCreateLavc: codec not found for ID %d", mc.CodecID)
	}

	// Allocate context
	ctx := C.avcodec_alloc_context3(codec)
	if ctx == nil {
		return fmt.Errorf("MediaCodecCreateLavc: failed to allocate codec context")
	}

	// Store context as unsafe.Pointer
	mc.Ctx = libav.WrapAVCodecContext(unsafe.Pointer(ctx))

	// C: if(cw->fmt_ctx != NULL) avcodec_copy_context(cw->ctx, cw->fmt_ctx)
	// fmt_ctx is the stream-side codec context passed to media_codec_create.
	if mc.FmtCtx != nil {
		C.copy_codec_context(unsafe.Pointer(ctx), mc.FmtCtx.CPtr())
	} else if fmtCtx != nil && streamIndex >= 0 {
		// Compat seam: callers that only have (AVFormatContext, streamIndex)
		// copy the codecpar equivalent — the FFmpeg 7 analog of the above.
		ret := C.copy_codec_params_to_context(fmtCtx.CPtr(), C.int(streamIndex), unsafe.Pointer(ctx))
		if ret < 0 {
			lavTS.Error("LAVC", "copy_codec_params_to_context failed: %d", ret)
		}
		// Note: We do NOT manually fix width/height here.
		// The FFmpeg library version mismatch causes avcodec_parameters_to_context
		// to copy w/h incorrectly, but avcodec_open2 corrects them internally.
		// Manually setting them before open2 can cause the decoder to fail
		// to produce frames (observed with H.264 in MP4 containers).
	}

	// Handle extradata — copy to both Go struct and C codec context
	// (only if not already set by copy_codec_params_to_context)
	if ctx.extradata == nil && mcp != nil && mcp.ExtraData != nil && len(mcp.ExtraData) > 0 {
		// Copy to Go struct
		mc.ExtraData = make([]byte, len(mcp.ExtraData))
		copy(mc.ExtraData, mcp.ExtraData)
		mc.ExtraDataSize = len(mcp.ExtraData)
		// Copy to C codec context (needed before avcodec_open2 for H.264 etc.)
		// C: calloc(1, mcp->extradata_size + AV_INPUT_BUFFER_PADDING_SIZE)
		// — FFmpeg bitstream readers may over-read extradata, the tail
		// must be zeroed padding (libav.c:416-420).
		cExtra := C.ml_calloc(1, C.size_t(len(mcp.ExtraData)+64))
		if cExtra != nil {
			C.ml_memcpy(cExtra, unsafe.Pointer(&mcp.ExtraData[0]), C.size_t(len(mcp.ExtraData)))
			ctx.extradata = (*C.uint8_t)(cExtra)
			ctx.extradata_size = C.int(len(mcp.ExtraData))
		}
	}

	// C: if(mcp && mcp->cheat_for_speed) cw->ctx->flags2 |=
	//   AV_CODEC_FLAG2_FAST (libav.c:423-424 — HTSP sets this).
	if mcp != nil && mcp.CheatForSpeed {
		ctx.flags2 |= 1 // AV_CODEC_FLAG2_FAST = (1 << 0)
	}

	// C: if(codec->type == AVMEDIA_TYPE_VIDEO) — check the found codec,
	// not mc.MediaType (callers set that after media_codec_create).
	if C.ml_codec_get_type(unsafe.Pointer(codec)) == C.AVMEDIA_TYPE_VIDEO {
		mc.Opaque = mc

		// C: if(!(video_settings.vdpau && cw->codec_id ==
		//   AV_CODEC_ID_H264)) cw->ctx->thread_count =
		//   gconf.concurrency;   (libav.c:429-432) — VDPAU removed,
		//   the guard is always true now.
		ctx.thread_count = C.int(mp.FAM.Gconf().Concurrency)

		// Enable AV_CODEC_FLAG_COPY_OPAQUE so the decoder propagates
		// AVPacket.opaque to AVFrame.opaque. This replaces the removed
		// reordered_opaque mechanism and allows correct metadata
		// association for B-frame reordering (V13 fix).
		ctx.flags |= C.AV_CODEC_FLAG_COPY_OPAQUE

		// C: cw->decode = &libav_decode_video; cw->flush = &libav_video_flush
		mc.Decode = libavDecodeVideoCb
		mc.Flush = libavVideoFlushCb

		// C: ctx->get_format = libav_get_format — the VDPAU arm
		// (mp_vdpau_dev, media.h:320) is removed with VDPAU itself;
		// only the hwaccel extensions remain.
		{
			// VAAPI/NVDEC extensions (no C counterpart): bind a
			// DRM/CUDA hw device. Same-GPU backend first: NVDEC
			// when the renderer is NVIDIA (PRIME offload), VAAPI
			// otherwise — the dmabuf zero-copy import only works
			// intra-device. The loser stays as fallback:
			// cross-GPU hw decode + sw transfer still beats pure
			// software.
			bound := false
			nvdecFirst := NvidiaRenderActive(mp.Sys.VS)
			tryNvdec := func() {
				if bound || mp.Sys.VS.Nvdec == 0 {
					return
				}
				if devref := CudaDevice(mp.Sys.VS); devref != nil {
					if MlCudaBindCodec(libav.WrapAVCodecContext(unsafe.Pointer(ctx)), devref) != 0 {
						lavTS.Error("LAVC", "NVDEC codec bind failed, using software decode")
					} else {
						bound = true
						lavTS.Debug("LAVC", "NVDEC hardware decode enabled (software transfer)")
					}
				}
			}
			if nvdecFirst {
				tryNvdec()
			}
			if !bound && mp.Sys.VS.Vaapi != 0 {
				if devref := VaapiDevice(mp.Sys.VS); devref != nil {
					if MlVaapiBindCodec(libav.WrapAVCodecContext(unsafe.Pointer(ctx)), devref) == 0 {
						bound = true
						if mp.Sys.VS != nil && mp.Sys.VS.VaapiEGL.ZeroCopy() {
							lavTS.Debug("LAVC", "VAAPI hardware decode enabled (dmabuf zero-copy)")
						} else {
							lavTS.Debug("LAVC", "VAAPI hardware decode enabled (software transfer)")
						}
					} else {
						lavTS.Error("LAVC", "VAAPI codec bind failed")
					}
				}
			}
			if !nvdecFirst {
				tryNvdec()
			}
			// Windows hwaccel (no C counterpart): D3D11VA first —
			// the modern path; DXVA2/D3D9 is the legacy fallback.
			// Both always transfer to software frames.
			if !bound && mp.Sys.VS.D3d11va != 0 {
				if devref := D3d11vaDevice(); devref != nil {
					if MlD3d11vaBindCodec(libav.WrapAVCodecContext(unsafe.Pointer(ctx)), devref) == 0 {
						bound = true
						lavTS.Debug("LAVC", "D3D11VA hardware decode enabled (software transfer)")
					} else {
						lavTS.Error("LAVC", "D3D11VA codec bind failed")
					}
				}
			}
			if !bound && mp.Sys.VS.Dxva2 != 0 {
				if devref := Dxva2Device(); devref != nil {
					if MlDxva2BindCodec(libav.WrapAVCodecContext(unsafe.Pointer(ctx)), devref) != 0 {
						lavTS.Error("LAVC", "DXVA2 codec bind failed, using software decode")
					} else {
						bound = true
						lavTS.Debug("LAVC", "DXVA2 hardware decode enabled (software transfer)")
					}
				}
			}
			if !bound {
				lavTS.Debug("LAVC", "Software video decode")
			}
		}
	}

	// Open codec
	ret := C.avcodec_open2(ctx, codec, nil)
	if ret < 0 {
		// C: av_freep(&cw->ctx)
		C.avcodec_free_context(&ctx)
		mc.Ctx = nil
		return fmt.Errorf("MediaCodecCreateLavc: failed to open codec: %d", ret)
	}

	return nil
}

// avcodecSendPacket sends a packet to the decoder
func avcodecSendPacket(ctx *libav.AVCodecContext, pkt *libav.AVPacket) int {
	ret := C.avcodec_send_packet((*C.AVCodecContext)(ctx.CPtr()), (*C.AVPacket)(pkt.CPtr()))
	return int(ret)
}

// avcodecReceiveFrame receives a frame from the decoder
func avcodecReceiveFrame(ctx *libav.AVCodecContext, frame *libav.AVFrame) int {
	ret := C.avcodec_receive_frame((*C.AVCodecContext)(ctx.CPtr()), (*C.AVFrame)(frame.CPtr()))
	return int(ret)
}

// avcodecFlushBuffers flushes the codec buffers
func avcodecFlushBuffers(ctx *libav.AVCodecContext) {
	C.avcodec_flush_buffers((*C.AVCodecContext)(ctx.CPtr()))
}

// FlushVideoCodec flushes the video codec's decoder buffers.
// Mirrors C's avcodec_flush_buffers() call in video_decoder seek handling.
func FlushVideoCodec(mc *mediacore.MediaCodec) {
	if mc != nil && mc.Ctx != nil {
		C.avcodec_flush_buffers((*C.AVCodecContext)(mc.Ctx.CPtr()))
	}
}

// avFrameUnref unreferences a frame
func avFrameUnref(frame *libav.AVFrame) {
	C.av_frame_unref((*C.AVFrame)(frame.CPtr()))
}

// avPictureAlloc allocates a picture (FFmpeg 7: uses av_frame_get_buffer)
func avPictureAlloc(pic *libav.AVFrame, pixFmt int, width int, height int) int {
	// In FFmpeg 7, use av_frame_get_buffer
	frame := (*C.AVFrame)(pic.CPtr())
	frame.format = C.int(pixFmt)
	frame.width = C.int(width)
	frame.height = C.int(height)
	ret := C.av_frame_get_buffer(frame, 1) // align = 1
	return int(ret)
}

// avPictureFree frees a picture (FFmpeg 7: uses av_frame_unref)
func avPictureFree(pic *libav.AVFrame) {
	// In FFmpeg 7, use av_frame_unref
	C.av_frame_unref((*C.AVFrame)(pic.CPtr()))
}

// AVFrame field accessors
func avFrameWidth(frame *libav.AVFrame) int {
	return int((*C.AVFrame)(frame.CPtr()).width)
}

func avFrameHeight(frame *libav.AVFrame) int {
	return int((*C.AVFrame)(frame.CPtr()).height)
}

func avFrameSARNum(frame *libav.AVFrame) int {
	return int((*C.AVFrame)(frame.CPtr()).sample_aspect_ratio.num)
}

func avFrameSARDen(frame *libav.AVFrame) int {
	return int((*C.AVFrame)(frame.CPtr()).sample_aspect_ratio.den)
}

func avFramePictType(frame *libav.AVFrame) int {
	return int((*C.AVFrame)(frame.CPtr()).pict_type)
}

func avFrameInterlacedFrame(frame *libav.AVFrame) int {
	// FFmpeg 7: interlaced_frame field removed
	// This information is now in flags or other fields
	return 0
}

func avFrameTopFieldFirst(frame *libav.AVFrame) int {
	// FFmpeg 7: top_field_first field removed
	// This information is now in flags or other fields
	return 0
}

func avFrameRepeatPict(frame *libav.AVFrame) int {
	return int((*C.AVFrame)(frame.CPtr()).repeat_pict)
}

func avFrameFormat(frame *libav.AVFrame) int {
	return int((*C.AVFrame)(frame.CPtr()).format)
}

func avFrameLinesize(frame *libav.AVFrame, plane int) int {
	return int((*C.AVFrame)(frame.CPtr()).linesize[plane])
}

func avFrameData(frame *libav.AVFrame, plane int) unsafe.Pointer {
	return unsafe.Pointer((*C.AVFrame)(frame.CPtr()).data[plane])
}

// Raw AVFrame* helpers for glw_video_yuvp's non-PBO upload path
// (glw_video_yuvp.c keeps gvs->gvs_frame as AVFrame* and mutates
// data/linesize for field-split interlaced upload). The private
// accessors above share the same raw pointer representation.

// AvFrameAllocRaw — C: av_frame_alloc()
func AvFrameAllocRaw() *libav.AVFrame {
	return libav.WrapAVFrame(unsafe.Pointer(C.av_frame_alloc()))
}

// AvFrameFreeRaw — C: av_frame_free(&f); consumes the pointer.
func AvFrameFreeRaw(frame *libav.AVFrame) {
	if frame == nil {
		return
	}
	f := (*C.AVFrame)(frame.CPtr())
	C.av_frame_free(&f)
}

// AvFrameCloneRaw — C: av_frame_clone (removed from FFmpeg's public
// API; ml_frame_clone implements the equivalent alloc+ref).
func AvFrameCloneRaw(frame *libav.AVFrame) *libav.AVFrame {
	return libav.WrapAVFrame(C.ml_frame_clone(frame.CPtr()))
}

// AvFrameGetBuffer — C: av_frame_get_buffer(f, align)
func AvFrameGetBuffer(frame *libav.AVFrame, align int) int {
	return int(C.av_frame_get_buffer((*C.AVFrame)(frame.CPtr()), C.int(align)))
}

// AvFrameSetFormat sets frame->format (C: AVPixelFormat as int)
func AvFrameSetFormat(frame *libav.AVFrame, format int) {
	(*C.AVFrame)(frame.CPtr()).format = C.int(format)
}

// AvFrameSetWidth sets frame->width
func AvFrameSetWidth(frame *libav.AVFrame, w int) {
	(*C.AVFrame)(frame.CPtr()).width = C.int(w)
}

// AvFrameSetHeight sets frame->height
func AvFrameSetHeight(frame *libav.AVFrame, h int) {
	(*C.AVFrame)(frame.CPtr()).height = C.int(h)
}

// AvFrameLinesize — C: f->linesize[plane]
func AvFrameLinesize(frame *libav.AVFrame, plane int) int {
	return int((*C.AVFrame)(frame.CPtr()).linesize[plane])
}

// AvFrameSetLinesize — C: f->linesize[plane] = v
func AvFrameSetLinesize(frame *libav.AVFrame, plane, v int) {
	(*C.AVFrame)(frame.CPtr()).linesize[plane] = C.int(v)
}

// AvFrameData — C: f->data[plane]
func AvFrameData(frame *libav.AVFrame, plane int) unsafe.Pointer {
	return unsafe.Pointer((*C.AVFrame)(frame.CPtr()).data[plane])
}

// AvFrameDataAdd — C: f->data[plane] += off
func AvFrameDataAdd(frame *libav.AVFrame, plane, off int) {
	C.ml_frame_data_add(frame.CPtr(), C.int(plane), C.int(off))
}

func avFrameColorSpace(frame *libav.AVFrame) int {
	return int((*C.AVFrame)(frame.CPtr()).colorspace)
}

// avFrameGetOpaque returns the opaque field from AVFrame as a uintptr.
// Returns 0 if the decoder did not propagate an opaque value
// (e.g., codec doesn't support COPY_OPAQUE, or frame from drain).
// The caller must distinguish 0 (not set) from a valid index.
// We use uintptr instead of unsafe.Pointer to avoid checkptr failures
// under -race, since the opaque value is actually an integer index+1,
// not a real pointer.
func avFrameGetOpaque(frame *libav.AVFrame) uintptr {
	return uintptr(C.ml_frame_get_opaque(frame.CPtr()))
}

// avPacketSetOpaque sets the opaque field on an AVPacket.
// The value is propagated to the corresponding AVFrame by the decoder
// when AV_CODEC_FLAG_COPY_OPAQUE is set on the codec context.
// We store (index + 1) as the opaque value so that 0 (NULL) means
// "not set" and 1 means "index 0" — distinguishing the two cases.
func avPacketSetOpaque(pkt *libav.AVPacket, value uintptr) {
	C.ml_packet_set_opaque(pkt.CPtr(), C.uintptr_t(value))
}

// avPacketGetOpaque returns the opaque field from an AVPacket as uintptr.
// Used by tests to verify that opaque is set BEFORE avcodec_send_packet.
func avPacketGetOpaque(pkt *libav.AVPacket) uintptr {
	return uintptr(C.ml_packet_get_opaque(pkt.CPtr()))
}

// avCodecContextGetFlags returns the flags field from an AVCodecContext.
// Used by tests to verify that AV_CODEC_FLAG_COPY_OPAQUE is set after
// avcodec_open2.
func avCodecContextGetFlags(ctx *libav.AVCodecContext) int {
	return int(C.ml_codec_ctx_get_flags(ctx.CPtr()))
}

// avCodecCtxWidth returns the codec context's width field.
func avCodecCtxWidth(ctx *libav.AVCodecContext) int {
	return int((*C.AVCodecContext)(ctx.CPtr()).width)
}

// avCodecCtxHeight returns the codec context's height field.
func avCodecCtxHeight(ctx *libav.AVCodecContext) int {
	return int((*C.AVCodecContext)(ctx.CPtr()).height)
}

// avCodecCtxPixFmt returns the codec context's pixel format.
func avCodecCtxPixFmt(ctx *libav.AVCodecContext) int {
	return int((*C.AVCodecContext)(ctx.CPtr()).pix_fmt)
}

// pixFmtIsHwaccel — AV_PIX_FMT_FLAG_HWACCEL formats have no CPU
// planes: the decode loop must not avPictureAlloc for them.
func pixFmtIsHwaccel(pixFmt int) bool {
	return C.ml_pixfmt_is_hwaccel(C.int(pixFmt)) != 0
}

// AV_CODEC_FLAG_COPY_OPAQUE value (1 << 7 = 128).
// Exported for test verification.
const AVCodecFlagCopyOpaque = 1 << 7

// AV_PICTURE_TYPE_B is the picture type for B-frames
const AV_PICTURE_TYPE_B = 3

// avPacketAlloc allocates a new AVPacket (FFmpeg 7: replaces av_init_packet)
func avPacketAlloc() *libav.AVPacket {
	return libav.WrapAVPacket(unsafe.Pointer(C.av_packet_alloc()))
}

// avPacketFree frees an AVPacket
func avPacketFree(pkt *libav.AVPacket) {
	p := (*C.AVPacket)(pkt.CPtr())
	C.av_packet_free(&p)
}

// avNewPacket creates a new AVPacket with data
func avNewPacket(pkt *libav.AVPacket, data unsafe.Pointer, size int) int {
	ret := int(C.av_new_packet((*C.AVPacket)(pkt.CPtr()), C.int(size)))
	if ret < 0 {
		return ret
	}
	// Copy data into the packet's allocated buffer
	p := (*C.AVPacket)(pkt.CPtr())
	if p.data != nil && data != nil && size > 0 {
		C.ml_memcpy(unsafe.Pointer(p.data), data, C.size_t(size))
	}
	return 0
}

// avPictureData gets data pointer from AVFrame (FFmpeg 7: AVPicture removed)
func avPictureData(pic *libav.AVFrame, plane int) unsafe.Pointer {
	return unsafe.Pointer((*C.AVFrame)(pic.CPtr()).data[plane])
}

// avPictureLinesize gets linesize from AVFrame (FFmpeg 7: AVPicture removed)
func avPictureLinesize(pic *libav.AVFrame, plane int) int {
	return int((*C.AVFrame)(pic.CPtr()).linesize[plane])
}

// avPictureAllocStruct allocates an AVFrame structure (FFmpeg 7: AVPicture removed)
func avPictureAllocStruct() *libav.AVFrame {
	return libav.WrapAVFrame(unsafe.Pointer(C.av_frame_alloc()))
}

// avPacketSetPTS sets the PTS field of an AVPacket
func avPacketSetPTS(pkt *libav.AVPacket, pts int64) {
	(*C.AVPacket)(pkt.CPtr()).pts = C.int64_t(pts)
}

// avPacketSetDTS sets the DTS field of an AVPacket
func avPacketSetDTS(pkt *libav.AVPacket, dts int64) {
	(*C.AVPacket)(pkt.CPtr()).dts = C.int64_t(dts)
}

// avPacketSetDuration sets the duration field of an AVPacket
func avPacketSetDuration(pkt *libav.AVPacket, duration int64) {
	(*C.AVPacket)(pkt.CPtr()).duration = C.int64_t(duration)
}

// avPacketSetFlags sets the flags field of an AVPacket
func avPacketSetFlags(pkt *libav.AVPacket, flags int) {
	(*C.AVPacket)(pkt.CPtr()).flags = C.int(flags)
}

// AvPacketFromInfo creates a native AVPacket from AVPacketInfo data.
// Returns an unsafe.Pointer to the AVPacket. Caller must free with AvPacketFree.
func AvPacketFromInfo(info *AVPacketInfo) *libav.AVPacket {
	pkt := C.av_packet_alloc()
	if pkt == nil {
		return nil
	}
	if info.Data != nil && len(info.Data) > 0 {
		ret := C.av_new_packet(pkt, C.int(len(info.Data)))
		if ret < 0 {
			C.av_packet_free(&pkt)
			return nil
		}
		C.ml_memcpy(unsafe.Pointer(pkt.data), unsafe.Pointer(&info.Data[0]), C.size_t(len(info.Data)))
	}
	pkt.pts = C.int64_t(info.PTS)
	pkt.dts = C.int64_t(info.DTS)
	pkt.duration = C.int64_t(info.Duration)
	pkt.flags = C.int(info.Flags)
	return libav.WrapAVPacket(unsafe.Pointer(pkt))
}

// AvPacketFree frees an AVPacket allocated by AvPacketFromInfo.
func AvPacketFree(pkt *libav.AVPacket) {
	if pkt == nil {
		return
	}
	p := (*C.AVPacket)(pkt.CPtr())
	C.av_packet_free(&p)
}

// PixFmtChromaSubSample — C: av_pix_fmt_get_chroma_sub_sample
// (glw_video_common.c:1299).
func PixFmtChromaSubSample(pixFmt int) (hshift, vshift int) {
	var h, v C.int
	C.ml_chroma_sub_sample(C.int(pixFmt), &h, &v)
	return int(h), int(v)
}

// PixFmt constants for video_deliver_lavc's switch — queried from the
// bundled FFmpeg headers at init (never hardcoded).
var (
	LavcPixFmtYUV420P     = int(C.ml_pixfmt_const(0))
	LavcPixFmtYUV422P     = int(C.ml_pixfmt_const(1))
	LavcPixFmtYUV444P     = int(C.ml_pixfmt_const(2))
	LavcPixFmtYUV410P     = int(C.ml_pixfmt_const(3))
	LavcPixFmtYUV411P     = int(C.ml_pixfmt_const(4))
	LavcPixFmtYUV440P     = int(C.ml_pixfmt_const(5))
	LavcPixFmtYUVJ420P    = int(C.ml_pixfmt_const(6))
	LavcPixFmtYUVJ422P    = int(C.ml_pixfmt_const(7))
	LavcPixFmtYUVJ444P    = int(C.ml_pixfmt_const(8))
	LavcPixFmtYUVJ440P    = int(C.ml_pixfmt_const(9))
	LavcPixFmtBGR24       = int(C.ml_pixfmt_const(11))
	LavcPixFmtYUV420P10LE = int(C.ml_pixfmt_const(12))
	LavcPixFmtXYZ12LE     = int(C.ml_pixfmt_const(13))
	LavcPixFmtVAAPI       = int(C.ml_pixfmt_const(14))
	LavcPixFmtCUDA        = int(C.ml_pixfmt_const(15))
	LavcPixFmtD3D11VA     = int(C.ml_pixfmt_const(16))
	LavcPixFmtDXVA2VLD    = int(C.ml_pixfmt_const(17))
)
