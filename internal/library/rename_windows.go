//go:build windows

package library

import (
	"os"

	"golang.org/x/sys/windows"
)

// renameNoReplace renames with MoveFileEx without MOVEFILE_REPLACE_EXISTING
// (and without MOVEFILE_COPY_ALLOWED): Windows refuses an existing target
// and a move to another volume.
func renameNoReplace(src, dst string) error {
	from, err := windows.UTF16PtrFromString(src)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: err}
	}
	to, err := windows.UTF16PtrFromString(dst)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: err}
	}
	if err := windows.MoveFileEx(from, to, 0); err != nil {
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: err}
	}
	return nil
}
