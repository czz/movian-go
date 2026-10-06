package htsmsg

import (
	"fmt"
	"github.com/czz/movian-go/internal/gconf"
	"sync"
	"time"

	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
)

// Store provides persistent storage for HTSMsg with caching.
// C: loaded_msg_list loaded_msgs + loaded_msg_mutex (htsmsg_store.c)
type Store struct {
	mu         sync.Mutex
	cache      map[string]*loadedMsg // C: LIST_HEAD loaded_msgs
	ps         *PersistentStorage    // C: global persistent_* functions
	flushDelay time.Duration         // C: SETTINGS_CACHE_DELAY
}

type loadedMsg struct {
	msg      *HTSMsg
	key      string
	dirty    bool
	refcount int32
	timer    *time.Timer
}

// SettingsCacheDelay is the delay before flushing dirty messages to disk
const SettingsCacheDelay = 2 * time.Second

// NewStore creates a new store. An empty settingsDir uses the global
// persistent storage path (C: gconf.persistent_path); a non-empty dir
// creates an isolated persistent storage rooted there (test hook).
func NewStore(settingsDir string) *Store {
	ps := NewPersistentStorage(settingsDir)
	if settingsDir == "" {
		// C: "" dir = the default persistent_* tree under
		// gconf.persistent_path (read live like persistent_file.c).
		ps.useGconfPath = true
	}
	return &Store{
		cache:      make(map[string]*loadedMsg),
		ps:         ps,
		flushDelay: SettingsCacheDelay,
	}
}

// SetFAM injects the file access manager into the persistent storage
// backend (C: the implicit global fa context).
func (s *Store) SetFAM(fam *fileaccesscore.FileAccessManager) { s.ps.SetFAM(fam) }

// SetGconf injects the process gconf into the persistent storage
// backend (C: gconf_t — persistent_path, enable_settings_debug).
func (s *Store) SetGconf(g *gconf.T) { s.ps.SetGconf(g) }

// Save saves a message to the store with the given key
func (s *Store) Save(msg *HTSMsg, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	lm, exists := s.cache[key]
	if !exists {
		lm = &loadedMsg{
			key:      key,
			refcount: 1,
		}
		s.cache[key] = lm
	} else {
		if lm.msg != nil {
			lm.msg.Release()
		}
	}

	lm.msg = msg.Copy()
	lm.dirty = true

	// Arm timer for delayed flush
	s.armFlushTimer(lm)

	return nil
}

// Load loads a message from the store with the given key.
// C: htsmsg_store_load — htsmsg_store_obtain(path, 0) + htsmsg_copy.
func (s *Store) Load(key string) (*HTSMsg, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	lm := s.obtain(key, false)
	if lm == nil || lm.msg == nil {
		return nil, fmt.Errorf("message not found: %s", key)
	}
	return lm.msg.Copy(), nil
}

// Remove removes a message from the store
// C: htsmsg_store_remove — drops the cached entry WITHOUT writing pending
// dirty data (lm->lm_dirty = 0 before lm_destroy), then persistent_remove.
func (s *Store) Remove(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	lm, exists := s.cache[key]
	if exists {
		lm.dirty = false
		// C: lm_destroy — htsmsg_release, LIST_REMOVE, callout_disarm
		if lm.msg != nil {
			lm.msg.Release()
		}
		if lm.timer != nil {
			lm.timer.Stop()
		}
		delete(s.cache, key)
		s.ps.PersistentStoreSync()
	}

	// C: persistent_remove("settings", key)
	s.ps.PersistentRemove("settings", key)
	return nil
}

// lmDestroy — C: lm_destroy (htsmsg_store.c:79-95). Writes the msg
// when dirty, syncs only if dosync, then releases + disarms + removes
// it. Returns whether a persistent sync is still needed.
func (s *Store) lmDestroy(lm *loadedMsg, dosync bool) bool {
	syncNeeded := false
	if lm.dirty {
		s.writeToDisk(lm)
		if dosync {
			s.ps.PersistentStoreSync()
		} else {
			syncNeeded = true
		}
	}
	if lm.msg != nil {
		lm.msg.Release()
		lm.msg = nil
	}
	delete(s.cache, lm.key)
	if lm.timer != nil {
		lm.timer.Stop()
	}
	return syncNeeded
}

// Flush destroys all loaded messages, writing dirty ones.
// C: htsmsg_store_flush — while((lm = LIST_FIRST(&loaded_msgs)))
//
//	sync_needed |= lm_destroy(lm, 0);
func (s *Store) Flush() error {
	s.mu.Lock()
	syncNeeded := false
	for _, lm := range s.cache {
		if s.lmDestroy(lm, false) {
			syncNeeded = true
		}
	}
	s.mu.Unlock()

	// C: if(sync_needed) persistent_store_sync();
	if syncNeeded {
		s.ps.PersistentStoreSync()
	}
	return nil
}

// Set — C: htsmsg_store_set (htsmsg_store.c:279-313). value_type -1
// deletes the field only; unknown types abort() in C → error in Go.
func (s *Store) Set(store string, key string, valueType int, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	lm := s.obtain(store, true)

	lm.msg.DeleteField(key)

	switch valueType {
	case -1:
		// Delete only, nothing to add
	case HmfMap, HmfList:
		if sub, ok := value.(*HTSMsg); ok {
			lm.msg.AddMsg(key, sub)
		}
	case HmfS64:
		if v, ok := value.(int64); ok {
			lm.msg.AddS64(key, v)
		}
	case HmfStr:
		if v, ok := value.(string); ok {
			lm.msg.AddStr(key, v)
		}
	default:
		return fmt.Errorf("unsupported value type: %d", valueType)
	}

	lm.dirty = true
	s.armFlushTimer(lm)

	return nil
}

// GetInt — C: htsmsg_store_get_int (htsmsg_store.c:317-324).
func (s *Store) GetInt(store string, key string, def int) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	lm := s.obtain(store, true)
	return int(lm.msg.GetS32OrDefault(key, int32(def)))
}

// GetStr — C: htsmsg_store_get_str (htsmsg_store.c:331-338).
func (s *Store) GetStr(store string, key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	lm := s.obtain(store, true)
	return lm.msg.GetStr(key)
}

// obtain — C: htsmsg_store_obtain (htsmsg_store.c:193-228). Cache hit
// returns the entry; otherwise the record is read from disk — missing
// or unparseable → nil unless create, in which case a fresh map is
// used. Every obtained entry is cached and gets the flush timer.
func (s *Store) obtain(key string, create bool) *loadedMsg {
	if lm, exists := s.cache[key]; exists {
		return lm
	}

	var r *HTSMsg
	if b, err := s.ps.PersistentLoad("settings", key); err == nil && b != nil {
		r, _ = DeserializeJSON(string(b.Data))
	}

	if r == nil && !create {
		return nil
	}
	if r == nil {
		r = NewMap()
	}

	lm := &loadedMsg{
		key:      key,
		msg:      r,
		refcount: 1,
	}
	s.cache[key] = lm
	s.armFlushTimer(lm)
	return lm
}

// armFlushTimer — C: callout_arm_managed(..., SETTINGS_CACHE_DELAY,
// htsmsg_store_lockmgr). On expiry the C timer calls
// htsmsg_store_timer_cb → lm_destroy(lm, 1): writes when dirty and
// evicts the entry entirely (it is NOT kept cached).
func (s *Store) armFlushTimer(lm *loadedMsg) {
	if lm.timer != nil {
		lm.timer.Stop()
	}

	lm.timer = time.AfterFunc(s.flushDelay, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.lmDestroy(lm, true)
	})
}

// writeToDisk writes a message to disk
// C: loaded_msg_write — htsmsg_json_serialize_to_str + persistent_write (void)
func (s *Store) writeToDisk(lm *loadedMsg) error {
	data, err := SerializeJSON(lm.msg, true)
	if err != nil {
		return err
	}
	s.ps.PersistentWrite("settings", lm.key, []byte(data))
	return nil
}

// (C's htsmsg_store.c has no store variants — the earlier Go-only
// MemoryStore/BinaryStore/XMLStore types were removed as non-canonical.)
