package blobcache

import (
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
	"unsafe"

	propcore "github.com/czz/movian-go/internal/prop"
)

// Constants matching C blobcache_file.c

const (
	BC2Magic07       = 0x62630207
	BC2Magic06       = 0x62630206
	BC2Magic05       = 0x62630205
	ItemHashSize     = 256
	ItemHashMask     = ItemHashSize - 1
	BlobCacheMinSize = 10 * 1000 * 1000
	BlobCacheMaxSize = 1000 * 1000 * 1000
	ImportantItem    = 0x1
)

// blobcacheItem matches C blobcache_item_t (blobcache_file.c:47-58)
type blobcacheItem struct {
	biLink           *blobcacheItem
	biEtag           string
	biKeyHash        uint64
	biContentHash    uint64
	biLastaccess     uint32
	biExpiry         uint32
	biModtime        uint32
	biSize           uint32
	biContentTypeLen uint8
	biFlags          uint8
}

// blobcacheFlush matches C blobcache_flush_t (blobcache_file.c:88-92)
type blobcacheFlush struct {
	bfLink    *blobcacheFlush
	bfKeyHash uint64
	bfBuf     *Buf
}

// Buf matches C buf_t (simplified for blobcache use)
type Buf struct {
	Ptr         []byte
	Size        int
	ContentType string
}

// bcState matches C enum (blobcache_file.c:107-111)
const (
	bcStateBadClock = iota
	bcStateRun
	bcStateStopping
)

// BlobCache matches C globals (blobcache_file.c:99-120)
type BlobCache struct {
	hashVector        [ItemHashSize]*blobcacheItem
	flushQueueHead    *blobcacheFlush
	flushQueueTail    *blobcacheFlush
	cacheLock         sync.Mutex
	cacheCond         *sync.Cond
	state             int
	loadedCacheIsFrom int
	indexDirty        bool
	currentCacheSize  uint64
	cachePath         string
	done              chan struct{}

	// notifyAdd — C: notify_add direct call from cache_clear. Injected
	// to avoid an import cycle (blobcache ← fileaccess/core ← htsmsg ←
	// notifications).
	notifyAdd func(notifyType int, icon string, delay int, format string, args ...any)
}

// SetNotifyAdd injects notifications.NotifyAdd (C: notify_add) so
// cache_clear can surface "Cache cleared" without an import cycle.
func (sys *BlobcacheSystem) SetNotifyAdd(
	fn func(notifyType int, icon string, delay int, format string, args ...any)) {
	sys.bc.notifyAdd = fn
}

// Cache returns the blob cache instance (nil until Start). Callers
// nil-check like C code paths that tolerate an uninitialized cache.
func (bs *BlobcacheSystem) Cache() *BlobCache { return bs.bc }

// IsRun reports whether the cache accepts writes
// (C: bcstate == BLOBCACHE_RUN, set by the flush thread once the
// wall clock is validated — writes during BAD_CLOCK are dropped).
func (bc *BlobCache) IsRun() bool {
	bc.cacheLock.Lock()
	defer bc.cacheLock.Unlock()
	return bc.state == bcStateRun
}

// BlobcacheSystem is a wrapper for initialization (matches C blobcache_init)
type BlobcacheSystem struct {
	bc          *BlobCache
	settingsMgr SettingsActionProvider
}

// SettingsActionProvider is the slice of the settings manager blobcache
// needs to register the "Clear cached files" action
// (C: settings_create_action under setting_get_dir("general:resets")).
type SettingsActionProvider interface {
	SettingGetDir(key string) *propcore.Prop
	CreateActionProp(parent *propcore.Prop, title, subtype string,
		callback func(opaque any, value any),
		opaque any, flags int)
}

// NewBlobcacheSystem creates a new blobcache system
func NewBlobcacheSystem(cachePath string) *BlobcacheSystem {
	return &BlobcacheSystem{bc: &BlobCache{cachePath: cachePath}}
}

// SetSettingsMgr wires the settings manager used by Start to register
// the "Clear cached files" action (C: settings_create_action call in
// blobcache_init).
func (bs *BlobcacheSystem) SetSettingsMgr(sm SettingsActionProvider) {
	bs.settingsMgr = sm
}

// Start initializes the blobcache system (matches C blobcache_init)
func (bs *BlobcacheSystem) Start() error {
	bs.bc.cacheCond = sync.NewCond(&bs.bc.cacheLock)
	blobcachePruneOld(bs.bc.cachePath)
	bc2Path := filepath.Join(bs.bc.cachePath, "bc2")
	if err := os.MkdirAll(bc2Path, 0755); err != nil {
		return fmt.Errorf("unable to create cache dir %s: %w", bc2Path, err)
	}
	bs.bc.loadIndex()

	// C: prop_t *dir = setting_get_dir("general:resets");
	//     settings_create_action(dir, _p("Clear cached files"), cache_clear, NULL, 0, NULL);
	if bs.settingsMgr != nil {
		dir := bs.settingsMgr.SettingGetDir("general:resets")
		bc := bs.bc
		bs.settingsMgr.CreateActionProp(dir, "Clear cached files", "",
			func(opaque, value any) { bc.cacheClear() }, nil, 0)
	}

	bs.bc.done = make(chan struct{})
	go bs.bc.flushThread()
	return nil
}

// cacheClear — C: cache_clear (blobcache_file.c:849-879). Settings action
// callback: prunes every cached item, resets accounting, saves the index,
// then notifies "Cache cleared".
func (bc *BlobCache) cacheClear() {
	bc.cacheLock.Lock()
	for i := range ItemHashSize {
		for p := bc.hashVector[i]; p != nil; {
			n := p.biLink
			bc.pruneItem(p)
			bc.indexDirty = true
			p = n
		}
		bc.hashVector[i] = nil
	}
	bc.currentCacheSize = 0
	bc.saveIndex()
	bc.cacheLock.Unlock()

	// C: notify_add(NULL, NOTIFY_INFO, NULL, 3, _("Cache cleared"))
	// NOTIFY_INFO == 0 (notifications.NotifyInfo).
	if bc.notifyAdd != nil {
		bc.notifyAdd(0, "", 3, "Cache cleared")
	}
}

// Fini shuts down the cache (matches C blobcache_fini —
// hts_thread_join waits for the flush thread to exit)
func (bs *BlobcacheSystem) Fini() {
	bs.bc.cacheLock.Lock()
	bs.bc.state = bcStateStopping
	bs.bc.cacheCond.Signal()
	bs.bc.cacheLock.Unlock()
	if bs.bc.done != nil {
		<-bs.bc.done
	}
}

// digestKey computes SHA-1 of key+stash, returns first 8 bytes as uint64.
// C: blobcache_file.c:144-157 — uses sha1, returns u64 from union
func digestKey(key, stash string) uint64 {
	h := sha1.New()
	h.Write([]byte(key))
	h.Write([]byte(stash))
	sum := h.Sum(nil)
	return binary.LittleEndian.Uint64(sum[:8])
}

// digestContent computes MurHash3_32 of data.
// C: blobcache_file.c:163-167
func digestContent(data []byte) uint64 {
	return uint64(MurHash3_32(data, 0))
}

// makeFilename generates the cache file path.
// C: blobcache_file.c:173-182
func (bc *BlobCache) makeFilename(hash uint64, forWrite bool) string {
	dir := uint8(hash)
	basePath := filepath.Join(bc.cachePath, "bc2")
	if forWrite {
		dirPath := filepath.Join(basePath, fmt.Sprintf("%02x", dir))
		os.MkdirAll(dirPath, 0755)
	}
	return filepath.Join(basePath, fmt.Sprintf("%02x", dir), fmt.Sprintf("%016x", hash))
}

// lookupItem assumes locked, finds item by key hash.
// C: blobcache_file.c:656-664
func (bc *BlobCache) lookupItem(dk uint64) *blobcacheItem {
	for p := bc.hashVector[dk&ItemHashMask]; p != nil; p = p.biLink {
		if p.biKeyHash == dk {
			return p
		}
	}
	return nil
}

// Put stores a blob in the cache.
// C: blobcache_file.c:415-488
func (bc *BlobCache) Put(key, stash string, buf *Buf, maxage int, etag string, mtime time.Time, flags int) int {
	dk := digestKey(key, stash)
	dc := digestContent(buf.Ptr)
	now := uint32(time.Now().Unix())

	if len(etag) > 255 {
		etag = ""
	}

	bc.cacheLock.Lock()
	if bc.state != bcStateRun {
		bc.cacheLock.Unlock()
		return 0
	}

	p := bc.lookupItem(dk)

	bc.cacheCond.Signal()
	bc.indexDirty = true

	if p != nil && p.biContentHash == dc && p.biSize == uint32(buf.Size) {
		p.biModtime = uint32(mtime.Unix())
		p.biExpiry = now + uint32(maxage)
		p.biLastaccess = now
		p.biFlags = uint8(flags)
		p.biEtag = etag
		bc.cacheLock.Unlock()
		return 1
	}

	bf := &blobcacheFlush{
		bfKeyHash: dk,
		bfBuf:     buf,
	}
	bc.flushQueueTailInsert(bf)
	bc.cacheCond.Signal()

	if p == nil {
		p = &blobcacheItem{
			biKeyHash:        dk,
			biSize:           0,
			biContentTypeLen: 0,
			biLink:           bc.hashVector[dk&ItemHashMask],
			biEtag:           "",
		}
		bc.hashVector[dk&ItemHashMask] = p
	}

	expiry := min(int64(maxage)+int64(now), 2147483647)

	p.biModtime = uint32(mtime.Unix())
	p.biEtag = etag
	p.biExpiry = uint32(expiry)
	p.biLastaccess = now
	p.biContentHash = dc
	bc.currentCacheSize -= uint64(p.biSize)
	p.biSize = uint32(buf.Size)
	bc.currentCacheSize += uint64(p.biSize)
	if buf.ContentType != "" {
		p.biContentTypeLen = uint8(len(buf.ContentType))
	} else {
		p.biContentTypeLen = 0
	}
	p.biFlags = uint8(flags)
	bc.cacheLock.Unlock()
	return 0
}

// flushQueueTailInsert inserts at tail of flush queue (TAILQ_INSERT_TAIL)
func (bc *BlobCache) flushQueueTailInsert(bf *blobcacheFlush) {
	if bc.flushQueueTail == nil {
		bc.flushQueueHead = bf
		bc.flushQueueTail = bf
	} else {
		bc.flushQueueTail.bfLink = bf
		bc.flushQueueTail = bf
	}
}

// flushQueueRemoveFirst removes and returns first item from flush queue (TAILQ_FIRST + TAILQ_REMOVE)
func (bc *BlobCache) flushQueueRemoveFirst() *blobcacheFlush {
	bf := bc.flushQueueHead
	if bf == nil {
		return nil
	}
	bc.flushQueueHead = bf.bfLink
	if bc.flushQueueHead == nil {
		bc.flushQueueTail = nil
	}
	bf.bfLink = nil
	return bf
}

// flushQueueReverseFind searches flush queue from tail (TAILQ_FOREACH_REVERSE)
func (bc *BlobCache) flushQueueReverseFind(keyHash uint64) *blobcacheFlush {
	for bf := bc.flushQueueTail; bf != nil; bf = bc.flushQueuePrev(bf) {
		if bf.bfKeyHash == keyHash {
			return bf
		}
	}
	return nil
}

// flushQueuePrev returns the previous element before bf in the queue
func (bc *BlobCache) flushQueuePrev(bf *blobcacheFlush) *blobcacheFlush {
	if bf == bc.flushQueueHead {
		return nil
	}
	prev := bc.flushQueueHead
	for prev != nil && prev.bfLink != bf {
		prev = prev.bfLink
	}
	return prev
}

// Get retrieves an item from the cache.
// C: blobcache_file.c:494-609
func (bc *BlobCache) Get(key, stash string, pad int, ignoreExpiry *bool, etagp *string, mtimep *time.Time) *Buf {
	dk := digestKey(key, stash)

	bc.cacheLock.Lock()

	var p *blobcacheItem
	var q **blobcacheItem

	if bc.state == bcStateStopping {
		p = nil
	} else {
		q = &bc.hashVector[dk&ItemHashMask]
		for p = *q; p != nil; p = *q {
			if p.biKeyHash == dk {
				break
			}
			q = &p.biLink
		}
	}

	if p == nil {
		bc.cacheLock.Unlock()
		return nil
	}

	now := uint32(time.Now().Unix())
	clockOk := now >= 1426926328
	expired := now > p.biExpiry && clockOk

	if expired && ignoreExpiry == nil {
		// bad: remove item
		*q = p.biLink
		bc.cacheLock.Unlock()
		return nil
	}

	// Search flush queue from tail (TAILQ_FOREACH_REVERSE)
	bf := bc.flushQueueReverseFind(p.biKeyHash)
	var b *Buf
	if bf != nil {
		b = bf.bfBuf
	}

	if b == nil {
		filename := bc.makeFilename(p.biKeyHash, false)
		bc.cacheLock.Unlock()
		data, err := os.ReadFile(filename)
		if err != nil {
			bc.cacheLock.Lock()
			*q = p.biLink
			bc.cacheLock.Unlock()
			return nil
		}
		if len(data) != int(p.biSize)+int(p.biContentTypeLen) {
			os.Remove(filename)
			bc.cacheLock.Lock()
			*q = p.biLink
			bc.cacheLock.Unlock()
			return nil
		}

		if mtimep != nil {
			*mtimep = time.Unix(int64(p.biModtime), 0)
		}
		if etagp != nil {
			*etagp = p.biEtag
		}

		bc.cacheLock.Lock()
		if bc.state == bcStateRun {
			p.biLastaccess = now
		}
		bc.indexDirty = true
		if ignoreExpiry != nil {
			*ignoreExpiry = expired
		}
		bc.cacheLock.Unlock()

		b = &Buf{
			Ptr:  make([]byte, int(p.biSize)+pad),
			Size: int(p.biSize),
		}
		offset := 0
		if p.biContentTypeLen > 0 {
			b.ContentType = string(data[:p.biContentTypeLen])
			offset = int(p.biContentTypeLen)
		}
		copy(b.Ptr, data[offset:offset+int(p.biSize)])
		return b
	}

	if mtimep != nil {
		*mtimep = time.Unix(int64(p.biModtime), 0)
	}
	if etagp != nil {
		*etagp = p.biEtag
	}
	if bc.state == bcStateRun {
		p.biLastaccess = now
	}
	bc.indexDirty = true
	if ignoreExpiry != nil {
		*ignoreExpiry = expired
	}
	bc.cacheLock.Unlock()
	return b
}

// GetMeta retrieves metadata for a cached blob.
// C: blobcache_file.c:618-650
func (bc *BlobCache) GetMeta(key, stash string, etagp *string, mtimep *time.Time) int {
	dk := digestKey(key, stash)
	bc.cacheLock.Lock()

	var p *blobcacheItem
	if bc.state == bcStateStopping {
		p = nil
	} else {
		p = bc.lookupItem(dk)
	}

	var r int
	if p != nil {
		r = 0
		if mtimep != nil {
			*mtimep = time.Unix(int64(p.biModtime), 0)
		}
		if etagp != nil {
			*etagp = p.biEtag
		}
	} else {
		r = -1
	}

	bc.cacheLock.Unlock()
	return r
}

// Evict removes a blob from the cache.
// C: blobcache_file.c:736-756
func (bc *BlobCache) Evict(key, stash string) {
	dk := digestKey(key, stash)
	bc.cacheLock.Lock()
	if bc.state == bcStateRun {
		q := &bc.hashVector[dk&ItemHashMask]
		for p := *q; p != nil; p = *q {
			if p.biKeyHash == dk {
				bc.currentCacheSize -= uint64(p.biSize)
				*q = p.biLink
				bc.pruneItem(p)
				break
			}
			q = &p.biLink
		}
	}
	bc.cacheLock.Unlock()
}

// pruneItem removes item file from disk (assumes locked).
// C: blobcache_file.c:722-729
func (bc *BlobCache) pruneItem(p *blobcacheItem) {
	filename := bc.makeFilename(p.biKeyHash, false)
	os.Remove(filename)
}

// accesstimecmp matches C accesstimecmp (blobcache_file.c:762-775)
func accesstimecmp(a, b *blobcacheItem) int {
	aImp := 0
	if a.biFlags&ImportantItem != 0 {
		aImp = 1
	}
	bImp := 0
	if b.biFlags&ImportantItem != 0 {
		bImp = 1
	}
	if aImp != bImp {
		return aImp - bImp
	}
	return int(a.biLastaccess) - int(b.biLastaccess)
}

// pruneToSize prunes cache items to fit within max size.
// C: blobcache_file.c:781-821
func (bc *BlobCache) pruneToSize(maxsize uint64) {
	tot := 0
	for i := range ItemHashSize {
		for p := bc.hashVector[i]; p != nil; p = p.biLink {
			tot++
		}
	}

	sv := make([]*blobcacheItem, tot)
	bc.currentCacheSize = 0
	j := 0
	for i := range ItemHashSize {
		for p := bc.hashVector[i]; p != nil; p = p.biLink {
			sv[j] = p
			j++
			bc.currentCacheSize += uint64(p.biSize)
		}
		bc.hashVector[i] = nil
	}

	slices.SortFunc(sv, accesstimecmp)

	i := 0
	for ; i < len(sv); i++ {
		p := sv[i]
		if bc.currentCacheSize < maxsize {
			break
		}
		bc.currentCacheSize -= uint64(p.biSize)
		bc.pruneItem(p)
		bc.indexDirty = true
	}

	for ; i < len(sv); i++ {
		p := sv[i]
		p.biLink = bc.hashVector[p.biKeyHash&ItemHashMask]
		bc.hashVector[p.biKeyHash&ItemHashMask] = p
	}

	bc.saveIndex()
}

// blobcachePruneOld removes old cache formats.
// C: blobcache_file.c:828-842
func blobcachePruneOld(cachePath string) {
	path := filepath.Join(cachePath, "blobcache")
	os.RemoveAll(path)
	os.Remove(filepath.Join(cachePath, "cachedb", "cache.db"))
	os.Remove(filepath.Join(cachePath, "cachedb", "cache.db-shm"))
	os.Remove(filepath.Join(cachePath, "cachedb", "cache.db-wal"))
}

// saveIndex saves the cache index to disk.
// C: blobcache_file.c:188-270
func (bc *BlobCache) saveIndex() {
	if !bc.indexDirty {
		return
	}

	filename := filepath.Join(bc.cachePath, "bc2", "index.dat")

	items := 0
	siz := 12 + 20
	for i := range ItemHashSize {
		for p := bc.hashVector[i]; p != nil; p = p.biLink {
			siz += 35 + len(p.biEtag)
			items++
		}
	}

	out := make([]byte, siz)
	offset := 0

	binary.LittleEndian.PutUint32(out[offset:], BC2Magic07)
	offset += 4
	binary.LittleEndian.PutUint32(out[offset:], uint32(items))
	offset += 4
	binary.LittleEndian.PutUint32(out[offset:], uint32(time.Now().Unix()))
	offset += 4

	for i := range ItemHashSize {
		for p := bc.hashVector[i]; p != nil; p = p.biLink {
			etaglen := len(p.biEtag)
			binary.LittleEndian.PutUint64(out[offset:], p.biKeyHash)
			offset += 8
			binary.LittleEndian.PutUint64(out[offset:], p.biContentHash)
			offset += 8
			binary.LittleEndian.PutUint32(out[offset:], p.biLastaccess)
			offset += 4
			binary.LittleEndian.PutUint32(out[offset:], p.biExpiry)
			offset += 4
			binary.LittleEndian.PutUint32(out[offset:], p.biModtime)
			offset += 4
			binary.LittleEndian.PutUint32(out[offset:], p.biSize)
			offset += 4
			out[offset] = p.biFlags
			offset++
			out[offset] = byte(etaglen)
			offset++
			out[offset] = p.biContentTypeLen
			offset++
			if etaglen > 0 {
				copy(out[offset:], p.biEtag)
				offset += etaglen
			}
		}
	}

	// SHA-1 checksum (20 bytes)
	h := sha1.New()
	h.Write(out[:siz-20])
	sum := h.Sum(nil)
	copy(out[siz-20:], sum)

	if err := os.WriteFile(filename, out, 0644); err != nil {
		return
	}
	bc.indexDirty = false
}

// loadIndex loads the cache index from disk.
// C: blobcache_file.c:276-409
func (bc *BlobCache) loadIndex() {
	filename := filepath.Join(bc.cachePath, "bc2", "index.dat")
	data, err := os.ReadFile(filename)
	if err != nil {
		return
	}

	if len(data) < 20 {
		return
	}

	// Verify SHA-1 (20 bytes)
	h := sha1.New()
	h.Write(data[:len(data)-20])
	sum := h.Sum(nil)
	if string(sum) != string(data[len(data)-20:]) {
		return
	}

	in := data
	offset := 0
	magic := binary.LittleEndian.Uint32(in[offset:])
	offset += 4
	items := int(binary.LittleEndian.Uint32(in[offset:]))
	offset += 4

	switch magic {
	case BC2Magic06:
		// Upgrade from older format
		fallthrough
	case BC2Magic07:
		bc.loadedCacheIsFrom = int(binary.LittleEndian.Uint32(in[offset:]))
		offset += 4
	case BC2Magic05:
		// Upgrade from older format
	default:
		return
	}

	for range items {
		p := &blobcacheItem{}
		var etaglen int

		switch magic {
		case BC2Magic05, BC2Magic06:
			p.biKeyHash = binary.LittleEndian.Uint64(in[offset:])
			offset += 8
			p.biContentHash = binary.LittleEndian.Uint64(in[offset:])
			offset += 8
			p.biLastaccess = binary.LittleEndian.Uint32(in[offset:])
			offset += 4
			p.biExpiry = binary.LittleEndian.Uint32(in[offset:])
			offset += 4
			p.biModtime = binary.LittleEndian.Uint32(in[offset:])
			offset += 4
			p.biSize = binary.LittleEndian.Uint32(in[offset:])
			offset += 4
			p.biContentTypeLen = in[offset+1]
			p.biFlags = 0
			etaglen = int(in[offset])
			offset += 34
		case BC2Magic07:
			p.biKeyHash = binary.LittleEndian.Uint64(in[offset:])
			offset += 8
			p.biContentHash = binary.LittleEndian.Uint64(in[offset:])
			offset += 8
			p.biLastaccess = binary.LittleEndian.Uint32(in[offset:])
			offset += 4
			p.biExpiry = binary.LittleEndian.Uint32(in[offset:])
			offset += 4
			p.biModtime = binary.LittleEndian.Uint32(in[offset:])
			offset += 4
			p.biSize = binary.LittleEndian.Uint32(in[offset:])
			offset += 4
			p.biFlags = in[offset]
			offset++
			etaglen = int(in[offset])
			offset++
			p.biContentTypeLen = in[offset]
			offset++
		}

		if etaglen > 0 {
			p.biEtag = string(in[offset : offset+etaglen])
			offset += etaglen
		} else {
			p.biEtag = ""
		}
		p.biLink = bc.hashVector[p.biKeyHash&ItemHashMask]
		bc.hashVector[p.biKeyHash&ItemHashMask] = p
		bc.currentCacheSize += uint64(p.biSize)
	}
}

// pruneStale removes files that are not in the index.
// C: blobcache_file.c:669-715
func (bc *BlobCache) pruneStale() {
	bc2Path := filepath.Join(bc.cachePath, "bc2")
	entries, err := os.ReadDir(bc2Path)
	if err != nil {
		return
	}
	for _, entry := range entries {
		n1 := entry.Name()
		if n1[0] == '.' {
			continue
		}
		dirPath := filepath.Join(bc2Path, n1)
		subEntries, err := os.ReadDir(dirPath)
		if err != nil {
			continue
		}
		for _, subEntry := range subEntries {
			n2 := subEntry.Name()
			if n2[0] == '.' {
				continue
			}
			filePath := filepath.Join(dirPath, n2)
			var k uint64
			if _, err := fmt.Sscanf(n2, "%016x", &k); err != nil || bc.lookupItem(k) == nil {
				os.Remove(filePath)
			}
		}
		os.Remove(dirPath)
	}
}

// computeMaxSize calculates the maximum cache size based on available disk space.
// C: blobcache_file.c:125-138
func (bc *BlobCache) computeMaxSize() uint64 {
	avail, ok := diskAvailBytes(bc.cachePath)
	if !ok {
		return BlobCacheMinSize
	}
	avail += bc.currentCacheSize
	avail = maxU64(BlobCacheMinSize, minU64(avail/10, BlobCacheMaxSize))
	return avail
}

// flushThread handles async flushing of items to disk.
// C: blobcache_file.c:875-957
func (bc *BlobCache) flushThread() {
	if bc.done != nil {
		defer close(bc.done)
	}
	time.Sleep(3 * time.Second)
	bc.pruneStale()

	bc.cacheLock.Lock()
	maxsize := bc.computeMaxSize()
	bc.pruneToSize(maxsize)
	bc.cacheLock.Unlock()

	// Wait for valid clock
	bc.cacheLock.Lock()
	for bc.state == bcStateBadClock {
		now := time.Now().Unix()
		if now < 1426926328 {
			bc.cacheCond.Wait()
		} else {
			bc.state = bcStateRun
		}
	}
	bc.cacheLock.Unlock()

	for {
		bc.cacheLock.Lock()
		if bc.state == bcStateStopping {
			bc.cacheLock.Unlock()
			break
		}
		bf := bc.flushQueueRemoveFirst()
		if bf == nil {
			if bc.indexDirty {
				// Wait with 5s timeout, save index on timeout
				// C: hts_cond_wait_timeout(&cache_cond, &cache_lock, 5000)
				// Go: use a timer goroutine to signal
				done := make(chan struct{})
				go func() {
					time.Sleep(5 * time.Second)
					select {
					case <-done:
					default:
						bc.cacheLock.Lock()
						bc.cacheCond.Signal()
						bc.cacheLock.Unlock()
					}
				}()
				bc.cacheCond.Wait()
				close(done)
				if bc.indexDirty {
					bc.saveIndex()
				}
			} else {
				bc.cacheCond.Wait()
			}
			bc.cacheLock.Unlock()
			continue
		}
		bc.cacheLock.Unlock()

		filename := bc.makeFilename(bf.bfKeyHash, true)
		b := bf.bfBuf

		var fileData []byte
		if b.ContentType != "" {
			fileData = append(fileData, []byte(b.ContentType)...)
		}
		fileData = append(fileData, b.Ptr...)

		if err := os.WriteFile(filename, fileData, 0644); err != nil {
			os.Remove(filename)
		}

		bc.cacheLock.Lock()
		maxsize := bc.computeMaxSize()
		if maxsize < bc.currentCacheSize {
			bc.pruneToSize(maxsize)
		}
		bc.cacheLock.Unlock()
	}

	bc.cacheLock.Lock()
	bc.saveIndex()
	bc.cacheLock.Unlock()
}

func maxU64(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}

func minU64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

// Ensure unsafe is imported (matching C pointer arithmetic semantics)
var _ = unsafe.Pointer(nil)
