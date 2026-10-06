//go:build !(linux && !android && !rpi && cgo)

package video

// VaapiAvailable — VAAPI extension: false without linux && cgo.
const VaapiAvailable = false

// NvdecAvailable — NVDEC extension: false without linux && cgo.
const NvdecAvailable = false
