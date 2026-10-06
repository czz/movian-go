/*
 * cedar_abi.c — C: src/video/cedar.c:493-500, 585-596, 822-833, 888-894
 * Defines the IVE/IOS/IFBM/IVBV interface tables as global symbols that
 * libvecore.so resolves directly from the executable. Function slots
 * point at Go-exported callbacks (cedar_export.go); sys_print is
 * variadic in the ABI so a shim forwards the fixed args.
 */
#include <stdarg.h>
#include "cedar_abi.h"

/* sys_print is variadic in the IOS ABI — the Go export takes the
 * fixed prefix only (the C body discards varargs anyway). */
static s32
sys_print_shim(u8 *func, u32 line, ...)
{
	return cedar_sys_print(func, line);
}

/****************************************************************************
 * VE interface — C: IVE (cedar.c:493-500)
 */
IVEControl_t IVE = {
	cedar_ve_reset_hardware,
	cedar_ve_enable_clock,
	cedar_ve_enable_intr,
	cedar_ve_wait_intr,
	cedar_ve_get_reg_base_addr,
	cedar_ve_get_memtype
};

/****************************************************************************
 * OS interface — C: IOS (cedar.c:585-596)
 */
IOS_t IOS = {
	cedar_mem_alloc,
	cedar_mem_free,
	cedar_mem_palloc,
	cedar_mem_pfree,
	cedar_mem_set,
	cedar_mem_cpy,
	cedar_mem_flush_cache,
	cedar_mem_get_phy_addr,
	sys_print_shim,
	cedar_sys_sleep
};

/****************************************************************************
 * FBM interface — C: IFBM, non-OLD_VE variant (cedar.c:822-833)
 */
IFBM_t IFBM = {
	cedar_fbm_deinit,
	cedar_fbm_request_frame,
	cedar_fbm_return_frame,
	cedar_fbm_share_frame,
	cedar_fbm_init_ex,
	cedar_fbm_init_ex_yv12,
	cedar_fbm_init_ex_yv32,
	cedar_fbm_flush_frame,
	cedar_fbm_print_status,
	cedar_fbm_alloc_yv12_frame_buffer,
};

/****************************************************************************
 * VBV interface — C: IVBV (cedar.c:888-894)
 */
IVBV_t IVBV = {
	cedar_vbv_request_stream_frame,
	cedar_vbv_return_stream_frame,
	cedar_vbv_flush_stream_frame,
	cedar_vbv_get_base_addr,
	cedar_vbv_get_buffer_size
};
