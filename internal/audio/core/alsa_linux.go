//go:build linux && !android && !pulse && !dummyaudio && !rpi

package core

// Canonical port of src/audio2/alsa.c (the CONFIG_LIBASOUND
// audio_driver_init — configure.linux disables libpulse when libasound
// is enabled, so this is the canonical Linux driver) and
// src/audio2/alsa_default.c (alsa_get_devicename → "default";
// arch_get_avtime lives in pkg/arch).

/*
#cgo LDFLAGS: -lasound
#include <alsa/asoundlib.h>
#include <stdlib.h>
#include <alloca.h>
#include <errno.h>

// *_alloca macros can't be called from Go (alloca frees when the
// helper returns) — use the documented *_malloc variants; callers
// must ml_*_free afterwards.
static int ml_hw_params_alloc(snd_pcm_hw_params_t **p) {
	return snd_pcm_hw_params_malloc(p);
}
static int ml_sw_params_alloc(snd_pcm_sw_params_t **p) {
	return snd_pcm_sw_params_malloc(p);
}
static int ml_status_alloc(snd_pcm_status_t **p) {
	return snd_pcm_status_malloc(p);
}
static long ml_htstamp_sec(snd_htimestamp_t *t) { return t->tv_sec; }
static long ml_htstamp_nsec(snd_htimestamp_t *t) { return t->tv_nsec; }
*/
import "C"

import (
	"errors"
	"time"
	"unsafe"

	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/trace"
)

// errReconfig — C: alsa_audio_reconfig's -1 (open/params failure).
var errReconfig = errors.New("alsa: reconfig failed")

// alsaDecoder — C: decoder_t (alsa.c:31-37). Hangs off
// AudioDecoder.AudioInstance like C's ac_alloc_size embedding.
type alsaDecoder struct {
	h                 *C.snd_pcm_t
	samples           int64
	maxFramesPerWrite int
	tmp               []byte
	pausedOK          bool
}

// alsaGetDevicename — C: alsa_get_devicename — moved to the per-platform
// files alsa_devicename_default.go ("default", alsa_default.c) and
// alsa_devicename_sunxi.go ("hw:1,0", sunxi_alsa.c).

// getALSA — C: (decoder_t *)ad — lazily allocates the embedded driver
// state like C's calloc'd ac_alloc_size.
func (ad *AudioDecoder) getALSA() *alsaDecoder {
	if d, ok := ad.AudioInstance.(*alsaDecoder); ok {
		return d
	}
	d := &alsaDecoder{}
	ad.AudioInstance = d
	return d
}

// alsaAudioFini — C: alsa_audio_fini (alsa.c:42-56).
func alsaAudioFini(ad *AudioDecoder) {
	d := ad.getALSA()
	if d.h != nil {
		C.snd_pcm_close(d.h)
		d.h = nil
		ad.ts.Trace(trace.TRACE_DEBUG, "ALSA", "Closing device")
	}
	d.tmp = nil
}

// alsaAudioReconfig — C: alsa_audio_reconfig (alsa.c:59-133).
func alsaAudioReconfig(ad *AudioDecoder) int {
	d := ad.getALSA()

	alsaAudioFini(ad)

	dev := C.CString(alsaGetDevicename())
	defer C.free(unsafe.Pointer(dev))

	var h *C.snd_pcm_t
	// C: if((r = snd_pcm_open(&h, dev, ..., 0) < 0)) — upstream quirk:
	// the comparison (0/1) is assigned to r, so snd_strerror sees 1.
	openErr := C.snd_pcm_open(&h, dev, C.SND_PCM_STREAM_PLAYBACK, 0)
	r := C.int(0)
	if openErr < 0 {
		r = 1
	}
	if r != 0 {
		ad.ts.Trace(trace.TRACE_ERROR, "ALSA", "Unable to open %s -- %s",
			alsaGetDevicename(), C.GoString(C.snd_strerror(r)))
		return -1
	}

	r = C.snd_pcm_set_params(h, C.SND_PCM_FORMAT_S16,
		C.SND_PCM_ACCESS_RW_INTERLEAVED, 2, 48000, 0, 100000)
	if r < 0 {
		ad.ts.Trace(trace.TRACE_ERROR, "ALSA", "Unable to set params on %s -- %s",
			alsaGetDevicename(), C.GoString(C.snd_strerror(r)))
		return -1
	}

	var hwp *C.snd_pcm_hw_params_t
	C.ml_hw_params_alloc(&hwp)
	defer C.snd_pcm_hw_params_free(hwp)
	C.snd_pcm_hw_params_current(h, hwp)

	var val C.uint
	var dir C.int
	var psize, bsize C.snd_pcm_uframes_t

	C.snd_pcm_hw_params_get_rate(hwp, &val, &dir)
	ad.OutSampleRate = int(val)

	C.snd_pcm_hw_params_get_buffer_size(hwp, &bsize)
	d.maxFramesPerWrite = int(bsize)

	C.snd_pcm_hw_params_get_period_size(hwp, &psize, &dir)
	ad.TileSize = int(psize)
	if q := int(bsize) / 4; q > ad.TileSize {
		ad.TileSize = q
	}

	var swp *C.snd_pcm_sw_params_t
	C.ml_sw_params_alloc(&swp)
	defer C.snd_pcm_sw_params_free(swp)
	C.snd_pcm_sw_params_current(h, swp)
	C.snd_pcm_sw_params_set_avail_min(h, swp, C.snd_pcm_uframes_t(ad.TileSize))
	C.snd_pcm_sw_params(h, swp)

	ad.ts.Trace(trace.TRACE_DEBUG, "ALSA",
		"Opened %s  tile_size=%d, frames_per_write=%d",
		alsaGetDevicename(), ad.TileSize, d.maxFramesPerWrite)

	// C: ad_out_sample_format/rate/channel_layout forced to S16/48k/stereo
	ad.OutSampleFormat = SampleFormatS16
	ad.OutSampleRate = 48000
	ad.OutChannelLayout = ChannelLayoutStereo
	d.h = h

	C.snd_pcm_prepare(d.h)

	channels := 2
	d.tmp = make([]byte, 2*channels*d.maxFramesPerWrite)
	return 0
}

// alsaAudioDeliver — C: alsa_audio_deliver (alsa.c:136-192).
// ac_deliver_unlocked: pulls `samples` frames (clamped by device
// availability) from ad.AVR, anchors mp_audio_clock*, returns 0.
func alsaAudioDeliver(ad *AudioDecoder, samples int, pts int64, epoch int) int {
	d := ad.getALSA()
	mp, _ := ad.MediaPipe.(*mediacore.MediaPipe)

	var c C.snd_pcm_sframes_t
retry:
	c = C.snd_pcm_sframes_t(C.snd_pcm_wait(d.h, 100))
	if c >= 0 {
		c = C.snd_pcm_avail_update(d.h)
	}
	if c == -C.EPIPE {
		C.snd_pcm_prepare(d.h)
		time.Sleep(100 * time.Millisecond)
		ad.ts.Trace(trace.TRACE_DEBUG, "ALSA", "Audio underrun")
		d.samples = 0
		goto retry
	}

	cInt := int(c)
	cInt = min(cInt, d.maxFramesPerWrite)
	cInt = min(cInt, samples)
	cInt = max(cInt, 0)

	// C: c = avresample_read(ad->ad_avr, planes, c) — pull from the
	// resampler into the interleaved tmp buffer.
	if ad.AVR != nil && cInt > 0 {
		need := cInt * ad.AVR.bytesPerSample
		if need > len(d.tmp) {
			need = len(d.tmp)
		}
		cInt = ad.AVR.Read(d.tmp[:need], cInt)
	}

	// C: snd_pcm_status → trigger_htstamp → mp_audio_clock* under
	// mp_clock_mutex (only when pts != AV_NOPTS_VALUE).
	var status *C.snd_pcm_status_t
	C.ml_status_alloc(&status)
	defer C.snd_pcm_status_free(status)
	if C.snd_pcm_status(d.h, status) >= 0 && pts != mediacore.PTSUnset && mp != nil {
		var hts C.snd_htimestamp_t
		C.snd_pcm_status_get_trigger_htstamp(status, &hts)
		ts := int64(C.ml_htstamp_sec(&hts))*1000000 + int64(C.ml_htstamp_nsec(&hts))/1000
		ts += d.samples * 1000000 / int64(ad.OutSampleRate)

		mp.ClockMutex.Lock()
		mp.AudioClockAvtime = ts
		mp.AudioClock = pts
		mp.AudioClockEpoch = int64(epoch)
		mp.ClockMutex.Unlock()
	}

	// C: snd_pcm_delay → ad_delay (µs)
	var fr C.snd_pcm_sframes_t
	if C.snd_pcm_delay(d.h, &fr) == 0 {
		ad.Delay = int(1000000 * int64(fr) / int64(ad.OutSampleRate))
	}

	if cInt > 0 {
		w := C.snd_pcm_writei(d.h, unsafe.Pointer(&d.tmp[0]),
			C.snd_pcm_uframes_t(cInt))
		d.samples += int64(w)
	}
	return 0
}

// alsaAudioPause — C: alsa_audio_pause (alsa.c:196-204).
func alsaAudioPause(ad *AudioDecoder) {
	d := ad.getALSA()
	if d.h == nil {
		return
	}
	r := C.snd_pcm_pause(d.h, 1)
	d.samples = 0
	d.pausedOK = r == 0
}

// alsaAudioPlay — C: alsa_audio_play (alsa.c:208-215).
func alsaAudioPlay(ad *AudioDecoder) {
	d := ad.getALSA()
	if d.h == nil {
		return
	}
	r := C.snd_pcm_pause(d.h, 0)
	// When the driver honors snd_pcm_pause, trigger_htstamp stays
	// anchored to the stream start — mp_audio_clock_avtime then lags
	// by the whole pause interval and the video decoder catches up
	// (fast-forward). On drivers without pause support C reaches the
	// same end state via underrun → -EPIPE → snd_pcm_prepare, which
	// rebases the trigger to now. Reproduce that end state here.
	if d.pausedOK && r == 0 {
		C.snd_pcm_prepare(d.h)
	}
	d.pausedOK = false
	d.samples = 0
}

// alsaAudioFlush — C: alsa_audio_flush (alsa.c:220-228).
func alsaAudioFlush(ad *AudioDecoder) {
	d := ad.getALSA()
	if d.h == nil {
		return
	}
	C.snd_pcm_drop(d.h)
	C.snd_pcm_prepare(d.h)
	d.samples = 0
}

// audioDriverStartPlatform — C: audio_driver_init → &alsa_audio_class
// (alsa.c:233-252). The alsa class implements fini/reconfig/
// deliver_unlocked/pause/play/flush — no ac_get_mode/ac_set_volume/
// ac_deliver_coded_locked.
func audioDriverStartPlatform(settings any) (*AudioClass, error) {
	return &AudioClass{
		Fini: func(ad *AudioDecoder) { alsaAudioFini(ad) },
		Reconfig: func(ad *AudioDecoder) error {
			if alsaAudioReconfig(ad) < 0 {
				return errReconfig
			}
			return nil
		},
		DeliverUnlocked: alsaAudioDeliver,
		Pause:           alsaAudioPause,
		Play:            alsaAudioPlay,
		Flush:           alsaAudioFlush,
	}, nil
}
