//go:build !windows

package server

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
)

// sysOpener opens files with xdg-open (Linux, BSD) or open (macOS). The
// path is one argument, never through a shell; it is absolute, so it
// cannot be read as an option.
type sysOpener struct {
	once sync.Once
	cmd  string // "" when there is none
}

func newSysOpener() opener { return &sysOpener{} }

func (o *sysOpener) find() {
	o.once.Do(func() {
		name := "xdg-open"
		if runtime.GOOS == "darwin" {
			name = "open"
		}
		if p, err := exec.LookPath(name); err == nil {
			o.cmd = p
		}
	})
}

func (o *sysOpener) available() bool {
	o.find()
	return o.cmd != ""
}

func (o *sysOpener) open(path string) error {
	o.find()
	if o.cmd == "" {
		return fmt.Errorf("no opener")
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("open %q: not an absolute path", path)
	}
	cmd := exec.Command(o.cmd, path)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() // reap; xdg-open and open return quickly
	return nil
}
