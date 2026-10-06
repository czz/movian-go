//go:build linux && pulse && !rpi

package core

// Canonical port of src/arch/linux/pulseaudio.c — the CONFIG_LIBPULSE
// audio_driver_init (built only when libasound is disabled;
// configure.linux:359 `disable libpulse` when alsa is enabled).
// Selected via the `pulse` build tag.

/*
#cgo pkg-config: libpulse
#include <pulse/pulseaudio.h>
#include <stdlib.h>
#include <string.h>

// Handle-table indirection: callbacks get a uintptr_t userdata (the Go
// decoder can't be handed to C). Registration lives in Go.

extern void paStreamWriteCB(uintptr_t h, size_t length);
extern void paContextErrorCB(char *msg);

extern pa_threaded_mainloop *movian_pa_mainloop;
extern pa_mainloop_api *movian_pa_api;
extern pa_context *movian_pa_ctx;
extern void movian_context_state_callback(pa_context *c, void *userdata);

// C: stream_write_callback (pulseaudio.c:63-76) — the unblocking part
// needs Go (mp_send_cmd); the rest stays here.
static void stream_write_callback(pa_stream *s, size_t length, void *userdata) {
	paStreamWriteCB((uintptr_t)userdata, length);
}

// C: stream_state_callback (pulseaudio.c:80-83).
static void stream_state_callback(pa_stream *s, void *userdata) {
	pa_threaded_mainloop_signal(movian_pa_mainloop, 0);
}

static void *ml_stream_new_extended(pa_format_info **vec, int n) {
	return pa_stream_new_extended(movian_pa_ctx, "Movian Go playback", vec, n, NULL);
}
static void *ml_stream_new_pcm(pa_sample_spec *ss, pa_channel_map *map, int video) {
	pa_proplist *pl = pa_proplist_new();
	pa_proplist_sets(pl, PA_PROP_MEDIA_ROLE, video ? "video" : "music");
	void *s = pa_stream_new_with_proplist(movian_pa_ctx, "Movian Go playback", ss, map, pl);
	pa_proplist_free(pl);
	return s;
}
static pa_context *ml_context_new(void) {
	pa_proplist *pl = pa_proplist_new();
	pa_proplist_sets(pl, PA_PROP_APPLICATION_ID, "com.moviango.movian");
	pa_proplist_sets(pl, PA_PROP_APPLICATION_NAME, "Movian Go");
	pa_context *c = pa_context_new_with_proplist(movian_pa_api, "Movian", pl);
	pa_proplist_free(pl);
	return c;
}
static void ml_set_stream_callbacks(pa_stream *s, uintptr_t h) {
	pa_stream_set_state_callback(s, stream_state_callback, (void*)h);
	pa_stream_set_write_callback(s, stream_write_callback, (void*)h);
}
static const char *ml_headers_version(void) { return pa_get_headers_version(); }
static void ml_cmap_push(pa_channel_map *m, pa_channel_position_t p) {
	m->map[m->channels++] = p;
}
static int ml_cmap_channels(pa_channel_map *m) { return m->channels; }
static long ml_ti_sec(const pa_timing_info *ti) { return ti->timestamp.tv_sec; }
static long ml_ti_usec(const pa_timing_info *ti) { return ti->timestamp.tv_usec; }
static long ml_ti_sink_usec(const pa_timing_info *ti) { return ti->sink_usec; }
static long ml_ti_transport_usec(const pa_timing_info *ti) { return ti->transport_usec; }
static long long ml_ti_widx(const pa_timing_info *ti) { return ti->write_index; }
static long long ml_ti_ridx(const pa_timing_info *ti) { return ti->read_index; }
*/
import "C"

import (
	"errors"
	"unsafe"

	mediacore "github.com/czz/movian-go/internal/media/core"
	medialibav "github.com/czz/movian-go/internal/media/libav"
	"github.com/czz/movian-go/internal/trace"
)

// C: AV_CH_* layout bits (av2pa_map input masks)
const (
	avChFrontLeft          = 0x1
	avChFrontRight         = 0x2
	avChFrontCenter        = 0x4
	avChLowFrequency       = 0x8
	avChBackLeft           = 0x10
	avChBackRight          = 0x20
	avChFrontLeftOfCenter  = 0x40
	avChFrontRightOfCenter = 0x80
	avChBackCenter         = 0x100
	avChSideLeft           = 0x200
	avChSideRight          = 0x400
	avChTopCenter          = 0x800
	avChTopFrontLeft       = 0x1000
	avChTopFrontCenter     = 0x2000
	avChTopFrontRight      = 0x4000
	avChTopBackLeft        = 0x8000
	avChTopBackCenter      = 0x10000
	avChTopBackRight       = 0x20000
	avChStereoLeft         = 0x20000000
	avChStereoRight        = 0x40000000
)

// av2paMap — C: av2pa_map (pulseaudio.c:88-110).
var av2paMap = []struct {
	avmask int64
	papos  C.pa_channel_position_t
}{
	{avChFrontLeft, C.PA_CHANNEL_POSITION_FRONT_LEFT},
	{avChFrontRight, C.PA_CHANNEL_POSITION_FRONT_RIGHT},
	{avChFrontCenter, C.PA_CHANNEL_POSITION_FRONT_CENTER},
	{avChLowFrequency, C.PA_CHANNEL_POSITION_LFE},
	{avChBackLeft, C.PA_CHANNEL_POSITION_REAR_LEFT},
	{avChBackRight, C.PA_CHANNEL_POSITION_REAR_RIGHT},
	{avChFrontLeftOfCenter, C.PA_CHANNEL_POSITION_FRONT_LEFT_OF_CENTER},
	{avChFrontRightOfCenter, C.PA_CHANNEL_POSITION_FRONT_RIGHT_OF_CENTER},
	{avChBackCenter, C.PA_CHANNEL_POSITION_REAR_CENTER},
	{avChSideLeft, C.PA_CHANNEL_POSITION_SIDE_LEFT},
	{avChSideRight, C.PA_CHANNEL_POSITION_SIDE_RIGHT},
	{avChTopCenter, C.PA_CHANNEL_POSITION_TOP_CENTER},
	{avChTopFrontLeft, C.PA_CHANNEL_POSITION_TOP_FRONT_LEFT},
	{avChTopFrontCenter, C.PA_CHANNEL_POSITION_TOP_FRONT_CENTER},
	{avChTopFrontRight, C.PA_CHANNEL_POSITION_TOP_FRONT_RIGHT},
	{avChTopBackLeft, C.PA_CHANNEL_POSITION_TOP_REAR_LEFT},
	{avChTopBackCenter, C.PA_CHANNEL_POSITION_TOP_REAR_CENTER},
	{avChTopBackRight, C.PA_CHANNEL_POSITION_TOP_REAR_RIGHT},
	{avChStereoLeft, C.PA_CHANNEL_POSITION_FRONT_LEFT},
	{avChStereoRight, C.PA_CHANNEL_POSITION_FRONT_RIGHT},
}

// pulseDecoder — C: decoder_t (pulseaudio.c:40-47).
type pulseDecoder struct {
	s         *C.pa_stream
	framesize int
	ss        C.pa_sample_spec
	blocked   bool
	ad        *AudioDecoder // C: &d->ad (for mp access in callbacks)
	h         uintptr       // callback handle-table key (Go seam)
}

// C: movian_pa_ctx globals + user_errmsg + the Go-only callback handle
// table live on audioDeps (//export cgo callbacks have no userdata;
// Go ptrs can't reach C). paErrSent == C user_errmsg != NULL
// (pulseaudio.c:118-123): notify only the first failure, cleared on
// READY.
func paRegister(d *pulseDecoder) uintptr {
	audioDeps.paHandlesM.Lock()
	defer audioDeps.paHandlesM.Unlock()
	if audioDeps.paHandles == nil {
		audioDeps.paHandles = map[uintptr]any{}
	}
	audioDeps.paNextH++
	audioDeps.paHandles[audioDeps.paNextH] = d
	return audioDeps.paNextH
}

func paLookup(h uintptr) *pulseDecoder {
	audioDeps.paHandlesM.Lock()
	defer audioDeps.paHandlesM.Unlock()
	d, _ := audioDeps.paHandles[h].(*pulseDecoder)
	return d
}

func paUnregister(h uintptr) {
	audioDeps.paHandlesM.Lock()
	defer audioDeps.paHandlesM.Unlock()
	delete(audioDeps.paHandles, h)
}

// paReregister drops the decoder's previous callback handle (the old
// stream was disconnected) before registering a fresh one — otherwise
// paHandles grows by one entry per reconfig/get_mode call.
func paReregister(d *pulseDecoder) {
	if d.h != 0 {
		paUnregister(d.h)
	}
	d.h = paRegister(d)
}

// PulseUserError — C: pulseaudio_error's user_errmsg notify_add seam.
// Called once per failure (like C's NULL guard); cleared on READY.
// SetPulseUserError wires the pulse-init error popup
// (C: pulse_init error path — linux tag).
func SetPulseUserError(fn func(msg string)) { audioDeps.pulseUserError = fn }

//export paContextErrorCB
func paContextErrorCB(msg *C.char) {
	s := C.GoString(msg)
	if audioDeps.pulseUserError != nil {
		audioDeps.pulseUserError("Unable to initialize audio system Pulseaudio -- " + s)
	}
	if audioDeps.ts != nil {
		audioDeps.ts.Trace(trace.TRACE_ERROR, "PA", "%s", s)
	}
}

//export paStreamWriteCB
func paStreamWriteCB(h C.uintptr_t, length C.size_t) {
	d := paLookup(uintptr(h))
	if d == nil || d.s == nil {
		return
	}
	mp, _ := d.ad.MediaPipe.(*mediacore.MediaPipe)
	writable := C.pa_stream_writable_size(d.s)
	if writable != 0 && d.blocked {
		d.blocked = false
		if mp != nil {
			mediacore.MpSendCmd(mp, mp.Audio, int(mediacore.MBCtrlUnblock))
		}
	}
}

// pulseaudioMakeContextReady — C: pulseaudio_make_context_ready
// (pulseaudio.c:131-168).
func pulseaudioMakeContextReady() int {
	for {
		switch C.pa_context_get_state(C.movian_pa_ctx) {
		case C.PA_CONTEXT_UNCONNECTED:
			if C.pa_context_connect(C.movian_pa_ctx, nil, 0, nil) < 0 {
				// C: if(user_errmsg == NULL) pulseaudio_error(...)
				if !audioDeps.paErrSent && audioDeps.pulseUserError != nil {
					audioDeps.pulseUserError("Unable to initialize audio system Pulseaudio -- " +
						C.GoString(C.pa_strerror(C.pa_context_errno(C.movian_pa_ctx))))
					audioDeps.paErrSent = true
				}
				C.pa_threaded_mainloop_unlock(C.movian_pa_mainloop)
				return -1
			}
			C.pa_threaded_mainloop_wait(C.movian_pa_mainloop)
		case C.PA_CONTEXT_CONNECTING, C.PA_CONTEXT_AUTHORIZING,
			C.PA_CONTEXT_SETTING_NAME:
			C.pa_threaded_mainloop_wait(C.movian_pa_mainloop)
		case C.PA_CONTEXT_FAILED, C.PA_CONTEXT_TERMINATED:
			return -1
		case C.PA_CONTEXT_READY:
			// C: prop_destroy(user_errmsg); user_errmsg = NULL
			audioDeps.paErrSent = false
			return 0
		}
	}
}

// pulseaudioGetMode — C: pulseaudio_get_mode (pulseaudio.c:173-241).
// Negotiates an IEC61937 passthru stream; returns AUDIO_MODE_SPDIF or
// AUDIO_MODE_PCM (0).
func pulseaudioGetMode(ad *AudioDecoder, codecID int, extradata []byte) (AudioMode, error) {
	d := ad.getPulse()
	var e C.pa_encoding_t
	switch codecID {
	case medialibav.AVCodecIDDts:
		e = C.PA_ENCODING_DTS_IEC61937
	case medialibav.AVCodecIDAc3:
		e = C.PA_ENCODING_AC3_IEC61937
	case medialibav.AVCodecIDEac3:
		e = C.PA_ENCODING_EAC3_IEC61937
	default:
		return ModePCM, nil
	}

	C.pa_threaded_mainloop_lock(C.movian_pa_mainloop)
	if pulseaudioMakeContextReady() != 0 {
		C.pa_threaded_mainloop_unlock(C.movian_pa_mainloop)
		return ModePCM, nil
	}

	if d.s != nil {
		C.pa_stream_disconnect(d.s)
		C.pa_stream_unref(d.s)
	}

	vec := C.pa_format_info_new()
	vec.encoding = e
	C.pa_format_info_set_rate(vec, 48000)
	C.pa_format_info_set_channels(vec, 2)
	var vecs [1]*C.pa_format_info
	vecs[0] = vec

	d.s = (*C.pa_stream)(C.ml_stream_new_extended(&vecs[0], 1))

	paReregister(d)
	C.ml_set_stream_callbacks(d.s, C.uintptr_t(d.h))

	flags := C.PA_STREAM_AUTO_TIMING_UPDATE | C.PA_STREAM_INTERPOLATE_TIMING |
		C.PA_STREAM_NOT_MONOTONIC
	C.pa_stream_connect_playback(d.s, nil, nil, C.pa_stream_flags_t(flags), nil, nil)

	for {
		switch C.pa_stream_get_state(d.s) {
		case C.PA_STREAM_UNCONNECTED, C.PA_STREAM_CREATING:
			C.pa_threaded_mainloop_wait(C.movian_pa_mainloop)
		case C.PA_STREAM_READY:
			C.pa_threaded_mainloop_unlock(C.movian_pa_mainloop)
			ad.TileSize = 512
			d.ss = *C.pa_stream_get_sample_spec(d.s)
			return ModeSPDIF, nil
		case C.PA_STREAM_TERMINATED, C.PA_STREAM_FAILED:
			C.pa_threaded_mainloop_unlock(C.movian_pa_mainloop)
			paUnregister(d.h)
			d.h = 0
			return ModePCM, nil
		}
	}
}

// pulseaudioAudioReconfig — C: pulseaudio_audio_reconfig
// (pulseaudio.c:246-363).
func pulseaudioAudioReconfig(ad *AudioDecoder) int {
	d := ad.getPulse()

	C.pa_threaded_mainloop_lock(C.movian_pa_mainloop)
	if pulseaudioMakeContextReady() != 0 {
		C.pa_threaded_mainloop_unlock(C.movian_pa_mainloop)
		return -1
	}

	if d.s != nil {
		C.pa_stream_disconnect(d.s)
		C.pa_stream_unref(d.s)
	}

	var cmap C.pa_channel_map

	ad.OutSampleRate = ad.InSampleRate
	d.ss.rate = C.uint32_t(ad.InSampleRate)

	ad.OutSampleFormat = SampleFormatFLT
	d.ss.format = C.PA_SAMPLE_FLOAT32NE
	d.framesize = 4 // sizeof(float)

	switch ad.InChannelLayout {
	case ChannelLayoutMono:
		d.ss.channels = 1
		ad.OutChannelLayout = ChannelLayoutMono
		C.pa_channel_map_init_mono(&cmap)
	case ChannelLayoutStereo:
		d.ss.channels = 2
		ad.OutChannelLayout = ChannelLayoutStereo
		C.pa_channel_map_init_stereo(&cmap)
	default:
		// C: pa_channel_map_init + iterate av2pa_map. The C has a
		// missing break after STEREO (pulseaudio.c:291-298) so the
		// default branch also runs for stereo — harmless there
		// (av2pa_map yields the same FL+FR map and channel count);
		// Go's explicit cases produce the identical end state.
		C.pa_channel_map_init(&cmap)
		ad.OutChannelLayout = 0
		for _, m := range av2paMap {
			if ad.InChannelLayout&m.avmask != 0 {
				ad.OutChannelLayout |= m.avmask
				C.ml_cmap_push(&cmap, m.papos)
			}
		}
		d.ss.channels = C.uint8_t(C.ml_cmap_channels(&cmap))
	}

	d.framesize *= int(d.ss.channels)
	if d.framesize <= 0 {
		// C would SIGFPE here (tile_size / framesize) when the input
		// layout maps to zero channels — fail instead.
		C.pa_threaded_mainloop_unlock(C.movian_pa_mainloop)
		return -1
	}
	ad.TileSize = int(C.pa_context_get_tile_size(C.movian_pa_ctx, &d.ss)) / d.framesize

	var mbuf [100]C.char
	var mbuf2 [C.PA_CHANNEL_MAP_SNPRINT_MAX]C.char
	ad.ts.Trace(trace.TRACE_DEBUG, "PA", "Created stream %s [%s] (tilesize=%d)",
		C.GoString(C.pa_sample_spec_snprint(&mbuf[0], 100, &d.ss)),
		C.GoString(C.pa_channel_map_snprint(&mbuf2[0], C.PA_CHANNEL_MAP_SNPRINT_MAX, &cmap)),
		ad.TileSize)

	mp, _ := ad.MediaPipe.(*mediacore.MediaPipe)
	video := 0
	if mp != nil && mp.Flags&mediacore.MPVideo != 0 {
		video = 1
	}
	d.s = (*C.pa_stream)(C.ml_stream_new_pcm(&d.ss, &cmap, C.int(video)))

	paReregister(d)
	C.ml_set_stream_callbacks(d.s, C.uintptr_t(d.h))

	flags := C.PA_STREAM_AUTO_TIMING_UPDATE | C.PA_STREAM_INTERPOLATE_TIMING
	C.pa_stream_connect_playback(d.s, nil, nil, C.pa_stream_flags_t(flags), nil, nil)

	for {
		switch C.pa_stream_get_state(d.s) {
		case C.PA_STREAM_UNCONNECTED, C.PA_STREAM_CREATING:
			C.pa_threaded_mainloop_wait(C.movian_pa_mainloop)
		case C.PA_STREAM_READY:
			C.pa_threaded_mainloop_unlock(C.movian_pa_mainloop)
			return 0
		case C.PA_STREAM_TERMINATED, C.PA_STREAM_FAILED:
			C.pa_threaded_mainloop_unlock(C.movian_pa_mainloop)
			paUnregister(d.h)
			d.h = 0
			return 1
		}
	}
}

// pulseaudioAudioCork — C: pulseaudio_audio_cork (pulseaudio.c:370-380).
func pulseaudioAudioCork(ad *AudioDecoder, b int) {
	d := ad.getPulse()
	if d.s == nil {
		return
	}
	C.pa_threaded_mainloop_lock(C.movian_pa_mainloop)
	o := C.pa_stream_cork(d.s, C.int(b), nil, nil)
	if o != nil {
		C.pa_operation_unref(o)
	}
	C.pa_threaded_mainloop_unlock(C.movian_pa_mainloop)
}

// pulseaudioAudioFlush — C: pulseaudio_audio_flush (pulseaudio.c:407-420).
func pulseaudioAudioFlush(ad *AudioDecoder) {
	d := ad.getPulse()
	if d.s == nil {
		return
	}
	C.pa_threaded_mainloop_lock(C.movian_pa_mainloop)
	o := C.pa_stream_flush(d.s, nil, nil)
	if o != nil {
		C.pa_operation_unref(o)
	}
	C.pa_threaded_mainloop_unlock(C.movian_pa_mainloop)
}

// pulseaudioAudioDeliver — C: pulseaudio_audio_deliver
// (pulseaudio.c:425-503).
func pulseaudioAudioDeliver(ad *AudioDecoder, samples int, pts int64, epoch int) int {
	d := ad.getPulse()
	if d.s == nil {
		return 0
	}

	var bytes C.size_t
	if ad.SPDIFMuxer != nil {
		bytes = C.size_t(ad.SPDIFFrameSize)
	} else {
		bytes = C.size_t(samples * d.framesize)
	}

	C.pa_threaded_mainloop_lock(C.movian_pa_mainloop)

	if C.pa_stream_writable_size(d.s) == 0 {
		d.blocked = true
		C.pa_threaded_mainloop_unlock(C.movian_pa_mainloop)
		return 1
	}

	var buf unsafe.Pointer
	C.pa_stream_begin_write(d.s, &buf, &bytes)

	if ad.SPDIFMuxer != nil {
		if ad.SPDIFFrameSize > 0 {
			C.memcpy(buf, unsafe.Pointer(&ad.SPDIFFrame[0]), C.size_t(ad.SPDIFFrameSize))
		}
	} else {
		rsamples := int(bytes) / d.framesize
		if rsamples > samples {
			rsamples = samples
		}
		if ad.AVR != nil && rsamples > 0 {
			// C: avresample_read(ad->ad_avr, data, rsamples) into buf
			bs := unsafe.Slice((*byte)(buf), int(bytes))
			ad.AVR.Read(bs, rsamples)
		}

		// C: float volume scale — master_mute ? 0 : master_volume *
		// vol_scale, applied to `floats` float32 samples. C uses
		// `samples` (not rsamples) — capped at rsamples for safety
		// (C's unbounded version can write past the PA buffer).
		var s float32
		if ad.manager != nil && ad.manager.masterMute == 0 {
			s = ad.manager.masterVolume * ad.VolScale
		}
		floats := rsamples * int(d.ss.channels)
		x := unsafe.Slice((*float32)(buf), floats)
		for i := range floats {
			x[i] *= s
		}
	}

	if pts != mediacore.PTSUnset {
		mp, _ := ad.MediaPipe.(*mediacore.MediaPipe)
		if mp != nil {
			mp.ClockMutex.Lock()
			ti := C.pa_stream_get_timing_info(d.s)
			if ti != nil {
				mp.AudioClockAvtime = int64(C.ml_ti_sec(ti))*1000000 +
					int64(C.ml_ti_usec(ti))
				busec := C.pa_bytes_to_usec(
					C.uint64_t(C.ml_ti_widx(ti)-C.ml_ti_ridx(ti)), &d.ss)
				ad.Delay = int(int64(C.ml_ti_sink_usec(ti)) + int64(busec) +
					int64(C.ml_ti_transport_usec(ti)))
				mp.AudioClock = pts - int64(ad.Delay)
				mp.AudioClockEpoch = int64(epoch)
			}
			mp.ClockMutex.Unlock()
		}
	}

	C.pa_stream_write(d.s, buf, bytes, nil, 0, C.PA_SEEK_RELATIVE)
	ad.SPDIFFrameSize = 0

	C.pa_threaded_mainloop_unlock(C.movian_pa_mainloop)
	return 0
}

// pulseaudioFini — C: pulseaudio_fini (pulseaudio.c:507-524).
func pulseaudioFini(ad *AudioDecoder) {
	d := ad.getPulse()
	C.pa_threaded_mainloop_lock(C.movian_pa_mainloop)
	if d.s != nil {
		C.pa_stream_disconnect(d.s)
		for C.pa_stream_get_state(d.s) != C.PA_STREAM_TERMINATED &&
			C.pa_stream_get_state(d.s) != C.PA_STREAM_FAILED {
			C.pa_threaded_mainloop_wait(C.movian_pa_mainloop)
		}
		C.pa_stream_unref(d.s)
		d.s = nil
	}
	C.pa_threaded_mainloop_unlock(C.movian_pa_mainloop)
	if d.h != 0 {
		paUnregister(d.h)
		d.h = 0
	}
}

// getPulse — C: (decoder_t *)ad — lazily allocates the embedded driver
// state like C's calloc'd ac_alloc_size.
func (ad *AudioDecoder) getPulse() *pulseDecoder {
	if d, ok := ad.AudioInstance.(*pulseDecoder); ok {
		return d
	}
	d := &pulseDecoder{ad: ad}
	ad.AudioInstance = d
	return d
}

// audioDriverStartPlatform — C: audio_driver_init →
// &pulseaudio_audio_class (pulseaudio.c:583-613).
func audioDriverStartPlatform(settings any) (*AudioClass, error) {
	if audioDeps.ts != nil {
		audioDeps.ts.Trace(trace.TRACE_DEBUG, "PA", "Headerversion: %s, library: %s",
			C.GoString(C.ml_headers_version()),
			C.GoString(C.pa_get_library_version()))
	}

	C.movian_pa_mainloop = C.pa_threaded_mainloop_new()
	C.movian_pa_api = C.pa_threaded_mainloop_get_api(C.movian_pa_mainloop)

	C.pa_threaded_mainloop_lock(C.movian_pa_mainloop)
	C.pa_threaded_mainloop_start(C.movian_pa_mainloop)

	C.movian_pa_ctx = C.ml_context_new()
	C.pa_context_set_state_callback(C.movian_pa_ctx, C.pa_context_notify_cb_t(C.movian_context_state_callback), nil)

	C.pa_threaded_mainloop_unlock(C.movian_pa_mainloop)

	return &AudioClass{
		Fini: func(ad *AudioDecoder) { pulseaudioFini(ad) },
		// C: ac_reconfig returns -1 (context) or 1 (stream) on failure.
		Reconfig: func(ad *AudioDecoder) error {
			if pulseaudioAudioReconfig(ad) != 0 {
				return errPulseReconfig
			}
			return nil
		},
		DeliverUnlocked: pulseaudioAudioDeliver,
		Pause:           func(ad *AudioDecoder) { pulseaudioAudioCork(ad, 1) },
		Play:            func(ad *AudioDecoder) { pulseaudioAudioCork(ad, 0) },
		Flush:           pulseaudioAudioFlush,
		GetMode:         pulseaudioGetMode,
	}, nil
}

// errPulseReconfig — C: pulseaudio_audio_reconfig's -1.
var errPulseReconfig = errors.New("pulseaudio: reconfig failed")
