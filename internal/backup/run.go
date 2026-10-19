package backup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"picmover/internal/library"
)

// Workers limits.
const (
	DefaultWorkers = 2
	MaxWorkers     = 16
)

// rowBatch is the number of verified files whose rows are written in one
// transaction: an interruption loses at most this many rows, and the
// next run re-verifies those files by hash instead of copying them.
const rowBatch = 64

// rowFlushInterval writes a partial batch when copies are slow.
const rowFlushInterval = 5 * time.Second

// Options configure Run.
type Options struct {
	// Owner is the library's identity (userdata Identity); the set must
	// belong to it or have no owner yet, and must have been last written
	// by this instance of the library unless TakeOver (see Set.Claim).
	Owner    Identity
	TakeOver bool
	// Library is recorded in the runs row (the source root, for reports).
	Library string
	Workers int  // parallel copies; <=0: DefaultWorkers; at most MaxWorkers
	Partial bool // when the target is too small, copy newest first until it is full
	// OnProgress is called every ProgressInterval and at the end.
	OnProgress func(Progress)
	// OnFailure is called for every file that could not be backed up.
	OnFailure func(Failure)
	// Logger receives the run's log besides the set's own log; nil:
	// only the set's log.
	Logger *slog.Logger
}

// Stats are the counters of a backup run (the runs row's stats and the
// job's result). The client relies on copied, moved, changed, bytes and
// failed ([{path, error}]); lists hold at most maxListed entries, with
// truncated set when one was capped.
type Stats struct {
	// The plan.
	Current int64 `json:"current"`
	Moved   int64 `json:"-"` // planned moves; Renamed is what was done
	Changed int64 `json:"changed"`
	New     int64 `json:"new"`
	Extra   int64 `json:"extra"`
	// What was done.
	Copied      int64 `json:"copied"`    // new and changed files copied and verified
	Renamed     int64 `json:"moved"`     // moved files renamed on the target
	Adopted     int64 `json:"adopted"`   // files already on the target (an interrupted run), verified by hash
	Refreshed   int64 `json:"refreshed"` // non-media files whose mtime changed but not their content
	FailedCount int64 `json:"failedCount"`
	BytesCopied int64 `json:"bytes"`
	// Partial: what did not fit.
	Partial   bool  `json:"partial"`
	LeftFiles int64 `json:"leftFiles"`
	LeftBytes int64 `json:"leftBytes"`
	// Reported, not backed up.
	SymlinkCount int64    `json:"symlinkCount"`
	Symlinks     []string `json:"symlinks"`
	MissingCount int64    `json:"missingCount"` // catalog rows without a file in the library
	Missing      []string `json:"missing"`
	InvalidCount int64    `json:"invalidCount"`
	// The library walk could not read these (the backup is incomplete).
	ScanErrorCount int64     `json:"scanErrorCount"`
	ScanErrors     []Failure `json:"scanErrors"`
	// Files left out because their name differs from another's only in
	// case or Unicode form; also counted in failedCount and listed in
	// failed.
	CollisionCount int64     `json:"collisionCount"`
	Failures       []Failure `json:"failed"`
	Truncated      bool      `json:"truncated,omitempty"`
	// Snapshots.
	UserSnapshot  bool `json:"userSnapshot"`
	MediaSnapshot bool `json:"mediaSnapshot"`
	// The snapshot's content had not changed since the last one: it was
	// not written again.
	UserSnapshotUnchanged  bool   `json:"userSnapshotUnchanged,omitempty"`
	MediaSnapshotUnchanged bool   `json:"mediaSnapshotUnchanged,omitempty"`
	SnapshotError          string `json:"snapshotError,omitempty"`
	Cancelled              bool   `json:"cancelled"`
	// Error is why the run stopped before it was done (RunError: no
	// paths), "" when it was not stopped by an error.
	Error   string `json:"error,omitempty"`
	Seconds int64  `json:"seconds"`
}

// ExitCode is the CLI exit status of a finished run: 0 when everything
// was verified, 1 when files failed, did not fit (partial), a snapshot
// failed, the library walk could not read a folder, or catalog files are
// missing in the library.
func (s *Stats) ExitCode() int {
	if s == nil {
		return 0
	}
	if s.FailedCount > 0 || s.LeftFiles > 0 || s.SnapshotError != "" || s.ScanErrorCount > 0 || s.MissingCount > 0 {
		return 1
	}
	return 0
}

// errSimulatedCrash ends a run the way a killed process would (tests).
var errSimulatedCrash = errors.New("simulated crash")

// crashAfter, when set, is asked after every finished file how many have
// finished; true ends the run at once, without writing the pending rows
// or the end of the run (a kill).
var crashAfter func(done int) bool

// Run backs the library up into the set: it plans, checks the free
// space, renames moved files, copies new and changed files (verified by
// read-back before their rows are written, rowBatch rows per
// transaction, newest first, opts.Workers at a time), then writes the
// user.db and media.db snapshots and the runs row. Per-file failures are
// in the stats; the error is for a run that could not be done (the set
// belongs to another library, no space without Partial, the target or
// backup.db failed) or was cancelled (ctx.Err(); the rows of the files
// done are written).
func Run(ctx context.Context, src Source, set *Set, opts Options) (Stats, error) {
	st := Stats{Symlinks: []string{}, Missing: []string{}, Failures: []Failure{}, ScanErrors: []Failure{}}
	started := time.Now()
	workers := opts.Workers
	if workers <= 0 {
		workers = DefaultWorkers
	}
	workers = min(workers, MaxWorkers)
	unlock, held, err := set.Lock("backup")
	if err != nil {
		return st, err
	}
	defer unlock()
	m := newMeter(opts.OnProgress)
	stop := m.run()
	defer stop()
	log, closeLog := setLogger(set, "backup", opts.Logger)
	defer closeLog()
	if held {
		set.closeInterrupted(ctx, log)
	}

	if err := set.checkSeparate(src.Lib); err != nil {
		return st, err
	}
	if err := set.Claim(ctx, opts.Owner, false, opts.TakeOver); err != nil {
		return st, err
	}
	m.phase(PhasePlan, 0, 0)
	plan, err := PlanFor(ctx, src, set)
	if err != nil {
		return st, err
	}
	st.Current, st.Moved, st.Changed, st.New, st.Extra = plan.Current.Files, plan.Moved.Files, plan.Changed.Files,
		plan.New.Files, plan.Extra.Files
	st.SymlinkCount, st.MissingCount, st.InvalidCount = int64(len(plan.Symlinks)), int64(len(plan.Missing)), int64(len(plan.Invalid))
	for _, s := range plan.Symlinks {
		st.Symlinks = appendCapped(st.Symlinks, s, &st.Truncated)
		log.Info("symbolic link skipped", "path", s)
	}
	for _, s := range plan.Missing {
		st.Missing = appendCapped(st.Missing, s, &st.Truncated)
		log.Warn("in the catalog but missing in the library", "path", s)
	}
	for _, s := range plan.Invalid {
		log.Warn("catalog path is not a library path; skipped", "path", s)
	}
	st.ScanErrorCount = int64(len(plan.Errors))
	for _, f := range plan.Errors {
		st.ScanErrors = appendFailure(st.ScanErrors, f, &st.Truncated)
		log.Error("the library walk could not read this; its files are not backed up", "path", f.Path, "err", f.Error)
	}
	st.CollisionCount = int64(len(plan.Collisions))
	for _, c := range plan.Collisions {
		f := collisionFailure(c)
		st.FailedCount++
		st.Failures = appendFailure(st.Failures, f, &st.Truncated)
		log.Error("name collision; not backed up", "path", c.Path, "other", c.Other)
		if opts.OnFailure != nil {
			opts.OnFailure(f)
		}
	}
	log.Info("plan", "current", plan.Current.Files, "moved", plan.Moved.Files, "changed", plan.Changed.Files,
		"new", plan.New.Files, "extra", plan.Extra.Files, "copy_bytes", plan.CopyBytes())

	if err := set.prepareTmp(); err != nil {
		return st, err
	}
	snapBytes := snapshotBytes(src)
	budget := int64(-1) // no limit
	if free := freeSpace(set.Dir); free >= 0 {
		need := plan.CopyBytes() + snapBytes
		if need+spaceMargin > free {
			if !opts.Partial {
				log.Error("not enough space", "need", need, "free", free)
				return st, &NoSpaceError{Need: need, Free: free}
			}
			st.Partial = true
			budget = max(free-snapBytes-spaceMargin, 0)
			log.Info("partial backup", "need", need, "free", free, "budget", budget)
		}
	}

	runID, err := set.beginRun(ctx, "backup", opts.Library)
	if err != nil {
		return st, err
	}
	finish := func() {
		st.Seconds = int64(time.Since(started).Seconds())
		if err := set.finishRun(runID, &st); err != nil {
			log.Error("record the run", "err", err)
		}
	}

	r := &runner{set: set, src: src, log: log, m: m, budget: budget, onFailure: opts.OnFailure, st: &st}
	m.phase(PhaseCopy, int64(len(plan.Items)), plan.CopyBytes())
	err = r.pipeline(ctx, plan.Items, workers)
	if errors.Is(err, errSimulatedCrash) {
		return st, err // as if killed: no end of run
	}
	if err != nil {
		if ctx.Err() != nil {
			st.Cancelled = true
			log.Info("backup cancelled", "copied", st.Copied)
			finish()
			return st, ctx.Err()
		}
		log.Error("backup failed", "err", err)
		st.Error = RunError(err)
		finish()
		return st, err
	}

	m.phase(PhaseSnapshot, 2, snapBytes)
	if err := snapshots(ctx, src, set, &st, log); err != nil {
		if ctx.Err() != nil {
			st.Cancelled = true
			finish()
			return st, ctx.Err()
		}
		st.SnapshotError = PublicError(err)
		if st.SnapshotError == "the file could not be copied (see the log)" {
			st.SnapshotError = "the user.db and media.db snapshots could not be written (see the log)"
		}
		log.Error("snapshot failed", "err", err)
	}
	finish()
	log.Info("backup finished", "copied", st.Copied, "renamed", st.Renamed, "adopted", st.Adopted,
		"failed", st.FailedCount, "left", st.LeftFiles, "bytes", st.BytesCopied)
	return st, nil
}

// spaceMargin is kept free on the target besides the snapshots.
const spaceMargin = 1 << 20

func snapshotBytes(src Source) int64 {
	var n int64
	if src.Lib == nil {
		return 0
	}
	for _, name := range []string{library.DBName, library.UserDBName, library.DBName + "-wal", library.UserDBName + "-wal"} {
		if fi, err := os.Stat(filepath.Join(src.Lib.Root(), name)); err == nil {
			n += fi.Size()
		}
	}
	return n
}

// runner carries a run's state across its workers.
type runner struct {
	set       *Set
	src       Source
	log       *slog.Logger
	m         *meter
	onFailure func(Failure)

	mu     sync.Mutex
	st     *Stats
	budget int64 // bytes the copies may still use; -1: no limit
	full   bool  // the target ran out of space
}

// result is a worker's outcome for one item.
type result struct {
	op    *rowOp
	also  *rowOp // a second change in the same batch (a moved file copied again drops the row at From)
	bytes int64
	// dirs are the folders a rename changed: they are synced once
	// before the batch with this result's row is committed, so a row is
	// only written for a durable file.
	dirs []string
}

func (r *runner) pipeline(ctx context.Context, items []Item, workers int) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan Item)
	results := make(chan result, workers*2)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for it := range jobs {
				if ctx.Err() != nil {
					continue
				}
				if res, ok := r.do(ctx, it); ok {
					results <- res
				}
			}
		})
	}
	go func() {
		defer close(jobs)
		for _, it := range items {
			// The partial budget is taken here, in the copy order, so the
			// newest files get the space whatever the workers' timing.
			if it.Class != ClassMoved && !r.reserve(it) {
				continue
			}
			select {
			case jobs <- it:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	var (
		batch   []rowOp
		dirs    = map[string]bool{}
		done    int
		werr    error
		crashed bool
	)
	flush := func() {
		if werr != nil || len(batch) == 0 {
			batch = batch[:0]
			return
		}
		// Each folder written into is synced once per batch, not once
		// per file; the rows follow.
		for d := range dirs {
			library.SyncDir(d)
		}
		clear(dirs)
		// Rows are written even when ctx is cancelled (apply's grace):
		// their files are done.
		if err := r.set.apply(ctx, batch); err != nil {
			werr = err
			cancel()
		}
		batch = batch[:0]
	}
	tick := time.NewTicker(rowFlushInterval)
	defer tick.Stop()
	for {
		select {
		case res, ok := <-results:
			if !ok {
				if crashed {
					return errSimulatedCrash
				}
				flush()
				if werr != nil {
					return werr
				}
				return ctx.Err()
			}
			if crashed {
				continue
			}
			done++
			if res.op != nil {
				batch = append(batch, *res.op)
			}
			if res.also != nil {
				batch = append(batch, *res.also)
			}
			for _, d := range res.dirs {
				dirs[d] = true
			}
			if crashAfter != nil && crashAfter(done) {
				crashed = true
				cancel()
				continue
			}
			if len(batch) >= rowBatch {
				flush()
			}
		case <-tick.C:
			if !crashed {
				flush()
			}
		}
	}
}

// fail records a failed item.
func (r *runner) fail(it Item, err error, detail string) {
	f := Failure{Path: it.Rel, Error: PublicError(err)}
	r.log.Error("backup failed for a file", "path", it.Rel, "step", detail, "err", err)
	r.mu.Lock()
	r.st.FailedCount++
	r.st.Failures = appendFailure(r.st.Failures, f, &r.st.Truncated)
	if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) {
		r.full = true
	}
	r.mu.Unlock()
	r.m.failed()
	if r.onFailure != nil {
		r.onFailure(f)
	}
}

// reserve takes size bytes of the partial budget; false when they do not
// fit (the item is left for the next run).
func (r *runner) reserve(it Item) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.full || (r.budget >= 0 && it.Size > r.budget) {
		r.st.LeftFiles++
		r.st.LeftBytes += it.Size
		return false
	}
	if r.budget >= 0 {
		r.budget -= it.Size
	}
	return true
}

func (r *runner) unreserve(size int64) {
	r.mu.Lock()
	if r.budget >= 0 {
		r.budget += size
	}
	r.mu.Unlock()
}

func (r *runner) count(field *int64, bytes int64) {
	r.mu.Lock()
	*field++
	r.st.BytesCopied += bytes
	r.mu.Unlock()
}

// do handles one item; ok is false when there is nothing to record.
func (r *runner) do(ctx context.Context, it Item) (result, bool) {
	r.m.current(it.Rel)
	if it.Class == ClassMoved {
		res, ok, fallback := r.move(ctx, it)
		if !fallback {
			r.m.done(1, 0)
			return res, ok
		}
		// The content is gone from the target: copy it, and drop the
		// row at From, whose file is gone (else a scrub would copy it
		// back there and the content would be kept twice for ever).
		from := it.From
		it.Class, it.From = ClassNew, ""
		if !r.reserve(it) {
			return result{}, false
		}
		res, ok = r.copy(ctx, it)
		if !ok {
			r.unreserve(it.Size)
			return res, ok
		}
		if _, err := os.Lstat(r.set.Abs(from)); errors.Is(err, fs.ErrNotExist) {
			res.also = &rowOp{delete: from}
		}
		return res, ok
	}
	res, ok := r.copy(ctx, it)
	if !ok {
		r.unreserve(it.Size)
	}
	return res, ok
}

func (r *runner) row(it Item, hash uint64, size int64, mtime int64) *FileRow {
	return &FileRow{RelPath: it.Rel, Hash: hash, Size: size, MTime: mtime, BackedAt: r.set.now().Unix()}
}

// move renames a moved file on the target. fallback asks for a copy
// instead (the target no longer has the content at From).
func (r *runner) move(ctx context.Context, it Item) (res result, ok, fallback bool) {
	src, dst := r.set.Abs(it.From), r.set.Abs(it.Rel)
	if err := r.set.within(src); err != nil {
		r.fail(it, err, "move")
		return result{}, false, false
	}
	if err := r.set.within(dst); err != nil {
		r.fail(it, err, "move")
		return result{}, false, false
	}
	if fi, err := os.Lstat(src); err != nil || !fi.Mode().IsRegular() {
		return result{}, false, true
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		r.fail(it, err, "move")
		return result{}, false, false
	}
	err := library.RenameNoReplace(src, dst)
	if err == nil {
		r.count(&r.st.Renamed, 0)
		r.log.Debug("renamed", "from", it.From, "to", it.Rel)
		return result{op: &rowOp{upsert: &FileRow{RelPath: it.Rel}, from: it.From},
			dirs: []string{filepath.Dir(dst), filepath.Dir(src)}}, true, false
	}
	if errors.Is(err, fs.ErrNotExist) {
		return result{}, false, true
	}
	if !errors.Is(err, fs.ErrExist) {
		r.fail(it, err, "move")
		return result{}, false, false
	}
	// Something without a row is at the destination: an interrupted run
	// renamed or copied the content there (adopted), or a file picmover
	// did not write, which is never moved away.
	if adopted, ok := r.adopt(ctx, it, dst); ok {
		return adopted, true, false
	}
	r.fail(it, errConflict, "move")
	return result{}, false, false
}

// errConflict: a file the set has no row for is at the destination and
// does not have the content; picmover did not write it and leaves it.
var errConflict = errors.New("a file the backup set does not know is already at this path on the target; move it away or remove it, then back up again")

// adopt checks a file found at dst that has no row: when it hashes to the
// item's content it is recorded as backed up (a run was interrupted after
// the copy, before its row was written).
func (r *runner) adopt(ctx context.Context, it Item, dst string) (result, bool) {
	fi, err := os.Lstat(dst)
	if err != nil || !fi.Mode().IsRegular() {
		return result{}, false
	}
	want, wantKnown := it.Hash, it.HashKnown
	libAbs := libPath(r.src.Lib.Root(), it.Rel)
	lfi, err := os.Stat(libAbs)
	if err != nil {
		return result{}, false
	}
	if !wantKnown {
		h, _, err := library.HashFileContext(ctx, libAbs)
		if err != nil {
			return result{}, false
		}
		want = h
	}
	h, n, err := library.HashFileContext(ctx, dst)
	if err != nil || h != want || n != lfi.Size() {
		return result{}, false
	}
	r.count(&r.st.Adopted, 0)
	r.m.done(1, 0)
	r.log.Info("already on the target; verified", "path", it.Rel)
	return result{op: &rowOp{upsert: r.row(it, h, n, lfi.ModTime().UnixNano())}}, true
}

// copy copies a new or changed file.
func (r *runner) copy(ctx context.Context, it Item) (result, bool) {
	libAbs := libPath(r.src.Lib.Root(), it.Rel)
	lfi, err := os.Lstat(libAbs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		r.fail(it, errMissingInLibrary, "stat")
		return result{}, false
	case err != nil:
		r.fail(it, err, "stat")
		return result{}, false
	case !lfi.Mode().IsRegular():
		r.fail(it, library.ErrNotRegularFile, "stat")
		return result{}, false
	}
	dst := r.set.Abs(it.Rel)
	if err := r.set.within(dst); err != nil {
		r.fail(it, err, "copy")
		return result{}, false
	}
	var want *uint64
	if it.HashKnown {
		h := it.Hash
		want = &h
	}
	if it.Class == ClassNew {
		if _, err := os.Lstat(dst); err == nil {
			// No row for this path: the content of an interrupted run
			// is adopted; anything else picmover did not write and
			// never moves away.
			if res, ok := r.adopt(ctx, it, dst); ok {
				return res, true
			}
			r.fail(it, errConflict, "copy")
			return result{}, false
		}
		res, err := library.CopyReadBackDeferSync(ctx, libAbs, dst, r.set.TmpDir(), want)
		if err != nil {
			if ctx.Err() == nil {
				r.fail(it, err, "copy")
			}
			return result{}, false
		}
		r.count(&r.st.Copied, res.Size)
		r.m.done(1, res.Size)
		return result{op: &rowOp{upsert: r.row(it, res.Hash, res.Size, res.MTime.UnixNano())}, bytes: res.Size,
			dirs: []string{filepath.Dir(dst)}}, true
	}
	// Changed: the new content is staged and verified first; only then
	// does the old file move to previous/ and the new one take its place.
	stage := filepath.Join(r.set.TmpDir(), "stage-"+randomSuffix())
	res, err := library.CopyReadBackDeferSync(ctx, libAbs, stage, r.set.TmpDir(), want)
	if err != nil {
		if ctx.Err() == nil {
			r.fail(it, err, "copy")
		}
		return result{}, false
	}
	if it.Row != nil && res.Hash == it.Row.Hash {
		// A non-media file whose mtime changed but not its content.
		os.Remove(stage)
		if fi, err := os.Lstat(dst); err == nil && fi.Mode().IsRegular() {
			r.count(&r.st.Refreshed, 0)
			r.m.done(1, 0)
			row := r.row(it, res.Hash, res.Size, res.MTime.UnixNano())
			row.BackedAt, row.VerifiedAt = it.Row.BackedAt, it.Row.VerifiedAt
			return result{op: &rowOp{upsert: row}}, true
		}
		// The target lost the file: copy it after all.
		if res, err = library.CopyReadBackDeferSync(ctx, libAbs, stage, r.set.TmpDir(), want); err != nil {
			if ctx.Err() == nil {
				r.fail(it, err, "copy")
			}
			return result{}, false
		}
	}
	// A destination that already has the new content (a run killed after
	// putting it in place, before its row was written) is adopted: a
	// correct copy never goes to previous/.
	if fi, err := os.Lstat(dst); err == nil && fi.Mode().IsRegular() && fi.Size() == res.Size {
		if h, n, err := library.HashFileContext(ctx, dst); err == nil && h == res.Hash && n == res.Size {
			os.Remove(stage)
			r.count(&r.st.Adopted, 0)
			r.m.done(1, 0)
			r.log.Info("already on the target; verified", "path", it.Rel)
			return result{op: &rowOp{upsert: r.row(it, res.Hash, res.Size, res.MTime.UnixNano())}}, true
		}
	}
	// The file at dst is the one the row at this path recorded (changed
	// items always have a row): its earlier content is kept.
	if _, err := os.Lstat(dst); err == nil {
		if _, err := r.set.keepPrevious(it.Rel); err != nil {
			os.Remove(stage)
			r.fail(it, err, "keep previous")
			return result{}, false
		}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		os.Remove(stage)
		r.fail(it, err, "copy")
		return result{}, false
	}
	if err := library.RenameNoReplace(stage, dst); err != nil {
		os.Remove(stage)
		r.fail(it, err, "rename into place")
		return result{}, false
	}
	r.count(&r.st.Copied, res.Size)
	r.m.done(1, res.Size)
	return result{op: &rowOp{upsert: r.row(it, res.Hash, res.Size, res.MTime.UnixNano())}, bytes: res.Size,
		dirs: []string{filepath.Dir(dst)}}, true
}

func randomSuffix() string {
	var b [8]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// metaUserSnapshotHash is the set meta key holding the hash (16 hex
// digits) of the user.db snapshot at the set root, checked by restore.
const metaUserSnapshotHash = "user_snapshot_hash"

// snapshots writes user.db and media.db at the set root, each a
// consistent copy (VACUUM INTO a temp file, fsync, then into place
// without replacing anything: the previous snapshot moves to previous/
// first). The previous user.db snapshot is kept (pruned with the keep
// window); the previous media.db snapshot, derived data, is removed once
// the new one is in place. The user.db snapshot's hash is stored in set
// meta for restore to verify.
func snapshots(ctx context.Context, src Source, set *Set, st *Stats, log *slog.Logger) error {
	if src.User != nil {
		// user.db changes on every backup (the target's last backup
		// time), so it is compared by its content without that
		// bookkeeping (userdata ContentHash); the snapshot on the target
		// must still hash as recorded.
		content, err := src.User.ContentHash(ctx)
		if err != nil {
			return err
		}
		cv, _ := set.Meta(ctx, metaUserContentHash)
		if sv, _ := set.Meta(ctx, metaUserSnapshotHash); cv == fmt.Sprintf("%016x", content) && sv != "" &&
			snapshotIntact(ctx, set, UserSnapshot, sv) {
			st.UserSnapshot, st.UserSnapshotUnchanged = true, true
			log.Info("user.db unchanged since the last snapshot")
		} else {
			tmp := filepath.Join(set.TmpDir(), "snap-user-"+randomSuffix()+".db")
			h, err := writeSnapshot(ctx, src.User.Snapshot, tmp)
			if err != nil {
				return err
			}
			if _, err := placeSnapshot(set, tmp, UserSnapshot); err != nil {
				return fmt.Errorf("the user.db snapshot: %w", err)
			}
			wctx := context.WithoutCancel(ctx)
			if err := set.SetMeta(wctx, metaUserSnapshotHash, fmt.Sprintf("%016x", h)); err != nil {
				return err
			}
			if err := set.SetMeta(wctx, metaUserContentHash, fmt.Sprintf("%016x", content)); err != nil {
				return err
			}
			st.UserSnapshot = true
			log.Info("user.db snapshot written")
		}
	}
	if src.Catalog != nil {
		tmp := filepath.Join(set.TmpDir(), "snap-media-"+randomSuffix()+".db")
		h, err := writeSnapshot(ctx, src.Catalog.Snapshot, tmp)
		if err != nil {
			return err
		}
		if unchangedSnapshot(ctx, set, MediaSnapshot, metaMediaSnapshotHash, h) {
			os.Remove(tmp)
			st.MediaSnapshot, st.MediaSnapshotUnchanged = true, true
			log.Info("media.db unchanged since the last snapshot")
		} else {
			old, err := placeSnapshot(set, tmp, MediaSnapshot)
			if err != nil {
				return fmt.Errorf("the media.db snapshot: %w", err)
			}
			if old != "" && set.inSet(old) {
				os.Remove(old)
			}
			if err := set.SetMeta(context.WithoutCancel(ctx), metaMediaSnapshotHash, fmt.Sprintf("%016x", h)); err != nil {
				return err
			}
			st.MediaSnapshot = true
			log.Info("media.db snapshot written")
		}
	}
	library.SyncDir(set.Dir)
	return nil
}

// metaMediaSnapshotHash is the set meta key holding the hash of the
// media.db snapshot at the set root.
const metaMediaSnapshotHash = "media_snapshot_hash"

// unchangedSnapshot reports whether the hash recorded for the snapshot
// name at the set root (meta key) is h and the file there, a regular
// file, still hashes to it: then the new snapshot is the same content
// and is not written again (reading it back is much cheaper than writing
// and syncing it, and proves it is intact).
func unchangedSnapshot(ctx context.Context, set *Set, name, key string, h uint64) bool {
	v, err := set.Meta(ctx, key)
	if err != nil || v != fmt.Sprintf("%016x", h) {
		return false
	}
	return snapshotIntact(ctx, set, name, v)
}

// snapshotIntact reports whether the snapshot name at the set root is a
// regular file hashing to want (16 hex digits).
func snapshotIntact(ctx context.Context, set *Set, name, want string) bool {
	p := set.Abs(name)
	if fi, err := os.Lstat(p); err != nil || !fi.Mode().IsRegular() {
		return false
	}
	got, _, err := library.HashFileContext(ctx, p)
	return err == nil && fmt.Sprintf("%016x", got) == want
}

// metaUserContentHash is the set meta key holding the user data's
// content hash (userdata ContentHash) at the last user.db snapshot.
const metaUserContentHash = "user_content_hash"

// writeSnapshot writes a snapshot to tmp with write, syncs it and
// returns its hash; tmp is removed on failure.
func writeSnapshot(ctx context.Context, write func(context.Context, string) error, tmp string) (uint64, error) {
	if err := write(ctx, tmp); err != nil {
		os.Remove(tmp)
		return 0, err
	}
	if err := syncFile(tmp); err != nil {
		os.Remove(tmp)
		return 0, err
	}
	h, _, err := library.HashFileContext(ctx, tmp)
	if err != nil {
		os.Remove(tmp)
		return 0, err
	}
	return h, nil
}

// placeSnapshot puts tmp at the set root as name: the snapshot there
// (a regular file; anything else is refused) moves to previous/ first,
// then tmp is renamed in without replacing anything. It returns where
// the previous snapshot went ("" when there was none). tmp is removed on
// failure.
func placeSnapshot(set *Set, tmp, name string) (string, error) {
	dst := set.Abs(name)
	old := ""
	if fi, err := os.Lstat(dst); err == nil {
		if !fi.Mode().IsRegular() {
			os.Remove(tmp)
			return "", fmt.Errorf("%s: %w", name, library.ErrNotRegularFile)
		}
		if old, err = set.keepPrevious(name); err != nil {
			os.Remove(tmp)
			return "", fmt.Errorf("keep the previous snapshot: %w", err)
		}
	}
	if err := library.RenameNoReplace(tmp, dst); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return old, nil
}

func syncFile(p string) error {
	f, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	err = f.Sync()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// setLogger returns a logger writing to a new log in the set's logs
// folder (<prefix>_<time>.log) and to extra, and makes it the set's log
// (the busy warnings of its writes); the close function closes the log
// file.
func setLogger(set *Set, prefix string, extra *slog.Logger) (*slog.Logger, func()) {
	l, closeFn := newSetLogger(set, prefix, extra)
	set.log = l
	return l, func() {
		set.log = extra
		closeFn()
	}
}

func newSetLogger(set *Set, prefix string, extra *slog.Logger) (*slog.Logger, func()) {
	var handlers []slog.Handler
	if extra != nil {
		handlers = append(handlers, extra.Handler())
	}
	closeFn := func() {}
	if err := realDir(set.MetaDir(), false); err == nil && realDir(set.LogDir(), true) == nil {
		if f, _, err := openUniqueLog(set.LogDir(), prefix); err == nil {
			handlers = append(handlers, slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug}))
			closeFn = func() { f.Close() }
		}
	}
	switch len(handlers) {
	case 0:
		return slog.New(slog.DiscardHandler), closeFn
	case 1:
		return slog.New(handlers[0]), closeFn
	}
	return slog.New(slog.NewMultiHandler(handlers...)), closeFn
}

// openUniqueLog creates <dir>/<prefix>_<time>.log, or -2, -3, ... when
// the name is taken.
func openUniqueLog(dir, prefix string) (*os.File, string, error) {
	stem := filepath.Join(dir, prefix+"_"+time.Now().Format("2006-01-02_15-04-05"))
	for i := 1; i <= 1000; i++ {
		p := stem + ".log"
		if i > 1 {
			p = fmt.Sprintf("%s-%d.log", stem, i)
		}
		f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
		if err == nil {
			return f, p, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, "", err
		}
	}
	return nil, "", errors.New("no free log name")
}
