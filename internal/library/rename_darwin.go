//go:build darwin

package library

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// renameNoReplace renames with renamex_np(RENAME_EXCL): the kernel refuses
// an existing target atomically. File systems without the flag fall back
// to link and unlink.
func renameNoReplace(src, dst string) error {
	err := unix.RenamexNp(src, dst, unix.RENAME_EXCL)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, unix.EINVAL), errors.Is(err, unix.ENOTSUP), errors.Is(err, unix.ENOSYS):
		return linkRename(src, dst)
	}
	return &os.LinkError{Op: "rename", Old: src, New: dst, Err: err}
}
