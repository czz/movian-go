// Canonical port of src/fileaccess/fa_aes.c — AES-128-CBC decrypting
// handle wrapper (fa_aescbc_open). Buffered multi-block decryption that
// holds back one block so the final block's padding can be stripped at
// EOF (aescbc_read's `outlen -= outbuffer[outlen-1]`).
package fileaccess

import (
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"io"
)

// C: MAX_BUFFER_BLOCKS / BLOCKSIZE (fa_aes.c)
const (
	aesMaxBufferBlocks = 150
	aesBlockSize       = 16
)

// aesFH — C: aes_fh_t (fa_aes.c:31)
type aesFH struct {
	src *Handle // C: a->src
	iv  [16]byte
	b   cipher.Block // C: a->aes (av_aes_alloc + av_aes_init 128,decrypt)

	inbuffer  [aesBlockSize * aesMaxBufferBlocks]byte
	outbuffer [aesBlockSize * aesMaxBufferBlocks]byte
	outptr    []byte // C: a->outptr — view into outbuffer
	inlen     int
	inlenUsed int
	outlen    int
	eof       bool
}

// Close — C: aes_close (fa_aes.c:47)
func (a *aesFH) Close() error {
	return a.src.Close()
}

// Read — C: aescbc_read (fa_aes.c:75). Serves out of the decrypted
// outbuffer; refills by decrypting all complete blocks minus one held
// back for the padding strip. At EOF strips `outbuffer[outlen-1]` bytes.
func (a *aesFH) Read(buf []byte) (int, error) {
	for {
		if a.outlen > 0 {
			size := min(a.outlen, len(buf))
			copy(buf, a.outptr[:size])
			a.outptr = a.outptr[size:]
			a.outlen -= size
			return size, nil
		}

		for a.inlen-a.inlenUsed < 2*aesBlockSize {
			n, err := a.src.Read(a.inbuffer[a.inlen:])
			if n <= 0 || err != nil {
				a.eof = true
				break
			}
			a.inlen += n
		}

		blocks := (a.inlen - a.inlenUsed) / aesBlockSize
		if blocks == 0 {
			return 0, io.EOF
		}
		if !a.eof {
			blocks--
		}

		// C: av_aes_crypt(aes, outbuffer, inbuffer+inlen_used, blocks,
		//                 iv, decrypt=1) — AES-CBC over `blocks` blocks.
		for i := range blocks {
			src := a.inbuffer[a.inlenUsed+i*aesBlockSize : a.inlenUsed+(i+1)*aesBlockSize]
			dst := a.outbuffer[i*aesBlockSize : (i+1)*aesBlockSize]
			a.b.Decrypt(dst, src)
			for j := range aesBlockSize {
				dst[j] ^= a.iv[j]
			}
			copy(a.iv[:], src)
		}

		a.outlen = aesBlockSize * blocks
		a.outptr = a.outbuffer[:a.outlen]
		a.inlenUsed += aesBlockSize * blocks

		if a.inlenUsed >= len(a.inbuffer)/2 {
			copy(a.inbuffer[:], a.inbuffer[a.inlenUsed:a.inlen])
			a.inlen -= a.inlenUsed
			a.inlenUsed = 0
		}
		if a.eof {
			a.outlen -= int(a.outbuffer[a.outlen-1])
		}
	}
}

// faProtocolAESCBC — C: fa_protocol_aescbc (fa_aes.c:134)
var faProtocolAESCBC = &FAProtocol{Name: "aescbc"}

// FAAescbcOpen — C: fa_aescbc_open (fa_aes.c:147). Returns a new handle
// decrypting `fa` with the given IV/key (seek and fsize are -1 — CBC
// streams are forward-only).
func FAAescbcOpen(fa *Handle, iv, key []byte) *Handle {
	if len(iv) < aesBlockSize {
		return nil // C memcpys 16 bytes — guard instead of OOB read
	}
	a := &aesFH{src: fa}
	copy(a.iv[:], iv)
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil
	}
	a.b = b
	return &Handle{
		fap:    faProtocolAESCBC,
		reader: a,
		seeker: aesNopSeeker{},
		url:    fa.url,
		size:   -1, // C: aescbc_fsize → -1
	}
}

// aesNopSeeker — C: aescbc_seek → -1 ("Seeking in CBC not possible")
type aesNopSeeker struct{}

func (aesNopSeeker) Seek(int64, int) (int64, error) {
	return -1, errors.New("Seeking in CBC not possible")
}
