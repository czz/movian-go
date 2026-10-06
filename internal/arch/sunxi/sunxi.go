//go:build sunxi

// sunxi.go — C: src/arch/sunxi/sunxi.c + sunxi.h — Allwinner sunxi
// platform core: /dev/disp + /dev/cedar_dev init, gfxmem tlsf pool,
// VE register access (macc mmap), background layer, cache flush.
package sunxi

/*
#cgo CFLAGS: -I${SRCDIR}/include
#cgo LDFLAGS: -L${SRCDIR}/lib -lvecore

#include <stdlib.h>
#include <string.h>
#include <stdio.h>
#include <stdint.h>
#include <assert.h>
#include <unistd.h>
#include <fcntl.h>
#include <sys/mman.h>
#include <sys/ioctl.h>
#include <sys/stat.h>
#include <linux/fb.h>
#include "tlsf.h"
#include <cedardev_api.h>
#include "drv_display_sun4i.h"

// ioctl/open are variadic — cgo can't call them directly.
static int sx_ioctl(int fd, unsigned long cmd, void *args) {
	return ioctl(fd, cmd, args);
}
static int sx_open(const char *path) {
	return open(path, O_RDWR);
}
static void *sx_mmap(size_t len, int fd, off_t off) {
	return mmap(NULL, len, PROT_READ | PROT_WRITE, MAP_SHARED, fd, off);
}
*/
import "C"

import (
	"fmt"
	"os"
	"sync"
	"unsafe"

	backendcore "github.com/czz/movian-go/internal/backend/core"
	imagepkg "github.com/czz/movian-go/internal/image"
	miscpkg "github.com/czz/movian-go/internal/misc"
	"github.com/czz/movian-go/internal/trace"
)

// sunxiT — C: sunxi_t (sunxi.h:31-41); sunxi — C: sunxi_t sunxi (sunxi.c:34)
type sunxiT struct {
	cedarfd    int
	dispfd     int
	fb0fd      int
	envInfo    C.cedarv_env_info_t
	gfxmem     C.tlsf_pool
	gfxmembase uint32
	gfxmemMu   sync.Mutex // C: hts_mutex_t gfxmem_mutex
	macc       unsafe.Pointer
	veVersion  int
	ts         *trace.TraceSystem // C: trace() global — injected
}

var sunxi sunxiT

// GfxmemLock/GfxmemUnlock — C: hts_mutex_lock/unlock(&sunxi.gfxmem_mutex)
func GfxmemLock()   { sunxi.gfxmemMu.Lock() }
func GfxmemUnlock() { sunxi.gfxmemMu.Unlock() }

// GfxmemMalloc — C: tlsf_malloc(sunxi.gfxmem, size)
func GfxmemMalloc(size int) unsafe.Pointer {
	return C.tlsf_malloc(sunxi.gfxmem, C.size_t(size))
}

// GfxmemMemalign — C: tlsf_memalign(sunxi.gfxmem, align, size)
func GfxmemMemalign(align, size int) unsafe.Pointer {
	return C.tlsf_memalign(sunxi.gfxmem, C.size_t(align), C.size_t(size))
}

// GfxmemFree — C: tlsf_free(sunxi.gfxmem, p)
func GfxmemFree(p unsafe.Pointer) {
	C.tlsf_free(sunxi.gfxmem, p)
}

// GfxmemBase — C: sunxi.gfxmem pool base VA (vbv_get_base_addr)
func GfxmemBase() uintptr { return uintptr(sunxi.gfxmembase) }

// PhymemStart / PhymemTotalSize — C: sunxi.env_info.phymem_*
func PhymemStart() uint32     { return uint32(sunxi.envInfo.phymem_start) }
func PhymemTotalSize() uint32 { return uint32(sunxi.envInfo.phymem_total_size) }

// Cedarfd / Dispfd / Fb0fd — C: sunxi.cedarfd / .dispfd / .fb0fd
func Cedarfd() int { return sunxi.cedarfd }
func Dispfd() int  { return sunxi.dispfd }
func Fb0fd() int   { return sunxi.fb0fd }

// VaToPa — C: va_to_pa (sunxi.h:55-58)
func VaToPa(va unsafe.Pointer) uint32 {
	return uint32(uintptr(va) - uintptr(sunxi.gfxmembase) +
		uintptr(sunxi.envInfo.phymem_start))
}

// ---------------------------------------------------------------------------
// C: VE register defines (sunxi.h:63-113)
// ---------------------------------------------------------------------------

const (
	veCtrl               = 0x000
	veVersionReg         = 0x0f0
	veH264FrameSize      = 0x200
	veH264PicHdr         = 0x204
	veH264SliceHdr       = 0x208
	veH264SliceHdr2      = 0x20c
	veH264PredWeight     = 0x210
	veH264QpParam        = 0x21c
	veH264Trigger        = 0x224
	veH264Status         = 0x228
	veH264CurMbNum       = 0x22c
	veH264OutputFrameIdx = 0x24c
	veH264BasicBits      = 0x2dc
	veH264RamWritePtr    = 0x2e0
	veH264RamWriteData   = 0x2e4
	veH264SdrotCtrl      = 0x240

	veSRAMH264RefList0 = 0x640
	veSRAMH264RefList1 = 0x664
)

// VeWrite — C: ve_write (sunxi.h:119-126)
func VeWrite(reg int, val uint32) {
	if reg > 0x1000 {
		panic("ve_write: reg out of range")
	}
	CedarDecodeWrite32(reg, int(val))
	*(*uint32)(unsafe.Pointer(uintptr(sunxi.macc) + uintptr(reg))) = val
}

// VeRead — C: ve_read (sunxi.h:128-136)
func VeRead(reg int) uint32 {
	if reg > 0x1000 {
		panic("ve_read: reg out of range")
	}
	v := *(*uint32)(unsafe.Pointer(uintptr(sunxi.macc) + uintptr(reg)))
	CedarDecodeRead32(reg, int(v))
	return v
}

// ---------------------------------------------------------------------------
// ioctl helpers — C: ioctl(sunxi.dispfd/cedarfd, cmd, args)
// ---------------------------------------------------------------------------

// DispIoctl — C: ioctl(sunxi.dispfd, cmd, unsigned long args[4])
func DispIoctl(cmd uint, args *[4]uintptr) int {
	return int(C.sx_ioctl(C.int(sunxi.dispfd), C.ulong(cmd),
		unsafe.Pointer(args)))
}

// CedarIoctl — C: ioctl(sunxi.cedarfd, cmd, arg)
func CedarIoctl(cmd uint, arg uintptr) int {
	return int(C.sx_ioctl(C.int(sunxi.cedarfd), C.ulong(cmd),
		unsafe.Pointer(arg)))
}

// Fb0Ioctl — C: ioctl(sunxi.fb0fd, cmd, arg) (sunxi_main.c:249,277)
func Fb0Ioctl(cmd uint, arg uintptr) int {
	return int(C.sx_ioctl(C.int(sunxi.fb0fd), C.ulong(cmd),
		unsafe.Pointer(arg)))
}

// ---------------------------------------------------------------------------
// C: disp_cleanup (sunxi.c:36-54)
// ---------------------------------------------------------------------------

func dispCleanup() {
	var args [4]uintptr

	args[1] = 101
	DispIoctl(C.DISP_CMD_VIDEO_STOP, &args)
	DispIoctl(C.DISP_CMD_LAYER_CLOSE, &args)
	DispIoctl(C.DISP_CMD_LAYER_RELEASE, &args)
	args[1] = 102
	DispIoctl(C.DISP_CMD_VIDEO_STOP, &args)
	DispIoctl(C.DISP_CMD_LAYER_CLOSE, &args)
	DispIoctl(C.DISP_CMD_LAYER_RELEASE, &args)
	args[1] = 103
	DispIoctl(C.DISP_CMD_VIDEO_STOP, &args)
	DispIoctl(C.DISP_CMD_LAYER_CLOSE, &args)
	DispIoctl(C.DISP_CMD_LAYER_RELEASE, &args)
}

// SunxiStart — C: sunxi_init (sunxi.c:57-170)
func SunxiStart(ts *trace.TraceSystem) {
	sunxi.ts = ts
	sunxi.dispfd = int(C.sx_open(C.CString("/dev/disp")))
	if sunxi.dispfd == -1 {
		fmt.Println("open(/dev/disp) failed")
		os.Exit(1)
	}

	sunxi.fb0fd = int(C.sx_open(C.CString("/dev/fb0")))
	if sunxi.fb0fd == -1 {
		fmt.Println("open(/dev/fb0) failed")
		os.Exit(1)
	}

	// C: #if 0'd DISP_CMD_VERSION check — kept out canonically

	dispCleanup()

	sunxi.cedarfd = int(C.sx_open(C.CString("/dev/cedar_dev")))
	if sunxi.cedarfd == -1 {
		fmt.Println("open(/dev/cedar_dev) failed")
		os.Exit(1)
	}

	if CedarIoctl(C.IOCTL_GET_ENV_INFO,
		uintptr(unsafe.Pointer(&sunxi.envInfo))) != 0 {
		fmt.Println("ioctl(IOCTL_GET_ENV_INFO) failed")
		os.Exit(1)
	}

	sunxi.macc = C.sx_mmap(4096, C.int(sunxi.cedarfd),
		C.off_t(sunxi.envInfo.address_macc))
	if sunxi.macc == C.MAP_FAILED {
		fmt.Println("mmap of macc failed")
		os.Exit(1)
	}

	ptr := C.sx_mmap(C.size_t(sunxi.envInfo.phymem_total_size),
		C.int(sunxi.cedarfd), C.off_t(sunxi.envInfo.phymem_start))
	if ptr == C.MAP_FAILED {
		fmt.Println("mmap failed")
		os.Exit(1)
	}

	sunxi.gfxmembase = uint32(uintptr(ptr))
	sunxi.gfxmem = C.tlsf_create(ptr,
		C.size_t(sunxi.envInfo.phymem_total_size))

	if CedarIoctl(C.IOCTL_ENGINE_REQ, 0) != 0 {
		fmt.Println("IOCTL_ENGINE_REQ failed")
		os.Exit(1)
	}

	CedarIoctl(C.IOCTL_ENABLE_VE, 0)
	CedarIoctl(C.IOCTL_SET_VE_FREQ, 320)
	CedarIoctl(C.IOCTL_RESET_VE, 0)

	VeWrite(veCtrl, 0x00130007)

	sunxi.veVersion = int(VeRead(veVersionReg)) >> 16

	sunxi.ts.Trace(trace.TRACE_INFO, "CEDAR",
		"Version 0x%04x opened. PA: %x VA: %x",
		sunxi.veVersion, uint32(sunxi.envInfo.phymem_start),
		sunxi.gfxmembase)

	var args [4]uintptr

	args[1] = 0x64
	if DispIoctl(C.DISP_CMD_LAYER_CK_OFF, &args) != 0 {
		fmt.Println("DISP_CMD_LAYER_CK_OFF failed")
		os.Exit(1)
	}

	args[1] = 0x64
	if DispIoctl(C.DISP_CMD_LAYER_ALPHA_OFF, &args) != 0 {
		fmt.Println("DISP_CMD_LAYER_ALPHA_OFF failed")
		os.Exit(1)
	}

	// C: #if 0'd colorkey setup — kept out canonically

	sunxiSetBg("/root/background.jpg")
}

// SunxiFini — C: sunxi_fini (sunxi.c:173-182)
func SunxiFini() {
	if CedarIoctl(C.IOCTL_ENGINE_REL, 0) != 0 {
		fmt.Println("IOCTL_ENGINE_REL failed")
		os.Exit(1)
	}
	dispCleanup()
}

// ---------------------------------------------------------------------------
// C: background layer (sunxi.c:184-362)
// ---------------------------------------------------------------------------

var (
	bgLayer       int     // C: static int bg_layer
	bgWantedAlpha float32 // C: static float bg_wanted_alpha
	bgOpen        int     // C: static int bg_open
)

// sunxiSetBg — C: sunxi_set_bg (sunxi.c:189-315). The function body is
// unreachable in C (early `return;` at :199) — preserved verbatim.
func sunxiSetBg(path string) {
	var args [4]uintptr
	width, height := 1280, 720

	return

	im := &backendcore.ImageMeta{}
	im.ReqWidth = width
	im.ReqHeight = height

	rpath := miscpkg.RstrAllocStr(path)

	var pm *imagepkg.Pixmap
	var ierr error
	if sunxiBs != nil {
		var img0 any
		img0, ierr = sunxiBs.Imageloader(miscpkg.RstrGet(rpath), im,
			nil, nil, nil)
		if img := img0; img != nil {
			if ic := img.(*imagepkg.Image).FindComponent(
				imagepkg.ComponentPixmap); ic != nil {
				pm = ic.Pixmap
			}
		}
	}
	miscpkg.RstrRelease(rpath)

	if pm == nil {
		errStr := ""
		if ierr != nil {
			errStr = ierr.Error()
		}
		sunxi.ts.Trace(trace.TRACE_ERROR, "BG", "Unable to load %s -- %s",
			path, errStr)
		return
	}

	var bpp int

	switch pm.Type {
	case imagepkg.PixmapRGB24:
		bpp = 3
	case imagepkg.PixmapBGR32, imagepkg.PixmapRGBA, imagepkg.PixmapBGRA:
		bpp = 4
	default:
		C.abort()
	}

	tsize := pm.Height * pm.Stride

	sunxi.gfxmemMu.Lock()
	dst := C.tlsf_memalign(sunxi.gfxmem, 1024, C.size_t(tsize))
	sunxi.gfxmemMu.Unlock()
	C.memcpy(dst, unsafe.Pointer(&pm.Data[0]), C.size_t(tsize))

	imagepkg.PixmapRelease(pm)

	var frmbuf C.__disp_video_fb_t
	frmbuf.addr[0] = C.uint32_t(VaToPa(dst))
	frmbuf.addr[1] = C.uint32_t(VaToPa(dst))
	frmbuf.addr[2] = C.uint32_t(VaToPa(dst))

	args[1] = C.DISP_LAYER_WORK_MODE_NORMAL
	hlay := DispIoctl(C.DISP_CMD_LAYER_REQUEST, &args)
	if hlay == -1 {
		os.Exit(3)
	}

	var l C.__disp_layer_info_t

	l.mode = C.DISP_LAYER_WORK_MODE_NORMAL
	l.pipe = 1

	l.fb.size.width = C.uint32_t(pm.Stride / bpp)
	l.fb.size.height = C.uint32_t(pm.Height)
	l.fb.addr[0] = frmbuf.addr[0]
	l.fb.addr[1] = frmbuf.addr[1]
	l.fb.addr[2] = frmbuf.addr[2]

	switch pm.Type {
	case imagepkg.PixmapRGB24:
		l.fb.format = C.DISP_FORMAT_RGB888
		l.fb.br_swap = 1
		l.fb.mode = C.DISP_MOD_INTERLEAVED
	case imagepkg.PixmapBGR32:
		l.fb.format = C.DISP_FORMAT_ARGB8888
		l.fb.br_swap = 1
		l.fb.mode = C.DISP_MOD_INTERLEAVED
	default:
		C.abort()
	}

	l.ck_enable = 0
	l.alpha_en = 1
	l.alpha_val = 0
	l.src_win.x = 0
	l.src_win.y = 0
	l.src_win.width = C.uint32_t(width)
	l.src_win.height = C.uint32_t(height)
	l.scn_win.x = 0
	l.scn_win.y = 0
	l.scn_win.width = C.uint32_t(width)
	l.scn_win.height = C.uint32_t(height)

	args[1] = uintptr(hlay)
	args[2] = uintptr(unsafe.Pointer(&l))
	args[3] = 0
	if r := DispIoctl(C.DISP_CMD_LAYER_SET_PARA, &args); r != 0 {
		fmt.Println("ioctl(disphd,DISP_CMD_LAYER_SET_PARA)")
	}

	args[1] = uintptr(hlay)
	args[2] = 0
	if r := DispIoctl(C.DISP_CMD_LAYER_OPEN, &args); r != 0 {
		fmt.Println("ioctl(disphd,DISP_CMD_LAYER_OPEN)")
	}

	bgOpen = 1

	args[1] = uintptr(hlay)
	if DispIoctl(C.DISP_CMD_LAYER_BOTTOM, &args) != 0 {
		fmt.Println("ioctl(disphd,DISP_CMD_LAYER_BOTTOM)")
	}

	bgLayer = hlay
}

// sunxiBs — the backend system for backend_imageloader in the
// (dead-code) sunxi_set_bg body; wired by the platform init.
var sunxiBs *backendcore.BackendSystem

// SetBackendSystem wires the backend system (C resolves via globals).
func SetBackendSystem(bs *backendcore.BackendSystem) { sunxiBs = bs }

// lp — C: #define LP(a, y0, y1) (((y0)*((a)-1.0)+(y1))/(a)) (sunxi.c:318)
func lp(a, y0, y1 float32) float32 {
	return (y0*(a-1.0) + y1) / a
}

// SunxiBgEveryFrame — C: sunxi_bg_every_frame (sunxi.c:320-362)
func SunxiBgEveryFrame(hide int) {
	var args [4]uintptr
	if bgLayer == 0 {
		return
	}

	args[1] = uintptr(bgLayer)

	var alpha float32
	if hide == 0 {
		alpha = 1
	}

	bgWantedAlpha = lp(16, bgWantedAlpha, alpha)

	a8 := int(bgWantedAlpha * 255.0)

	if a8 == 0 {
		if bgOpen != 0 {
			r := DispIoctl(C.DISP_CMD_LAYER_CLOSE, &args)
			if r != 0 {
				fmt.Println("ioctl(disphd,DISP_CMD_LAYER_CLOSE)")
			} else {
				bgOpen = 0
			}
		}
	} else {
		if bgOpen == 0 {
			r := DispIoctl(C.DISP_CMD_LAYER_OPEN, &args)
			if r != 0 {
				fmt.Println("ioctl(disphd,DISP_CMD_LAYER_OPEN)")
				return
			}
			bgOpen = 1
		}

		if DispIoctl(C.DISP_CMD_LAYER_BOTTOM, &args) != 0 {
			fmt.Println("ioctl(disphd,DISP_CMD_LAYER_BOTTOM)")
		}

		args[2] = uintptr(a8)
		if DispIoctl(C.DISP_CMD_LAYER_SET_ALPHA_VALUE, &args) != 0 {
			fmt.Println("ioctl(disphd, DISP_CMD_LAYER_SET_ALPHA_VALUE)")
		}
	}
}

// SunxiFlushCache — C: sunxi_flush_cache (sunxi.c:365-374)
func SunxiFlushCache(start unsafe.Pointer, len int) {
	r := C.struct_cedarv_cache_range{
		start: C.long(int(uintptr(start))),
		end:   C.long(int(uintptr(start)) + len),
	}
	CedarIoctl(C.IOCTL_FLUSH_CACHE, uintptr(unsafe.Pointer(&r)))
}

// SunxiVeWait — C: sunxi_ve_wait (sunxi.c:377-384)
func SunxiVeWait(timeout int) int {
	if sunxi.cedarfd == -1 {
		return 0
	}
	return CedarIoctl(C.IOCTL_WAIT_VE, uintptr(timeout))
}

// ---------------------------------------------------------------------------
// C: cedar decode debug mirror (sunxi.c:390-517)
// ---------------------------------------------------------------------------

var (
	sramWritePtr    int          // C: static int sram_write_ptr
	cedarMirror     [4096]uint32 // C: static uint32_t cedar_mirror[4096]
	cedarSramMirror [4096]uint32 // C: static uint32_t cedar_sram_mirror[4096]
	h264Trig        uint32       // C: static uint32_t h264_trig
	outputFrameIdx  uint32       // C: static uint32_t output_frame_idx
)

// dumpDpb — C: dump_dpb (sunxi.c:398-416)
func dumpDpb() {
	for i := 0; i < 18; i++ {
		marker := ""
		if i == int(outputFrameIdx) {
			marker = "[OUTPUT]"
		}
		fmt.Printf("DPB[%02d] %3d:%-3d flags:0x%02x bufs:%08x %08x %08x %08x %x %s\n",
			i,
			cedarSramMirror[0x100+0+i*8],
			cedarSramMirror[0x100+1+i*8],
			cedarSramMirror[0x100+2+i*8],
			cedarSramMirror[0x100+3+i*8],
			cedarSramMirror[0x100+4+i*8],
			cedarSramMirror[0x100+5+i*8],
			cedarSramMirror[0x100+6+i*8],
			cedarSramMirror[0x100+7+i*8],
			marker)
	}
}

// dumpRefs — C: dump_refs (sunxi.c:418-432)
func dumpRefs() {
	fmt.Printf("REFLIST0: 0x%08x 0x%08x 0x%08x 0x%08x\n",
		cedarSramMirror[veSRAMH264RefList0/4+0],
		cedarSramMirror[veSRAMH264RefList0/4+1],
		cedarSramMirror[veSRAMH264RefList0/4+2],
		cedarSramMirror[veSRAMH264RefList0/4+3])

	fmt.Printf("REFLIST1: 0x%08x 0x%08x 0x%08x 0x%08x\n",
		cedarSramMirror[veSRAMH264RefList1/4+0],
		cedarSramMirror[veSRAMH264RefList1/4+1],
		cedarSramMirror[veSRAMH264RefList1/4+2],
		cedarSramMirror[veSRAMH264RefList1/4+3])
}

// dumpRegs — C: dump_regs (sunxi.c:434-446)
func dumpRegs() {
	fmt.Printf("REGS: %08x %08x %08x %08x %08x %08x %08x %08x\n",
		cedarMirror[veH264FrameSize],
		cedarMirror[veH264PicHdr],
		cedarMirror[veH264SliceHdr],
		cedarMirror[veH264SliceHdr2],
		cedarMirror[veH264PredWeight],
		cedarMirror[veH264QpParam],
		cedarMirror[veH264CurMbNum],
		cedarMirror[veH264SdrotCtrl])
}

// CedarDecodeWrite32 — C: cedar_decode_write32 (sunxi.c:457-501)
func CedarDecodeWrite32(off int, value int) {
	if off < 4096 {
		cedarMirror[off] = uint32(value)
	}

	switch off {
	case veH264RamWritePtr:
		if value&3 != 0 {
			panic("SRAM write ptr not 4-aligned")
		}
		sramWritePtr = value
		fmt.Printf("   SRAM Write starts at 0x%x\n", value)
		return

	case veH264RamWriteData:
		if sramWritePtr >= 4096 {
			panic("SRAM write ptr out of range")
		}
		cedarSramMirror[sramWritePtr>>2] = uint32(value)
		sramWritePtr += 4
		return

	case veH264OutputFrameIdx:
		outputFrameIdx = uint32(value)
		return

	case veH264Trigger:
		h264Trig = uint32(value)
		switch value & 0xf {
		case 8:
			fmt.Printf("Decode h264 start\n")
			dumpRegs()
			// C: if(0) dump_dpb(); if(0) dump_refs();
			return
		case 2, 4, 5:
			return
		}
	}
}

// CedarDecodeRead32 — C: cedar_decode_read32 (sunxi.c:504-517)
func CedarDecodeRead32(off int, value int) {
	switch off {
	case veH264Status:
		return
	case veH264BasicBits:
		return
	}
}
