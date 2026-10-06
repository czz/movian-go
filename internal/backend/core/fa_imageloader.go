// Canonical port of src/fileaccess/fa_imageloader.c — the file backend's
// be_imageloader: buffer/stream image probing (jpeg/png/gif/bmp/svg),
// EXIF-thumbnail extraction, and the libav video-thumbnail pipeline
// (ifv_* parked format context, 5%-seek frame grab, sws scale, MJPEG
// re-encode into blobcache).
//
// Build-flag notes (matching build.x86_64-linux-gnu config):
//
//	ENABLE_LIBAV                    — on
//	ENABLE_LIBJPEG                  — off (no early libjpeg_decode path)
//	ENABLE_LIBAV_ATTACHMENT_POINTER — off (attachment thumbs come from
//	                                  st->codec->extradata, i.e. codecpar
//	                                  extradata in FFmpeg 7)
package core

import (
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/czz/movian-go/internal/blobcache"
	"github.com/czz/movian-go/internal/callout"
	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	imagepkg "github.com/czz/movian-go/internal/image"
	"github.com/czz/movian-go/internal/libav"
	medialibav "github.com/czz/movian-go/internal/media/libav"
	"github.com/czz/movian-go/internal/trace"
)

// ---------------------------------------------------------------------------
// format signatures — C: pngsig/gif87sig/gif89sig/svgsig1/svgsig2
// ---------------------------------------------------------------------------

var (
	ilPngSig  = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	ilGif87   = []byte("GIF87a")
	ilGif89   = []byte("GIF89a")
	ilSvgSig1 = []byte("<?xml")
	ilSvgSig2 = []byte("<svg")
)

// imageLoader — C: fa_imageloader.c static globals
// ifv_url/ifv_fctx/ifv_ctx/ifv_stream, thumbcodec/thumbctx,
// image_from_video_mutex[2], thumb_flush_callout, and
// fa_image_from_video's static stated_url/fs. Owned by BackendSystem.
type imageLoader struct {
	mu        [2]sync.Mutex // image_from_video_mutex[0..1]
	url       string        // ifv_url
	fctx      *medialibav.AVFormatCtx
	fctxLibav *libav.AVFormatContext // ifv_fctx — kept for FALibavCloseFormat
	ctx       *libav.AVCodecContext  // ifv_ctx — materialized stream codec ctx
	stream    int                    // ifv_stream
	statedURL string                 // stated_url
	statedFS  fileaccesscore.FileStat
	statedOK  bool
	thumbCtx  *libav.AVCodecContext // thumbctx (MJPEG encoder ctx)
	thumbOK   bool                  // thumbcodec != NULL
	callout   callout.Callout
	cs        *callout.CalloutSystem // C: callout_* — fam-injected
	ts        *trace.TraceSystem     // C: trace.c file statics — fam-injected
}

func newImageLoader() *imageLoader { return &imageLoader{} }

// FAImageloaderStart — C: fa_imageloader_init (fa_imageloader.c:67-74).
// Called from fileaccess_init via fileaccesscore.FAImageloaderStartHook.
func (il *imageLoader) FAImageloaderStart(fam *fileaccesscore.FileAccessManager) {
	il.cs = fam.CalloutSystem()
	il.ts = fam.TraceSystem()
	// C: hts_mutex_init(&image_from_video_mutex[0/1]) — Go mutexes are
	// zero-value ready.
	il.thumbOK = medialibav.ThumbcodecAvailable()
}

// ---------------------------------------------------------------------------
// fa_imageloader_buf — C: fa_imageloader_buf (fa_imageloader.c:82-140)
// ---------------------------------------------------------------------------

// probeImageFormat inspects the first bytes and returns the coded type.
// C: the signature-matching block shared by fa_imageloader_buf and
// fa_imageloader's stream path.
func ilProbeFormat(p []byte) imagepkg.CodedType {
	switch {
	case len(p) >= 3 && p[0] == 0xff && p[1] == 0xd8 && p[2] == 0xff:
		return imagepkg.CodedJPEG
	case len(p) >= 8 && string(p[:8]) == string(ilPngSig):
		return imagepkg.CodedPNG
	case len(p) >= 6 && (string(p[:6]) == string(ilGif87) ||
		string(p[:6]) == string(ilGif89)):
		return imagepkg.CodedGIF
	case len(p) >= 2 && p[0] == 'B' && p[1] == 'M':
		return imagepkg.CodedBMP
	case len(p) >= 12 && string(p[:4]) == "RIFF" &&
		string(p[8:12]) == "WEBP":
		// Go extension — WebP RIFF container; upstream C probes no WebP.
		return imagepkg.CodedWEBP
	case len(p) >= 5 && (string(p[:5]) == string(ilSvgSig1) ||
		(len(p) >= 4 && string(p[:4]) == string(ilSvgSig2))):
		return imagepkg.CodedSVG
	}
	return imagepkg.CodedNone
}

// faImageloaderBuf — C: fa_imageloader_buf. Probes a fully-loaded buffer
// and returns a coded image with header metadata populated. The C errbuf
// text is returned as the error.
func faImageloaderBuf(buf []byte) (*imagepkg.Image, error) {
	var width, height, orientation, progressive, planes int
	width, height = -1, -1

	if len(buf) < 16 {
		return nil, errors.New("Unknown format")
	}

	var fmt0 imagepkg.CodedType
	if buf[0] == 0xff && buf[1] == 0xd8 && buf[2] == 0xff {
		// C: jpeg_info(&ji, jpeginfo_mem_reader, &mi,
		//   JPEG_INFO_DIMENSIONS | JPEG_INFO_ORIENTATION, ...)
		var ji imagepkg.JPEGInfo
		mi := &imagepkg.JpegMeminfo{Data: buf}
		if r, jerr := imagepkg.JpegInfo(&ji, imagepkg.JpeginfoMemReader, mi,
			imagepkg.JPEGInfoDimensions|imagepkg.JPEGInfoOrientation,
			buf); r != 0 {
			return nil, jerr
		}
		fmt0 = imagepkg.CodedJPEG
		width = ji.Width
		height = ji.Height
		orientation = ji.Orientation
		progressive = ji.Progressive
		planes = ji.Components
		imagepkg.JpegInfoClear(&ji)
	} else {
		fmt0 = ilProbeFormat(buf)
		if fmt0 == imagepkg.CodedNone {
			return nil, errors.New("Unknown format")
		}
	}

	// C: image_coded_create_from_buf(buf, fmt)
	img := imagepkg.CodedCreateFromData(buf, fmt0)
	if img == nil {
		return nil, errors.New("Out of memory")
	}
	img.Width = uint16(clampU16(width))
	img.Height = uint16(clampU16(height))
	img.Orientation = uint8(orientation)
	img.ColorPlanes = uint8(planes)
	if progressive != 0 {
		img.Flags |= imagepkg.FlagProgressive
	}
	return img, nil
}

// ---------------------------------------------------------------------------
// fa_imageloader2 — C: fa_imageloader2 (fa_imageloader.c:146-168)
// ---------------------------------------------------------------------------

// ilLoadResult mirrors C's tagged return: image, NOT_MODIFIED sentinel,
// or NO_LOAD_METHOD sentinel (fall through to the streaming path).
const (
	ilOK = iota
	ilNotModified
	ilNoLoadMethod
)

func (il *imageLoader) faImageloader2(fam *fileaccesscore.FileAccessManager, url string,
	cacheControl *int, c any) (*imagepkg.Image, int, error) {

	// C: fa_load(url, FA_LOAD_ERRBUF, FA_LOAD_CACHE_CONTROL,
	//   FA_LOAD_CANCELLABLE, FA_LOAD_FLAGS(FA_NON_INTERACTIVE|FA_CONTENT_ON_ERROR),
	//   FA_LOAD_NO_FALLBACK, NULL)
	buf, err := fileaccesscore.FALoad(fam, url, cacheControl, c,
		fileaccesscore.FaNonInteractive|fileaccesscore.FaContentOnError)
	if err != nil {
		switch err {
		case fileaccesscore.ErrLoadNotModified:
			return nil, ilNotModified, nil
		case fileaccesscore.ErrLoadNoMethod:
			return nil, ilNoLoadMethod, nil
		default:
			return nil, ilOK, err // NULL → return (image_t*)NULL
		}
	}
	if buf == nil {
		return nil, ilOK, nil
	}

	img, derr := faImageloaderBuf(buf.Data[:buf.Size])
	return img, ilOK, derr
}

// ---------------------------------------------------------------------------
// jpeginfo_reader — C: jpeginfo_reader (fa_imageloader.c:175-181)
// ---------------------------------------------------------------------------

// jpeginfoReader — C: jpeginfo_reader (fa_imageloader.c:173-181).
// fa_seek(fh, offset, SEEK_SET) + fa_read.
func jpeginfoReader(handle any, buf []byte, offset int64,
	size int) int {
	fh := handle.(*fileaccesscore.Handle)
	if pos, err := fh.Seek(offset, 0); err != nil || pos != offset {
		return -1
	}
	n, _ := fh.Read(buf[:size])
	return n
}

// ---------------------------------------------------------------------------
// fa_imageloader — C: fa_imageloader (fa_imageloader.c:187-327)
// ---------------------------------------------------------------------------

// FAImageloader is be_file's be_imageloader. Returns *imagepkg.Image,
// NotModifiedImage, or nil with error.
func (il *imageLoader) FAImageloader(fam *fileaccesscore.FileAccessManager, url string,
	im0 *imagepkg.ImageMeta, cacheControl *int,
	c any, be *Backend) (any, error) {

	im := im0
	if im == nil {
		im = &imagepkg.ImageMeta{}
	}

	// C: #if ENABLE_LIBAV — '#' selects video-thumbnail mode
	if strings.IndexByte(url, '#') >= 0 {
		if img, verr := il.faImageFromVideo(fam, url, im, cacheControl, c); img != nil {
			return img, nil
		} else if verr != nil {
			return nil, verr
		}
		return nil, nil
	}

	if !im.WantThumb {
		img, how, lerr := il.faImageloader2(fam, url, cacheControl, c)
		if how != ilNoLoadMethod {
			if how == ilNotModified {
				return NotModifiedImage, nil
			}
			if img == nil {
				return nil, lerr
			}
			return img, nil
		}
		if lerr != nil {
			// C: errbuf set, then fall through to full open path
			_ = lerr
		}
	}

	// C: ONLY_CACHED(cache_control) — non-NULL and != BYPASS_CACHE
	if cacheControl != nil && *cacheControl != -1 {
		return nil, errors.New("Not cached")
	}

	// C: fa_open_resolver(url, ..., FA_BUFFERED_SMALL | FA_NON_INTERACTIVE,
	//   foe{foe_cancellable = c})
	foe := &fileaccesscore.OpenExtra{Cancellable: c}
	fh, err := fileaccesscore.FAOpenResolver(fam, url,
		fileaccesscore.FaBufferedSmall|fileaccesscore.FaNonInteractive, foe)
	if err != nil {
		return nil, err
	}

	p := make([]byte, 16)
	n, rerr := fh.Read(p)
	if rerr != nil || n != len(p) {
		fileaccesscore.FAClose(fh)
		return nil, errors.New("File too short")
	}

	var fmt0 imagepkg.CodedType
	width, height, orientation := -1, -1, 0

	if p[0] == 0xff && p[1] == 0xd8 && p[2] == 0xff {
		// C: jpeg_info(&ji, jpeginfo_reader, fh, DIMENSIONS|ORIENTATION|
		//   (im->im_want_thumb ? JPEG_INFO_THUMBNAIL : 0), p, 16, ...)
		var ji imagepkg.JPEGInfo
		jiflags := imagepkg.JPEGInfoDimensions |
			imagepkg.JPEGInfoOrientation
		if im.WantThumb {
			jiflags |= imagepkg.JPEGInfoThumbnail
		}
		if r, jerr := imagepkg.JpegInfo(&ji, jpeginfoReader, fh, jiflags,
			p[:]); r != 0 {
			fileaccesscore.FAClose(fh)
			return nil, jerr
		}

		if im.WantThumb && ji.Thumbnail != nil {
			// C: image_retain(ji.ji_thumbnail); im->im_flags |=
			//   IMAGE_ADAPTED
			thumb := ji.Thumbnail.Retain()
			fileaccesscore.FAClose(fh)
			imagepkg.JpegInfoClear(&ji)
			thumb.Flags |= imagepkg.FlagAdapted
			return thumb, nil
		}

		// ENABLE_LIBJPEG is off in this build — the early
		// libjpeg_decode path is not compiled (fa_imageloader.c:251).

		fmt0 = imagepkg.CodedJPEG
		width = ji.Width
		height = ji.Height
		orientation = ji.Orientation

		imagepkg.JpegInfoClear(&ji)
	} else {
		fmt0 = ilProbeFormat(p)
		if fmt0 == imagepkg.CodedNone {
			fileaccesscore.FAClose(fh)
			return nil, errors.New("Unknown format")
		}
	}

	// C: fa_fsize + image_coded_alloc + full read
	s, err := fileaccesscore.FSize(fh)
	if err != nil || s < 0 {
		fileaccesscore.FAClose(fh)
		return nil, errors.New("Can't read from non-seekable file")
	}

	// C: image_coded_alloc(&ptr, s, fmt)
	img, dst := imagepkg.CodedAlloc(int(s), fmt0)
	if img == nil {
		fileaccesscore.FAClose(fh)
		return nil, errors.New("Out of memory")
	}

	img.Width = uint16(clampU16(width))
	img.Height = uint16(clampU16(height))
	img.Orientation = uint8(orientation)

	fileaccesscore.FASeek(fh, 0, 0) // C: fa_seek(fh, SEEK_SET, 0)
	n, rerr = fh.Read(dst)
	fileaccesscore.FAClose(fh)

	if rerr != nil || int64(n) != s {
		img.Release() // C: image_release(img)
		return nil, errors.New("Read error")
	}
	return img, nil
}

// ---------------------------------------------------------------------------
// ifv_* — parked video format context for thumbnail sources
// (C: fa_imageloader.c:331-358)
// ---------------------------------------------------------------------------

// ifvClose — C: ifv_close (fa_imageloader.c:332-346)
func (il *imageLoader) ifvClose() {
	il.url = ""

	if il.ctx != nil {
		medialibav.CctxClose(il.ctx) // C: avcodec_close(ifv_ctx)
		il.ctx = nil
	}
	if il.fctxLibav != nil {
		libavSys := libav.GetGlobalLibAVSystem()
		libav.FALibavCloseFormat(libavSys, il.fctxLibav, false)
		il.fctxLibav = nil
		il.fctx = nil
	}
}

// ifvAutoclose — C: ifv_autoclose (fa_imageloader.c:352-361).
// If the thumb pipeline is busy, re-arm in 5s; else close the parked
// format context.
func (il *imageLoader) ifvAutoclose(c *callout.Callout, aux any) {
	if !il.mu[1].TryLock() {
		if cs := il.cs; cs != nil {
			cs.Arm(&il.callout, il.ifvAutoclose, nil, 5)
		}
	} else {
		il.ts.Trace(trace.TRACE_DEBUG, "Thumb",
			"Closing movie for thumb sources")
		il.ifvClose()
		il.mu[1].Unlock()
	}
}

// armThumbFlush — C: callout_arm(&thumb_flush_callout, ifv_autoclose,0,5)
func (il *imageLoader) armThumbFlush() {
	if cs := il.cs; cs != nil {
		cs.Arm(&il.callout, il.ifvAutoclose, nil, 5)
	}
}

// ---------------------------------------------------------------------------
// write_thumb — C: write_thumb (fa_imageloader.c:367-424)
// ---------------------------------------------------------------------------

// writeThumb re-encodes the grabbed frame to MJPEG at the requested size
// and stores it in the "videothumb" blobcache stash.
func (il *imageLoader) writeThumb(bc *blobcache.BlobCache, srcW, srcH, srcFmt int,
	sframe *libav.AVFrame, width, height int, cacheid string,
	mtime time.Time, ts *trace.TraceSystem) {

	if !il.thumbOK {
		return // C: if(thumbcodec == NULL) return
	}

	ctx := il.thumbCtx
	// C: if(ctx == NULL || ctx->width != width || ctx->height != height)
	if ctx == nil || medialibav.CctxWidth(ctx) != width ||
		medialibav.CctxHeight(ctx) != height {
		if ctx != nil {
			medialibav.CctxClose(ctx)
		}
		ctx = medialibav.ThumbctxAlloc()
		if ctx == nil {
			il.thumbCtx = nil
			return
		}
		medialibav.ThumbctxSetParams(ctx, width, height)
		if medialibav.ThumbctxOpen(ctx) < 0 {
			ts.Trace(trace.TRACE_ERROR, "THUMB",
				"Unable to open thumb encoder")
			medialibav.CctxClose(ctx)
			il.thumbCtx = nil
			return
		}
		il.thumbCtx = ctx
	}

	// C: av_frame_alloc + avpicture_alloc(YUVJ420P)
	oframe := libav.AvFrameAlloc()
	if oframe == nil {
		return
	}
	defer libav.AvFrameFree(oframe)

	if medialibav.FrameAllocImage(oframe, width, height,
		medialibav.PixFmtYUVJ420P()) < 0 {
		return
	}

	sws := medialibav.SwsGet(srcW, srcH, srcFmt,
		width, height, medialibav.PixFmtYUVJ420P())
	if sws != nil {
		medialibav.SwsScaleFrame(sws, sframe, oframe, srcH)
		medialibav.SwsFree(sws)
	}

	medialibav.FrameSetNoPTS(oframe)

	// C: avcodec_encode_video2(ctx, &out, oframe, &got_packet)
	if data, got := medialibav.ThumbctxEncode(ctx, oframe); got {
		if bc != nil {
			bc.Put(cacheid, "videothumb",
				&blobcache.Buf{Ptr: data, Size: len(data)},
				int(^uint32(0)>>1), "", mtime, 0) // INT32_MAX
		}
	}
}

// ---------------------------------------------------------------------------
// thumb_from_buf / thumb_from_attachment
// (C: fa_imageloader.c:429-462)
// ---------------------------------------------------------------------------

// thumbFromBuf — C: thumb_from_buf: decode-probe the buffer and cache it.
func (il *imageLoader) thumbFromBuf(bc *blobcache.BlobCache, buf []byte, cacheid string,
	mtime time.Time) (*imagepkg.Image, error) {
	img, err := faImageloaderBuf(buf)
	if img != nil {
		if bc != nil {
			bc.Put(cacheid, "videothumb",
				&blobcache.Buf{Ptr: buf, Size: len(buf)},
				int(^uint32(0)>>1), "", mtime, 0)
		}
	}
	return img, err
}

// thumbFromAttachment — C: thumb_from_attachment (attribute_unused under
// !ENABLE_LIBAV_ATTACHMENT_POINTER, ported for completeness).
func (il *imageLoader) thumbFromAttachment(fam *fileaccesscore.FileAccessManager,
	url string, offset, size int64, cacheid string,
	mtime time.Time) (*imagepkg.Image, error) {

	fh, err := fileaccesscore.FAOpenEx(fam, url,
		fileaccesscore.FaNonInteractive, nil)
	if err != nil {
		return nil, err
	}
	fh = fileaccesscore.FASliceOpen(fh, offset, size)
	buf := fileaccesscore.LoadAndClose(fh)
	if buf == nil {
		return nil, errors.New("Load error")
	}
	return il.thumbFromBuf(fam.BlobCache(), buf.Data[:buf.Size], cacheid, mtime)
}

// ---------------------------------------------------------------------------
// fa_image_from_video2 — C: fa_imageloader.c:471-736
// ---------------------------------------------------------------------------

const maxFrameScan = 500 // C: MAX_FRAME_SCAN

func (il *imageLoader) faImageFromVideo2(fam *fileaccesscore.FileAccessManager, url string,
	im *imagepkg.ImageMeta, cacheid string, sec int,
	mtime time.Time, c any) (*imagepkg.Image, error) {

	var img *imagepkg.Image
	var err error
	libavSys := libav.GetGlobalLibAVSystem()

	if il.url == "" || il.url != url {
		// Need to open
		fh, err := fileaccesscore.FAOpenEx(fam, url,
			fileaccesscore.FaBufferedBig|fileaccesscore.FaNonInteractive,
			nil)
		if err != nil {
			return nil, err
		}

		strategy := libav.FALibavGetStrategyForFile(fh)
		avio, err := libav.FALibavReopen(libavSys, fh, false)
		if err != nil || avio == nil {
			return nil, errors.New("Unable to open avio")
		}

		fctxLibav, err := libav.FALibavOpenFormat(avio, url,
			"", strategy)
		if fctxLibav == nil {
			libav.FALibavClose(libavSys, avio)
			return nil, errors.New("Unable to open format")
		}
		fctx := medialibav.WrapFormatCtx(fctxLibav)

		// C: if(!strcmp(fctx->iformat->name, "avi"))
		//    fctx->flags |= AVFMT_FLAG_GENPTS
		if fctx.GetFormatName() == "avi" {
			fctx.SetGenPTS()
		}

		var ctx *libav.AVCodecContext
		vstream := 0
		nb := fctx.GetNumStreams()
		for i := range nb {
			st := fctx.GetStreamInfo(i)
			if st == nil {
				continue
			}
			switch st.CodecType {
			case medialibav.AVMediaTypeVideo:
				if ctx == nil {
					vstream = i
					// C: ctx = fctx->streams[i]->codec — FFmpeg 7:
					// materialized codec ctx (caller-owned)
					ctx = fctx.CodecCtxFromStream(i)
				}
			case medialibav.AVMediaTypeAttachment():
				// C: mt = av_dict_get(st->metadata, "mimetype", ...)
				mt := fctx.GetStreamMetadata(i, "mimetype")
				if sec == -1 && (mt == "image/jpeg" || mt == "image/png") {
					// ENABLE_LIBAV_ATTACHMENT_POINTER is off — C's
					// #else branch adopts st->codec->extradata.
					ed := fctx.GetStreamExtradata(i)
					libav.FALibavCloseFormat(libavSys, fctxLibav, false)
					if ctx != nil {
						medialibav.CctxClose(ctx)
					}
					return il.thumbFromBuf(fam.BlobCache(), ed, cacheid, mtime)
				}
			}
		}
		if ctx == nil {
			libav.FALibavCloseFormat(libavSys, fctxLibav, false)
			return nil, nil
		}

		// C: codec = avcodec_find_decoder(ctx->codec_id);
		//    avcodec_open2(ctx, codec, NULL)
		if medialibav.CctxOpenDecoder(ctx) < 0 {
			medialibav.CctxClose(ctx)
			libav.FALibavCloseFormat(libavSys, fctxLibav, false)
			return nil, errors.New("Unable to open codec")
		}

		il.ifvClose()

		il.stream = vstream
		il.url = url
		il.fctx = fctx
		il.fctxLibav = fctxLibav
		il.ctx = ctx
	}

	frame := libav.AvFrameAlloc()
	if frame == nil {
		return nil, nil
	}
	defer libav.AvFrameFree(frame)

	cnt := maxFrameScan

	st := il.fctx.GetStreamInfo(il.stream)
	if st == nil {
		return nil, nil
	}

	if sec == -1 {
		// Automatically try to find a good frame
		durationSeconds := int(il.fctx.GetDuration() / 1000000)

		sec = max(1, int(float64(durationSeconds)*0.05)) // 5% of duration
		sec = min(sec, 150)                              // but no longer than 2:30 in
		sec = max(0, min(sec, durationSeconds-1))
		cnt = 1
	}

	// C: ts = av_rescale(sec, st->time_base.den, st->time_base.num)
	ts := medialibav.AvRescale(int64(sec), int64(st.TimeBaseDen),
		int64(st.TimeBaseNum))

	delayedSeek := false
	codecID := medialibav.CctxCodecID(il.ctx)
	if codecID == medialibav.CodecIDRV40() ||
		codecID == medialibav.CodecIDRV30() {
		// Must decode one frame
		delayedSeek = true
	} else {
		if il.fctx.SeekFrameStream(il.stream, ts) != nil {
			il.ifvClose()
			return nil, errors.New("Unable to seek to " +
				strconv.FormatInt(ts, 10))
		}
	}

	medialibav.CctxFlush(il.ctx) // C: avcodec_flush_buffers

	i := 0
	for {
		i++

		pkt, rerr := il.fctx.ReadPacketRaw()
		if rerr != nil {
			// C: r == AVERROR(EAGAIN) → continue (ReadPacketRaw
			// already retries EAGAIN internally); other errors →
			// ifv_close + break.
			il.ifvClose()
			break
		}
		if pkt == nil {
			break // C: r == AVERROR_EOF → break
		}

		if isCancelled(c) {
			err = errors.New("Cancelled")
			libav.AvPacketFree(pkt)
			break
		}

		if medialibav.PacketStreamIndex(pkt) != il.stream {
			libav.AvPacketFree(pkt)
			continue
		}
		cnt--
		wantPic := medialibav.PacketPTS(pkt) >= ts || cnt <= 0

		// C: ifv_ctx->skip_frame = want_pic ? DEFAULT : NONREF
		if wantPic {
			medialibav.CctxSetSkipFrame(il.ctx,
				medialibav.AVDiscardDefault())
		} else {
			medialibav.CctxSetSkipFrame(il.ctx,
				medialibav.AVDiscardNonRef())
		}

		gotPic := medialibav.CctxDecodeVideo(il.ctx, pkt, frame)
		libav.AvPacketFree(pkt)

		if delayedSeek {
			delayedSeek = false
			if il.fctx.SeekFrameStream(il.stream, ts) != nil {
				il.ifvClose()
				break
			}
			continue
		}

		// libav struggles seeking AVC Baseline@L4.0 — bound the loop.
		if i >= 100 {
			fam.TraceSystem().Trace(trace.TRACE_DEBUG, "Thumb",
				"Couldn't generate thumbnail for %s", url)
			break
		}

		if gotPic == 0 || !wantPic {
			continue
		}

		var w, h int
		ctxW := medialibav.CctxWidth(il.ctx)
		ctxH := medialibav.CctxHeight(il.ctx)
		switch {
		case im.ReqWidth != -1 && im.ReqHeight != -1:
			w, h = im.ReqWidth, im.ReqHeight
		case im.ReqWidth != -1:
			w = im.ReqWidth
			h = im.ReqWidth * ctxH / ctxW
		case im.ReqHeight != -1:
			w = im.ReqHeight * ctxW / ctxH
			h = im.ReqHeight
		default:
			w, h = im.ReqWidth, im.ReqHeight
		}

		// C: pixmap_create(w, h, PIXMAP_BGR32, 0). PIXMAP_BGR32 ==
		// AV_PIX_FMT_BGR32 (== AV_PIX_FMT_RGBA on LE).
		pm := imagepkg.PixmapCreate(w, h, imagepkg.PixmapBGR32, 0)
		if pm == nil {
			il.ifvClose()
			return nil, errors.New("Out of memory")
		}

		sws := medialibav.SwsGet(ctxW, ctxH,
			medialibav.CctxPixFmt(il.ctx),
			w, h, medialibav.PixFmtRGBA())
		if sws == nil {
			il.ifvClose()
			imagepkg.PixmapRelease(pm)
			return nil, errors.New("Scaling failed")
		}

		medialibav.SwsScaleToBuf(sws, frame, ctxH, &pm.Data[0],
			pm.Stride)
		medialibav.SwsFree(sws)

		il.writeThumb(fam.BlobCache(), ctxW, ctxH,
			medialibav.CctxPixFmt(il.ctx),
			frame, w, h, cacheid, mtime, fam.TraceSystem())

		img = imagepkg.CreateFromPixmap(pm)
		imagepkg.PixmapRelease(pm)
		break
	}

	if img == nil && err == nil {
		err = errors.New("Frame not found (scanned " +
			strconv.Itoa(maxFrameScan-cnt) + ")")
	}

	if il.ctx != nil {
		medialibav.CctxFlush(il.ctx)
		il.armThumbFlush()
	}
	return img, err
}

// ---------------------------------------------------------------------------
// fa_image_from_video — C: fa_imageloader.c:745-804
// ---------------------------------------------------------------------------

func (il *imageLoader) faImageFromVideo(fam *fileaccesscore.FileAccessManager, url0 string,
	im *imagepkg.ImageMeta, cacheControl *int,
	c any) (*imagepkg.Image, error) {

	// C: url = mystrdupa(url0); tim = strchr(url, '#'); *tim++ = 0
	url := url0
	tim := ""
	if before, after, ok := strings.Cut(url0, "#"); ok {
		url = before
		tim = after
	}

	var secs int
	if tim == "cover" {
		secs = -1
	} else {
		secs, _ = strconv.Atoi(tim)
	}

	il.mu[0].Lock()
	if url != il.statedURL {
		il.statedOK = false
		fs, err := fileaccesscore.StatEx(fam, url,
			fileaccesscore.FaNonInteractive)
		if err != nil {
			il.mu[0].Unlock()
			return nil, err
		}
		il.statedFS = *fs
		il.statedURL = url
		il.statedOK = true
	}
	stattime := il.statedFS.MTime
	il.mu[0].Unlock()

	var siz string
	switch {
	case im.ReqWidth < 100 && im.ReqHeight < 100:
		siz = "min"
	case im.ReqWidth < 200 && im.ReqHeight < 200:
		siz = "mid"
	default:
		siz = "max"
	}

	cacheid := url0 + "-" + siz
	if bc := fam.BlobCache(); bc != nil {
		var mtime time.Time
		if b := bc.Get(cacheid, "videothumb", 0, nil, nil, &mtime); b != nil {
			// C: mtime == stattime — time_t (second) resolution
			if mtime.Unix() == stattime.Unix() {
				img := imagepkg.CodedCreateFromData(b.Ptr[:b.Size],
					imagepkg.CodedJPEG)
				return img, nil
			}
		}
	}

	// C: ONLY_CACHED(cache_control)
	if cacheControl != nil && *cacheControl != -1 {
		return nil, errors.New("Not cached")
	}

	il.mu[1].Lock()
	img, verr := il.faImageFromVideo2(fam, url, im, cacheid, secs,
		stattime, c)
	il.mu[1].Unlock()

	if img != nil {
		img.Flags |= imagepkg.FlagAdapted // C: im_flags |= IMAGE_ADAPTED
	}
	return img, verr
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func clampU16(v int) int {
	return min(max(v, 0), 0xffff)
}
