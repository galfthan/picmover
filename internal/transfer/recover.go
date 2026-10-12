package transfer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/library"
	"picmover/internal/userdata"
)

var (
	// ErrInterrupted: an interrupted move never placed the file; it stayed
	// in the source library.
	ErrInterrupted = errors.New("the move was interrupted before the file was placed; it stayed in the source library")
	// ErrLostTrack: a journal names a file that is in neither library's
	// expected place. It cannot happen with the protocol's order; it is
	// reported with the paths in the log so a person can look.
	ErrLostTrack = errors.New("the file is neither in the source library nor in the target library; see the log")
	// ErrUnrecorded: the file reached the target but its source row is
	// gone, so its metadata is lost; record it with rebuild.
	ErrUnrecorded = errors.New("the file is in the target library but in neither catalog; run rebuild there")
)

// RecoverPair finishes the interrupted moves between a and b, in either
// direction (journals in b from a, and in a from b), and returns the
// number of files it finished. Moves from or into other libraries are
// left alone.
func RecoverPair(ctx context.Context, a, b Side, opts Options) (int, error) {
	if err := checkSides(a, b); err != nil {
		return 0, err
	}
	n, err := recoverInto(ctx, a, b, opts)
	if err != nil {
		return n, err
	}
	k, err := recoverInto(ctx, b, a, opts)
	return n + k, err
}

// recoverInto finishes the interrupted moves from `from` whose journals
// are in `to`.
func recoverInto(ctx context.Context, from, to Side, opts Options) (int, error) {
	list, err := ListInterrupted(to.Lib, time.Now())
	if err != nil {
		return 0, err
	}
	srcID, err := from.User.LibraryID(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, in := range list {
		if !IsSource(in, srcID, from.Lib.Root()) {
			continue
		}
		res, err := Recover(ctx, from, to, in.Path, opts)
		n += res.Recovered
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// IsSource reports whether the interrupted move in came from the library
// with this library id at root: the ids must match, and the roots too
// unless the journal's source root holds no library any more (the
// library folder was moved or renamed). A copy of a library shares its
// id, so the root is what tells them apart while both exist.
func IsSource(in Interrupted, libraryID, root string) bool {
	if in.ID == 0 || in.Source.ID == "" || in.Source.ID != libraryID {
		return false
	}
	if in.Source.Root == root {
		return true
	}
	_, err := os.Stat(filepath.Join(in.Source.Root, library.DBName))
	return errors.Is(err, fs.ErrNotExist)
}

// Recover finishes the interrupted move journalled at path, from `from`
// into `to` (both open, locked and serialised by the caller), following
// the table in docs/design/library-move.md ("The journal and recovery"):
// a file that reached the target goes forward (its row inserted, its
// source step done), a file that did not is left in the source. The
// target's import record is closed (outcome failed) when it is still
// running. Result.Recovered counts the files finished; the other counts
// are those of the files looked at.
func Recover(ctx context.Context, from, to Side, path string, opts Options) (Result, error) {
	if err := checkSides(from, to); err != nil {
		return Result{}, err
	}
	p, err := readJournal(path)
	if err != nil {
		return Result{}, err
	}
	if p.ended {
		return Result{}, nil
	}
	r := newRun(from, to, opts)
	r.res.RecordID = p.header.ID
	bg := context.WithoutCancel(ctx)
	rec, recErr := to.User.GetImport(bg, p.header.ID)
	if recErr == nil {
		r.session = rec.CatalogSession
	}
	if r.j, err = openJournal(path); err != nil {
		return Result{}, err
	}
	defer r.j.close()
	r.log.Info("finishing an interrupted move", "journal", path, "from", from.Lib.Root(), "to", to.Lib.Root())

	entries := p.ordered()
	open := 0
	for _, e := range entries {
		if !e.done && !e.failed {
			open++
		}
	}
	r.prog = Progress{Phase: PhaseRecover, TotalFiles: open}
	priorFailed := 0
	for _, e := range entries {
		switch {
		case e.done:
			r.items = append(r.items, userdata.ImportItem{Hash: e.hash, Status: itemStatus(e.kind)})
			r.all = append(r.all, e.hash)
			continue
		case e.failed:
			priorFailed++
			continue
		}
		before := r.res.Moved + r.res.Existing + r.res.Restored
		r.recoverEntry(bg, e)
		if r.res.Moved+r.res.Existing+r.res.Restored > before {
			r.res.Recovered++
		}
		r.prog.Files++
		r.progress(PhaseRecover, e.rel)
	}
	if err := r.flush(bg); err != nil {
		return r.res, err
	}
	if _, err := to.User.MergeIgnored(bg, from.User, r.all); err != nil {
		r.log.Warn("ignored pairs not copied", "err", err)
	}
	if recErr == nil && rec.Outcome == userdata.ImportRunning {
		r.res.Failed += priorFailed
		r.finishRecord(bg, userdata.ImportFailed)
		r.res.Failed -= priorFailed
	}
	if r.res.SourcePending == 0 {
		if err := r.j.end(); err != nil {
			return r.res, err
		}
	}
	r.log.Info("interrupted move finished", "recovered", r.res.Recovered, "failed", r.res.Failed,
		"source_pending", r.res.SourcePending)
	return r.res, nil
}

func itemStatus(kind string) string {
	switch kind {
	case kindExisting:
		return userdata.ItemExisting
	case kindRestore:
		return userdata.ItemRestored
	}
	return userdata.ItemImported
}

func statusOf(kind string) string {
	switch kind {
	case kindExisting:
		return StatusExisting
	case kindRestore:
		return StatusRestored
	}
	return StatusMoved
}

// row returns the catalog row of h, ok false when there is none.
func row(ctx context.Context, db *catalog.DB, h uint64) (catalog.Media, bool, error) {
	m, err := db.GetByHash(ctx, h)
	if errors.Is(err, sql.ErrNoRows) {
		return catalog.Media{}, false, nil
	}
	return m, err == nil, err
}

// sourcePresent reports whether the source row m is live with its file.
func (r *run) sourcePresent(m catalog.Media, ok bool) bool {
	if !ok || !m.DeletedAt.IsZero() {
		return false
	}
	_, err := r.from.Lib.LiveFile(m.RelPath)
	return err == nil
}

// finishSource does the source step of a photo now in the target: the
// source copy to the trash when it is there, else the source row
// forgotten (a rename moved the file), else nothing (done before).
func (r *run) finishSource(ctx context.Context, m catalog.Media, ok bool) error {
	switch {
	case r.sourcePresent(m, ok):
		return r.trashSource(ctx, m)
	case ok && m.DeletedAt.IsZero():
		return r.forgetSource(ctx, m)
	}
	return nil
}

// recoverEntry finishes one open journal entry.
func (r *run) recoverEntry(ctx context.Context, e *entry) {
	it := Item{Hash: e.hash, RelPath: e.rel}
	fail := func(err error) {
		if jerr := r.j.fail(e.hash); jerr != nil {
			r.log.Warn("journal", "err", jerr)
		}
		r.failed(it, err)
	}
	m, mok, err := row(ctx, r.from.DB, e.hash)
	if err != nil {
		r.failed(it, err) // the line stays open: try again later
		return
	}
	b, bok, err := row(ctx, r.to.DB, e.hash)
	if err != nil {
		r.failed(it, err)
		return
	}
	if bok && (e.kind == kindMove || b.DeletedAt.IsZero()) {
		// The photo reached the target: go forward.
		it.Status, it.Dest = statusOf(e.kind), b.RelPath
		it.SourceErr = r.finishSource(ctx, m, mok)
		r.succeeded(it)
		return
	}
	if e.kind != kindMove {
		fail(ErrInterrupted) // the restore never committed
		return
	}
	abs, ferr := r.to.Lib.LiveFile(e.to)
	placedThere := false
	if ferr == nil {
		got, _, herr := library.HashFileContext(ctx, abs)
		switch {
		case herr != nil:
			r.log.Warn("placed file unreadable", "hash", hexHash(e.hash), "dest", e.to, "err", herr)
		case got == e.hash:
			placedThere = true
		default:
			r.log.Warn("another file is at the target path; left alone", "hash", hexHash(e.hash), "dest", e.to,
				"found", hexHash(got))
		}
	}
	switch {
	case placedThere && mok:
		ok, err := r.to.Store.CopyFrom(ctx, r.from.Store, e.hash)
		if err != nil {
			r.log.Warn("renditions not copied; they will be generated", "hash", hexHash(e.hash), "err", err)
		}
		nr := r.targetRow(m, e.to, ok)
		if _, err := r.to.DB.InsertMedia(ctx, nil, &nr); err != nil {
			r.failed(it, fmt.Errorf("record the placed file: %w", err)) // stays open
			return
		}
		it.Status, it.Dest = StatusMoved, e.to
		it.SourceErr = r.finishSource(ctx, m, mok)
		r.succeeded(it)
	case placedThere:
		r.log.Error("placed file whose source row is gone", "hash", hexHash(e.hash), "dest", e.to)
		fail(ErrUnrecorded)
	case r.sourcePresent(m, mok):
		fail(ErrInterrupted)
	default:
		r.log.Error("interrupted move lost track of a file", "hash", hexHash(e.hash), "source", e.rel,
			"target", e.to, "source_row", mok)
		fail(ErrLostTrack)
	}
}
