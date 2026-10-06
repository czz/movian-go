// Package cenc implements ISO/IEC 23001-7 Common Encryption primitives:
// per-sample AES-128-CTR (cenc/cens) decryption and subsample maps.
// New in Go — no C counterpart; this is the decryption layer that sits
// between the demuxer (AVPacket + ENCRYPTION_INFO side data) and the
// decoder. Keys are supplied by a CDM session (pkg/drm/cdm); the cenc
// layer itself is key-system agnostic.
package cenc

import (
	"crypto/aes"
	"crypto/cipher"
	"fmt"
)

// Scheme is the ISO BMFF encryption scheme fourcc.
type Scheme uint32

const (
	SchemeCenc Scheme = 'c'<<24 | 'e'<<16 | 'n'<<8 | 'c' // AES-CTR, full subsamples
	SchemeCbc1 Scheme = 'c'<<24 | 'b'<<16 | 'c'<<8 | '1' // AES-CBC, full subsamples
	SchemeCens Scheme = 'c'<<24 | 'e'<<16 | 'n'<<8 | 's' // CTR + pattern
	SchemeCbcs Scheme = 'c'<<24 | 'b'<<16 | 'c'<<8 | 's' // CBC + pattern
)

func (s Scheme) String() string {
	return string([]byte{byte(s >> 24), byte(s >> 16), byte(s >> 8), byte(s)})
}

// Subsample is one (clear, protected) byte range of a sample.
type Subsample struct {
	Clear     uint32
	Protected uint32
}

// SampleInfo is the decryption metadata for one media sample
// (AVEncryptionInfo, libavutil/encryption_info.h).
type SampleInfo struct {
	Scheme         Scheme
	CryptByteBlock uint32 // pattern: 16-byte blocks encrypted
	SkipByteBlock  uint32 // pattern: 16-byte blocks clear
	KeyID          []byte // 16-byte default_KID
	IV             []byte // 8 or 16 bytes; zero-padded to 16
	Subsamples     []Subsample
}

// expandIV zero-pads an 8-byte IV to the 16-byte CTR/CBC block.
// 8-byte IVs occupy the high 8 bytes; the low 8 are the block counter.
func expandIV(iv []byte) ([16]byte, error) {
	var b [16]byte
	switch len(iv) {
	case 8, 16:
		copy(b[:], iv)
		return b, nil
	}
	return b, fmt.Errorf("cenc: bad IV size %d", len(iv))
}

// DecryptSample decrypts src into dst using a 16-byte AES key.
// dst may alias src (in-place decryption of the demuxer buffer).
// If info.Subsamples is empty the whole sample is protected.
func DecryptSample(dst, src, key []byte, info *SampleInfo) error {
	switch info.Scheme {
	case SchemeCenc, SchemeCens:
		return decryptCTR(dst, src, key, info)
	case SchemeCbc1, SchemeCbcs:
		return decryptCBC(dst, src, key, info)
	}
	return fmt.Errorf("cenc: unsupported scheme %s", info.Scheme)
}

// decryptCTR implements 'cenc'/'cens': one continuous AES-CTR keystream
// across all protected ranges; clear ranges do not consume keystream.
func decryptCTR(dst, src, key []byte, info *SampleInfo) error {
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	iv, err := expandIV(info.IV)
	if err != nil {
		return err
	}
	// Pattern encryption ('cens'): only crypt:(skip+crypt) block groups
	// inside protected data are encrypted. Fold the pattern into the
	// subsample walk by splitting protected ranges into crypt/skip runs.
	stream := cipher.NewCTR(block, iv[:])
	return applySubsamples(dst, src, info, func(d, s []byte) {
		if info.Scheme == SchemeCens && info.SkipByteBlock > 0 {
			cryptBlocks := int(info.CryptByteBlock)
			skipBlocks := int(info.SkipByteBlock)
			applyPattern(d, s, cryptBlocks, skipBlocks, stream.XORKeyStream)
		} else {
			stream.XORKeyStream(d, s)
		}
	})
}

// decryptCBC implements 'cbc1'/'cbcs': AES-CBC with the sample IV.
// Cipher state chains across subsamples within the sample.
func decryptCBC(dst, src, key []byte, info *SampleInfo) error {
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	iv, err := expandIV(info.IV)
	if err != nil {
		return err
	}
	dec := cipher.NewCBCDecrypter(block, iv[:])
	return applySubsamples(dst, src, info, func(d, s []byte) {
		if info.Scheme == SchemeCbcs && info.SkipByteBlock > 0 {
			cryptBlocks := int(info.CryptByteBlock)
			skipBlocks := int(info.SkipByteBlock)
			applyPattern(d, s, cryptBlocks, skipBlocks, func(dd, ss []byte) {
				// CBC needs whole blocks; cbc1/cbcs encrypted ranges are
				// block-aligned by construction.
				n := len(ss) &^ (aes.BlockSize - 1)
				if n > 0 {
					dec.CryptBlocks(dd[:n], ss[:n])
				}
				copy(dd[n:], ss[n:])
			})
			return
		}
		n := len(s) &^ (aes.BlockSize - 1)
		if n > 0 {
			dec.CryptBlocks(d[:n], s[:n])
		}
		copy(d[n:], s[n:])
	})
}

// applySubsamples walks (clear, protected) ranges; crypt is invoked on
// each protected slice. Clear ranges are copied verbatim.
func applySubsamples(dst, src []byte, info *SampleInfo, crypt func(d, s []byte)) error {
	if len(info.Subsamples) == 0 {
		copy(dst, src)
		crypt(dst, src)
		return nil
	}
	off := 0
	for _, ss := range info.Subsamples {
		clr, prot := int(ss.Clear), int(ss.Protected)
		if off+clr+prot > len(src) {
			return fmt.Errorf("cenc: subsample map exceeds sample size")
		}
		copy(dst[off:off+clr], src[off:off+clr])
		crypt(dst[off+clr:off+clr+prot], src[off+clr:off+clr+prot])
		off += clr + prot
	}
	// Trailing bytes after the last subsample are clear.
	copy(dst[off:], src[off:])
	return nil
}

// applyPattern splits s into crypt:skip groups of 16-byte blocks and
// runs cryptFn only on the crypt groups (cbcs/cens pattern mode).
func applyPattern(d, s []byte, cryptBlocks, skipBlocks int, cryptFn func(d, s []byte)) {
	for len(s) > 0 {
		cryptLen := min(cryptBlocks*aes.BlockSize, len(s))
		cryptFn(d[:cryptLen], s[:cryptLen])
		d, s = d[cryptLen:], s[cryptLen:]
		skipLen := min(skipBlocks*aes.BlockSize, len(s))
		copy(d[:skipLen], s[:skipLen])
		d, s = d[skipLen:], s[skipLen:]
	}
}
