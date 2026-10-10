package backup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultKeep is how long earlier versions in previous/ are kept.
const DefaultKeep = 30 * 24 * time.Hour

// PruneItem is one thing a prune removes: a backed-up file the library no
// longer has (Row), or an earlier version under previous/ older than the
// keep window.
type PruneItem struct {
	Path     string // set path (previous/ items: .picmover/previous/...)
	Size     int64
	Row      bool // a files row (removed with the file)
	Previous bool
}

// PrunePlan is what Prune removes.
type PrunePlan struct {
	Items []PruneItem // rows first (by path), then previous/ (by path)
	Count int
	Bytes int64
}

// ErrPruneCount is returned (wrapped) by Prune when the plan no longer
// has the confirmed number of items.
var ErrPruneCount = errors.New("the files to remove changed since they were listed; list them again and confirm the new count")

// PlanPrune lists what a prune removes: the plan's extras (rows whose path
// and content are not in the library, live or trashed) and the files under
// previous/ older than keep (by the Unix time in their name, else their
// modification time). The user.db and media.db snapshots are never
// included.
func PlanPrune(set *Set, plan *Plan, keep time.Duration, now time.Time) (*PrunePlan, error) {
	pp := &PrunePlan{}
	for _, r := range plan.Extras {
		pp.Items = append(pp.Items, PruneItem{Path: r.RelPath, Size: r.Size, Row: true})
		pp.Bytes += r.Size
	}
	var prev []PruneItem
	cutoff := now.Add(-keep)
	root := set.PreviousDir()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root && errors.Is(err, fs.ErrNotExist) {
				return fs.SkipAll
			}
			if d != nil && d.IsDir() && p != root {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		at := fi.ModTime()
		if i := strings.LastIndexByte(d.Name(), '.'); i >= 0 {
			if n, err := strconv.ParseInt(d.Name()[i+1:], 10, 64); err == nil && n > 0 {
				at = time.Unix(n, 0)
			}
		}
		if !at.Before(cutoff) {
			return nil
		}
		rel, err := filepath.Rel(set.Dir, p)
		if err != nil {
			return nil
		}
		prev = append(prev, PruneItem{Path: filepath.ToSlash(rel), Size: fi.Size(), Previous: true})
		return nil
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return nil, err
	}
	sort.Slice(prev, func(i, j int) bool { return prev[i].Path < prev[j].Path })
	for _, it := range prev {
		pp.Bytes += it.Size
	}
	pp.Items = append(pp.Items, prev...)
	pp.Count = len(pp.Items)
	return pp, nil
}

// PruneStats are the counters of a prune (the runs row's stats).
type PruneStats struct {
	Removed   int64     `json:"removed"`
	Bytes     int64     `json:"bytes"`
	Failures  []Failure `json:"failed"`
	Truncated bool      `json:"truncated,omitempty"`
	Seconds   int64     `json:"seconds"`
}

// PruneOptions configure Prune.
type PruneOptions struct {
	// Keep is how long earlier versions in previous/ are kept: 0 keeps
	// none (pass DefaultKeep for the default); negative is an error.
	Keep     time.Duration
	Expected int // the count the user confirmed
	// Owner is the library's identity: the set must belong to it (and
	// have been last written by this instance unless TakeOver).
	Owner    Identity
	TakeOver bool
	// Force prunes even when the library scan was incomplete (unreadable
	// folders) or catalog files are missing in the library.
	Force      bool
	Library    string
	OnProgress func(Progress)
	Logger     *slog.Logger
}

// ErrIncompleteScan is matched by an *IncompleteScanError.
var ErrIncompleteScan = errors.New("the library could not be read completely")

// IncompleteScanError: prune refused because the library walk could not
// read some folders or catalog files are missing in the library, so files
// that still exist could look gone.
type IncompleteScanError struct {
	Unreadable, Missing int
}

func (e *IncompleteScanError) Error() string {
	return fmt.Sprintf("prune refused: the library could not be read completely (%d unreadable folders or files, "+
		"%d catalog files missing), so files it still has could look deleted; fix that first, or force the prune", e.Unreadable, e.Missing)
}

func (e *IncompleteScanError) Is(target error) bool { return target == ErrIncompleteScan }

// CheckComplete returns an *IncompleteScanError when the plan's scan had
// errors or catalog files are missing in the library.
func (p *Plan) CheckComplete() error {
	if len(p.Errors) > 0 || len(p.Missing) > 0 {
		return &IncompleteScanError{Unreadable: len(p.Errors), Missing: len(p.Missing)}
	}
	return nil
}

// Prune plans again (PlanPrune) and, when the count equals
// opts.Expected, removes the items: only regular files inside the set
// (never through a link), each row with its file (a row whose file is
// already gone is just dropped), then the folders the removals left
// empty. Nothing else on the target and nothing in the library is
// touched. The set must belong to opts.Owner (Set.Claim with owned), and
// the library scan must be complete unless opts.Force.
func Prune(ctx context.Context, src Source, set *Set, opts PruneOptions) (PruneStats, error) {
	st := PruneStats{Failures: []Failure{}}
	started := time.Now()
	keep := opts.Keep
	if keep < 0 {
		return st, errors.New("backup: negative keep window")
	}
	if err := set.checkSeparate(src.Lib); err != nil {
		return st, err
	}
	unlock, held, err := set.Lock("prune")
	if err != nil {
		return st, err
	}
	defer unlock()
	if err := set.Claim(ctx, opts.Owner, true, opts.TakeOver); err != nil {
		return st, err
	}
	m := newMeter(opts.OnProgress)
	stop := m.run()
	defer stop()
	log, closeLog := setLogger(set, "prune", opts.Logger)
	defer closeLog()
	if held {
		set.closeInterrupted(ctx, log)
	}
	m.phase(PhasePlan, 0, 0)
	plan, err := PlanFor(ctx, src, set)
	if err != nil {
		return st, err
	}
	if err := plan.CheckComplete(); err != nil && !opts.Force {
		log.Error("prune refused", "err", err)
		return st, err
	}
	pp, err := PlanPrune(set, plan, keep, set.now())
	if err != nil {
		return st, err
	}
	if pp.Count != opts.Expected {
		return st, fmt.Errorf("%w (confirmed %d, now %d)", ErrPruneCount, opts.Expected, pp.Count)
	}
	runID, err := set.beginRun(ctx, "prune", opts.Library)
	if err != nil {
		return st, err
	}
	m.phase(PhasePrune, int64(pp.Count), pp.Bytes)
	dirs := map[string]bool{}
	var ops []rowOp
	flush := func() {
		if err := set.apply(context.WithoutCancel(ctx), ops); err != nil {
			log.Error("prune: write rows", "err", err)
		}
		ops = ops[:0]
	}
	for _, it := range pp.Items {
		if ctx.Err() != nil {
			break
		}
		m.current(it.Path)
		p := set.Abs(it.Path)
		err := removeSetFile(set, p)
		switch {
		case err == nil:
			st.Removed++
			st.Bytes += it.Size
			dirs[filepath.Dir(p)] = true
			log.Info("removed", "path", it.Path, "size", it.Size)
			m.done(1, it.Size)
		case errors.Is(err, fs.ErrNotExist) && it.Row:
			st.Removed++
			m.done(1, 0)
		default:
			log.Error("prune: remove", "path", it.Path, "err", err)
			st.Failures = appendFailure(st.Failures, Failure{Path: it.Path, Error: PublicError(err)}, &st.Truncated)
			m.failed()
			continue
		}
		if it.Row {
			ops = append(ops, rowOp{delete: it.Path})
			if len(ops) >= rowBatch {
				flush()
			}
		}
	}
	flush()
	removeEmptyDirs(set, dirs)
	st.Seconds = int64(time.Since(started).Seconds())
	if err := set.finishRun(runID, &st); err != nil {
		log.Error("record the run", "err", err)
	}
	if ctx.Err() != nil {
		return st, ctx.Err()
	}
	return st, nil
}

// removeSetFile removes the regular file p inside the set; a link, a
// folder or a path leading outside the set is refused.
func removeSetFile(set *Set, p string) error {
	if err := set.within(p); err != nil {
		return err
	}
	fi, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s: not a regular file: %w", p, errNotRegular)
	}
	return os.Remove(p)
}

var errNotRegular = errors.New("not a regular file")

// removeEmptyDirs removes the folders in dirs and their parents that are
// empty, up to (not including) the set folder and its .picmover and
// previous folders.
func removeEmptyDirs(set *Set, dirs map[string]bool) {
	stopAt := map[string]bool{set.Dir: true, set.MetaDir(): true, set.PreviousDir(): true,
		filepath.Join(set.MetaDir(), "trash"): true}
	list := make([]string, 0, len(dirs))
	for d := range dirs {
		list = append(list, d)
	}
	// Deepest first.
	sort.Slice(list, func(i, j int) bool { return len(list[i]) > len(list[j]) })
	for _, d := range list {
		for !stopAt[d] && inside(set.Dir, d) {
			if os.Remove(d) != nil { // fails unless empty
				break
			}
			d = filepath.Dir(d)
		}
	}
}
