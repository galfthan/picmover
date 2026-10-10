//go:build windows

package cmd

import (
	"os"

	"golang.org/x/sys/windows"
)

// termWidth returns the width in columns of the console window f, or 80.
func termWidth(f *os.File) int {
	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(windows.Handle(f.Fd()), &info); err != nil {
		return defaultTermWidth
	}
	if w := int(info.Window.Right-info.Window.Left) + 1; w > 0 {
		return w
	}
	return defaultTermWidth
}

// enableVT turns on ANSI escape processing for the console f and reports
// whether it is on.
func enableVT(f *os.File) bool {
	h := windows.Handle(f.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return false
	}
	if mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
		return true
	}
	return windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}
