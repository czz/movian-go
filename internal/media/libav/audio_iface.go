package libav

import (
	libav "github.com/czz/movian-go/internal/libav"
	mediacore "github.com/czz/movian-go/internal/media/core"
)

// AudioCodecDecoder is the seam between the media pipeline and any
// audio codec decode backend. *AudioDecoder (libav/cgo-backed)
// satisfies it today; a pure-Go codec decoder would implement the same
// interface and swap in through the NewLibAVAudio call sites without
// touching the pipeline.
//
// contract: lifecycle is New (factory) -> configure via the package
// Set* helpers -> the backend's internal decode thread drives itself
// -> Close. The pipeline holds the reference opaquely and only
// closes it; Close must be safe to call from the pipeline thread
// while the decode thread is running.
type AudioCodecDecoder interface {
	// Decode — decode one packet; returns the media buf or error.
	Decode(pkt *libav.AVPacket, pktPTSUs int64) (*mediacore.MediaBuf, error)
	// Drain — pull remaining decoded PCM on teardown.
	Drain() ([]byte, error)
	// Flush — drop decoder state (seek).
	Flush() error
	// Close — stop the decode thread and release all codec resources.
	// Idempotent; no calls allowed after.
	Close() error
}

// Close implements AudioCodecDecoder on *AudioDecoder, delegating to
// the package-level teardown (C: libav_audio_close).
func (ad *AudioDecoder) Close() error { return LibAVAudioClose(ad) }

// Decode/Drain/Flush delegate to the package-level codec ops so the
// concrete decoder satisfies the seam without callers holding the
// concrete type.
func (ad *AudioDecoder) Decode(pkt *libav.AVPacket, pktPTSUs int64) (*mediacore.MediaBuf, error) {
	return LibAVAudioDecode(ad, pkt, pktPTSUs)
}
func (ad *AudioDecoder) Drain() ([]byte, error) { return LibAVAudioDrain(ad) }
func (ad *AudioDecoder) Flush() error           { return LibAVAudioFlush(ad) }

// compile-time seam proof: the libav-backed decoder is an
// AudioCodecDecoder.
var _ AudioCodecDecoder = (*AudioDecoder)(nil)

// AudioDecoderFactory — the creation side of the seam (matches
// NewLibAVAudio's signature so a backend can be swapped wholesale).
type AudioDecoderFactory func(mc *mediacore.MediaCodec,
	mq *mediacore.MediaQueue, mp *mediacore.MediaPipe) (AudioCodecDecoder, error)
