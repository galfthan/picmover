package library

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// LockName is the lock file inside MetaDir. Like everything under
// MetaDir it is internal (IsInternal) and never imported as media.
const LockName = "lock"

// ErrLockUnsupported is wrapped by the error Lock returns when the file
// system cannot lock files (some network and 9p mounts). Lock then still
// returns a usable (no-op) unlock function: callers warn and go on
// without the lock rather than refusing to work.
var ErrLockUnsupported = errors.New("file locking is not supported on this file system")

// ErrLocked is returned by Lock when another process (or another Lock in
// this process) holds the library lock. The fields describe the holder as
// it recorded itself in the lock file; they are zero when it could not be
// read (for example while the holder is still writing it).
type ErrLocked struct {
	Path  string    // the lock file
	PID   int       // 0 when unknown
	Cmd   string    // the command holding the lock, e.g. "serve"
	Since time.Time // zero when unknown
}

func (e *ErrLocked) Error() string {
	var parts []string
	if e.PID > 0 {
		parts = append(parts, "pid "+strconv.Itoa(e.PID))
	}
	if e.Cmd != "" {
		parts = append(parts, e.Cmd)
	}
	if !e.Since.IsZero() {
		parts = append(parts, "since "+e.Since.Local().Format(time.DateTime))
	}
	msg := "library is in use by another picmover process"
	if len(parts) > 0 {
		msg += " (" + strings.Join(parts, ", ") + ")"
	}
	return msg
}

// LockPath returns the path of the library lock file.
func (l *Library) LockPath() string { return filepath.Join(l.root, MetaDir, LockName) }

// Lock takes the library's advisory, exclusive lock for a process that
// writes the library (serve, import, rebuild, thumbs, update-metadata,
// verify --rehash/--fill-size). The lock is an OS lock (flock on Unix,
// LockFileEx on Windows) on the held-open file <root>/.picmover/lock, so
// it is released when unlock is called or the process ends, however it
// ends: there are no stale locks. It does not block: when the lock is
// held, the error is an *ErrLocked naming the holder, which writes
// "pid=<pid> cmd=<name> since=<RFC3339>" into the file.
//
// When the file system does not support locking, Lock returns a no-op
// unlock and an error wrapping ErrLockUnsupported; the caller should warn
// and proceed. MetaDir is created when missing.
func (l *Library) Lock(ctx context.Context, name string) (unlock func(), err error) {
	_ = ctx // Lock never blocks; ctx is for future waiting variants
	if err := os.MkdirAll(filepath.Join(l.root, MetaDir), 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", filepath.Join(l.root, MetaDir), err)
	}
	p := l.LockPath()
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	switch err := lockFile(f); {
	case err == nil:
	case errors.Is(err, errWouldBlock):
		f.Close()
		return nil, readHolder(p)
	case isUnsupported(err):
		f.Close()
		return func() {}, fmt.Errorf("lock %s: %w (%v)", p, ErrLockUnsupported, err)
	default:
		f.Close()
		return nil, fmt.Errorf("lock %s: %w", p, err)
	}
	info := fmt.Sprintf("pid=%d cmd=%s since=%s\n", os.Getpid(), strings.ReplaceAll(name, " ", "_"),
		time.Now().Format(time.RFC3339))
	// The holder info is best effort: the lock itself is what counts.
	if f.Truncate(0) == nil {
		_, _ = f.WriteAt([]byte(info), 0)
	}
	var done bool
	return func() {
		if done {
			return
		}
		done = true
		_ = f.Truncate(0)
		_ = unlockFile(f)
		_ = f.Close()
	}, nil
}

// errWouldBlock is returned by lockFile when another holder has the lock.
var errWouldBlock = errors.New("lock held")

// readHolder returns an *ErrLocked describing the holder recorded in the
// lock file at p.
func readHolder(p string) *ErrLocked {
	e := &ErrLocked{Path: p}
	b, err := os.ReadFile(p)
	if err != nil {
		return e
	}
	for _, field := range strings.Fields(string(b)) {
		k, v, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		switch k {
		case "pid":
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				e.PID = n
			}
		case "cmd":
			e.Cmd = v
		case "since":
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				e.Since = t
			}
		}
	}
	return e
}
