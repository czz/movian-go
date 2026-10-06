// Canonical port of src/fileaccess/fa_zip.c — hand-rolled ZIP archive
// reader over any fa_handle (central-directory scan, refcounted archive
// cache, stored entries via the internal "zipfile" fap, deflated entries
// via fa_inflate_init).
package fileaccess

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"
)

// C: static HTS_MUTEX_DECL(zip_global_mutex) + zip_archive_list → fam.zip

// C: zip_hdr sizes
const (
	zipTrailerScanSize = 1024 // C: TRAILER_SCAN_SIZE
	zipDiskTrailerSize = 22   // sizeof(zip_hdr_disk_trailer_t)
	zipFileHeaderSize  = 46   // sizeof(zip_hdr_file_header_t)
	zipLocalHeaderSize = 30   // sizeof(zip_local_file_header_t)
)

// zipArchive — C: zip_archive_t (fa_zip.c:104)
type zipArchive struct {
	mu       sync.Mutex         // C: za_mutex
	refcount int                // C: za_refcount
	url      string             // C: za_url
	root     *zipFile           // C: za_root
	mtime    time.Time          // C: za_mtime
	fam      *FileAccessManager // C: implicit global fa context
}

// zipFile — C: zip_file_t (fa_zip.c:123)
type zipFile struct {
	files   []*zipFile // C: zf_files — LIST_INSERT_HEAD order
	archive *zipArchive
	name    string
	typ     int // CONTENT_DIR / CONTENT_FILE
	method  int

	uncompressedSize int64 // C: zf_uncompressed_size
	compressedSize   int64 // C: zf_compressed_size
	lhpos            int64 // C: zf_lhpos — local header position
}

// zipArchiveFindFile — C: zip_archive_find_file (fa_zip.c:143).
// Splits on '/' or '\\'; a trailing separator returns NULL; create!=0
// creates missing path components as CONTENT_DIR.
func zipArchiveFindFile(za *zipArchive, parent *zipFile, name string, create bool) *zipFile {
	if parent == nil {
		return nil
	}
	i := strings.IndexByte(name, '/')
	if j := strings.IndexByte(name, '\\'); i < 0 || (j >= 0 && j < i) {
		i = j
	}
	n := name
	var rest string
	hasMore := false
	if i >= 0 {
		rest = name[i+1:]
		if rest == "" {
			return nil
		}
		n = name[:i]
		hasMore = true
	}

	var zf *zipFile
	for _, f := range parent.files {
		if strings.EqualFold(n, f.name) {
			zf = f
			break
		}
	}
	if zf == nil {
		if !create {
			return nil
		}
		zf = &zipFile{archive: za, name: n}
		if hasMore {
			zf.typ = ContentDir
		} else {
			zf.typ = ContentFile
		}
		// C: LIST_INSERT_HEAD
		parent.files = slices.Insert(parent.files, 0, zf)
	}
	if hasMore {
		return zipArchiveFindFile(za, zf, rest, create)
	}
	return zf
}

// zipArchiveDestroyFile — C: zip_archive_destroy_file (fa_zip.c:183)
func zipArchiveDestroyFile(zf *zipFile) {
	for _, c := range zf.files {
		zipArchiveDestroyFile(c)
	}
	// C: free name/fullname + LIST_REMOVE — Go GC handles memory.
	zf.files = nil
}

// zipArchiveScrub — C: zip_archive_scrub (fa_zip.c:202)
func zipArchiveScrub(za *zipArchive) {
	if za.root != nil {
		zipArchiveDestroyFile(za.root)
		za.root = nil
	}
}

// zipArchiveLoad — C: zip_archive_load (fa_zip.c:217). Stats the URL,
// scans the last 1024 bytes for the end-of-central-directory record
// (PK\x05\x06), reads the central directory (with the displaced-offset
// fallback for self-extracting zips), and builds the file tree.
func zipArchiveLoad(za *zipArchive) int {
	fam := za.fam

	fs, err := Stat(fam, za.url)
	if err != nil {
		return -1
	}
	if fs.Size < zipDiskTrailerSize {
		return -1
	}
	asize := fs.Size
	za.mtime = fs.MTime

	fh, err := OpenEx(fam, za.url, nil, 0)
	if err != nil || fh == nil {
		return -1
	}

	scanOff := asize - zipTrailerScanSize
	var scanSize int64
	if scanOff < 0 {
		scanSize = asize
		scanOff = 0
	} else {
		scanSize = zipTrailerScanSize
	}

	scanBuf := make([]byte, scanSize)
	fh.Seek(scanOff, io.SeekStart)
	if n, _ := io.ReadFull(fh, scanBuf); int64(n) != scanSize {
		fh.Close()
		return -1
	}

	var cdsSize, cdsOff int64
	i := -1
	for k := int(scanSize) - zipDiskTrailerSize; k >= 0; k-- {
		if scanBuf[k] == 'P' && scanBuf[k+1] == 'K' &&
			scanBuf[k+2] == 5 && scanBuf[k+3] == 6 {
			cdsSize = int64(binary.LittleEndian.Uint32(scanBuf[k+12:]))
			cdsOff = int64(binary.LittleEndian.Uint32(scanBuf[k+16:]))
			i = k
			break
		}
	}
	if i == -1 {
		fh.Close()
		return -1
	}

	buf := make([]byte, cdsSize)
	if v, err := fh.Seek(cdsOff, io.SeekStart); err != nil || v != cdsOff ||
		func() bool { n, _ := io.ReadFull(fh, buf); return int64(n) != cdsSize }() {
		// C: memset(buf, 0, cds_off) — zero-fill on seek/read failure
		for k := range buf {
			buf[k] = 0
		}
	}

	var displacement int64

	validCDS := len(buf) >= 4 && buf[0] == 'P' && buf[1] == 'K' &&
		buf[2] == 1 && buf[3] == 2
	if !validCDS {
		// C: offset fallback — the central dir sits at
		// fs_size - (cds_size + distance-from-scan-start)
		o2 := asize - (cdsSize + (zipTrailerScanSize - int64(i)))
		fh.Seek(o2, io.SeekStart)
		if n, _ := io.ReadFull(fh, buf); int64(n) != cdsSize {
			fh.Close()
			return -1
		}
		if len(buf) < 4 || buf[0] != 'P' || buf[1] != 'K' ||
			buf[2] != 1 || buf[3] != 2 {
			fh.Close()
			return -1
		}
		displacement = o2 - cdsOff
	}

	za.root = &zipFile{typ: ContentDir, archive: za}

	for cdsSize > zipFileHeaderSize {
		if buf[0] != 'P' || buf[1] != 'K' || buf[2] != 1 || buf[3] != 2 {
			break
		}
		l := int(binary.LittleEndian.Uint16(buf[28:]))
		if l == 0 || zipFileHeaderSize+l > len(buf) {
			break
		}
		fname := string(buf[46 : 46+l])

		if fname[l-1] != '/' {
			// Not a directory
			if zf := zipArchiveFindFile(za, za.root, fname, true); zf != nil {
				zf.uncompressedSize = int64(binary.LittleEndian.Uint32(buf[24:]))
				zf.compressedSize = int64(binary.LittleEndian.Uint32(buf[20:]))
				zf.lhpos = int64(binary.LittleEndian.Uint32(buf[42:])) + displacement
				zf.method = int(binary.LittleEndian.Uint16(buf[10:]))
			}
		}

		entryLen := zipFileHeaderSize + l +
			int(binary.LittleEndian.Uint16(buf[30:])) +
			int(binary.LittleEndian.Uint16(buf[32:]))
		cdsSize -= int64(entryLen)
		if entryLen > len(buf) {
			break
		}
		buf = buf[entryLen:]
	}

	fh.Close()
	return 0
}

// zipArchiveUnref — C: zip_archive_unref (fa_zip.c:383)
func zipArchiveUnref(za *zipArchive) {
	fam := za.fam
	fam.zip.mu.Lock()
	za.refcount--
	if za.refcount == 0 {
		zipArchiveScrub(za)
		for i, a := range fam.zip.archives {
			if a == za {
				fam.zip.archives = slices.Delete(fam.zip.archives, i, i+1)
				break
			}
		}
	}
	fam.zip.mu.Unlock()
}

// zipArchiveFind — C: zip_archive_find (fa_zip.c:405). Matches the
// longest cached archive prefix, else walks up until a CONTENT_FILE
// stats. Holds one ref on return; the remainder is the in-archive path.
func zipArchiveFind(fam *FileAccessManager, url string) (*zipArchive, string) {
	if fam == nil || url == "" {
		return nil, ""
	}

	fam.zip.mu.Lock()
	var za *zipArchive
	u := url
	for {
		for _, a := range fam.zip.archives {
			if strings.EqualFold(a.url, u) {
				za = a
				break
			}
		}
		if za != nil {
			break
		}
		i := strings.LastIndex(u, "/")
		if i < 0 {
			break
		}
		u = u[:i]
	}

	if za == nil {
		// C: walk up until fa_stat yields CONTENT_FILE
		u = url
		for {
			if st, err := Stat(fam, u); err == nil && st.Type == ContentFile {
				break
			}
			i := strings.LastIndex(u, "/")
			if i < 0 {
				fam.zip.mu.Unlock()
				return nil, ""
			}
			u = u[:i]
		}
	}

	r := url[len(u):]
	if strings.HasPrefix(r, "/") {
		r = r[1:]
	}

	if za == nil {
		za = &zipArchive{url: u, fam: fam}
		fam.zip.archives = slices.Insert(fam.zip.archives, 0, za)
	}
	za.refcount++
	fam.zip.mu.Unlock()

	za.mu.Lock()
	if za.root == nil && zipArchiveLoad(za) != 0 {
		zipArchiveScrub(za)
	}
	za.mu.Unlock()

	return za, r
}

// zipFileFind — C: zip_file_find (fa_zip.c:464). One archive ref held.
func zipFileFind(fam *FileAccessManager, url string) *zipFile {
	za, r := zipArchiveFind(fam, url)
	if za == nil {
		return nil
	}
	var rf *zipFile
	if r != "" {
		rf = zipArchiveFindFile(za, za.root, r, false)
	} else {
		rf = za.root
	}
	if rf == nil {
		zipArchiveUnref(za)
	}
	return rf
}

// zipFileUnref — C: zip_file_unref (fa_zip.c:484)
func zipFileUnref(zf *zipFile) {
	zipArchiveUnref(zf.archive)
}

// zipRef — C: zip_ref_t (fa_zip.c:519) — a reference handle holding a file.
type zipRef struct {
	file *zipFile
}

func (r *zipRef) Read(buf []byte) (int, error) { return 0, errors.New("zip ref handle") }

func (r *zipRef) Close() error {
	zipFileUnref(r.file)
	return nil
}

// zipReference — C: zip_reference (fa_zip.c:529)
func zipReference(fap *FAProtocol, url string) *Handle {
	zf := zipFileFind(fap.Opaque.(*FileAccessManager), stripZIPScheme(url))
	if zf == nil {
		return nil
	}
	return &Handle{
		fap:    fap,
		reader: &zipRef{file: zf},
		url:    url,
	}
}

// zipUnreference — C: zip_unreference (fa_zip.c:545)
func zipUnreference(fh *Handle) {
	if fh != nil && fh.reader != nil {
		fh.reader.Close()
	}
}

// zipFH — C: zip_fh_t (fa_zip.c:560)
type zipFH struct {
	file          *zipFile // C: zfh_file
	archiveHandle *Handle  // C: zfh_archive_handle
	archivePos    int64    // C: zfh_archive_pos (never updated on read — canonical)
	fileStart     int64    // C: zfh_file_start — data position in archive
	pos           int64    // C: zfh_pos — compressed-stream position

	// C: zfh_reader_handle + zfh_reader_proto — method 0 → self through
	// zipfile ops; method 8 → inflator wrapping the zipfile handle.
	reader io.ReadSeekCloser
}

// zipfileRSC — C: the &zfh->h viewed through zip_file_protocol
// (fa_zip.c:702). Reads the raw compressed byte range.
type zipfileRSC struct{ zfh *zipFH }

// zipFileProtocolGate — C: zip_file_protocol (fa_zip.c:702) — the
// internal "zipfile" fap; not registered globally, used only as the
// dispatch gate for inflator source ops.
var zipFileProtocolGate = &FAProtocol{Name: "zipfile"}

// Read — C: zip_file_read (fa_zip.c:576). Bounded to compressed_size;
// seeks the archive handle when out of position. Canonical quirk:
// zfh_archive_pos is never updated on success, so each read re-seeks.
func (r *zipfileRSC) Read(buf []byte) (int, error) {
	zfh := r.zfh
	zf := zfh.file

	if zfh.pos < 0 || zfh.pos > zf.compressedSize {
		return 0, io.EOF
	}
	size := len(buf)
	if zfh.pos+int64(size) > zf.compressedSize {
		size = int(zf.compressedSize - zfh.pos)
	}
	if size <= 0 {
		return 0, io.EOF
	}
	wpos := zfh.pos + zfh.fileStart // real position in archive
	if wpos != zfh.archivePos {
		v, err := zfh.archiveHandle.Seek(wpos, io.SeekStart)
		if err != nil || v != wpos {
			zfh.archivePos = -1
			return -1, errors.New("zip: archive seek failed")
		}
	}
	n, err := zfh.archiveHandle.Read(buf[:size])
	if n > 0 {
		zfh.pos += int64(n)
	}
	return n, err
}

// Seek — C: zip_file_seek (fa_zip.c:619)
func (r *zipfileRSC) Seek(pos int64, whence int) (int64, error) {
	zfh := r.zfh
	var np int64
	switch whence {
	case io.SeekStart:
		np = pos
	case io.SeekCurrent:
		np = zfh.pos + pos
	case io.SeekEnd:
		np = zfh.file.compressedSize + pos
	default:
		return -1, errors.New("zip: invalid whence")
	}
	if np < 0 {
		return -1, errors.New("zip: negative position")
	}
	zfh.pos = np
	return np, nil
}

// Close — C: zip_file_close (fa_zip.c:655)
func (r *zipfileRSC) Close() error {
	return Close(r.zfh.archiveHandle)
}

// Read — C: zip_read (fa_zip.c:810) — dispatch through the reader proto.
func (z *zipFH) Read(buf []byte) (int, error) {
	return z.reader.Read(buf)
}

// Seek — C: zip_seek (fa_zip.c:821)
func (z *zipFH) Seek(pos int64, whence int) (int64, error) {
	return z.reader.Seek(pos, whence)
}

// Close — C: zip_close (fa_zip.c:796)
func (z *zipFH) Close() error {
	err := z.reader.Close()
	zipFileUnref(z.file)
	return err
}

// stripZIPScheme — fap_* methods receive the URL with "zip://" removed.
func stripZIPScheme(url string) string {
	return strings.TrimPrefix(url, "zip://")
}

// zipOpen — C: zip_open (fa_zip.c:720). Finds the entry, opens the
// archive, parses the local file header (PK\x03\x04), then picks the
// reader: stored → zipfile proto self-dispatch; deflated → inflator.
func zipOpen(fam *FileAccessManager, url string) (*Handle, error) {
	zf := zipFileFind(fam, url)
	if zf == nil {
		return nil, errors.New("Entry not found in archive")
	}
	if zf.typ != ContentFile {
		zipFileUnref(zf)
		return nil, errors.New("Entry is not a file")
	}

	za := zf.archive
	zfh := &zipFH{file: zf}

	ah, err := OpenEx(fam, za.url, nil, 0)
	if err != nil || ah == nil {
		zipFileUnref(zf)
		if err == nil {
			err = errors.New("Unable to open archive")
		}
		return nil, err
	}
	zfh.archiveHandle = ah

	zfh.archiveHandle.Seek(zf.lhpos, io.SeekStart)

	var h [zipLocalHeaderSize]byte
	if n, _ := io.ReadFull(zfh.archiveHandle, h[:]); n != zipLocalHeaderSize {
		zipFHFail(zfh)
		return nil, errors.New("Truncated ZIP file")
	}
	if h[0] != 'P' || h[1] != 'K' || h[2] != 3 || h[3] != 4 {
		zipFHFail(zfh)
		return nil, errors.New("Bad ZIP magic")
	}

	zfh.fileStart = zf.lhpos + zipLocalHeaderSize +
		int64(binary.LittleEndian.Uint16(h[26:])) +
		int64(binary.LittleEndian.Uint16(h[28:]))

	switch zf.method {
	case 0:
		// No compression — reads go through the zipfile fap on self
		zfh.reader = &zipfileRSC{zfh: zfh}
	case 8:
		// Inflate — C: fa_inflate_init(&zip_file_protocol, &zfh->h, usize)
		rsc := &zipfileRSC{zfh: zfh}
		inner := &Handle{
			fap:    zipFileProtocolGate,
			reader: rsc,
			seeker: rsc,
			url:    url,
			size:   zf.compressedSize,
		}
		if ih := NewFAInflate(zipFileProtocolGate, inner,
			zf.uncompressedSize); ih != nil {
			zfh.reader = ih
		} else {
			zipFHFail(zfh)
			return nil, errors.New("Unable to initialize inflator")
		}
	default:
		err := fmt.Errorf("Compression method %d not supported\n", zf.method)
		zipFHFail(zfh)
		return nil, err
	}

	return &Handle{
		reader: zfh,
		seeker: zfh,
		url:    url,
		size:   zf.uncompressedSize,
	}, nil
}

// zipFHFail — C: the bad: label (fa_zip.c:780-786)
func zipFHFail(zfh *zipFH) {
	if zfh.archiveHandle != nil {
		zfh.archiveHandle.Close()
	}
	zipFileUnref(zfh.file)
}

// zipStatFAP — C: zip_stat (fa_zip.c:849) as an fap_stat (filename
// already stripped of the "zip://" scheme by fa_resolve_proto).
func zipStatFAP(fap *FAProtocol, filename string, flags int) (*FileStat, error) {
	zf := zipFileFind(fap.Opaque.(*FileAccessManager), filename)
	if zf == nil {
		return nil, fapError{code: FAP_NOENT, msg: "Entry not found in archive"}
	}
	st := &FileStat{Type: zf.typ, Size: zf.uncompressedSize, MTime: zf.archive.mtime}
	zipFileUnref(zf)
	return st, nil
}

// ZIPProtocol — C: fa_protocol_zip (fa_zip.c:865)
type ZIPProtocol struct {
	fam *FileAccessManager // C: implicit global fa context
}

func (p *ZIPProtocol) Name() string { return "zip" }

func (p *ZIPProtocol) CanHandle(url string) bool {
	return strings.HasPrefix(url, "zip://")
}

// Open — C: zip_open via fap_open.
func (p *ZIPProtocol) Open(url string, extra *OpenExtra) (*Handle, error) {
	h, err := zipOpen(p.fam, stripZIPScheme(url))
	if h != nil {
		h.proto = p
	}
	return h, err
}

// ScanDir — C: zip_scandir (fa_zip.c:505)
func (p *ZIPProtocol) ScanDir(ctx context.Context, url0 string) (*Dir, error) {
	url := stripZIPScheme(url0)
	zf := zipFileFind(p.fam, url)
	if zf == nil {
		return nil, errors.New("Entry not found in archive")
	}
	if zf.typ != ContentDir {
		zipFileUnref(zf)
		return nil, errors.New("Entry is not a directory")
	}
	fd := DirAlloc()
	for _, c := range zf.files {
		DirAdd(fd, fmt.Sprintf("zip://%s/%s", url, c.name), c.name, c.typ)
	}
	zipFileUnref(zf)
	return fd, nil
}

// Stat — C: zip_stat via fam dispatch.
func (p *ZIPProtocol) Stat(url string) (*FileStat, error) {
	zf := zipFileFind(p.fam, stripZIPScheme(url))
	if zf == nil {
		return nil, errors.New("Entry not found in archive")
	}
	fs := &FileStat{
		Type:  zf.typ,
		Size:  zf.uncompressedSize,
		MTime: zf.archive.mtime,
	}
	zipFileUnref(zf)
	return fs, nil
}

// populateZIPFAProtocol — fill the global "zip" FAProtocol gate with the
// canonical ops (C: fa_protocol_zip's fap_stat/fap_reference/
// fap_unreference).
func populateZIPFAProtocol(fp *FAProtocol, fam *FileAccessManager) {
	fp.Opaque = fam
	fp.Stat = zipStatFAP
	fp.Reference = zipReference
	fp.Unreference = zipUnreference
}
