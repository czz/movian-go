package libav

/*
#include <libavformat/avformat.h>
#include <libavformat/avio.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

// Forward declarations for C functions (from libav_avio.c)
void c_set_fps_probe_size(void *ps, int64_t value);
void c_set_max_analyze_duration(void *ps, int64_t value);
void *avformat_open_input_wrapper(void *ps, const char *url, void *fmt, void *pb, char *errbuf, int errlen);

// Canonical fa_libav.c helpers (libav_avio.c)
int64_t fa_avio_seek0(void *pb);
const void *fa_find_input_format(const char *name);
int fa_probe_input_buffer(void *pb, const void **fmt, const char *url, int max_probe_size);
int fa_avformat_open_pb(void **fctx_out, void *pb, const char *url, const void *fmt, uintptr_t iokey);
int fa_find_stream_info(void *fctx);
void fa_avformat_close(void **fctx);
void fa_av_strerror(int err, char *errbuf, int errlen);
*/
import "C"

import (
	"errors"
	"fmt"
	"strings"
	"unsafe"
)

// Constants for libav open strategy
const (
	FaLibavOpenStrategyAudio            = 0
	FaLibavOpenStrategyVideoSeekable    = 1
	FaLibavOpenStrategyVideoNonSeekable = 2
)

// ErrorToStr converts an AV error code to string
func ErrorToStr(err int) string {
	if err == AverrorEOF {
		return "EOF"
	}
	if err == AverrorEagain {
		return "EAGAIN"
	}
	if err == AverrorEio {
		return "EIO"
	}
	return fmt.Sprintf("Error %d", err)
}

// C: mimetype2fmt (fa_libav.c:87-102) — strcasecmp lookup, all keys
// stored lowercase (C's "video/MP2T" included).
var mimetype2fmt = map[string]string{
	"video/x-matroska":        "matroska",
	"video/quicktime":         "mov",
	"video/mp4":               "mp4",
	"video/x-msvideo":         "avi",
	"video/mp2t":              "mpegts",
	"video/mpeg":              "mpegts",
	"video/vnd.dlna.mpeg-tts": "mpegts",
	"video/avi":               "avi",
	"video/nsv":               "nsv",
	"video/webm":              "webm",
	"audio/x-mpeg":            "mp3",
	"audio/mpeg":              "mp3",
	"application/ogg":         "ogg",
	"audio/aac":               "aac",
	"audio/aacp":              "aac",
}

// faLibavOpenError — C: fa_libav_open_error (fa_libav.c:75-86)
// Returns "<hdr>: <libav error>" as a Go error.
func faLibavOpenError(hdr string, code int) error {
	var cbuf [256]byte
	C.fa_av_strerror(C.int(code), (*C.char)(unsafe.Pointer(&cbuf[0])), C.int(len(cbuf)))
	return fmt.Errorf("%s: %s", hdr, C.GoString((*C.char)(unsafe.Pointer(&cbuf[0]))))
}

// FALibavOpenFormat — C: fa_libav_open_format (fa_libav.c:115-203).
// Rewinds the AVIOContext, resolves the mimetype to an input format via
// the mimetype2fmt table (av_find_input_format), otherwise probes
// (av_probe_input_buffer with a strategy-dependent probe size), opens with
// avformat_open_input, applies strategy probe/analyze limits, and runs
// avformat_find_stream_info. On failure with a mimetype hint it retries
// without it (canonical recursion).
func FALibavOpenFormat(avio *AVIOContext, url string, mimetype string, strategy int) (*AVFormatContext, error) {
	if avio == nil || avio.cPtr == nil {
		// No AVIOContext — direct URL open (Go callers: rtmp/icecast).
		fc, err := avformatOpenInput(url)
		if err != nil {
			return nil, err
		}
		// C: fa_libav_open_format always runs avformat_find_stream_info
		if err := avformatFindStreamInfo(fc); err != nil {
			avformatCloseInput(fc)
			return nil, err
		}
		return fc, nil
	}
	return faLibavOpenFormat(avio, url, mimetype, strategy)
}

func faLibavOpenFormat(avio *AVIOContext, url string, mimetype string, strategy int) (*AVFormatContext, error) {
	pb := avio.cPtr

	// C: avio_seek(avio, 0, SEEK_SET);
	C.fa_avio_seek0(pb)

	// C: mimetype → mimetype2fmt → av_find_input_format
	var fmtPtr unsafe.Pointer
	if mimetype != "" {
		if name, ok := mimetype2fmt[strings.ToLower(mimetype)]; ok {
			cn := C.CString(name)
			fmtPtr = unsafe.Pointer(C.fa_find_input_format(cn))
			C.free(unsafe.Pointer(cn))
		}
		// C: fmt==NULL → "Don't know mimetype, probing instead"
	}

	// C: probe_size by strategy
	probeSize := 0
	switch strategy {
	case FaLibavOpenStrategyAudio:
		probeSize = 4096
	case FaLibavOpenStrategyVideoNonSeekable:
		probeSize = 65536
	}

	cURL := C.CString(url)
	defer C.free(unsafe.Pointer(cURL))

	if fmtPtr == nil {
		if ret := C.fa_probe_input_buffer(pb, &fmtPtr, cURL, C.int(probeSize)); ret != 0 {
			return nil, faLibavOpenError("Unable to probe file", int(ret))
		}
		if fmtPtr == nil {
			return nil, errors.New("Unknown file format")
		}
		// C: TRACE "Probed as %s"
	}

	// Nested opens (HLS/DASH segments, sidecar files) go back through
	// fileaccess when the fa handle can open sub-URLs — https then uses
	// Go's TLS instead of a (nonexistent) FFmpeg TLS backend. Must be
	// armed BEFORE avformat_open_input: HLS opens variant playlists
	// already inside read_header.
	var iokey C.uintptr_t
	if opener, ok := avio.reader.(FileAccessOpener); ok && avio.sys != nil {
		avio.sys.ioOpenMapMu.Lock()
		avio.sys.ioOpenMap[uintptr(avio.cPtr)] = opener
		avio.sys.ioOpenMapMu.Unlock()
		iokey = C.uintptr_t(uintptr(avio.cPtr))
	}

	var fctx unsafe.Pointer
	ret := C.fa_avformat_open_pb(&fctx, pb, cURL, fmtPtr, iokey)
	if ret != 0 {
		if iokey != 0 {
			avio.sys.ioOpenMapMu.Lock()
			delete(avio.sys.ioOpenMap, uintptr(avio.cPtr))
			avio.sys.ioOpenMapMu.Unlock()
		}
		if mimetype != "" {
			// C: retry with mimetype=NULL (probe)
			return faLibavOpenFormat(avio, url, "", strategy)
		}
		return nil, faLibavOpenError("Unable to open file as input format", int(ret))
	}

	// C: strategy fields set before avformat_find_stream_info
	switch strategy {
	case FaLibavOpenStrategyAudio:
		C.c_set_fps_probe_size(fctx, 0)
		C.c_set_max_analyze_duration(fctx, 0)
	case FaLibavOpenStrategyVideoNonSeekable:
		C.c_set_fps_probe_size(fctx, 2)
		C.c_set_max_analyze_duration(fctx, 1)
	}

	if ret := C.fa_find_stream_info(fctx); ret < 0 {
		C.fa_avformat_close(&fctx)
		if mimetype != "" {
			return faLibavOpenFormat(avio, url, "", strategy)
		}
		return nil, faLibavOpenError("Unable to handle file contents", int(ret))
	}

	return &AVFormatContext{Filename: url, avio: avio, cPtr: unsafe.Pointer(fctx)}, nil
}

// FALibavOpenStrategyAudio — applies the strategy fields used inside
// fa_libav_open_format when an fctx was obtained another way.
// C: AUDIO → fps_probe_size=0, max_analyze_duration=0;
//
//	VIDEO_NON_SEEKABLE → fps_probe_size=2, max_analyze_duration=1;
//	VIDEO_SEEKABLE → defaults untouched.
func FALibavOpenStrategyAudio(fc *AVFormatContext, strategy int) error {
	if fc == nil || fc.cPtr == nil {
		return nil // No C context to modify
	}

	switch strategy {
	case FaLibavOpenStrategyAudio:
		C.c_set_fps_probe_size(fc.cPtr, 0)
		C.c_set_max_analyze_duration(fc.cPtr, 0)
	case FaLibavOpenStrategyVideoNonSeekable:
		C.c_set_fps_probe_size(fc.cPtr, 2)
		C.c_set_max_analyze_duration(fc.cPtr, 1)
	}

	return nil
}

// FALibavCloseFormat — C: fa_libav_close_format (fa_libav.c:222-231).
// avformat_close_input FIRST, then fa_close_with_park(avio->opaque, park),
// then the avio buffer/context are freed.
func FALibavCloseFormat(sys *LibAVSystem, fc *AVFormatContext, park bool) error {
	if fc == nil {
		return nil
	}
	avio := fc.avio
	avformatCloseInput(fc)
	if avio != nil {
		if avio.sys != nil {
			avio.sys.ioOpenMapMu.Lock()
			delete(avio.sys.ioOpenMap, uintptr(avio.cPtr))
			avio.sys.ioOpenMapMu.Unlock()
		}
		faLibavCloseAVIO(sys, avio, park)
	}
	return nil
}

// FALibavErrorToTxt converts libav error to string (fileaccess API)
// C: fa_libav_error_to_txt — av_strerror first, "libav error %d" only
// as fallback when av_strerror fails (fa_libav.c:251-257).
func FALibavErrorToTxt(err int, errbuf []byte, errlen int) {
	if errlen <= 0 || len(errbuf) == 0 {
		return
	}
	if errlen > len(errbuf) {
		errlen = len(errbuf)
	}
	C.fa_av_strerror(C.int(err), (*C.char)(unsafe.Pointer(&errbuf[0])),
		C.int(errlen))
}
