// Canonical port of src/fileaccess/fa_zlib.c — raw-DEFLATE inflating
// wrapper (fa_inflate_init/inflate_read/inflate_seek/inflate_close).
// Used by fa_zip for compressed entries; keeps a one-chunk (32 KiB)
// decoded window — seeks before the window rewind and re-decode.
package fileaccess

import (
	"compress/flate"
	"errors"
	"io"
)

// C: DECODESIZE (fa_zlib.c)
const decodeSize = 32768

// inflator — C: fa_inflator_t (fa_zlib.c:30-47)
type inflator struct {
	src    *Handle     // C: fi_src_handle
	srcFap *FAProtocol // C: fi_src_fap

	uncSize int64 // C: fi_unc_size — uncompressed size
	pos     int64 // C: fi_pos

	bufStart int64  // C: fi_bufstart — fi_buf starts at this position
	bufSize  int    // C: fi_bufsize
	buf      []byte // C: fi_buf (DECODESIZE)

	stream    io.ReadCloser // C: fi_zstream (raw DEFLATE, -MAX_WBITS)
	streamEnd bool          // C: stream_end
}

// faProtocolInflate — C: fa_protocol_inflate (fa_zlib.c:218)
var faProtocolInflate = &FAProtocol{Name: "inflate"}

// NewFAInflate — C: fa_inflate_init (fa_zlib.c:53-71). Wraps handle so
// reads return raw-DEFLATE-decompressed content; uncSize is the expected
// uncompressed size (drives SEEK_END and fsize).
func NewFAInflate(srcFap *FAProtocol, fh *Handle, uncSize int64) *Handle {
	fi := &inflator{
		src:     fh,
		srcFap:  srcFap,
		uncSize: uncSize,
		buf:     make([]byte, decodeSize),
		stream:  flate.NewReader(fh),
	}
	h := &Handle{
		fap:    faProtocolInflate,
		reader: fi,
		seeker: fi,
		url:    fh.url,
		size:   uncSize,
	}
	// C: inflate_fsize → fi_unc_size
	h.sizer = func() int64 { return fi.uncSize }
	return h
}

// Close — C: inflate_close (fa_zlib.c:77-86)
func (fi *inflator) Close() error {
	err := fi.src.Close()
	if fi.stream != nil {
		fi.stream.Close()
	}
	return err
}

// Read — C: inflate_read (fa_zlib.c:92-155). Serves from the decoded
// window when possible; otherwise fills the next 32 KiB chunk from the
// inflater. Seeking before the window rewinds the whole stream.
func (fi *inflator) Read(buf []byte) (int, error) {
	totalRead := 0
	size := len(buf)

	for size > 0 {
		if fi.pos < fi.bufStart {
			// C: rewind — inflateEnd + inflateInit2 + fap_seek(src,0)
			fi.stream.Close()
			fi.bufStart = 0
			fi.bufSize = 0
			fi.streamEnd = false
			if v, err := fi.src.Seek(0, io.SeekStart); err != nil || v != 0 {
				return 0, errors.New("inflate: rewind seek failed")
			}
			fi.stream = flate.NewReader(fi.src)
		}

		n := fi.pos - fi.bufStart // offset in decompressed buffer
		if n >= 0 && n < int64(fi.bufSize) {
			c := min(int64(size), int64(fi.bufSize)-n)
			copy(buf[totalRead:], fi.buf[n:n+c])
			size -= int(c)
			totalRead += int(c)
			fi.pos += c
			continue
		}

		if fi.streamEnd {
			break
		}

		// C: fi->fi_bufstart += fi->fi_bufsize; inflate until chunk
		// full or Z_STREAM_END.
		fi.bufStart += int64(fi.bufSize)
		nn, err := io.ReadFull(fi.stream, fi.buf)
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			fi.streamEnd = true
		} else if err != nil {
			// C: r != Z_OK → return -1 (discard total_read)
			return 0, err
		}
		fi.bufSize = nn
	}

	if totalRead == 0 && fi.streamEnd {
		return 0, io.EOF
	}
	return totalRead, nil
}

// Seek — C: inflate_seek (fa_zlib.c:161-186). Only repositions fi_pos;
// the read path decodes forward or rewinds as needed.
func (fi *inflator) Seek(pos int64, whence int) (int64, error) {
	var np int64
	switch whence {
	case io.SeekStart:
		np = pos
	case io.SeekCurrent:
		np = fi.pos + pos
	case io.SeekEnd:
		np = fi.uncSize + pos
	default:
		return -1, errors.New("inflate: invalid whence")
	}
	if np < 0 {
		return -1, errors.New("inflate: negative position")
	}
	fi.pos = np
	return np, nil
}
