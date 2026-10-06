//go:build rpi

// rpi_tv.go — C: src/arch/rpi/rpi_tv.c — HDMI display mode matching
// ("Match display and content framerate") via vc_tvservice + VPI hook.
package rpi

/*
#cgo CFLAGS: -DOMX_SKIP64BIT -I/opt/vc/include -I/opt/vc/include/interface/vmcs_host/linux -I/opt/vc/include/interface/vcos/pthreads
#cgo LDFLAGS: -L/opt/vc/lib -lbcm_host -lvcos -lvchiq_arm -lopenmaxil -lEGL -lGLESv2 -lmmal_core -lmmal_util -lmmal_vc_client -lvchostif

#include <stdlib.h>
#include <string.h>
#include <math.h>
#include <interface/vmcs_host/vc_tvservice.h>
#include <interface/vmcs_host/vc_vchi_gencmd.h>
#include "vc_gencmd_shim.h"
*/
import "C"

import (
	"math"
	"unsafe"

	"github.com/czz/movian-go/internal/htsmsg"
	propcore "github.com/czz/movian-go/internal/prop"
	settingscore "github.com/czz/movian-go/internal/settings"
	"github.com/czz/movian-go/internal/trace"
	"github.com/czz/movian-go/internal/video"
)

// C: static int respond (rpi_tv.c:196)
var respond int

// getDisplayAspectRatio — C: get_display_aspect_ratio (rpi_tv.c:33-48)
func getDisplayAspectRatio(aspect C.HDMI_ASPECT_T) float64 {
	switch aspect {
	case C.HDMI_ASPECT_4_3:
		return 4.0 / 3.0
	case C.HDMI_ASPECT_14_9:
		return 14.0 / 9.0
	case C.HDMI_ASPECT_16_9:
		return 16.0 / 9.0
	case C.HDMI_ASPECT_5_4:
		return 5.0 / 4.0
	case C.HDMI_ASPECT_16_10:
		return 16.0 / 10.0
	case C.HDMI_ASPECT_15_9:
		return 15.0 / 9.0
	case C.HDMI_ASPECT_64_27:
		return 64.0 / 27.0
	default:
		return 16.0 / 9.0
	}
}

// RpiSetDisplayFramerate — C: rpi_set_display_framerate (rpi_tv.c:53-170).
// Returns old mode so we can reset.
func RpiSetDisplayFramerate(fps float64, width, height int) int {
	var state C.TV_DISPLAY_STATE_T
	C.memset(unsafe.Pointer(&state), 0, C.sizeof_TV_DISPLAY_STATE_T)

	if C.vc_tv_get_display_state(&state) != 0 {
		rpiTS.Trace(trace.TRACE_DEBUG, "TV", "Failed to get TV state")
		return -1
	}

	rpiTS.Trace(trace.TRACE_DEBUG, "TV",
		"Searching for mode for %d x %d @ %f fps. Current mode=%d",
		width, height, fps, int(C.tv_hdmi_(&state).mode))

	var preferGroup C.HDMI_RES_GROUP_T
	var preferMode C.uint32_t

	nativeDeinterlace := 0 // C: local int native_deinterlace = 0 (rpi_tv.c:76)

	numModes := int(C.vc_tv_hdmi_get_supported_modes_new(
		C.HDMI_RES_GROUP_CEA, nil, 0, &preferGroup, &preferMode))

	if numModes <= 0 {
		return -1
	}

	modes := make([]C.TV_SUPPORTED_MODE_NEW_T, numModes)

	numModes = int(C.vc_tv_hdmi_get_supported_modes_new(
		C.HDMI_RES_GROUP_CEA, &modes[0], C.uint32_t(numModes),
		&preferGroup, &preferMode))

	var bestMode *C.TV_SUPPORTED_MODE_NEW_T
	bestScore := uint32(1 << 30)

	for i := 0; i < numModes; i++ {
		tv := &modes[i]

		var score uint32
		w := int(C.tvmode_width_(tv))
		h := int(C.tvmode_height_(tv))
		r := int(C.tvmode_frame_rate_(tv))

		// Check if frame rate match (equal or exact multiple)
		if math.Abs(float64(r)-1.0*fps)/fps < 0.002 {
			score += 0
		} else if math.Abs(float64(r)-2.0*fps)/fps < 0.002 {
			score += 1 << 8
		} else {
			score += (1 << 16) + (1<<20)/uint32(r)
		}

		// Check size too, only choose bigger resolutions
		if width != 0 && height != 0 {
			// cost of too small a resolution is high
			if d := width - w; d > 0 {
				score += uint32(d) * (1 << 16)
			}
			if d := height - h; d > 0 {
				score += uint32(d) * (1 << 16)
			}
			// cost of too high a resolution is lower
			if d := w - width; d > 0 {
				score += uint32(d) * (1 << 4)
			}
			if d := h - height; d > 0 {
				score += uint32(d) * (1 << 4)
			}
		}

		// native is good
		if C.tvmode_native_(tv) == 0 {
			score += 1 << 16
		}

		// prefer square pixels modes
		par := getDisplayAspectRatio(C.HDMI_ASPECT_T(C.tvmode_aspect_(tv))) *
			float64(h) / float64(w)
		score += uint32(math.Abs(par-1.0) * (1 << 12))

		if score < bestScore {
			bestMode = tv
			bestScore = score
		}
	}

	if bestMode == nil {
		return -1
	}

	rpiTS.Trace(trace.TRACE_DEBUG, "TV",
		"Output mode %d: %dx%d@%d %s%s:%x\n",
		int(C.tvmode_code_(bestMode)), int(C.tvmode_width_(bestMode)), int(C.tvmode_height_(bestMode)),
		int(C.tvmode_frame_rate_(bestMode)), natStr(C.tvmode_native_(bestMode)),
		scanStr(C.tvmode_scan_mode_(bestMode)), int(C.tvmode_code_(bestMode)))

	if nativeDeinterlace != 0 && C.tvmode_scan_mode_(bestMode) != 0 {
		var response [80]C.char
		cf := C.CString("hvs_update_fields %d")
		C.vc_gencmd1(&response[0], C.int(len(response)), cf, C.int(1))
		C.free(unsafe.Pointer(cf))
	}

	// if we are closer to ntsc version of framerate, let gpu know
	ifps := int(fps + 0.5)
	ntscFreq := 0
	if math.Abs(fps*1001.0/1000.0-float64(ifps)) < math.Abs(fps-float64(ifps)) {
		ntscFreq = 1
	}
	{
		var response [80]C.char
		cf := C.CString("hdmi_ntsc_freqs %d")
		C.vc_gencmd1(&response[0], C.int(len(response)), cf, C.int(ntscFreq))
		C.free(unsafe.Pointer(cf))
	}

	// Inform TV of any 3D settings — C leaves property unset for 2D
	var property C.HDMI_PROPERTY_PARAM_T
	property.property = C.HDMI_PROPERTY_3D_STRUCTURE
	property.param1 = C.HDMI_3D_FORMAT_NONE
	property.param2 = 0

	err := C.vc_tv_hdmi_power_on_explicit_new(C.HDMI_MODE_HDMI,
		C.HDMI_RES_GROUP_CEA, C.tvmode_code_(bestMode))

	if err != 0 {
		rpiTS.Trace(trace.TRACE_DEBUG, "TV", "Failed to set mode %d",
			int(C.tvmode_code_(bestMode)))
		return -1
	}
	return int(C.tv_hdmi_(&state).mode)
}

func natStr(native C.uint32_t) string {
	if native != 0 {
		return "N"
	}
	return ""
}

func scanStr(scan C.uint32_t) string {
	if scan != 0 {
		return "I"
	}
	return ""
}

// RpiTvVpi — C: rpi_tv_vpi (rpi_tv.c:199-221), registered via
// VPI_REGISTER; the Go wiring lives in cmd/movian-go init (rpi only).
func RpiTvVpi(op video.VPIOp, info *htsmsg.HTSMsg, p, origin *propcore.Prop) {
	if respond == 0 {
		return
	}

	if op == video.VPIStart {
		framerate, err := info.GetDbl("framerate")
		if err != nil {
			return
		}
		width, err := info.GetU32("width")
		if err != nil {
			return
		}
		height, err := info.GetU32("height")
		if err != nil {
			return
		}
		RpiSetDisplayFramerate(framerate, int(width), int(height))
		RestartUI = 1
	}

	if op == video.VPIStop {
		C.vc_tv_hdmi_power_on_preferred()
		RestartUI = 1
	}
}

// setFramerate — C: set_framerate (rpi_tv.c:230-234)
func setFramerate(aux any, x int) {
	respond = x
}

// RpiTvStart — C: rpi_tv_init (rpi_tv.c:239-253) +
// VPI_REGISTER(rpi_tv_vpi) + INITME(INIT_GROUP_IPC, rpi_tv_init, NULL, 10).
// The handler is appended to vpiHandlers in the platform init (cmd side).
func RpiTvStart(sm *settingscore.SettingsManager) {
	if sm == nil {
		return
	}
	set := sm.SettingGetDir("settings:tv")
	if set == nil {
		return
	}
	sm.SettingCreate(settingscore.SettingBool, set,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle,
		sm.P("Match display and content framerate"),
		settingscore.SettingTagValue, 0,
		settingscore.SettingTagCallback, func(o any, v any) {
			if i, ok := v.(int); ok {
				setFramerate(o, i)
			}
		}, nil,
		settingscore.SettingTagStore, "rpitv", "setframerate",
	)
}
