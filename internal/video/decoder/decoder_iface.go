package decoder

import mediacore "github.com/czz/movian-go/internal/media/core"

// Decoder is the seam between the media pipeline and any video decode
// backend. *VideoDecoder (libav/cgo-backed) satisfies it today; a
// pure-Go decoder would implement the same interface and swap in at
// NewVideoDecoder's call sites without touching the pipeline.
//
// contract: lifecycle is NewVideoDecoder -> decoderThread (internal)
// -> Stop/Destroy; DeliverFrame/InferPTS/SetCurrentTime are called by
// the decode backend feeding frames, NOT by the pipeline — the
// pipeline only holds the reference for lifecycle. Implementations
// must be safe for calls from the decoder's own thread.
type Decoder interface {
	// InferPTS — derive a PTS for a meta when the stream lacks one.
	// Called by the decode backend per packet.
	InferPTS(mbm *mediacore.MediaBufMeta, isBFrame bool) int64

	// SetCurrentTime — push playback clock state into the decoder.
	// Called by the decode backend on seeks/resume.
	SetCurrentTime(userTime int64, epoch int, pts int64, driveMode int)

	// DeliverFrame — hand a decoded frame to the video output queue.
	// Called by the decode backend per decoded frame.
	DeliverFrame(info *mediacore.FrameInfo) int

	// Stop — stop the decoder thread and drain the output queue.
	Stop()

	// Destroy — release all decoder resources; no calls allowed after.
	Destroy()
}

// compile-time seam proof: the libav-backed decoder is a Decoder.
var _ Decoder = (*VideoDecoder)(nil)
