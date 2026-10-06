//go:build windows

package arch

// Windows has no linux_trap.c counterpart — upstream never shipped a
// win32 arch. The trap handler is a no-op here; crashes go through the
// platform's own WER path (same rationale as android_trap.go).
func LinuxTrapStart(appversion, binary string) error {
	return nil
}
