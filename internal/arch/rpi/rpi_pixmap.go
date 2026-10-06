//go:build rpi

// rpi_pixmap.go — C: src/arch/rpi/rpi_pixmap.c — OMX hardware JPEG
// decoder (OMX.broadcom.image_decode + OMX.broadcom.resize → BGR32
// pixmap) registered as accel_image_decode.
package rpi

/*
#cgo CFLAGS: -DOMX_SKIP64BIT -I/opt/vc/include -I/opt/vc/include/interface/vmcs_host/linux -I/opt/vc/include/interface/vcos/pthreads
#cgo LDFLAGS: -L/opt/vc/lib -lbcm_host -lvcos -lvchiq_arm -lopenmaxil -lEGL -lGLESv2 -lmmal_core -lmmal_util -lmmal_vc_client -lvchostif

#include <stdlib.h>
#include <string.h>
#include <OMX_Core.h>
#include <OMX_Component.h>
#include <OMX_Broadcom.h>
#include "omx_shim.h"

static OMX_IMAGE_PORTDEFINITIONTYPE *pdi(OMX_PARAM_PORTDEFINITIONTYPE *p) { return &p->format.image; }

static void *rpi_memalign(size_t align, size_t size) {
	void *p = NULL;
	if (posix_memalign(&p, align, size)) return NULL;
	return p;
}
*/
import "C"

import (
	"errors"
	"runtime"
	"sync"
	"unsafe"

	"github.com/czz/movian-go/internal/image"
	"github.com/czz/movian-go/internal/misc"
)

// C: rpi_pixmap_decoder_t (rpi_pixmap.c:34-45)
type rpiPixmapDecoder struct {
	mtx     sync.Mutex              // C: rpd_mtx
	cond    *sync.Cond              // C: rpd_cond
	change  int                     // C: rpd_change
	outbuf  *C.OMX_BUFFERHEADERTYPE // C: rpd_outbuf
	decoder *OmxComponent           // C: rpd_decoder
	resizer *OmxComponent           // C: rpd_resizer
	tunnel  *OmxTunnel              // C: rpd_tunnel
	im      *image.ImageMeta        // C: rpd_im
	pm      *image.Pixmap           // C: rpd_pm
	buf     *C.OMX_BUFFERHEADERTYPE // C: rpd_buf
}

// decoderPortSettingsChanged — C: decoder_port_settings_changed
// (rpi_pixmap.c:52-62)
func decoderPortSettingsChanged(oc *OmxComponent) {
	rpd := oc.Opaque.(*rpiPixmapDecoder)
	rpd.mtx.Lock()
	rpd.change = 1
	rpd.cond.Signal()
	rpd.mtx.Unlock()
}

// pixmapDecoderCreate — C: pixmap_decoder_create (rpi_pixmap.c:67-99)
func pixmapDecoderCreate(cfmt C.OMX_IMAGE_CODINGTYPE) *rpiPixmapDecoder {
	rpd := &rpiPixmapDecoder{}
	rpd.cond = sync.NewCond(&rpd.mtx)

	rpd.decoder = OmxComponentCreate("OMX.broadcom.image_decode",
		&rpd.mtx, rpd.cond)

	rpd.decoder.PortSettingsChangedCb = decoderPortSettingsChanged
	rpd.decoder.Opaque = rpd

	rpd.resizer = OmxComponentCreate("OMX.broadcom.resize",
		&rpd.mtx, rpd.cond)

	OmxSetState(rpd.decoder, C.OMX_StateIdle)

	var fmt_ C.OMX_IMAGE_PARAM_PORTFORMATTYPE
	fmt_.nSize = C.OMX_U32(unsafe.Sizeof(fmt_))
	C.omx_init_version_(&fmt_.nVersion)
	fmt_.nPortIndex = C.OMX_U32(rpd.decoder.Inport)
	fmt_.eCompressionFormat = cfmt
	Omxchk(C.omx_SetParameter_(rpd.decoder.Handle,
		C.OMX_IndexParamImagePortFormat, C.OMX_PTR(unsafe.Pointer(&fmt_))), "OMX_SetParameter")

	// C: #ifndef NOCOPY path — NOCOPY is not defined in the build
	OmxAllocBuffers(rpd.decoder, rpd.decoder.Inport)
	OmxSetState(rpd.decoder, C.OMX_StateExecuting)

	return rpd
}

// setupTunnel — C: setup_tunnel (rpi_pixmap.c:104-169)
func setupTunnel(rpd *rpiPixmapDecoder) {
	var dstWidth, dstHeight int
	var portdef C.OMX_PARAM_PORTDEFINITIONTYPE

	if rpd.tunnel != nil {
		return
	}

	portdef.nSize = C.OMX_U32(unsafe.Sizeof(portdef))
	C.omx_init_version_(&portdef.nVersion)
	portdef.nPortIndex = C.OMX_U32(rpd.decoder.Outport)
	Omxchk(C.omx_GetParameter_(rpd.decoder.Handle,
		C.OMX_IndexParamPortDefinition, C.OMX_PTR(unsafe.Pointer(&portdef))), "OMX_GetParameter")
	C.pdi(&portdef).nSliceHeight = 16
	Omxchk(C.omx_SetParameter_(rpd.decoder.Handle,
		C.OMX_IndexParamPortDefinition, C.OMX_PTR(unsafe.Pointer(&portdef))), "OMX_SetParameter")

	dstWidth, dstHeight = image.PixmapComputeRescaleDim(rpd.im,
		int(C.pdi(&portdef).nFrameWidth), int(C.pdi(&portdef).nFrameHeight))

	portdef.nPortIndex = C.OMX_U32(rpd.resizer.Inport)
	Omxchk(C.omx_SetParameter_(rpd.resizer.Handle,
		C.OMX_IndexParamPortDefinition, C.OMX_PTR(unsafe.Pointer(&portdef))), "OMX_SetParameter")

	rpd.tunnel = OmxTunnelCreate(rpd.decoder, rpd.decoder.Outport,
		rpd.resizer, rpd.resizer.Inport, "decoder -> resizer")

	portdef.nSize = C.OMX_U32(unsafe.Sizeof(portdef))
	C.omx_init_version_(&portdef.nVersion)
	portdef.nPortIndex = C.OMX_U32(rpd.resizer.Outport)
	Omxchk(C.omx_GetParameter_(rpd.resizer.Handle,
		C.OMX_IndexParamPortDefinition, C.OMX_PTR(unsafe.Pointer(&portdef))), "OMX_GetParameter")

	stride := (dstWidth*4 + image.PixmapRowAlign - 1) &^ (image.PixmapRowAlign - 1)

	img := C.pdi(&portdef)
	img.eCompressionFormat = C.OMX_IMAGE_CodingUnused
	img.eColorFormat = C.OMX_COLOR_Format32bitABGR8888
	img.nFrameWidth = C.OMX_U32(dstWidth)
	img.nFrameHeight = C.OMX_U32(dstHeight)
	img.nStride = C.OMX_S32(stride)
	img.nSliceHeight = 0
	img.bFlagErrorConcealment = C.OMX_FALSE

	Omxchk(C.omx_SetParameter_(rpd.resizer.Handle,
		C.OMX_IndexParamPortDefinition, C.OMX_PTR(unsafe.Pointer(&portdef))), "OMX_SetParameter")

	Omxchk(C.omx_GetParameter_(rpd.resizer.Handle,
		C.OMX_IndexParamPortDefinition, C.OMX_PTR(unsafe.Pointer(&portdef))), "OMX_GetParameter")

	OmxSetState(rpd.resizer, C.OMX_StateExecuting)
	OmxPortEnable(rpd.resizer, rpd.resizer.Outport)

	// C: pm = calloc(1, sizeof(pixmap_t)); pm->pm_data =
	//   mymemalign(portdef.nBufferAlignment, portdef.nBufferSize)
	// OMX_UseBuffer writes into pm_data — it must be pinned C memory.
	pm := &image.Pixmap{
		Width:  int(img.nFrameWidth),
		Height: int(img.nFrameHeight),
		Stride: int(img.nStride),
		Type:   image.PixmapBGR32,
	}
	pm.Aspect = float32(pm.Width) / float32(pm.Height)
	pm.Flags |= image.PixmapOpaque

	cdata := C.rpi_memalign(C.size_t(portdef.nBufferAlignment),
		C.size_t(portdef.nBufferSize))
	pm.Data = unsafe.Slice((*byte)(cdata), int(portdef.nBufferSize))
	// C: pm_data is freed by pixmap_release → free it when Go collects
	// the pixmap (the C buffer cannot be reclaimed by the GC).
	runtime.SetFinalizer(pm, func(p *image.Pixmap) {
		C.free(unsafe.Pointer(&p.Data[0]))
	})
	rpd.pm = pm

	Omxchk(C.omx_UseBuffer_(rpd.resizer.Handle, &rpd.buf,
		C.OMX_U32(rpd.resizer.Outport), nil, portdef.nBufferSize,
		(*C.OMX_U8)(cdata)), "OMX_UseBuffer")

	OmxWaitCommand(rpd.resizer)
	Omxchk(C.omx_FillThisBuffer_(rpd.resizer.Handle, rpd.buf),
		"OMX_FillThisBuffer")
}

// rpiPixmapDecode — C: rpi_pixmap_decode (rpi_pixmap.c:185-350).
// Only the #else (copy) path is compiled — NOCOPY is a dead #ifdef.
func rpiPixmapDecode(cType image.CodedType, buf *misc.Buf,
	im *image.ImageMeta, img *image.Image) (*image.Pixmap, error) {
	if cType != image.CodedJPEG {
		return nil, nil
	}
	if img.Flags&image.FlagProgressive != 0 {
		return nil, nil
	}
	if img.ColorPlanes != 3 {
		return nil, nil
	}

	rpd := pixmapDecoderCreate(C.OMX_IMAGE_CodingJPEG)
	if rpd == nil {
		return nil, nil
	}
	rpd.im = im

	// C: const void *data = buf_data(buf); size_t len = buf_size(buf)
	data := buf.C8()
	len_ := len(data)

	rpd.mtx.Lock()

	for len_ > 0 {
		if rpd.decoder.StreamCorrupt != 0 {
			break
		}

		if rpd.change == 1 {
			rpd.change = 2
			rpd.mtx.Unlock()
			setupTunnel(rpd)
			rpd.mtx.Lock()
			continue
		}

		b := rpd.decoder.Avail
		if b == nil {
			rpd.cond.Wait()
			continue
		}

		rpd.decoder.Avail = (*C.OMX_BUFFERHEADERTYPE)(b.pAppPrivate)
		rpd.decoder.InflightBuffers++
		rpd.decoder.AvailBytes -= int(b.nAllocLen)

		rpd.mtx.Unlock()

		b.nOffset = 0
		n := int(b.nAllocLen)
		if len_ < n {
			n = len_
		}
		b.nFilledLen = C.OMX_U32(n)
		C.memcpy(unsafe.Pointer(b.pBuffer), unsafe.Pointer(&data[0]), C.size_t(n))
		b.nFlags = 0

		if len_ <= int(b.nAllocLen) {
			b.nFlags |= C.OMX_BUFFERFLAG_EOS
		}

		data = data[int(b.nFilledLen):]
		len_ -= int(b.nFilledLen)
		Omxchk(C.omx_EmptyThisBuffer_(rpd.decoder.Handle, b),
			"OMX_EmptyThisBuffer")

		rpd.mtx.Lock()
	}

	if rpd.decoder.StreamCorrupt == 0 {
		if rpd.change != 2 {
			for rpd.change == 0 && rpd.decoder.StreamCorrupt == 0 {
				rpd.cond.Wait()
			}
			rpd.mtx.Unlock()
			if rpd.decoder.StreamCorrupt != 0 {
				goto out
			}
			setupTunnel(rpd)
		} else {
			rpd.mtx.Unlock()
		}

		OmxWaitFillBuffer(rpd.resizer, rpd.buf)
	} else {
		rpd.mtx.Unlock()
	}

out:
	OmxFlushPort(rpd.decoder, rpd.decoder.Inport)
	OmxFlushPort(rpd.decoder, rpd.decoder.Outport)
	OmxFlushPort(rpd.resizer, rpd.resizer.Inport)
	OmxFlushPort(rpd.resizer, rpd.resizer.Outport)

	if rpd.tunnel != nil {
		OmxTunnelDestroy(rpd.tunnel)
		rpd.tunnel = nil
	}

	OmxSetState(rpd.decoder, C.OMX_StateIdle)
	OmxSetState(rpd.resizer, C.OMX_StateIdle)

	if rpd.buf != nil {
		Omxchk(C.omx_FreeBuffer_(rpd.resizer.Handle,
			C.OMX_U32(rpd.resizer.Outport), rpd.buf), "OMX_FreeBuffer")
	}

	OmxReleaseBuffers(rpd.decoder, rpd.decoder.Inport)

	OmxSetState(rpd.resizer, C.OMX_StateLoaded)
	OmxSetState(rpd.decoder, C.OMX_StateLoaded)

	OmxComponentDestroy(rpd.resizer)
	OmxComponentDestroy(rpd.decoder)

	outPm := rpd.pm
	if outPm == nil {
		return nil, errors.New("Load error")
	}
	return outPm, nil
}

// rpiPixmapStart — C: rpi_pixmap_init (rpi_pixmap.c:355-360)
func rpiPixmapStart() {
	image.SetAccelImageDecode(rpiPixmapDecode)
}
