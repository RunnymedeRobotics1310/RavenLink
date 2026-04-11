//go:build !windows

package lifecycle

import "syscall"

// execSelf replaces the current process with a new one via the
// POSIX exec system call. Does not return on success.
func execSelf(exe string, argv []string, env []string) error {
	return syscall.Exec(exe, argv, env)
}
