//go:build !windows

package shell

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// lockPath is the single-instance lock file: in $XDG_RUNTIME_DIR when set
// (a per-user tmpfs), else in the config directory. Its name carries the
// config directory's hash, like the Windows mutex name.
func lockPath(configDir string) string {
	name := "picmover-app-" + instanceKey(configDir)[:12] + ".lock"
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			return filepath.Join(d, name)
		}
	}
	return filepath.Join(configDir, "picmover-app.lock")
}

// lockInstance takes the single-instance lock (flock, released when the
// process ends however it ends). ErrRunning when another process holds it.
func lockInstance(configDir string) (release func(), err error) {
	p := lockPath(configDir)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrRunning
		}
		return nil, err
	}
	return func() {
		unix.Flock(int(f.Fd()), unix.LOCK_UN)
		f.Close()
	}, nil
}

// allowForeground is a Windows matter.
func allowForeground(pid int) {}
