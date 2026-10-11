//go:build windows

package shell

import "golang.org/x/sys/windows"

// The Win32 DLLs the shell calls into (loaded from the system directory).
var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
)
