//go:build !((linux || darwin || windows) && glfw)

package main

import (
	"github.com/czz/movian-go/internal/arch"
)

// uiGlfw — nil without the glfw build tag.
var uiGlfw *arch.LinuxUI

// renderLoopGlfw — no GLFW frontend in this build.
func renderLoopGlfw(ctx *appContext) {}
