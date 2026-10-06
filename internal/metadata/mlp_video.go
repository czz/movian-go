package metadata

// Canonical 1:1 port of src/metadata/mlp.c
//
// Lazy-loaded metadata: artist pictures, album art and video info are
// bound to props; a subscription monitor on each bound prop enqueues the
// item on the mlp queue only when an interested (non-monitor) subscriber
// appears. Worker threads (max 4) drain the queue and run the per-class
// load callback.
//
// C file-statics mapped onto the MetadataManager:
//   metadata_mutex          -> mm.mlpMutex
//   metadata_loading_cond   -> mm.mlpCond
//   metadata_num_threads    -> mm.metadataNumThreads
//   mlpqueue                -> mm.mlpQueue

import (
	"fmt"
	"strconv"
	"unsafe"

	dbpkg "github.com/czz/movian-go/internal/db"
	"github.com/czz/movian-go/internal/db/kvstore"
	eventpkg "github.com/czz/movian-go/internal/event"
	"github.com/czz/movian-go/internal/misc"
	"github.com/czz/movian-go/internal/nls"
	propcore "github.com/czz/movian-go/internal/prop"
)

// MetadataLazyVideo — C: metadata_lazy_video_t (mlp.c:397-437)
type MetadataLazyVideo struct {
	MLP       MetadataLazyProp // mlv_mlp
	Initiator *misc.Rstr       // mlv_initiator
	URL       *misc.Rstr       // mlv_url
	Filename  *misc.Rstr       // mlv_filename
	Folder    *misc.Rstr       // mlv_folder
	IMDBID    *misc.Rstr       // mlv_imdb_id

	Root *propcore.Prop // mlv_root
	M    *propcore.Prop // mlv_m

	TitleOpt     *propcore.Prop // mlv_title_opt
	InfoOpt      *propcore.Prop // mlv_info_opt
	InfoTextProp *propcore.Prop // mlv_info_text_prop
	InfoTextRstr *misc.Rstr     // mlv_info_text_rstr

	SourceOpt    *propcore.Prop         // mlv_source_opt
	SourceOptSub *propcore.Subscription // mlv_source_opt_sub

	AltOpt    *propcore.Prop         // mlv_alt_opt
	AltOptSub *propcore.Subscription // mlv_alt_opt_sub

	RefreshOpt *propcore.Prop         // mlv_refresh_opt
	RefreshSub *propcore.Subscription // mlv_refresh_sub

	CustomQuery    *misc.Rstr             // mlv_custom_query
	CustomQueryOpt *propcore.Prop         // mlv_custom_query_opt
	CustomQuerySub *propcore.Subscription // mlv_custom_query_sub

	CustomTitle    *misc.Rstr             // mlv_custom_title
	CustomTitleOpt *propcore.Prop         // mlv_custom_title_opt
	CustomTitleSub *propcore.Subscription // mlv_custom_title_sub

	OptionsMonitorSub *propcore.Subscription // mlv_options_monitor_sub

	// Triggers
	TrigTitle  *propcore.Subscription // mlv_trig_title
	TrigDesc   *propcore.Subscription // mlv_trig_desc
	TrigRating *propcore.Subscription // mlv_trig_rating

	Duration int          // mlv_duration
	Type     MetadataType // mlv_type
	Lonely   int          // mlv_lonely : 1
	Passive  int          // mlv_passive : 1
	Manual   int          // mlv_manual : 1
	QType    int          // mlv_qtype : 5
	// C: union { int16_t mlv_season; int16_t mlv_year; }
	Season  int16 // mlv_season (when QType == TVSHOW)
	Year    int16 // mlv_year   (when QType == MOVIE)
	Episode int16 // mlv_episode
	Dsid    int   // mlv_dsid
}

// mlvCleanup — C: mlv_cleanup (mlp.c:444-457)
func (mm *MetadataManager) mlvCleanup(mlv *MetadataLazyVideo) {
	pm := mm.pm
	pm.SetVEx(nil, mlv.M, "source", nil)
	pm.SetVEx(nil, mlv.M, "icon", nil)
	pm.SetVEx(nil, mlv.M, "tagline", nil)
	pm.SetVEx(nil, mlv.M, "description", nil)
	pm.SetVEx(nil, mlv.M, "backdrop", nil)
	pm.SetVEx(nil, mlv.M, "genre", nil)
	pm.SetVEx(nil, mlv.M, "year", nil)
	pm.SetVEx(nil, mlv.M, "rating", nil)
	pm.SetVEx(nil, mlv.M, "rating_count", nil)
	pm.SetVEx(nil, mlv.M, "vtype", nil)
}

// MLVUnbind — C: mlv_unbind (mlp.c:463-472)
func (mm *MetadataManager) MLVUnbind(mlv *MetadataLazyVideo, cleanup int) {
	if mlv == nil {
		return
	}
	mm.mlpMutex.Lock()
	if cleanup != 0 {
		mm.mlvCleanup(mlv)
		mm.pm.SetVEx(nil, mlv.M, "title", misc.RstrGet(mlv.Filename))
	}
	mm.mlpDestroy(&mlv.MLP)
	mm.mlpMutex.Unlock()
}

// buildInfoText — C: build_info_text (mlp.c:478-531)
func (mm *MetadataManager) buildInfoText(mlv *MetadataLazyVideo, md *Metadata) {
	var txt string

	if md == nil {
		txt = nls.GetRString("No data found")
	} else {
		var qtype string

		switch md.QType {
		case MetadataQTypeFilename:
			qtype = nls.GetRString("filename")
		case MetadataQTypeIMDB:
			qtype = nls.GetRString("IMDb ID")
		case MetadataQTypeCustomIMDB:
			qtype = nls.GetRString("custom IMDb ID")
		case MetadataQTypeDirectory:
			qtype = nls.GetRString("folder name")
		case MetadataQTypeCustom:
			qtype = nls.GetRString("custom query")
		case MetadataQTypeEpisode:
			qtype = nls.GetRString("filename as TV episode")
		case MetadataQTypeMovie:
			qtype = nls.GetRString("Movie title")
		case MetadataQTypeTVShow:
			qtype = nls.GetRString("Title, Season, Episode")
		}

		format := nls.GetRString("Metadata loaded from <b>%s</b> based on %s")

		ms := mlv.MLP.mm.MetadataSourceGet(mlv.Type, int(md.DSID))

		descr := "???"
		if ms != nil {
			descr = ms.Description
		}
		if qtype == "" {
			qtype = "???"
		}

		// C: snprintf(tmp, sizeof(tmp), rstr_get(fmt), ...)
		txt = fmt.Sprintf(format, descr, qtype)
	}

	// C: prop_set_rstring_ex(mlv->mlv_info_text_prop, NULL, txt,
	//    PROP_STR_RICH)
	if mlv.InfoTextProp != nil {
		mm.pm.SetStringEx(mlv.InfoTextProp, nil, txt, propcore.StringRich)
	}
	misc.RstrRelease(mlv.InfoTextRstr)
	mlv.InfoTextRstr = misc.RstrAllocStr(txt)
}

// isQtypeCompat — C: is_qtype_compat (mlp.c:537-549)
func isQtypeCompat(qa, qb int) int {
	if qa == qb {
		return 1
	}
	if qa == MetadataQTypeFilenameOrDirectory &&
		(qb == MetadataQTypeFilename || qb == MetadataQTypeDirectory ||
			qb == MetadataQTypeEpisode) {
		return 1
	}
	return 0
}

// queryByFilenameOrDirname — C: query_by_filename_or_dirname
// (mlp.c:555-649)
func (mm *MetadataManager) queryByFilenameOrDirname(dbc *dbpkg.DB,
	mlv *MetadataLazyVideo, msf *MetadataSourceFuncs, qtype *int,
	duration, lonely int) int64 {

	var year int
	var title *misc.Rstr
	var rval int64
	var season, episode int

	if MetadataFilenameToEpisode(misc.RstrGet(mlv.Filename),
		&season, &episode, &title) == 0 {

		if msf == nil || msf.QueryByEpisode == nil {
			misc.RstrRelease(title)
			return int64(MetadataPermanentError)
		}

		if title == nil {
			if mlv.Folder != nil {
				MetadataFolderToSeason(misc.RstrGet(mlv.Folder),
					nil, &title)
			}
			if title == nil {
				mm.metadataTrace(
					"Unable to figure out name of series from %s",
					misc.RstrGet(mlv.Filename))
				return int64(MetadataPermanentError)
			}
			mm.metadataTrace(
				"Performing search lookup for %s season:%d episode:%d, "+
					"based on filename and foldername",
				misc.RstrGet(title), season, episode)
		} else {
			mm.metadataTrace(
				"Performing search lookup for %s season:%d episode:%d, "+
					"based on filename",
				misc.RstrGet(title), season, episode)
		}

		rval = msf.QueryByEpisode(dbc, misc.RstrGet(mlv.URL),
			misc.RstrGet(title), season, episode,
			MetadataQTypeEpisode,
			misc.RstrGet(mlv.Initiator))
		*qtype = MetadataQTypeEpisode
		misc.RstrRelease(title)
		return rval
	}

	if msf == nil || msf.QueryByTitleAndYear == nil {
		return int64(MetadataPermanentError)
	}

	if IsReasonableMovieName(misc.RstrGet(mlv.Filename)) != 0 {

		MetadataFilenameToTitle(misc.RstrGet(mlv.Filename), &year, &title)

		mm.metadataTrace(
			"Performing search lookup for %s year:%d, based on filename",
			misc.RstrGet(title), year)

		rval = msf.QueryByTitleAndYear(dbc, misc.RstrGet(mlv.URL),
			misc.RstrGet(title), year,
			duration,
			MetadataQTypeFilename,
			misc.RstrGet(mlv.Initiator))
		*qtype = MetadataQTypeFilename

		if rval == int64(MetadataPermanentError) && year != 0 {
			// Try without year
			mm.metadataTrace(
				"Performing search lookup for %s without year, based on filename",
				misc.RstrGet(title))

			rval = msf.QueryByTitleAndYear(dbc, misc.RstrGet(mlv.URL),
				misc.RstrGet(title), 0,
				duration,
				MetadataQTypeFilename,
				misc.RstrGet(mlv.Initiator))
			*qtype = MetadataQTypeFilename
		}
		misc.RstrRelease(title)
	} else {
		rval = int64(MetadataPermanentError)
	}

	if rval == int64(MetadataPermanentError) && lonely != 0 && mlv.Folder != nil {

		MetadataFilenameToTitle(misc.RstrGet(mlv.Folder), &year, &title)

		mm.metadataTrace(
			"Performing search lookup for %s year:%d, based on folder name",
			misc.RstrGet(title), year)

		rval = msf.QueryByTitleAndYear(dbc, misc.RstrGet(mlv.URL),
			misc.RstrGet(title), year,
			duration,
			MetadataQTypeDirectory,
			misc.RstrGet(mlv.Initiator))
		*qtype = MetadataQTypeDirectory
		misc.RstrRelease(title)
	}

	return rval
}

// setPeople — C: set_people (mlp.c:695-716)
func (mm *MetadataManager) setPeople(parent *propcore.Prop,
	q []MetadataPerson, crew int) {
	pm := mm.pm
	pm.DestroyChilds(parent)
	for i := range q {
		mp := &q[i]
		p := pm.CreateRootEx("", false)
		pm.SetVEx(nil, p, "name", mp.Name)
		if crew != 0 {
			pm.SetVEx(nil, p, "department", mp.Department)
			pm.SetVEx(nil, p, "job", mp.Job)
		} else {
			pm.SetVEx(nil, p, "character", mp.Character)
		}
		pm.SetVEx(nil, p, "portrait", mp.Portrait)

		// C: if(prop_set_parent(p, parent)) { prop_destroy(p); break; }
		if pm.SetParentEx(p, parent, nil, "") != 0 {
			pm.Destroy(p)
			break // rest will fail as well
		}
	}
}

// setCastNCrew — C: set_cast_n_crew (mlp.c:722-732)
func (mm *MetadataManager) setCastNCrew(p *propcore.Prop, md *Metadata) {
	pm := mm.pm
	p2 := pm.RefInc(pm.CreateEx(p, "cast", nil, false, false))
	mm.setPeople(p2, md.Cast, 0)
	pm.RefDec(p2)

	p2 = pm.RefInc(pm.CreateEx(p, "crew", nil, false, false))
	mm.setPeople(p2, md.Crew, 1)
	pm.RefDec(p2)
}

// setArtwork — C: set_artwork (mlp.c:738-764)
func (mm *MetadataManager) setArtwork(p *propcore.Prop, name string,
	vec []string) {
	pm := mm.pm
	if vec == nil {
		return
	}
	// C: prop_set(p, name, PROP_SET_RSTRING, vec->v[0])
	pm.SetVEx(nil, p, name, vec[0])

	plural := name + "s"

	v := pm.RefInc(pm.CreateEx(p, plural, nil, false, false))
	if v == nil {
		return
	}
	pm.MarkChilds(v)
	for i := range vec {
		x := pm.RefInc(pm.CreateEx(v, vec[i], nil, false, false))
		pm.Unmark(x)
		pm.SetVEx(nil, x, "url", vec[i])
		pm.RefDec(x)
	}
	pm.DestroyMarkedChilds(v)
	pm.RefDec(v)
}

// mlvGetVideoInfo0 — C: mlv_get_video_info0 (mlp.c:754-1220).
//
// Runs with mm.mlpMutex held on entry (like C, metadata_mutex); the mutex
// is released around network/DB I/O and re-acquired before return.
// refresh=0: normal path; refresh=1: bypass cache (force re-query).
func (mm *MetadataManager) mlvGetVideoInfo0(dbc *dbpkg.DB,
	mlv *MetadataLazyVideo, refresh int) int {

	var title *misc.Rstr
	var md *Metadata
	var ms *MetadataSource
	var r int
	var fixedDs int
	var rval int64

	sq := misc.RstrGet(mlv.CustomQuery)
	sqIsImdbID := len(sq) >= 3 && sq[0] == 't' && sq[1] == 't' &&
		sq[2] >= '0' && sq[2] <= '9'

	// C: if(sq && !*sq) sq = NULL;  — an empty custom query behaves as
	// unset. sqIsSet models the C sq != NULL pointer check.
	sqIsSet := sq != ""

	/**
	 * Get a list of currently available metadata sources
	 */
	mm.msMu.Lock()
	numMsqi := len(mm.msSources[mlv.Type])
	msqivec := make([]MetadataSourceQueryInfo, numMsqi)
	for i, s := range mm.msSources[mlv.Type] {
		msqivec[i].MS = s
	}
	mm.msMu.Unlock()

	/**
	 * Copy mutable members before unlocking metadata_mutex
	 * (C comment mlp.c:797-810): mlv_custom_title, mlv_imdb_id,
	 * mlv_lonely, mlv_duration.
	 */
	duration := mlv.Duration
	lonely := mlv.Lonely

	var customTitle *misc.Rstr
	if mlv.CustomTitle == nil || misc.RstrGet(mlv.CustomTitle) != "" {
		customTitle = nil
	} else {
		customTitle = misc.RstrDup(mlv.CustomTitle)
	}

	imdbID := misc.RstrDup(mlv.IMDBID)

	mm.mlpRetain(&mlv.MLP) // Make sure we don't get deleted

	// Avoid racing lookups between different threads
	for mlv.MLP.Loading {
		mm.mlpCond.Wait()
	}
	mlv.MLP.Loading = true

	mm.mlpMutex.Unlock()

	mm.metadataTrace("Processing '%s' "+
		"custom_title=%s imdbid=%s lonely=%s duration=%d%s",
		misc.RstrGet(mlv.URL),
		misc.RstrGet(customTitle),
		misc.RstrGet(imdbID),
		map[bool]string{true: "yes", false: "no"}[lonely != 0],
		duration,
		map[bool]string{true: ", force-refresh", false: ""}[refresh != 0])

	disableCache := refresh != 0

	/**
	 * If duration is low skip this unless user have specified a custom
	 * query or if we have an IMDB ID
	 */
	if duration != -1 && duration < MetadataDurationLimit &&
		!sqIsSet && misc.RstrGet(imdbID) == "" {
		goto bad
	}

	if duration == -1 {
		duration = 0
	}

	if refresh == 0 {
		/**
		 * If we are not refreshing, read from database
		 * (basically our cache)
		 */
		r = mm.MetadbGetVideoinfo(dbc, misc.RstrGet(mlv.URL),
			msqivec, &fixedDs, &md, mlv.Manual)
		if r != 0 {
			goto done // Found something, we're done
		}
	} else {
		fixedDs = 0
	}

	mm.pm.SetVEx(nil, mlv.M, "loading", 1)

	if mlv.Manual == 0 && (md == nil || !md.Preferred) {

		for i := range numMsqi {

			msqi := &msqivec[i]
			ms = msqi.MS

			/* Skip disabled datasources */
			if ms.Enabled == 0 {
				continue
			}

			msf := ms.Funcs

			/* If we have a fixed datasource (requested by user)
			 * skip all other datasources
			 */
			if fixedDs != 0 && fixedDs != ms.ID {
				continue
			}

			/* Figure out what query to run */
			var qtype int
			var q string

			if msf != nil && msf.QueryByIMDBID != nil && sqIsImdbID {
				qtype = MetadataQTypeCustomIMDB
				q = sq
			} else if sqIsSet {
				qtype = MetadataQTypeCustom
				q = ""
			} else if msf != nil && msf.QueryByIMDBID != nil &&
				imdbID != nil {
				if mlv.Passive != 0 {
					continue
				}

				qtype = MetadataQTypeIMDB
				q = misc.RstrGet(imdbID)

			} else if mlv.QType == MetadataQTypeMovie {

				if msf == nil || msf.QueryByTitleAndYear == nil {
					continue
				}

				qtype = MetadataQTypeMovie
				q = ""

			} else if mlv.QType == MetadataQTypeTVShow {

				if msf == nil || msf.QueryByEpisode == nil {
					continue
				}

				qtype = MetadataQTypeTVShow
				q = ""

			} else {
				if mlv.Passive != 0 {
					continue
				}
				qtype = MetadataQTypeFilenameOrDirectory
				q = ""
			}

			if !disableCache {
				if md != nil && int(md.DSID) == ms.ID &&
					isQtypeCompat(qtype, md.QType) != 0 {
					break
				}

				/**
				 * If current metadata source is seen (marked by
				 * metadb_get_videoinfo()) and the query type
				 * corresponds we should be fully up to date,
				 * thus continue
				 */
				if msqi.Mark &&
					isQtypeCompat(qtype, msqi.QType) != 0 {

					/**
					 * This weirdness is to be able to requery if
					 * we discover that a movie is lonely in its
					 * folder (To query using directory name)
					 */
					if msqi.Status == MetaItemStatusAbsent &&
						msqi.QType == MetadataQTypeFilename &&
						lonely != 0 {

					} else {
						continue
					}
				}
			}

			rval = int64(mm.MetadbVideoitemDeleteFromDs(dbc,
				misc.RstrGet(mlv.URL), ms.ID))

			if rval == 0 {

				switch qtype {
				case MetadataQTypeIMDB, MetadataQTypeCustomIMDB:

					mm.metadataTrace(
						"Performing IMDB lookup for %s using %s for %s",
						q, ms.Name, misc.RstrGet(mlv.URL))

					rval = msf.QueryByIMDBID(dbc,
						misc.RstrGet(mlv.URL), q, qtype,
						misc.RstrGet(mlv.Initiator))

				case MetadataQTypeFilenameOrDirectory:
					var qt int
					rval = mm.queryByFilenameOrDirname(dbc, mlv,
						msf, &qt, duration, lonely)
					qtype = qt

				case MetadataQTypeMovie:
					mm.metadataTrace(
						"Performing search lookup on movie title %s, "+
							"year:%d using %s for %s",
						misc.RstrGet(mlv.Filename), mlv.Year,
						ms.Name, misc.RstrGet(mlv.URL))

					rval = msf.QueryByTitleAndYear(dbc,
						misc.RstrGet(mlv.URL),
						misc.RstrGet(mlv.Filename),
						int(mlv.Year), duration, qtype,
						misc.RstrGet(mlv.Initiator))

				case MetadataQTypeTVShow:
					rval = msf.QueryByEpisode(dbc,
						misc.RstrGet(mlv.URL),
						misc.RstrGet(mlv.Filename),
						int(mlv.Season), int(mlv.Episode),
						qtype,
						misc.RstrGet(mlv.Initiator))

				case MetadataQTypeCustom:
					if msf == nil || msf.QueryByTitleAndYear == nil {
						continue
					}

					mm.metadataTrace(
						"Performing custom search lookup for %s "+
							"using %s for %s",
						sq, ms.Name, misc.RstrGet(mlv.URL))
					rval = msf.QueryByTitleAndYear(dbc,
						misc.RstrGet(mlv.URL),
						sq, 0, duration, qtype,
						misc.RstrGet(mlv.Initiator))

				default:
					continue
				}
			}

			if rval == int64(MetadataDeadlock) ||
				rval == int64(MetadataTemporaryError) {
				mm.metadataTrace("%s for %s",
					misc.RstrGet(mlv.URL),
					map[bool]string{
						true:  "Deadlock",
						false: "Temporary error",
					}[rval == int64(MetadataDeadlock)])

				mm.pm.SetVEx(nil, mlv.M, "loading", 0)
				if md != nil {
					MetadataDestroy(md)
				}

				r = int(rval)
				goto done
			}

			if rval == int64(MetadataPermanentError) {
				rval = mm.MetadbInsertVideoitem(dbc,
					misc.RstrGet(mlv.URL), ms.ID,
					"0", nil, MetaItemStatusAbsent, 0,
					qtype, ms.CfgID)
			}

			if rval < 0 {
				mm.pm.SetVEx(nil, mlv.M, "loading", 0)
				if md != nil {
					MetadataDestroy(md)
				}
				r = int(rval)
				goto done
			}
		}
		if md != nil {
			MetadataDestroy(md)
		}
		md = nil
		r = mm.MetadbGetVideoinfo(dbc, misc.RstrGet(mlv.URL),
			msqivec, &fixedDs, &md, mlv.Manual)
	}

	if md != nil &&
		md.MetaItemStatus == MetaItemStatusPartial &&
		md.ExtID != "" &&
		func() bool {
			ms = mlv.MLP.mm.MetadataSourceGet(mlv.Type, int(md.DSID))
			return ms != nil
		}() &&
		ms.Funcs != nil && ms.Funcs.QueryByID != nil &&
		(mlv.MLP.ReqItems&ms.CompleteProps) != 0 {

		mm.metadataTrace(
			"Performing additional query for %s : %s", ms.Name,
			md.ExtID)

		rval = ms.Funcs.QueryByID(dbc, misc.RstrGet(mlv.URL),
			md.ExtID,
			misc.RstrGet(mlv.Initiator))
		MetadataDestroy(md)

		if rval == int64(MetadataDeadlock) {
			r = MetadataDeadlock
			goto done
		}

		if rval == int64(MetadataTemporaryError) {
			mm.metadataTrace("Temporary error for %s",
				misc.RstrGet(mlv.URL))
			r = MetadataTemporaryError
			goto done
		}

		if rval == int64(MetadataPermanentError) {
			mm.metadataTrace("Permanent error for %s",
				misc.RstrGet(mlv.URL))
		}

		r = mm.MetadbGetVideoinfo(dbc, misc.RstrGet(mlv.URL),
			msqivec, &fixedDs, &md, 0)
		if r != 0 {
			mm.pm.SetVEx(nil, mlv.M, "loading", 0)
			goto done
		}
	}

	// C: if(mlv->mlv_m != NULL) { title = rstr_dup(custom_title); ... }
	// The outer guard only gates the prop writes; the goto-bad path
	// lands below at label bad.
	if mlv.M != nil {
		title = misc.RstrDup(customTitle)
	}

	if md != nil && mlv.M != nil {
		{
			mlv.Dsid = int(md.DSID)
			ms = mlv.MLP.mm.MetadataSourceGet(mlv.Type, int(md.DSID))
			if ms != nil {
				mm.pm.SetVEx(nil, mlv.M, "source", ms.Description)
			}

			mm.pm.SetVEx(nil, mlv.M, "tagline", md.Tagline)
			mm.pm.SetVEx(nil, mlv.M, "description", md.Description)
			mm.pm.SetVEx(nil, mlv.M, "genre", md.Genre)

			mm.setArtwork(mlv.M, "icon", md.Icons)
			mm.setArtwork(mlv.M, "backdrop", md.Backdrops)
			mm.setArtwork(mlv.M, "thumb", md.Thumbs)

			if md.Year != 0 {
				mm.pm.SetVEx(nil, mlv.M, "year", int(md.Year))
			} else {
				mm.pm.SetVEx(nil, mlv.M, "year", nil)
			}

			if md.Rating >= 0 {
				mm.pm.SetVEx(nil, mlv.M, "rating", int(md.Rating))
			} else {
				mm.pm.SetVEx(nil, mlv.M, "rating", nil)
			}

			if md.RatingCount >= 0 {
				mm.pm.SetVEx(nil, mlv.M, "rating_count",
					md.RatingCount)
			} else {
				mm.pm.SetVEx(nil, mlv.M, "rating_count", nil)
			}

			mm.setCastNCrew(mlv.M, md)

			var season *Metadata
			var series *Metadata

			if md.Parent != nil &&
				md.Parent.Type == MetadataTypeSeason {
				season = md.Parent
				// It's a TV serie

				pepi := mm.pm.CreateEx(mlv.M, "episode", nil,
					false, false)
				psea := mm.pm.CreateEx(mlv.M, "season", nil,
					false, false)
				pser := mm.pm.CreateEx(mlv.M, "series", nil,
					false, false)
				pepi = mm.pm.RefInc(pepi)
				psea = mm.pm.RefInc(psea)
				pser = mm.pm.RefInc(pser)

				mm.pm.SetVEx(nil, pepi, "number", int(md.Idx))
				mm.pm.SetVEx(nil, pepi, "title", md.Title)
				mm.setArtwork(pepi, "backdrop", md.Backdrops)
				mm.setArtwork(pepi, "wideBanner", md.WideBanners)
				mm.setArtwork(pepi, "icon", md.Icons)

				mm.pm.SetVEx(nil, psea, "number",
					int(season.Idx))
				mm.pm.SetVEx(nil, psea, "title", season.Title)
				mm.setArtwork(psea, "backdrop",
					season.Backdrops)
				mm.setArtwork(psea, "wideBanner",
					season.WideBanners)
				mm.setArtwork(psea, "icon", season.Icons)

				mm.setCastNCrew(psea, season)

				if season.Parent != nil &&
					season.Parent.Type == MetadataTypeSeries {
					series = season.Parent

					mm.pm.SetVEx(nil, pser, "title",
						series.Title)

					mm.setArtwork(pser, "backdrop",
						series.Backdrops)
					mm.setArtwork(pser, "wideBanner",
						series.WideBanners)
					mm.setArtwork(pser, "icon",
						series.Icons)

					mm.setCastNCrew(pser, series)
				}

				if title == nil {
					title = misc.RstrDup(mlv.Filename)
				}

				mm.pm.RefDec(pepi)
				mm.pm.RefDec(psea)
				mm.pm.RefDec(pser)

			} else {
				if title == nil {
					title = misc.RstrAllocStr(md.Title)
				}
			}

			if season != nil {
				mm.pm.SetVEx(nil, mlv.M, "vtype", "tvseries")
			} else {
				mm.pm.SetVEx(nil, mlv.M, "vtype", nil)
			}

			mm.buildInfoText(mlv, md)
			MetadataDestroy(md)
		}
		goto setProps
	}

bad:
	// C: bad: if(mlv->mlv_m != NULL) { mlv_cleanup(mlv); ... }
	if mlv.M != nil {

		mm.mlvCleanup(mlv)

		if title == nil {
			if customTitle != nil {
				title = misc.RstrDup(customTitle)
			} else {
				title = misc.RstrDup(mlv.Filename)
			}
		}
		mlv.Dsid = 0
		mm.buildInfoText(mlv, nil)
	}

setProps:
	// C: prop_set(mlv->mlv_m, ...) — NULL-safe, no-ops for direct query
	if mlv.M != nil {
		mm.pm.SetVEx(nil, mlv.M, "title", misc.RstrGet(title))
		mm.pm.SetVEx(nil, mlv.M, "loading", 0)
	}

	misc.RstrRelease(title)
	r = 0

done:
	misc.RstrRelease(customTitle)
	misc.RstrRelease(imdbID)
	mm.mlpMutex.Lock()

	mlv.MLP.Loading = false
	mm.mlpCond.Broadcast()

	mm.mlpRelease(&mlv.MLP)
	return r
}

// mlvLoad — C: mlv_load (mlp.c:1226-1230)
func mlvLoad(dbc *dbpkg.DB, mlp *MetadataLazyProp) {
	mlp.mm.mlvGetVideoInfo0(dbc,
		(*MetadataLazyVideo)(unsafe.Pointer(mlp)), 0)
}

// loadAlternatives — C: load_alternatives (mlp.c:1238-1245)
func (mm *MetadataManager) loadAlternatives(mlv *MetadataLazyVideo) {
	p := mm.pm.CreateEx(mlv.AltOpt, "options", nil, false, false)
	p = mm.pm.RefInc(p)
	mm.MetadbVideoitemAlternatives(p, misc.RstrGet(mlv.URL), mlv.Dsid,
		mlv.AltOptSub)
	mm.pm.RefDec(p)
}

// mlvSetPreferred — C: mlv_set_preferred (mlp.c:1251-1258)
func (mm *MetadataManager) mlvSetPreferred(mlv *MetadataLazyVideo,
	vid int64) {
	dbc := mm.Get()
	mm.MetadbVideoitemSetPreferred(dbc, misc.RstrGet(mlv.URL), vid)
	mm.mlvGetVideoInfo0(dbc, mlv, 0)
	mm.Close(dbc)
}

// mlvSubAlternative — C: mlv_sub_alternative (mlp.c:1264-1293)
func (mm *MetadataManager) mlvSubAlternative(mlv *MetadataLazyVideo,
	event propcore.EventType, args ...any) {
	switch event {
	case propcore.EventDestroyed:
		mm.mlpDestroy(&mlv.MLP)

	case propcore.EventSubscriptionMonitorActive:
		mm.loadAlternatives(mlv)

	case propcore.EventSelectChild:
		var p *propcore.Prop
		if len(args) > 0 {
			p, _ = args[0].(*propcore.Prop)
		}
		if p != nil {
			if name := p.GetName(); name != "" {
				if vid, err := strconv.ParseInt(name, 10, 64); err == nil {
					mm.mlvSetPreferred(mlv, vid)
				}
			}
		}
	}
}

// loadSources — C: load_sources (mlp.c:1300-1345)
func (mm *MetadataManager) loadSources(mlv *MetadataLazyVideo) {
	pm := mm.pm
	var active *propcore.Prop

	vec := pm.CreateRootEx("", true)
	p := pm.RefInc(pm.CreateEx(mlv.SourceOpt, "options", nil, false, false))
	cur := mm.MetadbItemGetPreferredDs(misc.RstrGet(mlv.URL))

	if mlv.Manual == 0 {
		c := pm.CreateRootEx("0", false)
		pm.Link(nls.GetProp("Automatic"),
			pm.CreateEx(c, "title", nil, false, false),
			nil, false, false)
		pm.SetParentEx(c, vec, nil, "")
	}

	mm.msMu.Lock()

	for _, ms := range mm.msSources[mlv.Type] {
		if ms.Enabled == 0 {
			continue
		}
		c := pm.CreateRootEx(ms.Name, false)
		pm.CreateEx(c, "title", nil, false, false).SetString(ms.Description)
		pm.SetParentEx(c, vec, nil, "")
		if cur == ms.ID {
			active = pm.RefInc(c)
		}
	}
	mm.msMu.Unlock()

	c := pm.CreateRootEx("1", false)
	pm.Link(nls.GetProp("None"),
		pm.CreateEx(c, "title", nil, false, false), nil, false, false)
	pm.SetParentEx(c, vec, nil, "")
	if cur == 1 || (mlv.Manual != 0 && cur == 0) {
		active = pm.RefInc(c)
	}

	pm.DestroyChilds(p)
	pm.SetParentVector(vec, p, nil, "")

	if active != nil {
		pm.SelectChildPropEx(active, nil, mlv.SourceOptSub)
	} else if len(vec.GetChildren()) > 0 {
		pm.SelectChildPropEx(vec.GetChildren()[0], nil, mlv.SourceOptSub)
	}

	if active != nil {
		pm.RefDec(active)
	}
	pm.Destroy(vec)
	pm.RefDec(p)
}

// mlvSetSource — C: mlv_set_source (mlp.c:1350-1382)
func (mm *MetadataManager) mlvSetSource(mlv *MetadataLazyVideo,
	name string) {
	id := 0

	if name != "" {

		if name == "1" {
			// dsid 1 is reserved for local file
			id = 1
		} else {
			mm.msMu.Lock()

			for _, ms := range mm.msSources[mlv.Type] {
				if ms.Enabled != 0 && ms.Name == name {
					id = ms.ID
					break
				}
			}

			mm.msMu.Unlock()
		}
	}

	dbc := mm.Get()
	mm.MetadbItemSetPreferredDs(dbc, misc.RstrGet(mlv.URL), id)
	mm.mlvGetVideoInfo0(dbc, mlv, 0)
	mm.Close(dbc)
	mm.loadAlternatives(mlv)
}

// mlvSubSource — C: mlv_sub_source (mlp.c:1388-1416)
func (mm *MetadataManager) mlvSubSource(mlv *MetadataLazyVideo,
	event propcore.EventType, args ...any) {
	switch event {
	case propcore.EventDestroyed:
		mm.mlpDestroy(&mlv.MLP)

	case propcore.EventSubscriptionMonitorActive:
		mm.loadSources(mlv)

	case propcore.EventSelectChild:
		var p *propcore.Prop
		if len(args) > 0 {
			p, _ = args[0].(*propcore.Prop)
		}
		var name string
		if p != nil {
			name = p.GetName()
		}
		mm.mlvSetSource(mlv, name)
	}
}

// mlvRefreshVideoInfo — C: mlv_refresh_video_info (mlp.c:1422-1433)
func (mm *MetadataManager) mlvRefreshVideoInfo(mlv *MetadataLazyVideo) {
	dbc := mm.Get()

	mm.MetadbItemSetPreferredDs(dbc, misc.RstrGet(mlv.URL), 0)
	mm.MetadbVideoitemSetPreferred(dbc, misc.RstrGet(mlv.URL), 0)
	mm.mlvGetVideoInfo0(dbc, mlv, 1)
	mm.Close(dbc)
	mm.loadAlternatives(mlv)
}

// mlvSubActions — C: mlv_sub_actions (mlp.c:1439-1480)
func (mm *MetadataManager) mlvSubActions(mlv *MetadataLazyVideo,
	event propcore.EventType, args ...any) {
	switch event {
	case propcore.EventDestroyed:
		mm.mlpDestroy(&mlv.MLP)

	case propcore.EventExtEvent:
		if mlv.MLP.Zombie {
			return
		}
		if len(args) == 0 {
			return
		}
		ep := eventpkg.BaseEvent(args[0])
		if ep == nil || ep.GetType() != eventpkg.EVENT_DYNAMIC_ACTION {
			return
		}
		if ep.Payload != "item.metadata.refresh" {
			return
		}
		mm.mlvRefreshVideoInfo(mlv)
		mm.loadAlternatives(mlv)

		s := misc.RstrGet(mlv.CustomQuery)
		if s != "" {
			mm.kvstore.UrlOptSet(misc.RstrGet(mlv.URL),
				kvstore.DomainSys,
				"metacustomquery", kvstore.SetString, s)
		} else {
			mm.kvstore.UrlOptSet(misc.RstrGet(mlv.URL),
				kvstore.DomainSys,
				"metacustomquery", kvstore.SetVoid, nil)
		}
	}
}

// mlvCustomQueryCb — C: mlv_custom_query_cb (mlp.c:1487-1509)
func (mm *MetadataManager) mlvCustomQueryCb(mlv *MetadataLazyVideo,
	event propcore.EventType, args ...any) {
	switch event {
	case propcore.EventDestroyed:
		mm.mlpDestroy(&mlv.MLP)

	case propcore.EventSetRString:
		var s string
		if len(args) > 0 {
			s, _ = args[0].(string)
		}
		r := misc.RstrAllocStr(s)
		misc.RstrSet(&mlv.CustomQuery, r)
		misc.RstrRelease(r)
	}
}

// mlvCustomTitleCb — C: mlv_custom_title_cb (mlp.c:1516-1551)
func (mm *MetadataManager) mlvCustomTitleCb(mlv *MetadataLazyVideo,
	event propcore.EventType, args ...any) {
	switch event {
	case propcore.EventDestroyed:
		mm.mlpDestroy(&mlv.MLP)

	case propcore.EventSetRString:
		var s string
		var hadRstr bool
		if len(args) > 0 {
			s, hadRstr = args[0].(string)
		}
		r := misc.RstrAllocStr(s)
		misc.RstrSet(&mlv.CustomTitle, r)
		misc.RstrRelease(r)

		mm.MetadbItemSetUserTitle(misc.RstrGet(mlv.URL),
			misc.RstrGet(mlv.CustomTitle))

		if hadRstr && s == "" {
			mm.mlvRefreshVideoInfo(mlv)
		} else {
			t := mlv.CustomTitle
			if t == nil {
				t = mlv.Filename
			}
			mm.pm.SetVEx(nil, mlv.M, "title", misc.RstrGet(t))
		}
	}
}

// mlvKill — C: mlv_kill (mlp.c:1557-1568)
func mlvKill(mlp *MetadataLazyProp) {
	mlv := (*MetadataLazyVideo)(unsafe.Pointer(mlp))
	pm := mlp.mm.pm
	pm.Destroy(mlv.TitleOpt)
	pm.Destroy(mlv.InfoOpt)
	pm.Destroy(mlv.SourceOpt)
	pm.Destroy(mlv.AltOpt)
	pm.Destroy(mlv.CustomQueryOpt)
	pm.Destroy(mlv.CustomTitleOpt)
	pm.Destroy(mlv.RefreshOpt)
}

// mlvDtor — C: mlv_dtor (mlp.c:1574-1611)
func mlvDtor(mlp *MetadataLazyProp) {
	mlv := (*MetadataLazyVideo)(unsafe.Pointer(mlp))
	pm := mlp.mm.pm

	misc.RstrRelease(mlv.Initiator)
	if mlv.TrigTitle != nil {
		pm.Unsubscribe(mlv.TrigTitle)
	}
	if mlv.TrigDesc != nil {
		pm.Unsubscribe(mlv.TrigDesc)
	}
	if mlv.TrigRating != nil {
		pm.Unsubscribe(mlv.TrigRating)
	}

	if mlv.OptionsMonitorSub != nil {
		pm.Unsubscribe(mlv.OptionsMonitorSub)
	}

	if mlv.SourceOptSub != nil {
		pm.Unsubscribe(mlv.SourceOptSub)
	}
	if mlv.AltOptSub != nil {
		pm.Unsubscribe(mlv.AltOptSub)
	}
	if mlv.RefreshSub != nil {
		pm.Unsubscribe(mlv.RefreshSub)
	}
	if mlv.CustomTitleSub != nil {
		pm.Unsubscribe(mlv.CustomTitleSub)
	}
	if mlv.CustomQuerySub != nil {
		pm.Unsubscribe(mlv.CustomQuerySub)
	}

	if mlv.TitleOpt != nil {
		pm.RefDec(mlv.TitleOpt)
	}
	if mlv.InfoOpt != nil {
		pm.RefDec(mlv.InfoOpt)
	}
	if mlv.InfoTextProp != nil {
		pm.RefDec(mlv.InfoTextProp)
	}
	misc.RstrRelease(mlv.InfoTextRstr)
	if mlv.SourceOpt != nil {
		pm.RefDec(mlv.SourceOpt)
	}
	if mlv.AltOpt != nil {
		pm.RefDec(mlv.AltOpt)
	}
	if mlv.RefreshOpt != nil {
		pm.RefDec(mlv.RefreshOpt)
	}

	if mlv.CustomQueryOpt != nil {
		pm.RefDec(mlv.CustomQueryOpt)
	}
	if mlv.CustomTitleOpt != nil {
		pm.RefDec(mlv.CustomTitleOpt)
	}

	misc.RstrRelease(mlv.Filename)
	misc.RstrRelease(mlv.IMDBID)
	misc.RstrRelease(mlv.CustomQuery)
	misc.RstrRelease(mlv.CustomTitle)
	misc.RstrRelease(mlv.URL)
	misc.RstrRelease(mlv.Folder)

	if mlv.M != nil {
		pm.RefDec(mlv.M)
	}
	if mlv.Root != nil {
		pm.RefDec(mlv.Root)
	}
}

// mlcVideo — C: mlc_video (mlp.c:1617-1622)
var mlcVideo = MetadataLazyClass{
	Load: mlvLoad,
	Dtor: mlvDtor,
	Kill: mlvKill,
}

// mlvSub — C: mlv_sub (mlp.c:1628-1646)
func (mm *MetadataManager) mlvSub(mlv *MetadataLazyVideo, m *propcore.Prop,
	name string, id uint64) *propcore.Subscription {
	mlv.MLP.RefCount++

	var origins []*propcore.Prop
	value := mm.pm.GetByName([]string{"metadata", name}, 1, nil, &origins,
		&propcore.PropRootNode{P: m, Name: "metadata"})
	if value == nil {
		return nil
	}
	s := value.Subscribe(
		func(opaque any, ev propcore.EventType,
			args ...any) {
			mm.mlpSubCb(&mlv.MLP, ev, args...)
		},
		mlv,
		propcore.SubFlagTrackDestroyExp|propcore.SubFlagSubscriptionMonitor,
		propcore.SubUserInt(id),
		propcore.SubOrigins{Props: origins},
		propcore.SubMutex{Ptr: &mm.mlpMutex})
	mm.pm.RefDec(value)
	return s
}

// mlvAddOptions — C: mlv_add_options (mlp.c:1651-1814)
func (mm *MetadataManager) mlvAddOptions(mlv *MetadataLazyVideo) {
	pm := mm.pm
	var p *propcore.Prop
	var options *propcore.Prop

	vec := pm.CreateRootEx("", true)

	// -------------------------------------------------------------------
	// Separator

	mlv.TitleOpt = pm.RefInc(pm.CreateRootEx("", false))
	p = mlv.TitleOpt
	pm.SetVEx(nil, p, "type", "separator")
	pm.SetVEx(nil, p, "enabled", 1)

	pm.Link(nls.GetProp("Metadata"),
		pm.CreateEx(pm.CreateEx(mlv.TitleOpt, "metadata", nil,
			false, false), "title", nil, false, false),
		nil, false, false)

	pm.SetParentEx(p, vec, nil, "")

	// -------------------------------------------------------------------
	// Info

	mlv.InfoOpt = pm.RefInc(pm.CreateRootEx("", false))
	p = mlv.InfoOpt
	pm.SetVEx(nil, p, "type", "info")
	pm.SetVEx(nil, p, "enabled", 1)
	mlv.InfoTextProp = pm.RefInc(pm.CreateEx(
		pm.CreateEx(p, "metadata", nil, false, false),
		"title", nil, false, false))
	pm.SetStringEx(mlv.InfoTextProp, nil, misc.RstrGet(mlv.InfoTextRstr),
		propcore.StringRich)

	pm.SetParentEx(p, vec, nil, "")

	// -------------------------------------------------------------------
	// Metadata source selection

	mlv.SourceOpt = pm.RefInc(pm.CreateRootEx("", false))
	p = mlv.SourceOpt

	pm.SetVEx(nil, p, "type", "multiopt")
	pm.SetVEx(nil, p, "enabled", 1)

	pm.Link(nls.GetProp("Metadata source"),
		pm.CreateEx(pm.CreateEx(p, "metadata", nil, false, false),
			"title", nil, false, false),
		nil, false, false)

	options = pm.CreateEx(p, "options", nil, false, false)
	pm.LinkselectedCreate(options, p, "current", "value")

	mlv.SourceOptSub = options.Subscribe(
		func(opaque any, ev propcore.EventType,
			args ...any) {
			mm.mlvSubSource(mlv, ev, args...)
		},
		mlv,
		propcore.SubFlagSubscriptionMonitor|propcore.SubFlagTrackDestroy,
		propcore.SubMutex{Ptr: &mm.mlpMutex})
	mlv.MLP.RefCount++

	pm.SetParentEx(p, vec, nil, "")

	// -------------------------------------------------------------------
	// Metadata alternative selection

	mlv.AltOpt = pm.RefInc(pm.CreateRootEx("", false))
	p = mlv.AltOpt

	pm.SetVEx(nil, p, "type", "multiopt")
	pm.SetVEx(nil, p, "enabled", 1)

	pm.Link(nls.GetProp("Movie"),
		pm.CreateEx(pm.CreateEx(p, "metadata", nil, false, false),
			"title", nil, false, false),
		nil, false, false)

	options = pm.CreateEx(p, "options", nil, false, false)
	pm.LinkselectedCreate(options, p, "current", "value")

	mlv.AltOptSub = options.Subscribe(
		func(opaque any, ev propcore.EventType,
			args ...any) {
			mm.mlvSubAlternative(mlv, ev, args...)
		},
		mlv,
		propcore.SubFlagSubscriptionMonitor|propcore.SubFlagTrackDestroy,
		propcore.SubMutex{Ptr: &mm.mlpMutex})
	mlv.MLP.RefCount++

	pm.SetParentEx(p, vec, nil, "")

	// -------------------------------------------------------------------
	// Metadata search query

	mlv.CustomQueryOpt = pm.RefInc(pm.CreateRootEx("", false))
	p = mlv.CustomQueryOpt

	pm.SetVEx(nil, p, "type", "string")
	pm.SetVEx(nil, p, "enabled", 1)
	pm.SetVEx(nil, p, "action", "item.metadata.refresh")
	pm.SetVEx(nil, p, "value", misc.RstrGet(mlv.CustomQuery))

	pm.Link(nls.GetProp("Custom search query"),
		pm.CreateEx(pm.CreateEx(p, "metadata", nil, false, false),
			"title", nil, false, false),
		nil, false, false)

	{
		var origins []*propcore.Prop
		vp := mm.pm.GetByName([]string{"option", "value"}, 1, nil,
			&origins, &propcore.PropRootNode{P: p, Name: "option"})
		if vp != nil {
			mlv.CustomQuerySub = vp.Subscribe(
				func(opaque any, ev propcore.EventType,
					args ...any) {
					mm.mlvCustomQueryCb(mlv, ev, args...)
				},
				mlv,
				propcore.SubFlagTrackDestroy|
					propcore.SubNoInitialUpdate,
				propcore.SubOrigins{Props: origins},
				propcore.SubMutex{Ptr: &mm.mlpMutex})
			mm.pm.RefDec(vp)
		}
	}
	mlv.MLP.RefCount++

	pm.SetParentEx(p, vec, nil, "")

	// -------------------------------------------------------------------
	// Metadata refresh

	mlv.RefreshOpt = pm.RefInc(pm.CreateRootEx("", false))
	p = mlv.RefreshOpt

	pm.SetVEx(nil, p, "type", "action")
	pm.SetVEx(nil, p, "enabled", 1)
	pm.SetVEx(nil, p, "action", "item.metadata.refresh")

	pm.Link(nls.GetProp("Refresh metadata"),
		pm.CreateEx(pm.CreateEx(p, "metadata", nil, false, false),
			"title", nil, false, false),
		nil, false, false)

	mlv.RefreshSub = mlv.Root.Subscribe(
		func(opaque any, ev propcore.EventType,
			args ...any) {
			mm.mlvSubActions(mlv, ev, args...)
		},
		mlv,
		propcore.SubFlagTrackDestroy,
		propcore.SubMutex{Ptr: &mm.mlpMutex})
	mlv.MLP.RefCount++

	pm.SetParentEx(p, vec, nil, "")

	// -------------------------------------------------------------------
	// Custom movie title

	mlv.CustomTitleOpt = pm.RefInc(pm.CreateRootEx("", false))
	p = mlv.CustomTitleOpt

	pm.SetVEx(nil, p, "type", "string")
	pm.SetVEx(nil, p, "enabled", 1)
	pm.SetVEx(nil, p, "action", "item.metadata.refresh")
	pm.SetVEx(nil, p, "value", misc.RstrGet(mlv.CustomTitle))

	pm.Link(nls.GetProp("Custom title"),
		pm.CreateEx(pm.CreateEx(p, "metadata", nil, false, false),
			"title", nil, false, false),
		nil, false, false)

	{
		var origins []*propcore.Prop
		vp := mm.pm.GetByName([]string{"option", "value"}, 1, nil,
			&origins, &propcore.PropRootNode{P: p, Name: "option"})
		if vp != nil {
			mlv.CustomTitleSub = vp.Subscribe(
				func(opaque any, ev propcore.EventType,
					args ...any) {
					mm.mlvCustomTitleCb(mlv, ev, args...)
				},
				mlv,
				propcore.SubFlagTrackDestroy|
					propcore.SubNoInitialUpdate,
				propcore.SubOrigins{Props: origins},
				propcore.SubMutex{Ptr: &mm.mlpMutex})
			mm.pm.RefDec(vp)
		}
	}

	mlv.MLP.RefCount++

	pm.SetParentEx(p, vec, nil, "")

	// Add all options

	all := pm.RefInc(pm.CreateEx(mlv.Root, "options", nil, false, false))

	pm.SetParentVector(vec, all, nil, "")
	pm.Destroy(vec)

	pm.RefDec(all)
}

// mlvOptionsCb — C: mlv_options_cb (mlp.c:1820-1843)
func (mm *MetadataManager) mlvOptionsCb(mlv *MetadataLazyVideo,
	event propcore.EventType, args ...any) {
	switch event {
	case propcore.EventDestroyed:
		mm.mlpDestroy(&mlv.MLP)

	case propcore.EventSubscriptionMonitorActive:
		mm.mlvAddOptions(mlv)
		mlv.MLP.RefCount--
		if mlv.OptionsMonitorSub != nil {
			mm.pm.Unsubscribe(mlv.OptionsMonitorSub)
			mlv.OptionsMonitorSub = nil
		}
	}
}

// MetadataBindVideoInfo — C: metadata_bind_video_info (mlp.c:1848-1908).
// Go boundary takes plain strings for the rstr_t args ("" == NULL rstr).
func (mm *MetadataManager) MetadataBindVideoInfo(url, filename, imdbID string,
	duration int, root *propcore.Prop, folder string, lonely, passive int,
	year, season, episode int, manual int, initiator string) *MetadataLazyVideo {

	mlv := &MetadataLazyVideo{}
	mlv.MLP = *mlpAlloc(&mlcVideo)
	mlv.MLP.mm = mm

	// Go boundary: "" models a NULL rstr (rstr_dup(NULL) == NULL).
	if filename != "" {
		mlv.Filename = misc.RstrAllocStr(filename)
	}
	if folder != "" {
		mlv.Folder = misc.RstrAllocStr(folder)
	}
	mlv.URL = misc.RstrAllocStr(url)
	mlv.Duration = duration
	if imdbID != "" {
		mlv.IMDBID = misc.RstrAllocStr(imdbID)
	}
	mlv.Type = MetadataTypeVideo
	mlv.Lonely = lonely
	mlv.Passive = passive
	mlv.Manual = manual
	mlv.Root = mm.pm.RefInc(root)
	if initiator != "" {
		mlv.Initiator = misc.RstrAllocStr(initiator)
	}
	mlv.M = mm.pm.RefInc(mm.pm.CreateEx(root, "metadata", nil, false, false))

	if season >= 0 && episode >= 0 {
		mlv.QType = MetadataQTypeTVShow
		mlv.Season = int16(season)
		mlv.Episode = int16(episode)
	} else if year >= 0 {
		mlv.QType = MetadataQTypeMovie
		mlv.Year = int16(year)
	}

	mlv.CustomTitle = mm.metadbItemGetUserTitleRstr(
		misc.RstrGet(mlv.URL))
	if s, ok := mm.kvstore.UrlOptGetStringOK(misc.RstrGet(mlv.URL),
		kvstore.DomainSys, "metacustomquery"); ok {
		mlv.CustomQuery = misc.RstrAllocStr(s)
	}

	mm.mlpMutex.Lock()

	mlv.TrigTitle = mm.mlvSub(mlv, mlv.M, "title",
		MetadataPropTitle)
	mlv.TrigDesc = mm.mlvSub(mlv, mlv.M, "description",
		MetadataPropDescription)
	mlv.TrigRating = mm.mlvSub(mlv, mlv.M, "rating",
		MetadataPropRating)

	mlv.MLP.RefCount++

	{
		var origins []*propcore.Prop
		vp := mm.pm.GetByName([]string{"node", "options"}, 1, nil,
			&origins, &propcore.PropRootNode{P: root, Name: "node"})
		if vp != nil {
			mlv.OptionsMonitorSub = vp.Subscribe(
				func(opaque any, ev propcore.EventType,
					args ...any) {
					mm.mlvOptionsCb(mlv, ev, args...)
				},
				mlv,
				propcore.SubFlagSubscriptionMonitor|
					propcore.SubFlagTrackDestroy,
				propcore.SubOrigins{Props: origins},
				propcore.SubMutex{Ptr: &mm.mlpMutex})
			mm.pm.RefDec(vp)
		}
	}

	mm.mlpMutex.Unlock()

	return mlv
}

// metadbItemGetUserTitleRstr — C: metadb_item_get_user_title
// (metadb.c:1722-1755). Returns nil when the usertitle column is NULL,
// an empty rstr when the column is an empty string (the
// "explicitly cleared" marker checked by mlv_get_video_info0).
func (mm *MetadataManager) metadbItemGetUserTitleRstr(url string) *misc.Rstr {
	db := mm.Get()
	if db == nil {
		return nil
	}
	defer mm.Close(db)
	sel, rc := dbpkg.DBPrepare(db,
		"SELECT usertitle FROM item WHERE url=?1")
	if rc != dbpkg.SQLITE_OK {
		return nil
	}
	defer sel.Finalize()
	sel.BindText(1, url)
	if dbpkg.DBStep(sel) != dbpkg.SQLITE_ROW {
		return nil
	}
	if sel.ColumnType(0) == dbpkg.SQLITE_NULL {
		return nil
	}
	return misc.RstrAllocStr(sel.ColumnText(0))
}

// MLVSetIMDBID — C: mlv_set_imdb_id (mlp.c:1914-1923).
// Go boundary takes a plain string ("" == NULL rstr).
func (mm *MetadataManager) MLVSetIMDBID(mlv *MetadataLazyVideo, imdbID string) {
	var r *misc.Rstr
	if imdbID != "" {
		r = misc.RstrAllocStr(imdbID)
	}
	mm.mlpMutex.Lock()
	if misc.RstrEq(mlv.IMDBID, r) == 0 {
		misc.RstrSet(&mlv.IMDBID, r)
		mm.mlpEnqueue(&mlv.MLP)
	}
	mm.mlpMutex.Unlock()
	misc.RstrRelease(r)
}

// MLVSetDuration — C: mlv_set_duration (mlp.c:1929-1938)
func (mm *MetadataManager) MLVSetDuration(mlv *MetadataLazyVideo,
	duration int) {
	mm.mlpMutex.Lock()
	if mlv.Duration != duration {
		mlv.Duration = duration
		mm.mlpEnqueue(&mlv.MLP)
	}
	mm.mlpMutex.Unlock()
}

// MLVSetLonely — C: mlv_set_lonely (mlp.c:1944-1956)
func (mm *MetadataManager) MLVSetLonely(mlv *MetadataLazyVideo, lonely int) {
	mm.mlpMutex.Lock()
	if mlv.Lonely != lonely {
		mm.metadataTrace("Item '%s' is %slonely",
			misc.RstrGet(mlv.URL),
			map[bool]string{true: "", false: "not "}[lonely != 0])

		mlv.Lonely = lonely
		mm.mlpEnqueue(&mlv.MLP)
	}
	mm.mlpMutex.Unlock()
}

// MLVDirectQuery — C: mlv_direct_query (mlp.c:1962-1986)
func (mm *MetadataManager) MLVDirectQuery(dbc *dbpkg.DB, url, filename string,
	imdbID string, duration int, folder string, lonely int) int {
	var mlv MetadataLazyVideo

	mlv.URL = misc.RstrAllocStr(url)
	mlv.Filename = misc.RstrAllocStr(filename)
	if imdbID != "" {
		mlv.IMDBID = misc.RstrAllocStr(imdbID)
	}
	if folder != "" {
		mlv.Folder = misc.RstrAllocStr(folder)
	}
	mlv.Lonely = 1
	mlv.Duration = duration
	mlv.Type = MetadataTypeVideo

	mm.mlpMutex.Lock()
	r := mm.mlvGetVideoInfo0(dbc, &mlv, 1)
	mm.mlpMutex.Unlock()
	misc.RstrRelease(mlv.URL)
	misc.RstrRelease(mlv.Filename)
	misc.RstrRelease(mlv.IMDBID)
	misc.RstrRelease(mlv.Folder)
	return r
}
