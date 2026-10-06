//go:build linux && !sunxi

package core

// alsaGetDevicename — C: alsa_get_devicename (alsa_default.c:35-38).
// The plain-Linux build returns "default" (sunxi overrides to hw:1,0).
func alsaGetDevicename() string { return "default" }
