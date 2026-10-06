//go:build android

package libav

// Canonical port of src/arch/android/android_video_codec.c — the
// MediaCodec JNI video decoder. All JNI calls go through static
// wrappers (the C calls (*env)->Method(...) directly). The codec
// registers itself at prio 100 like C's REGISTER_CODEC(...,100), so
// it claims h264/hevc/mpeg4/mpeg2/vp8/vp9 before lavc's prio-1000
// fallback when video_accel is enabled.

/*
#cgo LDFLAGS: -ljnigraphics
#include <jni.h>
#include <stdlib.h>
#include <string.h>
#include <android/log.h>
#include <libavcodec/avcodec.h>

static void *ml_pkt_data(AVPacket *p) { return p->data; }
static int   ml_pkt_size(AVPacket *p) { return p->size; }

static void ml_avc_log(int prio, const char *msg) {
  __android_log_print(prio, "Movian Go", "%s", msg);
}

// --- JNIEnv wrappers (every (*env)->* call used by android_video_codec.c) --

static jclass    ml_find_class(JNIEnv *e, const char *n) {
  return (*e)->FindClass(e, n);
}
static jmethodID ml_get_method(JNIEnv *e, jclass c, const char *n, const char *s) {
  return (*e)->GetMethodID(e, c, n, s);
}
static jmethodID ml_get_static_method(JNIEnv *e, jclass c, const char *n, const char *s) {
  return (*e)->GetStaticMethodID(e, c, n, s);
}
static jfieldID  ml_get_field(JNIEnv *e, jclass c, const char *n, const char *s) {
  return (*e)->GetFieldID(e, c, n, s);
}
static jclass    ml_get_obj_class(JNIEnv *e, jobject o) {
  return (*e)->GetObjectClass(e, o);
}
static jobject   ml_new_global(JNIEnv *e, jobject o) {
  return (*e)->NewGlobalRef(e, o);
}
static void      ml_del_global(JNIEnv *e, jobject o) {
  (*e)->DeleteGlobalRef(e, o);
}
static void      ml_del_local(JNIEnv *e, jobject o) {
  (*e)->DeleteLocalRef(e, o);
}
static jstring   ml_new_string(JNIEnv *e, const char *s) {
  return (*e)->NewStringUTF(e, s);
}
static jobject   ml_new_object(JNIEnv *e, jclass c, jmethodID m) {
  return (*e)->NewObject(e, c, m);
}
static jint      ml_call_int(JNIEnv *e, jobject o, jmethodID m, jlong a) {
  return (*e)->CallIntMethod(e, o, m, a);
}
static jobject   ml_call_obj0(JNIEnv *e, jobject o, jmethodID m) {
  return (*e)->CallObjectMethod(e, o, m);
}
static jobject   ml_call_obj1i(JNIEnv *e, jobject o, jmethodID m, jint a) {
  return (*e)->CallObjectMethod(e, o, m, a);
}
static void      ml_call_void_queue(JNIEnv *e, jobject o, jmethodID m,
                                    jint idx, jint off, jint size,
                                    jlong pts, jint flags) {
  (*e)->CallVoidMethod(e, o, m, idx, off, size, pts, flags);
}
static jint      ml_call_dequeue_out(JNIEnv *e, jobject o, jmethodID m,
                                     jobject info, jlong timeout) {
  return (*e)->CallIntMethod(e, o, m, info, timeout);
}
static void      ml_call_release(JNIEnv *e, jobject o, jmethodID m,
                                 jint idx, jboolean render) {
  (*e)->CallVoidMethod(e, o, m, idx, render);
}
static void      ml_call_release_timed(JNIEnv *e, jobject o, jmethodID m,
                                       jint idx, jlong ts) {
  (*e)->CallVoidMethod(e, o, m, idx, ts);
}
static jobject   ml_call_static_obj(JNIEnv *e, jclass c, jmethodID m,
                                    jstring s, jint w, jint h) {
  return (*e)->CallStaticObjectMethod(e, c, m, s, w, h);
}
static jobject   ml_call_static_obj1s(JNIEnv *e, jclass c, jmethodID m,
                                      jstring s) {
  return (*e)->CallStaticObjectMethod(e, c, m, s);
}
static void      ml_call_static_void(JNIEnv *e, jclass c, jmethodID m,
                                     jobject a, jint b) {
  (*e)->CallStaticVoidMethod(e, c, m, a, b);
}
static jint      ml_call_get_integer(JNIEnv *e, jobject o, jmethodID m,
                                     jstring name) {
  return (*e)->CallIntMethod(e, o, m, name);
}
static void      ml_call_configure(JNIEnv *e, jobject o, jmethodID m,
                                   jobject fmt, jobject surf) {
  (*e)->CallVoidMethod(e, o, m, fmt, surf, NULL, 0);
}
static jobject   ml_get_arr_elem(JNIEnv *e, jobjectArray a, jint i) {
  return (*e)->GetObjectArrayElement(e, a, i);
}
static void     *ml_dba(JNIEnv *e, jobject bb) {
  return (*e)->GetDirectBufferAddress(e, bb);
}
static jlong     ml_dbc(JNIEnv *e, jobject bb) {
  return (*e)->GetDirectBufferCapacity(e, bb);
}
static jlong     ml_get_long_field(JNIEnv *e, jobject o, jfieldID f) {
  return (*e)->GetLongField(e, o, f);
}
static jint      ml_push_frame(JNIEnv *e, jint cap) {
  return (*e)->PushLocalFrame(e, cap);
}
static void      ml_pop_frame(JNIEnv *e) {
  (*e)->PopLocalFrame(e, NULL);
}
static jboolean  ml_exc_occurred(JNIEnv *e) {
  return (*e)->ExceptionOccurred(e) != NULL;
}
static void      ml_exc_clear(JNIEnv *e) {
  (*e)->ExceptionClear(e);
}
static void      ml_exc_describe(JNIEnv *e) {
  (*e)->ExceptionDescribe(e);
}
// ExceptionOccurred + Clear + throwable.toString() — a single line
// that survives stacks too deep for ExceptionDescribe's print.
static char     *ml_exc_string(JNIEnv *e) {
  jthrowable t = (*e)->ExceptionOccurred(e);
  if(t == NULL) return NULL;
  (*e)->ExceptionClear(e);
  jclass cls = (*e)->GetObjectClass(e, t);
  jmethodID mid = (*e)->GetMethodID(e, cls, "toString",
                                   "()Ljava/lang/String;");
  jstring s = (jstring)(*e)->CallObjectMethod(e, t, mid);
  if(s == NULL) { (*e)->ExceptionClear(e); return NULL; }
  const char *c = (*e)->GetStringUTFChars(e, s, NULL);
  char *out = c ? strdup(c) : NULL;
  if(c) (*e)->ReleaseStringUTFChars(e, s, c);
  (*e)->DeleteLocalRef(e, s);
  (*e)->DeleteLocalRef(e, t);
  return out;
}
*/
import "C"

import (
	"runtime"
	"runtime/cgo"
	"sync"
	"unsafe"

	"github.com/czz/movian-go/internal/arch"
	"github.com/czz/movian-go/internal/libav"
	mediacore "github.com/czz/movian-go/internal/media/core"
	propcore "github.com/czz/movian-go/internal/prop"
	decoder "github.com/czz/movian-go/internal/video/decoder"
)

// C: gconf.enable_MediaCodec_debug — read via avc.mp.FAM.Gconf().
// (main.h gconf_t) — enables the AVC timing instrumentation. Android
// settings binding arrives with the android dev-settings port.

// SetEnableMediaCodecDebug — C: gconf.enable_MediaCodec_debug.
// (debug flag now lives on the process gconf — written via deps.Gconf)

// avcBuffer — C: avc_buffer_t + avc_buffer_queue
// (android_video_codec.c:52-58). Go slice stands in for the TAILQ.
type avcBuffer struct {
	pts int64
	id  int
}

// androidVideoCodec — C: android_video_codec_t
// (android_video_codec.c:63-119).
type androidVideoCodec struct {
	decoder         unsafe.Pointer // C: avc_decoder (jobject, global ref)
	mediaCodecClass unsafe.Pointer // C: avc_MediaCodec (jclass, global ref)

	mime          string // C: avc_mime
	width, height int    // C: avc_width / avc_height

	async  bool // C: avc_async
	direct int  // C: avc_direct

	inputBuffers  unsafe.Pointer // C: avc_input_buffers (jobjectArray)
	outputBuffers unsafe.Pointer // C: avc_output_buffers (jobjectArray)
	bufferInfo    unsafe.Pointer // C: avc_buffer_info (jobject)

	dequeueInputBuffer       C.jmethodID
	getInputBuffer           C.jmethodID
	queueInputBuffer         C.jmethodID
	dequeueOutputBuffer      C.jmethodID
	releaseOutputBuffer      C.jmethodID
	releaseOutputBufferTimed C.jmethodID

	outWidth  int // C: avc_out_width
	outHeight int // C: avc_out_height
	outStride int // C: avc_out_stride
	outFmt    int // C: avc_out_fmt

	nicename string // C: avc_nicename

	ts1        int64 // C: avc_ts1 (MediaCodec debug timing)
	ts2        int64 // C: avc_ts2
	decodeTime int   // C: avc_decode_time

	asyncInputBuffers  []avcBuffer // C: avc_async_input_buffers (TAILQ)
	asyncOutputBuffers []avcBuffer // C: avc_async_output_buffers (TAILQ)

	mutex sync.Locker // C: avc_mutex = &mp->mp_mutex
	cond  *sync.Cond  // C: avc_cond = &mp->mp_video.mq_avail

	h264Parser decoder.H264Parser // C: avc_h264_parser

	bsf *libav.BSFContext // C: avc_bsf (av_bsf_* = FFmpeg-9 form of the
	// removed av_bitstream_filter_* API)

	codecInfo *propcore.Prop // C: avc_codec_info (ref-held)

	mp *mediacore.MediaPipe

	// handle — the int passed to Java as jopaque in
	// setVideoDecoderWrapper; C passes the truncated pointer, the Go
	// port passes a cgo.Handle (same mechanism: an int the JVM hands
	// back to the vd* natives).
	handle cgo.Handle
}

// jniEnv — attached JNIEnv for the calling OS thread
// (C: (*JVM)->GetEnv(JVM, &env, JNI_VERSION_1_6)).
func jniEnv() *C.JNIEnv { return (*C.JNIEnv)(unsafe.Pointer(arch.JNIEnv())) }

// checkException — C: check_exception (android_video_codec.c:41-49).
func checkException(env *C.JNIEnv, what string) int {
	cs := C.ml_exc_string(env)
	if cs == nil {
		return 0
	}
	lavTS.Debug("AVC", "JNI exception in %s: %s", what, C.GoString(cs))
	C.free(unsafe.Pointer(cs))
	return 1
}

// getInteger — C: getInteger (android_video_codec.c:121-129):
// MediaFormat.getInteger(name). Missing keys (e.g. "stride",
// "color-format" on some OMX firmwares) throw — the pending exception
// must be cleared here or the NEXT JNI call crashes ART in
// FindCatchBlock. Returns -1 when the key is absent.
func getInteger(env *C.JNIEnv, obj C.jobject, name string) int {
	cls := C.ml_get_obj_class(env, obj)
	mid := C.ml_get_method(env, cls, C.CString("getInteger"),
		C.CString("(Ljava/lang/String;)I"))
	jname := C.ml_new_string(env, C.CString(name))
	v := int(C.ml_call_get_integer(env, obj, mid, jname))
	if C.ml_exc_occurred(env) != 0 {
		C.ml_exc_clear(env)
		lavTS.Debug("AVC", "MediaFormat.getInteger(%s) absent", name)
		return -1
	}
	return v
}

// --- JNI callbacks invoked by the Java MediaCodec.Callback -----------
// (android_video_codec.c:131-228). jopaque is the int handle returned
// to Java via Core.setVideoDecoderWrapper; C stores the raw pointer —
// the Go port uses a cgo.Handle which is still a small int.

// VdInputAvailable — C: Java_..._Core_vdInputAvailable.
func VdInputAvailable(jopaque, jbuf int32) {
	h := cgo.Handle(uintptr(jopaque))
	avc := h.Value().(*androidVideoCodec)
	avc.mutex.Lock()
	avc.asyncInputBuffers = append(avc.asyncInputBuffers,
		avcBuffer{id: int(jbuf)})
	avc.cond.Signal()
	avc.mutex.Unlock()
}

// VdOutputAvailable — C: Java_..._Core_vdOutputAvailable.
func VdOutputAvailable(jopaque, jbuf int32, pts int64) {
	h := cgo.Handle(uintptr(jopaque))
	avc := h.Value().(*androidVideoCodec)
	avc.mutex.Lock()
	avc.asyncOutputBuffers = append(avc.asyncOutputBuffers,
		avcBuffer{id: int(jbuf), pts: pts})
	avc.cond.Signal()
	avc.mutex.Unlock()
}

// VdOutputFormatChanged — C: Java_..._Core_vdOutputFormatChanged.
func VdOutputFormatChanged(jopaque int32, jinfo unsafe.Pointer) {
	h := cgo.Handle(uintptr(jopaque))
	avc := h.Value().(*androidVideoCodec)
	env := jniEnv()
	if env == nil {
		return
	}
	info := C.jobject(jinfo)
	width := getInteger(env, info, "width")
	height := getInteger(env, info, "height")
	stride := getInteger(env, info, "stride")
	outFmt := getInteger(env, info, "color-format")

	lavTS.Debug("VIDEO", "Output format changed to %d x %d [%d] colfmt:%d",
		width, height, stride, outFmt)

	if avc.codecInfo != nil {
		avc.codecInfo.SetString(
			sprintfCodecInfo(avc.nicename, width, height))
	}
}

// VdError — C: Java_..._Core_vdError.
func VdError(jopaque int32) {
	lavTS.Error("AVC", "vdError")
}

// sprintfCodecInfo — C: snprintf("%s %dx%d (Accelerated)")
// (android_video_codec.c:207-210 / 290-293).
func sprintfCodecInfo(nicename string, w, h int) string {
	return nicename + " " + itoa(w) + "x" + itoa(h) + " (Accelerated)"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// avcEnq — C: avc_enq (android_video_codec.c:235-263): dequeue a
// MediaCodec input buffer, copy the packet in, queue it.
func avcEnq(env *C.JNIEnv, avc *androidVideoCodec, data []byte,
	size int, pts int64, flags int, timeout int64) int {

	idx := int(C.ml_call_int(env, C.jobject(avc.decoder),
		avc.dequeueInputBuffer, C.jlong(timeout)))
	if checkException(env, "dequeueInputBuffer") != 0 {
		return -2
	}
	if idx < 0 {
		return -1
	}

	bb := C.ml_get_arr_elem(env, C.jobjectArray(avc.inputBuffers), C.jint(idx))
	defer C.ml_del_local(env, bb)
	if checkException(env, "getInputBuffers[i]") != 0 || bb == 0 {
		return idx
	}

	bbBuf := C.ml_dba(env, bb)
	bbSize := int(C.ml_dbc(env, bb))
	if bbSize < size {
		lavTS.Debug("android", "Video packet buffer too small %d < %d",
			bbSize, size)
		return idx
	}
	if size > 0 && len(data) > 0 { // C memcpy(0) is a no-op; &data[0] is not
		C.memcpy(bbBuf, unsafe.Pointer(&data[0]), C.size_t(size))
	}

	C.ml_call_void_queue(env, C.jobject(avc.decoder),
		avc.queueInputBuffer, C.jint(idx), 0, C.jint(size),
		C.jlong(pts), C.jint(flags))
	checkException(env, "queueInputBuffer")
	return idx
}

// updateOutputFormat — C: update_output_format
// (android_video_codec.c:270-297).
func updateOutputFormat(env *C.JNIEnv, avc *androidVideoCodec) {
	mid := C.ml_get_method(env, C.jclass(avc.mediaCodecClass),
		C.CString("getOutputFormat"),
		C.CString("()Landroid/media/MediaFormat;"))
	format := C.ml_call_obj0(env, C.jobject(avc.decoder), mid)
	defer C.ml_del_local(env, format)
	if checkException(env, "getOutputFormat") != 0 || format == 0 {
		return
	}

	avc.outWidth = getInteger(env, format, "width")
	avc.outHeight = getInteger(env, format, "height")
	avc.outStride = getInteger(env, format, "stride")
	avc.outFmt = getInteger(env, format, "color-format")

	lavTS.Debug("VIDEO", "Output format changed to %d x %d [%d] colfmt:%d",
		avc.outWidth, avc.outHeight, avc.outStride, avc.outFmt)

	if avc.codecInfo != nil {
		avc.codecInfo.SetString(
			sprintfCodecInfo(avc.nicename, avc.outWidth, avc.outHeight))
	}
}

// avcGetOutputBuffers — C: avc_get_output_buffers
// (android_video_codec.c:300-314).
func avcGetOutputBuffers(env *C.JNIEnv, avc *androidVideoCodec) {
	mid := C.ml_get_method(env, C.jclass(avc.mediaCodecClass),
		C.CString("getOutputBuffers"),
		C.CString("()[Ljava/nio/ByteBuffer;"))
	obj := C.ml_call_obj0(env, C.jobject(avc.decoder), mid)
	checkException(env, "getOutputBuffers")
	if avc.outputBuffers != nil {
		C.ml_del_global(env, C.jobject(avc.outputBuffers))
	}
	if obj != 0 {
		avc.outputBuffers = unsafe.Pointer(C.ml_new_global(env, obj))
	} else {
		avc.outputBuffers = nil
	}
}

// fillFrameInfoFromPts — C: fill_frame_info_from_pts
// (android_video_codec.c:317-348): matches the returned PTS against
// the reorder ring to recover epoch/usertime/driveclock/duration/skip.
func fillFrameInfoFromPts(fi *mediacore.FrameInfo, vd *decoder.VideoDecoder,
	avc *androidVideoCodec, pts int64) int {
	fi.DARNum = avc.width
	fi.DARDen = avc.height
	fi.PTS = pts

	for i := 0; i < decoder.VideoDecoderReorderSize; i++ {
		mbm := &vd.Reorder[i]
		if mbm.PTS == pts {
			fi.Epoch = mbm.Epoch
			fi.UserTime = mbm.UserTime
			fi.DriveClock = mbm.DriveClock
			fi.Duration = mbm.Duration
			fi.PTS = mbm.PTS
			mbm.PTS = mediacore.PTSUnset
			if mbm.Flags.Skip {
				return 1
			}
			return 0
		}
	}
	return 0
}

// getOutput — C: get_output (android_video_codec.c:350-419): drains
// the MediaCodec output queue; in direct ('SURF') mode the codec
// renders to the Java Surface itself.
func getOutput(env *C.JNIEnv, avc *androidVideoCodec, loop bool,
	vd *decoder.VideoDecoder) {
	for {
		idx := int(C.ml_call_dequeue_out(env, C.jobject(avc.decoder),
			avc.dequeueOutputBuffer, C.jobject(avc.bufferInfo),
			C.jlong(15000)))
		checkException(env, "dequeueOutputBuffer")

		if idx >= 0 {
			cls := C.ml_get_obj_class(env, C.jobject(avc.bufferInfo))
			fPts := C.ml_get_field(env, cls,
				C.CString("presentationTimeUs"), C.CString("J"))
			pts := int64(C.ml_get_long_field(env,
				C.jobject(avc.bufferInfo), fPts))

			var fi mediacore.FrameInfo
			fillFrameInfoFromPts(&fi, vd, avc, pts)

			if avc.direct != 0 {
				fi.Type = mediacore.FourCCSURF // C: 'SURF'
				if avc.mp.FAM.Gconf().EnableMediaCodecDebug.Load() {
					avc.ts1 = arch.GetTS()
					if avc.ts2 != 0 {
						avc.decodeTime = int(avc.ts1 - avc.ts2)
					}
				}
				vd.DeliverFrame(&fi)
				if avc.mp.FAM.Gconf().EnableMediaCodecDebug.Load() {
					avc.ts2 = arch.GetTS()
				}
			} else {
				buf := C.ml_get_arr_elem(env,
					C.jobjectArray(avc.outputBuffers), C.jint(idx))
				if checkException(env, "outputBuffers[i]") != 0 || buf == 0 {
					break
				}
				ptr := C.ml_dba(env, buf)
				fi.Data[0] = unsafe.Slice((*byte)(ptr),
					avc.outWidth*avc.outHeight*2)
				fi.Pitch[0] = avc.outWidth * 2
				fi.Width = avc.outWidth
				fi.Height = avc.outHeight
				fi.Type = mediacore.FourCCYUVP // C: 'YUVP'
				vd.DeliverFrame(&fi)
				C.ml_del_local(env, buf)
			}

			C.ml_call_release(env, C.jobject(avc.decoder),
				avc.releaseOutputBuffer, C.jint(idx), 1)
			checkException(env, "releaseOutputBuffer")

		} else if idx == -2 {
			updateOutputFormat(env, avc)
			continue
		} else if idx == -3 {
			avcGetOutputBuffers(env, avc)
			continue
		} else {
			break
		}
		if !loop {
			break
		}
	}
}

// storeMetadata — C: store_metadata (android_video_codec.c:422-461):
// stash the packet meta in the reorder ring and infer the PTS the
// codec will hand back.
func storeMetadata(vd *decoder.VideoDecoder, mb *mediacore.MediaBuf,
	avc *androidVideoCodec, mc *mediacore.MediaCodec,
	data []byte, size int) int64 {
	mbm := &vd.Reorder[vd.ReorderPtr]
	mediacore.CopyMbmFromMb(mbm, mb)
	vd.ReorderPtr = (vd.ReorderPtr + 1) & decoder.VideoDecoderReorderMask

	isBFrame := false
	switch mc.CodecID {
	case mediacore.CodecID(C.AV_CODEC_ID_MPEG4):
		if mb.Size <= 7 {
			return 0
		}
		frameType := 0
		if len(data) > 4 && data[0] == 0x00 && data[1] == 0x00 &&
			data[2] == 0x01 && data[3] == 0xb6 {
			frameType = int(data[4]) >> 6
		}
		if frameType == 2 {
			isBFrame = true
		}
	case mediacore.CodecID(C.AV_CODEC_ID_H264):
		avc.h264Parser.DecodeData(data[:size])
		if avc.h264Parser.SliceTypeNOS == decoder.SliceTypeB {
			isBFrame = true
		}
	}

	mbm.PTS = vd.InferPTS(mbm, isBFrame)
	return mbm.PTS
}

// bsfFilter — C: the av_bitstream_filter_filter call inside
// android_codec_decode / decode_locked. FFmpeg 9 only has the
// send/receive AVBSF API (the old inline filter was removed), so the
// packet is wrapped in an AVPacket, pushed through the BSF and the
// filtered bytes are read back.
func bsfFilter(avc *androidVideoCodec, data []byte, size int,
	keyframe bool) ([]byte, bool) {
	if avc.bsf == nil {
		return nil, false
	}
	pkt := avPacketAlloc()
	if pkt == nil {
		return nil, false
	}
	defer avPacketFree(pkt)
	// C passed a raw pointer; &data[0] on an empty buffer would panic —
	// treat it like C's NULL (nothing to filter).
	var dp unsafe.Pointer
	if len(data) > 0 {
		dp = unsafe.Pointer(&data[0])
	}
	if avNewPacket(pkt, dp, size) < 0 {
		return nil, false
	}
	flags := 0
	if keyframe {
		flags |= 0x0001 // AV_PKT_FLAG_KEY
	}
	avPacketSetFlags(pkt, flags)
	if err := avc.bsf.SendPacket(pkt); err != nil {
		return nil, false
	}
	out := avPacketAlloc()
	if out == nil {
		return nil, false
	}
	defer avPacketFree(out)
	if err := avc.bsf.ReceivePacket(out); err != nil {
		return nil, false
	}
	n := int(C.ml_pkt_size((*C.AVPacket)(out.CPtr())))
	buf := make([]byte, n)
	C.memcpy(unsafe.Pointer(&buf[0]),
		C.ml_pkt_data((*C.AVPacket)(out.CPtr())), C.size_t(n))
	return buf, true
}

// mbData — C: mb->mb_data / mb->mb_size were macros over
// mb->mb_pkt->data/size; AVPacket-backed buffers carry the payload in
// mb.Pkt while plain buffers use mb.Data/mb.Size.
func mbData(mb *mediacore.MediaBuf) ([]byte, int) {
	if mb.Pkt != nil {
		if d := PacketData(mb.Pkt); len(d) > 0 {
			return d, len(d)
		}
	}
	return mb.Data, mb.Size
}

// androidCodecDecode — C: android_codec_decode
// (android_video_codec.c:464-505) — synchronous (pre-Lollipop) path.
func androidCodecDecode(mc *mediacore.MediaCodec, vdI any,
	mq *mediacore.MediaQueue, mb *mediacore.MediaBuf, reqsize int) {
	avc, _ := mc.Opaque.(*androidVideoCodec)
	vd, _ := vdI.(*decoder.VideoDecoder)
	if avc == nil || vd == nil {
		return
	}
	// JNIEnv is per-OS-thread — pin the goroutine so every C call below
	// runs on the thread jniEnv() attached (art crashes when an env is
	// used from a different thread).
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	env := jniEnv()
	if env == nil {
		return
	}
	C.ml_push_frame(env, 64)
	defer C.ml_pop_frame(env)

	data, size := mbData(mb)

	if avc.bsf != nil {
		if converted, ok := bsfFilter(avc, data, size, mb.Flags.Keyframe); ok {
			data = converted
			size = len(converted)
		}
	}

	pts := storeMetadata(vd, mb, avc, mc, data, size)
	timeout := int64(0)
	flags := 0
	if mb.Flags.Keyframe {
		flags = 1 // BUFFER_FLAG_KEY_FRAME
	}
	for {
		idx := avcEnq(env, avc, data, size, pts, flags, timeout)
		if idx == -2 {
			break
		}
		if idx == -1 {
			getOutput(env, avc, timeout > 0, vd)
			timeout = 1000
			continue
		}
		break
	}
}

// androidCodecFlush — C: android_codec_flush
// (android_video_codec.c:513-519) — empty upstream.
func androidCodecFlush(mc *mediacore.MediaCodec, vdI any) {}

// androidCodecDecodeLocked — C: android_codec_decode_locked
// (android_video_codec.c:521-651) — async (Lollipop+) path, runs with
// mp_mutex held; unlocks around JNI like the C.
func androidCodecDecodeLocked(mc *mediacore.MediaCodec, vdI any,
	mq *mediacore.MediaQueue, mb *mediacore.MediaBuf) int {
	avc, _ := mc.Opaque.(*androidVideoCodec)
	vd, _ := vdI.(*decoder.VideoDecoder)
	if avc == nil || vd == nil {
		return 1
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	mp := vd.MP

	// C: drain one async output buffer first (android_video_codec.c:530-585)
	if len(avc.asyncOutputBuffers) != 0 {
		buf := avc.asyncOutputBuffers[0]
		avc.asyncOutputBuffers = avc.asyncOutputBuffers[1:]

		mp.Mutex.Unlock()

		env := jniEnv()
		if env == nil {
			mp.Mutex.Lock()
			return 1
		}
		C.ml_push_frame(env, 64)

		var fi mediacore.FrameInfo
		skip := fillFrameInfoFromPts(&fi, vd, avc, buf.pts)

		now := arch.GetAvtime()
		mp.ClockMutex.Lock()
		rtd := mp.RealtimeDelta + mp.AVDelta
		epoch := mp.AudioClockEpoch
		mp.ClockMutex.Unlock()

		wt := fi.PTS + rtd

		if epoch == int64(fi.Epoch) && (wt-now) > 10000 && skip == 0 {
			if avc.releaseOutputBufferTimed != nil {
				C.ml_call_release_timed(env, C.jobject(avc.decoder),
					avc.releaseOutputBufferTimed,
					C.jint(buf.id), C.jlong(wt*1000))
			} else {
				C.ml_call_release(env, C.jobject(avc.decoder),
					avc.releaseOutputBuffer, C.jint(buf.id), 1)
			}
			checkException(env, "releaseOutputBuffer")

			fi.UpdatePtsOnly = true
			fi.Type = mediacore.FourCCSURF // C: 'SURF'
			fi.DARNum = avc.width
			fi.DARDen = avc.height
			fi.Height = avc.height
			vd.DeliverFrame(&fi)
		} else {
			C.ml_call_release(env, C.jobject(avc.decoder),
				avc.releaseOutputBuffer, C.jint(buf.id), 0)
			checkException(env, "releaseOutputBuffer")
		}

		mp.Mutex.Lock()
		C.ml_pop_frame(env)
	}

	// C: then wait for an async input buffer (android_video_codec.c:588-651)
	if len(avc.asyncInputBuffers) == 0 {
		return 1
	}
	buf := avc.asyncInputBuffers[0]
	avc.asyncInputBuffers = avc.asyncInputBuffers[1:]

	mp.Mutex.Unlock()

	data, size := mbData(mb)

	if avc.bsf != nil {
		if converted, ok := bsfFilter(avc, data, size, mb.Flags.Keyframe); ok {
			data = converted
			size = len(converted)
		}
	}

	pts := storeMetadata(vd, mb, avc, mc, data, size)
	flags := 0
	if mb.Flags.Keyframe {
		flags = 1 // BUFFER_FLAG_KEY_FRAME
	}

	env := jniEnv()
	if env == nil {
		mp.Mutex.Lock()
		return 1
	}
	C.ml_push_frame(env, 64)

	bb := C.ml_call_obj1i(env, C.jobject(avc.decoder),
		avc.getInputBuffer, C.jint(buf.id))
	if checkException(env, "getInputBuffer") != 0 || bb == 0 {
		mp.Mutex.Lock()
		C.ml_pop_frame(env)
		return 0
	}
	bbBuf := C.ml_dba(env, bb)
	bbSize := int(C.ml_dbc(env, bb))
	if bbSize < size {
		lavTS.Debug("android", "Video packet buffer too small %d < %d",
			bbSize, size)
		mp.Mutex.Lock()
		C.ml_pop_frame(env)
		return 0
	}
	if size > 0 && len(data) > 0 { // C memcpy(0) is a no-op; &data[0] is not
		C.memcpy(bbBuf, unsafe.Pointer(&data[0]), C.size_t(size))
	}

	C.ml_call_void_queue(env, C.jobject(avc.decoder),
		avc.queueInputBuffer, C.jint(buf.id), 0, C.jint(size),
		C.jlong(pts), C.jint(flags))
	checkException(env, "queueInputBuffer")

	C.ml_pop_frame(env)
	mp.Mutex.Lock()
	return 0
}

// androidCodecClose — C: android_codec_close
// (android_video_codec.c:654-690).
func androidCodecClose(mc *mediacore.MediaCodec) {
	avc, _ := mc.Opaque.(*androidVideoCodec)
	if avc == nil {
		return
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	env := jniEnv()
	if env != nil {
		C.ml_push_frame(env, 64)

		mid := C.ml_get_method(env, C.jclass(avc.mediaCodecClass),
			C.CString("release"), C.CString("()V"))
		C.ml_call_void_queue(env, C.jobject(avc.decoder), mid, 0, 0, 0, 0, 0)
		checkException(env, "MediaCodec.release")

		C.ml_del_global(env, C.jobject(avc.bufferInfo))
		if avc.outputBuffers != nil {
			C.ml_del_global(env, C.jobject(avc.outputBuffers))
		}
		if avc.inputBuffers != nil {
			C.ml_del_global(env, C.jobject(avc.inputBuffers))
		}
		C.ml_del_global(env, C.jobject(avc.decoder))
		C.ml_del_global(env, C.jobject(avc.mediaCodecClass))

		C.ml_pop_frame(env)
	}

	if avc.codecInfo != nil {
		if pm := avc.codecInfo.Manager(); pm != nil {
			pm.RefDec(avc.codecInfo)
		}
	}
	avc.h264Parser.Fini()
	if avc.bsf != nil {
		avc.bsf.Free()
	}
}

// androidCodecCreate — C: android_codec_create
// (android_video_codec.c:693-951).
func androidCodecCreate(mc *mediacore.MediaCodec,
	mcp *mediacore.MediaCodecParams, mp *mediacore.MediaPipe) int {
	var mime, nicename string

	// C: if(!video_settings.video_accel) return 1;
	if mp.Sys.VS.VideoAccel == 0 {
		lavTS.Debug("Video", "MediaCodec skipped: VideoAccel setting is 0")
		return 1
	}

	// C: codec_id carried raw AV_CODEC_ID_* values (media_codec_create
	// callers pass stream codec ids straight from libavcodec).
	switch mc.CodecID {
	case mediacore.CodecID(C.AV_CODEC_ID_H264):
		mime, nicename = "video/avc", "h264"
	case mediacore.CodecID(C.AV_CODEC_ID_HEVC):
		mime, nicename = "video/hevc", "h265"
	case mediacore.CodecID(C.AV_CODEC_ID_MPEG4):
		mime, nicename = "video/mp4v-es", "MPEG4"
	case mediacore.CodecID(C.AV_CODEC_ID_MPEG2VIDEO):
		mime, nicename = "video/mpeg2", "MPEG2"
	case mediacore.CodecID(C.AV_CODEC_ID_VP8):
		mime, nicename = "video/x-vnd.on2.vp8", "VP8"
	case mediacore.CodecID(C.AV_CODEC_ID_VP9):
		mime, nicename = "video/x-vnd.on2.vp9", "VP9"
	default:
		lavTS.Debug("Video", "MediaCodec skipped: codec_id=%d not in list",
			int(mc.CodecID))
		return 1
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	env := jniEnv()
	if env == nil {
		lavTS.Debug("Video", "MediaCodec skipped: no JNIEnv")
		return 1
	}

	class := C.ml_find_class(env, C.CString("android/media/MediaCodec"))
	if class == 0 {
		C.ml_exc_clear(env)
		lavTS.Debug("Video", "MediaCodec skipped: class not found")
		return 1
	}
	mid := C.ml_get_static_method(env, class,
		C.CString("createDecoderByType"),
		C.CString("(Ljava/lang/String;)Landroid/media/MediaCodec;"))

	jtype := C.ml_new_string(env, C.CString(mime))
	dec := C.ml_call_static_obj1s(env, class, mid, jtype)
	if checkException(env, "MediaCodec.createDecoderByType") != 0 ||
		dec == 0 {
		lavTS.Debug("Video", "MediaCodec skipped: no decoder for %s", mime)
		return 1
	}

	avc := &androidVideoCodec{mp: mp}
	if mp.VideoQueue != nil && mp.VideoQueue.PropCodec != nil {
		if pm := mp.VideoQueue.PropCodec.Manager(); pm != nil {
			avc.codecInfo = pm.RefInc(mp.VideoQueue.PropCodec)
		}
	}

	avc.decoder = unsafe.Pointer(C.ml_new_global(env, dec))
	avc.mediaCodecClass = unsafe.Pointer(C.ml_new_global(env,
		C.jobject(C.ml_get_obj_class(env, C.jobject(avc.decoder)))))
	avc.mime = mime
	avc.nicename = nicename

	if mcp != nil {
		avc.width = mcp.Width
		avc.height = mcp.Height
		if mc.FmtCtx != nil {
			switch mc.CodecID {
			case mediacore.CodecID(C.AV_CODEC_ID_H264):
				avc.bsf, _ = libav.NewBSF("h264_mp4toannexb",
					mc.FmtCtx.CPtr())
			case mediacore.CodecID(C.AV_CODEC_ID_HEVC):
				avc.bsf, _ = libav.NewBSF("hevc_mp4toannexb",
					mc.FmtCtx.CPtr())
			}
		}
	} else {
		avc.width = 1280
		avc.height = 720
	}

	// C: MediaCodec$BufferInfo instance (android_video_codec.c:785-789)
	bufferInfo := C.ml_find_class(env,
		C.CString("android/media/MediaCodec$BufferInfo"))
	if bufferInfo == 0 || checkException(env, "find BufferInfo") != 0 {
		lavTS.Debug("Video", "MediaCodec skipped: no BufferInfo class")
		return 1
	}
	mid = C.ml_get_method(env, bufferInfo, C.CString("<init>"),
		C.CString("()V"))
	bi := C.ml_new_object(env, bufferInfo, mid)
	avc.bufferInfo = unsafe.Pointer(C.ml_new_global(env, bi))

	mcCls := C.jclass(avc.mediaCodecClass)
	avc.dequeueInputBuffer = C.ml_get_method(env, mcCls,
		C.CString("dequeueInputBuffer"), C.CString("(J)I"))
	avc.getInputBuffer = C.ml_get_method(env, mcCls,
		C.CString("getInputBuffer"),
		C.CString("(I)Ljava/nio/ByteBuffer;"))
	avc.queueInputBuffer = C.ml_get_method(env, mcCls,
		C.CString("queueInputBuffer"), C.CString("(IIIJI)V"))
	avc.dequeueOutputBuffer = C.ml_get_method(env, mcCls,
		C.CString("dequeueOutputBuffer"),
		C.CString("(Landroid/media/MediaCodec$BufferInfo;J)I"))
	avc.releaseOutputBuffer = C.ml_get_method(env, mcCls,
		C.CString("releaseOutputBuffer"), C.CString("(IZ)V"))
	avc.releaseOutputBufferTimed = C.ml_get_method(env, mcCls,
		C.CString("releaseOutputBuffer"), C.CString("(IJ)V"))
	if C.ml_exc_occurred(env) != 0 {
		C.ml_exc_clear(env)
		avc.releaseOutputBufferTimed = nil
	}
	if avc.dequeueInputBuffer == nil || avc.getInputBuffer == nil ||
		avc.queueInputBuffer == nil || avc.dequeueOutputBuffer == nil ||
		avc.releaseOutputBuffer == nil {
		lavTS.Debug("Video",
			"MediaCodec skipped: missing codec methods")
		return 1
	}

	// C: async mode when setCallback exists (Android 5.0+)
	mid = C.ml_get_method(env, mcCls, C.CString("setCallback"),
		C.CString("(Landroid/media/MediaCodec$Callback;)V"))
	if C.ml_exc_occurred(env) != 0 {
		C.ml_exc_clear(env)
		mid = nil
	}
	if mid != nil {
		avc.async = true
		mc.DecodeLocked = androidCodecDecodeLocked

		avc.mutex = &mp.Mutex
		if mp.VideoQueue != nil {
			avc.cond = mp.VideoQueue.Avail
		}
		if avc.cond == nil {
			lavTS.Debug("Video",
				"MediaCodec: no video queue cond, using sync mode")
			avc.async = false
			mc.DecodeLocked = nil
			mc.Decode = androidCodecDecode
		}
	}
	if avc.async {
		avc.handle = cgo.NewHandle(avc)
		stcore := C.jclass(arch.STCore)
		mid = C.ml_get_static_method(env, stcore,
			C.CString("setVideoDecoderWrapper"),
			C.CString("(Landroid/media/MediaCodec;I)V"))
		C.ml_call_static_void(env, stcore, mid,
			C.jobject(avc.decoder), C.jint(avc.handle))
		// A pending Java exception on a JNI-attached (non-Java) thread
		// crashes ART in FindCatchBlock at the *next* JNI call — always
		// check+clear right here.
		if checkException(env, "setVideoDecoderWrapper") != 0 {
			lavTS.Debug("Video",
				"MediaCodec: setCallback failed, using sync mode")
			avc.async = false
			mc.DecodeLocked = nil
			mc.Decode = androidCodecDecode
		} else {
			lavTS.Debug("Video", "Accelerated codec in async mode")
		}
	}
	if mc.Decode == nil && mc.DecodeLocked == nil {
		mc.Decode = androidCodecDecode
	}

	avc.direct = 1
	mc.Opaque = avc
	mc.Close = androidCodecClose
	mc.Flush = androidCodecFlush

	// C: MediaFormat.createVideoFormat (android_video_codec.c:837-845)
	mediaFormat := C.ml_find_class(env,
		C.CString("android/media/MediaFormat"))
	if mediaFormat == 0 || checkException(env, "find MediaFormat") != 0 {
		lavTS.Debug("Video", "MediaCodec skipped: no MediaFormat class")
		return 1
	}
	mid = C.ml_get_static_method(env, mediaFormat,
		C.CString("createVideoFormat"),
		C.CString("(Ljava/lang/String;II)Landroid/media/MediaFormat;"))
	format := C.ml_call_static_obj(env, mediaFormat, mid,
		C.ml_new_string(env, C.CString(avc.mime)),
		C.jint(avc.width), C.jint(avc.height))
	if format == 0 || checkException(env, "createVideoFormat") != 0 {
		lavTS.Debug("Video", "MediaCodec skipped: createVideoFormat failed")
		return 1
	}

	// C: surface via mp->mp_set_video_codec('SURF', ...)
	//    (android_video_codec.c:856-867)
	surface := 0
	if avc.direct != 0 {
		var fi mediacore.FrameInfo
		fi.DARNum = avc.width
		fi.DARDen = avc.height
		fi.Height = avc.height
		if mp.SetVideoCodec != nil {
			surface = mp.SetVideoCodec(mediacore.FourCCSURF, mc,
				mp.VideoFrameOpaque, &fi)
		}
	}
	if surface <= 0 {
		// No SURF video engine (or it failed) — an invalid jobject in
		// configure is a JNI hard-error, not an exception.
		lavTS.Debug("Video",
			"MediaCodec skipped: no output surface (%d)", surface)
		return 1
	}

	// C: configure + start (android_video_codec.c:889-896).
	// NOTE: upstream calls setVideoDecoderWrapper once (callback
	// install). The port used to call it a second time here; the
	// duplicate codec.setCallback() throws IllegalStateException on
	// Android 8+ and flipping back to sync decode afterwards leaves the
	// Java-side async callback registered — every dequeue* then throws.
	// A single call (above) already handles the failure path.

	mid = C.ml_get_method(env, mcCls, C.CString("configure"),
		C.CString("(Landroid/media/MediaFormat;Landroid/view/Surface;Landroid/media/MediaCrypto;I)V"))
	if mid == nil || checkException(env, "configure mid") != 0 {
		lavTS.Debug("Video", "MediaCodec skipped: no configure method")
		return 1
	}
	C.ml_call_configure(env, C.jobject(avc.decoder), mid, format,
		C.jobject(unsafe.Pointer(uintptr(surface))))
	if checkException(env, "MediaCodec.configure") != 0 {
		lavTS.Debug("Video", "MediaCodec skipped: configure failed "+
			"(mime=%s %dx%d surface=%d)", avc.mime, avc.width, avc.height,
			surface)
		return 1
	}

	mid = C.ml_get_method(env, mcCls, C.CString("start"), C.CString("()V"))
	if mid == nil || checkException(env, "start mid") != 0 {
		lavTS.Debug("Video", "MediaCodec skipped: no start method")
		return 1
	}
	C.ml_call_obj0(env, C.jobject(avc.decoder), mid)
	if checkException(env, "MediaCodec.start") != 0 {
		lavTS.Debug("Video", "MediaCodec skipped: start failed")
		return 1
	}

	if !avc.async {
		// C: getInputBuffers + getOutputBuffers (sync mode only)
		mid = C.ml_get_method(env, mcCls, C.CString("getInputBuffers"),
			C.CString("()[Ljava/nio/ByteBuffer;"))
		obj := C.ml_call_obj0(env, C.jobject(avc.decoder), mid)
		checkException(env, "MediaCodec.getInputBuffers")
		avc.inputBuffers = unsafe.Pointer(C.ml_new_global(env, obj))
		avcGetOutputBuffers(env, avc)
	}

	if mc.CodecID == mediacore.CodecID(C.AV_CODEC_ID_H264) {
		avc.h264Parser.Setup(nil)
	}
	return 0
}

// androidCodecStart — C: android_codec_init
// (android_video_codec.c:953-957) — empty upstream.
func androidCodecStart() {}

func init() {
	// C: REGISTER_CODEC(android_codec_init, android_codec_create, 100)
	mediacore.MediaRegisterCodec(&mediacore.CodecDef{
		Start: androidCodecStart,
		Open:  androidCodecCreate,
		Prio:  100,
	})
}
