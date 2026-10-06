package libav

// RegisterLibavCodecs registers the libav codec(s) into mediacore.
// C-parity: REGISTER_CODEC constructors ran pre-main; the Go port calls
// this explicitly from the composition root (before media_init).
// Platform-tagged codec registrations (android_mediacodec) keep their
// init() as platform seams.
var libavCodecsRegistered bool

func RegisterLibavCodecs() {
	if libavCodecsRegistered {
		return
	}
	libavCodecsRegistered = true
	registerLibavVideoCodec()
}
