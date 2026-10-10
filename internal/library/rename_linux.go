//go:build linux

package library

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// renameNoReplace renames with renameat2(RENAME_NOREPLACE): the kernel
// refuses an existing target atomically. File systems without the flag
// fall back to link and unlink.
func renameNoReplace(src, dst string) error {
	err := unix.Renameat2(unix.AT_FDCWD, src, unix.AT_FDCWD, dst, unix.RENAME_NOREPLACE)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, unix.EINVAL), errors.Is(err, unix.ENOSYS), errors.Is(err, unix.EOPNOTSUPP):
		return linkRename(src, dst)
	}
	return &os.LinkError{Op: "rename", Old: src, New: dst, Err: err}
}
