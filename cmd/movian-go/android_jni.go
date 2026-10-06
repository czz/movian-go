//go:build android
// +build android

package main

// Canonical port of the JNI entry points from
// src/arch/android/android.c and src/arch/android/android_glw.c.
// The Go port builds as -buildmode=c-shared; ART calls JNI_OnLoad on
// System.loadLibrary and the Java Core class invokes the Java_*
// natives below. The Java shell (AndroidManifest, Core.java,
// VideoRenderer) is NOT part of the vendored C source drop.

/*
#cgo LDFLAGS: -llog
#include <jni.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

static const char *
ml_jni_getstr(JNIEnv *env, jstring s)
{
  return s == NULL ? NULL : (*env)->GetStringUTFChars(env, s, 0);
}

static void
ml_jni_relstr(JNIEnv *env, jstring s, const char *c)
{
  if(s != NULL && c != NULL)
    (*env)->ReleaseStringUTFChars(env, s, c);
}

static jclass
ml_jni_find_class(JNIEnv *env, const char *name)
{
  return (*env)->FindClass(env, name);
}

static jobject
ml_jni_new_global_ref(JNIEnv *env, jobject o)
{
  return (*env)->NewGlobalRef(env, o);
}
*/
import "C"

import (
	"crypto/md5"
	"encoding/hex"
	"log"
	"runtime"
	"sync/atomic"
	"unsafe"

	"github.com/czz/movian-go/internal/gconf"

	"github.com/czz/movian-go/internal/arch"
	eventpkg "github.com/czz/movian-go/internal/event"
	"github.com/czz/movian-go/internal/i18n"
	medialibav "github.com/czz/movian-go/internal/media/libav"
	navcore "github.com/czz/movian-go/internal/navigator"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/service"
	"github.com/czz/movian-go/internal/trace"
	uiglw "github.com/czz/movian-go/internal/ui/glw"
)

// androidCtx — the appContext built by coreInit; the JNI exports and
// the GLW frontend reach it through here (C uses globals).
var androidCtx *appContext

// androidNavigator — C: android_nav is the nav root prop; the Go port
// also keeps the *Navigator so nav_open(url) can be delivered to it.
var androidNavigator *navcore.Navigator

// coreStarted — C: static int initialized in coreInit
// (android.c:295-298).
var coreStarted int32

//export JNI_OnLoad
func JNI_OnLoad(vm *C.JavaVM, reserved unsafe.Pointer) C.jint {
	return C.jint(arch.JNIOnLoad(unsafe.Pointer(vm)))
}

//export Java_com_moviango_mediaplayer_Core_coreInit
func Java_com_moviango_mediaplayer_Core_coreInit(
	env *C.JNIEnv, obj C.jobject,
	jSettings, jCachedir, jSdcard, jAndroidID C.jstring,
	time24hrs C.jint,
	jMusic, jPictures, jMovies C.jstring,
	audioSampleRate, audioFramesPerBuffer C.jint) {

	// C: static int initialized; if(initialized) return; (android.c:295)
	if !atomic.CompareAndSwapInt32(&coreStarted, 0, 1) {
		return
	}

	c := C.ml_jni_find_class(env, C.CString("com/moviango/mediaplayer/Core"))
	arch.STCore = unsafe.Pointer(C.ml_jni_new_global_ref(env, C.jobject(c)))

	arch.TraceArch(trace.TRACE_INFO, "Core",
		"Native core init pid "+itoa(int(C.getpid()))+" SDK:"+itoa(arch.AndroidSDK))

	// C: gconf_t gconf — global written before main_init; the Go port
	// passes it to newAppContext.
	gc := &gconf.T{}

	// C: gconf.trace_level = TRACE_DEBUG (android.c:307)
	gc.TraceLevel = trace.TRACE_DEBUG

	// C: gconf.time_format_system = time_24hrs ? TIME_FORMAT_24 : TIME_FORMAT_12
	if time24hrs != 0 {
		gc.TimeFormatSystem = i18n.TimeFormat24
	} else {
		gc.TimeFormatSystem = i18n.TimeFormat12
	}

	// C: gettimeofday + srand — Go seeds math/rand automatically.

	settings := C.GoString(C.ml_jni_getstr(env, jSettings))
	cachedir := C.GoString(C.ml_jni_getstr(env, jCachedir))
	sdcard := C.GoString(C.ml_jni_getstr(env, jSdcard))
	androidID := C.GoString(C.ml_jni_getstr(env, jAndroidID))

	// C: android_fs_settings_path/cache_path/sdcard_path (android_fs.c:40-42)
	arch.AndroidFSSettingsPath = settings
	arch.AndroidFSCachePath = cachedir
	arch.AndroidFSSdcardPath = sdcard

	// C: gconf.persistent_path = strdup(settings) /
	//    gconf.cache_path = strdup(cachedir) (android.c:263-264).
	// Upstream master assigns the real JNI-provided directories;
	// the port uses os.* fs ops where C routed through fa, so the
	// real paths are required (a persistent:// URL only resolves
	// inside the fileaccess layer).
	gc.PersistentPath = settings
	gc.CachePath = cachedir

	// C: device_id = md5hex(android_id + android_serialno)
	//    (android.c:328-339)
	h := md5.New()
	h.Write([]byte(androidID))
	h.Write([]byte(arch.AndroidSerialno))
	gc.DeviceID = hex.EncodeToString(h.Sum(nil))

	// C: gconf.concurrency = sysconf(_SC_NPROCESSORS_CONF) (android.c:347)
	gc.Concurrency = runtime.NumCPU()

	// C: setlocale(LC_ALL, "") — Go runtime has no locale concept.
	// C: signal(SIGPIPE, SIG_IGN) (android.c:351)
	arch.AndroidArchStart()

	// C: main_init() (android.c:353) — the whole init chain.
	// On android there is no os.Args/parseOpts; gconf was populated
	// above exactly like the C coreInit does.
	ctx := newAppContext(gc)
	androidCtx = ctx

	// C: android_get_permission seam for the filesystem layer.
	arch.SetAndroidGetPermission(uiglw.AndroidGetPermission)

	// C: service_createp("androidstorage", _p("Android Storage"),
	//     "es://", "storage", NULL, 0, 1, SVC_ORIGIN_SYSTEM)
	//    (android.c:355-356)
	ctx.serviceSystem.ServiceCreatep("androidstorage",
		"Android Storage", "es://", "storage", "", false, true,
		service.SvcOriginSystem)

	// C: android_nav = nav_spawn() (android.c:358)
	androidNavigator = ctx.navSystem.Spawn(ctx.propManager)
	arch.AndroidNav = unsafe.Pointer(androidNavigator.PropRoot())

	// C: nav_open(android_intent, NULL) from glwResize
	//    (android_glw.c:270-272)
	uiglw.SetGlwAndroidNavOpen(func(url string) {
		if androidNavigator != nil {
			androidNavigator.OpenURL(url)
		}
	})

	// C: android_system_audio_sample_rate / frames_per_buffer
	//    (android.c:361-364)
	arch.AndroidSystemAudioSampleRate = int(audioSampleRate)
	arch.AndroidSystemAudioFramesPerBuffer = int(audioFramesPerBuffer)
}

// itoa — strconv.Itoa without importing strconv for two calls.
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

//export Java_com_moviango_mediaplayer_Core_openUri
func Java_com_moviango_mediaplayer_Core_openUri(
	env *C.JNIEnv, obj C.jobject, jURI C.jstring) {

	uri := C.ml_jni_getstr(env, jURI)
	defer C.ml_jni_relstr(env, jURI, uri)

	// C: event_create_openurl(.url = uri);
	//    prop_send_ext_event(prop_create(android_nav, "eventSink"), e);
	//    event_release(e);  (android.c:372-381)
	if arch.AndroidNav == nil || androidCtx == nil {
		// C parity: android_intent was delivered by glwResize once the
		// UI was up (android_glw.c:270). The port never wired a setter
		// for it, and onResume can race coreInit — stash the URI so the
		// first GlwResize delivers it instead of dropping it silently.
		arch.AndroidIntent = C.GoString(uri)
		return
	}
	args := &eventpkg.EventOpenURLArgs{URL: C.GoString(uri)}
	e := androidCtx.eventManager.CreateOpenURLArgs(args)
	sink := androidCtx.propManager.CreateEx(
		(*propcore.Prop)(arch.AndroidNav), "eventSink", nil, false, false)
	androidCtx.propManager.SendExtEvent(sink, &e.Event)
}

//export Java_com_moviango_mediaplayer_Core_networkStatusChanged
func Java_com_moviango_mediaplayer_Core_networkStatusChanged(
	env *C.JNIEnv, obj C.jobject) {
	// C: asyncio_trig_network_change() (android.c:387-391)
	androidCtx.asyncIO.TrigNetworkChange()
}

//export Java_com_moviango_mediaplayer_Core_permissionResult
func Java_com_moviango_mediaplayer_Core_permissionResult(
	env *C.JNIEnv, obj C.jobject, ok C.jboolean) {
	// C: permissionResult (android_glw.c:106-114)
	uiglw.CorePermissionResult(ok != 0)
}

// --- GLW frontend JNI (android_glw.c) ------------------------------------

//export Java_com_moviango_mediaplayer_Core_glwCreate
func Java_com_moviango_mediaplayer_Core_glwCreate(
	env *C.JNIEnv, obj C.jobject, vrp C.jobject) C.jint {
	return C.jint(uiglw.GlwCreate(unsafe.Pointer(env), unsafe.Pointer(vrp)))
}

//export Java_com_moviango_mediaplayer_Core_glwInit
func Java_com_moviango_mediaplayer_Core_glwInit(
	env *C.JNIEnv, obj C.jobject, id C.jint) {
	uiglw.GlwStart(int32(id))
}

//export Java_com_moviango_mediaplayer_Core_glwFini
func Java_com_moviango_mediaplayer_Core_glwFini(
	env *C.JNIEnv, obj C.jobject, id C.jint) {
	uiglw.GlwFini(int32(id))
}

//export Java_com_moviango_mediaplayer_Core_glwDestroy
func Java_com_moviango_mediaplayer_Core_glwDestroy(
	env *C.JNIEnv, obj C.jobject, id C.jint) {
	uiglw.GlwDestroy(unsafe.Pointer(env), int32(id))
}

//export Java_com_moviango_mediaplayer_Core_glwResize
func Java_com_moviango_mediaplayer_Core_glwResize(
	env *C.JNIEnv, obj C.jobject, id, width, height C.jint) {
	uiglw.GlwResize(int32(id), int32(width), int32(height))
}

//export Java_com_moviango_mediaplayer_Core_glwFlush
func Java_com_moviango_mediaplayer_Core_glwFlush(
	env *C.JNIEnv, obj C.jobject, id C.jint) {
	uiglw.GlwFlush(int32(id))
}

//export Java_com_moviango_mediaplayer_Core_glwStep
func Java_com_moviango_mediaplayer_Core_glwStep(
	env *C.JNIEnv, obj C.jobject, id C.jint) {
	uiglw.GlwStep(int32(id))
}

//export Java_com_moviango_mediaplayer_Core_glwMotion
func Java_com_moviango_mediaplayer_Core_glwMotion(
	env *C.JNIEnv, obj C.jobject, id, source, action, x, y C.jint,
	ts C.jlong) {
	uiglw.GlwMotion(int32(id), int32(source), int32(action), int32(x), int32(y), int64(ts))
}

//export Java_com_moviango_mediaplayer_Core_glwKeyDown
func Java_com_moviango_mediaplayer_Core_glwKeyDown(
	env *C.JNIEnv, obj C.jobject, id, keycode, unicode C.jint,
	shift C.jboolean) C.jboolean {
	if uiglw.GlwKeyDown(int32(id), int32(keycode), int32(unicode), shift != 0) {
		return 1
	}
	return 0
}

//export Java_com_moviango_mediaplayer_Core_glwKeyUp
func Java_com_moviango_mediaplayer_Core_glwKeyUp(
	env *C.JNIEnv, obj C.jobject, id, keycode C.jint) C.jboolean {
	if uiglw.GlwKeyUp(int32(id), int32(keycode)) {
		return 1
	}
	return 0
}

// --- MediaCodec async callbacks (android_video_codec.c) --------------------
// Called by the Java MediaCodec.Callback on its own thread; jopaque is
// the int handed to Core.setVideoDecoderWrapper at codec create.

//export Java_com_moviango_mediaplayer_Core_vdInputAvailable
func Java_com_moviango_mediaplayer_Core_vdInputAvailable(
	env *C.JNIEnv, obj C.jobject, jopaque, jbuf C.jint) C.jint {
	medialibav.VdInputAvailable(int32(jopaque), int32(jbuf))
	return 0
}

//export Java_com_moviango_mediaplayer_Core_vdOutputAvailable
func Java_com_moviango_mediaplayer_Core_vdOutputAvailable(
	env *C.JNIEnv, obj C.jobject, jopaque, jbuf C.jint,
	pts C.jlong) C.jint {
	medialibav.VdOutputAvailable(int32(jopaque), int32(jbuf), int64(pts))
	return 0
}

//export Java_com_moviango_mediaplayer_Core_vdOutputFormatChanged
func Java_com_moviango_mediaplayer_Core_vdOutputFormatChanged(
	env *C.JNIEnv, obj C.jobject, jopaque C.jint,
	jinfo C.jobject) C.jint {
	medialibav.VdOutputFormatChanged(int32(jopaque),
		unsafe.Pointer(jinfo))
	return 0
}

//export Java_com_moviango_mediaplayer_Core_vdError
func Java_com_moviango_mediaplayer_Core_vdError(
	env *C.JNIEnv, obj C.jobject, jopaque C.jint) C.jint {
	medialibav.VdError(int32(jopaque))
	return 0
}

func init() {
	log.SetFlags(0)
}
