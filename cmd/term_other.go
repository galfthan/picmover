//go:build !unix && !windows

package cmd

import "os"

func termWidth(*os.File) int { return defaultTermWidth }

func enableVT(*os.File) bool { return false }
