package misc

import (
	"bytes"
	"compress/flate"
	"fmt"
	"io"
)

// Port of src/misc/gz.c
//
// C uses zlib inflateInit2(&z, -MAX_WBITS) = raw DEFLATE stream.
// Go: compress/flate is the exact equivalent (RFC 1951 raw deflate).

// C: int gz_check(const buf_t *b)
func GzCheck(b *Buf) int {
	in := b.C8()
	if b.bSize < 10 || in[0] != 0x1f || in[1] != 0x8b || in[2] != 0x08 {
		return 0
	}
	return 1
}

// C: buf_t *gz_inflate(buf_t *bin, char *errbuf, size_t errlen)
// Returns (*Buf, error) — errbuf/errlen collapse into error.
func GzInflate(bin *Buf) (*Buf, error) {
	if GzCheck(bin) == 0 {
		bin.Release()
		return nil, fmt.Errorf("Invalid header")
	}

	in := bin.C8()
	inlen := bin.bSize

	if in[3] != 0 {
		bin.Release()
		return nil, fmt.Errorf("Header extensions is not supported")
	}

	in = in[10:]
	inlen -= 10
	_ = inlen

	// C: inflateInit2(&z, -MAX_WBITS)
	r := flate.NewReader(bytes.NewReader(in))

	// C: outlen = inlen * 2, doubled when full — io.ReadAll grows equivalently
	out, err := io.ReadAll(r)
	if err != nil {
		// C: snprintf(errbuf, errlen, "inflate: %s", z.msg)
		r.Close() // C: inflateEnd(&z)
		bin.Release()
		return nil, fmt.Errorf("inflate: %s", err)
	}
	r.Close() // C: inflateEnd(&z)
	bin.Release()

	// C: return buf_create_and_adopt(z.total_out, out, &free)
	b := BufCreate(len(out))
	copy(b.C8(), out)
	return b, nil
}
