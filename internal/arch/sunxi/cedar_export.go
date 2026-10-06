//go:build sunxi

// cedar_export.go — //export trampolines for the libve ABI callbacks.
// These symbols are referenced by the IVE/IOS/IFBM/IVBV tables in
// cedar_abi.c, which libvecore.so resolves from the executable.
// (cgo //export rule: this file's preamble holds declarations only.)
package sunxi

/*
#cgo CFLAGS: -I${SRCDIR}/include
#include <libve.h>
#include <commom_type.h>
*/
import "C"
import "unsafe"

// ---------------------------------------------------------------------------
// VE interface — C: cedar.c:445-490
// ---------------------------------------------------------------------------

//export cedar_ve_reset_hardware
func cedar_ve_reset_hardware() { veResetHardware() }

//export cedar_ve_enable_clock
func cedar_ve_enable_clock(enable C.u8, speed C.u32) {
	veEnableClock(enable != 0, uint32(speed))
}

//export cedar_ve_enable_intr
func cedar_ve_enable_intr(enable C.u8) { veEnableIntr(enable != 0) }

//export cedar_ve_wait_intr
func cedar_ve_wait_intr() C.s32 { return C.s32(veWaitIntr()) }

//export cedar_ve_get_reg_base_addr
func cedar_ve_get_reg_base_addr() C.u32 { return C.u32(veGetRegBaseAddr()) }

//export cedar_ve_get_memtype
func cedar_ve_get_memtype() C.memtype_e {
	return C.memtype_e(veGetMemtype())
}

// ---------------------------------------------------------------------------
// OS interface — C: cedar.c:504-581
// ---------------------------------------------------------------------------

//export cedar_mem_alloc
func cedar_mem_alloc(size C.u32) unsafe.Pointer {
	return memAlloc(uint32(size))
}

//export cedar_mem_free
func cedar_mem_free(p unsafe.Pointer) { memFree(p) }

//export cedar_mem_palloc
func cedar_mem_palloc(size, align C.u32) unsafe.Pointer {
	return memPalloc(uint32(size), uint32(align))
}

//export cedar_mem_pfree
func cedar_mem_pfree(p unsafe.Pointer) { memPfree(p) }

//export cedar_mem_set
func cedar_mem_set(mem unsafe.Pointer, value, size C.u32) {
	memSet(mem, uint32(value), uint32(size))
}

//export cedar_mem_cpy
func cedar_mem_cpy(dst, src unsafe.Pointer, size C.u32) {
	memCpy(dst, src, uint32(size))
}

//export cedar_mem_flush_cache
func cedar_mem_flush_cache(mem *C.u8, size C.u32) {
	memFlushCache(unsafe.Pointer(mem), uint32(size))
}

//export cedar_mem_get_phy_addr
func cedar_mem_get_phy_addr(virtualAddr C.u32) C.u32 {
	return C.u32(memGetPhyAddr(uint32(virtualAddr)))
}

//export cedar_sys_print
func cedar_sys_print(fn *C.u8, line C.u32) C.s32 {
	return C.s32(sysPrint(fn, uint32(line)))
}

//export cedar_sys_sleep
func cedar_sys_sleep(ms C.u32) { sysSleep(uint32(ms)) }

// ---------------------------------------------------------------------------
// FBM interface — C: cedar.c:601-805
// ---------------------------------------------------------------------------

//export cedar_fbm_teardown
func cedar_fbm_teardown(h C.Handle, parent unsafe.Pointer) {
	fbmTeardown(h, parent)
}

//export cedar_fbm_request_frame
func cedar_fbm_request_frame(h C.Handle) *C.vpicture_t {
	return fbmDecoderRequestFrame(h)
}

//export cedar_fbm_return_frame
func cedar_fbm_return_frame(f *C.vpicture_t, valid C.u8, h C.Handle) {
	fbmDecoderReturnFrame(f, valid != 0, h)
}

//export cedar_fbm_share_frame
func cedar_fbm_share_frame(f *C.vpicture_t, h C.Handle) {
	fbmDecoderShareFrame(f, h)
}

//export cedar_fbm_setup_ex
func cedar_fbm_setup_ex(maxFrameNum, minFrameNum C.u32, sizeY, sizeU,
	sizeV, sizeAlpha *C.u32, mode3d C._3d_mode_e,
	format C.pixel_format_e, unknown C.u8, parent unsafe.Pointer) C.Handle {
	return fbmSetupEx(uint32(maxFrameNum), uint32(minFrameNum),
		sizeY, sizeU, sizeV, sizeAlpha,
		int32(mode3d), int32(format), uint8(unknown), parent)
}

//export cedar_fbm_setup_ex_yv12
func cedar_fbm_setup_ex_yv12(maxFrameNum, minFrameNum C.u32, sizeY, sizeU,
	sizeV, sizeAlpha *C.u32, mode3d C._3d_mode_e,
	format C.pixel_format_e, unknown C.u8, parent unsafe.Pointer) C.Handle {
	return fbmSetupExYV12(uint32(maxFrameNum), uint32(minFrameNum),
		sizeY, sizeU, sizeV, sizeAlpha,
		int32(mode3d), int32(format), uint8(unknown), parent)
}

//export cedar_fbm_setup_ex_yv32
func cedar_fbm_setup_ex_yv32(maxFrameNum, minFrameNum C.u32, sizeY, sizeU,
	sizeV, sizeAlpha *C.u32, mode3d C._3d_mode_e,
	format C.pixel_format_e, unknown C.u8, parent unsafe.Pointer) C.Handle {
	return fbmSetupExYV32(uint32(maxFrameNum), uint32(minFrameNum),
		sizeY, sizeU, sizeV, sizeAlpha,
		int32(mode3d), int32(format), uint8(unknown), parent)
}

//export cedar_fbm_flush_frame
func cedar_fbm_flush_frame(h C.Handle, pts C.s64) {
	fbmFlushFrame(h, int64(pts))
}

//export cedar_fbm_print_status
func cedar_fbm_print_status(h C.Handle) { fbmPrintStatus(h) }

//export cedar_fbm_alloc_yv12_frame_buffer
func cedar_fbm_alloc_yv12_frame_buffer(h C.Handle) {
	fbmAllocYV12FrameBuffer(h)
}

// ---------------------------------------------------------------------------
// VBV interface — C: cedar.c:840-884
// ---------------------------------------------------------------------------

//export cedar_vbv_request_stream_frame
func cedar_vbv_request_stream_frame(vbv C.Handle) *C.vstream_data_t {
	return vbvRequestStreamFrame(vbv)
}

//export cedar_vbv_return_stream_frame
func cedar_vbv_return_stream_frame(stream *C.vstream_data_t,
	vbv C.Handle) {
	vbvReturnStreamFrame(stream, vbv)
}

//export cedar_vbv_flush_stream_frame
func cedar_vbv_flush_stream_frame(stream *C.vstream_data_t,
	vbv C.Handle) {
	vbvFlushStreamFrame(stream, vbv)
}

//export cedar_vbv_get_base_addr
func cedar_vbv_get_base_addr(vbv C.Handle) *C.u8 {
	return vbvGetBaseAddr(vbv)
}

//export cedar_vbv_get_buffer_size
func cedar_vbv_get_buffer_size(vbv C.Handle) C.u32 {
	return C.u32(vbvGetBufferSize(vbv))
}

// ---------------------------------------------------------------------------
// C: cedarv_f23_ic_version (cedar.c:898-904) — probed by libvecore
// ---------------------------------------------------------------------------

//export cedarv_f23_ic_version
func cedarv_f23_ic_version() C.int { return 1 }
