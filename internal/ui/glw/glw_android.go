//go:build android

package glw

// Canonical port of src/arch/android/android_glw.c + android_glw.h —
// the GLW frontend driven by JNI callbacks from the Java-side
// SurfaceView renderer thread (the guscio Java lives outside the
// vendored C tree; the JNI entry points are exported here verbatim).

/*
#include <jni.h>
#include <GLES2/gl2.h>
#include <string.h>
#include <stdlib.h>

#include "jni_exc.h"

// C: (*env)->CallVoidMethod on the VideoRenderer instance (vrp).
// ExceptionCheck/report after each JNI call — a pending exception
// must never cross a Call*Method (undefined behaviour per the JNI
// spec). Reporting is deferred (jni_exc.h) so the describe runs on
// a clean stack — it faults otherwise when the pending exception is
// StackOverflowError.
static void
ml_jni_vrp_void0(JNIEnv *env, jobject vrp, const char *name)
{
  jclass cls;
  ml_exc_setctx(name);
  cls = (*env)->GetObjectClass(env, vrp);
  jmethodID mid = (*env)->GetMethodID(env, cls, name, "()V");
  if(mid == NULL || (*env)->ExceptionCheck(env)) {
    ml_exc_report(env);
    return;
  }
  (*env)->CallVoidMethod(env, vrp, mid);
  if((*env)->ExceptionCheck(env))
    ml_exc_report(env);
}

static void
ml_jni_vrp_ask_permission(JNIEnv *env, jobject vrp, jstring perm)
{
  jclass cls;
  ml_exc_setctx("askPermission");
  cls = (*env)->GetObjectClass(env, vrp);
  jmethodID mid = (*env)->GetMethodID(env, cls, "askPermission",
                                    "(Ljava/lang/String;)V");
  if(mid == NULL || (*env)->ExceptionCheck(env)) {
    ml_exc_report(env);
    return;
  }
  (*env)->CallVoidMethod(env, vrp, mid, perm);
  if((*env)->ExceptionCheck(env))
    ml_exc_report(env);
}

// C: STCore.checkPermission (android_glw.c:62-65)
static jboolean
ml_jni_check_permission(JNIEnv *env, jclass stcore, jstring perm)
{
  jmethodID mid;
  ml_exc_setctx("checkPermission");
  mid = (*env)->GetStaticMethodID(env, stcore, "checkPermission",
                                          "(Ljava/lang/String;)Z");
  jboolean r;
  if(mid == NULL || (*env)->ExceptionCheck(env)) {
    ml_exc_report(env);
    return 0;
  }
  r = (*env)->CallStaticBooleanMethod(env, stcore, mid, perm);
  if((*env)->ExceptionCheck(env))
    ml_exc_report(env);
  return r;
}

static jobject
ml_jni_new_global_ref(JNIEnv *env, jobject o)
{
  return (*env)->NewGlobalRef(env, o);
}

static void
ml_jni_del_global_ref(JNIEnv *env, jobject o)
{
  (*env)->DeleteGlobalRef(env, o);
}

static jstring
ml_jni_new_string(JNIEnv *env, const char *s)
{
  return (*env)->NewStringUTF(env, s);
}
*/
import "C"

import (
	"runtime/cgo"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/czz/movian-go/internal/arch"
	eventpkg "github.com/czz/movian-go/internal/event"
	propcore "github.com/czz/movian-go/internal/prop"
	tracepkg "github.com/czz/movian-go/internal/trace"
)

// androidGlwRoot — C: android_glw_root_t (android_glw.h:25-39).
type androidGlwRoot struct {
	gr glwRoot

	vrp C.jobject // C: agr_vrp — global ref to the Java VideoRenderer

	running atomic.Bool // C: agr_running

	runcond *sync.Cond // C: agr_runcond (on gr mutex)

	dpadCenter lpHelper // C: agr_dpad_center

	disableScreensaverSub *propcore.Subscription // C: agr_disable_screensaver_sub
	navEventsinkSub       *propcore.Subscription // C: agr_nav_eventsink_sub
}

// lpHelper — C: lphelper_t (ui/longpress.h:25-29).
type lpHelper struct {
	down   int
	expire int64
}

const lpTimeout = 500000 // C: LP_TIMEOUT (longpress.h:23) µs

// longpressPeriodic — C: longpress_periodic (longpress.c:24-34).
func longpressPeriodic(lph *lpHelper, now int64) bool {
	if lph.down != 1 {
		return false
	}
	if lph.expire > now {
		return false
	}
	lph.down = 2
	return true
}

// longpressDown — C: longpress_down (longpress.c:37-43).
func longpressDown(lph *lpHelper) {
	if lph.down != 0 {
		return
	}
	lph.expire = arch.GetTS() + lpTimeout
	lph.down = 1
}

// longpressUp — C: longpress_up (longpress.c:46-51).
func longpressUp(lph *lpHelper) bool {
	r := lph.expire > arch.GetTS() && lph.down != 2
	lph.down = 0
	return r
}

// --- permission state machine (android_glw.c:38-99) -------------------

const (
	permissionStateNone = iota
	permissionStateWaiting
	permissionStateApproved
	permissionStateDenied
)

var (
	permissionGlwRoot *androidGlwRoot // C: permission_glw_root
	permissionMutex   sync.Mutex      // C: permission_mutex
	permissionCond    = sync.NewCond(&permissionMutex)
	permissionState   = permissionStateNone
)

// jniEnv — C: attach_current_thread() + env. arch.JNIEnv() returns
// the JNIEnv pointer from pkg/arch's cgo namespace; converted to this
// package's identical C type via unsafe.Pointer.
func jniEnv() *C.JNIEnv { return (*C.JNIEnv)(unsafe.Pointer(arch.JNIEnv())) }

// androidGetPermission — C: android_get_permission (android_glw.c:52-99).
// Called on any thread; JVM attach via arch JNIEnv().
func androidGetPermission(permission string, interactive bool) int {
	if arch.AndroidSDK < 23 {
		return 1 // Before Android 6.0 permissions are always granted
	}
	env := jniEnv()
	if env == nil {
		return 0
	}
	cperm := C.CString(permission)
	defer C.free(unsafe.Pointer(cperm))
	jstr := C.ml_jni_new_string(env, cperm)

	ok := C.ml_jni_check_permission(env, C.jclass(arch.STCore), jstr)
	if ok != 0 {
		return 1
	}
	if !interactive {
		return 0
	}

	permissionMutex.Lock()
	for permissionState != permissionStateNone {
		permissionCond.Wait()
	}
	if permissionGlwRoot == nil {
		permissionState = permissionStateNone
		permissionMutex.Unlock()
		return 0
	}
	permissionState = permissionStateWaiting

	C.ml_jni_vrp_ask_permission(env, permissionGlwRoot.vrp, jstr)

	for permissionState == permissionStateWaiting {
		permissionCond.Wait()
	}
	r := permissionState
	permissionState = permissionStateNone
	permissionCond.Broadcast()
	permissionMutex.Unlock()
	if r == permissionStateApproved {
		return 1
	}
	return 0
}

// CorePermissionResult — called by the JNI export below
// (C: Java_..._Core_permissionResult, android_glw.c:106-114).
func CorePermissionResult(ok bool) {
	permissionMutex.Lock()
	if ok {
		permissionState = permissionStateApproved
	} else {
		permissionState = permissionStateDenied
	}
	permissionCond.Broadcast()
	permissionMutex.Unlock()
}

// disScreensaverCallback — C: dis_screensaver_callback
// (android_glw.c:117-131).
func disScreensaverCallback(opaque any, value int) {
	agr := opaque.(*androidGlwRoot)
	env := jniEnv()
	if env == nil {
		return
	}
	name := "enableScreenSaver"
	if value != 0 {
		name = "disableScreenSaver"
	}
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	C.ml_jni_vrp_void0(env, agr.vrp, cname)
}

// navEventsink — C: nav_eventsink (android_glw.c:134-146): forwards
// ACTION_SYSTEM_HOME to the Java sysHome() method.
func navEventsink(opaque any, e any) {
	agr := opaque.(*androidGlwRoot)
	ev, _ := e.(*eventpkg.Event)
	if ev == nil || !ev.IsAction(eventpkg.ACTION_SYSTEM_HOME) {
		return
	}
	env := jniEnv()
	if env == nil {
		return
	}
	cname := C.CString("sysHome")
	defer C.free(unsafe.Pointer(cname))
	C.ml_jni_vrp_void0(env, agr.vrp, cname)
}

// --- JNI entry points -----------------------------------------------------

// AndroidGetPermission — exported seam wired into
// arch.AndroidGetPermissionFn by coreInit.
func AndroidGetPermission(permission string, interactive bool) int {
	return androidGetPermission(permission, interactive)
}

// GlwCreate — C: Java_..._Core_glwCreate (android_glw.c:148-189).
// env is the already-attached JNIEnv from the Java caller's thread;
// vrp is the local ref to the VideoRenderer. Both are passed as
// unsafe.Pointer because cgo types are per-package.
func GlwCreate(envP, vrpP unsafe.Pointer) int32 {
	env := (*C.JNIEnv)(envP)
	vrp := C.jobject(vrpP)
	agr := &androidGlwRoot{}
	agr.gr.grPropUi = glwDeps.pm.CreateRoot("ui")
	agr.gr.grPropNav = (*propcore.Prop)(arch.AndroidNav)

	if glwStart(&agr.gr) != 0 {
		return 0
	}
	agr.gr.grBe.gbrUseStencilBuffer = 1

	agr.runcond = sync.NewCond(&agr.gr.grMutex)

	agr.vrp = C.ml_jni_new_global_ref(env, vrp)

	agr.disableScreensaverSub = glwPropSubscribeTags(0,
		propTagCallbackInt, disScreensaverCallback, agr,
		propTagName, []string{"ui", "disableScreensaver"},
		propTagRoot, agr.gr.grPropUi,
		propTagCourier, agr.gr.grCourier,
	)

	agr.navEventsinkSub = glwPropSubscribeTags(0,
		propTagCallbackEvent, navEventsink, agr,
		propTagName, []string{"nav", "eventSink"},
		propTagNamedRoot, (*propcore.Prop)(arch.AndroidNav), "nav",
		propTagCourier, agr.gr.grCourier,
	)

	permissionMutex.Lock()
	permissionGlwRoot = agr
	permissionMutex.Unlock()

	glwLoadUniverse(&agr.gr)
	return int32(cgo.NewHandle(agr))
}

// agrFromID — C: (android_glw_root_t *)id — the jint is a cgo.Handle.
func agrFromID(id int32) *androidGlwRoot {
	if id == 0 {
		return nil
	}
	return cgo.Handle(id).Value().(*androidGlwRoot)
}

// GlwStart — C: Java_..._Core_glwInit (android_glw.c:192-201). Called on
// the renderer thread with the EGL context already current.
func GlwStart(id int32) {
	agr := agrFromID(id)
	if agr == nil {
		return
	}
	agr.running.Store(true)
	glwOpenglSetupContext(&agr.gr)
	C.glClearColor(0, 0, 0, 0)
}

// GlwFini — C: Java_..._Core_glwFini (android_glw.c:204-227).
func GlwFini(id int32) {
	agr := agrFromID(id)
	if agr == nil {
		return
	}
	gr := &agr.gr

	permissionMutex.Lock()
	permissionGlwRoot = nil
	permissionState = permissionStateDenied
	permissionCond.Broadcast()
	permissionMutex.Unlock()

	glwLock(gr)
	// Calling twice will unload all textures, etc
	glwReap(gr)
	glwReap(gr)
	glwFlush(gr)
	glwOpenglFiniContext(gr)
	agr.running.Store(false)
	agr.runcond.Signal()
	glwUnlock(gr)
}

// GlwDestroy — C: Java_..._Core_glwDestroy (android_glw.c:230-251).
func GlwDestroy(envP unsafe.Pointer, id int32) {
	env := (*C.JNIEnv)(envP)
	agr := agrFromID(id)
	if agr == nil {
		return
	}
	gr := &agr.gr

	if agr.disableScreensaverSub != nil {
		agr.disableScreensaverSub.Unsubscribe()
	}
	if agr.navEventsinkSub != nil {
		agr.navEventsinkSub.Unsubscribe()
	}

	glwLock(gr)
	for agr.running.Load() {
		agr.runcond.Wait()
	}
	glwUnlock(gr)

	C.ml_jni_del_global_ref(env, agr.vrp)

	glwUnloadUniverse(gr)
	glwFini(gr)
	glwReleaseRoot(gr)

	cgo.Handle(id).Delete()
}

// GlwAndroidNavOpenFn — C: nav_open (navigator.h) called on the first
// resize to deliver android_intent (android_glw.c:270-272). Wired by
// cmd/movian-go since glw must not import the navigator package.
var glwAndroidNavOpenFn func(url string)

// SetGlwAndroidNavOpen wires the navigator-open call for the Android
// UI (C: nav_open_url direct — provider lives in cmd/movian-go).
func SetGlwAndroidNavOpen(fn func(url string)) { glwAndroidNavOpenFn = fn }

// GlwResize — C: Java_..._Core_glwResize (android_glw.c:255-275).
func GlwResize(id, width, height int32) {
	agr := agrFromID(id)
	if agr == nil {
		return
	}
	glwDeps.ts.Trace(tracepkg.TRACE_INFO, "GLW",
		"Resized to %d x %d", int(width), int(height))
	agr.gr.grWidth = int(width)
	agr.gr.grHeight = int(height)

	if arch.AndroidIntent != "" {
		glwDeps.ts.Trace(tracepkg.TRACE_INFO, "INTENT",
			"Loading: %s", arch.AndroidIntent)
		if glwAndroidNavOpenFn != nil {
			glwAndroidNavOpenFn(arch.AndroidIntent)
		}
		arch.AndroidIntent = ""
	}
}

// GlwFlush — C: Java_..._Core_glwFlush (android_glw.c:278-292).
func GlwFlush(id int32) {
	agr := agrFromID(id)
	if agr == nil {
		return
	}
	gr := &agr.gr
	glwLock(gr)
	glwReap(gr)
	glwReap(gr)
	glwFlush(gr)
	glwUnlock(gr)
}

// GlwStep — C: Java_..._Core_glwStep (android_glw.c:295-365). One frame
// on the renderer thread.
func GlwStep(id int32) {
	agr := agrFromID(id)
	if agr == nil {
		return
	}
	gr := &agr.gr
	var zmax int

	glwLock(gr)
	C.glViewport(0, 0, C.GLsizei(gr.grWidth), C.GLsizei(gr.grHeight))
	C.glClear(C.GL_COLOR_BUFFER_BIT | C.GL_STENCIL_BUFFER_BIT)
	C.glEnable(C.GL_STENCIL_TEST)
	C.glColorMask(0, 0, 0, 0)
	C.glDepthMask(0)

	glwPrepareFrame(gr, 0)

	var rc glwRctx
	glwRctxSetup(&rc, gr.grWidth, gr.grHeight, 1, &zmax)
	glwLayout0(gr.grUniverse, &rc)
	glwRender0(gr.grUniverse, &rc)

	if glwDeps.gconf.EnableTouchDebug {
		glwLine(gr, &rc,
			float32(gr.grTouchStartX), 1,
			float32(gr.grTouchStartX), -1,
			1, 0, 0, 1)
		glwLine(gr, &rc,
			-1, float32(gr.grTouchStartY),
			1, float32(gr.grTouchStartY),
			1, 0, 0, 1)
		glwLine(gr, &rc,
			float32(gr.grTouchMoveX), 1,
			float32(gr.grTouchMoveX), -1,
			0, 1, 0, 1)
		glwLine(gr, &rc,
			-1, float32(gr.grTouchMoveY),
			1, float32(gr.grTouchMoveY),
			0, 1, 0, 1)
		glwLine(gr, &rc,
			float32(gr.grTouchEndX), 1,
			float32(gr.grTouchEndX), -1,
			0, 0, 1, 1)
		glwLine(gr, &rc,
			-1, float32(gr.grTouchEndY),
			1, float32(gr.grTouchEndY),
			0, 0, 1, 1)
	}

	glwUnlock(gr)

	C.glColorMask(1, 1, 1, 1)
	C.glDepthMask(1)

	glwPostScene(gr)

	if longpressPeriodic(&agr.dpadCenter, gr.grFrameStart) {
		e := glwDeps.em.CreateAction(eventpkg.ACTION_ITEMMENU).AsEvent()
		e.Flags |= eventpkg.EventKeypress
		glwInjectEvent(gr, e)
	}
}

// GlwMotion — C: Java_..._Core_glwMotion (android_glw.c:368-406).
func GlwMotion(id, source, action, x, y int32, ts int64) {
	agr := agrFromID(id)
	if agr == nil {
		return
	}
	gr := &agr.gr
	var gpe glwPointerEventT

	gpe.ts = ts * 1000
	switch action {
	case 0:
		gpe.typ = glwPointerTouchStart
	case 1:
		gpe.typ = glwPointerTouchEnd
	case 2:
		gpe.typ = glwPointerTouchMove
	case 3:
		gpe.typ = glwPointerTouchCancel
	default:
		return
	}

	glwLock(gr)
	gpe.screenX = (2.0*float32(x)/float32(gr.grWidth) - 1.0)
	gpe.screenY = -(2.0 * float32(y) / float32(gr.grHeight)) + 1.0
	glwPointerEvent(gr, &gpe)
	glwUnlock(gr)
}

// AKEYCODE constants (android/keycodes.h)
const (
	akeycodeBack             = 4
	akeycodeDpadUp           = 19
	akeycodeDpadDown         = 20
	akeycodeDpadLeft         = 21
	akeycodeDpadRight        = 22
	akeycodeDpadCenter       = 23
	akeycodeStar             = 17
	akeycodeDel              = 67
	akeycodeMenu             = 82
	akeycodeMediaRewind      = 89
	akeycodeMediaPlayPause   = 85
	akeycodeMediaFastForward = 90
	akeycodeEnter            = 66
	akeycodeButtonMode       = 110
	endOfAKEYCODE            = akeycodeButtonMode + 1
)

// btnToAction — C: btn_to_action[] (android_glw.c:414-427).
var btnToAction [endOfAKEYCODE][]eventpkg.ActionType

// shiftBtnToAction — C: shift_btn_to_action[] (android_glw.c:429-434).
var shiftBtnToAction [endOfAKEYCODE][]eventpkg.ActionType

func init() {
	btnToAction[akeycodeBack] = []eventpkg.ActionType{eventpkg.ACTION_NAV_BACK}
	btnToAction[akeycodeDpadLeft] = []eventpkg.ActionType{eventpkg.ACTION_LEFT}
	btnToAction[akeycodeDpadUp] = []eventpkg.ActionType{eventpkg.ACTION_UP}
	btnToAction[akeycodeDpadRight] = []eventpkg.ActionType{eventpkg.ACTION_RIGHT}
	btnToAction[akeycodeDpadDown] = []eventpkg.ActionType{eventpkg.ACTION_DOWN}
	btnToAction[akeycodeMenu] = []eventpkg.ActionType{eventpkg.ACTION_MENU}
	btnToAction[akeycodeStar] = []eventpkg.ActionType{eventpkg.ACTION_ITEMMENU}
	btnToAction[akeycodeMediaRewind] = []eventpkg.ActionType{eventpkg.ACTION_SEEK_BACKWARD}
	btnToAction[akeycodeMediaFastForward] = []eventpkg.ActionType{eventpkg.ACTION_SEEK_FORWARD}
	btnToAction[akeycodeMediaPlayPause] = []eventpkg.ActionType{eventpkg.ACTION_PLAYPAUSE}
	btnToAction[akeycodeEnter] = []eventpkg.ActionType{eventpkg.ACTION_ACTIVATE}
	btnToAction[akeycodeDel] = []eventpkg.ActionType{eventpkg.ACTION_NAV_BACK, eventpkg.ACTION_BS}

	shiftBtnToAction[akeycodeDpadLeft] = []eventpkg.ActionType{eventpkg.ACTION_MOVE_LEFT}
	shiftBtnToAction[akeycodeDpadUp] = []eventpkg.ActionType{eventpkg.ACTION_MOVE_UP}
	shiftBtnToAction[akeycodeDpadRight] = []eventpkg.ActionType{eventpkg.ACTION_MOVE_RIGHT}
	shiftBtnToAction[akeycodeDpadDown] = []eventpkg.ActionType{eventpkg.ACTION_MOVE_DOWN}
}

// GlwKeyDown — C: Java_..._Core_glwKeyDown (android_glw.c:437-485).
func GlwKeyDown(id, keycode, unicode int32, shift bool) bool {
	agr := agrFromID(id)
	if agr == nil {
		return false
	}
	gr := &agr.gr
	var e *eventpkg.Event

	// along with AKEYCODE_ENTER usually passed Line Feed char - 10
	if keycode == akeycodeEnter {
		unicode = 0
	}

	if unicode != 0 {
		e = glwDeps.em.CreateInt(eventpkg.EVENT_UNICODE, int(unicode)).AsEvent()
	} else {
		if keycode == akeycodeDpadCenter {
			longpressDown(&agr.dpadCenter)
			return true
		}
		if int(keycode) < endOfAKEYCODE {
			var avec []eventpkg.ActionType
			if shift {
				avec = shiftBtnToAction[keycode]
			} else {
				avec = btnToAction[keycode]
			}
			if len(avec) > 0 {
				e = glwDeps.em.CreateActionMulti(avec).AsEvent()
			}
		}
	}

	if e != nil {
		e.Flags |= eventpkg.EventKeypress
		glwInjectEvent(gr, e)
		return true
	}
	return false
}

// GlwKeyUp — C: Java_..._Core_glwKeyUp (android_glw.c:487-512).
func GlwKeyUp(id, keycode int32) bool {
	agr := agrFromID(id)
	if agr == nil {
		return false
	}
	var e *eventpkg.Event

	if keycode == akeycodeDpadCenter {
		if longpressUp(&agr.dpadCenter) {
			e = glwDeps.em.CreateAction(eventpkg.ACTION_ACTIVATE).AsEvent()
		}
	}

	if e != nil {
		e.Flags |= eventpkg.EventKeypress
		glwInjectEvent(&agr.gr, e)
		return true
	}
	return false
}
