//go:build android

package app

// AppDataRoot returns the application data root directory.
// C: support/dataroot/bundle.c — linked into $(LIB).so (android) and
// $(PROG).bundle; the dataroot is the embedded filebundle set.
func AppDataRoot() string {
	return "bundle://"
}
