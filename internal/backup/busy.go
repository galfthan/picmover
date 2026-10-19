package backup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"picmover/internal/library"
	"picmover/internal/userdata"
)

// backup.db keeps journal_mode DELETE (see setDSN): a writer's commit
// needs every reader gone, so a reader that holds its read lock longer
// than busy_timeout (a Backups page computing a plan over a slow USB
// drive, a file manager indexing the drive, another picmover process)
// makes the commit fail with SQLITE_BUSY. A run never fails for that:
// each write of the set goes through retryBusy, which tries again with
// backoff for busyBudget, logging a warning while it waits.

// busyBudget is how long a write of the set keeps trying while backup.db
// is busy (each attempt already waits busy_timeout, 10 s). Tests shorten
// it.
var busyBudget = 10 * time.Minute

// busyBackoffMax caps the pause between two attempts.
var busyBackoffMax = 5 * time.Second

// cancelGrace is the time a write still gets when its run was cancelled
// before it began: the rows of the files a cancelled run copied are
// still worth writing.
var cancelGrace = 30 * time.Second

// finishBudget bounds the retries of the runs row written at the end of
// a run (the run may already have used busyBudget).
var finishBudget = 2 * time.Minute

// IsBusy reports an SQLITE_BUSY (or SQLITE_LOCKED) error: another
// connection holds a lock on the database.
func IsBusy(err error) bool {
	if err == nil {
		return false
	}
	var ce interface{ Code() int }
	if errors.As(err, &ce) {
		c := ce.Code() & 0xff
		return c == 5 || c == 6 // SQLITE_BUSY, SQLITE_LOCKED
	}
	return strings.Contains(err.Error(), "database is locked")
}

// setBusyError is a write of the set (backup.db on the target) that
// gave up because the database stayed busy: RunError names it.
type setBusyError struct{ err error }

func (e *setBusyError) Error() string { return e.err.Error() }
func (e *setBusyError) Unwrap() error { return e.err }

// retryBusy runs fn until it succeeds, fails with anything but a busy
// database, or budget has passed; it waits between attempts (100 ms,
// doubling up to busyBackoffMax). Once ctx is done (also before the
// call) at most cancelGrace is left: the rows of files that are done are
// still worth writing, but a cancel must not wait the whole budget. fn
// gets a context that is not cancelled with ctx: a write that has
// started finishes. what names the write in the warnings. A busy error
// it gives up on is a *setBusyError.
func (s *Set) retryBusy(ctx context.Context, budget time.Duration, what string, fn func(context.Context) error) error {
	run := context.WithoutCancel(ctx)
	start := time.Now()
	deadline := start.Add(budget)
	done := ctx.Done()
	graced := false
	grace := func() {
		if !graced {
			graced, done = true, nil
			deadline = minTime(deadline, time.Now().Add(cancelGrace))
		}
	}
	if ctx.Err() != nil {
		grace()
	}
	wait := 100 * time.Millisecond
	var lastWarn time.Time
	for attempt := 1; ; attempt++ {
		err := fn(run)
		if err == nil {
			if attempt > 1 {
				s.logger().Info("backup.db is free again; written", "what", what, "waited", time.Since(start).Round(time.Second).String())
			}
			return nil
		}
		if !IsBusy(err) {
			return err
		}
		if !time.Now().Before(deadline) {
			return &setBusyError{fmt.Errorf("%w (gave up after %s of retries)", err, time.Since(start).Round(time.Second))}
		}
		if lastWarn.IsZero() || time.Since(lastWarn) >= time.Minute {
			s.logger().Warn("backup.db on the target is busy (another program or page is reading it); waiting to write",
				"what", what, "waited", time.Since(start).Round(time.Second).String(), "gives_up_after", budget.String())
			lastWarn = time.Now()
		}
		t := time.NewTimer(min(wait, max(time.Until(deadline), time.Millisecond)))
		select {
		case <-done:
			t.Stop()
			grace() // cancelled while waiting: cancelGrace more, then give up
		case <-t.C:
		}
		wait = min(wait*2, busyBackoffMax)
	}
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

// logger is the run's logger, or the default one outside a run.
func (s *Set) logger() *slog.Logger {
	if s.log != nil {
		return s.log
	}
	return slog.Default()
}

// RunError is the client-facing text of the error that stopped a run (no
// paths): "" for nil and for a cancelled run. The text names the cause:
// a busy backup.db, a target that went away, a full or failing disk.
func RunError(err error) string {
	if msg := KnownRunError(err); msg != "" {
		return msg
	}
	if err == nil || errors.Is(err, context.Canceled) {
		return ""
	}
	return "an unexpected error stopped the run (the log has the details)"
}

// KnownRunError is RunError for the causes it can name, "" otherwise.
func KnownRunError(err error) string {
	var ns *NoSpaceError
	switch {
	case err == nil, errors.Is(err, context.Canceled):
		return ""
	case errors.As(err, new(*setBusyError)):
		return "the backup database on the target stayed busy (another program or page kept it open for reading); " +
			"the files copied so far are kept, and Back up now continues from there"
	case IsBusy(err):
		// Another database (the library's user.db or media.db, or a
		// read of backup.db that is not retried).
		return "a database stayed busy (another program is using it); try again in a moment"
	case errors.Is(err, errTargetGone):
		return errTargetGone.Error()
	case errors.As(err, &ns):
		return ns.Error()
	case errors.Is(err, syscall.ENOSPC):
		return "the target is full"
	case errors.Is(err, syscall.EDQUOT):
		return "the disk quota on the target is exceeded"
	case errors.Is(err, syscall.EROFS):
		return "the target is on a read-only file system"
	case errors.Is(err, syscall.EIO):
		return "the disk reported an input/output error (it may be failing or was disconnected)"
	case errors.Is(err, errNotRealDir):
		return errNotRealDir.Error()
	case errors.Is(err, fs.ErrPermission):
		return "permission denied on the target"
	}
	return ""
}

// SetHeld reports whether a backup, scrub, prune or restore (of this
// process or another, on any host) holds the lock of the set called name
// on t, and names the holder from the lock file's record. It probes the
// OS lock through a read-only handle, released at once, and writes
// nothing; only where that cannot tell does it go by the record (a live
// process on this host; a record from another host counts as held). The
// Backups page then does not plan:
// a plan reads every row of backup.db, which would make the run's
// commits wait.
func SetHeld(t *Target, name string) (*library.ErrLocked, bool) {
	if !ValidSetName(name) {
		return nil, false
	}
	p := filepath.Join(t.Root, name, library.MetaDir, lockName)
	// The OS lock tells a live holder from a record that a crashed or
	// unplugged holder, or one on another host, left behind (probed
	// read-only: nothing is written, also on a write-protected target).
	if held, known := library.ProbeLock(p); known {
		if !held {
			return nil, false
		}
		h, ok := library.PeekHolder(p)
		if !ok {
			h = &library.ErrLocked{Path: p}
		}
		h.What = "backup set"
		return h, true
	}
	// The file system cannot lock (or not through a read-only handle):
	// the record, checked for a live process on this host.
	h, ok := library.PeekHolder(p)
	if !ok || h.Cmd == "check" {
		return nil, false
	}
	if host, _ := os.Hostname(); h.PID > 0 && (h.Host == "" || h.Host == host) && !processAlive(h.PID) {
		return nil, false
	}
	h.What = "backup set"
	return h, true
}

// Attempt is the user.db record (userdata.BackupAttempt) of a backup run
// that ended with st and err: kept in the library, so a run whose end
// backup.db on the target could not record is still known, and the
// Libraries page can tell an incomplete backup from none. It reads the
// set's counts (one short read).
func Attempt(ctx context.Context, set *Set, st *Stats, err error) userdata.BackupAttempt {
	a := userdata.BackupAttempt{At: time.Now(), SetFiles: -1, SetBytes: -1}
	if st != nil {
		a.LibraryFiles = st.Current + st.New + st.Changed + st.Moved
	}
	switch {
	case errors.Is(err, context.Canceled):
		a.Cancelled = true
	case err != nil:
		a.Error = RunError(err)
	}
	if set != nil {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if sum, err := set.Summary(sctx); err == nil {
			a.SetFiles, a.SetBytes = sum.Files, sum.Bytes
		}
	}
	return a
}

// SetBusyLimitsForTest sets backup.db's busy_timeout and the retry
// budgets (busyBudget, finishBudget, cancelGrace) for the tests of other
// packages, and returns a function that restores them. Not for
// production use; sets opened before keep their busy_timeout.
func SetBusyLimitsForTest(timeout, budget time.Duration) (restore func()) {
	oldTimeout, oldBudget, oldBackoff, oldFinish, oldGrace := setBusyTimeout, busyBudget, busyBackoffMax, finishBudget, cancelGrace
	setBusyTimeout, busyBudget, busyBackoffMax, finishBudget, cancelGrace = timeout, budget, timeout, budget, budget
	return func() {
		setBusyTimeout, busyBudget, busyBackoffMax, finishBudget, cancelGrace = oldTimeout, oldBudget, oldBackoff, oldFinish, oldGrace
	}
}

// RecordsAttempt reports whether a backup run that ended with err is
// recorded as an attempt (Attempt): not one refused because another
// backup, scrub, prune or restore holds the set, which did nothing and
// says nothing about the backup on the target.
func RecordsAttempt(err error) bool {
	var le *library.ErrLocked
	return !errors.As(err, &le)
}
