// Canonical port of src/fileaccess/fa_rar.c — RAR archive (RAR4) protocol.
// Supports uncompressed (method '0') entries; multi-volume archives via
// .rNN and .partN.rar naming. Reads span segments across volumes.
package fileaccess

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"
)

// C: fa_rar.c block type / flag constants
const (
	rarHeaderMain   = 0x73
	rarHeaderFile   = 0x74
	rarHeaderNewsub = 0x7a
	rarHeaderEndarc = 0x7b
)

const (
	lhdSplitBefore = 0x0001
	lhdSplitAfter  = 0x0002
	lhdPassword    = 0x0004
	lhdComment     = 0x0008
	lhdSolid       = 0x0010

	lhdWindowMask = 0x00e0
	lhdDirectory  = 0x00e0

	lhdLarge    = 0x0100
	lhdUnicode  = 0x0200
	lhdSalt     = 0x0400
	lhdVersion  = 0x0800
	lhdExttime  = 0x1000
	lhdExtflags = 0x2000
)

const (
	earcNextVolume = 0x0001 // not last volume
	earcDatacrc    = 0x0002
	earcRevspace   = 0x0004
	earcVolnumber  = 0x0008
)

// C: static hts_mutex_t rar_global_mutex + rar_archive_list rar_archives
// C: rar_global_mutex + rar_archives — grouped process state
// C: rar_archives list + mutex → fam.rar

// C: rar_archive_t
type rarArchive struct {
	mu       sync.Mutex // C: ra_mutex
	refcount int        // C: ra_refcount
	url      string     // C: ra_url
	volumes  []*rarVolume
	root     *rarFile // C: ra_root
	mtime    time.Time
	fam      *FileAccessManager // C: implicit global fa context
}

// C: rar_volume_t
type rarVolume struct {
	url string // C: rv_url
}

// C: rar_file_t — files and dirs share one node type.
type rarFile struct {
	segments []*rarSegment // C: rf_segments (TAILQ — append order)
	files    []*rarFile    // C: rf_files (LIST — insert head)
	name     string        // C: rf_name
	typ      int           // C: rf_type — ContentDir/ContentFile
	size     int64         // C: rf_size (accumulated packed size)
	archive  *rarArchive   // C: rf_archive
	method   byte          // C: rf_method
	unpver   byte          // C: rf_unpver
}

// C: rar_segment_t
type rarSegment struct {
	volume  *rarVolume // C: rs_volume
	offset  int64      // C: rs_offset  — offset in the unpacked file
	voffset int64      // C: rs_voffset — offset inside the volume file
	size    int64      // C: rs_size    — packed size in this volume
}

// rarArchiveFindFile — C: rar_archive_find_file (fa_rar.c:154).
// Walks/creates the file tree by '/' or '\' separated components.
// create==1 creates missing nodes; returns the leaf (or dir for trailing).
func rarArchiveFindFile(ra *rarArchive, parent *rarFile, name string,
	create int, unpver, method byte) *rarFile {

	if parent == nil {
		return nil
	}

	// C: s = strchr(name,'/') ?: strchr(name,'\\')
	n := name
	var rest string
	hasRest := false
	if i := strings.IndexAny(name, "/\\"); i >= 0 {
		n = name[:i]
		rest = name[i+1:]
		if rest == "" {
			return nil // C: if(*s == 0) return NULL
		}
		hasRest = true
	}

	var rf *rarFile
	// C: LIST_FOREACH — case-insensitive name match
	for _, c := range parent.files {
		if strings.EqualFold(n, c.name) {
			rf = c
			break
		}
	}

	if rf == nil {
		if create == 0 {
			return nil
		}
		rf = &rarFile{
			archive: ra,
			name:    n,
		}
		if hasRest {
			rf.typ = ContentDir
		} else {
			rf.typ = ContentFile
		}
		// C: LIST_INSERT_HEAD
		parent.files = slices.Insert(parent.files, 0, rf)
		if rf.typ == ContentFile {
			rf.unpver = unpver
			rf.method = method
		}
	}

	if hasRest {
		return rarArchiveFindFile(ra, rf, rest, create, unpver, method)
	}
	return rf
}

// rarArchiveDestroyFile — C: rar_archive_destroy_file (fa_rar.c:207).
// Go drops whole slices rather than per-node LIST_REMOVE.
func rarArchiveDestroyFile(rf *rarFile) {
	rf.segments = nil
	for _, c := range rf.files {
		rarArchiveDestroyFile(c)
	}
	rf.files = nil
}

// rarArchiveScrub — C: rar_archive_scrub (fa_rar.c:231)
func rarArchiveScrub(ra *rarArchive) {
	if ra.root != nil {
		rarArchiveDestroyFile(ra.root)
		ra.root = nil
	}
	ra.volumes = nil
}

// rarArchiveLoad — C: rar_archive_load (fa_rar.c:253).
// Parses the RAR4 headers of the archive (and any continuation volumes)
// and builds the rar_file tree + segment lists.
func rarArchiveLoad(ra *rarArchive) int {
	fam := ra.fam

	ra.root = &rarFile{typ: ContentDir, archive: ra}

	volumeIndex := -1

openVolume:
	for {
		filename := ra.url

		if volumeIndex >= 0 {
			// C: volume naming — last '.' → "r%02d", or partN variants.
			dot := strings.LastIndex(filename, ".")
			if dot < 0 {
				return -1
			}
			// C: find second-last '.' for .partN.rar forms
			s2 := -1
			for i := dot - 1; i >= 0; i-- {
				if filename[i] == '.' {
					s2 = i
					break
				}
			}
			switch {
			case s2 >= 0 && filename[s2:] == ".part1.rar":
				if volumeIndex == 0 {
					volumeIndex = 2
				}
				filename = filename[:s2] + fmt.Sprintf(".part%d.rar", volumeIndex)
			case s2 >= 0 && filename[s2:] == ".part01.rar":
				if volumeIndex == 0 {
					volumeIndex = 2
				}
				filename = filename[:s2] + fmt.Sprintf(".part%02d.rar", volumeIndex)
			case s2 >= 0 && filename[s2:] == ".part001.rar":
				if volumeIndex == 0 {
					volumeIndex = 2
				}
				filename = filename[:s2] + fmt.Sprintf(".part%03d.rar", volumeIndex)
			default:
				filename = filename[:dot+1] + fmt.Sprintf("r%02d", volumeIndex)
			}
		}

		volumeIndex++

		fh, err := Open(fam, filename)
		if fh == nil || err != nil {
			return -1
		}

		// Read & verify RAR signature: "Rar!\x1a\x07\x00"
		buf := make([]byte, 16)
		if n, _ := FARead(fh, buf[:7]); n != 7 {
			fh.Close()
			return -1
		}
		if buf[0] != 'R' || buf[1] != 'a' || buf[2] != 'r' ||
			buf[3] != '!' || buf[4] != 0x1a || buf[5] != 0x07 ||
			buf[6] != 0x0 {
			fh.Close()
			return -1
		}

		// Next we expect a MAIN_HEAD header (13 bytes)
		if n, _ := FARead(fh, buf[:13]); n != 13 {
			fh.Close()
			return -1
		}

		// C: if(ra->ra_mtime == 0 && !fa_stat(fh, &fs, NULL, 0))
		//      ra->ra_mtime = fs.fs_mtime;
		// C passes the fa_handle_t where fa_stat expects a URL string —
		// the stat always fails, so ra_mtime stays 0. Replicated: rar_stat
		// reports fs_mtime = 0 for archive entries.

		/* 2 bytes CRC */
		if buf[2] != rarHeaderMain {
			fh.Close()
			return -1
		}
		size := int(buf[5]) | int(buf[6])<<8
		if size != 13 {
			fh.Close()
			return -1
		}

		rv := &rarVolume{url: filename}
		// C: LIST_INSERT_HEAD(&ra->ra_volumes, rv, rv_link)
		ra.volumes = slices.Insert(ra.volumes, 0, rv)

		voff := int64(13 + 7)

		for {
			// Read a header
			if n, _ := FARead(fh, buf[:7]); n != 7 {
				break
			}
			flags := uint16(buf[3]) | uint16(buf[4])<<8
			size = int(buf[5]) | int(buf[6])<<8
			if size < 7 {
				break
			}
			size -= 7

			hdr := make([]byte, size)
			if n, _ := FARead(fh, hdr); n != size {
				break
			}
			voff += int64(7 + size)

			if buf[2] == rarHeaderFile || buf[2] == rarHeaderNewsub {
				packsize := uint64(hdr[0]) | uint64(hdr[1])<<8 |
					uint64(hdr[2])<<16 | uint64(hdr[3])<<24
				unpsize := uint64(hdr[4]) | uint64(hdr[5])<<8 |
					uint64(hdr[6])<<16 | uint64(hdr[7])<<24
				/* Skip HostOS    1 byte  */
				/* Skip FileCRC   4 bytes */
				/* Skip FileTime  4 bytes */
				unpver := hdr[17]
				method := hdr[18]
				nsize := int(hdr[19]) | int(hdr[20])<<8
				/* Skip FileAttr  4 bytes */
				x := 25

				if flags&lhdLarge != 0 {
					u32 := uint64(hdr[x]) | uint64(hdr[x+1])<<8 |
						uint64(hdr[x+2])<<16 | uint64(hdr[x+3])<<24
					packsize |= u32 << 32
					u32 = uint64(hdr[x+4]) | uint64(hdr[x+5])<<8 |
						uint64(hdr[x+6])<<16 | uint64(hdr[x+7])<<24
					unpsize |= u32 << 32
					x += 8
				}
				if x+nsize > len(hdr) {
					break
				}
				fname := string(hdr[x : x+nsize])

				if flags&lhdWindowMask != lhdDirectory {
					if rf := rarArchiveFindFile(ra, ra.root, fname, 1,
						unpver, method); rf != nil {
						rf.segments = append(rf.segments, &rarSegment{
							volume:  rv,
							offset:  rf.size,
							voffset: voff,
							size:    int64(packsize),
						})
						rf.size += int64(packsize)
					}
				}

				FASeek(fh, int64(packsize), io.SeekCurrent)
				voff += int64(packsize)

			} else if buf[2] == rarHeaderEndarc {
				x := 0
				if flags&earcDatacrc != 0 {
					if size < 4 {
						break
					}
					x += 4
				}
				if flags&earcVolnumber != 0 {
					if size-x < 4 {
						break
					}
					// u32 = hdr[x..x+4] — volume number, unused (C too)
				}
				fh.Close()
				fh = nil

				if flags&earcNextVolume != 0 {
					continue openVolume
				}
				return 0
			}
			// C: free(hdr) — no-op in Go
		}

		// C: err label — break out of the header loop on truncated input
		fh.Close()
		return -1
	}
}

// rarArchiveUnref — C: rar_archive_unref (fa_rar.c:445)
func rarArchiveUnref(ra *rarArchive) {
	fam := ra.fam
	fam.rar.mu.Lock()
	ra.refcount--
	if ra.refcount == 0 {
		rarArchiveScrub(ra)
		for i, a := range fam.rar.archives {
			if a == ra {
				fam.rar.archives = slices.Delete(fam.rar.archives, i, i+1)
				break
			}
		}
	}
	fam.rar.mu.Unlock()
}

// rarArchiveFind — C: rar_archive_find (fa_rar.c:467).
// Finds/creates the archive owning url's longest stat-able file prefix.
// Returns the archive (refcount held) and the in-archive path remainder.
func rarArchiveFind(fam *FileAccessManager, url string) (*rarArchive, string) {
	if fam == nil || url == "" {
		return nil, ""
	}

	fam.rar.mu.Lock()

	u := url
	var ra *rarArchive
	for {
		for _, a := range fam.rar.archives {
			if strings.EqualFold(a.url, u) {
				ra = a
				break
			}
		}
		if ra != nil {
			break
		}
		i := strings.LastIndex(u, "/")
		if i < 0 {
			break
		}
		u = u[:i]
	}

	if ra == nil {
		// Walk up until a real file stats (C: fa_stat == CONTENT_FILE)
		u = url
		for {
			if st, err := Stat(fam, u); err == nil && st.Type == ContentFile {
				break
			}
			i := strings.LastIndex(u, "/")
			if i < 0 {
				fam.rar.mu.Unlock()
				return nil, ""
			}
			u = u[:i]
		}
	}

	r := url[len(u):]
	if strings.HasPrefix(r, "/") {
		r = r[1:]
	}

	if ra == nil {
		ra = &rarArchive{url: u, fam: fam}
		// C: LIST_INSERT_HEAD(&rar_archives, ra, ra_link)
		fam.rar.archives = slices.Insert(fam.rar.archives, 0, ra)
	}
	ra.refcount++
	fam.rar.mu.Unlock()

	ra.mu.Lock()
	if ra.root == nil && rarArchiveLoad(ra) != 0 {
		rarArchiveScrub(ra)
	}
	ra.mu.Unlock()

	return ra, r
}

// rarFileFind — C: rar_file_find (fa_rar.c:538). Holds one archive ref on
// success (release via rarFileUnref).
func rarFileFind(fam *FileAccessManager, url string) *rarFile {
	ra, r := rarArchiveFind(fam, url)
	if ra == nil {
		return nil
	}
	var rf *rarFile
	if r != "" {
		rf = rarArchiveFindFile(ra, ra.root, r, 0, 0, 0)
	} else {
		rf = ra.root
	}
	if rf == nil {
		rarArchiveUnref(ra)
	}
	return rf
}

// rarFileUnref — C: rar_file_unref (fa_rar.c:558)
func rarFileUnref(rf *rarFile) {
	rarArchiveUnref(rf.archive)
}

// stripRARScheme — C's fap_* methods receive the URL with "rar://" already
// removed by fa_resolve_proto.
func stripRARScheme(url string) string {
	return strings.TrimPrefix(url, "rar://")
}

// RARProtocol — C: fa_protocol_rar (fa_rar.c:861)
type RARProtocol struct {
	fam *FileAccessManager // C: implicit global fa context
}

func (p *RARProtocol) Name() string { return "rar" }

func (p *RARProtocol) CanHandle(url string) bool {
	return strings.HasPrefix(url, "rar://")
}

// ScanDir — C: rar_scandir (fa_rar.c:568)
func (p *RARProtocol) ScanDir(ctx context.Context, url0 string) (*Dir, error) {
	url := stripRARScheme(url0)
	// C: strip trailing '/' (keeps at least index 0)
	for n := len(url) - 1; n > 0; n-- {
		if url[n] == '/' {
			url = url[:n]
		} else {
			break
		}
	}

	rf := rarFileFind(p.fam, url)
	if rf == nil {
		return nil, errors.New("Entry not found in archive")
	}
	if rf.typ != ContentDir {
		rarFileUnref(rf)
		return nil, errors.New("Entry is not a directory")
	}

	fd := DirAlloc()
	for _, c := range rf.files {
		DirAdd(fd, fmt.Sprintf("rar://%s/%s", url, c.name), c.name, c.typ)
	}
	rarFileUnref(rf)
	return fd, nil
}

// rarRef — C: rar_ref_t (fa_rar.c:627) — a reference handle holding a file.
type rarRef struct {
	file *rarFile
}

func (r *rarRef) Read(buf []byte) (int, error) { return 0, errors.New("rar ref handle") }
func (r *rarRef) Close() error {
	rarFileUnref(r.file)
	return nil
}

// rarReference — C: rar_reference (fa_rar.c:612)
func rarReference(fap *FAProtocol, url string) *Handle {
	rf := rarFileFind(fap.Opaque.(*FileAccessManager), stripRARScheme(url))
	if rf == nil {
		return nil
	}
	return &Handle{
		fap:    fap,
		reader: &rarRef{file: rf},
		url:    url,
	}
}

// rarUnreference — C: rar_unreference (fa_rar.c:631)
func rarUnreference(fh *Handle) {
	if fh != nil && fh.reader != nil {
		fh.reader.Close()
	}
}

// rarReader — C: rar_fd_t (fa_rar.c:644). Implements io.ReadCloser +
// io.Seeker over the file's segment list; volume handles open lazily.
type rarReader struct {
	file    *rarFile    // C: rfd_file
	segment *rarSegment // C: rfd_segment
	fh      *Handle     // C: rfd_fh — current volume handle
	fpos    int64       // C: rfd_fpos
}

// Read — C: rar_read (fa_rar.c:708)
func (r *rarReader) Read(buf []byte) (int, error) {
	rf := r.file
	fam := rf.archive.fam

	size := len(buf)
	if r.fpos+int64(size) > rf.size {
		size = int(rf.size - r.fpos)
	}
	if size <= 0 {
		return 0, io.EOF
	}

	c := 0
	for c < size {
		rs := r.segment
		if rs == nil || r.fpos < rs.offset || r.fpos >= rs.offset+rs.size {
			if r.fh != nil {
				r.fh.Close()
				r.fh = nil
			}
			rs = nil
			// C: TAILQ_FOREACH — first segment containing fpos
			for _, s := range rf.segments {
				if r.fpos < s.offset+s.size {
					rs = s
					break
				}
			}
			if rs == nil {
				return c, errors.New("rar: no segment for position")
			}
			r.segment = rs
		}

		w := size - c
		n := min(int64(w), rs.offset+rs.size-r.fpos)

		if r.fh == nil {
			var err error
			r.fh, err = Open(fam, rs.volume.url)
			if err != nil || r.fh == nil {
				return c, errors.New("rar: cannot open volume")
			}
		}

		o := r.fpos - rs.offset + rs.voffset
		if _, err := r.fh.Seek(o, io.SeekStart); err != nil {
			return c, err
		}
		// C: fa_read must return exactly r bytes
		got, err := io.ReadFull(r.fh, buf[c:c+int(n)])
		if got != int(n) {
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			return c, err
		}
		r.fpos += int64(got)
		c += got
	}
	return c, nil
}

// Seek — C: rar_seek (fa_rar.c:771). Only moves rfd_fpos; the volume fh
// is repositioned lazily on next read.
func (r *rarReader) Seek(pos int64, whence int) (int64, error) {
	var np int64
	switch whence {
	case io.SeekStart:
		np = pos
	case io.SeekCurrent:
		np = r.fpos + pos
	case io.SeekEnd:
		np = r.file.size + pos
	default:
		return -1, errors.New("rar: invalid whence")
	}
	if np < 0 {
		return -1, errors.New("rar: negative seek")
	}
	r.fpos = np
	return np, nil
}

// Close — C: rar_close (fa_rar.c:692)
func (r *rarReader) Close() error {
	if r.fh != nil {
		r.fh.Close()
		r.fh = nil
	}
	rarFileUnref(r.file)
	return nil
}

// Open — C: rar_open (fa_rar.c:656). Only stored (uncompressed, method
// '0') entries are readable.
func (p *RARProtocol) Open(url string, extra *OpenExtra) (*Handle, error) {
	rf := rarFileFind(p.fam, stripRARScheme(url))
	if rf == nil {
		return nil, errors.New("Entry not found in archive")
	}
	if rf.typ != ContentFile {
		rarFileUnref(rf)
		return nil, errors.New("Entry is not a file")
	}
	if rf.method != '0' {
		rarFileUnref(rf)
		return nil, errors.New("Compressed files in RAR archives is not supported")
	}
	rd := &rarReader{file: rf}
	return &Handle{
		proto:  p,
		reader: rd,
		seeker: rd,
		url:    url,
		size:   rf.size,
	}, nil
}

// Stat — C: rar_stat (fa_rar.c:815)
func (p *RARProtocol) Stat(url string) (*FileStat, error) {
	rf := rarFileFind(p.fam, stripRARScheme(url))
	if rf == nil {
		return nil, errors.New("Entry not found in archive")
	}
	fs := &FileStat{
		Type:  rf.typ,
		Size:  rf.size,
		MTime: rf.archive.mtime,
	}
	rarFileUnref(rf)
	return fs, nil
}

// rarStatFAP — C: rar_stat as an fap_stat (filename already stripped).
func rarStatFAP(fap *FAProtocol, filename string, flags int) (*FileStat, error) {
	rf := rarFileFind(fap.Opaque.(*FileAccessManager), filename)
	if rf == nil {
		return nil, fapError{code: FAP_NOENT, msg: "Entry not found in archive"}
	}
	st := &FileStat{Type: rf.typ, Size: rf.size, MTime: rf.archive.mtime}
	rarFileUnref(rf)
	return st, nil
}

// rarGetParts — C: rar_get_parts (fa_rar.c:840)
func rarGetParts(fap *FAProtocol, fd *Dir, filename string) error {
	ra, _ := rarArchiveFind(fap.Opaque.(*FileAccessManager), filename)
	if ra == nil {
		return errors.New("rar: archive not found")
	}
	for _, rv := range ra.volumes {
		DirAdd(fd, rv.url, rv.url, ContentFile)
	}
	rarArchiveUnref(ra)
	return nil
}

// populateRARFAProtocol — fill the global "rar" FAProtocol gate with the
// canonical ops (C: fa_protocol_rar's fap_stat/fap_get_parts/fap_reference/
// fap_unreference).
func populateRARFAProtocol(fp *FAProtocol, fam *FileAccessManager) {
	fp.Opaque = fam
	fp.Stat = rarStatFAP
	fp.GetParts = rarGetParts
	fp.Reference = rarReference
	fp.Unreference = rarUnreference
}
