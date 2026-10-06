//go:build !windows

package runcontrol

import "syscall"

// Write a control byte to the supervising shell fd (unix pipe fd).
func rcShellWrite(fd int, b []byte) error {
	_, err := syscall.Write(fd, b)
	return err
}
