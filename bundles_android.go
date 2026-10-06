//go:build android

package movian

import (
	"embed"

	fileaccess "github.com/czz/movian-go/internal/fileaccess"
)

// bundleData embeds the runtime resources — Go equivalent of the .c
// files that support/mkbundle generates from each BUNDLES entry
// (Makefile `BUNDLES`, configure.inc tail).
//
//go:embed res/metadb res/kvstore res/fileaccess res/tvheadend res/shaders/glsl res/fonts res/svg res/static res/ecmascript lang glwskins/flat
var bundleData embed.FS

// filebundlePrefixes — C: the BUNDLES list for the android build:
//
//	Makefile:     res/metadb (CONFIG_METADATA), res/kvstore (CONFIG_KVSTORE),
//	              res/fileaccess, res/tvheadend (CONFIG_HTSP),
//	              res/shaders/glsl (CONFIG_GLW_BACKEND_OPENGL_ES)
//	configure.inc: glwskins/flat (CONFIG_GLW, GLW_DEFAULT_SKIN="flat"),
//	              res/fonts, res/svg, lang, res/static, res/ecmascript
//
// mkbundle -p $< sets each filebundle's prefix to the entry itself.
// res/speaker_positions (audiotest) and guresources (gu) are disabled.
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
// manager — C: the filebundle_register() constructor calls emitted by
// mkbundle into each builddir/bundles/<prefix>.c object.
func RegisterFileBundles(bm *fileaccess.BundleManager) {
	for _, prefix := range filebundlePrefixes {
		if err := bm.BundleRegisterFS(prefix, prefix, bundleData); err != nil {
			panic("filebundle " + prefix + ": " + err.Error())
		}
	}
}
