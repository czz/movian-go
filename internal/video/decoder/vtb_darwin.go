//go:build darwin

package decoder

// Canonical port of src/video/vtb.c — VideoToolbox hardware decoder
// (macOS branch, !TARGET_OS_IPHONE: forces hw decode, OpenGL-compatible
// pixel buffers, kCVPixelFormatType_420YpCbCr8Planar → 'YUVP' delivery).
// The iOS-only 'CVPB' branch is kept structurally for parity.

/*
#cgo LDFLAGS: -framework VideoToolbox -framework CoreMedia -framework CoreVideo -framework CoreFoundation -framework CoreGraphics
#include <CoreFoundation/CoreFoundation.h>
#include <CoreMedia/CoreMedia.h>
#include <CoreVideo/CoreVideo.h>
#include <VideoToolbox/VideoToolbox.h>
#include <stdlib.h>

// Go-side output callback (C: picture_out — exported below with a POD
// signature; the trampoline unwraps CMTime).
extern void goVtbPictureOut(void *refcon, void *srcRefCon, int status,
                            unsigned int infoFlags, void *imageBuffer,
                            int64_t ptsValue, int32_t ptsScale,
                            int64_t durValue, int32_t durScale);

static void vtb_output_cb(void *decompressionOutputRefCon,
                          void *sourceFrameRefCon,
                          OSStatus status,
                          VTDecodeInfoFlags infoFlags,
                          CVImageBufferRef imageBuffer,
                          CMTime pts, CMTime dur) {
    goVtbPictureOut(decompressionOutputRefCon, sourceFrameRefCon,
                    (int)status, (unsigned int)infoFlags,
                    (void *)imageBuffer,
                    pts.value, pts.timescale,
                    dur.value, dur.timescale);
}

static void vtb_cb_record(VTDecompressionOutputCallbackRecord *r,
                          void *refcon) {
    r->decompressionOutputCallback = vtb_output_cb;
    r->decompressionOutputRefCon = refcon;
}

// Accessors for extern constants (not addressable from cgo).
static CFTypeRef vtb_kChromaBottom(void) { return kCVImageBufferChromaLocationBottomFieldKey; }
static CFTypeRef vtb_kChromaTop(void)    { return kCVImageBufferChromaLocationTopFieldKey; }
static CFTypeRef vtb_kChromaLeft(void)   { return kCVImageBufferChromaLocation_Left; }
static CFTypeRef vtb_kExtAtoms(void)     { return kCMFormatDescriptionExtension_SampleDescriptionExtensionAtoms; }
static CFTypeRef vtb_kEnableHW(void)     { return kVTVideoDecoderSpecification_EnableHardwareAcceleratedVideoDecoder; }
static CFTypeRef vtb_kRequireHW(void)    { return kVTVideoDecoderSpecification_RequireHardwareAcceleratedVideoDecoder; }
static CFTypeRef vtb_kGLCompat(void)     { return kCVPixelBufferOpenGLCompatibilityKey; }
static CFTypeRef vtb_kWidthKey(void)     { return kCVPixelBufferWidthKey; }
static CFTypeRef vtb_kHeightKey(void)    { return kCVPixelBufferHeightKey; }
static CFTypeRef vtb_kPixFmtKey(void)    { return kCVPixelBufferPixelFormatTypeKey; }
static CFTypeRef vtb_kBPRAlignKey(void)  { return kCVPixelBufferBytesPerRowAlignmentKey; }
static CFTypeRef vtb_kTrue(void)         { return kCFBooleanTrue; }
static CFTypeRef vtb_avcC(void)          { return CFSTR("avcC"); }
static const CFDictionaryKeyCallBacks *vtb_dictKCB(void) {
    return &kCFTypeDictionaryKeyCallBacks;
}
static const CFDictionaryValueCallBacks *vtb_dictVCB(void) {
    return &kCFTypeDictionaryValueCallBacks;
}
static CMTime vtb_cmtime(int64_t v, int32_t s) { return CMTimeMake(v, s); }
// void* entry points: modern cgo maps the CF/CV typedef'd pointers to
// distinct Go types (some uintptr-based), so Go<->C conversion through
// unsafe.Pointer is the only reliable route.
static CGSize vtb_displaySize(void *b) { return CVImageBufferGetDisplaySize((CVImageBufferRef)b); }
static CGSize vtb_encodedSize(void *b) { return CVImageBufferGetEncodedSize((CVImageBufferRef)b); }
static void   vtb_lockBuf(void *b)     { CVPixelBufferLockBaseAddress((CVPixelBufferRef)b, 0); }
static void   vtb_unlockBuf(void *b)   { CVPixelBufferUnlockBaseAddress((CVPixelBufferRef)b, 0); }
static void  *vtb_planeBase(void *b, size_t i) { return CVPixelBufferGetBaseAddressOfPlane((CVPixelBufferRef)b, i); }
static size_t vtb_planeBPR(void *b, size_t i)  { return CVPixelBufferGetBytesPerRowOfPlane((CVPixelBufferRef)b, i); }
static size_t vtb_planeHgt(void *b, size_t i)  { return CVPixelBufferGetHeightOfPlane((CVPixelBufferRef)b, i); }
static CFAllocatorRef vtb_allocDef(void) { return kCFAllocatorDefault; }
static void   vtb_retain(void *p)  { CFRetain((CFTypeRef)p); }
static void   vtb_release(void *p) { CFRelease((CFTypeRef)p); }
static OSStatus vtb_blkbuf(void *d, size_t sz, CMBlockBufferRef *out) {
    return CMBlockBufferCreateWithMemoryBlock(kCFAllocatorDefault, d, sz,
        kCFAllocatorNull, NULL, 0, sz, 0, out);
}
static OSStatus vtb_sampbuf(CMBlockBufferRef blk, CMFormatDescriptionRef f,
                            CMSampleTimingInfo *t, CMSampleBufferRef *out) {
    return CMSampleBufferCreate(kCFAllocatorDefault, blk, true, NULL, NULL,
        f, 1, 1, t, 0, NULL, out);
}
*/
import "C"

import (
	"fmt"
	"runtime/cgo"
	"sync"
	"unsafe"

	"github.com/czz/movian-go/internal/libav"

	mediacore "github.com/czz/movian-go/internal/media/core"
	tracepkg "github.com/czz/movian-go/internal/trace"
)

// vtbFrame — C: vtb_frame_t (vtb.c:39-43).
type vtbFrame struct {
	buf unsafe.Pointer // C: CVPixelBufferRef
	mbm mediacore.MediaBufMeta
}

// vtbDecoder — C: vtb_decoder_t (vtb.c:50-63).
type vtbDecoder struct {
	session C.VTDecompressionSessionRef
	fmtd    C.CMVideoFormatDescriptionRef

	mutex sync.Mutex
	vd    *VideoDecoder

	frames            []*vtbFrame // C: vtbd_frames (LIST_INSERT_SORTED)
	maxTS             int64
	flushTo           int64
	lastPTS           int64
	estimatedDuration int64
	pixelFormat       int

	handle cgo.Handle // Go-side ref passed as decompressionOutputRefCon
}

// destroyFrames — C: destroy_frames (vtb.c:82-87). Caller holds mutex.
func (vtbd *vtbDecoder) destroyFrames() {
	for _, vf := range vtbd.frames {
		C.vtb_release(vf.buf)
	}
	vtbd.frames = nil
}

// emitFrame — C: emit_frame (vtb.c:94-162). Called WITHOUT vtbd.mutex
// (the C caller unlocks around it).
func (vtbd *vtbDecoder) emitFrame(vf *vtbFrame, mq *mediacore.MediaQueue) {
	var fi mediacore.FrameInfo

	if vtbd.lastPTS != mediacore.PTSUnset && vf.mbm.PTS != mediacore.PTSUnset {
		d := vf.mbm.PTS - vtbd.lastPTS
		if d > 1000 && d < 1000000 {
			vtbd.estimatedDuration = d
		}
	}

	siz := C.vtb_displaySize(vf.buf)
	fi.DARNum = int(siz.width)
	fi.DARDen = int(siz.height)

	fi.PTS = vf.mbm.PTS
	fi.ColorSpace = -1
	fi.Epoch = vf.mbm.Epoch
	fi.DriveClock = vf.mbm.DriveClock
	fi.UserTime = vf.mbm.UserTime
	fi.VShift = 1
	fi.HShift = 1
	if vf.mbm.Duration > 10000 {
		fi.Duration = vf.mbm.Duration
	} else {
		fi.Duration = vtbd.estimatedDuration
	}

	siz = C.vtb_encodedSize(vf.buf)
	fi.Width = int(siz.width)
	fi.Height = int(siz.height)

	vd := vtbd.vd
	vd.EstimatedDuration = fi.Duration // For bitrate calculations

	switch uint32(vtbd.pixelFormat) {
	case uint32(C.kCVPixelFormatType_420YpCbCr8Planar):
		fi.Type = mediacore.FourCCYUVP // C: 'YUVP'

		C.vtb_lockBuf(vf.buf)

		for i := 0; i < 3; i++ {
			base := C.vtb_planeBase(vf.buf, C.size_t(i))
			pitch := int(C.vtb_planeBPR(vf.buf, C.size_t(i)))
			h := int(C.vtb_planeHgt(vf.buf, C.size_t(i)))
			fi.Data[i] = unsafe.Slice((*byte)(base), pitch*h)
			fi.Pitch[i] = pitch
		}

		if fi.Duration > 0 {
			vd.DeliverFrame(&fi)
		}

		C.vtb_unlockBuf(vf.buf)

	case uint32(C.kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange),
		uint32(C.kCVPixelFormatType_420YpCbCr8BiPlanarFullRange):
		// iOS-only upstream (TARGET_OS_IPHONE); unreachable on macOS
		// where pixel_format is always 420YpCbCr8Planar. fi_data[0]
		// carries the CVPixelBufferRef itself — expressed as a
		// zero-length slice aliasing the pointer value.
		fi.Type = mediacore.FourCCCVPB // C: 'CVPB'
		fi.Data[0] = *(*[]byte)(unsafe.Pointer(&struct {
			Data     unsafe.Pointer
			Len, Cap int
		}{unsafe.Pointer(vf.buf), 0, 0}))
		if fi.Duration > 0 {
			vd.DeliverFrame(&fi)
		}
	}

	vtbd.lastPTS = vf.mbm.PTS

	// C: prop_set_string(mq->mq_prop_codec, "h264 (VTB) %d x %d")
	if mq.PropCodec != nil {
		mq.PropCodec.SetString(fmt.Sprintf("h264 (VTB) %d x %d",
			fi.Width, fi.Height))
	}
}

// vfCmp — C: vf_cmp (vtb.c:169-181) — sort key (epoch, pts).
func vfCmp(a, b *vtbFrame) int {
	if a.mbm.Epoch < b.mbm.Epoch {
		return -1
	}
	if a.mbm.Epoch > b.mbm.Epoch {
		return 1
	}
	if a.mbm.PTS < b.mbm.PTS {
		return -1
	}
	if a.mbm.PTS > b.mbm.PTS {
		return 1
	}
	return 0
}

// pictureOut — C: picture_out (vtb.c:188-220), VTDecompressionOutputCallback.
// srcRefCon is &vd.Reorder[N] stored by vtbDecode.
//
//export goVtbPictureOut
func goVtbPictureOut(refcon, srcRefCon unsafe.Pointer, status C.int,
	infoFlags C.uint, imageBuffer unsafe.Pointer,
	ptsValue C.int64_t, ptsScale C.int32_t,
	durValue C.int64_t, durScale C.int32_t) {

	mbm := (*mediacore.MediaBufMeta)(srcRefCon)
	vtbd := cgo.Handle(refcon).Value().(*vtbDecoder)

	if imageBuffer == nil {
		return // No frame, typically from kVTDecodeFrame_DoNotOutputFrame
	}

	vf := &vtbFrame{mbm: *mbm, buf: imageBuffer}
	C.vtb_retain(imageBuffer)

	vtbd.mutex.Lock()

	// C: LIST_INSERT_SORTED(&vtbd->vtbd_frames, vf, vf_link, vf_cmp)
	i := 0
	for i < len(vtbd.frames) && vfCmp(vtbd.frames[i], vf) <= 0 {
		i++
	}
	vtbd.frames = append(vtbd.frames, nil)
	copy(vtbd.frames[i+1:], vtbd.frames[i:])
	vtbd.frames[i] = vf

	if vtbd.maxTS != mediacore.PTSUnset {
		if vf.mbm.PTS > vtbd.maxTS {
			vtbd.flushTo = vtbd.maxTS
			vtbd.maxTS = vf.mbm.PTS
		}
	} else {
		vtbd.maxTS = vf.mbm.PTS
	}
	vtbd.mutex.Unlock()
}

// vtbDecode — C: vtb_decode (vtb.c:227-298).
func vtbDecode(mc *mediacore.MediaCodec, vdi any,
	mq *mediacore.MediaQueue, mb *mediacore.MediaBuf, reqsize int) {
	vd := vdi.(*VideoDecoder)
	vtbd := mc.Opaque.(*vtbDecoder)
	var infoflags C.VTDecodeInfoFlags
	flags := C.kVTDecodeFrame_EnableAsynchronousDecompression |
		C.kVTDecodeFrame_EnableTemporalProcessing

	vtbd.vd = vd

	var blockBuf C.CMBlockBufferRef
	var dptr unsafe.Pointer
	if len(mb.Data) > 0 {
		dptr = unsafe.Pointer(&mb.Data[0])
	}
	status := C.vtb_blkbuf(dptr, C.size_t(mb.Size), &blockBuf)
	if status != 0 {
		decoderTS.ts.Trace(tracepkg.TRACE_ERROR, "VTB", "Data buffer allocation error %d", int(status))
		return
	}

	var ti C.CMSampleTimingInfo
	ti.duration = C.vtb_cmtime(C.int64_t(mb.Duration), 1000000)
	ti.presentationTimeStamp = C.vtb_cmtime(C.int64_t(mb.PTS), 1000000)
	ti.decodeTimeStamp = C.vtb_cmtime(C.int64_t(mb.DTS), 1000000)

	var sampleBuf C.CMSampleBufferRef
	status = C.vtb_sampbuf(blockBuf,
		C.CMFormatDescriptionRef(unsafe.Pointer(vtbd.fmtd)), &ti, &sampleBuf)

	C.vtb_release(unsafe.Pointer(blockBuf))
	if status != 0 {
		decoderTS.ts.Trace(tracepkg.TRACE_ERROR, "VTB", "Sample buffer allocation error %d", int(status))
		return
	}

	frameOpaque := unsafe.Pointer(&vd.Reorder[vd.ReorderPtr])
	mediacore.CopyMbmFromMb(&vd.Reorder[vd.ReorderPtr], mb)
	vd.ReorderPtr = (vd.ReorderPtr + 1) & VideoDecoderReorderMask

	if mb.Flags.Skip {
		flags |= C.kVTDecodeFrame_DoNotOutputFrame
	}

	status = C.VTDecompressionSessionDecodeFrame(vtbd.session, sampleBuf,
		C.VTDecodeFrameFlags(flags), frameOpaque, &infoflags)
	C.vtb_release(unsafe.Pointer(sampleBuf))
	if status != 0 {
		decoderTS.ts.Trace(tracepkg.TRACE_ERROR, "VTB", "Decoding error %d", int(status))
	}

	vtbd.mutex.Lock()

	if vtbd.flushTo != mediacore.PTSUnset {
		for len(vtbd.frames) > 0 {
			vf := vtbd.frames[0]
			if vtbd.flushTo < vf.mbm.PTS {
				break
			}
			vtbd.frames = vtbd.frames[1:]
			vtbd.mutex.Unlock()
			vtbd.emitFrame(vf, mq)
			vtbd.mutex.Lock()
			C.vtb_release(vf.buf)
		}
	}
	vtbd.mutex.Unlock()
}

// vtbFlush — C: vtb_flush (vtb.c:304-315).
func vtbFlush(mc *mediacore.MediaCodec, vd any) {
	vtbd := mc.Opaque.(*vtbDecoder)
	C.VTDecompressionSessionWaitForAsynchronousFrames(vtbd.session)
	vtbd.mutex.Lock()
	vtbd.destroyFrames()
	vtbd.maxTS = mediacore.PTSUnset
	vtbd.flushTo = mediacore.PTSUnset
	vtbd.lastPTS = mediacore.PTSUnset
	vtbd.mutex.Unlock()
}

// vtbClose — C: vtb_close (vtb.c:321-333).
func vtbClose(mc *mediacore.MediaCodec) {
	vtbd := mc.Opaque.(*vtbDecoder)
	C.VTDecompressionSessionWaitForAsynchronousFrames(vtbd.session)
	vtbd.destroyFrames()

	C.VTDecompressionSessionInvalidate(vtbd.session)
	C.vtb_release(unsafe.Pointer(vtbd.session))

	C.vtb_release(unsafe.Pointer(vtbd.fmtd))
	vtbd.handle.Delete()
}

// dictSetInt32 — C: dict_set_int32 (vtb.c:339-345).
func dictSetInt32(dict C.CFMutableDictionaryRef, key C.CFTypeRef, value int) {
	v := C.int32_t(value)
	num := C.CFNumberCreate(C.vtb_allocDef(), C.kCFNumberSInt32Type,
		unsafe.Pointer(&v))
	C.CFDictionarySetValue(dict, unsafe.Pointer(key), unsafe.Pointer(num))
	C.vtb_release(unsafe.Pointer(num))
}

// videoVtbCodecCreate — C: video_vtb_codec_create (vtb.c:351-501).
func videoVtbCodecCreate(mc *mediacore.MediaCodec,
	mcp *mediacore.MediaCodecParams, mp *mediacore.MediaPipe) int {

	// C: if(!video_settings.video_accel) return 1;
	if mp.Sys.VS.VideoAccel == 0 {
		return 1
	}

	switch mc.CodecID {
	case mediacore.CodecID(libav.AVCodecIDH264):
	default:
		return 1
	}

	if mcp == nil || len(mcp.ExtraData) == 0 || mcp.ExtraData[0] != 1 {
		return H264AnnexBToAvc(mc, mp, videoVtbCodecCreate)
	}

	configDict := C.CFDictionaryCreateMutable(C.vtb_allocDef(), 2,
		C.vtb_dictKCB(), C.vtb_dictVCB())

	C.CFDictionarySetValue(configDict, unsafe.Pointer(C.vtb_kChromaBottom()),
		unsafe.Pointer(C.vtb_kChromaLeft()))
	C.CFDictionarySetValue(configDict, unsafe.Pointer(C.vtb_kChromaTop()),
		unsafe.Pointer(C.vtb_kChromaLeft()))

	// Setup extradata
	extradataDict := C.CFDictionaryCreateMutable(C.vtb_allocDef(), 1,
		C.vtb_dictKCB(), C.vtb_dictVCB())

	extradata := C.CFDataCreate(C.vtb_allocDef(),
		(*C.UInt8)(unsafe.Pointer(&mcp.ExtraData[0])),
		C.CFIndex(len(mcp.ExtraData)))
	C.CFDictionarySetValue(extradataDict, unsafe.Pointer(C.vtb_avcC()),
		unsafe.Pointer(extradata))
	C.vtb_release(unsafe.Pointer(extradata))
	C.CFDictionarySetValue(configDict, unsafe.Pointer(C.vtb_kExtAtoms()),
		unsafe.Pointer(extradataDict))
	C.vtb_release(unsafe.Pointer(extradataDict))

	// C: #if !TARGET_OS_IPHONE — enable and force HW acceleration
	C.CFDictionarySetValue(configDict, unsafe.Pointer(C.vtb_kEnableHW()),
		unsafe.Pointer(C.vtb_kTrue()))
	C.CFDictionarySetValue(configDict, unsafe.Pointer(C.vtb_kRequireHW()),
		unsafe.Pointer(C.vtb_kTrue()))

	var fmtd C.CMVideoFormatDescriptionRef
	status := C.CMVideoFormatDescriptionCreate(C.vtb_allocDef(),
		C.CMVideoCodecType(C.kCMVideoCodecType_H264),
		C.int32_t(mcp.Width), C.int32_t(mcp.Height),
		C.CFDictionaryRef(unsafe.Pointer(configDict)), &fmtd)
	if status != 0 {
		decoderTS.ts.Trace(tracepkg.TRACE_DEBUG, "VTB", "Unable to create description %d", int(status))
		return 1
	}

	surfaceDict := C.CFDictionaryCreateMutable(C.vtb_allocDef(), 2,
		C.vtb_dictKCB(), C.vtb_dictVCB())

	C.CFDictionarySetValue(surfaceDict, unsafe.Pointer(C.vtb_kGLCompat()),
		unsafe.Pointer(C.vtb_kTrue()))

	vtbd := &vtbDecoder{}

	dictSetInt32(surfaceDict, C.vtb_kWidthKey(), mcp.Width)
	dictSetInt32(surfaceDict, C.vtb_kHeightKey(), mcp.Height)

	// C: #else (!TARGET_OS_IPHONE) — 420YpCbCr8Planar on macOS
	vtbd.pixelFormat = int(C.kCVPixelFormatType_420YpCbCr8Planar)

	dictSetInt32(surfaceDict, C.vtb_kPixFmtKey(), vtbd.pixelFormat)

	linewidth := mcp.Width

	switch uint32(vtbd.pixelFormat) {
	case uint32(C.kCVPixelFormatType_420YpCbCr8BiPlanarFullRange),
		uint32(C.kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange):
		linewidth *= 2
	}

	dictSetInt32(surfaceDict, C.vtb_kBPRAlignKey(), linewidth)

	vtbd.handle = cgo.NewHandle(vtbd)

	var cb C.VTDecompressionOutputCallbackRecord
	C.vtb_cb_record(&cb, unsafe.Pointer(vtbd.handle))

	// create decompression session
	status = C.VTDecompressionSessionCreate(C.vtb_allocDef(), fmtd,
		C.CFDictionaryRef(unsafe.Pointer(configDict)),
		C.CFDictionaryRef(unsafe.Pointer(surfaceDict)),
		&cb, &vtbd.session)

	C.vtb_release(unsafe.Pointer(configDict))
	C.vtb_release(unsafe.Pointer(surfaceDict))

	if status != 0 {
		decoderTS.ts.Trace(tracepkg.TRACE_DEBUG, "VTB", "Failed to open -- %d", int(status))
		C.vtb_release(unsafe.Pointer(fmtd))
		vtbd.handle.Delete()
		return 1
	}
	vtbd.fmtd = fmtd
	vtbd.maxTS = mediacore.PTSUnset
	vtbd.flushTo = mediacore.PTSUnset
	vtbd.lastPTS = mediacore.PTSUnset

	mc.Opaque = vtbd
	mc.Decode = vtbDecode
	mc.Close = vtbClose
	mc.Flush = vtbFlush

	decoderTS.ts.Trace(tracepkg.TRACE_DEBUG, "VTB", "Opened decoder")
	return 0
}

func init() {
	// C: REGISTER_CODEC(NULL, video_vtb_codec_create, 10)
	mediacore.MediaRegisterCodec(&mediacore.CodecDef{
		Open: videoVtbCodecCreate,
		Prio: 10,
	})
}
