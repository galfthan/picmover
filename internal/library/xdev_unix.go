//go:build !windows

package library

import (
	"errors"
	"syscall"
)

// IsCrossDevice reports whether err from os.Rename means source and
// destination are on different file systems (EXDEV).
func IsCrossDevice(err error) bool {
	return errors.Is(err, syscall.EXDEV)
}
