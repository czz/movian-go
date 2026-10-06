//go:build (!linux && !android && !darwin && !windows) || (dummyaudio && !rpi)

package core

// Canonical port of src/audio2/dummy_audio.c — the reference driver
// used when no platform driver is compiled (Linux builds alsa or
// pulse instead; this is the fallback provider of audio_driver_init).
//
// Note: C's dummy_audio_deliver only sleeps — it never drains the
// resampler. That canonical behavior is preserved verbatim.

import "time"

// dummyAudioReconfig — C: dummy_audio_reconfig (dummy_audio.c:44-59).
func dummyAudioReconfig(ad *AudioDecoder) int {
	ad.OutSampleFormat = SampleFormatS16
	ad.OutSampleRate = 48000
	ad.OutChannelLayout = ChannelLayoutStereo
	ad.TileSize = 1024
	return 0
}

// dummyAudioDeliver — C: dummy_audio_deliver (dummy_audio.c:62-68).
// Sleeps for the duration of `samples` at 48kHz; does not consume.
func dummyAudioDeliver(ad *AudioDecoder, samples int, pts int64, epoch int) int {
	sleeptime := 1000000 * samples / 48000
	time.Sleep(time.Duration(sleeptime) * time.Microsecond)
	return 0
}

// audioDriverStartPlatform — C: audio_driver_init → &dummy_audio_class
// (dummy_audio.c:89-117).
func audioDriverStartPlatform(settings any) (*AudioClass, error) {
	return &AudioClass{
		Fini:            func(ad *AudioDecoder) {},
		Reconfig:        func(ad *AudioDecoder) error { dummyAudioReconfig(ad); return nil },
		DeliverUnlocked: dummyAudioDeliver,
		Pause:           func(ad *AudioDecoder) {},
		Play:            func(ad *AudioDecoder) {},
		Flush:           func(ad *AudioDecoder) {},
	}, nil
}
