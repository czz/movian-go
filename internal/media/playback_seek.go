package media

import (
	"fmt"

	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/media/libav"
)

// SeekToPosition seeks to a position in seconds.
// After EOF, decoders may have been cleaned up via partialCleanup().
// This function rebuilds decoders if needed, allowing seek-after-EOF.
func (p *PlaybackPipeline) SeekToPosition(positionSec int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.format == nil {
		return fmt.Errorf("no media opened")
	}

	timestamp := positionSec * 1000000
	if err := p.format.SeekTimestamp(timestamp); err != nil {
		return fmt.Errorf("seek failed: %w", err)
	}

	// If decoders were cleaned up by partialCleanup (after EOF),
	// rebuild them so playback can resume after seek.
	if p.audioDecoder == nil && p.audioStreamIndex >= 0 {
		if err := p.rebuildAudioDecoder(); err != nil {
			return fmt.Errorf("seek: failed to rebuild audio decoder: %w", err)
		}
	}
	if p.videoCodec == nil && p.videoStreamIndex >= 0 {
		if err := p.rebuildVideoCodec(); err != nil {
			return fmt.Errorf("seek: failed to rebuild video codec: %w", err)
		}
	}

	// Flush audio decoder to clear buffered samples (mirrors C's avcodec_flush_buffers)
	if p.audioDecoder != nil {
		libav.LibAVAudioFlush(p.audioDecoder)
	}

	// Flush video decoder codec buffers (mirrors C's avcodec_flush_buffers in video_decoder)
	if p.videoCodec != nil && p.videoCodec.Ctx != nil {
		libav.FlushVideoCodec(p.videoCodec)
	}

	// Clear frame pacer queue to avoid displaying stale frames
	if p.framePacer != nil {
		p.framePacer.Clear()
	}

	// Get format start_time for coordinate system alignment.
	// C uses container timeline (includes start_time) for both seekTarget
	// and mp_set_current_time. Go must match this to ensure packet PTS
	// (which is in container timeline) aligns with seekTarget and clock.
	startTime := int64(0)
	if p.format != nil {
		startTime = p.format.GetStartTime()
	}

	// Reset media clock to new position.
	// Use container timeline (timestamp + startTime) to match C's
	// mp_set_current_time which receives mb_user_time in container timeline.
	// This ensures the clock is in the same coordinate system as packet PTS.
	if p.mediaClock != nil {
		p.mediaClock.Seek(timestamp + startTime)
	}

	// Set seek target for packet skipping (mirrors C's mq->mq_seektarget).
	// After av_seek_frame with BACKWARD, the file may be positioned at an
	// earlier keyframe. The packetLoop will skip packets with PTS < seekTarget
	// until it reaches the target position.
	//
	// C sets mq_seektarget = pos where pos = timestamp + start_time.
	// Packet PTS values (pktPtsUs) are in container timeline (include
	// start_time), so seekTarget must also include start_time for the
	// comparison to use the same coordinate system on both sides.
	p.seekTarget = timestamp + startTime

	// Increment epoch (mirrors C's mp_epoch++ in mp_flush_locked)
	p.epoch++
	if p.framePacer != nil {
		p.framePacer.SetEpoch(p.epoch)
	}

	// Reset state to stopped (user must call Play to resume)
	p.state = PlaybackStateStopped

	if p.propCurrentTime != nil {
		p.propMgr.SetIntEx(p.propCurrentTime, nil, int(positionSec))
	}

	return nil
}

// rebuildAudioDecoder recreates the audio decoder after partialCleanup.
func (p *PlaybackPipeline) rebuildAudioDecoder() error {
	if p.format == nil || p.audioStreamIndex < 0 {
		return nil
	}
	si := p.format.GetStreamInfo(p.audioStreamIndex)
	if si == nil {
		return fmt.Errorf("no audio stream info")
	}

	params := &mediacore.MediaCodecParams{
		CodecID:    mediacore.CodecID(si.CodecID),
		MediaType:  mediacore.MediaTypeAudio,
		SampleRate: si.SampleRate,
		Channels:   si.Channels,
	}
	extra := p.format.GetStreamExtradata(p.audioStreamIndex)
	if len(extra) > 0 {
		params.ExtraData = extra
		params.ExtraDataSize = len(extra)
		params.ExtradataSize = len(extra)
	}

	fmt2 := mediacore.MediaFormatCreate(p.format.LibavCtx())
	sctx := p.format.CodecCtxFromStream(p.audioStreamIndex)
	p.audioCodec = mediacore.MediaCodecCreate(
		mediacore.CodecID(si.CodecID), 0, fmt2, sctx.CPtr(), params, p.mediaPipe)
	libav.FreeCodecCtx(sctx)
	if p.audioCodec == nil {
		return fmt.Errorf("rebuild audio codec: no codec for id %d", si.CodecID)
	}
	p.audioCodec.FmtCtx = nil // temp ctx consumed by copy_codec_context
	p.audioCodec.MediaType = mediacore.MediaTypeAudio

	ad, err := libav.NewLibAVAudio(p.audioCodec, p.audioQueue, p.mediaPipe)
	if err != nil {
		return fmt.Errorf("rebuild audio decoder: %w", err)
	}
	libav.LibAVAudioSetSampleRate(ad, 48000)
	libav.LibAVAudioSetChannels(ad, 2)
	p.audioDecoder = ad
	return nil
}

// rebuildVideoCodec recreates the video codec context after partialCleanup.
func (p *PlaybackPipeline) rebuildVideoCodec() error {
	if p.format == nil || p.videoStreamIndex < 0 {
		return nil
	}
	si := p.format.GetStreamInfo(p.videoStreamIndex)
	if si == nil {
		return fmt.Errorf("no video stream info")
	}

	params := &mediacore.MediaCodecParams{
		CodecID:   mediacore.CodecID(si.CodecID),
		MediaType: mediacore.MediaTypeVideo,
		Width:     si.Width,
		Height:    si.Height,
	}
	extra := p.format.GetStreamExtradata(p.videoStreamIndex)
	if len(extra) > 0 {
		params.ExtraData = extra
		params.ExtraDataSize = len(extra)
		params.ExtradataSize = len(extra)
	}

	fmt2 := mediacore.MediaFormatCreate(p.format.LibavCtx())
	sctx := p.format.CodecCtxFromStream(p.videoStreamIndex)
	p.videoCodec = mediacore.MediaCodecCreate(
		mediacore.CodecID(si.CodecID), 0, fmt2, sctx.CPtr(), params, p.mediaPipe)
	libav.FreeCodecCtx(sctx)
	if p.videoCodec == nil {
		return fmt.Errorf("rebuild video codec: no codec for id %d", si.CodecID)
	}
	p.videoCodec.FmtCtx = nil // temp ctx consumed by copy_codec_context
	p.videoCodec.MediaType = mediacore.MediaTypeVideo

	p.videoDecoder = &libav.VideoDecoder{
		MP:              p.mediaPipe,
		DeliverCallback: p.DeliverCallback,
		ConvertPixFmt:   libav.AVPixFmtYuv420p,
		ConvertWidth:    si.Width,
		ConvertHeight:   si.Height,
	}
	if p.videoDecoder.DeliverCallback == nil {
		p.videoDecoder.DeliverCallback = p.DeliverCallback
	}
	return nil
}
