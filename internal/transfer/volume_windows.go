//go:build windows

package transfer

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// sameVolume reports whether the directories a and b are on one volume,
// by volume serial number, else by volume name (drive letter, UNC
// share); when neither works it reports true, and the move tries a
// rename and copies on ERROR_NOT_SAME_DEVICE (as internal/importer's
// sameDevice).
func sameVolume(a, b string) bool {
	sa, errA := volumeSerial(a)
	sb, errB := volumeSerial(b)
	if errA == nil && errB == nil {
		return sa == sb
	}
	va, vb := filepath.VolumeName(a), filepath.VolumeName(b)
	if va == "" || vb == "" {
		return true
	}
	return strings.EqualFold(va, vb)
}

func volumeSerial(p string) (uint32, error) {
	f, err := os.Open(p)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var fi windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &fi); err != nil {
		return 0, err
	}
	return fi.VolumeSerialNumber, nil
}

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
