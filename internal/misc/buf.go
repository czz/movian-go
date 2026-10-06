package misc

import (
	"sync/atomic"
	"unsafe"
)

// Port of src/misc/buf.h + src/misc/buf.c
//
// C: typedef struct buf {
//      atomic_t b_refcount;
//      size_t b_size;
//      void *b_ptr;
//      void (*b_free)(void *);
//      rstr_t *b_content_type;
//      uint8_t b_content[0];
//    } buf_t;

type Buf struct {
	bRefcount    atomic.Int32         // C: atomic_t b_refcount
	bSize        int                  // C: size_t b_size
	bPtr         unsafe.Pointer       // C: void *b_ptr — points into bContent or external
	bFree        func(unsafe.Pointer) // C: void (*b_free)(void *)
	bContentType *Rstr                // C: rstr_t *b_content_type
	bContent     []byte               // C: uint8_t b_content[0] (flexible array)
}

// C: buf_data(buf)
func (b *Buf) Data() unsafe.Pointer { return b.bPtr }

// C: buf_cstr(buf)
func (b *Buf) Cstr() *byte { return (*byte)(b.bPtr) }

// C: static __inline char *buf_str(buf_t *b)
func (b *Buf) Str() *byte {
	// C: assert(atomic_get(&b->b_refcount) == 1)
	if b.bRefcount.Load() != 1 {
		panic("buf_str: refcount != 1")
	}
	return (*byte)(b.bPtr)
}

// C: buf_len(buf) / buf_size(buf)
func (b *Buf) Len() int  { return b.bSize }
func (b *Buf) Size() int { return b.bSize }

// C: buf_c8(buf) — byte-slice view of b_ptr[0:b_size]
func (b *Buf) C8() []byte {
	return unsafe.Slice((*byte)(b.bPtr), b.bSize)
}

// C: static __inline buf_t *buf_retain(buf_t *b)
func (b *Buf) Retain() *Buf {
	b.bRefcount.Add(1)
	return b
}

// C: void buf_release(buf_t *b)
func (b *Buf) Release() {
	if b != nil && b.bRefcount.Add(-1) == 0 {
		if b.bFree != nil {
			b.bFree(b.bPtr)
		}
		RstrRelease(b.bContentType)
		// C: free(b)
	}
}

// C: buf_t *buf_make_writable(buf_t *b)
func BufMakeWritable(b *Buf) *Buf {
	if b.bRefcount.Load() == 1 {
		return b
	}

	b2 := BufCreateAndCopy(b.bSize, b.C8())

	b2.bContentType = RstrDup(b.bContentType)
	b.Release()
	return b2
}

// C: buf_t *buf_create_and_copy(size_t size, const void *data)
func BufCreateAndCopy(size int, data []byte) *Buf {
	b := BufCreate(size)
	if b != nil {
		copy(b.C8(), data[:size])
	}
	return b
}

// C: buf_t *buf_create(size_t size)
func BufCreate(size int) *Buf {
	// C: mymalloc(sizeof(buf_t) + size + 1); b_content[size] = 0
	content := make([]byte, size+1)
	b := &Buf{
		bSize:    size,
		bContent: content,
	}
	b.bRefcount.Store(1)
	b.bPtr = unsafe.Pointer(&content[0])
	b.bFree = nil
	b.bContentType = nil
	content[size] = 0
	return b
}

// C: buf_t *buf_create_and_adopt(size_t size, void *data, void (*freefn)(void *))
func BufCreateAndAdopt(size int, data unsafe.Pointer, freefn func(unsafe.Pointer)) *Buf {
	b := &Buf{}
	b.bRefcount.Store(1)
	b.bSize = size
	b.bPtr = data
	b.bFree = freefn
	b.bContentType = nil
	return b
}

// SetContentType — C: b->b_content_type = rstr_alloc(s)
func (b *Buf) SetContentType(s string) {
	RstrSet(&b.bContentType, RstrAllocStr(s))
}

// GetContentType — C: rstr_get(b->b_content_type) ("" when unset)
func (b *Buf) GetContentType() string {
	return RstrGet(b.bContentType)
}
