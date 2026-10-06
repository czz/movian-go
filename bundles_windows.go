//go:build windows

package movian

import (
	"embed"

	fileaccess "github.com/czz/movian-go/internal/fileaccess"
)

// bundleData embeds the runtime resources — same set as
// bundles_android.go so a bare movian.exe is self-contained:
// dataroot:// resolves to bundle:// and no res/, lang/ or glwskins/
// folders need to sit next to the binary.
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
