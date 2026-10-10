//go:build windows

package server

import (
	"errors"
	"io/fs"
	"syscall"

	"golang.org/x/sys/windows"
)

// osMissingErrnos are Windows errors for a name the OS rejects
// (isMissing answers 404 for them).
var osMissingErrnos = []error{windows.ERROR_INVALID_NAME, windows.ERROR_DIRECTORY}

// evalSymlinksGaps: filepath.EvalSymlinks fails on some Windows volumes
// (RAM disks, some network file systems); openOriginal then falls back to
// the lexical checks, like library.Open does for the root.
const evalSymlinksGaps = true

// roots returns every logical drive, e.g. C:\, D:\. Drives are not
// stat'ed: a disconnected network drive can take seconds to answer, and an
// empty card reader only fails when it is opened.
func roots() []dirJSON {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return nil
	}
	var out []dirJSON
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) != 0 {
			d := string(rune('A'+i)) + `:\`
			out = append(out, dirJSON{Name: d, Path: d})
		}
	}
	return out
}

// isHidden reports whether the entry has the Windows hidden attribute,
// read from the directory listing's own data (FindNextFile already
// returned the attributes) and only when that is unavailable with a
// separate GetFileAttributes call on path.
func isHidden(e fs.DirEntry, path string) bool {
	if info, err := e.Info(); err == nil {
		if d, ok := info.Sys().(*syscall.Win32FileAttributeData); ok && d != nil {
			return d.FileAttributes&windows.FILE_ATTRIBUTE_HIDDEN != 0
		}
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	attrs, err := windows.GetFileAttributes(p)
	return err == nil && attrs&windows.FILE_ATTRIBUTE_HIDDEN != 0
}

// isBusyErr reports a file another program holds open.
func isBusyErr(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}
