// Package app provides the application data root path.
// Mirrors C's app_dataroot() (src/main.h:52, support/dataroot/):
// installed builds return the compile-time SHOWTIME_DATADIR
// (datadir.c); dev builds return "./" (wd.c); bundle builds
// (android) return "bundle://" (bundle.c — see dataroot_android.go).
package app

// DataDir is the application data root directory.
// Equivalent of C's SHOWTIME_DATADIR: set at link time via
// `make install-build DATADIR=...` (-X pkg/app.DataDir=...).
// Empty → dev build (wd.c: "./").
var DataDir string

// AppName is the lowercase application name.
// C: APPNAME (config.h, e.g. "movian")
const AppName = "movian-go"

// AppNameUser is the user-facing application name.
// C: APPNAMEUSER (config.h, e.g. "Movian")
const AppNameUser = "Movian Go"

// AppDataRoot lives in dataroot.go (!android) and dataroot_android.go —
// the C support/dataroot/*.c link-time variants.
