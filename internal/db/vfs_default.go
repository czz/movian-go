//go:build !android

package db

import "strings"

// dbFSPath — see vfs_android.go. Off-android there are no
// persistent://, cache:// or es:// schemes; only file:// URLs arrive.
func dbFSPath(path string) string {
	return strings.TrimPrefix(path, "file://")
}
