//go:build android

package arch

// Canonical port of the JNI plumbing shared by src/arch/android/*.c.
// The C code calls (*env)->Method(env, ...) directly; in Go every JNI
// call goes through a static C wrapper in this preamble.
//
// contract: JNI boundary. JNIEnv via JNIEnv() is thread-local —
// never cache across calls; global refs via ml_jni_new_global_ref
// must be paired with a delete (the Android-16 crash was a leaked
// Surface ref); ml_jni_* helpers are the only permitted way to touch
// JVM state from Go.

/*
#cgo LDFLAGS: -landroid -llog
#include <jni.h>
#include <stdlib.h>
#include <string.h>
#include <android/log.h>
#include <sys/system_properties.h>

#include "jni_exc.h"

// ---- trace ------------------------------------------------------------

static void
ml_log_print(int prio, const char *tag, const char *msg)
{
  __android_log_print(prio, tag, "%s", msg);
}

static int
ml_sysprop_get(const char *name, char *out, int outlen)
{
  return __system_property_get(name, out);
}

// ---- VM / env ----------------------------------------------------------

static JNIEnv *
ml_jni_getenv(JavaVM *vm)
{
  JNIEnv *env = NULL;
  if((*vm)->GetEnv(vm, (void **)&env, JNI_VERSION_1_6) != JNI_OK)
    return NULL;
  return env;
}

static JNIEnv *
ml_jni_attach(JavaVM *vm)
{
  JNIEnv *env = NULL;
  if((*vm)->AttachCurrentThread(vm, &env, NULL) != JNI_OK)
    return NULL;
  return env;
}

static void
ml_jni_detach(JavaVM *vm)
{
  (*vm)->DetachCurrentThread(vm);
}

// ---- class / method / field lookup -------------------------------------

static jclass
ml_jni_find_class(JNIEnv *env, const char *name)
{
  jclass r = (*env)->FindClass(env, name);
  ml_aexc_report(env);
  return r;
}

static jclass
ml_jni_get_object_class(JNIEnv *env, jobject obj)
{
  return (*env)->GetObjectClass(env, obj);
}

static jmethodID
ml_jni_get_method(JNIEnv *env, jclass cls, const char *name, const char *sig)
{
  ml_aexc_setctx(name);
  return (*env)->GetMethodID(env, cls, name, sig);
}

static jmethodID
ml_jni_get_static_method(JNIEnv *env, jclass cls, const char *name,
                         const char *sig)
{
  jmethodID r;
  ml_aexc_setctx(name);
  r = (*env)->GetStaticMethodID(env, cls, name, sig);
  ml_aexc_report(env);
  return r;
}

static jfieldID
ml_jni_get_field(JNIEnv *env, jclass cls, const char *name, const char *sig)
{
  return (*env)->GetFieldID(env, cls, name, sig);
}

// ---- refs ----------------------------------------------------------------

static jobject
ml_jni_new_global_ref(JNIEnv *env, jobject obj)
{
  return (*env)->NewGlobalRef(env, obj);
}

static void
ml_jni_del_global_ref(JNIEnv *env, jobject obj)
{
  (*env)->DeleteGlobalRef(env, obj);
}

static void
ml_jni_del_local_ref(JNIEnv *env, jobject obj)
{
  (*env)->DeleteLocalRef(env, obj);
}

// ---- strings ---------------------------------------------------------------

static jstring
ml_jni_new_string(JNIEnv *env, const char *s)
{
  return (*env)->NewStringUTF(env, s);
}

static const char *
ml_jni_get_string_chars(JNIEnv *env, jstring s)
{
  return (*env)->GetStringUTFChars(env, s, NULL);
}

static void
ml_jni_release_string_chars(JNIEnv *env, jstring s, const char *chars)
{
  (*env)->ReleaseStringUTFChars(env, s, chars);
}

// ---- calls -------------------------------------------------------------------

static void
ml_jni_exc_report(JNIEnv *env)
{
  ml_aexc_report(env);
}

static void
ml_jni_call_void(JNIEnv *env, jobject obj, jmethodID mid)
{
  (*env)->CallVoidMethod(env, obj, mid);
  ml_jni_exc_report(env);
}

static void
ml_jni_call_void_obj(JNIEnv *env, jobject obj, jmethodID mid, jobject a0)
{
  (*env)->CallVoidMethod(env, obj, mid, a0);
  ml_jni_exc_report(env);
}

static void
ml_jni_call_void_iiii(JNIEnv *env, jobject obj, jmethodID mid,
                      jint a0, jint a1, jint a2, jint a3)
{
  (*env)->CallVoidMethod(env, obj, mid, a0, a1, a2, a3);
  ml_jni_exc_report(env);
}

static void
ml_jni_call_void_queue(JNIEnv *env, jobject obj, jmethodID mid,
                       jint idx, jint off, jint size, jlong pts, jint flags)
{
  (*env)->CallVoidMethod(env, obj, mid, idx, off, size, pts, flags);
  ml_jni_exc_report(env);
}

static void
ml_jni_call_void_release(JNIEnv *env, jobject obj, jmethodID mid,
                         jint idx, jboolean render)
{
  (*env)->CallVoidMethod(env, obj, mid, idx, render);
  ml_jni_exc_report(env);
}

static void
ml_jni_call_void_release_timed(JNIEnv *env, jobject obj, jmethodID mid,
                               jint idx, jlong ts)
{
  (*env)->CallVoidMethod(env, obj, mid, idx, ts);
  ml_jni_exc_report(env);
}

static jint
ml_jni_call_int(JNIEnv *env, jobject obj, jmethodID mid, jlong a0)
{
  jint r = (*env)->CallIntMethod(env, obj, mid, a0);
  ml_jni_exc_report(env);
  return r;
}

static jint
ml_jni_call_int_obj(JNIEnv *env, jobject obj, jmethodID mid,
                    jobject a0, jlong a1)
{
  jint r = (*env)->CallIntMethod(env, obj, mid, a0, a1);
  ml_jni_exc_report(env);
  return r;
}

static jint
ml_jni_call_int_str(JNIEnv *env, jobject obj, jmethodID mid, jstring s)
{
  jint r = (*env)->CallIntMethod(env, obj, mid, s);
  ml_jni_exc_report(env);
  return r;
}

static jobject
ml_jni_call_obj(JNIEnv *env, jobject obj, jmethodID mid)
{
  jobject r = (*env)->CallObjectMethod(env, obj, mid);
  ml_jni_exc_report(env);
  return r;
}

static jobject
ml_jni_call_obj_int(JNIEnv *env, jobject obj, jmethodID mid, jint a0)
{
  jobject r = (*env)->CallObjectMethod(env, obj, mid, a0);
  ml_jni_exc_report(env);
  return r;
}

static jboolean
ml_jni_call_static_bool_str(JNIEnv *env, jclass cls, jmethodID mid, jstring s)
{
  jboolean r = (*env)->CallStaticBooleanMethod(env, cls, mid, s);
  ml_jni_exc_report(env);
  return r;
}

static jobject
ml_jni_call_static_obj_ii(JNIEnv *env, jclass cls, jmethodID mid,
                          jint a0, jint a1)
{
  jobject r = (*env)->CallStaticObjectMethod(env, cls, mid, a0, a1);
  ml_jni_exc_report(env);
  return r;
}

static jobject
ml_jni_call_static_obj_str_ii(JNIEnv *env, jclass cls, jmethodID mid,
                              jstring s, jint a0, jint a1)
{
  jobject r = (*env)->CallStaticObjectMethod(env, cls, mid, s, a0, a1);
  ml_jni_exc_report(env);
  return r;
}

static jobject
ml_jni_call_static_obj0(JNIEnv *env, jclass cls, jmethodID mid)
{
  jobject r = (*env)->CallStaticObjectMethod(env, cls, mid);
  ml_jni_exc_report(env);
  return r;
}

static jint
ml_jni_call_static_int0(JNIEnv *env, jclass cls, jmethodID mid)
{
  jint r = (*env)->CallStaticIntMethod(env, cls, mid);
  ml_jni_exc_report(env);
  return r;
}

static jobject
ml_jni_call_static_obj_long(JNIEnv *env, jclass cls, jmethodID mid,
                            jlong a0)
{
  jobject r = (*env)->CallStaticObjectMethod(env, cls, mid, a0);
  ml_jni_exc_report(env);
  return r;
}

static void
ml_jni_call_static_void_str_long(JNIEnv *env, jclass cls, jmethodID mid,
                                 jstring s, jlong v)
{
  (*env)->CallStaticVoidMethod(env, cls, mid, s, v);
  ml_jni_exc_report(env);
}

static void
ml_jni_call_static_void_str_str(JNIEnv *env, jclass cls, jmethodID mid,
                                jstring a0, jstring a1)
{
  (*env)->CallStaticVoidMethod(env, cls, mid, a0, a1);
  ml_jni_exc_report(env);
}

static void
ml_jni_call_static_void0(JNIEnv *env, jclass cls, jmethodID mid)
{
  (*env)->CallStaticVoidMethod(env, cls, mid);
  ml_jni_exc_report(env);
}

static void
ml_jni_call_static_void_int(JNIEnv *env, jclass cls, jmethodID mid,
                            jint a0)
{
  (*env)->CallStaticVoidMethod(env, cls, mid, a0);
  ml_jni_exc_report(env);
}

static void
ml_jni_call_static_void_iiii(JNIEnv *env, jclass cls, jmethodID mid,
                             jint a0, jint a1, jint a2, jint a3)
{
  (*env)->CallStaticVoidMethod(env, cls, mid, a0, a1, a2, a3);
  ml_jni_exc_report(env);
}

static void
ml_jni_call_static_void_long(JNIEnv *env, jclass cls, jmethodID mid,
                             jlong a0)
{
  (*env)->CallStaticVoidMethod(env, cls, mid, a0);
  ml_jni_exc_report(env);
}

static void
ml_jni_call_static_void_int_str(JNIEnv *env, jclass cls, jmethodID mid,
                                jint a0, jstring a1)
{
  (*env)->CallStaticVoidMethod(env, cls, mid, a0, a1);
  ml_jni_exc_report(env);
}

static void
ml_jni_call_static_void_obj_int(JNIEnv *env, jclass cls, jmethodID mid,
                                jobject a0, jint a1)
{
  (*env)->CallStaticVoidMethod(env, cls, mid, a0, a1);
  ml_jni_exc_report(env);
}

static jobject
ml_jni_new_object(JNIEnv *env, jclass cls, jmethodID mid)
{
  return (*env)->NewObject(env, cls, mid);
}

// ---- arrays / buffers / fields ------------------------------------------------

static jobject
ml_jni_get_obj_array_elem(JNIEnv *env, jobjectArray arr, jint idx)
{
  return (*env)->GetObjectArrayElement(env, arr, idx);
}

static void *
ml_jni_direct_addr(JNIEnv *env, jobject buf)
{
  return (*env)->GetDirectBufferAddress(env, buf);
}

static jlong
ml_jni_direct_cap(JNIEnv *env, jobject buf)
{
  return (*env)->GetDirectBufferCapacity(env, buf);
}

static jlong
ml_jni_get_long_field(JNIEnv *env, jobject obj, jfieldID fid)
{
  return (*env)->GetLongField(env, obj, fid);
}

// ---- local frames / exceptions --------------------------------------------------

static jint
ml_jni_push_local(JNIEnv *env, jint cap)
{
  return (*env)->PushLocalFrame(env, cap);
}

static void
ml_jni_pop_local(JNIEnv *env)
{
  (*env)->PopLocalFrame(env, NULL);
}

static int
ml_jni_exc_check(JNIEnv *env)
{
  if((*env)->ExceptionOccurred(env)) {
    (*env)->ExceptionClear(env);
    return 1;
  }
  return 0;
}

*/
import "C"

import (
	"runtime"
	"time"
	"unsafe"
)

// JVM — C: JavaVM *JVM (android.c:55). Set by JNI_OnLoad.
var JVM *C.JavaVM

func init() { go jniExcDrain() }

// jniExcDrain — dedicated attached thread that describes stolen
// throwables (jni_exc.c) on a clean stack; describing in place
// faults when the pending exception is StackOverflowError because
// printStackTrace itself needs stack headroom.
func jniExcDrain() {
	runtime.LockOSThread()
	for {
		var ctx *C.char
		t := C.ml_aexc_take(&ctx)
		if t == 0 {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		if env := JNIEnv(); env != nil {
			C.ml_aexc_describe(env, t, ctx)
		}
	}
}

// STCore — C: jclass STCore (android.c:56): global ref to
// com/moviango/mediaplayer/Core. Set by Core.coreInit. Stored as
// unsafe.Pointer so callers from other cgo packages can convert.
var STCore unsafe.Pointer

// STApps — global ref to com/moviango/mediaplayer/Apps. Set by
// Core.coreInit alongside STCore: FindClass works there because it
// runs on a Java thread; a later FindClass from a Go thread would use
// the bootstrap classloader and fail with NoClassDefFoundError.
var STApps unsafe.Pointer

// STTv — global ref to com/moviango/mediaplayer/Tv (TIF bridge).
// Cached in coreInit for the same FindClass reason as STApps.
var STTv unsafe.Pointer

// AndroidNav — C: prop_t *android_nav (android.c:57). The single
// navigator root on android — set by coreInit via nav_spawn.
var AndroidNav unsafe.Pointer

// AndroidSDK — C: int android_sdk (android.c:58): ro.build.version.sdk.
var AndroidSDK int

// AndroidIntent — C: char android_intent[PATH_MAX] (android.c:53).
var AndroidIntent string

// AndroidFS paths — C: android_fs_settings_path/cache_path/sdcard_path
// (android_fs.c:40-42). Set by coreInit.
var (
	AndroidFSSettingsPath string
	AndroidFSCachePath    string
	AndroidFSSdcardPath   string
)

// Audio params — C: android_system_audio_sample_rate /
// android_system_audio_frames_per_buffer (android_audio.c:79-80).
var (
	AndroidSystemAudioSampleRate      int
	AndroidSystemAudioFramesPerBuffer int
)

// AndroidGetPermissionFn — C: android_get_permission
// (android_glw.c:52). The state machine lives in the GLW android
// frontend (permission_glw_root); pkg/fileaccess reaches it through
// this seam since arch is the lowest layer.
var androidGetPermissionFn func(permission string, interactive bool) int

// SetAndroidGetPermission wires the permission-check bridge
// (C: android_get_permission — provider: UI layer).
func SetAndroidGetPermission(fn func(permission string, interactive bool) int) {
	androidGetPermissionFn = fn
}

// AndroidGetPermission — C: android_get_permission (android_glw.c:52).
func AndroidGetPermission(permission string, interactive bool) int {
	if androidGetPermissionFn == nil {
		return 0
	}
	return androidGetPermissionFn(permission, interactive)
}

// TraceArch — C: trace_arch (android.c:63-75): __android_log_print.
func TraceArch(level int, prefix, str string) {
	var prio C.int
	switch level {
	case 0: // TRACE_EMERG
		prio = C.ANDROID_LOG_FATAL
	case 1: // TRACE_ERROR
		prio = C.ANDROID_LOG_ERROR
	case 2: // TRACE_INFO
		prio = C.ANDROID_LOG_INFO
	case 3: // TRACE_DEBUG
		prio = C.ANDROID_LOG_DEBUG
	default:
		prio = C.ANDROID_LOG_ERROR
	}
	tag := C.CString("Movian Go")
	msg := C.CString(prefix + " " + str)
	C.ml_log_print(prio, tag, msg)
	C.free(unsafe.Pointer(tag))
	C.free(unsafe.Pointer(msg))
}

// syspropGet — C: __system_property_get (android.c:231-237).
func syspropGet(name string) string {
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	buf := make([]byte, C.PROP_VALUE_MAX)
	n := C.ml_sysprop_get(cname, (*C.char)(unsafe.Pointer(&buf[0])),
		C.int(len(buf)))
	if n <= 0 {
		return ""
	}
	return string(buf[:n])
}

// JNIOnLoad — C: JNI_OnLoad (android.c:225-245). Called by the
// //export in cmd/movian-go; returns JNI_VERSION_1_6 and fills the
// device info globals.
func JNIOnLoad(vm unsafe.Pointer) int {
	JVM = (*C.JavaVM)(vm)
	AndroidManufacturer = syspropGet("ro.product.manufacturer")
	AndroidModel = syspropGet("ro.product.model")
	AndroidName = syspropGet("ro.product.name")
	AndroidVersion = syspropGet("ro.build.version.release")
	AndroidSerialno = syspropGet("ro.serialno")
	AndroidSDK = atoiOrZero(syspropGet("ro.build.version.sdk"))
	// Android never exports TZ to native code, so Go's time.Local
	// stays UTC — persist.sys.timezone carries the Java zone id
	// (e.g. "Europe/Rome"). LoadLocation reads bionic's zoneinfo.
	if tz := syspropGet("persist.sys.timezone"); tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			time.Local = loc
		}
	}
	return C.JNI_VERSION_1_6
}

// C: static char android_*[PROP_VALUE_MAX] (android.c:47-51).
var (
	AndroidManufacturer string
	AndroidModel        string
	AndroidName         string
	AndroidVersion      string
	AndroidSerialno     string
)

// JNIEnv returns the JNIEnv* for the current thread, attaching the
// thread to the JVM if needed (C: thread_trampoline's
// AttachCurrentThread, android_threads.c:136 — in Go a goroutine may
// run on any OS thread so attachment is per-call with LockOSThread).
// The caller must hold an OS-thread lock (runtime.LockOSThread) for
// the duration of the JNIEnv use; JniDetach detaches a thread that
// was attached by JNIEnv.
func JNIEnv() *C.JNIEnv {
	if JVM == nil {
		return nil
	}
	env := C.ml_jni_getenv(JVM)
	if env == nil {
		env = C.ml_jni_attach(JVM)
	}
	return env
}

// JniDetach — C: DetachCurrentThread (android_threads.c:148).
func JniDetach() {
	if JVM != nil {
		C.ml_jni_detach(JVM)
	}
}

// threadTrampolineAttach/threadTrampolineDetach — C: the
// AttachCurrentThread/DetachCurrentThread pair bracketing
// thread_trampoline (android_threads.c:136-148). Go goroutines are
// not OS threads; attaching locks the goroutine to its OS thread for
// the thread body's whole lifetime — the semantic equivalent.
func threadTrampolineAttach() {
	runtime.LockOSThread()
	C.ml_jni_attach(JVM)
}

func threadTrampolineDetach() {
	C.ml_jni_detach(JVM)
	runtime.UnlockOSThread()
}

func atoiOrZero(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0
		}
		n = n*10 + int(s[i]-'0')
	}
	return n
}

func init() {
	// C: thread_trampoline attaches every spawned pthread to the JVM
	// (android_threads.c:136-148). Wire the same bracket into the
	// generic goroutine thread creators.
	threadAttachHook = threadTrampolineAttach
	threadDetachHook = threadTrampolineDetach
}

// AndroidBitmapDestroy — C: android_bitmap_destroy
// (android_support.c:31-37): Bitmap.recycle() + DeleteGlobalRef.
func AndroidBitmapDestroy(env, bitmap unsafe.Pointer) {
	e := (*C.JNIEnv)(env)
	bm := C.jobject(bitmap)
	class := C.ml_jni_get_object_class(e, bm)
	mid := C.ml_jni_get_method(e, class,
		C.CString("recycle"), C.CString("()V"))
	C.ml_jni_call_void(e, bm, mid)
	C.ml_jni_del_global_ref(e, bm)
}

// AndroidBitmapCreate — C: android_bitmap_create
// (android_support.c:39-46): Core.createBitmap(w,h) → global ref.
func AndroidBitmapCreate(env unsafe.Pointer, width, height int) unsafe.Pointer {
	e := (*C.JNIEnv)(env)
	mid := C.ml_jni_get_static_method(e, C.jclass(STCore),
		C.CString("createBitmap"),
		C.CString("(II)Landroid/graphics/Bitmap;"))
	obj := C.ml_jni_call_static_obj_ii(e, C.jclass(STCore), mid,
		C.jint(width), C.jint(height))
	return unsafe.Pointer(C.ml_jni_new_global_ref(e, obj))
}

// ---- launcher support (Apps.java) -------------------------------------------
// New in Go — upstream C has no app-launcher feature.

// appsClass returns the Apps jclass (global ref cached in coreInit)
// or nil if not initialized.
func appsClass(env *C.JNIEnv) C.jclass {
	return C.jclass(STApps)
}

// AndroidAppsList — Apps.list() → JSON array of {pkg,label,icon}.
// Returns "" on failure.
func AndroidAppsList() string {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	env := JNIEnv()
	if env == nil {
		return ""
	}
	cls := appsClass(env)
	if unsafe.Pointer(cls) == nil {
		return ""
	}
	name := C.CString("list")
	sig := C.CString("()Ljava/lang/String;")
	defer C.free(unsafe.Pointer(name))
	defer C.free(unsafe.Pointer(sig))
	mid := C.ml_jni_get_static_method(env, cls, name, sig)
	if mid == nil {
		return ""
	}
	obj := C.ml_jni_call_static_obj0(env, cls, mid)
	if unsafe.Pointer(obj) == nil {
		return ""
	}
	defer C.ml_jni_del_local_ref(env, obj)
	s := C.jstring(obj)
	chars := C.ml_jni_get_string_chars(env, s)
	if chars == nil {
		return ""
	}
	defer C.ml_jni_release_string_chars(env, s, chars)
	return C.GoString(chars)
}

// AndroidAppLaunch — Apps.launch(pkg) → true when the activity started.
func AndroidAppLaunch(pkg string) bool {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	env := JNIEnv()
	if env == nil {
		TraceArch(1, "APPS", "launch "+pkg+": no env")
		return false
	}
	cls := appsClass(env)
	if unsafe.Pointer(cls) == nil {
		TraceArch(1, "APPS", "launch "+pkg+": no class")
		return false
	}
	name := C.CString("launch")
	sig := C.CString("(Ljava/lang/String;)Z")
	cpkg := C.CString(pkg)
	defer C.free(unsafe.Pointer(name))
	defer C.free(unsafe.Pointer(sig))
	defer C.free(unsafe.Pointer(cpkg))
	mid := C.ml_jni_get_static_method(env, cls, name, sig)
	if mid == nil {
		return false
	}
	jpkg := C.ml_jni_new_string(env, cpkg)
	defer C.ml_jni_del_local_ref(env, C.jobject(jpkg))
	ok := C.ml_jni_call_static_bool_str(env, cls, mid, jpkg) != 0
	TraceArch(1, "APPS", "launch "+pkg)
	return ok
}

// AndroidOpenHomeSettings — Apps.openHomeSettings(): system "default home
// app" screen (or the launcher chooser as a fallback).
func AndroidOpenHomeSettings() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	env := JNIEnv()
	if env == nil {
		TraceArch(1, "APPS", "openHomeSettings: no jni env")
		return
	}
	cls := appsClass(env)
	if unsafe.Pointer(cls) == nil {
		TraceArch(1, "APPS", "openHomeSettings: no Apps class")
		return
	}
	name := C.CString("openHomeSettings")
	sig := C.CString("()V")
	defer C.free(unsafe.Pointer(name))
	defer C.free(unsafe.Pointer(sig))
	mid := C.ml_jni_get_static_method(env, cls, name, sig)
	if mid == nil {
		TraceArch(1, "APPS", "openHomeSettings: no method")
		return
	}
	TraceArch(3, "APPS", "openHomeSettings: firing intent")
	C.ml_jni_call_static_void0(env, cls, mid)
}

// AndroidSetLauncherAlias — Apps.setLauncherAlias(int): enable or
// disable the HOME activity-alias, i.e. whether Movian is offered as
// a launcher candidate at all. This only changes component state —
// it never fires an intent or moves us to the background.
func AndroidSetLauncherAlias(on bool) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	env := JNIEnv()
	if env == nil {
		return
	}
	cls := appsClass(env)
	if unsafe.Pointer(cls) == nil {
		return
	}
	name := C.CString("setLauncherAlias")
	sig := C.CString("(I)V")
	defer C.free(unsafe.Pointer(name))
	defer C.free(unsafe.Pointer(sig))
	mid := C.ml_jni_get_static_method(env, cls, name, sig)
	if mid == nil {
		return
	}
	v := C.jint(0)
	if on {
		v = 1
	}
	C.ml_jni_call_static_void_int(env, cls, mid, v)
}

// ---- TV Input Framework bridge (Tv.java) -----------------------------------
// New in Go — upstream C has no TIF support. Tv.java exposes the
// system TvProvider channel list and tunes a TvView owned by
// GLWActivity.

// tvClass returns the Tv jclass (global ref cached in coreInit) or nil.
func tvClass(env *C.JNIEnv) C.jclass {
	return C.jclass(STTv)
}

// AndroidTvInputCount — Tv.inputCount(): >0 when the device exposes
// any TvInputService (tuner, IPTV, streaming).
func AndroidTvInputCount() int {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	env := JNIEnv()
	if env == nil {
		return 0
	}
	cls := tvClass(env)
	if unsafe.Pointer(cls) == nil {
		return 0
	}
	name := C.CString("inputCount")
	sig := C.CString("()I")
	defer C.free(unsafe.Pointer(name))
	defer C.free(unsafe.Pointer(sig))
	mid := C.ml_jni_get_static_method(env, cls, name, sig)
	if mid == nil {
		return 0
	}
	return int(C.ml_jni_call_static_int0(env, cls, mid))
}

// AndroidTvListInputs — Tv.listInputs() → JSON array.
func AndroidTvListInputs() string {
	return tvCallStaticString("listInputs")
}

// AndroidTvListChannels — Tv.listChannels() → JSON array.
func AndroidTvListChannels() string {
	return tvCallStaticString("listChannels")
}

// tvCallStaticString — shared helper for the no-arg JSON getters.
func tvCallStaticString(method string) string {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	env := JNIEnv()
	if env == nil {
		return ""
	}
	cls := tvClass(env)
	if unsafe.Pointer(cls) == nil {
		return ""
	}
	name := C.CString(method)
	sig := C.CString("()Ljava/lang/String;")
	defer C.free(unsafe.Pointer(name))
	defer C.free(unsafe.Pointer(sig))
	mid := C.ml_jni_get_static_method(env, cls, name, sig)
	if mid == nil {
		return ""
	}
	obj := C.ml_jni_call_static_obj0(env, cls, mid)
	if unsafe.Pointer(obj) == nil {
		return ""
	}
	defer C.ml_jni_del_local_ref(env, obj)
	s := C.jstring(obj)
	chars := C.ml_jni_get_string_chars(env, s)
	if chars == nil {
		return ""
	}
	defer C.ml_jni_release_string_chars(env, s, chars)
	return C.GoString(chars)
}

// AndroidTvTune — Tv.tune(inputId, channelId): tune the TvView to a
// TvProvider channel.
func AndroidTvTune(inputID string, channelID int64) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	env := JNIEnv()
	if env == nil {
		return
	}
	cls := tvClass(env)
	if unsafe.Pointer(cls) == nil {
		return
	}
	name := C.CString("tune")
	sig := C.CString("(Ljava/lang/String;J)V")
	cid := C.CString(inputID)
	defer C.free(unsafe.Pointer(name))
	defer C.free(unsafe.Pointer(sig))
	defer C.free(unsafe.Pointer(cid))
	mid := C.ml_jni_get_static_method(env, cls, name, sig)
	if mid == nil {
		return
	}
	jid := C.ml_jni_new_string(env, cid)
	defer C.ml_jni_del_local_ref(env, C.jobject(jid))
	C.ml_jni_call_static_void_str_long(env, cls, mid, jid, C.jlong(channelID))
}

// AndroidTvTuneUri — Tv.tuneUri(inputId, uri): tune by raw URI string.
// Vendor inputs may accept non-TvContract URIs (e.g. dvb:// triplet).
func AndroidTvTuneUri(inputID, uri string) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	env := JNIEnv()
	if env == nil {
		return
	}
	cls := tvClass(env)
	if unsafe.Pointer(cls) == nil {
		return
	}
	name := C.CString("tuneUri")
	sig := C.CString("(Ljava/lang/String;Ljava/lang/String;)V")
	cid := C.CString(inputID)
	curi := C.CString(uri)
	defer C.free(unsafe.Pointer(name))
	defer C.free(unsafe.Pointer(sig))
	defer C.free(unsafe.Pointer(cid))
	defer C.free(unsafe.Pointer(curi))
	mid := C.ml_jni_get_static_method(env, cls, name, sig)
	if mid == nil {
		return
	}
	jid := C.ml_jni_new_string(env, cid)
	defer C.ml_jni_del_local_ref(env, C.jobject(jid))
	juri := C.ml_jni_new_string(env, curi)
	defer C.ml_jni_del_local_ref(env, C.jobject(juri))
	C.ml_jni_call_static_void_str_str(env, cls, mid, jid, juri)
}

// AndroidTvUntune — Tv.untune(): release the TvView session.
func AndroidTvUntune() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	env := JNIEnv()
	if env == nil {
		return
	}
	cls := tvClass(env)
	if unsafe.Pointer(cls) == nil {
		return
	}
	name := C.CString("untune")
	sig := C.CString("()V")
	defer C.free(unsafe.Pointer(name))
	defer C.free(unsafe.Pointer(sig))
	mid := C.ml_jni_get_static_method(env, cls, name, sig)
	if mid == nil {
		return
	}
	C.ml_jni_call_static_void0(env, cls, mid)
}

// AndroidTvSetVideoRect — Tv.setVideoRect(l,t,r,b): place the TvView
// surface in pixels. An empty/negative rect restores fullscreen;
// the rect persists across retunes so the channel-list PiP survives.
func AndroidTvSetVideoRect(l, t, r, b int) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	env := JNIEnv()
	if env == nil {
		return
	}
	cls := tvClass(env)
	if unsafe.Pointer(cls) == nil {
		return
	}
	name := C.CString("setVideoRect")
	sig := C.CString("(IIII)V")
	defer C.free(unsafe.Pointer(name))
	defer C.free(unsafe.Pointer(sig))
	mid := C.ml_jni_get_static_method(env, cls, name, sig)
	if mid == nil {
		return
	}
	C.ml_jni_call_static_void_iiii(env, cls, mid,
		C.jint(l), C.jint(t), C.jint(r), C.jint(b))
}

// AndroidTvIsTuned — Tv.isTuned(): a TvView session is alive.
func AndroidTvIsTuned() bool {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	env := JNIEnv()
	if env == nil {
		return false
	}
	cls := tvClass(env)
	if unsafe.Pointer(cls) == nil {
		return false
	}
	name := C.CString("isTuned")
	sig := C.CString("()I")
	defer C.free(unsafe.Pointer(name))
	defer C.free(unsafe.Pointer(sig))
	mid := C.ml_jni_get_static_method(env, cls, name, sig)
	if mid == nil {
		return false
	}
	return C.ml_jni_call_static_int0(env, cls, mid) != 0
}

// AndroidTvSetup — Tv.openSetup(inputId): launch the input's setup
// activity (e.g. channel scan). Returns false when the input has none.
func AndroidTvSetup(inputID string) bool {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	env := JNIEnv()
	if env == nil {
		return false
	}
	cls := tvClass(env)
	if unsafe.Pointer(cls) == nil {
		return false
	}
	name := C.CString("openSetup")
	sig := C.CString("(Ljava/lang/String;)Z")
	cid := C.CString(inputID)
	defer C.free(unsafe.Pointer(name))
	defer C.free(unsafe.Pointer(sig))
	defer C.free(unsafe.Pointer(cid))
	mid := C.ml_jni_get_static_method(env, cls, name, sig)
	if mid == nil {
		return false
	}
	jid := C.ml_jni_new_string(env, cid)
	defer C.ml_jni_del_local_ref(env, C.jobject(jid))
	return C.ml_jni_call_static_bool_str(env, cls, mid, jid) != 0
}

// AndroidTvPause — Tv.pause(paused): timeshift pause/resume.
func AndroidTvPause(paused bool) {
	tvCallVoidInt("pause", "(Z)V", boolToInt32(paused))
}

// AndroidTvSeekTo — Tv.seekTo(ms): absolute wall-clock position in the
// timeshift buffer.
func AndroidTvSeekTo(ms int64) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	env := JNIEnv()
	if env == nil {
		return
	}
	cls := tvClass(env)
	if unsafe.Pointer(cls) == nil {
		return
	}
	name := C.CString("seekTo")
	sig := C.CString("(J)V")
	defer C.free(unsafe.Pointer(name))
	defer C.free(unsafe.Pointer(sig))
	mid := C.ml_jni_get_static_method(env, cls, name, sig)
	if mid == nil {
		return
	}
	C.ml_jni_call_static_void_long(env, cls, mid, C.jlong(ms))
}

// AndroidTvTimeshift — Tv.timeshift(): "start;cur" wall-clock ms.
func AndroidTvTimeshift() string {
	return tvCallStaticString("timeshift")
}

// AndroidTvTracks — Tv.tracks(): JSON array of audio/subtitle tracks.
func AndroidTvTracks() string {
	return tvCallStaticString("tracks")
}

// AndroidTvPrograms — Tv.programs(channelId): JSON now/next EPG rows
// (empty when the provider data isn't readable for this channel).
func AndroidTvPrograms(channelID int64) string {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	env := JNIEnv()
	if env == nil {
		return ""
	}
	cls := tvClass(env)
	if unsafe.Pointer(cls) == nil {
		return ""
	}
	name := C.CString("programs")
	sig := C.CString("(J)Ljava/lang/String;")
	defer C.free(unsafe.Pointer(name))
	defer C.free(unsafe.Pointer(sig))
	mid := C.ml_jni_get_static_method(env, cls, name, sig)
	if mid == nil {
		return ""
	}
	obj := C.ml_jni_call_static_obj_long(env, cls, mid, C.jlong(channelID))
	if unsafe.Pointer(obj) == nil {
		return ""
	}
	defer C.ml_jni_del_local_ref(env, obj)
	s := C.jstring(obj)
	chars := C.ml_jni_get_string_chars(env, s)
	if chars == nil {
		return ""
	}
	defer C.ml_jni_release_string_chars(env, s, chars)
	return C.GoString(chars)
}

// AndroidTvEpgURL — Tv.epgUrl(): XMLTV feed URL from the discovered
// channels sidecar JSON, "" when unset.
func AndroidTvEpgURL() string {
	return tvCallStaticString("epgUrl")
}

// AndroidTvSelectTrack — Tv.selectTrack(type, id).
func AndroidTvSelectTrack(trackType int, id string) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	env := JNIEnv()
	if env == nil {
		return
	}
	cls := tvClass(env)
	if unsafe.Pointer(cls) == nil {
		return
	}
	name := C.CString("selectTrack")
	sig := C.CString("(ILjava/lang/String;)V")
	cid := C.CString(id)
	defer C.free(unsafe.Pointer(name))
	defer C.free(unsafe.Pointer(sig))
	defer C.free(unsafe.Pointer(cid))
	mid := C.ml_jni_get_static_method(env, cls, name, sig)
	if mid == nil {
		return
	}
	jid := C.ml_jni_new_string(env, cid)
	defer C.ml_jni_del_local_ref(env, C.jobject(jid))
	C.ml_jni_call_static_void_int_str(env, cls, mid,
		C.jint(trackType), jid)
}

// tvCallVoidInt — shared helper for single-int Tv statics.
func tvCallVoidInt(method, sig string, v int32) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	env := JNIEnv()
	if env == nil {
		return
	}
	cls := tvClass(env)
	if unsafe.Pointer(cls) == nil {
		return
	}
	name := C.CString(method)
	csig := C.CString(sig)
	defer C.free(unsafe.Pointer(name))
	defer C.free(unsafe.Pointer(csig))
	mid := C.ml_jni_get_static_method(env, cls, name, csig)
	if mid == nil {
		return
	}
	C.ml_jni_call_static_void_int(env, cls, mid, C.jint(v))
}

func boolToInt32(b bool) int32 {
	if b {
		return 1
	}
	return 0
}

// AndroidOpenSettings — Apps.openSettings(): the Android system
// Settings app.
func AndroidOpenSettings() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	env := JNIEnv()
	if env == nil {
		return
	}
	cls := appsClass(env)
	if unsafe.Pointer(cls) == nil {
		return
	}
	name := C.CString("openSettings")
	sig := C.CString("()V")
	defer C.free(unsafe.Pointer(name))
	defer C.free(unsafe.Pointer(sig))
	mid := C.ml_jni_get_static_method(env, cls, name, sig)
	if mid == nil {
		return
	}
	C.ml_jni_call_static_void0(env, cls, mid)
}
