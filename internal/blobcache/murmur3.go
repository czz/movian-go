package blobcache

import (
	"encoding/binary"
	"unsafe"
)

// MurHash3_32 is a 1:1 port of MurHash3_32 from misc/murmur3.c.
// C: murmur3.c:72-123
func MurHash3_32(key []byte, seed uint32) uint32 {
	len := len(key)
	nblocks := len / 4

	h1 := seed

	const c1 uint32 = 0xcc9e2d51
	const c2 uint32 = 0x1b873593

	// body
	for i := range nblocks {
		k1 := binary.LittleEndian.Uint32(key[i*4:])
		k1 *= c1
		k1 = rotl32(k1, 15)
		k1 *= c2
		h1 ^= k1
		h1 = rotl32(h1, 13)
		h1 = h1*5 + 0xe6546b64
	}

	// tail
	tail := key[nblocks*4:]
	var k1 uint32
	switch len & 3 {
	case 3:
		k1 ^= uint32(tail[2]) << 16
		fallthrough
	case 2:
		k1 ^= uint32(tail[1]) << 8
		fallthrough
	case 1:
		k1 ^= uint32(tail[0])
		k1 *= c1
		k1 = rotl32(k1, 15)
		k1 *= c2
		h1 ^= k1
	}

	// finalization
	h1 ^= uint32(len)
	h1 = fmix32(h1)

	return h1
}

// rotl32 rotates x left by r bits.
// C: murmur3.c:34-37
func rotl32(x uint32, r int8) uint32 {
	return (x << r) | (x >> (32 - r))
}

// fmix32 finalization mix.
// C: murmur3.c:59-68
func fmix32(h uint32) uint32 {
	h ^= h >> 16
	h *= 0x85ebca6b
	h ^= h >> 13
	h *= 0xc2b2ae35
	h ^= h >> 16
	return h
}

// Ensure unsafe is imported for potential future use (matching C pointer arithmetic)
var _ = unsafe.Pointer(nil)
