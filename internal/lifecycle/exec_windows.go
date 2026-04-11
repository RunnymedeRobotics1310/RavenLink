//go:build windows

package lifecycle

import "errors"

// execSelf is unreachable on Windows (callers take the cmd.Start path
// in RestartSelf), but the symbol must exist for cross-platform builds.
func execSelf(exe string, argv []string, env []string) error {
	return errors.New("execSelf not supported on Windows; use Start + Exit")
}
