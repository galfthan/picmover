// Package transfer moves photos from one library of a workspace to
// another: their files, catalog rows, renditions and user data
// (docs/design/library-move.md). It never replaces a file and never
// deletes one: a copy across volumes is verified against the catalog
// hash and the original then goes to the source library's trash, as does
// a source copy of content the target already holds. Every step is
// journalled in the target library so a crash is finished by Recover.
package transfer

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

	"picmover/internal/catalog"
	"picmover/internal/importer"
	"picmover/internal/library"
	"picmover/internal/thumbs"
	"picmover/internal/userdata"
)

// Side is one library of a move, opened and locked by the caller, who
// also serialises other file operations on it (trash, restore, imports)
// for the duration of the call.
type Side struct {
	Lib   *library.Library
	DB    *catalog.DB
	User  *userdata.DB
	Store *thumbs.Store // writable
	Name  string        // the workspace id or folder name, for messages and the import record
}

// Item statuses.
const (
	StatusMoved    = "moved"    // placed in the target
	StatusExisting = "existing" // the target already had the content
	StatusRestored = "restored" // the target's trashed copy was restored
	StatusFailed   = "failed"   // left as it was
)

// Item is the outcome of one hash.
type Item struct {
	Hash    uint64
	Status  string
	RelPath string // in the source
	Dest    string // in the target; "" when failed
	Err     error  // StatusFailed: why
	// SourceErr is set when the photo is in the target but its source
	// copy could not be moved to the trash (or its source row removed);
	// the journal keeps it open and Recover retries.
	SourceErr error
}

// Progress phases.
const (
	PhasePlan    = "plan"
	PhaseMove    = "move"
	PhaseRecover = "recover"
)

// Progress is reported after every file.
type Progress struct {
	Phase             string
	Files, TotalFiles int
	Bytes, TotalBytes int64
	Current           string // the source path of the file being moved
}

// Options configure Run and Recover.
type Options struct {
	Logger     *slog.Logger
	OnProgress func(Progress)
	OnItem     func(Item)
	// OnRecord is called with the target's import record id once it is
	// written (Run only).
	OnRecord func(id int64)
	// BatchSize and BatchTime bound the target catalog transactions
	// (0: 200 files, 10 s).
	BatchSize int
	BatchTime time.Duration
	// Host is recorded in the import record (default: os.Hostname).
	Host string
}

// Result sums up a run.
type Result struct {
	Moved, Existing, Restored, Failed int
	// SourcePending counts photos in the target whose source step failed.
	SourcePending int
	Bytes         int64 // bytes copied across volumes
	RecordID      int64 // the target's import record; 0 when none was written
	Recovered     int   // files of interrupted moves finished first
	Cancelled     bool
}

var (
	// ErrSameLibrary refuses a move of a library into itself.
	ErrSameLibrary = errors.New("the target library is the source library")
	// ErrNotInSource: the hash is not in the source catalog.
	ErrNotInSource = errors.New("not in the catalog")
	// ErrTrashedInSource: the photo is in the source library's trash.
	ErrTrashedInSource = errors.New("in the trash; restore it first")
	// ErrSourceMismatch: the source file's bytes do not hash to its
	// catalog hash.
	ErrSourceMismatch = errors.New("the file does not match the catalog (it changed since it was imported)")
	// ErrTargetCopyMissing: the target's row of the content has no file.
	ErrTargetCopyMissing = errors.New("the target library's copy of this photo is missing; run verify there")
	// ErrTargetTrashMismatch: the target's trashed copy of the content
	// does not hash to it, so it is not restored.
	ErrTargetTrashMismatch = errors.New("the target library's trashed copy of this photo does not match the catalog; restore or delete it in the target's trash first")
	// ErrNoUserData: a library has no user.db open.
	ErrNoUserData = errors.New("the library's user data (user.db) is not open")
)

// NoSpaceError refuses a move across volumes that does not fit.
type NoSpaceError struct {
	Need, Free int64
}

func (e *NoSpaceError) Error() string {
	return fmt.Sprintf("not enough free space in the target library: %d bytes needed (with a 1 GiB margin), %d free", e.Need, e.Free)
}

// volumeShared and freeSpace are sameVolume and diskFree; tests replace
// them to move across "volumes" on one disk and to fill the disk.
var (
	volumeShared = sameVolume
	freeSpace    = diskFree
)

// crashAt is called at the named points of the protocol; a test panics
// there to leave the state of a crash.
var crashAt = func(string) {}

// spaceMargin is kept free on the target volume.
const spaceMargin = 1 << 30

// maxNameAttempts bounds the target names tried when one is taken between
// the check and the placement.
const maxNameAttempts = 100

// Message is the user-facing text of a per-file error: fixed texts, never
// a path or a raw OS error.
func Message(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrNotInSource), errors.Is(err, ErrTrashedInSource), errors.Is(err, ErrSourceMismatch),
		errors.Is(err, ErrTargetCopyMissing), errors.Is(err, ErrTargetTrashMismatch):
		return unwrapText(err)
	case errors.Is(err, library.ErrHashMismatch):
		return ErrSourceMismatch.Error()
	case errors.Is(err, library.ErrSourceChanged), errors.Is(err, library.ErrSizeChanged):
		return "the file changed while it was copied"
	case errors.Is(err, library.ErrTargetExists):
		return "the target library already has a file at this path"
	case errors.Is(err, library.ErrTrashTargetExists):
		return "the trash already holds a different file at this path"
	case errors.Is(err, library.ErrNotLibraryPath):
		return "the file's catalog path is not a library path"
	case errors.Is(err, library.ErrNotRegularFile):
		return "the path is a folder or a link, not a file"
	case errors.Is(err, importer.ErrRecordRestore):
		return "the catalog could not be updated"
	case errors.Is(err, fs.ErrNotExist):
		return "the file is missing"
	case isReadOnly(err):
		return "the library is on a read-only file system"
	case errors.Is(err, fs.ErrPermission):
		return "permission denied"
	case isNoSpace(err):
		return "no space left on the device"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	}
	return "the file could not be moved"
}

func unwrapText(err error) string {
	for _, s := range []error{ErrNotInSource, ErrTrashedInSource, ErrSourceMismatch, ErrTargetCopyMissing, ErrTargetTrashMismatch} {
		if errors.Is(err, s) {
			return s.Error()
		}
	}
	return err.Error()
}

// SameLibrary reports whether a and b are one library (one root, or one
// catalog file reached through two paths).
func SameLibrary(a, b *library.Library) bool {
	if a.Root() == b.Root() {
		return true
	}
	fa, errA := os.Stat(a.DBPath())
	fb, errB := os.Stat(b.DBPath())
	return errA == nil && errB == nil && os.SameFile(fa, fb)
}

// run is one Run or Recover.
type run struct {
	from, to Side
	opts     Options
	log      *slog.Logger
	j        *journal
	session  int64
	now      time.Time
	cross    bool // the libraries are on different volumes

	res      Result
	items    []userdata.ImportItem
	merge    []uint64              // hashes whose user data is merged at the next flush
	trashed  []userdata.TrashEntry // source rows trashed since the last flush (user.db trash rows)
	untrash  []uint64              // target rows restored since the last flush (user.db trash rows)
	finished []uint64              // hashes whose done line is written at the next flush
	all      []uint64              // every hash now in the target (ignored pairs)
	pending  []placed
	dirs     map[string]bool // target folders of unsynced copies
	reserved map[string]bool // target names chosen in this run (lower case)
	batchAt  time.Time
	prog     Progress
}

// placed is a file placed in the target whose row is not committed yet.
type placed struct {
	m    catalog.Media // the source row
	rel  string        // its target path
	mode string        // modeRename | modeCopy
	abs  string        // the placed file
}

func newRun(from, to Side, opts Options) *run {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 200
	}
	if opts.BatchTime <= 0 {
		opts.BatchTime = 10 * time.Second
	}
	if opts.Host == "" {
		opts.Host, _ = os.Hostname()
	}
	return &run{from: from, to: to, opts: opts, log: opts.Logger, now: time.Now(),
		dirs: map[string]bool{}, reserved: map[string]bool{}}
}

func checkSides(from, to Side) error {
	if from.Lib == nil || to.Lib == nil || from.DB == nil || to.DB == nil || from.Store == nil || to.Store == nil {
		return errors.New("transfer: both libraries must be open")
	}
	if from.User == nil || to.User == nil {
		return ErrNoUserData
	}
	if SameLibrary(from.Lib, to.Lib) {
		return ErrSameLibrary
	}
	return nil
}

func ref(ctx context.Context, s Side) (LibraryRef, error) {
	id, err := s.User.LibraryID(ctx)
	if err != nil {
		return LibraryRef{}, err
	}
	return LibraryRef{ID: id, Root: s.Lib.Root(), Name: s.Name}, nil
}

// dedupe returns hashes without repeats, in their first order.
func dedupe(hashes []uint64) []uint64 {
	seen := make(map[uint64]bool, len(hashes))
	out := make([]uint64, 0, len(hashes))
	for _, h := range hashes {
		if !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	return out
}

// Run moves the photos with these hashes from one library to the other
// (docs/design/library-move.md, "Per-file protocol"). Interrupted moves
// between the two libraries, in either direction, are finished first.
// Per-file failures are reported in OnItem and counted; the error is for
// what stops the whole run (the libraries, the record, the journal, no
// space). Cancelling ctx stops between files: a file already placed is
// always recorded.
func Run(ctx context.Context, from, to Side, hashes []uint64, opts Options) (Result, error) {
	if err := checkSides(from, to); err != nil {
		return Result{}, err
	}
	recovered, err := RecoverPair(ctx, from, to, opts)
	if err != nil {
		return Result{}, err
	}
	if ctx.Err() != nil {
		return Result{Recovered: recovered, Cancelled: true}, nil
	}
	r := newRun(from, to, opts)
	r.res.Recovered = recovered
	hashes = dedupe(hashes)
	bg := context.WithoutCancel(ctx)

	r.progress(PhasePlan, "")
	src, err := from.DB.ByHashes(ctx, hashes)
	if err != nil {
		return Result{}, err
	}
	dst, err := to.DB.ByHashes(ctx, hashes)
	if err != nil {
		return Result{}, err
	}
	r.cross = !volumeShared(from.Lib.Root(), to.Lib.Root())
	var bytes int64
	for _, h := range hashes {
		if m, ok := src[h]; ok && m.DeletedAt.IsZero() {
			if _, in := dst[h]; !in {
				bytes += m.Size
			}
		}
	}
	if r.cross && bytes > 0 {
		if free := freeSpace(to.Lib.Root()); free >= 0 && bytes+spaceMargin > free {
			return Result{Recovered: recovered}, &NoSpaceError{Need: bytes + spaceMargin, Free: free}
		}
	}
	if err := os.MkdirAll(to.Lib.TempDir(), 0o755); err != nil {
		return Result{}, err
	}
	srcRef, err := ref(ctx, from)
	if err != nil {
		return Result{}, err
	}
	dstRef, err := ref(ctx, to)
	if err != nil {
		return Result{}, err
	}
	if r.session, err = to.DB.BeginSession(ctx, from.Lib.Root(), userdata.ImportModeTransfer); err != nil {
		return Result{}, err
	}
	id, err := to.User.BeginImport(ctx, userdata.ImportRecord{
		StartedAt: r.now, Source: from.Lib.Root(), SourceName: from.Name, Mode: userdata.ImportModeTransfer,
		Host: opts.Host, CatalogSession: r.session,
	})
	if err != nil {
		return Result{}, err
	}
	r.res.RecordID = id
	if opts.OnRecord != nil {
		opts.OnRecord(id)
	}
	// A crash before the journal exists leaves a running record without
	// one: importrec.CloseCrashed closes it.
	crashAt("before-journal")
	if r.j, err = createJournal(to.Lib, Header{ID: id, Source: srcRef, Target: dstRef, StartedAt: r.now.Unix()}); err != nil {
		r.finishRecord(bg, userdata.ImportFailed)
		return r.res, err
	}
	defer r.j.close()
	r.log.Info("move started", "from", from.Lib.Root(), "to", to.Lib.Root(), "files", len(hashes),
		"bytes", bytes, "cross_volume", r.cross, "record", id)

	r.prog = Progress{Phase: PhaseMove, TotalFiles: len(hashes), TotalBytes: bytes}
	r.batchAt = time.Now()
	var fatal error
	for _, h := range hashes {
		if ctx.Err() != nil {
			r.res.Cancelled = true
			break
		}
		m, ok := src[h]
		switch {
		case !ok:
			r.failed(Item{Hash: h}, ErrNotInSource)
		case !m.DeletedAt.IsZero():
			r.failed(Item{Hash: h, RelPath: m.RelPath}, ErrTrashedInSource)
		default:
			b, in := dst[h]
			switch {
			case in && b.DeletedAt.IsZero():
				fatal = r.existing(bg, m, b)
			case in:
				fatal = r.restore(bg, m, b)
			default:
				fatal = r.place(ctx, m)
			}
		}
		if fatal != nil {
			break
		}
		r.prog.Files++
		r.progress(PhaseMove, m.RelPath)
		if len(r.pending) >= r.opts.BatchSize || time.Since(r.batchAt) >= r.opts.BatchTime {
			if fatal = r.flush(bg); fatal != nil {
				break
			}
		}
	}
	if err := r.flush(bg); err != nil && fatal == nil {
		fatal = err
	}
	r.finish(bg, fatal)
	return r.res, fatal
}

// finish closes the session, the import record and the journal.
func (r *run) finish(ctx context.Context, fatal error) {
	if _, err := r.to.User.MergeIgnored(ctx, r.from.User, r.all); err != nil {
		r.log.Warn("ignored pairs not copied", "err", err)
	}
	if err := r.to.DB.FinishSession(ctx, r.session, catalog.SessionStats{
		Imported: int64(r.res.Moved + r.res.Restored), Existing: int64(r.res.Existing), Errors: int64(r.res.Failed),
	}); err != nil {
		r.log.Warn("catalog session not finished", "err", err)
	}
	outcome := userdata.ImportDone
	switch {
	case fatal != nil:
		outcome = userdata.ImportFailed
	case r.res.Cancelled:
		outcome = userdata.ImportCancelled
	}
	r.finishRecord(ctx, outcome)
	if fatal == nil && r.j != nil && r.j.settled() {
		if err := r.j.end(); err != nil {
			r.log.Warn("journal not ended", "err", err)
		}
	}
	r.log.Info("move finished", "moved", r.res.Moved, "existing", r.res.Existing, "restored", r.res.Restored,
		"failed", r.res.Failed, "source_pending", r.res.SourcePending, "bytes", r.res.Bytes, "outcome", outcome, "err", fatal)
}

func (r *run) finishRecord(ctx context.Context, outcome string) {
	if r.res.RecordID == 0 {
		return
	}
	hashes := make([]uint64, len(r.items))
	for i, it := range r.items {
		hashes[i] = it.Hash
	}
	cover, _, err := r.to.DB.NewestOf(ctx, hashes)
	if err != nil {
		r.log.Warn("import record cover", "err", err)
	}
	var imported, existing int64
	for _, it := range r.items {
		if it.Status == userdata.ItemExisting {
			existing++
		} else {
			imported++
		}
	}
	if err := r.to.User.FinishImport(ctx, r.res.RecordID, userdata.ImportRecord{
		Outcome: outcome, Imported: imported, Existing: existing,
		Errors: int64(r.res.Failed), Bytes: r.res.Bytes, CatalogSession: r.session, Cover: cover,
	}, r.items); err != nil {
		r.log.Error("import record not finished", "record", r.res.RecordID, "err", err)
	}
}

func (r *run) progress(phase, current string) {
	if r.opts.OnProgress == nil {
		return
	}
	p := r.prog
	p.Phase, p.Current = phase, current
	r.opts.OnProgress(p)
}

func (r *run) report(it Item) {
	if r.opts.OnItem != nil {
		r.opts.OnItem(it)
	}
}

// failed reports a file left as it was.
func (r *run) failed(it Item, err error) {
	it.Status, it.Err = StatusFailed, err
	r.res.Failed++
	r.log.Info("file not moved", "hash", hexHash(it.Hash), "rel_path", it.RelPath, "err", err)
	r.report(it)
}

// succeeded counts a photo now in the target and reports it. Its user
// data, the user.db trash rows and its done line follow at the next flush
// (commit); without a source step its entry stays open.
func (r *run) succeeded(it Item) {
	switch it.Status {
	case StatusMoved:
		r.res.Moved++
		r.items = append(r.items, userdata.ImportItem{Hash: it.Hash, Status: userdata.ItemImported})
	case StatusExisting:
		r.res.Existing++
		r.items = append(r.items, userdata.ImportItem{Hash: it.Hash, Status: userdata.ItemExisting})
	case StatusRestored:
		r.res.Restored++
		r.items = append(r.items, userdata.ImportItem{Hash: it.Hash, Status: userdata.ItemRestored})
		r.untrash = append(r.untrash, it.Hash)
	}
	r.merge = append(r.merge, it.Hash)
	r.all = append(r.all, it.Hash)
	if it.SourceErr != nil {
		r.res.SourcePending++
		r.log.Warn("photo moved, but its source copy was not moved to the trash", "hash", hexHash(it.Hash),
			"rel_path", it.RelPath, "err", it.SourceErr)
	} else {
		r.finished = append(r.finished, it.Hash)
	}
	r.log.Info("file moved", "hash", hexHash(it.Hash), "status", it.Status, "rel_path", it.RelPath, "dest", it.Dest)
	r.report(it)
}

// existing handles content the target already holds live: its file must
// be there; the source copy goes to the source's trash.
func (r *run) existing(ctx context.Context, m, b catalog.Media) error {
	it := Item{Hash: m.Hash, RelPath: m.RelPath, Dest: b.RelPath, Status: StatusExisting}
	if _, err := r.to.Lib.LiveFile(b.RelPath); err != nil {
		r.log.Info("target copy unusable", "hash", hexHash(m.Hash), "rel_path", b.RelPath, "err", err)
		r.failed(it, ErrTargetCopyMissing)
		return nil
	}
	if err := r.j.begin(m.Hash, kindExisting, m.RelPath, b.RelPath, ""); err != nil {
		return err
	}
	it.SourceErr = r.trashSource(ctx, m)
	r.succeeded(it)
	return nil
}

// restore handles content in the target's trash: the trashed copy is
// hashed (as the importer does before a restore) and restored there (the
// one restore path), then the source copy goes to the source's trash.
// The target's user.db trash row goes at the next flush.
func (r *run) restore(ctx context.Context, m, b catalog.Media) error {
	it := Item{Hash: m.Hash, RelPath: m.RelPath, Status: StatusRestored}
	got, _, err := library.HashFileContext(ctx, r.to.Lib.TrashPath(b.RelPath))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		r.failed(it, ErrTargetCopyMissing)
		return nil
	case err != nil:
		r.failed(it, err)
		return nil
	case got != m.Hash:
		r.failed(it, ErrTargetTrashMismatch)
		return nil
	}
	if err := r.j.begin(m.Hash, kindRestore, m.RelPath, b.RelPath, ""); err != nil {
		return err
	}
	rel, err := importer.RestoreFromTrash(ctx, r.to.Lib, r.to.DB, b, nil)
	if err != nil {
		// When the file left the trash and could not be put back, the line
		// stays open: recovery finishes the restore.
		if _, lerr := os.Lstat(r.to.Lib.TrashPath(b.RelPath)); lerr == nil {
			if jerr := r.j.fail(m.Hash); jerr != nil {
				return jerr
			}
		}
		r.failed(it, err)
		return nil
	}
	it.Dest = rel
	it.SourceErr = r.trashSource(ctx, m)
	r.succeeded(it)
	return nil
}

// taken reports whether the target path c is in use: on disk, in the
// target catalog (case-insensitively, as rel_path is unique), or chosen
// for another file of this run.
func (r *run) taken(ctx context.Context) func(c string) (bool, error) {
	return func(c string) (bool, error) {
		if r.reserved[strings.ToLower(c)] {
			return true, nil
		}
		if _, err := os.Lstat(r.to.Lib.Abs(c)); err == nil {
			return true, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
		return r.to.DB.RelPathTaken(ctx, c)
	}
}

// place moves or copies a file the target does not hold (cases 3 and 4).
// Only an error that stops the run is returned.
func (r *run) place(ctx context.Context, m catalog.Media) error {
	it := Item{Hash: m.Hash, RelPath: m.RelPath}
	src, err := r.from.Lib.LiveFile(m.RelPath)
	if err != nil {
		r.failed(it, err)
		return nil
	}
	mode := modeRename
	if r.cross {
		mode = modeCopy
	} else {
		got, _, err := library.HashFileContext(ctx, src)
		switch {
		case errors.Is(err, context.Canceled):
			r.res.Cancelled = true
			return nil
		case err != nil:
			r.failed(it, err)
			return nil
		case got != m.Hash:
			r.failed(it, ErrSourceMismatch)
			return nil
		}
	}
	bg := context.WithoutCancel(ctx)
	for range maxNameAttempts {
		rel, err := library.UniqueName(m.RelPath, r.taken(bg))
		if err != nil {
			r.failed(it, err)
			return nil
		}
		r.reserved[strings.ToLower(rel)] = true
		if err := r.j.begin(m.Hash, kindMove, m.RelPath, rel, mode); err != nil {
			return err
		}
		if mode == modeRename {
			err = r.from.Lib.MoveFileTo(m.RelPath, r.to.Lib, rel)
			if library.IsCrossDevice(err) {
				// A mounted folder inside a library: copy this one.
				mode = modeCopy
				if err = r.j.begin(m.Hash, kindMove, m.RelPath, rel, mode); err != nil {
					return err
				}
			} else if err == nil {
				r.pending = append(r.pending, placed{m: m, rel: rel, mode: modeRename, abs: r.to.Lib.Abs(rel)})
				r.prog.Bytes += m.Size
				return nil
			}
		}
		var abs string
		if mode == modeCopy {
			abs, err = r.to.Lib.LiveTarget(rel)
			if err == nil {
				var res library.CopyResult
				res, err = library.CopyReadBackDeferSync(ctx, src, abs, r.to.Lib.TempDir(), &m.Hash)
				if err == nil {
					r.dirs[filepath.Dir(abs)] = true
					r.pending = append(r.pending, placed{m: m, rel: rel, mode: modeCopy, abs: abs})
					r.res.Bytes += res.Size
					r.prog.Bytes += res.Size
					return nil
				}
			}
		}
		if errors.Is(err, fs.ErrExist) || errors.Is(err, library.ErrTargetExists) {
			continue // taken meanwhile: the next name (rel stays reserved)
		}
		if jerr := r.j.fail(m.Hash); jerr != nil {
			return jerr
		}
		if errors.Is(err, context.Canceled) {
			r.res.Cancelled = true
			return nil
		}
		r.failed(it, err)
		return nil
	}
	if err := r.j.fail(m.Hash); err != nil {
		return err
	}
	r.failed(it, fmt.Errorf("%w: every name tried was taken", library.ErrTargetExists))
	return nil
}

// targetRow is the target's catalog row for the source row m placed at
// rel: m's metadata, imported now in this run's session.
func (r *run) targetRow(m catalog.Media, rel string, renditions bool) catalog.Media {
	row := m
	row.ID = 0
	row.RelPath = rel
	row.ImportedAt = r.now
	row.ImportSessionID = r.session
	row.DeletedAt = time.Time{}
	row.Rating = 0
	if !renditions && row.ThumbState == catalog.ThumbStateOK {
		row.ThumbState, row.ThumbError, row.ThumbAt = catalog.ThumbStatePending, "", time.Time{}
	}
	return row
}

// flush commits the placed files of the batch: their folders synced,
// renditions copied, rows inserted (one transaction; one by one when it
// fails, undoing the placement of a row that is not inserted), then the
// source step of each, and the user data of every photo now in the
// target (commit). The batch is taken out of pending first, so it is
// never flushed twice, and every file of it is reported even when a
// journal write fails; that error, which stops the run, is returned at
// the end.
func (r *run) flush(ctx context.Context) error {
	defer func() { r.batchAt = time.Now() }()
	batch := r.pending
	r.pending = nil
	seen := map[string]bool{}
	for d := range r.dirs {
		syncParents(r.to.Lib.Root(), d, seen)
	}
	clear(r.dirs)
	rows := make([]catalog.Media, len(batch))
	for i, p := range batch {
		ok, err := r.to.Store.CopyFrom(ctx, r.from.Store, p.m.Hash)
		if err != nil {
			r.log.Warn("renditions not copied; they will be generated", "hash", hexHash(p.m.Hash), "err", err)
		}
		rows[i] = r.targetRow(p.m, p.rel, ok)
	}
	crashAt("before-insert")
	inserted := make([]bool, len(rows))
	if len(rows) > 0 {
		if err := r.insertBatch(ctx, rows); err == nil {
			for i := range inserted {
				inserted[i] = true
			}
		} else {
			r.log.Warn("batch insert failed; inserting one by one", "files", len(rows), "err", err)
			for i := range rows {
				ok, err := r.to.DB.InsertMedia(ctx, nil, &rows[i])
				if err == nil && !ok && r.recordedAt(ctx, rows[i].Hash, rows[i].RelPath) {
					ok = true // recorded before (not by another row)
				}
				switch {
				case err != nil:
					r.log.Error("target row not inserted", "hash", hexHash(rows[i].Hash), "err", err)
				case !ok:
					r.log.Error("target row not inserted: the target catalog has another row of this content",
						"hash", hexHash(rows[i].Hash))
				default:
					inserted[i] = true
				}
			}
		}
	}
	var jerr error
	for i, p := range batch {
		it := Item{Hash: p.m.Hash, RelPath: p.m.RelPath}
		if !inserted[i] {
			if err := r.undo(p); err != nil {
				// The journal line stays open: recovery inserts the row.
				r.log.Error("placement not undone", "hash", hexHash(p.m.Hash), "dest", p.rel, "err", err)
			} else if err := r.j.fail(p.m.Hash); err != nil && jerr == nil {
				jerr = err // the line stays open: recovery finds nothing placed
			}
			r.failed(it, importer.ErrRecordRestore)
			continue
		}
		it.Status, it.Dest = StatusMoved, p.rel
		if p.mode == modeRename {
			it.SourceErr = r.forgetSource(ctx, p.m)
		} else {
			it.SourceErr = r.trashSource(ctx, p.m)
		}
		r.succeeded(it)
	}
	if err := r.commit(ctx); err != nil && jerr == nil {
		jerr = err
	}
	return jerr
}

// commit writes what follows the source steps since the last flush: the
// user.db trash rows of the source rows trashed, those of the target rows
// restored removed, the user data merged, and only then the done lines,
// so a crash before them leaves the entries open and recovery does it
// all again (each step is idempotent). When one of the user.db steps
// fails the entries stay open too. A journal error is returned.
func (r *run) commit(ctx context.Context) error {
	ok := true
	if len(r.trashed) > 0 {
		at := map[int64][]uint64{}
		for _, t := range r.trashed {
			at[t.DeletedAt.Unix()] = append(at[t.DeletedAt.Unix()], t.Hash)
		}
		for sec, hashes := range at {
			if err := r.from.User.Trash(ctx, hashes, time.Unix(sec, 0)); err != nil {
				r.log.Error("source trash not recorded in user.db", "err", err)
				ok = false
			}
		}
		r.trashed = r.trashed[:0]
	}
	if len(r.untrash) > 0 {
		if err := r.to.User.Untrash(ctx, r.untrash); err != nil {
			r.log.Error("restore not recorded in the target's user.db", "err", err)
			ok = false
		}
		r.untrash = r.untrash[:0]
	}
	crashAt("before-merge")
	if len(r.merge) > 0 {
		if res, err := r.to.User.MergeFrom(ctx, r.from.User, r.merge); err != nil {
			r.log.Error("user data not merged", "files", len(r.merge), "err", err)
			ok = false
		} else {
			r.log.Info("user data merged", "ratings", res.Ratings, "tags", res.Tags, "albums", res.Albums,
				"tags_created", res.TagsCreated, "albums_created", res.AlbumsCreated)
		}
		r.merge = r.merge[:0]
	}
	finished := r.finished
	r.finished = nil
	if !ok {
		return nil // the entries stay open: recovery merges again
	}
	for _, h := range finished {
		if err := r.j.done(h); err != nil {
			return err
		}
	}
	return nil
}

// recordedAt reports whether the target's row of h is live at rel.
func (r *run) recordedAt(ctx context.Context, h uint64, rel string) bool {
	b, ok, err := row(ctx, r.to.DB, h)
	return err == nil && ok && b.DeletedAt.IsZero() && strings.EqualFold(b.RelPath, rel)
}

// syncParents syncs dir and its parents up to root, the folders a
// placement may have created, each once per seen.
func syncParents(root, dir string, seen map[string]bool) {
	root = filepath.Clean(root)
	for d := filepath.Clean(dir); !seen[d]; {
		seen[d] = true
		library.SyncDir(d)
		p := filepath.Dir(d)
		if d == root || p == d || !strings.HasPrefix(p, root) {
			return
		}
		d = p
	}
}

// insertBatch inserts rows in one transaction; any row that is not
// inserted (an error, or its hash appeared meanwhile) fails the batch.
func (r *run) insertBatch(ctx context.Context, rows []catalog.Media) error {
	tx, err := r.to.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i := range rows {
		ok, err := r.to.DB.InsertMedia(ctx, tx, &rows[i])
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("catalog: %016x is already in the target catalog", rows[i].Hash)
		}
	}
	return tx.Commit()
}

// undo takes back a placement whose row could not be recorded: a renamed
// file is renamed back (never replacing), a copy is removed (the source
// still holds the original, which the copy was verified to equal).
func (r *run) undo(p placed) error {
	if p.mode == modeRename {
		return r.to.Lib.MoveFileTo(p.rel, r.from.Lib, p.m.RelPath)
	}
	abs, err := r.to.Lib.LiveFile(p.rel)
	if err != nil {
		return err
	}
	if err := os.Remove(abs); err != nil {
		return err
	}
	library.SyncDir(filepath.Dir(abs))
	return nil
}

// forgetSource ends a same-volume move in the source: the file has gone,
// so its row is removed (with its near-duplicate pairs) and its
// renditions deleted. User data stays (as for a deletion).
func (r *run) forgetSource(ctx context.Context, m catalog.Media) error {
	if err := r.from.DB.Remove(ctx, m.Hash); err != nil {
		return err
	}
	if err := r.from.Store.Delete(ctx, m.Hash); err != nil {
		r.log.Warn("source renditions not deleted", "hash", hexHash(m.Hash), "err", err)
	}
	return nil
}

// trashSource moves the source copy to the source library's trash, as
// the trash endpoint does: one rename, then deleted_at in the catalog (the
// rename is undone when that fails); the user.db trash row is written at
// the next flush. When the trash already holds another file at the
// photo's path, a free variant of the path is journalled, used and
// recorded, so recovery finds the file after a crash between the rename
// and the catalog update. A source file that is gone already (removed
// outside picmover; the photo is in the target) has its row forgotten.
func (r *run) trashSource(ctx context.Context, m catalog.Media) error {
	err := r.from.Lib.MoveToTrash(m.RelPath)
	if err == nil {
		crashAt("after-movetotrash")
		if err := r.from.DB.SetDeleted(ctx, m.Hash, r.now); err != nil {
			if rerr := r.from.Lib.RestoreTo(m.RelPath, m.RelPath); rerr != nil {
				r.log.Error("undo move to trash", "hash", hexHash(m.Hash), "err", rerr)
			}
			return err
		}
		r.trashed = append(r.trashed, userdata.TrashEntry{Hash: m.Hash, DeletedAt: r.now})
		return nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		r.log.Warn("source copy missing; its row is removed", "hash", hexHash(m.Hash), "rel_path", m.RelPath)
		return r.forgetSource(ctx, m)
	}
	if !errors.Is(err, library.ErrTrashTargetExists) {
		return err
	}
	taken := func(c string) (bool, error) {
		if strings.EqualFold(c, m.RelPath) {
			return true, nil
		}
		if _, err := os.Lstat(r.from.Lib.TrashPath(c)); err == nil {
			return true, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
		return r.from.DB.RelPathTaken(ctx, c)
	}
	alt, err := library.UniqueName(m.RelPath, taken)
	if err != nil {
		return err
	}
	if err := r.j.trash(m.Hash, alt); err != nil {
		return err
	}
	if err := r.from.Lib.MoveToTrashAs(m.RelPath, alt); err != nil {
		return err
	}
	crashAt("after-movetotrash")
	if err := r.from.DB.SetDeletedAs(ctx, m.Hash, r.now, alt); err != nil {
		if rerr := r.from.Lib.RestoreTo(alt, m.RelPath); rerr != nil {
			r.log.Error("undo move to trash", "hash", hexHash(m.Hash), "err", rerr)
		}
		return err
	}
	r.trashed = append(r.trashed, userdata.TrashEntry{Hash: m.Hash, DeletedAt: r.now})
	return nil
}

// markTrashed finishes a source step whose rename reached the source
// trash (at the catalog path trashRel) before a crash: the row is marked
// trashed there.
func (r *run) markTrashed(ctx context.Context, m catalog.Media, trashRel string) error {
	var err error
	if trashRel == m.RelPath {
		err = r.from.DB.SetDeleted(ctx, m.Hash, r.now)
	} else {
		err = r.from.DB.SetDeletedAs(ctx, m.Hash, r.now, trashRel)
	}
	if err != nil {
		return err
	}
	r.trashed = append(r.trashed, userdata.TrashEntry{Hash: m.Hash, DeletedAt: r.now})
	return nil
}

func isReadOnly(err error) bool { return errors.Is(err, syscall.EROFS) }
func isNoSpace(err error) bool  { return errors.Is(err, syscall.ENOSPC) }
