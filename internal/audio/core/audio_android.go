//go:build android

package core

// Canonical port of src/arch/android/android_audio.c — OpenSL ES
// output with the 8-slot PCM ring and the buffer-queue callback.

/*
#cgo LDFLAGS: -lOpenSLES
#include <SLES/OpenSLES.h>
#include <SLES/OpenSLES_Android.h>
#include <stdlib.h>
#include <string.h>
#include <math.h>
#include <unistd.h>
#include <sys/resource.h>

extern void goSLESBufferCallback(uintptr_t handle);
extern void goSLESPlayerCallback(uintptr_t handle, SLuint32 event);

// C: buffer_callback trampoline — SL wants a C function pointer.
static void
ml_sles_buffer_cb(SLAndroidSimpleBufferQueueItf bq, void *ctx)
{
  goSLESBufferCallback((uintptr_t)ctx);
}

static void
ml_sles_player_cb(SLPlayItf caller, void *ctx, SLuint32 event)
{
  goSLESPlayerCallback((uintptr_t)ctx, event);
}

static SLresult
ml_sles_create_engine(SLObjectItf *obj)
{
  return slCreateEngine(obj, 0, NULL, 0, NULL, NULL);
}

static SLresult
ml_sles_realize(SLObjectItf obj)
{
  return (*obj)->Realize(obj, SL_BOOLEAN_FALSE);
}

static SLresult
ml_sles_get_engine_itf(SLObjectItf obj, SLEngineItf *out)
{
  return (*obj)->GetInterface(obj, SL_IID_ENGINE, out);
}

static SLresult
ml_sles_get_play_itf(SLObjectItf obj, SLPlayItf *out)
{
  return (*obj)->GetInterface(obj, SL_IID_PLAY, out);
}

static SLresult
ml_sles_get_volume_itf(SLObjectItf obj, SLVolumeItf *out)
{
  return (*obj)->GetInterface(obj, SL_IID_VOLUME, out);
}

// Raise the calling thread's priority (nice value). Android grants
// apps negative nice via the zygote's RLIMIT_NICE — this is the same
// mechanism behind Process.THREAD_PRIORITY_URGENT_AUDIO (-19).
static int
ml_set_thread_prio(int niceval)
{
  return setpriority(PRIO_PROCESS, (id_t)gettid(), niceval);
}

static SLresult
ml_sles_get_bufq_itf(SLObjectItf obj, SLAndroidSimpleBufferQueueItf *out)
{
  return (*obj)->GetInterface(obj, SL_IID_ANDROIDSIMPLEBUFFERQUEUE, out);
}

static SLresult
ml_sles_create_output_mix(SLEngineItf eif, SLObjectItf *mixer)
{
  return (*eif)->CreateOutputMix(eif, mixer, 0, NULL, NULL);
}

// C: the reconfig()'s CreateAudioPlayer with the PCM buffer-queue
// source and OutputMix sink (android_audio.c:235-281).
static SLresult
ml_sles_create_audio_player(SLEngineItf eif, SLObjectItf *player,
                            SLObjectItf mixer,
                            SLuint32 numBufs, SLuint32 rateHz)
{
  SLDataLocator_AndroidSimpleBufferQueue loc_bufq = {
    SL_DATALOCATOR_ANDROIDSIMPLEBUFFERQUEUE, numBufs};
  SLDataFormat_PCM format_pcm = {SL_DATAFORMAT_PCM,
                                 2,
                                 rateHz,
                                 SL_PCMSAMPLEFORMAT_FIXED_16,
                                 SL_PCMSAMPLEFORMAT_FIXED_16,
                                 SL_SPEAKER_FRONT_LEFT |
                                 SL_SPEAKER_FRONT_RIGHT,
                                 SL_BYTEORDER_LITTLEENDIAN};
  SLDataSource audioSrc = {&loc_bufq, &format_pcm};
  SLDataLocator_OutputMix loc_outmix = {SL_DATALOCATOR_OUTPUTMIX, mixer};
  SLDataSink audioSnk = {&loc_outmix, NULL};
  const SLInterfaceID ids[2] = {SL_IID_ANDROIDSIMPLEBUFFERQUEUE,
                                SL_IID_VOLUME};
  const SLboolean req[2] = {SL_BOOLEAN_TRUE, SL_BOOLEAN_TRUE};
  return (*eif)->CreateAudioPlayer(eif, player, &audioSrc, &audioSnk,
                                   2, ids, req);
}

static void
ml_sles_destroy(SLObjectItf obj)
{
  (*obj)->Destroy(obj);
}

static SLresult
ml_sles_get_position(SLPlayItf pif, SLmillisecond *ms)
{
  return (*pif)->GetPosition(pif, ms);
}

static SLresult
ml_sles_set_play_state(SLPlayItf pif, SLuint32 state)
{
  return (*pif)->SetPlayState(pif, state);
}

static SLresult
ml_sles_register_bufq_cb(SLAndroidSimpleBufferQueueItf bif, uintptr_t ctx)
{
  return (*bif)->RegisterCallback(bif, ml_sles_buffer_cb, (void *)ctx);
}

static SLresult
ml_sles_register_play_cb(SLPlayItf pif, uintptr_t ctx)
{
  return (*pif)->RegisterCallback(pif, ml_sles_player_cb, (void *)ctx);
}

static SLresult
ml_sles_set_cb_events(SLPlayItf pif, SLuint32 mask)
{
  return (*pif)->SetCallbackEventsMask(pif, mask);
}

static SLresult
ml_sles_set_position_period(SLPlayItf pif, SLmillisecond ms)
{
  return (*pif)->SetPositionUpdatePeriod(pif, ms);
}

static SLresult
ml_sles_enqueue(SLAndroidSimpleBufferQueueItf bif, void *buf, SLuint32 size)
{
  return (*bif)->Enqueue(bif, buf, size);
}

static SLresult
ml_sles_clear(SLAndroidSimpleBufferQueueItf bif)
{
  return (*bif)->Clear(bif);
}

static SLresult
ml_sles_set_volume(SLVolumeItf vif, SLmillibel mb)
{
  return (*vif)->SetVolumeLevel(vif, mb);
}

static SLmillibel
ml_sles_gain_to_mb(float gain)
{
  int mb = lroundf(2000.f * log10f(gain));
  if(mb < SL_MILLIBEL_MIN)
    mb = SL_MILLIBEL_MIN;
  return mb;
}
*/
import "C"

import (
	"errors"
	"runtime"
	"runtime/cgo"
	"sync/atomic"
	"unsafe"

	"github.com/czz/movian-go/internal/arch"
	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/trace"
)

var errAudioStart = errors.New("opensles: init failed")

// C: __sync_synchronize() full barriers around the ring pointers
// (android_audio.c:356,381,404,420,474).
func atomicLoadFence()  { atomic.AddInt32(&fenceVar, 0) }
func atomicStoreFence() { atomic.AddInt32(&fenceVar, 0) }

var fenceVar int32

const (
	// PCM ring depth. C: PCM_RING_SIZE = 8 (android_audio.c:29) — that
	// gave ~150ms @48kHz of producer runway, which still starves on
	// slow TV-box CPUs during demux/decode jitter. 16 ≈ 320ms, ~64KB.
	pcmRingSize = 16
	pcmRingMask = pcmRingSize - 1

	// Hardware buffer-queue depth. C upstream used 2 — fine when PCM
	// tiles were huge, but ~46ms total is too tight for modern
	// decode+resample jitter on slow TV-box CPUs. 8 slots ≈ 170ms
	// @48kHz/1024f — enough to cover a decoder restart on YouTube
	// adaptive-resolution switches.
	numSlesBuffers = 8 // C: hardcoded 2 in android_audio.c

	// Tiles of real PCM required in the ring before the player is
	// unpaused after a reconfig/flush. Starting PLAYING on an empty
	// ring makes the buffer-queue callback emit silence tiles — the
	// silence↔audio edges are the audible crackle at start/seek.
	// 8 tiles ≈ 170ms @48kHz/1024f of runway before first sound.
	primeTiles = 8

	// Nice level for the audio threads — Process.THREAD_PRIORITY_URGENT_AUDIO.
	urgentAudioNice = -19
)

// decoderT — C: typedef struct decoder (android_audio.c:33-74).
type decoderT struct {
	ad *AudioDecoder

	engine C.SLObjectItf
	mixer  C.SLObjectItf
	player C.SLObjectItf

	eif C.SLEngineItf
	pif C.SLPlayItf
	vif C.SLVolumeItf
	bif C.SLAndroidSimpleBufferQueueItf

	framesize int

	pcmbuf     []byte
	pcmbufSize int

	availBuffers int32

	writePtr int32
	readPtr  int32

	gain       float32
	lastSetVol float32

	sleeptime int

	samplesSent int64
	underruns   int  // Go-only: diagnostic counter for the silence path
	cbPrioSet   bool // Go-only: callback thread already niced

	timestamp [pcmRingSize]int64
	epoch     [pcmRingSize]int

	markEpoch   int
	markTs      int64
	markSamples int64

	paused  bool
	priming bool // Go-only: player held PAUSED until the ring refills

	handle cgo.Handle
}

// decFromAd — C: (decoder_t *)ad — hangs off AudioInstance like
// C's ac_alloc_size embedding (same pattern as alsaDecoder).
func decFromAd(ad *AudioDecoder) *decoderT {
	if d, ok := ad.AudioInstance.(*decoderT); ok {
		return d
	}
	d := &decoderT{ad: ad}
	d.handle = cgo.NewHandle(d)
	ad.AudioInstance = d
	return d
}

// C: extern float audio_master_volume / int audio_master_mute
// (android_audio.c:76-77) — the manager's prop-backed fields.
func (d *decoderT) masterVolume() float32 {
	if d.ad.manager != nil {
		return d.ad.manager.GetMasterVolume()
	}
	return 1.0
}

func (d *decoderT) masterMute() bool {
	if d.ad.manager != nil {
		return d.ad.manager.GetMasterMute() != 0
	}
	return false
}

// androidAudioStart — C: android_audio_init (android_audio.c:88-125).
// Called on audioDecodeThread — pin the goroutine so the urgent-audio
// nice we set below survives for the decoder's lifetime.
func androidAudioStart(ad *AudioDecoder) error {
	runtime.LockOSThread()
	if C.ml_set_thread_prio(urgentAudioNice) != 0 {
		ad.ts.Trace(trace.TRACE_DEBUG, "SLES",
			"setpriority(%d) on decode thread failed — continuing anyway",
			urgentAudioNice)
	}

	d := decFromAd(ad)
	d.gain = 1.0
	d.lastSetVol = 0

	if C.ml_sles_create_engine(&d.engine) != 0 {
		ad.ts.Error("SLES", "Unable to create engine")
		return errAudioStart
	}
	if C.ml_sles_realize(d.engine) != 0 {
		ad.ts.Error("SLES", "Unable to realize engine")
		return errAudioStart
	}
	if C.ml_sles_get_engine_itf(d.engine, &d.eif) != 0 {
		ad.ts.Error("SLES", "Unable to get interface for engine")
		return errAudioStart
	}
	if C.ml_sles_create_output_mix(d.eif, &d.mixer) != 0 {
		ad.ts.Error("SLES", "Unable to create output mixer")
		return errAudioStart
	}
	if C.ml_sles_realize(d.mixer) != 0 {
		ad.ts.Error("SLES", "Unable to realize output mixer")
		return errAudioStart
	}
	return nil
}

// androidStopPlayer — C: android_stop_player (android_audio.c:131-145).
func androidStopPlayer(d *decoderT) {
	atomic.StoreInt32(&d.availBuffers, 0)
	d.pcmbuf = nil
	if d.player != nil {
		C.ml_sles_destroy(d.player)
		d.player = nil
		d.eif = nil
		d.pif = nil
		d.vif = nil
		d.bif = nil
	}
}

// androidAudioFini — C: android_audio_fini (android_audio.c:150-158).
func androidAudioFini(ad *AudioDecoder) {
	defer runtime.UnlockOSThread() // pairs with LockOSThread in Start
	d := decFromAd(ad)
	androidStopPlayer(d)
	if d.mixer != nil {
		C.ml_sles_destroy(d.mixer)
	}
	if d.engine != nil {
		C.ml_sles_destroy(d.engine)
	}
	if d.handle != 0 {
		d.handle.Delete()
	}
}

// The C trampolines (ml_sles_buffer_cb / ml_sles_player_cb) call the
// //export'ed Go functions below.

//export goSLESBufferCallback
func goSLESBufferCallback(handle C.uintptr_t) {
	d := cgo.Handle(handle).Value().(*decoderT)
	if !d.cbPrioSet {
		// Go-only: this callback runs on the OpenSLES worker thread —
		// raise it to urgent-audio priority once.
		d.cbPrioSet = true
		if C.ml_set_thread_prio(urgentAudioNice) != 0 {
			d.ad.ts.Trace(trace.TRACE_DEBUG, "SLES",
				"setpriority(%d) failed — continuing anyway", urgentAudioNice)
		}
	}
	atomicLoadFence()
	nr := (d.readPtr + 1) & pcmRingMask
	if nr == d.writePtr {
		// C: underrun — enqueue silence (android_audio.c:358-364)
		d.underruns++
		if d.underruns == 1 || d.underruns%16 == 0 {
			d.ad.ts.Trace(trace.TRACE_ERROR, "SLES",
				"buffer underrun #%d (%d bufs, tile=%d @%dHz)",
				d.underruns, numSlesBuffers, d.ad.TileSize,
				d.ad.OutSampleRate)
		}
		offset := int(d.readPtr) * d.pcmbufSize
		for i := range d.pcmbuf[offset : offset+d.pcmbufSize] {
			d.pcmbuf[offset+i] = 0
		}
		C.ml_sles_enqueue(d.bif,
			unsafe.Pointer(&d.pcmbuf[offset]), C.SLuint32(d.pcmbufSize))
		d.samplesSent += int64(d.ad.TileSize)
		return
	}

	offset := int(nr) * d.pcmbufSize
	pts := d.timestamp[nr]
	epo := d.epoch[nr]
	d.timestamp[nr] = mediacore.PTSUnset
	if pts != mediacore.PTSUnset {
		d.markTs = pts
		d.markEpoch = epo
		d.markSamples = d.samplesSent
	}

	C.ml_sles_enqueue(d.bif,
		unsafe.Pointer(&d.pcmbuf[offset]), C.SLuint32(d.pcmbufSize))
	d.samplesSent += int64(d.ad.TileSize)
	d.readPtr = nr
	atomicStoreFence()
}

//export goSLESPlayerCallback
func goSLESPlayerCallback(handle C.uintptr_t, event C.SLuint32) {
	d := cgo.Handle(handle).Value().(*decoderT)
	if event != 4 {
		return
	}
	var ms C.SLmillisecond
	C.ml_sles_get_position(d.pif, &ms)

	currentSample := int64(ms) * int64(d.ad.OutSampleRate) / 1000
	audioDelaySamples := d.samplesSent - currentSample +
		int64(d.ad.TileSize)*pcmRingSize

	d.ad.Delay = int(audioDelaySamples * 1000000 / int64(d.ad.OutSampleRate))
	if d.markTs != mediacore.PTSUnset {
		mp, _ := d.ad.MediaPipe.(*mediacore.MediaPipe)
		if mp != nil {
			mp.ClockMutex.Lock()
			mp.AudioClockEpoch = int64(d.markEpoch)
			mp.AudioClockAvtime = arch.GetAvtime()
			mp.AudioClock = d.markTs - int64(d.ad.Delay)
			mp.RealtimeDelta = mp.AudioClockAvtime - mp.AudioClock
			mp.ClockMutex.Unlock()
		}
		d.markTs = mediacore.PTSUnset
	}
}

// androidAudioReconfig — C: android_audio_reconfig
// (android_audio.c:224-346).
func androidAudioReconfig(ad *AudioDecoder) error {
	d := decFromAd(ad)

	androidStopPlayer(d)

	ad.OutSampleRate = arch.AndroidSystemAudioSampleRate
	if ad.OutSampleRate == 0 {
		ad.OutSampleRate = 44100
	}

	ad.OutSampleFormat = SampleFormatS16
	ad.OutChannelLayout = ChannelLayoutStereo

	d.framesize = 2 * 2

	ad.TileSize = arch.AndroidSystemAudioFramesPerBuffer
	if ad.TileSize == 0 {
		ad.TileSize = 1024
	}
	for ad.TileSize < 512 {
		ad.TileSize *= 2
	}

	d.sleeptime = 1000 * ad.TileSize / ad.OutSampleRate

	d.pcmbufSize = d.framesize * ad.TileSize
	d.pcmbuf = make([]byte, pcmRingSize*d.pcmbufSize)

	if C.ml_sles_create_audio_player(d.eif, &d.player, d.mixer,
		C.SLuint32(numSlesBuffers),
		C.SLuint32(ad.OutSampleRate*1000)) != 0 {
		ad.ts.Error("SLES", "Unable to create audio player")
		return errAudioStart
	}
	if C.ml_sles_realize(d.player) != 0 {
		ad.ts.Error("SLES", "Unable to realize audio player")
		return errAudioStart
	}
	if C.ml_sles_get_play_itf(d.player, &d.pif) != 0 {
		ad.ts.Error("SLES", "Unable to get player interface")
		return errAudioStart
	}
	if C.ml_sles_get_volume_itf(d.player, &d.vif) != 0 {
		ad.ts.Error("SLES", "Unable to get volume interface")
		return errAudioStart
	}
	if C.ml_sles_get_bufq_itf(d.player, &d.bif) != 0 {
		ad.ts.Error("SLES", "Unable to get buffer queue interface")
		return errAudioStart
	}
	if C.ml_sles_register_bufq_cb(d.bif, C.uintptr_t(d.handle)) != 0 {
		ad.ts.Error("SLES", "Unable to register callback")
		return errAudioStart
	}
	if C.ml_sles_register_play_cb(d.pif, C.uintptr_t(d.handle)) != 0 {
		ad.ts.Error("SLES", "Unable to set playback callback")
		return errAudioStart
	}
	if C.ml_sles_set_cb_events(d.pif, 0x1f) != 0 {
		ad.ts.Error("SLES", "Unable to set event mask")
		return errAudioStart
	}
	if C.ml_sles_set_position_period(d.pif, 100) != 0 {
		ad.ts.Error("SLES", "Unable to set position update period")
		return errAudioStart
	}

	for i := 0; i < pcmRingSize; i++ {
		d.timestamp[i] = mediacore.PTSUnset
	}

	// Go-only: always start PAUSED and let androidAudioDeliver unpause
	// once the ring holds primeTiles of real PCM — starting PLAYING on
	// an empty ring made the callback emit silence tiles (the audible
	// startup crackle). C upstream started PLAYING immediately.
	if C.ml_sles_set_play_state(d.pif, C.SL_PLAYSTATE_PAUSED) != 0 {
		ad.ts.Error("SLES", "Unable to set playback state")
		return errAudioStart
	}
	d.priming = !d.paused

	C.ml_sles_enqueue(d.bif, unsafe.Pointer(&d.pcmbuf[0]),
		C.SLuint32(d.pcmbufSize))
	d.readPtr = 0
	d.writePtr = 1

	atomic.StoreInt32(&d.availBuffers, int32(numSlesBuffers))
	return nil
}

// androidAudioDeliver — C: android_audio_deliver
// (android_audio.c:388-424). Runs with the audio decoder lock held
// (ac_deliver_locked).
func androidAudioDeliver(ad *AudioDecoder, samples int, pts int64, epoch int) int {
	d := decFromAd(ad)

	gain := d.gain * d.masterVolume()
	if d.masterMute() {
		gain = 0
	}
	if gain != d.lastSetVol {
		d.lastSetVol = gain
		if gain <= 0 {
			C.ml_sles_set_volume(d.vif, C.SL_MILLIBEL_MIN)
		} else {
			C.ml_sles_set_volume(d.vif, C.ml_sles_gain_to_mb(C.float(gain)))
		}
	}

	for ad.AVR.Available() >= ad.TileSize {
		atomicLoadFence()

		if (d.writePtr+1)&pcmRingMask == d.readPtr {
			return d.sleeptime
		}

		offset := int(d.writePtr) * d.pcmbufSize
		ad.AVR.Read(d.pcmbuf[offset:offset+d.pcmbufSize], ad.TileSize)

		if pts != mediacore.PTSUnset {
			d.timestamp[d.writePtr] = pts
			d.epoch[d.writePtr] = epoch

			// Go-only: publish the clock epoch as soon as the first
			// tile of a new epoch enters the ring while the player is
			// held paused for priming. The HEADATNEWPOS position
			// callback (which normally updates mp.AudioClockEpoch)
			// only fires while PLAYING — during priming it never runs,
			// so the video drain gate sees epoch != AudioClockEpoch
			// and drops every post-seek frame (frozen video).
			// AudioClock is estimated from ad.Delay (last measured
			// output latency); the periodic callback refines it once
			// playback resumes.
			if d.paused || d.priming {
				if mp, _ := ad.MediaPipe.(*mediacore.MediaPipe); mp != nil {
					mp.ClockMutex.Lock()
					if int64(epoch) != mp.AudioClockEpoch {
						mp.AudioClockEpoch = int64(epoch)
						mp.AudioClockAvtime = arch.GetAvtime()
						mp.AudioClock = pts - int64(ad.Delay)
						mp.RealtimeDelta = mp.AudioClockAvtime - mp.AudioClock
					}
					mp.ClockMutex.Unlock()
				}
			}
			pts = mediacore.PTSUnset
		}

		d.writePtr = (d.writePtr + 1) & pcmRingMask
		atomicStoreFence()

		// In-loop so the write that fills the ring also unpauses —
		// the full-ring path returns early above and must not strand
		// the player in PAUSED.
		if d.priming && !d.paused &&
			int((d.writePtr-d.readPtr)&pcmRingMask) >= primeTiles {
			d.priming = false
			ad.ts.Trace(trace.TRACE_DEBUG, "SLES",
				"primed: unpausing player")
			C.ml_sles_set_play_state(d.pif, C.SL_PLAYSTATE_PLAYING)
		}
	}
	return 0
}

// androidSetVolume — C: android_set_volume (android_audio.c:430-435).
func androidSetVolume(ad *AudioDecoder, scale float32) {
	d := decFromAd(ad)
	d.gain = scale
}

// androidAudioPause — C: android_audio_pause (android_audio.c:442-449).
func androidAudioPause(ad *AudioDecoder) {
	d := decFromAd(ad)
	if d.pif != nil {
		C.ml_sles_set_play_state(d.pif, C.SL_PLAYSTATE_PAUSED)
	}
	d.paused = true
}

// androidAudioPlay — C: android_audio_play (android_audio.c:455-462).
func androidAudioPlay(ad *AudioDecoder) {
	d := decFromAd(ad)
	d.paused = false
	if d.priming {
		// Refill gate armed (reconfig/flush): hold the pause and let
		// androidAudioDeliver unpause once the ring holds real PCM —
		// the pipeline sends MBCtrlPlay at playback start via the
		// hold gate, long before the decoder has produced audio.
		ad.ts.Trace(trace.TRACE_DEBUG, "SLES",
			"play ctrl while priming — deferred to fill gate")
		return
	}
	if d.pif != nil {
		C.ml_sles_set_play_state(d.pif, C.SL_PLAYSTATE_PLAYING)
	}
}

// androidAudioFlush — C: android_audio_flush (android_audio.c:468-475).
// Go-only: also drops the queued SLES buffers (stale pre-seek audio —
// C's 2-buffer queue drained in ~46ms, ours holds ~170ms) and re-primes
// the player so the refill doesn't play as a silence-tile crackle.
func androidAudioFlush(ad *AudioDecoder) {
	d := decFromAd(ad)
	if d.bif != nil {
		if !d.paused {
			d.priming = true
			ad.ts.Trace(trace.TRACE_DEBUG, "SLES",
				"flush: pausing + re-priming")
			C.ml_sles_set_play_state(d.pif, C.SL_PLAYSTATE_PAUSED)
		}
		C.ml_sles_clear(d.bif)
		for i := range d.pcmbuf[:d.pcmbufSize] {
			d.pcmbuf[i] = 0
		}
		// Re-kick with one silence tile: on resume it completes and
		// restarts the buffer-queue callback chain (same as reconfig).
		C.ml_sles_enqueue(d.bif, unsafe.Pointer(&d.pcmbuf[0]),
			C.SLuint32(d.pcmbufSize))
	}
	d.readPtr = 0
	d.writePtr = 1
	atomicStoreFence()
}

// audioDriverStartPlatform — C: audio_driver_init →
// &android_audio_class (android_audio.c:481-501).
func audioDriverStartPlatform(settings any) (*AudioClass, error) {
	return &AudioClass{
		Start:         androidAudioStart,
		Fini:          androidAudioFini,
		Reconfig:      androidAudioReconfig,
		DeliverLocked: androidAudioDeliver,
		SetVolume:     androidSetVolume,
		Pause:         androidAudioPause,
		Play:          androidAudioPlay,
		Flush:         androidAudioFlush,
	}, nil
}
