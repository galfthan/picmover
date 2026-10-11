//go:build !windows

package server

import (
	"errors"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
)

// roots returns the directory browser's starting points: /, the home
// directory, /media/<user>, /mnt, /Volumes and the gvfs mount directory
// (phones over MTP, see gvfsDir) when they exist.
func roots() []dirJSON {
	home, _ := os.UserHomeDir()
	media := ""
	if u, err := user.Current(); err == nil && u.Username != "" {
		media = filepath.Join("/media", u.Username)
	}
	return existingDirs("/", home, media, "/mnt", "/Volumes", gvfsDir())
}

// gvfsDir returns where gvfs mounts phones and cameras (MTP, gphoto2):
// $XDG_RUNTIME_DIR/gvfs, else /run/user/<uid>/gvfs.
func gvfsDir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "gvfs")
	}
	return filepath.Join("/run/user", strconv.Itoa(os.Getuid()), "gvfs")
}

// isHidden reports hidden entries beyond dot names; none on Unix.
func isHidden(fs.DirEntry, string) bool { return false }

// osMissingErrnos: no errors beyond the common ones (isMissing).
var osMissingErrnos []error

// evalSymlinksGaps: filepath.EvalSymlinks works on every Unix volume, so
// a failure to resolve refuses the file.
const evalSymlinksGaps = false

// isBusyErr reports a file or mount point in use.
func isBusyErr(err error) bool {
	return errors.Is(err, syscall.EBUSY) || errors.Is(err, syscall.ETXTBSY)
}
