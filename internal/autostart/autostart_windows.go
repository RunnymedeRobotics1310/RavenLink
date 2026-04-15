//go:build windows

package autostart

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const (
	appName = "RavenLink"
	runKey  = `Software\Microsoft\Windows\CurrentVersion\Run`
)

// Enable registers the current executable to run on user login.
func Enable() bool {
	exe, err := os.Executable()
	if err != nil {
		slog.Warn("failed to resolve executable path", "err", err)
		return false
	}
	// Resolve symlinks so the registry value survives across reboots.
	// os.Executable() can return a symlinked or 8.3-short-name path.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	// Skip registration if running from a temp/build-cache directory
	// (e.g. `go run`) — the binary won't exist after reboot.
	if strings.Contains(strings.ToLower(exe), `\temp\`) ||
		strings.Contains(strings.ToLower(exe), `\go-build`) {
		slog.Warn("autostart: skipping registration — executable is in a temp directory", "exe", exe)
		return false
	}
	cmd := `"` + exe + `" --minimized`

	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		slog.Warn("failed to open Run key", "err", err)
		return false
	}
	defer k.Close()

	if err := k.SetStringValue(appName, cmd); err != nil {
		slog.Warn("failed to write Run value", "err", err)
		return false
	}
	slog.Info("autostart: registered", "cmd", cmd)
	return true
}

// Disable removes the launch-on-login registry entry.
func Disable() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		if err == registry.ErrNotExist {
			return true
		}
		slog.Warn("failed to open Run key", "err", err)
		return false
	}
	defer k.Close()

	if err := k.DeleteValue(appName); err != nil && err != registry.ErrNotExist {
		slog.Warn("failed to delete Run value", "err", err)
		return false
	}
	return true
}

// IsEnabled reports whether the app is registered to run at login.
func IsEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()

	_, _, err = k.GetStringValue(appName)
	return err == nil
}
