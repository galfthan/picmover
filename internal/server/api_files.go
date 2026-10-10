package server

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"picmover/internal/library"
	"picmover/internal/thumbs"
)

// renditionCache is the Cache-Control of thumbnails and previews. They
// change when regenerated (rotation fix, thumbs --rebuild), so clients
// put the row's thumbAt in the URL (?v=<thumbAt>): a regenerated rendition
// has a new URL, and the ETag includes v so revalidation sees it too.
const renditionCache = "private, max-age=604800"

// renditionETag is "<hash>-<FormatVersion>-<v>".
func renditionETag(hash uint64, v string) string {
	return fmt.Sprintf(`"%016x-%d-%s"`, hash, thumbs.FormatVersion, v)
}

// renditionVersion returns the v query parameter (the row's thumbAt):
// 1-20 decimal digits, anything else counts as absent ("0").
func renditionVersion(r *http.Request) string {
	v := r.URL.Query().Get("v")
	if v == "" || len(v) > 20 {
		return "0"
	}
	for i := 0; i < len(v); i++ {
		if v[i] < '0' || v[i] > '9' {
			return "0"
		}
	}
	return v
}

// renditionHash parses the {hash} path value or answers 400.
func renditionHash(w http.ResponseWriter, r *http.Request) (uint64, bool) {
	h, ok := parseHash(r.PathValue("hash"))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid hash: want 16 lower-case hex digits")
	}
	return h, ok
}

func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request, l *libHandles) {
	hash, ok := renditionHash(w, r)
	if !ok {
		return
	}
	data, _, _, found, err := l.store.Thumb(r.Context(), hash)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !found {
		// Pending: the client retries after a thumbnail job, so never cache.
		writeError(w, http.StatusNotFound, "no thumbnail")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "image/jpeg")
	h.Set("ETag", renditionETag(hash, renditionVersion(r)))
	h.Set("Cache-Control", renditionCache)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request, l *libHandles) {
	hash, ok := renditionHash(w, r)
	if !ok {
		return
	}
	f, err := openRegular(l.store.PreviewPath(hash))
	if isMissing(err) {
		writeError(w, http.StatusNotFound, "no preview")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer f.Close()
	h := w.Header()
	h.Set("Content-Type", "image/jpeg")
	h.Set("ETag", renditionETag(hash, renditionVersion(r)))
	h.Set("Cache-Control", renditionCache)
	http.ServeContent(w, r, "", time.Time{}, f)
}

// openRegular opens p only when it is a regular file and not a symbolic
// link, checked with Lstat before and against the opened file after
// opening (so a link swapped in between is refused too). Anything else is
// fs.ErrNotExist.
//
// Errors that mean "no such file" for a malformed name are reported by
// isMissing; callers answer 404 for those and 500 for the rest.
func openRegular(p string) (*os.File, error) {
	lst, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !lst.Mode().IsRegular() {
		return nil, fs.ErrNotExist
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil || !os.SameFile(lst, st) {
		f.Close()
		return nil, fs.ErrNotExist
	}
	return f, nil
}

// handleFile serves the original of a catalog row. The path comes only
// from the row, and must resolve inside the library, outside picmover's
// own files, to a regular file whose last component is not a symbolic
// link. A trashed row is served from its trash path with the same checks
// against the trash directory. Anything else is 404.
func (s *Server) handleFile(w http.ResponseWriter, r *http.Request, l *libHandles) {
	download := false
	switch v := r.URL.Query().Get("download"); v {
	case "", "0":
	case "1":
		download = true
	default:
		writeError(w, http.StatusBadRequest, "invalid download value: want 1")
		return
	}
	m, ok := s.mediaRow(w, r, l)
	if !ok {
		return
	}
	f, err := s.openOriginal(l.lib, m.RelPath, !m.DeletedAt.IsZero())
	if isMissing(err) {
		s.log.Debug("file not available", "id", m.ID, "rel_path", m.RelPath, "err", err)
		writeError(w, http.StatusNotFound, "file not available")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	disp := "inline"
	if download {
		disp = "attachment"
	}
	h := w.Header()
	h.Set("Content-Type", contentType(m.Ext))
	h.Set("Content-Disposition", mime.FormatMediaType(disp, map[string]string{"filename": path.Base(m.RelPath)}))
	h.Set("ETag", fmt.Sprintf(`"%016x-%d"`, m.Hash, st.Size()))
	h.Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, "", st.ModTime(), f)
}

// errRefused is the isMissing error for a row whose path is refused.
var errRefused = fmt.Errorf("path refused: %w", fs.ErrNotExist)

// openOriginal opens the original at the catalog path rel (in the trash
// when trashed):
//
//  1. rel must have the shape of a library path (validRelPath), checked
//     before any filesystem call;
//  2. lexically inside the library and not one of picmover's own files;
//  3. resolved with filepath.EvalSymlinks (symlinked or junction
//     directories), the result must still be inside the library (whose
//     root library.Open resolved the same way) and not internal;
//  4. the last component of the unresolved path must be a regular file,
//     not a link (Lstat), and the opened file must be that same file.
//
// A trashed row's file is at lib.TrashPath(rel); "inside the library and
// not internal" then means inside lib.TrashDir() instead.
//
// A refusal is an error that isMissing reports.
func (s *Server) openOriginal(lib *library.Library, rel string, trashed bool) (*os.File, error) {
	if !validRelPath(rel) {
		return nil, errRefused
	}
	p := lib.Abs(rel)
	allowed := func(p string) bool { return lib.Contains(p) && !lib.IsInternal(p) }
	if trashed {
		p = lib.TrashPath(rel)
		allowed = func(p string) bool { return within(lib.TrashDir(), p) }
	}
	if !allowed(p) {
		return nil, errRefused
	}
	resolved, err := filepath.EvalSymlinks(p)
	switch {
	case err == nil:
	case evalSymlinksGaps && !isMissing(err):
		// Some Windows volumes cannot be resolved at all (library.Open
		// then keeps the unresolved root too): fall back to the lexical
		// checks above, as before.
		s.log.Debug("cannot resolve path; using it unresolved", "path", p, "err", err)
		resolved = p
	default:
		return nil, err
	}
	if !allowed(resolved) {
		return nil, errRefused
	}
	lst, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !lst.Mode().IsRegular() {
		return nil, errRefused
	}
	f, err := openRegular(resolved)
	if err != nil {
		return nil, err
	}
	if st, err := f.Stat(); err != nil || !os.SameFile(lst, st) {
		f.Close()
		return nil, errRefused
	}
	return f, nil
}

// within reports whether p lies strictly inside dir (lexically).
func within(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(rel)
}

// validRelPath reports whether rel has the shape of a path in the
// library: slash-separated components, none empty, "." or "..", none
// containing \, : (drive letters, NTFS streams) or NUL, and a first
// component that is not .picmover and does not start with media.db, nor
// (for a file at the root) an import_*.log, compared case-insensitively
// after trimming trailing dots and spaces (NTFS and APFS ignore case,
// Win32 drops trailing dots and spaces). The importer only writes
// type/YYYY/MM/name, so nothing legitimate is refused.
func validRelPath(rel string) bool {
	parts := strings.Split(rel, "/")
	for _, c := range parts {
		if c == "" || c == "." || c == ".." || strings.ContainsAny(c, "\\:\x00") {
			return false
		}
	}
	first := strings.ToLower(strings.TrimRight(parts[0], ". "))
	if first == "" || first == library.MetaDir || strings.HasPrefix(first, library.DBName) {
		return false
	}
	if len(parts) == 1 && strings.HasPrefix(first, "import_") && strings.HasSuffix(first, ".log") {
		return false
	}
	return true
}

// isMissing reports errors that mean the file is not available: it does
// not exist, the path was refused, or the OS rejected the name (not a
// directory, invalid, too long, a link loop). Callers answer 404 for
// these and a generic 500 for the rest.
func isMissing(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	for _, e := range missingErrnos {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// missingErrnos are the OS errors isMissing maps to 404, plus the
// per-OS ones in fs_*.go.
var missingErrnos = append([]error{syscall.ENOTDIR, syscall.EINVAL, syscall.ENAMETOOLONG, syscall.ELOOP},
	osMissingErrnos...)
