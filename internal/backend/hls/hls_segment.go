// Canonical port of src/backend/hls/hls.c — HLS playlist/variant/segment
// management and the dual-demuxer playback loop. hls.h structures are
// reproduced as Go types; all 55 C functions are mapped in file order.
//
// Sentinel pointers (void*)-1/-2/-3 are modelled by mbRef/segRef codes.
package hls

import (
	"fmt"
	"slices"
	"time"

	"github.com/czz/movian-go/internal/arch"
	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/misc"
	"github.com/czz/movian-go/internal/trace"
)

func hlsSegmentOpen(hs *hlsSegment) hlsError {
	hv := hs.Variant
	hd := hv.Demuxer
	h := hd.HLS

	if hs.FH != nil {
		panic("hs_fh != NULL")
	}
	hs.OpenTime = arch.GetTS()
	hs.BlockedCounter = h.Blocked
	hs.DLBase = hd.DownloadCounter

	foe := &fileaccesscore.OpenExtra{
		OpenTimeout: 3000,
		Cancellable: hd.Cancellable,
	}

	flags := fileaccesscore.FaBufferedBig | fileaccesscore.FaStreaming
	if hs.ByteOffset != -1 {
		flags &^= fileaccesscore.FaStreaming
	}

	fh, err := fileaccesscore.FAOpenEx(hs.Variant.Demuxer.HLS.MP.FAM, hs.URL, flags, foe)

	if fh == nil {
		if misc.CancellableIsCancelled(hd.Cancellable) != 0 {
			return hlsErrorSegmentNotFound
		}
		time.Sleep(500 * time.Millisecond)
		if foe.ProtocolError == 404 {
			return hlsErrorSegmentNotFound
		} else if foe.ProtocolError == 403 {
			hs.PermanentError = true
			return hlsErrorSegmentAccessDenied
		}
		_ = err
		return hlsErrorSegmentBroken
	}

	fileaccesscore.FADeadline(fh, 3000)

	if hs.ByteSize != -1 && hs.ByteOffset != -1 {
		fh = fileaccesscore.FASliceOpen(fh, int64(hs.ByteOffset),
			int64(hs.ByteSize))
	}

	hs.Size = fh.Size()

	if hs.Crypto == HLSCryptoAES128 {
		if hs.KeyURL != hv.KeyURL {
			hlsTrace(h, "Loading key %s", hs.KeyURL)
			hv.Key = nil
			hv.Key, _ = fileaccesscore.FALoad2(hs.Variant.Demuxer.HLS.MP.FAM, hs.KeyURL, nil)
			if hv.Key == nil {
				h.ts.Trace(trace.TRACE_ERROR, "HLS",
					"Unable to load key file %s", hs.KeyURL)
				fh.Close()
				return hlsErrorSegmentBadKey
			}
			hv.KeyURL = hs.KeyURL
		}
		fh = fileaccesscore.FAAescbcOpen(fh, hs.IV[:], hv.Key.Data)
	}
	hs.FH = fh
	hlsTrace(h, "Opened %s (sequence %d) ranges:[%d + %d] OK",
		hs.URL, hs.Seq, hs.ByteOffset, hs.ByteSize)
	return hlsErrorOK
}

func hlsSegmentClose(hs *hlsSegment) {
	if hs.FH == nil {
		return
	}

	hd := hs.Variant.Demuxer
	h := hd.HLS

	if hs.BlockedCounter == h.Blocked {
		ts := arch.GetTS() - hs.OpenTime
		size := hs.Size
		if size <= 0 {
			// No Content-Length (h2/h3/QUIC) — measure the
			// bytes actually consumed instead. In C the h1
			// transport always had a size, so hs_size was
			// never <= 0 on this path.
			size = hd.DownloadCounter - hs.DLBase
		}
		if ts > 1000 && size > 0 {
			bw := min(8000000*size/ts, 100000000)

			lowBuffer := h.MP.BufferDelay < 5000000
			var delta string
			if hd.BW == 0 {
				hd.BW = int(bw)
				delta = "Initial"
			} else if int(bw) < hd.BW {
				delta = "Decrease"
				if lowBuffer {
					hd.BW = (hd.BW + int(bw)) / 2
				} else {
					hd.BW = (hd.BW*7 + int(bw)) / 8
				}
			} else {
				delta = "Increase"
				hd.BW = (hd.BW + int(bw)) / 2
			}
			hlsTrace(h, "Estimated bandwidth updated %d bps "+
				"(most recent segment %d bps) "+
				"buffer: %ds (%s) delta: %s",
				hd.BW, int(bw),
				int(h.MP.BufferDelay/1000000),
				map[bool]string{true: "Low", false: "OK"}[lowBuffer],
				delta)
			hd.BWUpdated = 1
		}
	}

	hs.FH.Close()
	hs.FH = nil
}

func hvFindSegmentByTime(hv *hlsVariant, pos int64) *hlsSegment {
	// C: TAILQ_FOREACH_REVERSE — returns NULL when pos precedes all
	// segments (hs ends NULL), not the first segment.
	for i, hs := range slices.Backward(hv.Segments) {

		if hs.TimeOffset <= pos {
			if i == len(hv.Segments)-1 &&
				pos > hs.TimeOffset+hs.Duration {
				return nil
			}
			return hs
		}
	}
	return nil
}

func getCurrentVideoSeq(mp *mediacore.MediaPipe, h *hls) int {
	seq := h.LastEnqueuedSeq

	mp.Mutex.Lock()
	defer mp.Mutex.Unlock()
	if len(mp.Video.DataQueue) > 0 {
		seq = mp.Video.DataQueue[0].Sequence
	}
	return seq
}

// hvFindSegmentBySeq — C: hv_find_segment_by_seq (hls.c:762-789) — most
// requests are for same sequence so we keep a cache pointer.
func hvFindSegmentBySeq(hv *hlsVariant, seq int) *hlsSegment {
	hs := hv.SegmentSearch
	if hs != nil {
		if hs.Seq == seq {
			return hs
		}
		idx := -1
		for i, x := range hv.Segments {
			if x == hs {
				idx = i
				break
			}
		}
		if idx >= 0 && idx+1 < len(hv.Segments) &&
			hv.Segments[idx+1].Seq == seq {
			hv.SegmentSearch = hv.Segments[idx+1]
			return hv.Segments[idx+1]
		}
	}

	if len(hv.Segments) == 0 {
		return nil
	}
	last := hv.Segments[len(hv.Segments)-1]
	if last.Seq == seq {
		return last
	}

	for _, x := range hv.Segments {
		if x.Seq == seq {
			hv.SegmentSearch = x
			return x
		}
	}
	return nil
}

func hlsVariantOpen(hv *hlsVariant) {
	hd := hv.Demuxer
	h := hd.HLS

	if hd == &h.Primary {
		mp := h.MP
		var info string
		if hv.Bitrate != 0 {
			info = fmt.Sprintf("HLS %d kb/s", hv.Bitrate/1000)
		} else {
			info = "HLS"
		}
		if m := pm(h); m != nil {
			m.SetVEx(nil, mp.PropMetadata, "format", info)
		}

		dur := mediacore.PTSUnset
		if hv.Frozen {
			dur = hv.Duration
		}
		mediacore.MpSetDuration(mp, dur)
		if hv.Frozen {
			mediacore.MpSetClrFlags(mp, mediacore.MPCanSeek, 0)
		} else {
			mediacore.MpSetClrFlags(mp, 0, mediacore.MPCanSeek)
		}
	}
}

func hlsVariantClose(hv *hlsVariant) {
	if hv.DemuxerClose != nil {
		hv.DemuxerClose(hv)
	}
	if hv.CurrentSeg != nil {
		hlsSegmentClose(hv.CurrentSeg)
		hv.CurrentSeg = nil
	}
}

// Returns (segment, code) — code is codeEOF/codeNYA/0.
func hlsVariantSelectNextSegment(hv *hlsVariant) (*hlsSegment, int) {
	hd := hv.Demuxer
	h := hd.HLS
	mp := h.MP
	var hs *hlsSegment

	if hv.Loaded == 0 {
		if err := hlsVariantUpdate(hv, mp); err != hlsErrorOK {
			hlsBadVariant(hv, err)
			return nil, 0
		}
	}

retry:
	if hv.CurrentSeg == nil {
		hs = nil
		if hd.SeekToSegment != mediacore.PTSUnset {
			hs = hvFindSegmentByTime(hv, hd.SeekToSegment)

			if hv.Frozen && hs == nil {
				return nil, codeEOF
			}

			if hs != nil {
				hlsTrace(h, "%s: Seek to %d -- Segment %d", hd.Type,
					hd.SeekToSegment, hs.Seq)
			} else {
				hlsTrace(h, "%s: Seek to %d -- Segment not found",
					hd.Type, hd.SeekToSegment)
			}

			hd.SeekToSegment = mediacore.PTSUnset
		}

		if hs == nil {
			seq := getCurrentVideoSeq(h.MP, h)
			if seq == 0 && !hv.Frozen {
				if hv.StartTimeOffset != mediacore.PTSUnset {
					var acc int64
					for _, x := range hv.Segments {
						acc += x.Duration
						if hv.StartTimeOffset < acc {
							hs = x
							hlsTrace(h,
								"Live stream starting at segment %d",
								hs.Seq)
							break
						}
					}
				}
				if hs == nil {
					seq = max(hv.LastSeq-5, hv.FirstSeq)
					hlsTrace(h,
						"Live stream selecting initial segment %d",
						seq)
				}
			}

			if hs == nil && seq != 0 {
				for i := range 5 {
					hs = hvFindSegmentBySeq(hv, seq+i)
					if hs != nil {
						break
					}
					hlsTrace(h,
						"Lookup of seq %d failed, trying next",
						seq+i)
				}
			}
		}
		if hs == nil && len(hv.Segments) > 0 {
			hs = hv.Segments[0]
		}
	} else {
		idx := -1
		for i, x := range hv.Segments {
			if x == hv.CurrentSeg {
				idx = i
				break
			}
		}
		if idx >= 0 && idx+1 < len(hv.Segments) {
			hs = hv.Segments[idx+1]
		}
	}

	if hs != nil {
		return hs, 0
	}

	if hv.Frozen {
		// Not a live stream and no segment can be found, this is EOF
		return nil, codeEOF
	}

	if mp.BufferDelay < 10000000 {
		if hv.Loaded != time.Now().Unix() {
			if err := hlsVariantUpdate(hv, mp); err != hlsErrorOK {
				hlsBadVariant(hv, err)
				return nil, 0
			}
			goto retry
		}
	}

	// No segment available yet => sleep (but wakeup if we need to
	// exit or seek someplace)
	mp.Mutex.Lock()
	if len(mp.EventQueue) == 0 {
		if mpWaitBackpressureTimeout(mp, 1000) {
			mp.Mutex.Unlock()
			return nil, codeNYA
		}
	}
	mp.Mutex.Unlock()
	return nil, 0
}
