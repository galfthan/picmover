//go:build !windows

package library

import "os"

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
