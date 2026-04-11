//go:build !darwin

package tray

// installAppDelegate is a no-op on non-macOS platforms.
func installAppDelegate(dashboardURL string) {}
