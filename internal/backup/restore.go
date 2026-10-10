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
	"time"

	"picmover/internal/library"
)

// Restore modes.
const (
	ModeMissing  = "missing"   // copy back the files the library lacks (and user.db when it has none)
	ModeUserData = "user-data" // copy back user.db only
)

// ErrUserDBExists: user-data mode into a library that has a user.db,
// without Force.
var ErrUserDBExists = errors.New("the library already has a user.db; use --force to replace it (the current one is kept as user.db.bak-<time>)")

// ErrNoUserSnapshot: the set has no user.db snapshot.
var ErrNoUserSnapshot = errors.New("the backup set has no user.db snapshot")

// RestoreOptions configure Restore.
type RestoreOptions struct {
	Mode   string // ModeMissing (default) or ModeUserData
	Force  bool   // user-data: replace an existing user.db (kept as user.db.bak-<time>)
	DryRun bool   // count what would be restored; copy nothing
	// Library is recorded in the runs row.
	Library    string
	OnProgress func(Progress)
	OnFailure  func(Failure)
	Logger     *slog.Logger
}

// RestoreStats are the counters of a restore (the runs row's stats).
type RestoreStats struct {
	Restored    int64     `json:"restored"`
	Bytes       int64     `json:"bytes"`
	Present     int64     `json:"present"`   // the library has a file at the path: never overwritten
	Elsewhere   int64     `json:"elsewhere"` // the library has the content under another path
	FailedCount int64     `json:"failedCount"`
	Failures    []Failure `json:"failed"`
	Paths       []string  `json:"paths,omitempty"` // dry run: what would be restored
	Truncated   bool      `json:"truncated,omitempty"`
	// UserDB is what happened to user.db: "restored", "replaced",
	// "present" (the library has one; missing mode leaves it), "none"
	// (the set has no snapshot), or "" (not considered).
	UserDB string `json:"userDB"`
	// NeedsRebuild: the library has no media.db; the caller records the
	// restored files (an in-place rebuild).
	NeedsRebuild bool  `json:"needsRebuild"`
	Cancelled    bool  `json:"cancelled"`
	Seconds      int64 `json:"seconds"`
}

// ExitCode is 0 when everything was restored, 1 when files failed.
func (s *RestoreStats) ExitCode() int {
	if s.FailedCount > 0 {
		return 1
	}
	return 0
}

// Restore copies files from the set back into the library at lib (which
// may be empty or partial). Mode missing: every row whose path the
// library does not have and whose content it does not have elsewhere (a
// file of the same size hashing the same) is copied back with
// verification (CopyReadBack through the library's temp folder, never
// replacing anything); then user.db when the library has none. Mode
// user-data: only user.db, refused when the library has one unless Force
// (the current file and its -wal/-shm/-journal are renamed to
// user.db.bak-<time> first; the caller must have it closed).
// NeedsRebuild is set when the library has no media.db afterwards.
func Restore(ctx context.Context, set *Set, lib *library.Library, opts RestoreOptions) (RestoreStats, error) {
	var st RestoreStats
	started := time.Now()
	if opts.Mode == "" {
		opts.Mode = ModeMissing
	}
	if opts.Mode != ModeMissing && opts.Mode != ModeUserData {
		return st, fmt.Errorf("invalid restore mode %q: want %s or %s", opts.Mode, ModeMissing, ModeUserData)
	}
	m := newMeter(opts.OnProgress)
	stop := m.run()
	defer stop()
	var log *slog.Logger
	closeLog := func() {}
	if opts.DryRun {
		log = opts.Logger
		if log == nil {
			log = slog.New(slog.DiscardHandler)
		}
	} else {
		log, closeLog = setLogger(set, "restore", opts.Logger)
	}
	defer closeLog()
	var runID int64
	finish := func() {
		st.Seconds = int64(time.Since(started).Seconds())
		if _, err := os.Stat(lib.DBPath()); errors.Is(err, fs.ErrNotExist) {
			st.NeedsRebuild = true
		}
		if runID != 0 {
			if err := set.finishRun(runID, &st); err != nil {
				log.Error("record the run", "err", err)
			}
		}
	}
	if !opts.DryRun {
		var err error
		if runID, err = set.beginRun(ctx, "restore", opts.Library); err != nil {
			return st, err
		}
	}
	if opts.Mode == ModeUserData {
		err := restoreUserDB(ctx, set, lib, opts, &st, log)
		finish()
		return st, err
	}

	rows, err := set.Files(ctx)
	if err != nil {
		finish()
		return st, err
	}
	m.phase(PhasePlan, 0, 0)
	sizes, err := librarySizes(ctx, lib)
	if err != nil {
		finish()
		return st, err
	}
	hashes := map[string]uint64{} // library files hashed so far
	elsewhere := func(r FileRow) bool {
		for _, p := range sizes[r.Size] {
			h, ok := hashes[p]
			if !ok {
				var err error
				if h, _, err = library.HashFileContext(ctx, p); err != nil {
					continue
				}
				hashes[p] = h
			}
			if h == r.Hash {
				return true
			}
		}
		return false
	}
	var todo []FileRow
	var total int64
	for _, r := range rows {
		if ctx.Err() != nil {
			break
		}
		dst := lib.Abs(r.RelPath)
		if _, err := os.Lstat(dst); err == nil {
			st.Present++
			continue
		}
		if elsewhere(r) {
			st.Elsewhere++
			continue
		}
		todo = append(todo, r)
		total += r.Size
	}
	if ctx.Err() != nil {
		st.Cancelled = true
		finish()
		return st, ctx.Err()
	}
	if opts.DryRun {
		for _, r := range todo {
			st.Restored++
			st.Bytes += r.Size
			st.Paths = appendCapped(st.Paths, r.RelPath, &st.Truncated)
		}
		if _, err := os.Lstat(filepath.Join(lib.Root(), library.UserDBName)); err == nil {
			st.UserDB = "present"
		} else if _, err := os.Lstat(set.Abs(UserSnapshot)); err == nil {
			st.UserDB = "restored"
		} else {
			st.UserDB = "none"
		}
		finish()
		return st, nil
	}
	if err := os.MkdirAll(lib.TempDir(), 0o755); err != nil {
		finish()
		return st, err
	}
	m.phase(PhaseRestore, int64(len(todo)), total)
	for _, r := range todo {
		if ctx.Err() != nil {
			break
		}
		m.current(r.RelPath)
		if err := restoreFile(ctx, set, lib, r); err != nil {
			if ctx.Err() != nil {
				break
			}
			f := Failure{Path: r.RelPath, Error: PublicError(err)}
			log.Error("restore failed for a file", "path", r.RelPath, "err", err)
			st.FailedCount++
			st.Failures = appendFailure(st.Failures, f, &st.Truncated)
			m.failed()
			if opts.OnFailure != nil {
				opts.OnFailure(f)
			}
			continue
		}
		st.Restored++
		st.Bytes += r.Size
		m.done(1, r.Size)
		log.Info("restored", "path", r.RelPath)
	}
	if ctx.Err() != nil {
		st.Cancelled = true
		finish()
		return st, ctx.Err()
	}
	if err := restoreUserDB(ctx, set, lib, opts, &st, log); err != nil && !errors.Is(err, ErrUserDBExists) {
		log.Error("restore user.db", "err", err)
		st.FailedCount++
		st.Failures = appendFailure(st.Failures, Failure{Path: UserSnapshot, Error: PublicError(err)}, &st.Truncated)
	}
	finish()
	log.Info("restore finished", "restored", st.Restored, "present", st.Present, "elsewhere", st.Elsewhere,
		"failed", st.FailedCount, "user_db", st.UserDB)
	return st, nil
}

// restoreFile copies one backed-up file into the library (never
// replacing), verifying it against the row's hash.
func restoreFile(ctx context.Context, set *Set, lib *library.Library, r FileRow) error {
	src := set.Abs(r.RelPath)
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return library.ErrNotRegularFile
	}
	if !set.inSet(src) {
		return errOutsideSet
	}
	dst := lib.Abs(r.RelPath)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	// The folder must still be inside the library once its links are
	// resolved.
	if rd, err := filepath.EvalSymlinks(filepath.Dir(dst)); err == nil {
		root := lib.Root()
		if !samePath(rd, root) && !inside(root, rd) {
			return errOutsideLibrary
		}
	}
	want := r.Hash
	_, err = library.CopyReadBack(ctx, src, dst, lib.TempDir(), &want)
	return err
}

var errOutsideLibrary = errors.New("the path leads outside the library (a link in the library)")

// restoreUserDB copies the set's user.db snapshot into the library.
func restoreUserDB(ctx context.Context, set *Set, lib *library.Library, opts RestoreOptions, st *RestoreStats, log *slog.Logger) error {
	snap := set.Abs(UserSnapshot)
	if fi, err := os.Lstat(snap); err != nil || !fi.Mode().IsRegular() {
		st.UserDB = "none"
		if opts.Mode == ModeUserData {
			return ErrNoUserSnapshot
		}
		return nil
	}
	dst := filepath.Join(lib.Root(), library.UserDBName)
	if _, err := os.Lstat(dst); err == nil {
		if opts.Mode != ModeUserData {
			st.UserDB = "present"
			return nil
		}
		if !opts.Force {
			st.UserDB = "present"
			return ErrUserDBExists
		}
		bak, err := library.BackupCatalog(dst, time.Now())
		if err != nil {
			return err
		}
		log.Info("current user.db kept", "backup", bak)
		if err := os.MkdirAll(lib.TempDir(), 0o755); err != nil {
			return err
		}
		if _, err := library.CopyReadBack(ctx, snap, dst, lib.TempDir(), nil); err != nil {
			return err
		}
		st.UserDB = "replaced"
		return nil
	}
	if err := os.MkdirAll(lib.TempDir(), 0o755); err != nil {
		return err
	}
	if _, err := library.CopyReadBack(ctx, snap, dst, lib.TempDir(), nil); err != nil {
		return err
	}
	st.UserDB = "restored"
	log.Info("user.db restored")
	return nil
}

// librarySizes maps file sizes to the library's regular files (outside
// picmover's own files, the trash included), to find content the library
// already has under another path.
func librarySizes(ctx context.Context, lib *library.Library) (map[int64][]string, error) {
	out := map[int64][]string{}
	root := lib.Root()
	trash := lib.TrashDir()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil {
			if p == root {
				return err
			}
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if p == root {
			return nil
		}
		inTrash := p == trash || strings.HasPrefix(p, trash+string(filepath.Separator))
		if d.IsDir() {
			if filepath.Base(p) == library.MetaDir && filepath.Dir(p) == root || inTrash {
				return nil
			}
			if lib.IsInternal(p) {
				return fs.SkipDir
			}
			return nil
		}
		if (!inTrash && lib.IsInternal(p)) || !d.Type().IsRegular() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		out[fi.Size()] = append(out[fi.Size()], p)
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	return out, err
}
