//go:build android

package arch

import "unsafe"

// AndroidPlatform is the seam between the app and the Android/NDK
// world (JNI). All `func Android*` package functions ARE this seam —
// callers outside internal/arch never touch JNIEnv/jobject directly.
// This interface names the contract so a backend (another device
// class, a JNI-free implementation, a test fake) can conform.
//
// contract: JNI calls happen on whichever thread the caller runs —
// methods that need the UI/Java main thread dispatch through the
// arch layer; JNIEnv obtained via JNIEnv() is thread-local and must
// never be cached across calls; all returned strings are
// Java-object-to-Go copies (no shared buffers).
type AndroidPlatform interface {
	// Tuner — vendor TvInput tuning surface.
	TvTune(inputID string, channelID int64)
	TvTuneUri(inputID, uri string)
	TvUntune()
	TvIsTuned() bool

	// App surface — launch/settings/permission helpers.
	AppLaunch(pkg string) bool
	GetPermission(permission string, interactive bool) int
	OpenHomeSettings()
	SetLauncherAlias(on bool)

	// System — bitmap/lifecycle helpers crossing JNI.
	BitmapCreate(env unsafe.Pointer, width, height int) unsafe.Pointer
	BitmapDestroy(env, bitmap unsafe.Pointer)
}

// The package's own funcs are the seam implementation — kept as
// package functions (not a struct) because callers span 50+ sites
// already using the arch.Android* surface; swapping happens at the
// package level via build tags, matching every other boundary.
