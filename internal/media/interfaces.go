package media

// MediaProvider defines media source operations.
type MediaProvider interface {
	Open(url string) (any, error)
	Probe(url string) (any, error)
	Close(source any) error
}

// MediaSource defines media source operations.
type MediaSource interface {
	ReadPacket() (any, error)
	Seek(offset int64, whence int) (int64, error)
	GetDuration() int64
	GetStreams() []any
}

// MediaInfo defines media metadata information.
type MediaInfo interface {
	GetFormat() string
	GetDuration() int64
	GetBitRate() int
	GetStreams() []any
}
