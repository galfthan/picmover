//go:build unix

package library

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

// linkRename moves src to dst with link(2) and unlink(2): link never
// replaces an existing dst. A file system without hard links (FAT, exFAT,
// some network shares) falls back to checkedRename, which only narrows the
// race.
func linkRename(src, dst string) error {
	if err := os.Link(src, dst); err != nil {
		if errors.Is(err, fs.ErrExist) || IsCrossDevice(err) || errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EOPNOTSUPP) ||
			errors.Is(err, syscall.EMLINK) || errors.Is(err, syscall.ENOSYS) {
			return checkedRename(src, dst)
		}
		return err
	}
	if err := os.Remove(src); err != nil {
		// Leave things as they were: dst is only a second name of src.
		_ = os.Remove(dst)
		return err
	}
	return nil
}
