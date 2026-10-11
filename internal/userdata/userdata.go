// Package userdata is the user's own data about a library's media,
// <library>/user.db: ratings, tags, albums, the trash timestamps and the
// near-duplicate pairs the user chose to ignore.
//
// Unlike the catalog (media.db), which is derived from the files and may be
// rebuilt at any time, user.db cannot be regenerated: it is never touched by
// "picmover rebuild", and its schema will get real migrations. Every row is
// keyed by the catalog's content hash (stored as int64, the same conversion
// as media.hash), so ratings, tags and album memberships survive a rebuild
// and come back when the same content is imported again.
//
// The package exposes typed functions only; no SQL leaks to callers. Every
// write is a single transaction.
package userdata

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode"

	"picmover/internal/library"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// SchemaVersion is the user.db schema version this package reads and
// writes (PRAGMA user_version). Versions 1 (step 4) and 2 (step 5) are
// migrated on open, in order.
const SchemaVersion = 3

// FileName is the name of the user data database at the library root.
const FileName = "user.db"

const dsnParams = "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=foreign_keys(1)&_txlock=immediate"

const schemaV1 = `
CREATE TABLE ratings (
    hash       INTEGER PRIMARY KEY,
    rating     INTEGER NOT NULL CHECK (rating BETWEEN 1 AND 5),
    updated_at INTEGER NOT NULL
);
CREATE TABLE tags (
    id         INTEGER PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE COLLATE NOCASE,
    created_at INTEGER NOT NULL
);
CREATE TABLE media_tags (
    hash     INTEGER NOT NULL,
    tag_id   INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    added_at INTEGER NOT NULL,
    PRIMARY KEY (hash, tag_id)
);
CREATE INDEX media_tags_tag ON media_tags(tag_id);
CREATE TABLE albums (
    id         INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    cover_hash INTEGER
);
CREATE TABLE album_items (
    album_id INTEGER NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
    hash     INTEGER NOT NULL,
    position INTEGER NOT NULL,
    added_at INTEGER NOT NULL,
    PRIMARY KEY (album_id, hash)
);
CREATE INDEX album_items_hash ON album_items(hash);
CREATE TABLE trash (
    hash       INTEGER PRIMARY KEY,
    deleted_at INTEGER NOT NULL
);
CREATE TABLE similar_ignored (
    hash_a   INTEGER NOT NULL,
    hash_b   INTEGER NOT NULL,
    added_at INTEGER NOT NULL,
    PRIMARY KEY (hash_a, hash_b)
);
`

// schemaV2 is what version 2 adds to version 1: the backup targets and
// the library's identity (meta library_id, created_at).
const schemaV2 = `
CREATE TABLE backup_targets (
    id             TEXT PRIMARY KEY,
    label          TEXT NOT NULL,
    last_path      TEXT NOT NULL,
    last_host      TEXT NOT NULL,
    added_at       INTEGER NOT NULL,
    last_backup_at INTEGER,
    last_scrub_at  INTEGER,
    set_name       TEXT NOT NULL DEFAULT ''
);
CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
`

// schemaV3 is what version 3 adds to version 2: the import history.
// Ids are never reused (AUTOINCREMENT): a link to a deleted record must
// not open a later one. items and cover are the stored summary of the
// record's items (written when it is finished; the list never reads
// import_items): items is NULL for the modes that keep no items (rebuild,
// record), cover NULL when there is none.
const schemaV3 = `
CREATE TABLE imports (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT NOT NULL DEFAULT '',
    started_at      INTEGER NOT NULL,
    finished_at     INTEGER,
    source          TEXT NOT NULL,
    source_name     TEXT NOT NULL,
    mode            TEXT NOT NULL,
    host            TEXT NOT NULL,
    outcome         TEXT NOT NULL,
    imported        INTEGER NOT NULL DEFAULT 0,
    existing        INTEGER NOT NULL DEFAULT 0,
    duplicates      INTEGER NOT NULL DEFAULT 0,
    skipped         INTEGER NOT NULL DEFAULT 0,
    errors          INTEGER NOT NULL DEFAULT 0,
    bytes           INTEGER NOT NULL DEFAULT 0,
    catalog_session INTEGER,
    items           INTEGER,
    cover           INTEGER
);
CREATE TABLE import_items (
    import_id INTEGER NOT NULL REFERENCES imports(id) ON DELETE CASCADE,
    hash      INTEGER NOT NULL,
    status    TEXT NOT NULL,
    PRIMARY KEY (import_id, hash)
);
CREATE INDEX import_items_hash ON import_items(hash);
`

// migrations[v] upgrades a version-v database to version v+1 inside the
// migration transaction.
var migrations = map[int]func(ctx context.Context, d *DB, tx *sql.Tx) error{
	1: func(ctx context.Context, d *DB, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, schemaV2); err != nil {
			return err
		}
		return d.writeIdentity(ctx, tx)
	},
	2: func(ctx context.Context, _ *DB, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, schemaV3)
		return err
	},
}

// migratable reports whether Open upgrades a database of version v.
func migratable(v int) bool { return v >= 1 && v < SchemaVersion }

// Errors returned (wrapped) by the API.
var (
	// ErrNotFound: no tag or album has the given id.
	ErrNotFound = errors.New("not found")
	// ErrNameTaken: another tag has this name (case-insensitively).
	ErrNameTaken = errors.New("name is already taken")
	// ErrInvalidName: a tag or album name fails the length or character rules.
	ErrInvalidName = errors.New("invalid name")
	// ErrInvalidRating: a rating outside 0-5.
	ErrInvalidRating = errors.New("rating must be 0-5")
	// ErrUnsupportedVersion: user.db has a schema version this picmover
	// does not support (written by a newer one).
	ErrUnsupportedVersion = errors.New("unsupported user.db schema version")
	// ErrNotUserDB: the file has tables but no schema version.
	ErrNotUserDB = errors.New("not a picmover user database")
)

// VersionError is returned (wrapped) for a user.db whose schema version
// this picmover does not support; it matches ErrUnsupportedVersion. Its
// text names no path, so it can be shown to clients.
type VersionError struct {
	Version int
}

func (e *VersionError) Error() string {
	return fmt.Sprintf("user.db has schema version %d; this picmover supports %d: upgrade picmover", e.Version, SchemaVersion)
}

func (e *VersionError) Is(target error) bool { return target == ErrUnsupportedVersion }

// CheckFile checks the user.db at path without creating or changing it:
// nil when it is missing, empty, or has schema version SchemaVersion or an
// older one (which Open migrates); an
// error wrapping a *VersionError for another version and ErrNotUserDB for
// a file with tables but no version. It reads PRAGMA user_version through
// a read-only (immutable) open, so it works in read-only folders too and
// creates no side files.
func CheckFile(ctx context.Context, path string) error {
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && fi.Size() == 0) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("userdata: %w", err)
	}
	sqldb, err := openReadOnly(path)
	if err != nil {
		return err
	}
	defer sqldb.Close()
	var v, tables int
	if err := sqldb.QueryRowContext(ctx, `SELECT (SELECT user_version FROM pragma_user_version),
		(SELECT COUNT(*) FROM sqlite_master WHERE type = 'table')`).Scan(&v, &tables); err != nil {
		return fmt.Errorf("userdata: open %s: %w", path, err)
	}
	switch {
	case v == SchemaVersion, migratable(v), v == 0 && tables == 0:
		return nil
	case v == 0:
		return fmt.Errorf("userdata: %s: %w", path, ErrNotUserDB)
	}
	return fmt.Errorf("userdata: %s: %w", path, &VersionError{Version: v})
}

// openReadOnly opens the database at path read-only and immutable: no
// side files are created and nothing is written.
func openReadOnly(path string) (*sql.DB, error) {
	if strings.Contains(path, "?") {
		return nil, fmt.Errorf("userdata: database path %q contains '?', which is not supported", path)
	}
	p := filepath.ToSlash(path)
	if runtime.GOOS == "windows" && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p}
	sqldb, err := sql.Open("sqlite", u.String()+"?mode=ro&immutable=1")
	if err != nil {
		return nil, fmt.Errorf("userdata: open %s: %w", path, err)
	}
	return sqldb, nil
}

// PeekBackupSummary reads the backup summary of the user.db at path
// read-only (for a library that is not open). ok is false when the file
// is missing or empty or has no backup tables yet (version 1) or a
// version this picmover does not read.
func PeekBackupSummary(ctx context.Context, path string) (s BackupSummary, ok bool, err error) {
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && fi.Size() == 0) {
		return BackupSummary{}, false, nil
	}
	if err != nil {
		return BackupSummary{}, false, fmt.Errorf("userdata: %w", err)
	}
	sqldb, err := openReadOnly(path)
	if err != nil {
		return BackupSummary{}, false, err
	}
	defer sqldb.Close()
	var v int
	if err := sqldb.QueryRowContext(ctx, `SELECT user_version FROM pragma_user_version`).Scan(&v); err != nil {
		return BackupSummary{}, false, fmt.Errorf("userdata: open %s: %w", path, err)
	}
	if v < 2 || v > SchemaVersion {
		return BackupSummary{}, false, nil
	}
	s, err = backupSummary(ctx, sqldb)
	return s, err == nil, err
}

// Name limits, in runes after trimming.
const (
	MaxTagName   = 64
	MaxAlbumName = 128
)

// DB is an open user.db. It is safe for concurrent use.
type DB struct {
	db   *sql.DB
	path string
	now  func() time.Time // tests override it

	migratedFrom    int    // the version Open migrated from; 0: none
	migrationBackup string // the copy made before the migration
}

// Open opens the user data database at path, creating it with schema
// SchemaVersion when it is missing or empty. A database with another
// user_version (other than 0 for a fresh file), or a version-0 file that
// already has tables, is refused with an error naming the file: user.db
// is never silently ignored or overwritten.
func Open(ctx context.Context, path string) (*DB, error) {
	if path == "" {
		return nil, errors.New("userdata: empty database path")
	}
	if strings.Contains(path, "?") {
		// The driver splits the DSN at the first '?'.
		return nil, fmt.Errorf("userdata: database path %q contains '?', which is not supported", path)
	}
	sqldb, err := sql.Open("sqlite", path+dsnParams)
	if err != nil {
		return nil, fmt.Errorf("userdata: open %s: %w", path, err)
	}
	d := &DB{db: sqldb, path: path, now: time.Now}
	if err := d.ensureSchema(ctx); err != nil {
		sqldb.Close()
		return nil, err
	}
	return d, nil
}

func (d *DB) ensureSchema(ctx context.Context) error {
	v, tables, err := d.version(ctx)
	if err != nil {
		return fmt.Errorf("userdata: open %s: %w", d.path, err)
	}
	switch {
	case v == SchemaVersion:
		return nil
	case migratable(v):
		return d.migrate(ctx)
	case v == 0 && tables == 0:
		return d.create(ctx)
	case v == 0:
		return fmt.Errorf("userdata: %s is not a picmover user database (it has tables but no schema version): %w", d.path, ErrNotUserDB)
	default:
		return fmt.Errorf("userdata: %s: %w (the file is left untouched)", d.path, &VersionError{Version: v})
	}
}

// Version returns the database's schema version: SchemaVersion for a
// database opened by Open; an older one is possible for OpenReadOnly
// (version 2 has no import history).
func (d *DB) Version(ctx context.Context) (int, error) {
	v, _, err := d.version(ctx)
	if err != nil {
		return 0, fmt.Errorf("userdata: version: %w", err)
	}
	return v, nil
}

// version returns user_version and the number of tables.
func (d *DB) version(ctx context.Context) (v, tables int, err error) {
	err = d.db.QueryRowContext(ctx, `SELECT (SELECT user_version FROM pragma_user_version),
		(SELECT COUNT(*) FROM sqlite_master WHERE type = 'table')`).Scan(&v, &tables)
	return v, tables, err
}

// create writes the current schema into an empty database, with a new
// library identity.
func (d *DB) create(ctx context.Context) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("userdata: open %s: %w", d.path, err)
	}
	defer tx.Rollback()
	// Checked again inside the write transaction: another process may
	// have created the schema in between.
	var v, tables int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT user_version FROM pragma_user_version),
		(SELECT COUNT(*) FROM sqlite_master WHERE type = 'table')`).Scan(&v, &tables); err != nil {
		return fmt.Errorf("userdata: open %s: %w", d.path, err)
	}
	if v != 0 || tables != 0 {
		tx.Rollback()
		return d.ensureSchema(ctx)
	}
	if _, err := tx.ExecContext(ctx, schemaV1+schemaV2+schemaV3); err != nil {
		return fmt.Errorf("userdata: create schema: %w", err)
	}
	if err := d.writeIdentity(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion)); err != nil {
		return fmt.Errorf("userdata: set user_version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("userdata: create schema: %w", err)
	}
	return nil
}

// migrate upgrades the database to SchemaVersion: a consistent copy of
// it is written to user.db.bak-<YYYYMMDD-HHMMSS> next to it (never
// replacing a file), then one transaction re-reads the version, runs the
// steps from it to SchemaVersion in order (1 to 2: backup_targets and
// meta with a new library_id and created_at; 2 to 3: the import history)
// and sets user_version last. One copy covers every step. A failure
// leaves the old version in place.
func (d *DB) migrate(ctx context.Context) error {
	bak, err := d.backupCopy(ctx)
	if err != nil {
		return fmt.Errorf("userdata: migrate %s to version %d: back up: %w (the file is left untouched)", d.path, SchemaVersion, err)
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("userdata: migrate %s: %w", d.path, err)
	}
	defer tx.Rollback()
	var v int
	if err := tx.QueryRowContext(ctx, `SELECT user_version FROM pragma_user_version`).Scan(&v); err != nil {
		return fmt.Errorf("userdata: migrate %s: %w", d.path, err)
	}
	if !migratable(v) {
		// Another process migrated it in between; its copy is enough.
		tx.Rollback()
		os.Remove(bak)
		return d.ensureSchema(ctx)
	}
	from := v
	for ; v < SchemaVersion; v++ {
		if err := migrations[v](ctx, d, tx); err != nil {
			return fmt.Errorf("userdata: migrate %s from version %d: %w", d.path, v, err)
		}
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion)); err != nil {
		return fmt.Errorf("userdata: migrate %s: %w", d.path, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("userdata: migrate %s: %w", d.path, err)
	}
	d.migratedFrom, d.migrationBackup = from, bak
	return nil
}

// backupCopy writes a consistent copy of the database (VACUUM INTO) to
// <path>.bak-<stamp> and returns its path. The copy is written to a
// temporary name unique to this call (VACUUM INTO refuses an existing
// file, so two processes migrating at once must not share one) and then
// renamed without replacing: when <path>.bak-<stamp> is taken (another
// copy in the same second), -2, -3, ... are tried.
func (d *DB) backupCopy(ctx context.Context) (string, error) {
	base := d.path + ".bak-" + d.now().Format(backupStampLayout)
	var rnd [6]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", err
	}
	tmp := fmt.Sprintf("%s.%d-%x.tmp", base, os.Getpid(), rnd)
	if err := d.Snapshot(ctx, tmp); err != nil {
		os.Remove(tmp) // the name is ours alone
		return "", err
	}
	for i := 1; i <= 100; i++ {
		p := base
		if i > 1 {
			p = fmt.Sprintf("%s-%d", base, i)
		}
		err := library.RenameNoReplace(tmp, p)
		if err == nil {
			return p, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			os.Remove(tmp)
			return "", err
		}
	}
	os.Remove(tmp)
	return "", errors.New("no free backup name")
}

// backupStampLayout is library.BackupStampLayout.
const backupStampLayout = library.BackupStampLayout

// writeIdentity stores a new library_id (16 random hex digits) and
// created_at in meta, unless they exist.
func (d *DB) writeIdentity(ctx context.Context, tx *sql.Tx) error {
	id, err := newLibraryID()
	if err != nil {
		return fmt.Errorf("userdata: library id: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO meta (key, value) VALUES ('library_id', ?), ('created_at', ?)`,
		id, fmt.Sprint(d.now().Unix())); err != nil {
		return fmt.Errorf("userdata: write library id: %w", err)
	}
	return nil
}

// Migrated reports the schema version the database had before Open
// migrated it (0 when it did not) and the path of the copy made first.
func (d *DB) Migrated() (from int, backup string) { return d.migratedFrom, d.migrationBackup }

// Snapshot writes a consistent copy of the database to path (VACUUM
// INTO), which must not exist.
func (d *DB) Snapshot(ctx context.Context, path string) error {
	if _, err := d.db.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		return fmt.Errorf("userdata: snapshot: %w", err)
	}
	return nil
}

// Close closes the database. It may be called more than once.
func (d *DB) Close() error { return d.db.Close() }

// Path returns the database file path.
func (d *DB) Path() string { return d.path }

// hashArray encodes hashes as a JSON array of int64 for json_each.
func hashArray(hashes []uint64) string {
	ints := make([]int64, len(hashes))
	for i, h := range hashes {
		ints[i] = int64(h)
	}
	b, _ := json.Marshal(ints)
	return string(b)
}

// write runs fn in one IMMEDIATE transaction. Errors wrapping the
// package's sentinel errors are returned as they are.
func (d *DB) write(ctx context.Context, what string, fn func(tx *sql.Tx, now int64) error) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("userdata: %s: %w", what, err)
	}
	defer tx.Rollback()
	if err := fn(tx, d.now().Unix()); err != nil {
		for _, s := range []error{ErrNotFound, ErrNameTaken, ErrInvalidName, ErrInvalidRating} {
			if errors.Is(err, s) {
				return err
			}
		}
		return fmt.Errorf("userdata: %s: %w", what, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("userdata: %s: %w", what, err)
	}
	return nil
}

// queryHashes runs q and returns the first column as hashes (never nil).
func (d *DB) queryHashes(ctx context.Context, what, q string, args ...any) ([]uint64, error) {
	rows, err := d.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("userdata: %s: %w", what, err)
	}
	defer rows.Close()
	out := []uint64{}
	for rows.Next() {
		var h int64
		if err := rows.Scan(&h); err != nil {
			return nil, fmt.Errorf("userdata: %s: %w", what, err)
		}
		out = append(out, uint64(h))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("userdata: %s: %w", what, err)
	}
	return out, nil
}

// CleanName trims name and checks it: 1 to limit runes, no control or
// line/paragraph separator characters. It returns the trimmed name or an
// error wrapping ErrInvalidName.
func CleanName(name string, limit int) (string, error) {
	name = strings.TrimSpace(name)
	n := 0
	for _, r := range name {
		if unicode.IsControl(r) || r == ' ' || r == ' ' || r == '�' {
			return "", fmt.Errorf("%w: control characters are not allowed", ErrInvalidName)
		}
		n++
	}
	if n == 0 || n > limit {
		return "", fmt.Errorf("%w: want 1-%d characters", ErrInvalidName, limit)
	}
	return name, nil
}

// Counts are the row counts reported by the library endpoint.
type Counts struct {
	Ratings, Tags, Albums, Trashed, IgnoredPairs int64
}

// Counts returns the number of rated items, tags, albums, trash rows and
// ignored pairs.
func (d *DB) Counts(ctx context.Context) (Counts, error) {
	var c Counts
	err := d.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM ratings), (SELECT COUNT(*) FROM tags),
		(SELECT COUNT(*) FROM albums), (SELECT COUNT(*) FROM trash), (SELECT COUNT(*) FROM similar_ignored)`).
		Scan(&c.Ratings, &c.Tags, &c.Albums, &c.Trashed, &c.IgnoredPairs)
	if err != nil {
		return Counts{}, fmt.Errorf("userdata: counts: %w", err)
	}
	return c, nil
}
