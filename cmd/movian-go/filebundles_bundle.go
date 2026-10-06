//go:build bundle

package main

import (
	bundles "github.com/czz/movian-go"
	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
)

// registerFileBundles — wires the embedded resource set
// (bundles_bundle.go) into the bundle manager, same as the
// android/windows variants (C: support/dataroot/bundle.c link).
func registerFileBundles(bm *fileaccesscore.BundleManager) {
	bundles.RegisterFileBundles(bm)
}
