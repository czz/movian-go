package text

import (
	"slices"
)

// C: src/text/freetype.c — generic name <-> id map + context management.
// The full freetype.c renderer (face/glyph cache, text_render) is ported
// separately; this file contains the self-contained idmap section that
// parser.c's font_tag depends on.

// ft — freetype subsystem state, declared in freetype_render.go (the
// struct carries C.FT_* handles, so it lives in the cgo file).
// C file-scope statics consolidated: text_mutex, text_library,
// text_stroker, font_domain_tally, idmap_id_tally, idmaps,
// static_faces, dynamic_faces, glyph_hash, allglyphs, num_glyphs.

//----------------- generica name <-> id map --------------

// C: typedef struct idmap { LIST_ENTRY(idmap) link; char *name; int id;
//
//	int domain; } idmap_t;
type idmapT struct {
	name   string
	id     int
	domain int
}

// C: static int id_from_str(const char *str, int domain)
func (sys *System) idFromStr(str string, domain int) int {
	for _, im := range sys.ft.idmaps {
		if im.name == str && im.domain == domain {
			return im.id
		}
	}
	im := &idmapT{}
	im.name = str
	im.domain = domain
	sys.ft.idmapTally++
	im.id = sys.ft.idmapTally
	sys.ft.idmaps = slices.Insert(sys.ft.idmaps, 0, im) // LIST_INSERT_HEAD
	return im.id
}

// C: static idmap_t *idmap_find(int id)
func (sys *System) idmapFind(id int) *idmapT {
	for _, im := range sys.ft.idmaps {
		if im.id == id {
			return im
		}
	}
	return nil
}

// C: int freetype_family_id(const char *str, int font_domain)
func (sys *System) FreetypeFamilyId(str string, fontDomain int) int {
	sys.ft.mutex.Lock()
	id := sys.idFromStr(str, fontDomain)
	sys.ft.mutex.Unlock()
	return id
}

// C: int freetype_get_context(void)   // rename context -> font_domain
func (sys *System) FreetypeGetContext() int {
	var id int
	sys.ft.mutex.Lock()
	sys.ft.domainTally++
	id = sys.ft.domainTally
	sys.ft.mutex.Unlock()
	return id
}
