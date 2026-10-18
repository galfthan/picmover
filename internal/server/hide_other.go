//go:build !windows

package server

import "os/exec"

// hideWindow keeps cmd from opening a console window (Windows only).
func hideWindow(*exec.Cmd) {}
