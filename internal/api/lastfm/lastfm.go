package lastfm

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	dbpkg "github.com/czz/movian-go/internal/db"

	"github.com/czz/movian-go/internal/metadata"
	"github.com/czz/movian-go/internal/trace"
)

const (
	// C: LASTFM_APIKEY (lastfm.c:34)
	LastfmAPIKey = "e8fb67200bce49da092a9de1eb1c649c"
	LastfmAPIURL = "http://ws.audioscrobbler.com/2.0/"
)

// LastfmHandler handles Last.fm API requests
type LastfmHandler struct {
	db        *dbpkg.DB
	sourceID  int64
	apiKey    string
	apiSecret string
	ts        *trace.TraceSystem
	mm        *metadata.MetadataManager // C: implicit default manager — injected
}

// NewLastfmHandler creates a new Last.fm handler
func NewLastfmHandler(db *dbpkg.DB, apiKey, apiSecret string, ts *trace.TraceSystem, mm *metadata.MetadataManager) *LastfmHandler {
	if mm == nil {
		mm = metadata.NewMetadataManager(nil, nil)
	}
	return &LastfmHandler{
		db:        db,
		mm:        mm,
		apiKey:    apiKey,
		apiSecret: apiSecret,
		ts:        ts,
	}
}

// LastfmAlbumInfo represents Last.fm album info response
type LastfmAlbumInfo struct {
	XMLName xml.Name `xml:"lfm"`
	Status  string   `xml:"status,attr"`
	Album   struct {
		Name   string `xml:"name"`
		Artist string `xml:"artist"`
		MBID   string `xml:"mbid"`
		URL    string `xml:"url"`
		Images []struct {
			Size string `xml:"size,attr"`
			URL  string `xml:"#text"`
		} `xml:"image"`
		Tracks struct {
			Track []struct {
				Name   string `xml:"name"`
				Artist struct {
					Name string `xml:"name"`
					MBID string `xml:"mbid"`
				} `xml:"artist"`
			} `xml:"track"`
		} `xml:"tracks"`
	} `xml:"album"`
}

// LoadAlbumInfo loads album information from Last.fm
// This is the Go equivalent of lastfm_load_albuminfo in C
func (h *LastfmHandler) LoadAlbumInfo(album, artist string) error {
	if h.db == nil {
		return fmt.Errorf("database not initialized")
	}

	h.ts.Trace(trace.TRACE_DEBUG, "lastfm", "Loading coverart for album %s", album)

	// Build request URL
	params := url.Values{}
	params.Add("method", "album.getinfo")
	params.Add("artist", artist)
	params.Add("album", album)
	params.Add("api_key", h.apiKey)

	reqURL := LastfmAPIURL + "?" + params.Encode()

	// Make HTTP request
	resp, err := http.Get(reqURL)
	if err != nil {
		h.ts.Trace(trace.TRACE_DEBUG, "lastfm", "HTTP query to lastfm failed: %v", err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		h.ts.Trace(trace.TRACE_DEBUG, "lastfm", "HTTP query returned status %d", resp.StatusCode)
		return fmt.Errorf("HTTP error: %d", resp.StatusCode)
	}

	// Parse XML response
	var albumInfo LastfmAlbumInfo
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		h.ts.Trace(trace.TRACE_DEBUG, "lastfm", "Failed to read response: %v", err)
		return err
	}

	if err := xml.Unmarshal(body, &albumInfo); err != nil {
		h.ts.Trace(trace.TRACE_DEBUG, "lastfm", "XML parse failed: %v", err)
		return err
	}

	if albumInfo.Status != "ok" {
		h.ts.Trace(trace.TRACE_DEBUG, "lastfm", "Last.fm returned status: %s", albumInfo.Status)
		return fmt.Errorf("Last.fm error: %s", albumInfo.Status)
	}

	// Parse and store album info
	return h.parseAlbumInfo(&albumInfo, artist, album)
}

// parseAlbumInfo parses Last.fm album info and stores in database
// This is the Go equivalent of lastfm_parse_albuminfo in C
func (h *LastfmHandler) parseAlbumInfo(info *LastfmAlbumInfo, artist, album string) error {
	// Extract artist MBID from tracks if not in album
	artistMBID := info.Album.MBID
	if artistMBID == "" && len(info.Album.Tracks.Track) > 0 {
		for _, track := range info.Album.Tracks.Track {
			if track.Artist.Name == artist && track.Artist.MBID != "" {
				artistMBID = track.Artist.MBID
				break
			}
		}
	}

	// Get source ID from database
	sourceID := h.getSourceID()

	// Get or create artist in database
	// C: metadb_artist_get_by_title — returns id or <0 on error
	artistID := h.mm.MetadbArtistGetByTitle(h.db, artist, sourceID, artistMBID)
	if artistID < 0 {
		return fmt.Errorf("failed to get artist: id=%d", artistID)
	}

	// Get or create album in database
	albumID := h.mm.MetadbAlbumGetByTitle(h.db, album, artistID, sourceID, info.Album.MBID)
	if albumID < 0 {
		return fmt.Errorf("failed to get album: id=%d", albumID)
	}

	// Insert album art images
	for _, img := range info.Album.Images {
		if img.URL == "" {
			continue
		}

		var width, height int
		switch img.Size {
		case "medium":
			width, height = 64, 64
		case "large":
			width, height = 174, 174
		case "extralarge":
			width, height = 300, 300
		default:
			continue
		}

		h.mm.MetadbInsertAlbumart(h.db, albumID, img.URL, width, height)
	}

	return nil
}

// getSourceID looks up the Last.fm source ID from database
func (h *LastfmHandler) getSourceID() int {
	var id int
	stmt, rc := dbpkg.DBPrepare(h.db, "SELECT id FROM datasource WHERE name = ?")
	if rc != dbpkg.SQLITE_OK {
		return 0
	}
	stmt.BindText(1, "lastfm")
	if dbpkg.DBStep(stmt) != dbpkg.SQLITE_ROW {
		stmt.Finalize()
		return 0
	}
	id = stmt.ColumnInt(0)
	stmt.Finalize()
	return id
}

// Scrobble scrobbles a track to Last.fm
// This uses the modern Last.fm Scrobbling 2.0 API
func (h *LastfmHandler) Scrobble(artist, track, album string, timestamp int64, sessionKey string) error {
	params := url.Values{}
	params.Add("method", "track.scrobble")
	params.Add("artist", artist)
	params.Add("track", track)
	params.Add("timestamp", strconv.FormatInt(timestamp, 10))
	params.Add("api_key", h.apiKey)
	params.Add("sk", sessionKey)

	if album != "" {
		params.Add("album", album)
	}

	// Add API signature (basic - real implementation needs proper signing)
	params.Add("api_sig", h.generateSignature(params))

	resp, err := http.PostForm(LastfmAPIURL, params)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("scrobble failed with status %d", resp.StatusCode)
	}

	return nil
}

// UpdateNowPlaying updates the currently playing track on Last.fm
func (h *LastfmHandler) UpdateNowPlaying(artist, track, album string, sessionKey string) error {
	params := url.Values{}
	params.Add("method", "track.updateNowPlaying")
	params.Add("artist", artist)
	params.Add("track", track)
	params.Add("api_key", h.apiKey)
	params.Add("sk", sessionKey)

	if album != "" {
		params.Add("album", album)
	}

	params.Add("api_sig", h.generateSignature(params))

	resp, err := http.PostForm(LastfmAPIURL, params)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("updateNowPlaying failed with status %d", resp.StatusCode)
	}

	return nil
}

// generateSignature generates API signature following Last.fm authentication protocol
// 1. Order parameters alphabetically by name
// 2. Concatenate them into a string (no delimiters)
// 3. Append the secret
// 4. Generate MD5 hash
func (h *LastfmHandler) generateSignature(params url.Values) string {
	// Get parameter names and sort them alphabetically
	keys := make([]string, 0, len(params))
	for k := range params {
		// Exclude format and callback from signature
		if k != "format" && k != "callback" {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)

	// Build signature string using strings.Builder for efficiency
	var sigStr strings.Builder
	for _, k := range keys {
		sigStr.WriteString(k)
		sigStr.WriteString(params.Get(k))
	}

	// Append secret
	sigStr.WriteString(h.apiSecret)

	// Generate MD5 hash
	hash := md5.Sum([]byte(sigStr.String()))
	return hex.EncodeToString(hash[:])
}

// Start initializes the Last.fm metadata source
// This is the Go equivalent of lastfm_init in C
func (h *LastfmHandler) Start() error {
	// Register as metadata source
	// C: lastfm = metadata_add_source("lastfm", "last.fm",
	//      100000, METADATA_TYPE_MUSIC, NULL, 0, 0)  (lastfm.c:181-183)
	// C never fails — the returned source may be NULL when metadb is
	// unavailable; lastfm keeps working (source id looked up per call).
	source := h.mm.MetadataAddSource("lastfm", "last.fm", 100000,
		metadata.MetadataTypeMusic, nil, 0, 0)
	if source != nil {
		h.sourceID = int64(source.ID)
	}
	return nil
}
