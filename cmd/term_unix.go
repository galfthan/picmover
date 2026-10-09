//go:build unix

package cmd

import (
	"os"

	"golang.org/x/sys/unix"
)

// termWidth returns the width in columns of the terminal f, or 80.
func termWidth(f *os.File) int {
	ws, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 {
		return defaultTermWidth
	}
	return int(ws.Col)
}

// enableVT reports whether f understands ANSI escape sequences.
func enableVT(*os.File) bool { return true }
