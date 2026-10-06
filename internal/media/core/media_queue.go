package core

import (
	"math"
	"slices"
	"sync"

	"github.com/czz/movian-go/internal/libav"
	propcore "github.com/czz/movian-go/internal/prop"
)

// ---------------------------------------------------------------------------
// media_buf.c
// ---------------------------------------------------------------------------

// MediaBufDtorFrameInfo frees the frame_info attached to a media buffer.
// C: media_buf_dtor_frame_info (media_buf.c:22)
func MediaBufDtorFrameInfo(mb *MediaBuf) {
	mb.FrameInfo = nil
}

// mediaBufDtorAVPacket releases the packet payload.
// C: media_buf_dtor_avpacket (media_buf.c:31) — av_packet_unref(&mb->mb_pkt)
func mediaBufDtorAVPacket(mb *MediaBuf) {
	if mb.Pkt != nil {
		libav.AvPacketUnref(mb.Pkt)
		libav.AvPacketFree(mb.Pkt)
		mb.Pkt = nil
	}
	mb.Data = nil
	mb.Size = 0
}

// poolGet returns a media buffer from the pool.
// C: pool_get with POOL_ZERO_MEM — returned memory is zeroed.
func (p *MediaBufPool) poolGet() *MediaBuf {
	if p == nil {
		return &MediaBuf{}
	}
	p.Mutex.Lock()
	defer p.Mutex.Unlock()
	n := len(p.Buffers)
	if n == 0 {
		return &MediaBuf{}
	}
	mb := p.Buffers[n-1]
	p.Buffers = p.Buffers[:n-1]
	*mb = MediaBuf{} // POOL_ZERO_MEM
	return mb
}

// poolPut returns a media buffer to the pool.
// C: pool_put
func (p *MediaBufPool) poolPut(mb *MediaBuf) {
	if p == nil {
		return
	}
	p.Mutex.Lock()
	defer p.Mutex.Unlock()
	p.Buffers = append(p.Buffers, mb)
}

// MediaBufAllocLocked allocates a media buffer with a payload.
// C: media_buf_alloc_locked (media_buf.c:46)
func MediaBufAllocLocked(mp *MediaPipe, size int) *MediaBuf {
	mb := mp.MbPool.poolGet()
	mb.Data = make([]byte, size)
	mb.Size = size
	mb.Dtor = mediaBufDtorAVPacket
	return mb
}

// MediaBufAllocUnlocked allocates a media buffer with a payload.
// C: media_buf_alloc_unlocked (media_buf.c:57)
func MediaBufAllocUnlocked(mp *MediaPipe, size int) *MediaBuf {
	mp.Mutex.Lock()
	defer mp.Mutex.Unlock()
	return MediaBufAllocLocked(mp, size)
}

// CopyMbmFromMb copies buffer meta from a media buffer.
// C: copy_mbm_from_mb (media_buf.c:82)
func CopyMbmFromMb(mbm *MediaBufMeta, mb *MediaBuf) {
	mbm.UserTime = mb.UserTime
	mbm.PTS = mb.PTS
	mbm.DTS = mb.DTS
	mbm.Epoch = mb.Epoch
	mbm.Duration = mb.Duration
	mbm.Flags = mb.Flags
	mbm.Sequence = mb.Sequence
}

// MediaBufFreeLocked frees a media buffer.
// C: media_buf_free_locked (media_buf.c:96)
func MediaBufFreeLocked(mp *MediaPipe, mb *MediaBuf) {
	if mb == nil {
		return
	}
	if mb.Dtor != nil {
		mb.Dtor(mb)
	}
	if mb.Codec != nil {
		MediaCodecDeref(mb.Codec)
	}
	// Clear references before pooling (pool zeroes on next get)
	mb.Data = nil
	mb.FrameInfo = nil
	mb.Meta = nil
	mb.Codec = nil
	if mp != nil {
		mp.MbPool.poolPut(mb)
	}
}

// MediaBufFreeUnlocked frees a media buffer.
// C: media_buf_free_unlocked (media_buf.c:110)
func MediaBufFreeUnlocked(mp *MediaPipe, mb *MediaBuf) {
	mp.Mutex.Lock()
	defer mp.Mutex.Unlock()
	MediaBufFreeLocked(mp, mb)
}

// mbBufferedSize is the buffer size charged against mp_buffer_current.
// C: mb_buffered_size (media_buf.h:156) — MAX(mb->mb_size, 4096)
func mbBufferedSize(mb *MediaBuf) int {
	if mb.Size > 4096 {
		return mb.Size
	}
	return 4096
}

// MbBufferedSize — exported C counterpart (media_buf.h:156); used by
// the video decoder thread in pkg/video/decoder.
func MbBufferedSize(mb *MediaBuf) int { return mbBufferedSize(mb) }

// ---------------------------------------------------------------------------
// media_queue.c
// ---------------------------------------------------------------------------

// mqFlushQ removes buffers from a single queue.
// C: mq_flush_q (media_queue.c:25)
func mqFlushQ(mp *MediaPipe, mq *MediaQueue, q *[]*MediaBuf, full bool) {
	kept := (*q)[:0]
	for _, mb := range *q {
		if mb.DataType == int(MBCtrlExit) {
			kept = append(kept, mb)
			continue
		}
		if mb.DataType == int(MBCtrlUnblock) && !full {
			kept = append(kept, mb)
			continue
		}
		mq.PacketsCurrent--
		mp.BufferCurrent -= mbBufferedSize(mb)
		MediaBufFreeLocked(mp, mb)
	}
	*q = kept
}

// mqFlushLocked flushes a media queue. Must be called with mp locked.
// C: mq_flush_locked (media_queue.c:45)
func mqFlushLocked(mp *MediaPipe, mq *MediaQueue, full bool) {
	mq.LastDeqDTS = PTSUnset
	mqFlushQ(mp, mq, &mq.DataQueue, full)
	mqFlushQ(mp, mq, &mq.CtrlQueue, full)
	mqFlushQ(mp, mq, &mq.AuxQueue, full)
	MqUpdateStats(mp, mq, 1)
}

// MqFlush flushes a media queue.
// C: mq_flush (media_queue.c:57)
func MqFlush(mp *MediaPipe, mq *MediaQueue, full bool) {
	mp.Mutex.Lock()
	defer mp.Mutex.Unlock()
	mqFlushLocked(mp, mq, full)
	mpCheckUnderrun(mp)
}

// MpFlushLocked flushes both media queues. Must be called with mp locked.
// C: mp_flush_locked (media_queue.c:67)
func MpFlushLocked(mp *MediaPipe, final int) {
	v := mp.Video
	a := mp.Audio

	if a != nil {
		mqFlushLocked(mp, a, false)
	}
	if v != nil {
		mqFlushLocked(mp, v, false)
	}

	mp.Epoch++

	if v != nil && v.Stream >= 0 {
		mb := MediaBufAllocLocked(mp, 0)
		mb.DataType = int(MBCtrlFlush)
		mb.Data32 = int32(final)
		MbEnq(mp, v, mb)
	}

	if a != nil && a.Stream >= 0 {
		mb := MediaBufAllocLocked(mp, 0)
		mb.DataType = int(MBCtrlFlush)
		mb.Data32 = int32(final)
		MbEnq(mp, a, mb)
	}

	if mp.Satisfied == 0 {
		mp.Sys.BufferHungry.Add(-1)
		mp.Satisfied = 1
	}
}

// MpFlush flushes both media queues.
// C: mp_flush (media_queue.c:99)
func MpFlush(mp *MediaPipe) {
	mp.Mutex.Lock()
	defer mp.Mutex.Unlock()
	MpFlushLocked(mp, 0)
}

// mqGetBufferDelay computes the delay spanned by the queue's data packets.
// C: mq_get_buffer_delay (media_queue.c:112)
func mqGetBufferDelay(mq *MediaQueue) int64 {
	if mq.Stream == -1 {
		return 0
	}

	q := mq.DataQueue
	var f, l *MediaBuf
	if len(q) > 0 {
		f = q[0]
		l = q[len(q)-1]
	}

	if f == nil {
		mq.BufferDelay = 0
		return 0
	}

	cnt := 20
	for f != nil && f.DTS == PTSUnset && cnt > 0 {
		idx := indexOfBuf(q, f) + 1
		if idx >= len(q) {
			f = nil
		} else {
			f = q[idx]
		}
		cnt--
	}

	cnt = 20
	for l != nil && l.DTS == PTSUnset && cnt > 0 {
		idx := indexOfBuf(q, l) - 1
		if idx < 0 {
			l = nil
		} else {
			l = q[idx]
		}
		cnt--
	}

	if f != nil && l != nil && f.Epoch == l.Epoch &&
		l.DTS != PTSUnset && f.DTS != PTSUnset {
		mq.BufferDelay = max(l.DTS-f.DTS, 0)
	}

	return mq.BufferDelay
}

func indexOfBuf(q []*MediaBuf, mb *MediaBuf) int {
	for i, x := range q {
		if x == mb {
			return i
		}
	}
	return -1
}

// MpUpdateBufferDelay updates mp_buffer_delay from both queues.
// C: mp_update_buffer_delay (media_queue.c:161)
func MpUpdateBufferDelay(mp *MediaPipe) {
	var vd, ad int64
	if mp.Video != nil {
		vd = mqGetBufferDelay(mp.Video)
	}
	if mp.Audio != nil {
		ad = mqGetBufferDelay(mp.Audio)
	}

	mp.BufferDelay = max(ad, vd)
}

// mpEnqueueCheckPreBuffering releases the pre-buffering hold once enough
// data is buffered.
// C: mp_enqueue_check_pre_buffering (media_queue.c:173)
func mpEnqueueCheckPreBuffering(mp *MediaPipe) {
	if mp.HoldFlags&MPHoldPreBuffering != 0 {
		if mp.BufferDelay > mp.PreBufferDelay {
			mp.HoldFlags &^= MPHoldPreBuffering
			mpSetPlaystatusByHoldLocked(mp, "")
		}
	}
}

// backpressureLocked returns mp.Backpressure, lazily initializing it for
// pipes not created via MpCreate. Must be called with mp.Mutex held.
func backpressureLocked(mp *MediaPipe) *sync.Cond {
	if mp.Backpressure == nil {
		mp.Backpressure = sync.NewCond(&mp.Mutex)
	}
	return mp.Backpressure
}

// MbEnqueueWithEvents enqueues a buffer, blocking on backpressure unless a
// pending event is available (which is then returned instead of enqueueing).
// C: mb_enqueue_with_events (media_queue.c:189)
func MbEnqueueWithEvents(mp *MediaPipe, mq *MediaQueue, mb *MediaBuf) *MediaEvent {
	var e *MediaEvent

	mp.Mutex.Lock()

	vminpkt := 0
	if mp.Video != nil && mp.Video.Stream != -1 {
		vminpkt = 5
	}
	aminpkt := 0
	if mp.Audio != nil && mp.Audio.Stream != -1 {
		aminpkt = 5
	}

	MpUpdateBufferDelay(mp)
	mpEnqueueCheckPreBuffering(mp)

	for {
		if len(mp.EventQueue) > 0 {
			e = mp.EventQueue[0]
			break
		}

		// Check if we are inside the realtime delay bounds
		if mp.BufferDelay < int64(mp.MaxRealtimeDelay) {
			// Check if buffer is full
			if mp.BufferCurrent+mbBufferedSize(mb) < mp.BufferLimit {
				break
			}
		}

		// These two safeguards so we don't run out of packets in any
		// of the queues
		if mp.Video != nil && mp.Video.PacketsCurrent < vminpkt {
			break
		}
		if mp.Audio != nil && mp.Audio.PacketsCurrent < aminpkt {
			break
		}

		backpressureLocked(mp).Wait()
	}

	if e != nil {
		mp.EventQueue = mp.EventQueue[1:]
	} else {
		MbEnq(mp, mq, mb)
	}

	mp.Mutex.Unlock()
	return e
}

// MbEnqueueNoBlock enqueues without blocking. Returns -1 if queues are full,
// 0 if enqueue succeeded.
// C: mb_enqueue_no_block (media_queue.c:243)
func MbEnqueueNoBlock(mp *MediaPipe, mq *MediaQueue, mb *MediaBuf, auxtype int) int {
	mp.Mutex.Lock()

	MpUpdateBufferDelay(mp)
	mpEnqueueCheckPreBuffering(mp)

	if mp.BufferCurrent+mbBufferedSize(mb) > mp.BufferLimit &&
		mq.PacketsCurrent < 5 {
		mp.Mutex.Unlock()
		return -1
	}

	if auxtype != -1 {
		var after *MediaBuf
		for _, v := range slices.Backward(mq.AuxQueue) {
			if v.DataType == auxtype {
				after = v
				break
			}
		}

		if after == nil {
			mq.AuxQueue = slices.Insert(mq.AuxQueue, 0, mb)
		} else {
			idx := indexOfBuf(mq.AuxQueue, after)
			mq.AuxQueue = append(mq.AuxQueue, nil)
			copy(mq.AuxQueue[idx+2:], mq.AuxQueue[idx+1:])
			mq.AuxQueue[idx+1] = mb
		}
	} else {
		mq.DataQueue = append(mq.DataQueue, mb)
	}

	mq.PacketsCurrent++
	mp.BufferCurrent += mbBufferedSize(mb)
	mb.Epoch = int(mp.Epoch)
	MqUpdateStats(mp, mq, 0)
	if mq.Avail != nil {
		mq.Avail.Signal()
	}

	mp.Mutex.Unlock()
	return 0
}

// MbEnqueueAlways enqueues unconditionally.
// C: mb_enqueue_always (media_queue.c:283)
func MbEnqueueAlways(mp *MediaPipe, mq *MediaQueue, mb *MediaBuf) {
	mp.Mutex.Lock()
	defer mp.Mutex.Unlock()
	MbEnq(mp, mq, mb)
}

// MqUpdateStats updates buffer statistics and queue props.
// C: mq_update_stats (media_queue.c:295)
func MqUpdateStats(mp *MediaPipe, mq *MediaQueue, force int) {
	if force == 0 {
		mp.StatsUpdateLimiter--
		if mp.StatsUpdateLimiter > 0 {
			return
		}
	}
	mp.StatsUpdateLimiter = 100

	satisfied := mp.Eof ||
		mp.BufferCurrent == 0 ||
		mp.BufferCurrent*8 > mp.BufferLimit*7 ||
		mp.Flags&MPAlwaysSatisfied != 0

	if satisfied {
		if mp.Satisfied == 0 {
			mp.Sys.BufferHungry.Add(-1)
			mp.Satisfied = 1
		}
	} else {
		if mp.Satisfied != 0 {
			mp.Sys.BufferHungry.Add(1)
			mp.Satisfied = 0
		}
	}

	MpUpdateBufferDelay(mp)

	if mq.PropQlenCur != nil {
		mq.PropQlenCur.SetInt(mq.PacketsCurrent)
	}
	if mp.PropBufferCurrent != nil {
		mp.PropBufferCurrent.SetInt(mp.BufferCurrent)
	}
	if mp.PropBufferDelay != nil {
		if mp.BufferDelay == math.MaxInt32 {
			mp.PropBufferDelay.SetVoid()
		} else {
			mp.PropBufferDelay.SetFloat(float32(float64(mp.BufferDelay) / 1000000.0))
		}
	}
}

// MqSetup initializes a media queue.
// C: mq_init (media_queue.c:335)
func MqSetup(mq *MediaQueue, p *propcore.Prop, mp *MediaPipe) {
	mq.MP = mp
	mq.LastDeqDTS = PTSUnset
	mq.DataQueue = nil
	mq.CtrlQueue = nil
	mq.AuxQueue = nil

	mq.PacketsCurrent = 0
	mq.Stream = -1
	mq.Avail = sync.NewCond(&mp.Mutex)

	var pm *propcore.PropManager
	if p != nil {
		pm = p.Manager()
	}
	if pm == nil {
		return
	}
	mq.PropQlenCur = pm.CreateEx(p, "dqlen", nil, false, false)
	mq.PropQlenMax = pm.CreateEx(p, "dqmax", nil, false, false)
	mq.PropBitrate = pm.CreateEx(p, "bitrate", nil, false, false)
	mq.PropDecodeAvg = pm.CreateEx(p, "decodetime_avg", nil, false, false)
	mq.PropDecodePeak = pm.CreateEx(p, "decodetime_peak", nil, false, false)
	mq.PropUploadAvg = pm.CreateEx(p, "uploadtime_avg", nil, false, false)
	mq.PropUploadPeak = pm.CreateEx(p, "uploadtime_peak", nil, false, false)
	mq.PropCodec = pm.CreateEx(p, "codec", nil, false, false)
	mq.PropTooSlow = pm.CreateEx(p, "too_slow", nil, false, false)
}

// MqDestroy destroys a media queue.
// C: mq_destroy (media_queue.c:362) — hts_cond_destroy(&mq->mq_avail)
func MqDestroy(mq *MediaQueue) {
	// sync.Cond requires no teardown
}

// MpWaitForEmptyQueues blocks until the data queues drain or an event is
// pending; returns the event (or nil).
// C: mp_wait_for_empty_queues (media_queue.c:370)
func MpWaitForEmptyQueues(mp *MediaPipe) *MediaEvent {
	mp.Mutex.Lock()
	defer mp.Mutex.Unlock()

	// Only wait for data queues to drain, aux (subtitles) might be stalled
	for len(mp.EventQueue) == 0 &&
		((mp.Audio != nil && len(mp.Audio.DataQueue) > 0) ||
			(mp.Video != nil && len(mp.Video.DataQueue) > 0)) {
		backpressureLocked(mp).Wait()
	}

	var e *MediaEvent
	if len(mp.EventQueue) > 0 {
		e = mp.EventQueue[0]
		mp.EventQueue = mp.EventQueue[1:]
	}

	return e
}

// MpSendCmd sends a control command to a queue.
// C: mp_send_cmd (media_queue.c:398)
func MpSendCmd(mp *MediaPipe, mq *MediaQueue, cmd int) {
	mp.Mutex.Lock()
	defer mp.Mutex.Unlock()
	MpSendCmdLocked(mp, mq, cmd)
}

// MpSendCmdData sends a control command with a data pointer.
// C: mp_send_cmd_data (media_queue.c:410)
func MpSendCmdData(mp *MediaPipe, mq *MediaQueue, cmd int, d any) {
	mp.Mutex.Lock()
	defer mp.Mutex.Unlock()

	mb := MediaBufAllocLocked(mp, 0)
	mb.DataType = cmd
	if b, ok := d.([]byte); ok {
		mb.Data = b
		mb.Size = len(b)
	} else {
		mb.DataOpaque = d
	}
	MbEnq(mp, mq, mb)
}

// MpSendCmdU32 sends a control command with a u32 payload.
// C: mp_send_cmd_u32 (media_queue.c:429)
func MpSendCmdU32(mp *MediaPipe, mq *MediaQueue, cmd int, u uint32) {
	mp.Mutex.Lock()
	defer mp.Mutex.Unlock()

	mb := MediaBufAllocLocked(mp, 0)
	mb.DataType = cmd
	mb.Data32 = int32(u)
	MbEnq(mp, mq, mb)
}

// mbPropDtor releases the prop reference and packet payload.
// C: mb_prop_dtor (media_queue.c:447)
func mbPropDtor(mb *MediaBuf) {
	if mb.Prop != nil {
		if pm := mb.Prop.Manager(); pm != nil {
			pm.RefDec(mb.Prop)
		}
		mb.Prop = nil
	}
	mediaBufDtorAVPacket(mb)
}

// MpSendPropSetString queues a prop string update on a queue.
// C: mp_send_prop_set_string (media_queue.c:458)
func MpSendPropSetString(mp *MediaPipe, mq *MediaQueue, prop *propcore.Prop, str string) {
	datasize := len(str) + 1
	mp.Mutex.Lock()
	defer mp.Mutex.Unlock()

	mb := MediaBufAllocLocked(mp, datasize)
	copy(mb.Data, str)
	mb.Data[datasize-1] = 0
	mb.DataType = int(MBSetPropString)
	if pm := prop.Manager(); pm != nil {
		mb.Prop = pm.RefInc(prop)
	} else {
		mb.Prop = prop
	}
	mb.Dtor = mbPropDtor
	MbEnq(mp, mq, mb)
}

// MpSendCmdLocked sends a control command to a queue (mp locked).
// C: mp_send_cmd_locked (media_queue.c:481)
func MpSendCmdLocked(mp *MediaPipe, mq *MediaQueue, cmd int) {
	mb := mp.MbPool.poolGet()
	mb.DataType = cmd
	MbEnq(mp, mq, mb)
}

// MpSendVolumeUpdateLocked queues a volume-multiplier update on the audio
// queue (mp locked).
// C: mp_send_volume_update_locked (media_queue.c:492)
func MpSendVolumeUpdateLocked(mp *MediaPipe) {
	v := float32(math.Pow(10.0, float64(mp.VolUser)/20.0)) * float32(mp.VolUI)
	mb := MediaBufAllocLocked(mp, 0)
	mb.DataType = int(MBCtrlSetVolumeMultiplier)
	mb.Float = v
	MbEnq(mp, mp.Audio, mb)
}

// MbEnq appends a buffer to the appropriate queue (mp locked).
// C: mb_enq (media_queue.c:504)
func MbEnq(mp *MediaPipe, mq *MediaQueue, mb *MediaBuf) {
	doSignal := true

	if mb.DataType == int(MBSubtitle) {
		mq.AuxQueue = append(mq.AuxQueue, mb)
	} else if mb.DataType > int(MBCtrl) {
		mq.CtrlQueue = append(mq.CtrlQueue, mb)
	} else {
		mq.DataQueue = append(mq.DataQueue, mb)
		doSignal = !mq.NoDataInterest
	}
	mq.PacketsCurrent++
	mb.Epoch = int(mp.Epoch)
	mp.BufferCurrent += mbBufferedSize(mb)

	MqUpdateStats(mp, mq, 0)

	if doSignal && mq.Avail != nil {
		mq.Avail.Signal()
	}
}

// MpUnderrun handles buffer underrun during pre-buffering (mp locked).
// C: mp_underrun (media.c:878)
func MpUnderrun(mp *MediaPipe) {
	mp.HoldFlags |= MPHoldPreBuffering
	mpSetPlaystatusByHoldLocked(mp, "")
}

// mpCheckUnderrun checks if an underrun has happened (mp locked).
// C: mp_check_underrun (media.h:541)
func mpCheckUnderrun(mp *MediaPipe) {
	if mp.Flags&MPPreBuffering != 0 &&
		(mp.Video == nil || len(mp.Video.DataQueue) == 0) &&
		(mp.Audio == nil || len(mp.Audio.DataQueue) == 0) {
		MpUnderrun(mp)
	}
}

// MpCheckUnderrun — exported C counterpart (media.h:541); used by the
// video decoder thread in pkg/video/decoder (mp.Mutex held).
func MpCheckUnderrun(mp *MediaPipe) { mpCheckUnderrun(mp) }
