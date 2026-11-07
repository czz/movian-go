//go:build android && webpopup

// webpopup_android.go — the Android webpopup backend: the WebPopupActivity
// (Core.webPopupPrototype*) as a clearance-only popup. It is not a general
// browser: wpPlatformBrowser does nothing, and cookies are handed out only
// through SelectAndroidClearance for the popup's owner and origin (see
// webpopup_androidbridge.go). Nothing here logs cookies, tokens or UA strings.
package ui

/*
#include <jni.h>
#include <stdlib.h>

// cls is the borrowed global reference owned by arch.STCore. The returned
// jstring is a local reference; the caller deletes it. Argument strings are
// UTF-16 buffers (C-allocated by the caller) turned into local references
// created and deleted within this function. When hasLong is set, tail is
// passed as one trailing jlong argument.
static jstring popup_call(JNIEnv *e, jclass cls, const char *method,
                          const char *sig, const jchar **args,
                          const jsize *lens, int n, jlong tail, int hasLong) {
    jmethodID m = (*e)->GetStaticMethodID(e, cls, method, sig);
    if ((*e)->ExceptionCheck(e) || !m) { (*e)->ExceptionClear(e); return NULL; }
    jvalue a[8] = {0};
    if (n > 7) return NULL;
    for (int i = 0; i < n; i++) {
        a[i].l = (*e)->NewString(e, args[i], lens[i]);
        if ((*e)->ExceptionCheck(e) || !a[i].l) {
            (*e)->ExceptionClear(e);
            for (int j = 0; j < i; j++) (*e)->DeleteLocalRef(e, a[j].l);
            return NULL;
        }
    }
    if (hasLong) a[n].j = tail;
    jstring out = (jstring)(*e)->CallStaticObjectMethodA(e, cls, m, a);
    for (int i = 0; i < n; i++) (*e)->DeleteLocalRef(e, a[i].l);
    if ((*e)->ExceptionCheck(e)) {
        (*e)->ExceptionClear(e);
        if (out) (*e)->DeleteLocalRef(e, out);
        return NULL;
    }
    return out;
}
// s is a borrowed local reference; the caller releases the returned chars
// before deleting that reference.
static const jchar *popup_chars(JNIEnv *e, jstring s) { return (*e)->GetStringChars(e, s, NULL); }
// s is a borrowed local reference.
static jsize popup_len(JNIEnv *e, jstring s) { return (*e)->GetStringLength(e, s); }
// s is a borrowed local reference and p is the pointer from popup_chars.
static void popup_release(JNIEnv *e, jstring s, const jchar *p) { (*e)->ReleaseStringChars(e, s, p); }
// o is a local reference owned by the caller and is deleted here.
static void popup_delete(JNIEnv *e, jobject o) { (*e)->DeleteLocalRef(e, o); }
*/
import "C"

import (
	"runtime"
	"unsafe"

	"github.com/czz/movian-go/internal/arch"
)

// WebpopupSupportsBrowserLogin — the Android popup is clearance-only, not a
// full browser able to carry a login flow.
const WebpopupSupportsBrowserLogin = false

// androidJNI is the real androidPopupJNI: calls into com.moviango.mediaplayer.Core.
type androidJNI struct{}

var androidBridgeInst = newAndroidBridge(androidJNI{})

const (
	sigPopup   = "(Ljava/lang/String;Ljava/lang/String;Ljava/lang/String;Ljava/lang/String;Ljava/lang/String;Ljava/lang/String;J)Ljava/lang/String;"
	sigCookies = "(Ljava/lang/String;Ljava/lang/String;)Ljava/lang/String;"
	sigUA      = "()Ljava/lang/String;"
)

func (androidJNI) Popup(uri, title, trap, waitURL, waitName, owner string, waitTimeoutMs int64) string {
	return androidPopupCall("webPopupPrototype", sigPopup, waitTimeoutMs, true, uri, title, trap, waitURL, waitName, owner)
}

func (androidJNI) Cookies(uri, owner string) string {
	return androidPopupCall("webPopupPrototypeCookies", sigCookies, 0, false, uri, owner)
}

func (androidJNI) UserAgent() string {
	return androidPopupCall("webPopupPrototypeUA", sigUA, 0, false)
}

// androidPopupCall runs one static Core method on a locked OS thread with a
// JNIEnv for it. Every wait is bounded on the Java side (15 s claim, the
// cookie-wait limit or 30 min session, 10 s cookie query). The result string
// is copied before its chars and local reference are released.
func androidPopupCall(method, sig string, tail int64, hasTail bool, args ...string) string {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	env := (*C.JNIEnv)(unsafe.Pointer(arch.JNIEnv()))
	if env == nil || arch.STCore == nil || len(args) > 7 {
		return ""
	}
	cm, cs := C.CString(method), C.CString(sig)
	defer C.free(unsafe.Pointer(cm))
	defer C.free(unsafe.Pointer(cs))
	// Argument strings are copied to C memory as UTF-16 (JNI's UTF-8 is
	// modified UTF-8). The arrays below hold C pointers and lengths only,
	// so passing them to C is cgo-safe.
	var cargs [7]*C.jchar
	var clens [7]C.jsize
	for i, a := range args {
		u := jniUTF16(a)
		clens[i] = C.jsize(len(u))
		cargs[i] = (*C.jchar)(C.malloc(C.size_t(len(u)+1) * 2))
		if len(u) > 0 {
			copy(unsafe.Slice((*uint16)(unsafe.Pointer(cargs[i])), len(u)), u)
		}
		defer C.free(unsafe.Pointer(cargs[i]))
	}
	hasLong := C.int(0)
	if hasTail {
		hasLong = 1
	}
	result := C.popup_call(env, C.jclass(arch.STCore), cm, cs,
		(**C.jchar)(unsafe.Pointer(&cargs[0])), (*C.jsize)(unsafe.Pointer(&clens[0])),
		C.int(len(args)), C.jlong(tail), hasLong)
	if result == 0 {
		return ""
	}
	defer C.popup_delete(env, C.jobject(result))
	n := int(C.popup_len(env, result))
	chars := C.popup_chars(env, result)
	if chars == nil {
		return ""
	}
	defer C.popup_release(env, result, chars)
	if n == 0 {
		return ""
	}
	return fromJNIUTF16(unsafe.Slice((*uint16)(unsafe.Pointer(chars)), n)) // copies before release
}

func wpPlatformSetup()   {}
func wpPlatformOK() bool { return arch.STCore != nil }

// wpPlatformDispatch runs fn on its own goroutine: the Java popup call
// blocks, and the caller (WebpopupCreate) waits on the result channel.
func wpPlatformDispatch(fn func()) { go fn() }

func wpPlatformRunPopup(uri, title, trap string, wait *WebpopupCookieWait,
	wr *WebpopupResult, done chan<- *WebpopupResult) {
	androidBridgeInst.runPopup(uri, title, trap, wait, wr)
	done <- wr
}

// wpPlatformBrowser: the Android popup is clearance-only, not a general browser.
func wpPlatformBrowser(uri, title string) {}

func wpPlatformStorage(dir string) {}

func wpPlatformCookies(uri string, r *cookieRead) { androidBridgeInst.readCookies(uri, r) }

func wpPlatformDeleteCookies(host, path string, names map[string]bool, done chan int) {
	done <- 0
}

func wpPlatformUserAgent() string { return androidBridgeInst.userAgent() }

func WpMainCheck() {}

func WpActive() bool { return false }
