package metadata

import (
	"slices"
	"sync"
)

const (
	ContentUnknown  ContentType = 0
	ContentDir      ContentType = 1
	ContentFile     ContentType = 2
	ContentArchive  ContentType = 3
	ContentAudio    ContentType = 4
	ContentVideo    ContentType = 5
	ContentPlaylist ContentType = 6
	ContentDVD      ContentType = 7
	ContentImage    ContentType = 8
	ContentAlbum    ContentType = 9
	ContentPlugin   ContentType = 10
	ContentFont     ContentType = 11
	ContentShare    ContentType = 12
	ContentDocument ContentType = 13
)

const (
	MetadataTypeVideo  MetadataType = 1
	MetadataTypeSeason MetadataType = 2
	MetadataTypeSeries MetadataType = 3
	MetadataTypeMusic  MetadataType = 4
	MetadataTypeNum    MetadataType = 5 // C: METADATA_TYPE_num
)

const (
	MetadataImagePoster     MetadataImageType = 1
	MetadataImageBackdrop   MetadataImageType = 2
	MetadataImagePortrait   MetadataImageType = 3
	MetadataImageBannerWide MetadataImageType = 4
	MetadataImageThumb      MetadataImageType = 5
)

const (
	IndexStatusNoChange MetadataIndexStatus = -1
	IndexStatusUnset    MetadataIndexStatus = 0
	IndexStatusError    MetadataIndexStatus = 1
	IndexStatusAnalyzed MetadataIndexStatus = 2
)

// Metadata item status
const (
	MetaItemStatusAbsent   = 1
	MetaItemStatusPartial  = 2
	MetaItemStatusComplete = 3
)

// Metadata query types
const (
	MetadataQTypeFilename            = 1
	MetadataQTypeIMDB                = 2
	MetadataQTypeDirectory           = 3
	MetadataQTypeCustom              = 4
	MetadataQTypeCustomIMDB          = 5
	MetadataQTypeFilenameOrDirectory = 6
	MetadataQTypeEpisode             = 7
	MetadataQTypeMovie               = 8
	MetadataQTypeTVShow              = 9
)

// Metadata property flags
// C: metadata.h:270-287 — enum metadata_prop_t values used as bit
// positions in mlp_req_items (mlp_sub_cb does: id = 1 << id).
// Go: We store them as pre-shifted bit flags (1 << enum_value).
// The bit positions MUST match the C enum order exactly, including
// METADATA_PROP_VTYPE at position 3.
const (
	MetadataPropTitle          = 1 << 0  // C: METADATA_PROP_TITLE
	MetadataPropPoster         = 1 << 1  // C: METADATA_PROP_POSTER
	MetadataPropYear           = 1 << 2  // C: METADATA_PROP_YEAR
	MetadataPropVtype          = 1 << 3  // C: METADATA_PROP_VTYPE
	MetadataPropTagline        = 1 << 4  // C: METADATA_PROP_TAGLINE
	MetadataPropDescription    = 1 << 5  // C: METADATA_PROP_DESCRIPTION
	MetadataPropRating         = 1 << 6  // C: METADATA_PROP_RATING
	MetadataPropRatingCount    = 1 << 7  // C: METADATA_PROP_RATING_COUNT
	MetadataPropBackdrop       = 1 << 8  // C: METADATA_PROP_BACKDROP
	MetadataPropGenre          = 1 << 9  // C: METADATA_PROP_GENRE
	MetadataPropCast           = 1 << 10 // C: METADATA_PROP_CAST
	MetadataPropCrew           = 1 << 11 // C: METADATA_PROP_CREW
	MetadataPropEpisodeName    = 1 << 12 // C: METADATA_PROP_EPISODE_NAME
	MetadataPropSeasonName     = 1 << 13 // C: METADATA_PROP_SEASON_NAME
	MetadataPropArtistPictures = 1 << 14 // C: METADATA_PROP_ARTIST_PICTURES
	MetadataPropAlbumArt       = 1 << 15 // C: METADATA_PROP_ALBUM_ART
)

// Metadata errors
const (
	MetadataPermanentError = -1
	MetadataTemporaryError = -2
	MetadataDeadlock       = -3
)

// Metadata cache status
const (
	MetadataCacheStatusNo         = 0
	MetadataCacheStatusFull       = 1
	MetadataCacheStatusUnparented = 2
)

// Decoration represents metadata decoration
type Decoration struct {
	title       string
	description string
	icon        string
	backdrop    string
	// Extended fields for decoration management
	ID          string
	Type        DecorationType
	URL         string
	Language    string
	Rating      float32
	Votes       int
	Width       int
	Height      int
	AspectRatio string
	Primary     bool
	metadata    map[string]any
	mutex       sync.Mutex
}

// DecorationType represents the type of decoration
type DecorationType int

const (
	DecorationTypeUnknown DecorationType = iota
	DecorationTypePoster
	DecorationTypeBackdrop
	DecorationTypeBanner
	DecorationTypeThumb
	DecorationTypeLogo
	DecorationTypeClearArt
	DecorationTypeFanArt
)

func NewDecoration() *Decoration {
	return &Decoration{
		title:       "",
		description: "",
		icon:        "",
		backdrop:    "",
		Type:        DecorationTypeUnknown,
		Primary:     false,
		metadata:    make(map[string]any),
	}
}

func (d *Decoration) SetTitle(title string) {
	d.title = title
}

func (d *Decoration) SetDescription(desc string) {
	d.description = desc
}

func (d *Decoration) SetIcon(icon string) {
	d.icon = icon
}

func (d *Decoration) SetBackdrop(backdrop string) {
	d.backdrop = backdrop
}

func (d *Decoration) GetTitle() string {
	return d.title
}

func (d *Decoration) GetDescription() string {
	return d.description
}

func (d *Decoration) GetIcon() string {
	return d.icon
}

func (d *Decoration) GetBackdrop() string {
	return d.backdrop
}

func (d *Decoration) GetURL() string {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	return d.URL
}

func (d *Decoration) SetURL(url string) {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	d.URL = url
}

func (d *Decoration) GetType() DecorationType {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	return d.Type
}

func (d *Decoration) SetType(decType DecorationType) {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	d.Type = decType
}

func (d *Decoration) GetLanguage() string {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	return d.Language
}

func (d *Decoration) SetLanguage(lang string) {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	d.Language = lang
}

func (d *Decoration) GetRating() float32 {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	return d.Rating
}

func (d *Decoration) SetRating(rating float32) {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	d.Rating = rating
}

func (d *Decoration) GetVotes() int {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	return d.Votes
}

func (d *Decoration) SetVotes(votes int) {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	d.Votes = votes
}

func (d *Decoration) GetWidth() int {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	return d.Width
}

func (d *Decoration) SetWidth(width int) {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	d.Width = width
}

func (d *Decoration) GetHeight() int {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	return d.Height
}

func (d *Decoration) SetHeight(height int) {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	d.Height = height
}

func (d *Decoration) GetAspectRatio() string {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	return d.AspectRatio
}

func (d *Decoration) SetAspectRatio(aspectRatio string) {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	d.AspectRatio = aspectRatio
}

func (d *Decoration) IsPrimary() bool {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	return d.Primary
}

func (d *Decoration) SetPrimary(primary bool) {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	d.Primary = primary
}

func (d *Decoration) GetMetadata(key string) any {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	return d.metadata[key]
}

func (d *Decoration) SetMetadata(key string, value any) {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	d.metadata[key] = value
}

// DecorationManager manages decorations for media items
type DecorationManager struct {
	decorations map[string][]*Decoration
	mutex       sync.RWMutex
}

func NewDecorationManager() *DecorationManager {
	return &DecorationManager{
		decorations: make(map[string][]*Decoration),
	}
}

func (dm *DecorationManager) AddDecoration(itemID string, dec *Decoration) {
	dm.mutex.Lock()
	defer dm.mutex.Unlock()

	if dm.decorations[itemID] == nil {
		dm.decorations[itemID] = make([]*Decoration, 0)
	}
	dm.decorations[itemID] = append(dm.decorations[itemID], dec)
}

func (dm *DecorationManager) GetDecorations(itemID string) []*Decoration {
	dm.mutex.RLock()
	defer dm.mutex.RUnlock()

	if dm.decorations[itemID] == nil {
		return nil
	}

	result := make([]*Decoration, len(dm.decorations[itemID]))
	copy(result, dm.decorations[itemID])
	return result
}

func (dm *DecorationManager) GetDecorationsByType(itemID string, decType DecorationType) []*Decoration {
	dm.mutex.RLock()
	defer dm.mutex.RUnlock()

	if dm.decorations[itemID] == nil {
		return nil
	}

	result := make([]*Decoration, 0)
	for _, dec := range dm.decorations[itemID] {
		if dec.Type == decType {
			result = append(result, dec)
		}
	}
	return result
}

func (dm *DecorationManager) GetPrimaryDecoration(itemID string, decType DecorationType) *Decoration {
	dm.mutex.RLock()
	defer dm.mutex.RUnlock()

	if dm.decorations[itemID] == nil {
		return nil
	}

	for _, dec := range dm.decorations[itemID] {
		if dec.Type == decType && dec.Primary {
			return dec
		}
	}

	// Return first decoration of type if no primary
	for _, dec := range dm.decorations[itemID] {
		if dec.Type == decType {
			return dec
		}
	}

	return nil
}

func (dm *DecorationManager) SetPrimaryDecoration(itemID string, dec *Decoration) {
	dm.mutex.Lock()
	defer dm.mutex.Unlock()

	if dm.decorations[itemID] == nil {
		return
	}

	// Unset primary for all decorations of same type
	for _, d := range dm.decorations[itemID] {
		if d.Type == dec.Type {
			d.mutex.Lock()
			d.Primary = false
			d.mutex.Unlock()
		}
	}

	// Set new primary
	dec.mutex.Lock()
	dec.Primary = true
	dec.mutex.Unlock()
}

func (dm *DecorationManager) RemoveDecoration(itemID string, dec *Decoration) {
	dm.mutex.Lock()
	defer dm.mutex.Unlock()

	if dm.decorations[itemID] == nil {
		return
	}

	for i, d := range dm.decorations[itemID] {
		if d == dec {
			dm.decorations[itemID] = slices.Delete(dm.decorations[itemID], i, i+1)
			break
		}
	}
}

func (dm *DecorationManager) ClearDecorations(itemID string) {
	dm.mutex.Lock()
	defer dm.mutex.Unlock()

	delete(dm.decorations, itemID)
}

func (pi *PlayInfo) SetURL(url string) {
	pi.url = url
}

func (pi *PlayInfo) SetTitle(title string) {
	pi.title = title
}

func (pi *PlayInfo) GetURL() string {
	return pi.url
}

func (pi *PlayInfo) GetTitle() string {
	return pi.title
}
