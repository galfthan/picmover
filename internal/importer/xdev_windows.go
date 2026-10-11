//go:build windows

package importer

import (
	"errors"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// isCrossDevice reports whether err from os.Rename means source and
// destination are on different volumes.
func isCrossDevice(err error) bool {
	return errors.Is(err, windows.ERROR_NOT_SAME_DEVICE)
}

// syncDir is a no-op on Windows: NTFS journals metadata and directories
// cannot be opened for FlushFileBuffers without special flags.
func syncDir(string) {}

// sameDevice reports whether the file src and the directory dir are on the
// same volume, by volume serial number. When a serial cannot be read the
// volume names (drive letters, UNC shares) are compared case-insensitively;
// when neither works it reports true, and the caller tries a rename and
// copies on ERROR_NOT_SAME_DEVICE.
func sameDevice(src, dir string) bool {
	a, errA := volumeSerial(src)
	b, errB := volumeSerial(dir)
	if errA == nil && errB == nil {
		return a == b
	}
	va, vb := filepath.VolumeName(src), filepath.VolumeName(dir)
	if va == "" || vb == "" {
		return true
	}
	return strings.EqualFold(va, vb)
}

// volumeSerial returns the serial number of the volume holding p, a file
// or a directory (opened with FILE_FLAG_BACKUP_SEMANTICS).
func volumeSerial(p string) (uint32, error) {
	name, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return 0, err
	}
	h, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(h)
	var fi windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &fi); err != nil {
		return 0, err
	}
	return fi.VolumeSerialNumber, nil
}
