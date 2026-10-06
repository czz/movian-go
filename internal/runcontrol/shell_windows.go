//go:build windows

package runcontrol

import "syscall"

// Write a control byte to the supervising shell fd — a Win32 pipe
// HANDLE here (WriteFile semantics via syscall.Write).
func rcShellWrite(fd int, b []byte) error {
	_, err := syscall.Write(syscall.Handle(fd), b)
	return err
}
