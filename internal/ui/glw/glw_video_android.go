//go:build android

package glw

// Canonical port of src/ui/glw/glw_video_android.c — the two video
// engines registered on android: 'YUVP' (software frames converted
// into an ANativeWindow buffer) and 'SURF' (MediaCodec renders
// directly onto the Java-side SurfaceView).
//
// Upstream links libyuv for I420ToARGB; the conversion here is a
// small fixed-point BT.601 implementation in the preamble to avoid
// vendoring another library — same input/output contract.

/*
#include <jni.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <pthread.h>
#include <android/native_window.h>
#include <android/native_window_jni.h>

#include "jni_exc.h"
#include "jni_dispatch.h"

static jmethodID ml_vw_method(JNIEnv *e, jclass c, const char *n, const char *s) {
  jmethodID m;
  ml_exc_setctx(n);
  m = (*e)->GetMethodID(e, c, n, s);
  if(m == NULL || (*e)->ExceptionCheck(e)) {
    ml_exc_report(e);
    return NULL;
  }
  return m;
}
static jobject   ml_vw_call_obj(JNIEnv *e, jobject o, jmethodID m) {
  jobject r;
  if(m == NULL)
    return NULL;
  r = (*e)->CallObjectMethod(e, o, m);
  ml_exc_report(e);
  return r;
}
static void      ml_vw_call_void1(JNIEnv *e, jobject o, jmethodID m, jobject a) {
  if(m == NULL)
    return;
  (*e)->CallVoidMethod(e, o, m, a);
  ml_exc_report(e);
}
static void      ml_vw_call_void4(JNIEnv *e, jobject o, jmethodID m,
                                  jint a, jint b, jint c2, jint d) {
  if(m == NULL)
    return;
  (*e)->CallVoidMethod(e, o, m, a, b, c2, d);
  ml_exc_report(e);
}
static void      ml_vw_call_void0(JNIEnv *e, jobject o, jmethodID m) {
  if(m == NULL)
    return;
  (*e)->CallVoidMethod(e, o, m);
  ml_exc_report(e);
}

// I420ToARGB — upstream calls libyuv (glw_video_android.c:238-244);
// fixed-point BT.601 equivalent producing BGRA-in-memory ARGB32.
static void ml_i420_to_argb(const uint8_t *y, int ys,
                            const uint8_t *u, int us,
                            const uint8_t *v, int vs,
                            uint8_t *dst, int ds,
                            int w, int h) {
  for(int j = 0; j < h; j++) {
    const uint8_t *yp = y + j * ys;
    const uint8_t *up = u + (j >> 1) * us;
    const uint8_t *vp = v + (j >> 1) * vs;
    uint8_t *d = dst + j * ds;
    for(int i = 0; i < w; i++) {
      int c = yp[i] - 16;
      if(c < 0) c = 0;
      int dd = up[i >> 1] - 128;
      int e = vp[i >> 1] - 128;
      int r = (298 * c + 409 * e + 128) >> 8;
      int g = (298 * c - 100 * dd - 208 * e + 128) >> 8;
      int b = (298 * c + 516 * dd + 128) >> 8;
      if(r < 0) r = 0; else if(r > 255) r = 255;
      if(g < 0) g = 0; else if(g > 255) g = 255;
      if(b < 0) b = 0; else if(b > 255) b = 255;
      d[i*4+0] = (uint8_t)b;
      d[i*4+1] = (uint8_t)g;
      d[i*4+2] = (uint8_t)r;
      d[i*4+3] = 0xff;
    }
  }
}

// --- dispatcher jobs -------------------------------------------------------
// Every Java-side call on the VideoRenderer runs on the dispatcher
// thread (jni_dispatch.c): upstream issues them from a single pthread
// and the renderer's ReentrantLock needs one lock/unlock owner; it
// also keeps JNI calls on a stack ART measured correctly.

typedef struct { jobject vrp; jobject vr; jclass cls; } ml_vw_create_args;

static void *
ml_vw_create_job(JNIEnv *env, void *p)
{
  ml_vw_create_args *a = p;
  jclass cls = (*env)->GetObjectClass(env, a->vrp);
  jmethodID mid = ml_vw_method(env, cls, "createVideoRenderer",
      "()Lcom/moviango/mediaplayer/VideoRenderer;");
  jobject vr = ml_vw_call_obj(env, a->vrp, mid);
  a->vr = vr == NULL ? NULL : (*env)->NewGlobalRef(env, vr);
  jclass vrc = a->vr == NULL ? NULL : (*env)->GetObjectClass(env, a->vr);
  a->cls = vrc == NULL ? NULL : (jclass)(*env)->NewGlobalRef(env, vrc);
  return NULL;
}

static void ml_jd_create(ml_vw_create_args *a) { ml_jd_call(ml_vw_create_job, a); }

typedef struct { jobject vrp; jobject vr; jclass cls; } ml_vw_destroy_args;

static void *
ml_vw_destroy_job(JNIEnv *env, void *p)
{
  ml_vw_destroy_args *a = p;
  jclass cls = (*env)->GetObjectClass(env, a->vrp);
  jmethodID mid = ml_vw_method(env, cls, "destroyVideoRenderer",
      "(Lcom/moviango/mediaplayer/VideoRenderer;)V");
  ml_vw_call_void1(env, a->vrp, mid, a->vr);
  (*env)->DeleteGlobalRef(env, a->vr);
  (*env)->DeleteGlobalRef(env, a->cls);
  return NULL;
}

static void ml_jd_destroy(ml_vw_destroy_args *a) { ml_jd_call(ml_vw_destroy_job, a); }

typedef struct { jobject vr; jint x, y, w, h; } ml_vw_pos_args;

static void *
ml_vw_pos_job(JNIEnv *env, void *p)
{
  ml_vw_pos_args *a = p;
  jclass cls = (*env)->GetObjectClass(env, a->vr);
  jmethodID mid = ml_vw_method(env, cls, "setPosition", "(IIII)V");
  ml_vw_call_void4(env, a->vr, mid, a->x, a->y, a->w, a->h);
  return NULL;
}

static void ml_jd_setpos(ml_vw_pos_args *a) { ml_jd_call(ml_vw_pos_job, a); }

typedef struct {
  jobject vr;
  jclass  cls;
  const uint8_t *y, *u, *v;
  int ys, us, vs;
  int w, h;
} ml_vw_frame_args;

static void *ml_vw_frame_job(JNIEnv *, void *);

// The whole per-frame surface transaction runs as one job: getSurface
// takes the Java lock, the frame is posted, releaseSurface unlocks —
// lock/unlock always balanced on the dispatcher thread.
static void *
ml_vw_frame_job(JNIEnv *env, void *p)
{
  ml_vw_frame_args *a = p;
  ANativeWindow *anw;
  ANativeWindow_Buffer buf;
  jmethodID mid;
  jobject surface;

  (*env)->PushLocalFrame(env, 64);
  mid = ml_vw_method(env, a->cls, "getSurface", "()Landroid/view/Surface;");
  surface = ml_vw_call_obj(env, a->vr, mid);
  if(surface != NULL) {
    anw = ANativeWindow_fromSurface(env, surface);
    ANativeWindow_setBuffersGeometry(anw, a->w, a->h,
                                     WINDOW_FORMAT_RGBA_8888);
    ANativeWindow_lock(anw, &buf, NULL);
    if(a->y != NULL && a->u != NULL && a->v != NULL)
      ml_i420_to_argb(a->y, a->ys, a->u, a->us, a->v, a->vs,
                      buf.bits, buf.stride * 4, a->w, a->h);
    ANativeWindow_unlockAndPost(anw);
    ANativeWindow_release(anw);
    (*env)->DeleteLocalRef(env, surface);
  }
  mid = ml_vw_method(env, a->cls, "releaseSurface", "()V");
  ml_vw_call_void0(env, a->vr, mid);
  (*env)->PopLocalFrame(env, NULL);
  return NULL;
}

// The y/u/v planes point into Go slice memory.  Passing them as cgo
// arguments is allowed (they reference pointer-free memory); storing
// them inside the Go-allocated args struct is not — the runtime check
// panics on nested Go pointers.  So they arrive as arguments and are
// filled in here, after the checker has inspected &a.
static void ml_jd_post_frame(ml_vw_frame_args *a,
                             const uint8_t *y, const uint8_t *u,
                             const uint8_t *v,
                             int ys, int us, int vs) {
  a->y = y;
  a->u = u;
  a->v = v;
  a->ys = ys;
  a->us = us;
  a->vs = vs;
  ml_jd_call(ml_vw_frame_job, a);
}

static void *
ml_vw_surf_job(JNIEnv *env, void *p)
{
  jobject vr = p;
  jclass cls = (*env)->GetObjectClass(env, vr);
  jmethodID mid = ml_vw_method(env, cls, "getSurfaceUnlocked",
                               "()Landroid/view/Surface;");
  jobject s = ml_vw_call_obj(env, vr, mid);
  return s == NULL ? NULL : (*env)->NewGlobalRef(env, s);
}

static jobject ml_jd_get_surf(jobject vr) { return ml_jd_call(ml_vw_surf_job, vr); }
*/
import "C"

import (
	"runtime"
	"time"
	"unsafe"

	"github.com/czz/movian-go/internal/arch"
	mediacore "github.com/czz/movian-go/internal/media/core"
	decoder "github.com/czz/movian-go/internal/video/decoder"
)

// androidVideo — C: android_video_t (glw_video_android.c:34-42).
type androidVideo struct {
	videoRenderer      unsafe.Pointer // C: av_VideoRenderer (jobject)
	videoRendererClass unsafe.Pointer // C: av_VideoRendererClass (jclass)

	pts int64 // C: av_pts

	displayRect glwRect // C: av_display_rect
}

// vwEnv — C: (*JVM)->GetEnv(JVM, &env, JNI_VERSION_1_6).
func vwEnv() *C.JNIEnv { return (*C.JNIEnv)(unsafe.Pointer(arch.JNIEnv())) }

// jniExcDrain — dedicated attached thread that describes stolen
// throwables; its stack is clean so printStackTrace has headroom
// even when the pending exception was StackOverflowError.
func jniExcDrain() {
	runtime.LockOSThread()
	for {
		var ctx *C.char
		t := C.ml_exc_take(&ctx)
		if t == 0 {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		if env := vwEnv(); env != nil {
			C.ml_exc_describe(env, t, ctx)
		}
	}
}

func init() { go jniExcDrain() }

// agrOf — C: (android_glw_root_t *)gv->w.glw_root
// (glw_video_android.c:46,120). glwRoot is embedded first in both
// structs, so the pointer is directly convertible.
func agrOf(gv *GlwVideo) *androidGlwRoot {
	return (*androidGlwRoot)(unsafe.Pointer(gv.w.glwRoot))
}

// androidStart — C: android_init (glw_video_android.c:44-65).
func androidStart(gv *GlwVideo) int {
	agr := agrOf(gv)
	if C.ml_jd_start(unsafe.Pointer(arch.JVM)) != 0 {
		return -1
	}

	var a C.ml_vw_create_args
	a.vrp = C.jobject(agr.vrp)
	C.ml_jd_create(&a)

	av := &androidVideo{pts: mediacore.PTSUnset}
	gv.gvAux = unsafe.Pointer(av)

	av.videoRenderer = unsafe.Pointer(a.vr)
	av.videoRendererClass = unsafe.Pointer(a.cls)
	return 0
}

// androidNewframe — C: android_newframe (glw_video_android.c:71-76).
func androidNewframe(gv *GlwVideo, vd *decoder.VideoDecoder, flags int) int64 {
	av := (*androidVideo)(gv.gvAux)
	return av.pts
}

// androidRender — C: android_render (glw_video_android.c:81-108):
// pushes the video rect to the Java VideoRenderer when it moves.
func androidRender(gv *GlwVideo, rc *glwRctx) {
	av := (*androidVideo)(gv.gvAux)

	if gv.gvDarNum != 0 && gv.gvDarDen != 0 {
		glwStencilQuad(gv.w.glwRoot, rc)
	}

	if av.displayRect == gv.gvRect {
		return
	}
	av.displayRect = gv.gvRect

	var a C.ml_vw_pos_args
	a.vr = C.jobject(av.videoRenderer)
	a.x = C.jint(gv.gvRect.x1)
	a.y = C.jint(gv.gvRect.y1)
	a.w = C.jint(gv.gvRect.x2 - gv.gvRect.x1)
	a.h = C.jint(gv.gvRect.y2 - gv.gvRect.y1)
	C.ml_jd_setpos(&a)
}

// androidReset — C: android_reset (glw_video_android.c:114-135).
func androidReset(gv *GlwVideo) {
	agr := agrOf(gv)
	av := (*androidVideo)(gv.gvAux)

	var a C.ml_vw_destroy_args
	a.vrp = C.jobject(agr.vrp)
	a.vr = C.jobject(av.videoRenderer)
	a.cls = C.jclass(av.videoRendererClass)
	C.ml_jd_destroy(&a)
}

// androidYuvpDeliver — C: android_yuvp_deliver
// (glw_video_android.c:140-218): waits on the audio clock, converts
// the YUV frame into the ANativeWindow buffer, posts it.
func androidYuvpDeliver(fi *mediacore.FrameInfo, gv *GlwVideo,
	gve *glwVideoEngine) int {
	if C.ml_jd_start(unsafe.Pointer(arch.JVM)) != 0 {
		return -1
	}
	if glwVideoConfigure(gv, gve) != 0 {
		return -1
	}
	av := (*androidVideo)(gv.gvAux)
	mp := gv.gvMp

	var aclock, pts int64
	var aEpoch int64

	for { // C: recheck:
		if gv.w.glwFlags&glwDestroying != 0 {
			return -1
		}
		now := arch.GetAvtime()
		mp.ClockMutex.Lock()
		aclock = mp.AudioClock + now - mp.AudioClockAvtime + mp.AVDelta
		aEpoch = mp.AudioClockEpoch
		mp.ClockMutex.Unlock()

		pts = fi.PTS
		d := pts - aclock
		delta := int64(0)

		if pts == mediacore.PTSUnset || d < -5000000 || d > 5000000 {
			pts = gv.gvNextpts
		}
		if pts != mediacore.PTSUnset && (pts-delta) >= aclock &&
			aEpoch == int64(fi.Epoch) {
			waittime := (pts - delta) - aclock
			if waittime > 100000 {
				waittime = 100000
			}
			gv.gvSurfaceMutex.Unlock()
			time.Sleep(time.Duration(waittime) * time.Microsecond)
			gv.gvSurfaceMutex.Lock()
			continue
		}
		break
	}

	av.pts = pts

	var a C.ml_vw_frame_args
	a.vr = C.jobject(av.videoRenderer)
	a.cls = C.jclass(av.videoRendererClass)
	a.w = C.int(fi.Width)
	a.h = C.int(fi.Height)
	var y, u, v *C.uint8_t
	var ys, us, vs C.int
	if len(fi.Data[0]) > 0 {
		y = (*C.uint8_t)(unsafe.Pointer(&fi.Data[0][0]))
		ys = C.int(fi.Pitch[0])
	}
	if len(fi.Data[2]) > 0 {
		u = (*C.uint8_t)(unsafe.Pointer(&fi.Data[2][0]))
		us = C.int(fi.Pitch[2])
	}
	if len(fi.Data[1]) > 0 {
		v = (*C.uint8_t)(unsafe.Pointer(&fi.Data[1][0]))
		vs = C.int(fi.Pitch[1])
	}
	// C: hts_mutex_unlock(&gv->gv_surface_mutex) around the blocking
	// ANativeWindow_lock (glw_video_android.c:224-226); here the whole
	// posted job contains it — dequeueBuffer may wait for the consumer
	// and must not hold the GL renderer out of the mutex.
	gv.gvSurfaceMutex.Unlock()
	C.ml_jd_post_frame(&a, y, u, v, ys, us, vs)
	gv.gvSurfaceMutex.Lock()
	return 0
}

// glwAndroidVideoYuvp — C: glw_android_video_yuvp
// (glw_video_android.c:225-232).
var glwAndroidVideoYuvp = glwVideoEngine{
	gveType:     mediacore.FourCCYUVP, // C: 'YUVP'
	gveNewframe: androidNewframe,
	gveRender:   androidRender,
	gveReset:    androidReset,
	gveStart:    androidStart,
	gveDeliver:  androidYuvpDeliver,
}

// surfaceSetCodec — C: surface_set_codec
// (glw_video_android.c:285-317): polls getSurfaceUnlocked until the
// Java side hands back a Surface, returns it as int (C truncates the
// jobject to int — canonical behaviour kept via unsafe.Pointer).
func surfaceSetCodec(mc *mediacore.MediaCodec, gv *GlwVideo,
	fi *mediacore.FrameInfo, gve *glwVideoEngine) int {
	if C.ml_jd_start(unsafe.Pointer(arch.JVM)) != 0 {
		return 0
	}
	if glwVideoConfigure(gv, gve) != 0 {
		return 0
	}
	av := (*androidVideo)(gv.gvAux)

	for {
		surface := C.ml_jd_get_surf(C.jobject(av.videoRenderer))
		if surface != 0 {
			return int(uintptr(unsafe.Pointer(surface)))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// surfaceDeliver — C: surface_deliver (glw_video_android.c:320-382):
// MediaCodec owns the actual presentation; this only tracks the PTS.
func surfaceDeliver(fi *mediacore.FrameInfo, gv *GlwVideo,
	gve *glwVideoEngine) int {
	if glwVideoConfigure(gv, gve) != 0 {
		return -1
	}
	av := (*androidVideo)(gv.gvAux)
	mp := gv.gvMp
	if fi.UpdatePtsOnly {
		av.pts = fi.PTS
		return 0
	}

	var aclock int64
	var aEpoch int64

	for { // C: recheck:
		if gv.w.glwFlags&glwDestroying != 0 {
			return -1
		}
		now := arch.GetAvtime()
		mp.ClockMutex.Lock()
		aclock = mp.AudioClock + now - mp.AudioClockAvtime + mp.AVDelta
		aEpoch = mp.AudioClockEpoch
		mp.ClockMutex.Unlock()

		pts := fi.PTS
		delta := int64(0)
		d := pts - aclock
		if (pts == mediacore.PTSUnset || d < -5000000 || d > 5000000) &&
			gv.gvNextpts != mediacore.PTSUnset {
			pts = gv.gvNextpts
		}
		if pts != mediacore.PTSUnset && (pts-delta) >= aclock &&
			aEpoch == int64(fi.Epoch) {
			waittime := (pts - delta) - aclock
			if waittime > 100000 {
				waittime = 100000
			}
			gv.gvSurfaceMutex.Unlock()
			time.Sleep(time.Duration(waittime) * time.Microsecond)
			gv.gvSurfaceMutex.Lock()
			continue
		}

		if pts != mediacore.PTSUnset && fi.Duration > 0 {
			gv.gvNextpts = pts + fi.Duration
			gv.gvNextptsEpoch = fi.Epoch
		}
		av.pts = pts
		return 0
	}
}

// glwAndroidVideoSurface — C: glw_android_video_surface
// (glw_video_android.c:385-396).
var glwAndroidVideoSurface = glwVideoEngine{
	gveType:     mediacore.FourCCSURF, // C: 'SURF'
	gveNewframe: androidNewframe,
	gveRender:   androidRender,
	gveReset:    androidReset,
	gveStart:    androidStart,
	gveSetCodec: surfaceSetCodec,
	gveDeliver:  surfaceDeliver,
}

func init() {
	// C: GLW_REGISTER_GVE (glw_video_common.h)
	glwRegisterVideoEngine(&glwAndroidVideoYuvp)
	glwRegisterVideoEngine(&glwAndroidVideoSurface)
}
