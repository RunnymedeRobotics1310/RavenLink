//go:build !windows && !darwin

package autostart

import "log/slog"

// Enable is a stub on unsupported platforms.
func Enable() bool {
	slog.Warn("auto-start not supported on this platform")
	return false
}

// Disable is a stub on unsupported platforms.
func Disable() bool {
	return false
}

// IsEnabled always returns false on unsupported platforms.
func IsEnabled() bool {
	return false
}
