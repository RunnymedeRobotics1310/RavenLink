//go:build !darwin

package tray

import "fyne.io/systray"

// setTrayIcon on Windows/Linux just calls SetIcon with the
// platform-appropriate colored icon (ICO for Windows via makeIcon's
// build-tagged wrapper, PNG for Linux).
func setTrayIcon(colorName string) {
	systray.SetIcon(makeIcon(colorName))
}
