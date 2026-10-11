package shell

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// LogName is the app's log file in the config directory.
const LogName = "picmover-app.log"

// Log rotation: at logMaxBytes the file is renamed to .1 (.1 to .2), so
// at most logKeep old files are kept.
const (
	logMaxBytes = 5 << 20
	logKeep     = 2
)

// rotatingLog is an io.Writer appending to a file that is rotated when it
// would exceed max bytes. Safe for concurrent use.
type rotatingLog struct {
	mu   sync.Mutex
	path string
	max  int64
	keep int
	f    *os.File
	size int64
}

// openLog opens (appends to) path, rotating first when it is full.
func openLog(path string, max int64, keep int) (*rotatingLog, error) {
	l := &rotatingLog{path: path, max: max, keep: keep}
	if err := l.open(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *rotatingLog) open() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	l.f, l.size = f, fi.Size()
	return nil
}

// rotateLocked renames path.(i) to path.(i+1), path to path.1 and opens
// a new path.
func (l *rotatingLog) rotateLocked() error {
	if l.f != nil {
		l.f.Close()
		l.f = nil
	}
	if l.keep > 0 {
		os.Remove(fmt.Sprintf("%s.%d", l.path, l.keep))
		for i := l.keep - 1; i >= 1; i-- {
			os.Rename(fmt.Sprintf("%s.%d", l.path, i), fmt.Sprintf("%s.%d", l.path, i+1))
		}
		os.Rename(l.path, l.path+".1")
	} else {
		os.Remove(l.path)
	}
	return l.open()
}

func (l *rotatingLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		if err := l.open(); err != nil {
			return 0, err
		}
	}
	if l.size > 0 && l.size+int64(len(p)) > l.max {
		if err := l.rotateLocked(); err != nil {
			return 0, err
		}
	}
	n, err := l.f.Write(p)
	l.size += int64(n)
	return n, err
}

// Close closes the file.
func (l *rotatingLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// logPath is the log file of the config directory dir.
func logPath(dir string) string { return filepath.Join(dir, LogName) }
