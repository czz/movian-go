package media

import (
	"time"

	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/media/libav"
)

// packetLoop reads packets from the format context and dispatches them
// to the appropriate decoder queues.
func (p *PlaybackPipeline) packetLoop() {
	defer p.wg.Done()

	for {
		// Check stop signal at top of loop
		select {
		case <-p.stopChan:
			return
		default:
		}

		// Check if paused or format closed
		p.mu.Lock()
		if p.state == PlaybackStatePaused {
			p.mu.Unlock()
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if p.state == PlaybackStateStopped || p.format == nil {
			p.mu.Unlock()
			return
		}
		p.mu.Unlock()

		// Read next packet
		pkt, err := p.format.ReadPacket()
		if err != nil {
			p.readErrorCount++
			p.ts.Error("PACKETLOOP", "ReadPacket error (%d): %v", p.readErrorCount, err)
			if p.readErrorCount > 10 {
				// Too many consecutive errors — treat as fatal
				p.ts.Error("PACKETLOOP", "Too many read errors, stopping")
				p.mu.Lock()
				p.state = PlaybackStateStopped
				p.mu.Unlock()
				p.cleanupResources()
				if p.propPlayStatus != nil {
					p.propMgr.SetStringEx(p.propPlayStatus, nil, "error", 0)
				}
				return
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		p.readErrorCount = 0

		// EOF
		if pkt == nil {
			p.ts.Debug("PACKETLOOP", "EOF reached, draining decoders")
			// Drain audio decoder and resampler before EOF handling.
			// Mirrors C's mp_wait_for_empty_queues() + decoder drain.
			// This ensures the last buffered audio frames are written to ALSA.
			p.drainDecoders()
			p.handleEOF()
			return
		}

		// Check stop signal again before dispatch (dispatch may block on ALSA)
		select {
		case <-p.stopChan:
			return
		default:
		}

		// Seek target skip logic (mirrors C's fa_video.c:284-292).
		// After av_seek_frame with BACKWARD, the file may be positioned at
		// an earlier keyframe. Skip packets with PTS < seekTarget.
		// For audio: skip entirely (no inter-frame dependencies).
		// For video: decode but don't submit to FramePacer (need reference frames).
		// When PTS >= seekTarget, clear seekTarget and resume normal dispatch.
		if p.seekTarget > 0 {
			pktPtsUs := int64(0)
			if si := p.format.GetStreamInfo(pkt.StreamIndex); si != nil && si.TimeBaseDen > 0 {
				pktPtsUs = pkt.PTS * int64(si.TimeBaseNum) * 1000000 / int64(si.TimeBaseDen)
			}
			if pktPtsUs < p.seekTarget {
				// Packet is before seek target — skip
				if pkt.StreamIndex == p.videoStreamIndex && p.videoCodec != nil {
					// Video: decode but don't submit to FramePacer
					// (need to build reference frame chain)
					si := p.format.GetStreamInfo(pkt.StreamIndex)
					mb := &mediacore.MediaBuf{
						Data:     pkt.Data,
						Size:     pkt.Size,
						PTS:      rescaleStreamTs(pkt.PTS, si),
						DTS:      rescaleStreamTs(pkt.DTS, si),
						Duration: rescaleStreamTs(pkt.Duration, si),
						Stream:   pkt.StreamIndex,
					}
					if pkt.Flags&0x0001 != 0 {
						mb.Flags.Keyframe = true
					}
					// Temporarily remove DeliverCallback to prevent frame submission
					savedCallback := p.videoDecoder.DeliverCallback
					p.videoDecoder.DeliverCallback = nil
					libav.LibAVDecodeVideo(p.videoCodec, p.videoDecoder, p.videoQueue, mb, 0)
					p.videoDecoder.DeliverCallback = savedCallback
				}
				// Audio: skip entirely
				continue
			}
			// Reached seek target
			p.seekTarget = 0
		}

		// Backpressure is provided by the outputs:
		// - Audio: ALSA Write blocks when buffer is full (real-time consumption)
		// - Video: FramePacer.SubmitFrame blocks when queue is full (display rate)
		// This mirrors C's producer-consumer architecture where the producer
		// blocks on hts_cond_wait(mp_backpressure) when the queue is full.
		// In Go's single-threaded architecture, the packetLoop is both producer
		// and consumer, so backpressure comes from the output side.

		// Dispatch packet to appropriate decoder
		p.dispatchPacket(pkt)

		// Update position based on packet PTS
		p.updatePosition(pkt)

		// Update buffer monitoring properties (mirrors C's mq_update_stats)
		// Use frame pacer queue length as proxy for video buffer level
		if p.propBufferCurrent != nil && p.framePacer != nil {
			p.propMgr.SetIntEx(p.propBufferCurrent, nil, p.framePacer.QueueLen())
		}
	}
}

// rescaleStreamTs converts a timestamp from stream time_base to microseconds
// (AV_TIME_BASE_Q), matching C's rescale() in fa_video.c.
// AV_NOPTS_VALUE (-1) is preserved as-is, identical to C's rescale().
func rescaleStreamTs(ts int64, si *libav.StreamInfo) int64 {
	if ts < 0 {
		return ts
	}
	if si == nil || si.TimeBaseDen <= 0 {
		return ts
	}
	return ts * int64(si.TimeBaseNum) * 1000000 / int64(si.TimeBaseDen)
}

// dispatchPacket sends a packet to the appropriate decoder based on stream index
func (p *PlaybackPipeline) dispatchPacket(pkt *libav.AVPacketInfo) {
	if p.format == nil {
		return
	}
	if pkt.StreamIndex == p.videoStreamIndex && p.videoDecoder != nil && p.videoCodec != nil {
		// Convert PTS/DTS/Duration from stream time_base to microseconds,
		// matching C's rescale(fctx, pkt.pts, si) in fa_video.c.
		// Without this, the FramePacer (which uses microseconds) would
		// compare against raw time_base values, causing A/V desync.
		si := p.format.GetStreamInfo(pkt.StreamIndex)
		mb := &mediacore.MediaBuf{
			Data:     pkt.Data,
			Size:     pkt.Size,
			PTS:      rescaleStreamTs(pkt.PTS, si),
			DTS:      rescaleStreamTs(pkt.DTS, si),
			Duration: rescaleStreamTs(pkt.Duration, si),
			Stream:   pkt.StreamIndex,
		}
		if pkt.Flags&0x0001 != 0 {
			mb.Flags.Keyframe = true
		}

		libav.LibAVDecodeVideo(p.videoCodec, p.videoDecoder, p.videoQueue, mb, 0)

	} else if pkt.StreamIndex == p.audioStreamIndex && p.audioCodec != nil {
		// Capture epoch before decoding. If a seek happens during decode
		// (in another goroutine), the epoch will change and we skip the
		// clock update. This mirrors C's mb->mb_epoch check in
		// alsa_audio_deliver: if(mb->mb_epoch != mp->mp_audio_clock_epoch) return;
		dispatchEpoch := p.epoch

		avPkt := libav.AvPacketFromInfo(pkt)
		if avPkt != nil {
			// Calculate packet PTS in microseconds for the audio decoder.
			// This matches C's mb->mb_pts = rescale(fctx, pkt.pts, si)
			// which converts stream timebase to AV_TIME_BASE_Q (microseconds).
			ptsUs := int64(-1)
			if pkt.PTS >= 0 {
				si := p.format.GetStreamInfo(pkt.StreamIndex)
				if si != nil && si.TimeBaseDen > 0 {
					ptsUs = pkt.PTS * int64(si.TimeBaseNum) * 1000000 / int64(si.TimeBaseDen)
				}
			}

			outBuf, err := libav.LibAVAudioDecode(p.audioDecoder, avPkt, ptsUs)
			if err != nil {
				p.ts.Error("DISPATCH", "audio decode error: %v", err)
			}
			if err == nil && outBuf != nil && len(outBuf.Data) > 0 {
				// Check if a seek happened during decode (epoch changed).
				// If so, skip ALSA write and clock update — this packet
				// is from the old seek position and would corrupt the clock.
				if p.epoch != dispatchEpoch {
					return
				}
				// Check epoch again after decode (seek might have
				// happened during it).
				if p.epoch != dispatchEpoch {
					return
				}
				// Update media clock with audio PTS (master clock).
				// Uses SetAudioClockWithEpoch to also set the epoch for
				// AVDiff discontinuity detection (mirrors C's alsa.c:176
				// mp->mp_audio_clock_epoch = epoch).
				if p.mediaClock != nil && ptsUs >= 0 {
					p.mediaClock.SetAudioClockWithEpoch(ptsUs, p.epoch)
				}
			}
			libav.AvPacketFree(avPkt)
		}
	}
	// Other stream types (e.g. subtitles) are dropped like C's
	// fa_audio.c:271-275 — packets for streams that are not the selected
	// audio stream are discarded. Embedded subtitle rendering is handled
	// by the canonical video pipeline (fa_video.c → MB_SUBTITLE →
	// video_decoder → video_overlay_decode), not by this pipeline.
}

// updatePosition updates the position property based on packet PTS
func (p *PlaybackPipeline) updatePosition(pkt *libav.AVPacketInfo) {
	if p.propCurrentTime == nil || pkt.PTS < 0 {
		return
	}

	if p.format == nil {
		return
	}

	si := p.format.GetStreamInfo(pkt.StreamIndex)
	if si == nil || si.TimeBaseDen == 0 {
		return
	}

	ptsUs := pkt.PTS * int64(si.TimeBaseNum) * 1000000 / int64(si.TimeBaseDen)
	posSec := ptsUs / 1000000

	p.mu.Lock()
	if p.mediaPipe != nil {
		p.mediaPipe.CurrentTime = ptsUs
	}
	p.mu.Unlock()

	p.propMgr.SetIntEx(p.propCurrentTime, nil, int(posSec))
}

// drainDecoders drains the audio decoder and resampler at EOF.
// Mirrors C's mp_wait_for_empty_queues() + audio.c:768-771 drain.
func (p *PlaybackPipeline) drainDecoders() {
	p.mu.Lock()
	audioDecoder := p.audioDecoder
	p.mu.Unlock()

	if audioDecoder != nil {
		if _, err := libav.LibAVAudioDrain(audioDecoder); err != nil {
			p.ts.Error("DRAIN", "audio drain error: %v", err)
		}
	}
}

// handleEOF handles end of file.
// Mirrors C's behavior: the format context is NOT closed at EOF,
// allowing subsequent seek operations. Only decoders and ALSA are
// cleaned up; the format stays open for potential seek-after-EOF.
// Full cleanup happens when Stop() is called or a new Open() begins.
//
// V1 FIX: Before transitioning to Stopped state, wait for the FramePacer
// queue to drain. This mirrors C's mp_wait_for_empty_queues() which waits
// for the media data queue to empty while the render loop continues
// displaying frames from gv_decoded_queue. Without this wait, Go clears
// the FramePacer queue (via partialCleanup) while 0-2 decoded frames are
// still queued for display, causing them to be lost. C displays these
// frames before mp_shutdown blackouts the screen.
func (p *PlaybackPipeline) handleEOF() {
	// Wait for FramePacer to drain before stopping.
	// The render loop checks state == Playing before calling DisplayFrame,
	// so state must remain Playing during this wait.
	p.waitForFramePacerDrain()

	p.mu.Lock()
	p.state = PlaybackStateStopped
	p.mu.Unlock()

	// Partial cleanup: close decoders and ALSA, but keep format open
	// so SeekToPosition() can work after EOF.
	// This mirrors C where mp_shutdown() does not close the format context.
	p.partialCleanup()

	// Notify PlayQueue to advance to next track
	// Only advance if this was a natural EOF, not a user-initiated Stop.
	// When Stop() is called, it sets stopRequested=true; if handleEOF()
	// runs concurrently (ReadPacket was in progress), it sees the flag
	// and skips the callback, preventing unwanted track advancement.
	if p.OnEOFCallback != nil && !p.stopRequested {
		p.OnEOFCallback()
	}
}

// waitForFramePacerDrain waits for the FramePacer queue to empty before
// transitioning to Stopped state. This mirrors C's mp_wait_for_empty_queues()
// which waits for the media data queue to drain while the render loop
// continues displaying frames from gv_decoded_queue.
//
// Invariants:
//   - p.mu is NOT held (render loop's GetState needs it)
//   - state remains Playing (render loop checks this before DisplayFrame)
//   - clock continues advancing (FramePacer uses it for PTS scheduling)
//   - timeout prevents hanging if clock stops or render loop is stuck
//   - stopChan is checked for immediate user interruption
func (p *PlaybackPipeline) waitForFramePacerDrain() {
	if p.framePacer == nil {
		return
	}
	const drainTimeout = 500 * time.Millisecond
	const pollInterval = 10 * time.Millisecond
	deadline := time.Now().Add(drainTimeout)
	for time.Now().Before(deadline) {
		// Check stop signal — if user pressed stop, don't wait
		select {
		case <-p.stopChan:
			return
		default:
		}
		// Check if FramePacer queue is empty
		if p.framePacer.QueueLen() == 0 {
			return
		}
		time.Sleep(pollInterval)
	}
	// Timeout — proceed with cleanup (don't hang)
}
