//go:build windows

package server

import "golang.org/x/sys/windows"

// diskSpace returns the bytes available to the user on dir's volume and
// its size; -1, -1 when unknown.
func diskSpace(dir string) (free, total int64) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return -1, -1
	}
	var avail, size, all uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &size, &all); err != nil {
		return -1, -1
	}
	return int64(avail), int64(size)
}
