//go:build linux

package library

import (
	"os"

	"golang.org/x/sys/unix"
)

// dropCache asks the kernel to drop f's cached pages (best effort), so
// the next read comes from the disk. The file must have been fsynced:
// only clean pages are dropped.
func dropCache(f *os.File) {
	_ = unix.Fadvise(int(f.Fd()), 0, 0, unix.FADV_DONTNEED)
}
