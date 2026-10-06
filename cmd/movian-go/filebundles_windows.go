//go:build windows

package main

import (
	bundles "github.com/czz/movian-go"
	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
)

// registerFileBundles — wires the embedded windows resource set
// (bundles_windows.go) into the bundle manager, same as the
// android variant.
func registerFileBundles(bm *fileaccesscore.BundleManager) {
	bundles.RegisterFileBundles(bm)
}
