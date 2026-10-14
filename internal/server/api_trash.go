package server

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"net/http"
	"strings"
	"syscall"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/importer"
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

// fileOpError is the user-facing text of a failed rename or removal: a
// fixed text per kind of failure, never a path or a raw OS error; the
// detail goes to the log.
func (s *Server) fileOpError(what string, h uint64, rel string, err error) string {
	s.log.Info(what+" failed", "hash", hexHash(h), "rel_path", rel, "err", err)
	switch {
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
	case library.IsCrossDevice(err):
		return "the file is on another volume (a mounted folder); picmover moves files only within one volume and never copies and deletes"
	case errors.Is(err, syscall.EROFS):
		return "the library is on a read-only file system"
	case errors.Is(err, fs.ErrPermission):
		return "permission denied"
	case errors.Is(err, syscall.ENOSPC):
		return "no space left on the device"
	case isBusyErr(err):
		return "the file is in use by another program"
	}
	switch what {
	case "move to trash":
		return "the file could not be moved to the trash"
	case "restore":
		return "the file could not be restored"
	}
	return "the file could not be deleted"
}

// folderReplaced answers a mutation on a library whose folder was
// replaced while open: 409 {error, state: "error"}.
func folderReplaced(w http.ResponseWriter) {
	writeJSON(w, http.StatusConflict, stateError{Error: errReplaced.Error(), State: libError})
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
	if !l.entry.lockTrash() {
		writeError(w, http.StatusConflict, errMoving.Error())
		return
	}
	defer l.entry.trashMu.Unlock()
	if l.entry.checkFolder() != nil {
		folderReplaced(w)
		return
	}
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
		case !validRelPath(m.RelPath):
			fail("the file's catalog path is not a library path")
			continue
		}
		// MoveToTrash checks the rest like the file server: links
		// resolved, inside the library, not internal, a regular file.
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
	if !l.entry.lockTrash() {
		writeError(w, http.StatusConflict, errMoving.Error())
		return
	}
	defer l.entry.trashMu.Unlock()
	if l.entry.checkFolder() != nil {
		folderReplaced(w)
		return
	}
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
		case !validRelPath(m.RelPath):
			fail("the file's catalog path is not a library path")
			continue
		}
		// The one restore path, shared with an import that meets
		// trashed content.
		if _, err := importer.RestoreFromTrash(ctx, l.lib, l.db, m, nil); err != nil {
			fail(s.fileOpError("restore", h, m.RelPath, err))
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
	if !l.entry.lockTrash() {
		writeError(w, http.StatusConflict, errMoving.Error())
		return
	}
	defer l.entry.trashMu.Unlock()
	if l.entry.checkFolder() != nil {
		folderReplaced(w)
		return
	}
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
	if body.All {
		out.Deleted += s.removeTrashOrphans(ctx, l)
	}
	writeJSON(w, http.StatusOK, out)
}

// removeTrashOrphans deletes, for "Empty trash", the files under the trash
// directory that no trashed row names: a rebuild records no row for a
// trash file whose content or path is live, so nothing else could ever
// show or delete them. Files of trashed rows that remain (their deletion
// failed) are kept. It returns the number removed.
func (s *Server) removeTrashOrphans(ctx context.Context, l *libHandles) int {
	files, err := l.lib.TrashFiles()
	if err != nil {
		s.log.Error("list the trash", "err", err)
	}
	if len(files) == 0 {
		return 0
	}
	rows, err := l.db.ListTrashed(ctx)
	if err != nil {
		s.log.Error("list trashed rows", "err", err)
		return 0
	}
	kept := make(map[string]bool, len(rows))
	for _, m := range rows {
		kept[strings.ToLower(m.RelPath)] = true
	}
	n := 0
	for _, rel := range files {
		if kept[strings.ToLower(rel)] {
			continue
		}
		if err := l.lib.RemoveTrashFile(rel); err != nil {
			s.log.Info("remove orphan trash file", "rel_path", rel, "err", err)
			continue
		}
		n++
	}
	if n > 0 {
		s.log.Info("removed trash files without a catalog row", "count", n)
	}
	return n
}

// removeTrashFile removes the trash file of the catalog path rel: the
// path must have a library path's shape; library.RemoveTrashFile checks
// the rest (inside the trash with links resolved, a regular file). A
// missing file counts as removed.
func removeTrashFile(lib *library.Library, rel string) error {
	if !validRelPath(rel) {
		return library.ErrNotLibraryPath
	}
	return lib.RemoveTrashFile(rel)
}
