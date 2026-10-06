//go:build windows

package fileaccess

// Windows has no POSIX xattr — mirrors the C !HAVE_XATTR build where
// the fs ops report "not supported" to the prop layer.

// fsSetXattr — C: !HAVE_XATTR fallback.
func fsSetXattr(path, name string, data []byte) int {
	return FAP_NOT_SUPPORTED
}

// fsGetXattr — C: !HAVE_XATTR fallback.
func fsGetXattr(path, name string) ([]byte, int) {
	return nil, FAP_NOT_SUPPORTED
}
