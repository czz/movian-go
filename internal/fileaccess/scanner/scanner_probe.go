package scanner

import (
	"context"
	"strings"
	"time"

	"github.com/czz/movian-go/internal/db"
	"github.com/czz/movian-go/internal/gconf"

	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/metadata"
	"github.com/czz/movian-go/internal/notifications"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/trace"
)

// ScannerCreate creates a new scanner
func ScannerCreate(ctx context.Context, url string, mtime time.Time, dbg int, pm *propcore.PropManager, ts *trace.TraceSystem, fam *fileaccesscore.FileAccessManager, ms *mediacore.MediaSystem, ix *Indexer) *Scanner {
	s := &Scanner{
		ctx:           ctx,
		url:           url,
		mtime:         mtime,
		dbg:           dbg,
		pm:            pm,
		fam:           fam,
		ms:            ms,
		ix:            ix,
		metadataProbe: NewMetadataProbe(fam),
		metadb:        nil, // opened lazily via s.mm (C: metadb_get per-op)
		notifMgr:      notifications.NewNotificationManager(pm, nil, nil, nil),
		typemap:       NewTypeMap(),
		metadataMgr:   nil,
	}
	// C: s->s_pc = prop_courier_create_waitable() (scanner_create)
	s.pc = propcore.NewCourier("scanner")

	// Refcount starts at 1 for the scannerThread.
	// Each subscription (nodes, sort, dirfirst, onlySupported, includeInLibrary)
	// does its own Retain, balanced by Release on EventDestroyed.
	s.refcount.Store(1)
	s.running.Store(1)

	// Register in global registry for test cleanup

	return s
}

// ScannerDestroy destroys a scanner
func ScannerDestroy(s *Scanner) {
	s.closedb()
	// C: prop_courier_destroy(s->s_pc) (scanner_destroy)
	if s.pc != nil {
		s.pc.Destroy()
		s.pc = nil
	}

	// Clean up notification manager and its callout system goroutine.
	// Each scanner creates a NotificationManager which starts a CalloutSystem
	// loop goroutine. Without Fini(), this goroutine leaks.
	if s.notifMgr != nil {
		s.notifMgr.Fini()
		s.notifMgr = nil
	}

}

// ScannerRelease releases a scanner reference
// Decrements the reference count and destroys the scanner if it reaches zero
func ScannerRelease(s *Scanner) {
	if s.refcount.Add(-1) > 0 {
		return
	}
	// C: fa_unreference(s->s_ref) (fa_scanner.c:373)
	fileaccesscore.FAUnreference(s.ref)
	if s.pnf != nil {
		s.pnf.Release()
	}
	ScannerDestroy(s)
}

// ScannerRetain retains a scanner reference
// Increments the reference count to prevent premature destruction
func ScannerRetain(s *Scanner) {
	s.refcount.Add(1)
}

// closedb closes the metadata database
func (s *Scanner) closedb() {
	if s.metadb != nil {
		// metadb_close(s.metadb)
		s.metadb = nil
	}
}

// getdb gets the metadata database
func (s *Scanner) getdb() *db.DB {
	return s.metadb
}

// removePostfix — C: metadata_remove_postfix_rstr (metadata_str.c:365-380).
// Strips a trailing 4-char extension (".xyz") or ".m2ts" (5 chars) unless
// gconf.show_filename_extensions is set.
func removePostfix(filename string, g *gconf.T) string {
	if !fileaccesscore.ShowFilenameExtensions(g) {
		l := len(filename)
		if l > 4 && filename[l-4] == '.' {
			return filename[:l-4]
		}
		if l > 5 && strings.EqualFold(filename[l-5:], ".m2ts") {
			return filename[:l-5]
		}
	}
	return filename
}

// set_type sets the content type on a property
func set_type(pm *propcore.PropManager, proproot *propcore.Prop, typ int) {
	var typestr string
	switch typ {
	case ContentDir:
		typestr = "directory"
	case ContentFile:
		typestr = "file"
	case ContentAudio:
		typestr = "audio"
	case ContentVideo:
		typestr = "video"
	case ContentImage:
		typestr = "image"
	case ContentPlaylist:
		typestr = "playlist"
	case ContentArchive:
		typestr = "archive"
	case ContentShare:
		typestr = "share"
	case ContentFont:
		typestr = "font"
	default:
		typestr = "unknown"
	}
	if proproot != nil {
		typeProp := pm.CreateMulti(proproot, "type")
		if typeProp != nil {
			typeProp.SetString(typestr)
		}
	}
}

// make_prop creates a property for a directory entry
func make_prop(pm *propcore.PropManager, fde *ScannerEntry, g *gconf.T) {
	p := pm.CreateRootEx("", false)

	urlProp := pm.CreateMulti(p, "url")
	if urlProp != nil {
		urlProp.SetString(fde.url)
	}

	filenameProp := pm.CreateMulti(p, "filename")
	if filenameProp != nil {
		filenameProp.SetString(fde.filename)
	}

	set_type(pm, p, fde.typ)

	// Metadata handling
	var metadata *propcore.Prop
	if fde.metadata != nil {
		metadata = fde.metadata
		if metadata != nil && p != nil {
			metadata.SetParent(p)
		}
		fde.metadata = nil
	} else {
		// Create default metadata with title
		var title string
		if fde.typ == ContentDir || fde.typ == ContentShare {
			title = fde.filename
		} else {
			title = removePostfix(fde.filename, g)
		}

		metadata = pm.CreateMulti(p, "metadata")
		if metadata != nil {
			titleProp := pm.CreateMulti(metadata, "title")
			if titleProp != nil {
				titleProp.SetString(title)
			}
		}
	}

	if fde.statdone {
		if metadata != nil {
			timestampProp := pm.CreateMulti(metadata, "timestamp")
			if timestampProp != nil {
				timestampProp.SetInt(int(fde.stat.Modified.Unix()))
			}
		}
	}

	fde.prop = p

	// canDelete and canCopy properties
	canDeleteProp := pm.CreateMulti(p, "canDelete")
	if canDeleteProp != nil {
		// C: prop_set(p, "canDelete", PROP_SET_INT, gconf.fa_allow_delete)
		// (fa_scanner.c:172)
		canDelete := 0
		if fileaccesscore.FAAllowDelete(g) {
			canDelete = 1
		}
		canDeleteProp.SetInt(canDelete)
	}

	canCopyProp := pm.CreateMulti(p, "canCopy")
	if canCopyProp != nil {
		canCopyProp.SetInt(1)
	}
}

// deep_probe performs deep probing of a directory entry
func (s *Scanner) deepProbe(fde *ScannerEntry) {
	if fde.typ == ContentShare {
		return
	}

	fde.probestatus = FDEProbedContents

	s.ts.Debug("scanner", "Deep probing %s -- content_type:%d prop=%p",
		fde.url, fde.typ, fde.prop)

	if fde.typ != 0 {
		meta := s.pm.CreateMulti(fde.prop, "metadata")

		// C: fde->fde_md = metadb_metadata_get(getdb(s), url, mtime)
		if !fde.ignoreCache && fde.statdone &&
			(fde.md == nil || fde.md.CacheStatus == 0) {
			if fde.md != nil {
				fde.md.Destroy()
			}
			fde.md = s.metadataMgr.MetadbMetadataGet(s.getdb(), fde.url,
				fde.stat.Modified.Unix())
		}

		if fde.md == nil {
			// Probe fresh metadata
			if fde.typ == ContentDir {
				fde.md = s.metadataProbe.ProbeDir(fde.url)
			} else {
				fde.md = s.metadataProbe.ProbeMetadata(fde.url,
					fde.filename, nil)
			}
		}
		if fde.md != nil {
			fde.typ = int(fde.md.ContentType)
			fde.ignoreCache = false
		}

		// C: if(meta != NULL) switch(fde->fde_type) (fa_scanner.c:228-243) —
		// CONTENT_PLUGIN / CONTENT_FONT get custom props; everything else
		// falls through to metadata_to_proptree. The switch is NOT gated
		// on fde_md — font/plugin props attach even when probing fails.
		if meta != nil {
			switch fde.typ {
			case ContentPlugin:
				// C: plugin_props_from_file(fde->fde_prop,
				//   rstr_get(fde->fde_url))
				if pluginPropsFromFile != nil {
					pluginPropsFromFile(fde.prop, fde.url)
				}
			case ContentFont:
				// C: fontstash_props_from_title(fde->fde_prop,
				//   rstr_get(fde->fde_url), rstr_get(fde->fde_filename))
				if fontPropsFromTitle != nil {
					fontPropsFromTitle(fde.prop, fde.url, fde.filename)
				}
			default:
				if fde.md == nil {
					break
				}
				// C: metadata_to_proptree(fde->fde_md, meta, 1)
				if fde.md.Title != "" {
					titleProp := s.pm.CreateMulti(meta, "title")
					if titleProp != nil {
						titleProp.SetString(fde.md.Title)
					}
				}
				if fde.md.Artist != "" {
					artistProp := s.pm.CreateMulti(meta, "artist")
					if artistProp != nil {
						artistProp.SetString(fde.md.Artist)
					}
				}
				if fde.md.Album != "" {
					albumProp := s.pm.CreateMulti(meta, "album")
					if albumProp != nil {
						albumProp.SetString(fde.md.Album)
					}
				}
				if fde.md.Duration > 0 {
					durationProp := s.pm.CreateMulti(meta, "duration")
					if durationProp != nil {
						durationProp.SetFloat(fde.md.Duration)
					}
				}
			}
		}

		if fde.statdone && meta != nil {
			timestampProp := s.pm.CreateMulti(meta, "timestamp")
			if timestampProp != nil {
				timestampProp.SetInt(int(fde.stat.Modified.Unix()))
			}
		}

		if fde.md != nil {
			// C: switch(fde->fde_md->md_cache_status)
			switch fde.md.CacheStatus {
			case metadata.MetadataCacheStatusNo:
				s.metadataMgr.MetadbMetadataWrite(s.getdb(), fde.url,
					fde.stat.Modified.Unix(), fde.md, s.url,
					s.mtime.Unix(), metadata.IndexStatusNoChange)
			case metadata.MetadataCacheStatusUnparented:
				s.metadataMgr.MetadbParentItem(s.getdb(), fde.url, s.url)
			}
		}

		// C: fa_scanner.c:268-271 — playinfo_bind_url_to_prop creates
		// playcount/lastplayed/restartpos on the item prop.
		if fde.prop != nil && !fde.boundToMetadb && s.metadataMgr != nil {
			fde.boundToMetadb = true
			s.metadataMgr.PlayInfoBindURLToProp(fde.url, fde.prop)
		}
	}

	if fde.prop != nil {
		set_type(s.pm, fde.prop, fde.typ)
	}
}

// tryplay attempts to play a specific file
func (s *Scanner) tryplay() {
	if s.playme == "" {
		return
	}

	// Find the entry in the directory
	var fde *DirEntry
	if s.fd != nil {
		for _, entry := range s.fd.Entries {
			if entry.URL == s.playme {
				fde = entry
				break
			}
		}
	}

	if fde != nil && s.model != nil {
		// C: playqueue_load_with_source(fde->fde_prop, s->s_model, 0)
		// fde_prop is the entry's node created by make_prop() and
		// parented under s.nodes — find it by its "url" child.
		// C: playqueue_load_with_source — the playqueue is injected
		// (process-global singleton in C).
		if p := s.findEntryProp(s.playme); p != nil {
			if s.pq != nil {
				s.pq.LoadWithSource(p, s.model, 0)
			}
		}
		s.playme = ""
	}
}

// analyzer analyzes directory entries
func (s *Scanner) analyzer(probe bool) {
	if s.fd == nil || s.fd.Count == 0 {
		return
	}

	if probe {
		s.tryplay()
	}

	// C: deep_probe receives the real fa_dir_entry_t whose fde_prop was
	// set by make_prop. Go splits entry storage (DirEntry) from the item
	// prop (ScannerEntry.prop, parented under s.nodes) — resolve url→prop
	// once per pass so deepProbe updates the real item node.
	propByURL := make(map[string]*propcore.Prop, s.fd.Count)
	if s.nodes != nil {
		for _, c := range s.nodes.GetChildren() {
			if u := c.GetChild("url"); u != nil {
				propByURL[u.GetString()] = c
			}
		}
	}

	// Scan all entries
	for _, entry := range s.fd.Entries {
		// C: while(media_buffer_hungry && s->s_running) sleep(1);
		n := 0
		for s.ms.BufferHungry.Load() != 0 && s.running.Load() == 1 {
			time.Sleep(time.Second)
			n++
			if n%3 == 0 {
			}
		}

		if s.running.Load() != 1 {
			break
		}

		if entry.Probed == 0 { // FDEProbedNone
			if entry.Type == ContentFile {
				entry.Type = ContentTypeFromFilename(entry.Filename)
			}
			entry.Probed = 1 // FDEProbedFilename
		}

		if entry.Probed == 1 && probe { // FDEProbedFilename
			// Convert DirEntry to ScannerEntry for deep probing
			fde := &ScannerEntry{
				url:      entry.URL,
				filename: entry.Filename,
				typ:      entry.Type,
				statdone: entry.StatDone,
				prop:     propByURL[entry.URL], // C: fde_prop
				stat: FileInfo{
					Name:     entry.Filename,
					Size:     entry.Stat.Size,
					Type:     entry.Stat.Type,
					Modified: entry.Stat.MTime,
					URL:      entry.URL,
				},
				probestatus: ScannerProbeStatus(entry.Probed),
				ignoreCache: entry.IgnoreCache,
			}
			fde.md = entry.md // C: fde_md carried on the real dir entry
			s.deepProbe(fde)
			// Update entry with probed data
			entry.Type = fde.typ
			entry.Probed = fde.probestatus
			entry.IgnoreCache = fde.ignoreCache
			entry.md = fde.md
		}
	}
}

// scannerEntrySetup sets up a scanner entry
func (s *Scanner) scannerEntrySetup(fde *ScannerEntry, src string) int {
	s.ts.Debug("scanner", "%s: File %s added by %s", s.url, fde.url, src)

	if fde.typ == ContentFile {
		fde.typ = ContentTypeFromFilename(fde.filename)
	}

	if s.nodes == nil {
		return 0
	}

	make_prop(s.pm, fde, s.fam.Gconf())

	// C: if(!prop_set_parent(fde->fde_prop, s->s_nodes)) return 1; // OK
	if s.pm.SetParentEx(fde.prop, s.nodes, nil, "") == 0 {
		return 1 // OK
	}
	s.pm.Destroy(fde.prop)
	fde.prop = nil
	return 0
}

// scannerEntryDestroy destroys a scanner entry
func (s *Scanner) scannerEntryDestroy(fde *ScannerEntry, src string) {
	s.ts.Debug("scanner", "%s: File %s removed by %s", s.url, fde.url, src)
	if s.metadb != nil && fde.url != "" {
		s.metadataMgr.MetadbUnparentItem(s.metadb, fde.url)
	}
	if fde.prop != nil {
		s.pm.Destroy(fde.prop)
		fde.prop = nil
	}
	// fa_dir_entry_free(s.fd, fde) - Go garbage collection handles this
}
