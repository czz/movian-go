package metadata

import (
	"time"
)

// Content types - stored in database, must not change
type ContentType int

// ContentDirish checks if content type is directory-like
func ContentDirish(ct ContentType) bool {
	return ct == ContentDir || ct == ContentShare || ct == ContentArchive || ct == ContentPlaylist
}

// Metadata types - stored in database, must not change
type MetadataType int

// Image types - stored in database, must not change
type MetadataImageType int

// Index status - stored in database, must not change
type MetadataIndexStatus int

// MetadataStream represents a media stream (audio, video, subtitle)
type MetadataStream struct {
	StreamIndex int
	Title       string
	Info        string
	ISOLang     string
	Codec       string
	Type        int
	TrackNum    int
	Disposition int
	Channels    int // -1 == unknown
}

// MetadataPerson represents a person (actor, director, etc.)
type MetadataPerson struct {
	Name       string
	Character  string
	Department string
	Job        string
	Portrait   string
}

// Metadata represents media metadata
type Metadata struct {
	Redirect     string
	Manufacturer string
	Equipment    string

	Backdrops   []string
	Icons       []string
	WideBanners []string
	Thumbs      []string

	ExtID string

	Title  string
	Album  string
	Artist string
	Format string
	Genre  string

	Streams []MetadataStream
	Cast    []MetadataPerson
	Crew    []MetadataPerson

	Parent      *Metadata
	Description string
	Tagline     string

	IMDBID string

	ID       int64
	ParentID int64

	Time        time.Time
	ContentType ContentType
	Duration    float32

	Type MetadataType

	RatingCount int

	DSID   int16
	Tracks int16
	Track  int16

	Rating int16 // 0 - 100
	Year   int16
	Idx    int16 // -1 == unset (episode, season, etc. Depends on Type)

	MetaItemStatus int
	QType          int

	Preferred   bool
	CacheStatus int

	IndexStatus MetadataIndexStatus
	Loaded      bool // For lazy loading
}

// Create creates a new metadata structure
func Create() *Metadata {
	return &Metadata{
		Streams:        make([]MetadataStream, 0),
		Cast:           make([]MetadataPerson, 0),
		Crew:           make([]MetadataPerson, 0),
		Backdrops:      make([]string, 0),
		Icons:          make([]string, 0),
		WideBanners:    make([]string, 0),
		Thumbs:         make([]string, 0),
		Idx:            -1,
		MetaItemStatus: MetaItemStatusAbsent,
	}
}

// Destroy cleans up the metadata structure
func (md *Metadata) Destroy() {
	// Clear slices to allow garbage collection
	md.Streams = nil
	md.Cast = nil
	md.Crew = nil
	md.Backdrops = nil
	md.Icons = nil
	md.WideBanners = nil
	md.Thumbs = nil
	md.Parent = nil
}

// MetadataQTypeStr returns a string representation of a query type
func MetadataQTypeStr(qtype int) string {
	switch qtype {
	case MetadataQTypeFilename:
		return "filename"
	case MetadataQTypeIMDB:
		return "imdb"
	case MetadataQTypeDirectory:
		return "directory"
	case MetadataQTypeCustom:
		return "custom"
	case MetadataQTypeCustomIMDB:
		return "custom-imdb"
	case MetadataQTypeFilenameOrDirectory:
		return "filename-or-directory"
	case MetadataQTypeEpisode:
		return "episode"
	case MetadataQTypeMovie:
		return "movie"
	case MetadataQTypeTVShow:
		return "tvshow"
	default:
		return "unknown"
	}
}

// AddStream adds a stream to the metadata
func (md *Metadata) AddStream(codec string, streamType int, streamIndex int,
	title string, info string, isolang string,
	disposition int, trackNum int, channels int) {

	stream := MetadataStream{
		StreamIndex: streamIndex,
		Title:       title,
		Info:        info,
		ISOLang:     isolang,
		Codec:       codec,
		Type:        streamType,
		TrackNum:    trackNum,
		Disposition: disposition,
		Channels:    channels,
	}

	md.Streams = append(md.Streams, stream)
}

// AddPerson adds a person to cast or crew
func (md *Metadata) AddPerson(name string, character string, department string,
	job string, portrait string, isCrew bool) {

	person := MetadataPerson{
		Name:       name,
		Character:  character,
		Department: department,
		Job:        job,
		Portrait:   portrait,
	}

	if isCrew {
		md.Crew = append(md.Crew, person)
	} else {
		md.Cast = append(md.Cast, person)
	}
}

// Content2Type converts content type to string
func Content2Type(ct ContentType) string {
	switch ct {
	case ContentDir:
		return "directory"
	case ContentFile:
		return "file"
	case ContentArchive:
		return "archive"
	case ContentAudio:
		return "audio"
	case ContentVideo:
		return "video"
	case ContentPlaylist:
		return "playlist"
	case ContentDVD:
		return "dvd"
	case ContentImage:
		return "image"
	case ContentAlbum:
		return "album"
	case ContentPlugin:
		return "plugin"
	case ContentFont:
		return "font"
	case ContentShare:
		return "share"
	case ContentDocument:
		return "document"
	default:
		return "unknown"
	}
}

// Type2Content converts string to content type
func Type2Content(str string) ContentType {
	switch str {
	case "directory":
		return ContentDir
	case "file":
		return ContentFile
	case "archive":
		return ContentArchive
	case "audio":
		return ContentAudio
	case "video":
		return ContentVideo
	case "playlist":
		return ContentPlaylist
	case "dvd":
		return ContentDVD
	case "image":
		return ContentImage
	case "album":
		return ContentAlbum
	case "plugin":
		return ContentPlugin
	case "font":
		return ContentFont
	case "share":
		return ContentShare
	case "document":
		return ContentDocument
	default:
		return ContentUnknown
	}
}

// QTypeStr converts query type to string
func QTypeStr(qtype int) string {
	switch qtype {
	case MetadataQTypeFilename:
		return "filename"
	case MetadataQTypeIMDB:
		return "imdb"
	case MetadataQTypeDirectory:
		return "directory"
	case MetadataQTypeCustom:
		return "custom"
	case MetadataQTypeCustomIMDB:
		return "custom_imdb"
	case MetadataQTypeFilenameOrDirectory:
		return "filename_or_directory"
	case MetadataQTypeEpisode:
		return "episode"
	case MetadataQTypeMovie:
		return "movie"
	case MetadataQTypeTVShow:
		return "tvshow"
	default:
		return "unknown"
	}
}
