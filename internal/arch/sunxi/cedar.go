//go:build sunxi

// cedar.go — C: src/video/cedar.c — Allwinner CedarX (libve) video
// decoder. fbm_t/cedar_packet_t live in C memory because libvecore.so
// reads/writes vpicture_t/vstream_data_t fields directly; the decoder
// struct is Go-side and crosses the ABI as a cgo.Handle (vbv handle).
package sunxi

/*
#cgo CFLAGS: -I${SRCDIR}/include
#cgo LDFLAGS: -L${SRCDIR}/lib -lvecore -Wl,--export-dynamic

#include <stdlib.h>
#include <string.h>
#include <stdio.h>
#include <stdint.h>
#include <unistd.h>
#include "tlsf.h"
#include <cedardev_api.h>
#include "cedar_abi.h"
*/
import "C"

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/czz/movian-go/internal/libav"
	mediacore "github.com/czz/movian-go/internal/media/core"
	libavmedia "github.com/czz/movian-go/internal/media/libav"
	"github.com/czz/movian-go/internal/trace"
	decoder "github.com/czz/movian-go/internal/video/decoder"
)

// cedarMutex — C: static hts_mutex_t cedar_mutex (cedar.c:38).
// CEDAR_SESSION_LOCK is defined (cedar.c:48); CEDAR_DECODE_LOCK is not.
var cedarMutex sync.Mutex

const numFrameMeta = 64 // C: NUM_FRAME_META (cedar.c:96)

// cedarDecoder — C: cedar_decoder_t (cedar.c:98-112). The C struct is
// passed to libve_set_vbv() as the vbv Handle and handed back to the
// vbv_* callbacks; it stays a Go object bridged by cgo.Handle because
// cd_meta[] holds Go MediaBuf values (slices/interfaces).
type cedarDecoder struct {
	ve       C.Handle // C: cd_ve
	queue    C.struct_cedar_packet_queue
	vbv      cgoHandle
	layer    int // C: cd_layer (unused)
	framecnt int // C: cd_framecnt (unused)
	metaptr  int // C: cd_metaptr
	name     [64]byte
	meta     [numFrameMeta]mediacore.MediaBuf
	lastPTS  int64 // C: cd_last_pts
	estDur   int   // C: cd_estimated_duration
}

type cgoHandle = uintptr

// fbmMutex — overlay the Go mutex on fbm_t.mutex_opaque (C memory is
// stable; sync.Mutex contains no pointers). C: hts_mutex_t fbm_mutex.
func fbmMutex(fbm *C.fbm_t) *sync.Mutex {
	return (*sync.Mutex)(unsafe.Pointer(&fbm.mutex_opaque))
}

// ---------------------------------------------------------------------------
// C: fbm_release (cedar.c:126-154)
// ---------------------------------------------------------------------------

func fbmRelease(fbm *C.fbm_t) {
	if atomic.AddInt32((*int32)(&fbm.refcount), -1) > 1 {
		return
	}
	GfxmemLock()

	queued := int(C.picq_count(&fbm.fbm_queued))
	display := int(C.picq_count(&fbm.fbm_display))

	pics := unsafe.Slice(C.fbm_pics(fbm), int(fbm.numpics))
	for i := 0; i < int(fbm.numpics); i++ {
		C.tlsf_free(sunxi.gfxmem, unsafe.Pointer(pics[i].pic.y))
		C.tlsf_free(sunxi.gfxmem, unsafe.Pointer(pics[i].pic.u))
		C.tlsf_free(sunxi.gfxmem, unsafe.Pointer(pics[i].pic.v))
	}
	GfxmemUnlock()

	// C: hts_mutex_destroy(&fbm->fbm_mutex) — Go mutex needs no destroy

	if queued != 0 {
		panic(fmt.Sprintf("FBM %p (%s) still have queued frames",
			fbm, C.GoString(&fbm.fbm_name[0])))
	}

	if display != 0 {
		panic(fmt.Sprintf("FBM %p (%s) still have displaying frames",
			fbm, C.GoString(&fbm.fbm_name[0])))
	}

	C.free(unsafe.Pointer(fbm))
}

// CedarFrameDone — C: cedar_frame_done (cedar.c:160-170). Exported:
// called by the CEDR glw video engine when a surface is released.
func CedarFrameDone(p unsafe.Pointer) {
	pic := (*C.picture_t)(p)
	fbm := pic.fbm
	fbmMutex(fbm).Lock()
	C.picq_remove(&fbm.fbm_display, pic)
	C.picq_insert_head(&fbm.fbm_avail, pic)
	fbmMutex(fbm).Unlock()
	fbmRelease(fbm)
}

// ---------------------------------------------------------------------------
// C: media_codec callbacks (cedar.c:175-323)
// ---------------------------------------------------------------------------

// cedarFlush — C: cedar_flush (cedar.c:175-182)
func cedarFlush(mc *mediacore.MediaCodec, vd any) {
	cd := mc.Opaque.(*cedarDecoder)
	C.libve_reset(1, cd.ve)
	cd.lastPTS = mediacore.PTSUnset
}

// cedarDecode — C: cedar_decode (cedar.c:187-310)
func cedarDecode(mc *mediacore.MediaCodec, vdi any,
	mq *mediacore.MediaQueue, mb *mediacore.MediaBuf, reqsize int) {
	vd := vdi.(*decoder.VideoDecoder)
	cd := mc.Opaque.(*cedarDecoder)
	cp := (*C.cedar_packet_t)(C.calloc(1,
		C.size_t(unsafe.Sizeof(C.cedar_packet_t{}))))

	// Copy packet to cedar mem
	GfxmemLock()
	cp.cp_vsd.data = (*C.u8)(C.tlsf_malloc(sunxi.gfxmem,
		C.size_t(mb.Size)))
	GfxmemUnlock()
	if mb.Size > 0 {
		C.memcpy(unsafe.Pointer(cp.cp_vsd.data),
			unsafe.Pointer(&mb.Data[0]), C.size_t(mb.Size))
	}
	cp.cp_vsd.length = C.u32(mb.Size)

	cd.meta[cd.metaptr] = *mb
	cp.cp_vsd.pcr = C.u64(cd.metaptr)
	cd.metaptr = (cd.metaptr + 1) & (numFrameMeta - 1)

	cp.cp_vsd.valid = 1
	C.cpq_insert_tail(&cd.queue, cp)

	ts := time.Now().UnixMicro()

	libavmedia.AvgTimeStart(&vd.DecodeTime)
	res := C.libve_decode(0, 0, 0, cd.ve)
	libavmedia.AvgTimeStop(&vd.DecodeTime, mq.PropDecodeAvg,
		mq.PropDecodePeak)

	if res < 0 {
		sunxi.ts.Trace(trace.TRACE_ERROR, "CEDAR",
			"libve_decode() failed, possible mem leak")
		return
	}

	dectime := time.Now().UnixMicro() - ts
	if dectime > 1000000 {
		fmt.Printf("%s: Decode time %d very high\n",
			C.GoString((*C.char)(unsafe.Pointer(&cd.name[0]))), dectime)
		os.Exit(0)
	}
	fbm := (*C.fbm_t)(C.libve_get_fbm(cd.ve))
	if fbm == nil {
		fmt.Printf("%s: FBM IS NULL\n", C.GoString((*C.char)(unsafe.Pointer(&cd.name[0]))))
		return
	}
	C.strncpy(&fbm.fbm_name[0], (*C.char)(unsafe.Pointer(&cd.name[0])),
		64)

	mu := fbmMutex(fbm)
	mu.Lock()
	for {
		pic := C.picq_first(&fbm.fbm_queued)
		if pic == nil {
			break
		}
		C.picq_remove(&fbm.fbm_queued, pic)

		if !(pic.pic.pcr < numFrameMeta) { // assert pcr < NUM && >= 0
			panic("cedar: pic pcr out of range")
		}

		mb := &cd.meta[int(pic.pic.pcr)]

		if cd.lastPTS != mediacore.PTSUnset &&
			mb.PTS != mediacore.PTSUnset {
			d := mb.PTS - cd.lastPTS
			if d > 1000 && d < 100000 {
				cd.estDur = int(d)
			}
		}

		var fi mediacore.FrameInfo
		fi.PTS = mb.PTS
		fi.Epoch = mb.Epoch
		// C: fi.fi_delta = mb->mb_delta — neither field exists in
		// this tree's media.h; the C line is dead.
		if mb.Duration > 10000 {
			fi.Duration = mb.Duration
		} else {
			fi.Duration = int64(cd.estDur)
		}

		fi.DriveClock = mb.Flags.DriveClock
		fi.Interlaced = pic.pic.is_progressive == 0
		fi.TFF = pic.pic.top_field_first != 0
		fi.TopFieldFirst = fi.TFF
		fi.Width = int(pic.pic.display_width)
		fi.Height = int(pic.pic.display_height)
		fi.PixFmt = int(pic.pic.pixel_format)
		fi.DARNum = int(float32(pic.pic.display_width) *
			float32(pic.pic.aspect_ratio) / 1000.0)
		fi.DARDen = int(pic.pic.display_height)
		fi.Type = mediacore.FourCCCEDR // C: 'CEDR'

		vd.EstimatedDuration = fi.Duration
		cd.lastPTS = fi.PTS

		if pic.pic.y == nil { // C: assert(pic->pic.y != NULL)
			panic("cedar: pic y is NULL")
		}
		if fi.Duration > 0 && !mb.Flags.Skip {
			fi.Data[0] = unsafe.Slice((*byte)(
				unsafe.Pointer(pic.pic.y)), int(pic.pic.size_y))
			fi.Data[1] = unsafe.Slice((*byte)(
				unsafe.Pointer(pic.pic.u)), int(pic.pic.size_u))
			fi.Data[2] = unsafe.Slice((*byte)(
				unsafe.Pointer(pic.pic.v)), int(pic.pic.size_v))
			fi.Data[3] = unsafe.Slice((*byte)(
				unsafe.Pointer(pic)), 1)

			atomic.AddInt32((*int32)(&fbm.refcount), 1)
			C.picq_insert_head(&fbm.fbm_display, pic)

			mu.Unlock()
			vd.DeliverFrame(&fi)
			mu.Lock()
		} else {
			C.picq_insert_head(&fbm.fbm_avail, pic)
		}
	}
	mu.Unlock()
}

// cedarClose — C: cedar_close (cedar.c:313-323)
func cedarClose(mc *mediacore.MediaCodec) {
	cd := mc.Opaque.(*cedarDecoder)
	C.libve_close(1, cd.ve)
	fmt.Printf("%s: Close\n", C.GoString((*C.char)(unsafe.Pointer(&cd.name[0]))))
	// C: free(cd) — Go-side cd is GC'd; drop the vbv handle
	cedarVbvHandleDelete(C.Handle(unsafe.Pointer(cd.vbv)))
	cedarMutex.Unlock() // C: CEDAR_SESSION_LOCK unlock
}

// cedarCodecOpen — C: cedar_codec_open (cedar.c:333-438)
func cedarCodecOpen(mc *mediacore.MediaCodec,
	mcp *mediacore.MediaCodecParams, mp *mediacore.MediaPipe) int {
	var cfg C.vconfig_t
	var si C.vstream_info_t

	cfg.max_video_width = 3840
	cfg.max_video_height = 2160

	switch mc.CodecID {
	default:
		return 1

	case mediacore.CodecID(libav.AVCodecIDH264):
		si.format = C.STREAM_FORMAT_H264
		// C: CEDAR_USE_HATA is defined (cedar.c:44)
		if mcp == nil || len(mcp.ExtraData) == 0 ||
			mcp.ExtraData[0] != 1 {
			return decoder.H264AnnexBToAvc(mc, mp, cedarCodecOpen)
		}

		if mcp != nil {
			si.init_data = (*C.u8)(unsafe.Pointer(
				&mcp.ExtraData[0]))
			si.init_data_len = C.u32(len(mcp.ExtraData))
		}

	case mediacore.CodecID(libav.AVCodecIDMpeg2Video):
		return 1
		// C: si.format = STREAM_FORMAT_MPEG2; si.sub_format =
		// MPEG2_SUB_FORMAT_MPEG2 — dead code after `return 1`.

	case mediacore.CodecID(libav.AVCodecIDH263):
		return 1
		// C: si.format = STREAM_FORMAT_MPEG4; si.sub_format =
		// MPEG4_SUB_FORMAT_H263 — dead code after `return 1`.
	}

	// C: CEDAR_SESSION_LOCK (cedar.c:48)
	cedarMutex.Lock()

	ve := C.libve_open(&cfg, &si, nil) // C: non-OLD_VE
	if ve == nil {
		sunxi.ts.Trace(trace.TRACE_ERROR, "libve", "Unable to open libve")
		cedarMutex.Unlock()
		return 1
	}

	cd := &cedarDecoder{ve: ve, lastPTS: mediacore.PTSUnset}
	copyCString64(&cd.name, mp.Name)
	C.cpq_init(&cd.queue)
	cd.vbv = uintptr(unsafe.Pointer(cedarVbvHandleNew(cd)))
	C.libve_set_vbv(C.Handle(unsafe.Pointer(cd.vbv)), ve)

	mc.Opaque = cd
	mc.Decode = cedarDecode
	mc.Close = cedarClose
	mc.Flush = cedarFlush
	return 0
}

func copyCString64(dst *[64]byte, s string) {
	for i := 0; i < 63 && i < len(s); i++ {
		dst[i] = s[i]
	}
}

// ---------------------------------------------------------------------------
// vbv handle bridge — libve hands back our opaque Handle; it is a
// cgo.Handle value so libve never holds a raw Go pointer.
// ---------------------------------------------------------------------------

var (
	vbvMu      sync.Mutex
	vbvHandles = map[uintptr]*cedarDecoder{}
	vbvNext    uintptr
)

func cedarVbvHandleNew(cd *cedarDecoder) C.Handle {
	vbvMu.Lock()
	defer vbvMu.Unlock()
	vbvNext++
	vbvHandles[vbvNext] = cd
	return C.Handle(unsafe.Pointer(vbvNext))
}

func cedarVbvHandleGet(h C.Handle) *cedarDecoder {
	vbvMu.Lock()
	defer vbvMu.Unlock()
	return vbvHandles[uintptr(unsafe.Pointer(h))]
}

func cedarVbvHandleDelete(h C.Handle) {
	vbvMu.Lock()
	defer vbvMu.Unlock()
	delete(vbvHandles, uintptr(unsafe.Pointer(h)))
}

// ---------------------------------------------------------------------------
// C: VE interface implementations (cedar.c:445-490)
// ---------------------------------------------------------------------------

func veResetHardware() {
	// C: commented-out ioctl(sunxi.cedarfd, IOCTL_RESET_VE, 0)
}

func veEnableClock(enable bool, speed uint32) {
	if enable {
		CedarIoctl(C.IOCTL_ENABLE_VE, 0)
		fmt.Printf("Settings %d instead of %d\n", 240, speed)
		// C: commented-out ioctl(..., IOCTL_SET_VE_FREQ, 240)
	} else {
		CedarIoctl(C.IOCTL_DISABLE_VE, 0)
	}
}

func veEnableIntr(enable bool) {
	// NOP on Linux
}

func veWaitIntr() int32 {
	return int32(CedarIoctl(C.IOCTL_WAIT_VE, 2)) - 1
}

func veGetRegBaseAddr() uint32 {
	// C: non-CEDAR_TRAP path — (intptr_t)sunxi.macc
	return uint32(uintptr(sunxi.macc))
}

func veGetMemtype() int32 {
	return int32(C.MEMTYPE_DDR3_32BITS)
}

// ---------------------------------------------------------------------------
// C: OS interface implementations (cedar.c:504-581)
// ---------------------------------------------------------------------------

func memAlloc(size uint32) unsafe.Pointer {
	return C.malloc(C.size_t(size))
}

func memFree(p unsafe.Pointer) {
	C.free(p)
}

func memPalloc(size, align uint32) unsafe.Pointer {
	if size == 0 {
		size = align
	}
	GfxmemLock()
	r := C.tlsf_memalign(sunxi.gfxmem, C.size_t(align), C.size_t(size))
	GfxmemUnlock()
	return r
}

func memPfree(p unsafe.Pointer) {
	GfxmemLock()
	C.tlsf_free(sunxi.gfxmem, p)
	GfxmemUnlock()
}

func memSet(mem unsafe.Pointer, value, size uint32) {
	C.memset(mem, C.int(value), C.size_t(size))
}

func memCpy(dst, src unsafe.Pointer, size uint32) {
	C.memcpy(dst, src, C.size_t(size))
}

func memFlushCache(mem unsafe.Pointer, size uint32) {
	// NOP on linux
}

func memGetPhyAddr(virtualAddr uint32) uint32 {
	return virtualAddr - sunxi.gfxmembase +
		uint32(sunxi.envInfo.phymem_start)
}

func sysPrint(fn *C.u8, line uint32) int32 {
	fmt.Printf("%s\n", "sys_print")
	return 1
}

func sysSleep(ms uint32) {
	C.usleep(C.uint(ms * 1000))
}

// ---------------------------------------------------------------------------
// C: FBM interface implementations (cedar.c:601-805)
// ---------------------------------------------------------------------------

func fbmSetup(maxFrameNum, minFrameNum, sizeY, sizeU, sizeV, sizeA uint32,
	format int32) C.Handle {
	fbm := (*C.fbm_t)(C.calloc(1, C.size_t(unsafe.Sizeof(C.fbm_t{}))+
		C.size_t(unsafe.Sizeof(C.picture_t{}))*C.size_t(maxFrameNum)))

	C.picq_init(&fbm.fbm_avail)
	C.picq_init(&fbm.fbm_queued)
	C.picq_init(&fbm.fbm_display)
	// C: hts_mutex_init — zeroed Go mutex is ready
	fbm.refcount = 1

	fbm.numpics = C.int(maxFrameNum)
	fbm.fmt = C.pixel_format_e(format)

	pics := unsafe.Slice(C.fbm_pics(fbm), int(maxFrameNum))

	GfxmemLock()
	for i := 0; i < int(maxFrameNum); i++ {
		pics[i].pic.id = C.u32(i)
		pics[i].fbm = fbm
		pics[i].pic.size_y = C.u32(sizeY)
		pics[i].pic.size_u = C.u32(sizeU)
		pics[i].pic.size_v = C.u32(sizeV)

		if sizeY != 0 {
			pics[i].pic.y = (*C.u8)(C.tlsf_memalign(
				sunxi.gfxmem, 1024, C.size_t(sizeY)))
			if pics[i].pic.y == nil {
				panic("Out of CEDAR memory")
			}
		} else {
			pics[i].pic.y = nil
		}

		if sizeU != 0 {
			pics[i].pic.u = (*C.u8)(C.tlsf_memalign(
				sunxi.gfxmem, 1024, C.size_t(sizeU)))
			if pics[i].pic.u == nil {
				panic("Out of CEDAR memory")
			}
		} else {
			pics[i].pic.u = nil
		}

		if sizeV != 0 {
			pics[i].pic.v = (*C.u8)(C.tlsf_memalign(
				sunxi.gfxmem, 1024, C.size_t(sizeV)))
			if pics[i].pic.v == nil {
				panic("Out of CEDAR memory")
			}
		} else {
			pics[i].pic.v = nil
		}
		C.picq_insert_head(&fbm.fbm_avail, &pics[i])
	}
	GfxmemUnlock()

	return C.Handle(unsafe.Pointer(fbm))
}

func fbmSetupEx(maxFrameNum, minFrameNum uint32, sizeY, sizeU, sizeV,
	sizeAlpha *C.u32, mode3d int32, format int32, unknown uint8,
	parent unsafe.Pointer) C.Handle {
	return fbmSetup(maxFrameNum, minFrameNum,
		uint32(*sizeY), uint32(*sizeU), uint32(*sizeV),
		uint32(*sizeAlpha), format)
}

func fbmTeardown(h C.Handle, parent unsafe.Pointer) {
	fbm := (*C.fbm_t)(h)
	fbmRelease(fbm)
}

func fbmDecoderRequestFrame(h C.Handle) *C.vpicture_t {
	fbm := (*C.fbm_t)(h)

	ts := time.Now().UnixMicro()
	fbmMutex(fbm).Lock()
	ts = time.Now().UnixMicro() - ts
	if ts > 100000 {
		panic(fmt.Sprintf("Request frame long timeout %d", ts))
	}
	pic := C.picq_first(&fbm.fbm_avail)
	if pic != nil {
		C.picq_remove(&fbm.fbm_avail, pic)
	}
	fbmMutex(fbm).Unlock()
	if pic == nil {
		return nil // C: &pic->pic where pic == NULL
	}
	return &pic.pic
}

func fbmDecoderReturnFrame(f *C.vpicture_t, valid bool, h C.Handle) {
	fbm := (*C.fbm_t)(h)
	pic := (*C.picture_t)(unsafe.Pointer(f))
	if f == nil {
		return
	}

	fbmMutex(fbm).Lock()
	if valid {
		C.picq_insert_tail(&fbm.fbm_queued, pic)
	} else {
		fmt.Printf("%s: Return invalid frame: %v %p\n",
			C.GoString(&fbm.fbm_name[0]), valid, fbm)
		C.picq_insert_head(&fbm.fbm_avail, pic)
	}
	fbmMutex(fbm).Unlock()
}

func fbmDecoderShareFrame(f *C.vpicture_t, h C.Handle) {
	fmt.Printf("%s\n", "fbm_decoder_share_frame")

	fmt.Printf("f=%p\n", f)
	if f == nil {
		return
	}
	fmt.Printf("     dim: %d x %d\n", f.width, f.height)
	fmt.Printf("  stored: %d x %d\n", f.store_width, f.store_height)
	fmt.Printf("  offset: %d x %d\n", f.top_offset, f.left_offset)
	fmt.Printf(" display: %d x %d\n", f.display_width, f.display_height)
	fmt.Printf("      FR: %d\n", f.frame_rate)
	fmt.Printf("  aspect: %f\n", float32(f.aspect_ratio)/1000)
	fmt.Printf("  progressive: %d  TFF: %d, RTF: %d, RBF: %d\n",
		f.is_progressive, f.top_field_first,
		f.repeat_top_field, f.repeat_bottom_field)
	fmt.Printf("  pixel_format: %x\n", f.pixel_format)
	fmt.Printf("     PTS: %d\n", f.pts)

	C.abort()
}

func fbmSetupExYV12(maxFrameNum, minFrameNum uint32, sizeY, sizeU, sizeV,
	sizeAlpha *C.u32, mode3d int32, format int32, unknown uint8,
	parent unsafe.Pointer) C.Handle {
	fmt.Printf("%s\n", "fbm_init_ex_yv12")
	C.abort()
	return nil
}

func fbmSetupExYV32(maxFrameNum, minFrameNum uint32, sizeY, sizeU, sizeV,
	sizeAlpha *C.u32, mode3d int32, format int32, unknown uint8,
	parent unsafe.Pointer) C.Handle {
	fmt.Printf("%s\n", "fbm_init_ex_yv32")
	C.abort()
	return nil
}

func fbmFlushFrame(h C.Handle, pts int64) {
	fmt.Printf("%s\n", "fbm_flush_frame")
}

func fbmPrintStatus(h C.Handle) {
	fmt.Printf("%s\n", "fbm_print_status")
}

func fbmAllocYV12FrameBuffer(h C.Handle) {
	fmt.Printf("%s\n", "fbm_alloc_YV12_frame_buffer")
}

// ---------------------------------------------------------------------------
// C: VBV interface implementations (cedar.c:840-884)
// ---------------------------------------------------------------------------

func vbvRequestStreamFrame(vbv C.Handle) *C.vstream_data_t {
	cd := cedarVbvHandleGet(vbv)
	cp := C.cpq_first(&cd.queue)
	if cp == nil {
		return nil
	}
	C.cpq_remove(&cd.queue, cp)
	return &cp.cp_vsd
}

func vbvReturnStreamFrame(stream *C.vstream_data_t, vbv C.Handle) {
	cd := cedarVbvHandleGet(vbv)
	cp := (*C.cedar_packet_t)(unsafe.Pointer(stream))
	C.cpq_insert_head(&cd.queue, cp)
}

func vbvFlushStreamFrame(stream *C.vstream_data_t, vbv C.Handle) {
	cp := (*C.cedar_packet_t)(unsafe.Pointer(stream))
	GfxmemLock()
	C.tlsf_free(sunxi.gfxmem, unsafe.Pointer(cp.cp_vsd.data))
	GfxmemUnlock()
	C.free(unsafe.Pointer(cp))
}

func vbvGetBaseAddr(vbv C.Handle) *C.u8 {
	return (*C.u8)(unsafe.Pointer(uintptr(sunxi.gfxmembase)))
}

func vbvGetBufferSize(vbv C.Handle) uint32 {
	return uint32(sunxi.envInfo.phymem_total_size)
}

// ---------------------------------------------------------------------------
// C: cedar_codec_init + REGISTER_CODEC (cedar.c:1090-1104)
// ---------------------------------------------------------------------------

func cedarCodecStart() {
	// C: hts_mutex_init(&cedar_mutex) — zero Go mutex is ready
	CedarIoctl(C.IOCTL_RESET_VE, 0)
	// C: CEDAR_TRAP block is compiled out upstream — not ported.
}

func init() {
	// C: REGISTER_CODEC(cedar_codec_init, cedar_codec_open, 10)
	mediacore.MediaRegisterCodec(&mediacore.CodecDef{
		Start: cedarCodecStart,
		Open:  cedarCodecOpen,
		Prio:  10,
	})
}
