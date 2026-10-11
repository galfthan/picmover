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
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/importer"
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
	user *userdata.DB
	log  *slog.Logger

	mu    sync.Mutex
	id    int64
	err   error
	seen  map[uint64]bool
	items []userdata.ImportItem
}

// Begin records the start of a run from source (cleaned; for a rebuild the
// library root) in mode (userdata.ImportMode*). user nil (no user.db, or
// it could not be opened: openErr says why) gives a Recorder that records
// nothing and reports the failure. log may be nil.
func Begin(ctx context.Context, user *userdata.DB, openErr error, source, mode string, log *slog.Logger) *Recorder {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	r := &Recorder{user: user, log: log, seen: map[uint64]bool{}}
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
// keeps its first status).
func (r *Recorder) OnResult(res importer.Result) {
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

// Finish records the end of the run: outcome (userdata.Import*), the
// counts from stats, the catalog session and the items collected. It
// uses a context that is not cancelled with ctx, so a cancelled run is
// still recorded. It returns Err.
func (r *Recorder) Finish(ctx context.Context, outcome string, stats importer.Stats, sessionID int64) error {
	id := r.ID()
	if id == 0 {
		return r.Err()
	}
	r.mu.Lock()
	items := r.items
	r.mu.Unlock()
	rec := userdata.ImportRecord{Outcome: outcome, Imported: stats.Imported, Existing: stats.Existing,
		Duplicates: stats.Duplicates, Skipped: stats.SkippedSmall + stats.NonMedia, Errors: stats.Errors,
		Bytes: stats.BytesCopied, CatalogSession: sessionID}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	if err := r.user.FinishImport(wctx, id, rec, items); err != nil {
		r.fail("finish", err)
		return r.Err()
	}
	r.log.Info("import record written", "import", id, "outcome", outcome, "items", len(items))
	return nil
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
			Skipped: s.SkippedSmall + s.NonMedia, Errors: s.Errors, Hashes: s.Hashes})
	}
	return user.BackfillImports(ctx, in)
}
