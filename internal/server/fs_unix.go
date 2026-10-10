//go:build !windows

package server

import (
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
)

// roots returns the directory browser's starting points: /, the home
// directory, /media/<user>, /mnt and /Volumes when they exist.
func roots() []dirJSON {
	home, _ := os.UserHomeDir()
	media := ""
	if u, err := user.Current(); err == nil && u.Username != "" {
		media = filepath.Join("/media", u.Username)
	}
	return existingDirs("/", home, media, "/mnt", "/Volumes")
}

// isHidden reports hidden entries beyond dot names; none on Unix.
func isHidden(fs.DirEntry, string) bool { return false }

// osMissingErrnos: no errors beyond the common ones (isMissing).
var osMissingErrnos []error

// evalSymlinksGaps: filepath.EvalSymlinks works on every Unix volume, so
// a failure to resolve refuses the file.
const evalSymlinksGaps = false
