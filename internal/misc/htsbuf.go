package misc

import "fmt"

// HtsbufQueue is a byte queue of preallocated chunks.
// C: htsbuf_queue_t (htsmsg/htsbuf.h:39-51) — TAILQ of htsbuf_data
// chunks; Go keeps a slice of owned byte slices.
type HtsbufQueue struct {
	chunks  [][]byte // C: hq_q TAILQ of htsbuf_data
	len     int      // C: hq_size (total queued bytes)
	maxSize int      // C: hq_maxsize (0 = unlimited)
}

// HtsbufQueueSetup resets the queue (C: htsbuf_queue_init).
func (hq *HtsbufQueue) HtsbufQueueSetup(maxSize int) {
	hq.chunks = nil
	hq.len = 0
	hq.maxSize = maxSize
}

// Append copies len(buf) bytes into the queue (C: htsbuf_append).
func (hq *HtsbufQueue) Append(buf []byte) {
	if len(buf) == 0 {
		return
	}
	c := make([]byte, len(buf))
	copy(c, buf)
	hq.chunks = append(hq.chunks, c)
	hq.len += len(buf)
}

// AppendPrealloc adopts buf without copying (C: htsbuf_append_prealloc —
// the caller-allocated buffer is taken over by the queue).
func (hq *HtsbufQueue) AppendPrealloc(buf []byte) {
	if len(buf) == 0 {
		return
	}
	hq.chunks = append(hq.chunks, buf)
	hq.len += len(buf)
}

// AppendByte appends a single byte (C: htsbuf_append_byte).
func (hq *HtsbufQueue) AppendByte(b byte) {
	hq.chunks = append(hq.chunks, []byte{b})
	hq.len++
}

// Len returns the total queued bytes (C: hq->hq_size).
func (hq *HtsbufQueue) Len() int { return hq.len }

// Bytes flattens the queue into a single slice.
func (hq *HtsbufQueue) Bytes() []byte {
	out := make([]byte, 0, hq.len)
	for _, c := range hq.chunks {
		out = append(out, c...)
	}
	return out
}

// String returns the queue contents as a string (C: htsbuf_to_string —
// C returns a malloc'd NUL-terminated string; Go returns a value).
func (hq *HtsbufQueue) String() string { return string(hq.Bytes()) }

// htsbufAppendAndEscapeURL0 — C: htsbuf_append_and_escape_url0
// (htsbuf.c:338). Keeps [0-9A-Za-z_~.-]; escapes everything else as
// %XX (NOT url_escape/URL_ESCAPE_PARAM — a space becomes %20, not '+').
func htsbufAppendAndEscapeURL0(s string) []byte {
	const hexchars = "0123456789ABCDEF"
	var out []byte
	for i := range len(s) {
		c := s[i]
		if (c >= '0' && c <= '9') ||
			(c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			c == '_' || c == '~' || c == '.' || c == '-' {
			out = append(out, c)
		} else {
			out = append(out, '%', hexchars[(c>>4)&0xf], hexchars[c&0xf])
		}
	}
	return out
}

// AppendAndEscapeURL appends s URL-escaped (C: htsbuf_append_and_escape_url).
func (hq *HtsbufQueue) AppendAndEscapeURL(s string) {
	if len(s) == 0 {
		return // C: if(e == s) return
	}
	hq.Append(htsbufAppendAndEscapeURL0(s))
}

// AppendAndEscapeURLLen appends the first length bytes of s URL-escaped
// (C: htsbuf_append_and_escape_url_len).
func (hq *HtsbufQueue) AppendAndEscapeURLLen(s string, length int) {
	hq.AppendAndEscapeURL(s[:length])
}

// AppendQ appends all of src and empties it (C: htsbuf_appendq).
func (hq *HtsbufQueue) AppendQ(src *HtsbufQueue) {
	hq.chunks = append(hq.chunks, src.chunks...)
	hq.len += src.len
	src.chunks = nil
	src.len = 0
}

// QPrintf appends a formatted string (C: htsbuf_qprintf).
func (hq *HtsbufQueue) QPrintf(format string, args ...any) {
	hq.Append([]byte(fmt.Sprintf(format, args...)))
}

// Read removes up to length bytes from the head into buf and returns
// the count (C: htsbuf_read).
func (hq *HtsbufQueue) Read(buf []byte, length int) int {
	r := 0
	for r < length && len(hq.chunks) > 0 {
		c := hq.chunks[0]
		n := min(len(c), length-r)
		copy(buf[r:r+n], c[:n])
		r += n
		if n == len(c) {
			hq.chunks = hq.chunks[1:]
		} else {
			hq.chunks[0] = c[n:]
		}
	}
	hq.len -= r
	return r
}

// Peek copies up to length bytes from the head without consuming them
// (C: htsbuf_peek).
func (hq *HtsbufQueue) Peek(buf []byte, length int) int {
	r := 0
	for i := 0; r < length && i < len(hq.chunks); i++ {
		n := copy(buf[r:length], hq.chunks[i])
		r += n
	}
	return r
}

// Drop discards up to length bytes from the head and returns the count
// (C: htsbuf_drop).
func (hq *HtsbufQueue) Drop(length int) int {
	r := 0
	for r < length && len(hq.chunks) > 0 {
		c := hq.chunks[0]
		n := len(c)
		if n > length-r {
			n = length - r
			hq.chunks[0] = c[n:]
		} else {
			hq.chunks = hq.chunks[1:]
		}
		r += n
	}
	hq.len -= r
	return r
}

// Find returns the offset of the first byte v in the queue, or -1
// (C: htsbuf_find).
func (hq *HtsbufQueue) Find(v byte) int {
	off := 0
	for _, c := range hq.chunks {
		for i, b := range c {
			if b == v {
				return off + i
			}
		}
		off += len(c)
	}
	return -1
}

// Flush empties the queue (C: htsbuf_queue_flush).
func (hq *HtsbufQueue) Flush() {
	hq.chunks = nil
	hq.len = 0
}

// TailSpare returns the unused capacity of the tail chunk
// (C: hd->hd_data_size - hd->hd_data_len on TAILQ_LAST). Only nonzero
// when the tail was appended with spare capacity (AppendPrealloc of a
// slice with cap > len).
func (hq *HtsbufQueue) TailSpare() int {
	if len(hq.chunks) == 0 {
		return 0
	}
	last := hq.chunks[len(hq.chunks)-1]
	return cap(last) - len(last)
}

// FillTail extends the tail chunk into its spare capacity
// (C: hd->hd_data_len += c; hq->hq_size += c in tcp_read_into_spill).
func (hq *HtsbufQueue) FillTail(buf []byte) {
	if len(hq.chunks) == 0 || len(buf) == 0 {
		return
	}
	last := hq.chunks[len(hq.chunks)-1]
	n := min(len(buf), cap(last)-len(last))
	old := len(last)
	last = last[:old+n]
	copy(last[old:], buf[:n])
	hq.chunks[len(hq.chunks)-1] = last
	hq.len += n
}

// PopHead removes and returns the head chunk, or nil when empty
// (C: TAILQ_FIRST + TAILQ_REMOVE of htsbuf_data; hq_size updated).
func (hq *HtsbufQueue) PopHead() []byte {
	if len(hq.chunks) == 0 {
		return nil
	}
	c := hq.chunks[0]
	hq.chunks = hq.chunks[1:]
	hq.len -= len(c)
	return c
}

// EachChunk calls fn on each chunk without removing it
// (C: TAILQ_FOREACH over hq_q).
func (hq *HtsbufQueue) EachChunk(fn func([]byte)) {
	for _, c := range hq.chunks {
		fn(c)
	}
}
