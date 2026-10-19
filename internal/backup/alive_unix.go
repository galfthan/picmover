//go:build unix

package backup

import (
	"errors"
	"syscall"
)

// processAlive reports whether a process with pid exists (signal 0; a
// process of another user answers EPERM, and is alive).
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
