//go:build android

package main

import (
	bundles "github.com/czz/movian-go"
	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
)

// registerFileBundles — C: the filebundle_register() constructor calls
// emitted by support/mkbundle into each builddir/bundles/<prefix>.c
// object; they run when libcore.so is loaded, before main_init.
func registerFileBundles(bm *fileaccesscore.BundleManager) {
	bundles.RegisterFileBundles(bm)
}
