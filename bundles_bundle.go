//go:build bundle

package movian

import (
	"embed"

	fileaccess "github.com/czz/movian-go/internal/fileaccess"
)

// bundleData embeds the runtime resources — C link variant
// support/dataroot/bundle.c (same set as bundles_android.go /
// bundles_windows.go): with -tags bundle a bare movian-go binary is
// self-contained — dataroot:// resolves to bundle:// and no res/,
// lang/ or glwskins/ folders need to sit next to the binary.
//
//go:embed res/metadb res/kvstore res/fileaccess res/tvheadend res/shaders/glsl res/fonts res/svg res/static res/ecmascript lang glwskins/flat
var bundleData embed.FS

// filebundlePrefixes — same BUNDLES list as bundles_android.go.
var filebundlePrefixes = []string{
	"res/metadb",
	"res/kvstore",
	"res/fileaccess",
	"res/tvheadend",
	"res/shaders/glsl",
	"res/fonts",
	"res/svg",
	"res/static",
	"res/ecmascript",
	"lang",
	"glwskins/flat",
}

// RegisterFileBundles registers the embedded resources into the bundle
// manager — mirrors bundles_android.go.
func RegisterFileBundles(bm *fileaccess.BundleManager) {
	for _, prefix := range filebundlePrefixes {
		if err := bm.BundleRegisterFS(prefix, prefix, bundleData); err != nil {
			panic("filebundle " + prefix + ": " + err.Error())
		}
	}
}
