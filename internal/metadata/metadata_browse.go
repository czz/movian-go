package metadata

import (
	"slices"
	"sync"
)

// BrowseMDB represents browse metadata database
type BrowseMDB struct {
	items []MetadataItem
	mutex sync.RWMutex
}

type MetadataItem struct {
	ID       int64
	Title    string
	URL      string
	Icon     string
	Metadata *Metadata
	ParentID int64
	Type     string
}

func NewBrowseMDB() *BrowseMDB {
	return &BrowseMDB{
		items: make([]MetadataItem, 0),
	}
}

func (b *BrowseMDB) AddItem(item MetadataItem) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	b.items = append(b.items, item)
}

func (b *BrowseMDB) GetItems() []MetadataItem {
	b.mutex.RLock()
	defer b.mutex.RUnlock()
	result := make([]MetadataItem, len(b.items))
	copy(result, b.items)
	return result
}

func (b *BrowseMDB) Clear() {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	b.items = make([]MetadataItem, 0)
}

func (b *BrowseMDB) GetItemByID(id int64) *MetadataItem {
	b.mutex.RLock()
	defer b.mutex.RUnlock()

	for i := range b.items {
		if b.items[i].ID == id {
			return &b.items[i]
		}
	}
	return nil
}

func (b *BrowseMDB) GetItemsByParent(parentID int64) []MetadataItem {
	b.mutex.RLock()
	defer b.mutex.RUnlock()

	result := make([]MetadataItem, 0)
	for i := range b.items {
		if b.items[i].ParentID == parentID {
			result = append(result, b.items[i])
		}
	}
	return result
}

func (b *BrowseMDB) GetItemsByType(itemType string) []MetadataItem {
	b.mutex.RLock()
	defer b.mutex.RUnlock()

	result := make([]MetadataItem, 0)
	for i := range b.items {
		if b.items[i].Type == itemType {
			result = append(result, b.items[i])
		}
	}
	return result
}

func (b *BrowseMDB) FindItemByURL(url string) *MetadataItem {
	b.mutex.RLock()
	defer b.mutex.RUnlock()

	for i := range b.items {
		if b.items[i].URL == url {
			return &b.items[i]
		}
	}
	return nil
}

func (b *BrowseMDB) RemoveItem(id int64) bool {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	for i, item := range b.items {
		if item.ID == id {
			b.items = slices.Delete(b.items, i, i+1)
			return true
		}
	}
	return false
}

func (b *BrowseMDB) UpdateItem(item MetadataItem) bool {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	for i := range b.items {
		if b.items[i].ID == item.ID {
			b.items[i] = item
			return true
		}
	}
	return false
}

func (b *BrowseMDB) GetCount() int {
	b.mutex.RLock()
	defer b.mutex.RUnlock()
	return len(b.items)
}

func (ms *MetadataStr) Clear() {
	ms.strings = make(map[string]string)
}

func (mdb *MetaDB) Clear() {
	mdb.mutex.Lock()
	defer mdb.mutex.Unlock()
	mdb.items = make(map[int64]*Metadata)
	mdb.artists = make(map[string]int64)
	mdb.albums = make(map[string]int64)
}

func (mlp *MLP) Clear() {
	mlp.sources = make([]*MetadataSource, 0)
}
