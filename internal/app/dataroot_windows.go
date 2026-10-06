//go:build windows

package app

// AppDataRoot returns the application data root directory.
// Same link-time variant as support/dataroot/bundle.c on Android:
// windows executables are distributed bare (no res/ folder shipped
// next to movian.exe), so the dataroot is the embedded filebundle
// set registered by bundles_windows.go.
func AppDataRoot() string {
	return "bundle://"
}
