//go:build windows

package server

import (
	"os/exec"
	"syscall"
)

// createNoWindow is CREATE_NO_WINDOW: a console program started by the
// desktop app (a GUI program) gets no console window.
const createNoWindow = 0x08000000

// hideWindow keeps cmd from opening a console window.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}
