//go:build windows

package server

import (
	"fmt"
	"path/filepath"
	"runtime"

	"golang.org/x/sys/windows"
)

// sysOpener opens files with ShellExecute's "open" verb, the default
// program of the file type. No command interpreter is involved (cmd /c
// start would parse & and ^ in file names).
type sysOpener struct{}

func newSysOpener() opener { return sysOpener{} }

func (sysOpener) available() bool { return true }

func (sysOpener) open(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("open %q: not an absolute path", path)
	}
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	dir, err := windows.UTF16PtrFromString(filepath.Dir(path))
	if err != nil {
		return err
	}
	errc := make(chan error, 1)
	go func() {
		// Shell extensions may use COM: run on a thread of its own with
		// COM initialised, as ShellExecute's documentation asks.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE); err == nil {
			defer windows.CoUninitialize()
		}
		errc <- windows.ShellExecute(0, verb, file, nil, dir, windows.SW_SHOWNORMAL)
	}()
	return <-errc
}
