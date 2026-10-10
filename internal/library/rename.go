package library

import (
	"io/fs"
	"os"
)

// RenameNoReplace renames src to dst and never replaces an existing dst:
// the error then wraps fs.ErrExist. It uses the OS's atomic form
// (renameat2 RENAME_NOREPLACE on Linux, renamex_np RENAME_EXCL on macOS,
// link and unlink on other Unix systems, MoveFileEx without
// MOVEFILE_REPLACE_EXISTING on Windows). Only on a file system that has
// neither the flag nor hard links does it fall back to a check before the
// rename. A cross-device move fails as with os.Rename (IsCrossDevice).
func RenameNoReplace(src, dst string) error { return renameNoReplace(src, dst) }

// checkedRename is the fallback: refuse an existing dst, then rename.
// Another process can still create dst in between.
func checkedRename(src, dst string) error {
	if _, err := os.Lstat(dst); err == nil {
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: fs.ErrExist}
	}
	return os.Rename(src, dst)
}
