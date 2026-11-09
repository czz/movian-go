package ui

import (
	"runtime"
	"unsafe"
)

// withAttachedThread runs fn with a JNIEnv for the current OS thread, which
// stays locked to this goroutine for the whole call. attach returns the env
// and whether this very call attached the thread to the JVM; only then does
// detach run, and it runs before the thread is unlocked, also when fn
// panics. A thread that was already attached (a Java-created thread, a
// caller up the stack) is never detached here. If attach yields no env, fn
// does not run and the result is "".
//
// The logic is tagless so a host test covers it; the Android runtime effect
// (no JVM threads left attached) needs a device run.
func withAttachedThread(attach func() (unsafe.Pointer, bool), detach func(),
	fn func(env unsafe.Pointer) string) string {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	env, attached := attach()
	if attached {
		defer detach()
	}
	if env == nil {
		return ""
	}
	return fn(env)
}
