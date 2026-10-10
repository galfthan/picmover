// Package thumbs generates thumbnails (256 px, stored in thumbs.db) and
// previews (1600 px, stored as JPEG files) for media files, keyed by the
// file's content hash, and computes a perceptual hash of each preview.
//
// The cache lives in <library>/.picmover: thumbs.db holds the thumbnails
// and previews/<hh>/<hash>.jpg the previews. It is derived data: deleting
// it only means the thumbnails are generated again.
package thumbs

import (
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

	"picmover/internal/library"
)

// Sizes (longer side, pixels) and JPEG qualities of the two renditions.
const (
	ThumbSize      = 256  // stored in thumbs.db
	PreviewSize    = 1600 // stored on disk
	ThumbQuality   = 80
	PreviewQuality = 85
)

const (
	// DBName is the cache database file inside library.MetaDir.
	DBName = "thumbs.db"
	// PreviewDir is the preview directory inside library.MetaDir.
	PreviewDir = "previews"
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

// Store is the thumbnail and preview cache of one library. It is safe for
// concurrent use.
type Store struct {
	db       *sql.DB
	path     string
	previews string
	tmp      string
	readOnly bool
}

// oldSuffix marks the previous preview while Put replaces it.
const oldSuffix = ".old"

// maxCleanEntries bounds the directory entries cleanOld looks at.
const maxCleanEntries = 1 << 20

// cleanOld deals with <preview>.old files left by a Put that was
// interrupted (crash, power loss) while replacing a preview: when the
// preview itself is missing the .old file is the last good one and is
// renamed back (its thumbs.db row was never replaced), otherwise it is
// removed. Best effort: it only looks one fan-out level deep, stops after
// maxCleanEntries entries and ignores errors. Only one process may write
// a library at a time, so no Put of another process is in flight.
func (s *Store) cleanOld() {
	fans, err := os.ReadDir(s.previews)
	if err != nil {
		return
	}
	seen := 0
	for _, fan := range fans {
		if !fan.IsDir() {
			continue
		}
		dir := filepath.Join(s.previews, fan.Name())
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if seen++; seen > maxCleanEntries {
				return
			}
			name := e.Name()
			if !strings.HasSuffix(name, oldSuffix) || !e.Type().IsRegular() {
				continue
			}
			old := filepath.Join(dir, name)
			cur := strings.TrimSuffix(old, oldSuffix)
			if _, err := os.Lstat(cur); errors.Is(err, fs.ErrNotExist) {
				_ = os.Rename(old, cur)
			} else {
				_ = os.Remove(old)
			}
		}
	}
}

// OpenStore opens (creating when needed) <root>/.picmover/thumbs.db and
// <root>/.picmover/previews/. A thumbs.db with a user_version other than
// FormatVersion is refused with an error wrapping ErrFormat.
func OpenStore(lib *library.Library) (*Store, error) {
	meta := filepath.Join(lib.Root(), library.MetaDir)
	s := &Store{
		path:     filepath.Join(meta, DBName),
		previews: filepath.Join(meta, PreviewDir),
		tmp:      lib.TempDir(),
	}
	if strings.Contains(s.path, "?") {
		// The driver splits the DSN at the first '?'.
		return nil, fmt.Errorf("thumbs: database path %q contains '?', which is not supported", s.path)
	}
	for _, d := range []string{s.previews, s.tmp} {
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
// Delete on the returned Store fail.
func OpenStoreReadOnly(lib *library.Library) (*Store, error) {
	meta := filepath.Join(lib.Root(), library.MetaDir)
	s := &Store{
		path:     filepath.Join(meta, DBName),
		previews: filepath.Join(meta, PreviewDir),
		tmp:      lib.TempDir(),
		readOnly: true,
	}
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

// PreviewPath returns <previews>/<hh>/<hash-16hex>.jpg, where hh is the
// first two hex digits of the hash.
func (s *Store) PreviewPath(hash uint64) string {
	return filepath.Join(s.previews, fmt.Sprintf("%02x", hash>>56), fmt.Sprintf("%016x.jpg", hash))
}

// Thumb returns the stored thumbnail JPEG and its size. ok is false when
// there is none.
func (s *Store) Thumb(ctx context.Context, hash uint64) (jpeg []byte, w, h int, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, "SELECT data, width, height FROM thumbs WHERE hash = ?", key(hash)).Scan(&jpeg, &w, &h)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, 0, false, nil
	}
	if err != nil {
		return nil, 0, 0, false, fmt.Errorf("thumbs: read thumb %016x: %w", hash, err)
	}
	return jpeg, w, h, true, nil
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

// Has reports whether the thumbnail row and the preview file exist. A
// <preview>.old file (a replace in progress or interrupted) never counts.
func (s *Store) Has(ctx context.Context, hash uint64) (thumb, preview bool, err error) {
	var one int
	switch err := s.db.QueryRowContext(ctx, "SELECT 1 FROM thumbs WHERE hash = ?", key(hash)).Scan(&one); {
	case err == nil:
		thumb = true
	case !errors.Is(err, sql.ErrNoRows):
		return false, false, fmt.Errorf("thumbs: lookup %016x: %w", hash, err)
	}
	// Any stat failure (missing, a file in place of the fan-out directory,
	// an I/O error) counts as no preview: Generate then rewrites it, and
	// Put reports a real problem.
	if st, err := os.Stat(s.PreviewPath(hash)); err == nil {
		preview = st.Mode().IsRegular() && st.Size() > 0
	}
	return thumb, preview, nil
}

// Put stores both renditions of hash, replacing any existing ones. The
// preview is written to a temp file in the library's temp directory and
// fsynced. An existing preview is first renamed to <preview>.old; the new
// one is then renamed into place and only then is the thumbnail row
// inserted (INSERT OR REPLACE), so a crash never leaves a row without its
// preview. When a step fails the new preview is removed and the .old file
// is renamed back, so a failed Put never destroys the previous renditions;
// on success the .old file is removed. A .old file left by a crash is
// restored or removed by the next OpenStore.
func (s *Store) Put(ctx context.Context, hash uint64, thumb []byte, tw, th int, preview []byte) error {
	if s.readOnly {
		return fmt.Errorf("thumbs: put %016x: store is read-only", hash)
	}
	if len(thumb) == 0 || len(preview) == 0 || tw <= 0 || th <= 0 {
		return fmt.Errorf("thumbs: put %016x: empty rendition or invalid size %dx%d", hash, tw, th)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	dst := s.PreviewPath(hash)
	old := dst + oldSuffix
	tmp, err := s.writeTemp(filepath.Dir(dst), preview)
	if err != nil {
		return fmt.Errorf("thumbs: put %016x: %w", hash, err)
	}
	hadOld := false
	if fi, err := os.Lstat(dst); err == nil && fi.Mode().IsRegular() {
		if err := renameRetry(os.Rename, dst, old); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("thumbs: put %016x: keep previous preview: %w", hash, err)
		}
		hadOld = true
	}
	restore := func() {
		if hadOld {
			_ = renameRetry(os.Rename, old, dst) // replaces a new preview still at dst
		}
	}
	if err := renameRetry(os.Rename, tmp, dst); err != nil {
		os.Remove(tmp)
		restore()
		return fmt.Errorf("thumbs: put %016x: %w", hash, err)
	}
	syncDir(filepath.Dir(dst))
	_, err = s.db.ExecContext(ctx,
		"INSERT OR REPLACE INTO thumbs (hash, width, height, created_at, data) VALUES (?, ?, ?, ?, ?)",
		key(hash), tw, th, time.Now().Unix(), thumb)
	if err != nil {
		// The row (if any) still describes the previous preview: put that
		// back, or remove the new one when there was none.
		if hadOld {
			restore()
		} else {
			os.Remove(dst)
		}
		syncDir(filepath.Dir(dst))
		return fmt.Errorf("thumbs: put %016x: insert: %w", hash, err)
	}
	if hadOld {
		_ = os.Remove(old)
	}
	return nil
}

// writeTemp writes data to a fsynced temp file in the library's temp
// directory and creates dir, the preview's fan-out directory. It returns
// the temp file's path.
func (s *Store) writeTemp(dir string, data []byte) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(s.tmp, "preview-*.tmp")
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

// Delete removes both renditions of hash: the row first, then the preview,
// so a row never outlives its preview. Missing entries are not an error.
func (s *Store) Delete(ctx context.Context, hash uint64) error {
	if s.readOnly {
		return fmt.Errorf("thumbs: delete %016x: store is read-only", hash)
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM thumbs WHERE hash = ?", key(hash)); err != nil {
		return fmt.Errorf("thumbs: delete %016x: %w", hash, err)
	}
	if err := os.Remove(s.PreviewPath(hash)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("thumbs: delete %016x: %w", hash, err)
	}
	return nil
}

// Stats returns the number of thumbnails and the bytes used: the sum of the
// thumbnail blobs plus the sizes of all regular files under the previews
// directory (except .old files of an interrupted replace), found by a
// directory walk (one stat per preview; well under a second for 100k
// previews on a local disk). The walk honours ctx.
func (s *Store) Stats(ctx context.Context) (count int64, bytes int64, err error) {
	err = s.db.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(SUM(LENGTH(data)), 0) FROM thumbs").Scan(&count, &bytes)
	if err != nil {
		return 0, 0, fmt.Errorf("thumbs: stats: %w", err)
	}
	err = filepath.WalkDir(s.previews, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.Type().IsRegular() && !strings.HasSuffix(d.Name(), oldSuffix) {
			if info, err := d.Info(); err == nil {
				bytes += info.Size()
			}
		}
		return nil
	})
	if err != nil {
		return 0, 0, fmt.Errorf("thumbs: stats: %w", err)
	}
	return count, bytes, nil
}

// Rename retries: on Windows a preview that is being served is open
// without FILE_SHARE_DELETE (Go's os.Open), so renaming it fails with a
// sharing violation until the response is done. Previews are small, so a
// few short retries usually outlast the request.
const (
	renameAttempts = 5
	renameBackoff  = 50 * time.Millisecond
)

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
