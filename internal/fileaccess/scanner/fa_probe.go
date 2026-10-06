// Canonical port of the fa_probe.c pieces used by the fileaccess video
// backend: fa_probe_iso, fa_probe_dir, fa_metadata_from_fctx
// (fa_lavf_load_meta). Lives in pkg/fileaccess/scanner because it needs
// both fileaccess/core (handles) and metadata (metadata_t).

package scanner

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/htsmsg"
	imagepkg "github.com/czz/movian-go/internal/image"
	"github.com/czz/movian-go/internal/libav"
	medialibav "github.com/czz/movian-go/internal/media/libav"
	"github.com/czz/movian-go/internal/metadata"
	"github.com/czz/movian-go/internal/misc"
)

// isoSig — C: isosig (fa_probe.c:89) — ISO9660 "CD001" magic.
var isoSig = []byte{0x01, 0x43, 0x44, 0x30, 0x30, 0x31, 0x01, 0x00}

// faProbeIso0 — C: fa_probe_iso0 (fa_probe.c:387) — check the 128-byte
// block at 0x8000 for the ISO9660 signature; on match, fill md title and
// contenttype=DVD.
func faProbeIso0(md *metadata.Metadata, pb []byte) int {
	for i := range 8 {
		if pb[i] != isoSig[i] {
			return -1
		}
	}
	// p = &pb[40]; while(*p > 32 && p != &pb[72]) p++; *p = 0;
	p := 40
	for p != 72 && pb[p] > 32 {
		p++
	}
	if md != nil {
		md.Title = string(pb[40:p])
		md.ContentType = metadata.ContentDVD
	}
	return 0
}

// FAProbeIso — C: fa_probe_iso (fa_probe.c:414) — seek to 0x8000, read
// 128 bytes, check for ISO9660. Returns 0 when the file is an ISO image.
func FAProbeIso(md *metadata.Metadata, fh *fileaccesscore.Handle) int {
	// C: if(fa_seek_lazy(fh, 0x8000, SEEK_SET) != 0x8000) return -1;
	pos, err := fileaccesscore.Seek4(fh, 0x8000, 0 /* SEEK_SET */, true)
	if err != nil || pos != 0x8000 {
		return -1
	}
	pb := make([]byte, 128)
	n, _ := fileaccesscore.FARead(fh, pb)
	if n != 128 {
		return -1
	}
	return faProbeIso0(md, pb)
}

// FAProbeDir — C: fa_probe_dir (fa_probe.c:634) — a directory containing
// VIDEO_TS is a DVD.
func FAProbeDir(fam *fileaccesscore.FileAccessManager, url string) *metadata.Metadata {
	md := metadata.Create()
	md.ContentType = metadata.ContentDir

	for _, dir := range []string{"VIDEO_TS", "video_ts"} {
		fs, err := fileaccesscore.Stat(fam, fileaccesscore.FAPathJoin(url, dir))
		if err == nil && fs != nil && fs.Type == fileaccesscore.ContentDir {
			md.ContentType = metadata.ContentDVD
			return md
		}
	}
	return md
}

// FAMetadataFromFctx — C: fa_metadata_from_fctx (fa_probe.c:621).
func FAMetadataFromFctx(fctx *medialibav.AVFormatCtx) *metadata.Metadata {
	md := metadata.Create()
	faLavfLoadMeta(md, fctx, "")
	return md
}

// faLavfLoadMeta — C: fa_lavf_load_meta (fa_probe.c:430-538). Fills md
// from the format context: artist/album/format/duration on the fast
// path, then a per-stream scan that builds md->md_streams and derives
// the content type.
func faLavfLoadMeta(md *metadata.Metadata, fctx *medialibav.AVFormatCtx,
	filename string) {
	hasVideo := false
	hasAudio := false

	// C: md->md_artist = libav_metadata_rstr(artist) ?: (author)
	md.Artist = libavMetadataRstr(fctx, "artist")
	if md.Artist == "" {
		md.Artist = libavMetadataRstr(fctx, "author")
	}
	md.Album = libavMetadataRstr(fctx, "album")
	md.Format = fctx.GetFormatLongName()
	if d := fctx.GetDuration(); d != libav.AVNoPTSValue {
		md.Duration = float32(d) / 1000000
	}

	n := fctx.GetNumStreams()
	for i := range n {
		si := fctx.GetStreamInfo(i)
		if si == nil {
			continue
		}
		if si.CodecType == medialibav.AVMediaTypeAudio {
			hasAudio = true
		}
		// C: !(stream->disposition & AV_DISPOSITION_ATTACHED_PIC)
		if si.CodecType == medialibav.AVMediaTypeVideo &&
			fctx.GetStreamDisposition(i)&(1<<10) == 0 {
			hasVideo = true
		}
	}

	if hasAudio && !hasVideo {
		// C: CONTENT_AUDIO early return (fa_probe.c:463-475)
		md.ContentType = metadata.ContentAudio
		md.Title = libavMetadataRstr(fctx, "title")
		// C: libav_metadata_int(fctx->metadata, "track", filename?atoi:0)
		def := int64(0)
		if filename != "" {
			if v, err := strconv.Atoi(filename); err == nil {
				def = int64(v)
			}
		}
		md.Track = int16(fctx.GetFormatMetadataInt("track", def))
		return
	}

	hasAudio = false
	hasVideo = false

	atrack, strack, vtrack := 0, 0, 0
	for i := range n {
		si := fctx.GetStreamInfo(i)
		if si == nil {
			continue
		}
		codec := medialibav.HasDecoder(si.CodecID)
		var tn int

		switch si.CodecType {
		case medialibav.AVMediaTypeVideo:
			hasVideo = codec
			vtrack++
			tn = vtrack
		case medialibav.AVMediaTypeAudio:
			hasAudio = codec
			atrack++
			tn = atrack
		case medialibav.AVMediaTypeSubtitle:
			strack++
			tn = strack
		default:
			continue
		}

		// C: codec ? metadata_from_libav(tmp1, codec, avctx) : codecname(id)
		var info string
		if !codec {
			info = medialibav.CodecName(si.CodecID)
		} else {
			// C: metadata_from_libav(tmp1, sizeof(tmp1), codec, avctx)
			// — needs a materialized AVCodecContext (stream->codec).
			ctx := fctx.CodecCtxFromStream(i)
			info = medialibav.MetadataFromCodec(ctx)
			medialibav.FreeCodecCtx(ctx)
		}

		lang := fctx.GetStreamMetadata(i, "language")
		title := fctx.GetStreamMetadata(i, "title")

		// C: metadata_add_stream(md, codecname(avctx->codec_id),
		//   avctx->codec_type, i, title, tmp1, lang,
		//   stream->disposition, tn, avctx->channels)
		metadata.MetadataAddStream(md, medialibav.CodecName(si.CodecID),
			si.CodecType, i, title, info, lang,
			fctx.GetStreamDisposition(i), tn, si.Channels)
	}

	md.ContentType = metadata.ContentFile
	if hasVideo {
		md.ContentType = metadata.ContentVideo
	} else if hasAudio {
		md.ContentType = metadata.ContentAudio
	}
}

// FAProbeISOIsDVD — convenience: returns true when fh probes as ISO.
func FAProbeISOIsDVD(fh *fileaccesscore.Handle) bool {
	return FAProbeIso(nil, fh) == 0
}

// IsHLSPlaylist — C: the #EXTM3U detection in be_file_playvideo
// (fa_video.c:617-642).
func IsHLSPlaylist(buf []byte) bool {
	s := string(buf)
	return strings.HasPrefix(s, "#EXTM3U") &&
		(strings.Contains(s, "#EXT-X-STREAM-INF:") ||
			strings.Contains(s, "#EXTINF"))
}

// ---------------------------------------------------------------------------
// C: fa_probe.c header/metadata probe path (fa_probe_metadata and helpers)
// ---------------------------------------------------------------------------

// Header signatures — C: pngsig, gifsig, ttfsig, otfsig, pdfsig, offsig
// (fa_probe.c:87-93).
var (
	pngSig = []byte{137, 80, 78, 71, 13, 10, 26, 10}
	gifSig = []byte{'G', 'I', 'F', '8', '9', 'a'}
	ttfSig = []byte{0, 1, 0, 0, 0}
	otfSig = []byte{'O', 'T', 'T', 'O'}
	pdfSig = []byte{'%', 'P', 'D', 'F', '-'}
	offSig = []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}
)

// metdataSetRedirect — C: metdata_set_redirect (fa_probe.c:178).
func metdataSetRedirect(md *metadata.Metadata, format string, args ...any) {
	md.Redirect = fmt.Sprintf(format, args...)
}

// libavMetadataRstr — C: libav_metadata_rstr (fa_probe.c:100-123).
// utf8_verify → copy → strip trailing chars <= ' ' and '-' → reject empty
// and "http://" values.
func libavMetadataRstr(fctx *medialibav.AVFormatCtx, key string) string {
	v := fctx.GetFormatMetadataValue(key)
	if !utf8.ValidString(v) {
		return ""
	}
	b := []byte(v)
	for len(b) > 0 && (b[len(b)-1] <= ' ' || b[len(b)-1] == '-') {
		b = b[:len(b)-1]
	}
	s := string(b)
	if s == "" || len(s) >= 7 && strings.EqualFold(s[:7], "http://") {
		return ""
	}
	return s
}

// jpeginfoReader — C: jpeginfo_reader (fa_probe.c:200-207).
// fa_seek(handle, offset, SEEK_SET) + fa_read.
func jpeginfoReader(handle any, buf []byte, offset int64,
	size int) int {
	fh := handle.(*fileaccesscore.Handle)
	if pos, err := fh.Seek(offset, 0); err != nil || pos != offset {
		return -1
	}
	n, _ := fileaccesscore.Read(fh, buf[:size])
	return n
}

// faProbeExif — C: fa_probe_exif (fa_probe.c:210-224).
func faProbeExif(md *metadata.Metadata, fh *fileaccesscore.Handle,
	pb []byte, buflen int) {
	var ji imagepkg.JPEGInfo
	if r, _ := imagepkg.JpegInfo(&ji, jpeginfoReader, fh,
		imagepkg.JPEGInfoDimensions|imagepkg.JPEGInfoOrientation|
			imagepkg.JPEGInfoMetadata,
		pb[:buflen]); r != 0 {
		return
	}
	md.Time = time.Unix(ji.Time, 0)
	md.Manufacturer = ji.Manufacturer
	md.Equipment = ji.Equipment
	imagepkg.JpegInfoClear(&ji)
}

// faProbeHeader — C: fa_probe_header (fa_probe.c:230-384). Returns 1 when
// the header identified the content.
func faProbeHeader(fam *fileaccesscore.FileAccessManager, md *metadata.Metadata, url string,
	fh *fileaccesscore.Handle, filename string, buf []byte, l int) int {

	if l >= 256 && bytes.Equal(buf[:11], []byte("d8:announce")) {
		md.ContentType = metadata.ContentArchive
		metdataSetRedirect(md, "torrentfile://%s/", url)
		return 1
	}

	if l >= 256 && bytes.Equal(buf[:17], []byte("d13:announce-list")) {
		md.ContentType = metadata.ContentArchive
		metdataSetRedirect(md, "torrentfile://%s/", url)
		return 1
	}

	if fileaccesscore.FABrowseArchives(fam.Gconf()) && l >= 16 &&
		buf[0] == 'R' && buf[1] == 'a' && buf[2] == 'r' && buf[3] == '!' &&
		buf[4] == 0x1a && buf[5] == 0x07 && buf[6] == 0x0 && buf[9] == 0x73 {

		flags := uint16(buf[10]) | uint16(buf[11])<<8
		if flags&0x101 == 1 {
			// Don't include slave volumes
			md.ContentType = metadata.ContentUnknown
			return 1
		}
		metdataSetRedirect(md, "rar://%s", url)
		md.ContentType = metadata.ContentArchive
		return 1
	}

	if fileaccesscore.FABrowseArchives(fam.Gconf()) && l > 4 &&
		buf[0] == 0x50 && buf[1] == 0x4b && buf[2] == 0x03 && buf[3] == 0x04 {

		b, _ := fileaccesscore.FALoad(fam,
			fmt.Sprintf("zip://%s/plugin.json", url), nil, nil, 0)
		if b != nil && len(b.Data) > 0 {
			json, err := htsmsg.DeserializeJSON(string(b.Data))
			if err == nil && json != nil {
				title := json.GetStr("title")
				if title != "" && json.GetStr("id") != "" &&
					json.GetStr("type") != "" {
					md.Title = title
					md.ContentType = metadata.ContentPlugin
					return 1
				}
			}
		}
		metdataSetRedirect(md, "zip://%s", url)
		md.ContentType = metadata.ContentArchive
		return 1
	}

	// C: fa_probe_playlist is inside #if 0 (dead code) — not ported.

	if l > 16 && buf[0] == 0xff && buf[1] == 0xd8 && buf[2] == 0xff {
		// JPEG image
		md.ContentType = metadata.ContentImage
		faProbeExif(md, fh, buf, l) // Try to get more info
		return 1
	}

	if l >= 8 && bytes.Equal(buf[:8], pngSig) {
		// PNG
		md.ContentType = metadata.ContentImage
		return 1
	}

	if l >= len(pdfSig) && bytes.Equal(buf[:len(pdfSig)], pdfSig) {
		// PDF
		md.ContentType = metadata.ContentDocument
		return 1
	}

	if l >= len(offSig) && bytes.Equal(buf[:len(offSig)], offSig) {
		// MS OFFICE
		md.ContentType = metadata.ContentDocument
		return 1
	}

	if l >= 6 && buf[0] == 'B' && buf[1] == 'M' {
		// BMP
		siz := int64(buf[2]) | int64(buf[3])<<8 | int64(buf[4])<<16 |
			int64(buf[5])<<24
		if fsiz, err := fileaccesscore.FSize(fh); err == nil && siz == fsiz {
			md.ContentType = metadata.ContentImage
			return 1
		}
	}

	if l >= len(gifSig) && bytes.Equal(buf[:len(gifSig)], gifSig) {
		// GIF
		md.ContentType = metadata.ContentImage
		return 1
	}

	if l >= 12 && bytes.Equal(buf[:4], []byte("RIFF")) &&
		bytes.Equal(buf[8:12], []byte("WEBP")) {
		// WebP — Go extension (upstream C has no WebP probe).
		md.ContentType = metadata.ContentImage
		return 1
	}

	if l >= 5 && bytes.Equal(buf[:5], []byte("<?xml")) &&
		misc.FindStr(buf, l, "<svg") >= 0 {
		// SVG
		md.ContentType = metadata.ContentImage
		return 1
	}

	if l >= 4 && buf[0] == '%' && buf[1] == 'P' && buf[2] == 'D' && buf[3] == 'F' {
		md.ContentType = metadata.ContentUnknown
		return 1
	}

	if (l >= 5 && bytes.Equal(buf[:5], ttfSig)) ||
		(l >= 4 && bytes.Equal(buf[:4], otfSig)) {
		// TTF or OTF
		md.ContentType = metadata.ContentFont
		return 1
	}

	if l > 16 && bytes.HasPrefix(buf, []byte("#EXTM3U")) {
		s := string(buf[:l])
		if strings.Contains(s, "#EXT-X-STREAM-INF:") ||
			strings.Contains(s, "#EXT-X-TARGETDURATION:") ||
			strings.Contains(s, "#EXT-X-MEDIA-SEQUENCE:") {
			// Top level HLS playlist
			md.ContentType = metadata.ContentVideo
			return 1
		}
		metdataSetRedirect(md, "playlist:%s", url)
		md.ContentType = metadata.ContentPlaylist
		return 1
	}
	return 0
}

// FAProbeMetadata — C: fa_probe_metadata (fa_probe.c:544-619).
func FAProbeMetadata(fam *fileaccesscore.FileAccessManager, url string,
	filename string, stats any) (*metadata.Metadata, error) {

	if strings.HasSuffix(url, ".m3u") {
		// C: strrchr(url, '.') postfix == ".m3u" — some files can just be
		// figured out by the file ending.
		md := metadata.Create()
		metdataSetRedirect(md, "playlist:%s", url)
		md.ContentType = metadata.ContentPlaylist
		return md, nil
	}

	park := true
	foe := &fileaccesscore.OpenExtra{Stats: stats}

	fh, err := fileaccesscore.FAOpenEx(fam, url,
		fileaccesscore.FaBufferedSmall, foe)
	if fh == nil {
		return nil, err
	}

	md := metadata.Create()

	buf := make([]byte, 4097)
	l := faRead(fh, buf[:4096])
	if l > 0 {
		buf[l] = 0

		// C: #if ENABLE_PLUGINS → plugin_probe_for_autoinstall (fa_probe.c:570)
		if fn := fam.PluginProbeForAutoinstall(); fn != nil {
			fn(fh, buf, l, url)
		}
		// C: #if ENABLE_VMIR → np_fa_probe redirect — deliberately
		// excluded: VMIR native plugins are not supported by design
		// (ext/vmir + src/np excluded from the port).

		if faProbeHeader(fam, md, url, fh, filename, buf, l) != 0 {
			fileaccesscore.FACloseWithPark(fh, park)
			return md, nil
		}
	}
	fileaccesscore.Seek4(fh, 0, 0 /* SEEK_SET */, false)

	if FAProbeIso(md, fh) == 0 {
		fileaccesscore.FACloseWithPark(fh, park)
		return md, nil
	}

	// C: strategy = fa_libav_get_strategy_for_file(fh)
	libavSys := libav.GetGlobalLibAVSystem()
	strategy := libav.FALibavGetStrategyForFile(fh)

	avio, aerr := libav.FALibavReopen(libavSys, fh, false)
	if aerr != nil || avio == nil {
		fileaccesscore.FAClose(fh)
		md.Destroy()
		return nil, aerr
	}

	fctx, ferr := libav.FALibavOpenFormat(avio, url,
		"", strategy)
	if fctx == nil {
		libav.FALibavClose(libavSys, avio)
		md.Destroy()
		return nil, ferr
	}

	faLavfLoadMeta(md, medialibav.WrapFormatCtx(fctx), filename)
	libav.FALibavCloseFormat(libavSys, fctx, park)
	return md, nil
}

// faRead — C: fa_read int semantics (n >= 0, -1 on error) for the probe path.
func faRead(fh *fileaccesscore.Handle, buf []byte) int {
	n, err := fileaccesscore.Read(fh, buf)
	if n > 0 {
		return n
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return -1
	}
	return n
}
