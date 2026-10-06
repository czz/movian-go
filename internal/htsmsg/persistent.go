package htsmsg

import (
	"github.com/czz/movian-go/internal/gconf"
	"sync"

	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
)

// PersistentStorage manages persistent storage.
// C: gconf.persistent_path + the global fileaccess layer
// (src/htsmsg/persistent_file.c).
type PersistentStorage struct {
	mu             sync.RWMutex // guards persistentPath + fam
	persistentPath string       // explicit dir; "" + useGconfPath reads gconf
	useGconfPath   bool         // C: "" dir store reads gconf.persistent_path live
	fam            *fileaccesscore.FileAccessManager

	// gconf — C: gconf_t fields (persistent_path, enable_settings_debug).
	gconf *gconf.T
}

// SetGconf injects the process gconf (C: gconf_t — owned by main).
func (ps *PersistentStorage) SetGconf(g *gconf.T) { ps.gconf = g }

// gcfg — C: gconf_t reads; nil-safe for unwired/test paths.
func (ps *PersistentStorage) gcfg() *gconf.T {
	if ps.gconf == nil {
		ps.gconf = gconf.New()
	}
	return ps.gconf
}

// NewPersistentStorage creates a new persistent storage manager
func NewPersistentStorage(path string) *PersistentStorage {
	return &PersistentStorage{
		persistentPath: path,
	}
}

// SetPath sets the persistent storage path
// C: gconf.persistent_path assignment
func (ps *PersistentStorage) SetPath(path string) {
	ps.mu.Lock()
	ps.persistentPath = path
	ps.mu.Unlock()
}

// SetFAM sets the FileAccessManager used for persistent I/O
// (C uses the global fileaccess layer).
func (ps *PersistentStorage) SetFAM(fam *fileaccesscore.FileAccessManager) {
	ps.mu.Lock()
	ps.fam = fam
	ps.mu.Unlock()
}

// path — the effective persistent root. ""-dir stores read
// gconf.persistent_path live (C: persistent_* read gconf each call).
func (ps *PersistentStorage) path() string {
	if ps.useGconfPath {
		return ps.gcfg().PersistentPath
	}
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return ps.persistentPath
}
