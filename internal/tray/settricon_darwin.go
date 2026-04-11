//go:build darwin

package tray

import "fyne.io/systray"

// setTrayIcon on macOS uses SetTemplateIcon so AppKit renders the
// icon correctly for the menu bar. Template icons are black+alpha
// masks that macOS tints automatically based on light/dark mode and
// selection state. The regular colored icon is passed as the fallback
// for anywhere else the systray library might display the icon.
//
// Without the template path, fyne.io/systray on recent macOS versions
// often fails to display the menu bar icon at all (no visible item,
// even though onReady fires successfully).
func setTrayIcon(colorName string) {
	template := renderTemplateIconPNG()
	regular := makeIcon(colorName)
	systray.SetTemplateIcon(template, regular)
}
