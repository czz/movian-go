//go:build windows

package core

// WASAPI audio driver — NO upstream C counterpart (upstream never
// shipped win32 audio; mac_audio.c's AudioQueue driver is the closest
// model). Shared-mode event-driven IAudioClient on the default render
// endpoint: the DeliverUnlocked seam maps to GetCurrentPadding +
// IAudioRenderClient GetBuffer/ReleaseBuffer, the clock anchor maps to
// IAudioClock::GetPosition, volume to ISimpleAudioVolume.

/*
#cgo windows LDFLAGS: -lole32 -luuid -lksuser -lavrt
#define COBJMACROS
#define CINTERFACE
#include <windows.h>
#include <mmdeviceapi.h>
#include <audioclient.h>
#include <avrt.h>
#include <initguid.h>
#include <string.h>
#include <stdint.h>

// The WASAPI GUIDs are not in mingw's libuuid — define them locally
// (values from the SDK headers).
DEFINE_GUID(CLSID_MMDeviceEnumerator, 0xBCDE0395, 0xE52F, 0x467C,
            0x8E, 0x3D, 0xC4, 0x57, 0x92, 0x91, 0x69, 0x2E);
DEFINE_GUID(IID_IMMDeviceEnumerator, 0xA95664D2, 0x9614, 0x4F35,
            0xA7, 0x46, 0xDE, 0x8D, 0xB6, 0x36, 0x17, 0xE6);
DEFINE_GUID(IID_IAudioClient, 0x1CB9AD4C, 0xDBFA, 0x4c32,
            0xB1, 0x78, 0xC2, 0xF5, 0x68, 0xA7, 0x03, 0xB2);
DEFINE_GUID(IID_IAudioRenderClient, 0xF294ACFC, 0x3146, 0x4483,
            0xA7, 0xBF, 0xAD, 0xDC, 0xA7, 0xC2, 0x60, 0xE2);
DEFINE_GUID(IID_ISimpleAudioVolume, 0x87FB5498, 0x68A6, 0x4E40,
            0x92, 0x15, 0x01, 0x47, 0xA5, 0xA1, 0x34, 0xDC);
DEFINE_GUID(IID_IAudioClock, 0xCD63314F, 0x3FBA, 0x4a1b,
            0x81, 0x2C, 0xEF, 0x96, 0x35, 0x87, 0x28, 0xE7);

typedef struct {
    IAudioClient        *client;
    IAudioRenderClient  *render;
    ISimpleAudioVolume  *vol;
    IAudioClock         *clock;
    HANDLE               event;
    uint32_t             buf_frames;
    uint32_t             frame_bytes;
    uint32_t             rate;
} ml_wasapi;

// ml_wasapi_open — CoCreateInstance(MMDeviceEnumerator) →
// GetDefaultAudioEndpoint(eRender) → Activate(IAudioClient) →
// Initialize(shared, EVENTCALLBACK, ~40ms) → SetEventHandle →
// GetService(render/vol/clock). fmt: float32 PCM at the engine mix
// rate (caller's resampler — ad.AVR — converts).
static int
ml_wasapi_open(ml_wasapi *w, uint32_t rate, uint16_t channels)
{
    IMMDeviceEnumerator *en = NULL;
    IMMDevice *dev = NULL;
    WAVEFORMATEX *mix = NULL, wfx;
    REFERENCE_TIME dur = 400000; // 40ms in 100ns units
    HRESULT hr;

    memset(w, 0, sizeof(*w));
    w->event = CreateEvent(NULL, FALSE, FALSE, NULL);
    if(w->event == NULL)
        return -1;

    CoInitializeEx(NULL, COINIT_MULTITHREADED);

    hr = CoCreateInstance(&CLSID_MMDeviceEnumerator, NULL,
        CLSCTX_ALL, &IID_IMMDeviceEnumerator, (void **)&en);
    if(FAILED(hr))
        return -2;
    hr = en->lpVtbl->GetDefaultAudioEndpoint(en, eRender,
        eMultimedia, &dev);
    en->lpVtbl->Release(en);
    if(FAILED(hr))
        return -3;
    hr = dev->lpVtbl->Activate(dev, &IID_IAudioClient, CLSCTX_ALL,
        NULL, (void **)&w->client);
    dev->lpVtbl->Release(dev);
    if(FAILED(hr))
        return -4;

    hr = w->client->lpVtbl->GetMixFormat(w->client, &mix);
    if(FAILED(hr))
        return -5;
    rate = mix->nSamplesPerSec; // shared mode: engine rate wins
    w->rate = rate;
    CoTaskMemFree(mix);

    memset(&wfx, 0, sizeof(wfx));
    wfx.wFormatTag = WAVE_FORMAT_IEEE_FLOAT;
    wfx.nChannels = channels;
    wfx.nSamplesPerSec = rate;
    wfx.wBitsPerSample = 32;
    wfx.nBlockAlign = wfx.nChannels * 4;
    wfx.nAvgBytesPerSec = rate * wfx.nBlockAlign;

    hr = w->client->lpVtbl->Initialize(w->client,
        AUDCLNT_SHAREMODE_SHARED,
        AUDCLNT_STREAMFLAGS_EVENTCALLBACK, dur, 0, &wfx, NULL);
    if(FAILED(hr))
        return -6;
    w->client->lpVtbl->SetEventHandle(w->client, w->event);
    w->client->lpVtbl->GetBufferSize(w->client, &w->buf_frames);
    w->frame_bytes = wfx.nBlockAlign;

    hr = w->client->lpVtbl->GetService(w->client,
        &IID_IAudioRenderClient, (void **)&w->render);
    if(FAILED(hr))
        return -7;
    w->client->lpVtbl->GetService(w->client,
        &IID_ISimpleAudioVolume, (void **)&w->vol);
    w->client->lpVtbl->GetService(w->client,
        &IID_IAudioClock, (void **)&w->clock);
    return 0;
}

// ml_wasapi_padding — frames currently queued (GetCurrentPadding).
static uint32_t
ml_wasapi_padding(ml_wasapi *w)
{
    uint32_t p = 0;
    if(w->client)
        w->client->lpVtbl->GetCurrentPadding(w->client, &p);
    return p;
}

// ml_wasapi_write — write `frames` interleaved frames; returns frames
// actually written (clamped by available space).
static uint32_t
ml_wasapi_write(ml_wasapi *w, const void *data, uint32_t frames)
{
    BYTE *buf = NULL;
    uint32_t pad = 0, avail;

    w->client->lpVtbl->GetCurrentPadding(w->client, &pad);
    avail = w->buf_frames - pad;
    if(frames > avail)
        frames = avail;
    if(frames == 0)
        return 0;
    if(FAILED(w->render->lpVtbl->GetBuffer(w->render, frames, &buf)))
        return 0;
    memcpy(buf, data, frames * w->frame_bytes);
    w->render->lpVtbl->ReleaseBuffer(w->render, frames, 0);
    return frames;
}

// ml_wasapi_position — IAudioClock::GetPosition: device position in
// frames + the QPC timestamp it corresponds to (100ns units).
static int
ml_wasapi_position(ml_wasapi *w, uint64_t *pos, uint64_t *qpc)
{
    if(w->clock == NULL)
        return -1;
    return FAILED(w->clock->lpVtbl->GetPosition(w->clock, pos, qpc))
        ? -1 : 0;
}

static void ml_wasapi_start(ml_wasapi *w)
{
    if(w->client)
        w->client->lpVtbl->Start(w->client);
}
static void ml_wasapi_stop(ml_wasapi *w)
{
    if(w->client)
        w->client->lpVtbl->Stop(w->client);
}
static void ml_wasapi_reset(ml_wasapi *w)
{
    if(w->client)
        w->client->lpVtbl->Reset(w->client);
}
static void ml_wasapi_volume(ml_wasapi *w, float level)
{
    if(w->vol)
        w->vol->lpVtbl->SetMasterVolume(w->vol, level, NULL);
}

static void
ml_wasapi_close(ml_wasapi *w)
{
    if(w->client) {
        w->client->lpVtbl->Stop(w->client);
        w->client->lpVtbl->Release(w->client);
    }
    if(w->render) w->render->lpVtbl->Release(w->render);
    if(w->vol)    w->vol->lpVtbl->Release(w->vol);
    if(w->clock)  w->clock->lpVtbl->Release(w->clock);
    if(w->event)  CloseHandle(w->event);
}
*/
import "C"

import (
	"unsafe"

	archpkg "github.com/czz/movian-go/internal/arch"
	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/trace"
)

// wasapiDecoder — hangs off AudioDecoder.AudioInstance like C's
// ac_alloc_size embedding (mac_audio.c decoder_t role).
type wasapiDecoder struct {
	owner     *AudioDecoder
	w         C.ml_wasapi
	framesize int
	tmp       []byte
	samples   int64 // frames written since (re)start
	underrun  int
}

func (ad *AudioDecoder) getWasapi() *wasapiDecoder {
	if d, ok := ad.AudioInstance.(*wasapiDecoder); ok {
		return d
	}
	d := &wasapiDecoder{owner: ad}
	ad.AudioInstance = d
	return d
}

// wasapiAudioReconfig — mac_audio_reconfig's role: close the old
// client, open on the engine mix rate, start.
func wasapiAudioReconfig(ad *AudioDecoder) int {
	d := ad.getWasapi()

	if d.w.client != nil {
		C.ml_wasapi_close(&d.w)
		d.w.client = nil
	}

	ad.OutChannelLayout = ChannelLayoutStereo
	ad.OutSampleFormat = SampleFormatFLT // WASAPI shared native
	ad.OutSampleRate = ad.InSampleRate   // provisional

	r := C.ml_wasapi_open(&d.w, C.uint32_t(ad.InSampleRate), C.uint16_t(2))
	if r != 0 {
		ad.ts.Trace(trace.TRACE_ERROR, "WASAPI",
			"open failed (%d) — audio disabled", int(r))
		return 1
	}
	// Shared mode pins the engine mix rate — read it back (it may
	// differ from what we asked for; AVR converts).
	ad.OutSampleRate = int(d.w.rate)
	d.framesize = int(d.w.frame_bytes)
	d.tmp = make([]byte, 4096*d.framesize)
	d.samples = 0

	ad.ts.Trace(trace.TRACE_DEBUG, "WASAPI", "Start %d Hz",
		ad.OutSampleRate)
	C.ml_wasapi_start(&d.w)
	return 0
}

// wasapiAudioDeliver — alsa_audio_deliver's role: wait for buffer
// space (event-driven instead of snd_pcm_wait), pull from ad.AVR,
// write, anchor the audio clock, report ad.Delay. Returns 0.
func wasapiAudioDeliver(ad *AudioDecoder, samples int, pts int64,
	epoch int) int {
	d := ad.getWasapi()
	mp, _ := ad.MediaPipe.(*mediacore.MediaPipe)
	if d.w.client == nil {
		return -1
	}

	// C: snd_pcm_wait(h, 100) — WaitForSingleObject on the event
	// handle (signaled when buffer space is available).
	pad := int(C.ml_wasapi_padding(&d.w))
	avail := int(d.w.buf_frames) - pad
	if avail <= 0 {
		C.WaitForSingleObject(d.w.event, 100)
		pad = int(C.ml_wasapi_padding(&d.w))
		avail = int(d.w.buf_frames) - pad
		if avail <= 0 {
			return 100 // C: retry path — wait 100ms on mq_avail
		}
	}

	cInt := avail
	if cInt > samples {
		cInt = samples
	}
	if cap(d.tmp) < cInt*d.framesize {
		d.tmp = make([]byte, cInt*d.framesize)
	}
	if ad.AVR != nil && cInt > 0 {
		need := cInt * ad.AVR.bytesPerSample
		if need > len(d.tmp) {
			need = len(d.tmp)
		}
		cInt = ad.AVR.Read(d.tmp[:need], cInt)
	}

	// Clock anchor — IAudioClock::GetPosition gives the device play
	// position in frames + the QPC it maps to: the frames being
	// written now will play at avtime ≈ now + delay. Same anchor
	// semantics as alsa's trigger_htstamp + samples.
	if pts != mediacore.PTSUnset && mp != nil {
		ad.Delay = int(1000000 * int64(pad) / int64(ad.OutSampleRate))
		mp.ClockMutex.Lock()
		mp.AudioClockAvtime = archpkg.GetTS() + int64(ad.Delay)
		mp.AudioClock = pts
		mp.AudioClockEpoch = int64(epoch)
		mp.ClockMutex.Unlock()
	}

	if cInt > 0 {
		w := int(C.ml_wasapi_write(&d.w, unsafe.Pointer(&d.tmp[0]),
			C.uint32_t(cInt)))
		d.samples += int64(w)
	}
	return 0
}

func wasapiAudioFini(ad *AudioDecoder) {
	d := ad.getWasapi()
	if d.w.client != nil {
		C.ml_wasapi_close(&d.w)
		d.w.client = nil
	}
}

func wasapiAudioPause(ad *AudioDecoder) {
	d := ad.getWasapi()
	C.ml_wasapi_stop(&d.w)
}

func wasapiAudioPlay(ad *AudioDecoder) {
	d := ad.getWasapi()
	if d.w.client != nil {
		C.ml_wasapi_start(&d.w)
	}
}

func wasapiAudioFlush(ad *AudioDecoder) {
	d := ad.getWasapi()
	if d.w.client != nil {
		C.ml_wasapi_stop(&d.w)
		C.ml_wasapi_reset(&d.w)
		C.ml_wasapi_start(&d.w)
		d.samples = 0
	}
}

func wasapiAudioSetVolume(ad *AudioDecoder, level float32) {
	d := ad.getWasapi()
	C.ml_wasapi_volume(&d.w, C.float(level))
}

// audioDriverStartPlatform — the windows driver (wasapi); same
// AudioClass seam as alsa/mac_audio.
func audioDriverStartPlatform(settings any) (*AudioClass, error) {
	return &AudioClass{
		Fini:            wasapiAudioFini,
		Reconfig:        func(ad *AudioDecoder) error { wasapiAudioReconfig(ad); return nil },
		DeliverUnlocked: wasapiAudioDeliver,
		Pause:           wasapiAudioPause,
		Play:            wasapiAudioPlay,
		Flush:           wasapiAudioFlush,
		SetVolume:       wasapiAudioSetVolume,
	}, nil
}
