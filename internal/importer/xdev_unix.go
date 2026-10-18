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

// sameDevice reports whether the file src and the directory dir are on the
// same file system (st_dev). On any error it reports true: the caller then
// tries a rename and copies on EXDEV.
func sameDevice(src, dir string) bool {
	var a, b syscall.Stat_t
	if syscall.Stat(src, &a) != nil || syscall.Stat(dir, &b) != nil {
		return true
	}
	return a.Dev == b.Dev
}

// fileDev returns the file system device (st_dev) of fi.
func fileDev(fi os.FileInfo) (uint64, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Dev), true //nolint:unconvert // int32 on darwin
}
