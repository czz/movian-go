// MD4 message digest (RFC 1320) — local implementation standing in for
// C's libcrypto md4_init/md4_update/md4_final (fa_nativesmb.c NTLM_hash).
package smb

import (
	"encoding/binary"
	"math/bits"
)

// md4Sum computes the 16-byte MD4 digest of data.
func md4Sum(data []byte) [16]byte {
	var state [4]uint32
	state[0], state[1], state[2], state[3] = 0x67452301, 0xefcdab89, 0x98badcfe, 0x10325476

	// Padded length: data + 0x80 + zeros + 8-byte LE bit length
	padded := make([]byte, 0, len(data)+72)
	padded = append(padded, data...)
	padded = append(padded, 0x80)
	for len(padded)%64 != 56 {
		padded = append(padded, 0)
	}
	var bitlen [8]byte
	binary.LittleEndian.PutUint64(bitlen[:], uint64(len(data))*8)
	padded = append(padded, bitlen[:]...)

	var x [16]uint32
	for off := 0; off < len(padded); off += 64 {
		for i := range 16 {
			x[i] = binary.LittleEndian.Uint32(padded[off+i*4:])
		}
		a, b, c, d := state[0], state[1], state[2], state[3]

		f := func(x, y, z uint32) uint32 { return (x & y) | (^x & z) }
		g := func(x, y, z uint32) uint32 { return (x & y) | (x & z) | (y & z) }
		h := func(x, y, z uint32) uint32 { return x ^ y ^ z }

		// Round 1
		for _, s := range [][4]int{
			{0, 1, 2, 3}, {4, 5, 6, 7}, {8, 9, 10, 11}, {12, 13, 14, 15},
		} {
			a += f(b, c, d) + x[s[0]]
			a = bits.RotateLeft32(a, 3)
			d += f(a, b, c) + x[s[1]]
			d = bits.RotateLeft32(d, 7)
			c += f(d, a, b) + x[s[2]]
			c = bits.RotateLeft32(c, 11)
			b += f(c, d, a) + x[s[3]]
			b = bits.RotateLeft32(b, 19)
		}
		// Round 2
		for _, s := range [][4]int{
			{0, 4, 8, 12}, {1, 5, 9, 13}, {2, 6, 10, 14}, {3, 7, 11, 15},
		} {
			a += g(b, c, d) + x[s[0]] + 0x5a827999
			a = bits.RotateLeft32(a, 3)
			d += g(a, b, c) + x[s[1]] + 0x5a827999
			d = bits.RotateLeft32(d, 5)
			c += g(d, a, b) + x[s[2]] + 0x5a827999
			c = bits.RotateLeft32(c, 9)
			b += g(c, d, a) + x[s[3]] + 0x5a827999
			b = bits.RotateLeft32(b, 13)
		}
		// Round 3
		for _, s := range [][4]int{
			{0, 8, 4, 12}, {2, 10, 6, 14}, {1, 9, 5, 13}, {3, 11, 7, 15},
		} {
			a += h(b, c, d) + x[s[0]] + 0x6ed9eba1
			a = bits.RotateLeft32(a, 3)
			d += h(a, b, c) + x[s[1]] + 0x6ed9eba1
			d = bits.RotateLeft32(d, 9)
			c += h(d, a, b) + x[s[2]] + 0x6ed9eba1
			c = bits.RotateLeft32(c, 11)
			b += h(c, d, a) + x[s[3]] + 0x6ed9eba1
			b = bits.RotateLeft32(b, 15)
		}

		state[0] += a
		state[1] += b
		state[2] += c
		state[3] += d
	}

	var out [16]byte
	for i := range 4 {
		binary.LittleEndian.PutUint32(out[i*4:], state[i])
	}
	return out
}
