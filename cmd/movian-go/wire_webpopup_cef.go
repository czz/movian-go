//go:build cef

package main

// wire_webpopup_cef.go — embedded webpopup: glue between the CEF
// session (internal/ui) and the websurface GLW widget
// (internal/ui/glw). glw imports ui already, so ui cannot import glw;
// the seam is injected here at init.

import (
	"github.com/czz/movian-go/internal/ui"
	uiglw "github.com/czz/movian-go/internal/ui/glw"
)

func init() {
	ui.SetWebsurfaceGlue(
		func(h any) bool {
			return uiglw.WebsurfaceShow(h.(uiglw.WebsurfaceHooks))
		},
		uiglw.WebsurfaceHide,
	)
}
