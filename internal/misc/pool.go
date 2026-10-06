package misc

// C: src/misc/pool.c, src/misc/pool.h — fixed-size item pool allocator.
//
// 1:1 port. C carves items out of 64KB malloc'd segments using raw
// pointer arithmetic and a free-list threaded through dead items.
// Go replicates this with unsafe.Pointer arithmetic over []byte segments
// (the segments are pinned for the pool's lifetime via p_segments).

import (
	"sync"
	"unsafe"
)

// C: typedef struct pool_item { struct pool_item *link; } pool_item_t;
type poolItem struct {
	link *poolItem
}

// C: typedef struct pool_segment { LIST_ENTRY... } pool_segment_t;
type poolSegment struct {
	psLinkNext  *poolSegment
	psLinkPrev  **poolSegment  // C: LIST_ENTRY prev-pointer
	psAddr      unsafe.Pointer // base of the 64KB allocation
	psAvailSize int
	psAllocSize int
}

// C: typedef struct pool { ... } pool_t;  (pool.h:35-47)
type Pool struct {
	pSegments    *poolSegment // C: LIST_HEAD(pool_segment_list, pool_segment)
	pItemSizeReq int          // Size requested by user
	pItemSize    int          // Actual size of memory allocated
	pFlags       int
	pMutex       sync.Mutex         // C: hts_mutex_t p_mutex
	pItem        *poolItem          // free list head
	segs         [][]unsafe.Pointer // keeps segment backing arrays alive
	// (C relies on malloc not moving)
	pNumOut int
	pName   string
}

// C: #define POOL_ZERO_MEM 0x2
const PoolZeroMem = 0x2

// C: #define ROUND_UP(p, round) ((p + round - 1) & ~(round - 1))
func roundUp(p, round int) int {
	return (p + round - 1) &^ (round - 1)
}

const poolSegmentSize = 65536 // C: size_t size = 65536 in pool_segment_create

// C: static void pool_segment_create(pool_t *p)  (pool.c:69-98)
//
// Allocates one 64KB segment. In C the pool_segment_t header lives at the
// END of the segment; item area = [addr, addr+avail). We keep the same
// layout: Go poolSegment struct is heap-allocated separately but the item
// geometry (avail = size - topsiz) is preserved.
func (p *Pool) poolSegmentCreate() {
	size := poolSegmentSize
	// Back the segment with an unsafe.Pointer array (not []byte): pooled
	// items store Go pointers and the GC must scan their words — a []byte
	// arena is noscan and lets referents die while still referenced,
	// which C malloc semantics never allow.
	addr := make([]unsafe.Pointer, size/int(unsafe.Sizeof(uintptr(0))))
	topsiz := roundUp(int(unsafe.Sizeof(poolSegment{})), int(unsafe.Sizeof(uintptr(0))))

	ps := &poolSegment{
		psAddr:      unsafe.Pointer(&addr[0]),
		psAllocSize: size,
		psAvailSize: size - topsiz,
	}
	// keep the segment backing array alive for the pool lifetime
	p.segs = append(p.segs, addr)

	// thread the free list through the item area (C: pi->link chain)
	var prev *poolItem
	var pi *poolItem
	for i := 0; i <= ps.psAvailSize-p.pItemSize; i += p.pItemSize {
		pi = (*poolItem)(unsafe.Add(ps.psAddr, i))
		pi.link = prev
		prev = pi
	}

	// C: LIST_INSERT_HEAD(&p->p_segments, ps, ps_link)
	ps.psLinkNext = p.pSegments
	if ps.psLinkNext != nil {
		ps.psLinkNext.psLinkPrev = &ps.psLinkNext
	}
	p.pSegments = ps
	ps.psLinkPrev = &p.pSegments

	p.pItem = pi
}

// C: void pool_init(pool_t *p, const char *name, size_t item_size, int flags)
// (pool.c:106-124)
func (p *Pool) PoolSetup(name string, itemSize int, flags int) {
	p.pItemSizeReq = itemSize
	itemSize = roundUp(itemSize, 8)
	p.pName = name
	p.pItemSize = itemSize
	p.pFlags = flags
}

// C: pool_t *pool_create(const char *name, size_t item_size, int flags)
// (pool.c:131-136)
func PoolCreate(name string, itemSize int, flags int) *Pool {
	p := &Pool{}
	p.PoolSetup(name, itemSize, flags)
	return p
}

// C: void pool_destroy(pool_t *p)  (pool.c:184-219)
func (p *Pool) PoolDestroy() {
	p.pSegments = nil
	p.segs = nil // drop segment backing refs (C: free)
}

// C: void *pool_get(pool_t *p)  (pool.c:226-263, non-DEBUG/non-MALLOC path)
func (p *Pool) PoolGet() unsafe.Pointer {
	p.pNumOut++
	pi := p.pItem
	if pi == nil {
		p.poolSegmentCreate()
		pi = p.pItem
	}
	p.pItem = pi.link

	if p.pFlags&PoolZeroMem != 0 {
		// C: memset(pi, 0, p->p_item_size)
		b := unsafe.Slice((*byte)(unsafe.Pointer(pi)), p.pItemSize)
		for i := range b {
			b[i] = 0
		}
	}
	return unsafe.Pointer(pi)
}

// C: void pool_put(pool_t *p, void *ptr)  (pool.c:270+)
func (p *Pool) PoolPut(ptr unsafe.Pointer) {
	pi := (*poolItem)(ptr)
	pi.link = p.pItem
	p.pItem = pi
	p.pNumOut--
}
