//go:build !((linux || darwin || windows) && glfw) && !android && !(linux && rpi) && !(linux && sunxi)

package main

import (
	navcore "github.com/czz/movian-go/internal/navigator"
	settingscore "github.com/czz/movian-go/internal/settings"
)

// glwFrontendEarlyStart — no GLW backend in this build.
func glwFrontendEarlyStart(ctx *appContext, settingsManager *settingscore.SettingsManager) {
}

// glwFrontendSettingsStart — no GLW backend in this build.
func glwFrontendSettingsStart(ctx *appContext) {}

// glwFrontendLateStart — no GLW backend in this build.
func glwFrontendLateStart(ctx *appContext, defaultNav *navcore.Navigator, settingsManager *settingscore.SettingsManager) {
}
