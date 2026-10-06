// Canonical port of src/fileaccess/fa_bwlimit.c — bandwidth-limiting
// read wrapper: each read sleeps for the bytes-per-second budget deficit,
// carrying elapsed-time spill between reads.
package fileaccess

import (
	"io"
	"time"
)

// bwlimitFile — C: bwlimit_t (fa_bwlimit.c:26-31)
type bwlimitFile struct {
	src   *Handle // C: s_src
	bps   int64   // C: s_bps — bytes per second
	spill int64   // C: s_spill
}

// faProtocolBwlimit — C: fa_protocol_bwlimit (fa_bwlimit.c:85)
var faProtocolBwlimit = &FAProtocol{Name: "bwlimit"}

// Read — C: bwlimit_read (fa_bwlimit.c:62-79). Times the source read and
// sleeps off the difference between elapsed time and the bps budget;
// a fast read banks its elapsed time as positive spill for the next call
// (canonical quirk preserved).
func (s *bwlimitFile) Read(buf []byte) (int, error) {
	ts := time.Now()
	n, err := s.src.Read(buf)
	elapsed := time.Since(ts).Microseconds()

	delay := s.spill + int64(n)*1000000/s.bps - elapsed
	if delay > 0 {
		time.Sleep(time.Duration(delay) * time.Microsecond)
		s.spill = 0
	} else {
		s.spill = elapsed
	}
	return n, err
}

// Seek — C: bwlimit_seek (fa_bwlimit.c:47-52) — passthrough.
func (s *bwlimitFile) Seek(pos int64, whence int) (int64, error) {
	return s.src.Seek(pos, whence)
}

// Close — C: bwlimit_close (fa_bwlimit.c:36-41).
func (s *bwlimitFile) Close() error {
	return s.src.Close()
}

// FABwlimitOpen — C: fa_bwlimit_open (fa_bwlimit.c:98-105). Wraps fh so
// reads are throttled to bps bytes/second.
func FABwlimitOpen(fh *Handle, bps int) *Handle {
	s := &bwlimitFile{src: fh, bps: int64(bps)}
	h := &Handle{
		fap:    faProtocolBwlimit,
		reader: s,
		seeker: s,
		url:    fh.url,
		size:   -1,
	}
	// C: bwlimit_fsize → fa_fsize(s_src)
	h.sizer = func() int64 { return s.src.Size() }
	return h
}

var _ io.Seeker = (*bwlimitFile)(nil)
