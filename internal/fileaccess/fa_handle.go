package fileaccess

import (
	"errors"
	"fmt"
	"io"
)

// NewHandle builds a Handle from explicit streams — the package-external
// counterpart of the internal `&Handle{...}` literals used by bundled
// protocols (C: fa_handle_t is embeddable; external fap impls need this).
// C: fileaccess.c — fh_proto set from the fam-level Protocol's FAProtocol.
func NewHandle(fam *FileAccessManager, proto Protocol, url string,
	reader io.ReadCloser, writer io.WriteCloser, seeker io.Seeker,
	sizer func() int64) *Handle {
	h := &Handle{
		fam:    fam,
		proto:  proto,
		url:    url,
		reader: reader,
		writer: writer,
		seeker: seeker,
		sizer:  sizer,
	}
	// C: fh->fh_proto = fap (fileaccess.c) — resolve the global FAProtocol
	if proto != nil {
		h.fap = fam.lookupFAProtocol(proto.Name())
	}
	return h
}

// Read reads data from the handle into the provided buffer
func (h *Handle) Read(buf []byte) (int, error) {
	if h == nil || h.reader == nil {
		return 0, fmt.Errorf("handle not readable")
	}
	return h.reader.Read(buf)
}

// Seek seeks to a position in the file
func (h *Handle) Seek(offset int64, whence int) (int64, error) {
	if h == nil || h.seeker == nil {
		return 0, fmt.Errorf("handle not seekable")
	}
	pos, err := h.seeker.Seek(offset, whence)
	if err == nil {
		h.position = pos
	}
	return pos, err
}

// Seek4 — method form of package Seek4 so *Handle satisfies lazy-aware
// seek interfaces (libav's fa_libav_seek passes AVSEEK-derived lazy).
// C: fa_seek4 dispatches fh->fh_proto->fap_seek(fh, pos, whence, lazy).
func (h *Handle) Seek4(pos int64, whence int, lazy bool) (int64, error) {
	return Seek4(h, pos, whence, lazy)
}

// Size returns the size of the file
// C: fap_fsize — resolved lazily for buffered files (fab_fsize).
func (h *Handle) Size() int64 {
	if h.sizer != nil {
		h.size = h.sizer()
	}
	return h.size
}

// CloseWithPark — C: fa_close_with_park on this handle (fap_park dispatch).
// Method form so *Handle satisfies libav's park-aware close interface.
func (h *Handle) CloseWithPark(park bool) error {
	return FACloseWithPark(h, park)
}

// Close closes the handle and releases associated resources
func (h *Handle) Close() error {
	if h == nil || h.closed {
		return nil
	}
	h.closed = true
	if h.reader != nil {
		return h.reader.Close()
	}
	return nil
}

// FALoad — C: fa_load(url, FA_LOAD_CACHE_CONTROL,
// FA_LOAD_CANCELLABLE, FA_LOAD_FLAGS, FA_LOAD_NO_FALLBACK, NULL).
// The full tagged form is faLoad (fileaccess_ops.go) via FALoadArgs.
func FALoad(fam *FileAccessManager, url string,
	cacheControl *int, c any, flags int) (*Buffer, error) {
	// C: fa_load without FA_LOAD_TAG_NO_FALLBACK — falls back to
	// fap_open + fa_fsize + fa_read when the protocol has no fap_load
	// (fs is such a protocol, fa_fs.c:865).
	return faLoad(fam, url, &FALoadArgs{
		CacheControl: cacheControl,
		Cancellable:  c,
		Flags:        flags,
	})
}

// FALoad2 — C: fa_load with the full tag set (fileaccess.c:1498).
func FALoad2(fam *FileAccessManager, url string, a *FALoadArgs) (*Buffer, error) {
	return faLoad(fam, url, a)
}

// Buffer represents an in-memory buffer for file data
type Buffer struct {
	Data        []byte // Buffer data
	Size        int    // Buffer size
	ContentType string // C: buf_t.b_content_type
}

// faRead — C: fa_read returns int (n >= 0 bytes read, -1 on error,
// 0 at EOF). Maps io.Reader semantics to the C contract.
func faRead(fh *Handle, buf []byte) int {
	n, err := Read(fh, buf)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return n
		}
		return -1
	}
	return n
}

// LoadAndClose loads the entire file into memory and closes the handle.
// C: fa_load_and_close (fileaccess.c:2000-2055) — seeks to start, then
// reads fsize bytes in one go, or grows the buffer for non-seekable fh.
func LoadAndClose(fh *Handle) *Buffer {
	if fh == nil {
		return nil
	}

	size := fh.size
	if size < 0 {
		size = -1
	}

	// C: fa_seek(fh, 0, SEEK_SET)
	if fh.seeker != nil {
		fh.seeker.Seek(0, io.SeekStart)
		fh.position = 0
	}

	var data []byte

	if size == -1 {
		// Unknown size: grow buffer until short read (C: fa_load_and_close
		// non-seekable branch).
		alloced := 0
		read := 0
		for {
			alloced = alloced*2 + 1000
			newData := make([]byte, alloced+1)
			copy(newData, data)
			data = newData

			maxRead := alloced - read
			r := faRead(fh, data[read:read+maxRead])
			if r < 0 {
				Close(fh)
				return nil
			}
			read += r
			if r < maxRead {
				break
			}
		}
		data = data[:read]
	} else {
		data = make([]byte, size+1)
		r := faRead(fh, data[:size])
		Close(fh)

		if int64(r) != size {
			return nil
		}
		data = data[:size]
	}

	// C: mem[size] = 0 — trailing NUL kept in capacity, Size excludes it.
	return &Buffer{
		Data: data,
		Size: len(data),
	}
}

// Open opens a file at the given URL
func Open(fam *FileAccessManager, url string) (*Handle, error) {
	return OpenEx(fam, url, nil, 0)
}

// OpenEx opens a file with extra options and flags
func OpenEx(fam *FileAccessManager, url string, extra *OpenExtra, flags int) (*Handle, error) {
	var proto Protocol
	if fam != nil {
		proto = fam.FindProtocol(url)
	}
	if proto == nil {
		// C: fa_resolve_proto rewrites dataroot:// (and fap_redirect
		// targets) before matching and dispatches on the resolved fap
		// directly. Our adapters are URL-keyed, so rebuild the URL from
		// the resolved proto name + post-scheme filename.
		if rproto, fname, err := fam.FAResolveProto(url); err == nil && rproto != nil && fname != url {
			url = fname
			if rproto != fam.nativeProto {
				url = rproto.Name + "://" + fname
			}
			if fam != nil {
				proto = fam.FindProtocol(url)
			}
		}
	}
	if proto == nil {
		return nil, errors.New("no protocol handler")
	}

	// C: fap_open(fh, url, errbuf, errsize, flags, foe) — flags reach the
	// protocol open; carry them through OpenExtra.
	if extra == nil {
		extra = &OpenExtra{}
	}
	extra.Flags = flags

	h, err := proto.Open(url, extra)
	if h != nil && h.fap == nil {
		// C: fh->fh_proto = fap — carry the FAProtocol gate so fap-level
		// ops (fap_park, fap_no_parking, fap_set_read_timeout) dispatch.
		h.fap = fam.lookupFAProtocol(proto.Name())
	}
	return h, err
}

// Close closes a file handle and releases resources
func Close(fh *Handle) error {
	if fh == nil || fh.closed {
		return nil
	}

	fh.closed = true

	if fh.reader != nil {
		return fh.reader.Close()
	}
	if fh.writer != nil {
		return fh.writer.Close()
	}

	return nil
}

// Read reads data from a file handle into the provided buffer
func Read(fh *Handle, buf []byte) (int, error) {
	if fh == nil || fh.closed {
		return 0, io.EOF
	}

	if fh.reader == nil {
		return 0, errors.New("handle not readable")
	}

	n, err := fh.reader.Read(buf)
	if err != nil {
		return n, err
	}

	fh.position += int64(n)
	return n, nil
}

// Write writes data to a file handle from the provided buffer
func Write(fh *Handle, buf []byte) (int, error) {
	if fh == nil || fh.closed {
		return 0, errors.New("handle closed")
	}

	if fh.writer == nil {
		return 0, errors.New("handle not writable")
	}

	n, err := fh.writer.Write(buf)
	if err != nil {
		return n, err
	}

	fh.position += int64(n)
	return n, nil
}

// Seek seeks to a position in a file
func Seek(fh *Handle, pos int64, whence int) (int64, error) {
	return Seek4(fh, pos, whence, false)
}

// lazySeeker — C: fap_seek's `lazy` parameter. Protocols that
// distinguish lazy seeks (fa_http, fa_buffer) implement this; Seek4
// dispatches to it when present.
type lazySeeker interface {
	Seek4(pos int64, whence int, lazy bool) (int64, error)
}

// Seek4 seeks to a position with lazy option
// C: fh->fh_proto->fap_seek(fh, pos, whence, lazy)
func Seek4(fh *Handle, pos int64, whence int, lazy bool) (int64, error) {
	if fh == nil || fh.closed {
		return 0, errors.New("handle closed")
	}

	if fh.seeker == nil {
		return 0, errors.New("handle not seekable")
	}

	var newPos int64
	var err error
	if ls, ok := fh.seeker.(lazySeeker); ok {
		newPos, err = ls.Seek4(pos, whence, lazy)
	} else {
		newPos, err = fh.seeker.Seek(pos, whence)
	}
	if err != nil {
		return newPos, err
	}

	fh.position = newPos
	return newPos, nil
}

// FSize returns the size of a file handle.
// C: fa_fsize (fileaccess.c:380) — fh->fh_proto->fap_fsize(fh): the
// protocol's own size, which may legitimately be -1 (unknown). No
// seek-based guessing — that would diverge from C (e.g. http_fsize
// returns -1 for streaming files).
func FSize(fh *Handle) (int64, error) {
	if fh == nil {
		return -1, errors.New("nil handle")
	}
	return fh.Size(), nil
}

// Ftruncate — C: fa_ftruncate (fileaccess.c:390-396). Dispatches to
// fh->fh_proto->fap_ftruncate; returns FAP_NOT_SUPPORTED when absent.
func Ftruncate(fh *Handle, newsize int64) int {
	if fh == nil || fh.fap == nil || fh.fap.Ftruncate == nil {
		return FAP_NOT_SUPPORTED
	}
	if err := fh.fap.Ftruncate(fh, newsize); err != nil {
		return FAP_ERROR
	}
	return FAP_OK
}

// Stat returns file statistics for the given URL
func Stat(fam *FileAccessManager, url string) (*FileStat, error) {
	return StatEx(fam, url, 0)
}

// StatEx returns file statistics with additional flags
func StatEx(fam *FileAccessManager, url string, flags int) (*FileStat, error) {
	var proto Protocol
	if fam != nil {
		proto = fam.FindProtocol(url)
	}
	if proto == nil {
		// C: fa_resolve_proto rewrites dataroot:// before matching and
		// dispatches on the resolved fap — rebuild the URL the same way
		// OpenEx does.
		if rproto, fname, err := fam.FAResolveProto(url); err == nil && rproto != nil && fname != url {
			url = fname
			if rproto != fam.nativeProto {
				url = rproto.Name + "://" + fname
			}
			if fam != nil {
				proto = fam.FindProtocol(url)
			}
		}
	}
	if proto == nil {
		return nil, errors.New("no protocol handler")
	}

	return proto.Stat(url)
}

// CanHandle checks if a URL can be handled by any registered protocol
// C: fa_can_handle (fileaccess.c:173-183) — fa_resolve_proto + fap_release
// + free(filename), returns 1 on success / 0 on resolve failure.
func CanHandle(fam *FileAccessManager, url string) error {
	proto, _, err := fam.FAResolveProto(url)
	if err != nil {
		return err
	}
	fapRelease(proto)
	return nil
}

// FASliceOpen — C: fa_slice_open (fa_slice.c:136-146) — wraps fh so that
// reads/seeks are confined to [offset, offset+size). The returned handle's
// position starts at 0; closing it closes the parent.
func FASliceOpen(fh *Handle, offset, size int64) *Handle {
	if fh == nil {
		return nil
	}
	s := &sliceReadSeekCloser{parent: fh, start: offset, size: size}
	return &Handle{
		fap:    faProtocolSlice,
		reader: s,
		seeker: s,
		url:    fh.url,
		size:   size,
	}
}

// FileStat2 returns file statistics (alternative name to avoid naming conflicts)
func FileStat2(fam *FileAccessManager, url string) (*FileStat, error) {
	return Stat(fam, url)
}
