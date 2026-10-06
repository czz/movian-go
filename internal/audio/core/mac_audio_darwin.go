//go:build darwin

package core

// mac_audio_darwin.go — canonical port of src/audio2/mac_audio.c
//
// CoreAudio AudioQueue driver: 8-buffer ring, buffers returned by the
// AudioQueue callback are recycled by size, the audio clock is anchored
// from AudioQueueEnqueueBufferWithParameters' output timestamp.

/*
#cgo LDFLAGS: -framework AudioToolbox -framework CoreFoundation
#include <TargetConditionals.h>
#include <AudioToolbox/AudioQueue.h>
#include <mach/mach_time.h>
#include <stdint.h>
#include <stdlib.h>
#include <unistd.h>

extern void macAudioReturnBuf(void *aux, AudioQueueRef aq, AudioQueueBufferRef buf);

// C: do_nothing() keeps the audio CFRunLoop alive (audio_test.c:50).
static void do_nothing(CFRunLoopTimerRef timer, void *info) {}

// C: audio_thread() — publishes the thread's run loop, installs the
// keep-alive timer, and runs the loop forever.
static void *ml_runloop_current(void) { return CFRunLoopGetCurrent(); }
static void ml_runloop_add_keepalive(void *rl) {
    CFRunLoopTimerRef t = CFRunLoopTimerCreate(NULL, CFAbsoluteTimeGetCurrent(),
                                             1000000.0f, 0, 0, do_nothing, NULL);
    CFRunLoopAddTimer((CFRunLoopRef)rl, t, kCFRunLoopCommonModes);
}
static void ml_runloop_run(void) { CFRunLoopRun(); }

// C: AudioQueueNewOutput(..., return_buf, d, audio_run_loop, ...) — the
// Go-exported macAudioReturnBuf is the queue callback.
static OSStatus ml_aq_new_output(AudioStreamBasicDescription *desc,
                                 void *aux, void *runloop, AudioQueueRef *out) {
    return AudioQueueNewOutput(desc, macAudioReturnBuf, aux,
                               (CFRunLoopRef)runloop,
                               kCFRunLoopCommonModes, 0, out);
}

// C: ios_set_audio_status_enable — only exists on iOS builds.
#if TARGET_OS_IOS == 1
extern void ios_set_audio_status_enable(int on);
static void ml_ios_set_audio_status(int on) { ios_set_audio_status_enable(on); }
#else
static void ml_ios_set_audio_status(int on) { (void)on; }
#endif

static void *ml_buf_data(AudioQueueBufferRef b) { return b->mAudioData; }
static void ml_buf_set_size(AudioQueueBufferRef b, uint32_t n) { b->mAudioDataByteSize = n; }
static uint32_t ml_ats_valid(AudioTimeStamp *ats) { return ats->mFlags & kAudioTimeStampHostTimeValid; }

// C: arch_get_avtime (osx_app.m:50) — CoreAudio host time in us.
// AudioConvertHostTimeToNanos/AudioGetCurrentHostTime were removed
// from the modern SDK; mach_absolute_time + timebase is identical
// (this is what those APIs did internally).
static int64_t ml_arch_get_avtime(void) {
    static mach_timebase_info_data_t tb;
    if (tb.denom == 0) mach_timebase_info(&tb);
    return (int64_t)(mach_absolute_time() * tb.numer / tb.denom / 1000LL);
}
*/
import "C"

import (
	"errors"
	"runtime/cgo"
	"sync/atomic"
	"time"
	"unsafe"

	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/trace"
)

// errReconfig — C: mac_audio_reconfig's 1 (AudioQueueNewOutput/Start
// failure).
var errReconfig = errors.New("audioqueue: reconfig failed")

// C: #define NUM_BUFS 8
const macAudioNumBufs = 8

// macAudioBuf — C: buffers[NUM_BUFS] entry {buf, avail, size}.
type macAudioBuf struct {
	buf   C.AudioQueueBufferRef
	avail bool
	size  int
}

// macAudioDecoder — C: decoder_t (mac_audio.c:34-42). Hangs off
// AudioDecoder.AudioInstance like C's ac_alloc_size embedding.
type macAudioDecoder struct {
	owner     *AudioDecoder // Go-only: back-pointer for the AQ callback
	aq        C.AudioQueueRef
	framesize int
	underrun  int
	buffers   [macAudioNumBufs]macAudioBuf
	handle    cgo.Handle // Go-only: userdata for the AQ callback
}

// C: static CFRunLoopRef audio_run_loop;
var macAudioRunLoop unsafe.Pointer

// C: static mach_timebase_info_data_t timebase;
var macAudioTimebase C.mach_timebase_info_data_t

// C: audio_thread (mac_audio.c:58) — the run-loop thread body.
func macAudioThread() {
	rl := C.ml_runloop_current()
	atomic.StorePointer(&macAudioRunLoop, rl) // C: __sync_synchronize()
	C.ml_runloop_add_keepalive(rl)
	C.ml_runloop_run()
}

// getMac — C: (decoder_t *)ad.
func (ad *AudioDecoder) getMac() *macAudioDecoder {
	if d, ok := ad.AudioInstance.(*macAudioDecoder); ok {
		return d
	}
	d := &macAudioDecoder{owner: ad}
	ad.AudioInstance = d
	return d
}

//export macAudioReturnBuf
func macAudioReturnBuf(aux unsafe.Pointer, aq C.AudioQueueRef, buf C.AudioQueueBufferRef) {
	d := cgo.Handle(uintptr(aux)).Value().(*macAudioDecoder)
	mp, _ := d.owner.MediaPipe.(*mediacore.MediaPipe)
	if mp == nil {
		return
	}
	// C: hts_mutex_lock(&mp->mp_mutex)
	mp.Mutex.Lock()
	for i := 0; i < macAudioNumBufs; i++ {
		if d.buffers[i].buf == buf {
			d.buffers[i].avail = true
			mp.Audio.Avail.Signal()
			mp.Mutex.Unlock()
			tavail := 0
			for i = 0; i < macAudioNumBufs; i++ {
				if d.buffers[i].avail {
					tavail++
				}
			}
			if tavail == macAudioNumBufs {
				d.underrun = 1
			}
			return
		}
	}
	mp.Mutex.Unlock()
	panic("macAudioReturnBuf: unknown buffer") // C: abort()
}

// macAudioAllocbuf — C: allocbuf (mac_audio.c:106).
func (d *macAudioDecoder) allocbuf(pos, bytes int) C.AudioQueueBufferRef {
	d.buffers[pos].size = bytes
	C.AudioQueueAllocateBuffer(d.aq, C.UInt32(bytes), &d.buffers[pos].buf)
	d.buffers[pos].avail = false
	return d.buffers[pos].buf
}

// macAudioGetbuf — C: getbuf (mac_audio.c:119).
func (d *macAudioDecoder) getbuf(bytes int) C.AudioQueueBufferRef {
	// C: assert(d->aq != NULL)
	for i := 0; i < macAudioNumBufs; i++ {
		if d.buffers[i].avail && d.buffers[i].size >= bytes {
			d.buffers[i].avail = false
			return d.buffers[i].buf
		}
	}
	for i := 0; i < macAudioNumBufs; i++ {
		if d.buffers[i].buf == nil {
			return d.allocbuf(i, bytes)
		}
	}
	for i := 0; i < macAudioNumBufs; i++ {
		if d.buffers[i].avail {
			C.AudioQueueFreeBuffer(d.aq, d.buffers[i].buf)
			return d.allocbuf(i, bytes)
		}
	}
	return nil
}

// macAudioFini — C: mac_audio_fini (mac_audio.c:147).
func macAudioFini(ad *AudioDecoder) {
	d := ad.getMac()
	if d.aq != nil {
		C.AudioQueueDispose(d.aq, C.Boolean(1)) // C: true
		d.aq = nil
		C.ml_ios_set_audio_status(-1) // C: ios_set_audio_status_enable(-1)
	}
	if d.handle != 0 {
		d.handle.Delete()
		d.handle = 0
	}
}

// macAudioReconfig — C: mac_audio_reconfig (mac_audio.c:163).
func macAudioReconfig(ad *AudioDecoder) int {
	d := ad.getMac()

	if d.aq != nil {
		C.AudioQueueDispose(d.aq, C.Boolean(0)) // C: false
		d.aq = nil
		for i := 0; i < macAudioNumBufs; i++ {
			d.buffers[i].buf = nil
			d.buffers[i].avail = false
			d.buffers[i].size = 0
		}
	} else {
		C.ml_ios_set_audio_status(1) // C: ios_set_audio_status_enable(1)
	}

	var desc C.AudioStreamBasicDescription

	// Coding
	desc.mFormatID = C.AudioFormatID(C.kAudioFormatLinearPCM)

	// Rate
	ad.OutSampleRate = ad.InSampleRate
	desc.mSampleRate = C.Float64(ad.OutSampleRate)

	// Channel configuration
	ad.OutChannelLayout = ChannelLayoutStereo // C: AV_CH_LAYOUT_STEREO
	desc.mChannelsPerFrame = 2

	// Sample format
	var sampleSize int
	switch ad.InSampleFormat {
	case SampleFormatS32, SampleFormatS32P:
		ad.OutSampleFormat = SampleFormatS32
		desc.mFormatFlags = C.AudioFormatFlags(C.kAudioFormatFlagIsSignedInteger)
		sampleSize = 4
	case SampleFormatS16, SampleFormatS16P:
		if ad.InChannelLayout == ChannelLayoutStereo {
			ad.OutSampleFormat = SampleFormatS16
			desc.mFormatFlags = C.AudioFormatFlags(C.kAudioFormatFlagIsSignedInteger)
			sampleSize = 2
			break
		}
		// C: FALLTHRU
		fallthrough
	default:
		ad.OutSampleFormat = SampleFormatFLT
		desc.mFormatFlags = C.AudioFormatFlags(C.kAudioFormatFlagIsFloat)
		sampleSize = 4
	}

	desc.mBytesPerFrame = desc.mChannelsPerFrame * C.UInt32(sampleSize)
	desc.mBitsPerChannel = C.UInt32(8 * sampleSize)
	desc.mFramesPerPacket = 1
	desc.mBytesPerPacket = desc.mBytesPerFrame

	d.framesize = int(desc.mBytesPerFrame)

	ad.ts.Trace(trace.TRACE_DEBUG, "AudioQueue", "Start %d Hz",
		ad.OutSampleRate)

	if d.handle == 0 {
		d.handle = cgo.NewHandle(d)
	}
	r := C.ml_aq_new_output(&desc, unsafe.Pointer(uintptr(d.handle)),
		atomic.LoadPointer(&macAudioRunLoop), &d.aq)
	if r != 0 {
		d.aq = nil
		ad.ts.Trace(trace.TRACE_ERROR, "AudioQueue",
			"AudioQueueNewOutput() error %d", int(r))
		return 1
	}

	r = C.AudioQueueStart(d.aq, nil)
	if r != 0 {
		ad.ts.Trace(trace.TRACE_ERROR, "AudioQueue",
			"AudioQueueStart() error %d", int(r))
		C.AudioQueueDispose(d.aq, C.Boolean(0))
		d.aq = nil
		return 1
	}
	// C: AudioQueueSetParameter(d->aq, kAudioQueueParam_Volume,
	//   ad->ad_vol_scale)
	C.AudioQueueSetParameter(d.aq, C.AudioQueueParameterID(C.kAudioQueueParam_Volume),
		C.AudioQueueParameterValue(ad.VolScale))
	return 0
}

// macAudioPause — C: mac_audio_pause (mac_audio.c:260).
func macAudioPause(ad *AudioDecoder) {
	d := ad.getMac()
	if d.aq != nil {
		C.AudioQueuePause(d.aq)
	}
}

// macAudioSetVolume — C: mac_audio_set_volume (mac_audio.c:272).
func macAudioSetVolume(ad *AudioDecoder, level float32) {
	d := ad.getMac()
	if d.aq != nil {
		C.AudioQueueSetParameter(d.aq, C.AudioQueueParameterID(C.kAudioQueueParam_Volume),
			C.AudioQueueParameterValue(level))
	}
}

// macAudioPlay — C: mac_audio_play (mac_audio.c:284).
func macAudioPlay(ad *AudioDecoder) {
	d := ad.getMac()
	if d.aq != nil {
		C.AudioQueueStart(d.aq, nil)
	}
}

// macAudioFlush — C: mac_audio_flush (mac_audio.c:296).
func macAudioFlush(ad *AudioDecoder) {
	d := ad.getMac()
	if d.aq != nil {
		C.AudioQueueReset(d.aq)
	}
}

// macAudioDeliver — C: mac_audio_deliver (mac_audio.c:308).
// Returns 0 on success, -1 when no buffer is available (blocked).
func macAudioDeliver(ad *AudioDecoder, samples int, pts int64, epoch int) int {
	d := ad.getMac()
	bytes := samples * d.framesize

	if d.underrun != 0 {
		d.underrun = 0
		time.Sleep(40 * time.Millisecond) // C: usleep(40000)
	}

	b := d.getbuf(bytes)
	if b == nil {
		return -1
	}

	// C: data[0] = b->mAudioData; avresample_read(ad->ad_avr, data, samples)
	if ad.AVR != nil {
		dst := unsafe.Slice((*byte)(C.ml_buf_data(b)), bytes)
		ad.AVR.Read(dst, samples)
	}
	C.ml_buf_set_size(b, C.UInt32(bytes))

	var ats C.AudioTimeStamp
	C.AudioQueueEnqueueBufferWithParameters(d.aq, b, 0, nil,
		0, 0, 0, nil, nil, &ats)

	if C.ml_ats_valid(&ats) != 0 && pts != mediacore.PTSUnset {
		now := int64(C.mach_absolute_time())
		t := now * int64(macAudioTimebase.numer) /
			(int64(macAudioTimebase.denom) * 1000)

		ad.Delay = int(t - int64(C.ml_arch_get_avtime()))

		mp, _ := ad.MediaPipe.(*mediacore.MediaPipe)
		if mp != nil {
			mp.ClockMutex.Lock()
			mp.AudioClockAvtime = t
			mp.AudioClockEpoch = int64(epoch)
			mp.AudioClock = pts
			mp.ClockMutex.Unlock()
		}
	}
	return 0
}

// audioDriverStartPlatform — C: audio_driver_init (mac_audio.c:369).
func audioDriverStartPlatform(settings any) (*AudioClass, error) {
	C.mach_timebase_info(&macAudioTimebase)
	go macAudioThread() // C: hts_thread_create_detached("audioloop", ...)
	for {
		// C: __sync_synchronize(); if(audio_run_loop) break; usleep(100)
		if atomic.LoadPointer(&macAudioRunLoop) != nil {
			break
		}
		time.Sleep(100 * time.Microsecond)
	}
	// C: &mac_audio_class — fini/reconfig/deliver_locked/pause/play/
	// flush/set_volume.
	return &AudioClass{
		Fini: func(ad *AudioDecoder) { macAudioFini(ad) },
		Reconfig: func(ad *AudioDecoder) error {
			if macAudioReconfig(ad) != 0 {
				return errReconfig
			}
			return nil
		},
		DeliverLocked: macAudioDeliver,
		Pause:         macAudioPause,
		Play:          macAudioPlay,
		Flush:         macAudioFlush,
		SetVolume:     macAudioSetVolume,
	}, nil
}
