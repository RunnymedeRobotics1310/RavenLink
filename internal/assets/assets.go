// Package assets embeds static image assets shared across multiple
// packages (tray icon, .icns generation, etc).
package assets

import (
	_ "embed"
)

// Team1310Logo is the team 1310 raven/phoenix logo as a raw PNG.
// Source: https://raveneye.team1310.ca/assets/logo-kAGSIXdr.png
//
//go:embed team1310-logo.png
var Team1310Logo []byte
