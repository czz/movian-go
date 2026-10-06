//go:build !android

package fileaccess

// registerAndroidProtocols — no-op off android; the es/cache/
// persistent schemes exist only there (android_fs.c).
func registerAndroidProtocols(fam *FileAccessManager) {}

// populateFSGate — off android: the "file" gate carries the fa_fs.c
// filesystem ops (fs_stat/fs_unlink/… — fileaccess.c populate).
func populateFSGate(fam *FileAccessManager) {
	if fp := fam.lookupFAProtocol("file"); fp != nil {
		fam.populateFSFAProtocol(fp)
	}
}
