package htsmsg

import (
	"strings"

	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/trace"
)

/*
 * Port of src/htsmsg/persistent_file.c
 *
 * C reads gconf.persistent_path (global) and routes all I/O through the
 * fileaccess layer (fa_load/fa_open_ex/fa_write/fa_rename/...).
 * PersistentStorage holds the persistent_path + the FileAccessManager.
 */

// C: #define RENAME_CANT_OVERWRITE 0  (non-PS3)
const renameCantOverwrite = false

// C: persistent_store_sync — STOS only; no-op elsewhere
func (ps *PersistentStorage) PersistentStoreSync() {
	// C: #ifdef STOS — if(gconf.persistent_path) arch_sync_path(...)
	//    (persistent_file.c:46-49). syncPath is a real syncfs under
	//    -tags mgos, a no-op otherwise.
	if pp := ps.path(); pp != "" {
		syncPath(pp)
	}
}

// famOrDefault returns ps.fam or the global default (C uses the global
// fileaccess layer, always initialized by fa_init()).
func (ps *PersistentStorage) famOrDefault() *fileaccesscore.FileAccessManager {
	ps.mu.RLock()
	fam := ps.fam
	ps.mu.RUnlock()
	if fam != nil {
		return fam
	}
	if fam == nil {
		// C: the FAP_REGISTER'd fs protocol resolves file:// even before
		// fileaccess_init — only the static protocol-registration phase
		// may run here. A full Start() would fire http_init → load_cookies
		// → htsmsg_store_load, re-entering the store while its mutex is
		// held by a load in progress (self-deadlock).
		fam = fileaccesscore.NewFileAccessManager(propcore.NewPropManager(), nil)
		fam.RegisterProtocols()
	}
	return fam
}

// C: static int buildpath(char *dst, size_t dstsize, const char *subdir,
//
//	const char *key, const char *postfix)
//
// Sanitizes only the key+postfix portion (n = dst + strlen(dst)).
func (ps *PersistentStorage) buildpath(subdir, key, postfix string) (string, int) {
	pp := ps.path()
	if pp == "" {
		return "", -1
	}

	dst := pp + "/" + subdir + "/"

	n := key + postfix
	// C: while(*n) { if(*n == ':' || '?' || '*' || >127 || <32) *n = '_'; }
	n = strings.Map(func(r rune) rune {
		if r == ':' || r == '?' || r == '*' || r > 127 || r < 32 {
			return '_'
		}
		return r
	}, n)

	return dst + n, 0
}

// C: buf_t *persistent_load(const char *group, const char *key,
//
//	char *errbuf, size_t errlen)
func (ps *PersistentStorage) PersistentLoad(group, key string) (*fileaccesscore.Buffer, error) {
	fullpath, rc := ps.buildpath(group, key, "")
	if rc < 0 {
		return nil, nil
	}

	// C: fa_load(fullpath, FA_LOAD_ERRBUF(errbuf, errlen), NULL)
	fh, err := fileaccesscore.FAOpenEx(ps.famOrDefault(), fullpath, 0, nil)
	if err != nil {
		return nil, err
	}
	return fileaccesscore.LoadAndClose(fh), nil
}

// C: void persistent_remove(const char *group, const char *key)
func (ps *PersistentStorage) PersistentRemove(group, key string) {
	fullpath, rc := ps.buildpath(group, key, "")
	if rc != 0 {
		return
	}

	// C: fa_unlink(fullpath, NULL, 0)
	fileaccesscore.FAUnlink(ps.famOrDefault(), fullpath)
}

// C: void persistent_write(const char *group, const char *key,
//
//	const void *data, int len)
func (ps *PersistentStorage) PersistentWrite(group, key string, data []byte) {
	postfix := ".tmp"
	if renameCantOverwrite {
		postfix = ""
	}
	fullpath, rc := ps.buildpath(group, key, postfix)
	if rc != 0 {
		return
	}

	// C: char *x = strrchr(fullpath, '/'); *x = 0; fa_makedirs(...); *x = '/';
	if x := strings.LastIndexByte(fullpath, '/'); x >= 0 {
		dir := fullpath[:x]
		if err := fileaccesscore.FAMakedirs(ps.famOrDefault(), dir); err != nil {
			ps.famOrDefault().TraceSystem().Trace(trace.TRACE_ERROR, "Persistent",
				"Unable to create dir %s -- %v", dir, err)
			return
		}
	}

	// C: fa_handle_t *fh = fa_open_ex(fullpath, errbuf, sizeof(errbuf), FA_WRITE, NULL)
	fh, err := fileaccesscore.FAOpenEx(ps.famOrDefault(), fullpath, fileaccesscore.FaWrite, nil)
	if err != nil {
		ps.famOrDefault().TraceSystem().Trace(trace.TRACE_ERROR, "Persistent",
			"Unable to create \"%s\" - %v", fullpath, err)
		return
	}

	ok := true

	// C: if(fa_write(fh, data, len) != len)
	if n, werr := fileaccesscore.FAWrite(fh, data); werr != nil || n != len(data) {
		ps.famOrDefault().TraceSystem().Trace(trace.TRACE_ERROR, "Persistent",
			"Failed to write file %s", fullpath)
		ok = false
	}

	fileaccesscore.FAClose(fh)
	if !ok {
		fileaccesscore.FAUnlink(ps.famOrDefault(), fullpath)
		return
	}

	opath := fullpath
	if !renameCantOverwrite {
		fullpath2, rc := ps.buildpath(group, key, "")
		if rc != 0 {
			return
		}

		// C: fa_rename(fullpath, fullpath2, errbuf, sizeof(errbuf))
		if rerr := fileaccesscore.FARename(ps.famOrDefault(), fullpath, fullpath2); rerr != nil {
			ps.famOrDefault().TraceSystem().Trace(trace.TRACE_ERROR, "Settings",
				"Failed to rename \"%s\" -> \"%s\" - %v",
				fullpath, fullpath2, rerr)
		}
		opath = fullpath2
	}
	// C: SETTINGS_TRACE("Wrote %d bytes to \"%s\"", len, opath)
	ps.settingsTrace("Wrote %d bytes to \"%s\"", len(data), opath)
}

// gcfg.EnableSettingsDebug — C: gconf.enable_settings_debug (main.h:253),
// written by the "settingsdebug" dev bool in init_dev_settings. Gates
// SETTINGS_TRACE (persistent_file.c:34-38).

// settingsTrace — C: SETTINGS_TRACE (persistent_file.c:34-38)
func (ps *PersistentStorage) settingsTrace(format string, args ...any) {
	if ps.gcfg().EnableSettingsDebug.Load() {
		ps.famOrDefault().TraceSystem().Trace(trace.TRACE_DEBUG, "Settings", format, args...)
	}
}
