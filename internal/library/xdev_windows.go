//go:build windows

package library

import (
	"errors"

	"golang.org/x/sys/windows"
)

// IsCrossDevice reports whether err from os.Rename means source and
// destination are on different volumes (ERROR_NOT_SAME_DEVICE).
func IsCrossDevice(err error) bool {
	return errors.Is(err, windows.ERROR_NOT_SAME_DEVICE)
}
