//go:build android

package arch

// Android has no linux_trap.c counterpart — android.mk does not build
// it and bionic lacks execinfo.h (pre-API 33). The trap handler is a
// no-op here; crashes go through the platform's own tombstone path.
func LinuxTrapStart(appversion, binary string) error {
	return nil
}
