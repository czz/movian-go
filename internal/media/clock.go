package media

import (
	"unsafe"

	"math"
	"sync"
	"time"

	mediacore "github.com/czz/movian-go/internal/media/core"
)

// MediaClock tracks the current playback time using audio PTS anchored
// to wall time, mirroring C's mp_audio_clock / mp_audio_clock_avtime system.
//
// C reference (glw_video_common.c:193-265):
//
//	aclock = mp_audio_clock + frame_start_avtime - mp_audio_clock_avtime + mp_avdelta
//	avdiff = aclock - video_pts
//
// The audio PTS is anchored to wall time at the moment audio is written to
// the hardware. The current audio clock is extrapolated by adding elapsed
// wall time since the anchor point.
type MediaClock struct {
	mu sync.Mutex

	// audioClock is the audio PTS at the moment audio was written (mp_audio_clock)
	audioClock int64

	// audioClockAvtime is the wall time (microseconds since monotonic epoch)
	// when audioClock was set (mp_audio_clock_avtime)
	audioClockAvtime int64

	// audioClockEpoch is the epoch for discontinuity detection (mp_audio_clock_epoch)
	audioClockEpoch int

	// avdelta is the user-adjustable audio delay offset in microseconds (mp_avdelta)
	avdelta int64

	// wallStart is when playback started (wall clock)
	wallStart time.Time

	// pausedAt is when pause began (zero if not paused)
	pausedAt time.Time

	// totalPaused is accumulated paused duration
	totalPaused time.Duration

	// hasAudio indicates if audio stream is present
	hasAudio bool

	// offset is added to PTS for seek adjustments
	offset int64

	// epoch is the current clock epoch (incremented on seek/discontinuity)
	epoch int
}

// NewMediaClock creates a new media clock.
func NewMediaClock(hasAudio bool) *MediaClock {
	return &MediaClock{
		wallStart: time.Now(),
		hasAudio:  hasAudio,
	}
}

// monotonicUs returns current wall time in microseconds from a monotonic source.
// This mirrors C's arch_get_avtime().
func monotonicUs() int64 {
	return int64(time.Now().UnixNano() / 1000)
}

// UpdateAudioPTS sets the current audio clock, anchoring it to wall time.
// This mirrors C's alsa_audio_deliver() which sets:
//
//	mp_audio_clock = pts
//	mp_audio_clock_avtime = trigger_htstamp + samples/sample_rate
//
// The wall time is the current monotonic time (approximation — C uses
// snd_pcm_status_get_trigger_htstamp for precision, but monotonic time
// is sufficient for Go's ALSA backend which writes synchronously).
func (mc *MediaClock) UpdateAudioPTS(ptsUs int64) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.audioClock = ptsUs
	mc.audioClockAvtime = monotonicUs()
}

// SetAudioClockWithEpoch sets the audio clock with an epoch for discontinuity detection.
func (mc *MediaClock) SetAudioClockWithEpoch(ptsUs int64, epoch int) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.audioClock = ptsUs
	mc.audioClockAvtime = monotonicUs()
	mc.audioClockEpoch = epoch
	// Clear offset once audio clock takes over.
	// In C, mp_audio_clock IS the absolute position — there's no offset.
	// The offset is only used for wall clock fallback before audio starts
	// (e.g., right after seek before first audio packet is decoded).
	// Once audio clock is set, offset must be 0 to avoid double-counting.
	mc.offset = 0
}

// SetAVDelta sets the user-adjustable audio delay offset (microseconds).
// Mirrors C's update_av_delta() in media_settings.c:37-43.
func (mc *MediaClock) SetAVDelta(deltaUs int64) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.avdelta = deltaUs
}

// GetAVDelta returns the current AV delta setting.
func (mc *MediaClock) GetAVDelta() int64 {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	return mc.avdelta
}

// GetAudioClock returns the extrapolated audio clock at the current wall time.
// This mirrors C's:
//
//	aclock = mp_audio_clock + gr_frame_start_avtime - mp_audio_clock_avtime + mp_avdelta
func (mc *MediaClock) GetAudioClock() int64 {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	if mc.audioClock <= 0 || mc.audioClockAvtime == 0 {
		return mc.offset
	}
	now := monotonicUs()
	elapsed := now - mc.audioClockAvtime
	return mc.audioClock + elapsed + mc.avdelta + mc.offset
}

// GetAudioClockEpoch returns the current audio clock epoch.
// Used by ComputeAVDiff to detect epoch mismatch (mirrors C's
// mp_audio_clock_epoch != epoch check at glw_video_common.c:204).
func (mc *MediaClock) GetAudioClockEpoch() int {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	return mc.audioClockEpoch
}

// GetTime returns the current playback time in microseconds.
// If audio is present and clock is anchored, uses audio clock extrapolation.
// Otherwise uses wall clock fallback.
func (mc *MediaClock) GetTime() int64 {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	if mc.hasAudio && mc.audioClock > 0 && mc.audioClockAvtime > 0 {
		now := monotonicUs()
		elapsed := now - mc.audioClockAvtime
		return mc.audioClock + elapsed + mc.avdelta + mc.offset
	}

	// Wall clock fallback
	elapsed := time.Since(mc.wallStart) - mc.totalPaused
	if !mc.pausedAt.IsZero() {
		elapsed -= time.Since(mc.pausedAt)
	}
	return int64(elapsed/time.Microsecond) + mc.offset
}

// Pause stops the clock.
func (mc *MediaClock) Pause() {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	if mc.pausedAt.IsZero() {
		mc.pausedAt = time.Now()
	}
}

// Resume continues the clock after a pause.
func (mc *MediaClock) Resume() {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	if !mc.pausedAt.IsZero() {
		pausedDur := time.Since(mc.pausedAt)
		mc.totalPaused += pausedDur
		// Re-anchor audio clock to current wall time
		if mc.audioClockAvtime > 0 {
			mc.audioClockAvtime = monotonicUs()
		}
		mc.pausedAt = time.Time{}
	}
}

// Seek resets the clock to a new position (microseconds).
func (mc *MediaClock) Seek(posUs int64) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.offset = posUs
	mc.audioClock = 0
	mc.audioClockAvtime = 0
	mc.wallStart = time.Now()
	mc.totalPaused = 0
	mc.pausedAt = time.Time{}
	mc.epoch++
}

// GetEpoch returns the current clock epoch.
func (mc *MediaClock) GetEpoch() int {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	return mc.epoch
}

// KalmanFilter implements a 1D Kalman filter for AVDiff smoothing.
// Mirrors C's kalman_t in misc/kalman.h:27-59.
//
// Parameters (from C):
//
//	Q = 1.0/100000.0 (very small process noise → slow response, strong smoothing)
//	R = 0.01 (measurement noise)
type KalmanFilter struct {
	xNext float64 // Predicted state estimate
	P     float64 // Error covariance
	K     float64 // Kalman gain
	Q     float64 // Process noise covariance
	R     float64 // Measurement noise covariance
}

// NewKalmanFilter creates a new Kalman filter with C's parameters.
func NewKalmanFilter() *KalmanFilter {
	return &KalmanFilter{
		P:     1.0,
		Q:     1.0 / 100000.0,
		R:     0.01,
		xNext: 0.0,
	}
}

// Start resets the filter to initial state (mirrors C's kalman_init).
func (k *KalmanFilter) Reset() {
	k.P = 1.0
	k.Q = 1.0 / 100000.0
	k.R = 0.01
	k.xNext = 0.0
}

// Update processes a new measurement and returns the smoothed estimate.
// Mirrors C's kalman_update():
//
//	P_next = P + Q
//	K = P_next / (P_next + R)
//	x = x_next + K * (z - x_next)
//	P = (1 - K) * P_next
//	x_next = x
func (k *KalmanFilter) Update(z float64) float64 {
	pNext := k.P + k.Q
	k.K = pNext / (pNext + k.R)
	x := k.xNext + k.K*(z-k.xNext)
	k.P = (1 - k.K) * pNext
	k.xNext = x
	return x
}

// AVDiff status codes (mirrors C's glw_video_common.h defines)
const (
	AVDiffLocked         = 0
	AVDiffNoLock         = 1
	AVDiffIncorrectEpoch = 2
	AVDiffHold           = 3
	AVDiffCatchUp        = 4
)

// AVDiff thresholds (from C's glw_video_common.c:224-225)
const (
	AVDiffHoldThreshold    = -0.1 // seconds: video ahead of audio by > 100ms
	AVDiffCatchUpThreshold = 0.1  // seconds: audio ahead of video by > 100ms
	AVDiffLockThreshold    = 30.0 // seconds: if |avdiff| > 30s, no lock
)

// ComputeAVDiff calculates the audio-video difference.
// Mirrors C's glw_video_compute_avdiff() (glw_video_common.c:193-265).
//
// Returns: (avdiffUs, avdiffSmoothed, statusCode)
//
//	avdiffUs = audio_clock - video_pts (microseconds)
//	avdiffSmoothed = Kalman-filtered avdiff in seconds
//	statusCode = AVDiffLocked | AVDiffNoLock | AVDiffHold | AVDiffCatchUp | AVDiffIncorrectEpoch
//
// videoEpoch is the epoch of the current video frame. If it doesn't match
// the audio clock epoch, AVDiffIncorrectEpoch is returned (mirrors C's
// mp_audio_clock_epoch != epoch check at glw_video_common.c:204-208).
func (fp *FramePacer) ComputeAVDiff(videoPtsUs int64, videoEpoch int) (int64, float64, int) {
	// Epoch check — mirrors C's glw_video_common.c:204-208:
	// if(mp->mp_audio_clock_epoch != epoch) {
	//   gv->gv_avdiff_x = 0;
	//   kalman_init(&gv->gv_avfilter);
	//   code = AVDIFF_INCORRECT_EPOCH;
	// }
	audioEpoch := fp.clock.GetAudioClockEpoch()
	if audioEpoch != videoEpoch {
		fp.avfilter.Reset()
		return 0, 0, AVDiffIncorrectEpoch
	}

	audioClock := fp.clock.GetAudioClock()
	avdiffUs := audioClock - videoPtsUs
	avdiffSec := float64(avdiffUs) / 1000000.0

	// Kalman filter smoothing
	avdiffSmoothed := fp.avfilter.Update(avdiffSec)

	// Clamp to ±30 seconds (mirrors C's clamp at line 220)
	if avdiffSmoothed > AVDiffLockThreshold {
		avdiffSmoothed = AVDiffLockThreshold
	} else if avdiffSmoothed < -AVDiffLockThreshold {
		avdiffSmoothed = -AVDiffLockThreshold
	}

	// Determine status code (mirrors C's threshold checks at lines 216-225)
	// C: abs(gv_avdiff) < 30000000  where gv_avdiff is in microseconds (30s)
	// Go: avdiffSec is in seconds, AVDiffLockThreshold is 30.0 seconds
	code := AVDiffLocked
	if math.Abs(avdiffSec) > AVDiffLockThreshold {
		code = AVDiffNoLock
	} else if avdiffSmoothed > AVDiffCatchUpThreshold {
		code = AVDiffCatchUp
	} else if avdiffSmoothed < AVDiffHoldThreshold {
		code = AVDiffHold
	}

	return avdiffUs, avdiffSmoothed, code
}

// ComputeOutputDuration adjusts frame display duration based on AVDiff.
// Mirrors C's glw_video_compute_output_duration() (glw_video_common.c:152-171).
//
// Positive AVDiff (audio ahead) → longer duration (slow down video)
// Negative AVDiff (video ahead) → shorter duration (speed up video)
// Uses quadratic function: delta = (avdiff * 1000)^2, clamped to ±5000us.
func (fp *FramePacer) ComputeOutputDuration(frameDurationUs int64, avdiffSmoothed float64) int64 {
	const maxDiff = int64(5000) // microseconds

	var delta int64
	if avdiffSmoothed > 0 {
		delta = min(int64(math.Pow(avdiffSmoothed*1000.0, 2)), maxDiff)
	} else if avdiffSmoothed < 0 {
		delta = max(-int64(math.Pow(-avdiffSmoothed*1000.0, 2)), -maxDiff)
	}
	return frameDurationUs + delta
}

// FramePacer manages a bounded queue of decoded video frames and
// schedules display based on PTS vs the media clock.
// This mirrors C's render-thread frame scheduling in glw_video_render().
type FramePacer struct {
	mu   sync.Mutex
	cond *sync.Cond

	clock *MediaClock

	// Queue of frames sorted by PTS
	frames []*mediacoreFrameEntry

	// Max queue size
	maxSize int

	// Current displayed frame (for repeated display)
	currentFrame *mediacoreFrameEntry

	// dropThreshold: if frame PTS is this much behind clock, drop it
	dropThreshold int64 // microseconds

	// AVDiff smoothing filter (mirrors C's gv_avfilter)
	avfilter *KalmanFilter

	// Counters for instrumentation
	droppedFrames   int
	heldFrames      int
	displayedFrames int

	// epoch is the current playback epoch, updated by SetEpoch.
	// Used by computeAVDiffLocked for epoch mismatch detection.
	epoch int
}

type mediacoreFrameEntry struct {
	frame    *mediacore.FrameInfo
	ptsUs    int64
	duration int64
}

// NewFramePacer creates a new frame pacer with AVDiff-based scheduling.
func NewFramePacer(clock *MediaClock) *FramePacer {
	fp := &FramePacer{
		clock:         clock,
		maxSize:       16,
		dropThreshold: 100000, // 100ms — drop frames more than 100ms late
		avfilter:      NewKalmanFilter(),
	}
	fp.cond = sync.NewCond(&fp.mu)
	return fp
}

// SetEpoch updates the frame pacer's epoch (called after seek).
func (fp *FramePacer) SetEpoch(epoch int) {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	fp.epoch = epoch
}

// SubmitFrame adds a decoded frame to the queue (called from decode thread).
// Blocks if the queue is full, providing backpressure equivalent to C's
// mb_enqueue_with_events() which blocks on hts_cond_wait(mp_backpressure)
// when the video queue is full. The display loop (DisplayFrame) consumes
// frames at real-time rate, unblocking SubmitFrame.
func (fp *FramePacer) SubmitFrame(fi *mediacore.FrameInfo) {
	fp.mu.Lock()
	defer fp.mu.Unlock()

	// Block if queue is full — this is the video backpressure mechanism.
	// C's producer blocks when mp_buffer_current >= mp_buffer_limit;
	// Go's packetLoop blocks here when the frame queue is full.
	// The display loop consumes frames at real-time rate (governed by clock),
	// naturally throttling the packetLoop to real-time playback speed.
	for len(fp.frames) >= fp.maxSize {
		fp.cond.Wait()
	}

	fp.frames = append(fp.frames, &mediacoreFrameEntry{
		frame:    fi,
		ptsUs:    fi.PTS,
		duration: fi.Duration,
	})
}

// DisplayFrame returns the frame that should be displayed now, or nil if
// no frame is ready. Uses AVDiff-based scheduling mirroring C's
// glw_video_newframe_blend() (glw_video_common.c:390-400).
//
// AVDiffHold: video is ahead of audio → hold current frame, don't advance
// AVDiffCatchUp: audio is ahead of video → drop current frame, try next
func (fp *FramePacer) DisplayFrame() *mediacore.FrameInfo {
	clockTime := fp.clock.GetTime()

	fp.mu.Lock()
	defer fp.mu.Unlock()

	// Drop frames that are too late (CATCH_UP logic)
	// Mirrors C: if avdiff > 0.1s, drop frame and reset Kalman.
	// C drops one frame, resets Kalman, then goto again to try next.
	// We drop all late frames but only reset Kalman once (matching C's
	// single kalman_init per catch-up cycle).
	kalmanResetDone := false
	for len(fp.frames) > 0 {
		f := fp.frames[0]
		if f.ptsUs < clockTime-fp.dropThreshold {
			// Frame is too late, drop it
			if f.frame != nil && f.frame.RefRelease != nil {
				f.frame.RefRelease(unsafe.Pointer(f.frame.RefAux))
			}
			fp.frames = fp.frames[1:]
			fp.droppedFrames++
			// Signal SubmitFrame that space is available
			fp.cond.Signal()
			// Reset Kalman once per catch-up cycle (mirrors C's kalman_init at line 397)
			if !kalmanResetDone && fp.avfilter != nil {
				fp.avfilter.Reset()
				kalmanResetDone = true
			}
			continue
		}
		break
	}

	// Check AVDiff for HOLD logic before displaying
	// Mirrors C: if avdiff < -0.1s, hold current frame
	if len(fp.frames) > 0 && fp.currentFrame != nil {
		nextFrame := fp.frames[0]
		_, _, code := fp.computeAVDiffLocked(nextFrame.ptsUs)
		if code == AVDiffHold {
			// Video is ahead of audio — hold current frame
			fp.heldFrames++
			if fp.avfilter != nil {
				fp.avfilter.Reset()
			}
			return fp.currentFrame.frame
		}
	}

	// Display frames that are due
	for len(fp.frames) > 0 {
		f := fp.frames[0]
		if f.ptsUs <= clockTime {
			// Frame is due — display it
			if fp.currentFrame != nil && fp.currentFrame.frame != nil && fp.currentFrame.frame.RefRelease != nil {
				fp.currentFrame.frame.RefRelease(unsafe.Pointer(fp.currentFrame.frame.RefAux))
			}
			fp.currentFrame = f
			fp.frames = fp.frames[1:]
			fp.displayedFrames++
			// Signal SubmitFrame that space is available
			fp.cond.Signal()
			continue
		}
		break
	}

	if fp.currentFrame != nil {
		return fp.currentFrame.frame
	}
	return nil
}

// computeAVDiffLocked computes AVDiff without acquiring the mutex (caller must hold lock).
func (fp *FramePacer) computeAVDiffLocked(videoPtsUs int64) (int64, float64, int) {
	// Epoch check (mirrors C's glw_video_common.c:204-208)
	audioEpoch := fp.clock.GetAudioClockEpoch()
	if audioEpoch != fp.epoch {
		fp.avfilter.Reset()
		return 0, 0, AVDiffIncorrectEpoch
	}

	audioClock := fp.clock.GetAudioClock()
	avdiffUs := audioClock - videoPtsUs
	avdiffSec := float64(avdiffUs) / 1000000.0

	avdiffSmoothed := fp.avfilter.Update(avdiffSec)

	if avdiffSmoothed > AVDiffLockThreshold {
		avdiffSmoothed = AVDiffLockThreshold
	} else if avdiffSmoothed < -AVDiffLockThreshold {
		avdiffSmoothed = -AVDiffLockThreshold
	}

	code := AVDiffLocked
	if math.Abs(avdiffSec) > AVDiffLockThreshold {
		code = AVDiffNoLock
	} else if avdiffSmoothed > AVDiffCatchUpThreshold {
		code = AVDiffCatchUp
	} else if avdiffSmoothed < AVDiffHoldThreshold {
		code = AVDiffHold
	}

	return avdiffUs, avdiffSmoothed, code
}

// Clear empties the frame queue and releases all frames.
// Wakes up any SubmitFrame blocked on cond.Wait so it can
// re-check the queue size (which is now 0).
func (fp *FramePacer) Clear() {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	for _, f := range fp.frames {
		if f.frame != nil && f.frame.RefRelease != nil {
			f.frame.RefRelease(unsafe.Pointer(f.frame.RefAux))
		}
	}
	fp.frames = nil
	if fp.currentFrame != nil && fp.currentFrame.frame != nil && fp.currentFrame.frame.RefRelease != nil {
		fp.currentFrame.frame.RefRelease(unsafe.Pointer(fp.currentFrame.frame.RefAux))
	}
	fp.currentFrame = nil
	// Wake up any blocked SubmitFrame callers
	fp.cond.Broadcast()
}

// QueueLen returns the number of frames in the queue.
func (fp *FramePacer) QueueLen() int {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	return len(fp.frames)
}

// GetStats returns frame pacing statistics for instrumentation.
func (fp *FramePacer) GetStats() (displayed, dropped, held int) {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	return fp.displayedFrames, fp.droppedFrames, fp.heldFrames
}
