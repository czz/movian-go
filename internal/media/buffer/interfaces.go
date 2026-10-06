package buffer

// BufferProvider defines media buffer operations.
type BufferProvider interface {
	Allocate(size int) (any, error)
	Release(buf any)
	GetCapacity(buf any) int
}

// BufferQueue defines buffer queue operations.
type BufferQueue interface {
	Enqueue(buf any) error
	Dequeue() (any, error)
	Flush()
	Count() int
	IsEmpty() bool
}

// BufferPool defines buffer pool operations for reuse.
type BufferPool interface {
	Get(size int) any
	Put(buf any)
	Trim(threshold int)
	Stats() any
}
