// Package paths resolves OS-standard locations for config files, data
// directories, and log files so RavenLink can run cleanly whether it's
// launched from a terminal (cwd=project) or from Finder/Explorer
// (cwd=/ or $HOME).
package paths

import (
	"os"
	"path/filepath"
	"runtime"
)

// AppDir returns the application's working directory — the base under
// which config.yaml and data/ live. Order of precedence:
//
//  1. $RAVENLINK_HOME (env override for CI / custom installs)
//  2. The current working directory, if it contains config.yaml
//  3. ~/Library/Application Support/RavenLink (macOS)
//     %APPDATA%\RavenLink (Windows)
//     $XDG_CONFIG_HOME/ravenlink or ~/.config/ravenlink (Linux)
//
// If no existing directory matches, the OS-standard path is created
// and returned so the app has somewhere to put config and data.
func AppDir() (string, error) {
	if env := os.Getenv("RAVENLINK_HOME"); env != "" {
		if err := os.MkdirAll(env, 0o755); err != nil {
			return "", err
		}
		return env, nil
	}

	// On Windows, always use %APPDATA%\RavenLink so config and data
	// live in the same place regardless of how RavenLink was launched
	// (autostart from System32, double-click from Downloads, terminal).
	// On macOS/Linux, honour a CWD config.yaml for development.
	if runtime.GOOS != "windows" {
		if wd, err := os.Getwd(); err == nil {
			if _, err := os.Stat(filepath.Join(wd, "config.yaml")); err == nil {
				return wd, nil
			}
		}
	}

	dir, err := standardAppDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// standardAppDir returns the OS-native "user application support" path.
func standardAppDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "RavenLink"), nil
	case "windows":
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "RavenLink"), nil
		}
		return filepath.Join(home, "AppData", "Roaming", "RavenLink"), nil
	default:
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			return filepath.Join(xdg, "ravenlink"), nil
		}
		return filepath.Join(home, ".config", "ravenlink"), nil
	}
}

// LogPath returns the OS-native log file path.
//
//	macOS:   ~/Library/Logs/RavenLink/ravenlink.log
//	Windows: %LOCALAPPDATA%\RavenLink\ravenlink.log
//	Linux:   $XDG_CACHE_HOME/ravenlink/ravenlink.log or ~/.cache/ravenlink/ravenlink.log
func LogPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	var dir string
	switch runtime.GOOS {
	case "darwin":
		dir = filepath.Join(home, "Library", "Logs", "RavenLink")
	case "windows":
		if appData := os.Getenv("LOCALAPPDATA"); appData != "" {
			dir = filepath.Join(appData, "RavenLink")
		} else {
			dir = filepath.Join(home, "AppData", "Local", "RavenLink")
		}
	default:
		if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
			dir = filepath.Join(xdg, "ravenlink")
		} else {
			dir = filepath.Join(home, ".cache", "ravenlink")
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "ravenlink.log"), nil
}
