// Package autostart manages launch-on-login registration for the
// RavenLink bridge across Windows, macOS, and Linux.
package autostart

import "log/slog"

// Sync ensures the autostart state matches shouldEnable. If the
// current state already matches, it is a no-op.
func Sync(shouldEnable bool) {
	current := IsEnabled()
	if shouldEnable && !current {
		if Enable() {
			slog.Info("auto-start registered")
		}
	} else if !shouldEnable && current {
		if Disable() {
			slog.Info("auto-start unregistered")
		}
	}
}
