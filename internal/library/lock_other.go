//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly || windows)

package library

import (
	"errors"
	"os"
)

var errNoLocking = errors.New("no file locking on this platform")

func lockFile(*os.File) error      { return errNoLocking }
func unlockFile(*os.File) error    { return nil }
func isUnsupported(err error) bool { return errors.Is(err, errNoLocking) }
