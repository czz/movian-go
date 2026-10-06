package metadata

// Canonical 1:1 port of src/metadata/metadata_sources.c
//
// Metadata source registry: providers (tmdb, tvdb, lastfm, ...) register
// via MetadataAddSource, get a row in the metadb "datasource" table, a
// settings node with an "Enabled" toggle, and a position in the
// per-type source queue ordered by priority.

import (
	"slices"

	dbpkg "github.com/czz/movian-go/internal/db"
	propcore "github.com/czz/movian-go/internal/prop"
	settingscore "github.com/czz/movian-go/internal/settings"
	"github.com/czz/movian-go/internal/trace"
)

// msTagKey — C: static int tagkey (metadata_sources.c:49). C uses the
// variable's address as an opaque prop_tag key; Go uses an empty-struct
// type identity instead (msTagKey{} == msTagKey{}, no storage needed).
type msTagKey struct{}

// msPrioCmp — C: ms_prio_cmp (metadata_sources.c:55-58)
func msPrioCmp(a, b *MetadataSource) int {
	return a.Prio - b.Prio
}

// msSetEnable — C: ms_set_enable (metadata_sources.c:65-92)
// Runs under metadata_sources_mutex (SETTING_MUTEX).
func msSetEnable(opaque any, value any) {
	ms := opaque.(*MetadataSource)

	v := 0
	switch val := value.(type) {
	case int:
		v = val
	case bool:
		if val {
			v = 1
		}
	}
	ms.Enabled = v

	mm := ms.mm
	pm := mm.pm

	// C: prop_setv(ms->ms_settings, "metadata", "enabled", NULL,
	//              PROP_SET_INT, ms->ms_enabled)
	if ms.Settings != nil {
		meta := pm.Subfind(ms.Settings, []string{"metadata"}, 0, 0, nil)
		if meta != nil {
			pm.SetVEx(nil, meta, "enabled", ms.Enabled)
		}
	}

	db := mm.Get()
	if db == nil {
		return
	}

	stmt, rc := dbpkg.DBPrepare(db,
		"UPDATE datasource "+
			"SET enabled = ?2 "+
			"WHERE id = ?1")

	if rc != dbpkg.SQLITE_OK {
		mm.Close(db)
		return
	}

	stmt.BindInt(1, ms.ID)
	stmt.BindInt(2, ms.Enabled)

	dbpkg.DBStep(stmt)
	stmt.Finalize()
	mm.Close(db)
}

// MetadataAddSource — C: metadata_add_source (metadata_sources.c:97-213)
func (mm *MetadataManager) MetadataAddSource(name, description string, prio int,
	mtype MetadataType, funcs *MetadataSourceFuncs,
	partials, complete uint64) *MetadataSource {

	pm := mm.pm
	sm := mm.settingsMgr

	db := mm.Get()
	if db == nil {
		return nil
	}

	id := MetadataPermanentError
	enabled := 1

	var ms *MetadataSource
	var queue []*MetadataSource
	var idx int
	var next *MetadataSource

again:
	if dbpkg.DBBegin(db) != 0 {
		goto err
	}

	{
		stmt, rc := dbpkg.DBPrepare(db,
			"SELECT id,prio,enabled FROM datasource WHERE name=?1")

		if rc != dbpkg.SQLITE_OK {
			goto err
		}

		stmt.BindText(1, name)

		rc = dbpkg.DBStep(stmt)
		if rc == dbpkg.SQLITE_LOCKED {
			stmt.Finalize()
			dbpkg.DBRollbackDeadlock(db)
			goto again
		}

		if rc == dbpkg.SQLITE_ROW {
			id = stmt.ColumnInt(0)
			// C: both type checks are on column 1 (upstream quirk —
			// the enabled check tests column 1's type, not column 2's)
			if stmt.ColumnType(1) == dbpkg.SQLITE_INTEGER {
				prio = stmt.ColumnInt(1)
			}
			if stmt.ColumnType(1) == dbpkg.SQLITE_INTEGER {
				enabled = stmt.ColumnInt(2)
			}
			stmt.Finalize()
		} else {
			stmt.Finalize()

			ins, rc := dbpkg.DBPrepare(db,
				"INSERT INTO datasource "+
					"(name, prio, type, enabled) "+
					"VALUES "+
					"(?1, ?2, ?3, ?4)")

			if rc != dbpkg.SQLITE_OK {
				mm.ts.Trace(trace.TRACE_ERROR, "SQLITE",
					"SQL Error at MetadataAddSource")
			}

			if ins != nil {
				ins.BindText(1, name)
				ins.BindInt(2, prio)
				ins.BindInt(3, int(mtype))
				ins.BindInt(4, enabled)

				rc = dbpkg.DBStep(ins)
				ins.Finalize()
			}
			if rc == dbpkg.SQLITE_LOCKED {
				dbpkg.DBRollbackDeadlock(db)
				goto again
			}
			id = int(db.LastInsertRowid())
		}
	}
	dbpkg.DBCommit(db)
	mm.Close(db)

	ms = &MetadataSource{
		Type:          mtype,
		Prio:          prio,
		Name:          name,
		Description:   description,
		ID:            id,
		Funcs:         funcs,
		Enabled:       enabled,
		PartialProps:  partials,
		CompleteProps: complete,
		mm:            mm,
	}

	// C: ms->ms_settings = settings_add_dir_cstr(
	//        metadata_sources_settings[type], ms->ms_description,
	//        NULL, NULL, NULL, NULL)
	if sm != nil {
		ms.Settings = sm.AddDirCStr(mm.msSettings[mtype],
			ms.Description, "", "", "", "")
	}

	// C: prop_tag_set(ms->ms_settings, &tagkey, ms)
	if ms.Settings != nil {
		pm.TagSet(ms.Settings, msTagKey{}, ms)
	}

	mm.msMu.Lock()

	// C: setting_create(SETTING_BOOL, ms->ms_settings, 0,
	//       SETTING_TITLE(_p("Enabled")),
	//       SETTING_MUTEX(&metadata_sources_mutex),
	//       SETTING_CALLBACK(ms_set_enable, ms),
	//       SETTING_VALUE(ms->ms_enabled), NULL)
	if sm != nil && ms.Settings != nil {
		sm.SettingCreate(settingscore.SettingBool, ms.Settings, 0,
			settingscore.SettingTagTitle, sm.P("Enabled"),
			settingscore.SettingTagMutex, &mm.msMu,
			settingscore.SettingTagCallback, msSetEnable, ms,
			settingscore.SettingTagValue, ms.Enabled)
	}

	// C: ms_set_enable(ms, enabled)
	msSetEnable(ms, enabled)

	// C: TAILQ_INSERT_SORTED(&metadata_sources[type], ms, ms_link,
	//        ms_prio_cmp, metadata_source_t)
	// Insert after all entries with prio <= ms.Prio (stable).
	queue = mm.msSources[mtype]
	idx = len(queue)
	for i, e := range queue {
		if msPrioCmp(ms, e) < 0 {
			idx = i
			break
		}
	}
	queue = append(queue, nil)
	copy(queue[idx+1:], queue[idx:])
	queue[idx] = ms
	mm.msSources[mtype] = queue

	// C: n = TAILQ_NEXT(ms, ms_link);
	//    prop_move(ms->ms_settings, n ? n->ms_settings : NULL)
	if idx+1 < len(queue) {
		next = queue[idx+1]
	}
	if ms.Settings != nil {
		var before *propcore.Prop
		if next != nil {
			before = next.Settings
		}
		pm.Move(ms.Settings, before)
	}

	mm.msMu.Unlock()

	return ms

err:
	mm.Close(db)
	return nil
}

// classHandleMove — C: class_handle_move (metadata_sources.c:221-260).
// Runs under metadata_sources_mutex.
func classHandleMove(ms, before *MetadataSource) {
	mm := ms.mm
	mtype := ms.Type

	// C: TAILQ_REMOVE(&metadata_sources[type], ms, ms_link)
	queue := mm.msSources[mtype]
	for i, e := range queue {
		if e == ms {
			queue = slices.Delete(queue, i, i+1)
			break
		}
	}

	// C: if(before) TAILQ_INSERT_BEFORE else TAILQ_INSERT_TAIL
	if before != nil {
		for i, e := range queue {
			if e == before {
				queue = append(queue, nil)
				copy(queue[i+1:], queue[i:])
				queue[i] = ms
				break
			}
		}
	} else {
		queue = append(queue, ms)
	}
	mm.msSources[mtype] = queue

	db := mm.Get()
	if db == nil {
		return
	}

	prio := 1
	for _, e := range queue {
		e.Prio = prio
		prio++

		stmt, rc := dbpkg.DBPrepare(db,
			"UPDATE datasource "+
				"SET prio = ?1 "+
				"WHERE "+
				"id = ?2")

		if rc != dbpkg.SQLITE_OK {
			db.TraceSystem().Trace(trace.TRACE_ERROR, "SQLITE",
				"SQL Error at classHandleMove")
		} else {
			stmt.BindInt(1, e.Prio)
			stmt.BindInt(2, e.ID)

			dbpkg.DBStep(stmt)
			stmt.Finalize()
		}
	}
	mm.Close(db)
}

// providerClassNodeSub — C: provider_class_node_sub
// (metadata_sources.c:267-286). Runs under metadata_sources_mutex.
func providerClassNodeSub(opaque any, event propcore.EventType,
	args ...any) {
	mm := opaque.(*MetadataManager)
	switch event {
	case propcore.EventReqMoveChild:
		// C: p1 = va_arg(ap, prop_t *); p2 = va_arg(ap, prop_t *)
		var p1, p2 *propcore.Prop
		if len(args) > 0 {
			p1, _ = args[0].(*propcore.Prop)
		}
		if len(args) > 2 {
			p2, _ = args[2].(*propcore.Prop)
		}

		pm := mm.pm

		var ms, before *MetadataSource
		if v := pm.TagGet(p1, msTagKey{}); v != nil {
			ms, _ = v.(*MetadataSource)
		}
		if p2 != nil {
			if v := pm.TagGet(p2, msTagKey{}); v != nil {
				before, _ = v.(*MetadataSource)
			}
		}
		classHandleMove(ms, before)
		pm.Move(p1, p2)
	}
}

// addProviderClass — C: add_provider_class (metadata_sources.c:293-315)
func addProviderClass(mm *MetadataManager, pc *propcore.PropConcat, mtype MetadataType,
	title *propcore.Prop) {
	pm := mm.pm

	c := pm.CreateRoot("")

	mm.msSettings[mtype] = c

	d := pm.CreateRoot("")

	// C: prop_link(title, prop_create(prop_create(d, "metadata"), "title"))
	pm.Link(title,
		pm.CreateEx(pm.CreateEx(d, "metadata", nil, false, false),
			"title", nil, false, false),
		nil, false, false)

	// C: prop_set_string(prop_create(d, "type"), "separator")
	pm.CreateEx(d, "type", nil, false, false).SetString("separator")

	n := pm.CreateEx(c, "nodes", nil, false, false)

	pc.AddSource(n, d)

	// C: prop_subscribe(0, PROP_TAG_CALLBACK, provider_class_node_sub,
	//       NULL, PROP_TAG_MUTEX, &metadata_sources_mutex,
	//       PROP_TAG_ROOT, n, NULL)
	n.Subscribe(providerClassNodeSub, mm,
		0,
		propcore.SubMutex{Ptr: &mm.msMu})
}

// MetadataSourceGet — C: metadata_source_get (metadata_sources.c:321-335)
func (mm *MetadataManager) MetadataSourceGet(mtype MetadataType, id int) *MetadataSource {
	mm.msMu.Lock()
	var ms *MetadataSource
	for _, e := range mm.msSources[mtype] {
		if e.Enabled != 0 && e.ID == id {
			ms = e
			break
		}
	}
	mm.msMu.Unlock()

	// C note: "This must be fixed when we can delete metadata_sources —
	// we need to retain a reference or something like that"
	return ms
}

// MetadataSourcesStart — C: metadata_sources_init
// (metadata_sources.c:341-357)
func (mm *MetadataManager) MetadataSourcesStart() {
	sm := mm.settingsMgr
	pm := mm.pm
	if sm == nil || pm == nil {
		return
	}

	// C: s = settings_add_dir(NULL, _p("Metadata"), "metadata", NULL,
	//        _p("Metadata configuration and provider settings"),
	//        "settings:metadata")
	s := sm.AddDir(nil, sm.P("Metadata"), "metadata", "",
		sm.P("Metadata configuration and provider settings"),
		"settings:metadata")

	// C: pc = prop_concat_create(prop_create(s, "nodes"))
	pc := propcore.PropConcatCreate(pm,
		pm.CreateEx(s, "nodes", nil, false, false))

	addProviderClass(mm, pc, MetadataTypeVideo, sm.P("Providers for Video"))
	addProviderClass(mm, pc, MetadataTypeMusic, sm.P("Providers for Music"))
}
