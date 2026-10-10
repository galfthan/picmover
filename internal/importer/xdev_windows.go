//go:build windows

package importer

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isCrossDevice reports whether err from os.Rename means source and
// destination are on different volumes.
func isCrossDevice(err error) bool {
	return errors.Is(err, windows.ERROR_NOT_SAME_DEVICE)
}

// syncDir is a no-op on Windows: NTFS journals metadata and directories
// cannot be opened for FlushFileBuffers without special flags.
func syncDir(string) {}
