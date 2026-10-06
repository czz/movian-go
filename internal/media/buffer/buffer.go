package buffer

import (
	"unsafe"

	mediacore "github.com/czz/movian-go/internal/media/core"
)

// NewMediaBufPool creates a new media buffer pool
func NewMediaBufPool(size int) *mediacore.MediaBufPool {
	return &mediacore.MediaBufPool{
		Buffers: make([]*mediacore.MediaBuf, 0, size),
		Size:    size,
	}
}

// Alloc allocates a media buffer from the pool
func Alloc(pool *mediacore.MediaBufPool, payloadSize int) *mediacore.MediaBuf {
	pool.Mutex.Lock()
	defer pool.Mutex.Unlock()

	if len(pool.Buffers) > 0 {
		mb := pool.Buffers[len(pool.Buffers)-1]
		pool.Buffers = pool.Buffers[:len(pool.Buffers)-1]
		mb.Data = make([]byte, payloadSize)
		mb.Size = payloadSize
		return mb
	}

	return &mediacore.MediaBuf{
		Data: make([]byte, payloadSize),
		Size: payloadSize,
	}
}

// Free returns a media buffer to the pool
func Free(pool *mediacore.MediaBufPool, mb *mediacore.MediaBuf) {
	pool.Mutex.Lock()
	defer pool.Mutex.Unlock()

	if len(pool.Buffers) < pool.Size {
		mb.Data = nil
		mb.Size = 0
		pool.Buffers = append(pool.Buffers, mb)
	}
}

// CopyMetaFromMB copies metadata from a media buffer
func CopyMetaFromMB(mbm *mediacore.MediaBufMeta, mb *mediacore.MediaBuf) {
	mbm.PTS = mb.PTS
	mbm.DTS = mb.DTS
	mbm.Duration = int64(mb.Duration)
	mbm.UserTime = mb.UserTime
	mbm.Epoch = mb.Epoch
	mbm.Sequence = mb.Sequence
	mbm.Flags = mb.Flags
	mbm.AspectOverride = mb.AspectOverride
	mbm.DisableDeinterlacer = mb.DisableDeinterlacer
	mbm.DriveClock = mb.DriveClock
}

// MediaBufFreeLocked frees a media buffer (locked version)
func MediaBufFreeLocked(mp *mediacore.MediaPipe, mb *mediacore.MediaBuf) {
	if mb.Dtor != nil {
		mb.Dtor(mb)
	}
}

// MediaBufFreeUnlocker frees a media buffer (unlocked version)
func MediaBufFreeUnlocker(mp *mediacore.MediaPipe, mb *mediacore.MediaBuf) {
	mp.Mutex.Lock()
	defer mp.Mutex.Unlock()
	MediaBufFreeLocked(mp, mb)
}

// MediaBufAllocLocked allocates a media buffer (locked version)
func MediaBufAllocLocked(mp *mediacore.MediaPipe, payloadSize int) *mediacore.MediaBuf {
	return &mediacore.MediaBuf{
		Data: make([]byte, payloadSize),
		Size: payloadSize,
	}
}

// MediaBufAllocUnlocker allocates a media buffer (unlocked version)
func MediaBufAllocUnlocker(mp *mediacore.MediaPipe, payloadSize int) *mediacore.MediaBuf {
	mp.Mutex.Lock()
	defer mp.Mutex.Unlock()
	return MediaBufAllocLocked(mp, payloadSize)
}

// MediaBufDtorFrameInfo is a destructor for frame info buffers
func MediaBufDtorFrameInfo(mb *mediacore.MediaBuf) {
	if mb.FrameInfo != nil {
		if mb.FrameInfo.RefRelease != nil {
			mb.FrameInfo.RefRelease(unsafe.Pointer(mb.FrameInfo.RefAux))
		}
		mb.FrameInfo = nil
	}
}

// BufferedSize returns the buffered size of a media buffer
func BufferedSize(mb *mediacore.MediaBuf) int {
	if mb.Size > 4096 {
		return mb.Size
	}
	return 4096
}

// GetData returns the data from a media buffer
func GetData(mb *mediacore.MediaBuf) []byte {
	return mb.Data
}

// GetSize returns the size of a media buffer
func GetSize(mb *mediacore.MediaBuf) int {
	return mb.Size
}

// Clone creates a clone of a media buffer
func Clone(mb *mediacore.MediaBuf) *mediacore.MediaBuf {
	newMB := &mediacore.MediaBuf{
		Data:     make([]byte, len(mb.Data)),
		Size:     mb.Size,
		PTS:      mb.PTS,
		DTS:      mb.DTS,
		Duration: mb.Duration,
		UserTime: mb.UserTime,
		Epoch:    mb.Epoch,
		Sequence: mb.Sequence,
		Flags:    mb.Flags,
	}
	copy(newMB.Data, mb.Data)
	return newMB
}

// SetData sets the data for a media buffer
func SetData(mb *mediacore.MediaBuf, data []byte) {
	mb.Data = data
}

// SetSize sets the size for a media buffer
func SetSize(mb *mediacore.MediaBuf, size int) {
	mb.Size = size
}

// SetDestructor sets the destructor for a media buffer
func SetDestructor(mb *mediacore.MediaBuf, dtor func(*mediacore.MediaBuf)) {
	mb.Dtor = dtor
}
