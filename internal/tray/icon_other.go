//go:build !windows

package tray

// makeIcon returns icon bytes in the format expected by fyne.io/systray
// on macOS and Linux: raw PNG.
func makeIcon(name string) []byte {
	return renderIconPNG(name)
}
