package libav

/*
#include <libavcodec/packet.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

// Forward declaration for C function (from libav_avio.c)
void c_av_packet_unref(void *pkt);

// Helper: free an AVPacket via its C pointer, avoiding &wrapper.cPtr
// which the cgo checker flags as "Go pointer to Go pointer".
static void ml_av_packet_free(void *pkt_ptr) {
    AVPacket *pkt = (AVPacket *)pkt_ptr;
    av_packet_free(&pkt);
}

// C: av_packet_ref for raw packet pointers (media_buf_from_avpkt_unlocked)
static int ml_av_packet_ref(void *dst_ptr, void *src_ptr) {
    return av_packet_ref((AVPacket *)dst_ptr, (AVPacket *)src_ptr);
}

static int64_t ml_av_packet_get_pts(void *p) { return ((AVPacket *)p)->pts; }
static int64_t ml_av_packet_get_dts(void *p) { return ((AVPacket *)p)->dts; }
static int64_t ml_av_packet_get_duration(void *p) { return ((AVPacket *)p)->duration; }
static int ml_av_packet_get_stream_index(void *p) { return ((AVPacket *)p)->stream_index; }
static int ml_av_packet_get_size(void *p) { return ((AVPacket *)p)->size; }
static void *ml_av_packet_get_data(void *p) { return ((AVPacket *)p)->data; }
*/
import "C"

import (
	"unsafe"
)

// AVPacket flags
const (
	AVPktFlagKey = 0x0001 // The packet contains a keyframe
)

// AVPacket represents a packet (Go wrapper for C.AVPacket)
type AVPacket struct {
	Data        []byte
	Duration    int64
	PTS         int64
	DTS         int64
	StreamIndex int
	Flags       int
	cPtr        unsafe.Pointer // Pointer to C AVPacket
}

// GetData returns the packet data
func (p *AVPacket) GetData() []byte {
	return p.Data
}

// avPacketUnref unreferences the packet data
// FFmpeg 7 API: av_packet_unref is still available and unchanged
// Uses C wrapper from libav_avio.c
func avPacketUnref(pkt *AVPacket) {
	if pkt != nil && pkt.cPtr != nil {
		C.c_av_packet_unref(pkt.cPtr)
	}
}

// AvPacketUnref unreferences the packet data (exported)
func AvPacketUnref(pkt *AVPacket) {
	avPacketUnref(pkt)
}

// avPacketGetFlags gets the flags from a C AVPacket
func avPacketGetFlags(pkt *AVPacket) int {
	if pkt == nil || pkt.cPtr == nil {
		return 0
	}
	cPkt := (*C.AVPacket)(pkt.cPtr)
	return int(cPkt.flags)
}

// AvPacketAlloc allocates a packet
func AvPacketAlloc() *AVPacket {
	ptr := C.av_packet_alloc()
	if ptr == nil {
		return nil
	}
	return &AVPacket{Data: make([]byte, 0), cPtr: unsafe.Pointer(ptr)}
}

// WrapAVPacket wraps a C AVPacket* allocated outside this wrapper
// (e.g. by a media/libav shim). Nil-safe: a nil C pointer yields nil.
// The Go-side field cache (PTS/DTS/...) is NOT populated — accessors
// that need C fields read them via cPtr.
func WrapAVPacket(ptr unsafe.Pointer) *AVPacket {
	if ptr == nil {
		return nil
	}
	return &AVPacket{cPtr: ptr}
}

// AvPacketFree frees a packet
func AvPacketFree(pkt *AVPacket) {
	if pkt != nil && pkt.cPtr != nil {
		C.ml_av_packet_free(pkt.cPtr)
		pkt.cPtr = nil
	}
}

// CPtr returns the underlying C AVPacket pointer.
func (p *AVPacket) CPtr() unsafe.Pointer {
	if p == nil {
		return nil
	}
	return p.cPtr
}

// AvPacketSetData copies data into C memory and sets it on the packet.
// C: pkt.data = data; pkt.size = size (stack AVPacket in
// hls_ts.c probe_duration). The packet owns the copy; released by
// AvPacketFree/AvPacketUnrefPtr.
func AvPacketSetData(pkt *AVPacket, data []byte) {
	if pkt == nil || pkt.cPtr == nil {
		return
	}
	cp := (*C.AVPacket)(pkt.cPtr)
	cp.data = nil
	cp.size = 0
	if len(data) == 0 {
		pkt.Data = nil
		return
	}
	cbuf := C.malloc(C.size_t(len(data) + 64)) // FF_INPUT_BUFFER_PADDING_SIZE
	C.memset(cbuf, 0, C.size_t(len(data)+64))
	C.memcpy(cbuf, unsafe.Pointer(&data[0]), C.size_t(len(data)))
	cp.data = (*C.uint8_t)(cbuf)
	cp.size = C.int(len(data))
	pkt.Data = data
}

// AvPacketAllocPtr allocates a raw C AVPacket.
// C: av_packet_alloc.
func AvPacketAllocPtr() unsafe.Pointer {
	return unsafe.Pointer(C.av_packet_alloc())
}

// AvPacketFreePtr frees a raw C AVPacket and clears the pointer.
// C: av_packet_free(&pkt).
func AvPacketFreePtr(pktp *unsafe.Pointer) {
	if pktp == nil || *pktp == nil {
		return
	}
	C.ml_av_packet_free(*pktp)
	*pktp = nil
}

// AvPacketRefPtr creates a new reference to src's data in dst.
// C: av_packet_ref(dst, src) (media_buf.c:81).
func AvPacketRefPtr(dst, src unsafe.Pointer) int {
	return int(C.ml_av_packet_ref(dst, src))
}

// AvPacketUnrefPtr unreferences a raw C AVPacket.
// C: av_packet_unref(&mb->mb_pkt) (media_buf.c:31).
func AvPacketUnrefPtr(pkt unsafe.Pointer) {
	if pkt != nil {
		C.c_av_packet_unref(pkt)
	}
}

// AvPacketFieldsPtr reads the canonical fields out of a raw C AVPacket.
func AvPacketFieldsPtr(pkt unsafe.Pointer) (pts, dts, duration int64, streamIndex, size int, data []byte) {
	if pkt == nil {
		return 0, 0, 0, 0, 0, nil
	}
	pts = int64(C.ml_av_packet_get_pts(pkt))
	dts = int64(C.ml_av_packet_get_dts(pkt))
	duration = int64(C.ml_av_packet_get_duration(pkt))
	streamIndex = int(C.ml_av_packet_get_stream_index(pkt))
	size = int(C.ml_av_packet_get_size(pkt))
	if size > 0 {
		if d := C.ml_av_packet_get_data(pkt); d != nil {
			data = C.GoBytes(d, C.int(size))
		}
	}
	return
}
