package libav

/*
#include <libavformat/avio.h>
#include <stdint.h>
#include <stdlib.h>

// Forward declarations for Go callbacks (defined below via //export)
extern int goReadCallback(void *opaque, uint8_t *buf, int size);
extern int64_t goSeekCallback(void *opaque, int64_t offset, int whence);

// Forward declarations for C functions (from libav_avio.c)
void *c_alloc_avio_context(void *opaque, int seekable);
void c_free_avio_context(void *avio_ptr);
void fa_avio_free(void *avio);
*/
import "C"

import (
	"errors"
	"fmt"
	"io"
	"unsafe"
)

//export goReadCallback
func goReadCallback(opaque unsafe.Pointer, buf *C.uint8_t, size C.int) C.int {
	if globalLibAVSystem == nil {
		return -1
	}
	// Lookup only under the lock — holding it during Read would
	// serialize every AVIO read process-wide (C has no such lock;
	// a stalled HTTP reader must not freeze other demuxers).
	globalLibAVSystem.ReaderMapLock.Lock()
	reader, ok := globalLibAVSystem.ReaderMap[opaque]
	globalLibAVSystem.ReaderMapLock.Unlock()
	if !ok || reader == nil {
		return -1
	}

	// C: fa_read fills buf in place — read straight into the C buffer,
	// no GoBytes bounce-copy.
	goBuf := unsafe.Slice((*byte)(unsafe.Pointer(buf)), int(size))
	n, err := reader.Read(goBuf)
	if n > 0 {
		return C.int(n)
	}
	if err != nil {
		if errors.Is(err, io.EOF) {
			return 0 // C: fa_read returns 0 at EOF
		}
		return -1
	}
	return C.int(n)
}

//export goSeekCallback
func goSeekCallback(opaque unsafe.Pointer, offset C.int64_t, whence C.int) C.int64_t {
	if globalLibAVSystem == nil {
		return -1
	}
	globalLibAVSystem.ReaderMapLock.Lock()
	reader, ok := globalLibAVSystem.ReaderMap[opaque]
	globalLibAVSystem.ReaderMapLock.Unlock()
	if !ok || reader == nil {
		return -1
	}

	// C: fa_libav_seek — AVSEEK_SIZE → fa_fsize (fa_libav.c:47-48)
	if whence == C.int(AvseekSize) {
		return C.int64_t(reader.Size())
	}

	// C: lazy = !(whence & AVSEEK_FORCE);
	//    fa_seek4(fh, offset, whence & ~AVSEEK_FORCE, lazy) (fa_libav.c:50-51)
	// Lazy seeks let protocols (HTTP without ranges) fail fast instead
	// of draining the connection or reopening it (fa_http.c:2159).
	lazy := whence&C.int(AvseekForce) == 0
	actualWhence := whence & ^C.int(AvseekForce)

	var pos int64
	var err error
	if ls, ok := reader.(interface {
		Seek4(pos int64, whence int, lazy bool) (int64, error)
	}); ok {
		pos, err = ls.Seek4(int64(offset), int(actualWhence), lazy)
	} else {
		pos, err = reader.Seek(int64(offset), int(actualWhence))
	}
	if err != nil {
		return -1
	}
	return C.int64_t(pos)
}

// AVIOContext represents I/O context (Go wrapper for C.AVIOContext)
type AVIOContext struct {
	Buffer     []byte
	BufferSize int
	Opaque     unsafe.Pointer
	Seekable   int
	reader     FileAccessReader // C: avio->opaque (the fa_handle_t)
	cPtr       unsafe.Pointer   // Pointer to C AVIOContext
	cBuffer    unsafe.Pointer   // Pointer to C buffer (for cleanup)
	sys        *LibAVSystem
}

// FileAccessReader is the interface for file access (basic for libav integration)
type FileAccessReader interface {
	Read(buf []byte) (int, error)
	Seek(offset int64, whence int) (int64, error)
	Close() error
	Size() int64
}

// FileAccessHandle wraps a FileAccessReader for libav
type FileAccessHandle struct {
	reader FileAccessReader
}

// Constants for AVIO seek
const (
	AvseekSize  = 0x10000
	AvseekForce = 0x20000
)

// FALibavReopen creates an AVIOContext for a file handle (fileaccess API)
// This uses C avio_alloc_context with Go callbacks
func FALibavReopen(sys *LibAVSystem, reader FileAccessReader, noSeek bool) (*AVIOContext, error) {
	if reader == nil {
		return nil, fmt.Errorf("reader is nil")
	}
	if sys == nil {
		return nil, fmt.Errorf("libav system is nil")
	}

	seekable := !noSeek && reader.Size() >= 0

	// Register reader in global map for callbacks
	// Use a heap-allocated token as opaque key (not uintptr) to satisfy go vet.
	// The token must be non-zero-size: &struct{}{} collapses to zerobase,
	// giving every AVIOContext the same opaque → ReaderMap collision.
	// (C uses the unique fa_handle_t* itself.)
	sys.ReaderMapLock.Lock()
	token := new(byte)
	opaque := unsafe.Pointer(token)
	sys.opaqueTokens = append(sys.opaqueTokens, token)
	sys.ReaderMap[opaque] = reader
	sys.ReaderMapLock.Unlock()

	if seekable {
		// Seek to beginning
		_, err := reader.Seek(0, 0) // SEEK_SET = 0
		if err != nil {
			sys.ReaderMapLock.Lock()
			delete(sys.ReaderMap, opaque)
			sys.ReaderMapLock.Unlock()
			return nil, fmt.Errorf("seek failed: %w", err)
		}
	}

	// Call C wrapper to allocate AVIOContext with Go callbacks
	seekableInt := 0
	if seekable {
		seekableInt = 1
	}
	cAvio := C.c_alloc_avio_context(opaque, C.int(seekableInt))
	if cAvio == nil {
		sys.ReaderMapLock.Lock()
		delete(sys.ReaderMap, opaque)
		sys.ReaderMapLock.Unlock()
		return nil, fmt.Errorf("c_alloc_avio_context failed")
	}

	avio := &AVIOContext{
		Buffer:     nil, // Buffer is managed by C
		BufferSize: 32768,
		Opaque:     opaque,
		Seekable:   seekableInt,
		reader:     reader,
		cPtr:       unsafe.Pointer(cAvio),
		cBuffer:    nil, // Buffer is freed by c_free_avio_context
		sys:        sys,
	}
	sys.avioByCPtrMu.Lock()
	sys.avioByCPtr[uintptr(avio.cPtr)] = avio
	sys.avioByCPtrMu.Unlock()

	return avio, nil
}

// FALibavClose — C: fa_libav_close (fa_libav.c:210-215).
// fa_close(avio->opaque) then frees the avio buffer + context.
func FALibavClose(sys *LibAVSystem, avio *AVIOContext) error {
	if avio == nil {
		return nil
	}
	faLibavCloseAVIO(sys, avio, false)
	return nil
}

// faLibavCloseAVIO closes the fa_handle behind avio (with parking) and
// frees the C AVIOContext + its buffer.
func faLibavCloseAVIO(sys *LibAVSystem, avio *AVIOContext, park bool) {
	reader := avio.reader
	if avio.Opaque != nil && sys != nil {
		sys.ReaderMapLock.Lock()
		delete(sys.ReaderMap, avio.Opaque)
		sys.ReaderMapLock.Unlock()
	}
	if sys != nil {
		sys.avioByCPtrMu.Lock()
		delete(sys.avioByCPtr, uintptr(avio.cPtr))
		sys.avioByCPtrMu.Unlock()
	}
	if reader != nil {
		// C: fa_close_with_park(fh, park)
		if cp, ok := reader.(interface {
			CloseWithPark(park bool) error
		}); ok {
			cp.CloseWithPark(park)
		} else {
			reader.Close()
		}
	}
	// C: av_free(avio->buffer); av_free(avio);
	if avio.cPtr != nil {
		C.fa_avio_free(avio.cPtr)
		avio.cPtr = nil
	}
	avio.Buffer = nil
}

//export goFaIoOpen
func goFaIoOpen(key C.uintptr_t, curl *C.char, pbOut *unsafe.Pointer) C.int {
	sys := globalLibAVSystem
	if sys == nil || curl == nil || pbOut == nil {
		return -5 // AVERROR(EIO)
	}
	u := C.GoString(curl)
	sys.ioOpenMapMu.Lock()
	opener := sys.ioOpenMap[uintptr(key)]
	sys.ioOpenMapMu.Unlock()
	if opener == nil {
		return -2 // AVERROR(ENOENT)
	}
	fh, err := opener.OpenSubURL(u)
	if err != nil || fh == nil {
		return -2
	}
	avio, err := FALibavReopen(sys, fh, false)
	if err != nil || avio == nil {
		if fh != nil {
			fh.Close()
		}
		return -5
	}
	*pbOut = avio.cPtr
	return 0
}

//export goFaIoClose
func goFaIoClose(key C.uintptr_t, pb unsafe.Pointer) C.int {
	sys := globalLibAVSystem
	if sys == nil || pb == nil {
		return 0
	}
	sys.avioByCPtrMu.Lock()
	avio := sys.avioByCPtr[uintptr(pb)]
	sys.avioByCPtrMu.Unlock()
	if avio == nil {
		return 0
	}
	faLibavCloseAVIO(sys, avio, false)
	return 0
}

// AvioSize — C: avio_size(avio) (fa_video.c:671) — total stream size.
func AvioSize(avio *AVIOContext) int64 {
	if avio == nil || avio.cPtr == nil {
		return 0
	}
	return int64(C.avio_size((*C.AVIOContext)(avio.cPtr)))
}

// FALibavGetStrategyForFile returns the appropriate open strategy (fileaccess API)
func FALibavGetStrategyForFile(reader FileAccessReader) int {
	if reader.Size() < 0 {
		return FaLibavOpenStrategyVideoNonSeekable
	}
	return FaLibavOpenStrategyVideoSeekable
}
