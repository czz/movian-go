package metadata

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	backendcore "github.com/czz/movian-go/internal/backend/core"
	dbpkg "github.com/czz/movian-go/internal/db"
	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	settingscore "github.com/czz/movian-go/internal/settings"

	"github.com/czz/movian-go/internal/htsmsg"
	"github.com/czz/movian-go/internal/metadata"
	prop "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/trace"
	"github.com/czz/movian-go/internal/usage"
)

const (
	TvdbApikey = "0ADF8BA762FED295"
)

// TVDBClient represents the TVDB client state
type TVDBClient struct {
	usage    *usage.Reporter // C: usage_event global (usage.c)
	source   *metadata.MetadataSource
	language string
	mu       sync.Mutex
	pm       *prop.PropManager
	ts       *trace.TraceSystem
	mm       *metadata.MetadataManager         // C: implicit default manager — injected
	fam      *fileaccesscore.FileAccessManager // C: implicit global fa context — injected
	sm       *settingscore.SettingsManager     // C: global settings access
}

// NewTVDBClient creates a new TVDB client
func NewTVDBClient(pm *prop.PropManager, ts *trace.TraceSystem, mm *metadata.MetadataManager, fam *fileaccesscore.FileAccessManager) *TVDBClient {
	if mm == nil {
		mm = metadata.NewMetadataManager(nil, nil)
	}
	return &TVDBClient{
		language: "en",
		pm:       pm,
		ts:       ts,
		mm:       mm,
		fam:      fam,
	}
}

type tvdbSeason struct {
	num            int
	videoItemID    int64
	artworkDeleted bool
}

type tvdbSeasonList struct {
	seasons []*tvdbSeason
}

func (l *tvdbSeasonList) add(num int, videoItemID int64) *tvdbSeason {
	s := &tvdbSeason{
		num:         num,
		videoItemID: videoItemID,
	}
	l.seasons = append(l.seasons, s)
	return s
}

func (l *tvdbSeasonList) find(num int) *tvdbSeason {
	for _, s := range l.seasons {
		if s.num == num {
			return s
		}
	}
	return nil
}

func (l *tvdbSeasonList) flush() {
	l.seasons = nil
}

func (c *TVDBClient) getLang() string {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.language == "" {
		return ""
	}
	return c.language
}

// loadXML — C: loadxml (tvdb.c:50-74). fa_load + htsmsg_xml_deserialize_buf.
func (c *TVDBClient) loadXML(format string, args ...any) (*htsmsg.HTSMsg, error) {
	urlStr := "http://www.thetvdb.com/api/" + fmt.Sprintf(format, args...)

	result, lerr := fileaccesscore.FALoad2(c.fam, urlStr,
		&fileaccesscore.FALoadArgs{
			Flags: fileaccesscore.FaCompression,
		})
	if result == nil {
		c.ts.Trace(trace.TRACE_INFO, "TVDB", "Unable to query for %s -- %s", urlStr, lerr.Error())
		return nil, fmt.Errorf("load error: %w", lerr)
	}

	doc, err := htsmsg.DeserializeXMLBuf(result.Data[:result.Size])
	result = nil // C: buf consumed by htsmsg_xml_deserialize_buf
	if err != nil {
		c.ts.Trace(trace.TRACE_ERROR, "TVDB", "Unable to parse XML from %s -- %s", urlStr, err.Error())
		return nil, fmt.Errorf("XML parse error: %w", err)
	}

	return doc, nil
}

func (c *TVDBClient) loadActors(db *dbpkg.DB, seriesID string, seriesVID int64, seasons *tvdbSeasonList, qtype int) (int64, error) {
	doc, err := c.loadXML("%s/series/%s/actors.xml", TvdbApikey, seriesID)
	if err != nil {
		return metadata.MetadataTemporaryError, err
	}
	defer doc.Release()

	c.mm.MetadbDeleteVideocast(db, seriesVID)

	list := doc.GetMap("Actors")
	if list != nil {
		for _, f := range list.GetFields() {
			actor := f.GetMap()
			if actor == nil {
				continue
			}

			name := actor.GetStr("Name")
			role := actor.GetStr("Role")
			image := actor.GetStr("Image")
			sortOrder := actor.GetStr("SortOrder")
			id := actor.GetStr("id")

			if name == "" || role == "" || id == "" {
				continue
			}

			var imageURL *string
			if image != "" {
				url := fmt.Sprintf("https://www.thetvdb.com/banners/%s", image)
				imageURL = &url
			}

			order := 4
			if sortOrder != "" {
				if val, err := strconv.Atoi(sortOrder); err == nil {
					order = val
				}
			}

			department := "Cast"
			job := "Actor"

			imageURLStr := ""
			if imageURL != nil {
				imageURLStr = *imageURL
			}
			c.mm.MetadbInsertVideocast(db, seriesVID,
				name, role, department, job, order,
				imageURLStr, 0, 0,
				id)
		}
	}

	return 0, nil
}

func (c *TVDBClient) loadBanners(db *dbpkg.DB, seriesID string, seriesVID int64, seasons *tvdbSeasonList, qtype int) (int64, error) {
	doc, err := c.loadXML("%s/series/%s/banners.xml", TvdbApikey, seriesID)
	if err != nil {
		return metadata.MetadataTemporaryError, err
	}
	defer doc.Release()

	c.mm.MetadbDeleteVideoart(db, seriesVID)

	list := doc.GetMap("Banners")
	if list != nil {
		for _, f := range list.GetFields() {
			banner := f.GetMap()
			if banner == nil {
				continue
			}

			bannerPath := banner.GetStr("BannerPath")
			bannerType := banner.GetStr("BannerType")
			bannerType2 := banner.GetStr("BannerType2")
			seasonStr := banner.GetStr("Season")
			ratingStr := banner.GetStr("Rating")
			language := banner.GetStr("Language")

			if bannerPath == "" || bannerType == "" || bannerType2 == "" {
				continue
			}

			if language != "" && strings.ToLower(language) != c.getLang() {
				continue
			}

			bannerURL := fmt.Sprintf("https://www.thetvdb.com/banners/%s", bannerPath)

			if seasonStr != "" {
				season, err := strconv.Atoi(seasonStr)
				if err != nil {
					continue
				}

				s := seasons.find(season)
				var seasonVID int64
				if s == nil {
					seasonVID, err = tvdbMetadataFindSeason(db, seasons, seriesID, season, qtype, seriesVID, c)
					if err != nil {
						return -1, err
					}
					s = seasons.find(season)
				} else {
					seasonVID = s.videoItemID
				}

				if s != nil && !s.artworkDeleted {
					c.mm.MetadbDeleteVideoart(db, seasonVID)
					s.artworkDeleted = true
				}

				var imgType metadata.MetadataImageType
				if bannerType2 == "season" {
					imgType = metadata.MetadataImagePoster
				} else if bannerType2 == "seasonwide" {
					imgType = metadata.MetadataImageBannerWide
				} else {
					continue
				}

				weight := 0
				if ratingStr != "" {
					if val, err := strconv.ParseFloat(ratingStr, 64); err == nil {
						weight = int(val * 1000)
					}
				}

				c.mm.MetadbInsertVideoart(db, seasonVID, bannerURL, imgType, 0, 0, weight, "", 0)
				continue
			}

			var imgType metadata.MetadataImageType
			if bannerType == "poster" {
				imgType = metadata.MetadataImagePoster
			} else if bannerType == "fanart" {
				imgType = metadata.MetadataImageBackdrop
			} else if bannerType == "series" {
				imgType = metadata.MetadataImageBannerWide
			} else {
				continue
			}

			titled := 0
			if bannerType2 == "graphical" || bannerType2 == "text" {
				titled = 1
			}

			weight := 0
			if ratingStr != "" {
				if val, err := strconv.ParseFloat(ratingStr, 64); err == nil {
					weight = int(val * 1000)
				}
			}

			c.mm.MetadbInsertVideoart(db, seriesVID, bannerURL, imgType, 0, 0, weight, "", titled)
		}
	}

	return 0, nil
}

func tvdbMetadataFindSeason(db *dbpkg.DB, seasons *tvdbSeasonList, seriesID string, num int, qtype int, seriesVID int64, c *TVDBClient) (int64, error) {
	s := seasons.find(num)
	if s != nil {
		return s.videoItemID, nil
	}

	seasonURL := fmt.Sprintf("tvdb:series:%s:%d", seriesID, num)

	itemID := c.mm.MetadbGetVideoitem(db, seasonURL)
	if itemID <= 0 {
		md := metadata.Create()
		md.Type = metadata.MetadataTypeSeason
		md.ParentID = seriesVID
		md.Idx = int16(num)

		extID := fmt.Sprintf("s%s-e%d", seriesID, num)
		itemID = c.mm.MetadbInsertVideoitem(db, seasonURL,
			c.source.ID, extID, md,
			metadata.MetaItemStatusComplete, 0, qtype,
			c.source.CfgID)
		md.Destroy()
	}

	if itemID > 0 {
		seasons.add(num, itemID)
	}

	return itemID, nil
}

func (c *TVDBClient) findSeries(db *dbpkg.DB, id string, qtype int, seasons *tvdbSeasonList) (int64, error) {
	seriesURL := fmt.Sprintf("tvdb:series:%s", id)

	seriesVID := c.mm.MetadbGetVideoitem(db, seriesURL)
	if seriesVID > 0 {
		return seriesVID, nil
	}

	doc, err := c.loadXML("%s/series/%s/%s.xml", TvdbApikey, id, c.getLang())
	if err != nil {
		return metadata.MetadataTemporaryError, err
	}
	defer doc.Release()

	tags := doc.GetMapMulti("Data", "Series")
	if tags == nil {
		return metadata.MetadataTemporaryError, fmt.Errorf("no series data")
	}

	md := metadata.Create()
	md.Type = metadata.MetadataTypeSeries

	if title := tags.GetStr("SeriesName"); title != "" {
		md.Title = title
	}
	if overview := tags.GetStr("Overview"); overview != "" {
		md.Description = overview
	}

	if rating := tags.GetStr("Rating"); rating != "" {
		if val, err := strconv.ParseFloat(rating, 64); err == nil {
			md.Rating = int16(val * 10)
		}
	}

	if ratingCount := tags.GetStr("RatingCount"); ratingCount != "" {
		if val, err := strconv.Atoi(ratingCount); err == nil {
			md.RatingCount = val
		}
	}

	if imdbID := tags.GetStr("IMDB_ID"); imdbID != "" {
		md.IMDBID = imdbID
	}

	extID := fmt.Sprintf("s%s", id)
	dsid2, cfgid2 := 0, int64(0)
	if c.source != nil {
		dsid2, cfgid2 = c.source.ID, c.source.CfgID
	}
	seriesVID = c.mm.MetadbInsertVideoitem(db, seriesURL,
		dsid2, extID, md,
		metadata.MetaItemStatusComplete, 0, qtype,
		cfgid2)
	md.Destroy()

	c.loadBanners(db, id, seriesVID, seasons, qtype)
	c.loadActors(db, id, seriesVID, seasons, qtype)

	return seriesVID, nil
}

func (c *TVDBClient) queryByEpisode(db *dbpkg.DB, itemURL string, title string, season int, episode int, qtype int, initiator string) (int64, error) {
	c.usage.Event("TVDB query by episode", 1,
		"qtype", metadata.MetadataQTypeStr(qtype),
		"initiator", initiator)

	// C: fa_load("http://www.thetvdb.com/api/GetSeries.php",
	//   FA_LOAD_QUERY_ARG("seriesname", title), FA_LOAD_QUERY_ARG("language", "all"),
	//   FA_LOAD_FLAGS(FA_COMPRESSION), NULL)
	result, lerr := fileaccesscore.FALoad2(c.fam,
		"http://www.thetvdb.com/api/GetSeries.php",
		&fileaccesscore.FALoadArgs{
			QueryArgs: [][2]string{
				{"seriesname", title},
				{"language", "all"},
			},
			Flags: fileaccesscore.FaCompression,
		})
	if result == nil {
		c.ts.Trace(trace.TRACE_INFO, "TVDB", "Unable to search for %s -- %s", title, lerr.Error())
		return metadata.MetadataTemporaryError, lerr
	}

	gs, err := htsmsg.DeserializeXMLBuf(result.Data[:result.Size])
	result = nil // C: buf consumed
	if err != nil {
		c.ts.Trace(trace.TRACE_ERROR, "TVDB", "Unable to parse XML -- %s", err.Error())
		return metadata.MetadataTemporaryError, err
	}
	defer gs.Release()

	seriesID := gs.GetStrMulti("Data", "Series", "seriesid")
	if seriesID == "" {
		c.ts.Trace(trace.TRACE_INFO, "TVDB", "No series id in response")
		return metadata.MetadataTemporaryError, fmt.Errorf("no series id")
	}

	seriesIDCopy := seriesID

	epi, err := c.loadXML("%s/series/%s/default/%d/%d/%s.xml",
		TvdbApikey, seriesIDCopy, season, episode, c.getLang())
	if err != nil {
		return metadata.MetadataTemporaryError, err
	}
	defer epi.Release()

	md := metadata.Create()
	md.Type = metadata.MetadataTypeVideo

	tags := epi.GetMapMulti("Data", "Episode")

	seasons := &tvdbSeasonList{}

	seriesVID, err := c.findSeries(db, seriesIDCopy, qtype, seasons)
	if err != nil {
		md.Destroy()
		return -1, err
	}

	itemID := int64(metadata.MetadataTemporaryError)

	if tags != nil {
		seasonNumStr := tags.GetStr("SeasonNumber")
		if seasonNumStr == "" {
			md.Destroy()
			return itemID, nil
		}

		seasonNum, err := strconv.Atoi(seasonNumStr)
		if err != nil {
			md.Destroy()
			return itemID, nil
		}

		_, err = tvdbMetadataFindSeason(db, seasons, seriesIDCopy, seasonNum, qtype, seriesVID, c)
		if err != nil {
			md.Destroy()
			return -1, err
		}

		s := seasons.find(seasonNum)
		if s == nil {
			md.Destroy()
			return metadata.MetadataTemporaryError, fmt.Errorf("season not found")
		}

		md.ParentID = s.videoItemID

		if episodeName := tags.GetStr("EpisodeName"); episodeName != "" {
			md.Title = episodeName
		}
		if overview := tags.GetStr("Overview"); overview != "" {
			md.Description = overview
		}
		md.Idx = int16(episode)

		if rating := tags.GetStr("Rating"); rating != "" {
			if val, err := strconv.ParseFloat(rating, 64); err == nil {
				md.Rating = int16(val * 10)
			}
		}

		if ratingCount := tags.GetStr("RatingCount"); ratingCount != "" {
			if val, err := strconv.Atoi(ratingCount); err == nil {
				md.RatingCount = val
			}
		}

		if imdbID := tags.GetStr("IMDB_ID"); imdbID != "" {
			md.IMDBID = imdbID
		}

		extID := tags.GetStr("id")
		if extID == "" {
			extID = seriesIDCopy
		}

		dsid, cfgid := 0, int64(0)
		if c.source != nil {
			dsid, cfgid = c.source.ID, c.source.CfgID
		}
		itemID = c.mm.MetadbInsertVideoitem(db, itemURL,
			dsid, extID, md,
			metadata.MetaItemStatusComplete, 0, qtype,
			cfgid)

		if itemID >= 0 {
			c.mm.MetadbDeleteVideoart(db, itemID)

			thumb := tags.GetStr("filename")
			if thumb != "" {
				thumbURL := fmt.Sprintf("https://www.thetvdb.com/banners/%s", thumb)
				c.mm.MetadbInsertVideoart(db, itemID, thumbURL, metadata.MetadataImageThumb, 0, 0, 1, "", 0)
			}
		}
	}

	md.Destroy()
	seasons.flush()
	return itemID, nil
}

// setLangCallback — C: set_lang (tvdb.c:496-502). Runs under the
// settings/prop mutex — must NOT take c.mu (init() holds it while the
// synchronous SETTINGS_INITIAL_UPDATE fires).
func (c *TVDBClient) setLangCallback(opaque any, value any) {
	str, _ := value.(string)
	if len(str) != 2 {
		str = "en"
	}
	c.language = str
	c.updateCfgID()
}

// updateCfgID — C: update_cfgid (tvdb.c:43-46).
//
//	tvdb->ms_cfgid = (1 << 24) | tvdb_language[0] |
//	  (tvdb_language[1] << 8);
func (c *TVDBClient) updateCfgID() {
	if c.source == nil || len(c.language) < 2 {
		return
	}
	c.source.CfgID = (1 << 24) | int64(c.language[0]) | (int64(c.language[1]) << 8)
}

func (c *TVDBClient) setup() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.language = "en"

	// C: fns (tvdb.c:487-489)
	fns := &metadata.MetadataSourceFuncs{
		QueryByEpisode: func(db *dbpkg.DB, itemURL, title string,
			season int, episode int, qtype int,
			initiator string) int64 {
			r, _ := c.queryByEpisode(db, itemURL, title, season,
				episode, qtype, initiator)
			return r
		},
	}

	// C: tvdb = metadata_add_source("tvdb", "thetvdb.com", 100000,
	//      METADATA_TYPE_VIDEO, &fns, 0, complete)  (tvdb.c:514-529)
	c.source = c.mm.MetadataAddSource("tvdb", "thetvdb.com", 100000,
		metadata.MetadataTypeVideo, fns,
		// Properties we resolve for a partial lookup
		0,
		// Properties we resolve for a complete lookup
		uint64(metadata.MetadataPropTitle)|
			uint64(metadata.MetadataPropPoster)|
			uint64(metadata.MetadataPropYear)|
			uint64(metadata.MetadataPropDescription)|
			uint64(metadata.MetadataPropRating)|
			uint64(metadata.MetadataPropRatingCount)|
			uint64(metadata.MetadataPropGenre)|
			uint64(metadata.MetadataPropCast)|
			uint64(metadata.MetadataPropCrew)|
			uint64(metadata.MetadataPropBackdrop))

	if c.source == nil {
		return
	}

	// C: tvdb_init settings tail (tvdb.c:532-543)
	//   prop_t *globallang = prop_create_multi(prop_get_global(),
	//     "i18n", "iso639_1", NULL);
	//   setting_create(SETTING_STRING, tvdb->ms_settings,
	//     SETTINGS_INITIAL_UPDATE,
	//     SETTING_TITLE(_p("Language (ISO 639-1 code)")),
	//     SETTING_STORE("tvdb", "language"),
	//     SETTING_VALUE_PROP(globallang),
	//     SETTING_CALLBACK(set_lang, NULL), NULL);
	if c.sm != nil && c.source.Settings != nil {
		globallang := c.pm.CreateMultiPath(c.pm.GetGlobal(), "i18n", "iso639_1")

		c.sm.SettingCreate(settingscore.SettingString, c.source.Settings,
			settingscore.SettingsInitialUpdate,
			settingscore.SettingTagTitle,
			c.sm.P("Language (ISO 639-1 code)"),
			settingscore.SettingTagStore, "tvdb", "language",
			settingscore.SettingTagValueProp, globallang,
			settingscore.SettingTagCallback, c.setLangCallback, nil)
	}
}

// NewTVDBMetadata — C: tvdb_init (tvdb.c:507-544, INIT_GROUP_API).
// sm is the settings manager (C: global settings access).
func NewTVDBMetadata(pm *prop.PropManager, bs *backendcore.BackendSystem, sm *settingscore.SettingsManager, mm *metadata.MetadataManager) *TVDBClient {
	client := NewTVDBClient(pm, nil, mm, bs.FileAccessManager())
	client.usage = bs.Usage()
	client.sm = sm
	client.setup()
	return client
}
