package core

// RegisterBuiltinCodecs registers codecs that live inside mediacore.
// C-parity: REGISTER_CODEC constructors ran pre-main; the Go port calls
// this explicitly from the composition root (before media_init).
// The registry is prio-sorted, so call order is only significant for
// equal priorities.
var codecsBuiltinRegistered bool

func RegisterBuiltinCodecs() {
	if codecsBuiltinRegistered {
		return
	}
	codecsBuiltinRegistered = true
	registerDvdspuCodec()
}
