/*
 * omx_shim.h — real-function wrappers for the OMX macros that cgo
 * cannot call directly (OMX_SendCommand, OMX_GetParameter, ... are
 * function-like macros in OMX_Core.h, not symbols).
 *
 * Canonical port: each wrapper forwards to the same macro expansion
 * the C code uses.
 */
#pragma once

#include <OMX_Core.h>
#include <OMX_Component.h>

static inline OMX_ERRORTYPE omx_SendCommand_(OMX_HANDLETYPE h,
					     OMX_COMMANDTYPE cmd,
					     OMX_U32 nParam,
					     OMX_PTR pCmdData) {
	return OMX_SendCommand(h, cmd, nParam, pCmdData);
}

static inline OMX_ERRORTYPE omx_GetParameter_(OMX_HANDLETYPE h,
					      OMX_INDEXTYPE idx,
					      OMX_PTR p) {
	return OMX_GetParameter(h, idx, p);
}

static inline OMX_ERRORTYPE omx_SetParameter_(OMX_HANDLETYPE h,
					      OMX_INDEXTYPE idx,
					      OMX_PTR p) {
	return OMX_SetParameter(h, idx, p);
}

static inline OMX_ERRORTYPE omx_GetConfig_(OMX_HANDLETYPE h,
					   OMX_INDEXTYPE idx,
					   OMX_PTR p) {
	return OMX_GetConfig(h, idx, p);
}

static inline OMX_ERRORTYPE omx_SetConfig_(OMX_HANDLETYPE h,
					   OMX_INDEXTYPE idx,
					   OMX_PTR p) {
	return OMX_SetConfig(h, idx, p);
}

static inline OMX_ERRORTYPE omx_GetState_(OMX_HANDLETYPE h,
					  OMX_STATETYPE *pState) {
	return OMX_GetState(h, pState);
}

static inline OMX_ERRORTYPE omx_EmptyThisBuffer_(OMX_HANDLETYPE h,
						 OMX_BUFFERHEADERTYPE *b) {
	return OMX_EmptyThisBuffer(h, b);
}

static inline OMX_ERRORTYPE omx_FillThisBuffer_(OMX_HANDLETYPE h,
						OMX_BUFFERHEADERTYPE *b) {
	return OMX_FillThisBuffer(h, b);
}

static inline OMX_ERRORTYPE omx_AllocateBuffer_(OMX_HANDLETYPE h,
						OMX_BUFFERHEADERTYPE **b,
						OMX_U32 port,
						OMX_PTR app,
						OMX_U32 size) {
	return OMX_AllocateBuffer(h, b, port, app, size);
}

static inline OMX_ERRORTYPE omx_UseBuffer_(OMX_HANDLETYPE h,
					   OMX_BUFFERHEADERTYPE **b,
					   OMX_U32 port,
					   OMX_PTR app,
					   OMX_U32 size,
					   OMX_U8 *data) {
	return OMX_UseBuffer(h, b, port, app, size, data);
}

static inline OMX_ERRORTYPE omx_FreeBuffer_(OMX_HANDLETYPE h,
					    OMX_U32 port,
					    OMX_BUFFERHEADERTYPE *b) {
	return OMX_FreeBuffer(h, port, b);
}

static inline OMX_ERRORTYPE omx_SetupTunnel_(OMX_HANDLETYPE out,
					     OMX_U32 outport,
					     OMX_HANDLETYPE in,
					     OMX_U32 inport) {
	return OMX_SetupTunnel(out, outport, in, inport);
}

/* OMX_INIT_STRUCTURE's version part — OMX_VERSIONTYPE is a union
   (opaque byte array to cgo), so init via helper. */
static inline void omx_init_version_(OMX_VERSIONTYPE *v) {
	v->nVersion = OMX_VERSION;
}
