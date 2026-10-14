// Package thumbs generates thumbnails (256 px, stored in thumbs.db), large
// thumbnails (640 px) and previews (1600 px, both stored as files) for
// media files, keyed by the file's content hash, and computes a perceptual
// hash of each preview.
//
// The cache lives in <library>/.picmover: thumbs.db holds the thumbnails,
// large/<hh>/<hash>.webp the large thumbnails and previews/<hh>/<hash>.webp
// (or .jpg, written by older versions) the previews. New renditions are
// WebP; existing JPEG ones stay valid and are read by sniffing their bytes.
// It is derived data: deleting it only means the thumbnails are generated
// again.
package thumbs

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver

	"picmover/internal/imaging"
	"picmover/internal/library"
)

// Sizes (longer side, pixels) and WebP qualities of the three renditions.
const (
	ThumbSize      = 256  // stored in thumbs.db
	LargeSize      = 640  // stored on disk, derived from the preview
	PreviewSize    = 1600 // stored on disk
	ThumbQuality   = 80
	LargeQuality   = 82
	PreviewQuality = 85
)

const (
	// DBName is the cache database file inside library.MetaDir.
	DBName = "thumbs.db"
	// PreviewDir is the preview directory inside library.MetaDir.
	PreviewDir = "previews"
	// LargeDir is the large thumbnail directory inside library.MetaDir.
	LargeDir = "large"
	// FormatVersion is thumbs.db's PRAGMA user_version.
	FormatVersion = 1
)

// ErrFormat is returned (wrapped) by OpenStore for a thumbs.db with a
// different format version. The cache can simply be deleted.
var ErrFormat = errors.New("thumbs: unsupported cache format")

// dsnParams are the catalog's connection pragmas (internal/catalog
// dsnParams), repeated here because that constant is unexported.
const dsnParams = "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=foreign_keys(1)&_txlock=immediate"

const schema = `CREATE TABLE thumbs (
    hash       INTEGER PRIMARY KEY,
    width      INTEGER NOT NULL,
    height     INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    data       BLOB NOT NULL
)`

// Store is the thumbnail, large thumbnail and preview cache of one
// library. It is safe for concurrent use.
type Store struct {
	db       *sql.DB
	path     string
	previews string
	large    string
	tmp      string
	readOnly bool
	// ls is what this Store remembers about large thumbnails it could
	// not derive or write (largestate.go).
	ls largeState
}

// File extensions. New previews are WebP; older ones (and any written from
// JPEG bytes) JPEG. Preview lookups try previewExts in order.
const (
	extWebP = ".webp"
	extJPEG = ".jpg"
)

var previewExts = [...]string{extWebP, extJPEG}

// oldSuffix marks a previous preview while Put replaces it.
const oldSuffix = ".old"

// tmpSuffix ends the temp files of large thumbnails being written into a
// fan-out directory of the large directory (see PutLarge).
const tmpSuffix = ".tmp"

// staleTmpAge is the age after which cleanOld removes a large thumbnail
// temp file: younger ones may belong to a server deriving one right now.
const staleTmpAge = time.Hour

// maxCleanEntries bounds the directory entries cleanOld looks at.
const maxCleanEntries = 1 << 20

// cleanOld deals with what interrupted writes (crash, power loss) leave
// behind. A <preview>.old file is left by a Put that was replacing a
// preview: when no preview of that hash exists (in either format) the .old
// file is the last good one and is renamed back (its thumbs.db row was
// never replaced), otherwise it is removed. In the large directory, temp
// files older than staleTmpAge are removed. Best effort: it only looks one
// fan-out level deep, stops after maxCleanEntries entries and ignores
// errors. Only one process may write thumbs.db at a time, so no Put of
// another process is in flight (a server deriving large thumbnails writes
// only temp files younger than staleTmpAge).
func (s *Store) cleanOld() {
	seen := 0
	walkFans(s.previews, &seen, func(dir string, e fs.DirEntry) {
		name := e.Name()
		if !strings.HasSuffix(name, oldSuffix) || !e.Type().IsRegular() {
			return
		}
		old := filepath.Join(dir, name)
		cur := strings.TrimSuffix(old, oldSuffix)
		stem := strings.TrimSuffix(cur, filepath.Ext(cur))
		for _, ext := range previewExts {
			if _, err := os.Lstat(stem + ext); err == nil {
				_ = os.Remove(old)
				return
			}
		}
		_ = os.Rename(old, cur)
	})
	walkFans(s.large, &seen, func(dir string, e fs.DirEntry) {
		if !strings.HasSuffix(e.Name(), tmpSuffix) || !e.Type().IsRegular() {
			return
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > staleTmpAge {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	})
}

// walkFans calls fn for each entry of each fan-out directory under root,
// counting entries in *seen and stopping after maxCleanEntries.
func walkFans(root string, seen *int, fn func(dir string, e fs.DirEntry)) {
	fans, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, fan := range fans {
		if !fan.IsDir() {
			continue
		}
		dir := filepath.Join(root, fan.Name())
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if *seen++; *seen > maxCleanEntries {
				return
			}
			fn(dir, e)
		}
	}
}

// newStore returns a Store for lib's cache paths, without opening it.
func newStore(lib *library.Library, readOnly bool) *Store {
	meta := filepath.Join(lib.Root(), library.MetaDir)
	return &Store{
		path:     filepath.Join(meta, DBName),
		previews: filepath.Join(meta, PreviewDir),
		large:    filepath.Join(meta, LargeDir),
		tmp:      lib.TempDir(),
		readOnly: readOnly,
	}
}

// OpenStore opens (creating when needed) <root>/.picmover/thumbs.db,
// <root>/.picmover/previews/ and <root>/.picmover/large/. A thumbs.db with
// a user_version other than FormatVersion is refused with an error
// wrapping ErrFormat.
func OpenStore(lib *library.Library) (*Store, error) {
	s := newStore(lib, false)
	if strings.Contains(s.path, "?") {
		// The driver splits the DSN at the first '?'.
		return nil, fmt.Errorf("thumbs: database path %q contains '?', which is not supported", s.path)
	}
	for _, d := range []string{s.previews, s.large, s.tmp} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("thumbs: %w", err)
		}
	}
	db, err := sql.Open("sqlite", s.path+dsnParams)
	if err != nil {
		return nil, fmt.Errorf("thumbs: open %s: %w", s.path, err)
	}
	s.db = db
	if err := s.ensureSchema(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	s.cleanOld()
	return s, nil
}

// OpenStoreReadOnly opens an existing <root>/.picmover/thumbs.db for
// reading only: it never creates a file or directory, never converts the
// journal mode and never takes a write lock (SQLite URI mode=ro plus
// PRAGMA query_only). A missing database, or one with a format version
// other than FormatVersion (wrapping ErrFormat), is an error. Put and
// Delete on the returned Store fail; PutLarge, which writes only the large
// directory, works (the server derives large thumbnails on demand).
func OpenStoreReadOnly(lib *library.Library) (*Store, error) {
	s := newStore(lib, true)
	fi, err := os.Stat(s.path)
	if err != nil {
		return nil, fmt.Errorf("thumbs: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("thumbs: %s is not a regular file", s.path)
	}
	db, err := sql.Open("sqlite", readOnlyDSN(s.path))
	if err != nil {
		return nil, fmt.Errorf("thumbs: open %s: %w", s.path, err)
	}
	s.db = db
	var v int
	if err := db.QueryRowContext(context.Background(), "PRAGMA user_version").Scan(&v); err != nil {
		db.Close()
		return nil, fmt.Errorf("thumbs: open %s: %w", s.path, err)
	}
	if v != FormatVersion {
		db.Close()
		return nil, fmt.Errorf("%w: %s has format version %d, want %d; delete it (and the %s directory) to regenerate the cache",
			ErrFormat, s.path, v, FormatVersion, PreviewDir)
	}
	return s, nil
}

// readOnlyDSN returns a SQLite URI that opens path read-only. Only names
// starting with "file:" get SQLite's own URI parameters (mode=ro); the
// driver still applies its underscore keys.
func readOnlyDSN(path string) string {
	p := filepath.ToSlash(path)
	if runtime.GOOS == "windows" && !strings.HasPrefix(p, "/") {
		p = "/" + p // file:///C:/dir/thumbs.db
	}
	u := url.URL{Scheme: "file", Path: p}
	return u.String() + "?mode=ro&_pragma=busy_timeout(10000)&_pragma=query_only(1)"
}

// ensureSchema creates the table in an empty database, inside an IMMEDIATE
// transaction so concurrent openers create it once, and checks the version.
func (s *Store) ensureSchema(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("thumbs: open %s: %w", s.path, err)
	}
	defer tx.Rollback()
	var v, tables int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return fmt.Errorf("thumbs: open %s: read user_version: %w", s.path, err)
	}
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table'").Scan(&tables); err != nil {
		return fmt.Errorf("thumbs: open %s: inspect schema: %w", s.path, err)
	}
	switch {
	case v == FormatVersion:
		return nil
	case v == 0 && tables == 0:
		if _, err := tx.ExecContext(ctx, schema); err != nil {
			return fmt.Errorf("thumbs: create schema: %w", err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", FormatVersion)); err != nil {
			return fmt.Errorf("thumbs: set user_version: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("thumbs: create schema: %w", err)
		}
		return nil
	}
	return fmt.Errorf("%w: %s has format version %d, want %d; delete it (and the %s directory) to regenerate the cache",
		ErrFormat, s.path, v, FormatVersion, PreviewDir)
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// key converts a content hash to SQLite's signed INTEGER.
func key(hash uint64) int64 { return int64(hash) }

// fanPath returns <dir>/<hh>/<hash-16hex><ext>, where hh is the first two
// hex digits of the hash.
func fanPath(dir string, hash uint64, ext string) string {
	return filepath.Join(dir, fmt.Sprintf("%02x", hash>>56), fmt.Sprintf("%016x%s", hash, ext))
}

// PreviewPath returns the path of the stored preview of hash: the first of
// <previews>/<hh>/<hash-16hex>.webp and .jpg that is a regular file (hh is
// the first two hex digits of the hash), or the .webp path when neither
// is. A <preview>.old file never counts.
func (s *Store) PreviewPath(hash uint64) string {
	for _, ext := range previewExts {
		p := fanPath(s.previews, hash, ext)
		if fi, err := os.Lstat(p); err == nil && fi.Mode().IsRegular() {
			return p
		}
	}
	return fanPath(s.previews, hash, extWebP)
}

// LargePath returns <large>/<hh>/<hash-16hex>.webp, the large thumbnail of
// hash.
func (s *Store) LargePath(hash uint64) string { return fanPath(s.large, hash, extWebP) }

// Thumb returns the stored thumbnail (WebP, or JPEG when written by an
// older version: sniff the bytes) and its size. ok is false when there is
// none.
func (s *Store) Thumb(ctx context.Context, hash uint64) (data []byte, w, h int, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, "SELECT data, width, height FROM thumbs WHERE hash = ?", key(hash)).Scan(&data, &w, &h)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, 0, false, nil
	}
	if err != nil {
		return nil, 0, 0, false, fmt.Errorf("thumbs: read thumb %016x: %w", hash, err)
	}
	return data, w, h, true, nil
}

// thumbSize returns the stored thumbnail size without reading its data.
func (s *Store) thumbSize(ctx context.Context, hash uint64) (w, h int, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, "SELECT width, height FROM thumbs WHERE hash = ?", key(hash)).Scan(&w, &h)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, fmt.Errorf("thumbs: read thumb %016x: %w", hash, err)
	}
	return w, h, true, nil
}

// Has reports whether the thumbnail row, the large thumbnail file and the
// preview file exist. A <preview>.old file (a replace in progress or
// interrupted) never counts.
func (s *Store) Has(ctx context.Context, hash uint64) (thumb, large, preview bool, err error) {
	var one int
	switch err := s.db.QueryRowContext(ctx, "SELECT 1 FROM thumbs WHERE hash = ?", key(hash)).Scan(&one); {
	case err == nil:
		thumb = true
	case !errors.Is(err, sql.ErrNoRows):
		return false, false, false, fmt.Errorf("thumbs: lookup %016x: %w", hash, err)
	}
	// Any stat failure (missing, a file in place of the fan-out directory,
	// an I/O error) counts as missing: Generate then rewrites it, and Put
	// reports a real problem.
	return thumb, nonEmptyFile(s.LargePath(hash)), nonEmptyFile(s.PreviewPath(hash)), nil
}

// nonEmptyFile reports whether p is a non-empty regular file.
func nonEmptyFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular() && st.Size() > 0
}

// previewExt returns the file extension for preview bytes: .webp or .jpg
// by their sniffed format.
func previewExt(data []byte) (string, error) {
	switch imaging.Sniff(bytes.NewReader(data)) {
	case imaging.FormatWebP:
		return extWebP, nil
	case imaging.FormatJPEG:
		return extJPEG, nil
	}
	return "", errors.New("preview is neither WebP nor JPEG")
}

// Put stores the renditions of hash, replacing any existing ones: the
// thumbnail row, the preview (named .webp or .jpg after its sniffed
// bytes) and the large thumbnail (nil: none; an existing one is removed,
// since it was derived from the previous preview, and is filled again
// later).
//
// The preview and the large thumbnail are first written to fsynced temp
// files in the library's temp directory. Existing previews (either
// format) are renamed to <preview>.old; the new preview is renamed into
// place, then the large thumbnail, and only then is the thumbnail row
// inserted (INSERT OR REPLACE), so a crash never leaves a row without its
// preview. When a step fails the new preview and the large thumbnail are
// removed and the .old files renamed back, so a failed Put never destroys
// the previous thumbnail and preview; on success the .old files are
// removed. A .old file left by a crash is restored or removed by the next
// OpenStore.
func (s *Store) Put(ctx context.Context, hash uint64, thumb []byte, tw, th int, large, preview []byte) error {
	if s.readOnly {
		return fmt.Errorf("thumbs: put %016x: store is read-only", hash)
	}
	if len(thumb) == 0 || len(preview) == 0 || tw <= 0 || th <= 0 {
		return fmt.Errorf("thumbs: put %016x: empty rendition or invalid size %dx%d", hash, tw, th)
	}
	ext, err := previewExt(preview)
	if err != nil {
		return fmt.Errorf("thumbs: put %016x: %w", hash, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	dst := fanPath(s.previews, hash, ext)
	tmp, err := s.writeTemp(filepath.Dir(dst), "preview-*.tmp", preview)
	if err != nil {
		return fmt.Errorf("thumbs: put %016x: %w", hash, err)
	}
	largeDst := s.LargePath(hash)
	var largeTmp string
	if len(large) > 0 {
		if largeTmp, err = s.writeTemp(filepath.Dir(largeDst), "large-*.tmp", large); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("thumbs: put %016x: %w", hash, err)
		}
	}
	dropTemps := func() {
		os.Remove(tmp)
		if largeTmp != "" {
			os.Remove(largeTmp)
		}
	}
	// Keep every existing preview: a WebP one replaces a JPEG one too.
	var olds []string
	restore := func() {
		for _, p := range olds {
			_ = renameRetry(os.Rename, p+oldSuffix, p) // replaces a new preview still at p
		}
	}
	replacedDst := false
	for _, e := range previewExts {
		p := fanPath(s.previews, hash, e)
		if fi, err := os.Lstat(p); err == nil && fi.Mode().IsRegular() {
			if err := renameRetry(os.Rename, p, p+oldSuffix); err != nil {
				dropTemps()
				restore()
				return fmt.Errorf("thumbs: put %016x: keep previous preview: %w", hash, err)
			}
			olds = append(olds, p)
			replacedDst = replacedDst || p == dst
		}
	}
	// undo puts the previous previews back (removing the new one when it
	// replaced none of the same name) and removes the large thumbnail: it
	// is derived data, filled again from whichever preview remains.
	undo := func() {
		if !replacedDst {
			os.Remove(dst)
		}
		restore()
		os.Remove(largeDst)
		syncDir(filepath.Dir(dst))
	}
	if err := renameRetry(os.Rename, tmp, dst); err != nil {
		dropTemps()
		restore()
		return fmt.Errorf("thumbs: put %016x: %w", hash, err)
	}
	if largeTmp != "" {
		err = renameRetry(os.Rename, largeTmp, largeDst)
	} else if err = renameRetry(removeFile, largeDst, ""); errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	if err != nil {
		if largeTmp != "" {
			os.Remove(largeTmp)
		}
		undo()
		return fmt.Errorf("thumbs: put %016x: large: %w", hash, err)
	}
	syncDir(filepath.Dir(dst))
	syncDir(filepath.Dir(largeDst))
	_, err = s.db.ExecContext(ctx,
		"INSERT OR REPLACE INTO thumbs (hash, width, height, created_at, data) VALUES (?, ?, ?, ?, ?)",
		key(hash), tw, th, time.Now().Unix(), thumb)
	if err != nil {
		// The row (if any) still describes the previous preview.
		undo()
		return fmt.Errorf("thumbs: put %016x: insert: %w", hash, err)
	}
	for _, p := range olds {
		_ = os.Remove(p + oldSuffix)
	}
	return nil
}

// PutLarge stores data as the large thumbnail of hash unless one exists;
// written reports whether it did. It writes a fsynced temp file into the
// fan-out directory and renames it into place without replacing
// (library.RenameNoReplace), so concurrent writers (a server deriving on
// demand, a thumbs job) never clobber each other and readers never see a
// partial file. A zero-length file in its place (left by external
// corruption; never by this code) is removed first, so it is replaced
// rather than served forever. It works on a read-only Store: the large
// directory is the only thing a server writes. A failed write is
// remembered (LargeUnwritable) until a write succeeds.
func (s *Store) PutLarge(hash uint64, data []byte) (written bool, err error) {
	if len(data) == 0 {
		return false, fmt.Errorf("thumbs: put large %016x: empty rendition", hash)
	}
	dst := s.LargePath(hash)
	if nonEmptyFile(dst) {
		return false, nil
	}
	removeEmptyFile(dst)
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.setLargeWritable(false)
		return false, fmt.Errorf("thumbs: put large %016x: %w", hash, err)
	}
	tmp, err := writeSynced(dir, fmt.Sprintf(".%016x-*%s", hash, tmpSuffix), data)
	if err != nil {
		s.setLargeWritable(false)
		return false, fmt.Errorf("thumbs: put large %016x: %w", hash, err)
	}
	if err := library.RenameNoReplace(tmp, dst); err != nil {
		os.Remove(tmp)
		if errors.Is(err, fs.ErrExist) {
			return false, nil
		}
		s.setLargeWritable(false)
		return false, fmt.Errorf("thumbs: put large %016x: %w", hash, err)
	}
	syncDir(dir)
	s.setLargeWritable(true)
	return true, nil
}

// removeEmptyFile removes p when it is a zero-length regular file.
func removeEmptyFile(p string) {
	if st, err := os.Lstat(p); err == nil && st.Mode().IsRegular() && st.Size() == 0 {
		_ = os.Remove(p)
	}
}

// writeTemp writes data to a fsynced temp file (named after pattern) in
// the library's temp directory and creates dir, the rendition's fan-out
// directory. It returns the temp file's path.
func (s *Store) writeTemp(dir, pattern string, data []byte) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return writeSynced(s.tmp, pattern, data)
}

// writeSynced writes data to a new fsynced temp file in dir and returns its
// path.
func writeSynced(dir, pattern string, data []byte) (string, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	return tmp, nil
}

// syncDir flushes directory metadata so a completed rename survives a
// crash. Errors are ignored: Windows cannot flush directory handles.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
}

// CopyFrom copies the renditions of hash from src, another library's
// store, into s with Put: the thumbnail, the preview and the large
// thumbnail when src has one. ok is false, and nothing is written, when
// src has no thumbnail or no preview of hash; the caller then leaves the
// row's thumbnails pending.
func (s *Store) CopyFrom(ctx context.Context, src *Store, hash uint64) (ok bool, err error) {
	thumb, tw, th, ok, err := src.Thumb(ctx, hash)
	if err != nil || !ok {
		return false, err
	}
	preview, err := os.ReadFile(src.PreviewPath(hash))
	if errors.Is(err, fs.ErrNotExist) || (err == nil && len(preview) == 0) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("thumbs: copy %016x: %w", hash, err)
	}
	large, err := os.ReadFile(src.LargePath(hash))
	if errors.Is(err, fs.ErrNotExist) {
		large, err = nil, nil
	}
	if err != nil {
		return false, fmt.Errorf("thumbs: copy %016x: %w", hash, err)
	}
	if err := s.Put(ctx, hash, thumb, tw, th, large, preview); err != nil {
		return false, err
	}
	return true, nil
}

// Delete removes every rendition of hash: the row first, then the
// previews (either format) and the large thumbnail, so a row never
// outlives its preview. Missing entries are not an error.
func (s *Store) Delete(ctx context.Context, hash uint64) error {
	if s.readOnly {
		return fmt.Errorf("thumbs: delete %016x: store is read-only", hash)
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM thumbs WHERE hash = ?", key(hash)); err != nil {
		return fmt.Errorf("thumbs: delete %016x: %w", hash, err)
	}
	paths := []string{s.LargePath(hash)}
	for _, ext := range previewExts {
		paths = append(paths, fanPath(s.previews, hash, ext))
	}
	for _, p := range paths {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("thumbs: delete %016x: %w", hash, err)
		}
	}
	return nil
}

// Stats returns the number of thumbnails and the bytes used: the sum of the
// thumbnail blobs plus the sizes of all regular files under the previews
// and large directories (except .old and temp files of interrupted
// writes), found by a directory walk (one stat per file; well under a
// second for 100k previews on a local disk). The walk honours ctx.
func (s *Store) Stats(ctx context.Context) (count int64, size int64, err error) {
	err = s.db.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(SUM(LENGTH(data)), 0) FROM thumbs").Scan(&count, &size)
	if err != nil {
		return 0, 0, fmt.Errorf("thumbs: stats: %w", err)
	}
	for _, root := range []string{s.previews, s.large} {
		err = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			name := d.Name()
			if d.Type().IsRegular() && !strings.HasSuffix(name, oldSuffix) && !strings.HasSuffix(name, tmpSuffix) {
				if info, err := d.Info(); err == nil {
					size += info.Size()
				}
			}
			return nil
		})
		if err != nil {
			return 0, 0, fmt.Errorf("thumbs: stats: %w", err)
		}
	}
	return count, size, nil
}

// Rename retries: on Windows a preview that is being served is open
// without FILE_SHARE_DELETE (Go's os.Open), so renaming it fails with a
// sharing violation until the response is done. Previews are small, so a
// few short retries usually outlast the request.
const (
	renameAttempts = 5
	renameBackoff  = 50 * time.Millisecond
)

// removeFile removes p; its signature fits renameRetry (to is unused).
func removeFile(p, _ string) error { return os.Remove(p) }

// renameRetry calls rename(from, to) up to renameAttempts times, sleeping
// renameBackoff between attempts, and returns the last error. A missing
// source is not retried.
func renameRetry(rename func(from, to string) error, from, to string) error {
	var err error
	for i := 0; i < renameAttempts; i++ {
		if i > 0 {
			time.Sleep(renameBackoff)
		}
		if err = rename(from, to); err == nil || errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return err
}
