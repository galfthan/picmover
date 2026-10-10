//go:build windows

package backup

import "golang.org/x/sys/windows"

// diskFree returns the bytes available to the user on dir's volume, -1
// when unknown.
func diskFree(dir string) int64 {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return -1
	}
	var avail, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &total, &free); err != nil {
		return -1
	}
	return int64(avail)
}
