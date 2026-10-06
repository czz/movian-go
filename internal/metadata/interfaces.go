package metadata

// MetadataProvider defines metadata retrieval operations.
type MetadataProvider interface {
	GetTitle(url string) string
	GetArtist(url string) string
	GetAlbum(url string) string
	GetDuration(url string) int64
	GetContentType(url string) string
}

// MetadataWriter defines metadata write operations.
type MetadataWriter interface {
	SetTitle(url, title string) error
	SetArtist(url, artist string) error
	SetAlbum(url, album string) error
}

// MetadataDBProvider defines metadata database operations.
type MetadataDBProvider interface {
	Lookup(url string) (*Metadata, error)
	Store(url string, meta *Metadata) error
	Delete(url string) error
}
