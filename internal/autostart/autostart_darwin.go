//go:build darwin

package autostart

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

const plistLabel = "ca.team1310.ravenlink"

// xmlEscape escapes the five XML predefined entities so that arbitrary
// strings can be safely interpolated into an XML/plist document.
func xmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return r.Replace(s)
}

func plistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", plistLabel+".plist")
}

// Enable writes a LaunchAgent plist that runs the current executable
// at login.
func Enable() bool {
	exe, err := os.Executable()
	if err != nil {
		slog.Warn("failed to resolve executable path", "err", err)
		return false
	}

	path := plistPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		slog.Warn("failed to create LaunchAgents directory", "err", err)
		return false
	}

	content := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>--minimized</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
</dict>
</plist>
`, xmlEscape(plistLabel), xmlEscape(exe))

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		slog.Warn("failed to write LaunchAgent plist", "err", err)
		return false
	}
	return true
}

// Disable removes the LaunchAgent plist.
func Disable() bool {
	path := plistPath()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		slog.Warn("failed to remove LaunchAgent plist", "err", err)
		return false
	}
	return true
}

// IsEnabled reports whether the LaunchAgent plist exists.
func IsEnabled() bool {
	_, err := os.Stat(plistPath())
	return err == nil
}
