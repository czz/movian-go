package metadata

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	dbpkg "github.com/czz/movian-go/internal/db"
	"github.com/czz/movian-go/internal/usage"

	backendcore "github.com/czz/movian-go/internal/backend/core"
	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/htsmsg"
	imagepkg "github.com/czz/movian-go/internal/image"
	"github.com/czz/movian-go/internal/metadata"
	httpnet "github.com/czz/movian-go/internal/networking/http"
	propcore "github.com/czz/movian-go/internal/prop"
	settingscore "github.com/czz/movian-go/internal/settings"
	"github.com/czz/movian-go/internal/trace"
)

const (
	defaultTMDBAPIKey = "a0d71cffe2d6693d462af9e4f336bc06"
)

// Wired by cmd/movian-go init; nil-safe methods make unwired calls no-ops.

type tmdbImageSize struct {
	width  int
	height int
	prefix string
}

type TMDBClient struct {
	usage          *usage.Reporter // C: usage_event global (usage.c)
	apiKey         string
	imageBaseURL   string
	language       string
	useOrigTitle   bool
	posterSizes    []*tmdbImageSize
	backdropSizes  []*tmdbImageSize
	profileSizes   []*tmdbImageSize
	configured     bool
	rateLimitTimer *time.Timer
	mu             sync.RWMutex
	pm             *propcore.PropManager
	source         *metadata.MetadataSource
	ts             *trace.TraceSystem
	mm             *metadata.MetadataManager         // C: implicit default manager — injected
	fam            *fileaccesscore.FileAccessManager // C: implicit global fa context — injected
	sm             *settingscore.SettingsManager     // C: global settings access
}

func (c *TMDBClient) trace(format string, args ...any) {
	if c.ts != nil && c.ts.GetEnableMetadataDebug() {
		c.ts.Trace(trace.TRACE_DEBUG, "TMDB", format, args...)
	}
}

// handleRateLimit — C: tmdb_handle_rate_limit (tmdb.c:71-84). Reads
// "retry-after" from the response headers and frees the list.
func (c *TMDBClient) handleRateLimit(responseHeaders *httpnet.HTTPHeaderList) {
	waitTime := 5 * time.Second
	if responseHeaders != nil {
		if retry, ok := responseHeaders.Get("retry-after"); ok {
			if val, err := strconv.Atoi(retry); err == nil {
				waitTime = time.Duration(val+1) * time.Second
			}
		}
		responseHeaders.Free() // C: http_headers_free(response_headers)
	}

	c.trace("Rate limited - Throttling requests for %v", waitTime)

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.rateLimitTimer != nil {
		c.rateLimitTimer.Stop()
	}
	c.rateLimitTimer = time.AfterFunc(waitTime, func() {
		c.mu.Lock()
		c.rateLimitTimer = nil
		c.mu.Unlock()
	})
}

func (c *TMDBClient) checkRateLimit(ctx context.Context) error {
	c.mu.RLock()
	timer := c.rateLimitTimer
	c.mu.RUnlock()
	return c.checkRateLimitLocked(ctx, timer)
}

// checkRateLimitLocked checks rate limit given the timer value (avoids re-entrant locking)
func (c *TMDBClient) checkRateLimitLocked(ctx context.Context, timer *time.Timer) error {
	if timer != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
	return nil
}

func (c *TMDBClient) getLang() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.getLangLocked()
}

// getLangLocked returns the language assuming the mutex is already held
func (c *TMDBClient) getLangLocked() string {
	return c.language
}

func (c *TMDBClient) addSize(p *[]*tmdbImageSize, str string, aspect float64) {
	width := 0
	height := 0

	if len(str) > 0 && str[0] == 'w' {
		if val, err := strconv.Atoi(str[1:]); err == nil {
			width = val
			if aspect > 0 {
				height = int(float64(width) / aspect)
			}
		}
	} else if len(str) > 0 && str[0] == 'h' {
		if val, err := strconv.Atoi(str[1:]); err == nil {
			height = val
			if aspect > 0 {
				width = int(float64(height) * aspect)
			}
		}
	}

	*p = append(*p, &tmdbImageSize{
		prefix: str,
		width:  width,
		height: height,
	})
}

func (c *TMDBClient) addSizes(p *[]*tmdbImageSize, img *htsmsg.HTSMsg, field string, aspect float64) {
	list := img.GetList(field)
	if list == nil {
		return
	}

	for _, f := range list.GetFields() {
		if f.GetType() == htsmsg.HmfStr {
			c.addSize(p, f.GetStrValue(), aspect)
		}
	}
}

func (c *TMDBClient) insertVideoArt(db *dbpkg.DB, itemID int64, imgType metadata.MetadataImageType, path string, pfx string) {
	url := fmt.Sprintf("tmdb:image:%s:%s", pfx, path)
	c.mm.MetadbInsertVideoart(db, itemID, url, imgType, 0, 0, 0, "", 0)
}

func (c *TMDBClient) insertVideoArts(db *dbpkg.DB, itemID int64, imgType metadata.MetadataImageType, list *htsmsg.HTSMsg, pfx string) {
	limit := 0
	for _, f := range list.GetFields() {
		if limit >= 10 {
			break
		}

		p := f.GetMap()
		if p == nil {
			continue
		}

		path := p.GetStr("file_path")
		if path == "" {
			continue
		}

		width := p.GetU32OrDefault("width", 0)
		height := p.GetU32OrDefault("height", 0)
		weight := p.GetDblOrDefault("vote_average", 0) * 1000

		url := fmt.Sprintf("tmdb:image:%s:%s", pfx, path)
		c.mm.MetadbInsertVideoart(db, itemID, url, imgType, int(width), int(height), int(weight), "", 0)
		limit++
	}
}

func (c *TMDBClient) parseConfig(doc *htsmsg.HTSMsg) error {
	img := doc.GetMap("images")
	if img == nil {
		return fmt.Errorf("no images in config")
	}

	baseURL := img.GetStr("base_url")
	if baseURL == "" {
		return fmt.Errorf("no base_url in config")
	}

	c.imageBaseURL = baseURL
	c.addSizes(&c.posterSizes, img, "poster_sizes", 0.675)
	c.addSizes(&c.backdropSizes, img, "backdrop_sizes", 1.777777)
	c.addSizes(&c.profileSizes, img, "profile_sizes", 0.675)

	return nil
}

func (c *TMDBClient) configure(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.configured {
		return nil
	}

	// C: fa_load("http://api.themoviedb.org/3/configuration",
	//   FA_LOAD_ERRBUF, FA_LOAD_QUERY_ARG("api_key"), FA_LOAD_QUERY_ARG("language"),
	//   FA_LOAD_FLAGS(FA_COMPRESSION | FA_IMPORTANT), NULL)
	qargs := [][2]string{{"api_key", c.apiKey}}
	if lang := c.getLangLocked(); lang != "" {
		// C: getlang() returns NULL when unset — FA_LOAD_QUERY_ARG
		// queues only non-NULL values.
		qargs = append(qargs, [2]string{"language", lang})
	}
	result, err := fileaccesscore.FALoad2(c.fam,
		"http://api.themoviedb.org/3/configuration",
		&fileaccesscore.FALoadArgs{
			QueryArgs: qargs,
			Flags:     fileaccesscore.FaCompression | fileaccesscore.FaImportant,
		})
	if result == nil {
		c.ts.Trace(trace.TRACE_INFO, "TMDB", "Unable to get configuration -- %s", err.Error())
		return fmt.Errorf("unable to load config: %w", err)
	}

	doc, jerr := htsmsg.DeserializeJSON(string(result.Data[:result.Size]))
	result = nil // C: buf_release(result)
	if jerr != nil {
		c.ts.Trace(trace.TRACE_ERROR, "TMDB", "Got bad JSON from config -- %s", jerr)
		return fmt.Errorf("bad JSON: %s", jerr)
	}

	if err := c.parseConfig(doc); err != nil {
		doc.Release()
		return err
	}
	doc.Release()
	c.configured = true

	return nil
}

func (c *TMDBClient) loadMovieCast(ctx context.Context, lookupID string) (*htsmsg.HTSMsg, error) {
	// C: tmdb_load_movie_cast (tmdb.c:281-322)
	url := fmt.Sprintf("http://api.themoviedb.org/3/movie/%s/casts", lookupID)

	// C: retry:
	for {
		if err := c.checkRateLimit(ctx); err != nil { // C: tmdb_check_rate_limit()
			return nil, err
		}

		httpResponseCode := 0
		var responseHeaders httpnet.HTTPHeaderList

		qargs := [][2]string{{"api_key", c.apiKey}}
		if lang := c.getLang(); lang != "" {
			qargs = append(qargs, [2]string{"language", lang})
		}
		result, lerr := fileaccesscore.FALoad2(c.fam, url,
			&fileaccesscore.FALoadArgs{
				QueryArgs:    qargs,
				RespHeaders:  &responseHeaders,
				ProtocolCode: &httpResponseCode,
				Flags:        fileaccesscore.FaCompression,
			})
		if result == nil {
			if httpResponseCode == 429 {
				c.handleRateLimit(&responseHeaders)
				continue // C: goto retry
			}
			responseHeaders.Free() // C: http_headers_free(&response_headers)
			c.ts.Trace(trace.TRACE_INFO, "TMDB", "Load error %s", lerr.Error())
			return nil, fmt.Errorf("load error: %w", lerr)
		}
		responseHeaders.Free() // C: http_headers_free(&response_headers)

		doc, jerr := htsmsg.DeserializeJSON(string(result.Data[:result.Size]))
		result = nil // C: buf_release(result)
		if jerr != nil {
			c.ts.Trace(trace.TRACE_ERROR, "TMDB", "Got bad JSON from %s -- %s", url, jerr.Error())
			return nil, fmt.Errorf("bad JSON: %w", jerr)
		}

		return doc, nil
	}
}

func (c *TMDBClient) insertMovieCast(db *dbpkg.DB, itemID int64, doc *htsmsg.HTSMsg) {
	cast := doc.GetList("cast")
	if cast != nil {
		for _, f := range cast.GetFields() {
			p := f.GetMap()
			if p == nil {
				continue
			}

			profilePath := p.GetStr("profile_path")
			var imageURL string
			if profilePath != "" {
				imageURL = fmt.Sprintf("tmdb:image:profile:%s", profilePath)
			}

			id := strconv.FormatUint(uint64(p.GetU32OrDefault("id", 0)), 10)

			name := p.GetStr("name")
			character := p.GetStr("character")
			department := "Cast"
			job := "Actor"
			order := int(p.GetU32OrDefault("order", 0))

			c.mm.MetadbInsertVideocast(db, itemID,
				name, character, department, job, order,
				imageURL, 0, 0,
				id)
		}
	}

	crew := doc.GetList("crew")
	order := 0
	if crew != nil {
		for _, f := range crew.GetFields() {
			p := f.GetMap()
			if p == nil {
				continue
			}

			profilePath := p.GetStr("profile_path")
			var imageURL string
			if profilePath != "" {
				imageURL = fmt.Sprintf("tmdb:image:profile:%s", profilePath)
			}

			id := strconv.FormatUint(uint64(p.GetU32OrDefault("id", 0)), 10)

			name := p.GetStr("name")
			department := p.GetStr("department")
			job := p.GetStr("job")

			c.mm.MetadbInsertVideocast(db, itemID,
				name, "", department, job, order,
				imageURL, 0, 0,
				id)
			order++
		}
	}
}

// C: tmdb_load_movie_info (tmdb.c:392-460) — adds cache_info out-param.
func (c *TMDBClient) loadMovieInfo(ctx context.Context, db *dbpkg.DB, itemURL string, lookupID string, qtype int, cacheInfo *int) (int64, error) {
	url := fmt.Sprintf("http://api.themoviedb.org/3/movie/%s", lookupID)
	imageLanguage := fmt.Sprintf("%s,null", c.getLang())

	// C: retry:
	for {
		if err := c.checkRateLimit(ctx); err != nil { // C: tmdb_check_rate_limit()
			return metadata.MetadataTemporaryError, err
		}

		httpResponseCode := 0
		var responseHeaders httpnet.HTTPHeaderList

		// C: FA_LOAD_QUERY_ARG("api_key"), ("language", getlang()),
		// ("append_to_response"), ("include_image_language") — NULL skipped.
		qargs := [][2]string{{"api_key", c.apiKey}}
		if lang := c.getLang(); lang != "" {
			qargs = append(qargs, [2]string{"language", lang})
		}
		qargs = append(qargs,
			[2]string{"append_to_response", "images,trailers"},
			[2]string{"include_image_language", imageLanguage})
		result, lerr := fileaccesscore.FALoad2(c.fam, url,
			&fileaccesscore.FALoadArgs{
				QueryArgs:    qargs,
				CacheInfo:    cacheInfo,
				RespHeaders:  &responseHeaders,
				ProtocolCode: &httpResponseCode,
				Flags:        fileaccesscore.FaCompression,
			})
		if result == nil {
			if httpResponseCode == 429 {
				c.handleRateLimit(&responseHeaders)
				continue // C: goto retry
			}
			responseHeaders.Free() // C: http_headers_free(&response_headers)
			c.ts.Trace(trace.TRACE_INFO, "TMDB", "Load error %s", lerr.Error())
			return metadata.MetadataTemporaryError, fmt.Errorf("load error: %w", lerr)
		}
		responseHeaders.Free() // C: http_headers_free(&response_headers)

		doc, jerr := htsmsg.DeserializeJSON(string(result.Data[:result.Size]))
		result = nil // C: buf_release(result)
		if jerr != nil {
			c.ts.Trace(trace.TRACE_ERROR, "TMDB", "Got bad JSON from %s -- %s", url, jerr.Error())
			return metadata.MetadataTemporaryError, fmt.Errorf("bad JSON: %w", jerr)
		}

		return c.processMovieInfo(ctx, db, itemURL, lookupID, qtype, doc)
	}
}

func (c *TMDBClient) processMovieInfo(ctx context.Context, db *dbpkg.DB, itemURL string, lookupID string, qtype int, doc *htsmsg.HTSMsg) (int64, error) {
	md := metadata.Create()
	defer md.Destroy()
	defer doc.Release()

	if overview := doc.GetStr("overview"); overview != "" {
		md.Description = overview
	}
	if tagline := doc.GetStr("tagline"); tagline != "" {
		md.Tagline = tagline
	}
	if imdbID := doc.GetStr("imdb_id"); imdbID != "" {
		md.IMDBID = imdbID
	}

	titleField := "title"
	c.mu.RLock()
	useOrigTitle := c.useOrigTitle
	c.mu.RUnlock()
	if useOrigTitle {
		titleField = "original_title"
	}
	if title := doc.GetStr(titleField); title != "" {
		md.Title = title
	}

	if voteAverage := doc.GetDblOrDefault("vote_average", 0); voteAverage > 0 {
		md.Rating = int16(voteAverage * 10)
	}

	md.RatingCount = int(doc.GetS32OrDefault("vote_count", -1))
	md.Duration = float32(doc.GetS32OrDefault("runtime", 0) * 60)

	if releaseDate := doc.GetStr("release_date"); releaseDate != "" && len(releaseDate) >= 4 {
		if year, err := strconv.Atoi(releaseDate[:4]); err == nil {
			md.Year = int16(year)
		}
	}

	itemID := int64(metadata.MetadataTemporaryError)

	id := doc.GetU32OrDefault("id", 0)
	if id > 0 {
		cast, err := c.loadMovieCast(ctx, lookupID)
		if err == nil {
			defer cast.Release()
		}

		popularity := doc.GetDblOrDefault("popularity", 0)

		tmdbID := strconv.FormatUint(uint64(id), 10)
		dsid, cfgid := 0, int64(0)
		if c.source != nil {
			dsid, cfgid = c.source.ID, c.source.CfgID
		}
		itemID = c.mm.MetadbInsertVideoitem(db, itemURL,
			dsid, tmdbID, md,
			metadata.MetaItemStatusComplete, int64(popularity*1000),
			qtype, cfgid)

		if itemID >= 0 {
			images := doc.GetMap("images")
			var backdrops, posters *htsmsg.HTSMsg
			if images != nil {
				backdrops = images.GetList("backdrops")
				posters = images.GetList("posters")
			}

			if backdrops != nil {
				c.insertVideoArts(db, itemID, metadata.MetadataImageBackdrop, backdrops, "backdrop")
			} else if backdropPath := doc.GetStr("backdrop_path"); backdropPath != "" {
				c.insertVideoArt(db, itemID, metadata.MetadataImageBackdrop, backdropPath, "backdrop")
			}

			if posters != nil {
				c.insertVideoArts(db, itemID, metadata.MetadataImagePoster, posters, "poster")
			} else if posterPath := doc.GetStr("poster_path"); posterPath != "" {
				c.insertVideoArt(db, itemID, metadata.MetadataImagePoster, posterPath, "poster")
			}

			genres := doc.GetList("genres")
			if genres != nil {
				for _, f := range genres.GetFields() {
					g := f.GetMap()
					if g == nil {
						continue
					}

					if title := g.GetStr("name"); title != "" {
						c.mm.MetadbInsertVideogenre(db, itemID, title)
					}
				}
			}

			if cast != nil {
				c.insertMovieCast(db, itemID, cast)
			}
		}
	}

	status := "Not found"
	if itemID != metadata.MetadataTemporaryError {
		status = "Found"
	}
	c.trace("Loaded movie info for %s -- %s", lookupID, status)

	return itemID, nil
}

// C: tmdb_search_movie (tmdb.c:533-636) — adds cache_info out-param.
func (c *TMDBClient) queryByTitleAndYear0(ctx context.Context, db *dbpkg.DB, itemURL string, title string, year int, duration int, qtype int, cacheInfo *int) (int64, error) {
	var yearText string
	if year > 0 {
		yearText = strconv.Itoa(year)
	}

	const url = "http://api.themoviedb.org/3/search/movie"

	// C: retry:
	for {
		if err := c.checkRateLimit(ctx); err != nil { // C: tmdb_check_rate_limit()
			return metadata.MetadataTemporaryError, err
		}

		httpResponseCode := 0
		var responseHeaders httpnet.HTTPHeaderList

		// C: FA_LOAD_QUERY_ARG("query"), ("year", yeartxt ?: NULL),
		// ("api_key"), ("language", getlang()) — NULL skipped.
		qargs := [][2]string{{"query", title}}
		if yearText != "" {
			qargs = append(qargs, [2]string{"year", yearText})
		}
		qargs = append(qargs, [2]string{"api_key", c.apiKey})
		if lang := c.getLang(); lang != "" {
			qargs = append(qargs, [2]string{"language", lang})
		}
		result, lerr := fileaccesscore.FALoad2(c.fam, url,
			&fileaccesscore.FALoadArgs{
				QueryArgs:    qargs,
				RespHeaders:  &responseHeaders,
				ProtocolCode: &httpResponseCode,
				Flags:        fileaccesscore.FaCompression,
				CacheInfo:    cacheInfo,
			})
		if result == nil {
			if httpResponseCode == 429 {
				c.handleRateLimit(&responseHeaders)
				continue // C: goto retry
			}
			responseHeaders.Free() // C: http_headers_free(&response_headers)
			return metadata.MetadataTemporaryError, fmt.Errorf("load error: %w", lerr)
		}
		responseHeaders.Free() // C: http_headers_free(&response_headers)

		doc, jerr := htsmsg.DeserializeJSON(string(result.Data[:result.Size]))
		result = nil // C: buf_release(result)
		if jerr != nil {
			c.ts.Trace(trace.TRACE_ERROR, "TMDB", "Got bad JSON from %s -- %s", url, jerr.Error())
			return metadata.MetadataTemporaryError, fmt.Errorf("bad JSON: %w", jerr)
		}

		return c.processSearchResults(db, itemURL, title, year, qtype, doc)
	}
}

func (c *TMDBClient) processSearchResults(db *dbpkg.DB, itemURL string, title string, year int, qtype int, doc *htsmsg.HTSMsg) (int64, error) {
	results := doc.GetS32OrDefault("total_results", 0)
	c.trace("Query '%s' year:%d -> %d pages %d results",
		title, year,
		doc.GetS32OrDefault("total_pages", -1),
		results)

	resultList := doc.GetList("results")

	rval := int64(metadata.MetadataPermanentError)

	if resultList != nil {
		for _, f := range resultList.GetFields() {
			res := f.GetMap()
			if res == nil {
				continue
			}

			id := res.GetU32OrDefault("id", 0)
			if id == 0 {
				continue
			}

			md := metadata.Create()

			titleField := "title"
			c.mu.RLock()
			useOrigTitle := c.useOrigTitle
			c.mu.RUnlock()
			if useOrigTitle {
				titleField = "original_title"
			}
			if resTitle := res.GetStr(titleField); resTitle != "" {
				md.Title = resTitle
			}

			if releaseDate := res.GetStr("release_date"); releaseDate != "" && len(releaseDate) >= 4 {
				if year, err := strconv.Atoi(releaseDate[:4]); err == nil {
					md.Year = int16(year)
				}
			}

			popularity := res.GetDblOrDefault("popularity", 0)

			tmdbID := strconv.FormatUint(uint64(id), 10)
			dsid, cfgid := 0, int64(0)
			if c.source != nil {
				dsid, cfgid = c.source.ID, c.source.CfgID
			}
			itemID := c.mm.MetadbInsertVideoitem(db, itemURL,
				dsid, tmdbID, md,
				metadata.MetaItemStatusPartial,
				int64(popularity*1000), qtype, cfgid)
			md.Destroy()
			if itemID < 0 {
				doc.Release()
				return -1, fmt.Errorf("metadb_insert_videoitem failed: %d", itemID)
			}

			if itemID < 0 {
				doc.Release()
				return itemID, nil
			}

			if posterPath := res.GetStr("poster_path"); posterPath != "" {
				c.insertVideoArt(db, itemID, metadata.MetadataImagePoster, posterPath, "poster")
			}
			if backdropPath := res.GetStr("backdrop_path"); backdropPath != "" {
				c.insertVideoArt(db, itemID, metadata.MetadataImageBackdrop, backdropPath, "backdrop")
			}

			rval = 0
		}
	}

	doc.Release()
	return rval, nil
}

func (c *TMDBClient) QueryByTitleAndYear(ctx context.Context, db *dbpkg.DB, itemURL string, title string, year int, duration int, qtype int, initiator string) (int64, error) {
	if c.source == nil {
		return metadata.MetadataTemporaryError, nil
	}

	if err := c.configure(ctx); err != nil {
		return metadata.MetadataTemporaryError, err
	}

	cacheInfo := 0
	rval, err := c.queryByTitleAndYear0(ctx, db, itemURL, title, year, duration, qtype, &cacheInfo)

	c.usage.Event("TMDB query by title", 1,
		"qtype", metadata.MetadataQTypeStr(qtype),
		"initiator", initiator,
		"result", resultToStr(rval, cacheInfo))

	return rval, err
}

func (c *TMDBClient) QueryByIMDBID(ctx context.Context, db *dbpkg.DB, itemURL string, imdbID string, qtype int, initiator string) (int64, error) {
	if c.source == nil {
		return metadata.MetadataTemporaryError, nil
	}

	if err := c.configure(ctx); err != nil {
		return metadata.MetadataTemporaryError, err
	}

	cacheInfo := 0
	rval, err := c.loadMovieInfo(ctx, db, itemURL, imdbID, qtype, &cacheInfo)

	c.usage.Event("TMDB query by IMDB-id", 1,
		"qtype", metadata.MetadataQTypeStr(qtype),
		"initiator", initiator,
		"result", resultToStr(rval, cacheInfo))

	return rval, err
}

func (c *TMDBClient) QueryByID(ctx context.Context, db *dbpkg.DB, itemURL string, imdbID string, initiator string) (int64, error) {
	if c.source == nil {
		return metadata.MetadataTemporaryError, nil
	}

	if err := c.configure(ctx); err != nil {
		return metadata.MetadataTemporaryError, err
	}

	cacheInfo := 0
	rval, err := c.loadMovieInfo(ctx, db, itemURL, imdbID, 0, &cacheInfo)

	c.usage.Event("TMDB query by id", 1,
		"initiator", initiator,
		"result", resultToStr(rval, cacheInfo))

	return rval, err
}

// resultToStr — C: result_to_str (tmdb.c:637-658). Renders the
// usage_event "result" segment from rval + FA_CACHE_INFO.
func resultToStr(rval int64, cacheInfo int) string {
	switch rval {
	case metadata.MetadataTemporaryError:
		return "Temporary error"
	case metadata.MetadataPermanentError:
		return "Permanent error"
	default:
		switch cacheInfo {
		case fileaccesscore.FACacheInfoFromCache:
			return "OK (Cached)"
		case fileaccesscore.FACacheInfoFromCacheNotModified:
			return "OK (Cached-not-modified)"
		case fileaccesscore.FACacheInfoExpiredFromCache:
			return "OK (Cached-expired)"
		default:
			return "OK"
		}
	}
}

func (c *TMDBClient) SetUseOrigTitle(use bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.useOrigTitle = use
}

func (c *TMDBClient) SetLanguage(lang string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(lang) >= 2 {
		c.language = lang[:2]
	} else {
		c.language = ""
	}
}

func NewTMDBClient(apiKey string, pm *propcore.PropManager, ts *trace.TraceSystem, mm *metadata.MetadataManager, fam *fileaccesscore.FileAccessManager) *TMDBClient {
	if mm == nil {
		mm = metadata.NewMetadataManager(nil, nil)
	}
	if apiKey == "" {
		apiKey = defaultTMDBAPIKey
	}
	return &TMDBClient{
		apiKey:   apiKey,
		language: "en",
		pm:       pm,
		ts:       ts,
		mm:       mm,
		fam:      fam,
	}
}

func (c *TMDBClient) setup() {
	c.mu.Lock()
	defer c.mu.Unlock()

	// C: search_fns (tmdb.c:733-737)
	fns := &metadata.MetadataSourceFuncs{
		QueryByTitleAndYear: func(db *dbpkg.DB, itemURL, title string,
			year int, duration int, qtype int, initiator string) int64 {
			r, _ := c.QueryByTitleAndYear(context.Background(), db,
				itemURL, title, year, duration, qtype, initiator)
			return r
		},
		QueryByIMDBID: func(db *dbpkg.DB, itemURL, imdbID string,
			qtype int, initiator string) int64 {
			r, _ := c.QueryByIMDBID(context.Background(), db,
				itemURL, imdbID, qtype, initiator)
			return r
		},
		QueryByID: func(db *dbpkg.DB, itemURL, id string,
			initiator string) int64 {
			r, _ := c.QueryByID(context.Background(), db,
				itemURL, id, initiator)
			return r
		},
	}

	// C: tmdb = metadata_add_source("tmdb", "themoviedb.org", 100001,
	//      METADATA_TYPE_VIDEO, &search_fns, partial, complete)
	//    (tmdb.c:772-789)
	c.source = c.mm.MetadataAddSource("tmdb", "themoviedb.org", 100001,
		metadata.MetadataTypeVideo, fns,
		// Properties we resolve for a partial lookup
		uint64(metadata.MetadataPropTitle)|
			uint64(metadata.MetadataPropPoster)|
			uint64(metadata.MetadataPropYear),
		// Properties we resolve for a complete lookup
		uint64(metadata.MetadataPropTagline)|
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

	// C: tmdb_init settings tail (tmdb.c:791-813)
	//   prop_t *globallang = prop_create_multi(prop_get_global(),
	//     "i18n", "iso639_1", NULL);
	//   setting_create(SETTING_STRING, tmdb->ms_settings,
	//     SETTINGS_INITIAL_UPDATE,
	//     SETTING_TITLE(_p("Language (ISO 639-1 code)")),
	//     SETTING_VALUE_PROP(globallang),
	//     SETTING_CALLBACK(set_lang, NULL),
	//     SETTING_STORE("tmdb", "language"), NULL);
	//   setting_create(SETTING_BOOL, tmdb->ms_settings,
	//     SETTINGS_INITIAL_UPDATE,
	//     SETTING_TITLE(_p("Use original title")),
	//     SETTING_CALLBACK(use_orig_title, NULL),
	//     SETTING_STORE("tmdb", "enabled"), NULL);
	if c.sm != nil && c.source.Settings != nil {
		globallang := c.pm.CreateMultiPath(c.pm.GetGlobal(), "i18n", "iso639_1")

		c.sm.SettingCreate(settingscore.SettingString, c.source.Settings,
			settingscore.SettingsInitialUpdate,
			settingscore.SettingTagTitle,
			c.sm.P("Language (ISO 639-1 code)"),
			settingscore.SettingTagValueProp, globallang,
			settingscore.SettingTagCallback, c.setLang, nil,
			settingscore.SettingTagStore, "tmdb", "language")

		c.sm.SettingCreate(settingscore.SettingBool, c.source.Settings,
			settingscore.SettingsInitialUpdate,
			settingscore.SettingTagTitle,
			c.sm.P("Use original title"),
			settingscore.SettingTagCallback, c.useOrigTitleCb, nil,
			settingscore.SettingTagStore, "tmdb", "enabled")
	}
}

// setLang — C: set_lang (tmdb.c:754-762). Runs under the settings/prop
// mutex (like C's SETTING_CALLBACK) — must NOT take c.mu (init() holds
// it while the synchronous SETTINGS_INITIAL_UPDATE fires).
func (c *TMDBClient) setLang(opaque any, value any) {
	str, _ := value.(string)
	c.language = str
	c.updateCfgID()
}

// useOrigTitleCb — C: use_orig_title (tmdb.c:744-748). Same locking
// rule as setLang.
func (c *TMDBClient) useOrigTitleCb(opaque any, value any) {
	v, _ := value.(int)
	c.useOrigTitle = v != 0
	c.updateCfgID()
}

// updateCfgID — C: update_cfgid (tmdb.c:115-119).
//
//	tmdb->ms_cfgid = (1 << 24) | tmdb_language[0] |
//	  (tmdb_language[1] << 8) | (tmdb_use_orig_title << 16);
func (c *TMDBClient) updateCfgID() {
	if c.source == nil {
		return
	}
	var l0, l1, uo int64
	if len(c.language) > 0 {
		l0 = int64(c.language[0])
	}
	if len(c.language) > 1 {
		l1 = int64(c.language[1])
	}
	if c.useOrigTitle {
		uo = 1
	}
	c.source.CfgID = (1 << 24) | l0 | (l1 << 8) | (uo << 16)
}

// NewTMDBMetadata initializes TMDB metadata backend
// Returns a TMDBClient that should be used for all TMDB operations
// NewTMDBMetadata — C: tmdb_init (tmdb.c:764-814, INIT_GROUP_API) plus
// the BE_REGISTER'd be_tmdb imageloader backend. sm is the settings
// manager (C: global settings access).
func NewTMDBMetadata(pm *propcore.PropManager, bs *backendcore.BackendSystem, sm *settingscore.SettingsManager, mm *metadata.MetadataManager) *TMDBClient {
	client := NewTMDBClient("", pm, nil, mm, bs.FileAccessManager())
	client.usage = bs.Usage()
	client.sm = sm
	client.setup()
	tmdbBackendSetup(client, bs)
	return client
}

// tmdbBackendCanHandle — C: be_tmdb.be_canhandle.
func tmdbBackendCanHandle(url string) int {
	if len(url) > 5 && url[:5] == "tmdb:" {
		return 1
	}
	return 0
}

func tmdbBackendImageloader(client *TMDBClient, bs *backendcore.BackendSystem,
	url string, imageMeta any,
	cacheControl *int, cancellable any, be *backendcore.Backend) (any, error) {

	if client == nil {
		return nil, nil
	}

	ctx := context.Background()
	if err := client.configure(ctx); err != nil {
		return nil, errors.New("Failed to load TMDB configuration")
	}

	var sizes []*tmdbImageSize
	var path string

	client.mu.RLock()
	if p, ok := strings.CutPrefix(url, "tmdb:image:poster:"); ok {
		sizes = client.posterSizes
		path = p
	} else if p, ok := strings.CutPrefix(url, "tmdb:image:backdrop:"); ok {
		sizes = client.backdropSizes
		path = p
	} else if p, ok := strings.CutPrefix(url, "tmdb:image:profile:"); ok {
		sizes = client.profileSizes
		path = p
	}
	client.mu.RUnlock()

	// C: else { snprintf(errbuf, "Invalid TMDB url"); return NULL; }
	if path == "" {
		return nil, errors.New("Invalid TMDB url")
	}

	m := htsmsg.NewList()

	for _, s := range sizes {
		img := htsmsg.NewMap()
		u := fmt.Sprintf("%s%s%s", client.imageBaseURL, s.prefix, path)
		img.AddStr("url", u)
		if s.width > 0 {
			img.AddU32("width", uint32(s.width))
		}
		if s.height > 0 {
			img.AddU32("height", uint32(s.height))
		}
		m.AddMsg("", img)
	}

	// C: rstr = htsmsg_json_serialize_to_rstr(m, "imageset:")
	rstr, err := htsmsg.SerializeJSONToRstr(m, "imageset:")
	m.Release()
	if err != nil {
		return nil, errors.New("Failed to serialize image set")
	}

	// C: img = backend_imageloader(rstr, im, errbuf, errlen,
	//     cache_control, c, be) (tmdb.c:868-870)
	img, ierr := bs.Imageloader(rstr, imageMeta, cacheControl,
		cancellable, be)
	// C: if(img != NULL && img != NOT_MODIFIED) img->im_flags |= IMAGE_ADAPTED
	if img != nil && img != backendcore.NotModifiedImage {
		if i, ok := img.(*imagepkg.Image); ok {
			i.Flags |= imagepkg.FlagAdapted
		}
	}
	return img, ierr
}

func tmdbBackendSetup(client *TMDBClient, bs *backendcore.BackendSystem) {
	// Register TMDB backend
	be := &backendcore.Backend{
		Prefix:    "tmdb:",
		CanHandle: tmdbBackendCanHandle,
		Imageloader: func(url string, imageMeta any,
			cacheControl *int, cancellable any, be *backendcore.Backend) (any, error) {
			if be != nil && be.Opaque != nil {
				if client, ok := be.Opaque.(*TMDBClient); ok {
					return tmdbBackendImageloader(client, bs, url, imageMeta, cacheControl, cancellable, be)
				}
			}
			return nil, nil
		},
		Opaque: client,
	}
	if bs != nil {
		bs.Register(be)
	}
}
