//go:build !linux && !darwin && !freebsd && !windows

package transfer

// diskFree is unknown on this platform.
func diskFree(string) int64 { return -1 }
