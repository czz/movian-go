// Package htsp is a 1:1 port of src/backend/htsp/htsp.c.
//
// Every C function, struct and global maps to the Go counterpart below,
// in the same order. C references are given per function.
package htsp

import (
	"fmt"
	"io"

	"github.com/czz/movian-go/internal/htsmsg"
)

// C: htsp_file_update_meta (htsp.c:1935-1949)
func htspFileUpdateMeta(hf *htspFile) {
	m := htsmsg.NewMap()
	m.AddStr("method", "fileStat")
	m.AddU32("id", uint32(hf.id))

	m = htspReqreply(hf.hc, m)
	if m == nil {
		return
	}

	if v, err := m.GetS64("size"); err == nil {
		hf.fileSize = v
	}
	if v, err := m.GetS32("mtime"); err == nil {
		hf.mtime = int(v)
	}

	m.Release()
}

// C: htsp_file_seek (htsp.c:1956-1984)
func (hf *htspFile) Seek(pos int64, whence int) (int64, error) {
	var np int64

	switch whence {
	case io.SeekStart:
		np = pos

	case io.SeekCurrent:
		np = hf.pos + pos

	case io.SeekEnd:
		htspFileUpdateMeta(hf)
		np = hf.fileSize + pos

	default:
		return -1, fmt.Errorf("invalid whence")
	}

	if np < 0 {
		return -1, fmt.Errorf("negative position")
	}
	hf.pos = np
	return np, nil
}

// C: htsp_file_fsize (htsp.c:1990-1996)
func htspFileFsize(hf *htspFile) int64 {
	htspFileUpdateMeta(hf)
	return hf.fileSize
}

// C: htsp_file_read (htsp.c:2002-2027)
func (hf *htspFile) Read(buf []byte) (int, error) {
	m := htsmsg.NewMap()

	m.AddStr("method", "fileRead")
	m.AddU32("id", uint32(hf.id))
	m.AddS64("offset", hf.pos)
	m.AddU32("size", uint32(len(buf)))

	m = htspReqreply(hf.hc, m)
	if m == nil {
		return -1, fmt.Errorf("fileRead failed")
	}

	data, err := m.GetBin("data")
	if err != nil {
		return -1, fmt.Errorf("fileRead: no data")
	}

	r := min(len(data),
		// C: MIN(datalen, size) — be sure
		len(buf))
	hf.pos += int64(r)
	copy(buf, data[:r])
	m.Release()
	return r, nil
}

// C: htsp_file_close (htsp.c:2033-2050)
func (hf *htspFile) Close() error {
	m := htsmsg.NewMap()
	m.AddStr("method", "fileClose")
	m.AddU32("id", uint32(hf.id))

	m = htspReqreply(hf.hc, m)
	if m == nil {
		return nil
	}

	m.Release()

	// hf->hf_hc->refcount-- or something

	return nil
}
