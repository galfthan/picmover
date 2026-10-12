// Package importrec writes the import history of user.db (see
// docs/design/imports.md) for the CLI and the server: a Recorder records
// one import or rebuild run, Backfill copies the catalog's import sessions
// once per library.
//
// Recording never fails an import: every failure is logged at error level
// and kept in Recorder.Err for the summary.
package importrec

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/importer"
	"picmover/internal/transfer"
	"picmover/internal/userdata"
)

// hostName is os.Hostname; tests replace it.
var hostName = func() string {
	h, _ := os.Hostname()
	return h
}

// Recorder records one run. Begin writes the record (outcome running);
// OnResult collects the items from the importer's results; Finish writes
// the counts, the outcome and the items. Its methods are safe for
// concurrent use (OnResult is called from pipeline goroutines).
type Recorder struct {
	user  *userdata.DB
	log   *slog.Logger
	keeps bool // the mode keeps items (userdata.ModeKeepsItems)

	mu      sync.Mutex
	id      int64
	err     error
	session int64 // the catalog session written by SetSession
	seen    map[uint64]bool
	items   []userdata.ImportItem
}

// Begin records the start of a run from source (cleaned; for a rebuild the
// library root) in mode (userdata.ImportMode*). user nil (no user.db, or
// it could not be opened: openErr says why) gives a Recorder that records
// nothing and reports the failure. log may be nil.
func Begin(ctx context.Context, user *userdata.DB, openErr error, source, mode string, log *slog.Logger) *Recorder {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	r := &Recorder{user: user, log: log, keeps: userdata.ModeKeepsItems(mode), seen: map[uint64]bool{}}
	if user == nil {
		if openErr == nil {
			openErr = errors.New("no user.db")
		}
		r.fail("open user.db", openErr)
		return r
	}
	if source != "" {
		source = filepath.Clean(source)
	}
	id, err := user.BeginImport(ctx, userdata.ImportRecord{Source: source, Mode: mode, Host: hostName()})
	if err != nil {
		r.fail("begin", err)
		return r
	}
	r.id = id
	log.Info("import record begun", "import", id)
	return r
}

func (r *Recorder) fail(what string, err error) {
	r.log.Error("import record could not be written", "step", what, "err", err)
	r.mu.Lock()
	if r.err == nil {
		r.err = fmt.Errorf("import record: %s: %w", what, err)
	}
	r.mu.Unlock()
}

// ID is the record's id; 0 when it could not be written.
func (r *Recorder) ID() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.id
}

// Err is the first failure to write the record, nil when none.
func (r *Recorder) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

// ItemStatus maps an importer result to an item status: imported,
// restored or existing; ok is false for results that are not items
// (duplicates, skipped, non-media, errors) and for results without a
// hash.
func ItemStatus(res importer.Result) (status string, ok bool) {
	if res.Hash == 0 {
		return "", false
	}
	switch {
	case res.Status == importer.StatusImported && res.Restored:
		return userdata.ItemRestored, true
	case res.Status == importer.StatusImported:
		return userdata.ItemImported, true
	case res.Status == importer.StatusExisting:
		return userdata.ItemExisting, true
	}
	return "", false
}

// OnResult collects the item of an importer result (a hash seen before
// keeps its first status). A rebuild collects none: its record keeps no
// items (userdata.ModeKeepsItems).
func (r *Recorder) OnResult(res importer.Result) {
	if !r.keeps {
		return
	}
	status, ok := ItemStatus(res)
	if !ok {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen[res.Hash] {
		return
	}
	r.seen[res.Hash] = true
	r.items = append(r.items, userdata.ImportItem{Hash: res.Hash, Status: status})
}

// Outcome maps the error of importer.Run to the record's outcome: done
// (a failed thumbnail stage included: every file is recorded), cancelled
// or failed.
func Outcome(runErr error) string {
	var stageErr *importer.ThumbStageError
	switch {
	case runErr == nil, errors.As(runErr, &stageErr):
		return userdata.ImportDone
	case errors.Is(runErr, context.Canceled):
		return userdata.ImportCancelled
	}
	return userdata.ImportFailed
}

// SetSession writes the catalog session of the run on the record as
// soon as the importer has one (from its progress callback), so the
// record of a process that dies keeps its link to the rows it committed
// (CloseCrashed). It writes once; 0 and later calls are ignored. A
// failure is logged at error level and kept in Err.
func (r *Recorder) SetSession(ctx context.Context, session int64) {
	if session == 0 {
		return
	}
	r.mu.Lock()
	id, done := r.id, r.session != 0
	if id != 0 && !done {
		r.session = session
	}
	r.mu.Unlock()
	if id == 0 || done {
		return
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	if err := r.user.SetImportSession(wctx, id, session); err != nil {
		r.fail("session", err)
	}
}

// Newest finds the newest live row among hashes (catalog.DB.NewestOf):
// the cover of a finished record.
type Newest interface {
	NewestOf(ctx context.Context, hashes []uint64) (hash uint64, ok bool, err error)
}

// Finish records the end of the run: outcome (userdata.Import*), the
// counts from stats, the catalog session, the items collected and their
// cover, the newest of them in cat (nil, or a failure to read it: the
// last item). It uses a context that is not cancelled with ctx, so a
// cancelled run is still recorded. It returns Err.
func (r *Recorder) Finish(ctx context.Context, outcome string, stats importer.Stats, sessionID int64, cat Newest) error {
	id := r.ID()
	if id == 0 {
		return r.Err()
	}
	r.mu.Lock()
	items := r.items
	r.mu.Unlock()
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	var cover uint64
	if cat != nil && len(items) > 0 {
		hashes := make([]uint64, len(items))
		for i, it := range items {
			hashes[i] = it.Hash
		}
		if h, ok, err := cat.NewestOf(wctx, hashes); err != nil {
			r.log.Warn("import record cover", "err", err)
		} else if ok {
			cover = h
		}
	}
	rec := userdata.ImportRecord{Outcome: outcome, Imported: stats.Imported, Existing: stats.Existing,
		Duplicates: stats.Duplicates, Skipped: stats.SkippedSmall + stats.NonMedia, Errors: stats.Errors,
		Bytes: stats.BytesCopied, CatalogSession: sessionID, Cover: cover}
	if err := r.user.FinishImport(wctx, id, rec, items); err != nil {
		r.fail("finish", err)
		return r.Err()
	}
	r.log.Info("import record written", "import", id, "outcome", outcome, "items", len(items))
	return nil
}

// SessionRows returns the rows of a catalog session: their hashes and
// the newest live one (catalog.DB.SessionHashes).
type SessionRows func(ctx context.Context, session int64) (hashes []uint64, newest uint64, err error)

// CloseCrashed closes the records left running by a process that died:
// every running record for which owned (nil: none) is false gets outcome
// failed, finished now, and the rows of its catalog session (when it is
// known and rows is not nil) as its imported items. A session whose rows
// cannot be read closes the record without items (logged). Records of
// mode transfer are left to the move's recovery, which closes them from
// their journal; one whose journal does not exist (the process died
// between writing the record and creating the journal: nothing was
// moved) is closed here. It returns the number of records closed.
func CloseCrashed(ctx context.Context, user *userdata.DB, rows SessionRows, owned func(id int64) bool, log *slog.Logger) (int, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	running, err := user.RunningImports(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, rec := range running {
		if owned != nil && owned(rec.ID) {
			continue
		}
		if rec.Mode == userdata.ImportModeTransfer && !journalMissing(user, rec.ID) {
			continue // closed by the move's recovery, from its journal
		}
		var (
			hashes []uint64
			newest uint64
		)
		if rec.CatalogSession != 0 && rows != nil && rec.KeepsItems() {
			if hashes, newest, err = rows(ctx, rec.CatalogSession); err != nil {
				log.Warn("crashed import record: catalog rows", "import", rec.ID, "session", rec.CatalogSession, "err", err)
				hashes, newest = nil, 0
			}
		}
		closed, err := user.CloseCrashedImport(ctx, rec.ID, hashes, newest)
		if err != nil {
			return n, err
		}
		if closed {
			n++
			log.Info("crashed import record closed", "import", rec.ID, "items", len(hashes))
		}
	}
	return n, nil
}

// journalMissing reports whether the move journal of the transfer record
// id is certainly not there: user.db is at the library root, the journal
// in its transfers folder. Anything but "does not exist" counts as there.
func journalMissing(user *userdata.DB, id int64) bool {
	if user.Path() == "" {
		return false
	}
	_, err := os.Lstat(transfer.JournalPath(filepath.Dir(user.Path()), id))
	return errors.Is(err, fs.ErrNotExist)
}

// Backfill copies the catalog's import sessions into user's import
// history the first time (userdata.BackfillImports); it does nothing when
// that has happened. It returns the number of records written.
func Backfill(ctx context.Context, cat *catalog.DB, user *userdata.DB) (int, error) {
	return backfill(ctx, user, cat.Sessions)
}

// BackfillFile is Backfill from the catalog file at path, read without
// writing anything to it (catalog.SessionsFile): a rebuild runs it on
// the old catalog before renaming it away.
func BackfillFile(ctx context.Context, path string, user *userdata.DB) (int, error) {
	return backfill(ctx, user, func(ctx context.Context) ([]catalog.Session, error) {
		return catalog.SessionsFile(ctx, path)
	})
}

func backfill(ctx context.Context, user *userdata.DB, list func(context.Context) ([]catalog.Session, error)) (int, error) {
	done, err := user.ImportsBackfilled(ctx)
	if err != nil || done {
		return 0, err
	}
	sessions, err := list(ctx)
	if err != nil {
		return 0, err
	}
	in := make([]userdata.BackfillSession, 0, len(sessions))
	for _, s := range sessions {
		in = append(in, userdata.BackfillSession{CatalogSession: s.ID, StartedAt: s.StartedAt, FinishedAt: s.FinishedAt,
			Source: s.Source, Mode: s.Mode, Imported: s.Imported, Existing: s.Existing, Duplicates: s.Duplicates,
			Skipped: s.SkippedSmall + s.NonMedia, Errors: s.Errors, Hashes: s.Hashes, Cover: s.Newest})
	}
	return user.BackfillImports(ctx, in)
}
