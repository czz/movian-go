package core

const (
	// PTSUnset — C: PTS_UNSET = INT64_C(0x8000000000000000)
	// (media.h:78) — identical to AV_NOPTS_VALUE.
	PTSUnset = int64(-9223372036854775808)

	// CodecID represents codec identifiers
	CodecIDNone CodecID = iota
	CodecIDAC3
	CodecIDEAC3
	CodecIDAAC
	CodecIDMP2
	CodecIDMP3
	CodecIDMPEG2Video
	CodecIDMPEG4
	CodecIDH263
	CodecIDH264
	CodecIDH265
	CodecIDVP8
	CodecIDVP9
	CodecIDAV1
	CodecIDDTS
	CodecIDOpus
	CodecIDVorbis
	CodecIDFLAC
	CodecIDPCM
	CodecIDMJPEG
	CodecIDPNG
	CodecIDGIF
	CodecIDBMP
	CodecIDDVBSubtitle
	CodecIDMOVText
	CodecIDDVDSubtitle
)

const (
	MbAudio    = 1
	MbVideo    = 2
	MbSubtitle = 3
)

const (
	// C: media.h:496-498 — MP_BUFFER_NONE=0, MP_BUFFER_SHALLOW=2, MP_BUFFER_DEEP=3
	MPBufferNone    = 0
	MPBufferShallow = 2
	MPBufferDeep    = 3
)

const (
	MbSpecialEOF = 0x10000000
)

const (
	MediaTypeVideo      MediaType = 0
	MediaTypeAudio      MediaType = 1
	MediaTypeData       MediaType = 2
	MediaTypeSubtitle   MediaType = 3
	MediaTypeAttachment MediaType = 4
)

const (
	MPPrimaable       MediaPipeFlags = 1 << 0
	MPPreBuffering    MediaPipeFlags = 1 << 1
	MPVideo           MediaPipeFlags = 1 << 2
	MPFlushOnHold     MediaPipeFlags = 1 << 3
	MPAlwaysSatisfied MediaPipeFlags = 1 << 4
	MPCanSeek         MediaPipeFlags = 1 << 5
	MPCanPause        MediaPipeFlags = 1 << 6
	MPCanEject        MediaPipeFlags = 1 << 7
)

const (
	MPHoldPause        MediaHoldFlags = 1 << 0
	MPHoldPreBuffering MediaHoldFlags = 1 << 1
	MPHoldOS           MediaHoldFlags = 1 << 2
	MPHoldStream       MediaHoldFlags = 1 << 3
	MPHoldDisplay      MediaHoldFlags = 1 << 4
	MPHoldSync         MediaHoldFlags = 1 << 5
)

// C carries event_type_t values (event.h) on mp_eq — Go's MediaEvent.Type
// holds int(event.EVENT_*). The old private MediaEvent* numbering
// (EOF=1..Hold=9) collided with the C wire range and has been removed.

const (
	MBVideo MediaBufDataType = iota
	MBAudio
	MBSetPropString
	MBDVDCLUT
	MBDVDResetSPU
	MBDVDSPU
	MBDVDPCI
	MBSubtitle
	MBCtrl
	MBCtrlFlush
	MBCtrlPause
	MBCtrlPlay
	MBCtrlExit
	MBCtrlFlushSubtitles
	MBCtrlDVDHilite
	MBCtrlExtSubtitle
	MBCtrlRestart
	MBCtrlReconfigure
	MBCtrlReqOutputSize
	MBCtrlDVDSPU2
	MBCtrlUnblock
	MBCtrlSetVolumeMultiplier
)

const (
	MaxUserAudioGain = 20.0
)
