package backup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sync"
	"syscall"
	"time"

	"picmover/internal/library"
)

// Progress phases.
const (
	PhasePlan     = "plan"
	PhaseCopy     = "copy"
	PhaseSnapshot = "snapshot"
	PhaseScrub    = "scrub"
	PhasePrune    = "prune"
	PhaseRestore  = "restore"
)

// ProgressInterval is how often progress is reported.
const ProgressInterval = 500 * time.Millisecond

// Progress is reported every ProgressInterval while an operation runs
// and once at its end.
type Progress struct {
	Phase      string `json:"phase"`
	Files      int64  `json:"files"`
	Bytes      int64  `json:"bytes"`
	TotalFiles int64  `json:"totalFiles"`
	TotalBytes int64  `json:"totalBytes"`
	Current    string `json:"current"` // set path of a file being worked on
	Failed     int64  `json:"failed"`
	// Rate is bytes per second since the phase began; ETA the seconds
	// left at that rate (-1 when unknown).
	Rate float64 `json:"rate"`
	ETA  int64   `json:"eta"`
}

// meter keeps the counters of a running operation and reports them.
type meter struct {
	mu      sync.Mutex
	p       Progress
	started time.Time
	fn      func(Progress)
}

func newMeter(fn func(Progress)) *meter { return &meter{fn: fn, started: time.Now()} }

func (m *meter) phase(name string, files, bytes int64) {
	m.mu.Lock()
	m.p = Progress{Phase: name, TotalFiles: files, TotalBytes: bytes, Failed: m.p.Failed}
	m.started = time.Now()
	m.mu.Unlock()
	m.report()
}

func (m *meter) current(rel string) {
	m.mu.Lock()
	m.p.Current = rel
	m.mu.Unlock()
}

func (m *meter) done(files, bytes int64) {
	m.mu.Lock()
	m.p.Files += files
	m.p.Bytes += bytes
	m.mu.Unlock()
}

func (m *meter) failed() {
	m.mu.Lock()
	m.p.Failed++
	m.p.Files++
	m.mu.Unlock()
}

func (m *meter) snapshot() Progress {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.p
	p.ETA = -1
	if el := time.Since(m.started).Seconds(); el > 0.5 && p.Bytes > 0 {
		p.Rate = float64(p.Bytes) / el
		if left := p.TotalBytes - p.Bytes; left >= 0 {
			p.ETA = int64(float64(left) / p.Rate)
		}
	}
	return p
}

func (m *meter) report() {
	if m.fn != nil {
		m.fn(m.snapshot())
	}
}

// run reports every ProgressInterval until the returned stop is called
// (which reports once more).
func (m *meter) run() (stop func()) {
	if m.fn == nil {
		return func() {}
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		t := time.NewTicker(ProgressInterval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				m.report()
			}
		}
	})
	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
			wg.Wait()
			m.report()
		})
	}
}

// Failure is a file an operation could not handle; Error is a fixed text
// (no paths, no OS text), the detail is in the log.
type Failure struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// maxListed caps the lists kept in run stats.
const maxListed = 500

// appendCapped appends s unless the list holds maxListed entries; then
// it sets *truncated.
func appendCapped(list []string, s string, truncated *bool) []string {
	if len(list) >= maxListed {
		*truncated = true
		return list
	}
	return append(list, s)
}

func appendFailure(list []Failure, f Failure, truncated *bool) []Failure {
	if len(list) >= maxListed {
		*truncated = true
		return list
	}
	return append(list, f)
}

// errMissingInLibrary: the library file vanished before it was copied.
var errMissingInLibrary = errors.New("the file is missing in the library")

// PublicError is the client-facing text of a file operation's error.
func PublicError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errMissingInLibrary):
		return "the file is missing in the library"
	case errors.Is(err, library.ErrHashMismatch):
		return "the copy did not match the expected content (the file changed, or the disk returned other bytes)"
	case errors.Is(err, library.ErrSourceChanged):
		return "the file changed while it was copied"
	case errors.Is(err, errOutsideSet):
		return errOutsideSet.Error()
	case errors.Is(err, errOutsideLibrary):
		return errOutsideLibrary.Error()
	case errors.Is(err, ErrUserDBExists), errors.Is(err, ErrNoUserSnapshot):
		return err.Error()
	case errors.Is(err, library.ErrNotRegularFile), errors.Is(err, errNotRegular):
		return "the path is a folder or a link, not a file"
	case errors.Is(err, fs.ErrExist):
		return "a different file is already at this path"
	case errors.Is(err, fs.ErrNotExist):
		return "the file is missing"
	case errors.Is(err, syscall.ENOSPC):
		return "no space left on the device"
	case errors.Is(err, syscall.EROFS):
		return "the destination is on a read-only file system"
	case errors.Is(err, fs.ErrPermission):
		return "permission denied"
	case errors.Is(err, syscall.EIO):
		return "input/output error (the disk may be failing)"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	}
	return "the file could not be copied (see the log)"
}

// NoSpaceError is returned by Run when the copies need more space than
// the target has (without Partial).
type NoSpaceError struct {
	Need, Free int64
}

func (e *NoSpaceError) Error() string {
	return fmt.Sprintf("the backup needs %s but the target has %s free; free some space, use a larger target, "+
		"or back up the newest files only (partial)", humanBytes(e.Need), humanBytes(e.Free))
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 5; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// freeSpace returns the bytes available to this user on dir's volume, -1
// when unknown. Tests replace it.
var freeSpace = diskFree
