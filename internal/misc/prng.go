package misc

import (
	"crypto/rand"
	"unsafe"
)

// C: typedef struct { uint32_t a; uint32_t b; uint32_t c; uint32_t d; } prng_t;
type Prng struct {
	A uint32
	B uint32
	C uint32
	D uint32
}

// C: #define rot(x,k) (((x)<<(k))|((x)>>(32-(k))))
func rot(x, k uint32) uint32 {
	return (x << k) | (x >> (32 - k))
}

// C: uint32_t prng_get(prng_t *x)
func PrngGet(x *Prng) uint32 {
	e := x.A - rot(x.B, 27)
	x.A = x.B ^ rot(x.C, 17)
	x.B = x.C + x.D
	x.C = x.D + e
	x.D = e + x.A
	return x.D
}

// C: void prng_init(prng_t *x, uint32_t b, uint32_t c)
func PrngSeed(x *Prng, b, c uint32) {
	x.A = 0xf1ea5eed
	x.B = b
	x.C = c
	x.D = b
	for range 20 {
		PrngGet(x)
	}
}

// C: void prng_init2(prng_t *x)
// C: arch_get_random_bytes(x, sizeof(prng_t)) — fills x with OS random bytes.
// Go: crypto/rand.Read provides the same OS-backed entropy on Linux.
func PrngSeed2(x *Prng) {
	buf := unsafe.Slice((*byte)(unsafe.Pointer(x)), unsafe.Sizeof(*x))
	rand.Read(buf)
	for range 20 {
		PrngGet(x)
	}
}
