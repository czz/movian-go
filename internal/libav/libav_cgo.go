//go:generate go run generate_cgo.go

package libav

/*
#include <libavutil/mathematics.h>
#include <libavutil/mem.h>
#include <libavutil/pixdesc.h>
#include <stdlib.h>
*/
import "C"

import (
	"sync"
	"sync/atomic"
	"unsafe"
)

// FileAccessOpener is implemented by fa handles that can open related
// URLs (HLS/DASH segments, sidecars) through the same fileaccess
// manager — giving nested FFmpeg opens the same TLS/HTTP stack as the
// top-level stream.
type FileAccessOpener interface {
	OpenSubURL(url string) (FileAccessReader, error)
}

// LibAVSystem manages libav state without global state
type LibAVSystem struct {
	ReaderMap     map[unsafe.Pointer]FileAccessReader
	ReaderMapLock sync.Mutex
	opaqueTokens  []*byte
	// fctx ptr → opener for FFmpeg's io_open callback (nested opens)
	ioOpenMap   map[uintptr]FileAccessOpener
	ioOpenMapMu sync.Mutex
	// C AVIOContext* → our AVIOContext, for the io_close callback
	avioByCPtr   map[uintptr]*AVIOContext
	avioByCPtrMu sync.Mutex
	Initialized  bool
}

// NewLibAVSystem creates a new libav system
func NewLibAVSystem() *LibAVSystem {
	return &LibAVSystem{
		ReaderMap:    make(map[unsafe.Pointer]FileAccessReader),
		opaqueTokens: make([]*byte, 0),
		ioOpenMap:    make(map[uintptr]FileAccessOpener),
		avioByCPtr:   make(map[uintptr]*AVIOContext),
		Initialized:  false,
	}
}

// Global libav system instance for C callbacks
// This is necessary because C callbacks cannot receive Go context
var globalLibAVSystem *LibAVSystem

// SetGlobalLibAVSystem sets the global libav system instance
// This must be called before using libav functions
func SetGlobalLibAVSystem(sys *LibAVSystem) {
	globalLibAVSystem = sys
}

// GetGlobalLibAVSystem returns the global libav system instance
func GetGlobalLibAVSystem() *LibAVSystem {
	return globalLibAVSystem
}

// AVRational represents a rational number
type AVRational struct {
	Num int
	Den int
}

// Constants
const (
	AVNoPTSValue          = int64(-9223372036854775808) // 0x8000000000000000 as int64
	AverrorEagain         = -11
	AverrorEOF            = -541478725
	AverrorEio            = -541478727
	AverrorInvaliddata    = -1094995529
	AvseekFlagBackward    = 1
	AvmediaTypeUnknown    = -1
	AvmediaTypeVideo      = 0
	AvmediaTypeAudio      = 1
	AvmediaTypeData       = 2
	AvmediaTypeSubtitle   = 3
	AvmediaTypeAttachment = 4
	AVPixFmtNone          = -1
	AVPixFmtYuv420p       = 0
	AVPixFmtRgb24         = 2
	AVPixFmtRgb32         = 28
	AVPixFmtRGBA          = 28
	AVPixFmtBgr32         = 29
	// Codec IDs
	AVCodecIDH264       = 27
	AVCodecIDMjpeg      = 7
	AVCodecIDPng        = 61
	AVCodecIDDts        = 7609
	AVCodecIDMpeg1Video = 1
	AVCodecIDMpeg2Video = 2
	AVCodecIDH263       = 4
	AVCodecIDMpeg4      = 12
	AVCodecIDVp8        = 139
	AVCodecIDVp9        = 167
	AVCodecIDHevc       = 173

	// FFmpeg enum AVCodecID values used by backend packet demuxers
	// (src/backend/rtmp/rtmp.c passes raw AV_CODEC_ID_* to
	// media_codec_create).
	AVCodecIDVP6F = 92
	AVCodecIDMP3  = 86017
	AVCodecIDAAC  = 86018
	AVCodecIDAC3  = 86019
	// SWS flags
	SwsBilinear = 2
	SwsBicubic  = 4
	// Color space constants
	AvcolSpcUnspecified = 0
	AvcolSpcBt709       = 1
	AvcolSpcBt470bg     = 5
	AvcolSpcSmpte170m   = 6
	AvcolSpcSmpte240m   = 7
	// Color range constants
	AvcolRangeUnspecified = 0
	AvcolRangeMpeg        = 1
	AvcolRangeJpeg        = 2
	// Color primaries constants
	AvcolPriUnspecified = 0
	AvcolPriBt709       = 1
	AvcolPriBt470bg     = 5
	AvcolPriSmpte170m   = 6
	// Color transfer characteristics constants
	AvcolTrcUnspecified = 0
	AvcolTrcBt709       = 1
	AvcolTrcSmpte170m   = 6
	AvcolTrcSmpte240m   = 7
	// Chroma location constants
	AvchromaLocUnspecified = 0
	AvchromaLocLeft        = 1
	AvchromaLocCenter      = 2
	AvchromaLocTopleft     = 3
)

// AVTimeBaseQ creates an AVRational
func AVTimeBaseQ(num, den int) AVRational {
	return AVRational{Num: num, Den: den}
}

// AVRescaleQ rescales a timestamp from one timebase to another.
// C: av_rescale_q(a, b, c) = av_rescale_rnd(a, b.num*c.den,
//
//	b.den*c.num, AV_ROUND_NEAR_INF)
func AVRescaleQ(ts int64, from, to AVRational) int64 {
	b := int64(from.Num) * int64(to.Den)
	c := int64(from.Den) * int64(to.Num)
	if c == 0 {
		return 0
	}
	// AV_ROUND_NEAR_INF: round to nearest, halfway away from zero
	if ts >= 0 {
		return (ts*b + c/2) / c
	}
	return (ts*b - c/2) / c
}

var initialized atomic.Bool

// AvFreep frees memory
func AvFreep(ptr unsafe.Pointer) {
	C.av_freep(ptr)
}

// avGetPixFmtName gets the name of a pixel format
func avGetPixFmtName(pixFmt int) string {
	cName := C.av_get_pix_fmt_name(C.enum_AVPixelFormat(C.int(pixFmt)))
	if cName == nil {
		return ""
	}
	return C.GoString(cName)
}

// SetupLibAV initializes the LibAV library (FFmpeg 7)
func SetupLibAV() error {
	// FFmpeg 4.0+ doesn't require av_register_all()
	initialized.Store(true)
	return nil
}

// AvMalloc — C: av_malloc(size).
func AvMalloc(size int) unsafe.Pointer {
	return unsafe.Pointer(C.av_malloc(C.size_t(size)))
}
