//go:build !linux && !darwin && !freebsd && !windows

package server

// diskSpace is unknown on this platform.
func diskSpace(string) (free, total int64) { return -1, -1 }
