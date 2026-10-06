//go:build !windows

package arch

// Non-windows stub — the real implementation lives in windows.go
// (MachineGuid device id). Compiled out on every other OS; the call
// site is runtime.GOOS-gated anyway.

// WindowsDeviceID — windows only.
func WindowsDeviceID() string { return "" }
