package ui

import (
	"runtime"
	"testing"
	"unsafe"
)

// The scoped attach helper behind androidPopupCall: a thread the call
// attached itself is detached before the OS thread is unlocked; a thread that
// was already attached is left alone. Whether the runtime really stops leaking
// JVM threads needs a device run; this checks the ownership logic.
func TestWithAttachedThreadDetachesOnlyOwnAttach(t *testing.T) {
	var marker byte
	env := unsafe.Pointer(&marker)
	for _, tc := range []struct {
		name       string
		env        unsafe.Pointer
		attached   bool
		wantDetach int
		wantRun    bool
	}{
		{"attached by this call", env, true, 1, true},
		{"already attached", env, false, 0, true},
		{"attach failed", nil, false, 0, false},
	} {
		var order []string
		got := withAttachedThread(
			func() (unsafe.Pointer, bool) { order = append(order, "attach"); return tc.env, tc.attached },
			func() { order = append(order, "detach") },
			func(e unsafe.Pointer) string {
				order = append(order, "run")
				if e != env {
					t.Errorf("%s: fn got a different env", tc.name)
				}
				return "result"
			})
		detaches, ran := 0, false
		for _, o := range order {
			switch o {
			case "detach":
				detaches++
			case "run":
				ran = true
			}
		}
		if detaches != tc.wantDetach || ran != tc.wantRun {
			t.Errorf("%s: order %v, want %d detach(es), run=%v", tc.name, order, tc.wantDetach, tc.wantRun)
		}
		if tc.wantDetach == 1 && order[len(order)-1] != "detach" {
			t.Errorf("%s: detach must come after the call: %v", tc.name, order)
		}
		if (got == "result") != tc.wantRun {
			t.Errorf("%s: result %q", tc.name, got)
		}
	}
}

// A call that panics still detaches what it attached, and the OS thread is
// not left locked.
func TestWithAttachedThreadDetachesOnPanic(t *testing.T) {
	var marker byte
	detached := false
	func() {
		defer func() { _ = recover() }()
		withAttachedThread(
			func() (unsafe.Pointer, bool) { return unsafe.Pointer(&marker), true },
			func() { detached = true },
			func(unsafe.Pointer) string { panic("boom") })
	}()
	if !detached {
		t.Fatal("a panicking call left the thread attached")
	}
	runtime.UnlockOSThread() // no-op if already unlocked; keeps the test goroutine clean
}
