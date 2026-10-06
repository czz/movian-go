//go:generate go run generate_cgo.go

package image

// Canonical port of src/image/image_decoder_libav.c.

/*
#include <libavcodec/avcodec.h>
#include <libavutil/imgutils.h>
#include <libavutil/pixfmt.h>
#include <libavutil/pixdesc.h>
#include <libswscale/swscale.h>
#include <string.h>

// image_decode_libav uses the old avcodec_decode_video2; the Go port
// maps it to send/receive (see below).
*/
import "C"
import (
	"errors"
	"fmt"
	"github.com/czz/movian-go/internal/gconf"
	"runtime"
	"unsafe"

	"github.com/czz/movian-go/internal/arch"
	miscbuf "github.com/czz/movian-go/internal/misc"
)

// fulhack — C: fulhack (image_decoder_libav.c:52-79). Copies the
// first three planes verbatim into an RGB24 pixmap (the "ugly hack"
// — not a real YUV conversion), then rescales as RGB24.
func fulhack(frame *C.AVFrame, srcW, srcH, dstW, dstH, withAlpha,
	margin int, g *gconf.T) *Pixmap {
	pm := PixmapCreate(srcW, srcH, PixmapRGB24, 0)
	d0 := (*[1 << 30]byte)(unsafe.Pointer(frame.data[0]))
	d1 := (*[1 << 30]byte)(unsafe.Pointer(frame.data[1]))
	d2 := (*[1 << 30]byte)(unsafe.Pointer(frame.data[2]))
	for y := range srcH {
		dst := pmPixelOff(pm, 0, y)
		o0 := int(frame.linesize[0]) * y
		o1 := int(frame.linesize[1]) * y
		o2 := int(frame.linesize[2]) * y
		for x := range srcW {
			pm.Data[dst] = d0[o0+x]
			dst++
			pm.Data[dst] = d1[o1+x]
			dst++
			pm.Data[dst] = d2[o2+x]
			dst++
		}
	}

	// AVPicture pict2 wrapping the RGB24 pixmap
	var d [8]*C.uint8_t
	var l [8]C.int
	d[0] = (*C.uint8_t)(unsafe.Pointer(&pm.Data[pmPixelOff(pm, 0, 0)]))
	l[0] = C.int(pm.Stride)
	var pinner runtime.Pinner
	pinner.Pin(&pm.Data[0])
	pinner.Pin(&d) // pin array + its Go-pointer elements for cgo
	pinner.Pin(&l)
	pm2 := pixmapRescaleSwscalePict(frame, d, l, C.AV_PIX_FMT_RGB24,
		srcW, srcH, dstW, dstH, withAlpha, margin, g)
	pinner.Unpin()
	PixmapRelease(pm)
	return pm2
}

// pixmapRescaleSwscalePict — C: pixmap_rescale_swscale
// (image_decoder_libav.c:84-188). srcData/srcLinesize carry the
// AVPicture planes.
func pixmapRescaleSwscalePict(frame *C.AVFrame, srcData [8]*C.uint8_t,
	srcLinesize [8]C.int, srcPixFmt C.int, srcW, srcH, dstW, dstH,
	withAlpha, margin int, g *gconf.T) *Pixmap {
	var dstPixFmt C.int
	outflags := 0

	desc := C.av_pix_fmt_desc_get(C.enum_AVPixelFormat(srcPixFmt))
	if desc != nil && (desc.flags&C.AV_PIX_FMT_FLAG_ALPHA) == 0 {
		outflags |= PixmapOpaque
	}

	switch C.enum_AVPixelFormat(srcPixFmt) {
	case C.AV_PIX_FMT_Y400A, C.AV_PIX_FMT_BGRA, C.AV_PIX_FMT_RGBA,
		C.AV_PIX_FMT_ABGR, C.AV_PIX_FMT_ARGB:
		// __PPC__ returns NULL — LE build: convert
		dstPixFmt = C.AV_PIX_FMT_BGR32

	case C.AV_PIX_FMT_YUVA444P:
		return fulhack(frame, srcW, srcH, dstW, dstH, withAlpha, margin, g)

	default:
		dstPixFmt = C.AV_PIX_FMT_BGR32
	}

	// FFmpeg ≥5 (utils.c handle_jpeg): deprecated jpeg-range formats are
	// internally rewritten to the modern equivalent with src_range=1 and
	// a "deprecated pixel format" warning. Map them here and pass the
	// range explicitly via sws_setColorspaceDetails — identical
	// conversion, no warning.
	srcRange := C.int(0)
	switch C.enum_AVPixelFormat(srcPixFmt) {
	case C.AV_PIX_FMT_YUVJ420P:
		srcPixFmt = C.int(C.AV_PIX_FMT_YUV420P)
		srcRange = 1
	case C.AV_PIX_FMT_YUVJ411P:
		srcPixFmt = C.int(C.AV_PIX_FMT_YUV411P)
		srcRange = 1
	case C.AV_PIX_FMT_YUVJ422P:
		srcPixFmt = C.int(C.AV_PIX_FMT_YUV422P)
		srcRange = 1
	case C.AV_PIX_FMT_YUVJ444P:
		srcPixFmt = C.int(C.AV_PIX_FMT_YUV444P)
		srcRange = 1
	case C.AV_PIX_FMT_YUVJ440P:
		srcPixFmt = C.int(C.AV_PIX_FMT_YUV440P)
		srcRange = 1
	case C.AV_PIX_FMT_GRAY8, C.AV_PIX_FMT_YA8,
		C.AV_PIX_FMT_GRAY9LE, C.AV_PIX_FMT_GRAY9BE,
		C.AV_PIX_FMT_GRAY10LE, C.AV_PIX_FMT_GRAY10BE,
		C.AV_PIX_FMT_GRAY12LE, C.AV_PIX_FMT_GRAY12BE,
		C.AV_PIX_FMT_GRAY14LE, C.AV_PIX_FMT_GRAY14BE,
		C.AV_PIX_FMT_GRAY16LE, C.AV_PIX_FMT_GRAY16BE,
		C.AV_PIX_FMT_YA16BE, C.AV_PIX_FMT_YA16LE:
		// Full-range formats per handle_jpeg (no rewrite → no warning,
		// but the explicit range matches what init would apply).
		srcRange = 1
	}

	swsFlags := C.int(C.SWS_LANCZOS)
	if g != nil && g.EnableImageDebug.Load() {
		swsFlags |= C.SWS_PRINT_INFO
	}

	sws := C.sws_getContext(C.int(srcW), C.int(srcH),
		C.enum_AVPixelFormat(srcPixFmt),
		C.int(dstW), C.int(dstH), C.enum_AVPixelFormat(dstPixFmt),
		swsFlags, nil, nil, nil)
	if sws == nil {
		return nil
	}

	// C-equivalent of the internal defaults (utils.c:1176): default
	// coefficient tables, explicit srcRange, dstRange=0, identity
	// brightness/contrast/saturation.
	C.sws_setColorspaceDetails(sws,
		C.sws_getCoefficients(C.SWS_CS_DEFAULT), srcRange,
		C.sws_getCoefficients(C.SWS_CS_DEFAULT), 0,
		0, 1<<16, 1<<16)

	var pm *Pixmap
	switch C.enum_AVPixelFormat(dstPixFmt) {
	case C.AV_PIX_FMT_RGB24:
		pm = PixmapCreate(dstW, dstH, PixmapRGB24, margin)
	default:
		pm = PixmapCreate(dstW, dstH, PixmapBGR32, margin)
	}

	if pm == nil {
		C.sws_freeContext(sws)
		return nil
	}

	// Set scale destination with respect to margin
	var dstData [8]*C.uint8_t
	var dstLinesize [8]C.int

	// swscale requires 16-byte aligned dst pointer and stride to use its
	// aligned SIMD paths (swscale.c:346-360); otherwise it logs
	// "dstStride is not aligned" under SWS_PRINT_INFO and falls back to
	// unaligned accesses. pm.Stride is only 8-aligned (C-exact, GL row
	// pitch depends on it) and pmPixelOff(0,0) is offset by the margin,
	// so when either check fails we scale into an aligned scratch buffer
	// and blit the rows into the pixmap.
	var scratch []byte
	scratchStride := 0
	off0 := pmPixelOff(pm, 0, 0)
	if pm.Stride%16 == 0 &&
		uintptr(unsafe.Pointer(&pm.Data[off0]))%16 == 0 {
		dstData[0] = (*C.uint8_t)(unsafe.Pointer(&pm.Data[off0]))
		dstLinesize[0] = C.int(pm.Stride)
	} else {
		bpp := bytesPerPixel(pm.Type)
		scratchStride = (dstW*bpp + 15) &^ 15
		scratch = arch.MyMemalign(16, scratchStride*dstH+8)
		dstData[0] = (*C.uint8_t)(unsafe.Pointer(&scratch[0]))
		dstLinesize[0] = C.int(scratchStride)
	}

	var pinner runtime.Pinner
	pinner.Pin(&pm.Data[0])
	if scratch != nil {
		pinner.Pin(&scratch[0])
	}
	pinner.Pin(&srcData) // copy of caller's array holds Go ptrs
	pinner.Pin(&srcLinesize)
	pinner.Pin(&dstData)
	pinner.Pin(&dstLinesize)
	C.sws_scale(sws, &srcData[0], &srcLinesize[0], 0, C.int(srcH),
		&dstData[0], &dstLinesize[0])
	pinner.Unpin()

	if scratch != nil {
		bpp := bytesPerPixel(pm.Type)
		for y := range dstH {
			copy(pm.Data[pmPixelOff(pm, 0, y):][:dstW*bpp],
				scratch[y*scratchStride:][:dstW*bpp])
		}
	}

	C.sws_freeContext(sws)
	pm.Flags |= outflags
	return pm
}

// pixmapRescaleSwscale — helper for the frame-typed call sites:
// forwards frame.data/linesize.
func pixmapRescaleSwscale(frame *C.AVFrame, srcPixFmt C.int, srcW,
	srcH, dstW, dstH, withAlpha, margin int, g *gconf.T) *Pixmap {
	return pixmapRescaleSwscalePict(frame, frame.data, frame.linesize,
		srcPixFmt, srcW, srcH, dstW, dstH, withAlpha, margin, g)
}

// swizzleXWZY — C: swizzle_xwzy (image_decoder_libav.c:191-200).
func swizzleXWZY(dst, src []uint32, count int) {
	for i := range count {
		u32 := src[i]
		dst[i] = (u32 & 0xff00ff00) | (u32&0xff)<<16 | (u32&0xff0000)>>16
	}
}

// pixmap32bitSwizzle — C: pixmap_32bit_swizzle
// (image_decoder_libav.c:202-230). #if __BIG_ENDIAN__ only — this is a
// little-endian build so it always returns NULL.
func pixmap32bitSwizzle(frame *C.AVFrame, pixFmt C.int, w, h, m int) *Pixmap {
	return nil
}

// pixmapFromAvpic — C: pixmap_from_avpic
// (image_decoder_libav.c:237-338).
func pixmapFromAvpic(frame *C.AVFrame, pixFmt C.int, srcW, srcH,
	reqW, reqH int, meta *ImageMeta, g *gconf.T) *Pixmap {
	needFormatConv := false
	var fmt0 PixmapType

	switch C.enum_AVPixelFormat(pixFmt) {
	default:
		needFormatConv = true

	case C.AV_PIX_FMT_RGB24:
		if meta.CornerRadius != 0 {
			needFormatConv = true
		} else {
			fmt0 = PixmapRGB24
		}

	case C.AV_PIX_FMT_BGR32:
		fmt0 = PixmapBGR32

	case C.AV_PIX_FMT_Y400A:
		if !meta.CanMono {
			needFormatConv = true
			break
		}
		fmt0 = PixmapIA

	case C.AV_PIX_FMT_GRAY8:
		if !meta.CanMono {
			needFormatConv = true
			break
		}
		fmt0 = PixmapI

	case C.AV_PIX_FMT_PAL8:
		palette := (*[256]uint32)(unsafe.Pointer(frame.data[1]))
		for i := range 256 {
			if (palette[i] >> 24) == 0 {
				palette[i] = 0
			}
		}
		needFormatConv = true
	}

	wantRescale := reqW != srcW || reqH != srcH
	wantAlpha := int(meta.CornerRadius)

	if wantRescale || needFormatConv {
		pm := pixmapRescaleSwscale(frame, pixFmt, srcW, srcH,
			reqW, reqH, wantAlpha, int(meta.Margin), g)
		if pm != nil {
			return pm
		}

		if needFormatConv {
			pm = pixmapRescaleSwscale(frame, pixFmt, srcW, srcH,
				srcW, srcH, wantAlpha, int(meta.Margin), g)
			if pm != nil {
				return pm
			}
			return pixmap32bitSwizzle(frame, pixFmt, srcW, srcH,
				int(meta.Margin))
		}
	}

	pm := PixmapCreate(srcW, srcH, fmt0, int(meta.Margin))
	if pm == nil {
		return nil
	}

	dst := pmPixelOff(pm, 0, 0)
	srcData := (*[1 << 30]byte)(unsafe.Pointer(frame.data[0]))
	srcStride := int(frame.linesize[0])
	h := srcH

	if srcStride != pm.Stride {
		srcOff := 0
		for ; h > 0; h-- {
			copy(pm.Data[dst:dst+pm.Stride],
				srcData[srcOff:srcOff+pm.Stride])
			srcOff += srcStride
			dst += pm.Stride
		}
	} else {
		copy(pm.Data[dst:dst+pm.Stride*srcH],
			srcData[:pm.Stride*srcH])
	}
	return pm
}

// PixmapComputeRescaleDim — C: pixmap_compute_rescale_dim
// (image_decoder_libav.c:344-387). Non-static in C (also used by
// libjpeg path).
func PixmapComputeRescaleDim(meta *ImageMeta, srcWidth, srcHeight int) (int, int) {
	var w, h int
	if meta.WantThumb {
		w = 160
		h = 160 * srcHeight / srcWidth
	} else {
		w = srcWidth
		h = srcHeight
	}

	if meta.ReqWidth != -1 && meta.ReqHeight != -1 {
		w = meta.ReqWidth
		h = meta.ReqHeight
	} else if meta.ReqWidth != -1 {
		w = meta.ReqWidth
		h = meta.ReqWidth * srcHeight / srcWidth
	} else if meta.ReqHeight != -1 {
		w = meta.ReqHeight * srcWidth / srcHeight
		h = meta.ReqHeight
	}

	if w > 64 && h > 64 {
		if meta.MaxWidth != 0 && w > meta.MaxWidth {
			h = h * meta.MaxWidth / w
			w = meta.MaxWidth
		}
		if meta.MaxHeight != 0 && h > meta.MaxHeight {
			w = w * meta.MaxHeight / h
			h = meta.MaxHeight
		}
	}
	return w, h
}

// ImageDecodeLibAV — C: image_decode_libav
// (image_decoder_libav.c:393-488). avcodec_decode_video2 is mapped to
// the send_packet/receive_frame pair (API availability).
func ImageDecodeLibAV(cType CodedType, buf *miscbuf.Buf,
	meta *ImageMeta, g *gconf.T) (*Pixmap, error) {
	var codec *C.AVCodec

	data := buf.C8()

	switch cType {
	case CodedPNG:
		codec = C.avcodec_find_decoder(C.AV_CODEC_ID_PNG)

	case CodedJPEG:
		// C: jpeg_info(&ji, jpeginfo_mem_reader, &mi,
		//   JPEG_INFO_DIMENSIONS, buf_data, buf_size, errbuf, errlen)
		var ji JPEGInfo
		mi := &JpegMeminfo{Data: data}
		if r, jerr := JpegInfo(&ji, JpeginfoMemReader, mi,
			JPEGInfoDimensions, data); r != 0 {
			return nil, jerr
		}
		codec = C.avcodec_find_decoder(C.AV_CODEC_ID_MJPEG)

	case CodedGIF:
		codec = C.avcodec_find_decoder(C.AV_CODEC_ID_GIF)

	case CodedBMP:
		codec = C.avcodec_find_decoder(C.AV_CODEC_ID_BMP)

	case CodedWEBP:
		// Go extension — libavcodec's native WebP decoder (VP8+VP8L).
		// Upstream C (image_decoder_libav.c) has no WebP case.
		codec = C.avcodec_find_decoder(C.AV_CODEC_ID_WEBP)

	default:
		codec = nil
	}

	if codec == nil {
		return nil, errors.New("No codec for image format")
	}

	ctx := C.avcodec_alloc_context3(codec)

	if C.avcodec_open2(ctx, codec, nil) < 0 {
		C.av_free(unsafe.Pointer(ctx))
		return nil, errors.New("Unable to open codec")
	}

	frame := C.av_frame_alloc()

	// cgo: packet.data must not point into Go memory (cgoCheckPtrWrite).
	// Copy into a C-owned packet instead — C: av_init_packet + data ptr.
	packet := C.av_packet_alloc()
	if C.av_new_packet(packet, C.int(buf.Size())) == 0 && buf.Size() > 0 {
		C.memcpy(unsafe.Pointer(packet.data), buf.Data(), C.size_t(buf.Size()))
	}
	C.avcodec_send_packet(ctx, packet)
	r := C.avcodec_receive_frame(ctx, frame)
	C.av_packet_free(&packet)

	if r < 0 || ctx.width == 0 || ctx.height == 0 {
		err := fmt.Errorf("Unable to decode image of size (%d x %d)",
			int(ctx.width), int(ctx.height))
		C.avcodec_free_context(&ctx)
		C.av_frame_free(&frame)
		return nil, err
	}

	w, h := PixmapComputeRescaleDim(meta, int(ctx.width), int(ctx.height))

	pm := pixmapFromAvpic(frame, C.int(ctx.pix_fmt), int(ctx.width),
		int(ctx.height), w, h, meta, g)

	if pm != nil {
		pm.Aspect = float32(w) / float32(h)
	}
	C.av_frame_free(&frame)

	C.avcodec_free_context(&ctx)
	if pm == nil {
		return nil, errors.New("Out of memory")
	}
	return pm, nil
}
