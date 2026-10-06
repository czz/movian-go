//go:build !windows

package fileaccess

// C: fa_fs.c fs_set_xattr / fs_get_xattr (HAVE_XATTR section).

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

// fsSetXattr — C: fs_set_xattr (fa_fs.c:719, HAVE_XATTR).
// Linux: names get a "user." prefix; NULL data removes the attr.
func fsSetXattr(path, name string, data []byte) int {
	name = "user." + name
	if data == nil {
		unix.Removexattr(path, name)
		return 0
	}
	if err := unix.Setxattr(path, name, data, 0); err != nil {
		switch err {
		case syscall.EROFS:
			return FAP_PERMISSION_DENIED
		case syscall.ENOTSUP:
			return FAP_NOT_SUPPORTED
		default:
			return FAP_ERROR
		}
	}
	return 0
}

// fsGetXattr — C: fs_get_xattr (fa_fs.c:760, HAVE_XATTR).
func fsGetXattr(path, name string) ([]byte, int) {
	name = "user." + name
	sz, err := unix.Getxattr(path, name, nil)
	if err != nil {
		if errors.Is(err, syscall.ENODATA) {
			return nil, 0 // C: *datap=NULL, *lenp=0, return 0
		}
		if errors.Is(err, syscall.ENOTSUP) {
			return nil, FAP_NOT_SUPPORTED
		}
		return nil, FAP_ERROR
	}
	buf := make([]byte, sz)
	if _, err := unix.Getxattr(path, name, buf); err != nil {
		return nil, FAP_ERROR
	}
	return buf, 0
}
