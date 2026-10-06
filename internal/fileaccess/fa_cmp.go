// Canonical port of src/fileaccess/fa_cmp.c — debug wrapper comparing
// every read/seek/fsize on a handle against a local reference file;
// exits(1) on divergence (fatal verification, not a recoverable error).
package fileaccess

import (
	"fmt"
	"io"
	"os"
)

// cmpFH — C: cmp_t (fa_cmp.c:33)
type cmpFH struct {
	src *Handle  // C: s_src
	fd  *os.File // C: s_fd
}

// faProtocolCmp — C: fa_protocol_cmp (fa_cmp.c:139)
var faProtocolCmp = &FAProtocol{Name: "cmp"}

// cmpFatal — C: TRACE(TRACE_ERROR, "FACMP", ...) + exit(1)
func cmpFatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "FACMP: "+format+"\n", args...)
	os.Exit(1)
}

// Close — C: cmp_close (fa_cmp.c:43)
func (s *cmpFH) Close() error {
	err := s.src.Close()
	s.fd.Close()
	return err
}

// Seek — C: cmp_seek (fa_cmp.c:55) — divergent positions are fatal.
func (s *cmpFH) Seek(pos int64, whence int) (int64, error) {
	r1, err1 := s.src.Seek(pos, whence)
	r2, err2 := s.fd.Seek(pos, whence)
	if err1 != nil {
		r1 = -1
	}
	if err2 != nil {
		r2 = -1
	}
	if r1 != r2 {
		cmpFatal("seek(%d, %d) failed fa:%d local:%d", pos, whence, r1, r2)
	}
	return r1, nil
}

// Read — C: cmp_read (fa_cmp.c:98) — count or byte divergence is fatal.
func (s *cmpFH) Read(buf []byte) (int, error) {
	size := len(buf)
	r1, err1 := s.src.Read(buf)

	tmp := make([]byte, size)
	pos, _ := s.fd.Seek(0, io.SeekCurrent)
	r2, _ := s.fd.Read(tmp)

	if err1 != nil {
		r1 = -1
	}
	if r1 != r2 {
		cmpFatal("read(%d) @ %d failed fa:%d local:%d", size, pos, r1, r2)
	}
	for i := range r1 {
		if buf[i] != tmp[i] {
			cmpFatal("Read(%d) mismatch @ %d + %d got %02x expected %02x",
				size, pos, i, buf[i], tmp[i])
		}
	}
	return r1, err1
}

// FACmpOpen — C: fa_cmp_open (fa_cmp.c:153). If the reference file
// can't be opened, returns the unwrapped source handle.
func FACmpOpen(fa *Handle, fname string) *Handle {
	fd, err := os.Open(fname)
	if err != nil {
		return fa
	}
	fmt.Fprintf(os.Stderr, "FACMP: Using %s as reference for compare\n", fname)
	s := &cmpFH{src: fa, fd: fd}
	h := &Handle{
		fap:    faProtocolCmp,
		reader: s,
		seeker: s,
		url:    fa.url,
	}
	// C: fap_fsize = cmp_fsize
	h.sizer = s.Size
	return h
}

// Size — C: cmp_fsize (fa_cmp.c:76) — size mismatch is fatal.
func (s *cmpFH) Size() int64 {
	r1 := s.src.Size()
	var st, err = s.fd.Stat()
	if err != nil {
		cmpFatal("Stat failed -- %s", err)
	}
	if st.Size() != r1 {
		cmpFatal("fsize() failed fa:%d local:%d", r1, st.Size())
	}
	return r1
}

// ==================== FileAccess-level comparator ====================
// Go-side convenience wrapper (predates the canonical fa_cmp port):
// delegates every FileAccess op to the underlying implementation and,
// for reads/seeks/size, cross-checks against a local reference file.
