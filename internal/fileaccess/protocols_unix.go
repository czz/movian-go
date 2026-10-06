//go:build !windows

package fileaccess

import (
	"syscall"
)

// FSInfo returns filesystem information for Unix systems
func (p *FSProtocol) FSInfo(url string) (*FileSystemInfo, error) {
	path := p.urlToPath(url)

	var stat syscall.Statfs_t
	err := syscall.Statfs(path, &stat)
	if err != nil {
		return nil, err
	}

	return &FileSystemInfo{
		Size:  stat.Blocks * uint64(stat.Bsize),
		Avail: stat.Bavail * uint64(stat.Bsize),
	}, nil
}
