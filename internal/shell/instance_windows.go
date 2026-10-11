//go:build windows

package shell

import (
	"errors"

	"golang.org/x/sys/windows"
)

// lockInstance creates the named mutex Local\picmover-app-<sha1 of the
// config dir>; ErrRunning when it already exists (another instance in
// this session). The handle is closed on release or when the process
// ends.
func lockInstance(configDir string) (release func(), err error) {
	name, err := windows.UTF16PtrFromString(`Local\picmover-app-` + instanceKey(configDir))
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		if h != 0 {
			windows.CloseHandle(h)
		}
		return nil, ErrRunning
	}
	if err != nil {
		return nil, err
	}
	return func() { windows.CloseHandle(h) }, nil
}

var procAllowSetForegroundWindow = user32.NewProc("AllowSetForegroundWindow")

// allowForeground lets the first instance (pid) bring its window to the
// front: the second instance was started by the user, so it may pass on
// its foreground right.
func allowForeground(pid int) {
	if pid > 0 {
		procAllowSetForegroundWindow.Call(uintptr(pid))
	}
}

// isSharingViolation reports the errors a rename over a file gives while
// another process (an antivirus, the indexer) holds it open.
func isSharingViolation(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_LOCK_VIOLATION) ||
		errors.Is(err, windows.ERROR_ACCESS_DENIED)
}
