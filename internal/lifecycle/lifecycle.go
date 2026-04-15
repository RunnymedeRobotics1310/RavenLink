// Package lifecycle provides cross-platform self-restart (re-exec)
// and helpers for opening the default web browser.
package lifecycle

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
)

// RestartSelf replaces the current process with a fresh invocation of
// the same executable, with the same arguments and environment.
//
// On Unix, uses syscall.Exec (actual process replacement — the PID
// stays the same, open file descriptors are closed via O_CLOEXEC).
// On Windows, syscall.Exec is not supported, so we spawn a new
// detached process and exit the current one.
//
// Callers should invoke this AFTER any in-flight work is complete
// (e.g., after graceful shutdown of subsystems). On Unix this is
// strictly required because the current process image is replaced —
// no deferred functions run.
func RestartSelf() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not resolve executable path: %w", err)
	}

	slog.Info("self-restart requested", "exe", exe, "args", os.Args)

	if runtime.GOOS == "windows" {
		cmd := exec.Command(exe, os.Args[1:]...)
		cmd.Env = os.Environ()
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("failed to spawn replacement process: %w", err)
		}
		// Detach so the child outlives us.
		_ = cmd.Process.Release()
		os.Exit(0)
		return nil
	}
	return execSelf(exe, os.Args, os.Environ())
}

// OpenBrowser opens the given URL in the user's default browser.
// Non-blocking. Errors are logged but not returned — browser launch
// is best-effort.
func OpenBrowser(url string) {
	openDefault(url)
}

// OpenFile opens a local file with the OS default handler for its
// extension. For .wpilog this is typically AdvantageScope.
func OpenFile(path string) {
	openDefault(path)
}

func openDefault(target string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		// cmd /c start is more reliable than rundll32 for local files.
		cmd = exec.Command("cmd", "/c", "start", "", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	if err := cmd.Start(); err != nil {
		slog.Warn("could not open", "target", target, "err", err)
		return
	}
	// Reap the child so it doesn't become a zombie.
	go func() { _ = cmd.Wait() }()
}
