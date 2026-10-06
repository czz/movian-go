//go:build !android && !windows && !bundle

package main

import fileaccesscore "github.com/czz/movian-go/internal/fileaccess"

// registerFileBundles — non-android builds link no mkbundle objects
// (support/dataroot/wd.c|datadir.c serve dataroot:// from the fs).
func registerFileBundles(bm *fileaccesscore.BundleManager) {}
