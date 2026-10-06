//go:build linux && sunxi

package core

// alsaGetDevicename — C: alsa_get_devicename (sunxi_alsa.c:27-30).
// The sunxi HDMI/codec ALSA device.
func alsaGetDevicename() string { return "hw:1,0" }
