package transfer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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
	// ErrOtherTarget: Recover was given a journal addressed to another
	// library (one that came along when a library folder was copied).
	ErrOtherTarget = errors.New("transfer: the journal belongs to another library")
)

// RecoverPair finishes the interrupted moves between a and b, in either
// direction (journals in b from a, and in a from b), and returns the
// number of files it finished. Moves from or into other libraries are
// left alone. Cancelling ctx stops between files (Recover).
func RecoverPair(ctx context.Context, a, b Side, opts Options) (int, error) {
	if err := checkSides(a, b); err != nil {
		return 0, err
	}
	n, err := recoverInto(ctx, a, b, opts)
	if err != nil || ctx.Err() != nil {
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
		if ctx.Err() != nil {
			break
		}
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
	return sameRoot(in.Source.Root, root) || !holdsLibrary(in.Source.Root)
}

// Recover finishes the interrupted move journalled at path, from `from`
// into `to` (both open, locked and serialised by the caller), following
// the table in docs/design/library-move.md ("The journal and recovery"):
// a file that reached the target goes forward (its row inserted, its
// source step done), a file that did not is left in the source. A
// journal addressed to another target is refused (ErrOtherTarget). An
// entry whose state cannot be read, or whose step fails, stays open: the
// journal is ended, and the target's import record closed (outcome
// failed) when it is still running, only when no entry is open, so the
// move stays interrupted and a later Recover tries again. Cancelling ctx
// stops between entries (Result.Cancelled); an entry is never stopped
// half-way. Result.Recovered counts the files finished; the other counts
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
	// By root, not by id: a copy of the library folder shares the id, and
	// a user.db made anew has another.
	if !addressedTo(p.header.Target, to.Lib.Root()) {
		return Result{}, ErrOtherTarget
	}
	bg := context.WithoutCancel(ctx)
	r := newRun(from, to, opts)
	r.res.RecordID = p.header.ID
	rec, recErr := to.User.GetImport(bg, p.header.ID)
	if recErr == nil {
		r.session = rec.CatalogSession
	}
	if r.j, err = openJournal(p); err != nil {
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
		if ctx.Err() != nil {
			r.res.Cancelled = true
			continue // the done entries still count in the items
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
	if r.j.settled() {
		// The record before the end line: a crash between them leaves a
		// finished record and a journal that the next Recover ends.
		if recErr == nil && rec.Outcome == userdata.ImportRunning {
			r.res.Failed += priorFailed
			r.finishRecord(bg, userdata.ImportFailed)
			r.res.Failed -= priorFailed
		}
		if err := r.j.end(); err != nil {
			return r.res, err
		}
	}
	r.log.Info("interrupted move finished", "recovered", r.res.Recovered, "failed", r.res.Failed,
		"source_pending", r.res.SourcePending, "open", len(r.j.open), "cancelled", r.res.Cancelled)
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

// sourceInTrash returns the catalog path in the source trash holding the
// source copy of the live row m whose file is not in its place: a crash
// came between the source step's rename to the trash and its catalog
// update. The path is the journalled trash name, else m's own, and its
// file must hash to m. "" when the copy is not there (a same-volume
// rename moved the file away, or it is gone); an error when it cannot be
// read.
func (r *run) sourceInTrash(ctx context.Context, e *entry, m catalog.Media) (string, error) {
	if e.kind == kindMove && e.mode == modeRename && e.trash == "" {
		return "", nil // a rename has no source trash step
	}
	rel := e.trash
	if rel == "" {
		rel = m.RelPath
	}
	p := r.from.Lib.TrashPath(rel)
	fi, err := os.Lstat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", nil
	case err != nil:
		return "", err
	case !fi.Mode().IsRegular():
		return "", nil
	}
	got, _, err := library.HashFileContext(ctx, p)
	if err != nil {
		return "", err
	}
	if got != m.Hash {
		return "", nil
	}
	return rel, nil
}

// finishSource does the source step of a photo now in the target: the
// source copy to the trash when it is in its place; the catalog update
// when a crash came after its rename to the trash; else the source row
// forgotten (a rename moved the file, or it is gone); nothing when the
// row is gone or trashed (done before; a trashed row gets its user.db
// trash row, which the crash may have missed).
func (r *run) finishSource(ctx context.Context, e *entry, m catalog.Media, ok bool) error {
	switch {
	case !ok:
		return nil
	case !m.DeletedAt.IsZero():
		r.trashed = append(r.trashed, userdata.TrashEntry{Hash: m.Hash, DeletedAt: m.DeletedAt})
		return nil
	}
	_, err := r.from.Lib.LiveFile(m.RelPath)
	switch {
	case err == nil:
		return r.trashSource(ctx, m)
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	rel, err := r.sourceInTrash(ctx, e, m)
	switch {
	case err != nil:
		return err
	case rel != "":
		r.log.Info("source copy found in the source trash", "hash", hexHash(m.Hash), "trash_path", rel)
		return r.markTrashed(ctx, m, rel)
	}
	return r.forgetSource(ctx, m)
}

// restoredFile looks for the file of the target's trashed row b after a
// crash inside its restore (the rename out of the trash done, the catalog
// not updated): when the trash no longer holds it, the restore put it at
// b's path or at one of its UniqueName variants, where it hashes to b.
// "" when it is still in the trash or was not found.
func (r *run) restoredFile(ctx context.Context, b catalog.Media) (string, error) {
	if _, err := os.Lstat(r.to.Lib.TrashPath(b.RelPath)); err == nil {
		return "", nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	var found string
	var readErr error
	library.UniqueName(b.RelPath, func(c string) (bool, error) {
		p, err := r.to.Lib.LiveFile(c)
		if err != nil {
			return true, nil // nothing (usable) there: the next name
		}
		if fi, err := os.Stat(p); err != nil || fi.Size() != b.Size {
			return true, nil
		}
		if !strings.EqualFold(c, b.RelPath) {
			if taken, err := r.to.DB.RelPathTaken(ctx, c); err != nil || taken {
				return true, nil // another row's file
			}
		}
		got, _, err := library.HashFileContext(ctx, p)
		if err != nil {
			readErr = err
			return false, err
		}
		if got != b.Hash {
			return true, nil
		}
		found = c
		return false, nil
	})
	if found == "" && readErr != nil {
		return "", readErr
	}
	return found, nil
}

// recoverEntry finishes one open journal entry. An entry whose state
// cannot be read, or whose step fails, is reported failed and stays open.
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
	if bok && e.kind == kindRestore && !b.DeletedAt.IsZero() {
		rel, err := r.restoredFile(ctx, b)
		if err != nil {
			r.failed(it, err)
			return
		}
		if rel != "" {
			if err := r.to.DB.ClearDeleted(ctx, b.Hash, rel); err != nil {
				r.failed(it, err)
				return
			}
			r.log.Info("interrupted restore finished in the target", "hash", hexHash(e.hash), "dest", rel)
			b.DeletedAt, b.RelPath = time.Time{}, rel
		}
	}
	if bok && (e.kind == kindMove || b.DeletedAt.IsZero()) {
		// The photo reached the target: go forward.
		it.Status, it.Dest = statusOf(e.kind), b.RelPath
		it.SourceErr = r.finishSource(ctx, e, m, mok)
		r.succeeded(it)
		return
	}
	if e.kind != kindMove {
		r.back(ctx, e, it, m, mok, fail) // the restore never committed, or the target's copy went away
		return
	}
	abs, ferr := r.to.Lib.LiveFile(e.to)
	placedThere := false
	switch {
	case ferr == nil:
		got, _, herr := library.HashFileContext(ctx, abs)
		switch {
		case herr != nil:
			r.log.Warn("placed file unreadable; the entry stays open", "hash", hexHash(e.hash), "dest", e.to, "err", herr)
			r.failed(it, herr)
			return
		case got == e.hash:
			placedThere = true
		default:
			r.log.Warn("another file is at the target path; left alone", "hash", hexHash(e.hash), "dest", e.to,
				"found", hexHash(got))
		}
	case errors.Is(ferr, fs.ErrNotExist), errors.Is(ferr, library.ErrNotRegularFile), errors.Is(ferr, library.ErrNotLibraryPath):
		// Nothing of this move is there.
	default:
		r.log.Warn("target path unreadable; the entry stays open", "hash", hexHash(e.hash), "dest", e.to, "err", ferr)
		r.failed(it, ferr)
		return
	}
	switch {
	case placedThere && mok:
		// The copy's folders are synced before the row and the source step
		// rely on it.
		syncParents(r.to.Lib.Root(), filepath.Dir(abs), map[string]bool{})
		ok, err := r.to.Store.CopyFrom(ctx, r.from.Store, e.hash)
		if err != nil {
			r.log.Warn("renditions not copied; they will be generated", "hash", hexHash(e.hash), "err", err)
		}
		nr := r.targetRow(m, e.to, ok)
		inserted, err := r.to.DB.InsertMedia(ctx, nil, &nr)
		if err == nil && !inserted && !r.recordedAt(ctx, e.hash, e.to) {
			err = errors.New("the target catalog has another row of this content")
		}
		if err != nil {
			r.failed(it, fmt.Errorf("record the placed file: %w", err)) // stays open
			return
		}
		it.Status, it.Dest = StatusMoved, e.to
		it.SourceErr = r.finishSource(ctx, e, m, mok)
		r.succeeded(it)
	case placedThere:
		r.log.Error("placed file whose source row is gone", "hash", hexHash(e.hash), "dest", e.to)
		fail(ErrUnrecorded)
	default:
		r.back(ctx, e, it, m, mok, fail)
	}
}

// back ends an entry whose photo is not in the target: it stays in the
// source (ErrInterrupted). A live source row whose file a crash in the
// source step left in the source trash gets the file back in its place
// first. A source with neither a live file nor a trashed row is the
// lost-track case; a source file that cannot be looked at stays open.
func (r *run) back(ctx context.Context, e *entry, it Item, m catalog.Media, mok bool, fail func(error)) {
	if mok && m.DeletedAt.IsZero() {
		_, err := r.from.Lib.LiveFile(m.RelPath)
		if errors.Is(err, fs.ErrNotExist) {
			rel, terr := r.sourceInTrash(ctx, e, m)
			if terr == nil && rel != "" {
				if terr = r.from.Lib.RestoreTo(rel, m.RelPath); terr == nil {
					r.log.Info("source copy put back from the source trash", "hash", hexHash(e.hash), "trash_path", rel)
					err = nil
				}
			}
			if terr != nil {
				r.failed(it, terr) // stays open
				return
			}
		}
		switch {
		case err == nil:
			fail(ErrInterrupted)
			return
		case !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, library.ErrNotRegularFile) &&
			!errors.Is(err, library.ErrNotLibraryPath):
			r.failed(it, err) // stays open
			return
		}
	}
	if mok && !m.DeletedAt.IsZero() {
		fail(ErrInterrupted) // in the source's trash
		return
	}
	r.log.Error("interrupted move lost track of a file", "hash", hexHash(e.hash), "source", e.rel,
		"target", e.to, "source_row", mok)
	fail(ErrLostTrack)
}
