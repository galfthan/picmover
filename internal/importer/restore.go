package importer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"picmover/internal/catalog"
	"picmover/internal/library"
	"picmover/internal/metadata"
)

// ErrRecordRestore is returned (wrapped) by RestoreFromTrash when the file
// was moved back but the catalog could not record it; the move has been
// undone.
var ErrRecordRestore = errors.New("the catalog could not be updated")

// ErrNotTrashed is returned by RestoreFromTrash for a live row.
var ErrNotTrashed = errors.New("not in the trash")

// RestoreFromTrash moves the trashed row m's file from the trash back into
// the library (library.Restore: its catalog path, or the first free
// UniqueName variant when that is taken on disk, in the catalog or by
// taken, which may be nil) and makes the row live at that path. If the
// catalog update fails the file is moved back into the trash and the
// error wraps ErrRecordRestore. It is the one restore path, shared by the
// server's restore endpoint and by an import that meets trashed content.
// The caller serialises it with other trash operations on the library and
// updates user.db.
func RestoreFromTrash(ctx context.Context, lib *library.Library, db *catalog.DB, m catalog.Media,
	taken func(rel string) (bool, error)) (string, error) {
	if m.DeletedAt.IsZero() {
		return "", ErrNotTrashed
	}
	// The row's own path is not "taken": rel_path is unique
	// case-insensitively, so only this row can hold it.
	isTaken := func(c string) (bool, error) {
		if taken != nil {
			if t, err := taken(c); err != nil || t {
				return t, err
			}
		}
		if strings.EqualFold(c, m.RelPath) {
			return false, nil
		}
		return db.RelPathTaken(ctx, c)
	}
	rel, err := lib.Restore(m.RelPath, isTaken)
	if err != nil {
		return "", err
	}
	if err := db.ClearDeleted(ctx, m.Hash, rel); err != nil {
		if uerr := lib.MoveToTrashAs(rel, m.RelPath); uerr != nil {
			return "", fmt.Errorf("%w: %v; undo failed: %v", ErrRecordRestore, err, uerr)
		}
		return "", fmt.Errorf("%w: %v", ErrRecordRestore, err)
	}
	return rel, nil
}

// restoreTrashed handles an imported file whose content belongs to a
// trashed row: the trashed copy is restored instead of importing the file
// again (otherwise "Empty trash" would delete the library's only copy).
// The trashed copy is hashed before the restore and the restored file
// after it; only then is a move source deleted. The result is imported,
// "restored from trash". A staged (copy-first) move source is read a second
// time (verifySource) before it is deleted, as for any copy-first move.
// The result has Restored set.
func (im *Importer) restoreTrashed(ctx context.Context, hash uint64, srcPath, origin string, fromZip, staged bool) *Result {
	if _, loaded := im.inflight.LoadOrStore(hash, origin); loaded {
		return &Result{Status: StatusDuplicate, Source: origin, Message: "duplicate of another file in this import", Hash: hash}
	}
	defer im.inflight.Delete(hash)
	if l := im.opts.TrashLock; l != nil {
		l.Lock()
		defer l.Unlock()
	}
	fail := func(msg string, err error) *Result {
		return &Result{Status: StatusError, Source: origin, Message: msg, Err: err}
	}
	m, err := im.db.GetByHash(ctx, hash)
	if err != nil {
		return fail("catalog lookup", err)
	}
	if m.DeletedAt.IsZero() { // restored meanwhile
		return &Result{Status: StatusDuplicate, Source: origin, Dest: m.RelPath, Message: "already in catalog", Hash: hash}
	}
	keepSource := "; the source file is kept"
	if h, _, err := library.HashFile(im.lib.TrashPath(m.RelPath)); err != nil || h != hash {
		if err == nil {
			err = fmt.Errorf("%s: content differs from the catalog", m.RelPath)
		}
		return fail("the file is in the trash, but its trashed copy cannot be restored; restore or delete it in the trash first"+keepSource, err)
	}
	taken := func(c string) (bool, error) {
		im.mu.Lock()
		defer im.mu.Unlock()
		_, ok := im.claimed[strings.ToLower(c)]
		return ok, nil
	}
	rel, err := RestoreFromTrash(ctx, im.lib, im.db, m, taken)
	if err != nil {
		return fail("restore from trash"+keepSource, err)
	}
	im.mu.Lock()
	im.claimLocked(rel)
	im.mu.Unlock()
	if im.opts.OnRestored != nil {
		if err := im.opts.OnRestored(ctx, hash); err != nil {
			im.log.Error("record restore in user.db", "hash", fmt.Sprintf("%016x", hash), "err", err)
		}
	}
	r := &Result{Status: StatusImported, Source: origin, Dest: rel, Message: "restored from trash", Hash: hash, Restored: true,
		Kind: metadata.Kind(m.FileType)}
	if !im.opts.Move || fromZip {
		return r
	}
	// A move deletes the source only after the library copy is verified.
	if h, _, err := library.HashFile(im.lib.Abs(rel)); err != nil || h != hash {
		if err == nil {
			err = fmt.Errorf("%s: restored content differs", rel)
		}
		im.log.Error("verify restored file", "dest", rel, "err", err)
		r.Message += "; the restored copy could not be verified" + keepSource
		return r
	}
	if staged {
		if err := im.verifySource(ctx, srcPath, hash); err != nil {
			im.log.Error("verify move source", "source", srcPath, "err", err)
			r.Message += "; the source could not be verified" + keepSource
			return r
		}
	}
	if err := os.Remove(srcPath); err != nil {
		im.log.Error("remove move source", "source", srcPath, "err", err)
		r.Message += "; could not remove source: " + err.Error()
	} else {
		r.Message += "; source removed"
	}
	return r
}
