package fileaccess

import (
	"context"
	"errors"
	"maps"
	"strings"
)

// Dir represents a directory listing with entries and count
type Dir struct {
	Entries map[string]*DirEntry // Map of filename to directory entry
	Count   int                  // Number of entries
}

// Free releases resources associated with the directory
func (d *Dir) Free() {
	if d != nil {
		d.Entries = nil
		d.Count = 0
	}
}

// DirAlloc allocates a new directory structure
func DirAlloc() *Dir {
	return &Dir{
		Entries: make(map[string]*DirEntry),
		Count:   0,
	}
}

// DirFree frees a directory structure and releases its resources
func DirFree(fd *Dir) {
	if fd != nil {
		fd.Entries = nil
		fd.Count = 0
	}
}

// DirAdd adds an entry to a directory
func DirAdd(fd *Dir, path string, name string, fileType int) *DirEntry {
	if fd == nil {
		return nil
	}

	entry := &DirEntry{
		Filename: name,
		URL:      path,
		Type:     fileType,
		Stat:     FileStat{},
		StatDone: false,
		Probed:   0,
		Marked:   false,
	}

	fd.Entries[name] = entry
	fd.Count++

	return entry
}

// DirFind finds an entry in a directory by URL
func DirFind(fd *Dir, url string) *DirEntry {
	if fd == nil {
		return nil
	}

	for _, entry := range fd.Entries {
		if entry.URL == url {
			return entry
		}
	}
	return nil
}

// DirEntryFree removes and frees a directory entry
func DirEntryFree(fd *Dir, fde *DirEntry) {
	if fd == nil || fde == nil {
		return
	}

	delete(fd.Entries, fde.Filename)
	fd.Count--
}

// DirEntryStat stats a directory entry and populates its Stat field
func DirEntryStat(fam *FileAccessManager, fde *DirEntry) error {
	if fde == nil {
		return errors.New("nil entry")
	}

	var proto Protocol
	url := fde.URL
	if fam != nil {
		proto = fam.FindProtocol(url)
	}
	if proto == nil {
		// C: fa_resolve_proto rewrites dataroot:// before matching.
		if rproto, fname, err := fam.FAResolveProto(url); err == nil && rproto != nil && fname != url {
			url = fname
			if rproto != fam.nativeProto {
				url = rproto.Name + "://" + fname
			}
			if fam != nil {
				proto = fam.FindProtocol(url)
			}
		}
	}
	if proto == nil {
		return errors.New("no protocol handler")
	}

	stat, err := proto.Stat(url)
	if err != nil {
		return err
	}

	fde.Stat = *stat
	fde.StatDone = true
	return nil
}

// DirInsert inserts an entry into a directory
func DirInsert(fd *Dir, fde *DirEntry) {
	if fd == nil || fde == nil {
		return
	}

	fd.Entries[fde.Filename] = fde
	fd.Count++
}

// DirRemove removes an entry from a directory
func DirRemove(fd *Dir, fde *DirEntry) {
	DirEntryFree(fd, fde)
}

// DirPrint prints a directory for debugging purposes
func DirPrint(fd *Dir) {
	if fd == nil {
		return
	}

	for name, entry := range fd.Entries {
		// Debug output removed - would use trace system
		_ = name
		_ = entry
	}
}

// ScanDir scans a directory and returns its contents
func ScanDir(fam *FileAccessManager, url string) (*Dir, error) {
	return ScanDirEx(fam, url, 0)
}

// faFlagsCtxKey carries the fa_scandir flags (FA_NON_INTERACTIVE etc.)
// through the Protocol.ScanDir ctx — C: fap_scan receives them directly.
type faFlagsCtxKey struct{}

// FAFlagsCtx builds a ctx carrying the fap_scan flags — for callers
// that invoke a specific protocol's ScanDir directly (C: fap_scan's
// flags parameter), e.g. the FTP server calling fa_protocol_vfs.
func FAFlagsCtx(flags int) context.Context {
	return context.WithValue(context.Background(), faFlagsCtxKey{}, flags)
}

// FAFlagsFromCtx extracts the flags ScanDirEx carried — 0 when absent.
func FAFlagsFromCtx(ctx context.Context) int {
	if v, ok := ctx.Value(faFlagsCtxKey{}).(int); ok {
		return v
	}
	return 0
}

// ScanDirEx scans a directory with additional flags
func ScanDirEx(fam *FileAccessManager, url string, flags int) (*Dir, error) {
	var proto Protocol
	if fam != nil {
		proto = fam.FindProtocol(url)
	}
	if proto == nil {
		// C: fa_resolve_proto rewrites dataroot:// before matching.
		if rproto, fname, err := fam.FAResolveProto(url); err == nil && rproto != nil && fname != url {
			url = fname
			if rproto != fam.nativeProto {
				url = rproto.Name + "://" + fname
			}
			if fam != nil {
				proto = fam.FindProtocol(url)
			}
		}
	}
	if proto == nil {
		// Fallback: use FSProtocol for file:// URLs when no manager is available
		if strings.HasPrefix(url, "file://") || !strings.Contains(url, "://") {
			proto = &FSProtocol{}
		}
	}
	if proto == nil {
		return nil, errors.New("no protocol handler")
	}

	return proto.ScanDir(context.WithValue(context.Background(),
		faFlagsCtxKey{}, flags), url)
}

// ScanDir2 scans a directory into an existing Dir structure
func ScanDir2(fam *FileAccessManager, fd *Dir, url string, flags int) error {
	var proto Protocol
	if fam != nil {
		proto = fam.FindProtocol(url)
	}
	if proto == nil {
		// C: fa_resolve_proto rewrites dataroot:// before matching.
		if rproto, fname, err := fam.FAResolveProto(url); err == nil && rproto != nil && fname != url {
			url = fname
			if rproto != fam.nativeProto {
				url = rproto.Name + "://" + fname
			}
			if fam != nil {
				proto = fam.FindProtocol(url)
			}
		}
	}
	if proto == nil {
		return errors.New("no protocol handler")
	}

	if fd == nil {
		return errors.New("directory is nil")
	}

	// Scan the directory and merge entries into existing Dir
	newDir, err := proto.ScanDir(context.WithValue(context.Background(),
		faFlagsCtxKey{}, flags), url)
	if err != nil {
		return err
	}

	// Merge entries
	maps.Copy(fd.Entries, newDir.Entries)
	fd.Count += newDir.Count

	return nil
}
