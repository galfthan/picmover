//go:build !windows

package importer

import (
	"errors"
	"os"
	"syscall"
)

// isCrossDevice reports whether err from os.Rename means source and
// destination are on different file systems.
func isCrossDevice(err error) bool {
	return errors.Is(err, syscall.EXDEV)
}

// syncDir flushes directory metadata so a completed rename survives a crash.
// Best effort: errors are ignored.
func syncDir(dir string) {
	f, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = f.Sync()
	_ = f.Close()
}
