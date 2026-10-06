//go:build rpi

package core

import (
	"errors"
)

// audioDriverStartPlatform — C: audio_driver_init provided by
// src/arch/rpi/rpi_audio.c on the RPi build. The class itself lives in
// pkg/arch/rpi (it needs OMX), which imports this package — hence the
// registered hook. Returns the dummy-class behaviour of failing loudly
// if the platform package was not linked in.
func audioDriverStartPlatform(settings any) (*AudioClass, error) {
	if audioDeps.platformDriverStart == nil {
		return nil, errors.New("rpi: OMX audio driver not linked")
	}
	return audioDeps.platformDriverStart(settings)
}
