package server

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/library"
	"picmover/internal/thumbs"
)

// Trash operations work on catalog rows only, never on request paths:
// a hash names a row, and the row's rel_path (shape-checked with
// validRelPath) the file. Each hash is reported on its own; one failure
// never stops the batch. Nothing here deletes a live file: the only
// deletion is of trashed rows' files under the trash directory, with
// confirm: true.

type hashesBody struct {
	Hashes []string `json:"hashes"`
}

type failedJSON struct {
	Hash  string `json:"hash"`
	Error string `json:"error"`
}

type trashResultJSON struct {
	Trashed int          `json:"trashed"`
	Failed  []failedJSON `json:"failed"`
}

type restoreResultJSON struct {
	Restored int          `json:"restored"`
	Failed   []failedJSON `json:"failed"`
}

type deleteResultJSON struct {
	Deleted int          `json:"deleted"`
	Failed  []failedJSON `json:"failed"`
}

// fileOpError is the user-facing text of a failed rename or removal; the
// detail (paths, OS text) goes to the log.
func (s *Server) fileOpError(what string, h uint64, rel string, err error) string {
	s.log.Info(what+" failed", "hash", hexHash(h), "rel_path", rel, "err", err)
	switch {
	case errors.Is(err, library.ErrTrashTargetExists):
		return "the trash already holds a different file at this path"
	case errors.Is(err, fs.ErrNotExist):
		return "the file is missing"
	case library.IsCrossDevice(err):
		return "the file is on another volume (a mounted folder); picmover moves files only within one volume and never copies and deletes"
	}
	return what + " failed: " + err.Error()
}

// trashRows returns the catalog rows of hashes and reports the hashes
// without a row.
func trashRows(ctx context.Context, l *libHandles, hashes []uint64, failed *[]failedJSON) (map[uint64]catalog.Media, error) {
	rows, err := l.db.ByHashes(ctx, hashes)
	if err != nil {
		return nil, err
	}
	for _, h := range hashes {
		if _, ok := rows[h]; !ok {
			*failed = append(*failed, failedJSON{Hash: hexHash(h), Error: "not in the catalog"})
		}
	}
	return rows, nil
}

// handleTrash moves the files of live rows to the trash: the file to
// .picmover/trash/<rel_path> (one rename), deleted_at in the catalog and
// a trash row in user.db.
func (s *Server) handleTrash(w http.ResponseWriter, r *http.Request, l *libHandles) {
	var body hashesBody
	if !decodeBody(w, r, &body) {
		return
	}
	hashes, err := parseHashes("hashes", body.Hashes, maxBodyHashes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	l.entry.trashMu.Lock()
	defer l.entry.trashMu.Unlock()
	ctx := context.WithoutCancel(r.Context()) // a file moved must be recorded
	out := trashResultJSON{Failed: []failedJSON{}}
	rows, err := trashRows(ctx, l, hashes, &out.Failed)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	now := time.Now()
	var done []uint64
	for _, h := range hashes {
		m, ok := rows[h]
		if !ok {
			continue
		}
		fail := func(msg string) { out.Failed = append(out.Failed, failedJSON{Hash: hexHash(h), Error: msg}) }
		switch {
		case !m.DeletedAt.IsZero():
			fail("already in the trash")
			continue
		case !validRelPath(m.RelPath) || l.lib.IsInternal(l.lib.Abs(m.RelPath)):
			fail("the file's catalog path is not a library path")
			continue
		}
		if err := l.lib.MoveToTrash(m.RelPath); err != nil {
			fail(s.fileOpError("move to trash", h, m.RelPath, err))
			continue
		}
		if err := l.db.SetDeleted(ctx, h, now); err != nil {
			s.log.Error("record trash", "hash", hexHash(h), "err", err)
			if _, rerr := l.lib.Restore(m.RelPath, nil); rerr != nil {
				s.log.Error("undo move to trash", "hash", hexHash(h), "err", rerr)
			}
			fail("the catalog could not be updated")
			continue
		}
		done = append(done, h)
	}
	if len(done) > 0 {
		if err := l.user.Trash(ctx, done, now); err != nil {
			// The catalog is the view of trash state; user.db only keeps
			// the timestamp for a rebuild.
			s.log.Error("record trash in user.db", "err", err)
		}
	}
	out.Trashed = len(done)
	writeJSON(w, http.StatusOK, out)
}

// handleRestore moves trashed files back to their catalog path (or a
// free variant of it when that is taken now) and makes their rows live.
func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request, l *libHandles) {
	var body hashesBody
	if !decodeBody(w, r, &body) {
		return
	}
	hashes, err := parseHashes("hashes", body.Hashes, maxBodyHashes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	l.entry.trashMu.Lock()
	defer l.entry.trashMu.Unlock()
	ctx := context.WithoutCancel(r.Context())
	out := restoreResultJSON{Failed: []failedJSON{}}
	rows, err := trashRows(ctx, l, hashes, &out.Failed)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var done []uint64
	for _, h := range hashes {
		m, ok := rows[h]
		if !ok {
			continue
		}
		fail := func(msg string) { out.Failed = append(out.Failed, failedJSON{Hash: hexHash(h), Error: msg}) }
		switch {
		case m.DeletedAt.IsZero():
			fail("not in the trash")
			continue
		case !validRelPath(m.RelPath) || !within(l.lib.TrashDir(), l.lib.TrashPath(m.RelPath)):
			fail("the file's catalog path is not a library path")
			continue
		}
		// The row's own path is not "taken": rel_path is unique
		// case-insensitively, so only this row can hold it.
		taken := func(c string) (bool, error) {
			if strings.EqualFold(c, m.RelPath) {
				return false, nil
			}
			return l.db.RelPathTaken(ctx, c)
		}
		rel, err := l.lib.Restore(m.RelPath, taken)
		if err != nil {
			fail(s.fileOpError("restore", h, m.RelPath, err))
			continue
		}
		if err := l.db.ClearDeleted(ctx, h, rel); err != nil {
			s.log.Error("record restore", "hash", hexHash(h), "err", err)
			if uerr := l.lib.MoveToTrashAs(rel, m.RelPath); uerr != nil {
				s.log.Error("undo restore", "hash", hexHash(h), "err", uerr)
			}
			fail("the catalog could not be updated")
			continue
		}
		done = append(done, h)
	}
	if len(done) > 0 {
		if err := l.user.Untrash(ctx, done); err != nil {
			s.log.Error("record restore in user.db", "err", err)
		}
	}
	out.Restored = len(done)
	writeJSON(w, http.StatusOK, out)
}

type trashDeleteBody struct {
	Hashes  []string `json:"hashes"`
	All     bool     `json:"all"`
	Confirm bool     `json:"confirm"`
}

// handleTrashDelete deletes trashed rows for good: the file under the
// trash directory (a missing one counts as deleted), the catalog row, the
// user.db trash row and the cached renditions. Ratings, tags and album
// memberships stay. It needs confirm: true (400 otherwise).
func (s *Server) handleTrashDelete(w http.ResponseWriter, r *http.Request, l *libHandles) {
	var body trashDeleteBody
	if !decodeBody(w, r, &body) {
		return
	}
	if !body.Confirm {
		writeError(w, http.StatusBadRequest, "permanent deletion needs \"confirm\": true")
		return
	}
	if body.All == (len(body.Hashes) > 0) {
		writeError(w, http.StatusBadRequest, "give either hashes or \"all\": true")
		return
	}
	hashes, err := parseHashes("hashes", body.Hashes, maxBodyHashes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	l.entry.trashMu.Lock()
	defer l.entry.trashMu.Unlock()
	ctx := context.WithoutCancel(r.Context())
	out := deleteResultJSON{Failed: []failedJSON{}}
	var rows map[uint64]catalog.Media
	if body.All {
		list, err := l.db.ListTrashed(ctx)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		rows = make(map[uint64]catalog.Media, len(list))
		for _, m := range list {
			rows[m.Hash] = m
			hashes = append(hashes, m.Hash)
		}
	} else if rows, err = trashRows(ctx, l, hashes, &out.Failed); err != nil {
		s.internalError(w, r, err)
		return
	}
	var store *thumbs.Store // writer, opened on first use
	defer func() {
		if store != nil {
			store.Close()
		}
	}()
	var done []uint64
	for _, h := range hashes {
		m, ok := rows[h]
		if !ok {
			continue
		}
		fail := func(msg string) { out.Failed = append(out.Failed, failedJSON{Hash: hexHash(h), Error: msg}) }
		if m.DeletedAt.IsZero() {
			fail("not in the trash; only trashed files can be deleted")
			continue
		}
		if err := removeTrashFile(l.lib, m.RelPath); err != nil {
			fail(s.fileOpError("delete", h, m.RelPath, err))
			continue
		}
		if err := l.db.Remove(ctx, h); err != nil && !errors.Is(err, sql.ErrNoRows) {
			s.log.Error("remove catalog row", "hash", hexHash(h), "err", err)
			fail("the file was deleted, but the catalog could not be updated")
			continue
		}
		if store == nil {
			if store, err = thumbs.OpenStore(l.lib); err != nil {
				s.log.Error("open thumbnail cache", "err", err)
				store = nil
			}
		}
		if store != nil {
			if err := store.Delete(ctx, h); err != nil {
				s.log.Error("delete renditions", "hash", hexHash(h), "err", err)
			}
		}
		done = append(done, h)
	}
	if len(done) > 0 {
		if err := l.user.Untrash(ctx, done); err != nil {
			s.log.Error("remove user.db trash rows", "err", err)
		}
	}
	out.Deleted = len(done)
	writeJSON(w, http.StatusOK, out)
}

// errNotRemovable refuses a trash path that is not a file.
var errNotRemovable = errors.New("the trash entry is not a file")

// removeTrashFile removes the trash file of the catalog path rel. The
// path must have a library path's shape and lie inside the trash
// directory; it must not be a directory. A missing file counts as
// removed. A symbolic link is removed itself, never its target.
func removeTrashFile(lib *library.Library, rel string) error {
	if !validRelPath(rel) {
		return errNotRemovable
	}
	p := lib.TrashPath(rel)
	if !within(lib.TrashDir(), p) {
		return errNotRemovable
	}
	fi, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return errNotRemovable
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
